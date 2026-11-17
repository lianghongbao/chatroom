package biz

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	v1 "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/oss"

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
	// ErrUserBanned 账号被禁用（status=0）。登录 / 刷新 token 时返回。
	ErrUserBanned = kratoserrors.Forbidden(v1.ErrorReason_USER_BANNED.String(), "account is disabled")
	// ErrUserPhoneTaken 手机号已被占用（全局唯一）。注册 / 改手机号时返回。
	ErrUserPhoneTaken = kratoserrors.Conflict(v1.ErrorReason_USER_PHONE_TAKEN.String(), "phone number already taken")
	// ErrUserRateLimited 触发限频。注册超额时返回。
	ErrUserRateLimited = kratoserrors.TooManyRequests(v1.ErrorReason_USER_RATE_LIMITED.String(), "too many requests, try again later")
)

// 输入长度上限。schema 里没给 username / nickname / avatar 定 MaxLen，
// 所以得在 biz 层挡 —— 否则超长值会一路写进库（MySQL 那边也可能截断）。
const (
	MinPasswordLength = 6
	MaxPasswordLength = 72 // bcrypt 的硬上限，超过会被 bcrypt 拒绝
	MaxUsernameLength = 36
	MaxNicknameLength = 64
	MaxAvatarLength   = 512
	// PhoneLength 中国大陆手机号长度。11 位：1 开头 + 3~9 + 9 位数字。
	PhoneLength = 11
)

// validatePhone 校验手机号格式。
// phone 是待校验的手机号；空串视为「不填」，返回 nil。
//
// 只做格式校验，不查库 —— 唯一性由数据库的唯一索引最终仲裁
// （并发下两个请求同时通过应用层校验，靠索引兜住）。
func validatePhone(phone string) error {
	if phone == "" {
		return nil
	}
	if len(phone) != PhoneLength {
		return ErrUserInvalidArgument
	}
	// 首位必须是 1，第二位 3-9（正则里就是 [3-9]），其余 9 位必须是数字。
	for i, c := range phone {
		switch {
		case i == 0:
			if c != '1' {
				return ErrUserInvalidArgument
			}
		case i == 1:
			if c < '3' || c > '9' {
				return ErrUserInvalidArgument
			}
		default:
			if c < '0' || c > '9' {
				return ErrUserInvalidArgument
			}
		}
	}
	return nil
}

// validatePassword 校验密码长度。password 是明文密码；不做 trim，只拒绝「全是空白」的密码。
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

// validateUsername 校验用户名长度。username 由调用方先 trim + 小写化。
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
// nickname 昵称
// avatar 头像地址
func validateProfile(nickname, avatar string) error {
	if len(nickname) > MaxNicknameLength {
		return ErrUserInvalidArgument
	}
	if len(avatar) > MaxAvatarLength {
		return ErrUserInvalidArgument
	}
	return nil
}

// derefOrEmpty 取可空指针的值。
// s 为 nil 时返空串。
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// hashPassword 算 bcrypt 哈希，把 bcrypt 的错误翻译成 typed error。password 是明文密码。
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

// User 用户领域模型。Email / Phone 仅自己可见；IsVisibleToStranger=false 时
// GetUserByID 对他人折叠成 NOT_FOUND。
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

// IsBanned 判断账号是否被禁用。u 为 nil 时返 false。
func (u *User) IsBanned() bool {
	return u != nil && u.Status == UserStatusBanned
}

// UserRepo 用户仓储接口。
type UserRepo interface {
	Save(ctx context.Context, u *User) (string, error)
	FindByID(ctx context.Context, id string) (*User, error)
	// FindByIDWithHash 与 FindByID 同义，但保留 PasswordHash，供改密比对旧密码。
	FindByIDWithHash(ctx context.Context, id string) (*User, error)
	FindByUsername(ctx context.Context, username string) (*User, error)
	// FindByPhone 按手机号查用户。手机号全局唯一，最多一个结果。
	FindByPhone(ctx context.Context, phone string) (*User, error)
	// 批量查；找不到的 id 在结果中不出现，不返错。返回顺序与 ids 无关
	FindByIDs(ctx context.Context, ids []string) ([]*User, error)
	Update(ctx context.Context, id string, nickname, avatar string) (*User, error)
	// 仅传非 nil 指针的字段被写入。Phone / Email 都在里面（见 UserUpdateFields）。
	UpdateFields(ctx context.Context, id string, fields UserUpdateFields) (*User, error)
	UpdatePassword(ctx context.Context, id string, newHash string) error
	// 改账号状态（禁用 / 解禁）。调用方是管理侧，不开放给普通用户
	UpdateStatus(ctx context.Context, id string, status UserStatus) error
}

