package biz

import (
	"context"
	"errors"
	"testing"
)

// TestGetUserByUsername_FindsByExactUsername 按用户名精确查到人。
func TestGetUserByUsername_FindsByExactUsername(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")

	got, err := uc.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want %q", got.Username, "alice")
	}
}

// TestGetUserByUsername_IgnoresCase 用户名存库时已小写化，查询侧同样小写化才能对上。
func TestGetUserByUsername_IgnoresCase(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")

	got, err := uc.GetUserByUsername(context.Background(), "ALICE")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
}

// TestGetUserByUsername_TrimsSpaces 输入框里常带空格。
func TestGetUserByUsername_TrimsSpaces(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")

	got, err := uc.GetUserByUsername(context.Background(), "  alice  ")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
}

// TestGetUserByUsername_NotFound 查无此人返 ErrUserNotFound。
func TestGetUserByUsername_NotFound(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	_, err := uc.GetUserByUsername(context.Background(), "nobody")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

// TestGetUserByUsername_RejectsEmpty 空用户名返参数错误，不是「查无此人」。
func TestGetUserByUsername_RejectsEmpty(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	_, err := uc.GetUserByUsername(context.Background(), "   ")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}

// TestGetUserByUsername_IgnoresVisibility 与 GetUserByID 的关键区别：
// 对方关了「对陌生人可见」也照样能查到 —— 好友申请本来就允许陌生人互相发起，
// 折叠成 NOT_FOUND 会让加好友这条路直接堵死。
func TestGetUserByUsername_IgnoresVisibility(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.IsVisibleToStranger = false

	got, err := uc.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
}

// TestGetUserByUsername_ClearsSensitiveFields 查到的永远是「别人」，
// 邮箱/电话/密码哈希一律不外泄。
func TestGetUserByUsername_ClearsSensitiveFields(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.Email = "alice@example.com"
	seeded.Phone = "13800000000"

	got, err := uc.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.Email != "" {
		t.Errorf("Email = %q, want 空", got.Email)
	}
	if got.Phone != "" {
		t.Errorf("Phone = %q, want 空", got.Phone)
	}
	if got.PasswordHash != "" {
		t.Errorf("PasswordHash 没被清空")
	}
}
