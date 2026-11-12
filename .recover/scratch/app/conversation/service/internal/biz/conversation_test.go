package biz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"
)

// fakeConvRepo 是 biz.ConversationRepo 的内存替身。语义对齐 data 层：
//   - 单聊唯一：同一对成员只能有一个 type=1 会话。
//   - uk_conv_user 唯一：同一会话内不能有重复成员。
type fakeConvRepo struct {
	byID   map[uint64]*Conversation
	nextID uint64
	// saveCalls 记录 Save 被调用的次数，用来断言"复用单聊不写库"。
	saveCalls int
	// unread 存每个 (conversationID, userID) 的未读数。
	unread map[string]uint32
}

func newFakeConvRepo() *fakeConvRepo {
	return &fakeConvRepo{byID: make(map[uint64]*Conversation), nextID: 1, unread: make(map[string]uint32)}
}

// copyConv 复制一份，避免测试里的修改污染 repo 内部状态。
func copyConv(c *Conversation) *Conversation {
	if c == nil {
		return nil
	}
	cp := *c
	cp.MemberIDs = append([]string(nil), c.MemberIDs...)
	return &cp
}

func (r *fakeConvRepo) Save(_ context.Context, c *Conversation) (*Conversation, bool, error) {
	r.saveCalls++
	if c.Type == ConversationTypeSingle && len(c.MemberIDs) == 2 {
		existing, err := r.FindSingleByMembers(context.Background(), c.MemberIDs[0], c.MemberIDs[1])
		if err == nil && existing != nil {
			return existing, true, nil
		}
	}
	id := r.nextID
	r.nextID++
	stored := copyConv(c)
	stored.ID = id
	stored.MemberCount = uint32(len(stored.MemberIDs))
	// 落库后才有时间戳 —— repo 返回的是带时间戳的会话。
	stored.CreatedAt = time.Now()
	stored.UpdatedAt = stored.CreatedAt
	r.byID[id] = stored
	return copyConv(stored), false, nil
}

func (r *fakeConvRepo) FindByID(_ context.Context, id uint64) (*Conversation, error) {
	c, ok := r.byID[id]
	if !ok {
		return nil, ErrConversationNotFound
	}
	return copyConv(c), nil
}

func (r *fakeConvRepo) FindSingleByMembers(_ context.Context, a, b string) (*Conversation, error) {
	for _, c := range r.byID {
		if c.Type != ConversationTypeSingle {
			continue
		}
		hasA, hasB := false, false
		for _, m := range c.MemberIDs {
			if m == a {
				hasA = true
			}
			if m == b {
				hasB = true
			}
		}
		if hasA && hasB {
			return copyConv(c), nil
		}
	}
	return nil, ErrConversationNotFound
}

