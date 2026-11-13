package gateway

import (
	"context"
	"sync"
	"time"
)

// Lifecycle 管理网关的后台任务（订阅 Redis Pub/Sub、空闲连接清理）。
type Lifecycle struct {
	gw         *Server
	subscriber *Subscriber
	// invalidator 订阅成员失效通知。nil 时不订阅。
	invalidator *InvalidatorSubscriber

	// stopIdle 停止空闲连接清理任务。Start 时赋值。
	stopIdle func()
	// subDone 通知订阅协程退出。
	subDone chan struct{}
	// subClose 保证 subDone 只被关一次。
	// 协程的 defer 和 Stop() 都会关它 —— 关两次同一个 channel 会 panic。
	subClose sync.Once
	// started 是否已 Start。防止重复 Start。
	started bool
}

// NewLifecycle 构造 Lifecycle，gw 是网关，subscriber 为 nil 时不订阅。
func NewLifecycle(gw *Server, subscriber *Subscriber) *Lifecycle {
	return &Lifecycle{gw: gw, subscriber: subscriber}
}

// NewLifecycleWithInvalidator 构造 Lifecycle。gw 是网关，
// subscriber 是成员失效通知的订阅器。
func NewLifecycleWithInvalidator(gw *Server, subscriber *Subscriber, invalidator *InvalidatorSubscriber) *Lifecycle {
	return &Lifecycle{gw: gw, subscriber: subscriber, invalidator: invalidator}
}

// WithInvalidator 挂上成员失效通知的订阅器，s 为 nil 时不订阅。
func (l *Lifecycle) WithInvalidator(s *InvalidatorSubscriber) *Lifecycle {
	l.invalidator = s
	return l
}

// Start 启动后台任务，ctx 结束时应取消。幂等 —— 重复调用只生效一次。
func (l *Lifecycle) Start(ctx context.Context) {
	if l.started {
		return
	}
	l.started = true
	l.stopIdle = l.gw.StartBackground(ctx)

	if l.subscriber == nil && l.invalidator == nil {
		return
	}

	l.subDone = make(chan struct{})
	go func() {
		defer l.closeSubDone()
		l.runSubscriber(ctx, l.subscriber)
	}()
	if l.invalidator != nil {
		go func() {
			l.runSubscriber(ctx, l.invalidator.Subscriber)
		}()
	}
}

// runSubscriber 订阅直到 ctx 结束；订阅断了就退避后重连。sub 为 nil 时直接返回。
func (l *Lifecycle) runSubscriber(ctx context.Context, sub *Subscriber) {
	if sub == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.subDone:
			return
		default:
		}

		err := sub.Run(ctx)
		if ctx.Err() != nil {
			return
		}

		// 退避 —— 不管 Run 是正常返回还是报错，都要等一下再重连。
		// 少了这一步就是热循环。
		select {
		case <-ctx.Done():
			return
		case <-l.subDone:
			return
		case <-time.After(2 * time.Second):
		}

		if err != nil {
			// 订阅出错，下一轮继续重试。
			continue
		}
	}
}

// closeSubDone 关闭 subDone，只关一次。
func (l *Lifecycle) closeSubDone() {

	l.subClose.Do(func() {
		if l.subDone != nil {
			close(l.subDone)
		}
	})
}

// Stop 关停：停止后台任务并关掉所有连接。
func (l *Lifecycle) Stop() {
	if l.stopIdle != nil {
		l.stopIdle()
	}
	l.closeSubDone()
	l.gw.Shutdown()
}
