package service

import (
	"context"
	"testing"
	"time"

	"chatroom/app/user/service/internal/biz"

	pb "chatroom/api/user/service/v1"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// TestServiceRegister_PassesEmailToUsecase 注册请求里的邮箱要传到 usecase。
func TestServiceRegister_PassesEmailToUsecase(t *testing.T) {
	repo := &fakeRepo{saveID: "uid-42"}
	s := newServiceWithRepo(repo)

	_, err := s.Register(context.Background(), &pb.RegisterRequest{
		Username: "alice",
		Password: "secret123",
		Nickname: "Alice",
		Email:    "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !repo.saveCalls.called {
		t.Fatal("usecase.Register not called")
	}
	if got := repo.saveCalls.user.Email; got != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got)
	}
}

// TestServiceRegister_EmailOptional 不填邮箱也能注册。
func TestServiceRegister_EmailOptional(t *testing.T) {
	repo := &fakeRepo{saveID: "uid-42"}
	s := newServiceWithRepo(repo)

	_, err := s.Register(context.Background(), &pb.RegisterRequest{
		Username: "alice",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := repo.saveCalls.user.Email; got != "" {
		t.Errorf("Email = %q, want 空", got)
	}
}

// TestServiceUpdateUser_PassesEmailToUsecase mask 里带 email 时要把邮箱传下去。
func TestServiceUpdateUser_PassesEmailToUsecase(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{
		Email:      "alice@example.com",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"email"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !repo.updateFieldsCalls.called {
		t.Fatal("usecase.UpdateUserFields not called")
	}
	if repo.updateFieldsCalls.fields.Email == nil {
		t.Fatal("Email 没传给 usecase")
	}
	if *repo.updateFieldsCalls.fields.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", *repo.updateFieldsCalls.fields.Email)
	}
}

// TestServiceUpdateUser_ClearsEmail mask 里带 email 且值为空串时，
// 也要把空串传下去 —— 空串 = 清空邮箱。
func TestServiceUpdateUser_ClearsEmail(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{
		Email:      "",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"email"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if repo.updateFieldsCalls.fields.Email == nil {
		t.Fatal("Email 没传给 usecase")
	}
	if *repo.updateFieldsCalls.fields.Email != "" {
		t.Errorf("Email = %q, want 空串", *repo.updateFieldsCalls.fields.Email)
	}
}

// TestServiceUpdateUser_IgnoresEmailWithoutMask mask 里没 email 时邮箱不进 usecase。
func TestServiceUpdateUser_IgnoresEmailWithoutMask(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{
		Nickname:   "Alice",
		Email:      "alice@example.com",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nickname"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if repo.updateFieldsCalls.fields.Email != nil {
		t.Errorf("Email = %v, want nil（mask 里没有 email）", *repo.updateFieldsCalls.fields.Email)
	}
}
