package oss

import (
	"context"
	"errors"
	"net/http"
	"strings"

	alioss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

// ObjectInfo 对象的元信息。
type ObjectInfo struct {
	// Size 字节数。
	Size int64
	// ContentType MIME 类型。
	ContentType string
}

// StatObject 查对象的元信息，确认它真的存在。
// ctx 上下文
// fileKey object key
//
// 对象不存在时返 ErrObjectNotFound。
func (c *Client) StatObject(ctx context.Context, fileKey string) (*ObjectInfo, error) {
	key := extractFileKey(fileKey)
	if key == "" {
		return nil, ErrObjectNotFound
	}

	cred, err := c.fetchSTSCredential(ctx)
	if err != nil {
		return nil, err
	}

	client := c.ossClient(cred)
	result, err := client.HeadObject(ctx, &alioss.HeadObjectRequest{
		Bucket: alioss.Ptr(c.cfg.Bucket),
		Key:    alioss.Ptr(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}

	info := &ObjectInfo{Size: result.ContentLength}
	if result.ContentType != nil {
		info.ContentType = *result.ContentType
	}
	return info, nil
}

// CheckObject 确认对象存在，且大小与类型符合预期。
// ctx 上下文
// fileKey object key
// maxSize 允许的最大字节数，小于等于 0 表示不限
// allowedContentTypes 允许的 MIME 类型白名单，为空表示不限
//
// 不存在时返 ErrObjectNotFound；大小超限或类型不符时返 ErrObjectMismatch。
func (c *Client) CheckObject(ctx context.Context, fileKey string, maxSize int64, allowedContentTypes []string) error {
	info, err := c.StatObject(ctx, fileKey)
	if err != nil {
		return err
	}
	if maxSize > 0 && info.Size > maxSize {
		return ErrObjectMismatch
	}
	if len(allowedContentTypes) > 0 && !containsFold(allowedContentTypes, info.ContentType) {
		return ErrObjectMismatch
	}
	return nil
}

// ListObjects 列前缀下的对象。
// ctx 上下文
// prefix key 前缀
// maxKeys 最多返回几个
//
// 返回 object key 列表。
func (c *Client) ListObjects(ctx context.Context, prefix string, maxKeys int) ([]string, error) {
	if maxKeys <= 0 {
		maxKeys = 100
	}
	cred, err := c.fetchSTSCredential(ctx)
	if err != nil {
		return nil, err
	}
	result, err := c.ossClient(cred).ListObjects(ctx, &alioss.ListObjectsRequest{
		Bucket:  alioss.Ptr(c.cfg.Bucket),
		Prefix:  alioss.Ptr(prefix),
		MaxKeys: int32(maxKeys),
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(result.Contents))
	for _, obj := range result.Contents {
		if obj.Key != nil {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

// ossClient 构造一个用指定凭证的 OSS SDK 客户端。
func (c *Client) ossClient(cred *STSCredential) *alioss.Client {
	provider := credentials.NewStaticCredentialsProvider(
		cred.AccessKeyId,
		cred.AccessKeySecret,
		cred.SecurityToken,
	)
	return alioss.NewClient(&alioss.Config{
		Region:              alioss.Ptr(c.cfg.Region),
		Endpoint:            alioss.Ptr(c.cfg.Endpoint),
		CredentialsProvider: provider,
	})
}

// isNotFound 判断错误是不是「对象不存在」。
func isNotFound(err error) bool {
	var serr *alioss.ServiceError
	if errors.As(err, &serr) {
		return serr.StatusCode == http.StatusNotFound
	}
	return false
}

// containsFold 大小写不敏感地判断切片里有没有这个值。
func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
