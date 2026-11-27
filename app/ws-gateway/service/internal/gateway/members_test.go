package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestMemberCache_CachesWithinTTL TTL 内不重复回源。
func TestMemberCache_CachesWithinTTL(t *testing.T) {
	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, id uint64) ([]string, error) {
		calls++
		return []string{"uid-1", "uid-2"}, nil
	}), time.Minute)

	for i := 0; i < 3; i++ {
		members, err := c.Get(context.Background(), 7)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(members) != 2 {
			t.Fatalf("成员数 = %d, want 2", len(members))
		}
	}
	if calls != 1 {
		t.Errorf("回源次数 = %d, want 1", calls)
	}
}

// TestMemberCache_ExpiresAfterTTL TTL 过了要重新回源 —— 群成员变了
// 得能看见。
func TestMemberCache_ExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		calls++
		return []string{"uid-1"}, nil
	}), time.Millisecond)

	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Errorf("回源次数 = %d, want 2", calls)
	}
}

// TestMemberCache_Invalidate 手动失效后要重新回源。
func TestMemberCache_Invalidate(t *testing.T) {
	calls := 0
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		calls++
		return []string{"uid-1"}, nil
	}), time.Minute)

	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	c.Invalidate(7)
	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if calls != 2 {
		t.Errorf("回源次数 = %d, want 2", calls)
	}
}

// TestMemberCache_ErrorPropagates 回源失败要往上返 —— 查不到成员就不能广播。
func TestMemberCache_ErrorPropagates(t *testing.T) {
	want := errors.New("conversation down")
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, _ uint64) ([]string, error) {
		return nil, want
	}), time.Minute)

	if _, err := c.Get(context.Background(), 7); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestMemberCache_NilFetcher 空 fetcher 时返 nil，不报错。
func TestMemberCache_NilFetcher(t *testing.T) {
	c := NewMemberCache(nil, time.Minute)
	members, err := c.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if members != nil {
		t.Errorf("members = %v, want nil", members)
	}
}

// TestMemberCache_NilReceiver nil 缓存上调用不能 panic。
func TestMemberCache_NilReceiver(t *testing.T) {
	var c *MemberCache
	c.Invalidate(7)
	if _, err := c.Get(context.Background(), 7); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestMemberCache_PerConversationIsolation 不同会话各缓存各的。
func TestMemberCache_PerConversationIsolation(t *testing.T) {
	c := NewMemberCache(MemberFetcherFunc(func(_ context.Context, id uint64) ([]string, error) {
		return []string{fmt.Sprintf("uid-%d", id), fmt.Sprintf("uid-%d", id+100)}, nil
	}), time.Minute)

	m1, err := c.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	m2, err := c.Get(context.Background(), 8)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(m1) != 2 || m1[0] != "uid-7" {
		t.Errorf("会话 7 的成员 = %v", m1)
	}
	if len(m2) != 2 || m2[0] != "uid-8" {
		t.Errorf("会话 8 的成员 = %v", m2)
	}
}
