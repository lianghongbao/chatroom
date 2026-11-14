package service

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/middleware"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	fieldmaskpb "google.golang.org/protobuf/types/known/fieldmaskpb"
)

// bcryptHash 生成明文口令的 bcrypt 哈希，供 fake repo 造用户用。plain 是明文口令。
func bcryptHash(t *testing.T, plain string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return string(h)
}

// service 直接持有 *biz.UserUsecase（官方模板的做法），所以替身落在
// usecase 下面的 repo 接口这一层 —— service 测试断言「DTO 字段有没有
// 原样透传到 biz」，biz 层自己的规则由 biz 测试覆盖。

// fakeRepo 是 biz.UserRepo 的替身。每个方法把入参记到 calls 里，同时
// 按字段返回预设结果。
type fakeRepo struct {
	saveID    string
	saveErr   error
	saveCalls struct {
		user   *biz.User
		called bool
	}

	findByIDRet   *biz.User
	findByIDErr   error
	findByIDCalls struct {
		id     string
		called bool
	}

	findByUsernameRet *biz.User
	findByUsernameErr error

	findByPhoneRet *biz.User
	findByPhoneErr error

	findByIDsRet []*biz.User
	findByIDsErr error
	findByIDsIDs []string

	updateRet   *biz.User
	updateErr   error
	updateCalls struct {
		id               string
		nickname, avatar string
		called           bool
	}

	updateFieldsRet   *biz.User
	updateFieldsErr   error
	updateFieldsCalls struct {
		id     string
		fields biz.UserUpdateFields
		called bool
	}

	updatePasswordCalls struct {
		uid     string
		newHash string
		called  bool
	}

	updateStatusCalls struct {
		uid    string
		status biz.UserStatus
		called bool
	}
}

func (f *fakeRepo) Save(_ context.Context, u *biz.User) (string, error) {
	f.saveCalls.called = true
	f.saveCalls.user = u
	return f.saveID, f.saveErr
}

func (f *fakeRepo) FindByID(_ context.Context, id string) (*biz.User, error) {
	f.findByIDCalls.called = true
	f.findByIDCalls.id = id
	if f.findByIDErr != nil {
		return nil, f.findByIDErr
	}
	if f.findByIDRet == nil {
		return nil, biz.ErrUserNotFound
	}
	return f.findByIDRet, nil
}

func (f *fakeRepo) FindByIDWithHash(ctx context.Context, id string) (*biz.User, error) {
	return f.FindByID(ctx, id)
}

func (f *fakeRepo) FindByUsername(_ context.Context, username string) (*biz.User, error) {
	if f.findByUsernameErr != nil {
		return nil, f.findByUsernameErr
	}
	return f.findByUsernameRet, nil
}

func (f *fakeRepo) FindByPhone(_ context.Context, phone string) (*biz.User, error) {
	if f.findByPhoneErr != nil {
		return nil, f.findByPhoneErr
	}
	return f.findByPhoneRet, nil
}

func (f *fakeRepo) FindByIDs(_ context.Context, ids []string) ([]*biz.User, error) {
	f.findByIDsIDs = ids
	return f.findByIDsRet, f.findByIDsErr
}

func (f *fakeRepo) Update(_ context.Context, id string, nickname, avatar string) (*biz.User, error) {
	f.updateCalls.called = true
	f.updateCalls.id = id
	f.updateCalls.nickname = nickname
	f.updateCalls.avatar = avatar
	return f.updateRet, f.updateErr
}

func (f *fakeRepo) UpdateFields(_ context.Context, id string, fields biz.UserUpdateFields) (*biz.User, error) {
	f.updateFieldsCalls.called = true
	f.updateFieldsCalls.id = id
	f.updateFieldsCalls.fields = fields
	return f.updateFieldsRet, f.updateFieldsErr
}

func (f *fakeRepo) UpdatePassword(_ context.Context, id string, newHash string) error {
	f.updatePasswordCalls.called = true
	f.updatePasswordCalls.uid = id
	f.updatePasswordCalls.newHash = newHash
	return nil
}

func (f *fakeRepo) UpdateStatus(_ context.Context, id string, status biz.UserStatus) error {
	f.updateStatusCalls.called = true
	f.updateStatusCalls.uid = id
	f.updateStatusCalls.status = status
	return nil
}

