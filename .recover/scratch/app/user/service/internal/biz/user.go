package biz

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	v1 "chatroom/user-service/api/user/v1"
	"chatroom/user-service/internal/conf"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound           = kratoserrors.NotFound(v1.ErrorReason_USER_NOT_FOUND.String(), "user not found")
	ErrUserInvalidArgument    = kratoserrors.BadRequest(v1.ErrorReason_USER_INVALID_ARGUMENT.String(), "invalid user argument")
	ErrUserAlreadyExists      = kratoserrors.Conflict(v1.ErrorReason_USER_ALREADY_EXISTS.String(), "user already exists")
	ErrUserBadCredentials     = kratoserrors.Unauthorized(v1.ErrorReason_USER_BAD_CREDENTIALS.String(), "invalid username or password")
	ErrUserLoginLocked        = kratoserrors.TooManyRequests(v1.ErrorReason_USER_LOGIN_LOCKED.String(), "too many failed login attempts, try again later")
	ErrRefreshInvalid         = kratoserrors.Unauthorized(v1.ErrorReason_USER_REFRESH_INVALID.String(), "refresh token invalid or expired")
	ErrTokenBlacklisted       = kratoserrors.Unauthorized(v1.ErrorReason_USER_TOKEN_BLACKLISTED.String(), "token has been revoked")
	ErrUserInvalidOldPassword = kratoserrors.Unauthorized(v1.ErrorReason_USER_INVALID_OLD_PASSWORD.String(), "invalid old password")
	// ErrUserBanned 账号被禁用（status=0）。登录 / 刷新 token 时返回
	ErrUserBanned = kratoserrors.Forbidden(v1.ErrorReason_USER_BANNED.String(), "account is disabled")
)

// 输入长度上限。schema 里没给 username / nickname / avatar 定 MaxLen，
// 所以得在 biz 层挡 —— 否则超长值会一路写进库（MySQL 那边也可能截断）。
const (
	MinPasswordLength = 6
	MaxPasswordLength = 72 // bcrypt 的硬上限，超过会被 bcrypt 拒绝
	MaxUsernameLength = 36
	MaxNicknameLength = 64
	MaxAvatarLength   = 512
)

// validatePassword 校验密码长度。
//
// 密码**不做 trim** —— 它是不透明的字节串，注册时怎么存的就得怎么比对。
// 任何一处 trim 都会让「注册时带空格」的老用户再也改不了密码
// （存的哈希是 " pass "，比对时却拿 "pass" 去比）。
//
// 唯一被拒的是「全是空白」—— 那种密码等价于没设。
func validatePassword(password string) error {
	if password == "" {
		return ErrUserInvalidArgument
	}
	if strings.TrimSpace(password) == "" {
		return ErrUserInvalidArgument
	}
	if len(password) < MinPasswordLength {
		return ErrUserInvalidArgument
	}
	// bcrypt 只吃前 72 字节，超长会被 bcrypt.ErrPasswordTooLong 拒掉。
	// 提前挡掉，给用户一个明确的 400 而不是 500。
	if len(password) > MaxPasswordLength {
		return ErrUserInvalidArgument
	}
	return nil
}

// validateUsername 校验用户名长度。调用方负责 trim + 小写化。
func validateUsername(username string) error {
	if username == "" {
		return ErrUserInvalidArgument
	}
	if len(username) > MaxUsernameLength {
		return ErrUserInvalidArgument
	}
	return nil
}

// validateProfile 校验昵称 / 头像长度。
func validateProfile(nickname, avatar string) error {
	if len(nickname) > MaxNicknameLength {
		return ErrUserInvalidArgument
	}
	if len(avatar) > MaxAvatarLength {
		return ErrUserInvalidArgument
	}
	return nil
}

