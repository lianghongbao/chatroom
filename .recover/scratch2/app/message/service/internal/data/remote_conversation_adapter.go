package data

import (
	"context"
	"errors"
)

// errNoRemoteConversation 没有可用的下游 conversation 服务。
var errNoRemoteConversation = errors.New("conversation service downstream unavailable")

// errNoRemoteSecret 没配 internal_secret —— 无法通过下游的内部凭证校验。
var errNoRemoteSecret = errors.New("internal secret not configured")

// MembershipAdapter 把 RemoteConversationClient 适配成 biz.ConversationMembershipChecker。
type MembershipAdapter struct {
	cli RemoteConversationClient
}

// NewMembershipAdapter wire 注入 RemoteConversationClient。nil 时 IsMember 返错误。
func NewMembershipAdapter(cli RemoteConversationClient) *MembershipAdapter {
	return &MembershipAdapter{cli: cli}
}

// IsMember 查 userID 是不是 conversationID 的成员。err 透传给 biz，由 biz 决定拒绝。
func (a *MembershipAdapter) IsMember(ctx context.Context, conversationID uint64, userID string) (bool, error) {
	if a == nil || a.cli == nil {
		return false, errNoRemoteConversation
	}
	return a.cli.CheckMembership(ctx, conversationID, userID)
}

// UnreadAdapter 把 RemoteConversationClient 适配成 biz.UnreadCounter。
type UnreadAdapter struct {
	cli RemoteConversationClient
}

// NewUnreadAdapter wire 注入 RemoteConversationClient。nil 时所有方法返错误。
func NewUnreadAdapter(cli RemoteConversationClient) *UnreadAdapter {
	return &UnreadAdapter{cli: cli}
}

// GetUnread 查某成员的会话级未读数。
func (a *UnreadAdapter) GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.GetUnread(ctx, conversationID, userID)
}

// IncUnread 给某成员的会话级未读数加 delta，返回更新后的值。
func (a *UnreadAdapter) IncUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.UpdateUnread(ctx, conversationID, userID, delta)
}

// IncUnreadBatch 给一批成员的会话级未读数加 delta。
// 走下游的批量端点，一次跨进程调用搞定整个 fan-out。
func (a *UnreadAdapter) IncUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.UpdateUnreadBatch(ctx, conversationID, userIDs, delta)
}

// ClearUnread 把未读数清零，返回清零后的值。
// 走下游的 ClearUnread 端点，服务端一条 UPDATE 原子清零。
func (a *UnreadAdapter) ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.ClearUnread(ctx, conversationID, userID)
}

// ListUnread 查某 uid 在所有会话的未读数。
func (a *UnreadAdapter) ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error) {
	if a == nil || a.cli == nil {
		return nil, errNoRemoteConversation
	}
	return a.cli.ListUnread(ctx, userID)
}

// MembersAdapter 把 RemoteConversationClient 适配成 gateway.MemberFetcher。
type MembersAdapter struct {
	cli RemoteConversationClient
}

// NewMembersAdapter wire 注入 RemoteConversationClient。nil 时 ListConversationMembers 返错误。
func NewMembersAdapter(cli RemoteConversationClient) *MembersAdapter {
	return &MembersAdapter{cli: cli}
}

// ListConversationMembers 列出会话的全部成员。
func (a *MembersAdapter) ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error) {
	if a == nil || a.cli == nil {
		return nil, errNoRemoteConversation
	}
	return a.cli.ListConversationMembers(ctx, conversationID)
}

// MemberListerAdapter 把 RemoteConversationClient 适配成 biz.ConversationMemberLister。
type MemberListerAdapter struct {
	cli RemoteConversationClient
}

// NewMemberListerAdapter wire 注入 RemoteConversationClient。nil 时 ListConversationMembers 返错误。
func NewMemberListerAdapter(cli RemoteConversationClient) *MemberListerAdapter {
	return &MemberListerAdapter{cli: cli}
}

// ListConversationMembers 列出会话的全部成员。
func (a *MemberListerAdapter) ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error) {
	if a == nil || a.cli == nil {
		return nil, errNoRemoteConversation
	}
	return a.cli.ListConversationMembers(ctx, conversationID)
}

// UserConversationListerAdapter 把 RemoteConversationClient 适配成 gateway.UserConversationLister。
type UserConversationListerAdapter struct {
	cli RemoteConversationClient
}

// NewUserConversationListerAdapter wire 注入 RemoteConversationClient。nil 时 ListUserConversations 返错误。
func NewUserConversationListerAdapter(cli RemoteConversationClient) *UserConversationListerAdapter {
	return &UserConversationListerAdapter{cli: cli}
}

// ListUserConversations 列某用户参与的所有会话 id。
func (a *UserConversationListerAdapter) ListUserConversations(ctx context.Context, userID string) ([]uint64, error) {
	if a == nil || a.cli == nil {
		return nil, errNoRemoteConversation
	}
	return a.cli.ListUserConversations(ctx, userID)
}
