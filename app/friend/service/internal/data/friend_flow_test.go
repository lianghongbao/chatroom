package data

import (
	"context"
	"testing"
	"time"

	userv1 "chatroom/api/user/service/v1"
	"chatroom/app/friend/service/internal/biz"
)

// fakeRemoteUserClient 是 RemoteUserClient 的内存替身。它站在**跨服务边界**上 —— 真实链路是 friend 服务经 nacos discovery
// 调 user 服务的 gRPC 端点 GetFriendProfiles。这里换成同进程调用，
// 跳过传输层，但保留「friend biz 拿到的是好友的昵称/头像」这个契约。
type fakeRemoteUserClient struct {
	// users uid → 用户资料。
	users map[string]*userv1.UserInfo
	// err 返回错误（模拟 user 服务不可达）。
	err error
}

func newFakeRemoteUserClient() *fakeRemoteUserClient {
	return &fakeRemoteUserClient{users: map[string]*userv1.UserInfo{}}
}

// put 登记一个用户。
func (c *fakeRemoteUserClient) put(id string, nickname string) {
	c.users[id] = &userv1.UserInfo{Id: id, Nickname: nickname}
}

func (c *fakeRemoteUserClient) GetUserByID(ctx context.Context, callerID, targetID string) (*userv1.UserInfo, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.users[targetID], nil
}

func (c *fakeRemoteUserClient) GetFriendProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := make([]*userv1.UserInfo, 0, len(ids))
	for _, id := range ids {
		if u, ok := c.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (c *fakeRemoteUserClient) GetConversationMemberProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error) {
	return c.GetFriendProfiles(ctx, ids)
}

// 确保 fakeRemoteUserClient 满足 RemoteUserClient —— 编译期检查。
var _ RemoteUserClient = (*fakeRemoteUserClient)(nil)