// derefOrEmpty 取可空指针的值，nil 返空串。
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// hashPassword 算 bcrypt 哈希，把 bcrypt 的错误翻译成 typed error。
//
// 不翻译的话，超长密码（>72 字节）会返回 bcrypt.ErrPasswordTooLong ——
// service 层不认得它，会当成未知错误报 500。输入校验已经挡住了大部分，
// 这里兜底。
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return "", ErrUserInvalidArgument
		}
		return "", err
	}
	return string(hash), nil
}

// token 类型常量。access/refresh 共用 sign，通过 typ claim 区分用途。
const (
	tokenTypeAccess  = "access"
	tokenTypeRefresh = "refresh"
	defaultIssuer    = "chatroom"
)

// User 用户领域模型。Email/Phone 仅自己可见；IsVisibleToStranger=false 时 GetUserByID 折叠 NOT_FOUND。
type User struct {
	ID                  string
	Username            string
	Nickname            string
	Avatar              string
	Email               string
	Phone               string
	PasswordHash        string
	IsVisibleToStranger bool
	Status              UserStatus
	CreatedAt           time.Time
}

// UserStatus 账号状态。1 正常 / 0 禁用。
type UserStatus uint8

const (
	UserStatusNormal UserStatus = 1 // 正常账号，可登录
	UserStatusBanned UserStatus = 0 // 禁用账号，登录与刷新 token 均被拦截
)

// IsBanned 账号被禁用。
func (u *User) IsBanned() bool {
	return u != nil && u.Status == UserStatusBanned
}

// UserRepo 用户仓储接口。
type UserRepo interface {
	Save(ctx context.Context, u *User) (string, error)
	FindByID(ctx context.Context, id string) (*User, error)
	FindByUsername(ctx context.Context, username string) (*User, error)
	// 批量查；找不到的 id 在结果中不出现，不返错。返回顺序与 ids 无关
	FindByIDs(ctx context.Context, ids []string) ([]*User, error)
	Update(ctx context.Context, id string, nickname, avatar string) (*User, error)
	// 仅传非 nil 指针的字段被写入。Email / Phone 不在此，靠专门端点改
	UpdateFields(ctx context.Context, id string, fields UserUpdateFields) (*User, error)
	UpdatePassword(ctx context.Context, id string, newHash string) error
	// 改账号状态（禁用 / 解禁）。调用方是管理侧，不开放给普通用户
	UpdateStatus(ctx context.Context, id string, status UserStatus) error
}

// UserUpdateFields UpdateFields 的可空字段集合；service 从 proto FieldMask 拼装。
type UserUpdateFields struct {
	Nickname *string
	Avatar   *string
}

// LoginLimiter 登录频控抽象。
//
// clientIP 是调用方 IP —— 限频要按「用户名 + IP」两个维度：
// 只按用户名锁的话，任何人知道别人的用户名就能把他锁在门外
// （5 次错密码就锁 15 分钟），这是现成的 DoS。
type LoginLimiter interface {
	// 在 Login 入口调用，命中锁定返回 ErrUserLoginLocked
	Allow(ctx context.Context, username, clientIP string) error
	// 凭据校验通过后清掉失败计数
	OnSuccess(ctx context.Context, username, clientIP string)
	// 凭据校验失败后累加失败计数。参数非法的路径不调用，避免被恶意空请求刷到锁定
	OnFailure(ctx context.Context, username, clientIP string)
}

// Release Acquire 的释放闭包，必须幂等；用闭包强制 caller defer 调用。
type Release func()

// Locker 按 key 的非重入互斥。Acquire 返 (release, nil) / (nil, ErrLockBusy) / (noopRelease, nil)。
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Release, error)
}

// ErrLockBusy 锁被他人持有。biz 折叠为 ErrUserAlreadyExists (Register) / ErrUserLoginLocked (Login)，不向客户端暴露锁的存在。
var ErrLockBusy = kratoserrors.Conflict(
	v1.ErrorReason_USER_ALREADY_EXISTS.String(),
	"request is being processed, retry later",
)

