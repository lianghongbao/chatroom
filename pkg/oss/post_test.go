package oss

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// TestPostPolicyShape 验证 policy 的 conditions 结构。
//
// 关键断言：content-length-range 必须是**扁平三元组**
// ["content-length-range", min, max]，不是 {"content-length-range": [min, max]}。
// 写错成对象形式的话 OSS 会静默忽略这条条件，上限根本不生效 ——
// 这种错不会报错，只会让限制失效，所以要用测试钉死。
func TestPostPolicyShape(t *testing.T) {
	client := &Client{cfg: Config{
		Bucket: "chatroomoss",
		Region: "cn-guangzhou",
	}}

	cred := &STSCredential{
		AccessKeyId:     "STS.test",
		AccessKeySecret: "secret",
		SecurityToken:   "token",
	}
	// 绕过 STS 缓存，直接塞一份凭证。
	client.stsCred = cred
	client.stsCached = timeNowForTest()

	form, err := client.GeneratePostUploadForm(t.Context(), PostUploadOptions{
		Dir:         "avatar/u1/",
		Ext:         "png",
		AllowedExts: []string{"png"},
		MinSize:     1,
		MaxSize:     5 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(form.Policy)
	if err != nil {
		t.Fatalf("policy 不是合法 base64: %v", err)
	}
	var policy struct {
		Conditions []any `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("policy 不是合法 JSON: %v", err)
	}

	// 找 content-length-range 那条。
	var clr []any
	for _, c := range policy.Conditions {
		arr, ok := c.([]any)
		if !ok {
			continue
		}
		if len(arr) > 0 && arr[0] == "content-length-range" {
			clr = arr
		}
	}
	if clr == nil {
		t.Fatalf("policy 里没有 content-length-range 条件: %s", raw)
	}
	if len(clr) != 3 {
		t.Fatalf("content-length-range 必须是 3 元素扁平数组, 实际 len=%d: %v", len(clr), clr)
	}
	if got := clr[1].(float64); got != 1 {
		t.Errorf("min 不对: %v", got)
	}
	if got := clr[2].(float64); got != 5*1024*1024 {
		t.Errorf("max 不对: %v", got)
	}
}

// TestPostPolicyNoMaxSize 没给上限就该报错 —— 不能签出一个没限制的表单。
func TestPostPolicyNoMaxSize(t *testing.T) {
	client := &Client{cfg: Config{Bucket: "b", Region: "r"}}
	client.stsCred = &STSCredential{AccessKeyId: "a", AccessKeySecret: "s", SecurityToken: "t"}
	client.stsCached = timeNowForTest()

	_, err := client.GeneratePostUploadForm(t.Context(), PostUploadOptions{
		Dir:         "avatar/u1/",
		Ext:         "png",
		AllowedExts: []string{"png"},
		MaxSize:     0,
	})
	if err != ErrInvalidMaxSize {
		t.Fatalf("应该返 ErrInvalidMaxSize, 实际: %v", err)
	}
}

// timeNowForTest 返回「现在」，让测试里的 STS 凭证缓存保持有效。
func timeNowForTest() time.Time {
	return time.Now()
}

// TestPostSigningKeyDerived 验 V4 密钥派生：日期和地域都参与。
func TestPostSigningKeyDerived(t *testing.T) {
	client := &Client{cfg: Config{Bucket: "b", Region: "cn-guangzhou"}}

	k1 := client.derivePostSigningKey("secret", "20240102")
	k2 := client.derivePostSigningKey("secret", "20240102")
	if string(k1) != string(k2) {
		t.Fatal("同参数应派生出同密钥")
	}
	if len(k1) != 32 {
		t.Fatalf("signing key 应为 32 字节，实际 %d", len(k1))
	}
	if string(client.derivePostSigningKey("secret", "20240103")) == string(k1) {
		t.Fatal("日期参与派生，换日期应换密钥")
	}

	client.cfg.Region = "cn-shanghai"
	if string(client.derivePostSigningKey("secret", "20240102")) == string(k1) {
		t.Fatal("地域参与派生，换地域应换密钥")
	}
}

// TestPostPolicyContentType 验 MIME 限制写进了 policy。
//
// 用的是 **in** 而不是 eq —— eq 只接受一个值，而头像有 jpeg/png/webp 好几种。
// 写错成 eq 的话只认一种 MIME，另外两种正常上传会被拒。
func TestPostPolicyContentType(t *testing.T) {
	client := &Client{cfg: Config{Bucket: "b", Region: "cn-guangzhou"}}
	client.stsCred = &STSCredential{AccessKeyId: "a", AccessKeySecret: "s", SecurityToken: "t"}
	client.stsCached = timeNowForTest()

	form, err := client.GeneratePostUploadForm(t.Context(), PostUploadOptions{
		Dir:          "avatar/u1/",
		Ext:          "png",
		AllowedExts:  []string{"png"},
		MaxSize:      1024,
		ContentTypes: []string{"image/jpeg", "image/png", "image/webp"},
	})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	raw, _ := base64.StdEncoding.DecodeString(form.Policy)
	var policy struct {
		Conditions []any `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("policy 不是合法 JSON: %v", err)
	}

	found := false
	for _, c := range policy.Conditions {
		arr, ok := c.([]any)
		if !ok || len(arr) != 3 {
			continue
		}
		if arr[0] != "in" || arr[1] != "$content-type" {
			continue
		}
		found = true
		types, ok := arr[2].([]any)
		if !ok {
			t.Fatalf("in 条件的值应该是数组: %v", arr[2])
		}
		if len(types) != 3 {
			t.Errorf("MIME 白名单应该有 3 项，实际 %d: %v", len(types), types)
		}
		want := map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
		for _, tp := range types {
			s, _ := tp.(string)
			if !want[s] {
				t.Errorf("不该有的 MIME: %q", s)
			}
		}
	}
	if !found {
		t.Fatalf("policy 里没有 in $content-type 条件: %s", raw)
	}
}

// TestPostPolicyNoContentType 不传 MIME 白名单就不写这条条件。
func TestPostPolicyNoContentType(t *testing.T) {
	client := &Client{cfg: Config{Bucket: "b", Region: "cn-guangzhou"}}
	client.stsCred = &STSCredential{AccessKeyId: "a", AccessKeySecret: "s", SecurityToken: "t"}
	client.stsCached = timeNowForTest()

	form, err := client.GeneratePostUploadForm(t.Context(), PostUploadOptions{
		Dir:         "avatar/u1/",
		Ext:         "png",
		AllowedExts: []string{"png"},
		MaxSize:     1024,
	})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	raw, _ := base64.StdEncoding.DecodeString(form.Policy)
	var policy struct {
		Conditions []any `json:"conditions"`
	}
	_ = json.Unmarshal(raw, &policy)

	for _, c := range policy.Conditions {
		arr, ok := c.([]any)
		if !ok || len(arr) != 3 {
			continue
		}
		if arr[1] == "$content-type" {
			t.Fatalf("没传白名单却写了 content-type 条件: %s", raw)
		}
	}
}
