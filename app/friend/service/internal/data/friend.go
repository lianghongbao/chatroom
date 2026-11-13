package data

import (
	"context"
	"errors"
	"fmt"

	"chatroom/app/friend/service/internal/biz"
	"chatroom/app/friend/service/internal/data/ent"
	"chatroom/app/friend/service/internal/data/ent/friend"

	"github.com/go-sql-driver/mysql"
)

// friendRepo biz.FriendRepo 的 ent 实现。
// 表 friends 上挂 (user_id, friend_id) unique、(friend_id)、(user_id, id) 三个索引。
type friendRepo struct {
	data *Data
}

// NewFriendRepo 构造 biz.FriendRepo 的 ent 实现。data 是数据层实例。
func NewFriendRepo(data *Data) biz.FriendRepo {
	return &friendRepo{data: data}
}

// toBizFriend 把 ent.Friend PO 折成 biz.Friend DO。po 为 nil 时返 nil。
func toBizFriend(po *ent.Friend) *biz.Friend {
	if po == nil {
		return nil
	}
	return &biz.Friend{
		ID: po.ID, UserID: po.UserID, FriendID: po.FriendID,
		Status: biz.FriendStatus(po.Status), Message: po.Message,
		CreatedAt: po.CreatedAt, UpdatedAt: po.UpdatedAt,
	}
}

// Save 新建好友关系。f 是待落库的关系；撞唯一索引时折叠为 ErrFriendAlreadyApplied。
func (r *friendRepo) Save(ctx context.Context, f *biz.Friend) (uint64, error) {
	po, err := r.data.db.Friend.Create().
		SetUserID(f.UserID).SetFriendID(f.FriendID).
		SetStatus(uint8(f.Status)).SetMessage(f.Message).
		SetPairKey(pairKey(f.UserID, f.FriendID)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return 0, biz.ErrFriendAlreadyApplied
		}
		// 并发插同一条 pair_key 时 InnoDB 可能报死锁（1213）而不是
		// 唯一索引冲突（1062）。这两种情况语义一样：这条「两人对」已经有人建了。
		if isDeadlockErr(err) {
			return 0, biz.ErrFriendAlreadyApplied
		}
		return 0, err
	}
	return po.ID, nil
}

// isDeadlockErr 判断 err 是不是 InnoDB 死锁（1213）或锁等待超时（1205）。
// 1213 = ER_LOCK_DEADLOCK，1205 = ER_LOCK_WAIT_TIMEOUT。
func isDeadlockErr(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}

// pairKey 归一化的「两人对」唯一键。a、b 是两个 uid，升序拼成 "a,b"，
// 保证 (a,b) 和 (b,a) 落到同一个键，唯一索引才挡得住反向并发。
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "," + b
}

// FindByID 按主键查好友关系。id 是记录主键，找不到返 ErrFriendNotFound。
func (r *friendRepo) FindByID(ctx context.Context, id uint64) (*biz.Friend, error) {
	po, err := r.data.db.Friend.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrFriendNotFound
		}
		return nil, err
	}
	return toBizFriend(po), nil
}

// FindByPair 按 (user_id, friend_id) 正向查。userID 是发起方，friendID 是接收方，
// 找不到返 ErrFriendNotFound。
func (r *friendRepo) FindByPair(ctx context.Context, userID, friendID string) (*biz.Friend, error) {
	po, err := r.data.db.Friend.Query().
		Where(friend.UserIDEQ(userID), friend.FriendIDEQ(friendID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrFriendNotFound
		}
		return nil, err
	}
	return toBizFriend(po), nil
}

// FindBothDirections 一次查 (a,b) 和 (b,a) 两条。a、b 是两个 uid，
// 返回正向与反向两条记录，0 条时返 (nil, nil, nil)。
func (r *friendRepo) FindBothDirections(ctx context.Context, a, b string) (*biz.Friend, *biz.Friend, error) {
	pos, err := r.data.db.Friend.Query().
		Where(friend.Or(
			friend.And(friend.UserIDEQ(a), friend.FriendIDEQ(b)),
			friend.And(friend.UserIDEQ(b), friend.FriendIDEQ(a)),
		)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	var forward, reverse *biz.Friend
	for _, po := range pos {
		f := toBizFriend(po)
		if f.UserID == a {
			forward = f
		} else {
			reverse = f
		}
	}
	return forward, reverse, nil
}

// FindAcceptedByUserIDAndID 数据库层过滤 status=ACCEPTED 且 id>cursor，按 id 升序。
// userID 是操作者 ID，cursor 是上一页末位 ID，limit 是每页条数。
// 双向命中：caller 既可能在 user_id 列也可能在 friend_id 列。
func (r *friendRepo) FindAcceptedByUserIDAndID(ctx context.Context, userID string, cursor uint64, limit int) ([]*biz.Friend, error) {
	q := r.data.db.Friend.Query().
		Where(
			friend.Or(
				friend.UserIDEQ(userID),
				friend.FriendIDEQ(userID),
			),
			friend.StatusEQ(uint8(biz.FriendStatusAccepted)),
		).
		Order(ent.Asc(friend.FieldID))
	if cursor > 0 {
		q = q.Where(friend.IDGT(cursor))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	pos, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Friend, 0, len(pos))
	for _, po := range pos {
		out = append(out, toBizFriend(po))
	}
	return out, nil
}

// UpdateStatusIfRecipient 单条 UPDATE 带鉴权 + 状态守卫。
// id 是记录主键，recipientID 是接收方 ID，from、to 是状态迁移的两端。
// 命中 0 行折叠 ErrFriendNotFound。
func (r *friendRepo) UpdateStatusIfRecipient(ctx context.Context, id uint64, recipientID string, from, to biz.FriendStatus) (*biz.Friend, error) {
	n, err := r.data.db.Friend.Update().
		Where(friend.IDEQ(id), friend.FriendIDEQ(recipientID), friend.StatusEQ(uint8(from))).
		SetStatus(uint8(to)).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, biz.ErrFriendNotFound
	}
	po, err := r.data.db.Friend.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, biz.ErrFriendNotFound
		}
		return nil, err
	}
	return toBizFriend(po), nil
}

