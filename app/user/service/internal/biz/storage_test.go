package biz

import (
	"errors"
	"testing"

	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/oss"
)

// newStorageUsecase 构造一个带对象存储的用例。
// storage 存储配置
func newStorageUsecase(storage StorageOptions) *UserUsecase {
	return NewUserUsecase(nil, nil, nil, nil, nil, nil, storage, nil)
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
func TestAllowedHosts(t *testing.T) {
	client := testOSSClient(t)
	s := &conf.Storage{UrlPrefix: "https://cdn.example.com"}
	hosts := allowedHosts(client, s)
	if len(hosts) == 0 {
		t.Fatal("应该提取出 host")
	}
	found := false
	for _, h := range hosts {
		if h == "test-bucket.oss-cn-hangzhou.aliyuncs.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("没提取到上传域名，实际 %v", hosts)
	}
	// 公开前缀的域名也要在白名单里。
	found = false
	for _, h := range hosts {
		if h == "cdn.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("没提取到公开前缀域名，实际 %v", hosts)
	}
}

// TestAvatarContentTypes 后缀映射成 MIME 类型。
func TestAvatarContentTypes(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{AllowedExts: []string{"jpg", "png", "webp"}})
	types := uc.avatarContentTypes()
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
	got := uc2.avatarContentTypes()
	if got != nil {
		t.Errorf("没配白名单时应该返 nil，实际 %v", got)
	}
}

// TestAvatarURLAllowed 校验 host / 前缀 / 后缀。
func TestAvatarURLAllowed(t *testing.T) {
	uc := newStorageUsecase(StorageOptions{
		Client:       testOSSClient(t),
		AllowedExts:  []string{"png"},
		AllowedHosts: []string{"cdn.example.com"},
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