func (r *fakeConvRepo) ListByUserID(_ context.Context, userID string, cursor ConversationCursor, limit int) ([]*Conversation, error) {
	// 游标只带 id —— repo 层反查那一行的 updated_at 拼复合谓词，
	// 与 data 层的行为一致。
	var anchorUpdatedAt time.Time
	if !cursor.IsZero() {
		if c, ok := r.byID[cursor.ID]; ok {
			anchorUpdatedAt = c.UpdatedAt
		}
	}
	var out []*Conversation
	for _, c := range r.byID {
		member := false
		for _, m := range c.MemberIDs {
			if m == userID {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		if !cursor.IsZero() {
			if anchorUpdatedAt.IsZero() {
				// 游标行没了 —— 退化成只按 id 继续。
				if c.ID >= cursor.ID {
					continue
				}
			} else {
				if c.UpdatedAt.After(anchorUpdatedAt) {
					continue
				}
				if c.UpdatedAt.Equal(anchorUpdatedAt) && c.ID >= cursor.ID {
					continue
				}
			}
		}
		out = append(out, copyConv(c))
	}
	// 按 (updated_at DESC, id DESC) 排。
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeConvRepo) IsMember(_ context.Context, conversationID uint64, userID string) (bool, error) {
	c, ok := r.byID[conversationID]
	if !ok {
		return false, nil
	}
	for _, m := range c.MemberIDs {
		if m == userID {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeConvRepo) AddMembers(_ context.Context, conversationID uint64, userIDs []string) (int, int, error) {
	c, ok := r.byID[conversationID]
	if !ok {
		return 0, 0, ErrConversationNotFound
	}
	existing := make(map[string]struct{}, len(c.MemberIDs))
	for _, m := range c.MemberIDs {
		existing[m] = struct{}{}
	}
	added := 0
	for _, uid := range userIDs {
		if _, ok := existing[uid]; ok {
			continue
		}
		existing[uid] = struct{}{}
		c.MemberIDs = append(c.MemberIDs, uid)
		added++
	}
	sort.Strings(c.MemberIDs)
	c.MemberCount = uint32(len(c.MemberIDs))
	return added, int(c.MemberCount), nil
}

func (r *fakeConvRepo) RemoveMember(_ context.Context, conversationID uint64, userID string) (int, error) {
	c, ok := r.byID[conversationID]
	if !ok {
		return 0, ErrConversationNotFound
	}
	out := c.MemberIDs[:0]
	found := false
	for _, m := range c.MemberIDs {
		if m == userID {
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return 0, ErrConversationNotFound
	}
	c.MemberIDs = out
	c.MemberCount = uint32(len(c.MemberIDs))
	return int(c.MemberCount), nil
}

func (r *fakeConvRepo) UpdateName(_ context.Context, id uint64, name string) (*Conversation, error) {
	c, ok := r.byID[id]
	if !ok {
		return nil, ErrConversationNotFound
	}
	c.Name = name
	return copyConv(c), nil
}

func (r *fakeConvRepo) TransferOwner(_ context.Context, conversationID uint64, newOwnerID string) (*Conversation, error) {
	c, ok := r.byID[conversationID]
	if !ok {
		return nil, ErrConversationNotFound
	}
	found := false
	for _, m := range c.MemberIDs {
		if m == newOwnerID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrConversationNotFound
	}
	c.OwnerID = newOwnerID
	return copyConv(c), nil
}

func (r *fakeConvRepo) Dissolve(_ context.Context, conversationID uint64) error {
	if _, ok := r.byID[conversationID]; !ok {
		return ErrConversationNotFound
	}
	delete(r.byID, conversationID)
	return nil
}

func (r *fakeConvRepo) TouchConversation(_ context.Context, id uint64) error {
	c, ok := r.byID[id]
	if !ok {
		return ErrConversationNotFound
	}
	c.UpdatedAt = time.Now()
	return nil
}

// unread 存每个 (conversationID, userID) 的未读数。
func (r *fakeConvRepo) UpdateUnread(_ context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if _, ok := r.byID[conversationID]; !ok {
		return 0, ErrConversationNotFound
	}
	key := unreadKey(conversationID, userID)
	target := int32(r.unread[key]) + delta
	if target < 0 {
		target = 0
	}
	r.unread[key] = uint32(target)
	return r.unread[key], nil
}

func (r *fakeConvRepo) UpdateUnreadBatch(_ context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if _, ok := r.byID[conversationID]; !ok {
		return 0, ErrConversationNotFound
	}
	var n uint32
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		key := unreadKey(conversationID, userID)
		target := int32(r.unread[key]) + delta
		if target < 0 {
			target = 0
		}
		r.unread[key] = uint32(target)
		n++
	}
	return n, nil
}

func (r *fakeConvRepo) GetUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	if _, ok := r.byID[conversationID]; !ok {
		return 0, ErrConversationNotFound
	}
	return r.unread[unreadKey(conversationID, userID)], nil
}

func (r *fakeConvRepo) ClearUnread(_ context.Context, conversationID uint64, userID string) (uint32, error) {
	if _, ok := r.byID[conversationID]; !ok {
		return 0, ErrConversationNotFound
	}
	r.unread[unreadKey(conversationID, userID)] = 0
	return 0, nil
}

func (r *fakeConvRepo) ListUnread(_ context.Context, userID string) (map[uint64]uint32, error) {
	out := make(map[uint64]uint32)
	for key, n := range r.unread {
		if n == 0 {
			continue
		}
		cid, uid := splitUnreadKey(key)
		if uid != userID {
			continue
		}
		out[cid] = n
	}
	return out, nil
}

// unreadKey 未读数的 map key。
func unreadKey(conversationID uint64, userID string) string {
	return fmt.Sprintf("%d:%s", conversationID, userID)
}

// splitUnreadKey 解析 unreadKey。
func splitUnreadKey(key string) (uint64, string) {
	var cid uint64
	var uid string
	_, _ = fmt.Sscanf(key, "%d:%s", &cid, &uid)
	return cid, uid
}

// fakeConvFetcher 是 biz.ConversationProfileFetcher 的内存替身。
type fakeConvFetcher struct {
	profiles map[string]*UserProfile
	err      error
	calls    int
}

func (f *fakeConvFetcher) FetchProfiles(_ context.Context, ids []string) ([]*UserProfile, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*UserProfile, 0, len(ids))
	for _, id := range ids {
		if p, ok := f.profiles[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// fakeExistenceChecker 是 biz.UserExistenceChecker 的内存替身。
//
//	existing 存在的 uid 集合；为空时视为"全部存在"（跳过校验）。
//	err 非 nil 时模拟 user 服务不可达 —— biz 应该放行而不是拦下。
type fakeExistenceChecker struct {
	existing map[string]struct{}
	err      error
	calls    int
}

func (f *fakeExistenceChecker) CheckUsersExist(_ context.Context, ids []string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.existing == nil {
		return nil, nil
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := f.existing[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func newTestConvUsecase() (*ConversationUsecase, *fakeConvRepo) {
	repo := newFakeConvRepo()
	return NewConversationUsecase(repo, nil, nil, nil, nil), repo
}

func TestConversation_CreateSingleChatReusesExisting(t *testing.T) {
	uc, repo := newTestConvUsecase()

	c1, reused, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-1", "uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("第一次创建失败: %v", err)
	}
	if reused {
		t.Fatal("第一次创建不该是复用")
	}

	// 反向创建同一对成员（调用方是 2）——应复用同一个会话。
	c2, reused2, err := uc.CreateConversation(context.Background(), "uid-2", []string{"uid-1", "uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("第二次创建失败: %v", err)
	}
	if !reused2 {
		t.Fatal("第二次创建应复用已有单聊")
	}
	if c1.ID != c2.ID {
		t.Fatalf("复用后 id 不一致: %d vs %d", c1.ID, c2.ID)
	}
	if repo.saveCalls != 1 {
		t.Fatalf("复用路径不应写库，saveCalls = %d, want 1", repo.saveCalls)
	}
}

// TestConversation_CreateSingleChatReuseFillsProfiles 复用单聊时也要带昵称/头像。
// 复用路径不走 Save，直接返回 repo 查到的会话。如果这里不补资料，同一个会话
// 首次建出来有 member_nicknames、重复建就没有 —— 字段形状随调用时机变化。
func TestConversation_CreateSingleChatReuseFillsProfiles(t *testing.T) {
	repo := newFakeConvRepo()
	fetcher := &fakeConvFetcher{profiles: map[string]*UserProfile{
		"uid-1": {UserID: "uid-1", Nickname: "Alice", Avatar: "a.png"},
		"uid-2": {UserID: "uid-2", Nickname: "Bob", Avatar: "b.png"},
	}}
	uc := NewConversationUsecase(repo, fetcher, nil, nil, nil)

	if _, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, ""); err != nil {
		t.Fatalf("第一次创建失败: %v", err)
	}

	c, reused, err := uc.CreateConversation(context.Background(), "uid-2", []string{"uid-1"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("复用失败: %v", err)
	}
	if !reused {
		t.Fatal("第二次创建应复用")
	}
	if len(c.MemberNicknames) != len(c.MemberIDs) {
		t.Fatalf("复用时昵称数组长度 %d 与成员数 %d 不一致", len(c.MemberNicknames), len(c.MemberIDs))
	}
	if c.MemberNicknames[0] != "Alice" || c.MemberNicknames[1] != "Bob" {
		t.Fatalf("复用时昵称错位: %v", c.MemberNicknames)
	}
	if c.MemberAvatars[0] != "a.png" {
		t.Fatalf("复用时头像错位: %v", c.MemberAvatars)
	}
}

func TestConversation_CreateSingleChatNormalizesMemberOrder(t *testing.T) {
	uc, _ := newTestConvUsecase()

	// 调用方 1，成员列表写 [2, 1]（含调用方）——归一化后仍是 {1,2}。
	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-1"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if len(c.MemberIDs) != 2 || c.MemberIDs[0] != "uid-1" || c.MemberIDs[1] != "uid-2" {
		t.Fatalf("成员列表未归一化: %v", c.MemberIDs)
	}
	if c.Type != ConversationTypeSingle {
		t.Fatalf("类型应为单聊，实际 %v", c.Type)
	}
	if c.OwnerID != "" {
		t.Fatalf("单聊 owner 应为空串，实际 %q", c.OwnerID)
	}
	if c.Name != "" {
		t.Fatalf("单聊不应有名字，实际 %q", c.Name)
	}
}

func TestConversation_CreateInfersTypeFromMemberCount(t *testing.T) {
	uc, _ := newTestConvUsecase()

	// type 未指定（0），2 人 → 单聊。
	single, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, 0, "")
	if err != nil {
		t.Fatalf("创建单聊失败: %v", err)
	}
	if single.Type != ConversationTypeSingle {
		t.Fatalf("2 人应推断为单聊，实际 %v", single.Type)
	}

	// type 未指定（0），3 人 → 群聊。
	group, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, 0, "群")
	if err != nil {
		t.Fatalf("创建群聊失败: %v", err)
	}
	if group.Type != ConversationTypeGroup {
		t.Fatalf("3 人应推断为群聊，实际 %v", group.Type)
	}
	if group.OwnerID != "uid-1" {
		t.Fatalf("群主应为创建者 uid-1，实际 %q", group.OwnerID)
	}
}

func TestConversation_CreateRejectsSingleMember(t *testing.T) {
	uc, _ := newTestConvUsecase()

	_, _, err := uc.CreateConversation(context.Background(), "uid-1", nil, ConversationTypeGroup, "群")
	if !errors.Is(err, ErrConversationInvalidArgument) {
		t.Fatalf("应返回 INVALID_ARGUMENT，实际 %v", err)
	}
}

func TestConversation_GetConversationHidesFromNonMember(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 成员 2 能看到。
	if _, err := uc.GetConversation(context.Background(), "uid-2", c.ID); err != nil {
		t.Fatalf("成员应能看到会话: %v", err)
	}
	// 非成员 3 看不到 —— 折叠成 NOT_FOUND，不泄露"会话存在"。
	_, err = uc.GetConversation(context.Background(), "uid-3", c.ID)
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("非成员应得到 NOT_FOUND，实际 %v", err)
	}
}

func TestConversation_AddMembersOwnerOnly(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 非群主调用 → NOT_OWNER。
	_, _, err = uc.AddMembers(context.Background(), "uid-2", c.ID, []string{"uid-3"})
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主应得到 NOT_OWNER，实际 %v", err)
	}

	// 群主加人。
	count, added, err := uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-3"})
	if err != nil {
		t.Fatalf("群主加人失败: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if count != 3 {
		t.Fatalf("member_count = %d, want 3", count)
	}

	// 重复加同一人 → added = 0，不报错。
	_, added2, err := uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-3"})
	if err != nil {
		t.Fatalf("重复加人失败: %v", err)
	}
	if added2 != 0 {
		t.Fatalf("重复加人 added = %d, want 0", added2)
	}
}

func TestConversation_AddMembersRejectsSingleChat(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	_, _, err = uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-3"})
	if !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("单聊加人不该成功，实际 %v", err)
	}
}

func TestConversation_RemoveMemberOwnerOnlyAndNotSelf(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 群主不能踢自己。
	_, err = uc.RemoveMember(context.Background(), "uid-1", c.ID, "uid-1")
	if !errors.Is(err, ErrConversationInvalidArgument) {
		t.Fatalf("群主踢自己应被拒，实际 %v", err)
	}

	// 非群主不能踢人。
	_, err = uc.RemoveMember(context.Background(), "uid-2", c.ID, "uid-3")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主踢人应得到 NOT_OWNER，实际 %v", err)
	}

	// 群主踢普通成员。
	count, err := uc.RemoveMember(context.Background(), "uid-1", c.ID, "uid-3")
	if err != nil {
		t.Fatalf("群主踢人失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("member_count = %d, want 2", count)
	}
}

func TestConversation_LeaveConversationOwnerCannotLeave(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 群主退群 → 拒绝。
	err = uc.LeaveConversation(context.Background(), "uid-1", c.ID)
	if !errors.Is(err, ErrConversationOwnerCannotLeave) {
		t.Fatalf("群主退群应被拒，实际 %v", err)
	}

	// 普通成员退群 → 成功。
	if err := uc.LeaveConversation(context.Background(), "uid-2", c.ID); err != nil {
		t.Fatalf("普通成员退群失败: %v", err)
	}
}

func TestConversation_UpdateNameOwnerOnly(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "旧名")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 非群主改名 → NOT_OWNER。
	_, err = uc.UpdateConversation(context.Background(), "uid-2", c.ID, "新名")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主改名应得到 NOT_OWNER，实际 %v", err)
	}

	// 空名 → INVALID_ARGUMENT。
	_, err = uc.UpdateConversation(context.Background(), "uid-1", c.ID, "   ")
	if !errors.Is(err, ErrConversationInvalidArgument) {
		t.Fatalf("空名应被拒，实际 %v", err)
	}

	// 超长名 → INVALID_ARGUMENT。
	_, err = uc.UpdateConversation(context.Background(), "uid-1", c.ID, string(make([]byte, MaxConversationNameLen+1)))
	if !errors.Is(err, ErrConversationInvalidArgument) {
		t.Fatalf("超长名应被拒，实际 %v", err)
	}

	// 群主改名 → 成功。
	updated, err := uc.UpdateConversation(context.Background(), "uid-1", c.ID, "新名")
	if err != nil {
		t.Fatalf("群主改名失败: %v", err)
	}
	if updated.Name != "新名" {
		t.Fatalf("name = %q, want 新名", updated.Name)
	}
}

func TestConversation_UpdateNameRejectsSingleChat(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	_, err = uc.UpdateConversation(context.Background(), "uid-1", c.ID, "新名")
	if !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("单聊改名应被拒，实际 %v", err)
	}
}