// TokenStore token 状态。Redis 不可用时降级：Blacklist/SaveRefresh 失败仅 warn，ConsumeRefresh 失败则拒绝 refresh。
type TokenStore interface {
	Blacklist(ctx context.Context, jti string, ttl time.Duration) error
	IsBlacklisted(ctx context.Context, jti string) (bool, error)
	SaveRefresh(ctx context.Context, jti string, uid string, userAgent string, ttl time.Duration) error
	// 原子 GET uid + DEL。找不到 / 已过期 / 已被轮换均返 ErrRefreshInvalid
	ConsumeRefresh(ctx context.Context, jti string) (string, error)
	RevokeRefresh(ctx context.Context, jti string) error
}

// LoginResult 登录成功的 token 对；access/refresh 合并让 service 只做一次 DTO 转换。
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // access 剩余秒数
}

// UserCache 用户读缓存抽象。Get 返回的 *User 必须不含 PasswordHash（data 反序列化时清空）。
type UserCache interface {
	// 命中返 (user, ver, nil)；未命中 (nil, 0, nil)。Redis 错返 (nil, 0, err)，caller 走 DB 路径
	Get(ctx context.Context, id string) (*User, int64, error)
	Set(ctx context.Context, u *User, ver int64) error
	// 自增 verKey，让旧缓存值被识别为 stale。返回新 ver
	Invalidate(ctx context.Context, id string) (int64, error)
	Version(ctx context.Context, id string) (int64, error)
}

// UserUsecase 用户用例。
type UserUsecase struct {
	repo    UserRepo
	auth    *conf.Auth
	lim     LoginLimiter
	locker  Locker
	store   TokenStore
	cache   UserCache
	tokenFn func(uid string) (string, error) // auth 配置有效时才赋值，避免冷启动签发空密钥 token
	logger  *slog.Logger
}

// NewUserUsecase auth 用于签发 JWT（nil 时关闭登录）。lim/locker/store/cache 传 nil 关闭对应功能。
func NewUserUsecase(repo UserRepo, auth *conf.Auth, lim LoginLimiter, locker Locker, store TokenStore, cache UserCache, logger *slog.Logger) *UserUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	uc := &UserUsecase{repo: repo, auth: auth, lim: lim, locker: locker, store: store, cache: cache, logger: logger}
	if auth != nil && auth.GetJwtSecret() != "" {
		uc.tokenFn = uc.signToken
	}
	return uc
}