// UserUpdateFields 可空字段集合；service 从 proto FieldMask 拼装。
type UserUpdateFields struct {
	Nickname *string
	Avatar   *string
	// Phone 手机号。指针非 nil 时写入；指向空串表示「清空手机号」。
	Phone *string
	// Email 邮箱。指针非 nil 时写入；指向空串表示「清空邮箱」。
	// 不唯一、不校验格式 —— 目前只是备注字段。
	Email *string
}

// LoginLimiter 登录频控抽象。
// clientIP 是调用方 IP —— 限频按「用户名 + IP」两个维度分桶。
type LoginLimiter interface {
	// Allow 在 Login 入口调用，命中锁定返回 ErrUserLoginLocked。
	// username 是用户名，clientIP 是调用方 IP。
	Allow(ctx context.Context, username, clientIP string) error
	// OnSuccess 凭据校验通过后清掉失败计数。
	OnSuccess(ctx context.Context, username, clientIP string)
	// OnFailure 凭据校验失败后累加失败计数。参数非法的路径不调用，避免被恶意空请求刷到锁定。
	OnFailure(ctx context.Context, username, clientIP string)
}

// RegisterLimiter 注册频控抽象。
//
// 与 LoginLimiter 分开是因为维度不同：注册按「IP」限，不按用户名 ——
// 用户名可以随便编，按用户名限挡不住批量注册。
type RegisterLimiter interface {
	// Allow 在 Register 入口调用，超配额返回 ErrUserRateLimited。
	// clientIP 是调用方 IP。
	Allow(ctx context.Context, clientIP string) error
}

// Release Acquire 的释放闭包，必须幂等；用闭包强制 caller defer 调用。
type Release func()

// Locker 按 key 的非重入互斥。Acquire 返 (release, nil) / (nil, ErrLockBusy) / (noopRelease, nil)。
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Release, error)
}

// ErrLockBusy 锁被他人持有。biz 折叠为 ErrUserAlreadyExists (Register) /
// ErrUserLoginLocked (Login)，不向客户端暴露锁的存在。
var ErrLockBusy = kratoserrors.Conflict(
	v1.ErrorReason_USER_ALREADY_EXISTS.String(),
	"request is being processed, retry later",
)

// TokenStore token 状态。Redis 不可用时降级：Blacklist / SaveRefresh 失败仅 warn，
// ConsumeRefresh 失败则拒绝 refresh。
type TokenStore interface {
	Blacklist(ctx context.Context, jti string, ttl time.Duration) error
	IsBlacklisted(ctx context.Context, jti string) (bool, error)
	SaveRefresh(ctx context.Context, jti string, uid string, userAgent string, ttl time.Duration) error
	// ConsumeRefresh 原子 GET uid + DEL。找不到 / 已过期 / 已被轮换均返 ErrRefreshInvalid。
	ConsumeRefresh(ctx context.Context, jti string) (string, error)
	RevokeRefresh(ctx context.Context, jti string) error
}

// LoginResult 登录成功的 token 对；access / refresh 合并让 service 只做一次 DTO 转换。
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // access 剩余秒数
}

// UserCache 用户读缓存抽象。Get 返回的 *User 必须不含 PasswordHash（data 反序列化时清空）。
type UserCache interface {
	// Get 命中返 (user, ver, nil)；未命中 (nil, 0, nil)。Redis 错返 (nil, 0, err)。
	Get(ctx context.Context, id string) (*User, int64, error)
	// Set 写回缓存。
	Set(ctx context.Context, u *User, ver int64) error
	// Invalidate 自增 verKey，让旧缓存值被识别为 stale。返回新 ver。
	Invalidate(ctx context.Context, id string) (int64, error)
	// Version 读当前版本号。
	Version(ctx context.Context, id string) (int64, error)
}

// UserUsecase 用户用例。
type UserUsecase struct {
	repo          UserRepo
	auth          *conf.Auth
	lim           LoginLimiter
	regLim        RegisterLimiter
	locker        Locker
	store         TokenStore
	cache         UserCache
	tokenFn       func(uid string) (string, error) // auth 配置有效时才赋值，避免冷启动签发空密钥 token
	logger        *slog.Logger
	oss           *oss.Client // 对象存储客户端，为 nil 时关闭上传
	avatarExts    []string    // 头像允许的后缀白名单
	avatarMaxSize int64       // 头像最大字节数，0 表示不限
}