func TestConversation_CursorRoundTrip(t *testing.T) {
	c := &Conversation{ID: 0xABCDEF, UpdatedAt: time.UnixMicro(1700000000123456).UTC()}
	enc := encodeCursor(c)
	if enc == 0 {
		t.Fatal("游标编码不应为 0")
	}
	// 游标只带 id —— 微秒时间戳塞不进 uint64，所以不编码时间戳。
	if enc != c.ID {
		t.Fatalf("游标应为会话 id %d，实际 %d", c.ID, enc)
	}
	dec := decodeCursor(enc)
	if dec.ID != c.ID {
		t.Fatalf("id 解码错误: %d, want %d", dec.ID, c.ID)
	}
}

func TestConversation_DecodeCursorZeroIsFirstPage(t *testing.T) {
	c := decodeCursor(0)
	if !c.IsZero() {
		t.Fatalf("cursor=0 应是首页，实际 %v", c)
	}
}

func TestConversation_ListPaginatesWithCursor(t *testing.T) {
	uc, repo := newTestConvUsecase()

	// 建 3 个会话，updated_at 递增。
	base := time.Now()
	for i := 0; i < 3; i++ {
		c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-uint64(i + 2)"}, ConversationTypeGroup, "群")
		if err != nil {
			t.Fatalf("创建失败: %v", err)
		}
		repo.byID[c.ID].UpdatedAt = base.Add(time.Duration(i) * time.Second)
	}

	page1, next, err := uc.ListConversations(context.Background(), "uid-1", 0, 2)
	if err != nil {
		t.Fatalf("第一页失败: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页 len = %d, want 2", len(page1))
	}
	if next == 0 {
		t.Fatal("拿满一页应有 next_cursor")
	}

	page2, next2, err := uc.ListConversations(context.Background(), "uid-1", next, 2)
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("第二页 len = %d, want 1", len(page2))
	}
	if next2 != 0 {
		t.Fatalf("末页 next_cursor 应为 0，实际 %d", next2)
	}

	// 两页之间不能重复。
	seen := map[uint64]bool{}
	for _, c := range append(page1, page2...) {
		if seen[c.ID] {
			t.Fatalf("分页重复返回了会话 %d", c.ID)
		}
		seen[c.ID] = true
	}
}

