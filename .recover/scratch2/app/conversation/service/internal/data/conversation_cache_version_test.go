package data

import (
	"context"
	"testing"
)

// TestConversationCache_StaleWriteBackRejected 陈旧的回填要被拒绝。
//
// 场景：读者查库拿到成员列表（版本 v），这期间有人加/减了成员
// （版本变成 v+1），读者这时才回填 —— 写进去的是陈旧数据。
// 版本号比对就是挡这个的：回填带的版本跟当前版本对不上就放弃。
func TestConversationCache_StaleWriteBackRejected(t *testing.T) {
	c, _ := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900701

	// 读者在查库前取到版本 0
	ver := c.MembersVersion(ctx, convID)

	// 期间有人改了成员 —— 版本 +1
	c.InvalidateMembers(ctx, convID)

	// 读者这时才回填（带的是旧版本）
	c.SetMembers(ctx, convID, []string{"uid-stale"}, ver)

	// 陈旧数据不该进缓存
	got, _, err := c.Members(ctx, convID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("缓存里有 %v —— 陈旧的回填被写进去了", got)
	}
}

// TestConversationCache_FreshWriteBackAccepted 版本没变时回填要成功。
func TestConversationCache_FreshWriteBackAccepted(t *testing.T) {
	c, _ := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900702

	ver := c.MembersVersion(ctx, convID)
	c.SetMembers(ctx, convID, []string{"uid-1", "uid-2"}, ver)

	got, _, err := c.Members(ctx, convID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("成员数 = %d, want 2", len(got))
	}
}

// TestConversationCache_InvalidateBumpsVersion 失效要让版本 +1 ——
// 正在进行的回填靠这个发现「数据变了」。
func TestConversationCache_InvalidateBumpsVersion(t *testing.T) {
	c, _ := newTestConvCache(t)
	ctx := context.Background()
	const convID = 900703

	before := c.MembersVersion(ctx, convID)
	c.InvalidateMembers(ctx, convID)
	after := c.MembersVersion(ctx, convID)

	if after != before+1 {
		t.Errorf("版本号 = %d, want %d（失效应该 +1）", after, before+1)
	}
}
