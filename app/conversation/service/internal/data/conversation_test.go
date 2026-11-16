package data

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"chatroom/app/conversation/service/internal/biz"
	"chatroom/app/conversation/service/internal/data/ent/conversation"
	"chatroom/app/conversation/service/internal/data/ent/conversationmember"
)

// newTestConvRepo 构造接真实 MySQL 的 conversationRepo。
// 返回 biz.ConversationRepo 接口，与 wire 里注入的类型一致。
func newTestConvRepo(t *testing.T) (*Data, biz.ConversationRepo) {
	t.Helper()
	d, _ := newTestDB(t)
	return d, NewConversationRepo(d)
}

// TestConversationRepo_SaveAndFind 覆盖会话 + 成员的写入与读回。
func TestConversationRepo_SaveAndFind(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	conv := &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"},
	}
	saved, reused, err := repo.Save(ctx, conv)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if reused {
		t.Fatal("首次保存不该是复用")
	}
	if saved == nil || saved.ID == 0 {
		t.Fatal("Save 应返回带 id 的会话")
	}
	// 返回的会话要带落库后的时间戳 —— 调用方直接拿去当响应，不再查库。
	if saved.CreatedAt.IsZero() {
		t.Fatal("created_at 不该是零值")
	}
	if saved.UpdatedAt.IsZero() {
		t.Fatal("updated_at 不该是零值")
	}

	got, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Type != biz.ConversationTypeGroup {
		t.Fatalf("type = %v, want group", got.Type)
	}
	if got.Name != "群" {
		t.Fatalf("name = %q, want 群", got.Name)
	}
	if got.OwnerID != "uid-1" {
		t.Fatalf("owner_id = %q, want uid-1", got.OwnerID)
	}
	if got.MemberCount != 3 {
		t.Fatalf("member_count = %d, want 3", got.MemberCount)
	}
	// 成员列表按 user_id 升序。
	if len(got.MemberIDs) != 3 {
		t.Fatalf("member_ids = %v, want 3 个", got.MemberIDs)
	}
	for i := 1; i < len(got.MemberIDs); i++ {
		if got.MemberIDs[i-1] >= got.MemberIDs[i] {
			t.Fatalf("成员未升序: %v", got.MemberIDs)
		}
	}

	// 找不到的会话 → ErrConversationNotFound。
	if _, err := repo.FindByID(ctx, 999999); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
	_ = d
}

// TestConversationRepo_SaveSingleChatIsIdempotent 覆盖单聊幂等：
// 同一对成员重复 Save 复用同一个会话。
func TestConversationRepo_SaveSingleChatIsIdempotent(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	conv := &biz.Conversation{
		Type:      biz.ConversationTypeSingle,
		MemberIDs: []string{"uid-1", "uid-2"},
	}
	saved1, reused1, err := repo.Save(ctx, conv)
	if err != nil {
		t.Fatalf("第一次 Save: %v", err)
	}
	if reused1 {
		t.Fatal("首次保存不该是复用")
	}

	// 反向成员顺序（2,1）——归一化后是同一对。
	conv2 := &biz.Conversation{
		Type:      biz.ConversationTypeSingle,
		MemberIDs: []string{"uid-2", "uid-1"},
	}
	saved2, reused2, err := repo.Save(ctx, conv2)
	if err != nil {
		t.Fatalf("第二次 Save: %v", err)
	}
	if !reused2 {
		t.Fatal("第二次保存应复用")
	}
	if saved1.ID != saved2.ID {
		t.Fatalf("id 不一致: %d vs %d", saved1.ID, saved2.ID)
	}

	// 反查也只应有一条。
	found, err := repo.FindSingleByMembers(ctx, "uid-1", "uid-2")
	if err != nil {
		t.Fatalf("FindSingleByMembers: %v", err)
	}
	if found.ID != saved1.ID {
		t.Fatalf("FindSingleByMembers id = %d, want %d", found.ID, saved1.ID)
	}
	_ = d
}