// TestConversation_ListExactPageSizeHasNoNextCursor 正好取完所有会话时，
// 末页不该再带 next_cursor —— 客户端据此判断"到底了"。
func TestConversation_ListExactPageSizeHasNoNextCursor(t *testing.T) {
	uc, repo := newTestConvUsecase()

	base := time.Now()
	for i := 0; i < 4; i++ {
		c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-uint64(i + 2)"}, ConversationTypeGroup, "群")
		if err != nil {
			t.Fatalf("创建失败: %v", err)
		}
		repo.byID[c.ID].UpdatedAt = base.Add(time.Duration(i) * time.Second)
	}

	// page_size 正好等于总数 → 一页取完，不该有 next_cursor。
	items, next, err := uc.ListConversations(context.Background(), "uid-1", 0, 4)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("len = %d, want 4", len(items))
	}
	if next != 0 {
		t.Fatalf("取完所有会话后 next_cursor 应为 0，实际 %d", next)
	}
}

func TestConversation_ListOnlyReturnsOwnConversations(t *testing.T) {
	uc, _ := newTestConvUsecase()

	if _, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群1"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, _, err := uc.CreateConversation(context.Background(), "uid-3", []string{"uid-4"}, ConversationTypeGroup, "群2"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	items, _, err := uc.ListConversations(context.Background(), "uid-1", 0, 50)
	if err != nil {
		t.Fatalf("列会话失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("用户 1 应只看到 1 个会话，实际 %d", len(items))
	}
	if items[0].Name != "群1" {
		t.Fatalf("看到了别人的会话: %q", items[0].Name)
	}
}

func TestConversation_FillProfilesAttachesNicknames(t *testing.T) {
	repo := newFakeConvRepo()
	fetcher := &fakeConvFetcher{profiles: map[string]*UserProfile{
		"uid-1": {UserID: "uid-1", Nickname: "Alice", Avatar: "a.png"},
		"uid-2": {UserID: "uid-2", Nickname: "Bob", Avatar: "b.png"},
	}}
	uc := NewConversationUsecase(repo, fetcher, nil, nil, nil)

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 建会话时就该带上昵称/头像（不用等列表接口）。
	if len(c.MemberNicknames) != len(c.MemberIDs) {
		t.Fatalf("建会话时昵称数组长度 %d 与成员数 %d 不一致", len(c.MemberNicknames), len(c.MemberIDs))
	}
	if c.MemberNicknames[0] != "Alice" || c.MemberNicknames[1] != "Bob" {
		t.Fatalf("昵称顺序错位: %v", c.MemberNicknames)
	}
	if c.MemberAvatars[0] != "a.png" {
		t.Fatalf("头像错位: %v", c.MemberAvatars)
	}

	items, _, err := uc.ListConversations(context.Background(), "uid-1", 0, 50)
	if err != nil {
		t.Fatalf("列会话失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	got := items[0]
	if len(got.MemberNicknames) != len(got.MemberIDs) {
		t.Fatalf("昵称数组长度 %d 与成员数 %d 不一致", len(got.MemberNicknames), len(got.MemberIDs))
	}
	if got.MemberNicknames[0] != "Alice" || got.MemberNicknames[1] != "Bob" {
		t.Fatalf("昵称顺序错位: %v", got.MemberNicknames)
	}
	if got.MemberAvatars[0] != "a.png" {
		t.Fatalf("头像错位: %v", got.MemberAvatars)
	}
}

// TestConversation_CreateReturnsPersistedTimestamps 建会话返回的会话要带落库后的
// created_at / updated_at —— 客户端要拿它做排序和展示，不能是零值。
func TestConversation_CreateReturnsPersistedTimestamps(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if c.CreatedAt.IsZero() {
		t.Fatal("created_at 不该是零值")
	}
	if c.UpdatedAt.IsZero() {
		t.Fatal("updated_at 不该是零值")
	}
}

func TestConversation_FillProfilesDegradesOnFetcherError(t *testing.T) {
	repo := newFakeConvRepo()
	fetcher := &fakeConvFetcher{err: errors.New("user service down")}
	uc := NewConversationUsecase(repo, fetcher, nil, nil, nil)

	if _, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// user service 不可达不该阻断会话列表 —— 返回会话，昵称留空。
	items, _, err := uc.ListConversations(context.Background(), "uid-1", 0, 50)
	if err != nil {
		t.Fatalf("降级路径不该返错: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	if len(items[0].MemberIDs) != 2 {
		t.Fatalf("成员列表应完整，实际 %v", items[0].MemberIDs)
	}
	// 降级时昵称/头像留空，但数组长度必须仍与 MemberIDs 一致 ——
	// 前端按下标对应成员，长度不齐会串位。
	if len(items[0].MemberNicknames) != len(items[0].MemberIDs) {
		t.Fatalf("降级时昵称数组长度 %d 应等于成员数 %d", len(items[0].MemberNicknames), len(items[0].MemberIDs))
	}
	for _, n := range items[0].MemberNicknames {
		if n != "" {
			t.Fatalf("降级时昵称应留空，实际 %v", items[0].MemberNicknames)
		}
	}
}

func TestConversation_CreateRejectsTooManyMembers(t *testing.T) {
	uc, _ := newTestConvUsecase()

	members := make([]string, 0, MaxGroupMembers+1)
	for i := 1; i <= MaxGroupMembers+1; i++ {
		members = append(members, fmt.Sprintf("uid-%d", i))
	}
	_, _, err := uc.CreateConversation(context.Background(), "uid-1", members, ConversationTypeGroup, "大群")
	if !errors.Is(err, ErrConversationMemberLimit) {
		t.Fatalf("超员应得到 MEMBER_LIMIT，实际 %v", err)
	}
}

// TestConversation_LeaveSingleChatRejected 单聊不支持退群。单聊固定两人，一方退群会留下只有 1 个成员的"单聊" —— 对方列表里的会话
// 缺一个 peer，而且违反"单聊固定 2 人"的不变量。
func TestConversation_LeaveSingleChatRejected(t *testing.T) {
	uc, repo := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	err = uc.LeaveConversation(context.Background(), "uid-1", c.ID)
	if !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("单聊退群应被拒，实际 %v", err)
	}

	// 会话必须完好 —— 两个人都还在。
	got, err := repo.FindByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.MemberCount != 2 {
		t.Fatalf("member_count = %d, want 2（单聊退群不该改变成员数）", got.MemberCount)
	}
}

// TestConversation_TransferOwner 群主转让给群成员。
func TestConversation_TransferOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	updated, err := uc.TransferOwner(context.Background(), "uid-1", c.ID, "uid-2")
	if err != nil {
		t.Fatalf("转让群主失败: %v", err)
	}
	if updated.OwnerID != "uid-2" {
		t.Fatalf("owner_id = %q, want uid-2", updated.OwnerID)
	}
}

// TestConversation_TransferOwnerOnlyOwner 转让群主只有群主能做。
func TestConversation_TransferOwnerOnlyOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = uc.TransferOwner(context.Background(), "uid-2", c.ID, "uid-3")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主转让应被拒，实际 %v", err)
	}
}

