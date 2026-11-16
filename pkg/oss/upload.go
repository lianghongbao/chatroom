package oss

import (
	"context"
	"errors"
	"strings"
)

// 上传策略错误。
//
// 这一层的错误是**框架级**的 —— 各服务把自己的错误映射成自己的 API 错误原因
// （见各域 error_reason.proto），业务代码不该直接用这里的错误当 API 响应。
var (
	// ErrUploadExtNotAllowed 后缀不在白名单内。
	ErrUploadExtNotAllowed = errors.New("oss: 后缀不在白名单内")
	// ErrUploadObjectNotFound OSS 上没有这个对象。
	ErrUploadObjectNotFound = errors.New("oss: 对象不存在")
	// ErrUploadObjectMismatch 对象大小或类型跟预期不符。
	ErrUploadObjectMismatch = errors.New("oss: 对象与上传策略不符")
	// ErrUploadURLInvalid 地址不是本系统签发的。
	ErrUploadURLInvalid = errors.New("oss: 地址非法")
	// ErrUploadNotConfigured 对象存储没配。
	ErrUploadNotConfigured = errors.New("oss: 对象存储未配置")
	// ErrUploadScopeEmpty 上传范围标识为空。
	ErrUploadScopeEmpty = errors.New("oss: 上传范围标识为空")
)

// UploadPolicy 一类上传对象的策略。
//
// 用户头像、群头像的差别只有「存哪个目录」和「谁能改」——
// 后者由各服务自己的业务规则管，这里只管前者加上传相关的共性约束。
type UploadPolicy struct {
	// Dir object key 的目录前缀，末尾带斜杠，如 "avatar/uuid7/"。
	//
	// 会写进 policy 的 starts-with 条件 —— 客户端拿着临时凭证也只能写进这个目录。
	Dir string
	// AllowedExts 允许的后缀白名单。留空表示不允许任何后缀。
	AllowedExts []string
	// MaxSize 单文件大小上限（字节）。必须大于 0。
	MaxSize int64
	// MinSize 单文件大小下限（字节）。小于等于 0 表示不限下界。
	MinSize int64
}

// Uploader 按策略签发直传凭证并校验对象地址。
//
// 它把「签发表单 + 校验地址」两件事放在一起，因为两边必须用**同一套**
// 目录前缀和后缀白名单 —— 签发时放宽、校验时收紧会留下能写不能用的凭证；
// 反过来则是能通过校验但写不进去的地址。
type Uploader struct {
	// client OSS 客户端。nil 表示对象存储没配，上传功能关闭。
	client *Client
	// policy 上传策略。
	policy UploadPolicy
	// allowedHosts 允许的 host（不含 scheme 和端口），小写。
	allowedHosts []string
}

// NewUploader 构造 Uploader。
// client OSS 客户端，为 nil 时返回的 Uploader 不可用
// policy 上传策略
// allowedHosts 额外允许的 host（CDN 域名等），允许没有 scheme
//
// 返回的 Uploader 在 client 为 nil 时仍可用，但所有签发 / 校验方法都返回错误。
func NewUploader(client *Client, policy UploadPolicy, allowedHosts []string) *Uploader {
	hosts := make([]string, 0, len(allowedHosts)+2)
	if client != nil {
		hosts = appendHost(hosts, client.UploadHost())
		hosts = appendHost(hosts, client.cfg.URLPrefix)
	}
	return &Uploader{
		client:       client,
		policy:       policy,
		allowedHosts: hosts,
	}
}

// appendHost 从 URL 里取出 host 并追加到列表。
// list 已有 host 列表
// raw URL 原文，允许没有 scheme
func appendHost(list []string, raw string) []string {
	if raw == "" {
		return list
	}
	// 没有 scheme 就当成纯 host。
	host := raw
	if _, after, ok := strings.Cut(raw, "://"); ok {
		host = after
	}
	// 去掉路径和端口。
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return list
	}
	return append(list, strings.ToLower(host))
}

// Available 对象存储是否可用。
func (u *Uploader) Available() bool {
	return u != nil && u.client != nil
}

// Policy 返回上传策略。
func (u *Uploader) Policy() UploadPolicy {
	return u.policy
}

