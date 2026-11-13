package data

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"chatroom/app/conversation/service/internal/biz"
	"chatroom/app/conversation/service/internal/data/ent"
	"chatroom/app/conversation/service/internal/data/ent/conversation"
	"chatroom/app/conversation/service/internal/data/ent/conversationmember"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// defaultConvListLimit 会话列表默认单页大小。repo 层兜底，biz 层也会夹一次。
const defaultConvListLimit = 50

// conversationRepo biz.ConversationRepo 的 ent 实现。
type conversationRepo struct {
	data  *Data
	cache *ConversationCache // 成员列表缓存（Redis Set）。nil 时降级为每次查库
}

// NewConversationRepo 构造 biz.ConversationRepo 的 ent 实现。
// d 是 data 客户端。
func NewConversationRepo(d *Data) biz.ConversationRepo {
	if d == nil {
		return nil
	}
	return &conversationRepo{data: d, cache: NewConversationCache(d)}
}

// Save 新建会话 + 成员。c 是待落库的会话。事务内完成，保证 uk_conv_user 不冲突。
// 单聊复用：事务外先查一次已有单聊，命中直接返回（reused=true）；并发由 pair_key 唯一索引仲裁。
func (r *conversationRepo) Save(ctx context.Context, c *biz.Conversation) (*biz.Conversation, bool, error) {
	if c == nil {
		return nil, false, biz.ErrConversationInvalidArgument
	}

	// 成员列表升序 —— DO 的约定（MemberIDs / MemberNicknames / MemberAvatars 按下标对齐，
	// 顺序必须确定）。repo 是公开接口，不能假设调用方都走过 biz。
	memberIDs := append([]string(nil), c.MemberIDs...)
	sort.Strings(memberIDs)

	// 单聊幂等的快路径。
	if c.Type == biz.ConversationTypeSingle && len(memberIDs) == 2 {
		existing, err := r.FindSingleByMembers(ctx, memberIDs[0], memberIDs[1])
		if err == nil && existing != nil {
			return existing, true, nil
		}
		if err != nil && !errors.Is(err, biz.ErrConversationNotFound) {
			return nil, false, err
		}
	}

	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return nil, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	create := tx.Conversation.Create().
		SetType(uint8(c.Type)).
		SetName(c.Name).
		SetOwnerID(c.OwnerID)
	// 单聊写 pair_key（成员对唯一键），群聊留 NULL —— NULL 不受唯一索引约束。
	var pairKey *string
	if c.Type == biz.ConversationTypeSingle && len(memberIDs) == 2 {
		key := singlePairKey(memberIDs[0], memberIDs[1])
		pairKey = &key
		create.SetPairKey(key)
	}

	convPO, err := create.Save(ctx)
	if err != nil {
		// 并发建同一个单聊：唯一索引挡住了后到的那一方。回查已存在的会话复用。
		if pairKey != nil && isUniqueConstraintErr(err) {
			_ = tx.Rollback()
			existing, findErr := r.FindSingleByMembers(ctx, memberIDs[0], memberIDs[1])
			if findErr == nil && existing != nil {
				return existing, true, nil
			}
			return nil, false, err
		}
		return nil, false, err
	}

	for _, uid := range memberIDs {
		if _, err := tx.ConversationMember.Create().
			SetConversationID(convPO.ID).
			SetUserID(uid).
			Save(ctx); err != nil {
			return nil, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	committed = true

	// 成员列表变了，作废该会话的成员缓存。
	r.cache.InvalidateMembers(ctx, convPO.ID)
	return &biz.Conversation{
		ID:          convPO.ID,
		Type:        biz.ConversationType(convPO.Type),
		Name:        convPO.Name,
		OwnerID:     convPO.OwnerID,
		MemberIDs:   memberIDs,
		MemberCount: uint32(len(memberIDs)),
		CreatedAt:   convPO.CreatedAt,
		UpdatedAt:   convPO.UpdatedAt,
	}, false, nil
}

// singlePairKey 单聊的成员对唯一键。两个 uid 升序拼成 "a,b"，反向并发落到同一个键。
// a 成员 uid
// b 成员 uid
func singlePairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "," + b
}

// isUniqueConstraintErr 判断 err 是不是唯一约束冲突。err 是待判定的错误；
// MySQL 错误码 1062 (ER_DUP_ENTRY) / 1586 (ER_DUP_ENTRY_WITH_KEY_NAME)。
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == 1062 || myErr.Number == 1586
	}
	// ent 有时把冲突包成 ConstraintError。
	var entErr *ent.ConstraintError
	if errors.As(err, &entErr) {
		return true
	}
	return false
}

