package service

import (
	"context"

	"chatroom/pkg/middleware"
	pb "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"

	"github.com/go-kratos/kratos/v3/log"
)

// UserService user.proto 的 transport adapter：参数校验、调 usecase、DO ↔ DTO 转换。
type UserService struct {
	pb.UnimplementedUserServer

	uc   *biz.UserUsecase
	auth *conf.Auth
}

// NewUserService 构造 UserService。
func NewUserService(uc *biz.UserUsecase, auth *conf.Auth) *UserService {
	return &UserService{uc: uc, auth: auth}
}

// Register 转发注册请求到 usecase。
func (s *UserService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	id, err := s.uc.Register(ctx, req.GetUsername(), req.GetPassword(), req.GetNickname())
	if err != nil {
		return nil, err
	}
	return &pb.RegisterReply{Id: id}, nil
}

// Login 登录入口；老字段 token / user_id 兼容填充 access_token。
func (s *UserService) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	res, uid, err := s.uc.Login(ctx, req.GetUsername(), req.GetPassword(), biz.ClientIPFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return convertLoginReply(res, uid), nil
}

// RefreshToken refresh 换新一对。
func (s *UserService) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.LoginReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	res, err := s.uc.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, err
	}
	return convertLoginReply(res, ""), nil
}

// Logout access 来自 ctx，refresh 解析失败时 refreshJTI 留空。
func (s *UserService) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	accessJTI, _ := middleware.JTIFromContext(ctx)
	accessExp, _ := middleware.ExpFromContext(ctx)
	refreshJTI := s.parseRefreshJTI(ctx, req.GetRefreshToken())

	if err := s.uc.Logout(ctx, accessJTI, refreshJTI, accessExp); err != nil {
		return nil, err
	}
	return &pb.LogoutReply{}, nil
}

// parseRefreshJTI 解析 refresh 拿 jti，解析失败返空串。
func (s *UserService) parseRefreshJTI(ctx context.Context, raw string) string {
	if raw == "" || s.auth == nil {
		return ""
	}
	_, jti, _, err := middleware.ParseRefreshToken(raw, s.auth.GetJwtSecret(), s.auth.GetTokenIssuer())
	if err != nil {
		log.WarnContext(ctx, "logout 解析 refresh 失败", "err", err)
		return ""
	}
	return jti
}

// GetUser 返回当前用户资料。uid 由 ctx 派生。
func (s *UserService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserInfo, error) {
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}
	u, err := s.uc.GetUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	return convertUserReply(u), nil
}

// UpdateUser partial update，白名单 nickname/avatar；空 mask 走老全量路径。
func (s *UserService) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.UserInfo, error) {
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}

	maskPaths := req.GetUpdateMask().GetPaths()
	if len(maskPaths) == 0 {
		if req.GetNickname() == "" && req.GetAvatar() == "" {
			return nil, biz.ErrUserInvalidArgument
		}
		u, err := s.uc.UpdateUser(ctx, uid, req.GetNickname(), req.GetAvatar())
		if err != nil {
			return nil, err
		}
		return convertUserReply(u), nil
	}

	fields := biz.UserUpdateFields{}
	for _, p := range maskPaths {
		switch p {
		case "nickname":
			v := req.GetNickname()
			fields.Nickname = &v
		case "avatar":
			v := req.GetAvatar()
			fields.Avatar = &v
		default:
			// 非白名单字段忽略，不报错
		}
	}
	u, err := s.uc.UpdateUserFields(ctx, uid, fields)
	if err != nil {
		return nil, err
	}
	return convertUserReply(u), nil
}

