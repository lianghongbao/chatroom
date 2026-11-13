package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultSendQueueSize = 64
	defaultOnlineTTL     = 90 * time.Second
	defaultWriteWait     = 10 * time.Second
	// 超过这个时间没收到任何数据就判定连接死了
	defaultPongWait = 60 * time.Second
	// ping 间隔，必须比 pongWait 小
	defaultPingInterval = 25 * time.Second
	defaultIdleTimeout  = 2 * time.Minute
)

// TokenVerifier 校验握手 token 返用户 uid。
type TokenVerifier interface {
	// Verify 校验 raw 是原始 token，返回用户 ID。
	Verify(raw string) (string, error)
}

// MessageSender 发消息到 message 服务（网关不落库，只转发）。
type MessageSender interface {
	// Send 转发一条消息。token 是发起方的 JWT，message 服务靠它识别发消息的人；
	// senderID 是发送者 uid；msgType 是消息类型；content 是消息内容。
	// 返回落库后的消息 id 与创建时间。
	Send(ctx context.Context, token string, senderID string, conversationID uint64, msgType uint8, content string) (messageID uint64, createdAt int64, err error)
}

// MessageLister 从 message 服务拉消息（断线重连补消息用）。
type MessageLister interface {
	// ListSince 拉 id 严格大于 sinceID 的消息，按 id 正序；sinceID = 0 表示从头拉。
	// token 是发起方的 JWT。返回消息列表、最后一条消息 id、是否还有更多。
	ListSince(ctx context.Context, token string, conversationID, sinceID uint64, limit int32) (msgs []MessageInfo, lastID uint64, hasMore bool, err error)
}

// MessageInfo 网关侧的最小消息视图。
type MessageInfo struct {
	ID             uint64
	ConversationID uint64
	SenderID       string
	Type           uint8 // 1=文本 2=图片
	Content        string
	CreatedAt      int64
}

// Config 网关配置。
type Config struct {
	JWTSecret     string
	TokenIssuer   string
	InstanceID    string // 本实例 ID，写进 Redis 的在线状态值
	Path          string
	SendQueueSize int
	WriteWait     time.Duration
	PongWait      time.Duration
	PingInterval  time.Duration
	IdleTimeout   time.Duration
}

// Server WebSocket 网关。
type Server struct {
	cfg     Config
	pool    *Pool
	sender  MessageSender
	verify  TokenVerifier
	members *MemberCache
	logger  *slog.Logger

	upgrader websocket.Upgrader

	online        *OnlineReporter        // 可为 nil（不上报）
	presence      *Presence              // 可为 nil（查不到在线状态）
	conversations UserConversationLister // 为 nil 时不通知在线状态变化
	lister        MessageLister          // 可为 nil（不支持补消息）

	wg sync.WaitGroup
}

