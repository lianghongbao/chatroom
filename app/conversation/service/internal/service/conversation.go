package service

import (
	"context"

	pb "chatroom/api/conversation/service/v1"
	"chatroom/app/conversation/service/internal/biz"
	"chatroom/pkg/middleware"
)

// ConversationService 会话服务的传输层适配器。
// 只做请求校验、取调用方 uid、DO→DTO 转换。
type ConversationService struct {
	pb.UnimplementedConversationServer

	uc *biz.ConversationUsecase
}

// NewConversationService 构造 ConversationService。uc 是会话用例。
func NewConversationService(uc *biz.ConversationUsecase) *ConversationService {
	return &ConversationService{uc: uc}
}

// CreateConversation 创建会话。req 是创建请求，调用方 uid 从 ctx 取。
func (s *ConversationService) CreateConversation(ctx context.Context, req *pb.CreateConversationRequest) (*pb.CreateConversationReply, error) {
	if req == nil {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	if len(req.GetMemberIds()) == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	// type 是枚举，未校验的转换会把非法值带进 biz 层。
	typ, err := conversationTypeFromDTO(req.GetType())
	if err != nil {
		return nil, err
	}
	if len(req.GetName()) > biz.MaxConversationNameLen {
		return nil, biz.ErrConversationInvalidArgument
	}
	conv, reused, err := s.uc.CreateConversation(ctx, uid, req.GetMemberIds(), typ, req.GetName())
	if err != nil {
		return nil, err
	}
	return &pb.CreateConversationReply{Conversation: convertConversation(conv), Reused: reused}, nil
}

// conversationTypeFromDTO 把 proto 会话类型转成 biz 类型。
// t 是 proto 枚举值，未知值返 ErrConversationInvalidArgument。
func conversationTypeFromDTO(t pb.ConversationType) (biz.ConversationType, error) {
	switch t {
	case pb.ConversationType_CONVERSATION_TYPE_UNSPECIFIED:
		// 缺省 —— biz 按成员数推断。
		return 0, nil
	case pb.ConversationType_CONVERSATION_TYPE_SINGLE:
		return biz.ConversationTypeSingle, nil
	case pb.ConversationType_CONVERSATION_TYPE_GROUP:
		return biz.ConversationTypeGroup, nil
	default:
		return 0, biz.ErrConversationInvalidArgument
	}
}

// ListConversations 列出调用方的所有会话。req 是列表请求，
// cursor 是上一页游标（首页传空），pageSize 是每页条数。
func (s *ConversationService) ListConversations(ctx context.Context, req *pb.ListConversationsRequest) (*pb.ListConversationsReply, error) {
	if req == nil {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	pageSize := clampPageSize(req.GetPageSize())
	convs, next, err := s.uc.ListConversations(ctx, uid, req.GetCursor(), pageSize)
	if err != nil {
		return nil, err
	}
	out := &pb.ListConversationsReply{NextCursor: next, Conversations: make([]*pb.ConversationInfo, 0, len(convs))}
	for _, c := range convs {
		out.Conversations = append(out.Conversations, convertConversation(c))
	}
	return out, nil
}

// GetConversation 查会话详情。req 是查询请求，调用方 uid 从 ctx 取。
func (s *ConversationService) GetConversation(ctx context.Context, req *pb.GetConversationRequest) (*pb.GetConversationReply, error) {
	if req == nil || req.GetId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	conv, err := s.uc.GetConversation(ctx, uid, req.GetId())
	if err != nil {
		return nil, err
	}
	return &pb.GetConversationReply{Conversation: convertConversation(conv)}, nil
}

// AddMembers 往会话加成员。req 是加成员请求，调用方 uid 从 ctx 取。
func (s *ConversationService) AddMembers(ctx context.Context, req *pb.AddMembersRequest) (*pb.AddMembersReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	if len(req.GetMemberIds()) == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	count, added, err := s.uc.AddMembers(ctx, uid, req.GetConversationId(), req.GetMemberIds())
	if err != nil {
		return nil, err
	}
	return &pb.AddMembersReply{MemberCount: count, AddedCount: added}, nil
}

// RemoveMember 踢人。req 是踢人请求，调用方 uid 从 ctx 取。
func (s *ConversationService) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*pb.RemoveMemberReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetMemberId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	count, err := s.uc.RemoveMember(ctx, uid, req.GetConversationId(), req.GetMemberId())
	if err != nil {
		return nil, err
	}
	return &pb.RemoveMemberReply{MemberCount: count}, nil
}

// LeaveConversation 主动退群。req 是退群请求，调用方 uid 从 ctx 取。
func (s *ConversationService) LeaveConversation(ctx context.Context, req *pb.LeaveConversationRequest) (*pb.LeaveConversationReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	if err := s.uc.LeaveConversation(ctx, uid, req.GetConversationId()); err != nil {
		return nil, err
	}
	return &pb.LeaveConversationReply{}, nil
}

// UpdateConversation 改会话名。req 是改名请求，调用方 uid 从 ctx 取。
func (s *ConversationService) UpdateConversation(ctx context.Context, req *pb.UpdateConversationRequest) (*pb.UpdateConversationReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	conv, err := s.uc.UpdateConversation(ctx, uid, req.GetConversationId(), req.GetName())
	if err != nil {
		return nil, err
	}
	return &pb.UpdateConversationReply{Conversation: convertConversation(conv)}, nil
}

// TransferOwner 转让群主。req 是转让请求，调用方 uid 从 ctx 取。
func (s *ConversationService) TransferOwner(ctx context.Context, req *pb.TransferOwnerRequest) (*pb.TransferOwnerReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetNewOwnerId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	conv, err := s.uc.TransferOwner(ctx, uid, req.GetConversationId(), req.GetNewOwnerId())
	if err != nil {
		return nil, err
	}
	return &pb.TransferOwnerReply{Conversation: convertConversation(conv)}, nil
}

// DissolveConversation 解散会话。req 是解散请求，调用方 uid 从 ctx 取。
func (s *ConversationService) DissolveConversation(ctx context.Context, req *pb.DissolveConversationRequest) (*pb.DissolveConversationReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	if err := s.uc.DissolveConversation(ctx, uid, req.GetConversationId()); err != nil {
		return nil, err
	}
	return &pb.DissolveConversationReply{}, nil
}

// ListMembers 列会话成员。req 是列表请求，调用方 uid 从 ctx 取。
func (s *ConversationService) ListMembers(ctx context.Context, req *pb.ListMembersRequest) (*pb.ListMembersReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrConversationInvalidArgument
	}
	conv, err := s.uc.ListMembers(ctx, uid, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	out := &pb.ListMembersReply{MemberIds: conv.MemberIDs}
	if len(conv.MemberNicknames) > 0 {
		out.MemberNicknames = conv.MemberNicknames
	}
	if len(conv.MemberAvatars) > 0 {
		out.MemberAvatars = conv.MemberAvatars
	}
	return out, nil
}

// CheckMembership 查某用户是不是会话成员。req 是查询请求，
// userId 是被查的用户 ID。
func (s *ConversationService) CheckMembership(ctx context.Context, req *pb.CheckMembershipRequest) (*pb.CheckMembershipReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	isMember, err := s.uc.CheckMembership(ctx, req.GetConversationId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &pb.CheckMembershipReply{IsMember: isMember}, nil
}

// UpdateUnread 增减某成员的会话级未读数。req 是请求，
// delta 是增减量（负数表示减少）。
func (s *ConversationService) UpdateUnread(ctx context.Context, req *pb.UpdateUnreadRequest) (*pb.UpdateUnreadReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	n, err := s.uc.UpdateUnread(ctx, req.GetConversationId(), req.GetUserId(), req.GetDelta())
	if err != nil {
		return nil, err
	}
	return &pb.UpdateUnreadReply{UnreadCount: n}, nil
}

// UpdateUnreadBatch 批量增减多个成员的会话级未读数，返回成功更新的成员数。
// req 是请求，delta 是增减量。
// 消息服务发消息后的 fan-out 用它 —— 逐个调 UpdateUnread 是 N 次跨进程调用。
func (s *ConversationService) UpdateUnreadBatch(ctx context.Context, req *pb.UpdateUnreadBatchRequest) (*pb.UpdateUnreadBatchReply, error) {
	if req == nil || req.GetConversationId() == 0 || len(req.GetUserIds()) == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	n, err := s.uc.UpdateUnreadBatch(ctx, req.GetConversationId(), req.GetUserIds(), req.GetDelta())
	if err != nil {
		return nil, err
	}
	return &pb.UpdateUnreadBatchReply{Updated: n}, nil
}

// GetUnread 查某成员的会话级未读数。req 是查询请求，userId 是成员 ID。
func (s *ConversationService) GetUnread(ctx context.Context, req *pb.GetUnreadRequest) (*pb.GetUnreadReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	n, err := s.uc.GetUnread(ctx, req.GetConversationId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &pb.GetUnreadReply{UnreadCount: n}, nil
}

// ClearUnread 把某成员的会话级未读数清零。req 是请求，userId 是成员 ID。
func (s *ConversationService) ClearUnread(ctx context.Context, req *pb.ClearUnreadRequest) (*pb.ClearUnreadReply, error) {
	if req == nil || req.GetConversationId() == 0 || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	n, err := s.uc.ClearUnread(ctx, req.GetConversationId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &pb.ClearUnreadReply{UnreadCount: n}, nil
}

// ListUnread 查某用户在所有会话的未读数。req 是查询请求，userId 是用户 ID。
func (s *ConversationService) ListUnread(ctx context.Context, req *pb.ListUnreadRequest) (*pb.ListUnreadReply, error) {
	if req == nil || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	per, err := s.uc.ListUnread(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if per == nil {
		per = map[uint64]uint32{}
	}
	return &pb.ListUnreadReply{PerConversation: per}, nil
}

// ListConversationMembers 列出会话的全部成员（服务间内部接口）。
// req 是查询请求。
func (s *ConversationService) ListConversationMembers(ctx context.Context, req *pb.ListConversationMembersRequest) (*pb.ListConversationMembersReply, error) {
	if req == nil || req.GetConversationId() == 0 {
		return nil, biz.ErrConversationInvalidArgument
	}
	ids, err := s.uc.ListConversationMembers(ctx, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	return &pb.ListConversationMembersReply{MemberIds: ids}, nil
}

// ListUserConversations 列出某个用户参与的所有会话 id（服务间内部接口）。
// req 是查询请求，userId 是用户 ID。
func (s *ConversationService) ListUserConversations(ctx context.Context, req *pb.ListUserConversationsRequest) (*pb.ListUserConversationsReply, error) {
	if req == nil || req.GetUserId() == "" {
		return nil, biz.ErrConversationInvalidArgument
	}
	ids, err := s.uc.ListUserConversations(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []uint64{}
	}
	return &pb.ListUserConversationsReply{ConversationIds: ids}, nil
}

// convertConversation 把 biz 会话转成 proto 会话信息。
// c 是领域模型，为 nil 时返 nil。昵称 / 头像数组与 member_ids 等长且下标对齐。
func convertConversation(c *biz.Conversation) *pb.ConversationInfo {
	if c == nil {
		return nil
	}
	info := &pb.ConversationInfo{
		Id:          c.ID,
		Type:        conversationTypeToDTO(c.Type),
		Name:        c.Name,
		OwnerId:     c.OwnerID,
		MemberCount: c.MemberCount,
		MemberIds:   c.MemberIDs,
		UpdatedAt:   c.UpdatedAt.Unix(),
		CreatedAt:   c.CreatedAt.Unix(),
	}
	if len(c.MemberNicknames) > 0 {
		info.MemberNicknames = c.MemberNicknames
	}
	if len(c.MemberAvatars) > 0 {
		info.MemberAvatars = c.MemberAvatars
	}
	if !c.LastMessageAt.IsZero() {
		info.LastMessage = c.LastMessage
		info.LastMessageAt = c.LastMessageAt.Unix()
	}
	return info
}

// conversationTypeToDTO 把 biz 会话类型转成 proto 枚举。
// t 是 biz 类型，未知值返 UNSPECIFIED。
func conversationTypeToDTO(t biz.ConversationType) pb.ConversationType {
	switch t {
	case biz.ConversationTypeSingle:
		return pb.ConversationType_CONVERSATION_TYPE_SINGLE
	case biz.ConversationTypeGroup:
		return pb.ConversationType_CONVERSATION_TYPE_GROUP
	default:
		return pb.ConversationType_CONVERSATION_TYPE_UNSPECIFIED
	}
}

// clampPageSize 限制每页条数在 1..200，n 越界时回退默认 50。
func clampPageSize(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
