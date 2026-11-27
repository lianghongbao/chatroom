package biz

import "github.com/google/wire"

// ProviderSet 是 user 服务的 biz providers。
var ProviderSet = wire.NewSet(NewUserUsecase)