// TestConversationRepo_FindSingleByMembersNotFound 不存在时返 ErrConversationNotFound。
func TestConversationRepo_FindSingleByMembersNotFound(t *testing.T) {
	d, repo := newTestConvRepo(t)

	if _, err := repo.FindSingleByMembers(context.Background(), "uid-1", "uid-2"); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
	_ = d
}

// TestConversationRepo_IsMember 覆盖成员判定。
func TestConversationRepo_IsMember(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, tc := range []struct {
		uid  string
		want bool
	}{{"uid-1", true}, {"uid-2", true}, {"uid-3", false}} {
		got, err := repo.IsMember(ctx, saved.ID, tc.uid)
		if err != nil {
			t.Fatalf("IsMember(%s): %v", tc.uid, err)
		}
		if got != tc.want {
			t.Fatalf("IsMember(%s) = %v, want %v", tc.uid, got, tc.want)
		}
	}
	_ = d
}

// TestConversationRepo_AddMembersSkipsExisting 覆盖加成员的幂等：
// 已在会里的用户跳过，added 只算真正新增的。
func TestConversationRepo_AddMembersSkipsExisting(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	added, total, err := repo.AddMembers(ctx, saved.ID, []string{"uid-2", "uid-3"})
	if err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1（2 已在会里）", added)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}

	got, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.MemberCount != 3 {
		t.Fatalf("member_count = %d, want 3", got.MemberCount)
	}
	_ = d
}

// TestConversationRepo_RemoveMember 覆盖踢人。
func TestConversationRepo_RemoveMember(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	total, err := repo.RemoveMember(ctx, saved.ID, "uid-3")
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}

	got, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.MemberCount != 2 {
		t.Fatalf("member_count = %d, want 2", got.MemberCount)
	}
	for _, m := range got.MemberIDs {
		if m == "uid-3" {
			t.Fatal("成员 uid-3 应已被踢")
		}
	}

	// 踢不存在的人 → ErrConversationNotFound。
	if _, err := repo.RemoveMember(ctx, saved.ID, "uid-999"); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
	_ = d
}

