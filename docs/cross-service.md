# 跨服务通信

五个服务跑在五个进程里。本文讲它们之间怎么调、契约放在哪、下游挂了怎么办。

相关：[架构与分层](architecture.md) ｜ [README](../README.md)

## 跨服务调用

一个服务不 import 另一个服务的 `internal/`（Go 的 `internal/` 可见性规则在编译期就挡住了）。
跨域调用走**调用方**域 `biz` 里声明的接口，实现放在**调用方**域的 `data` 里：

```
app/friend/service/internal/biz/       → type FriendProfileFetcher interface
app/friend/service/internal/data/      → type Adapter 实现它（包住 remote_user.go）
```

调对方服务的 gRPC 客户端就在同一个 `data` 包里，文件名 `remote_<对方>.go`
（`remote_user.go`、`remote_conversation.go`、`remote_message.go`）—— `remote_`
前缀标记它是**跨进程**调用。它只做 gRPC 通道，不碰业务模型，是纯传输层。

## 跨服务契约

有些值是两个服务之间的契约 —— 两边必须算出完全一样的字符串，抄两份字面量
是静默故障。这些统一放在 **`chatroom/pkg/keys`**（只有一份）：

- `MessageChannel(convID)` / `MessageChannelPattern()` —— 消息广播的 Pub/Sub 频道名
- `MemberInvalidatedChannel()` —— 会话成员变更通知频道
- `TokenBlacklistKey(jti)` —— access token 黑名单
- `RefreshTokenKey(jti)` —— refresh token 存储位置

跨域错误原因分两处：域特有的留在 `api/<域>/service/v1/error_reason.proto`，
框架级的（未认证、上游不可用……）是 **`chatroom/pkg/errors`** 里的纯 Go 枚举
（`COMMON_*`）—— 只有一份，因为 reason 字符串也是跨服务契约。

域特有的原因留在各自的 proto 里，比如「token 被拉黑」用 user 域的
`USER_TOKEN_BLACKLISTED`。各服务在自己的 `internal/server` 里通过
`middleware.WithBlacklistedReason(...)` 把原因传进去
（`pkg/middleware` 不认识任何服务的 proto）。

## 内部凭证

**对外接口**用 JWT。access token 15 分钟，refresh token 7 天；refresh token 在 Redis 里
按 jti 存，注销即删；access token 的 jti 进黑名单（`chatroom:token:bl:`），供登出后吊销。

**仅供服务间调用**的端点走内部凭证 `X-Internal-Token`，与 JWT 校验分开：

| 端点 | 调用方 | 用途 |
|---|---|---|
| `/api.user.v1.User/GetFriendProfiles` | friend | 拉好友昵称 / 头像 |
| `/api.user.v1.User/GetConversationMemberProfiles` | conversation | 拉会话成员昵称 / 头像 |
| `/api.conversation.v1.Conversation/CheckMembership` | message | 发消息前确认调用方在不在会里 |
| `/api.conversation.v1.Conversation/UpdateUnread` | message | 增减会话级未读数 |
| `/api.conversation.v1.Conversation/UpdateUnreadBatch` | message | 批量增减会话级未读数 |
| `/api.conversation.v1.Conversation/GetUnread` | message | 查会话级未读数 |
| `/api.conversation.v1.Conversation/ClearUnread` | message | 清零会话级未读数（原子） |
| `/api.conversation.v1.Conversation/ListUnread` | message | 查所有会话的未读数（角标） |
| `/api.conversation.v1.Conversation/ListConversationMembers` | ws-gateway | 算广播目标（网关没有用户身份） |
| `/api.conversation.v1.Conversation/ListUserConversations` | ws-gateway | 用户上下线时找「他在哪些会话里」，好通知同伴 |

前两个 user 端点都跳过 `is_visible_to_stranger` 判定 —— 同会话成员 / 已加好友的人应该互相可见，
陌生人可见性防护不该挡住。

## 降级语义：拉不到 ≠ 不存在

跨服务调用拿不到结果时**必须返 error，不能返空列表 / 空切片**。
有一批接口用「请求了但没回来」判定「用户不存在」，返空会被误判。

