package service

import "github.com/google/wire"

// ProviderSet 是 user 服务的 service providers。
var ProviderSet = wire.NewSet(NewUserService)
