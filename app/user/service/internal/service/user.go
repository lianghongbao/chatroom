package service

import (
	"context"

	pb "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/middleware"

	"github.com/go-kratos/kratos/v3/log"
)

// UserService user.proto 的 transport adapter：参数校验、调 usecase、DO ↔ DTO 转换。
type UserService struct {
	pb.UnimplementedUserServer

	uc   *biz.UserUsecase
	auth *conf.Auth
}

// NewUserService 构造 UserService。
// uc 用户用例
// auth 鉴权配置段
func NewUserService(uc *biz.UserUsecase, auth *conf.Auth) *UserService {
	return &UserService{uc: uc, auth: auth}
}

// Register 注册用户。req 是注册请求，返回新用户 ID。
// phone 可空，非空时全局唯一。email 可空，不唯一也不校验格式。
func (s *UserService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	id, err := s.uc.Register(ctx, req.GetUsername(), req.GetPassword(), req.GetNickname(), req.GetPhone(), req.GetEmail())
	if err != nil {
		return nil, err
	}
	return &pb.RegisterReply{Id: id}, nil
}

// Login 登录。req 是登录请求；老字段 token / user_id 兼容填充 access_token。
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

// RefreshToken 用 refresh token 换新的一对。req 是刷新请求。
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

// Logout 登出。
// req 登出请求
// access 来自 ctx
// refresh 解析失败时 refreshJTI 留空。
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

// parseRefreshJTI 解析 refresh token 拿 JTI。
// raw 是 token 串
// 解析失败返空串。
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

// GetUser 返回当前用户资料。
// req 查询请求
// uid 由 ctx 派生。
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

// UpdateUser 更新资料。req 是更新请求，partial update 白名单 nickname / avatar / phone / email；
// 空 mask 走全量路径（只有 nickname + avatar）。
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
		case "phone":
			// 空串 = 清空手机号 —— 跟 nickname / avatar 不同，这里允许空值。
			v := req.GetPhone()
			fields.Phone = &v
		case "email":
			// 空串 = 清空邮箱。语义跟 phone 一样。
			v := req.GetEmail()
			fields.Email = &v
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

// GetUploadToken 签发头像直传凭证。req 是签发请求，返回预签名的直传地址。
//
// 客户端拿到地址后直接 PUT 文件到 OSS，服务端不碰文件字节。
func (s *UserService) GetUploadToken(ctx context.Context, req *pb.GetUploadTokenRequest) (*pb.GetUploadTokenReply, error) {
	if req == nil {
		return nil, biz.ErrUserInvalidArgument
	}
	uid, ok := middleware.FromContext(ctx)
	if !ok {
		return nil, biz.ErrUserInvalidArgument
	}
	token, err := s.uc.GetUploadToken(ctx, uid, req.GetExt())
	if err != nil {
		return nil, err
	}
	return &pb.GetUploadTokenReply{
		Method:        token.Method,
		Url:           token.Url,
		FileKey:       token.FileKey,
		PublicUrl:     token.PublicUrl,
		SignedHeaders: token.SignedHeaders,
		ExpireAt:      token.ExpireAt,
	}, nil
}

// ChangePassword 改密。req 是改密请求；改完强制重登（黑名单当前 access + 撤销当前 refresh）。
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

// GetUserByID 查指定用户资料。req 是查询请求；查他人时清空 email / phone，
// biz 已按可见性折叠不可见。
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

// GetUsersByIDs 批量按 ID 查公开资料。req 是批量查询请求；避免 N+1。
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

// GetUserByUsername 按用户名精确查公开资料。req 是按用户名查询的请求。
// 加好友时用户记不住 uid，用用户名找人。不做可见性判定，但清空邮箱/电话。
func (s *UserService) GetUserByUsername(ctx context.Context, req *pb.GetUserByUsernameRequest) (*pb.UserInfo, error) {
	if req == nil || req.GetUsername() == "" {
		return nil, biz.ErrUserInvalidArgument
	}
	u, err := s.uc.GetUserByUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	return convertUserReply(u), nil
}

// GetUserByPhone 按手机号精确查公开资料。req 是按手机号查询的请求。
// 加好友时用户常常只记得手机号。不做可见性判定，但清空邮箱/电话。
func (s *UserService) GetUserByPhone(ctx context.Context, req *pb.GetUserByPhoneRequest) (*pb.UserInfo, error) {
	if req == nil || req.GetPhone() == "" {
		return nil, biz.ErrUserInvalidArgument
	}
	u, err := s.uc.GetUserByPhone(ctx, req.GetPhone())
	if err != nil {
		return nil, err
	}
	return convertUserReply(u), nil
}

// GetFriendProfiles 批量拉好友公开资料，供 friend 服务补全好友列表。req 是批量查询请求。
// 走内部凭证鉴权，不走用户 JWT，故不取 ctx 里的 uid。
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
// req 批量查询请求
// 走内部凭证鉴权，不走用户 JWT，故不取 ctx 里的 uid。
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

// convertLoginReply DO→DTO。
// res token 对
// uid 用户 ID
// 老字段 token / user_id 用 access_token 兜底。
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

// convertUserReply DO→DTO。u 是用户资料。
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