// Register 注册：锁 + FindByUsername 快速路径 + bcrypt + Save；撞 username 唯一索引兜底并发竞态。
// username trim 后小写化，nickname trim 空格。
func (uc *UserUsecase) Register(ctx context.Context, username, password, nickname string) (string, error) {
	username = strings.TrimSpace(username)
	nickname = strings.TrimSpace(nickname)
	if err := validateUsername(username); err != nil {
		return "", err
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	if err := validateProfile(nickname, ""); err != nil {
		return "", err
	}
	username = strings.ToLower(username)

	rel, err := uc.acquireRegisterLock(ctx, username)
	if err != nil {
		if errors.Is(err, ErrLockBusy) {
			return "", ErrUserAlreadyExists
		}
		return "", err
	}
	defer rel()

	if existing, err := uc.repo.FindByUsername(ctx, username); err == nil && existing != nil {
		return "", ErrUserAlreadyExists
	} else if err != nil && !errors.Is(err, ErrUserNotFound) {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	u := &User{Username: username, Nickname: nickname, PasswordHash: hash}
	return uc.repo.Save(ctx, u)
}

// acquireRegisterLock 取注册锁。TTL 5s 覆盖 bcrypt + DB Insert 最坏情况。
func (uc *UserUsecase) acquireRegisterLock(ctx context.Context, username string) (Release, error) {
	if uc.locker == nil {
		return func() {}, nil
	}
	return uc.locker.Acquire(ctx, "register:lock:"+username, 5*time.Second)
}

// Login 校验凭据，签发 access+refresh 并落 TokenStore。限频在前减少无效锁竞争。
//
// clientIP 是调用方 IP，参与登录限频 —— 只按用户名锁的话，
// 知道用户名的人就能把账号锁在门外。
func (uc *UserUsecase) Login(ctx context.Context, username, password, clientIP string) (*LoginResult, string, error) {
	if uc.tokenFn == nil {
		return nil, "", ErrUserBadCredentials
	}
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return nil, "", err
	}
	// 密码不 trim，跟注册时保持一致（见 validatePassword 的注释）。
	if password == "" {
		return nil, "", ErrUserInvalidArgument
	}
	username = strings.ToLower(username)

	if uc.lim != nil {
		if err := uc.lim.Allow(ctx, username, clientIP); err != nil {
			return nil, "", err
		}
	}

	rel, err := uc.acquireLoginLock(ctx, username)
	if err != nil {
		if errors.Is(err, ErrLockBusy) {
			return nil, "", ErrUserLoginLocked
		}
		return nil, "", err
	}
	defer rel()

	u, err := uc.repo.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			uc.notifyFailure(ctx, username, clientIP)
			return nil, "", ErrUserBadCredentials
		}
		return nil, "", err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		uc.notifyFailure(ctx, username, clientIP)
		return nil, "", ErrUserBadCredentials
	}
	// 禁用校验放在密码比对之后：否则错误密码会撞出 USER_BANNED，
	// 等于对外暴露"这个账号存在但被禁"
	if u.IsBanned() {
		return nil, "", ErrUserBanned
	}

	accessToken, _, accessExp, err := uc.signAccess(u.ID)
	if err != nil {
		return nil, "", err
	}
	refreshToken, refreshJTI, refreshExp, err := uc.signRefresh(u.ID)
	if err != nil {
		return nil, "", err
	}

	if uc.store != nil {
		ua := userAgentFromContext(ctx)
		if err := uc.store.SaveRefresh(ctx, refreshJTI, u.ID, ua, time.Until(refreshExp)); err != nil {
			uc.logger.WarnContext(ctx, "保存 refresh 失败", "err", err)
		}
	}

	uc.notifySuccess(ctx, username, clientIP)

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(time.Until(accessExp).Seconds()),
	}, u.ID, nil
}

// acquireLoginLock 取登录锁。TTL 2s 覆盖"读 user + bcrypt + 写 token"全链路。
func (uc *UserUsecase) acquireLoginLock(ctx context.Context, username string) (Release, error) {
	if uc.locker == nil {
		return func() {}, nil
	}
	return uc.locker.Acquire(ctx, "login:lock:"+username, 2*time.Second)
}

// notifyFailure 凭据校验失败时调；空 username（参数非法路径）不调，防止空刷。
func (uc *UserUsecase) notifyFailure(ctx context.Context, username, clientIP string) {
	if uc.lim != nil {
		uc.lim.OnFailure(ctx, username, clientIP)
	}
}

// notifySuccess 凭据校验通过后调；清掉 LoginLimiter 失败计数。
func (uc *UserUsecase) notifySuccess(ctx context.Context, username, clientIP string) {
	if uc.lim != nil {
		uc.lim.OnSuccess(ctx, username, clientIP)
	}
}

// Refresh refresh 换新一对：解析 → ConsumeRefresh 删旧 → 签发新对 → SaveRefresh。
func (uc *UserUsecase) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	if uc.tokenFn == nil || uc.store == nil {
		return nil, ErrRefreshInvalid
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, ErrUserInvalidArgument
	}
	refreshJTI, uid, err := uc.parseRefresh(refreshToken)
	if err != nil {
		return nil, err
	}
	// 禁用校验放在 ConsumeRefresh 之前：被禁用户的 refresh 不被消费，
	// 否则一旦解禁还能继续用
	u, err := uc.repo.FindByID(ctx, uid)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrRefreshInvalid
		}
		return nil, err
	}
	if u.IsBanned() {
		return nil, ErrUserBanned
	}
	if _, err := uc.store.ConsumeRefresh(ctx, refreshJTI); err != nil {
		return nil, err
	}

	accessToken, _, accessExp, err := uc.signAccess(uid)
	if err != nil {
		return nil, err
	}
	newRefreshToken, newRefreshJTI, newRefreshExp, err := uc.signRefresh(uid)
	if err != nil {
		return nil, err
	}

	ua := userAgentFromContext(ctx)
	if err := uc.store.SaveRefresh(ctx, newRefreshJTI, uid, ua, time.Until(newRefreshExp)); err != nil {
		uc.logger.WarnContext(ctx, "保存新 refresh 失败", "err", err)
	}

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		ExpiresIn:    int64(time.Until(accessExp).Seconds()),
	}, nil
}

