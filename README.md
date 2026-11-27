# chatroom

一个基于 Kratos v3 的即时通讯后端，按领域拆成五个独立服务。

| 服务 | 职责 | HTTP | gRPC |
|---|---|---|---|
| `chatroom-user` | 注册登录、token、用户资料、账号状态 | 8000 | 9000 |
| `chatroom-friend` | 好友申请 / 接受 / 拒绝 / 移除、好友列表 | 8001 | 9001 |
| `chatroom-conversation` | 单聊 / 群聊、成员增删、成员列表、群主转让 / 解散、未读数 | 8002 | 9002 |
| `chatroom-message` | 消息落库、历史分页、已读 / 未读 | 8003 | 9003 |
| `chatroom-ws-gateway` | WebSocket 长连接、在线状态、消息广播 | 8004 | — |

五个进程共用 MySQL、Redis、Nacos，端口刻意错开，便于本地同时启动。

## 快速开始

跑起来只需要三步：起底座、起服务、验证。

**1. 起底座**（MySQL 3306 + Redis 6379 + Nacos 8848 + Jaeger）。本机已有原生
Redis/Nacos 就别起，端口会冲突：

```bash
docker compose -f app/user/service/deploy/docker-compose.yaml up -d
docker compose -f app/user/service/deploy/docker-compose.yaml down   # 停掉
```

**2. 起服务**。整仓**一个** Go module，但每个服务要在**自己的目录**里跑：

```bash
make run-all       # 仓库根跑，五个服务全起（后台）
make stop-all      # 停掉
```

`run-all` 按依赖顺序逐个起：`user` → `conversation` → `message` → `ws-gateway` → `friend`。
后几个要经 nacos discovery 调前面的服务，先起被调方。每个服务之间隔 1s 让 nacos
注册先生效。

单独起一个服务（前台，日志直接打在屏幕上）：

```bash
cd app/user/service && make run
```

`run-all` 起的日志在 `/tmp/chatroom-<服务>.log`。

**3. 验证**：

```bash
curl http://127.0.0.1:8000/healthz     # liveness，只要进程活着就 200
curl http://127.0.0.1:8000/readyz      # readiness，探依赖，硬依赖挂了 503
```

工具链（wire / kratos CLI）：

```bash
go install github.com/google/wire/cmd/wire@latest
go install github.com/go-kratos/kratos/cmd/kratos/v3@latest
```

protoc 插件不用单独装 —— `api/gen.sh` / `app/gen-conf.sh` 会把钉好版本的
插件编到 `.cache/protoc-plugins/`。

> **手工 `go run` 时必须带全环境变量**，否则 nacos 鉴权失败，服务启动就 panic
> （报 `Code: 401, Message: User not found!` —— 这是 nacos 登录失败，不是业务用户不存在）。
> `make run` 已经带好了；手工起就照抄 `pkg/tools/dev-env.mk` 里的 `COMMON_ENV`。
>
> 别用 `set -a; . pkg/tools/dev-env.mk` 导环境变量 —— 那是 Make 语法，
> `?=` 不是合法 shell，source 会静默失败，`NACOS_USERNAME`/`NACOS_PASSWORD` 是空的
> → 401 panic。要手工导就逐个 `export`。

### 环境变量

下表是本地开发用得到的全部变量。默认值就是 `configs/config.yaml` 里的占位符。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `NACOS_SERVERS` | `127.0.0.1:8848` | 配置中心 + 注册中心 |
| `NACOS_NAMESPACE` | `3dbbf765-...` | 命名空间 id |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | `nacos` / `123456` | nacos 账号 |
| `DB_SOURCE` | `root:123456@tcp(127.0.0.1:3306)/chat?...` | MySQL DSN |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis |
| `AUTH_JWT_SECRET` | dev 占位 | JWT 签名密钥 |
| `AUTH_INTERNAL_SECRET` | dev 占位 | 服务间内部调用凭证，**五个服务必须一致** |
| `GATEWAY_HTTP_ADDR` | `0.0.0.0:8004` | 网关监听地址。多实例时每个实例给不同端口 |
| `GATEWAY_INSTANCE_ID` | hostname | 网关实例 id。多实例时必须错开，否则在线状态互相覆盖 |

