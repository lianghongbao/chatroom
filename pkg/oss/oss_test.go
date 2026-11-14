package oss

import (
	"testing"
)

// testConfig 一份可用于测试的配置。
func testConfig() Config {
	return Config{
		Endpoint:        "oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "test-bucket",
		Region:          "cn-hangzhou",
		RoleArn:         "acs:ram::123456789:role/oss-upload",
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		UploadHost:      "https://test-bucket.oss-cn-hangzhou.aliyuncs.com",
		URLPrefix:       "https://cdn.example.com",
	}
}

func TestNewClientIncompleteConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"缺 bucket", Config{Region: "cn-hangzhou", RoleArn: "r", AccessKeyID: "a", AccessKeySecret: "s"}},
		{"缺 region", Config{Bucket: "b", RoleArn: "r", AccessKeyID: "a", AccessKeySecret: "s"}},
		{"缺 role_arn", Config{Bucket: "b", Region: "r", AccessKeyID: "a", AccessKeySecret: "s"}},
		{"缺 ak", Config{Bucket: "b", Region: "r", RoleArn: "r", AccessKeySecret: "s"}},
		{"缺 sk", Config{Bucket: "b", Region: "r", RoleArn: "r", AccessKeyID: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.cfg); err != ErrConfigIncomplete {
				t.Fatalf("期望 ErrConfigIncomplete，实际 %v", err)
			}
		})
	}
}

func TestValidateExt(t *testing.T) {
	allowed := []string{"jpg", "jpeg", "png", "webp"}

	ok := []string{"png", "PNG", ".png", "jpg"}
	for _, ext := range ok {
		if err := ValidateExt(ext, allowed); err != nil {
			t.Errorf("后缀 %q 应该通过，实际 %v", ext, err)
		}
	}

	bad := []string{"exe", "php", ""}
	for _, ext := range bad {
		if err := ValidateExt(ext, allowed); err != ErrInvalidExt {
			t.Errorf("后缀 %q 应该被拒，实际 %v", ext, err)
		}
	}

	// 空白名单要 fail-closed，不能放行任意后缀。
	if err := ValidateExt("exe", nil); err != ErrInvalidExt {
		t.Errorf("空白名单应该拒绝，实际 %v", err)
	}
	if err := ValidateExt("exe", []string{}); err != ErrInvalidExt {
		t.Errorf("空白名单应该拒绝，实际 %v", err)
	}
}

