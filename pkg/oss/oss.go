// Package oss 对接阿里云 OSS：签发直传凭证（STS + V4 签名）、拼公开 URL、校验对象。
//
// 本包不含业务语义 —— 不知道「头像」是什么，只知道 key 前缀和后缀白名单。
// 业务规则（哪些后缀算头像、上多大）由各服务自己的 biz 层决定。
//
// 客户端直传流程：
//
//  1. 客户端调业务接口拿上传凭证（STS 临时 AK + V4 policy + 签名）
//  2. 客户端直接 POST 文件到 OSS，服务端不碰文件字节
//  3. 客户端把 OSS 返回的 URL 提交给业务接口落库
package oss

import (
	"errors"
	"strings"
)

// 基础错误。
var (
	// ErrConfigIncomplete 配置缺项。
	ErrConfigIncomplete = errors.New("oss: 配置缺失")
	// ErrAssumeRoleFailed STS AssumeRole 失败。
	ErrAssumeRoleFailed = errors.New("oss: STS AssumeRole 失败")
	// ErrInvalidExt 后缀不在白名单内。
	ErrInvalidExt = errors.New("oss: 后缀不在白名单内")
	// ErrObjectNotFound OSS 上没有这个对象。
	ErrObjectNotFound = errors.New("oss: 对象不存在")
	// ErrObjectMismatch 对象大小或 content-type 跟预期不符。
	ErrObjectMismatch = errors.New("oss: 对象属性不匹配")
)

// SignatureVersion OSS V4 签名版本号。
const SignatureVersion = "OSS4-HMAC-SHA256"

// 默认参数。
const (
	// DefaultSTSSessionDuration STS 临时凭证有效期。
	DefaultSTSSessionDuration = 3600 // 秒
	// DefaultPolicyExpiration 上传 policy 有效期。
	DefaultPolicyExpiration = 3600 // 秒
	// DefaultSTSCacheTTL 本地 STS 凭证缓存时长。
	//
	// 必须小于 DefaultSTSSessionDuration —— 缓存的凭证被浏览器用的时候
	// 还得没过期。
	DefaultSTSCacheTTL = 1800 // 秒
)

// Config OSS 配置。
type Config struct {
	// Endpoint OSS 服务端点，如 oss-cn-hangzhou.aliyuncs.com。
	Endpoint string
	// Bucket 存储空间名。
	Bucket string
	// Region 地域，如 cn-hangzhou。用于 V4 签名派生和拼 host。
	Region string
	// RoleArn STS 角色 ARN，如 acs:ram::123456789:role/oss-upload。
	RoleArn string
	// AccessKeyID 长期 RAM AK。只用于调 STS，不出服务端。
	AccessKeyID string
	// AccessKeySecret 长期 RAM SK。只用于调 STS，不出服务端。
	AccessKeySecret string
	// UploadHost 客户端直传目标，如 https://bucket.oss-cn-hangzhou.aliyuncs.com。
	// 留空回退到 https://<bucket>.<endpoint>。
	UploadHost string
	// URLPrefix 公开访问前缀，如 https://cdn.example.com。
	// 留空表示不做公开访问 —— 只能靠签名 URL 取。
	URLPrefix string
}

// validate 校验配置完整性。
func (c Config) validate() error {
	missing := make([]string, 0, 5)
	if c.Bucket == "" {
		missing = append(missing, "bucket")
	}
	if c.Region == "" {
		missing = append(missing, "region")
	}
	if c.RoleArn == "" {
		missing = append(missing, "role_arn")
	}
	if c.AccessKeyID == "" {
		missing = append(missing, "access_key_id")
	}
	if c.AccessKeySecret == "" {
		missing = append(missing, "access_key_secret")
	}
	if len(missing) > 0 {
		return ErrConfigIncomplete
	}
	return nil
}

// uploadHost 返回客户端直传目标。
func (c Config) uploadHost() string {
	if c.UploadHost != "" {
		return strings.TrimRight(c.UploadHost, "/")
	}
	if c.Endpoint == "" {
		return ""
	}
	return "https://" + c.Bucket + "." + c.Endpoint
}

// URL 拼公开访问地址。key 为空时返空串。
// 入参可以是裸 key，也可以是完整 URL —— 完整 URL 会先抽出 key 再拼。
func (c Config) URL(fileKey string) string {
	if fileKey == "" {
		return ""
	}
	prefix := strings.TrimRight(c.URLPrefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/" + strings.TrimPrefix(extractFileKey(fileKey), "/")
}

// extractFileKey 从裸 key 或完整 URL 里取出 object key。
func extractFileKey(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	// 带 scheme 的当 URL 处理，取 path 部分。
	if i := strings.Index(in, "://"); i >= 0 {
		rest := in[i+3:]
		// 去掉 host。
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[j:]
		} else {
			rest = ""
		}
		// 去掉 query 和 fragment。
		if j := strings.IndexAny(rest, "?#"); j >= 0 {
			rest = rest[:j]
		}
		return strings.TrimPrefix(rest, "/")
	}
	return strings.TrimPrefix(in, "/")
}
