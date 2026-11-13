// 本文件是 message service 的**远程** gRPC client —— ws-gateway 跨进程转发聊天帧。
// 网关不落库，只转发。
package data

import (
	"context"
	"errors"
	"time"

	messagev1 "chatroom/api/message/service/v1"

	"chatroom/pkg/observability"

	oteltrace "github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"go.opentelemetry.io/otel"

	"google.golang.org/grpc/metadata"
)

// RemoteMessageClient 暴露网关实际用到的三个方法。
type RemoteMessageClient interface {
	// 走调用方的用户 JWT —— message 服务要确认「发消息的人是不是会话成员」
	SendMessage(ctx context.Context, token string, conversationID uint64, typ uint8, content string) (*messagev1.MessageInfo, error)
	ListMessages(ctx context.Context, token string, conversationID uint64, cursor uint64, limit int32) ([]*messagev1.MessageInfo, uint64, error)
	// 拉「比某个 id 更新的」消息，按 id 正序。断线重连补消息用。
	ListMessagesSince(ctx context.Context, token string, conversationID, sinceID uint64, limit int32) ([]*messagev1.MessageInfo, uint64, bool, error)
}

// RemoteMessageEndpoint 是 message 服务在 nacos 里的服务名（discovery 查找用）。
// 必须带 "discovery:///" 前缀和 ".grpc" 后缀，否则 gRPC 报 "produced zero addresses"。
const RemoteMessageEndpoint = "discovery:///chatroom-message.grpc"

// NewRemoteMessageClient 构造 RemoteMessageClient。
// discoverer 是服务发现器，obs 是观测能力。
// discoverer 为 nil 时返 noop。超时 2s。
func NewRemoteMessageClient(discoverer registry.Discovery, obs *observability.Provider) (RemoteMessageClient, func(), error) {
	if discoverer == nil {
		return &noopRemoteMessageClient{}, func() {}, nil
	}
	conn, err := grpc.NewClient(
		context.Background(),
		grpc.WithDiscovery(discoverer),
		grpc.WithMiddleware(oteltrace.Client(
			oteltrace.WithTracerProvider(otel.GetTracerProvider()),
			oteltrace.WithPropagator(otel.GetTextMapPropagator()),
			oteltrace.WithTracerName("grpc-client"),
		)),
		grpc.WithEndpoint(RemoteMessageEndpoint),
		grpc.WithTimeout(2*time.Second),
	)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = conn.Close() }
	return &remoteMessageGRPCClient{cli: messagev1.NewMessageClient(conn)}, cleanup, nil
}

type remoteMessageGRPCClient struct {
	cli messagev1.MessageClient
}

// SendMessage 发消息。token 是发起方的用户 JWT，conversationID 是会话 ID，
// typ 是消息类型，content 是消息内容。
func (c *remoteMessageGRPCClient) SendMessage(ctx context.Context, token string, conversationID uint64, typ uint8, content string) (*messagev1.MessageInfo, error) {
	req := &messagev1.SendMessageRequest{
		ConversationId: conversationID,
		Type:           messageTypeFromUint8(typ),
		Content:        content,
	}
	reply, err := c.cli.SendMessage(withToken(ctx, token), req)
	if err != nil {
		return nil, err
	}
	return reply.GetMessage(), nil
}

// ListMessages 拉历史消息。token 是发起方的用户 JWT，conversationID 是会话 ID，
// cursor 为 0 表示从最新开始，limit 是单页条数。
func (c *remoteMessageGRPCClient) ListMessages(ctx context.Context, token string, conversationID uint64, cursor uint64, limit int32) ([]*messagev1.MessageInfo, uint64, error) {
	req := &messagev1.ListMessagesRequest{
		ConversationId: conversationID,
		Cursor:         cursor,
		PageSize:       limit,
	}
	reply, err := c.cli.ListMessages(withToken(ctx, token), req)
	if err != nil {
		return nil, 0, err
	}
	return reply.GetMessages(), reply.GetNextCursor(), nil
}

// ListMessagesSince 拉「比某个 id 更新的」消息，按 id 正序。
// token 是发起方的用户 JWT，conversationID 是会话 ID，sinceID 为基准消息 id，
// limit 是单页条数。
func (c *remoteMessageGRPCClient) ListMessagesSince(ctx context.Context, token string, conversationID, sinceID uint64, limit int32) ([]*messagev1.MessageInfo, uint64, bool, error) {
	req := &messagev1.ListMessagesSinceRequest{
		ConversationId: conversationID,
		SinceId:        sinceID,
		PageSize:       limit,
	}
	reply, err := c.cli.ListMessagesSince(withToken(ctx, token), req)
	if err != nil {
		return nil, 0, false, err
	}
	return reply.GetMessages(), reply.GetLastMessageId(), reply.GetHasMore(), nil
}

// messageTypeFromUint8 把 uint8 消息类型转成 proto 枚举。
func messageTypeFromUint8(t uint8) messagev1.MessageType {
	switch t {
	case 2:
		return messagev1.MessageType_MESSAGE_TYPE_IMAGE
	default:
		return messagev1.MessageType_MESSAGE_TYPE_TEXT
	}
}

// withToken 把 token 放进出站 metadata 的 Authorization 头。token 是用户 JWT。
// 网关是替用户转发请求，token 得原样带过去 —— message 服务靠它识别发消息的人。
func withToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set("authorization", "Bearer "+token)
	return metadata.NewOutgoingContext(ctx, md)
}

// errNoRemoteMessage 没有可用的下游 message 服务。
var errNoRemoteMessage = errors.New("message service downstream unavailable")

// noopRemoteMessageClient 未配 discovery 时的兜底。
type noopRemoteMessageClient struct{}

func (noopRemoteMessageClient) SendMessage(context.Context, string, uint64, uint8, string) (*messagev1.MessageInfo, error) {
	return nil, errNoRemoteMessage
}

func (noopRemoteMessageClient) ListMessages(context.Context, string, uint64, uint64, int32) ([]*messagev1.MessageInfo, uint64, error) {
	return nil, 0, errNoRemoteMessage
}

func (noopRemoteMessageClient) ListMessagesSince(context.Context, string, uint64, uint64, int32) ([]*messagev1.MessageInfo, uint64, bool, error) {
	return nil, 0, false, errNoRemoteMessage
}
