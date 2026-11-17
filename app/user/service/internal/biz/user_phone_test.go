package biz

import (
	"context"
	"errors"
	"testing"
)

// TestValidatePhone 校验手机号格式。
func TestValidatePhone(t *testing.T) {
	cases := []struct {
		name  string
		phone string
		valid bool
	}{
		{"空串视为不填", "", true},
		{"标准 11 位", "13812345678", true},
		{"199 开头", "19912345678", true},
		{"166 开头", "16612345678", true},
		{"位数不足", "1381234567", false},
		{"位数超长", "138123456789", false},
		{"首位不是 1", "23812345678", false},
		{"第二位是 2", "12812345678", false},
		{"第二位是 0", "10812345678", false},
		{"含字母", "1381234567a", false},
		{"含符号", "138-1234-567", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePhone(tc.phone)
			if tc.valid && err != nil {
				t.Errorf("validatePhone(%q) = %v, want nil", tc.phone, err)
			}
			if !tc.valid && !errors.Is(err, ErrUserInvalidArgument) {
				t.Errorf("validatePhone(%q) = %v, want ErrUserInvalidArgument", tc.phone, err)
			}
		})
	}
}

// TestRegister_StoresPhone 注册时填的手机号要落库。
func TestRegister_StoresPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	id, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "13812345678", "", "1.2.3.4")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := repo.FindByPhone(context.Background(), "13812345678")
	if err != nil {
		t.Fatalf("FindByPhone: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
}

// TestRegister_PhoneOptional 不填手机号也能注册。
func TestRegister_PhoneOptional(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	if _, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "", "", "1.2.3.4"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	u, err := repo.FindByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("FindByUsername: %v", err)
	}
	if u.Phone != "" {
		t.Errorf("Phone = %q, want 空", u.Phone)
	}
}

// TestRegister_RejectsDuplicatePhone 手机号全局唯一，撞了返 ErrUserPhoneTaken。
func TestRegister_RejectsDuplicatePhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	if _, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "13812345678", "", "1.2.3.4"); err != nil {
		t.Fatalf("Register(alice): %v", err)
	}
	_, err := uc.Register(context.Background(), "bob", "secret123", "Bob", "13812345678", "", "1.2.3.4")
	if !errors.Is(err, ErrUserPhoneTaken) {
		t.Errorf("err = %v, want ErrUserPhoneTaken", err)
	}
}

// TestRegister_RejectsInvalidPhone 格式不对直接拒，不进库。
func TestRegister_RejectsInvalidPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	_, err := uc.Register(context.Background(), "alice", "secret123", "Alice", "12345", "", "1.2.3.4")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}

// TestGetUserByPhone_FindsByExactPhone 按手机号精确查到人。
func TestGetUserByPhone_FindsByExactPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.Phone = "13812345678"

	got, err := uc.GetUserByPhone(context.Background(), "13812345678")
	if err != nil {
		t.Fatalf("GetUserByPhone: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
}

// TestGetUserByPhone_NotFound 查无此人返 ErrUserNotFound。
func TestGetUserByPhone_NotFound(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seedUser(t, repo, "alice", "secret123")

	_, err := uc.GetUserByPhone(context.Background(), "13800000000")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

// TestGetUserByPhone_RejectsInvalidPhone 格式不对返参数错误，不是「查无此人」。
func TestGetUserByPhone_RejectsInvalidPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)

	_, err := uc.GetUserByPhone(context.Background(), "abc")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}

// TestGetUserByPhone_IgnoresVisibility 跟 GetUserByUsername 一个语义：
// 对方关了「对陌生人可见」也照样能查到 —— 加好友这条路不能被堵死。
func TestGetUserByPhone_IgnoresVisibility(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.Phone = "13812345678"
	seeded.IsVisibleToStranger = false

	got, err := uc.GetUserByPhone(context.Background(), "13812345678")
	if err != nil {
		t.Fatalf("GetUserByPhone: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
}

// TestGetUserByPhone_ClearsSensitiveFields 查到的永远是「别人」，
// 邮箱/电话/密码哈希一律不外泄 —— 连查手机号本身也不回显。
func TestGetUserByPhone_ClearsSensitiveFields(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.Phone = "13812345678"
	seeded.Email = "alice@example.com"

	got, err := uc.GetUserByPhone(context.Background(), "13812345678")
	if err != nil {
		t.Fatalf("GetUserByPhone: %v", err)
	}
	if got.Phone != "" {
		t.Errorf("Phone = %q, want 空", got.Phone)
	}
	if got.Email != "" {
		t.Errorf("Email = %q, want 空", got.Email)
	}
	if got.PasswordHash != "" {
		t.Errorf("PasswordHash 没被清空")
	}
}

// TestUpdateUserFields_SetsPhone 改资料时能写手机号。
func TestUpdateUserFields_SetsPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")

	phone := "13812345678"
	got, err := uc.UpdateUserFields(context.Background(), seeded.ID, UserUpdateFields{Phone: &phone})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Phone != phone {
		t.Errorf("Phone = %q, want %q", got.Phone, phone)
	}
}

// TestUpdateUserFields_ClearsPhone 手机号传空串 = 清空。
func TestUpdateUserFields_ClearsPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	seeded := seedUser(t, repo, "alice", "secret123")
	seeded.Phone = "13812345678"

	empty := ""
	got, err := uc.UpdateUserFields(context.Background(), seeded.ID, UserUpdateFields{Phone: &empty})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Phone != "" {
		t.Errorf("Phone = %q, want 空（清空）", got.Phone)
	}
}

// TestUpdateUserFields_RejectsDuplicatePhone 改手机号不能撞上别人的号。
func TestUpdateUserFields_RejectsDuplicatePhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	alice := seedUser(t, repo, "alice", "secret123")
	bob := seedUser(t, repo, "bob", "secret123")
	bob.Phone = "13812345678"

	phone := "13812345678"
	_, err := uc.UpdateUserFields(context.Background(), alice.ID, UserUpdateFields{Phone: &phone})
	if !errors.Is(err, ErrUserPhoneTaken) {
		t.Errorf("err = %v, want ErrUserPhoneTaken", err)
	}
}

// TestUpdateUserFields_KeepsOwnPhone 改成自己已经有的号不该报冲突。
func TestUpdateUserFields_KeepsOwnPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	alice := seedUser(t, repo, "alice", "secret123")
	alice.Phone = "13812345678"

	phone := "13812345678"
	got, err := uc.UpdateUserFields(context.Background(), alice.ID, UserUpdateFields{Phone: &phone})
	if err != nil {
		t.Fatalf("UpdateUserFields: %v", err)
	}
	if got.Phone != phone {
		t.Errorf("Phone = %q, want %q", got.Phone, phone)
	}
}

// TestUpdateUserFields_RejectsInvalidPhone 格式不对拒掉，不写库。
func TestUpdateUserFields_RejectsInvalidPhone(t *testing.T) {
	repo := newFakeUserRepo()
	uc := NewUserUsecase(repo, nil, nil, nil, nil, nil, nil, StorageOptions{}, nil)
	alice := seedUser(t, repo, "alice", "secret123")

	bad := "123"
	_, err := uc.UpdateUserFields(context.Background(), alice.ID, UserUpdateFields{Phone: &bad})
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Errorf("err = %v, want ErrUserInvalidArgument", err)
	}
}
