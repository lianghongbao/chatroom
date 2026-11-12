package service

import (
	"context"

	pb "chatroom/api/friend/service/v1"
	"chatroom/app/friend/service/internal/biz"
	"chatroom/pkg/middleware"
)

// FriendService friend.proto 的 transport adapter。
// 只做请求校验 + 取 caller uid + DO→DTO 转换。
type FriendService struct {
	pb.UnimplementedFriendServer

	uc *biz.FriendUsecase
}

// NewFriendService new a friend service.
func NewFriendService(uc *biz.FriendUsecase) *FriendService {
	return &FriendService{uc: uc}
}

// ApplyFriend 发起好友申请；caller uid 由 ctx 派生。
func (s *FriendService) ApplyFriend(ctx context.Context, req *pb.ApplyFriendRequest) (*pb.ApplyFriendReply, error) {
	if req == nil || req.GetTargetId() == "" {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	// 附言长度按 rune 计 —— 中文一个字算一个，不是三个字节。
	if len([]rune(req.GetMessage())) > biz.MaxApplyMessageLen {
		return nil, biz.ErrFriendInvalidArgument
	}
	res, err := s.uc.Apply(ctx, uid, req.GetTargetId(), req.GetMessage())
	if err != nil {
		return nil, err
	}
	return &pb.ApplyFriendReply{
		RequestId: res.RequestID,
		Status:    friendStatusToDTO(res.Status),
	}, nil
}

// AcceptFriend 同意好友申请；caller uid 由 ctx 派生。
func (s *FriendService) AcceptFriend(ctx context.Context, req *pb.AcceptFriendRequest) (*pb.AcceptFriendReply, error) {
	if req == nil || req.GetRequestId() == 0 {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	f, err := s.uc.Accept(ctx, uid, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	return &pb.AcceptFriendReply{Friend: convertFriend(f)}, nil
}

// RejectFriend 拒绝好友申请；caller uid 由 ctx 派生。
func (s *FriendService) RejectFriend(ctx context.Context, req *pb.RejectFriendRequest) (*pb.RejectFriendReply, error) {
	if req == nil || req.GetRequestId() == 0 {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	if err := s.uc.Reject(ctx, uid, req.GetRequestId()); err != nil {
		return nil, err
	}
	return &pb.RejectFriendReply{}, nil
}

// RemoveFriend 删除好友；caller uid 由 ctx 派生。
func (s *FriendService) RemoveFriend(ctx context.Context, req *pb.RemoveFriendRequest) (*pb.RemoveFriendReply, error) {
	if req == nil || req.GetFriendId() == "" {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	if err := s.uc.Remove(ctx, uid, req.GetFriendId()); err != nil {
		return nil, err
	}
	return &pb.RemoveFriendReply{}, nil
}

// ListFriends 列好友；caller uid 由 ctx 派生。
func (s *FriendService) ListFriends(ctx context.Context, req *pb.ListFriendsRequest) (*pb.ListFriendsReply, error) {
	if req == nil {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	friends, next, err := s.uc.List(ctx, uid, req.GetCursor(), clampPageSize(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	out := &pb.ListFriendsReply{NextCursor: next, Friends: make([]*pb.FriendInfo, 0, len(friends))}
	for _, f := range friends {
		out.Friends = append(out.Friends, convertFriend(f))
	}
	return out, nil
}

// ListPendingRequests 列好友申请收件箱；caller uid 由 ctx 派生。
func (s *FriendService) ListPendingRequests(ctx context.Context, req *pb.ListPendingRequestsRequest) (*pb.ListPendingRequestsReply, error) {
	if req == nil {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	reqs, next, err := s.uc.ListPendingRequests(ctx, uid, req.GetCursor(), clampPageSize(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	out := &pb.ListPendingRequestsReply{NextCursor: next, Requests: make([]*pb.FriendInfo, 0, len(reqs))}
	for _, f := range reqs {
		out.Requests = append(out.Requests, convertFriend(f))
	}
	return out, nil
}

// CountPending 查待处理申请数；caller uid 由 ctx 派生。
func (s *FriendService) CountPending(ctx context.Context, req *pb.CountPendingRequest) (*pb.CountPendingReply, error) {
	if req == nil {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	n, err := s.uc.CountPending(ctx, uid)
	if err != nil {
		return nil, err
	}
	return &pb.CountPendingReply{Count: n}, nil
}

// convertFriend biz.Friend → proto.FriendInfo。f == nil 时返 nil。
//
// friend_id 始终是「对方」的 uid：正向关系取 FriendID，反向关系取 UserID。
// biz 层不映射这层语义 —— 它只知道关系记录的两端是谁。
func convertFriend(f *biz.Friend) *pb.FriendInfo {
	if f == nil {
		return nil
	}
	info := &pb.FriendInfo{
		Id:              f.ID,
		Message:         f.Message,
		CreatedAtFriend: f.CreatedAt.Unix(),
	}
	// Profile 为 nil = 未补全 / 对方不可见。昵称头像留空，不报错。
	if f.Profile != nil {
		info.FriendId = f.Profile.UserID
		info.Nickname = f.Profile.Nickname
		info.Avatar = f.Profile.Avatar
		info.CreatedAt = f.Profile.CreatedAt.Unix()
	} else {
		info.FriendId = f.FriendID
	}
	return info
}

// friendStatusToDTO biz.FriendStatus (0/1/2) → proto FriendStatus (1/2/3)。
// proto 的 UNSPECIFIED=0 跟 biz 的 PENDING=0 撞位，所以要整体平移。
func friendStatusToDTO(s biz.FriendStatus) pb.FriendStatus {
	switch s {
	case biz.FriendStatusPending:
		return pb.FriendStatus_FRIEND_STATUS_PENDING
	case biz.FriendStatusAccepted:
		return pb.FriendStatus_FRIEND_STATUS_ACCEPTED
	case biz.FriendStatusRejected:
		return pb.FriendStatus_FRIEND_STATUS_REJECTED
	default:
		return pb.FriendStatus_FRIEND_STATUS_UNSPECIFIED
	}
}

// clampPageSize 限制 page_size 在合法区间（1..200），其它回退默认 50。
func clampPageSize(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
