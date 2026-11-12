// Package server 是 ws-gateway 的 server 装配层。
//
// 网关没有 proto 服务 —— 它只提供 WS 端点（/ws）和一个健康检查
package server

import (
	"github.com/google/wire"
)

// ProviderSet 是 server 层的 providers。
var ProviderSet = wire.NewSet(NewHTTPServer)
