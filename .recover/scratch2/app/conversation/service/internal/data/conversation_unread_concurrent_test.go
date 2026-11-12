package data

import (
	"context"
	"sync"
	"testing"

	"chatroom/conversation-service/internal/data/ent/conversationmember"
)

// TestConversationRepo_UpdateUnreadConcurrent 加未读数不能丢更新。
//
// 旧实现是「读出来 → 算一下 → 带条件写回去」的乐观锁重试。但 MySQL 默认
// REPEATABLE READ 下，事务里重读拿到的是同一个快照，条件 UPDATE 的
// WHERE unread_count = <旧值> 永远匹配不上 —— 每次重试都是 0 行，
// 5 轮走完直接返回冲突错误。加的未读数就这么静默丢了。
//
// 正确做法是把加减放到数据库端做（SET unread_count = unread_count + ?），
// 让并发写由 InnoDB 的行锁串行化。
func TestConversationRepo_UpdateUnreadConcurrent(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	const goroutines = 8
	const perGoroutine = 5
	const total = goroutines * perGoroutine

	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := repo.UpdateUnread(ctx, convID, "uid-2", 1); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 UpdateUnread 失败: %v", err)
	}

	// 直接查库看真实值，别信返回值。
	po, err := d.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(convID),
			conversationmember.UserIDEQ("uid-2"),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("查成员: %v", err)
	}
	if int(po.UnreadCount) != total {
		t.Errorf("未读数 = %d, want %d（并发更新丢了）", po.UnreadCount, total)
	}
}

// TestConversationRepo_UpdateUnreadConcurrentNeverNegative 并发减未读数时，
// 结果不能是负数 —— 也不能因为「钳到 0」把别人的加给覆盖掉。
func TestConversationRepo_UpdateUnreadConcurrentNeverNegative(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.UpdateUnread(ctx, convID, "uid-2", 3); err != nil {
		t.Fatalf("UpdateUnread: %v", err)
	}

	const goroutines = 6
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.UpdateUnread(ctx, convID, "uid-2", -10); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 UpdateUnread 失败: %v", err)
	}

	po, err := d.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(conversationID),
			conversationmember.UserIDEQ("uid-2"),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("查成员: %v", err)
	}
	if po.UnreadCount != 0 {
		t.Errorf("未读数 = %d, want 0", po.UnreadCount)
	}
}

// TestConversationRepo_UpdateUnreadMissingMember 成员不存在时不该建出新行，
// 也不该把别的会话的未读数改掉。
func TestConversationRepo_UpdateUnreadMissingMember(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.UpdateUnread(ctx, convID, "uid-not-a-member", 1); err == nil {
		t.Errorf("对不存在的成员加未读数应该报错, got nil")
	}

	n, err := d.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(convID),
			conversationmember.UserIDEQ("uid-not-a-member"),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("计数: %v", err)
	}
	if n != 0 {
		t.Errorf("凭空多出 %d 行成员记录, want 0", n)
	}
}
