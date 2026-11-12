// Package health 提供服务的健康检查端点。
//
// 两个端点，语义完全不同，别混：
//
//   - liveness（/healthz）——「进程还活着吗」。只看进程本身，
//     不碰任何外部依赖。依赖挂了**绝不能**让它失败：重启 pod
//     解决不了 MySQL 宕机，只会把 pod 搞成 CrashLoopBackOff，
//     故障面反而更大。
//
//   - readiness（/readyz）——「这个 pod 现在能接流量吗」。检查
//     关键依赖。依赖挂了就把这个 pod 从 Service 的 endpoint 里摘掉，
//     流量不再往这儿打。
//
// 每个依赖分「硬 / 软」两类，判据只有一条：**这个依赖挂了，
// 服务还能不能正确处理请求**。
//
//   - 硬依赖：挂了服务什么都做不了 → readiness 失败（503）。
//     本项目的 MySQL 是硬依赖。
//   - 软依赖：挂了服务降级但还能用 → 只算 degraded，readiness 仍然 200。
//     本项目的 Redis 在 4 个 DB 服务里是软依赖：不可达时限频 / 黑名单 /
//     缓存都降级放行，跟代码里的降级约定一致。
//
// 把软依赖做成硬依赖会让 Redis 一抖整个集群摘光；把硬依赖做成软依赖
// 则会让流量继续打进一个什么都做不了的 pod。分清楚这两者是重点。
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v3/log"
)

// Status 检查状态。
type Status string

const (
	// StatusOK 正常。
	StatusOK Status = "ok"
	// StatusDegraded 依赖降级 —— 服务还能对外服务，但有功能不可用。
	StatusDegraded Status = "degraded"
	// StatusFailed 不可用 —— 服务接不了流量。
	StatusFailed Status = "failed"
)

// DefaultTimeout 单项依赖检查的默认超时。
//
// 比 HTTP server 的 timeout 短：探针应该快速给出结论，
// 不能跟客户端请求抢连接池。
const DefaultTimeout = 2 * time.Second

// Check 一个依赖的检查。
type Check struct {
	// Name 检查名，进响应体。
	Name string
	// Hard=true → 检查失败 = 服务不可用（readiness 返回 503）。
	// Hard=false → 检查失败只算降级（readiness 仍然 200）。
	Hard bool
	// Probe 探测函数。返回 error 即视为检查失败。
	Probe func(ctx context.Context) error
}

// Result 单项检查的结果。
type Result struct {
	Status Status `json:"status"`
	// LatencyMS 该项检查耗时（毫秒）。
	LatencyMS int64 `json:"latency_ms"`
	// Error 失败原因。成功时为空。
	Error string `json:"error,omitempty"`
}

// Report 一次就绪检查的完整结果。
type Report struct {
	// Status 整体状态。
	Status Status `json:"status"`
	// Checks 各项检查的结果，key 是检查名。
	Checks map[string]Result `json:"checks"`
}

// Checker 依赖检查集合。并发安全。
type Checker struct {
	timeout time.Duration

	mu     sync.RWMutex
	checks []Check

	// lastStatus 上一次的整体状态，用来避免探针每 10 秒打一次
	// 日志把日志刷爆 —— 只在状态变化时记一条。
	lastStatus Status
}

// NewChecker 构造 Checker。timeout <= 0 时用 DefaultTimeout。
func NewChecker(timeout time.Duration, checks ...Check) *Checker {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Checker{timeout: timeout, checks: checks, lastStatus: StatusOK}
}

// Add 追加一个依赖检查。
func (c *Checker) Add(name string, hard bool, probe func(ctx context.Context) error) {
	if c == nil || probe == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, Check{Name: name, Hard: hard, Probe: probe})
}

// Run 跑一遍所有检查，返回整体结果。
//
// 各检查**并行**跑：单个依赖的超时不会把总耗时拖成 N 倍，
// 探针才能在 timeoutSeconds 内给出结论。
func (c *Checker) Run(ctx context.Context) Report {
	if c == nil {
		return Report{Status: StatusOK, Checks: map[string]Result{}}
	}
	c.mu.RLock()
	checks := make([]Check, len(c.checks))
	copy(checks, c.checks)
	c.mu.RUnlock()

	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i := range checks {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = c.runOne(ctx, checks[idx])
		}(i)
	}
	wg.Wait()

	report := Report{Status: StatusOK, Checks: make(map[string]Result, len(checks))}
	hardFailed := false
	softFailed := false

	for i, check := range checks {
		result := results[i]
		report.Checks[check.Name] = result
		switch {
		case result.Status == StatusOK:
		case check.Hard:
			hardFailed = true
		default:
			softFailed = true
		}
	}

	switch {
	case hardFailed:
		report.Status = StatusFailed
	case softFailed:
		report.Status = StatusDegraded
	default:
		report.Status = StatusOK
	}
	return report
}

// runOne 跑单项检查。单次超时不拖累整份报告 —— 超时算这项失败。
func (c *Checker) runOne(ctx context.Context, check Check) Result {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	err := check.Probe(ctx)
	result := Result{Status: StatusOK, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		result.Status = StatusFailed
		result.Error = err.Error()
	}
	return result
}

// Ready 返回 readiness handler。
//
// 硬依赖全过 → 200；软依赖挂了 → 200 但状态是 degraded；
// 硬依赖挂了 → 503。
func (c *Checker) Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := c.Run(r.Context())
		c.logTransition(report)

		w.Header().Set("Content-Type", "application/json")
		if report.Status == StatusFailed {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(report)
	}
}

// logTransition 只在整体状态变化时记一条日志。
//
// 探针每 10 秒打一次，状态不变也每次都记的话，日志会被探针刷爆，
// 真正的故障信息反而看不见。
func (c *Checker) logTransition(report Report) {
	if c == nil {
		return
	}
	c.mu.Lock()
	changed := c.lastStatus != report.Status
	c.lastStatus = report.Status
	c.mu.Unlock()
	if !changed {
		return
	}
	switch report.Status {
	case StatusFailed:
		log.Error("readiness 失败，服务已从负载均衡摘除", "checks", report.Checks)
	case StatusDegraded:
		log.Warn("readiness 降级，服务仍可接流量", "checks", report.Checks)
	default:
		log.Info("readiness 恢复正常", "checks", report.Checks)
	}
}

// Live 返回 liveness handler。
//
// 只报进程活着，不碰依赖 —— 依赖挂了也不能让 liveness 失败。
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": string(StatusOK)})
	}
}

// SortedNames 返回检查名（排序后），给调用方做稳定输出用。
func (c *Checker) SortedNames() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.checks))
	for _, check := range c.checks {
		names = append(names, check.Name)
	}
	sort.Strings(names)
	return names
}

// CheckOf 按名字查一项检查。
//
// 给调用方做断言用：硬 / 软依赖的划分是「策略」，写在各自服务的
// NewHealthChecker 里，这里把它暴露出来才能被测试守住 —— 比如
// 「Redis 只能算软依赖」这条约定被改坏时，测试要能立刻发现。
func (c *Checker) CheckOf(name string) (Check, bool) {
	if c == nil {
		return Check{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, check := range c.checks {
		if check.Name == name {
			return check, true
		}
	}
	return Check{}, false
}
