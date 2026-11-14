package data

import (
	"context"
	"errors"
	"testing"
)

// 没配 internal_secret 时必须返 error，不能返空结果。
//
// 返空结果会被 adapter 当成「这些用户真不存在」——
// conversation 的建会话 / 加成员会 100% 报「用户不存在」，
// 而不是「user 服务不可用」。这两种情况的可观测性完全不同。
func TestRemoteUser_NoInternalSecret_ReturnsError(t *testing.T) {
	c := &remoteUserGRPCClient{internalSecret: ""}
	ctx := context.Background()

	t.Run("GetConversationMemberProfiles", func(t *testing.T) {
		users, err := c.GetConversationMemberProfiles(ctx, []string{"u1", "u2"})
		if err == nil {
			t.Fatalf("期望 error，实际返 %v —— 返空结果会被误判成「用户不存在」", users)
		}
		if !errors.Is(err, errNoRemoteSecret) {
			t.Fatalf("期望 errNoRemoteSecret，实际 %v", err)
		}
		if users != nil {
			t.Fatalf("出错时不该返 users，实际 %v", users)
		}
	})

	t.Run("GetFriendProfiles", func(t *testing.T) {
		users, err := c.GetFriendProfiles(ctx, []string{"u1"})
		if err == nil {
			t.Fatalf("期望 error，实际返 %v", users)
		}
		if !errors.Is(err, errNoRemoteSecret) {
			t.Fatalf("期望 errNoRemoteSecret，实际 %v", err)
		}
	})
}

// adapter 层不能把「下游没配」压成「全员缺失」。
func TestConversationAdapter_NoSecret_PropagatesError(t *testing.T) {
	a := NewConversationAdapter(&remoteUserGRPCClient{internalSecret: ""})
	ctx := context.Background()

	missing, err := a.CheckUsersExist(ctx, []string{"u1", "u2"})
	if err == nil {
		t.Fatalf("期望 error，实际 missing=%v —— 全员被误判成不存在", missing)
	}
	if len(missing) != 0 {
		t.Fatalf("出错时不该返 missing 列表，实际 %v", missing)
	}
}