// TestFriendFlow_ApplyAcceptThenBothSeeEachOther 覆盖 PRD 验收标准
// 「两个用户能注册、登录、加好友」里 friend 域的部分。全链路：A 申请 B → B 收件箱看到申请 → B 接受 → 双方好友列表都能看到
// 对方，且昵称/头像已跨服务补全。走的是**生产路径**：biz.FriendUsecase → data.Adapter → data.RemoteUserClient
// （这里用 fake 替身）→ proto → DO。
func TestFriendFlow_ApplyAcceptThenBothSeeEachOther(t *testing.T) {
	d, _ := newTestDB(t)
	ctx := context.Background()

	// 两个用户。真实链路里他们是 user 服务里的两行记录；
	// 这里只在 fakeRemoteUserClient 里登记资料（friend 域只关心 uid + 资料）。
	const aliceID, bobID = "alice", "bob"
	cli := newFakeRemoteUserClient()
	cli.put(aliceID, "Alice")
	cli.put(bobID, "Bob")

	// 生产路径的 adapter：client → biz.FriendProfile。
	adapter := NewFriendAdapter(cli)
	uc := biz.NewFriendUsecase(
		NewFriendRepo(d),
		adapter,
		nil, // FriendLimiter: nil 关闭
		nil, // logger
	)

	// 1. A 申请 B。
	applyRes, err := uc.Apply(ctx, aliceID, bobID, "hi")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applyRes.Status != biz.FriendStatusPending {
		t.Fatalf("apply status = %v, want PENDING", applyRes.Status)
	}

	// 2. B 收件箱能看到这条申请，且 friend_id 指向申请发起方 A。
	pending, _, err := uc.ListPendingRequests(ctx, bobID, 0, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending len = %d, want 1", len(pending))
	}
	if pending[0].FriendID != aliceID {
		t.Fatalf("pending friend_id = %q, want %q (the applicant)", pending[0].FriendID, aliceID)
	}

	// 3. B 接受。Accept 返回的 friend_id 必须是「对方」(A)，不是 caller 自己(B)。
	accepted, err := uc.Accept(ctx, bobID, applyRes.RequestID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.FriendID != aliceID {
		t.Fatalf("accept returned friend_id = %q, want %q (the peer, not the caller)", accepted.FriendID, aliceID)
	}

	// 4. 双方好友列表都能看到对方。
	listA, _, err := uc.List(ctx, aliceID, 0, 10)
	if err != nil {
		t.Fatalf("list alice: %v", err)
	}
	listB, _, err := uc.List(ctx, bobID, 0, 10)
	if err != nil {
		t.Fatalf("list bob: %v", err)
	}
	if len(listA) != 1 {
		t.Fatalf("alice friend list len = %d, want 1", len(listA))
	}
	if len(listB) != 1 {
		t.Fatalf("bob friend list len = %d, want 1", len(listB))
	}
	if listA[0].FriendID != bobID {
		t.Fatalf("alice list friend_id = %q, want %q", listA[0].FriendID, bobID)
	}
	if listB[0].FriendID != aliceID {
		t.Fatalf("bob list friend_id = %q, want %q", listB[0].FriendID, aliceID)
	}

	// 5. 昵称/头像已跨服务补全 —— 这一步覆盖 is_visible_to_stranger 与
	// 好友场景的冲突：双方默认对陌生人不可见，但好友列表必须拿到昵称。
	if listA[0].Profile == nil {
		t.Fatal("alice list: profile not filled")
	}
	if listA[0].Profile.Nickname != "Bob" {
		t.Fatalf("alice sees nickname %q, want %q", listA[0].Profile.Nickname, "Bob")
	}
	if listB[0].Profile == nil {
		t.Fatal("bob list: profile not filled")
	}
	if listB[0].Profile.Nickname != "Alice" {
		t.Fatalf("bob sees nickname %q, want %q", listB[0].Profile.Nickname, "Alice")
	}
}

// TestFriendFlow_UserServiceDownDegradesGracefully 覆盖
// 「user 服务不可达时好友列表降级 —— 关系还在，昵称留空」。
//
// 降级不能让主路径挂掉 —— 拉资料失败只该让昵称/头像空着。
func TestFriendFlow_UserServiceDownDegradesGracefully(t *testing.T) {
	d, _ := newTestDB(t)
	ctx := context.Background()

	const aliceID, bobID = "alice", "bob"
	cli := newFakeRemoteUserClient()
	cli.put(aliceID, "Alice")
	cli.put(bobID, "Bob")

	uc := biz.NewFriendUsecase(NewFriendRepo(d), NewFriendAdapter(cli), nil, nil)

	applyRes, err := uc.Apply(ctx, aliceID, bobID, "hi")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := uc.Accept(ctx, bobID, applyRes.RequestID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// user 服务挂了。
	cli.err = context.DeadlineExceeded

	list, _, err := uc.List(ctx, aliceID, 0, 10)
	if err != nil {
		t.Fatalf("list must degrade, not fail: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("alice friend list len = %d, want 1 (relation survives)", len(list))
	}
	if list[0].Profile != nil {
		t.Fatal("profile must be nil when the profile fetch fails")
	}
}

// TestFriendFlow_RemoveFriendBothDirections 覆盖「任意一方都能删好友」。
func TestFriendFlow_RemoveFriendBothDirections(t *testing.T) {
	d, _ := newTestDB(t)
	ctx := context.Background()

	const aliceID, bobID = "alice", "bob"
	cli := newFakeRemoteUserClient()
	cli.put(aliceID, "Alice")
	cli.put(bobID, "Bob")

	uc := biz.NewFriendUsecase(NewFriendRepo(d), NewFriendAdapter(cli), nil, nil)

	applyRes, err := uc.Apply(ctx, aliceID, bobID, "hi")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := uc.Accept(ctx, bobID, applyRes.RequestID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// 由接收方 B 删除。
	if err := uc.Remove(ctx, bobID, aliceID); err != nil {
		t.Fatalf("remove by receiver: %v", err)
	}
	listA, _, err := uc.List(ctx, aliceID, 0, 10)
	if err != nil {
		t.Fatalf("list alice: %v", err)
	}
	if len(listA) != 0 {
		t.Fatalf("alice friend list len = %d, want 0 after remove", len(listA))
	}
}

// TestFriendFlow_AdapterStripsContactInfo 覆盖「好友资料不外泄联系方式」。内部凭证端点返回的资料不该带 email / phone —— 好友列表用不上，
// 外泄了就是隐私漏洞。
func TestFriendFlow_AdapterStripsContactInfo(t *testing.T) {
	ctx := context.Background()
	cli := newFakeRemoteUserClient()
	email := "alice@example.com"
	cli.users["1"] = &userv1.UserInfo{
		Id:       "1",
		Nickname: "Alice",
		Email:    &email,
	}

	profiles, err := NewFriendAdapter(cli).FetchProfiles(ctx, "1", []string{"1"})
	if err != nil {
		t.Fatalf("FetchProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles len = %d, want 1", len(profiles))
	}
	// biz.FriendProfile 本身就没有联系方式字段 —— 编译期就挡住了。
	if profiles[0].Nickname != "Alice" {
		t.Fatalf("nickname = %q, want %q", profiles[0].Nickname, "Alice")
	}
	if profiles[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be populated from the proto timestamp")
	}
	_ = time.Now
}