// Logout access 进黑名单 + refresh 删除。均 warn 不返错。
// accessJTI 由 middleware 从 ctx 注入；refreshJTI 解析失败留空。
func (uc *UserUsecase) Logout(ctx context.Context, accessJTI, refreshJTI string, accessExp time.Time) error {
	if uc.store == nil {
		return nil
	}
	if accessJTI != "" {
		ttl := time.Until(accessExp)
		if ttl < time.Second {
			ttl = time.Second
		}
		if err := uc.store.Blacklist(ctx, accessJTI, ttl); err != nil {
			uc.logger.WarnContext(ctx, "拉黑 access 失败", "err", err)
		}
	}
	if refreshJTI != "" {
		if err := uc.store.RevokeRefresh(ctx, refreshJTI); err != nil {
			uc.logger.WarnContext(ctx, "撤销 refresh 失败", "err", err)
		}
	}
	return nil
}

// parseRefresh 解析校验 refresh token；失败一律折叠 ErrRefreshInvalid，不区分原因避免暴露服务端行为。
func (uc *UserUsecase) parseRefresh(raw string) (jti string, uid string, err error) {
	uid, jti, _, err = uc.verify(raw, tokenTypeRefresh)
	if err != nil {
		return "", "", ErrRefreshInvalid
	}
	return jti, uid, nil
}

// GetUser cache-aside：先 cache，ver 校验通过直返；未命中/ver 不匹配 → DB → 回写。
func (uc *UserUsecase) GetUser(ctx context.Context, id string) (*User, error) {
	if id == "" {
		return nil, ErrUserInvalidArgument
	}

	// 版本号要在**读 DB 之前**取。
	//
	// 反过来（先读 DB 再取版本）会有个窗口：读 DB 之后、读版本之前，
	// 有人改了资料 —— 版本号 +1，然后我们把**旧的 DB 数据**配上**新的版本号**
	// 写回缓存。这份陈旧数据从此看起来是新鲜的，永远不会被失效。
	//
	// 先取版本就不一样了：改动会把版本推高，我们写回的是旧版本，
	// 下次读缓存时版本对不上 → 重新读 DB。
	var ver int64
	if uc.cache != nil {
		cached, cachedVer, err := uc.cache.Get(ctx, id)
		if err != nil {
			uc.logger.WarnContext(ctx, "用户缓存读取降级", "err", err)
		} else if cached != nil {
			curVer, verr := uc.cache.Version(ctx, id)
			if verr != nil {
				uc.logger.WarnContext(ctx, "用户缓存版本查询降级", "err", verr)
			} else if cachedVer == curVer {
				return cached, nil
			}
		}
		// 取回写要用的版本号（读 DB 之前）。
		ver, _ = uc.cache.Version(ctx, id)
	}

	u, err := uc.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""

	if uc.cache != nil {
		if err := uc.cache.Set(ctx, u, ver); err != nil {
			uc.logger.WarnContext(ctx, "用户缓存回写降级", "err", err)
		}
	}
	return u, nil
}