## 架构概览

这一节讲代码怎么组织：三层怎么分、目录怎么摆、服务之间怎么依赖。

### 技术栈

下面这套组合是本项目的全部依赖面。

- **Kratos v3** —— 三层分层（service / biz / data）+ Wire 依赖注入
- **Protobuf** —— API 先行，`api/<域>/service/v1/` 定义，生成 gRPC / HTTP / OpenAPI
- **ent v0.14.6** —— ORM，每个服务各有一份 schema（`app/<服务>/service/internal/data/ent/schema/`）
- **Redis** —— 缓存、限频、token 黑名单、Pub/Sub 广播、在线状态
- **Nacos 3.x** —— 服务注册发现 + 配置中心
- **JWT** —— 双 token（access / refresh），中间件鉴权
- **gorilla/websocket** —— WebSocket 长连接

### 分层约定

三种模型形状穿过三层。`biz` 拥有 DO，`data` 拥有 PO，`service` 只做 DTO ↔ DO 转换。

```
   client ──► DTO ──► service ──► DO ──► biz ──► DO ──► data ──► PO ──► storage
                                  ▲                ▲
                                  │ declares       │ implements
                                  └─── repo IF ────┘
```

| 层 | 拥有 | 边界上说什么 | 绝不说 |
|---|---|---|---|
| service | — | DTO ↔ DO | PO、存储客户端 |
| biz | DO、usecase、repo 接口 | DO | DTO、PO、存储客户端 |
| data | PO、repo 实现 | DO ↔ PO | DTO |

- `service` 依赖**契约包** 和 `biz`，**不依赖** `data`
- `biz` 只为错误码枚举依赖**契约包**，**不依赖** `service` / `data`
- `data` 依赖 `biz` 来实现 repo 接口，**不依赖** `service`
- `cmd` 是唯一用 Wire 把三层接起来的地方

新增一个资源时的顺序：定义 DTO → 建 DO + repo 接口 → 实现 repo → Wire 接线和注册服务 → `make all`（在服务目录里跑）。

> 依赖方向的完整解释（「为什么 service 不依赖 data」「跨域接口放哪」）见
> [docs/architecture.md](docs/architecture.md)。

### 目录结构