// FindByID 按 id 查会话（含成员列表）。id 是会话 ID。
func (r *conversationRepo) FindByID(ctx context.Context, id uint64) (*biz.Conversation, error) {
	po, err := r.data.db.Conversation.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}
	return r.toBizWithMembers(ctx, po)
}

// FindSingleByMembers 查两个成员之间的单聊。
// a 成员 uid
// b 成员 uid
// 直接用 pair_key 查 —— 单条索引命中，不用 join conversation_members。
func (r *conversationRepo) FindSingleByMembers(ctx context.Context, a, b string) (*biz.Conversation, error) {
	po, err := r.data.db.Conversation.Query().
		Where(conversation.PairKeyEQ(singlePairKey(a, b))).
		Order(ent.Asc(conversation.FieldID)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}
	return r.toBizWithMembers(ctx, po)
}

// ListByUserID 列出该用户参与的会话，按 updated_at DESC, id DESC。
// userID 用户 uid
// cursor 零值表示首页
// limit 单页条数
func (r *conversationRepo) ListByUserID(ctx context.Context, userID string, cursor biz.ConversationCursor, limit int) ([]*biz.Conversation, error) {
	if limit <= 0 {
		limit = defaultConvListLimit
	}
	q := r.data.db.Conversation.Query().
		Where(conversation.HasMembersWith(conversationmember.UserIDEQ(userID))).
		Order(ent.Desc(conversation.FieldUpdatedAt), ent.Desc(conversation.FieldID)).
		Limit(limit)
	if !cursor.IsZero() {
		// 反查游标行的 updated_at。查不到（会话被删了）时退化成「只按 id 继续」，
		// 保证分页不会因为一行被删就整个卡住。
		anchor, err := r.data.db.Conversation.Get(ctx, cursor.ID)
		if err != nil {
			if !ent.IsNotFound(err) {
				return nil, err
			}
			anchor = nil
		}
		if anchor == nil {
			q = q.Where(conversation.IDLT(cursor.ID))
		} else {
			q = q.Where(
				conversation.Or(
					conversation.UpdatedAtLT(anchor.UpdatedAt),
					conversation.And(
						conversation.UpdatedAtEQ(anchor.UpdatedAt),
						conversation.IDLT(cursor.ID),
					),
				),
			)
		}
	}
	pos, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Conversation, 0, len(pos))
	for _, po := range pos {
		c, err := r.toBizWithMembers(ctx, po)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// IsMember 判断 user 是否是会话成员。
// conversationID 会话 ID
// userID 用户 uid
func (r *conversationRepo) IsMember(ctx context.Context, conversationID uint64, userID string) (bool, error) {
	n, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// UpdateUnread 增减某成员的会话级未读数，返回更新后的值，不会减到 0 以下。
// conversationID 会话 ID
// userID 成员 uid
// delta 增减量
// 走原生 SQL 的原子表达式 SET unread_count = GREATEST(unread_count + ?, 0)，避免读-改-写丢失更新。
func (r *conversationRepo) UpdateUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if r.data.sqlDB == nil {
		return 0, errors.New("data.sqlDB 未初始化")
	}

	// 一条 UPDATE 改完并取回新值（MySQL 没有 UPDATE ... RETURNING，
	// 所以改完之后再查一次；两次之间别的并发写只可能让返回值更「新」，不会倒退）。
	const updateSQL = `UPDATE conversation_members
		SET unread_count = GREATEST(unread_count + ?, 0)
		WHERE conversation_id = ? AND user_id = ?`

	if _, err := r.data.sqlDB.ExecContext(ctx, updateSQL, delta, conversationID, userID); err != nil {
		return 0, err
	}

	// 不能靠 RowsAffected 判断成员在不在 —— 已经是 0 再减，GREATEST 把它钳回 0，
	// 值没变，affected 就是 0。查一次行在不在，顺带把新值取回来。
	po, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, biz.ErrConversationNotFound
		}
		return 0, err
	}
	if po.UnreadCount < 0 {
		return 0, nil
	}
	return uint32(po.UnreadCount), nil
}

