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

// NewFriendService 构造 FriendService。
// uc 好友用例
func NewFriendService(uc *biz.FriendUsecase) *FriendService {
	return &FriendService{uc: uc}
}

// ApplyFriend 发起好友申请。req 是请求，caller uid 由 ctx 派生。
func (s *FriendService) ApplyFriend(ctx context.Context, req *pb.ApplyFriendRequest) (*pb.ApplyFriendReply, error) {
	if req == nil || req.GetTargetId() == "" {
		return nil, biz.ErrFriendInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrFriendInvalidArgument
	}
	// 附言长度不在这里判 —— 截断是 biz 层的职责（超过 MaxApplyMessageLen
	// 的部分被裁掉，而不是整条申请被拒）。service 只做非空校验。
	res, err := s.uc.Apply(ctx, uid, req.GetTargetId(), req.GetMessage())
	if err != nil {
		return nil, err
	}
	return convertApplyReply(res), nil
}

// AcceptFriend 同意好友申请。req 是请求，caller uid 由 ctx 派生。
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
	return &pb.AcceptFriendReply{Friend: convertFriendInfo(f)}, nil
}

// RejectFriend 拒绝好友申请。req 是请求，caller uid 由 ctx 派生。
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

// RemoveFriend 删除好友。req 是请求，caller uid 由 ctx 派生。
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

// ListFriends 列好友。req 是请求，caller uid 由 ctx 派生。
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
		out.Friends = append(out.Friends, convertFriendInfo(f))
	}
	return out, nil
}

// ListPendingRequests 列好友申请收件箱。req 是请求，caller uid 由 ctx 派生。
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
		out.Requests = append(out.Requests, convertFriendInfo(f))
	}
	return out, nil
}

// CountPending 查待处理申请数。req 是请求，caller uid 由 ctx 派生。
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

// convertFriendInfo 把 biz.Friend 折成 proto.FriendInfo。f == nil 时返 nil。
// friend_id 始终是「对方」的 uid：正向关系取 FriendID，反向关系取 UserID。
func convertFriendInfo(f *biz.Friend) *pb.FriendInfo {
	if f == nil {
		return nil
	}
	info := &pb.FriendInfo{
		Id:      f.ID,
		Message: f.Message,
		// created_at_friend 是「关系建立时间」——status 转 ACCEPTED 的时刻，
		// 对应 DO 的 UpdatedAt，不是记录创建时间 CreatedAt。
		CreatedAtFriend: f.UpdatedAt.Unix(),
	}
	// Profile 为 nil 表示未补全或对方不可见。昵称头像留空，不报错。
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

// convertApplyReply 把 biz.FriendApplyResult 折成 proto.ApplyFriendReply。
// r == nil 时返一个空 reply（不是 nil）—— 调用方已经过了参数校验。
func convertApplyReply(r *biz.FriendApplyResult) *pb.ApplyFriendReply {
	if r == nil {
		return &pb.ApplyFriendReply{}
	}
	return &pb.ApplyFriendReply{
		RequestId: r.RequestID,
		Status:    friendStatusToDTO(r.Status),
	}
}

// friendStatusToDTO 把 biz.FriendStatus（0/1/2）折成 proto FriendStatus（1/2/3）。
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
// n 请求里的 page_size
func clampPageSize(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
