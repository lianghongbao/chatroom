package conf

import "chatroom/pkg/nacos"

// NacosOptions 把本服务的 registry.nacos 段翻译成 pkg/nacos.Options。r 是本服务的 registry 配置。
//
// 翻译放在这里而不是 pkg/nacos 里：pkg/nacos 不该认识任何服务的 conf
// proto（那会让共享库反向依赖具体服务）。conf proto 本来就是每服务一份，
// 翻译跟着它走是对的。
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
