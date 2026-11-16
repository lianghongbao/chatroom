package service

import (
	"context"

	pb "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/biz"
	"chatroom/pkg/middleware"
	"chatroom/pkg/oss"
)

// MessageService message.proto 的 transport adapter。
// 只做请求校验、从 ctx 取调用者身份、DO→DTO 转换。
type MessageService struct {
	pb.UnimplementedMessageServer

	uc *biz.MessageUsecase
}

// NewMessageService 构造 MessageService。uc 是消息用例。
func NewMessageService(uc *biz.MessageUsecase) *MessageService {
	return &MessageService{uc: uc}
}

// SendMessage 发消息。req 是发消息请求。
func (s *MessageService) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	// type 是枚举，未校验的转换会把非法值带进 biz 层。
	typ, err := messageTypeFromDTO(req.GetType())
	if err != nil {
		return nil, err
	}
	msg, err := s.uc.SendMessage(ctx, uid, req.GetConversationId(), typ, req.GetContent())
	if err != nil {
		return nil, err
	}
	return &pb.SendMessageReply{Message: convertMessageInfo(msg)}, nil
}

// ListMessages 拉历史消息，按 id 倒序（新的在前）。req 是历史消息请求。
func (s *MessageService) ListMessages(ctx context.Context, req *pb.ListMessagesRequest) (*pb.ListMessagesReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	msgs, next, err := s.uc.ListMessages(ctx, uid, req.GetConversationId(), req.GetCursor(), clampPageSize(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	out := &pb.ListMessagesReply{NextCursor: next, Messages: make([]*pb.MessageInfo, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, convertMessageInfo(m))
	}
	return out, nil
}

// ListMessagesSince 拉「比某个 id 更新的」消息，按 id 正序（旧的在前）。req 是补消息请求。
func (s *MessageService) ListMessagesSince(ctx context.Context, req *pb.ListMessagesSinceRequest) (*pb.ListMessagesSinceReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	msgs, last, more, err := s.uc.ListMessagesSince(ctx, uid, req.GetConversationId(), req.GetSinceId(), clampPageSize(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	out := &pb.ListMessagesSinceReply{
		LastMessageId: last,
		HasMore:       more,
		Messages:      make([]*pb.MessageInfo, 0, len(msgs)),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, convertMessageInfo(m))
	}
	return out, nil
}

// MarkRead 清零调用方自己的会话未读数。req 是清未读请求。
func (s *MessageService) MarkRead(ctx context.Context, req *pb.MarkReadRequest) (*pb.MarkReadReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	n, err := s.uc.MarkRead(ctx, uid, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	return &pb.MarkReadReply{UnreadCount: n}, nil
}

// GetUnreadCount 查未读数。不传 conversation_id 时返回所有会话的未读总数。req 是查未读请求。
func (s *MessageService) GetUnreadCount(ctx context.Context, req *pb.GetUnreadCountRequest) (*pb.GetUnreadCountReply, error) {
	if req == nil {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	total, per, err := s.uc.GetUnreadCount(ctx, uid, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	out := &pb.GetUnreadCountReply{UnreadCount: total}
	// per_conversation 只在没指定 conversation_id 时有值。
	if req.GetConversationId() == 0 && len(per) > 0 {
		out.PerConversation = per
	}
	return out, nil
}

// GetImageUploadToken 签发聊天图片直传凭证。req 是签发请求。
func (s *MessageService) GetImageUploadToken(ctx context.Context, req *pb.GetImageUploadTokenRequest) (*pb.GetImageUploadTokenReply, error) {
	if req == nil {
		return nil, biz.ErrMessageInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrMessageInvalidArgument
	}
	token, err := s.uc.GetImageUploadToken(ctx, uid, req.GetExt())
	if err != nil {
		return nil, err
	}
	return &pb.GetImageUploadTokenReply{
		Host:      token.Host,
		Fields:    postFormFields(token),
		FileKey:   token.Key,
		PublicUrl: token.PublicUrl,
		ExpireAt:  token.ExpireAt,
		MaxSize:   token.MaxSize,
	}, nil
}

// postFormFields 把 PostObject 凭证转成客户端要提交的表单域。
// token 签发出来的凭证
//
// key 放在 fields 里 —— 客户端自己指定 object key，
// 服务端给的只是推荐值；policy 限定了前缀，客户端改不出自己的目录。
func postFormFields(token *oss.PostUploadForm) map[string]string {
	if token == nil {
		return nil
	}
	return map[string]string{
		"key":                     token.Key,
		"policy":                  token.Policy,
		"x-oss-signature-version": oss.SignatureVersion,
		"x-oss-credential":        token.Credential,
		"x-oss-date":              token.DateTime,
		"x-oss-security-token":    token.SecurityToken,
		"x-oss-signature":         token.Signature,
		"success_action_status":   "200",
	}
}

// messageTypeFromDTO 把 proto MessageType 转成 biz.MessageType。
// t 是 proto 里的消息类型枚举，未知值返 ErrMessageInvalidArgument。
func messageTypeFromDTO(t pb.MessageType) (biz.MessageType, error) {
	switch t {
	case pb.MessageType_MESSAGE_TYPE_UNSPECIFIED:
		// 缺省 —— 按文本处理。
		return biz.MessageTypeText, nil
	case pb.MessageType_MESSAGE_TYPE_TEXT:
		return biz.MessageTypeText, nil
	case pb.MessageType_MESSAGE_TYPE_IMAGE:
		return biz.MessageTypeImage, nil
	case pb.MessageType_MESSAGE_TYPE_EMOJI:
		return biz.MessageTypeEmoji, nil
	default:
		return 0, biz.ErrMessageInvalidArgument
	}
}

// convertMessageInfo 把 biz.Message 转成 proto.MessageInfo。m 是领域对象。
func convertMessageInfo(m *biz.Message) *pb.MessageInfo {
	if m == nil {
		return nil
	}
	return &pb.MessageInfo{
		Id:             m.ID,
		ConversationId: m.ConversationID,
		SenderId:       m.SenderID,
		Type:           messageTypeToDTO(m.Type),
		Content:        m.Content,
		CreatedAt:      m.CreatedAt.Unix(),
	}
}

// messageTypeToDTO 把 biz.MessageType 转成 proto MessageType。t 是领域里的消息类型，未知值返 UNSPECIFIED。
func messageTypeToDTO(t biz.MessageType) pb.MessageType {
	switch t {
	case biz.MessageTypeText:
		return pb.MessageType_MESSAGE_TYPE_TEXT
	case biz.MessageTypeImage:
		return pb.MessageType_MESSAGE_TYPE_IMAGE
	case biz.MessageTypeEmoji:
		return pb.MessageType_MESSAGE_TYPE_EMOJI
	default:
		return pb.MessageType_MESSAGE_TYPE_UNSPECIFIED
	}
}

// clampPageSize 限制 page_size 在合法区间（1..200）。n 是请求里的分页大小。
func clampPageSize(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
