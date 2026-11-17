package biz

import (
	"errors"
	"testing"

	"chatroom/pkg/oss"
)

// newStorageUsecase 构造一个带对象存储的用例。
// storage 存储配置
func newStorageUsecase(storage StorageOptions) *UserUsecase {
	return NewUserUsecase(nil, nil, nil, nil, nil, nil, nil, storage, nil)
}

// testOSSClient 构造一个测试用的 OSS 客户端。
func testOSSClient(t *testing.T) *oss.Client {
	t.Helper()
	client, err := oss.NewClient(oss.Config{
		Endpoint:        "oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "test-bucket",
		Region:          "cn-hangzhou",
		RoleArn:         "acs:ram::123456789:role/oss-upload",
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		UploadHost:      "https://test-bucket.oss-cn-hangzhou.aliyuncs.com",
		URLPrefix:       "https://cdn.example.com",
	})
	if err != nil {
		t.Fatalf("构造 OSS 客户端失败：%v", err)
	}
	return client
}

// TestGetUploadTokenNotConfigured 没配对象存储时签发凭证要报错。
func TestGetUploadTokenNotConfigured(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{})
	_, err := uc.GetUploadToken(t.Context(), "u1", "png")
	if !errors.Is(err, ErrStorageNotConfigured) {
		t.Fatalf("期望 ErrStorageNotConfigured，实际 %v", err)
	}
}

// TestGetUploadTokenExtNotAllowed 后缀不在白名单时要报错。
func TestGetUploadTokenExtNotAllowed(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{
		Client:      testOSSClient(t),
		AllowedExts: []string{"png", "jpg"},
	})
	cases := []string{"exe", "php", "", "svg"}
	for _, ext := range cases {
		t.Run(ext, func(t *testing.T) {
			_, err := uc.GetUploadToken(t.Context(), "u1", ext)
			if !errors.Is(err, ErrUploadExtNotAllowed) {
				t.Fatalf("后缀 %q 期望 ErrUploadExtNotAllowed，实际 %v", ext, err)
			}
		})
	}
}

// TestGetUploadTokenNoUID 没有 uid 时要报错。
func TestGetUploadTokenNoUID(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{
		Client:      testOSSClient(t),
		AllowedExts: []string{"png"},
	})
	_, err := uc.GetUploadToken(t.Context(), "", "png")
	if !errors.Is(err, ErrUserInvalidArgument) {
		t.Fatalf("期望 ErrUserInvalidArgument，实际 %v", err)
	}
}

// TestAvatarDir key 前缀按 uid 拼。
func TestAvatarDir(t *testing.T) {
	if got := avatarDir("u1"); got != "avatar/u1/" {
		t.Fatalf("期望 avatar/u1/，实际 %q", got)
	}
	// uid 里的斜杠会被带进前缀 —— uid 是 UUIDv7，不会出现斜杠。
	if got := avatarDir("u1/u2"); got != "avatar/u1/u2/" {
		t.Fatalf("实际 %q", got)
	}
}

// TestAllowedHosts 从上传域名和公开前缀里提取 host。
//
// host 白名单的归一化（去 scheme / 路径 / 端口）现在在 pkg/oss 的
// NewUploader 里做 —— 用户头像和群头像共用同一份。
func TestAllowedHosts(t *testing.T) {
	client := testOSSClient(t)
	uploader := oss.NewUploader(client, oss.UploadPolicy{
		Dir:         avatarDir("u1"),
		AllowedExts: []string{"png"},
		MaxSize:     1024,
	}, nil)

	// 上传域名和公开前缀的域名都要在白名单里，且是裸 host（不含 scheme）。
	clean, err := uploader.ValidateURLFormat("https://test-bucket.oss-cn-hangzhou.aliyuncs.com/avatar/u1/a.png")
	if err != nil {
		t.Fatalf("上传域名应该通过校验，实际 %v", err)
	}
	if clean != "https://test-bucket.oss-cn-hangzhou.aliyuncs.com/avatar/u1/a.png" {
		t.Fatalf("地址被改写了，实际 %q", clean)
	}
	if _, err := uploader.ValidateURLFormat("https://cdn.example.com/avatar/u1/a.png"); err != nil {
		t.Fatalf("公开前缀域名应该通过校验，实际 %v", err)
	}
	// 别的域名不行。
	if _, err := uploader.ValidateURLFormat("https://evil.example.com/avatar/u1/a.png"); err == nil {
		t.Fatal("别人的域名不该通过校验")
	}
	// 别人的目录也不行 —— 前缀锁死在 avatar/u1/。
	if _, err := uploader.ValidateURLFormat("https://cdn.example.com/avatar/u2/a.png"); err == nil {
		t.Fatal("别人的目录不该通过校验")
	}
}

// TestAvatarContentTypes 后缀映射成 MIME 类型。
//
// 映射逻辑现在在 pkg/oss 的 Uploader 里（用户头像和群头像共用），
// 这里从用户域的角度验一遍：换掉后缀白名单，行为跟着变。
func TestAvatarContentTypes(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{AllowedExts: []string{"jpg", "png", "webp"}})
	types := oss.ContentTypesByExt(uc.avatarExts)
	want := map[string]bool{"image/jpeg": false, "image/png": false, "image/webp": false}
	for _, ct := range types {
		if _, ok := want[ct]; ok {
			want[ct] = true
		}
	}
	for ct, ok := range want {
		if !ok {
			t.Errorf("缺 MIME 类型 %q，实际 %v", ct, types)
		}
	}
	// 没配白名单就没有类型限制。
	uc2 := newStorageUsecase(StorageOptions{})
	got := oss.ContentTypesByExt(uc2.avatarExts)
	if got != nil {
		t.Errorf("没配白名单时应该返 nil，实际 %v", got)
	}
}

// TestAvatarURLAllowed 校验 host / 前缀 / 后缀。
func TestAvatarURLAllowed(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{
		Client:      testOSSClient(t),
		AllowedExts: []string{"png"},
	})

	ok := "https://cdn.example.com/avatar/u1/a.png"
	if !uc.avatarURLAllowed("u1", ok) {
		t.Error("合法头像地址应该通过")
	}

	bad := []struct {
		name string
		uid  string
		raw  string
	}{
		{"别人的目录", "u1", "https://cdn.example.com/avatar/u2/a.png"},
		{"host 不对", "u1", "https://evil.com/avatar/u1/a.png"},
		{"后缀不对", "u1", "https://cdn.example.com/avatar/u1/a.exe"},
		{"不是 http", "u1", "ftp://cdn.example.com/avatar/u1/a.png"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if uc.avatarURLAllowed(tc.uid, tc.raw) {
				t.Error("应该被拒")
			}
		})
	}
}