// TestConversation_TransferOwnerTargetMustBeMember 新群主必须在会里。
func TestConversation_TransferOwnerTargetMustBeMember(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = uc.TransferOwner(context.Background(), "uid-1", c.ID, "uid-999")
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("转让给非成员应被拒，实际 %v", err)
	}
}

// TestConversation_TransferOwnerToSelfRejected 把群主转给自己没意义。
func TestConversation_TransferOwnerToSelfRejected(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = uc.TransferOwner(context.Background(), "uid-1", c.ID, "uid-1")
	if !errors.Is(err, ErrConversationAlreadyMember) {
		t.Fatalf("转给自己应被拒，实际 %v", err)
	}
}

// TestConversation_TransferOwnerUnlocksLeaving 转让后原群主才能退群。
//
// 这是"群主被困死"的回归测试 —— 转让是唯一逃生口。
func TestConversation_TransferOwnerUnlocksLeaving(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 转让前退群被拒。
	if err := uc.LeaveConversation(context.Background(), "uid-1", c.ID); !errors.Is(err, ErrConversationOwnerCannotLeave) {
		t.Fatalf("群主退群应被拒，实际 %v", err)
	}

	if _, err := uc.TransferOwner(context.Background(), "uid-1", c.ID, "uid-2"); err != nil {
		t.Fatalf("转让群主失败: %v", err)
	}

	// 转让后退群成功。
	if err := uc.LeaveConversation(context.Background(), "uid-1", c.ID); err != nil {
		t.Fatalf("转让后退群应成功，实际 %v", err)
	}
}