// IssueUploadForm 签发一次直传表单。
// ctx 上下文
// ext 文件后缀，不带点
//
// 返回的表单里 key 前缀已经锁死在 policy.Dir —— 客户端改不了。
func (u *Uploader) IssueUploadForm(ctx context.Context, ext string) (*PostUploadForm, error) {
	if !u.Available() {
		return nil, ErrUploadNotConfigured
	}
	ext = NormalizeExt(ext)
	if err := ValidateExt(ext, u.policy.AllowedExts); err != nil {
		return nil, ErrUploadExtNotAllowed
	}
	// 大小上限必须有值 —— 没有上限就等于没开这个功能，
	// 不该签出一个什么都能传的表单。
	if u.policy.MaxSize <= 0 {
		return nil, ErrUploadNotConfigured
	}
	// 没配 url_prefix 时签不出公开地址 —— 客户端拿到空的 public_url，
	// 上传完不知道该把哪个地址存下来。与其发一个半成品凭证，不如早失败。
	if u.client.Config().URLPrefix == "" {
		return nil, ErrUploadNotConfigured
	}
	return u.client.GeneratePostUploadForm(ctx, PostUploadOptions{
		Dir:          u.policy.Dir,
		Ext:          ext,
		AllowedExts:  u.policy.AllowedExts,
		MinSize:      u.policy.MinSize,
		MaxSize:      u.policy.MaxSize,
		ContentTypes: u.contentTypes(),
	})
}

// ValidateURL 校验一个地址是不是本系统签发过的对象地址，并确认对象真的存在。
// ctx 上下文
// raw 待校验的地址
//
// 返回去掉 query 和 fragment 的干净 URL。
//
// 三道关：
//  1. URL 的 host / 目录前缀 / 后缀要在白名单内
//  2. 对象必须真的存在，且大小和类型符合预期
func (u *Uploader) ValidateURL(ctx context.Context, raw string) (string, error) {
	if !u.Available() {
		return "", ErrUploadNotConfigured
	}
	clean, err := u.client.ValidateURL(raw, URLRules{
		AllowedHosts:    u.allowedHosts,
		AllowedPrefixes: []string{u.policy.Dir},
		AllowedExts:     u.policy.AllowedExts,
	})
	if err != nil {
		return "", ErrUploadURLInvalid
	}
	// 对象必须存在 —— 否则库里会留下一个坏图。
	if err := u.client.CheckObject(ctx, clean, u.policy.MaxSize, u.contentTypes()); err != nil {
		switch {
		case errors.Is(err, ErrObjectNotFound):
			return "", ErrUploadObjectNotFound
		case errors.Is(err, ErrObjectMismatch):
			return "", ErrUploadObjectMismatch
		default:
			return "", err
		}
	}
	return clean, nil
}

// ValidateURLFormat 只校验地址格式，不确认对象是否存在。
// raw 待校验的地址
//
// 用于已经落库的历史数据 —— 那时候没做存在性校验，
// 不能因为对象已被清理就判非法。
func (u *Uploader) ValidateURLFormat(raw string) (string, error) {
	if !u.Available() {
		return "", ErrUploadNotConfigured
	}
	clean, err := u.client.ValidateURL(raw, URLRules{
		AllowedHosts:    u.allowedHosts,
		AllowedPrefixes: []string{u.policy.Dir},
		AllowedExts:     u.policy.AllowedExts,
	})
	if err != nil {
		return "", ErrUploadURLInvalid
	}
	return clean, nil
}

// contentTypes 返回允许的 MIME 类型。
//
// 后缀白名单挡的是文件名，content-type 挡的是真实内容 ——
// 改后缀骗过去的文件在这里会被拦下。
func (u *Uploader) contentTypes() []string {
	return ContentTypesByExt(u.policy.AllowedExts)
}

// ContentTypesByExt 把后缀白名单映射成 MIME 类型白名单。
// exts 后缀白名单
//
// 返回 nil 表示不限定 content-type。
func ContentTypesByExt(exts []string) []string {
	if len(exts) == 0 {
		return nil
	}
	types := make([]string, 0, len(exts))
	for _, ext := range exts {
		if mime := MIMEByExt(ext); mime != "" {
			types = append(types, mime)
		}
	}
	return types
}
