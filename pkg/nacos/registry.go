package nacos

import (
	"github.com/go-kratos/kratos/contrib/registry/nacos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
)

// NewRegistrar 把命名 client 包装成 kratos 的 registry.Registrar。
// cli 为 nil 表示没配 nacos，跳过注册。
func NewRegistrar(cli naming_client.INamingClient, o Options) (registry.Registrar, func(), error) {
	if cli == nil {
		return nil, func() {}, nil
	}

	opts := []nacos.Option{
		nacos.Option(nacos.WithGroup(o.GroupOrDefault())),
	}
	if o.ClusterName != "" {
		opts = append(opts, nacos.Option(nacos.WithCluster(o.ClusterName)))
	}
	return nacos.New(cli, opts...), func() {}, nil
}

// NewDiscovery 把命名 client 包装成 kratos 的 registry.Discovery。
// cli 为 nil 表示没配 nacos，跳过发现。
func NewDiscovery(cli naming_client.INamingClient, o Options) (registry.Discovery, func(), error) {
	if cli == nil {
		return nil, func() {}, nil
	}

	opts := []nacos.Option{
		nacos.Option(nacos.WithGroup(o.GroupOrDefault())),
	}
	if o.ClusterName != "" {
		opts = append(opts, nacos.Option(nacos.WithCluster(o.ClusterName)))
	}
	return nacos.New(cli, opts...), func() {}, nil
}
