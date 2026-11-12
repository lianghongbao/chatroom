package biz

import "github.com/google/wire"

// ProviderSet 是 message 服务的 biz providers。
var ProviderSet = wire.NewSet(NewMessageUsecase)
