package biz

import (
	"context"

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
}

// NewStorageOptions 把 OSS 客户端和配置段翻译成 StorageOptions。
// client OSS 客户端，为 nil 时关闭上传
// s storage 配置段，为 nil 时关闭上传
func NewStorageOptions(client *oss.Client, s *conf.Storage) StorageOptions {
	if s == nil {
		return StorageOptions{Client: client}
	}
	return StorageOptions{
		Client:      client,
		AllowedExts: s.GetAvatarAllowedExts(),
		MaxSize:     s.GetAvatarMaxSize(),
	}
}

// GetUploadToken 签发头像直传凭证。
// ctx 上下文
// uid 当前用户 ID
// ext 文件后缀，不带点
//
// 返回 PostObject 表单 —— 客户端把文件 POST 到 OSS，服务端不碰文件字节。
func (uc *UserUsecase) GetUploadToken(ctx context.Context, uid, ext string) (*oss.PostUploadForm, error) {
	if uid == "" {
		return nil, ErrUserInvalidArgument
	}
	form, err := uc.avatarUploader(uid).IssueUploadForm(ctx, ext)
	if err != nil {
		return nil, mapUploadError(err)
	}
	return form, nil
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
	if uid == "" {
		return "", ErrUserInvalidArgument
	}
	clean, err := uc.avatarUploader(uid).ValidateURL(ctx, raw)
	if err != nil {
		return "", mapUploadError(err)
	}
	return clean, nil
}

// avatarUploader 返回当前用户的头像上传器。
// uid 当前用户 ID
//
// 每个 uid 一个目录 —— 目录前缀按 uid 拼，客户端拿着临时凭证也只能写进
// 自己的目录。
func (uc *UserUsecase) avatarUploader(uid string) *oss.Uploader {
	return oss.NewUploader(uc.oss, oss.UploadPolicy{
		Dir:         avatarDir(uid),
		AllowedExts: uc.avatarExts,
		MaxSize:     uc.avatarMaxSize,
		MinSize:     1,
	}, nil)
}

// mapUploadError 把 pkg/oss 的框架级错误映射成用户域的 API 错误。
// err 原始错误
func mapUploadError(err error) error {
	switch err {
	case oss.ErrUploadNotConfigured:
		return ErrStorageNotConfigured
	case oss.ErrUploadExtNotAllowed:
		return ErrUploadExtNotAllowed
	case oss.ErrUploadObjectNotFound:
		return ErrUploadObjectNotFound
	case oss.ErrUploadObjectMismatch:
		return ErrUploadObjectMismatch
	case oss.ErrUploadURLInvalid:
		return ErrUploadURLInvalid
	default:
		return err
	}
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
	clean, err := uc.avatarUploader(uid).ValidateURLFormat(raw)
	return err == nil && clean != ""
}