// UpdateUser 旧全量路径：nickname/avatar 都必非空；partial update 走 UpdateUserFields。
func (uc *UserUsecase) UpdateUser(ctx context.Context, id string, nickname, avatar string) (*User, error) {
	if id == "" {
		return nil, ErrUserInvalidArgument
	}
	nickname = strings.TrimSpace(nickname)
	avatar = strings.TrimSpace(avatar)
	if nickname == "" || avatar == "" {
		return nil, ErrUserInvalidArgument
	}
	if err := validateProfile(nickname, avatar); err != nil {
		return nil, err
	}
	u, err := uc.repo.Update(ctx, id, nickname, avatar)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""

	if uc.cache != nil {
		if _, err := uc.cache.Invalidate(ctx, id); err != nil {
			uc.logger.WarnContext(ctx, "用户缓存失效降级", "err", err)
		}
	}
	return u, nil
}

// UpdateUserFields 字段粒度更新；至少一个字段被设置；非 nil 字段不能为空字符串。
func (uc *UserUsecase) UpdateUserFields(ctx context.Context, id string, fields UserUpdateFields) (*User, error) {
	if id == "" {
		return nil, ErrUserInvalidArgument
	}
	if fields.Nickname == nil && fields.Avatar == nil {
		return nil, ErrUserInvalidArgument
	}
	if fields.Nickname != nil {
		v := strings.TrimSpace(*fields.Nickname)
		if v == "" {
			return nil, ErrUserInvalidArgument
		}
		fields.Nickname = &v
	}
	if fields.Avatar != nil {
		v := strings.TrimSpace(*fields.Avatar)
		if v == "" {
			return nil, ErrUserInvalidArgument
		}
		fields.Avatar = &v
	}
	if err := validateProfile(derefOrEmpty(fields.Nickname), derefOrEmpty(fields.Avatar)); err != nil {
		return nil, err
	}

	u, err := uc.repo.UpdateFields(ctx, id, fields)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""

	if uc.cache != nil {
		if _, err := uc.cache.Invalidate(ctx, id); err != nil {
			uc.logger.WarnContext(ctx, "用户缓存失效降级", "err", err)
		}
	}
	return u, nil
}

// GetUserByID viewer==target 走完整 User；他人 + 不可见 折叠 NOT_FOUND；他人 + 可见 清空 Email/Phone。
func (uc *UserUsecase) GetUserByID(ctx context.Context, viewerID, targetID string) (*User, error) {
	if viewerID == "" || targetID == "" {
		return nil, ErrUserInvalidArgument
	}

	u, err := uc.repo.FindByID(ctx, targetID)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""

	if viewerID != targetID && !u.IsVisibleToStranger {
		return nil, ErrUserNotFound
	}

	if viewerID != targetID {
		u.Email = ""
		u.Phone = ""
	}
	return u, nil
}

// GetUsersByIDs 批量按 ID 查公开资料，避免 N+1。上限 200；不存在/不可见静默剔除。
func (uc *UserUsecase) GetUsersByIDs(ctx context.Context, viewerID string, ids []string) ([]*User, error) {
	if viewerID == "" {
		return nil, ErrUserInvalidArgument
	}
	if len(ids) == 0 {
		return []*User{}, nil
	}
	if len(ids) > maxBatchIDs {
		ids = ids[:maxBatchIDs]
	}

	loaded, err := uc.loadUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]*User, 0, len(loaded))
	for _, id := range ids {
		u, ok := loaded[id]
		if !ok || u == nil {
			continue
		}
		if viewerID != u.ID && !u.IsVisibleToStranger {
			continue
		}
		if viewerID != u.ID {
			u.Email = ""
			u.Phone = ""
		}
		// 复制避免 caller 改 u 时污染 cache
		c := *u
		out = append(out, &c)
	}
	return out, nil
}

