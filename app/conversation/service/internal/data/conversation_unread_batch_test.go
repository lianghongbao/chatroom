package data

import (
	"context"
	"testing"

	"chatroom/app/conversation/service/internal/data/ent/conversationmember"
)

// TestConversationRepo_UpdateUnreadBatch 批量加未读数。
//
// 消息服务发一条消息要给所有非发送方成员加未读 —— 逐个调是 N 次跨进程调用。
func TestConversationRepo_UpdateUnreadBatch(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	// 会话里只有 uid-1 / uid-2（newUnreadTestConv 建的两个）
	n, err := repo.UpdateUnreadBatch(ctx, convID, []string{"uid-1", "uid-2"}, 3)
	if err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if n != 2 {
		t.Errorf("更新成员数 = %d, want 2", n)
	}

	for _, uid := range []string{"uid-1", "uid-2"} {
		po, err := d.db.ConversationMember.Query().
			Where(
				conversationmember.ConversationIDEQ(convID),
				conversationmember.UserIDEQ(uid),
			).
			Only(ctx)
		if err != nil {
			t.Fatalf("查成员 %s: %v", uid, err)
		}
		if po.UnreadCount != 3 {
			t.Errorf("%s 的未读数 = %d, want 3", uid, po.UnreadCount)
		}
	}
}

// TestConversationRepo_UpdateUnreadBatchSkipsNonMembers 不是成员的 uid
// 跳过 —— 未读数只是角标，少算一个不该让整批失败。
func TestConversationRepo_UpdateUnreadBatchSkipsNonMembers(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	n, err := repo.UpdateUnreadBatch(ctx, convID, []string{"uid-1", "uid-not-member"}, 1)
	if err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("更新成员数 = %d, want 1（非成员被跳过）", n)
	}

	// 非成员不能凭空多出一行
	cnt, err := d.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(convID),
			conversationmember.UserIDEQ("uid-not-member"),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("凭空多出 %d 行成员记录, want 0", cnt)
	}
}

// TestConversationRepo_UpdateUnreadBatchDeduplicates 同一个 uid 传两次
// 只算一次。
func TestConversationRepo_UpdateUnreadBatchDeduplicates(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	n, err := repo.UpdateUnreadBatch(ctx, convID, []string{"uid-2", "uid-2", "uid-2"}, 1)
	if err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("更新成员数 = %d, want 1（重复 uid 只算一次）", n)
	}

	po, err := d.db.ConversationMember.Query().
		Where(
			conversationmember.ConversationIDEQ(convID),
			conversationmember.UserIDEQ("uid-2"),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("查成员: %v", err)
	}
	if po.UnreadCount != 1 {
		t.Errorf("未读数 = %d, want 1（重复的 uid 只加了一次）", po.UnreadCount)
	}
}

// TestConversationRepo_UpdateUnreadBatchNeverNegative 批量减也不会减到 0 以下。
func TestConversationRepo_UpdateUnreadBatchNeverNegative(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	if _, err := repo.UpdateUnreadBatch(ctx, convID, []string{"uid-1", "uid-2"}, 3); err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if _, err := repo.UpdateUnreadBatch(ctx, convID, []string{"uid-1", "uid-2"}, -100); err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}

	for _, uid := range []string{"uid-1", "uid-2"} {
		po, err := d.db.ConversationMember.Query().
			Where(
				conversationmember.ConversationIDEQ(convID),
				conversationmember.UserIDEQ(uid),
			).
			Only(ctx)
		if err != nil {
			t.Fatalf("查成员 %s: %v", uid, err)
		}
		if po.UnreadCount != 0 {
			t.Errorf("%s 的未读数 = %d, want 0", uid, po.UnreadCount)
		}
	}
}

// TestConversationRepo_UpdateUnreadBatchEmptyList 空列表是合法的 no-op。
func TestConversationRepo_UpdateUnreadBatchEmptyList(t *testing.T) {
	d, repo := newTestConvRepo(t)
	ctx := context.Background()
	convID := newUnreadTestConv(t, d, repo)

	n, err := repo.UpdateUnreadBatch(ctx, convID, nil, 1)
	if err != nil {
		t.Fatalf("UpdateUnreadBatch: %v", err)
	}
	if n != 0 {
		t.Errorf("更新成员数 = %d, want 0", n)
	}
}
