// Package server 是 message 域的 server 装配层：构造 HTTP/gRPC server 并注册本域路由。
package server

import (
	"github.com/google/wire"
)

// InternalOps 该域的内部端点。message 域没有内部端点。
func InternalOps() []string {
	return nil
}

// ProviderSet 是 server 层的 providers。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)