// GetFriendProfiles 批量拉「好友可见」的公开资料，供 friend 服务补全好友列表。
// 与 GetUsersByIDs 的区别：跳过 is_visible_to_stranger 判定。仅限服务间调用。
func (uc *UserUsecase) GetFriendProfiles(ctx context.Context, ids []string) ([]*User, error) {
	if len(ids) == 0 {
		return []*User{}, nil
	}
	if len(ids) > maxBatchIDs {
		ids = ids[:maxBatchIDs]
	}

	loaded, err := uc.loadUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]*User, 0, len(loaded))
	for _, id := range ids {
		u, ok := loaded[id]
		if !ok || u == nil {
			continue
		}
		// 好友资料只暴露公开字段，联系方式一律清空
		u.Email = ""
		u.Phone = ""
		u.PasswordHash = ""
		c := *u
		out = append(out, &c)
	}
	return out, nil
}

// GetConversationMemberProfiles 批量拉「会话成员可见」的公开资料，供 conversation 服务补全会话成员列表。
// 会话成员未必是好友，但同在一个会话里就互相可见，故同样跳过可见性判定。
func (uc *UserUsecase) GetConversationMemberProfiles(ctx context.Context, ids []string) ([]*User, error) {
	return uc.GetFriendProfiles(ctx, ids)
}

// loadUsersByIDs cache-aside 批量按 ID 读用户，忽略可见性判定。
// 返回 map[id]*User；不存在的 id 不在结果中。
func (uc *UserUsecase) loadUsersByIDs(ctx context.Context, ids []string) (map[string]*User, error) {
	type hit struct {
		u         *User
		fromCache bool
	}
	hits := make(map[string]hit, len(ids))
	missing := make([]string, 0, len(ids))

	if uc.cache != nil {
		for _, id := range ids {
			cached, ver, err := uc.cache.Get(ctx, id)
			if err != nil {
				uc.logger.WarnContext(ctx, "用户缓存读取降级", "err", err)
				missing = append(missing, id)
				continue
			}
			if cached == nil {
				missing = append(missing, id)
				continue
			}
			curVer, verr := uc.cache.Version(ctx, id)
			if verr != nil {
				uc.logger.WarnContext(ctx, "用户缓存版本查询降级", "err", verr)
				missing = append(missing, id)
				continue
			}
			if ver != curVer {
				missing = append(missing, id)
				continue
			}
			hits[id] = hit{u: cached, fromCache: true}
		}
	} else {
		missing = append(missing, ids...)
	}

	if len(missing) > 0 {
		pos, err := uc.repo.FindByIDs(ctx, missing)
		if err != nil {
			return nil, err
		}
		loaded := make(map[string]*User, len(pos))
		for _, u := range pos {
			u.PasswordHash = ""
			loaded[u.ID] = u
			if uc.cache != nil {
				ver, _ := uc.cache.Version(ctx, u.ID)
				if err := uc.cache.Set(ctx, u, ver); err != nil {
					uc.logger.WarnContext(ctx, "用户缓存写入降级", "err", err)
				}
			}
		}
		for _, id := range missing {
			if u, ok := loaded[id]; ok {
				hits[id] = hit{u: u, fromCache: false}
			}
		}
	}

	out := make(map[string]*User, len(hits))
	for id, h := range hits {
		if h.u != nil {
			out[id] = h.u
		}
	}
	return out, nil
}

// maxBatchIDs 单次 GetUsersByIDs 上限，与 friend service ListFriends page_size (200) 对齐。
const maxBatchIDs = 200

// ChangePassword 改密。旧错/不存在折叠 ErrUserBadCredentials；改完强制登出当前会话。
func (uc *UserUsecase) ChangePassword(ctx context.Context, uid string, oldPassword, newPassword, accessJTI, refreshJTI string, accessExp time.Time) error {
	if uid == "" {
		return ErrUserInvalidArgument
	}
	// 密码不 trim —— 它是不透明的字节串，注册时怎么存的就得怎么比对。
	// 这里一旦 trim，注册时带空格的老用户就再也改不了密码
	//（存的哈希是 " pass "，这里却拿 "pass" 去比）。
	if oldPassword == "" {
		return ErrUserInvalidArgument
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if oldPassword == newPassword {
		return ErrUserInvalidArgument
	}

	u, err := uc.repo.FindByID(ctx, uid)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			uc.notifyFailure(ctx, "", "")
			return ErrUserBadCredentials
		}
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPassword)); err != nil {
		uc.notifyFailure(ctx, u.Username, "")
		return ErrUserBadCredentials
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := uc.repo.UpdatePassword(ctx, uid, hash); err != nil {
		return err
	}

	// 强制登出：复用 Logout 路径，任意字段为空时跳过
	if accessJTI != "" || refreshJTI != "" {
		_ = uc.Logout(ctx, accessJTI, refreshJTI, accessExp)
	}

	uc.notifySuccess(ctx, u.Username, "")
	return nil
}

