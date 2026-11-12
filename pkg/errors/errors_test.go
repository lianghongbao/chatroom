package errors

import "testing"

// COMMON_* 是跨服务契约，值变了就是契约破坏。这里把映射钉住。
func TestReasonMatchesEnumName(t *testing.T) {
	cases := map[ErrorReason]string{
		ErrorReasonCommonUnspecified:          "COMMON_UNSPECIFIED",
		ErrorReasonCommonUnauthenticated:      "COMMON_UNAUTHENTICATED",
		ErrorReasonCommonPermissionDenied:     "COMMON_PERMISSION_DENIED",
		ErrorReasonCommonInternalTokenInvalid: "COMMON_INTERNAL_TOKEN_INVALID",
		ErrorReasonCommonUpstreamUnavailable:  "COMMON_UPSTREAM_UNAVAILABLE",
		ErrorReasonCommonRateLimited:          "COMMON_RATE_LIMITED",
		ErrorReasonCommonInvalidArgument:      "COMMON_INVALID_ARGUMENT",
		ErrorReasonCommonTimeout:              "COMMON_TIMEOUT",
	}
	for v, want := range cases {
		if got := Reason(v); got != want {
			t.Errorf("Reason(%d) = %q, want %q", v, got, want)
		}
	}
}