// fakeTokenStore 是 biz.TokenStore 的替身，记下 logout / change-password
// 传进来的 token 信息。
type fakeTokenStore struct {
	blacklistCalls struct {
		jti    string
		ttl    time.Duration
		called bool
	}
	isBlacklistedRet bool

	saveRefreshCalls struct {
		jti       string
		uid       string
		userAgent string
		ttl       time.Duration
		called    bool
	}

	consumeRefreshUID   string
	consumeRefreshErr   error
	consumeRefreshCalls struct {
		jti    string
		called bool
	}

	revokeRefreshCalls struct {
		jti    string
		called bool
	}
}

func (f *fakeTokenStore) Blacklist(_ context.Context, jti string, ttl time.Duration) error {
	f.blacklistCalls.called = true
	f.blacklistCalls.jti = jti
	f.blacklistCalls.ttl = ttl
	return nil
}

func (f *fakeTokenStore) IsBlacklisted(_ context.Context, jti string) (bool, error) {
	return f.isBlacklistedRet, nil
}

func (f *fakeTokenStore) SaveRefresh(_ context.Context, jti string, uid string, userAgent string, ttl time.Duration) error {
	f.saveRefreshCalls.called = true
	f.saveRefreshCalls.jti = jti
	f.saveRefreshCalls.uid = uid
	f.saveRefreshCalls.userAgent = userAgent
	f.saveRefreshCalls.ttl = ttl
	return nil
}

func (f *fakeTokenStore) ConsumeRefresh(_ context.Context, jti string) (string, error) {
	f.consumeRefreshCalls.called = true
	f.consumeRefreshCalls.jti = jti
	return f.consumeRefreshUID, f.consumeRefreshErr
}

func (f *fakeTokenStore) RevokeRefresh(_ context.Context, jti string) error {
	f.revokeRefreshCalls.called = true
	f.revokeRefreshCalls.jti = jti
	return nil
}

// fakeLimiter 是 biz.LoginLimiter 的替身。
type fakeLimiter struct {
	allowErr   error
	allowCalls struct {
		username string
		clientIP string
		called   bool
	}
	onSuccessCalls struct {
		username string
		clientIP string
		called   bool
	}
	onFailureCalls struct {
		username string
		clientIP string
		called   bool
	}
}

func (f *fakeLimiter) Allow(_ context.Context, username, clientIP string) error {
	f.allowCalls.called = true
	f.allowCalls.username = username
	f.allowCalls.clientIP = clientIP
	return f.allowErr
}

func (f *fakeLimiter) OnSuccess(_ context.Context, username, clientIP string) {
	f.onSuccessCalls.called = true
	f.onSuccessCalls.username = username
	f.onSuccessCalls.clientIP = clientIP
}

func (f *fakeLimiter) OnFailure(_ context.Context, username, clientIP string) {
	f.onFailureCalls.called = true
	f.onFailureCalls.username = username
	f.onFailureCalls.clientIP = clientIP
}

// fakeLocker 是 biz.Locker 的替身，恒返可释放的 no-op。
type fakeLocker struct {
	acquireCalls struct {
		key    string
		ttl    time.Duration
		called bool
	}
}

func (f *fakeLocker) Acquire(_ context.Context, key string, ttl time.Duration) (biz.Release, error) {
	f.acquireCalls.called = true
	f.acquireCalls.key = key
	f.acquireCalls.ttl = ttl
	return func() {}, nil
}

// fakeHeader 是 transport.Header 的最小实现，仅 Get 被 auth 中间件使用。
type fakeHeader struct{ values map[string]string }

func (h *fakeHeader) Get(key string) string    { return h.values[key] }
func (h *fakeHeader) Set(k, v string)          { h.values[k] = v }
func (h *fakeHeader) Add(k, v string)          { h.values[k] = v }
func (h *fakeHeader) Keys() []string           { return nil }
func (h *fakeHeader) Values(k string) []string { return nil }

// fakeTransporter 是 transport.Transporter 的最小实现。auth.Server 中间件只
// 用到 Operation / RequestHeader; 其余返回零值即可。
type fakeTransporter struct {
	header transport.Header
}

func (f *fakeTransporter) Kind() transport.Kind            { return transport.KindGRPC }
func (f *fakeTransporter) Endpoint() string                { return "" }
func (f *fakeTransporter) Operation() string               { return "/api.user.v1.User/GetUser" }
func (f *fakeTransporter) RequestHeader() transport.Header { return f.header }
func (f *fakeTransporter) ReplyHeader() transport.Header   { return &fakeHeader{} }

