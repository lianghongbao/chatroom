package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeBlacklist 是 TokenChecker 的替身。
type fakeBlacklist struct {
	blacklisted bool
	err         error
	jti         string
	calls       int
}

func (f *fakeBlacklist) IsBlacklisted(_ context.Context, jti string) (bool, error) {
	f.calls++
	f.jti = jti
	return f.blacklisted, f.err
}

// testJWT 造一个能通过校验的 access token。uid 是用户 ID，jti 可自定义 ——
// 黑名单是按 jti 查的。
func testJWT(t *testing.T, secret, issuer, uid, jti string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uid,
		"jti": jti,
		"typ": "access",
		"iss": issuer,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// TestJWTVerifierRejectsBlacklistedToken token 被登出后要拒掉握手 ——
// 否则用户登出了，WS 长连接还能继续用。
func TestJWTVerifierRejectsBlacklistedToken(t *testing.T) {
	const secret = "test-secret"
	const issuer = "chatroom"

	tok := testJWT(t, secret, issuer, "uid-42", "jti-1")
	bl := &fakeBlacklist{blacklisted: true}

	v := NewJWTVerifier(secret, issuer, bl)
	_, err := v.Verify(tok)
	if err == nil {
		t.Fatal("被拉黑的 token 该被拒")
	}
	if bl.calls != 1 {
		t.Errorf("查黑名单次数 = %d, want 1", bl.calls)
	}
	if bl.jti != "jti-1" {
		t.Errorf("jti = %q, want jti-1", bl.jti)
	}
}

// TestJWTVerifierAcceptsNormalToken 正常 token 要放行。
func TestJWTVerifierAcceptsNormalToken(t *testing.T) {
	const secret = "test-secret"
	const issuer = "chatroom"

	tok := testJWT(t, secret, issuer, "uid-42", "jti-1")
	bl := &fakeBlacklist{blacklisted: false}

	v := NewJWTVerifier(secret, issuer, bl)
	uid, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if uid != "uid-42" {
		t.Errorf("uid = %q, want uid-42", uid)
	}
}

// TestJWTVerifierDegradesWhenBlacklistFails 查黑名单失败要放行 ——
// 宁可放过一个已登出的连接，也不能让 Redis 抖动把所有连接都拒了。
func TestJWTVerifierDegradesWhenBlacklistFails(t *testing.T) {
	const secret = "test-secret"
	const issuer = "chatroom"

	tok := testJWT(t, secret, issuer, "uid-42", "jti-1")
	bl := &fakeBlacklist{err: errors.New("redis down")}

	v := NewJWTVerifier(secret, issuer, bl)
	uid, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("降级时该放行: %v", err)
	}
	if uid != "uid-42" {
		t.Errorf("uid = %q, want uid-42", uid)
	}
}

// TestJWTVerifierWithoutBlacklist 没有黑名单能力时按老行为放行 ——
// Redis 没配时网关仍能工作。
func TestJWTVerifierWithoutBlacklist(t *testing.T) {
	const secret = "test-secret"
	const issuer = "chatroom"

	tok := testJWT(t, secret, issuer, "uid-42", "jti-1")

	v := NewJWTVerifier(secret, issuer, nil)
	uid, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if uid != "uid-42" {
		t.Errorf("uid = %q, want uid-42", uid)
	}
}

// TestJWTVerifierRejectsInvalidToken token 本身不合法要拒。
func TestJWTVerifierRejectsInvalidToken(t *testing.T) {
	v := NewJWTVerifier("test-secret", "chatroom", &fakeBlacklist{})
	if _, err := v.Verify("not-a-jwt"); err == nil {
		t.Fatal("非法 token 该被拒")
	}
}
