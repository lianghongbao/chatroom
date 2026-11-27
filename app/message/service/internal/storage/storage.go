// Package storage 装配对象存储能力（阿里云 OSS）。
//
// 翻译放在服务这边，避免 pkg/oss 反向依赖具体服务的 conf proto ——
// pkg/oss 只认自己的 Config 结构。
package storage

import (
	"log/slog"

	"chatroom/app/message/service/internal/conf"
	"chatroom/pkg/oss"
)

// NewClient 用 storage 配置段构造 OSS 客户端。
// s storage 配置段
// logger 日志器，用于记录降级原因
//
// 配置不完整（缺 bucket / region / role_arn / AK / SK）时返 nil ——
// 对象存储是可选能力，没配就关掉聊天图片上传，不影响服务的其他功能。
func NewClient(s *conf.Storage, logger *slog.Logger) *oss.Client {
	if s == nil {
		return nil
	}
	client, err := oss.NewClient(conf.OSSOptions(s))
	if err != nil {
		if logger != nil {
			logger.Warn("对象存储配置不完整，聊天图片上传功能已关闭", "err", err)
		}
		return nil
	}
	return client
}