// jwtFor 用 secret 给 uid 签 HS256 token，仅用于测试构造请求 ctx。
// secret 是签名密钥，uid 是用户 ID。
func jwtFor(t *testing.T, secret string, uid string) string {
	t.Helper()
	tok := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, jwtv5.MapClaims{
		"sub": uid,
		"jti": "test-jti",
		"typ": "access",
		"iss": "chatroom", // middleware.Server(secret, "", nil) 走默认 issuer "chatroom"
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// authedCtxWithToken 走一次 auth.Server 中间件，让它把 uid 写到 ctx。
// bearer 是待校验的 token 串，secret 是签名密钥。
// 中间件依赖 transport.FromServerContext，因此先用 fakeTransporter 包一层。
func authedCtxWithToken(t *testing.T, bearer, secret string) context.Context {
	t.Helper()
	hdr := &fakeHeader{values: map[string]string{"Authorization": "Bearer " + bearer}}
	tr := &fakeTransporter{header: hdr}
	ctx := transport.NewServerContext(context.Background(), tr)

	mw := middleware.Server(secret, "", nil) // nil store = 关闭黑名单检查
	type dummyReq struct{}
	var capturedCtx context.Context
	_, err := mw(func(ctx context.Context, _ any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	})(ctx, &dummyReq{})
	if err != nil {
		t.Fatalf("auth middleware rejected test ctx: %v", err)
	}
	if capturedCtx == nil {
		t.Fatal("auth middleware did not invoke next")
	}
	// 额外确认 uid 已写入; 否则 service.GetUser 也会走到 missing-uid 分支,
	// 报错信息会和真实失败原因不同。
	if _, ok := middleware.FromContext(capturedCtx); !ok {
		t.Fatal("auth middleware did not write uid into ctx")
	}
	return capturedCtx
}

const testSecret = "test-secret-32-bytes-long-xxxxxx"

// newService 构造 service + 它下面的 fake 依赖。
// service 直持 *biz.UserUsecase，所以替身是 repo / tokenStore / limiter /
// locker —— 全部是 biz 层声明的接口。
func newService(repo *fakeRepo, store *fakeTokenStore, lim *fakeLimiter, lock *fakeLocker) *UserService {
	auth := &conf.Auth{JwtSecret: testSecret, TokenIssuer: "chatroom"}
	uc := biz.NewUserUsecase(repo, auth, lim, lock, store, nil, biz.StorageOptions{}, nil)
	return NewUserService(uc, auth)
}

// newServiceWithRepo 是最常用的组合：一个空 repo + 一个可断言的 fake repo。
// repo 是 userRepo 替身。
func newServiceWithRepo(repo *fakeRepo) *UserService {
	return newService(repo, &fakeTokenStore{}, &fakeLimiter{}, &fakeLocker{})
}
func TestServiceRegister_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.Register(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceRegister_PassesFieldsToUsecase(t *testing.T) {
	repo := &fakeRepo{saveID: "uid-42"}
	s := newServiceWithRepo(repo)

	reply, err := s.Register(context.Background(), &pb.RegisterRequest{
		Username: "alice",
		Password: "secret123",
		Nickname: "Alice",
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if reply.GetId() != "uid-42" {
		t.Fatalf("reply.Id = %s, want uid-42", reply.GetId())
	}
	if !repo.saveCalls.called {
		t.Fatal("usecase.Register not called")
	}
	got := repo.saveCalls.user
	if got.Username != "alice" || got.Nickname != "Alice" {
		t.Fatalf("calls = %+v, want alice/Alice", got)
	}
	// 密码必须哈希后落库，明文不能进 repo。
	if got.PasswordHash == "secret123" || got.PasswordHash == "" {
		t.Fatalf("PasswordHash = %q, want a bcrypt hash", got.PasswordHash)
	}
}

func TestServiceRegister_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepo{saveErr: wantErr}
	s := newServiceWithRepo(repo)

	_, err := s.Register(context.Background(), &pb.RegisterRequest{Username: "alice", Password: "secret123"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("error = %v, want boom", err)
	}
}
func TestServiceLogin_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.Login(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceLogin_ReturnsTokenAndUID(t *testing.T) {
	repo := &fakeRepo{findByUsernameRet: &biz.User{
		ID:           "uid-7",
		Username:     "alice",
		PasswordHash: bcryptHash(t, "secret123"),
		Status:       biz.UserStatusNormal,
	}}
	s := newServiceWithRepo(repo)

	reply, err := s.Login(context.Background(), &pb.LoginRequest{Username: "alice", Password: "secret123"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if reply.GetAccessToken() == "" && reply.GetToken() == "" {
		t.Fatal("reply has no access token")
	}
	if reply.GetUserId() != "uid-7" {
		t.Fatalf("reply.UserId = %s, want uid-7", reply.GetUserId())
	}
}

func TestServiceLogin_PropagatesError(t *testing.T) {
	repo := &fakeRepo{findByUsernameErr: biz.ErrUserBadCredentials}
	s := newServiceWithRepo(repo)

	if _, err := s.Login(context.Background(), &pb.LoginRequest{Username: "alice", Password: "x"}); !kratoserrors.IsUnauthorized(err) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}
func TestServiceGetUser_MissingUID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUser(context.Background(), &pb.GetUserRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetUser_UsesUIDFromContext(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &fakeRepo{findByIDRet: &biz.User{
		ID:        "uid-99",
		Username:  "alice",
		Nickname:  "Alice",
		Avatar:    "https://x/a.png",
		CreatedAt: created,
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-99"), "test-secret")
	reply, err := s.GetUser(ctx, &pb.GetUserRequest{})
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	if repo.findByIDCalls.id != "uid-99" {
		t.Fatalf("usecase called with id = %s, want uid-99", repo.findByIDCalls.id)
	}
	if reply.GetId() != "uid-99" || reply.GetUsername() != "alice" || reply.GetNickname() != "Alice" {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.GetCreatedAt() != created.Unix() {
		t.Fatalf("reply.CreatedAt = %d, want %d", reply.GetCreatedAt(), created.Unix())
	}
}

func TestServiceGetUser_PropagatesError(t *testing.T) {
	repo := &fakeRepo{findByIDErr: biz.ErrUserNotFound}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")
	_, err := s.GetUser(ctx, &pb.GetUserRequest{})
	if !kratoserrors.IsNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
func TestServiceUpdateUser_MissingUID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.UpdateUser(context.Background(), &pb.UpdateUserRequest{Nickname: "n", Avatar: "a"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceUpdateUser_PassesUIDAndFields(t *testing.T) {
	created := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	repo := &fakeRepo{updateRet: &biz.User{
		ID:        "uid-5",
		Username:  "alice",
		Nickname:  "Alice2",
		Avatar:    "https://x/new.png",
		CreatedAt: created,
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-5"), "test-secret")
	reply, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{Nickname: "Alice2", Avatar: "https://x/new.png"})
	if err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	if repo.updateCalls.id != "uid-5" {
		t.Fatalf("usecase id = %s, want uid-5", repo.updateCalls.id)
	}
	if repo.updateCalls.nickname != "Alice2" || repo.updateCalls.avatar != "https://x/new.png" {
		t.Fatalf("calls = %+v", repo.updateCalls)
	}
	if reply.GetNickname() != "Alice2" || reply.GetAvatar() != "https://x/new.png" {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.GetCreatedAt() != created.Unix() {
		t.Fatalf("reply.CreatedAt = %d, want %d", reply.GetCreatedAt(), created.Unix())
	}
}

func TestServiceUpdateUser_PropagatesError(t *testing.T) {
	repo := &fakeRepo{updateErr: biz.ErrUserInvalidArgument}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")
	if _, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{Nickname: "n", Avatar: "a"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}
func TestConvertUserReply_Nil(t *testing.T) {
	if got := convertUserReply(nil); got != nil {
		t.Fatalf("convertUserReply(nil) = %+v, want nil", got)
	}
}

func TestConvertUserReply_FieldMapping(t *testing.T) {
	created := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	u := &biz.User{
		ID:           "uid-12",
		Username:     "alice",
		Nickname:     "Alice",
		Avatar:       "https://x/a.png",
		PasswordHash: "should-not-leak", // service 层不脱敏, 这里仅验证不传
		CreatedAt:    created,
	}
	got := convertUserReply(u)
	if got.GetId() != "uid-12" || got.GetUsername() != "alice" || got.GetNickname() != "Alice" || got.GetAvatar() != "https://x/a.png" {
		t.Fatalf("field mapping wrong: %+v", got)
	}
	if got.GetCreatedAt() != created.Unix() {
		t.Fatalf("CreatedAt = %d, want %d", got.GetCreatedAt(), created.Unix())
	}
}
func TestServiceChangePassword_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.ChangePassword(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceChangePassword_MissingUID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	_, err := s.ChangePassword(context.Background(), &pb.ChangePasswordRequest{
		OldPassword: "old", NewPassword: "newpwd1",
	})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceChangePassword_PassesFieldsAndTokens(t *testing.T) {
	repo := &fakeRepo{findByIDRet: &biz.User{
		ID:           "uid-7",
		Username:     "alice",
		PasswordHash: bcryptHash(t, "oldpwd"),
		Status:       biz.UserStatusNormal,
	}}
	store := &fakeTokenStore{}
	s := newService(repo, store, &fakeLimiter{}, &fakeLocker{})
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")

	_, err := s.ChangePassword(ctx, &pb.ChangePasswordRequest{
		OldPassword: "oldpwd",
		NewPassword: "newpwd1",
	})
	if err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if !repo.updatePasswordCalls.called {
		t.Fatal("usecase.ChangePassword not called")
	}
	if repo.updatePasswordCalls.uid != "uid-7" {
		t.Fatalf("uid = %s, want uid-7", repo.updatePasswordCalls.uid)
	}
	if repo.updatePasswordCalls.newHash == "newpwd1" || repo.updatePasswordCalls.newHash == "" {
		t.Fatalf("newHash = %q, want a bcrypt hash", repo.updatePasswordCalls.newHash)
	}
	// accessJTI/exp 来自 ctx，service 必须把它们透传
	if !store.blacklistCalls.called {
		t.Fatal("access token was not blacklisted")
	}
	if store.blacklistCalls.jti != "test-jti" {
		t.Fatalf("blacklist jti = %q, want test-jti", store.blacklistCalls.jti)
	}
	if store.blacklistCalls.ttl <= 0 {
		t.Fatal("blacklist ttl is zero")
	}
}

func TestServiceChangePassword_PropagatesError(t *testing.T) {
	repo := &fakeRepo{findByIDRet: &biz.User{
		ID:           "uid-1",
		Username:     "alice",
		PasswordHash: bcryptHash(t, "not-the-old-one"),
		Status:       biz.UserStatusNormal,
	}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")
	_, err := s.ChangePassword(ctx, &pb.ChangePasswordRequest{OldPassword: "old", NewPassword: "newpwd1"})
	if !kratoserrors.IsUnauthorized(err) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}
func TestServiceGetUserByID_MissingUID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUserByID(context.Background(), &pb.GetUserByIDRequest{Id: "uid-5"}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetUserByID_EmptyID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")
	if _, err := s.GetUserByID(ctx, &pb.GetUserByIDRequest{Id: ""}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetUserByID_StrangerClearsContact(t *testing.T) {
	repo := &fakeRepo{findByIDRet: &biz.User{
		ID:       "uid-5",
		Username: "alice",
		Nickname: "Alice",
		Avatar:   "https://x/a.png",
		Email:    "leaked@example.com",
		Phone:    "13800000000",
		// 可见 —— 陌生人能看基础资料，但联系方式要清空
		IsVisibleToStranger: true,
	}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-99"), "test-secret") // viewer=99

	reply, err := s.GetUserByID(ctx, &pb.GetUserByIDRequest{Id: "uid-5"})
	if err != nil {
		t.Fatalf("GetUserByID() error = %v", err)
	}
	if repo.findByIDCalls.id != "uid-5" {
		t.Fatalf("repo called with id = %s, want uid-5", repo.findByIDCalls.id)
	}
	// 陌生人场景：service 必须把 email/phone 清空
	if reply.Email != nil {
		t.Fatalf("reply.Email = %v, want nil (stranger)", reply.Email)
	}
	if reply.Phone != nil {
		t.Fatalf("reply.Phone = %v, want nil (stranger)", reply.Phone)
	}
	if reply.GetId() != "uid-5" || reply.GetNickname() != "Alice" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestServiceGetUserByID_SelfKeepsContact(t *testing.T) {
	repo := &fakeRepo{findByIDRet: &biz.User{
		ID: "uid-5", Username: "alice", Nickname: "Alice", Avatar: "x",
		Email: "alice@example.com", Phone: "13800000000",
	}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-5"), "test-secret") // viewer=5

	reply, err := s.GetUserByID(ctx, &pb.GetUserByIDRequest{Id: "uid-5"})
	if err != nil {
		t.Fatalf("GetUserByID() error = %v", err)
	}
	if reply.Email == nil || reply.GetEmail() != "alice@example.com" {
		t.Fatalf("reply.Email = %v, want alice@example.com", reply.Email)
	}
	if reply.Phone == nil || reply.GetPhone() != "13800000000" {
		t.Fatalf("reply.Phone = %v, want 13800000000", reply.Phone)
	}
}

func TestServiceGetUserByID_PropagatesNotFound(t *testing.T) {
	repo := &fakeRepo{findByIDErr: biz.ErrUserNotFound}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")
	_, err := s.GetUserByID(ctx, &pb.GetUserByIDRequest{Id: "uid-999"})
	if !kratoserrors.IsNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
func TestServiceGetUsersByIDs_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUsersByIDs(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetUsersByIDs_MissingUID(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUsersByIDs(context.Background(), &pb.GetUsersByIDsRequest{Ids: []string{"uid-1", "uid-2"}}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetUsersByIDs_EmptyIDs(t *testing.T) {
	repo := &fakeRepo{}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")

	reply, err := s.GetUsersByIDs(ctx, &pb.GetUsersByIDsRequest{Ids: nil})
	if err != nil {
		t.Fatalf("GetUsersByIDs: %v", err)
	}
	if len(reply.GetUsers()) != 0 {
		t.Fatalf("users = %d, want 0", len(reply.GetUsers()))
	}
}

func TestServiceGetUsersByIDs_PassesViewerAndIDs(t *testing.T) {
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &fakeRepo{findByIDsRet: []*biz.User{
		{ID: "uid-11", Nickname: "Bob", Avatar: "https://x/b", CreatedAt: t1, IsVisibleToStranger: true},
		{ID: "uid-22", Nickname: "Carol", Avatar: "https://x/c", CreatedAt: t1.Add(time.Hour), IsVisibleToStranger: true},
	}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")

	reply, err := s.GetUsersByIDs(ctx, &pb.GetUsersByIDsRequest{Ids: []string{"uid-11", "uid-22", "uid-33"}})
	if err != nil {
		t.Fatalf("GetUsersByIDs: %v", err)
	}
	if len(repo.findByIDsIDs) != 3 || repo.findByIDsIDs[2] != "uid-33" {
		t.Fatalf("ids = %v, want [11 22 33]", repo.findByIDsIDs)
	}
	if len(reply.GetUsers()) != 2 {
		t.Fatalf("len = %d, want 2 (id 33 由 biz 折叠)", len(reply.GetUsers()))
	}
	if reply.GetUsers()[0].GetNickname() != "Bob" ||
		reply.GetUsers()[1].GetNickname() != "Carol" {
		t.Fatalf("nicknames = %+v", reply.GetUsers())
	}
}

func TestServiceGetUsersByIDs_PropagatesError(t *testing.T) {
	repo := &fakeRepo{findByIDsErr: biz.ErrUserInvalidArgument}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-1"), "test-secret")
	_, err := s.GetUsersByIDs(ctx, &pb.GetUsersByIDsRequest{Ids: []string{"uid-1"}})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}
func TestServiceUpdateUser_FieldMask_NicknameOnly(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{ID: "uid-5", Nickname: "Alice2"}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-5"), "test-secret")

	mask := &fieldmaskpb.FieldMask{Paths: []string{"nickname"}}
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{
		Nickname:   "Alice2",
		Avatar:     "should-not-update",
		UpdateMask: mask,
	})
	if err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	if !repo.updateFieldsCalls.called {
		t.Fatal("UpdateUserFields not called")
	}
	if repo.updateFieldsCalls.fields.Nickname == nil || *repo.updateFieldsCalls.fields.Nickname != "Alice2" {
		t.Fatalf("fields.Nickname = %+v", repo.updateFieldsCalls.fields.Nickname)
	}
	if repo.updateFieldsCalls.fields.Avatar != nil {
		t.Fatalf("fields.Avatar should be nil, got %+v", repo.updateFieldsCalls.fields.Avatar)
	}
}

func TestServiceUpdateUser_FieldMask_UnknownFieldIgnored(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{ID: "uid-5"}}
	s := newServiceWithRepo(repo)
	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-5"), "test-secret")

	// mask 里的字段都不可改 —— service 拼出的 fields 全空，
	// biz 判定「没有可更新字段」并返回 invalid argument。
	mask := &fieldmaskpb.FieldMask{Paths: []string{"id", "username"}} // 都是不可改字段
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{UpdateMask: mask})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("UpdateUser() error = %v, want bad request", err)
	}
}

// 该端点走服务间内部凭证（middleware.InternalServer 中间件），不走用户 JWT，
// 所以 service 层不读 middleware.FromContext —— ctx 里没有 token 也应正常工作。

func TestServiceGetFriendProfiles_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetFriendProfiles(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// 无 JWT ctx 也能调通 —— 鉴权由中间件负责，service 不依赖 middleware.FromContext。
func TestServiceGetFriendProfiles_NoAuthContext(t *testing.T) {
	repo := &fakeRepo{findByIDsRet: []*biz.User{
		{ID: "uid-7", Username: "bob", Nickname: "Bob", Avatar: "b.png"},
	}}
	s := newServiceWithRepo(repo)

	reply, err := s.GetFriendProfiles(context.Background(), &pb.GetFriendProfilesRequest{Ids: []string{"uid-7"}})
	if err != nil {
		t.Fatalf("GetFriendProfiles: %v", err)
	}
	if len(reply.GetUsers()) != 1 {
		t.Fatalf("users = %d, want 1", len(reply.GetUsers()))
	}
	u := reply.GetUsers()[0]
	if u.GetId() != "uid-7" || u.GetUsername() != "bob" || u.GetNickname() != "Bob" || u.GetAvatar() != "b.png" {
		t.Fatalf("field mapping wrong: %+v", u)
	}
}

// ids 原样透传给 usecase（不做业务裁剪，裁剪在 biz 层）。
func TestServiceGetFriendProfiles_PassesIDs(t *testing.T) {
	repo := &fakeRepo{}
	s := newServiceWithRepo(repo)

	ids := []string{"uid-3", "uid-5", "uid-9"}
	if _, err := s.GetFriendProfiles(context.Background(), &pb.GetFriendProfilesRequest{Ids: ids}); err != nil {
		t.Fatalf("GetFriendProfiles: %v", err)
	}
	if len(repo.findByIDsIDs) != len(ids) {
		t.Fatalf("ids = %v, want %v", repo.findByIDsIDs, ids)
	}
	for i, id := range ids {
		if repo.findByIDsIDs[i] != id {
			t.Fatalf("ids = %v, want %v", repo.findByIDsIDs, ids)
		}
	}
}

func TestServiceGetFriendProfiles_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepo{findByIDsErr: wantErr}
	s := newServiceWithRepo(repo)

	if _, err := s.GetFriendProfiles(context.Background(), &pb.GetFriendProfilesRequest{Ids: []string{"uid-1"}}); err != wantErr {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

// 跟 GetFriendProfiles 一样走服务间内部凭证，不走用户 JWT，
// 所以 service 层不读 middleware.FromContext —— ctx 里没有 token 也应正常工作。

func TestServiceGetConversationMemberProfiles_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetConversationMemberProfiles(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

func TestServiceGetConversationMemberProfiles_NoAuthContext(t *testing.T) {
	repo := &fakeRepo{findByIDsRet: []*biz.User{
		{ID: "uid-7", Username: "bob", Nickname: "Bob", Avatar: "b.png"},
	}}
	s := newServiceWithRepo(repo)

	reply, err := s.GetConversationMemberProfiles(context.Background(), &pb.GetConversationMemberProfilesRequest{Ids: []string{"uid-7"}})
	if err != nil {
		t.Fatalf("GetConversationMemberProfiles: %v", err)
	}
	if len(reply.GetUsers()) != 1 {
		t.Fatalf("users = %d, want 1", len(reply.GetUsers()))
	}
	u := reply.GetUsers()[0]
	if u.GetId() != "uid-7" || u.GetUsername() != "bob" || u.GetNickname() != "Bob" || u.GetAvatar() != "b.png" {
		t.Fatalf("field mapping wrong: %+v", u)
	}
}

func TestServiceGetConversationMemberProfiles_PassesIDs(t *testing.T) {
	repo := &fakeRepo{}
	s := newServiceWithRepo(repo)

	ids := []string{"uid-3", "uid-5", "uid-9"}
	if _, err := s.GetConversationMemberProfiles(context.Background(), &pb.GetConversationMemberProfilesRequest{Ids: ids}); err != nil {
		t.Fatalf("GetConversationMemberProfiles: %v", err)
	}
	if len(repo.findByIDsIDs) != len(ids) {
		t.Fatalf("ids = %v, want %v", repo.findByIDsIDs, ids)
	}
	for i, id := range ids {
		if repo.findByIDsIDs[i] != id {
			t.Fatalf("ids = %v, want %v", repo.findByIDsIDs, ids)
		}
	}
}

func TestServiceGetConversationMemberProfiles_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepo{findByIDsErr: wantErr}
	s := newServiceWithRepo(repo)

	if _, err := s.GetConversationMemberProfiles(context.Background(), &pb.GetConversationMemberProfilesRequest{Ids: []string{"uid-1"}}); err != wantErr {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

// TestServiceGetUserByUsername_ReturnsUser 按用户名查到人，返的资料带 uid。
func TestServiceGetUserByUsername_ReturnsUser(t *testing.T) {
	repo := &fakeRepo{findByUsernameRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		Nickname:  "Alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	reply, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if reply.GetId() != "uid-7" {
		t.Errorf("Id = %q, want uid-7", reply.GetId())
	}
	if reply.GetUsername() != "alice" {
		t.Errorf("Username = %q, want alice", reply.GetUsername())
	}
}

// TestServiceGetUserByUsername_NilRequest 空请求返参数错误。
func TestServiceGetUserByUsername_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUserByUsername(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceGetUserByUsername_EmptyUsername 空用户名返参数错误。
func TestServiceGetUserByUsername_EmptyUsername(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceGetUserByUsername_PropagatesError 查无此人时把底层错误带出去，
// service 层不吞 —— 前端靠 404 区分「没有这个用户名」和「服务挂了」。
func TestServiceGetUserByUsername_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepo{findByUsernameErr: wantErr}
	s := newServiceWithRepo(repo)

	if _, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{Username: "alice"}); err != wantErr {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

// TestServiceGetUserByPhone_ReturnsUser 按手机号查到人。
func TestServiceGetUserByPhone_ReturnsUser(t *testing.T) {
	repo := &fakeRepo{findByPhoneRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		Nickname:  "Alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	reply, err := s.GetUserByPhone(context.Background(), &pb.GetUserByPhoneRequest{Phone: "13812345678"})
	if err != nil {
		t.Fatalf("GetUserByPhone: %v", err)
	}
	if reply.GetId() != "uid-7" {
		t.Errorf("Id = %q, want uid-7", reply.GetId())
	}
}

// TestServiceGetUserByPhone_NilRequest 空请求返参数错误。
func TestServiceGetUserByPhone_NilRequest(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUserByPhone(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceGetUserByPhone_EmptyPhone 空手机号返参数错误。
func TestServiceGetUserByPhone_EmptyPhone(t *testing.T) {
	s := newServiceWithRepo(&fakeRepo{})
	if _, err := s.GetUserByPhone(context.Background(), &pb.GetUserByPhoneRequest{}); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceUpdateUser_PassesPhoneToUsecase mask 里带 phone 时要把手机号传下去。
func TestServiceUpdateUser_PassesPhoneToUsecase(t *testing.T) {
	repo := &fakeRepo{updateFieldsRet: &biz.User{
		ID:        "uid-7",
		Username:  "alice",
		CreatedAt: time.Now(),
	}}
	s := newServiceWithRepo(repo)

	ctx := authedCtxWithToken(t, jwtFor(t, "test-secret", "uid-7"), "test-secret")
	_, err := s.UpdateUser(ctx, &pb.UpdateUserRequest{
		Phone:      "13812345678",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"phone"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !repo.updateFieldsCalls.called {
		t.Fatal("usecase.UpdateUserFields not called")
	}
	if repo.updateFieldsCalls.fields.Phone == nil {
		t.Fatal("Phone 没传给 usecase")
	}
	if *repo.updateFieldsCalls.fields.Phone != "13812345678" {
		t.Errorf("Phone = %q, want 13812345678", *repo.updateFieldsCalls.fields.Phone)
	}
}
