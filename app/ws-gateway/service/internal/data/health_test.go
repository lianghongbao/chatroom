package data

import (
	"testing"
)

// 依赖分类是回归点，不是随手写的。
//
// 对 ws-gateway 来说 Redis 是硬依赖：Pub/Sub 是消息投递给 WS 客户端的唯一通道。
// 把它改成软依赖，症状是「带着死掉的订阅继续接新连接」，
// 客户端永远等不到消息，而且没有报错 —— 更难查。
func TestNewHealthChecker_DependencyClassification(t *testing.T) {
	c := NewHealthChecker(nil)

	// Redis 是硬依赖：Pub/Sub 是消息投递的唯一通道，快速失败更好。
	check, ok := c.CheckOf("redis")
	if !ok {
		t.Fatal("少了 redis 的检查项")
	}
	if !check.Hard {
		t.Error("redis 该是硬依赖 —— Pub/Sub 挂了消息就投不出去")
	}
}

// ws-gateway 没有 MySQL —— 不该有 mysql 检查项。
func TestNewHealthChecker_NoMySQL(t *testing.T) {
	c := NewHealthChecker(nil)
	if _, ok := c.CheckOf("mysql"); ok {
		t.Error("ws-gateway 不用 MySQL，不该有 mysql 检查项")
	}
}

// 没配 Redis 时探针要返 error，不能返 nil ——
// 返 nil 等于告诉 K8s「我准备好了」，但这个网关其实收不到任何消息。
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
	if len(names) != 1 || names[0] != "redis" {
		t.Errorf("检查项 = %v，期望 [redis]", names)
	}
}
