package gateway

import (
	"errors"
	"sync"
	"time"
)

// 准入限制的默认值。
const (
	// defaultHandshakeRate 单 IP 每秒允许的握手次数。
	//
	// 20 次/秒 对正常客户端绰绰有余（重连也就一两次），
	// 但足够挡住「无成本狂刷握手把内存打满」的攻击。
	defaultHandshakeRate = 20

	// defaultHandshakeBurst 单 IP 的突发握手额度。
	//
	// 是额度的两倍 —— 允许客户端短时间重连几次，
	// 但攒不出几百次。
	defaultHandshakeBurst = 40

	// defaultMaxConnections 单实例的总连接数上限。
	//
	// 每条连接占一条 goroutine + 一个写队列（64 条消息槽位），
	// 5000 条连接量级上是几十 MB —— 再多就该交给 HPA 扩容了。
	defaultMaxConnections = 5000

	// defaultMaxConnsPerUser 单用户的连接数上限。
	//
	// 5 条够多端登录（手机 + 平板 + 桌面浏览器 + 备用设备）。
	// 再多就是异常 —— 一个客户端开几百条连接能吃满单用户的配额，
	// 相当于把别人挤下线。
	defaultMaxConnsPerUser = 5
)

// handshakeLimiter 单 IP 的握手限流器 —— 内存令牌桶。
//
// 为什么不用 Redis 令牌桶（pkg/limiter）：
// 握手限流要跟「连接数上限」配合，而连接数是**单实例**的资源 ——
// 连接活在这个进程的内存里。放进 Redis 反而会出事：
// 限流额度变成全集群共享，但连接还是各算各的，限流跟不上实际压力。
// 内存桶的限制是「本实例的连接数」，两者粒度一致。
//
// 为什么不用固定窗口：窗口边界能放行两倍流量 ——
// 卡着边界狂刷握手，两秒内能打进去 2 倍额度的请求。
// 令牌桶匀速发牌，稳态速率卡死。
type handshakeLimiter struct {
	mu sync.Mutex
	// buckets 按 IP 分桶。IP → *ipBucket。
	buckets map[string]*ipBucket
	// rate 每秒补充的令牌数。
	rate float64
	// burst 桶容量（突发额度）。
	burst float64
	// lastGC 上次清理的时间。用来淘汰长时间没活动的桶 ——
	// 不清理的话，每个来过的 IP 都留一条记录，内存被慢慢吃光。
	lastGC time.Time
}

// ipBucket 单个 IP 的令牌桶状态。
type ipBucket struct {
	// tokens 剩余令牌数。
	tokens float64
	// last ��次补充令牌的时间。
	last time.Time
}

// newHandshakeLimiter 构造握手限流器。
// rate 每秒补充的令牌数；<= 0 用默认值。
// burst 桶容量；<= 0 用默认值。
func newHandshakeLimiter(rate, burst int) *handshakeLimiter {
	if rate <= 0 {
		rate = defaultHandshakeRate
	}
	if burst <= 0 {
		burst = defaultHandshakeBurst
	}
	return &handshakeLimiter{
		buckets: make(map[string]*ipBucket),
		rate:    float64(rate),
		burst:   float64(burst),
		lastGC:  time.Now(),
	}
}

// Allow 取一个令牌。ip 是调用方 IP。返回是否放行。
func (l *handshakeLimiter) Allow(ip string) bool {
	if l == nil {
		return true
	}
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.gc(now)

	b, ok := l.buckets[ip]
	if !ok {
		// 新 IP：满桶开始。
		b = &ipBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}

	// 补充令牌。
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gc 清理长时间没活动的桶。调用方必须已持有锁。
//
// 不清理的话，每个来过的 IP 都留一条记录 ——
// 攻击者轮换源 IP 就能把内存吃光，限流器自己成了攻击面。
func (l *handshakeLimiter) gc(now time.Time) {
	// 清理间隔取桶存活期的两倍 —— 一个 IP 安静这么久，
	// 它的桶早就补满了，留着没意义。
	const gcInterval = 2 * time.Minute
	if now.Sub(l.lastGC) < gcInterval {
		return
	}
	l.lastGC = now
	for ip, b := range l.buckets {
		if now.Sub(b.last) > gcInterval {
			delete(l.buckets, ip)
		}
	}
}

// 连接数超限的错误。用 sentinel error 区分两种超限 ——
// 它们给客户端的状态码不一样。
var (
	// errConnLimitTotal 实例总连接数超了。服务端满了 → 503。
	errConnLimitTotal = errors.New("total connection limit reached")
	// errConnLimitUser 单用户连接数超了。客户端自己的配额 → 429。
	errConnLimitUser = errors.New("per-user connection limit reached")
)

// connectionLimiter 连接数配额 —— 单实例的总连接数 + 单用户的连接数。
//
// 配额是**原子预留**的：先占名额，再做 Upgrade。
// Upgrade 失败必须把名额还回去 —— 否则一次失败的握手
// 就永久占掉一个名额，攻击者用假 token 就能把配额耗光。
type connectionLimiter struct {
	mu sync.Mutex
	// total 当前连接数。
	total int
	// perUser 每个用户的连接数。uid → count。
	perUser map[string]int
	// maxTotal 总连接数上限。<= 0 表示不限。
	maxTotal int
	// maxPerUser 单用户连接数上限。<= 0 表示不限。
	maxPerUser int
}

// newConnectionLimiter 构造连接配额。
// maxTotal 总连接数上限；<= 0 用默认值。
// maxPerUser 单用户连接数上限；<= 0 用默认值。
func newConnectionLimiter(maxTotal, maxPerUser int) *connectionLimiter {
	if maxTotal <= 0 {
		maxTotal = defaultMaxConnections
	}
	if maxPerUser <= 0 {
		maxPerUser = defaultMaxConnsPerUser
	}
	return &connectionLimiter{
		perUser:    make(map[string]int),
		maxTotal:   maxTotal,
		maxPerUser: maxPerUser,
	}
}

// reserve 预留一个连接名额。uid 是连接的用户 ID。
// 返回 nil 表示预留成功；返回错误说明超了哪种配额。
func (l *connectionLimiter) reserve(uid string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxTotal {
		return errConnLimitTotal
	}
	if l.perUser[uid] >= l.maxPerUser {
		return errConnLimitUser
	}
	l.total++
	l.perUser[uid]++
	return nil
}

// release 归还一个连接名额。uid 是连接的用户 ID。
// 必须与 reserve 配对 —— Upgrade 失败也要调。
func (l *connectionLimiter) release(uid string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total > 0 {
		l.total--
	}
	if n := l.perUser[uid]; n > 1 {
		l.perUser[uid] = n - 1
	} else {
		delete(l.perUser, uid)
	}
}
