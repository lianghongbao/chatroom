package biz

import (
	"context"
	"strconv"

	v1 "chatroom/api/conversation/service/v1"
	"chatroom/app/conversation/service/internal/conf"
	"chatroom/pkg/oss"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// 群头像相关的 API 错误。
//
// 错误原因在 api/conversation/service/v1/error_reason.proto 里声明 ——
// 跟其他域的命名风格一致，前端按 reason 字符串映射文案。
var (
	// ErrStorageNotConfigured 对象存储没配。签发直传凭证时返回。
	ErrStorageNotConfigured = kratoserrors.ServiceUnavailable(v1.ErrorReason_CONVERSATION_STORAGE_NOT_CONFIGURED.String(), "object storage is not configured")

	// ErrUploadExtNotAllowed 群头像文件后缀不在白名单内。
	ErrUploadExtNotAllowed = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_UPLOAD_EXT_NOT_ALLOWED.String(), "file extension not allowed")

	// ErrUploadObjectNotFound OSS 上没有这个对象。
	ErrUploadObjectNotFound = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_UPLOAD_OBJECT_NOT_FOUND.String(), "uploaded object not found")

	// ErrUploadObjectMismatch 对象大小或类型跟预期不符。
	ErrUploadObjectMismatch = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_UPLOAD_OBJECT_MISMATCH.String(), "uploaded object does not match policy")

	// ErrUploadURLInvalid 群头像地址不是本系统签发的。
	ErrUploadURLInvalid = kratoserrors.BadRequest(v1.ErrorReason_CONVERSATION_UPLOAD_URL_INVALID.String(), "group avatar url is not valid")
)

// groupAvatarDir 群头像的 object key 前缀。
// conversationID 是会话 ID。
//
// 前缀按会话 ID 拼 —— 客户端拿着临时凭证也只能写进自己群的目录，
// 写不进别的群。跟个人头像的 avatar/<uid>/ 是两套目录，不会混。
func groupAvatarDir(conversationID uint64) string {
	return "group_avatar/" + strconv.FormatUint(conversationID, 10) + "/"
}

// StorageOptions 对象存储能力的装配参数。
type StorageOptions struct {
	// Client OSS 客户端，为 nil 时关闭群头像上传。
	Client *oss.Client
	// AllowedExts 群头像允许的后缀白名单。
	AllowedExts []string
	// MaxSize 群头像最大字节数，0 表示不限。
	MaxSize int64
}

// NewStorageOptions 把 OSS 客户端和配置段翻译成 StorageOptions。
// client OSS 客户端，为 nil 时关闭群头像上传
// s storage 配置段，为 nil 时关闭群头像上传
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

// GetGroupAvatarUploadToken 签发群头像直传凭证。
// ctx 上下文
// callerID 调用方 uid
// conversationID 会话 ID
// ext 文件后缀，不带点
//
// 返回 PostObject 表单 —— 客户端把文件 POST 到 OSS，服务端不碰文件字节。
//
// 仅群主可调用 —— 凭证本身就能往群里写文件，权限必须在这里判。
func (uc *ConversationUsecase) GetGroupAvatarUploadToken(ctx context.Context, callerID string, conversationID uint64, ext string) (*oss.PostUploadForm, error) {
	if uc.repo == nil {
		return nil, ErrConversationInvalidArgument
	}
	if callerID == "" || conversationID == 0 {
		return nil, ErrConversationInvalidArgument
	}
	// 先查会话再校验参数 —— 非群主报 NOT_OWNER，而不是被参数校验先拦下。
	conv, err := uc.repo.FindByID(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if !conv.Type.IsGroup() {
		return nil, ErrConversationNotGroup
	}
	if conv.OwnerID != callerID {
		return nil, ErrConversationNotOwner
	}

	form, err := uc.groupAvatarUploader(conversationID).IssueUploadForm(ctx, ext)
	if err != nil {
		return nil, mapUploadError(err)
	}
	return form, nil
}

// ValidateGroupAvatarURL 校验群头像地址，并确认对象真的在 OSS 上。
// ctx 上下文
// conversationID 会话 ID
// raw 待校验的群头像地址
//
// 返回去掉 query 和 fragment 的干净 URL。
//
// 三道关：
//  1. URL 的 host / 目录前缀 / 后缀要在白名单内
//  2. 前缀必须是自己群的目录 —— 不能引用别的群的头像
//  3. 对象必须真的存在，且大小和类型符合预期
func (uc *ConversationUsecase) ValidateGroupAvatarURL(ctx context.Context, conversationID uint64, raw string) (string, error) {
	if conversationID == 0 {
		return "", ErrConversationInvalidArgument
	}
	clean, err := uc.groupAvatarUploader(conversationID).ValidateURL(ctx, raw)
	if err != nil {
		return "", mapUploadError(err)
	}
	return clean, nil
}

// groupAvatarUploader 返回指定群的群头像上传器。
// conversationID 会话 ID
func (uc *ConversationUsecase) groupAvatarUploader(conversationID uint64) *oss.Uploader {
	return oss.NewUploader(uc.oss, oss.UploadPolicy{
		Dir:         groupAvatarDir(conversationID),
		AllowedExts: uc.avatarExts,
		MaxSize:     uc.avatarMaxSize,
		MinSize:     1,
	}, nil)
}

// mapUploadError 把 pkg/oss 的框架级错误映射成群聊域的 API 错误。
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