// UpdateUnreadBatch 批量增减多个成员的未读数，返回成功更新的成员数。
// conversationID 会话 ID
// userIDs 成员 uid 列表
// delta 增减量
// 不是成员的 uid 不算失败
// 成员数单独查，不用 RowsAffected 近似 —— GREATEST 钳回 0 时 affected 会是 0。
func (r *conversationRepo) UpdateUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if r.data.sqlDB == nil {
		return 0, errors.New("data.sqlDB 未初始化")
	}
	if len(userIDs) == 0 {
		return 0, nil
	}

	// 去重 + 去掉空串 —— 同一个 uid 传两次会让 IN 列表重复。
	uniq := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		uniq = append(uniq, uid)
	}
	if len(uniq) == 0 {
		return 0, nil
	}

	placeholders := strings.Repeat("?,", len(uniq))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(uniq)+2)
	args = append(args, delta, conversationID)
	for _, uid := range uniq {
		args = append(args, uid)
	}

	// 占位符数量由 uid 个数决定 —— 拼出来的 SQL 里只有问号，
	// 用户输入（uid）走参数绑定，不会拼进语句。
	updateSQL := fmt.Sprintf(`UPDATE conversation_members
		SET unread_count = GREATEST(unread_count + ?, 0)
		WHERE conversation_id = ? AND user_id IN (%s)`, placeholders)

	if _, err := r.data.sqlDB.ExecContext(ctx, updateSQL, args...); err != nil {
		return 0, err
	}

	// 数一下真正改到的成员数。不是成员的 uid 自然不在结果里。
	n, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDIn(uniq...),
		).
		Count(ctx)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

// GetUnread 查某成员的会话级未读数。
// conversationID 会话 ID
// userID 成员 uid
func (r *conversationRepo) GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	po, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, biz.ErrConversationNotFound
		}
		return 0, err
	}
	return uint32(po.UnreadCount), nil
}

// ClearUnread 把某成员的会话级未读数清零，返回清零后的值。成员不存在返 ErrConversationNotFound。
// conversationID 会话 ID
// userID 成员 uid
func (r *conversationRepo) ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	// 先确认成员行存在。不能靠 UPDATE 的 affected 行数判断 ——
	// ent 的 UPDATE 只统计「值真的变了」的行，未读数本来就是 0 时 affected 为 0。
	exists, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		Exist(ctx)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, biz.ErrConversationNotFound
	}
	if _, err := r.data.db.ConversationMember.Update().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		SetUnreadCount(0).
		Save(ctx); err != nil {
		return 0, err
	}
	return 0, nil
}

// ListUnread 查某 uid 在所有会话的未读数（key = conversation_id），只返回未读数 > 0 的会话。
// userID 是用户 uid。
func (r *conversationRepo) ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error) {
	pos, err := r.data.db.ConversationMember.Query().
		Where(
			conversationmember.UserIDEQ(userID),
			conversationmember.UnreadCountGT(0),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]uint32, len(pos))
	for _, po := range pos {
		out[po.ConversationID] = uint32(po.UnreadCount)
	}
	return out, nil
}

