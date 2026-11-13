package gateway

import (
	"context"
	"log/slog"
	"os"
	"time"

	"chatroom/app/ws-gateway/service/internal/conf"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// backgroundCtx 用于连接关闭时的清理动作。不用请求 ctx，那时请求可能已取消。
func backgroundCtx() context.Context {
	return context.Background()
}

// ProviderSet 注册网关的构造函数。
var ProviderSet = wire.NewSet(
	NewGatewayConfig,
	NewDependencies,
	NewMemberCacheFromDependencies,
	NewServerFromConfig,
	NewSubscriberForServer,
	NewMemberInvalidationSubscriber,
	NewTokenVerifierFromConfig,
)

// GatewayConfig 网关自己的配置（不在 conf proto 里 —— 这些是网关独有的）。
type GatewayConfig struct {
	InstanceID     string        // 本实例 ID。多实例部署时每个实例一个
	Path           string        // WS 路径
	SendQueueSize  int           // 每连接写队列长度
	PingInterval   time.Duration // ping 间隔
	PongWait       time.Duration // 等 pong 超时
	WriteWait      time.Duration // 写超时
	IdleTimeout    time.Duration // 空闲踢出阈值。0 = 不踢
	OnlineTTL      time.Duration // 在线状态 TTL
	MemberCacheTTL time.Duration // 会话成员缓存有效期
}

// NewGatewayConfig 返回默认配置。
func NewGatewayConfig() GatewayConfig {

	return GatewayConfig{
		InstanceID:     defaultInstanceID(),
		Path:           "/ws",
		SendQueueSize:  defaultSendQueueSize,
		PingInterval:   defaultPingInterval,
		PongWait:       defaultPongWait,
		WriteWait:      defaultWriteWait,
		IdleTimeout:    defaultIdleTimeout,
		OnlineTTL:      defaultOnlineTTL,
		MemberCacheTTL: defaultMemberCacheTTL,
	}
}

// defaultInstanceID 默认实例 ID。优先取 GATEWAY_INSTANCE_ID 环境变量，其次 hostname。
func defaultInstanceID() string {
	if id := os.Getenv("GATEWAY_INSTANCE_ID"); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "gateway"
	}
	return host
}

// Dependencies 网关的依赖包。wire 把这一组打成一个结构体传。
type Dependencies struct {
	Pool    *Pool
	Online  *OnlineReporter
	Members *MemberCache
}

// NewDependencies 构造网关依赖。
// rdb Redis 客户端，为 nil 时不上报在线状态
// members 会话成员查询器，为 nil 时广播推不出去
// cfg 网关配置
func NewDependencies(rdb redis.Cmdable, members MemberFetcher, cfg GatewayConfig) *Dependencies {
	online := NewOnlineReporter(rdb, cfg.InstanceID, cfg.OnlineTTL)

	// pool 先声明，闭包在调用时才求值，那时 pool 已经赋值了
	var pool *Pool
	pool = NewPool(func(userID string, c *Connection) {
		// 还有别的连接就不算离线
		if pool != nil && pool.Count(userID) > 0 {
			return
		}
		online.MarkOffline(backgroundCtx(), userID)
	})

	return &Dependencies{
		Pool:    pool,
		Online:  online,
		Members: NewMemberCache(members, cfg.MemberCacheTTL),
	}
}

// NewMemberCacheFromDependencies 暴露依赖包里的成员缓存。
// deps 依赖包
// 成员失效通知的处理要拿到同一个缓存实例 —— 否则失效的是另一个 map。
func NewMemberCacheFromDependencies(deps *Dependencies) *MemberCache {
	return deps.Members
}

// NewServerFromConfig 构造网关服务。
// cfg 网关配置
// deps 依赖包
// sender 消息转发器
// verify 握手鉴权器
// convs 「列某用户参与的所有会话」的能力
// lister 增量拉消息的能力
// logger 日志器
func NewServerFromConfig(cfg GatewayConfig, deps *Dependencies, sender MessageSender, verify TokenVerifier, convs UserConversationLister, lister MessageLister, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Path == "" {
		cfg.Path = "/ws"
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = defaultInstanceID()
	}
	srv := NewServer(Config{
		InstanceID:    cfg.InstanceID,
		Path:          cfg.Path,
		SendQueueSize: cfg.SendQueueSize,
		WriteWait:     cfg.WriteWait,
		PongWait:      cfg.PongWait,
		PingInterval:  cfg.PingInterval,
		IdleTimeout:   cfg.IdleTimeout,
	}, deps.Pool, sender, verify, deps.Members, deps.Online, logger)
	srv.WithUserConversations(convs) // 上线/下线时通知会话里的同伴
	srv.WithMessageLister(lister)    // 断线重连补回漏掉的消息
	return srv
}

// NewSubscriberForServer 构造 Redis 订阅器，回调把消息推给网关。
// gw 网关
// rdb Redis 客户端，为 nil 时返 nil
// logger 日志器
func NewSubscriberForServer(gw *Server, rdb *redis.Client, logger *slog.Logger) *Subscriber {

	if logger == nil {
		logger = slog.Default()
	}
	return NewSubscriber(rdb, func(channel string, payload []byte) {
		gw.HandleBroadcast(payload)
	})
}

// NewTokenVerifierFromConfig 构造 token 校验器。
// auth 鉴权配置段
// rdb Redis 客户端，为 nil 时不查黑名单（降级放行）
func NewTokenVerifierFromConfig(auth *conf.Auth, rdb *redis.Client) TokenVerifier {
	return NewJWTVerifier(auth.GetJwtSecret(), auth.GetTokenIssuer(), NewBlacklistCheckerFromRedis(rdb))
}
