package data

import (
	"context"

	"chatroom/app/ws-gateway/service/internal/gateway"
)

// Sender 把 RemoteMessageClient 适配成 gateway.MessageSender。
type Sender struct {
	cli RemoteMessageClient
}

// NewSender 注入 RemoteMessageClient。
// cli 为 nil 时 Send 返错误。
func NewSender(cli RemoteMessageClient) *Sender {
	return &Sender{cli: cli}
}

// Send 转发一条消息到 message 服务。
// token 发起方的 JWT
// senderID 发送者 uid
// conversationID 会话 id
// msgType 消息类型
// content 消息内容
func (s *Sender) Send(ctx context.Context, token string, senderID string, conversationID uint64, msgType uint8, content string) (uint64, int64, error) {
	if s == nil || s.cli == nil {
		return 0, 0, errNoRemoteMessage
	}
	info, err := s.cli.SendMessage(ctx, token, conversationID, msgType, content)
	if err != nil {
		return 0, 0, err
	}
	return info.GetId(), info.GetCreatedAt(), nil
}

// 确保 Sender 满足 gateway.MessageSender —— 编译期检查。
var _ gateway.MessageSender = (*Sender)(nil)

// Lister 把 RemoteMessageClient 适配成 gateway.MessageLister（断线重连时补消息）。
type Lister struct {
	cli RemoteMessageClient
}

// NewLister 注入 RemoteMessageClient。
// cli 为 nil 时 ListSince 返错误。
func NewLister(cli RemoteMessageClient) *Lister {
	return &Lister{cli: cli}
}

// ListSince 拉「id 严格大于 sinceID」的消息
// token 发起方的 JWT
// conversationID 会话 id
// sinceID 基准消息 id
// limit 单页条数
// 返回消息列表、最后一条消息 id、是否还有更多。
func (l *Lister) ListSince(ctx context.Context, token string, conversationID, sinceID uint64, limit int32) ([]gateway.MessageInfo, uint64, bool, error) {
	if l == nil || l.cli == nil {
		return nil, 0, false, errNoRemoteMessage
	}
	infos, last, hasMore, err := l.cli.ListMessagesSince(ctx, token, conversationID, sinceID, limit)
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]gateway.MessageInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, gateway.MessageInfo{
			ID:             info.GetId(),
			ConversationID: info.GetConversationId(),
			SenderID:       info.GetSenderId(),
			Type:           uint8(info.GetType()),
			Content:        info.GetContent(),
			CreatedAt:      info.GetCreatedAt(),
		})
	}
	return out, last, hasMore, nil
}

// 确保 Lister 满足 gateway.MessageLister —— 编译期检查。
var _ gateway.MessageLister = (*Lister)(nil)
