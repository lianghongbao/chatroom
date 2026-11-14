package oss

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Client OSS 客户端。进程内单例 —— STS 凭证缓存在实例上。
type Client struct {
	cfg Config

	// 长期 AK/SK 换 STS 临时凭证的本地缓存。
	stsMu     sync.Mutex
	stsCred   *STSCredential
	stsCached time.Time
}

// NewClient 构造 Client。
// cfg OSS 配置
//
// 配置不完整时返 ErrConfigIncomplete —— 缺 bucket / region / role_arn / AK / SK
// 的话签发出来的凭证一定不能用，不如早失败。
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg}, nil
}

// Config 返回当前配置。
func (c *Client) Config() Config {
	return c.cfg
}

// UploadHost 返回客户端直传目标。
func (c *Client) UploadHost() string {
	return c.cfg.uploadHost()
}

// URL 拼公开访问地址。key 为空时返空串。
func (c *Client) URL(fileKey string) string {
	return c.cfg.URL(fileKey)
}

// URLs 批量拼公开访问地址，顺序与入参一致。
func (c *Client) URLs(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = c.URL(k)
	}
	return out
}

// STSCredential STS 临时凭证。
type STSCredential struct {
	// AccessKeyId 临时 AK。
	AccessKeyId string
	// AccessKeySecret 临时 SK。**不能直接转发给客户端**。
	AccessKeySecret string
	// SecurityToken 临时 token。
	SecurityToken string
	// Expiration 过期时间。
	Expiration time.Time
}

// FetchSTSCredential 取 STS 临时凭证，命中缓存直接返回。
// ctx 上下文
func (c *Client) FetchSTSCredential(ctx context.Context) (*STSCredential, error) {
	return c.fetchSTSCredential(ctx)
}

// fetchSTSCredential 取 STS 临时凭证，命中缓存直接返回。
func (c *Client) fetchSTSCredential(ctx context.Context) (*STSCredential, error) {
	c.stsMu.Lock()
	defer c.stsMu.Unlock()

	now := time.Now()
	if c.stsCred != nil && now.Before(c.stsCached.Add(DefaultSTSCacheTTL*time.Second)) {
		return c.stsCred, nil
	}

	cred, err := c.assumeRole(ctx)
	if err != nil {
		return nil, err
	}
	c.stsCred = cred
	c.stsCached = now
	return cred, nil
}

// AssumeRole 调 STS 换取临时凭证。
func (c *Client) assumeRole(ctx context.Context) (*STSCredential, error) {
	cred, err := newRAMRoleCredential(ctx, c.cfg, DefaultSTSSessionDuration)
	if err != nil {
		return nil, ErrAssumeRoleFailed
	}
	return cred, nil
}

// NormalizeExt 归一化后缀：小写、去掉前导点。
func NormalizeExt(ext string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
}

// MIMEByExt 按后缀查 MIME 类型。
// ext 文件后缀，不带点
//
// 查不到时返空串 —— 调用方自己决定放不放行。
func MIMEByExt(ext string) string {
	switch NormalizeExt(ext) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

// ValidateExt 校验后缀是否在白名单内。
// ext 待校验后缀
// allowedExts 允许的后缀白名单
//
// 白名单为空时一律拒绝 —— 不做「空 = 放行」，
// 免得调用方漏传就把任意后缀放过去了。
func ValidateExt(ext string, allowedExts []string) error {
	if len(allowedExts) == 0 {
		return ErrInvalidExt
	}
	ext = NormalizeExt(ext)
	if ext == "" {
		return ErrInvalidExt
	}
	for _, allow := range allowedExts {
		if NormalizeExt(allow) == ext {
			return nil
		}
	}
	return ErrInvalidExt
}
