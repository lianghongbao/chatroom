// Package nacos 对接 Nacos：配置中心（config.Source）、注册（registry.Registrar）
// 与服务发现（registry.Discovery）。
package nacos

import (
	"net"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// DefaultPort 缺端口时的回退值。
const DefaultPort uint64 = 8848

// ParseAddr 解析 "ip:port"，空串或 host 为空时回退到 127.0.0.1。
// raw 是地址原文。
func ParseAddr(raw string) (host string, port uint64) {
	if raw == "" {
		return "127.0.0.1", DefaultPort
	}
	h, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		return raw, DefaultPort
	}
	if h == "" {
		h = "127.0.0.1"
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return h, DefaultPort
	}
	return h, p
}

// Options 描述一个服务连 nacos 的方式。
type Options struct {
	ServerAddresses []string // nacos server 地址列表，形如 "127.0.0.1:8848"
	NamespaceId     string   // 命名空间。空表示 public
	Group           string   // 服务分组。空表示 DEFAULT_GROUP
	ClusterName     string   // 集群名。空表示 DEFAULT
	ServiceName     string   // 注册名。别的服务按这个名字发现本服务
	DataID          string   // 业务配置的 dataId。空则回退到 <ServiceName>.yaml
	Username        string   // nacos 鉴权凭证
	Password        string
}

// serverConfigs 地址列表 → nacos SDK 的 server 配置。
func (o Options) serverConfigs() []constant.ServerConfig {
	sc := make([]constant.ServerConfig, 0, len(o.ServerAddresses))
	for _, raw := range o.ServerAddresses {
		host, port := ParseAddr(raw)
		sc = append(sc, constant.ServerConfig{IpAddr: host, Port: port})
	}
	return sc
}

// GroupOrDefault 归一化后的 group 名。
func (o Options) GroupOrDefault() string {
	if o.Group != "" {
		return o.Group
	}
	return constant.DEFAULT_GROUP
}

// DataIDOrDefault 归一化后的配置 dataId。
func (o Options) DataIDOrDefault() string {
	if o.DataID != "" {
		return o.DataID
	}
	if o.ServiceName != "" {
		return o.ServiceName + ".yaml"
	}
	return "chatroom.yaml"
}

// NewConfigClient 构造 nacos 配置中心 client。
func NewConfigClient(o Options) (config_client.IConfigClient, error) {
	cc := &constant.ClientConfig{
		NamespaceId:         o.NamespaceId,
		Username:            o.Username,
		Password:            o.Password,
		TimeoutMs:           5000,
		NotLoadCacheAtStart: true,
		// 固定缓存目录。SDK 默认放 <exe-dir>/cache/config，go run 的临时目录建不出这个子目录
		CacheDir: "/tmp/nacos-cache",
	}

	return clients.NewConfigClient(vo.NacosClientParam{
		ServerConfigs: o.serverConfigs(),
		ClientConfig:  cc,
	})
}

// NewNamingClient 构造命名 client，注册端和发现端共用。
// 没配 nacos 地址时返回 nil。
func NewNamingClient(o Options) (naming_client.INamingClient, func(), error) {
	if len(o.ServerAddresses) == 0 {
		return nil, func() {}, nil
	}

	cli, err := clients.NewNamingClient(vo.NacosClientParam{
		ServerConfigs: o.serverConfigs(),
		ClientConfig: &constant.ClientConfig{
			Username:            o.Username,
			Password:            o.Password,
			NamespaceId:         o.NamespaceId,
			TimeoutMs:           5000,
			NotLoadCacheAtStart: true,
			LogDir:              "/tmp/nacos/log",
		},
	})
	if err != nil {
		return nil, func() {}, err
	}
	return cli, func() { cli.CloseClient() }, nil
}
