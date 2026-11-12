package biz

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMessage_UnreadGRPCNotFoundKeepsOriginalError 跨进程的 404 不能被错判成 503。
//
// 下游 conversation 服务是 gRPC 调用，返回的是 *status.Error。
// 它没有 Unwrap()，所以 errors.As(err, &*kratoserrors.Error) 永远匹配不上 ——
// 旧实现用 errors.As，跨进程的 404 全被当成「下游不可达」返回 503。
func TestMessage_UnreadGRPCNotFoundKeepsOriginalError(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	// 模拟 gRPC 对端返回 NotFound
	unread.err = status.Error(codes.NotFound, "conversation not found")

	_, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 7)
	if err == nil {
		t.Fatal("应该返错")
	}
	if kratoserrors.IsServiceUnavailable(err) {
		t.Errorf("跨进程的 404 被错判成 503: %v", err)
	}
	se := kratoserrors.FromError(err)
	if se == nil || se.Code != 404 {
		t.Errorf("err = %v, want 404", err)
	}
}

// TestMessage_UnreadGRPCUnavailableBecomes503 下游真的不可达时才返回 503。
func TestMessage_UnreadGRPCUnavailableBecomes503(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.err = status.Error(codes.Unavailable, "connection refused")

	_, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 7)
	if err == nil {
		t.Fatal("应该返错")
	}
	if !kratoserrors.IsServiceUnavailable(err) {
		t.Errorf("err = %v, want 503", err)
	}
}

// TestMessage_UnreadGRPCForbiddenBecomes503 非 404 的下游错误统一按 503 处理。
func TestMessage_UnreadGRPCForbiddenBecomes503(t *testing.T) {
	uc, _, membership, _, unread := newTestMessageUsecase()
	membership.add(7, "uid-42")
	unread.err = status.Error(codes.PermissionDenied, "internal secret mismatch")

	_, _, err := uc.GetUnreadCount(context.Background(), "uid-42", 7)
	if err == nil {
		t.Fatal("应该返错")
	}
	if !kratoserrors.IsServiceUnavailable(err) {
		t.Errorf("err = %v, want 503", err)
	}
}