// TestConversationRepo_UpdateGroupInfo 覆盖改群名和群头像。
func TestConversationRepo_UpdateGroupInfo(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "旧名",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.UpdateGroupInfo(ctx, saved.ID, "新名", "")
	if err != nil {
		t.Fatalf("UpdateGroupInfo: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("name = %q, want 新名", got.Name)
	}

	// 只改头像、名字留空 —— 名字要保持不变。
	got, err = repo.UpdateGroupInfo(ctx, saved.ID, "", "https://cdn.example.com/group_avatar/1/a.jpg")
	if err != nil {
		t.Fatalf("UpdateGroupInfo avatar: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("name = %q, want 新名（未改名字时不能被清掉）", got.Name)
	}
	if got.Avatar != "https://cdn.example.com/group_avatar/1/a.jpg" {
		t.Fatalf("avatar = %q", got.Avatar)
	}

	if _, err := repo.UpdateGroupInfo(ctx, 999999, "x", ""); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
	_ = d
}

// TestConversationRepo_ListByUserIDPaginates 覆盖列表分页。复合游标谓词是 (updated_at < c.updated_at) OR
// (updated_at = c.updated_at AND saved.ID < c.id) —— 这里验证跨页不重不漏。
func TestConversationRepo_ListByUserIDPaginates(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, _, err := repo.Save(ctx, &biz.Conversation{
			Type:      biz.ConversationTypeGroup,
			Name:      "群",
			OwnerID:   "uid-1",
			MemberIDs: []string{"uid-1", "uid-uint64(i + 2)"},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// 第一页：2 条。
	page1, err := repo.ListByUserID(ctx, "uid-1", biz.ConversationCursor{}, 2)
	if err != nil {
		t.Fatalf("第一页: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页 len = %d, want 2", len(page1))
	}

	// 第二页：用第一页末条的 id 作游标。
	cursor := biz.ConversationCursor{ID: page1[len(page1)-1].ID}
	page2, err := repo.ListByUserID(ctx, "uid-1", cursor, 2)
	if err != nil {
		t.Fatalf("第二页: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页 len = %d, want 2", len(page2))
	}

	// 第三页：剩 1 条。
	cursor2 := biz.ConversationCursor{ID: page2[len(page2)-1].ID}
	page3, err := repo.ListByUserID(ctx, "uid-1", cursor2, 2)
	if err != nil {
		t.Fatalf("第三页: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("第三页 len = %d, want 1", len(page3))
	}

	// 三页合起来 5 条，不重不漏。
	seen := map[uint64]bool{}
	for _, c := range append(append(page1, page2...), page3...) {
		if seen[c.ID] {
			t.Fatalf("分页重复返回会话 %d", c.ID)
		}
		seen[c.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("共看到 %d 个会话，want 5", len(seen))
	}
	_ = d
}

// TestConversationRepo_ListByUserIDOnlyOwnConversations 别人的会话不该出现在列表里。
func TestConversationRepo_ListByUserIDOnlyOwnConversations(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	if _, _, err := repo.Save(ctx, &biz.Conversation{
		Type: biz.ConversationTypeGroup, Name: "群1", OwnerID: "uid-1", MemberIDs: []string{"uid-1", "uid-2"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, _, err := repo.Save(ctx, &biz.Conversation{
		Type: biz.ConversationTypeGroup, Name: "群2", OwnerID: "uid-3", MemberIDs: []string{"uid-3", "uid-4"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.ListByUserID(ctx, "uid-1", biz.ConversationCursor{}, 50)
	if err != nil {
		t.Fatalf("ListByUserID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Name != "群1" {
		t.Fatalf("看到了别人的会话: %q", got[0].Name)
	}
	_ = d
}

// TestConversationRepo_TouchConversationUpdatesUpdatedAt 覆盖活跃时间更新。
func TestConversationRepo_TouchConversationUpdatesUpdatedAt(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type: biz.ConversationTypeGroup, Name: "群", OwnerID: "uid-1", MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}

	if err := repo.TouchConversation(ctx, saved.ID); err != nil {
		t.Fatalf("TouchConversation: %v", err)
	}
	after, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at 未推进: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
	_ = d
}

// TestConversationRepo_ListWithDeletedCursorDoesNotStall 游标指向的会话被删后，
// 分页应该能继续（退化成只按 saved.ID 往后翻），而不是报错卡住。
func TestConversationRepo_ListWithDeletedCursorDoesNotStall(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	var ids []uint64
	for i := 0; i < 3; i++ {
		saved, _, err := repo.Save(ctx, &biz.Conversation{
			Type: biz.ConversationTypeGroup, Name: "群", OwnerID: "uid-1", MemberIDs: []string{"uid-1", "uid-uint64(i + 2)"},
		})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		ids = append(ids, saved.ID)
	}

	// 删掉游标指向的那一行。先删成员 —— 外键是 NO ACTION，
	// 有成员时删不掉会话。
	if _, err := d.db.ConversationMember.Delete().Where(conversationmember.ConversationIDEQ(ids[0])).Exec(ctx); err != nil {
		t.Fatalf("delete members: %v", err)
	}
	if err := d.db.Conversation.DeleteOneID(ids[0]).Exec(ctx); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}

	// 游标指向已删除的会话 → 反查不到，退化成只按 saved.ID 继续。
	// saved.ID 1、2 还在，会话 2、3 的 saved.ID 比游标大 → 不该返回。
	got, err := repo.ListByUserID(ctx, "uid-1", biz.ConversationCursor{ID: ids[0]}, 10)
	if err != nil {
		t.Fatalf("ListByUserID: %v", err)
	}
	for _, c := range got {
		if c.ID == ids[0] {
			t.Fatal("已删除的会话不该返回")
		}
	}
	// 退化成 saved.ID 比较后，saved.ID 大于游标的会话会漏掉 —— 这是降级的已知代价，
	// 但至少不能报错、不能卡住分页。这里断言不报错即可。
	_ = d
}

// TestConversationRepo_SingleChatUniqueConstraint 单聊唯一约束真的在数据库上。并发建同一对成员的单聊时，事务外的预查挡不住 —— 真正的仲裁是这个唯一
// 索引。本测试直接绕开 repo 往库里插一条重复 pair_key，验证数据库会拒。索引约束的是**归一化后**的键（"uid-1,uid-2"）—— 归一化由 singlePairKey 负责，
// 仓库里只有一个写入点且它总是调用 singlePairKey。这条测试同时盯住这个前提。
func TestConversationRepo_SingleChatUniqueConstraint(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeSingle,
		MemberIDs: []string{"uid-2", "uid-1"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 成员顺序反过来也要落到同一个归一化键 —— 幂等查询才对得上，
	// 唯一索引也才拦得住反向并发。
	po, err := d.db.Conversation.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if po.PairKey == nil {
		t.Fatal("单聊的 pair_key 不该是 NULL")
	}
	if *po.PairKey != "uid-1,uid-2" {
		t.Fatalf("pair_key = %q, want \"uid-1,uid-2\"（两个 uid 应升序拼接）", *po.PairKey)
	}

	// 直接插一条归一化后相同的行 —— 唯一索引应该拒掉。
	_, err = d.db.Conversation.Create().
		SetType(uint8(biz.ConversationTypeSingle)).
		SetName("").
		SetOwnerID("").
		SetPairKey("uid-1,uid-2").
		Save(ctx)
	if err == nil {
		t.Fatal("重复 pair_key 插入应被唯一索引拒绝")
	}
}

// TestConversationRepo_GroupsShareNullPairKey 多个群聊的 pair_key 都是 NULL。
// MySQL 唯一索引允许存在多个 NULL。
func TestConversationRepo_GroupsShareNullPairKey(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		saved, _, err := repo.Save(ctx, &biz.Conversation{
			Type:      biz.ConversationTypeGroup,
			Name:      "群",
			OwnerID:   "uid-1",
			MemberIDs: []string{"uid-1", "uid-2"},
		})
		if err != nil {
			t.Fatalf("第 %d 个群建会话失败: %v", i, err)
		}
		if saved.ID == 0 {
			t.Fatal("会话应有 id")
		}
	}

	// 三个群聊的 pair_key 都是 NULL。
	n, err := d.db.Conversation.Query().Where(conversation.PairKeyIsNil()).Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("pair_key 为 NULL 的会话数 = %d, want 3", n)
	}
}

// TestConversationRepo_TransferOwner 转让群主。
func TestConversationRepo_TransferOwner(t *testing.T) {
	_, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	updated, err := repo.TransferOwner(ctx, saved.ID, "uid-2")
	if err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}
	if updated.OwnerID != "uid-2" {
		t.Fatalf("owner_id = %q, want uid-2", updated.OwnerID)
	}

	// 新群主不在会里 → ErrConversationNotFound。
	if _, err := repo.TransferOwner(ctx, saved.ID, "uid-999"); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
}

// TestConversationRepo_Dissolve 解散会话：会话和成员记录一起没。
func TestConversationRepo_Dissolve(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := repo.Dissolve(ctx, saved.ID); err != nil {
		t.Fatalf("Dissolve: %v", err)
	}

	if _, err := repo.FindByID(ctx, saved.ID); !errors.Is(err, biz.ErrConversationNotFound) {
		t.Fatalf("会话应已删除，实际 %v", err)
	}

	// 成员记录也要清干净 —— 否则外键还挂着。
	n, err := d.db.ConversationMember.Query().
		Where(conversationmember.ConversationIDEQ(saved.ID)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count members: %v", err)
	}
	if n != 0 {
		t.Fatalf("残留成员记录 %d 条, want 0", n)
	}
}

// TestConversationRepo_ConcurrentSingleChatCreatesOneConversation 并发建同一对
// 成员的单聊，只能有一个会话。事务外的预查挡不住并发 —— 两个请求可能都查不到，然后都去插。真正的仲裁
// 是 pair_key 的唯一索引：后到的一方插入失败，回查并复用已有会话。
func TestConversationRepo_ConcurrentSingleChatCreatesOneConversation(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	const workers = 8
	var wg sync.WaitGroup
	ids := make([]uint64, workers)
	errs := make([]error, workers)

	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // 尽量让所有 goroutine 同时进 Save
			saved, _, err := repo.Save(ctx, &biz.Conversation{
				Type:      biz.ConversationTypeSingle,
				MemberIDs: []string{"uid-1", "uid-2"},
			})
			if err != nil {
				errs[idx] = err
				return
			}
			ids[idx] = saved.ID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d 报错: %v", i, err)
		}
	}
	// 所有 worker 必须拿到同一个会话 id。
	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("worker %d 拿到会话 %d，其他拿到 %d —— 并发建出了多个单聊", i, id, ids[0])
		}
	}

	// 库里也只能有一个单聊。
	n, err := d.db.Conversation.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("库里会话数 = %d, want 1", n)
	}

	// 成员记录也不能重复 —— 失败的 worker 回滚了事务。
	m, err := d.db.ConversationMember.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count members: %v", err)
	}
	if m != 2 {
		t.Fatalf("成员记录数 = %d, want 2", m)
	}
}

// TestConversationRepo_AddMembersEnforcesLimit repo 层也兜人数上限。biz 层已经查过上限，但 repo 是唯一能数到"当前真实成员数"的地方 ——
// 并发加群时 biz 的快照可能是旧的。
func TestConversationRepo_AddMembersEnforcesLimit(t *testing.T) {
	_, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 直接给 repo 一个超量的请求 —— 绕开 biz 层。
	members := make([]string, 0, biz.MaxGroupMembers)
	for i := 3; i <= biz.MaxGroupMembers+1; i++ {
		members = append(members, fmt.Sprintf("uid-%d", i))
	}

	if _, _, err := repo.AddMembers(ctx, saved.ID, members); !errors.Is(err, biz.ErrConversationMemberLimit) {
		t.Fatalf("error = %v, want ErrConversationMemberLimit", err)
	}

	// 上限内不该受影响。
	_, total, err := repo.AddMembers(ctx, saved.ID, []string{"uid-3", "uid-4", "uid-5"})
	if err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
}

// TestConversationRepo_AddMembersEmptyListStillCounts 传空列表时也要返回真实成员数。
func TestConversationRepo_AddMembersEmptyListStillCounts(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2", "uid-3"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	added, total, err := repo.AddMembers(ctx, saved.ID, nil)
	if err != nil {
		t.Fatalf("AddMembers(空): %v", err)
	}
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	_ = d
}

// TestConversationRepo_RemoveMemberKeepsGroupNonEmpty 踢人不能把会话踢成空壳。回归测试：repo 层原来没有下界守卫，理论上能删掉最后一个成员行，
// 留下 0 个成员、没有 owner 的孤儿会话。现在守卫在事务内。
func TestConversationRepo_RemoveMemberKeepsGroupNonEmpty(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 踢掉 2 —— 还剩群主 1，合法。
	total, err := repo.RemoveMember(ctx, saved.ID, "uid-2")
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}

	// 再踢群主 —— 踢完会话就空了，repo 必须拦下。
	if _, err := repo.RemoveMember(ctx, saved.ID, "uid-1"); err == nil {
		t.Fatal("踢掉最后一个成员应被拦下")
	}

	got, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.MemberCount != 1 {
		t.Fatalf("member_count = %d, want 1（最后一人不该被踢掉）", got.MemberCount)
	}
	_ = d
}

// TestConversationRepo_SaveSortsMemberIDs Save 落库的成员列表是升序。
// DO 约定 MemberIDs / MemberNicknames / MemberAvatars 按下标对齐，
// 顺序必须确定。repo 是公开接口，不能假设调用方都走过 biz 的 normalizeMembers。
func TestConversationRepo_SaveSortsMemberIDs(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()

	// 故意乱序 —— repo 应该自己排。
	saved, _, err := repo.Save(ctx, &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-30", "uid-10", "uid-20"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	want := []string{"uid-10", "uid-20", "uid-30"}
	for i, id := range saved.MemberIDs {
		if id != want[i] {
			t.Fatalf("MemberIDs = %v, want %v", saved.MemberIDs, want)
		}
	}

	got, err := repo.FindByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	for i, id := range got.MemberIDs {
		if id != want[i] {
			t.Fatalf("落库后 MemberIDs = %v, want %v", got.MemberIDs, want)
		}
	}
	_ = d
}

// newUnreadTestConv 建一个带成员的会话，返回会话 id。
func newUnreadTestConv(t *testing.T, d *Data, repo biz.ConversationRepo) uint64 {
	t.Helper()
	ctx := context.Background()
	conv := &biz.Conversation{
		Type:      biz.ConversationTypeGroup,
		Name:      "群",
		OwnerID:   "uid-1",
		MemberIDs: []string{"uid-1", "uid-2"},
	}
	saved, _, err := repo.Save(ctx, conv)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return saved.ID
}

// 未读数能加减，且不会减到 0 以下。
func TestConversationRepo_UpdateUnread(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	n, err := repo.UpdateUnread(ctx, convID, "uid-2", 3)
	if err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}
	if n != 3 {
		t.Errorf("未读数 = %d, want 3", n)
	}

	n, err = repo.UpdateUnread(ctx, convID, "uid-2", -1)
	if err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}
	if n != 2 {
		t.Errorf("未读数 = %d, want 2", n)
	}

	// 减过头了应该停在 0。
	n, err = repo.UpdateUnread(ctx, convID, "uid-2", -100)
	if err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}
	if n != 0 {
		t.Errorf("未读数 = %d, want 0", n)
	}
}