// signAccess 签发 access token。默认 15m，access_ttl 未设置回退 token_ttl。
func (uc *UserUsecase) signAccess(uid string) (string, string, time.Time, error) {
	ttl, issuer := uc.tokenConfig()
	if d := uc.auth.GetAccessTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	return uc.sign(uid, tokenTypeAccess, ttl, issuer)
}

// signRefresh 签发 refresh token。默认 7d。
func (uc *UserUsecase) signRefresh(uid string) (string, string, time.Time, error) {
	ttl, issuer := uc.tokenConfig()
	ttl = 7 * 24 * time.Hour
	if d := uc.auth.GetRefreshTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	return uc.sign(uid, tokenTypeRefresh, ttl, issuer)
}

// tokenConfig 抽出 token_ttl fallback + issuer 默认值的统一逻辑。
func (uc *UserUsecase) tokenConfig() (time.Duration, string) {
	ttl := time.Hour
	if d := uc.auth.GetTokenTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	issuer := defaultIssuer
	if uc.auth.GetTokenIssuer() != "" {
		issuer = uc.auth.GetTokenIssuer()
	}
	return ttl, issuer
}

// sign access/refresh 共用签发，返回 token/jti/exp。
func (uc *UserUsecase) sign(uid string, typ string, ttl time.Duration, issuer string) (string, string, time.Time, error) {
	if uc.tokenFn == nil {
		return "", "", time.Time{}, ErrUserBadCredentials
	}
	jti := newJTI()
	now := time.Now()
	exp := now.Add(ttl)
	claims := jwt.MapClaims{
		"sub": uid, "jti": jti, "typ": typ, "iss": issuer,
		"iat": now.Unix(), "exp": exp.Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(uc.auth.GetJwtSecret()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	return tok, jti, exp, nil
}

// signToken 保留为 nil-safe（NewUserUsecase 的 tokenFn 赋值处使用）。
//
//nolint:unused
func (uc *UserUsecase) signToken(uid string) (string, error) {
	tok, _, _, err := uc.signAccess(uid)
	return tok, err
}

// verify middleware 与 biz 共用的 token 解析校验 iss/typ/exp；失败原因不暴露给 caller。
func (uc *UserUsecase) verify(raw, expectedType string) (string, string, time.Time, error) {
	_, issuer := uc.tokenConfig()
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return []byte(uc.auth.GetJwtSecret()), nil
	})
	if err != nil || !tok.Valid {
		return "", "", time.Time{}, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}

	if iss, _ := claims["iss"].(string); iss != issuer {
		return "", "", time.Time{}, jwt.ErrTokenInvalidIssuer
	}
	if typ, _ := claims["typ"].(string); typ != expectedType {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}
	expFloat, ok := claims["exp"].(float64)
	if !ok {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}
	exp := time.Unix(int64(expFloat), 0)
	if time.Now().After(exp) {
		return "", "", time.Time{}, jwt.ErrTokenExpired
	}
	sub, ok := claims["sub"]
	if !ok {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}
	uid, err := toUID(sub)
	if err != nil || uid == "" {
		return "", "", time.Time{}, jwt.ErrTokenInvalidClaims
	}
	return uid, jti, exp, nil
}

// toUID 从 JWT claim 取出 uid（UUIDv7 字符串）。
func toUID(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", jwt.ErrTokenInvalidClaims
	}
	return s, nil
}
