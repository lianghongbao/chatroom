package conf

import "chatroom/pkg/nacos"

// NacosOptions 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options。
// r 是 registry 配置段。
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
