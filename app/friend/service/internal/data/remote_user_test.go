package data

import (
	"context"
	"errors"
	"testing"
)

// 没配 internal_secret 时必须返 error，不能返空结果。
//
// 返空结果会被 adapter 当成「这些用户真不存在」——
// 好友请求会被误判成「对方不存在」，而不是「user 服务不可用」。
func TestRemoteUser_NoInternalSecret_ReturnsError(t *testing.T) {
	c := &remoteUserGRPCClient{internalSecret: ""}
	ctx := context.Background()

	t.Run("GetFriendProfiles", func(t *testing.T) {
		users, err := c.GetFriendProfiles(ctx, []string{"u1", "u2"})
		if err == nil {
			t.Fatalf("期望 error，实际返 %v —— 返空结果会被误判成「用户不存在」", users)
		}
		if !errors.Is(err, errNoRemoteSecret) {
			t.Fatalf("期望 errNoRemoteSecret，实际 %v", err)
		}
	})

	t.Run("GetConversationMemberProfiles", func(t *testing.T) {
		users, err := c.GetConversationMemberProfiles(ctx, []string{"u1"})
		if err == nil {
			t.Fatalf("期望 error，实际返 %v", users)
		}
		if !errors.Is(err, errNoRemoteSecret) {
			t.Fatalf("期望 errNoRemoteSecret，实际 %v", err)
		}
	})
}
