package biz

import "github.com/google/wire"

// ProviderSet conversation 服务的 biz 层 providers。
var ProviderSet = wire.NewSet(NewConversationUsecase)
