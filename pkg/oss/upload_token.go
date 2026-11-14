package oss

import (
	"context"
	"time"

	"github.com/google/uuid"

	alioss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

// UploadOptions 签发上传凭证的入参。
type UploadOptions struct {
	// Dir key 前缀，末尾必须带斜杠，如 "avatar/abc123/"。
	Dir string
	// Ext 文件后缀，不带点，如 "png"。
	Ext string
	// ContentType 文件的 MIME 类型，参与签名。留空表示不限定。
	ContentType string
	// AllowedExts 允许的后缀白名单。
	AllowedExts []string
	// MaxSize 允许的最大字节数，小于等于 0 表示不限。
	MaxSize int64
	// TTL 凭证有效期，小于等于 0 时用 DefaultPolicyExpiration。
	TTL int
}

// PresignedUpload 预签名的直传地址。
type PresignedUpload struct {
	// Method 上传方法，恒为 PUT。
	Method string
	// Url 带签名的直传地址。客户端直接 PUT 到这里。
	Url string
	// FileKey object key。
	FileKey string
	// PublicUrl 上传后的公开访问地址。
	PublicUrl string
	// SignedHeaders 客户端必须带上的请求头。
	SignedHeaders map[string]string
	// ExpireAt 过期时间，Unix 秒。
	ExpireAt int64
}

// GenerateUploadToken 签发一次直传凭证。
// ctx 上下文
// opts 上传参数
//
// 后缀不在白名单内时返 ErrInvalidExt。
//
// 产出的是**预签名 URL** —— 客户端把文件 PUT 到 Url 即可，
// 服务端不碰文件字节。
func (c *Client) GenerateUploadToken(ctx context.Context, opts UploadOptions) (*PresignedUpload, error) {
	if err := ValidateExt(opts.Ext, opts.AllowedExts); err != nil {
		return nil, err
	}
	dir := opts.Dir
	if dir == "" {
		dir = "/"
	}

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultPolicyExpiration
	}

	fileKey := dir + newObjectID() + "." + NormalizeExt(opts.Ext)

	presigned, err := c.PresignUploadURLWithType(ctx, fileKey, time.Duration(ttl)*time.Second, opts.ContentType)
	if err != nil {
		return nil, err
	}
	presigned.FileKey = fileKey
	presigned.PublicUrl = c.URL(fileKey)
	return presigned, nil
}

// PresignUploadURL 签一个预签名的直传地址。
// ctx 上下文
// fileKey object key
// ttl 有效期
//
// 客户端拿到后直接 PUT 文件到这个地址。
//
// Content-Type 参与签名 —— 客户端必须带上完全一样的这个值，
// 否则 OSS 算出的 canonical request 跟签名时的不一样，返 SignatureDoesNotMatch。
func (c *Client) PresignUploadURL(ctx context.Context, fileKey string, ttl time.Duration) (*PresignedUpload, error) {
	return c.PresignUploadURLWithType(ctx, fileKey, ttl, "")
}

// PresignUploadURLWithType 签一个预签名的直传地址，指定 Content-Type。
// ctx 上下文
// fileKey object key
// ttl 有效期
// contentType 文件的 MIME 类型。留空表示不限定 ——
// 但那样客户端必须也不带 Content-Type，实际用不上。
func (c *Client) PresignUploadURLWithType(ctx context.Context, fileKey string, ttl time.Duration, contentType string) (*PresignedUpload, error) {
	if ttl <= 0 {
		ttl = DefaultPolicyExpiration * time.Second
	}

	cred, err := c.fetchSTSCredential(ctx)
	if err != nil {
		return nil, err
	}

	client := c.ossClient(cred)
	result, err := client.Presign(ctx, &alioss.PutObjectRequest{
		Bucket:      alioss.Ptr(c.cfg.Bucket),
		Key:         alioss.Ptr(fileKey),
		ContentType: alioss.Ptr(contentType),
	}, alioss.PresignExpires(ttl))
	if err != nil {
		return nil, err
	}

	headers := make(map[string]string, len(result.SignedHeaders)+1)
	for k, v := range result.SignedHeaders {
		headers[k] = v
	}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}

	return &PresignedUpload{
		Method:        result.Method,
		Url:           result.URL,
		SignedHeaders: headers,
		ExpireAt:      result.Expiration.Unix(),
	}, nil
}

// newObjectID 生成 object key 里的唯一标识。
func newObjectID() string {
	return uuid.NewString()
}