整仓是**一个** Go module（`module chatroom`），一个 `go.mod`，没有 `replace`
也没有 `go.work`。目录按官方 [go-kratos/beer-shop](https://github.com/go-kratos/beer-shop)
的思路分：`api/` 放契约，`app/` 放微服务。

```
chatroom/
  Makefile            **仓库根编排**：api / build / test / vet / config / proto / wire / all
  go.mod              module chatroom —— 整仓一份
  third_party/        第三方 proto 的唯一副本（全仓库一份）
    google/api/         annotations.proto + http.proto（离线依赖）
  api/                **契约** —— 每个业务域一个，只有 proto + 生成的桩代码
    gen.sh              契约生成脚本（protoc，从仓库根跑）
    user/service/        chatroom/api/user/service/v1
      v1/                proto + 生成的 grpc / http stub
      openapi.yaml       生成产物
      Makefile           api
    friend/service/
    conversation/service/
    message/service/
  app/                **微服务** —— 一个业务域一个可部署服务
    gen-conf.sh        配置 proto 生成脚本（protoc，从仓库根跑）
    user/service/
      cmd/server/        入口 + Wire 注入
      internal/conf/     本服务自己的配置 proto + 生成代码
        conf.proto       配置 proto（五个服务里文件名都一样）
        conf.pb.go       生成产物
      internal/service/  传输层适配，一个资源一个文件
      internal/server/   HTTP / gRPC server 构造 + 路由注册
        server.go        ProviderSet、内部端点清单
        http.go          NewHTTPServer
        grpc.go          NewGRPCServer
      internal/biz/      领域模型、usecase、repo 接口、错误
      internal/data/     repo 实现、存储客户端、ent schema
        remote_<svc>.go  调其它服务的 gRPC 客户端（跨进程）
        health.go        依赖探测：硬 / 软依赖判定
      internal/observability/ 本服务的观测配置翻译层（conf → pkg/observability.Config）
      configs/bootstrap.yaml  引导配置：连哪个 nacos、哪条 dataId
      configs/config.yaml     本服务的配置（原样推进 nacos）
      Dockerfile         本服务的镜像（构建上下文 = **仓库根目录**，因为依赖同仓的 pkg/ 和 api/）
      Makefile           服务级 target：build / run / test / config / docker-build
    friend/service/
    conversation/service/
    message/service/
    ws-gateway/service/    WebSocket 网关：连接池、心跳、协议编解码、广播
                           **没有对应的 api/** —— 它不对外暴露业务契约
  pkg/                **跨服务共享的公共包**（同一个 module 下的普通目录）
    nacos/       Nacos client + 注册/发现 + 配置源
    keys/        Redis key / Pub-Sub 频道名（跨服务契约的权威）
    errors/      框架级错误原因 COMMON_*（跨服务契约的权威）
    health/      健康检查（liveness / readiness，跨服务统一语义）
    middleware/  JWT + 内部凭证 + 黑名单中间件
    breaker/     跨服务 gRPC 调用的熔断器
    dbpool/      database/sql 连接池调优
    observability/ Metrics + tracing 中间件
    testutil/    测试辅助（DB / Redis / JWT）
    tools/       构建辅助（dev-env.mk / nacos-config.sh）
  docs/             本 README 的展开文档
```

公共代码抽在 `pkg/` 是因为这些是横切关注点，必须所有服务一致。放在 `pkg/` 之后
由编译器保证一致。`pkg` 不依赖任何服务，依赖方向是单向的：服务 → pkg。

### 依赖的是契约，不是服务

跨服务只 import 对方的**契约包**（`api/<域>/service/v1`），不 import 对方的
`internal/`。契约单独放在 `api/`，所以 user 服务换 ORM、拆表、改内部结构时，
friend 的代码一个字都不用动。

依赖方向永远是**服务 → 契约 → 无**：

- `pkg` 不反向依赖任何服务。
- 契约之间**互不依赖**（每个域的 proto 不 import 别的域的 proto）。

单个 module 意味着这些依赖是**同一个仓库内的 import path**，不需要 `replace`，
本地改完契约立刻生效。要把某个域拆成独立仓库，只需把 `api/<域>` 抽成单独的
module 并改版本号 —— import 路径和依赖箭头不变。

每个进程只注册自己域的路由。`chatroom-user` 在 :8000 响应 `/api/user/*`，
对 `/api/friend/*` 返回 404。跨服务的端点（如 user 的 `GetFriendProfiles`）
走 `X-Internal-Token` 内部凭证，且在 `internal/server` 的 `InternalOps` 里登记。

## 核心约定

这一节是跨服务、跨文件都必须一致的约定 —— 违反它们不会编译报错，只会在运行时静默出错。

### 用户标识（uid）

用户标识是**字符串 UUIDv7**，不是自增整数。

```
01a19d58-f992-7ca9-807e-dc1cb20c5ef7
```

- **定宽 36 字符**，DB 列类型 `CHAR(36)`。
- **UUIDv7**：前 48 位是毫秒时间戳，字符串字典序 = 生成时间序。代码里需要一致全序的地方
  （单聊 `pair_key` 归一化、成员列表排序）直接按字符串排。
- **不可枚举**：自增 id 会被遍历，uid 不会。
- **不透明**：客户端不解析、不依赖其结构。

JWT 的 `sub` claim 存的就是 uid 字符串。

> **uid ≠ 记录主键。** `users.id` 是 uid（字符串），但 `friends.id`、`conversations.id`、
> `messages.id` 仍是自增整数 —— 那是记录标识，只在本服务内部用，不跨服务传。
> 跨服务传的永远是 uid。

「不存在」的哨兵值是空串 `""`，不是 `0`。

### 认证与内部凭证

对外接口用 JWT，服务间调用走另一套内部凭证 —— 两套校验完全分开。

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

### 跨服务契约

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

### 降级与熔断

跨服务调用拿不到结果时**必须返 error，不能返空列表 / 空切片**。
有一批接口用「请求了但没回来」判定「用户不存在」，返空会被误判。

| 场景 | 返回 | 上层行为 |
|---|---|---|
| user 服务不可达 / 未配 `internal_secret` | error | 存在性校验**放行** |
| user 服务可达，用户真不存在 | missing 列表 | 拒掉，返回 `CONVERSATION_USER_NOT_FOUND` |
| 批量拉资料时某一片失败 | 整体 error | 只记 warn，列表主体照常返回 |

user 服务的批量端点单次最多收 **200 个 id**，超出部分静默截断。调用方必须
自己分片（`remote_user.go` 的 `batchProfiles`），否则被截断的 id 会被当成「不存在」。

下游宕机或变慢时，熔断器直接拒掉，请求快速失败，上游降级逻辑立刻生效。
实现全在 **`chatroom/pkg/breaker`**，接在跨服务 gRPC 客户端的中间件链**最内层**：

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

只有 5xx（500 / 503 / 504）算失败：下游返 404「用户不存在」是业务结果。
熔断拒绝时上层拿到的是 **error**（不是空结果）。

> 完整的降级契约、熔断参数理由、内部凭证清单和跨服务踩坑见
> [docs/cross-service.md](docs/cross-service.md)。

## 各服务要点

这一节讲每个服务自己的业务规则 —— 不看代码就猜不到的那些。

### 会话服务的群管理规则

几条不看代码就猜不到的规则：

- **单聊不能退群**。单聊只有两个成员，退群会让另一个人落单，接口直接返回
  `CONVERSATION_NOT_GROUP`。单聊也没有群主，所以没有转让 / 解散。
- **群主不能退群**，得先转让群主（`TransferOwner`）或解散（`DissolveConversation`）。
  新群主必须是群成员，否则返 `CONVERSATION_NOT_FOUND`（不泄露会话是否存在）。
- **非成员访问群详情按「不存在」处理**（`CONVERSATION_NOT_FOUND`），避免被探测出
  「这个会话存在但你不在线」。只有列成员（`ListMembers`）返 `CONVERSATION_NOT_MEMBER`。
- **加成员会校验用户真实存在**，不存在的 uid 返 `CONVERSATION_USER_NOT_FOUND` ——
  否则会留下一个永远没有昵称头像的幽灵成员。user 服务不可达时放行（降级为查库）。
- **成员上限**由 `biz.MaxGroupMembers` 控制，biz 层和 repo 层各挡一次。

### 群头像

群头像跟个人头像是两套独立的东西，但共用 `pkg/oss` 的同一份上传抽象。

| | 个人头像 | 群头像 |
|---|---|---|
| 签约服务 | user | conversation（自己签，不绕 user） |
| object 前缀 | `avatar/<uid>/` | `group_avatar/<会话ID>/` |
| 谁能改 | 本人 | 只有群主 |
| 存储列 | `users.avatar` | `conversations.avatar` |

流程跟个人头像一样，走 PostObject 直传 —— 图片字节不经过自己的服务器：

1. 客户端调 `GetGroupAvatarUploadToken`（`POST /api/conversation/avatar/upload_token`）
   拿凭证。签发前校验「是群聊 + 调用方是群主」。
2. 客户端把 `fields` 原样塞进 FormData，文件放在**最后一个**域，POST 到 `host`。
3. 客户端把返回的 `public_url` 塞进 `UpdateConversation` 的 `avatar`。

服务端不碰文件字节，但要验三道关 —— 都在 `pkg/oss.Uploader.ValidateURL` 里：

- **host / 目录前缀 / 后缀**要在白名单内。前缀锁死在 `group_avatar/<会话ID>/`，
  客户端拿着临时凭证也写不进别的群目录。
- **对象必须真的存在**，且大小和 MIME 类型符合 policy —— 只查格式的话，
  别人传个外链就能把群头像指到任意地方。
- **后缀白名单 fail closed**：没配白名单就是「不允许任何后缀」，不是「允许全部」。

`UpdateConversation` 的 `name` 和 `avatar` 都留空 = 无操作，返
`CONVERSATION_INVALID_ARGUMENT`。留空表示「不改」，所以改头像时传空串当名字即可。

### 上传对象的缓存策略

个人头像、群头像、聊天图片三类上传走同一套 PostObject 直传，
**都会给对象带上 `Cache-Control: public, max-age=31536000, immutable`**。

敢声明 immutable 是因为 object key 每次上传都是新的（`pkg/oss` 里的 `newObjectID`），
**同一个地址永远对应同一份内容**。不设这个头的话 OSS 不返回 `Cache-Control`，
缓存策略由浏览器自己决定，实际效果是每次都回源校验一遍。

这个字段**必须同时出现在 policy 的 conditions 和表单域里**，OSS 两边都会校验：

- policy 里有、表单域里没有 → 拒（`InvalidArgument`）
- 表单域里有、policy 里没有 → 也拒

两边都由 `pkg/oss.PostUploadForm` 生成（`GeneratePostUploadForm` 写 policy，
`Fields()` 出表单域），**别在别处自己拼一份字段清单** —— 少一个字段
上传全挂，而且报错信息跟 Cache-Control 没关系，很难查。

客户端要把 `fields` 原样提交（chatroom-web 就是整个 FormData 抄过去）。
注意：**老的客户端如果丢掉不认识的字段，上传会失败** —— policy 要求这个字段存在。

### 消息服务的规则

消息是本项目最复杂的域，几条规则不看代码就猜不到。

- **历史消息用游标分页，不用 OFFSET**。游标是「上一页最后一条消息的 id」，
  下一条查 `id < cursor`。复合索引 `(conversation_id, id)` 覆盖过滤 + 排序，不回表。
- **「拉更旧的」和「拉更新的」是两个方向，要两个端点**。
  `ListMessages`（`/api/message/list`）**倒序**翻历史，游标是「比这个 id 更旧的」；
  断线重连补消息要「比这个 id 更新的」，方向相反，所以单开
  `ListMessagesSince`（`/api/message/since`）按 id **正序**返回。网关的 `resume` 帧走它。
- **发消息要校验成员身份**，下游不可达时**拒绝**而不是放行。成员校验能力缺失
  （接线配错）也拒绝 —— 放行等于消息服务完全不做鉴权。
- **消息内容存 TEXT，不是 VARCHAR**。ent 的 `field.String` 不带 `MaxLen` 时默认映射成
  `varchar(255)`，85 字的中文消息就存不下。要显式写
  `SchemaType(map[string]string{"mysql": "text"})`。
- **未读数存在 conversation 服务的成员表上**（`conversation_members.unread_count`），
  message 服务经内部凭证增减。
- **发消息后给除发送方外的所有成员加一条未读**。加未读失败只记日志，不影响发消息结果。
- **未读数清零走独立的 `ClearUnread` 端点**，服务端一条 UPDATE 清零。
  别让调用方「先 GetUnread 再 UpdateUnread(-n)」—— 两步之间有窗口，并发到达的新消息会被吞掉。
- **未读数下游不可达返 503**，不是 500。
- **查单个会话的未读数也要先验成员身份**，否则能改 `conversation_id` 探测别人会话。
- **消息内容上限** `biz.MaxMessageContentLen`（按 rune 数算，不是字节）。

### WebSocket 网关

网关**无状态**：不落库，只维护连接 + 转发。端点 `ws://host:8004/ws?token={JWT}`。
token 放在 query 里而不是 header —— 浏览器的 WebSocket API 不能自定义 header。

上行 / 下行帧的完整 JSON 见 [docs/ws-gateway.md](docs/ws-gateway.md#协议)，
这里只列最常用的三种帧：

| 帧 | 方向 | 语义 |
|---|---|---|
| `chat` | 上行 | 发消息。网关转发到 message 服务落库，成功后回 `ack` |
| `status` | 上行 | 查在线状态。带 `user_id` 查一个人，带 `conversation_id` 查整个会话（回 `presence` 帧） |
| `resume` | 上行 | 断线重连补消息。带 `last_message_id`，网关调 `ListMessagesSince` 按**正序**补回来 |

消息本身**不**通过发送方那条连接推回 —— 它走 Redis Pub/Sub 广播回来，
所有连接（含发送方自己的其它端）都收到，路径统一：

```
客户端A ──ws──► ws-gateway ──gRPC──► message 服务 ──► MySQL 落库
                    ▲                                    │
                    │                                    ▼
              Redis Pub/Sub ◄─── chatroom:msg:{convId} ─┘
                    │
                    └──► 目标成员在本实例上的连接 ──► 客户端B
```

在线状态存在 Redis 里，所以**跨实例可见**。多实例部署时**端口和实例 id 都要错开**
（`GATEWAY_HTTP_ADDR` / `GATEWAY_INSTANCE_ID`），否则在线状态互相覆盖。

网关是**唯一没有业务鉴权兜底的入口**，握手上锁了三道闸门（先限流再验 token）：

| 闸门 | 环境变量 | 默认 | 拒的时候 |
|---|---|---|---|
| 单 IP 握手限流（令牌桶） | `GATEWAY_HANDSHAKE_RATE` / `GATEWAY_HANDSHAKE_BURST` | 20 次/秒，突发 40 | **429** |
| 本实例连接数上限 | `GATEWAY_MAX_CONNECTIONS` | 5000 | **503** |
| 单用户连接数上限 | `GATEWAY_MAX_CONNS_PER_USER` | 5 | **429** |

> 协议帧全表、消息流转细节、多实例部署、准入限制和几条规则见
> [docs/ws-gateway.md](docs/ws-gateway.md)。

### 限流

限流分两类，机制不同，别混：

| 类型 | 机制 | 状态存哪 | 用在哪 |
|---|---|---|---|
| **业务限流** | Redis + Lua 令牌桶 | Redis（全集群共享） | 登录失败锁、注册、好友操作 |
| **准入限流** | 内存令牌桶 | 进程内存 | WS 握手、连接数配额 |

两种都用**令牌桶**：桶容量 `capacity`，每秒补 `rate` 个令牌。
用 Lua 脚本做「读余量 → 扣减 → 写回」，Redis 单线程保证原子。

业务限流在 **biz 层显式调用**，不在中间件层 —— 限流维度是「调用方身份」
（用户名 + IP、caller uid），不是「端点」。

| 场景 | 维度 | 默认参数 | 超了返回 |
|---|---|---|---|
| 登录失败 | 用户名 + IP | 容量 5 次，15 分钟回充满 | `USER_LOGIN_LOCKED` (429) |
| 注册 | IP | 容量 10，速率 5 个/分钟 | `USER_RATE_LIMITED` (429) |
| 用户名校验 | IP | 容量 60，速率 2 个/秒 | `USER_RATE_LIMITED` (429) |
| 好友申请 | 调用方 uid | 容量 30，速率 30 次/分钟 | `FRIEND_RATE_LIMITED` (429) |
| 好友同意/拒绝 | 调用方 uid | 容量 60，速率 60 次/分钟 | `FRIEND_RATE_LIMITED` (429) |
| 删好友 | 调用方 uid | 容量 30，速率 30 次/分钟 | `FRIEND_RATE_LIMITED` (429) |

参数在 `configs/config.yaml` 的 `rate_limit` / `rate_limits` 段里调。

Redis 不可达时**放行**，不拒绝。唯一例外是 ws-gateway 的连接数配额 ——
配额在内存里，Redis 挂了它照样工作。

## 配置与运维

配置按 **file < nacos < env** 合并，每个服务读**两个**文件：`bootstrap.yaml`
（连哪个 nacos，**不进** nacos）和 `config.yaml`（业务参数，**进** nacos）。
拆开是为了断掉循环依赖 —— nacos 上会存着自己地址的一份拷贝，那份陈旧的拷贝会悄悄
盖掉本地刚改的新值。

nacos 上改完配置**不会热更新** —— 服务只在启动时读一次配置，改完要重启。

| 端点 | 语义 | 探什么 | 失败后果 |
|---|---|---|---|
| `/healthz` | **liveness** | 只看进程是否活着，**不碰任何依赖** | 依赖挂了也**不能**失败：重启 pod 解决不了 MySQL 宕机 |
| `/readyz` | **readiness** | 关键依赖 | 硬依赖挂了返 **503**，K8s 把这个 pod 从 endpoint 里摘掉 |

硬软依赖只有一个判据：**这个依赖挂了，服务还能不能正确处理请求**。
MySQL 对四个业务服务是硬依赖、Redis 是软依赖；Redis 对 ws-gateway 是硬依赖
（Pub/Sub 是消息投递到客户端的唯一通道）。

数据库连接池的默认值在生产上不安全，每个服务都显式配了 `max_open_conns` /
`max_idle_conns` / `conn_max_lifetime` / `conn_max_idle_time`（兜底在 `pkg/dbpool`）。
⚠️ 连接额度是**所有服务共享**的：5 服务 × 10 副本 × 10 连接 = 500 条，
MySQL 的 `max_connections` 要配得比这大（默认 151 不够）。

metrics 和 trace 都**默认关闭**，用 `METRICS_ADDR` / `OTEL_EXPORTER_OTLP_ENDPOINT` 打开；
空值表示「关」，不是「用默认地址」（本地五个服务同时起会撞端口）。

> 配置加载顺序、环境变量全表、探针配置、可观测性细节、连接池参数和部署流程见
> [docs/ops.md](docs/ops.md)。生产部署手册见 [deploy/README.md](deploy/README.md)。

## 开发指南

这一节是日常改代码要用的流程 —— 改表、生成、测试。

### 表结构变更

**权威是 ent schema**（每个服务各有一份，在 `app/<服务>/service/internal/data/ent/schema/`）。
改完 schema 重启对应服务，ent 会比对现状，只发必要的 `ALTER TABLE` 把库对齐到 schema。

```bash
vim app/<服务>/service/internal/data/ent/schema/xxx.go
make -C app/<服务>/service run   # 重启即生效
```

注意：**ent 不删列** —— 废弃的列要手工 `ALTER TABLE ... DROP COLUMN`。
**不建外键约束**，引用完整性靠应用层保证。

> 多副本下的并发 DDL（auto-migrate 每个进程启动时各自执行）、单聊唯一性、
> `migrations/*.sql` 的用途，见 [docs/schema.md](docs/schema.md)。

### 代码生成

`*.pb.go`、`*_grpc.pb.go`、`*_http.pb.go`、`wire_gen.go` 都是生成物，**不要手改**。

```bash
# 契约：生成 grpc / http stub + openapi.yaml
cd api/user/service && make api

# 配置 + Wire（在服务目录里跑）
cd app/user/service && make all
```

仓库根聚合所有服务和契约：

```bash
make api     # 四个契约包重新生成
make config  # 五个服务的 conf proto
make proto   # 契约 + 各服务的 conf proto
make all     # 契约 + conf proto + Wire + go mod tidy
make build   # 五个服务
make test    # 五个服务的单元测试
```

> 生成脚本的 proto_path 约定、kratos CLI 生成 service 骨架的用法，见
> [docs/codegen.md](docs/codegen.md)。

### 测试

分层测试：service 测试 fake repo 并在 fake 之上建真的 usecase，biz 测试 fake repo，
data 测试直接打真实存储。gateway 包的测试起真实的 `httptest` 服务器 + 真实
WebSocket 客户端，Redis 相关的用例（订阅 / 在线状态）在 `CHATROOM_TEST_REDIS_ADDR`
不可达时 skip。

集成测试每个用例自建临时库 `chatroom_test_<纳秒>`、跑完自动 DROP，不碰共享的 `test` 库。
底层是 `CHATROOM_TEST_ROOT_DSN` 环境变量，未设置时这些用例会 skip。

公共测试辅助在 `chatroom/pkg/testutil/`（各服务共用一份）：

| 辅助 | 作用 |
|---|---|
| `testutil.NewDB(t)` | 建名字唯一的临时库，跑调用方的 `Migrate`，结束时 DROP |
| `testutil.NewRedis(t)` | 连 DB 15 的客户端，不可达就 skip |
| `testutil.AuthedCtx(t, uid)` | 带有效 JWT 的 context |

各域的 data 层测试在自己的 `app/<域>/service/internal/data/` 下，用该域的
`newTestDB(t)`（见 `testsupport_test.go`）包住 `testutil.NewDB`。

跨域用例（拉 user 服务的资料等）放在**调用方**域的 data 包里，
把 gRPC client 换成内存替身 —— 传输层跳过，但走的是生产 adapter 路径。

> **Redis Pub/Sub 不按 DB 隔离** —— DB 14 上的订阅照样收得到 DB 15 上发的消息。
> Pub/Sub 的测试隔离靠**频道名**，不靠换 DB。

> **gorilla/websocket 的读超时是不可恢复的**。一旦 `SetReadDeadline` 到点，
> 连接就永久废了 —— 后续 `ReadMessage` 立刻返回同一个超时错误，重新设更长的
> deadline 也没用。所以客户端**不能**用「设个短超时读一帧」等新帧，那会把连接毒死。
>
> 正确姿势是「每条连接一个后台读协程 + channel」—— 读阻塞在 channel 上，
> 不用 deadline，超时也不影响连接。心跳用 pong。

### 常用命令

每个服务在自己的目录里跑：

```bash
cd app/user/service
make build             # 编译到 app/user/service/bin/chatroom-user
make vet
make test              # 单元测试，不碰外部依赖
make test-integration  # data 层 repo + 跨域链路，需要 MySQL
make clean
```

五个服务 + 公共 module 全跑一遍（仓库根）：

```bash
make build
make test
```

## 文档索引

| 文档 | 内容 |
|---|---|
| [CLAUDE.md](CLAUDE.md) | 分层契约与跨服务语义 —— **改模板前必读** |
| [docs/architecture.md](docs/architecture.md) | 分层约定的完整解释、依赖方向、目录结构详解、Protobuf 注册表 |
| [docs/cross-service.md](docs/cross-service.md) | 跨服务调用、契约、降级、熔断、Redis key、踩坑 |
| [docs/ws-gateway.md](docs/ws-gateway.md) | WebSocket 网关协议帧全表、消息流转、多实例、准入限制 |
| [docs/schema.md](docs/schema.md) | 表结构变更、ent auto-migrate、并发 DDL、单聊唯一性 |
| [docs/codegen.md](docs/codegen.md) | 代码生成约定、protoc proto_path、kratos CLI |
| [docs/ops.md](docs/ops.md) | 配置加载顺序、健康检查、可观测性、连接池、部署 |
| [deploy/README.md](deploy/README.md) | 生产部署手册（compose / K8s / CI） |