func TestURL(t *testing.T) {
	c := &Client{cfg: testConfig()}

	cases := []struct {
		name string
		key  string
		want string
	}{
		{"裸 key", "avatar/u1/a.png", "https://cdn.example.com/avatar/u1/a.png"},
		{"带首斜杠", "/avatar/u1/a.png", "https://cdn.example.com/avatar/u1/a.png"},
		{"完整 URL", "https://cdn.example.com/avatar/u1/a.png", "https://cdn.example.com/avatar/u1/a.png"},
		{"空 key", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.URL(tc.key); got != tc.want {
				t.Errorf("期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

func TestURLNoPrefix(t *testing.T) {
	cfg := testConfig()
	cfg.URLPrefix = ""
	c := &Client{cfg: cfg}

	if got := c.URL("avatar/u1/a.png"); got != "" {
		t.Errorf("没配 URLPrefix 时应该返空串，实际 %q", got)
	}
}

func TestUploadHostFallback(t *testing.T) {
	cfg := testConfig()
	cfg.UploadHost = ""
	c := &Client{cfg: cfg}

	want := "https://test-bucket.oss-cn-hangzhou.aliyuncs.com"
	if got := c.UploadHost(); got != want {
		t.Errorf("期望 %q，实际 %q", want, got)
	}
}

func TestValidateURL(t *testing.T) {
	c := &Client{cfg: testConfig()}
	rules := URLRules{
		AllowedHosts:    []string{"cdn.example.com"},
		AllowedPrefixes: []string{"avatar/u1/"},
		AllowedExts:     []string{"png", "jpg"},
	}

	ok := []struct {
		name string
		raw  string
		want string
	}{
		{"标准 URL", "https://cdn.example.com/avatar/u1/a.png", "https://cdn.example.com/avatar/u1/a.png"},
		{"剥掉 query", "https://cdn.example.com/avatar/u1/a.png?x=1", "https://cdn.example.com/avatar/u1/a.png"},
		{"剥掉 fragment", "https://cdn.example.com/avatar/u1/a.png#frag", "https://cdn.example.com/avatar/u1/a.png"},
		{"bucket 默认域名", "https://test-bucket.oss-cn-hangzhou.aliyuncs.com/avatar/u1/a.png", "https://test-bucket.oss-cn-hangzhou.aliyuncs.com/avatar/u1/a.png"},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ValidateURL(tc.raw, rules)
			if err != nil {
				t.Fatalf("不该报错，实际 %v", err)
			}
			if got != tc.want {
				t.Errorf("期望 %q，实际 %q", tc.want, got)
			}
		})
	}

	bad := []struct {
		name string
		raw  string
		want error
	}{
		{"空串", "", ErrURLInvalid},
		{"非 http 协议", "ftp://cdn.example.com/avatar/u1/a.png", ErrURLInvalid},
		{"host 不在白名单", "https://evil.com/avatar/u1/a.png", ErrURLHostNotAllowed},
		{"前缀不匹配", "https://cdn.example.com/avatar/u2/a.png", ErrURLPrefixNotMatch},
		{"后缀不在白名单", "https://cdn.example.com/avatar/u1/a.exe", ErrURLExtNotAllowed},
		{"没有后缀", "https://cdn.example.com/avatar/u1/avatar", ErrURLExtNotAllowed},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.ValidateURL(tc.raw, rules)
			if err != tc.want {
				t.Errorf("期望 %v，实际 %v", tc.want, err)
			}
		})
	}

	// 后缀白名单为空要 fail-closed。
	if _, err := c.ValidateURL("https://cdn.example.com/avatar/u1/a.png", URLRules{AllowedHosts: []string{"cdn.example.com"}}); err != ErrURLExtNotAllowed {
		t.Errorf("空白名单应该拒绝，实际 %v", err)
	}
}

func TestExtractFileKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"avatar/u1/a.png", "avatar/u1/a.png"},
		{"/avatar/u1/a.png", "avatar/u1/a.png"},
		{"https://cdn.example.com/avatar/u1/a.png", "avatar/u1/a.png"},
		{"https://cdn.example.com/avatar/u1/a.png?x=1", "avatar/u1/a.png"},
		{"https://cdn.example.com/avatar/u1/a.png#f", "avatar/u1/a.png"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := extractFileKey(tc.in); got != tc.want {
			t.Errorf("输入 %q 期望 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"avatar/u1/":  "/avatar/u1/",
		"avatar/u1":   "/avatar/u1/",
		"/avatar/u1/": "/avatar/u1/",
		"":            "/",
	}
	for in, want := range cases {
		if got := normalizePrefix(in); got != want {
			t.Errorf("输入 %q 期望 %q，实际 %q", in, want, got)
		}
	}
}

func TestMIMEByExt(t *testing.T) {
	cases := map[string]string{
		"jpg":  "image/jpeg",
		"JPEG": "image/jpeg",
		".png": "image/png",
		"webp": "image/webp",
		"gif":  "image/gif",
		"exe":  "",
		"":     "",
	}
	for ext, want := range cases {
		if got := MIMEByExt(ext); got != want {
			t.Errorf("MIMEByExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestGenerateUploadTokenExtNotAllowed(t *testing.T) {
	client, err := NewClient(testConfig())
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	_, err = client.GenerateUploadToken(t.Context(), UploadOptions{
		Dir:         "avatar/u1/",
		Ext:         "exe",
		AllowedExts: []string{"png"},
	})
	if err != ErrInvalidExt {
		t.Fatalf("期望 ErrInvalidExt，实际 %v", err)
	}
}