// NewUserUsecase 构造用户用例
// repo 仓储实现
// auth 鉴权配置，为 nil 时关闭登录
// lim 登录限频器，为 nil 时关闭登录限频
// regLim 注册限频器，为 nil 时关闭注册限频
// locker 注册锁，为 nil 时关闭加锁
// store token 存储，为 nil 时关闭持久化
// cache 资料缓存，为 nil 时关闭缓存
// storage 对象存储能力，为零值时关闭头像上传
// logger 日志器，为 nil 时用默认 logger。
func NewUserUsecase(repo UserRepo, auth *conf.Auth, lim LoginLimiter, regLim RegisterLimiter, locker Locker, store TokenStore, cache UserCache, storage StorageOptions, logger *slog.Logger) *UserUsecase {
	if logger == nil {
		logger = slog.Default()
	}
	uc := &UserUsecase{repo: repo, auth: auth, lim: lim, regLim: regLim, locker: locker, store: store, cache: cache, logger: logger}
	if auth != nil && auth.GetJwtSecret() != "" {
		uc.tokenFn = uc.signToken
	}
	uc.oss = storage.Client
	uc.avatarExts = storage.AllowedExts
	uc.avatarMaxSize = storage.MaxSize
	return uc
}

// Register 注册用户并返回新用户 ID。
// username 用户名
// password 明文密码
// nickname 是昵称（可空，留空时回退成 username）
// phone 是手机号（可空，留空表示不填）。非空时全局唯一，被占用返 ErrUserPhoneTaken。
// email 是邮箱（可空，留空表示不填）。不唯一、不校验格式。
// clientIP 调用方 IP，用于注册限频。
// username trim 后小写化。
func (uc *UserUsecase) Register(ctx context.Context, username, password, nickname, phone, email, clientIP string) (string, error) {
	username = strings.TrimSpace(username)
	nickname = strings.TrimSpace(nickname)
	phone = strings.TrimSpace(phone)
	email = strings.TrimSpace(email)
	if err := validateUsername(username); err != nil {
		return "", err
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	if err := validateProfile(nickname, ""); err != nil {
		return "", err
	}
	if err := validatePhone(phone); err != nil {
		return "", err
	}
	username = strings.ToLower(username)

	// 注册限频放在参数校验之后、取锁之前。
	//
	// 位置很讲究：
	//   - 参数非法不该消耗配额 —— 恶意空请求会把正常用户的额度耗光，
	//     所以先校验参数再限频。
	//   - 限频在取锁之前 —— 限频是 Redis 往返，比内存锁慢，
	//     但它挡的是「一个 IP 狂注册」这种量级，取锁那点开销无所谓。
	if uc.regLim != nil {
		if err := uc.regLim.Allow(ctx, clientIP); err != nil {
			return "", err
		}
	}

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
	// 手机号占用检查。并发下可能双双通过 —— 靠数据库唯一索引最终仲裁
	// （data 层把唯一索引冲突翻译成 ErrUserPhoneTaken）。
	if phone != "" {
		if existing, err := uc.repo.FindByPhone(ctx, phone); err == nil && existing != nil {
			return "", ErrUserPhoneTaken
		} else if err != nil && !errors.Is(err, ErrUserNotFound) {
			return "", err
		}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	u := &User{Username: username, Nickname: nickname, PasswordHash: hash, Phone: phone, Email: email}
	return uc.repo.Save(ctx, u)
}

// acquireRegisterLock 取注册锁。username 是用户名；TTL 5s。
func (uc *UserUsecase) acquireRegisterLock(ctx context.Context, username string) (Release, error) {
	if uc.locker == nil {
		return func() {}, nil
	}
	return uc.locker.Acquire(ctx, "register:lock:"+username, 5*time.Second)
}

// Login 校验凭据，签发 access + refresh 并落 TokenStore。username 是用户名，
// password 明文密码
// clientIP 调用方 IP。返回 token 对与用户 ID
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

// acquireLoginLock 取登录锁。username 是用户名；TTL 2s。
func (uc *UserUsecase) acquireLoginLock(ctx context.Context, username string) (Release, error) {
	if uc.locker == nil {
		return func() {}, nil
	}
	return uc.locker.Acquire(ctx, "login:lock:"+username, 2*time.Second)
}

// notifyFailure 凭据校验失败时调；username 为空（参数非法路径）不调，防止空刷。
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

// Refresh 用 refresh token 换新的一对 token。refreshToken 是旧的 refresh token。
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

// Logout 把当前会话的 token 作废：access 进黑名单 + refresh 删除。
// accessJTI access token 的 JTI
// refreshJTI refresh token 的 JTI
// accessExp access token 的过期时间；均 warn 不返错
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

// parseRefresh 解析校验 refresh token，返回 JTI 与用户 ID。
// raw 是 token 原文
// 失败一律折叠 ErrRefreshInvalid，不区分原因避免暴露服务端行为。
func (uc *UserUsecase) parseRefresh(raw string) (jti string, uid string, err error) {
	uid, jti, _, err = uc.verify(raw, tokenTypeRefresh)
	if err != nil {
		return "", "", ErrRefreshInvalid
	}
	return jti, uid, nil
}

// GetUser 按 ID 读用户资料，id 是用户 ID。cache-aside：命中且版本一致直返，否则读 DB 并回写。
func (uc *UserUsecase) GetUser(ctx context.Context, id string) (*User, error) {
	if id == "" {
		return nil, ErrUserInvalidArgument
	}

	// 版本号要在读 DB 之前取 —— 否则可能把旧 DB 数据配上新版本号写回缓存，
	// 这份陈旧数据从此看起来是新鲜的。
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

// UpdateUser 全量更新资料。
// id 用户 ID
// nickname 昵称
// avatar 头像地址
// 两者都必非空；partial update 走 UpdateUserFields。
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
	// 配了对象存储就校验头像地址 —— host / 前缀 / 后缀白名单 + 对象真实存在。
	if uc.oss != nil {
		clean, err := uc.ValidateAvatarURL(ctx, id, avatar)
		if err != nil {
			return nil, err
		}
		avatar = clean
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

// UpdateUserFields 字段粒度更新。
// id 用户 ID
// fields 待改字段集合
// 至少一个字段被设置；nickname / avatar 非 nil 时不能为空字符串。
// phone / email 例外：空串表示「清空」。
func (uc *UserUsecase) UpdateUserFields(ctx context.Context, id string, fields UserUpdateFields) (*User, error) {
	if id == "" {
		return nil, ErrUserInvalidArgument
	}
	if fields.Nickname == nil && fields.Avatar == nil && fields.Phone == nil && fields.Email == nil {
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
		// 配了对象存储就校验地址 —— host / 前缀 / 后缀白名单 + 对象真实存在。
		// 没配就只留长度校验（历史行为）。
		if uc.oss != nil {
			clean, err := uc.ValidateAvatarURL(ctx, id, v)
			if err != nil {
				return nil, err
			}
			v = clean
		}
		fields.Avatar = &v
	}
	if err := validateProfile(derefOrEmpty(fields.Nickname), derefOrEmpty(fields.Avatar)); err != nil {
		return nil, err
	}
	// 手机号：空串表示「清空」，所以不能像 nickname 那样把空串判为非法。
	// 非空时才校验格式。
	if fields.Phone != nil {
		v := strings.TrimSpace(*fields.Phone)
		if err := validatePhone(v); err != nil {
			return nil, err
		}
		// 占用检查 —— 改自己的号不能撞上别人的。
		// 撞唯一索引的最终仲裁在 data 层。
		if v != "" {
			if existing, err := uc.repo.FindByPhone(ctx, v); err == nil && existing != nil && existing.ID != id {
				return nil, ErrUserPhoneTaken
			} else if err != nil && !errors.Is(err, ErrUserNotFound) {
				return nil, err
			}
		}
		fields.Phone = &v
	}
	// 邮箱：空串表示「清空」，语义跟手机号一样。不用查库占位 —— 邮箱不唯一。
	// 也不校验格式：等真正接「验证码改密」时再加。
	if fields.Email != nil {
		v := strings.TrimSpace(*fields.Email)
		fields.Email = &v
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

// GetUserByID 读指定用户的资料。
// viewerID 调用者 ID
// targetID 被查用户 ID
// 他人 + 不可见折叠 NOT_FOUND；他人 + 可见清空 Email / Phone。
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

// GetUserByUsername 按用户名精确查公开资料。
// username 用户名
//
// 跟 GetUserByID 的区别：**不做「对陌生人可见」判定**。
// 好友申请本来就允许陌生人互相发起，对方关了可见性就查不到人的话，
// 加好友这条路直接堵死。Email / Phone 一律清空 —— 查到的永远是「别人」。
func (uc *UserUsecase) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrUserInvalidArgument
	}
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	// username 存库时已小写化（见 Register），查询侧同样小写化才能对上。
	username = strings.ToLower(username)

	u, err := uc.repo.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	u.Email = ""
	u.Phone = ""
	return u, nil
}

// GetUserByPhone 按手机号精确查公开资料。
// phone 手机号
//
// 跟 GetUserByUsername 语义一致：不做「对陌生人可见」判定，
// 手机号全局唯一所以最多一个结果。Email / Phone 一律清空 —— 查到的永远是「别人」。
func (uc *UserUsecase) GetUserByPhone(ctx context.Context, phone string) (*User, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return nil, ErrUserInvalidArgument
	}
	if err := validatePhone(phone); err != nil {
		return nil, err
	}

	u, err := uc.repo.FindByPhone(ctx, phone)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	u.Email = ""
	u.Phone = ""
	return u, nil
}

// GetUsersByIDs 批量按 ID 查公开资料。
// viewerID 调用者 ID
// ids 待查用户 ID 列表
// 上限 200，不存在 / 不可见静默剔除。
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
// ids 是待查用户 ID 列表；跳过 is_visible_to_stranger 判定，仅限服务间调用。
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

// GetConversationMemberProfiles 批量拉「会话成员可见」的公开资料，供 conversation
// 服务补全会话成员列表。ids 是待查用户 ID 列表；跳过可见性判定。
func (uc *UserUsecase) GetConversationMemberProfiles(ctx context.Context, ids []string) ([]*User, error) {
	return uc.GetFriendProfiles(ctx, ids)
}

// loadUsersByIDs cache-aside 批量按 ID 读用户，忽略可见性判定。
// ids 是待查用户 ID 列表；返回 map[id]*User，不存在的 id 不在结果中。
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

// maxBatchIDs 单次批量查询上限，与 friend service ListFriends page_size (200) 对齐。
const maxBatchIDs = 200

// ChangePassword 修改密码，改完强制登出当前会话。
// uid 用户 ID
// oldPassword 旧明文密码
// newPassword 新明文密码
// accessJTI 当前会话的 access token 标识
// refreshJTI 当前会话的 refresh token 标识
// accessExp access token 的过期时间；旧密码错 / 用户不存在折叠 ErrUserBadCredentials。
func (uc *UserUsecase) ChangePassword(ctx context.Context, uid string, oldPassword, newPassword, accessJTI, refreshJTI string, accessExp time.Time) error {
	if uid == "" {
		return ErrUserInvalidArgument
	}
	// 密码不 trim —— 注册时怎么存的就得怎么比对。
	if oldPassword == "" {
		return ErrUserInvalidArgument
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if oldPassword == newPassword {
		return ErrUserInvalidArgument
	}

	u, err := uc.repo.FindByIDWithHash(ctx, uid)
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

// signAccess 签发 access token。uid 是用户 ID，返回 token 串、JTI、过期时间；
// 默认 15m，access_ttl 未设置回退 token_ttl。
func (uc *UserUsecase) signAccess(uid string) (string, string, time.Time, error) {
	ttl, issuer := uc.tokenConfig()
	if d := uc.auth.GetAccessTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	return uc.sign(uid, tokenTypeAccess, ttl, issuer)
}

// signRefresh 签发 refresh token。uid 是用户 ID，返回 token 串、JTI、过期时间；默认 7d。
func (uc *UserUsecase) signRefresh(uid string) (string, string, time.Time, error) {
	ttl, issuer := uc.tokenConfig()
	ttl = 7 * 24 * time.Hour
	if d := uc.auth.GetRefreshTtl(); d != nil && d.AsDuration() > 0 {
		ttl = d.AsDuration()
	}
	return uc.sign(uid, tokenTypeRefresh, ttl, issuer)
}

// tokenConfig 抽出 token_ttl fallback + issuer 默认值的统一逻辑，返回 TTL 与签发者。
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

// sign access / refresh 共用签发。
// uid 用户 ID
// typ token 类型
// ttl 有效期
// issuer 签发者
// 返回 token 串、JTI、过期时间。
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

// signToken 签发 access token 并只返回 token 串。uid 是用户 ID。
//
//nolint:unused
func (uc *UserUsecase) signToken(uid string) (string, error) {
	tok, _, _, err := uc.signAccess(uid)
	return tok, err
}

// verify 解析校验 token。
// raw token 串
// expectedType 期望的 token 类型
// 返回用户 ID、JTI、过期时间。校验 iss / typ / exp，失败原因不暴露给 caller。
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
// v 是 claim 原始值。
func toUID(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", jwt.ErrTokenInvalidClaims
	}
	return s, nil
}