// ChangePassword 改密后强制重登（黑名单当前 access + 撤销当前 refresh）。
func (s *UserService) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*pb.ChangePasswordReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}

	accessJTI, _ := middleware.JTIFromContext(ctx)
	accessExp, _ := middleware.ExpFromContext(ctx)
	// refresh token 也要撤销 —— 只拉黑 access 的话，refresh 还在，
	// 拿着它还能换出新 access，等于改密没生效。
	refreshJTI := s.parseRefreshJTI(ctx, req.GetRefreshToken())

	if err := s.uc.ChangePassword(ctx, uid, req.GetOldPassword(), req.GetNewPassword(), accessJTI, refreshJTI, accessExp); err != nil {
		return nil, err
	}
	return &pb.ChangePasswordReply{}, nil
}

// GetUserByID 查他人时清空 email/phone，biz 已按可见性折叠不可见。
func (s *UserService) GetUserByID(ctx context.Context, req *pb.GetUserByIDRequest) (*pb.UserInfo, error) {
	if req == nil || req.GetId() == "" {
		return nil, biz.ErrUserInvalidArgument
	}
	viewerID, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}
	u, err := s.uc.GetUserByID(ctx, viewerID, req.GetId())
	if err != nil {
		return nil, err
	}
	reply := convertUserReply(u)
	if viewerID != req.GetId() {
		reply.Email = nil
		reply.Phone = nil
	}
	return reply, nil
}

// GetUsersByIDs 批量按 ID 查公开资料，避免 N+1。
func (s *UserService) GetUsersByIDs(ctx context.Context, req *pb.GetUsersByIDsRequest) (*pb.GetUsersByIDsReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	viewerID, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}
	users, err := s.uc.GetUsersByIDs(ctx, viewerID, req.GetIds())
	if err != nil {
		return nil, err
	}
	out := &pb.GetUsersByIDsReply{Users: make([]*pb.UserInfo, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, convertUserReply(u))
	}
	return out, nil
}

// GetFriendProfiles 批量拉好友公开资料，供 friend 服务补全好友列表。
// 走内部凭证（X-Internal-Token）鉴权，不走用户 JWT，故不取 ctx 里的 uid。
func (s *UserService) GetFriendProfiles(ctx context.Context, req *pb.GetFriendProfilesRequest) (*pb.GetFriendProfilesReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	users, err := s.uc.GetFriendProfiles(ctx, req.GetIds())
	if err != nil {
		return nil, err
	}
	out := &pb.GetFriendProfilesReply{Users: make([]*pb.UserInfo, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, convertUserReply(u))
	}
	return out, nil
}

// GetConversationMemberProfiles 批量拉会话成员公开资料，供 conversation 服务补全成员列表。
// 走内部凭证（X-Internal-Token）鉴权，不走用户 JWT，故不取 ctx 里的 uid。
func (s *UserService) GetConversationMemberProfiles(ctx context.Context, req *pb.GetConversationMemberProfilesRequest) (*pb.GetConversationMemberProfilesReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	users, err := s.uc.GetConversationMemberProfiles(ctx, req.GetIds())
	if err != nil {
		return nil, err
	}
	out := &pb.GetConversationMemberProfilesReply{Users: make([]*pb.UserInfo, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, convertUserReply(u))
	}
	return out, nil
}

// convertLoginReply DTO 转换，老字段 token / user_id 用 access_token 兜底。
func convertLoginReply(res *biz.LoginResult, uid string) *pb.LoginReply {
	if res == nil {
		return &pb.LoginReply{}
	}
	return &pb.LoginReply{
		Token:        res.AccessToken,
		UserId:       uid,
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresIn:    res.ExpiresIn,
		TokenType:    "Bearer",
	}
}

// convertUserReply DO→DTO，PasswordHash 已由 biz 清空。
func convertUserReply(u *biz.User) *pb.UserInfo {
	if u == nil {
		return nil
	}
	reply := &pb.UserInfo{
		Id: u.ID, Username: u.Username, Nickname: u.Nickname, Avatar: u.Avatar,
		CreatedAt: u.CreatedAt.Unix(),
	}
	if u.Email != "" {
		e := u.Email
		reply.Email = &e
	}
	if u.Phone != "" {
		p := u.Phone
		reply.Phone = &p
	}
	return reply
}
