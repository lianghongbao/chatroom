package service

import (
	"context"
	"testing"

	pb "chatroom/api/user/service/v1"
	"chatroom/app/user/service/internal/biz"
	"chatroom/app/user/service/internal/conf"
	"chatroom/pkg/oss"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// newStorageService 构造一个带对象存储的 service。
// storage 存储配置
func newStorageService(storage biz.StorageOptions) *UserService {
	auth := &conf.Auth{JwtSecret: testSecret, TokenIssuer: "chatroom"}
	uc := biz.NewUserUsecase(&fakeRepo{}, auth, nil, nil, nil, nil, nil, storage, nil)
	return NewUserService(uc, auth)
}

// testOSSClient 构造一个测试用的 OSS 客户端。
func testOSSClient(t *testing.T) *oss.Client {
	t.Helper()
	client, err := oss.NewClient(oss.Config{
		Endpoint:        "oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "test-bucket",
		Region:          "cn-hangzhou",
		RoleArn:         "acs:ram::123456789:role/oss-upload",
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		UploadHost:      "https://test-bucket.oss-cn-hangzhou.aliyuncs.com",
		URLPrefix:       "https://cdn.example.com",
	})
	if err != nil {
		t.Fatalf("构造 OSS 客户端失败：%v", err)
	}
	return client
}

// TestServiceGetUploadToken_NilRequest 请求为 nil 时报参数错误。
func TestServiceGetUploadToken_NilRequest(t *testing.T) {
	s := newStorageService(biz.StorageOptions{})
	if _, err := s.GetUploadToken(context.Background(), nil); !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceGetUploadToken_NoAuth 没鉴权上下文时报参数错误。
func TestServiceGetUploadToken_NoAuth(t *testing.T) {
	s := newStorageService(biz.StorageOptions{})
	_, err := s.GetUploadToken(context.Background(), &pb.GetUploadTokenRequest{Ext: "png"})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}

// TestServiceGetUploadToken_StorageNotConfigured 没配对象存储时报 503。
func TestServiceGetUploadToken_StorageNotConfigured(t *testing.T) {
	s := newStorageService(biz.StorageOptions{})
	ctx := authedCtxWithToken(t, jwtFor(t, testSecret, "u1"), testSecret)
	_, err := s.GetUploadToken(ctx, &pb.GetUploadTokenRequest{Ext: "png"})
	if !kratoserrors.IsServiceUnavailable(err) {
		t.Fatalf("error = %v, want service unavailable", err)
	}
}

// TestServiceGetUploadToken_ExtNotAllowed 后缀不在白名单时报 400。
func TestServiceGetUploadToken_ExtNotAllowed(t *testing.T) {
	s := newStorageService(biz.StorageOptions{
		Client:      testOSSClient(t),
		AllowedExts: []string{"png"},
	})
	ctx := authedCtxWithToken(t, jwtFor(t, testSecret, "u1"), testSecret)
	_, err := s.GetUploadToken(ctx, &pb.GetUploadTokenRequest{Ext: "exe"})
	if !kratoserrors.IsBadRequest(err) {
		t.Fatalf("error = %v, want bad request", err)
	}
}
