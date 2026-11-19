package biz

import (
	"context"
	"errors"
	"testing"
)

// TestCheckUsernameAvailable_NotTaken 没人用过 → 可用。
func TestCheckUsernameAvailable_NotTaken(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	available, err := uc.CheckUsernameAvailable(context.Background(), "bob", "1.2.3.4")
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if !available {
		t.Errorf("available = false, want true")
	}
}

// TestCheckUsernameAvailable_Taken 已经有人用了 → 不可用。
func TestCheckUsernameAvailable_Taken(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	available, err := uc.CheckUsernameAvailable(context.Background(), "alice", "1.2.3.4")
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if available {
		t.Errorf("available = true, want false")
	}
}

// TestCheckUsernameAvailable_IsCaseSensitive 用户名**区分大小写** ——
// 已有 alice 时，ALICE 是另一个名字，仍然可用。
func TestCheckUsernameAvailable_IsCaseSensitive(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	available, err := uc.CheckUsernameAvailable(context.Background(), "ALICE", "1.2.3.4")
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if !available {
		t.Errorf("available = false, want true —— 大小写应该区分")
	}
}

// TestCheckUsernameAvailable_TrimsSpaces 输入框里常带空格。
func TestCheckUsernameAvailable_TrimsSpaces(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	available, err := uc.CheckUsernameAvailable(context.Background(), "  alice  ", "1.2.3.4")
	if err != nil {
		t.Fatalf("CheckUsernameAvailable: %v", err)
	}
	if available {
		t.Errorf("available = true, want false")
	}
}

// TestCheckUsernameAvailable_RejectsEmpty 空用户名返参数错误。
// 「查无此人」和「参数非法」是两回事 —— 空串返 available=true 会让前端
// 显示「用户名可用」，但真正注册时后端一定拒绝。
func TestCheckUsernameAvailable_RejectsEmpty(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	_, err := uc.CheckUsernameAvailable(context.Background(), "   ", "1.2.3.4")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}

// TestCheckUsernameAvailable_RateLimited 超配额返限频错误。
//
// 这个端点公开可达（注册时还没登录），不限频就是绕过注册限频的枚举通道。
func TestCheckUsernameAvailable_RateLimited(t *testing.T) {
	repo := newFakeUserRepo()
	lim := &fakeRegisterLimiter{}
	uc := NewUserUsecase(repo, nil, nil, lim, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	_, err := uc.CheckUsernameAvailable(context.Background(), "bob", "1.2.3.4")
	if !errors.Is(err, ErrUserRateLimited) {
		t.Errorf("err = %v, want ErrUserRateLimited", err)
	}
}

// fakeRegisterLimiter 是 RegisterLimiter 的内存替身，一律拒绝。
type fakeRegisterLimiter struct{}

func (l *fakeRegisterLimiter) Allow(ctx context.Context, clientIP string) error {
	return ErrUserRateLimited
}

func (l *fakeRegisterLimiter) AllowCheck(ctx context.Context, clientIP string) error {
	return ErrUserRateLimited
}