// ClearUnread 一条 UPDATE 清零。
func TestConversationRepo_ClearUnread(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.UpdateUnread(ctx, convID, "uid-2", 7); err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}

	n, err := repo.ClearUnread(ctx, convID, "uid-2")
	if err != nil {
		t.Fatalf("ClearUnread: %v", err)
	}
	if n != 0 {
		t.Errorf("清零后 = %d, want 0", n)
	}

	got, err := repo.GetUnread(ctx, convID, "uid-2")
	if err != nil {
		t.Fatalf("GetUnread: %v", err)
	}
	if got != 0 {
		t.Errorf("读回来 = %d, want 0", got)
	}
}

// 清零不该影响同会话的其它成员。
func TestConversationRepo_ClearUnreadOnlyAffectsTarget(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.UpdateUnread(ctx, convID, "uid-1", 4); err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}
	if _, err := repo.UpdateUnread(ctx, convID, "uid-2", 9); err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}

	if _, err := repo.ClearUnread(ctx, convID, "uid-2"); err != nil {
		t.Fatalf("ClearUnread: %v", err)
	}

	got, err := repo.GetUnread(ctx, convID, "uid-1")
	if err != nil {
		t.Fatalf("GetUnread: %v", err)
	}
	if got != 4 {
		t.Errorf("成员 1 的未读数 = %d, want 4（不该被影响）", got)
	}
}

// 清零不存在的成员返「不存在」。
func TestConversationRepo_ClearUnreadNotFound(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.ClearUnread(ctx, convID, "uid-999"); err == nil {
		t.Fatal("清零不存在的成员应该返错")
	}
}
