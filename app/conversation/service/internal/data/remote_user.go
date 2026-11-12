// 本文件是 user service 的远程 gRPC client —— conversation service 跨进程拉用户资料。
package data

import (
	"context"
	"time"

	userv1 "chatroom/api/user/service/v1"
	"chatroom/app/conversation/service/internal/conf"
	"chatroom/pkg/observability"

	oteltrace "github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"go.opentelemetry.io/otel"

	"google.golang.org/grpc/metadata"
)

// RemoteUserClient 暴露 caller 实际用到的方法，保持窄接口。
type RemoteUserClient interface {
	// GetUserByID 拉指定 uid 的公开资料。鉴权由 authForwarding 把当前 token 投到下游。
	GetUserByID(ctx context.Context, callerID, targetID string) (*userv1.UserInfo, error)
	// GetFriendProfiles 批量拉「好友可见」的公开资料，跳过陌生人可见性判定。
	// 走服务间内部凭证（internalForwarding 注入 X-Internal-Token）
	GetFriendProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error)
	// GetConversationMemberProfiles 批量拉「会话成员可见」的公开资料。
	// 走服务间内部凭证（internalForwarding 注入 X-Internal-Token）
	GetConversationMemberProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error)
}

// RemoteUserEndpoint 是 user 服务在 nacos 里的服务名（discovery 查找用）。
// "discovery:///" 前缀让 target 走服务发现；".grpc" 后缀对应 nacos 注册时
// 拼上的 endpoint scheme —— 少一个都会报 "produced zero addresses"。
const RemoteUserEndpoint = "discovery:///chatroom-user.grpc"

// NewRemoteUserClient 构造 RemoteUserClient。discoverer == nil 时返 noop，所有调用降级返 (nil/nil, nil)。
// 超时 1.5s：fan-out 调 N 个，单条拖太久会拖累整批。
func NewRemoteUserClient(discoverer registry.Discovery, auth *conf.Auth, obs *observability.Provider) (RemoteUserClient, func(), error) {
	if discoverer == nil {
		return &noopRemoteUserClient{}, func() {}, nil
	}
	internalSecret := ""
	if auth != nil {
		internalSecret = auth.GetInternalSecret()
	}
	conn, err := grpc.NewClient(
		context.Background(),
		grpc.WithDiscovery(discoverer),
		grpc.WithEndpoint(RemoteUserEndpoint),
		// WithMiddleware 是「赋值」不是「追加」—— 调多次后者会覆盖前者。
		// 用户 JWT 透传、内部凭证、trace 三个中间件必须放进同一次调用。
		grpc.WithMiddleware(
			authForwarding(),
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
	return &remoteUserGRPCClient{cli: userv1.NewUserClient(conn), internalSecret: internalSecret}, cleanup, nil
}

type remoteUserGRPCClient struct {
	cli            userv1.UserClient
	internalSecret string // 服务间内部凭证。空表示未配置，GetFriendProfiles 降级返空
}

// GetUserByID 把 targetID 折成 GetUserByIDRequest；user service 按「看他人」路径处理。
// callerID 不直接使用，识别靠下游解析 token。
func (c *remoteUserGRPCClient) GetUserByID(ctx context.Context, _, targetID string) (*userv1.UserInfo, error) {
	return c.cli.GetUserByID(ctx, &userv1.GetUserByIDRequest{Id: targetID})
}

// GetFriendProfiles 调 user 的好友资料端点。走内部凭证，跳过陌生人可见性。
// 未配置 internalSecret 时降级返空。
func (c *remoteUserGRPCClient) GetFriendProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error) {
	if c.internalSecret == "" {
		return nil, nil
	}
	reply, err := c.cli.GetFriendProfiles(ctx, &userv1.GetFriendProfilesRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	return reply.GetUsers(), nil
}

// GetConversationMemberProfiles 调 user 的会话成员资料端点。走内部凭证。
// 未配置 internalSecret 时降级返空。
func (c *remoteUserGRPCClient) GetConversationMemberProfiles(ctx context.Context, ids []string) ([]*userv1.UserInfo, error) {
	if c.internalSecret == "" {
		return nil, nil
	}
	reply, err := c.cli.GetConversationMemberProfiles(ctx, &userv1.GetConversationMemberProfilesRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	return reply.GetUsers(), nil
}

// authForwarding 把入站 Authorization 投到下游，user service 据此识别 caller。
// ctx 没带 token 时透传 nothing，下游返 401。
func authForwarding() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			ctx = forwardAuth(ctx)
			return handler(ctx, req)
		}
	}
}

// forwardAuth 从入站请求头取 Authorization 写入出站 metadata。
func forwardAuth(ctx context.Context) context.Context {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return ctx
	}
	raw := tr.RequestHeader().Get("Authorization")
	if raw == "" {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set("authorization", raw)
	return metadata.NewOutgoingContext(ctx, md)
}

// noopRemoteUserClient 未配 discovery 时的兜底，所有调用返 nil。
type noopRemoteUserClient struct{}

func (noopRemoteUserClient) GetUserByID(_ context.Context, _, _ string) (*userv1.UserInfo, error) {
	return nil, nil
}

func (noopRemoteUserClient) GetFriendProfiles(_ context.Context, _ []string) ([]*userv1.UserInfo, error) {
	return nil, nil
}

func (noopRemoteUserClient) GetConversationMemberProfiles(_ context.Context, _ []string) ([]*userv1.UserInfo, error) {
	return nil, nil
}

// internalForwarding 把服务间内部凭证注入出站 metadata 的 X-Internal-Token 头。
// secret 为空时不注入任何 header——下游校验会 fail closed 返 403，调用方走降级。
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
