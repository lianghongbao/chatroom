package data

import (
	"testing"
)

// 依赖分类是回归点，不是随手写的。
//
// 把 Redis 从软依赖改成硬依赖，症状是「Redis 一抖，所有服务的 pod 全被摘光」——
// 故障面比降级本身大得多。MySQL 反过来：从硬改成软，
// 服务会带着「什么都读不了」的状态继续接流量。
//
// 所以这两个分类必须被测试钉住。
func TestNewHealthChecker_DependencyClassification(t *testing.T) {
	c := NewHealthChecker(nil)

	cases := []struct {
		name string
		hard bool
	}{
		// MySQL 是硬依赖：没有它什么都读不了写不了。
		{"mysql", true},
		// Redis 是软依赖：限频、黑名单、缓存、广播都是降级放行。
		{"redis", false},
	}

	for _, tc := range cases {
		check, ok := c.CheckOf(tc.name)
		if !ok {
			t.Errorf("少了 %q 的检查项", tc.name)
			continue
		}
		if check.Hard != tc.hard {
			t.Errorf("%q 的硬软分类错了：hard = %v，期望 %v", tc.name, check.Hard, tc.hard)
		}
	}
}

// 没配数据库时探针要返 error，不能返 nil ——
// 返 nil 等于告诉 K8s「我准备好了」，但这个服务其实什么都干不了。
func TestPingMySQL_NotConfigured(t *testing.T) {
	var d *Data
	if err := d.PingMySQL(t.Context()); err == nil {
		t.Error("没配 mysql 时 PingMySQL 该返 error")
	}
}

// Redis 同理：没配时探针要失败。
func TestPingRedis_NotConfigured(t *testing.T) {
	var d *Data
	if err := d.PingRedis(t.Context()); err == nil {
		t.Error("没配 redis 时 PingRedis 该返 error")
	}
}

// 检查项名称要稳定 —— K8s 探针和监控面板按名字匹配。
func TestNewHealthChecker_CheckNames(t *testing.T) {
	c := NewHealthChecker(nil)
	names := c.SortedNames()
	want := []string{"mysql", "redis"}
	if len(names) != len(want) {
		t.Fatalf("检查项 = %v，期望 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("检查项[%d] = %q，期望 %q", i, names[i], want[i])
		}
	}
}
