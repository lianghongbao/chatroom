package biz

import (
	"context"

	v1 "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/conf"
	"chatroom/pkg/oss"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// 聊天图片相关的 API 错误。
//
// 错误原因在 api/message/service/v1/error_reason.proto 里声明 ——
// 跟其他域的命名风格一致，前端按 reason 字符串映射文案。
var (
	// ErrStorageNotConfigured 对象存储没配。签发直传凭证时返回。
	ErrStorageNotConfigured = kratoserrors.ServiceUnavailable(v1.ErrorReason_MESSAGE_STORAGE_NOT_CONFIGURED.String(), "object storage is not configured")

	// ErrUploadExtNotAllowed 聊天图片文件后缀不在白名单内。
	ErrUploadExtNotAllowed = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_UPLOAD_EXT_NOT_ALLOWED.String(), "file extension not allowed")

	// ErrUploadObjectNotFound OSS 上没有这个对象。
	ErrUploadObjectNotFound = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_UPLOAD_OBJECT_NOT_FOUND.String(), "uploaded object not found")

	// ErrUploadObjectMismatch 对象大小或类型跟预期不符。
	ErrUploadObjectMismatch = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_UPLOAD_OBJECT_MISMATCH.String(), "uploaded object does not match policy")

	// ErrUploadURLInvalid 聊天图片地址不是本系统签发的。
	ErrUploadURLInvalid = kratoserrors.BadRequest(v1.ErrorReason_MESSAGE_UPLOAD_URL_INVALID.String(), "image url is not valid")
)

// chatImageDir 聊天图片的 object key 前缀。
// callerID 是调用方 uid。
//
// 前缀按 uid 拼 —— 客户端拿着临时凭证也只能写进自己名下的目录，
// 写不进别人的目录。跟个人头像的 avatar/<uid>/ 、群头像的
// group_avatar/<会话ID>/ 是三套目录，不会混。
func chatImageDir(callerID string) string {
	return "chat_image/" + callerID + "/"
}

// StorageOptions 对象存储能力的装配参数。
type StorageOptions struct {
	// Client OSS 客户端，为 nil 时关闭聊天图片上传。
	Client *oss.Client
	// AllowedExts 聊天图片允许的后缀白名单。
	AllowedExts []string
	// MaxSize 聊天图片最大字节数，0 表示不限。
	MaxSize int64
}

// NewStorageOptions 把 OSS 客户端和配置段翻译成 StorageOptions。
// client OSS 客户端，为 nil 时关闭聊天图片上传
// s storage 配置段，为 nil 时关闭聊天图片上传
func NewStorageOptions(client *oss.Client, s *conf.Storage) StorageOptions {
	if s == nil {
		return StorageOptions{Client: client}
	}
	return StorageOptions{
		Client:      client,
		AllowedExts: s.GetImageAllowedExts(),
		MaxSize:     s.GetImageMaxSize(),
	}
}

// GetImageUploadToken 签发聊天图片直传凭证。
// ctx 上下文
// callerID 调用方 uid
// ext 文件后缀，不带点
//
// 返回 PostObject 表单 —— 客户端把文件 POST 到 OSS，服务端不碰文件字节。
func (uc *MessageUsecase) GetImageUploadToken(ctx context.Context, callerID string, ext string) (*oss.PostUploadForm, error) {
	if callerID == "" {
		return nil, ErrMessageInvalidArgument
	}
	form, err := uc.chatImageUploader(callerID).IssueUploadForm(ctx, ext)
	if err != nil {
		return nil, mapUploadError(err)
	}
	return form, nil
}

// ValidateImageURL 校验聊天图片地址，并确认对象真的在 OSS 上。
// ctx 上下文
// callerID 调用方 uid
// raw 待校验的图片地址
//
// 返回去掉 query 和 fragment 的干净 URL。
//
// 三道关：
//  1. URL 的 host / 目录前缀 / 后缀要在白名单内
//  2. 前缀必须是自己名下的目录 —— 不能引用别人的图片
//  3. 对象必须真的存在，且大小和类型符合预期
func (uc *MessageUsecase) ValidateImageURL(ctx context.Context, callerID string, raw string) (string, error) {
	if callerID == "" {
		return "", ErrMessageInvalidArgument
	}
	clean, err := uc.chatImageUploader(callerID).ValidateURL(ctx, raw)
	if err != nil {
		return "", mapUploadError(err)
	}
	return clean, nil
}

// chatImageUploader 返回指定用户的聊天图片上传器。
// callerID 调用方 uid
func (uc *MessageUsecase) chatImageUploader(callerID string) *oss.Uploader {
	return oss.NewUploader(uc.oss, oss.UploadPolicy{
		Dir:         chatImageDir(callerID),
		AllowedExts: uc.imageExts,
		MaxSize:     uc.imageMaxSize,
		MinSize:     1,
	}, nil)
}

// mapUploadError 把 pkg/oss 的框架级错误映射成消息域的 API 错误。
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
