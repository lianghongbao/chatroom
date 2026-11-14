package biz

import (
	"context"
	"strings"

	v1 "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/oss"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// ErrStorageNotConfigured 对象存储没配。签发直传凭证时返回。
var ErrStorageNotConfigured = kratoserrors.ServiceUnavailable(v1.ErrorReason_USER_STORAGE_NOT_CONFIGURED.String(), "object storage is not configured")

// ErrUploadExtNotAllowed 后缀不在白名单内。
var ErrUploadExtNotAllowed = kratoserrors.BadRequest(v1.ErrorReason_USER_UPLOAD_EXT_NOT_ALLOWED.String(), "file extension not allowed")

// ErrUploadObjectNotFound OSS 上没有这个对象。
var ErrUploadObjectNotFound = kratoserrors.BadRequest(v1.ErrorReason_USER_UPLOAD_OBJECT_NOT_FOUND.String(), "uploaded object not found")

// ErrUploadObjectMismatch 对象大小或类型跟预期不符。
var ErrUploadObjectMismatch = kratoserrors.BadRequest(v1.ErrorReason_USER_UPLOAD_OBJECT_MISMATCH.String(), "uploaded object does not match policy")

// ErrUploadURLInvalid 头像地址不是本系统签发的。
var ErrUploadURLInvalid = kratoserrors.BadRequest(v1.ErrorReason_USER_UPLOAD_URL_INVALID.String(), "avatar url is not valid")

// avatarDir 头像的 object key 前缀。
// uid 是当前用户 ID。
//
// 前缀按 uid 拼 —— 客户端拿着临时凭证也只能写进自己的目录。
func avatarDir(uid string) string {
	return "avatar/" + uid + "/"
}

// StorageOptions 对象存储能力的装配参数。
type StorageOptions struct {
	// Client OSS 客户端，为 nil 时关闭上传。
	Client *oss.Client
	// AllowedExts 头像允许的后缀白名单。
	AllowedExts []string
	// MaxSize 头像最大字节数，0 表示不限。
	MaxSize int64
	// AllowedHosts 头像地址允许的 host。
	AllowedHosts []string
}

// NewStorageOptions 把 OSS 客户端和配置段翻译成 StorageOptions。
// client OSS 客户端，为 nil 时关闭上传
// s storage 配置段，为 nil 时关闭上传
func NewStorageOptions(client *oss.Client, s *conf.Storage) StorageOptions {
	if s == nil {
		return StorageOptions{Client: client}
	}
	return StorageOptions{
		Client:       client,
		AllowedExts:  s.GetAvatarAllowedExts(),
		MaxSize:      s.GetAvatarMaxSize(),
		AllowedHosts: allowedHosts(client, s),
	}
}

// allowedHosts 返回头像地址允许的 host 列表。
// client OSS 客户端，为 nil 时返空
// s storage 配置段
func allowedHosts(client *oss.Client, s *conf.Storage) []string {
	hosts := make([]string, 0, 2)
	if client != nil {
		hosts = appendHost(hosts, client.UploadHost())
	}
	hosts = appendHost(hosts, s.GetUrlPrefix())
	return hosts
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

// GetUploadToken 签发头像直传凭证。
// ctx 上下文
// uid 当前用户 ID
// ext 文件后缀，不带点
//
// 返回预签名的直传地址 —— 客户端把文件 PUT 过去，服务端不碰文件字节。
func (uc *UserUsecase) GetUploadToken(ctx context.Context, uid, ext string) (*oss.PresignedUpload, error) {
	if uc.oss == nil {
		return nil, ErrStorageNotConfigured
	}
	if uid == "" {
		return nil, ErrUserInvalidArgument
	}
	ext = oss.NormalizeExt(ext)
	if err := oss.ValidateExt(ext, uc.avatarExts); err != nil {
		return nil, ErrUploadExtNotAllowed
	}

	dir := avatarDir(uid)
	return uc.oss.GenerateUploadToken(ctx, oss.UploadOptions{
		Dir:         dir,
		Ext:         ext,
		ContentType: oss.MIMEByExt(ext),
		AllowedExts: uc.avatarExts,
		MaxSize:     uc.avatarMaxSize,
	})
}

// ValidateAvatarURL 校验头像地址，并确认对象真的在 OSS 上。
// ctx 上下文
// uid 当前用户 ID
// raw 待校验的头像地址
//
// 返回去掉 query 和 fragment 的干净 URL。
//
// 三道关：
//  1. URL 的 host / 前缀 / 后缀要在白名单内
//  2. 前缀必须是自己 uid 的目录 —— 不能引用别人的头像
//  3. 对象必须真的存在，且大小和类型符合预期
func (uc *UserUsecase) ValidateAvatarURL(ctx context.Context, uid, raw string) (string, error) {
	if uc.oss == nil {
		return "", ErrStorageNotConfigured
	}
	if uid == "" {
		return "", ErrUserInvalidArgument
	}

	clean, err := uc.oss.ValidateURL(raw, oss.URLRules{
		AllowedHosts:    uc.ossHosts,
		AllowedPrefixes: []string{avatarDir(uid)},
		AllowedExts:     uc.avatarExts,
	})
	if err != nil {
		return "", ErrUploadURLInvalid
	}

	// 对象必须存在 —— 否则库里会留下一个坏图。
	if err := uc.oss.CheckObject(ctx, clean, uc.avatarMaxSize, uc.avatarContentTypes()); err != nil {
		switch err {
		case oss.ErrObjectNotFound:
			return "", ErrUploadObjectNotFound
		case oss.ErrObjectMismatch:
			return "", ErrUploadObjectMismatch
		default:
			return "", err
		}
	}
	return clean, nil
}

// avatarContentTypes 返回头像允许的 MIME 类型。
//
// 后缀白名单挡的是文件名，content-type 挡的是真实内容 ——
// 改后缀骗过去的文件在这里会被拦下。
func (uc *UserUsecase) avatarContentTypes() []string {
	if len(uc.avatarExts) == 0 {
		return nil
	}
	types := make([]string, 0, len(uc.avatarExts))
	for _, ext := range uc.avatarExts {
		if mime := oss.MIMEByExt(ext); mime != "" {
			types = append(types, mime)
		}
	}
	return types
}

// avatarURLAllowed 判断头像地址是不是本系统签发的（不查 OSS）。
// uid 当前用户 ID
// raw 待校验的头像地址
//
// 只看格式，不确认对象是否存在 —— 用于已经落库的历史数据，
// 那时候没做存在性校验，不能因为对象已被清理就判非法。
func (uc *UserUsecase) avatarURLAllowed(uid, raw string) bool {
	if uc.oss == nil {
		return false
	}
	clean, err := uc.oss.ValidateURL(raw, oss.URLRules{
		AllowedHosts:    uc.ossHosts,
		AllowedPrefixes: []string{avatarDir(uid)},
		AllowedExts:     uc.avatarExts,
	})
	return err == nil && clean != ""
}