// NewServer 构造网关。
// cfg 网关配置
// pool 连接池
// sender 消息转发器
// verify 握手鉴权器
// members 会话成员缓存，算广播目标用，为 nil 时不广播
// online 在线状态上报器，为 nil 时不上报在线状态
// logger 日志器
func NewServer(cfg Config, pool *Pool, sender MessageSender, verify TokenVerifier, members *MemberCache, online *OnlineReporter, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Path == "" {
		cfg.Path = "/ws"
	}
	if cfg.SendQueueSize <= 0 {
		cfg.SendQueueSize = defaultSendQueueSize
	}
	if cfg.WriteWait <= 0 {
		cfg.WriteWait = defaultWriteWait
	}
	if cfg.PongWait <= 0 {
		cfg.PongWait = defaultPongWait
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = "gateway"
	}
	return &Server{
		cfg:      cfg,
		pool:     pool,
		sender:   sender,
		verify:   verify,
		members:  members,
		logger:   logger,
		online:   online,
		presence: NewPresence(online, pool, members),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// 放开同源检查 —— 准入由握手鉴权把关
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// WithUserConversations 注入「列某用户参与的所有会话」的能力。
// l 是列会话的能力实现，为 nil 时不通知在线状态变化。
func (s *Server) WithUserConversations(l UserConversationLister) *Server {
	if s == nil {
		return s
	}
	s.conversations = l
	return s
}

// WithMessageLister 注入「拉增量消息」的能力。
// l 是拉增量消息的能力实现，为 nil 时 resume 帧回「服务不可用」。
func (s *Server) WithMessageLister(l MessageLister) *Server {
	if s == nil {
		return s
	}
	s.lister = l
	return s
}

// Handler 返回 WS 的 HTTP handler。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleWS)
}

// Path WS 路径。默认 /ws。
func (s *Server) Path() string {
	return s.cfg.Path
}

// handleWS 处理 WS 握手 + 连接生命周期。w 是响应写入器，r 是 HTTP 请求。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// token 在 query 里（浏览器 WebSocket API 不能自定义 header）
	raw := r.URL.Query().Get("token")
	if raw == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	if s.verify == nil {
		http.Error(w, "auth not configured", http.StatusInternalServerError)
		return
	}
	uid, err := s.verify.Verify(raw)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 失败时它已经写过响应了
		s.logger.WarnContext(r.Context(), "WebSocket 升级失败", "err", err)
		return
	}

	conn := NewConnection(ws, uid, raw, s.cfg.SendQueueSize)
	// 只有第一条连接才广播「上线」
	wasOffline := s.pool.Count(uid) == 0
	s.pool.Add(conn)
	s.online.MarkOnline(r.Context(), uid)

	s.logger.InfoContext(r.Context(), "WebSocket 已连接",
		"user_id", uid, "instance", s.cfg.InstanceID, "conns", s.pool.Count(uid))

	// 放协程里 —— 同步做会拖慢握手
	if wasOffline {
		go s.NotifyOnlineStateChange(uid, true)
	}

	s.wg.Add(1)
	defer func() {
		s.wg.Done()
		// Remove 会触发 pool 的 onClose 回调 —— 那里已经判断了
		// 「还有别的连接就不算离线」，在线状态由它统一管。这里不要再来一次 MarkOffline。
		s.pool.Remove(conn)
		// 还有别的连接就不算离线
		nowOffline := s.pool.Count(uid) == 0
		conn.Close()
		s.logger.InfoContext(r.Context(), "WebSocket 已断开",
			"user_id", uid, "instance", s.cfg.InstanceID)
		if nowOffline {
			go s.NotifyOnlineStateChange(uid, false)
		}
	}()

	// 读协程在这里阻塞，写协程单独跑
	go s.writePump(conn)
	s.readPump(conn)
}

// readPump 读协程：处理客户端上行帧 + 响应 pong。conn 是当前连接。
func (s *Server) readPump(conn *Connection) {
	defer conn.Close()

	conn.conn.SetReadLimit(maxMessageSize)
	// 设置读超时（配合 Ping/Pong 做心跳）
	_ = conn.conn.SetReadDeadline(time.Now().Add(s.cfg.PongWait))
	// 收到 pong 就续期读超时 + 更新活跃时间
	conn.conn.SetPongHandler(func(string) error {
		conn.Touch()
		return conn.conn.SetReadDeadline(time.Now().Add(s.cfg.PongWait))
	})

	for {
		msgType, payload, err := conn.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.logger.WarnContext(context.Background(), "WebSocket 读取异常", "err", err, "user_id", conn.UserID)
			}
			return
		}
		conn.Touch()
		_ = conn.conn.SetReadDeadline(time.Now().Add(s.cfg.PongWait))

		if msgType != websocket.TextMessage {
			continue // 只认文本帧
		}
		s.handleFrame(conn, payload)
	}
}

// handleFrame 处理一帧客户端上行消息。conn 是当前连接，payload 是帧原文。
func (s *Server) handleFrame(conn *Connection, payload []byte) {
	frame, err := DecodeClientFrame(payload)
	if err != nil {
		conn.Send(EncodeErrorFrame("malformed frame", "INVALID_FRAME"))
		return
	}

	switch frame.Type {
	case FramePing:
		conn.Send(EncodePongFrame())
	case FrameChat:
		// 转发到 message 服务是同步调用（最多等 5s），放协程里跑。
		// 不放的话读协程会被卡住，收不到任何后续帧（包括 pong）。
		s.dispatchAsync(conn, func() { s.handleChat(conn, frame) })
	case FrameStatus:
		// 查在线状态要问下游（Redis / conversation 服务），同样可能慢。
		s.dispatchAsync(conn, func() { s.handleStatus(conn, frame) })
	case FrameResume: // 断线重连后补消息
		// 补消息要拉下游，最坏一页 50 条。
		s.dispatchAsync(conn, func() { s.handleResume(conn, frame) })
	default:
		conn.Send(EncodeErrorFrame("unsupported frame type: "+string(frame.Type), "UNSUPPORTED_FRAME"))
	}
}

