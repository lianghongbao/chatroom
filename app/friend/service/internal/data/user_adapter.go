package data

import (
	"context"
	"time"

	"chatroom/app/friend/service/internal/biz"
)

// FriendAdapter 把 RemoteUserClient 适配成 biz.FriendProfileFetcher。
// 本 Adapter 集中处理 proto ↔ DO 转换。cli == nil 时透传「无可用下游」。
type FriendAdapter struct {
	cli RemoteUserClient
}

// NewFriendAdapter 构造 FriendAdapter。
// cli 下游客户端
func NewFriendAdapter(cli RemoteUserClient) *FriendAdapter {
	return &FriendAdapter{cli: cli}
}

// FetchProfiles 把 GetFriendProfiles 结果折成 []*biz.FriendProfile。
// callerID 是调用方，ids 是目标 uid 列表。
// 整批 err 透传，由 biz 走降级（昵称/头像留空，不阻断好友列表）。
func (a *FriendAdapter) FetchProfiles(ctx context.Context, callerID string, ids []string) ([]*biz.FriendProfile, error) {
	if a == nil || a.cli == nil {
		return nil, nil
	}
	users, err := a.cli.GetFriendProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.FriendProfile, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		out = append(out, &biz.FriendProfile{
			UserID:    u.GetId(),
			Nickname:  u.GetNickname(),
			Avatar:    u.GetAvatar(),
			CreatedAt: time.Unix(u.GetCreatedAt(), 0),
		})
	}
	return out, nil
}

// 确保 FriendAdapter 满足 biz 的接口 —— 编译期检查。
var _ biz.FriendProfileFetcher = (*FriendAdapter)(nil)
