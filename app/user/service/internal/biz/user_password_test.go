package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"chatroom/app/user/service/internal/conf"
)

// TestChangePassword_RevokesRefreshToken 改密必须把 refresh token 一起撤销。
// 只拉黑 access token 的话，refresh token 还在，改密等于没改。
func TestChangePassword_RevokesRefreshToken(t *testing.T) {
	repo := newFakeUserRepo()
	auth := &conf.Auth{JwtSecret: "a1b2c3d4e5f67890abcdef1234567890"}
	store := newFakeTokenStore()
	uc := NewUserUsecase(repo, auth, nil, nil, nil, store, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	const accessJTI = "access-jti-1"
	const refreshJTI = "refresh-jti-1"
	// 模拟 refresh token 已经存进 store（登录时发的）
	if err := store.SaveRefresh(context.Background(), refreshJTI, "uid-1", "raw", time.Hour); err != nil {
		t.Fatalf("SaveRefresh: %v", err)
	}

	err := uc.ChangePassword(context.Background(), "uid-1", "secret123", "newpass456", accessJTI, refreshJTI, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// access 被拉黑
	if !store.blacklist[accessJTI] {
		t.Errorf("access token 没被拉黑")
	}
	// refresh 被撤销
	if store.revokeCt != 1 {
		t.Errorf("RevokeRefresh 调用次数 = %d, want 1", store.revokeCt)
	}
	if _, ok := store.refreshByJTI[refreshJTI]; ok {
		t.Errorf("refresh token 还在 store 里 —— 改密后还能拿它换新 access")
	}
}

// TestChangePassword_KeepsPasswordWhitespace 密码里的空格原样保留。
// 中间任何一次 trim 都会让老用户再也改不了密码。
func TestChangePassword_KeepsPasswordWhitespace(t *testing.T) {
	repo := newFakeUserRepo()
	auth := &conf.Auth{JwtSecret: "a1b2c3d4e5f67890abcdef1234567890"}
	uc := NewUserUsecase(repo, auth, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	const pwd = " pass "
	if _, err := uc.Register(context.Background(), "alice", pwd, "Alice", "", "", "1.2.3.4"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 用带空格的旧密码改密 —— 应该成功
	if err := uc.ChangePassword(context.Background(), "uid-1", pwd, "newpass456", "", "", time.Time{}); err != nil {
		t.Fatalf("ChangePassword 用带空格的旧密码失败: %v（密码被 trim 了？）", err)
	}
}

// TestLogin_PasswordWhitespaceRoundTrip 注册用带空格密码，登录也要能过。
func TestLogin_PasswordWhitespaceRoundTrip(t *testing.T) {
	repo := newFakeUserRepo()
	auth := &conf.Auth{JwtSecret: "a1b2c3d4e5f67890abcdef1234567890"}
	uc := NewUserUsecase(repo, auth, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	const pwd = " pass "
	if _, err := uc.Register(context.Background(), "alice", pwd, "Alice", "", "", "1.2.3.4"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, _, err := uc.Login(context.Background(), "alice", pwd, ""); err != nil {
		t.Fatalf("Login 用带空格的密码失败: %v（密码被 trim 了？）", err)
	}
}

// TestRegister_RejectsOverlongInputs 超长输入要在 biz 层就挡掉，
// 不能写进库（或者变成 500）。
func TestRegister_RejectsOverlongInputs(t *testing.T) {
	cases := []struct {
		name     string
		username string
		password string
		nickname string
	}{
		{"超长用户名", strings.Repeat("u", MaxUsernameLength+1), "secret123", "n"},
		{"超长昵称", "alice", "secret123", strings.Repeat("n", MaxNicknameLength+1)},
		{"超长密码", "alice", strings.Repeat("p", MaxPasswordLength+1), "n"},
		{"过短密码", "alice", "12345", "n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeUserRepo()
			auth := &conf.Auth{JwtSecret: "a1b2c3d4e5f67890abcdef1234567890"}
			uc := NewUserUsecase(repo, auth, nil, nil, nil, nil, nil, StorageOptions{}, nil)

			_, err := uc.Register(context.Background(), tc.username, tc.password, tc.nickname, "", "", "1.2.3.4")
			if !errors.Is(err, ErrUserInvalidArgument) {
				t.Errorf("err = %v, want ErrUserInvalidArgument", err)
			}
			if len(repo.users) != 0 {
				t.Errorf("超长输入不该写进库, 但库里有 %d 条", len(repo.users))
			}
		})
	}
}

// TestUpdateUser_RejectsOverlongProfile 改资料时也要挡超长昵称 / 头像。
func TestUpdateUser_RejectsOverlongProfile(t *testing.T) {
	repo := newFakeUserRepo()
	auth := &conf.Auth{JwtSecret: "a1b2c3d4e5f67890abcdef1234567890"}
	uc := NewUserUsecase(repo, auth, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	_, err := uc.UpdateUser(context.Background(), "uid-1", strings.Repeat("n", MaxNicknameLength+1), "http://x/y.png")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}

	_, err = uc.UpdateUser(context.Background(), "uid-1", "Alice", strings.Repeat("a", MaxAvatarLength+1))
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}