// dispatchAsync 把要访问下游的帧处理丢到协程里跑。conn 是当前连接，
// fn 是处理函数。只对会阻塞的帧类型用；ping / pong 必须同步回。
func (s *Server) dispatchAsync(conn *Connection, fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

// handleStatus 处理查在线状态请求。conn 是当前连接，frame 是帧原文。
// 带 conversation_id 查会话成员，带 user_id 查单个人。
func (s *Server) handleStatus(conn *Connection, frame *ClientFrame) {
	if s.presence == nil {
		conn.Send(EncodeErrorFrame("presence unavailable", "SERVICE_UNAVAILABLE"))
		return
	}

	if frame.UserID != "" {
		status := s.presence.Status(context.Background(), frame.UserID)
		conn.Send(EncodeOnlineFrame(status.UserID, status.Online))
		return
	}

	if frame.ConversationID == 0 {
		conn.Send(EncodeErrorFrame("conversation_id or user_id is required", "INVALID_ARGUMENT"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	statuses, err := s.presence.ConversationStatus(ctx, frame.ConversationID)
	if err != nil {
		s.logger.WarnContext(ctx, "查在线状态失败", "err", err, "conversation_id", frame.ConversationID)
		conn.Send(EncodeErrorFrame("presence unavailable", "SERVICE_UNAVAILABLE"))
		return
	}
	conn.Send(EncodePresenceFrame(statuses))
}

// handleResume 处理断线重连后的补消息：客户端报「我最后看到的是 id=X」，
// 网关问 message 服务「X 之后有什么」，按顺序推给客户端，再回结果帧。
// conn 是当前连接，frame 的 conversation_id 是会话 id，last_message_id 是基准消息 id。
func (s *Server) handleResume(conn *Connection, frame *ClientFrame) {
	if frame.ConversationID == 0 {
		conn.Send(EncodeErrorFrame("conversation_id is required", "INVALID_ARGUMENT"))
		return
	}
	if s.lister == nil {
		conn.Send(EncodeErrorFrame("message service unavailable", "SERVICE_UNAVAILABLE"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgs, lastID, hasMore, err := s.lister.ListSince(ctx, conn.Token, frame.ConversationID, frame.LastMessageID, resumePageSize)
	if err != nil {
		s.logger.WarnContext(ctx, "补消息失败", "err", err, "user_id", conn.UserID, "conversation_id", frame.ConversationID)
		conn.Send(EncodeErrorFrame(clientFacingSendError(err), "RESUME_FAILED"))
		return
	}

	for i := range msgs {
		m := msgs[i]
		conn.Send(EncodeMessageFrame(&MessageData{
			MessageID:      m.ID,
			ConversationID: m.ConversationID,
			SenderID:       m.SenderID,
			MsgType:        m.Type,
			Content:        m.Content,
			CreatedAt:      m.CreatedAt,
		}))
	}

	conn.Send(EncodeResumeResultFrame(&ResumeData{
		ConversationID: frame.ConversationID,
		Count:          len(msgs),
		LastMessageID:  lastID,
		HasMore:        hasMore,
	}))
	s.logger.InfoContext(ctx, "补消息完成",
		"user_id", conn.UserID, "conversation_id", frame.ConversationID,
		"count", len(msgs), "last_message_id", lastID, "has_more", hasMore)
}

// 一次补消息的最大条数。不能超过写队列容量，否则补的消息会被丢掉
const resumePageSize = 50

// handleChat 处理客户端发来的聊天帧 —— 转发到 message 服务落库。
// conn 是当前连接，frame 的 conversation_id 是会话 id，msg_type 是消息类型，
// content 是消息内容。
func (s *Server) handleChat(conn *Connection, frame *ClientFrame) {
	if frame.ConversationID == 0 {
		conn.Send(EncodeErrorFrame("conversation_id is required", "INVALID_ARGUMENT"))
		return
	}
	if frame.Content == "" {
		conn.Send(EncodeErrorFrame("content is required", "INVALID_ARGUMENT"))
		return
	}
	if s.sender == nil {
		conn.Send(EncodeErrorFrame("message service unavailable", "SERVICE_UNAVAILABLE"))
		return
	}

	// 不能用连接的 ctx —— readPump 返回时会被取消，而发消息要等它落库
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgID, createdAt, err := s.sender.Send(ctx, conn.Token, conn.UserID, frame.ConversationID, frame.MsgType, frame.Content)
	if err != nil {
		s.logger.WarnContext(ctx, "消息发送失败", "err", err, "user_id", conn.UserID, "conversation_id", frame.ConversationID)
		conn.Send(EncodeErrorFrame(clientFacingSendError(err), "SEND_FAILED"))
		return
	}

	// 消息本身由 Redis Pub/Sub 广播回来，这里只回 ack
	conn.Send(EncodeAckFrame(&AckData{
		MessageID:      msgID,
		ConversationID: frame.ConversationID,
		CreatedAt:      createdAt,
	}))
}

// writePump 写协程：唯一的写者。conn 是当前连接。
// gorilla/websocket 不允许并发写，其它地方都通过 Send 往队列塞。
// 这里只写不设读超时 —— 读超时一触发连接就永久废了。
func (s *Server) writePump(conn *Connection) {
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case <-conn.closed:
			return
		// 从队列取出 payload，通过 WebSocket 发给客户端
		case payload := <-conn.send:
			_ = conn.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteWait))
			if err := conn.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		// 定期发送 Ping 帧，检测客户端是否还活着
		case <-ticker.C:
			// 控制层 ping：让客户端回 pong，顺便刷新读超时
			_ = conn.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteWait))
			if err := conn.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// 单帧最大字节数，防止有人拿 WS 发大包把网关内存打满
const maxMessageSize = 64 * 1024

// HandleBroadcast 处理一条从 Redis Pub/Sub 收到的消息，推给本实例上的目标连接。
// payload 是广播体原文。广播目标靠内部端点列成员 —— 查不到成员就丢弃。
func (s *Server) HandleBroadcast(payload []byte) {
	msg, err := DecodePublishPayload(payload)
	if err != nil {
		s.logger.Warn("广播体解析失败", "err", err)
		return
	}
	if s.members == nil {
		s.logger.Warn("缺少会话成员查询能力，广播丢弃", "conversation_id", msg.ConversationID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	memberIDs, err := s.members.Get(ctx, msg.ConversationID)
	if err != nil {
		s.logger.Warn("查会话成员失败，广播丢弃", "err", err, "conversation_id", msg.ConversationID)
		return
	}
	s.BroadcastToMembers(memberIDs, msg.SenderID, payload)
}

// BroadcastToMembers 把消息推给会话的所有成员（除发送方）。
// memberIDs 是会话成员列表，由调用方从 conversation 服务取；
// senderID 是发送方；payload 是广播体原文。
func (s *Server) BroadcastToMembers(memberIDs []string, senderID string, payload []byte) {
	msg, err := DecodePublishPayload(payload)
	if err != nil {
		s.logger.Warn("广播体解析失败", "err", err)
		return
	}
	frame := EncodeMessageFrame(&MessageData{
		MessageID:      msg.MessageID,
		ConversationID: msg.ConversationID,
		SenderID:       msg.SenderID,
		MsgType:        msg.Type,
		Content:        msg.Content,
		CreatedAt:      msg.CreatedAt,
	})

	for _, uid := range memberIDs {
		// 发送方已经收到 ack 了
		if uid == senderID {
			continue
		}
		for _, conn := range s.pool.Get(uid) {
			conn.Send(frame)
		}
	}
}

// StartBackground 启动空闲连接清理任务，ctx 结束时任务退出。返回一个停止函数。
func (s *Server) StartBackground(ctx context.Context) func() {
	if s.cfg.IdleTimeout <= 0 {
		return func() {}
	}
	interval := s.cfg.IdleTimeout / 2
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if n := s.pool.EvictIdle(s.cfg.IdleTimeout); n > 0 {
					s.logger.Info("踢掉空闲连接", "count", n, "idle_timeout", s.cfg.IdleTimeout)
				}
			}
		}
	}()
	return func() { close(done) }
}

// Shutdown 关掉所有连接，等它们结束。
func (s *Server) Shutdown() {
	s.pool.CloseAll()
}

// clientFacingSendError 把下游错误翻成给客户端看的话。
// 跨进程后拿到的是 gRPC status 错误，所以按 gRPC 码判：
// PermissionDenied → 不是会话成员，InvalidArgument → 消息非法，
// NotFound → 会话不存在，Unavailable → message 服务不可用，其余一律 send failed。
func clientFacingSendError(err error) string {
	if err == nil {
		return "send failed"
	}
	st, ok := status.FromError(err)
	if !ok {
		return "send failed"
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return "you are not a member of this conversation"
	case codes.InvalidArgument:
		return "invalid message"
	case codes.NotFound:
		return "conversation not found"
	case codes.Unavailable:
		return "message service unavailable"
	}
	return "send failed"
}

// Wait 等待所有连接结束。
func (s *Server) Wait() {
	s.wg.Wait()
}

// OnlineCount 当前在线用户数。
func (s *Server) OnlineCount() int {
	return s.pool.OnlineCount()
}

// ConnectionCount 当前连接数。
func (s *Server) ConnectionCount() int {
	return s.pool.Total()
}

// formatInt64 便于日志输出的 int64 字符串。
// v 要格式化的值
func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
