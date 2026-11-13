// 本文件是 conversation service 的远程 gRPC client —— message service 跨进程访问会话。
// 用于确认会话成员身份与维护会话级未读数。走 nacos discovery。
package data

import (
	"context"
	"time"

	conversationv1 "chatroom/api/conversation/service/v1"
	"chatroom/app/message/service/internal/conf"
	"chatroom/pkg/observability"

	oteltrace "github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"go.opentelemetry.io/otel"

	"google.golang.org/grpc/metadata"
)

// RemoteConversationClient 暴露 message 服务实际用到的 conversation 端点。
// 全部走服务间内部凭证，这几个端点在 conversation 服务上不对用户开放。
type RemoteConversationClient interface {
	// CheckMembership 查某 uid 是不是会话成员。conversationID 是会话 ID，userID 是用户 ID。
	CheckMembership(ctx context.Context, conversationID uint64, userID string) (bool, error)
	// UpdateUnread 增减某成员的会话级未读数，返回更新后的值。delta 是增量。
	UpdateUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error)
	// UpdateUnreadBatch 批量增减多个成员的会话级未读数。conversationID 是会话 ID，
	// userIDs 是成员 ID 列表，delta 是增量。发消息后的未读 fan-out 用它。
	UpdateUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error)
	// GetUnread 查某成员的会话级未读数。conversationID 是会话 ID，userID 是用户 ID。
	GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ClearUnread 把某成员的会话级未读数清零，服务端一条 UPDATE 原子完成。返回清零后的值。
	ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error)
	// ListUnread 查某 uid 在所有会话的未读数（key = conversation_id）。
	ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error)
	// ListConversationMembers 列出会话的全部成员（不校验调用方身份）。conversationID 是会话 ID。
	ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error)
	// ListUserConversations 列某用户参与的所有会话 id（不校验调用方身份）。userID 是用户 ID。
	ListUserConversations(ctx context.Context, userID string) ([]uint64, error)
}

// UpdateUnreadBatch 批量增减多个成员的会话级未读数。conversationID 是会话 ID，
// userIDs 是成员 ID 列表，delta 是增量。一次调用搞定整个 fan-out。
func (c *remoteConversationGRPCClient) UpdateUnreadBatch(ctx context.Context, conversationID uint64, userIDs []string, delta int32) (uint32, error) {
	if c.internalSecret == "" {
		return 0, errNoRemoteSecret
	}
	reply, err := c.cli.UpdateUnreadBatch(ctx, &conversationv1.UpdateUnreadBatchRequest{
		ConversationId: conversationID,
		UserIds:        userIDs,
		Delta:          delta,
	})
	if err != nil {
		return 0, err
	}
	return reply.GetUpdated(), nil
}

// RemoteConversationEndpoint 是 conversation 服务在 nacos 里的服务名（discovery 查找用）。
// 必须带 "discovery:///" 前缀和 ".grpc" 后缀，否则 gRPC 报 "produced zero addresses"。
const RemoteConversationEndpoint = "discovery:///chatroom-conversation.grpc"

// NewRemoteConversationClient 构造 RemoteConversationClient。
// discoverer 服务发现客户端，为 nil 时返 noop
// auth 鉴权配置，取内部凭证
// obs 观测能力
// 超时 1.5s：发消息是同步链路，下游卡住会拖慢用户请求。
func NewRemoteConversationClient(discoverer registry.Discovery, auth *conf.Auth, obs *observability.Provider) (RemoteConversationClient, func(), error) {
	if discoverer == nil {
		return &noopRemoteConversationClient{}, func() {}, nil
	}
	internalSecret := ""
	if auth != nil {
		internalSecret = auth.GetInternalSecret()
	}
	conn, err := grpc.NewClient(
		context.Background(),
		grpc.WithDiscovery(discoverer),
		grpc.WithEndpoint(RemoteConversationEndpoint),
		// WithMiddleware 是「赋值」不是「追加」—— 调多次后者会覆盖前者。
		// 内部凭证和 trace 两个中间件必须放进同一次调用。
		grpc.WithMiddleware(
			internalForwarding(internalSecret),
			oteltrace.Client(
				oteltrace.WithTracerProvider(otel.GetTracerProvider()),
				oteltrace.WithPropagator(otel.GetTextMapPropagator()),
				oteltrace.WithTracerName("grpc-client"),
			),
		),
		grpc.WithTimeout(1500*time.Millisecond),
	)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = conn.Close() }
	return &remoteConversationGRPCClient{cli: conversationv1.NewConversationClient(conn), internalSecret: internalSecret}, cleanup, nil
}

type remoteConversationGRPCClient struct {
	cli            conversationv1.ConversationClient
	internalSecret string // 服务间内部凭证。空表示未配置，调用降级返错误
}

// CheckMembership 查成员身份。conversationID 是会话 ID，userID 是用户 ID。
func (c *remoteConversationGRPCClient) CheckMembership(ctx context.Context, conversationID uint64, userID string) (bool, error) {
	if c.internalSecret == "" {
		return false, errNoRemoteSecret
	}
	reply, err := c.cli.CheckMembership(ctx, &conversationv1.CheckMembershipRequest{
		ConversationId: conversationID,
		UserId:         userID,
	})
	if err != nil {
		return false, err
	}
	return reply.GetIsMember(), nil
}