// TestConversation_TransferOwnerRejectsSingleChat 单聊没有群主可转。
func TestConversation_TransferOwnerRejectsSingleChat(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = uc.TransferOwner(context.Background(), "uid-1", c.ID, "uid-2")
	if !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("单聊转让应被拒，实际 %v", err)
	}
}

// TestConversation_Dissolve 群主解散群聊，会话与成员记录一起消失。
func TestConversation_Dissolve(t *testing.T) {
	uc, repo := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := uc.DissolveConversation(context.Background(), "uid-1", c.ID); err != nil {
		t.Fatalf("解散失败: %v", err)
	}

	if _, err := repo.FindByID(context.Background(), c.ID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("会话应已删除，实际 %v", err)
	}
}

// TestConversation_DissolveOnlyOwner 解散只有群主能做。
func TestConversation_DissolveOnlyOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := uc.DissolveConversation(context.Background(), "uid-2", c.ID); !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主解散应被拒，实际 %v", err)
	}
}

// TestConversation_DissolveRejectsSingleChat 单聊不支持解散。
func TestConversation_DissolveRejectsSingleChat(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeSingle, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := uc.DissolveConversation(context.Background(), "uid-1", c.ID); !errors.Is(err, ErrConversationNotGroup) {
		t.Fatalf("单聊解散应被拒，实际 %v", err)
	}
}

