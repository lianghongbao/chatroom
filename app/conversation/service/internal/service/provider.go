package service

import "github.com/google/wire"

// ProviderSet conversation 服务的 service 层 providers。
var ProviderSet = wire.NewSet(NewConversationService)
