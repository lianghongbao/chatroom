package gateway

import (
	"context"
	"time"
)

// Presence 在线状态查询。
// 分两层：全局在线（Redis 的 online:{uid}，跨实例可见）和本实例在线（连接池里查得到）。
type Presence struct {
	// online 全局在线状态。
	online *OnlineReporter
	// pool 本实例的连接池。
	pool *Pool
	// members 会话成员查询（算「这个会话里谁在线」用）。
	members *MemberCache
	// timeout 查 Redis 的超时。
	timeout time.Duration
}

// defaultPresenceTimeout 查在线状态的默认超时。查不到就当离线，不值得让客户端等太久。
const defaultPresenceTimeout = 500 * time.Millisecond

// NewPresence 构造 Presence。online / pool / members 为 nil 时相应查询不可用。
func NewPresence(online *OnlineReporter, pool *Pool, members *MemberCache) *Presence {
	return &Presence{
		online:  online,
		pool:    pool,
		members: members,
		timeout: defaultPresenceTimeout,
	}
}

// UserStatus 某个用户的在线状态。
type UserStatus struct {
	UserID string `json:"user_id"`
	Online bool   `json:"online"`
	//
	// Online 为 true 但 Local 为 false —— 他连在别的网关实例上
	Local bool `json:"local"`
}

// Status 查单个用户的在线状态，userID 是用户 ID。
func (p *Presence) Status(ctx context.Context, userID string) UserStatus {
	if p == nil {
		return UserStatus{UserID: userID}
	}
	status := UserStatus{UserID: userID}

	// 本实例的连接池先查 —— 不用走网络
	if p.pool != nil {
		status.Local = p.pool.Count(userID) > 0
	}
	// 本实例有连接就一定在线，不用问 Redis
	if status.Local {
		status.Online = true
		return status
	}
	if p.online == nil {
		return status
	}

	// 查 Redis 要有超时 —— 在线状态查不到就当离线
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	status.Online = p.online.IsOnline(cctx, userID)
	return status
}

// ConversationStatus 查一个会话里各成员的在线状态，conversationID 是会话 ID。
// 返 nil 表示查不了（成员列表拿不到）—— 跟「没人在线」要区分开。
func (p *Presence) ConversationStatus(ctx context.Context, conversationID uint64) ([]UserStatus, error) {
	if p == nil || p.members == nil {
		return nil, nil
	}
	members, err := p.members.Get(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}
	out := make([]UserStatus, 0, len(members))
	for _, uid := range members {
		out = append(out, p.Status(ctx, uid))
	}
	return out, nil
}

// PresenceData 会话级在线状态帧的载荷。
type PresenceData struct {
	Members map[string]bool `json:"members"`
}

// EncodePresenceFrame 编一个会话级在线状态帧，statuses 是各成员的在线状态。
func EncodePresenceFrame(statuses []UserStatus) []byte {
	members := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		members[s.UserID] = s.Online
	}
	b, err := EncodeServerFrame(FramePresence, &PresenceData{Members: members})
	if err != nil {
		// 兜底：返回一个手写的最小帧，让客户端至少知道出错了
		return []byte(`{"type":"presence","data":{"members":{}}}`)
	}
	return b
}

// NotifyOnlineStateChange 通知会话同伴「某人的在线状态变了」。
// userID 是状态变化的用户，online 是变化后的状态。
// 流程：查出这个人在哪些会话 → 每个会话取出其它成员 → 推 online 帧。
// 返回推给了几个人。出错只记日志不返回错误。
func (s *Server) NotifyOnlineStateChange(userID string, online bool) int {
	if s == nil || s.pool == nil || s.presence == nil {
		return 0
	}
	// 不能按「他是否还在本实例」判断 —— 本实例上可能还有别人要收到通知
	if s.conversations == nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	convIDs, err := s.conversations.ListUserConversations(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "查用户会话失败", "err", err, "user_id", userID)
		return 0
	}

	frame := EncodeOnlineFrame(userID, online)
	notified := 0
	for _, convID := range convIDs {
		members, err := s.members.Get(ctx, convID)
		if err != nil {
			s.logger.WarnContext(ctx, "查会话成员失败", "err", err, "conversation_id", convID)
			continue
		}
		for _, uid := range members {
			// 不通知自己
			if uid == userID {
				continue
			}
			// 推给这个用户的所有连接 —— 他可能开了好几个端
			for _, conn := range s.pool.Get(uid) {
				if conn.Send(frame) {
					notified++
				}
			}
		}
	}
	return notified
}