// AddMembers 批量加成员，返回 (实际新增数, 加完后的成员总数)，成员数在事务内数出来 —— 并发下这个数才是真值。
// conversationID 会话 ID
// userIDs 要加的用户 uid
func (r *conversationRepo) AddMembers(ctx context.Context, conversationID uint64, userIDs []string) (int, int, error) {
	// userIDs 为空不是错误 —— 调用方想拿一个准确的成员数。这种情况照样数一遍，
	// 返回 (0, 当前总数)。
	if len(userIDs) == 0 {
		total, err := r.data.db.ConversationMember.Query().
			Where(conversationmember.ConversationIDEQ(conversationID)).
			Count(ctx)
		if err != nil {
			return 0, 0, err
		}
		return 0, total, nil
	}
	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 事务内数一次当前成员数 —— 加人上限要在 repo 层兜一道。
	current, err := tx.ConversationMember.Query().
		Where(conversationmember.ConversationIDEQ(conversationID)).
		Count(ctx)
	if err != nil {
		return 0, 0, err
	}

	added := 0
	for _, uid := range userIDs {
		// 已存在就跳过（uk_conv_user 也会兜底，但先查能少一次唯一冲突报错）。
		n, err := tx.ConversationMember.Query().
			Where(
				conversationmember.ConversationIDEQ(conversationID),
				conversationmember.UserIDEQ(uid),
			).
			Count(ctx)
		if err != nil {
			return 0, 0, err
		}
		if n > 0 {
			continue
		}
		// 上限是「最多 MaxGroupMembers 人」。判断的是加完之后的人数：
		// current+added+1 > Max 才算超限。
		if current+added+1 > biz.MaxGroupMembers {
			return 0, 0, biz.ErrConversationMemberLimit
		}
		if _, err := tx.ConversationMember.Create().
			SetConversationID(conversationID).
			SetUserID(uid).
			Save(ctx); err != nil {
			return 0, 0, err
		}
		added++
	}

	// 事务内数成员数 —— 并发下这个数才是真值。
	total, err := tx.ConversationMember.Query().
		Where(conversationmember.ConversationIDEQ(conversationID)).
		Count(ctx)
	if err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	committed = true

	r.cache.InvalidateMembers(ctx, conversationID)
	return added, total, nil
}

// RemoveMember 踢人。找不到成员返 ErrConversationNotFound。返回踢完后的成员总数（事务内数出）。
// conversationID 会话 ID
// userID 被踢的用户 uid
func (r *conversationRepo) RemoveMember(ctx context.Context, conversationID uint64, userID string) (int, error) {
	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	deleted, err := tx.ConversationMember.Delete().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(userID),
		).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	if deleted == 0 {
		return 0, biz.ErrConversationNotFound
	}

	// 事务内数成员数 —— 并发下这个数才是真值。
	total, err := tx.ConversationMember.Query().
		Where(conversationmember.ConversationIDEQ(conversationID)).
		Count(ctx)
	if err != nil {
		return 0, err
	}

	// 不能把会话删成空壳。守卫放在事务里：并发踢人时最后一个人不会被踢掉。
	if total < 1 {
		return 0, biz.ErrConversationEmpty
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true

	r.cache.InvalidateMembers(ctx, conversationID)
	return total, nil
}

// UpdateName 改群名。
// id 会话 ID
// name 新群名
func (r *conversationRepo) UpdateName(ctx context.Context, id uint64, name string) (*biz.Conversation, error) {
	po, err := r.data.db.Conversation.UpdateOneID(id).
		SetName(name).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}
	return r.toBizWithMembers(ctx, po)
}