// TestConversation_ListMembers 列成员，昵称跟 uid 下标对齐。
func TestConversation_ListMembers(t *testing.T) {
	repo := newFakeConvRepo()
	fetcher := &fakeConvFetcher{profiles: map[string]*UserProfile{
		"uid-1": {UserID: "uid-1", Nickname: "Alice", Avatar: "a.png"},
		"uid-2": {UserID: "uid-2", Nickname: "Bob", Avatar: "b.png"},
	}}
	uc := NewConversationUsecase(repo, fetcher, nil, nil, nil)

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	conv, err := uc.ListMembers(context.Background(), "uid-1", c.ID)
	if err != nil {
		t.Fatalf("列成员失败: %v", err)
	}
	if len(conv.MemberIDs) != 2 {
		t.Fatalf("成员数 = %d, want 2", len(conv.MemberIDs))
	}
	if len(conv.MemberNicknames) != len(conv.MemberIDs) {
		t.Fatalf("昵称数组长度 %d 应与成员数 %d 相等", len(conv.MemberNicknames), len(conv.MemberIDs))
	}
}

// TestConversation_ListMembersRejectsNonMember 非成员列成员被拒。
//
// 返的是 NOT_FOUND 而不是 FORBIDDEN —— 跟 GetConversation 一致，
// 不泄露「会话存在但你不是成员」。同一个事实两个接口给不同状态码，
// 客户端没法写统一逻辑。
func TestConversation_ListMembersRejectsNonMember(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = uc.ListMembers(context.Background(), "uid-999", c.ID)
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("非成员列成员应被拒，实际 %v", err)
	}
}

// TestConversation_AddMembersRejectsUnknownUser 加不存在的用户被拒。
func TestConversation_AddMembersRejectsUnknownUser(t *testing.T) {
	repo := newFakeConvRepo()
	existence := &fakeExistenceChecker{existing: map[string]struct{}{"uid-1": {}, "uid-2": {}}}
	uc := NewConversationUsecase(repo, nil, existence, nil, nil)

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, _, err = uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-999"})
	if !errors.Is(err, ErrConversationUserNotFound) {
		t.Fatalf("加不存在的用户应被拒，实际 %v", err)
	}
}

// TestConversation_AddMembersAllowsWhenCheckerDown user 服务不可达时放行。
//
// 挡不住正常业务比放进幽灵成员更糟。
func TestConversation_AddMembersAllowsWhenCheckerDown(t *testing.T) {
	repo := newFakeConvRepo()
	existence := &fakeExistenceChecker{err: errors.New("user service down")}
	uc := NewConversationUsecase(repo, nil, existence, nil, nil)

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	count, added, err := uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-999"})
	if err != nil {
		t.Fatalf("校验服务不可达时不该报错，实际 %v", err)
	}
	if added != 1 || count != 3 {
		t.Fatalf("added = %d, count = %d, want 1 / 3", added, count)
	}
}

// TestConversation_RemoveMemberReturnsRepoCount 踢人后的成员数取 repo 的值。
//
// 不该由调用方拿旧快照自己减 —— 并发踢人时那个数会错。
func TestConversation_RemoveMemberReturnsRepoCount(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3", "uid-4"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	count, err := uc.RemoveMember(context.Background(), "uid-1", c.ID, "uid-2")
	if err != nil {
		t.Fatalf("踢人失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("member_count = %d, want 3", count)
	}
}

// TestConversation_AddMembersReturnsRepoCount 加人后的成员数取 repo 的值。
func TestConversation_AddMembersReturnsRepoCount(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	count, added, err := uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-3", "uid-4"})
	if err != nil {
		t.Fatalf("加人失败: %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	if count != 4 {
		t.Fatalf("member_count = %d, want 4", count)
	}
}

// TestConversation_UpdateNameNonOwnerGetsNotOwner 非群主改群名报 NOT_OWNER。
//
// 不是 INVALID_ARGUMENT —— 参数校验不该抢在权限校验前面。
func TestConversation_UpdateNameNonOwnerGetsNotOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 名字合法，但调用方不是群主。
	_, err = uc.UpdateConversation(context.Background(), "uid-2", c.ID, "新名")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主改名应得 NOT_OWNER，实际 %v", err)
	}

	// 名字非法，且调用方不是群主 —— 仍应是 NOT_OWNER。
	_, err = uc.UpdateConversation(context.Background(), "uid-2", c.ID, "")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主改名应得 NOT_OWNER，实际 %v", err)
	}
}

