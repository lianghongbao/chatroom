package data

import (
	"context"
	"errors"

	"chatroom/app/conversation/service/internal/biz"
)

// ConversationAdapter 把 RemoteUserClient 适配成 biz.ConversationProfileFetcher，集中处理 proto ↔ DO 转换。
// cli 为 nil 时透传「无可用下游」。
type ConversationAdapter struct {
	cli RemoteUserClient
}

// NewConversationAdapter wire 注入 RemoteUserClient。cli 是远程用户客户端，为 nil 时仍可用。
func NewConversationAdapter(cli RemoteUserClient) *ConversationAdapter {
	return &ConversationAdapter{cli: cli}
}

// FetchProfiles 把 GetConversationMemberProfiles 结果折成 []*biz.UserProfile。ids 是要查的用户 ID 列表，整批 err 透传给 biz。
func (a *ConversationAdapter) FetchProfiles(ctx context.Context, ids []string) ([]*biz.UserProfile, error) {
	if a == nil || a.cli == nil {
		return nil, nil
	}
	users, err := a.cli.GetConversationMemberProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.UserProfile, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		out = append(out, &biz.UserProfile{
			UserID:   u.GetId(),
			Nickname: u.GetNickname(),
			Avatar:   u.GetAvatar(),
		})
	}
	return out, nil
}

// CheckUsersExist 返回 ids 里不存在的 uid 列表。请求了但没回来的就是不存在；err 透传给 biz 放行。
func (a *ConversationAdapter) CheckUsersExist(ctx context.Context, ids []string) ([]string, error) {
	if a == nil || a.cli == nil {
		// 没有下游可用 —— 返错误让 biz 放行，而不是谎报「全部存在」。
		return nil, errNoDownstream
	}
	if len(ids) == 0 {
		return nil, nil
	}
	users, err := a.cli.GetConversationMemberProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	found := make(map[string]struct{}, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		found[u.GetId()] = struct{}{}
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// errNoDownstream 没有可用的下游 user 服务。
var errNoDownstream = errors.New("user service downstream unavailable")

// 确保 ConversationAdapter 满足 biz 的两个接口 —— 编译期检查。
var (
	_ biz.ConversationProfileFetcher = (*ConversationAdapter)(nil)
	_ biz.UserExistenceChecker       = (*ConversationAdapter)(nil)
)
