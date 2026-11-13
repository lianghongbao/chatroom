// Package errors 定义跨服务共用的框架级错误原因（与具体业务无关）。
package errors

// ErrorReason 框架级错误原因。
type ErrorReason int32

const (
	ErrorReasonCommonUnspecified          ErrorReason = 0 // 未指定
	ErrorReasonCommonUnauthenticated      ErrorReason = 1 // 凭证缺失或无效
	ErrorReasonCommonPermissionDenied     ErrorReason = 2 // 已认证但无权限
	ErrorReasonCommonInternalTokenInvalid ErrorReason = 3 // 内部凭证缺失或无效
	ErrorReasonCommonUpstreamUnavailable  ErrorReason = 4 // 上游服务不可达
	ErrorReasonCommonRateLimited          ErrorReason = 5 // 限频命中
	ErrorReasonCommonInvalidArgument      ErrorReason = 6 // 参数非法
	ErrorReasonCommonTimeout              ErrorReason = 7 // 超时
)

// reasonName 枚举值 → 字符串。
var reasonName = map[int32]string{
	0: "COMMON_UNSPECIFIED",
	1: "COMMON_UNAUTHENTICATED",
	2: "COMMON_PERMISSION_DENIED",
	3: "COMMON_INTERNAL_TOKEN_INVALID",
	4: "COMMON_UPSTREAM_UNAVAILABLE",
	5: "COMMON_RATE_LIMITED",
	6: "COMMON_INVALID_ARGUMENT",
	7: "COMMON_TIMEOUT",
}

// Reason 取枚举值名字符串。r 是枚举值。
func Reason(r ErrorReason) string {
	return reasonName[int32(r)]
}
