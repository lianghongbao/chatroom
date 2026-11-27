package biz

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestApply_TruncatesMessageByRunes 附言按 rune 截断，不能切出半个字符。
func TestApply_TruncatesMessageByRunes(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	// 300 个中文字符 = 900 字节。
	msg := strings.Repeat("好", 300)

	res, err := uc.Apply(context.Background(), "uid-1", "uid-2", msg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res == nil {
		t.Fatal("Apply 返回 nil")
	}

	stored := repo.byPair[encodePair("uid-1", "uid-2")]
	if stored == nil {
		t.Fatal("没存下申请记录")
	}
	if !utf8.ValidString(stored.Message) {
		t.Errorf("截断后不是合法 UTF-8: %q", stored.Message)
	}
	if n := utf8.RuneCountInString(stored.Message); n != MaxApplyMessageLen {
		t.Errorf("截断后 %d 个 rune, want %d", n, MaxApplyMessageLen)
	}
}

// TestApply_ShortMessageUnchanged 短消息不该被改动。
func TestApply_ShortMessageUnchanged(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	const msg = "你好，我想加你为好友"
	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", msg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stored := repo.byPair[encodePair("uid-1", "uid-2")]
	if stored == nil {
		t.Fatal("没存下申请记录")
	}
	if stored.Message != msg {
		t.Errorf("message = %q, want %q", stored.Message, msg)
	}
}

// TestApply_ExactlyMaxLength 正好等于上限的消息不该被截。
func TestApply_ExactlyMaxLength(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)

	msg := strings.Repeat("a", MaxApplyMessageLen)
	if _, err := uc.Apply(context.Background(), "uid-1", "uid-2", msg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stored := repo.byPair[encodePair("uid-1", "uid-2")]
	if stored == nil {
		t.Fatal("没存下申请记录")
	}
	if stored.Message != msg {
		t.Errorf("message 长度 = %d, want %d", len(stored.Message), len(msg))
	}
}

// TestApply_ReversePendingAutoAccept 反向 PENDING 自动收尾成好友。
func TestApply_ReversePendingAutoAccept(t *testing.T) {
	repo := newFakeFriendRepo()
	uc := NewFriendUsecase(repo, &fakeFriendFetcher{}, &fakeFriendLimiter{}, nil)
	ctx := context.Background()

	// uid-2 先申请 uid-1。
	if _, err := uc.Apply(ctx, "uid-2", "uid-1", "hi"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// uid-1 反过来申请 uid-2 —— 应该自动成好友。
	res, err := uc.Apply(ctx, "uid-1", "uid-2", "hi back")
	if err != nil {
		t.Fatalf("反向申请失败: %v", err)
	}
	if res.Status != FriendStatusAccepted {
		t.Errorf("status = %v, want ACCEPTED", res.Status)
	}
}
