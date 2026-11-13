package conf

import "chatroom/pkg/nacos"

// NacosOptions 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options。r 是本服务的 registry 配置。
// 翻译放在服务这边，避免 pkg/nacos 反向依赖具体服务的 conf proto。
func NacosOptions(r *Registry) nacos.Options {
	n := r.GetNacos()
	return nacos.Options{
		ServerAddresses: n.GetServerAddresses(),
		NamespaceId:     n.GetNamespaceId(),
		Group:           n.GetGroup(),
		ClusterName:     n.GetClusterName(),
		ServiceName:     n.GetServiceName(),
		DataID:          n.GetDataId(),
		Username:        n.GetUsername(),
		Password:        n.GetPassword(),
	}
}