// GetRecipientAndStatus 鉴权失败后的最小探测。id 是记录主键，
// 返回接收方 ID 与状态，只读 friend_id + status 两列。
func (r *friendRepo) GetRecipientAndStatus(ctx context.Context, id uint64) (string, biz.FriendStatus, error) {
	po, err := r.data.db.Friend.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", 0, biz.ErrFriendNotFound
		}
		return "", 0, err
	}
	return po.FriendID, biz.FriendStatus(po.Status), nil
}

// FindPendingByRecipientID 收件箱列表：查作为目标方收到的 PENDING 申请。
// recipientID 是接收方 ID，cursor 是上一页末位 ID，limit 是每页条数。
func (r *friendRepo) FindPendingByRecipientID(ctx context.Context, recipientID string, cursor uint64, limit int) ([]*biz.Friend, error) {
	q := r.data.db.Friend.Query().
		Where(friend.FriendIDEQ(recipientID), friend.StatusEQ(uint8(biz.FriendStatusPending))).
		Order(ent.Asc(friend.FieldID))
	if cursor > 0 {
		q = q.Where(friend.IDGT(cursor))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	pos, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Friend, 0, len(pos))
	for _, po := range pos {
		out = append(out, toBizFriend(po))
	}
	return out, nil
}

// CountPendingByRecipientID 收件箱计数。recipientID 是接收方 ID。
func (r *friendRepo) CountPendingByRecipientID(ctx context.Context, recipientID string) (uint64, error) {
	n, err := r.data.db.Friend.Query().
		Where(friend.FriendIDEQ(recipientID), friend.StatusEQ(uint8(biz.FriendStatusPending))).
		Count(ctx)
	if err != nil {
		return 0, err
	}
	return uint64(n), nil
}

// DeleteByEitherPair 「双向查 + 删」包同一事务，避免两步操作间的竞态。
// callerID、otherID 是两个 uid。仅删 status=ACCEPTED 的记录；PENDING/REJECTED 不删。
// deleted=true 表示删了至少一条。
func (r *friendRepo) DeleteByEitherPair(ctx context.Context, callerID, otherID string) (bool, error) {
	if r.data == nil || r.data.db == nil {
		// nil 说明内部 DI 失败，不是 caller 输入错。
		return false, fmt.Errorf("friendRepo: data layer not initialized")
	}
	tx, err := r.data.db.Tx(ctx)
	if err != nil {
		return false, err
	}
	// defer Rollback 兜底，Commit 后 Rollback 是 no-op。
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	client := tx.Client()
	tryDelete := func(po *ent.Friend) (bool, error) {
		if po.Status != uint8(biz.FriendStatusAccepted) {
			return false, nil
		}
		if _, err := client.Friend.Delete().Where(friend.IDEQ(po.ID)).Exec(ctx); err != nil {
			return false, err
		}
		return true, nil
	}

	// 正向探。
	po, err := client.Friend.Query().
		Where(friend.UserIDEQ(callerID), friend.FriendIDEQ(otherID)).Only(ctx)
	if err == nil {
		ok, err := tryDelete(po)
		if err != nil {
			return false, err
		}
		if ok {
			if err := tx.Commit(); err != nil {
				return false, err
			}
			committed = true
			return true, nil
		}
		return false, nil
	}
	if !ent.IsNotFound(err) {
		return false, err
	}

	// 反向探。
	po, err = client.Friend.Query().
		Where(friend.UserIDEQ(otherID), friend.FriendIDEQ(callerID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	ok, err := tryDelete(po)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

// DeleteByPair 按正向 (user_id, friend_id) 单条删。userID 是发起方，friendID 是接收方，
// 不命中返 ErrFriendNotFound。
func (r *friendRepo) DeleteByPair(ctx context.Context, userID, friendID string) error {
	n, err := r.data.db.Friend.Delete().
		Where(friend.UserIDEQ(userID), friend.FriendIDEQ(friendID)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return biz.ErrFriendNotFound
	}
	return nil
}
