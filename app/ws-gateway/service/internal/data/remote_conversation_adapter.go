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

// NewMembershipAdapter 注入 RemoteConversationClient。
// cli 为 nil 时 IsMember 返错误。
func NewMembershipAdapter(cli RemoteConversationClient) *MembershipAdapter {
	return &MembershipAdapter{cli: cli}
}

// IsMember 查该用户是不是会话成员。
// conversationID 会话 ID
// userID 用户 ID
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

// NewUnreadAdapter 注入 RemoteConversationClient。
// cli 为 nil 时所有方法返错误。
func NewUnreadAdapter(cli RemoteConversationClient) *UnreadAdapter {
	return &UnreadAdapter{cli: cli}
}

// GetUnread 查某成员的会话级未读数。
// conversationID 会话 ID
// userID 用户 ID
func (a *UnreadAdapter) GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.GetUnread(ctx, conversationID, userID)
}

// IncUnread 给某成员的会话级未读数加 delta，返回更新后的值。
// conversationID 会话 ID
// userID 用户 ID
// delta 增量（可为负）
func (a *UnreadAdapter) IncUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.UpdateUnread(ctx, conversationID, userID, delta)
}

// ClearUnread 把未读数清零，返回清零后的值。
// conversationID 会话 ID
// userID 用户 ID
func (a *UnreadAdapter) ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if a == nil || a.cli == nil {
		return 0, errNoRemoteConversation
	}
	return a.cli.ClearUnread(ctx, conversationID, userID)
}

// ListUnread 查某用户在所有会话的未读数。userID 是用户 ID。
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

// NewMembersAdapter 注入 RemoteConversationClient。
// cli 为 nil 时 ListConversationMembers 返错误。
func NewMembersAdapter(cli RemoteConversationClient) *MembersAdapter {
	return &MembersAdapter{cli: cli}
}

// ListConversationMembers 列出会话的全部成员。conversationID 是会话 ID。
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

// NewMemberListerAdapter 注入 RemoteConversationClient。
// cli 为 nil 时 ListConversationMembers 返错误。
func NewMemberListerAdapter(cli RemoteConversationClient) *MemberListerAdapter {
	return &MemberListerAdapter{cli: cli}
}

// ListConversationMembers 列出会话的全部成员。conversationID 是会话 ID。
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

// NewUserConversationListerAdapter 注入 RemoteConversationClient。
// cli 为 nil 时 ListUserConversations 返错误。
func NewUserConversationListerAdapter(cli RemoteConversationClient) *UserConversationListerAdapter {
	return &UserConversationListerAdapter{cli: cli}
}

// ListUserConversations 列某用户参与的所有会话 id。userID 是用户 ID。
func (a *UserConversationListerAdapter) ListUserConversations(ctx context.Context, userID string) ([]uint64, error) {
	if a == nil || a.cli == nil {
		return nil, errNoRemoteConversation
	}
	return a.cli.ListUserConversations(ctx, userID)
}
