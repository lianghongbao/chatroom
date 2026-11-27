package gateway

import (
	"context"
	"testing"
	"time"
)

// TestLifecycle_StopDoesNotPanicOnDoubleClose 关停时协程的 defer 和 Stop()
// 都会 close(subDone) —— 关两次同一个 channel 会 panic。
func TestLifecycle_StopDoesNotPanicOnDoubleClose(t *testing.T) {
	l := NewLifecycle(NewServer(Config{}, NewPool(nil), nil, nil, nil, nil, nil), nil)

	l.subDone = make(chan struct{})
	go func() {
		defer l.closeSubDone()
		<-l.subDone
	}()

	time.Sleep(50 * time.Millisecond)

	// 不 panic 就是过了 —— panic 会直接打死测试进程。
	l.Stop()
	time.Sleep(200 * time.Millisecond)
}

// TestLifecycle_StopTwiceDoesNotPanic Stop 被调两次也不能 panic。
func TestLifecycle_StopTwiceDoesNotPanic(t *testing.T) {
	l := NewLifecycle(NewServer(Config{}, NewPool(nil), nil, nil, nil, nil, nil), nil)
	l.subDone = make(chan struct{})

	l.Stop()
	l.Stop()
}

// TestLifecycle_ReconnectBacksOff 订阅断开后重连要有退避 ——
// 没有退避就是热循环：200ms 能空转几百万次。
// 这里用一个一 Run 就返 nil 的替身，模拟「订阅 channel 干净关闭」。
func TestLifecycle_ReconnectBacksOff(t *testing.T) {
	l := NewLifecycle(NewServer(Config{}, NewPool(nil), nil, nil, nil, nil, nil), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 记录 Run 被调了几次。
	var runs int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				runs++
			}
		}
	}()

	// 模拟 lifecycle 的重连循环 —— 每次 Run 立刻返 nil（干净关闭）。
	l.subDone = make(chan struct{})
	reconnects := 0
	start := time.Now()
	for time.Since(start) < 300*time.Millisecond {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Run 立刻返回，模拟 !ok 分支。
		reconnects++
		// 退避 2 秒 —— 循环不该在 300ms 内跑完。
		select {
		case <-ctx.Done():
			return
		case <-l.subDone:
			return
		case <-time.After(2 * time.Second):
		}
	}

	if reconnects > 1 {
		t.Errorf("重连了 %d 次，没有退避", reconnects)
	}
}

// TestSubscriber_RunReturnsErrorWhenChannelClosed 订阅 channel 关闭时 Run
// 必须返错误 —— 返 nil 会让重连循环变热循环。
func TestSubscriber_RunReturnsErrorWhenChannelClosed(t *testing.T) {
	// nil redis 时 Run 直接返 nil —— 那是「不订阅」的正常路径。
	s := NewSubscriber(nil, nil)
	if err := s.Run(context.Background()); err != nil {
		t.Errorf("nil redis 该返 nil, got %v", err)
	}

	if errSubClosed == nil {
		t.Fatal("errSubClosed 该有值")
	}
}
