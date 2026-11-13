package data

import (
	"context"
	"sync"
	"testing"

	"chatroom/app/friend/service/internal/biz"
	"chatroom/app/friend/service/internal/data/ent"
	"chatroom/app/friend/service/internal/data/ent/friend"
)

// TestFriendRepo_ConcurrentOppositeApplyCreatesOneRow 反向并发申请只能建一条记录。
//
// A 申请 B 的同时 B 也申请 A —— 两条记录的方向相反，
// uk_user_friend 只挡同向重复，挡不住这个。真正的仲裁者是归一化的
// pair_key 唯一索引（"a,b" 和 "b,a" 落到同一个键）。
func TestFriendRepo_ConcurrentOppositeApplyCreatesOneRow(t *testing.T) {
	d, _ := newTestDB(t)
	repo := NewFriendRepo(d)
	ctx := context.Background()

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			// 一半正向一半反向。
			user, fr := "uid-1", "uid-2"
			if idx%2 == 1 {
				user, fr = "uid-2", "uid-1"
			}
			_, err := repo.Save(ctx, &biz.Friend{
				UserID: user, FriendID: fr,
				Status: biz.FriendStatusPending, Message: "hi",
			})
			if err != nil && err != biz.ErrFriendAlreadyApplied {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Save 报错: %v", err)
	}

	n, err := d.db.Friend.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("库里 %d 条好友记录, want 1（反向并发建出了多条）", n)
	}
}

// TestFriendRepo_PairKeyIsNormalized pair_key 必须归一化 ——
// 两个 uid 升序拼接，跟方向无关。
func TestFriendRepo_PairKeyIsNormalized(t *testing.T) {
	d, _ := newTestDB(t)
	repo := NewFriendRepo(d)
	ctx := context.Background()

	if _, err := repo.Save(ctx, &biz.Friend{
		UserID: "uid-2", FriendID: "uid-1",
		Status: biz.FriendStatusPending,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	po, err := d.db.Friend.Query().Only(ctx)
	if err != nil {
		t.Fatalf("查: %v", err)
	}
	if po.PairKey != "uid-1,uid-2" {
		t.Errorf("pair_key = %q, want \"uid-1,uid-2\"（两个 uid 应升序拼接）", po.PairKey)
	}
}

// TestFriendRepo_PairKeyUniqueIndexRejectsDuplicate pair_key 的唯一索引
// 得真的挡得住重复插入 —— 这是反向并发唯一的仲裁者。
func TestFriendRepo_PairKeyUniqueIndexRejectsDuplicate(t *testing.T) {
	d, _ := newTestDB(t)
	ctx := context.Background()

	// 直接绕开 repo 插一条重复 pair_key，验证数据库会拒。
	if _, err := d.db.Friend.Create().
		SetUserID("uid-1").SetFriendID("uid-2").
		SetPairKey("uid-1,uid-2").
		Save(ctx); err != nil {
		t.Fatalf("第一条插入失败: %v", err)
	}

	_, err := d.db.Friend.Create().
		SetUserID("uid-2").SetFriendID("uid-1").
		SetPairKey("uid-1,uid-2").
		Save(ctx)
	if err == nil {
		t.Fatal("重复 pair_key 插入应被唯一索引拒绝")
	}
	if !ent.IsConstraintError(err) {
		t.Errorf("err = %v, want constraint error", err)
	}
}

// TestFriendRepo_QueryByPairKey 按归一化键查得到那条记录。
func TestFriendRepo_QueryByPairKey(t *testing.T) {
	d, _ := newTestDB(t)
	repo := NewFriendRepo(d)
	ctx := context.Background()

	if _, err := repo.Save(ctx, &biz.Friend{
		UserID: "uid-2", FriendID: "uid-1",
		Status: biz.FriendStatusPending,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	po, err := d.db.Friend.Query().
		Where(friend.PairKeyEQ("uid-1,uid-2")).
		Only(ctx)
	if err != nil {
		t.Fatalf("按 pair_key 查: %v", err)
	}
	if po.UserID != "uid-2" || po.FriendID != "uid-1" {
		t.Errorf("查到的记录方向不对: user_id=%q friend_id=%q", po.UserID, po.FriendID)
	}
}