// TestConversation_RemoveMemberNonOwnerGetsNotOwner 非群主踢人报 NOT_OWNER。
func TestConversation_RemoveMemberNonOwnerGetsNotOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 非群主踢自己 —— 报 NOT_OWNER，不是 INVALID_ARGUMENT。
	_, err = uc.RemoveMember(context.Background(), "uid-2", c.ID, "uid-2")
	if !errors.Is(err, ErrConversationNotOwner) {
		t.Fatalf("非群主踢人应得 NOT_OWNER，实际 %v", err)
	}
}

// TestConversation_AddMembersAllAlreadyMemberReturnsFreshCount 要加的人全在会里时，
// 成员数取 repo 的事务内计数，不是 AddMembers 之前 FindByID 读到的快照。
//
// 两条分支的数必须同一个口径 —— 否则并发下会给出不一致的 member_count。
func TestConversation_AddMembersAllAlreadyMemberReturnsFreshCount(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2", "uid-3"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 2、3 已经在会里了 —— 加人后 added=0，但 member_count 仍要是真实值。
	count, added, err := uc.AddMembers(context.Background(), "uid-1", c.ID, []string{"uid-2", "uid-3"})
	if err != nil {
		t.Fatalf("加已存在成员应成功: %v", err)
	}
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if count != 3 {
		t.Fatalf("member_count = %d, want 3", count)
	}
}

// TestConversation_RemoveMemberKeepsOwner 群主 + 1 个成员时踢掉那个人是合法的 ——
// 会话还剩群主，不算空。
//
// 回归测试：守卫曾经写成 count<=2，把这种最常见的情况也拦了。
func TestConversation_RemoveMemberKeepsOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if c.MemberCount != 2 {
		t.Fatalf("初始 member_count = %d, want 2", c.MemberCount)
	}

	count, err := uc.RemoveMember(context.Background(), "uid-1", c.ID, "uid-2")
	if err != nil {
		t.Fatalf("踢人失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("member_count = %d, want 1", count)
	}
}

// TestConversation_LeaveConversationKeepsOwner 群主 + 1 个成员时，非群主退群是合法的 ——
// 会话还剩群主。
func TestConversation_LeaveConversationKeepsOwner(t *testing.T) {
	uc, _ := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := uc.LeaveConversation(context.Background(), "uid-2", c.ID); err != nil {
		t.Fatalf("退群失败: %v", err)
	}

	got, err := uc.GetConversation(context.Background(), "uid-1", c.ID)
	if err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	if got.MemberCount != 1 {
		t.Fatalf("member_count = %d, want 1", got.MemberCount)
	}
}

// ClearUnread 把该成员的会话级未读数清零。
func TestConversation_ClearUnread(t *testing.T) {
	uc, repo := newTestConvUsecase()

	c, _, err := uc.CreateConversation(context.Background(), "uid-1", []string{"uid-2"}, ConversationTypeGroup, "群")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := uc.UpdateUnread(context.Background(), c.ID, "uid-2", 5); err != nil {
		t.Fatalf("加未读失败: %v", err)
	}

	n, err := uc.ClearUnread(context.Background(), c.ID, "uid-2")
	if err != nil {
		t.Fatalf("清零失败: %v", err)
	}
	if n != 0 {
		t.Errorf("清零后 = %d, want 0", n)
	}
	if got := repo.unread[unreadKey(c.ID, "uid-2")]; got != 0 {
		t.Errorf("成员 uid-2 的未读数 = %d, want 0", got)
	}
}

// 清零不存在的会话返「不存在」。
func TestConversation_ClearUnreadNotFound(t *testing.T) {
	uc, _ := newTestConvUsecase()
	if _, err := uc.ClearUnread(context.Background(), 999, "uid-1"); err == nil {
		t.Fatal("清零不存在的会话应该返错")
	}
}

// 参数非法时不该打到 repo。
func TestConversation_ClearUnreadInvalidArgument(t *testing.T) {
	uc, _ := newTestConvUsecase()
	if _, err := uc.ClearUnread(context.Background(), 0, "uid-1"); err == nil {
		t.Fatal("conversation_id=0 应该返错")
	}
	if _, err := uc.ClearUnread(context.Background(), 1, "uid-0"); err == nil {
		t.Fatal("user_id=0 应该返错")
	}
}
