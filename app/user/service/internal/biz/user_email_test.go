package biz

import (
	"context"
	"testing"
)

// TestRegister_StoresEmail 注册时填的邮箱要落库。
func TestRegister_StoresEmail(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "alice@example.com")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "alice@example.com")
	}
}

// TestRegister_EmailOptional 不填邮箱也能注册。
func TestRegister_EmailOptional(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Email != "" {
		t.Errorf("Email = %q, want 空", got.Email)
	}
}

// TestRegister_EmailNotUnique 邮箱不唯一 —— 两个人填同一个邮箱都能注册成功。
//
// 邮箱没加唯一索引，所以这里不存在 ErrUserEmailTaken 之类的错误。
func TestRegister_EmailNotUnique(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	const shared = "shared@example.com"
	if _, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", shared); err != nil {
		t.Fatalf("Register alice: %v", err)
	}
	if _, err := uc.Register(context.Background(), "bob", "secret123", "Bob", "", shared); err != nil {
		t.Fatalf("Register bob: %v", err)
	}
}

// TestRegister_EmailNoFormatValidation 邮箱不做格式校验 —— 什么字符串都能填进去。
//
// 邮箱目前只是个备注字段，格式校验等接「验证码改密」时再加。
func TestRegister_EmailNoFormatValidation(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	if _, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "not-an-email"); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// TestUpdateUserFields_SetsEmail 通过 update_mask 改邮箱。
func TestUpdateUserFields_SetsEmail(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	email := "alice@example.com"
	got, err := uc.UpdateUserFields(context.Background(), id, UserUpdateFields{Email: &email})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "alice@example.com")
	}
}

// TestUpdateUserFields_ClearsEmail 空串表示清空邮箱 —— 跟手机号一个语义。
func TestUpdateUserFields_ClearsEmail(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "alice@example.com")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	empty := ""
	got, err := uc.UpdateUserFields(context.Background(), id, UserUpdateFields{Email: &empty})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Email != "" {
		t.Errorf("Email = %q, want 空", got.Email)
	}
}

// TestUpdateUserFields_EmailTrims 邮箱首尾空白被 trim 掉。
func TestUpdateUserFields_EmailTrims(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	email := "  alice@example.com  "
	got, err := uc.UpdateUserFields(context.Background(), id, UserUpdateFields{Email: &email})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "alice@example.com")
	}
}

// TestUpdateUserFields_EmailOnly 只改邮箱也算「有字段被设置」。
//
// 之前 phone-only 更新会被 guard 拒掉（400），这里守住 email 的同类路径。
func TestUpdateUserFields_EmailOnly(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	email := "alice@example.com"
	if _, err := uc.UpdateUserFields(context.Background(), id, UserUpdateFields{Email: &email}); err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
}

// TestUpdateUserFields_RequiresAnyField 一个字段都没设就拒。
func TestUpdateUserFields_RequiresAnyField(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := uc.UpdateUserFields(context.Background(), id, UserUpdateFields{}); err == nil {
		t.Error("UpdateUserFields 空 fields = nil, want 错误")
	}
}

// TestGetUserByID_HidesEmailFromOthers 查别人时邮箱被清空。
func TestGetUserByID_HidesEmailFromOthers(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil)

	viewerID, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "alice@example.com")
	if err != nil {
		t.Fatalf("Register alice: %v", err)
	}
	targetID, err := uc.Register(context.Background(), "bob", "secret123", "Bob", "", "bob@example.com")
	if err != nil {
		t.Fatalf("Register bob: %v", err)
	}

	// 自己看自己：邮箱要有值。
	self, err := uc.GetUserByID(context.Background(), viewerID, viewerID)
	if err != nil {
		t.Fatalf("GetUserByID 自己: %v", err)
	}
	if self.Email != "alice@example.com" {
		t.Errorf("自己看自己 Email = %q, want %q", self.Email, "alice@example.com")
	}

	// 看别人：邮箱要空。
	// bob 默认 is_visible_to_stranger=false，会被折叠成 NOT_FOUND —— 先把他设成可见。
	if err := repo.UpdateStatus(context.Background(), targetID, UserStatusNormal); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	target, err := repo.FindByID(context.Background(), targetID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	target.IsVisibleToStranger = true

	other, err := uc.GetUserByID(context.Background(), viewerID, targetID)
	if err != nil {
		t.Fatalf("GetUserByID 别人: %v", err)
	}
	if other.Email != "" {
		t.Errorf("看别人 Email = %q, want 空", other.Email)
	}
}
