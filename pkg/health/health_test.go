package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"chatroom/pkg/health"
)

// doReady 打一次 readiness，返回状态码和解出来的报告。
func doReady(t *testing.T, c *health.Checker) (int, health.Report) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.Ready()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var report health.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("解析响应体失败: %v, body=%s", err, rec.Body.String())
	}
	return rec.Code, report
}

func TestReadyAllDependenciesOK(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("mysql", true, func(context.Context) error { return nil })
	c.Add("redis", false, func(context.Context) error { return nil })

	code, report := doReady(t, c)
	if code != http.StatusOK {
		t.Fatalf("全部依赖正常应返 200, got %d", code)
	}
	if report.Status != health.StatusOK {
		t.Fatalf("状态应为 ok, got %s", report.Status)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("应报告 2 项检查, got %d", len(report.Checks))
	}
	if report.Checks["mysql"].Status != health.StatusOK {
		t.Fatalf("mysql 应为 ok, got %s", report.Checks["mysql"].Status)
	}
}

// 硬依赖挂了 → 503。这是 readiness 存在的意义：pod 要被摘出 endpoint。
func TestReadyHardDependencyDown(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("mysql", true, func(context.Context) error { return errors.New("connection refused") })
	c.Add("redis", false, func(context.Context) error { return nil })

	code, report := doReady(t, c)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("硬依赖挂了应返 503, got %d", code)
	}
	if report.Status != health.StatusFailed {
		t.Fatalf("状态应为 failed, got %s", report.Status)
	}
	if report.Checks["mysql"].Error == "" {
		t.Fatal("失败项应带上错误原因")
	}
}

// 软依赖挂了 → 仍然 200，但状态是 degraded。
// 这是本次改造最关键的一条：Redis 抖一下不能把 pod 全摘光。
func TestReadySoftDependencyDownStillServes(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("mysql", true, func(context.Context) error { return nil })
	c.Add("redis", false, func(context.Context) error { return errors.New("i/o timeout") })

	code, report := doReady(t, c)
	if code != http.StatusOK {
		t.Fatalf("软依赖挂了应仍返 200, got %d", code)
	}
	if report.Status != health.StatusDegraded {
		t.Fatalf("状态应为 degraded, got %s", report.Status)
	}
	if report.Checks["redis"].Status != health.StatusFailed {
		t.Fatalf("redis 项本身应为 failed, got %s", report.Checks["redis"].Status)
	}
	if report.Checks["mysql"].Status != health.StatusOK {
		t.Fatalf("mysql 不受影响, got %s", report.Checks["mysql"].Status)
	}
}

// 硬软都挂 → 仍然按硬依赖处理（503）。
func TestReadyHardWinsOverSoft(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("mysql", true, func(context.Context) error { return errors.New("down") })
	c.Add("redis", false, func(context.Context) error { return errors.New("down") })

	code, report := doReady(t, c)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("应返 503, got %d", code)
	}
	if report.Status != health.StatusFailed {
		t.Fatalf("状态应为 failed, got %s", report.Status)
	}
}

// liveness 永远 200 —— 依赖挂了也不能让 pod 重启。
func TestLiveAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	health.Live()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness 应恒返 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type 应为 application/json, got %q", ct)
	}
}

// 探测超时算该项失败，但不能拖垮整个报告。
func TestCheckTimeoutCountsAsFailure(t *testing.T) {
	c := health.NewChecker(50 * time.Millisecond)
	c.Add("slow", true, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	code, report := doReady(t, c)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("超时算失败, 应返 503, got %d", code)
	}
	if report.Checks["slow"].Status != health.StatusFailed {
		t.Fatalf("slow 应为 failed, got %s", report.Checks["slow"].Status)
	}
}

// 各检查并行跑：串行的话第一个探测会等到超时，barrier 永远凑不齐。
func TestChecksRunConcurrently(t *testing.T) {
	c := health.NewChecker(2 * time.Second)
	ready := make(chan struct{})
	var count int32
	probe := func(ctx context.Context) error {
		if atomic.AddInt32(&count, 1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.Add("a", true, probe)
	c.Add("b", true, probe)

	code, report := doReady(t, c)
	if code != http.StatusOK {
		t.Fatalf("并行检查应全部通过, got %d (%+v)", code, report.Checks)
	}
}

// nil Checker 不能 panic —— 依赖注入漏了也不该把 server 打死。
func TestNilCheckerIsSafe(t *testing.T) {
	var c *health.Checker
	code, report := doReady(t, c)
	if code != http.StatusOK {
		t.Fatalf("nil checker 应返 200, got %d", code)
	}
	if report.Status != health.StatusOK {
		t.Fatalf("状态应为 ok, got %s", report.Status)
	}
	if names := c.SortedNames(); names != nil {
		t.Fatalf("nil checker 的检查名应为空, got %v", names)
	}
	if _, ok := c.CheckOf("mysql"); ok {
		t.Fatal("nil checker 不应查到检查项")
	}
}

// CheckOf 要能查出来并保留硬 / 软标记。
func TestCheckOfKeepsHardFlag(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("mysql", true, func(context.Context) error { return nil })
	c.Add("redis", false, func(context.Context) error { return nil })

	mysql, ok := c.CheckOf("mysql")
	if !ok {
		t.Fatal("应能查到 mysql")
	}
	if !mysql.Hard {
		t.Fatal("mysql 应为硬依赖")
	}
	redis, ok := c.CheckOf("redis")
	if !ok {
		t.Fatal("应能查到 redis")
	}
	if redis.Hard {
		t.Fatal("redis 应为软依赖")
	}
	if _, ok := c.CheckOf("nope"); ok {
		t.Fatal("不存在的检查名应返回 false")
	}
}

// SortedNames 稳定排序，给可观测性用。
func TestSortedNames(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("redis", false, func(context.Context) error { return nil })
	c.Add("mysql", true, func(context.Context) error { return nil })

	got := c.SortedNames()
	if len(got) != 2 || got[0] != "mysql" || got[1] != "redis" {
		t.Fatalf("SortedNames 应返回排序后的名字, got %v", got)
	}
}

// Add 传 nil probe 不该 panic。
func TestAddIgnoresNilProbe(t *testing.T) {
	c := health.NewChecker(time.Second)
	c.Add("broken", true, nil)
	if names := c.SortedNames(); len(names) != 0 {
		t.Fatalf("nil probe 不应被登记, got %v", names)
	}
}
