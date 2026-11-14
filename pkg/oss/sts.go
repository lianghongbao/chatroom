package oss

import (
	"context"
	"time"

	"github.com/aliyun/credentials-go/credentials"
)

// newRAMRoleCredential 用长期 RAM AK/SK 调 STS AssumeRole 换临时凭证。
// ctx 上下文
// cfg OSS 配置
// sessionDuration 临时凭证有效期（秒）
func newRAMRoleCredential(ctx context.Context, cfg Config, sessionDuration int) (*STSCredential, error) {
	cred, err := credentials.NewCredential(
		new(credentials.Config).
			SetType("ram_role_arn").
			SetAccessKeyId(cfg.AccessKeyID).
			SetAccessKeySecret(cfg.AccessKeySecret).
			SetRoleArn(cfg.RoleArn).
			SetRoleSessionName("oss-upload").
			SetRoleSessionExpiration(sessionDuration),
	)
	if err != nil {
		return nil, err
	}

	cm, err := cred.GetCredential()
	if err != nil {
		return nil, err
	}
	if cm.AccessKeyId == nil || cm.AccessKeySecret == nil || cm.SecurityToken == nil {
		return nil, ErrAssumeRoleFailed
	}

	return &STSCredential{
		AccessKeyId:     *cm.AccessKeyId,
		AccessKeySecret: *cm.AccessKeySecret,
		SecurityToken:   *cm.SecurityToken,
		Expiration:      time.Now().Add(time.Duration(sessionDuration) * time.Second),
	}, nil
}