// UpdateUnread 增减未读数。conversationID 是会话 ID，userID 是用户 ID，delta 是增量。
func (c *remoteConversationGRPCClient) UpdateUnread(ctx context.Context, conversationID uint64, userID string, delta int32) (uint32, error) {
	if c.internalSecret == "" {
		return 0, errNoRemoteSecret
	}
	reply, err := c.cli.UpdateUnread(ctx, &conversationv1.UpdateUnreadRequest{
		ConversationId: conversationID,
		UserId:         userID,
		Delta:          delta,
	})
	if err != nil {
		return 0, err
	}
	return reply.GetUnreadCount(), nil
}

// GetUnread 查未读数。conversationID 是会话 ID，userID 是用户 ID。
func (c *remoteConversationGRPCClient) GetUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if c.internalSecret == "" {
		return 0, errNoRemoteSecret
	}
	reply, err := c.cli.GetUnread(ctx, &conversationv1.GetUnreadRequest{
		ConversationId: conversationID,
		UserId:         userID,
	})
	if err != nil {
		return 0, err
	}
	return reply.GetUnreadCount(), nil
}

// ClearUnread 清零未读数。conversationID 是会话 ID，userID 是用户 ID。
func (c *remoteConversationGRPCClient) ClearUnread(ctx context.Context, conversationID uint64, userID string) (uint32, error) {
	if c.internalSecret == "" {
		return 0, errNoRemoteSecret
	}
	reply, err := c.cli.ClearUnread(ctx, &conversationv1.ClearUnreadRequest{
		ConversationId: conversationID,
		UserId:         userID,
	})
	if err != nil {
		return 0, err
	}
	return reply.GetUnreadCount(), nil
}

// ListUnread 批量查未读数。userID 是用户 ID。
func (c *remoteConversationGRPCClient) ListUnread(ctx context.Context, userID string) (map[uint64]uint32, error) {
	if c.internalSecret == "" {
		return nil, errNoRemoteSecret
	}
	reply, err := c.cli.ListUnread(ctx, &conversationv1.ListUnreadRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	per := reply.GetPerConversation()
	if per == nil {
		per = map[uint64]uint32{}
	}
	return per, nil
}

// ListConversationMembers 列出会话的全部成员。conversationID 是会话 ID。
// 只对内部服务开放。
func (c *remoteConversationGRPCClient) ListConversationMembers(ctx context.Context, conversationID uint64) ([]string, error) {
	if c.internalSecret == "" {
		return nil, errNoRemoteSecret
	}
	reply, err := c.cli.ListConversationMembers(ctx, &conversationv1.ListConversationMembersRequest{
		ConversationId: conversationID,
	})
	if err != nil {
		return nil, err
	}
	return reply.GetMemberIds(), nil
}

// ListUserConversations 列某用户参与的所有会话 id。userID 是用户 ID。
// 只对内部服务开放。
func (c *remoteConversationGRPCClient) ListUserConversations(ctx context.Context, userID string) ([]uint64, error) {
	if c.internalSecret == "" {
		return nil, errNoRemoteSecret
	}
	reply, err := c.cli.ListUserConversations(ctx, &conversationv1.ListUserConversationsRequest{
		UserId: userID,
	})
	if err != nil {
		return nil, err
	}
	return reply.GetConversationIds(), nil
}

// internalForwarding 把服务间内部凭证注入出站 metadata 的 X-Internal-Token 头。
// secret 内部凭证
func internalForwarding(secret string) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if secret == "" {
				return handler(ctx, req)
			}
			md, _ := metadata.FromOutgoingContext(ctx)
			md = md.Copy()
			md.Set("x-internal-token", secret)
			return handler(metadata.NewOutgoingContext(ctx, md), req)
		}
	}
}

// noopRemoteConversationClient 未配 discovery 时的兜底，所有调用返错误。
// 返错误而不是「假成功」，让 biz 层能拒绝发消息。
type noopRemoteConversationClient struct{}

func (noopRemoteConversationClient) CheckMembership(context.Context, uint64, string) (bool, error) {
	return false, errNoRemoteConversation
}

func (noopRemoteConversationClient) UpdateUnread(context.Context, uint64, string, int32) (uint32, error) {
	return 0, errNoRemoteConversation
}

func (noopRemoteConversationClient) UpdateUnreadBatch(context.Context, uint64, []string, int32) (uint32, error) {
	return 0, errNoRemoteConversation
}

func (noopRemoteConversationClient) GetUnread(context.Context, uint64, string) (uint32, error) {
	return 0, errNoRemoteConversation
}

func (noopRemoteConversationClient) ClearUnread(context.Context, uint64, string) (uint32, error) {
	return 0, errNoRemoteConversation
}

func (noopRemoteConversationClient) ListUnread(context.Context, string) (map[uint64]uint32, error) {
	return nil, errNoRemoteConversation
}

func (noopRemoteConversationClient) ListConversationMembers(context.Context, uint64) ([]string, error) {
	return nil, errNoRemoteConversation
}

func (noopRemoteConversationClient) ListUserConversations(context.Context, string) ([]uint64, error) {
	return nil, errNoRemoteConversation
}