// TransferOwner 转让群主，事务内完成。新群主必须在会里，否则返 ErrConversationNotFound。
// conversationID 会话 ID
// newOwnerID 新群主的 uid
func (r *conversationRepo) TransferOwner(ctx context.Context, conversationID uint64, newOwnerID string) (*biz.Conversation, error) {
	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	n, err := tx.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ(newOwnerID),
		).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, biz.ErrConversationNotFound
	}

	po, err := tx.Conversation.UpdateOneID(conversationID).
		SetOwnerID(newOwnerID).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	return r.toBizWithMembers(ctx, po)
}

// Dissolve 解散会话，连同全部成员记录一起删掉。conversationID 是会话 ID。
// 事务内完成；成员记录有指向 conversations 的外键，所以先删成员再删会话。
func (r *conversationRepo) Dissolve(ctx context.Context, conversationID uint64) error {
	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ConversationMember.Delete().
		Where(conversationmember.ConversationIDEQ(conversationID)).
		Exec(ctx); err != nil {
		return err
	}

	if err := tx.Conversation.DeleteOneID(conversationID).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return biz.ErrConversationNotFound
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true

	r.cache.InvalidateMembers(ctx, conversationID)
	return nil
}

// TouchConversation 更新会话的 updated_at（成员变动 / 改名时调用）。id 是会话 ID。
func (r *conversationRepo) TouchConversation(ctx context.Context, id uint64) error {
	_, err := r.data.db.Conversation.UpdateOneID(id).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	return err
}

// toBizWithMembers ent PO → biz DO，含成员列表。成员列表走 Redis 缓存，未命中查库并回填。
// po 是 ent 会话行。
func (r *conversationRepo) toBizWithMembers(ctx context.Context, po *ent.Conversation) (*biz.Conversation, error) {
	if po == nil {
		return nil, biz.ErrConversationNotFound
	}
	// 版本号要在查库**之前**取 —— 回填时带着它，
	// 期间有人改成员的话版本会变，回填自动放弃（见 SetMembers 的注释）。
	ver := r.cache.MembersVersion(ctx, po.ID)

	members, _, err := r.cache.Members(ctx, po.ID)
	if err != nil {
		// 缓存出错不阻断主路径，直接查库。
		members, err = r.membersFromDB(ctx, po.ID)
		if err != nil {
			return nil, err
		}
	} else if len(members) == 0 {
		// 缓存未命中：回源查库并回填。
		members, err = r.membersFromDB(ctx, po.ID)
		if err != nil {
			return nil, err
		}
		r.cache.SetMembers(ctx, po.ID, members, ver)
	}
	return &biz.Conversation{
		ID:          po.ID,
		Type:        biz.ConversationType(po.Type),
		Name:        po.Name,
		OwnerID:     po.OwnerID,
		MemberIDs:   members,
		MemberCount: uint32(len(members)),
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}, nil
}

// membersFromDB 直接查库拿成员列表（升序）。conversationID 是会话 ID。
func (r *conversationRepo) membersFromDB(ctx context.Context, conversationID uint64) ([]string, error) {
	pos, err := r.data.db.ConversationMember.Query().
		Where(conversationmember.ConversationIDEQ(conversationID)).
		Order(ent.Asc(conversationmember.FieldUserID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pos))
	for _, po := range pos {
		out = append(out, po.UserID)
	}
	return out, nil
}

// ConversationCache 会话相关 Redis 缓存。key 是 chatroom:conv:{id}:members（Set，会话成员 uid）。
// 缓存是派生数据，MySQL 是权威源：写路径「先写库，再删缓存」，读路径未命中回源。
type ConversationCache struct {
	rdb        redis.Cmdable
	membersTTL time.Duration // 成员列表缓存 TTL。成员变动时主动删，短 TTL 是漏删时的兜底
}

// NewConversationCache 构造 ConversationCache。
// d 是 data 客户端
// Redis 不可用时返 nil。
func NewConversationCache(d *Data) *ConversationCache {
	if d == nil || d.Redis() == nil {
		return nil
	}
	return &ConversationCache{rdb: d.Redis(), membersTTL: 5 * time.Minute}
}