| 场景 | 返回 | 上层行为 |
|---|---|---|
| user 服务不可达 / 未配 `internal_secret` | error | 存在性校验**放行** |
| user 服务可达，用户真不存在 | missing 列表 | 拒掉，返回 `CONVERSATION_USER_NOT_FOUND` |
| 批量拉资料时某一片失败 | 整体 error | 只记 warn，列表主体照常返回 |

user 服务的批量端点单次最多收 **200 个 id**，超出部分静默截断。调用方必须
自己分片（`remote_user.go` 的 `batchProfiles`），否则被截断的 id 会被当成「不存在」。

## 熔断：下游不健康时快速失败

下游宕机或变慢时，熔断器直接拒掉，请求快速失败，上游降级逻辑立刻生效。
实现全在 **`chatroom/pkg/breaker`**，接在 5 个跨服务 gRPC 客户端的中间件链**最内层**：

```go
grpc.WithMiddleware(
    authForwarding(),      // 用户 JWT 透传
    internalForwarding(),  // 内部凭证
    oteltrace.Client(),    // trace
    breaker.Client(),      // 熔断 —— 放最内层
)
```

放最内层：被熔断拒绝的请求仍进得了 tracing，trace 里能看到这次拒绝。

默认参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `failureRatio` | 0.5 | 窗口内失败率超过它就熔断 |
| `request` | 20 | 窗口内至少这么多请求才评估 |
| `window` | 10s | 滚动统计窗口（桶宽 1s） |
| `openDuration` | 30s | 熔断打开后多久进半开状态试探 |
| `halfOpenMax` | 1 | 半开状态下放行的试探请求数 |

不用 Kratos 内置的 SRE 熔断器：它窗口只有 3s 且要求窗口内至少 20 个请求才评估，
5 QPS 的服务永远达不到，熔断器等于没有。这里窗口放宽到 10s。

只有 5xx（500 / 503 / 504）算失败：下游返 404「用户不存在」是业务结果。

熔断拒绝时上层拿到的是 **error**（不是空结果）。

## 跨服务调用踩坑

client 里的 endpoint 必须是 `discovery:///chatroom-user.grpc`。
nacos 注册时会给 service name 补 `.grpc` 后缀，少写后缀会报 `produced zero addresses`。

另一个坑：kratos gRPC 客户端**只转发 `transport.RequestHeader()`**。自己往
`outgoing metadata` 里塞的头会被丢掉 —— 内部凭证发不出去，症状是内部端点
一律 403。

`WithMiddleware` 是**赋值不是追加**：调多次后者覆盖前者。只调一次，把要加的
中间件一次列齐。

## Redis key

跨服务共享的 key 前缀和频道名都在 `chatroom/pkg/keys`（唯一权威）：

| key | 用途 |
|---|---|
| `chatroom:user:{uid}` | 用户资料缓存 |
| `chatroom:user:ver:{uid}` | 用户缓存版本号，资料变更时递增 |
| `chatroom:conv:{id}:members` | 会话成员集合 |
| `chatroom:token:bl:{jti}` | access token 黑名单 |
| `chatroom:refresh:{jti}` | refresh token |
| `chatroom:limit:{scope}:{标识}` | 限流令牌桶（hash：`tokens` 余量 + `ts` 时间戳） |
| `chatroom:msg:{convId}` | 消息广播频道（Pub/Sub，不持久化） |
| `chatroom:member:invalidated` | 会话成员变更通知频道 |
| `chatroom:lock:{业务key}` | 互斥锁 |
| `online:{uid}` | 在线状态，值 = 网关实例 id，带 TTL |

缓存是派生数据，MySQL 是权威源。写路径一律「先写库，再删缓存」。

`chatroom:limit:` 前缀下的 scope 见下表：

| scope | 维度 | 默认参数 |
|---|---|---|
| `login:fail` | 用户名 + IP | 容量 5 次失败，15 分钟回充满 |
| `login:locked` | 用户名 + IP | 锁定标记，带 TTL |
| `register` | IP | 容量 10，速率 5 个/分钟 |
| `check_username` | IP | 容量 60，速率 2 个/秒（注册页输入防抖，比注册桶宽松） |
| `friend:apply` | 调用方 uid | 容量 30，速率 30 次/分钟 |
| `friend:accept_reject` | 调用方 uid | 容量 60，速率 60 次/分钟 |
| `friend:remove` | 调用方 uid | 容量 30，速率 30 次/分钟 |
