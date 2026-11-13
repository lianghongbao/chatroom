package service

import (
	"context"

	pb "chatroom/api/message/service/v1"
	"chatroom/app/message/service/internal/biz"
	"chatroom/pkg/middleware"
)

// MessageService message.proto 的 transport adapter。
// 只做请求校验 + 从 ctx 取调用者身份 + DO→DTO 转换。
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

// messageTypeFromDTO proto MessageType → biz.MessageType。t 是 proto 里的消息类型枚举，
// 未知值返 ErrMessageInvalidArgument。
func messageTypeFromDTO(t pb.MessageType) (biz.MessageType, error) {
	switch t {
	case pb.MessageType_MESSAGE_TYPE_UNSPECIFIED:
		// 缺省 —— 按文本处理。
		return biz.MessageTypeText, nil
	case pb.MessageType_MESSAGE_TYPE_TEXT:
		return biz.MessageTypeText, nil
	case pb.MessageType_MESSAGE_TYPE_IMAGE:
		return biz.MessageTypeImage, nil
	default:
		return 0, biz.ErrMessageInvalidArgument
	}
}

// convertMessageInfo biz.Message → proto.MessageInfo。m 是领域对象，m == nil 时返 nil。
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

// messageTypeToDTO biz.MessageType (1/2) → proto MessageType (1/2)。t 是领域里的消息类型，
// 未知值返 UNSPECIFIED。
func messageTypeToDTO(t biz.MessageType) pb.MessageType {
	switch t {
	case biz.MessageTypeText:
		return pb.MessageType_MESSAGE_TYPE_TEXT
	case biz.MessageTypeImage:
		return pb.MessageType_MESSAGE_TYPE_IMAGE
	default:
		return pb.MessageType_MESSAGE_TYPE_UNSPECIFIED
	}
}

// clampPageSize 限制 page_size 在合法区间（1..200）。n 是请求里的分页大小，其它值回退默认 50。
func clampPageSize(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