// convMembersKey 会话成员集合 key。conversationID 是会话 ID。
func convMembersKey(conversationID uint64) string {
	return "chatroom:conv:" + formatUint(conversationID) + ":members"
}

// convMembersVerKey 会话成员集合的版本号 key。conversationID 是会话 ID；
// 回填时用 Lua 比对版本，版本变了就放弃回填。
func convMembersVerKey(conversationID uint64) string {
	return "chatroom:conv:" + formatUint(conversationID) + ":members:ver"
}

// formatUint 把 uint64 转成十进制字符串。v 是待转换的值。
func formatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}

// Members 读会话成员 uid 列表（升序）。conversationID 是会话 ID，未命中返 (nil, nil)；
// 第二个返回值是回填时该带的版本号（读成员列表之前取）。
func (c *ConversationCache) Members(ctx context.Context, conversationID uint64) ([]string, int64, error) {
	if c == nil || c.rdb == nil {
		return nil, 0, nil
	}
	vals, err := c.rdb.SMembers(ctx, convMembersKey(conversationID)).Result()
	if err != nil {
		return nil, 0, err
	}
	if len(vals) == 0 {
		return nil, 0, nil
	}
	out := make([]string, 0, len(vals))
	out = append(out, vals...)
	sort.Strings(out)
	return out, 0, nil
}

// MembersVersion 读成员集合的版本号。conversationID 是会话 ID，缺失返 0。
func (c *ConversationCache) MembersVersion(ctx context.Context, conversationID uint64) int64 {
	if c == nil || c.rdb == nil {
		return 0
	}
	v, err := c.rdb.Get(ctx, convMembersVerKey(conversationID)).Result()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// SetMembers 回填会话成员集合。
// conversationID 会话 ID
// members 成员 uid 列表
// ver 是回源之前取到的版本号；版本变了就放弃回填。
func (c *ConversationCache) SetMembers(ctx context.Context, conversationID uint64, members []string, ver int64) {
	if c == nil || c.rdb == nil || len(members) == 0 {
		return
	}
	vals := make([]any, 0, len(members))
	for _, m := range members {
		vals = append(vals, m)
	}

	// Lua：比对版本 → 一致才 Del + SAdd + Expire。
	// 「比对」和「写」必须是一个原子步骤，所以用 Lua。
	const setIfVersionMatches = `
local cur = redis.call('GET', KEYS[2])
if cur == false then
  cur = '0'
end
if tonumber(cur) ~= tonumber(ARGV[1]) then
  return 0
end
redis.call('DEL', KEYS[1])
redis.call('SADD', KEYS[1], unpack(ARGV, 3))
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1
`
	keys := []string{convMembersKey(conversationID), convMembersVerKey(conversationID)}
	args := make([]any, 0, len(vals)+2)
	args = append(args, ver, int(c.membersTTL.Seconds()))
	for _, m := range vals {
		args = append(args, m)
	}
	if err := c.rdb.Eval(ctx, setIfVersionMatches, keys, args...).Err(); err != nil {
		slog.WarnContext(ctx, "会话成员缓存回填降级", "err", err, "conversation_id", conversationID)
	}
}

// InvalidateMembers 删掉会话成员集合缓存并把版本号 +1。conversationID 是会话 ID。
func (c *ConversationCache) InvalidateMembers(ctx context.Context, conversationID uint64) {
	if c == nil || c.rdb == nil {
		return
	}
	// 版本号先 +1，让正在进行的回填（带着旧版本）失败。顺序不能反。
	if err := c.rdb.Incr(ctx, convMembersVerKey(conversationID)).Err(); err != nil {
		slog.WarnContext(ctx, "会话成员缓存版本递增降级", "err", err, "conversation_id", conversationID)
	}
	if err := c.rdb.Del(ctx, convMembersKey(conversationID)).Err(); err != nil {
		slog.WarnContext(ctx, "会话成员缓存失效降级", "err", err, "conversation_id", conversationID)
	}
}
