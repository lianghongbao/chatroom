// Package keys 构造跨服务共用的 Redis key 与 Pub-Sub 频道名。
package keys

import "strconv"

// Redis key / Pub-Sub 频道名前缀。
const (
	keyPrefix = "chatroom:" // 全局前缀

	msgChannelPrefix         = keyPrefix + "msg:"               // 消息广播频道，后缀是会话 ID
	tokenBlacklistPrefix     = keyPrefix + "token:bl:"          // access token 黑名单，后缀是 JTI
	refreshTokenPrefix       = keyPrefix + "refresh:"           // refresh token 存储，后缀是 JTI
	msgChannelPattern        = keyPrefix + "msg:*"              // 广播频道匹配模式
	memberInvalidatedChannel = keyPrefix + "member:invalidated" // 成员变更通知频道
	lockPrefix               = keyPrefix + "lock:"              // 互斥锁前缀，后缀是业务 key
)

// MessageChannel 消息广播频道名。
// conversationID 会话 ID
func MessageChannel(conversationID uint64) string {
	return msgChannelPrefix + strconv.FormatUint(conversationID, 10)
}

// MessageChannelPattern 消息广播频道的匹配模式。
func MessageChannelPattern() string {
	return msgChannelPattern
}

// MemberInvalidatedChannel 会话成员变更通知频道名。
func MemberInvalidatedChannel() string {
	return memberInvalidatedChannel
}

// TokenBlacklistKey access token 黑名单的 redis key。
// jti token 的 JTI claim
func TokenBlacklistKey(jti string) string {
	return tokenBlacklistPrefix + jti
}

// RefreshTokenKey refresh token 存储的 redis key。
// jti token 的 JTI claim
func RefreshTokenKey(jti string) string {
	return refreshTokenPrefix + jti
}

// LockKey 互斥锁的 redis key。
// key 业务侧的 key（如 "register:lock:alice"）
func LockKey(key string) string {
	return lockPrefix + key
}

// FormatUint 把 uint64 转成十进制字符串。
// v 要转换的值
func FormatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}
