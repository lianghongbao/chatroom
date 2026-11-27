package conf

import (
	"chatroom/pkg/oss"
)

// OSSOptions 把本服务的 storage 段翻译成 pkg/oss.Config。
// s storage 配置段。
func OSSOptions(s *Storage) oss.Config {
	return oss.Config{
		Endpoint:        s.GetEndpoint(),
		Bucket:          s.GetBucket(),
		Region:          s.GetRegion(),
		RoleArn:         s.GetRoleArn(),
		AccessKeyID:     s.GetAccessKeyId(),
		AccessKeySecret: s.GetAccessKeySecret(),
		UploadHost:      s.GetUploadHost(),
		URLPrefix:       s.GetUrlPrefix(),
	}
}
