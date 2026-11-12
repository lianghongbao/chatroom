// Package server 是 friend 域的 server 装配层
//
// 构造 HTTP/gRPC server 并注册 friend 域自己的路由。
package server

import (
	"github.com/google/wire"
)

// InternalOps 该域的内部端点；friend 域没有内部端点
func InternalOps() []string {
	return nil
}

// ProviderSet 是 server 层的 providers。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)
