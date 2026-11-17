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

	// 限流桶前缀，后缀是「维度:标识」。
	//
	// 所有令牌桶共用一个前缀 —— 桶的形状一样（hash{tokens,ts}），
	// 维度靠后缀区分。分前缀没意义，反而容易写重。
	limitBucketPrefix = keyPrefix + "limit:"
)

// limitZeroPlaceholder 是限流维度标识为空时的占位符。
//
// 空标识不能拼进 key —— 那样所有「没有标识」的调用方会共用一个桶，
// 互相影响。占位符把「空标识」也变成一个有名字的独立维度。
const limitZeroPlaceholder = "_zero_"

// LimitBucketKey 限流令牌桶的 redis key。
//
// scope 是限流维度（如 "login:fail"、"friend:apply"），后缀是标识
// （用户名、uid、IP 等）。维度拼错不会撞 —— 不同 scope 是不同桶。
//
// identifier 为空时用占位符，保证每个 scope 都有独立的一个「空标识」桶。
func LimitBucketKey(scope, identifier string) string {
	if identifier == "" {
		identifier = limitZeroPlaceholder
	}
	return limitBucketPrefix + scope + ":" + identifier
}

// LimitBucketPattern 某个限流维度下所有桶的 key 匹配模式。
//
// scope 是限流维度前缀（如 "login:"、"friend:"）。返回的模式
// 匹配该前缀下的所有桶。
//
// 用途：清理 —— 测试之间共用一个 Redis DB，残留的桶会让下一次
// 从「桶已经空了」开始，测出莫名其妙的失败。
func LimitBucketPattern(scopePrefix string) string {
	return limitBucketPrefix + scopePrefix + "*"
}

// MessageChannel 消息广播频道名。conversationID 是会话 ID。
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
// jti 是 token 的 JTI claim。
func TokenBlacklistKey(jti string) string {
	return tokenBlacklistPrefix + jti
}

// RefreshTokenKey refresh token 存储的 redis key。
// jti 是 token 的 JTI claim。
func RefreshTokenKey(jti string) string {
	return refreshTokenPrefix + jti
}

// LockKey 互斥锁的 redis key。key 是业务侧的 key（如 "register:lock:alice"）。
func LockKey(key string) string {
	return lockPrefix + key
}

// FormatUint 把 uint64 转成十进制字符串。v 是要转换的值。
func FormatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}
