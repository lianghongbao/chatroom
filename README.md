# chatroom

一个基于 Kratos v3 的即时通讯后端，按领域拆成五个独立服务：

| 服务 | 职责 | HTTP | gRPC |
|---|---|---|---|
| `chatroom-user` | 注册登录、token、用户资料、账号状态 | 8000 | 9000 |
| `chatroom-friend` | 好友申请 / 接受 / 拒绝 / 移除、好友列表 | 8001 | 9001 |
| `chatroom-conversation` | 单聊 / 群聊、成员增删、成员列表、群主转让 / 解散、未读数 | 8002 | 9002 |
| `chatroom-message` | 消息落库、历史分页、已读 / 未读 | 8003 | 9003 |
| `chatroom-ws-gateway` | WebSocket 长连接、在线状态、消息广播 | 8004 | — |

五个进程共用 MySQL、Redis、Nacos，端口刻意错开，便于本地同时启动。

## 技术栈

- **Kratos v3** —— 三层分层（service / biz / data）+ Wire 依赖注入
- **Protobuf** —— API 先行，`api/<域>/service/v1/` 定义，生成 gRPC / HTTP / OpenAPI
- **ent v0.14.6** —— ORM，每个服务各有一份 schema（`app/<服务>/service/internal/data/ent/schema/`）
- **Redis** —— 缓存、限频、token 黑名单、Pub/Sub 广播、在线状态
- **Nacos 3.x** —— 服务注册发现 + 配置中心
- **JWT** —— 双 token（access / refresh），中间件鉴权
- **gorilla/websocket** —— WebSocket 长连接

## 分层约定

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

### 跨服务调用

一个服务不 import 另一个服务的 `internal/`（Go 的 `internal/` 可见性规则在编译期就挡住了）。
跨域调用走**调用方**域 `biz` 里声明的接口，实现放在**调用方**域的 `data` 里：

```
app/friend/service/internal/biz/       → type FriendProfileFetcher interface
app/friend/service/internal/data/      → type Adapter 实现它（包住 remote_user.go）
```

调对方服务的 gRPC 客户端就在同一个 `data` 包里，文件名 `remote_<对方>.go`
（`remote_user.go`、`remote_conversation.go`、`remote_message.go`）—— `remote_`
前缀标记它是**跨进程**调用。它只做 gRPC 通道，不碰业务模型，是纯传输层。

### 跨服务契约

有些值是两个服务之间的契约 —— 两边必须算出完全一样的字符串。抄两份字面量
是静默故障：消息发到没人订阅的频道，或者 token 拉黑了但中间件读的是另一个
key。这些统一放在 **`chatroom/pkg/keys`**（只有一份）：

- `MessageChannel(convID)` / `MessageChannelPattern()` —— 消息广播的 Pub/Sub 频道名
- `MemberInvalidatedChannel()` —— 会话成员变更通知频道
- `TokenBlacklistKey(jti)` —— access token 黑名单
- `RefreshTokenKey(jti)` —— refresh token 存储位置

> **为什么只有一份**：这些曾经是每个服务 `internal/utils` 里的五份副本，
> 而且**已经漂移成三个版本** —— conversation 和 ws-gateway 的副本多了
> `MemberInvalidatedChannel`，另外三个服务没有。发布方和订阅方各自算各自的，
> 频道名对不上就是消息静默丢失（不报错，只是对方永远收不到）。
> 抽到 `chatroom/pkg` 之后由编译器保证一致。

跨域错误原因分两处：域特有的留在 `api/<域>/service/v1/error_reason.proto`（对外契约），
框架级的（未认证、上游不可用……）是 **`chatroom/pkg/errors`** 里的
纯 Go 枚举（`COMMON_*`，`type ErrorReason int32`）—— 只有一份，
因为 reason 字符串是跨服务契约（调用方靠它判断「该重试」还是「该报错」）。
`pkg/errors` 直接持有这份枚举并提供 `Reason()` 辅助。

域特有的原因仍然留在各自的 proto 里，比如「token 被拉黑」用的是
user 域的 `USER_TOKEN_BLACKLISTED` —— 拉黑动作发生在 user 服务，
但每个服务都要查黑名单，所以各服务在自己的 `internal/server` 里
通过 `middleware.WithBlacklistedReason(...)` 把原因传进去
（`pkg/middleware` 不认识任何服务的 proto）。

### 服务边界

每个进程只注册自己域的路由。`chatroom-user` 在 :8000 响应 `/api/user/*`，
对 `/api/friend/*` 返回 404。跨服务的端点（如 user 的 `GetFriendProfiles`）
走 `X-Internal-Token` 内部凭证，且在 `internal/server` 的 `InternalOps` 里登记。

## 目录结构

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
```

公共代码抽在 `pkg/` 是有意的：**这些是横切关注点，必须所有服务一致**。
之前它们是各服务 `internal/` 里的五份副本，已经漂移过 ——
`internal/observability` 的兜底服务名五份全是 `"chatroom-user"`，
四个服务的 trace 会认成同一个服务。放在 `pkg/` 之后由编译器保证一致。

`pkg` 不依赖任何服务（`pkg/` 下没有任何 `app/` 的 import），依赖方向是
单向的：服务 → pkg。

### 跨服务依赖怎么走

一个服务不 import 另一个服务的 `internal/` —— Go 的 `internal/` 可见性规则
在编译期就挡住了。跨服务只 import 对方的**契约包**（`api/<域>/service/v1`），
那是公开契约。

### 依赖的是契约，不是服务

这是微服务「独立演进」能不能落地的前提。调用方需要的是「user 域对外的
契约」，不是「user 服务的实现」。所以契约单独放在 `api/`：

```
chatroom/api/user/service/v1     契约：proto + 生成的 grpc / http stub。没有实现。
chatroom/app/user/service        实现：biz / data / service。依赖上面的契约包。
```

`app/friend/service` 依赖 `chatroom/api/user/service/v1`，**不依赖**
`chatroom/app/user/service`。带来的直接好处：

- user 服务换 ORM、拆表、改内部结构 → friend 的代码一个字都不用动。
- 契约没变的情况下，user 服务重新部署，friend 连重新编译都不需要。

> 之前契约就躺在服务目录里（`<服务>/api/<域>/v1/`），调用方直接 import
> 整个服务包。编译期能过，但代价是**调用方把对方的实现也拖进了依赖图**：
> user 服务加一个第三方库、改一个内部包名，friend 的构建就跟着抖。
> 契约独立成包之后，依赖图上只剩「服务 → 契约」这一条边。

依赖方向永远是**服务 → 契约 → 无**：

- `pkg` 不反向依赖任何服务 —— 否则 `pkg` 一改，所有服务都得跟着改。
- 契约之间**互不依赖**（每个域的 proto 不 import 别的域的 proto）——
  否则 user 的契约一变，friend 连编译都要重跑。

单个 module 意味着这些依赖是**同一个仓库内的 import path**，不需要
`replace`，本地改完契约立刻生效。要把某个域拆成独立仓库时，只需要把
`api/<域>` 抽成单独的 module 并改版本号 —— import 路径和依赖箭头不变。

每个服务有**两份**本地配置，按「这个值该由谁决定」分：

| 文件 | 装什么 | 进 nacos 吗 |
|------|--------|------------|
| `app/<服务>/service/configs/bootstrap.yaml` | `registry.nacos` —— 连哪个 nacos、哪条 dataId | **不进** |
| `app/<服务>/service/configs/config.yaml` | `server` / `data` / `auth` / `observability` | **进，原样推** |

拆开是为了断掉一个循环依赖：业务配置就放在 nacos 上，所以「连哪个 nacos」
必须在读它之前就知道。两者放一个文件，nacos 上就会存着自己地址的一份拷贝，
那份陈旧的拷贝会悄悄盖掉本地刚改的新值。

运行时默认路径是**相对服务目录**的，`make run` 直接用默认值；Docker 里用
`-conf /data/conf/config.yaml -bootstrap /data/conf/bootstrap.yaml` 覆盖。

配置值按 **file < nacos < env** 合并（后面的赢）：

- **file** —— `app/<服务>/service/configs/config.yaml`，业务默认值 + `${ENV}` 占位符
- **nacos** —— `chatroom-<服务>.yaml`，各环境的运行时参数
- **env** —— 密钥、DSN 等部署时注入的东西

yaml 里的 `${VAR}` 由 Kratos 拿合并后的 map 解析，所以本地文件写的占位符
照样生效。`bootstrap.yaml` 不参与这个合并 —— 它在单独的第一遍里被读，用来
定位 nacos。

本地文件和 nacos 那份是同一份配置的两份拷贝，用工具同步（每个服务一个脚本，
只管自己那份）：

```bash
cd app/user/service
make nacos-config          # 对比本地和 nacos，有差异时退出码非 0
make nacos-config-push     # 把本地推到 nacos
```

推送脚本只推 `config.yaml`，并且会拒绝推还带着 `registry` 键的文件。

nacos 上改完配置**不会热更新** —— 服务只在启动时读一次配置，改完要重启。

## 本地开发

依赖：MySQL 3306、Redis 6379、Nacos 8848。没有的话用 docker compose 起一套
（本机已有原生 Redis/Nacos 就别起，端口冲突）：

```bash
docker compose -f app/user/service/deploy/docker-compose.yaml up -d    # 起 MySQL + Redis + Nacos
docker compose -f app/user/service/deploy/docker-compose.yaml down    # 停掉
```

工具链（wire / kratos CLI）：

```bash
go install github.com/google/wire/cmd/wire@latest
go install github.com/go-kratos/kratos/cmd/kratos/v3@latest
```

protoc 插件不用单独装 —— `api/gen.sh` / `app/gen-conf.sh` 会把钉好版本的
插件编到 `.cache/protoc-plugins/`（按版本组合做 key）。

起服务 —— 整仓**一个** Go module，但在**自己的目录**里跑：

```bash
cd app/user/service && make run          # 前台启动（带全 env + NACOS_SERVICE_NAME）
```

五个服务一起起（后台，日志在 `/tmp/chatroom-<服务>.log`）：

```bash
for svc in user friend conversation message ws-gateway; do
  (cd $svc-service && nohup make run > /tmp/chatroom-$svc.log 2>&1 &)
  sleep 1
done
```

启动顺序有依赖：`user` → `conversation` → `message` → `ws-gateway`。
后几个要经 nacos discovery 调前面的服务，先起被调方。

> **手工 `go run` 时必须带全环境变量**，否则 nacos 配置中心鉴权失败，服务启动就 panic
> （报 `Code: 401, Message: User not found!` —— 这条是 nacos 登录失败，不是业务用户不存在）。
> `make run` 已经带好了；手工起就照抄 `pkg/tools/dev-env.mk` 里的 `COMMON_ENV`。
>
> 注意**别用 `set -a; . pkg/tools/dev-env.mk` 导环境变量** —— 那是 Make 语法，
> `?=` 不是合法 shell，source 会静默失败，`NACOS_USERNAME`/`NACOS_PASSWORD` 是空的
> → 401 panic。要手工导就逐个 `export`。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `NACOS_SERVERS` | `127.0.0.1:8848` | 配置中心 + 注册中心 |
| `NACOS_NAMESPACE` | `3dbbf765-...` | 命名空间 id |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | `nacos` / `123456` | nacos 账号 |
| `DB_SOURCE` | `root:123456@tcp(127.0.0.1:3306)/test?...` | MySQL DSN |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis |
| `AUTH_JWT_SECRET` | dev 占位 | JWT 签名密钥 |
| `AUTH_INTERNAL_SECRET` | dev 占位 | 服务间内部调用凭证，**五个服务必须一致** |
| `GATEWAY_HTTP_ADDR` | `0.0.0.0:8004` | 网关监听地址。多实例时每个实例给不同端口 |
| `GATEWAY_INSTANCE_ID` | hostname | 网关实例 id。多实例时必须错开，否则在线状态互相覆盖 |

## 用户标识（uid）

用户标识是**字符串 UUIDv7**，不是自增整数。

```
01a19d58-f992-7ca9-807e-dc1cb20c5ef7
```

- **定宽 36 字符**，DB 列类型 `CHAR(36)`。
- **UUIDv7**：前 48 位是毫秒时间戳，所以字符串字典序 = 生成时间序。代码里需要一致全序的地方（单聊 `pair_key` 归一化、成员列表排序）直接按字符串排就行。
- **不可枚举**：自增 id 会被遍历，uid 不会。
- **不透明**：客户端不解析、不依赖其结构。

JWT 的 `sub` claim 存的就是 uid 字符串。

> **uid ≠ 记录主键。** `users.id` 是 uid（字符串），但 `friends.id`、`conversations.id`、`messages.id` 仍是自增整数 —— 那是记录标识，只在本服务内部用，不跨服务传。跨服务传的永远是 uid。

「不存在」的哨兵值是空串 `""`，不是 `0`。

## 表结构变更

**权威是 ent schema**（每个服务各有一份，在 `app/<服务>/service/internal/data/ent/schema/`）。
改完 schema 重启对应服务，ent 会比对现状，
只发必要的 `ALTER TABLE`（加列 / 改列宽 / 补索引）把库对齐到 schema。不动已有数据。

```bash
vim app/<服务>/service/internal/data/ent/schema/xxx.go
make -C app/<服务>/service run   # 重启即生效
```

注意：**ent 不删列** —— 废弃的列要手工 `ALTER TABLE ... DROP COLUMN`。

**不建外键约束**（`schema.WithForeignKeys(false)`）。引用完整性靠应用层保证：
成员行由 repo 在同一个事务里跟着会话一起写 / 删（`AddMembers` / `RemoveMember` /
`Dissolve` 都在事务内），不会出现孤儿行。好处是成员表可以单独清理 / 归档，
不被父表约束绑住。

代价：数据库不再挡脏数据。`conversation_members.conversation_id` 指向已删除的会话
这种行，DB 不会报错，要靠应用层保证写路径正确。

> ent 的 auto-migrate **不删已有约束**。已经建过外键的库（比如改之前初始化过的），
> 要手工 `ALTER TABLE conversation_members DROP FOREIGN KEY <约束名>`。

> **多副本下的并发 DDL** —— auto-migrate 是每个进程启动时各自执行的。
> 同一个镜像的多个副本同时起来时，schema 已经对齐 → ent 不发 DDL → 安全。
> 但「镜像里带了新 schema」撞上「HPA 恰好同时扩容」，多个副本可能同时发
> `ALTER TABLE`，撞上 `Duplicate column name` / metadata lock 超时，
> 新副本 CrashLoopBackOff。
>
> 规避：发版窗口内先把副本数压到 1（`kubectl scale deployment/chatroom-xxx --replicas=1`），
> 等 schema 对齐、pod 正常起来后再放开；滚动更新本身是安全的 ——
> `maxSurge` 默认只多起一个副本，同一时刻只有一个进程在改 schema。

`app/<服务>/service/migrations/*.sql` 是「空库初始化快照」，用来在不依赖 Go 运行时的情况下建出空库
（干净的测试库、手工初始化）。文件名 `<YYYYMMDDHHMMSS>_<服务名>.sql`，按字典序即创建时间序。

> `CREATE TABLE IF NOT EXISTS` 在表已存在时整条跳过，所以这些脚本**不能**用来对齐已有的库。

改完 schema 建议顺手核对脚本与 ent 产物是否一致：

```bash
make -C app/<服务>/service test -count=1
```

### 单聊唯一性

同一对成员的单聊必须唯一。`conversations.pair_key` 存归一化后的成员对
（`"uid1,uid2"`，小的在前），**唯一索引**是唯一的仲裁者 —— 并发建单聊时两个请求
都会尝试插入同一个 `pair_key`，后到的一方撞索引，被 repo 捕获后回查并复用已有会话。

群聊的 `pair_key` 是 `NULL`，MySQL 唯一索引不约束 NULL，所以群聊互不冲突。

> 归一化只在 repo 的写入路径做一次。绕过 repo 直接写库会写入未归一化的 key，
> 索引就挡不住重复单聊了。

## 认证与跨服务调用

**对外接口**用 JWT。access token 15 分钟，refresh token 7 天；refresh token 在 Redis 里
按 jti 存，注销即删；access token 的 jti 进黑名单（`chatroom:token:bl:`），供登出后吊销。

**仅供服务间调用**的端点走内部凭证 `X-Internal-Token`，与 JWT 校验分开：

| 端点 | 调用方 | 用途 |
|---|---|---|
| `/api.user.v1.User/GetFriendProfiles` | friend | 拉好友昵称 / 头像 |
| `/api.user.v1.User/GetConversationMemberProfiles` | conversation | 拉会话成员昵称 / 头像 |
| `/api.conversation.v1.Conversation/CheckMembership` | message | 发消息前确认调用方在不在会里 |
| `/api.conversation.v1.Conversation/UpdateUnread` | message | 增减会话级未读数 |
| `/api.conversation.v1.Conversation/GetUnread` | message | 查会话级未读数 |
| `/api.conversation.v1.Conversation/ClearUnread` | message | 清零会话级未读数（原子） |
| `/api.conversation.v1.Conversation/ListUnread` | message | 查所有会话的未读数（角标） |
| `/api.conversation.v1.Conversation/ListConversationMembers` | ws-gateway | 算广播目标（网关没有用户身份） |
| `/api.conversation.v1.Conversation/ListUserConversations` | ws-gateway | 用户上下线时找「他在哪些会话里」，好通知同伴 |

前两个 user 端点都跳过 `is_visible_to_stranger` 判定 —— 同会话成员 / 已加好友的人应该互相可见，
陌生人可见性防护不该挡住。

### 降级语义：拉不到 ≠ 不存在

跨服务调用拿不到结果时，**必须返 error，不能返空列表 / 空切片**。

原因：有一批接口用「请求了但没回来」判定「用户不存在」（conversation 的
`CheckUsersExist` 就是这样）。下游不可用时返空，上层会把**降级**误判成
**用户不存在** —— 建群聊 / 加成员直接返 404，症状还是「用户不存在」
这种误导性的错误。

具体约定：

| 场景 | 返回 | 上层行为 |
|---|---|---|
| user 服务不可达 / 未配 `internal_secret` | error | 存在性校验**放行**（挡不住业务比放进幽灵成员更糟） |
| user 服务可达，用户真不存在 | missing 列表 | 拒掉，返回 `CONVERSATION_USER_NOT_FOUND` |
| 批量拉资料时某一片失败 | 整体 error | 只记 warn，列表主体照常返回 |

批量接口还有一条：**user 服务的批量端点单次最多收 200 个 id，超出部分静默截断**。
调用方必须自己分片（`app/conversation/service/internal/data/remote_user.go` 的
`batchProfiles`），否则被截断的那部分 id 会被当成「不存在」，200 人以上的群就建不出来。

### 熔断：下游不健康时快速失败

下游宕机或变慢时，调用方每次调用都要等满超时（1.5s）才失败，而上游请求
源源不断进来 —— goroutine、连接、内存都在堆积，这叫**故障放大**。熔断器在
下游明显不健康时直接拒掉，请求快速失败，上游的降级逻辑立刻生效。

实现全在 **`chatroom/pkg/breaker`**，接在 5 个跨服务 gRPC 客户端的中间件链**最内层**：

```go
grpc.WithMiddleware(
    authForwarding(),      // 用户 JWT 透传
    internalForwarding(),  // 内部凭证
    oteltrace.Client(),    // trace
    breaker.Client(),      // 熔断 —— 放最内层
)
```

放最内层是有意的：被熔断拒绝的请求仍然进得了 tracing 中间件，trace 里能看到
这次拒绝 —— 放外面的话就是「上游突然大面积报错，但一条 trace 都查不到」。

默认参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `failureRatio` | 0.5 | 窗口内失败率超过它就熔断 |
| `request` | 20 | 窗口内至少这么多请求才评估（样本太少统计没意义） |
| `window` | 10s | 滚动统计窗口 |
| `openDuration` | 30s | 熔断打开后多久进半开状态试探 |
| `halfOpenMax` | 3 | 半开状态下最多放几个试探请求 |

**为什么不用 Kratos 内置的 SRE 熔断器**：内置的窗口只有 3s，且要求窗口内至少
20 个请求才评估失败率。窗口内请求数 = QPS × 3 —— 5 QPS 的服务窗口内才 15 个请求，
永远达不到 20，熔断器等于没有。这里把窗口放宽到 10s。

只有 5xx（500 / 503 / 504）算失败：下游返 404「用户不存在」是业务结果，
不是下游不健康，不该计入熔断统计。

熔断拒绝时上层拿到的是 **error**（不是空结果）—— 这正是上面「降级语义」
要求的那件事。`pkg/breaker/breaker_test.go` 里有熔断器本身的回归用例
（计数窗口、只算 5xx、样本不足不熔断）。

### 跨服务调用踩坑

client 里的 endpoint 必须是 `discovery:///chatroom-user.grpc`。

nacos 注册时会给 service name 补 `.grpc` 后缀，而 discovery 侧按带后缀的名字查实例。
少写后缀会报 `produced zero addresses`。

## WebSocket 网关

网关**无状态**：不落库，只维护连接 + 转发。端点 `ws://host:8004/ws?token={JWT}`。

token 放在 query 里而不是 header —— 浏览器的 WebSocket API 不能自定义 header。

### 协议

上行：

```json
{ "type": "chat", "conversation_id": 1, "msg_type": 1, "content": "hello" }
{ "type": "status", "user_id": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90" }
{ "type": "status", "conversation_id": 1 }
{ "type": "resume", "conversation_id": 1, "last_message_id": 9 }
{ "type": "ping" }
```

下行：

```json
{ "type": "message", "data": { "message_id": 9, "conversation_id": 1, "sender_id": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90", "msg_type": 1, "content": "hello", "created_at": 1700000000 } }
{ "type": "ack", "data": { "message_id": 9, "conversation_id": 1, "created_at": 1700000000 } }
{ "type": "pong" }
{ "type": "online", "data": { "userId": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90", "online": true } }
{ "type": "presence", "data": { "conversation_id": 1, "members": { "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90": true, "0192f0a2-7c3d-7b2e-9f41-8a5c6d7e8f90": false } } }
{ "type": "resume_result", "data": { "conversation_id": 1, "count": 3, "last_message_id": 12, "has_more": false } }
{ "type": "error", "data": { "message": "...", "code": "SEND_FAILED" } }
```

`chat` 帧由网关转发到 message 服务落库，落库成功后给发送方回 `ack`；
消息本身**不**通过发送方那条连接推回 —— 它走 Redis Pub/Sub 广播回来，
所有连接（含发送方自己的其它端）都收到，路径统一。

### 三种帧的语义

- **`status` 查在线状态**。带 `user_id` 查一个人，带 `conversation_id` 查整个会话
  （回 `presence` 帧）。在线状态存在 Redis 里，所以**跨实例可见** —— A 连在
  网关 1 也能查到连在网关 2 的 B。
- **`resume` 断线重连补消息**。客户端记住自己最后看到的那条消息 id，重连后带上
  `last_message_id`，网关调 message 服务的 `ListMessagesSince` 把 id 更大的消息
  按**正序**补回来（跟正常推送走同一个 `message` 帧，客户端只需要一套处理逻辑），
  最后回一个 `resume_result` 帧。一次最多补 50 条 —— 写队列默认 64，补太多会挤掉消息。
- **`online` 帧既是响应也是通知**。别人上下线时，会话里的同伴也会收到 `online` 帧。

### 消息流转

```
客户端A ──ws──► ws-gateway ──gRPC──► message 服务 ──► MySQL 落库
                    ▲                                    │
                    │                                    ▼
              Redis Pub/Sub ◄─── chatroom:msg:{convId} ─┘
                    │
                    └──► 目标成员在本实例上的连接 ──► 客户端B
```

网关订阅 `chatroom:msg:*`（模式订阅），收到广播后按会话成员列表算出推送目标。

### 多实例部署

网关是无状态的，多开几个实例直接水平扩。要注意两点：

- **端口要错开**。同一台机器上跑多个实例时用 `GATEWAY_HTTP_ADDR` 给每个实例一个
  不同端口（默认 `0.0.0.0:8004`）。
- **实例 id 要错开**。用 `GATEWAY_INSTANCE_ID` 指定（默认取 hostname）。
  这不是可选项 —— 同一台机器上 hostname 是一样的，两个实例写进 Redis 的
  `online:{uid}` 值会撞，而那个值正是用来判断「谁还连着这个用户」的。

```bash
GATEWAY_HTTP_ADDR=0.0.0.0:8004 GATEWAY_INSTANCE_ID=gw-1 chatroom-ws-gateway \
  -conf app/ws-gateway/service/configs/config.yaml -bootstrap app/ws-gateway/service/configs/bootstrap.yaml
GATEWAY_HTTP_ADDR=0.0.0.0:8005 GATEWAY_INSTANCE_ID=gw-2 chatroom-ws-gateway \
  -conf app/ws-gateway/service/configs/config.yaml -bootstrap app/ws-gateway/service/configs/bootstrap.yaml
```

每个实例都订阅 `chatroom:msg:*`，都会收到**所有**会话的消息，包括自己没人的会话 ——
再按会话成员列表过滤出「连在本实例上的那些连接」推出去。一期就这么做
（实例数 × 消息 QPS，量级不大）；真要省可以改成按需 SUBSCRIBE，但那样要处理
订阅 / 退订的并发和漏订。

> K8s 里上面那两件事不用手工做：`GATEWAY_INSTANCE_ID` 直接取 pod 名
> （`fieldRef: metadata.name`），天然错开；副本数交给 HPA —— 见
> `app/ws-gateway/service/deploy/k8s/20-ws-gateway-scaling.yaml`。

### 准入限制

网关是**唯一没有业务鉴权兜底的入口** —— 别的服务进来至少要过 JWT，
网关这边握手失败只是关连接。一个客户端可以无成本地狂刷握手，把内存打满。
所以握手上锁了三道闸门（`internal/gateway/limiter.go` + `pool.go`）：

| 闸门 | 环境变量 | 默认 | 拒的时候 |
|---|---|---|---|
| 单 IP 握手限流（令牌桶） | `GATEWAY_HANDSHAKE_RATE` / `GATEWAY_HANDSHAKE_BURST` | 20 次/秒，突发 40 | **429** |
| 本实例连接数上限 | `GATEWAY_MAX_CONNECTIONS` | 5000 | **503** |
| 单用户连接数上限 | `GATEWAY_MAX_CONNS_PER_USER` | 5 | **429** |

顺序是**先限流再验 token**：IP 限流在最外层，不然攻击者可以用假 token
把验签的 CPU 打满。连接配额是**原子预留**的 —— 预留成功才做 Upgrade，
Upgrade 失败必须把配额还回去，否则一次失败的握手就永久占掉一个名额。

上限按**单实例**计。扩容后总上限 = 副本数 × `GATEWAY_MAX_CONNECTIONS`，
调参时别忘了乘。

IP 从 `RemoteAddr` 取。只有网关前面确实有 ingress、且客户端**不能**直连
网关时，才开 `GATEWAY_TRUST_PROXY_HEADER=true` 读 `X-Forwarded-For` ——
开着的话任何人都能伪造这个头绕过 IP 限流。

### 几条规则

- **单写者模型**。只有 `writePump` 调 `WriteMessage`，其它地方往 `send` 队列里塞 ——
  gorilla/websocket 不允许并发写，两条协程同时写会交错字节流。
- **队列满时丢最老的一条**。聊天场景宁可丢中间态，也不能让一个卡住的客户端把网关内存拖爆。
- **成员身份确认不了就拒绝**。conversation 服务不可达时 message 服务返
  `ErrMessageConversationUnavailable`，不 fail open —— 否则谁都能往别人的会话发消息。
- **广播失败不影响落库结果**。消息已经存下来了，客户端拉历史能拿到。
- **在线状态用 Lua 比较并删**。用户可能在另一个实例重连，GET 再 DEL 的两步写法会误删新实例写的状态。
- **成员列表带 TTL 缓存**（默认 1 分钟）。每条消息都问一次 conversation 服务太贵；
  代价是刚被踢的人最多多收一分钟消息。


## Redis key

| key | 用途 |
|---|---|
| `chatroom:user:{uid}` | 用户资料缓存 |
| `chatroom:user:ver:{uid}` | 用户缓存版本号，资料变更时递增 |
| `chatroom:conv:{id}:members` | 会话成员集合 |
| `chatroom:token:bl:{jti}` | access token 黑名单 |
| `chatroom:refresh:{jti}` | refresh token |
| `chatroom:login:fail:{user}` / `chatroom:login:locked:{user}` | 登录失败计数 / 锁定 |
| `chatroom:friend:apply:` / `:accept_reject:` / `:remove:` | 好友操作限频 |
| `chatroom:msg:{convId}` | 消息广播频道（Pub/Sub，不持久化） |
| `online:{uid}` | 在线状态，值 = 网关实例 id，带 TTL |

缓存是派生数据，MySQL 是权威源。写路径一律「先写库，再删缓存」。

## 会话服务的群管理规则

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

## 消息服务的规则

- **历史消息用游标分页，不用 OFFSET**。游标是「上一页最后一条消息的 id」，
  下一条查 `id < cursor`。复合索引 `(conversation_id, id)` 覆盖过滤 + 排序，不回表。
  OFFSET 在数据变动时会漏 / 重，游标不会。
- **「拉更旧的」和「拉更新的」是两个方向，要两个端点**。
  `ListMessages`（`/api/message/list`）是**倒序**翻历史 —— 游标语义是「比这个 id 更旧的」，
  断线重连补消息要的是「比这个 id 更新的」，方向相反，没法复用同一个游标。
  所以单开了一个 `ListMessagesSince`（`/api/message/since`），按 id **正序**返回。
  网关的 `resume` 帧走的就是它。
- **发消息要校验成员身份**，且下游不可达时**拒绝**而不是放行（见上面的 WebSocket 章节）。
  连成员校验能力本身缺失（接线配错）也拒绝 —— 放行等于消息服务完全不做鉴权。
- **消息内容存 TEXT，不是 VARCHAR**。ent 的 `field.String` 不带 `MaxLen` 时
  默认映射成 `varchar(255)`，255 字节连一条 85 字的中文消息都存不下 ——
  biz 层放行但库拒收，用户看到的是 500。要显式写
  `SchemaType(map[string]string{"mysql": "text"})`。
- **未读数存在 conversation 服务的成员表上**（`conversation_members.unread_count`）——
  成员关系在那边，未读数跟着一起。message 服务经内部凭证增减。
- **发消息后给除发送方外的所有成员加一条未读**。加未读失败只记日志，不影响发消息结果。
- **未读数清零走独立的 `ClearUnread` 端点**，服务端一条 UPDATE 清零。
  别让调用方「先 GetUnread 再 UpdateUnread(-n)」—— 那两步之间有窗口，
  并发到达的新消息会被一起吞掉；而且未读数是 uint32，转 int32 超过 2^31 会溢出。
- **未读数下游不可达返 503**，不是 500。500 会让调用方以为是自己请求写错了。
- **查单个会话的未读数也要先验成员身份**，否则能改 `conversation_id`
  去探测别人会话的未读数。
- **消息内容上限** `biz.MaxMessageContentLen`（按 rune 数算，不是字节）。

## 测试

每个服务在自己的目录里跑：

```bash
cd app/user/service
make test              # 单元测试，不碰外部依赖
make test-integration  # data 层 repo + 跨域链路，需要 MySQL
```

五个服务 + 公共 module 全跑一遍（仓库根）：

```bash
make test
```

集成测试每个用例自建临时库 `chatroom_test_<纳秒>`、跑完自动 DROP，不碰共享的 `test` 库。
底层是 `CHATROOM_TEST_ROOT_DSN` 环境变量，未设置时这些用例会 skip。

分层测试：service 测试 fake repo 并在 fake 之上建真的 usecase，biz 测试 fake repo，data 测试直接打真实存储。
gateway 包的测试起真实的 `httptest` 服务器 + 真实 WebSocket 客户端，
Redis 相关的用例（订阅 / 在线状态）在 `CHATROOM_TEST_REDIS_ADDR` 不可达时 skip。

公共测试辅助在 `chatroom/pkg/testutil/`（各服务共用一份）：

| 辅助 | 作用 |
|---|---|
| `testutil.NewDB(t)` | 建名字唯一的临时库，跑调用方的 `Migrate`，结束时 DROP |
| `testutil.NewRedis(t)` | 连 DB 15 的客户端，不可达就 skip |
| `testutil.AuthedCtx(t, uid)` | 带有效 JWT 的 context |

各域的 data 层测试在自己的 `app/<域>/service/internal/data/` 下，用该域的 `newTestDB(t)`
（见 `testsupport_test.go`）包住 `testutil.NewDB`。

跨域用例（拉 user 服务的资料等）放在**调用方**域的 data 包里，
把 gRPC client 换成内存替身 —— 传输层跳过，但走的是生产 adapter 路径。

> **Redis Pub/Sub 不按 DB 隔离** —— DB 14 上的订阅照样收得到 DB 15 上发的消息。
> 所以 Pub/Sub 的测试隔离靠**频道名**，不靠换 DB。写这类测试时别假设
> 「换个 DB 号就互不干扰」，那会得到一个单跑过、全量跑失败的间歇性失败。

> **gorilla/websocket 的读超时是不可恢复的**。一旦 `SetReadDeadline` 到点，
> 连接就永久废了 —— 后续 `ReadMessage` 立刻返回同一个超时错误，重新设一个
> 更长的 deadline 也没用。所以客户端侧**不能**用「设个短超时读一帧」的方式
> 等新帧：那会把连接毒死，后面所有读都返回空，看起来像「服务端没推消息」。
>
> 正确姿势是「每条连接一个后台读协程 + channel」—— 读阻塞在 channel 上，
> 不用 deadline，超时也不影响连接（帧还在 channel 里排队）。
> 写 WebSocket 客户端时别用读超时做「拉取式」交互，心跳用 pong。

## 代码生成

契约在自己的目录里跑（生成 grpc / http stub + openapi.yaml）：

```bash
cd api/user/service
make api      # 重新生成 grpc / http stub + openapi.yaml（protoc，见 api/gen.sh）
```

服务在自己的目录里跑：

```bash
cd app/user/service
make config   # 重新生成 conf proto（protoc，见 app/gen-conf.sh）
make generate # 跑 go generate（Wire）
make all      # config + generate
```

kratos CLI 生成 service 层骨架要在服务目录里跑 —— 它要吃契约 proto 的路径：

```bash
cd app/user/service
kratos proto server ../../../api/conversation/service/v1/conversation.proto \
    --target-dir=internal/service
```

五个服务 + 四个契约全跑一遍（仓库根）：

```bash
make all     # 契约 + conf proto + Wire + go mod tidy
```

`*.pb.go`、`*_grpc.pb.go`、`*_http.pb.go`、`wire_gen.go` 都是生成物，**不要手改**。

生成分两处跑 —— **契约**在契约目录，**配置**在服务目录：

```bash
cd api/user/service
../../gen.sh user                           # 契约 proto（grpc / http / openapi）

cd ../../app/user/service
../../gen-conf.sh user                      # conf proto
```

契约用 **protoc** 而不是 buf —— 契约 proto 要 import 仓库根的
`third_party/google/api/annotations.proto`，而 buf v2 的 module 根是单一目录，
跨目录引用会被拒（`is outside the context directory`），`deps:` 又只接受 registry 名。
protoc 原生支持多个 `--proto_path`，这正是官方 `go-kratos/beer-shop` 的做法。

conf proto 也用 protoc：整仓是一个 module，多个服务的 conf 包可能被链进同一个
二进制，而 protobuf 的注册表按「相对 proto_path 根的路径」做 key。脚本从**仓库根**
跑，`--proto_path=app`，注册路径就成了 `<服务>/service/internal/conf/conf.proto`，
带服务名这一层前缀天然避开撞车。契约那边同理：`--proto_path=api` →
`<域>/service/v1/<文件>.proto`。

插件版本在 `api/gen.sh` / `app/gen-conf.sh` 里用 `@version` 钉死，编译产物缓存在
`.cache/protoc-plugins/`（按版本组合命名），并发跑不会互相踩。

`paths=source_relative` 让 `v1/xxx.proto` 生成到 `v1/xxx.pb.go` —— pb 代码跟 proto 待在一起。

全部一次跑完（仓库根）：

```bash
make api     # 四个契约包重新生成
make config  # 五个服务的 conf proto
make proto   # 契约 + 各服务的 conf proto
make build   # 五个服务
make test    # 五个服务的单元测试
```

### service 层也是生成的

`app/<服务>/service/internal/service/<服务>.go` 由 **kratos CLI** 生成，不是手写的：

```bash
kratos proto server ../../../api/conversation/service/v1/conversation.proto \
    --target-dir=internal/service
```

在对应服务的目录里跑（`user` / `friend` / `conversation` / `message`
四个域有业务 proto）—— proto 在契约包里，路径要带上 `../../../api/`。

生成器**只在文件不存在时写** —— 已存在的会报 `<Service> already exists` 并跳过，
所以重跑不会覆盖已实现的版本。

CLI 生成的是**骨架**：内嵌 `pb.Unimplemented<Service>Server`、无参构造函数、
以及每个 RPC 一个 `return &pb.<Reply>{}, nil` 的空方法。往里填
`*biz.<资源>Usecase` 字段、构造函数参数、真实实现 —— 方法**签名**保持生成器
写出来的样子，那是跟 `*_grpc.pb.go` 的契约。

CLI 装法：`go install github.com/go-kratos/kratos/cmd/kratos/v3@latest`。

## 健康检查

每个服务暴露两个端点，语义完全不同 —— 混用会直接制造故障：

| 端点 | 语义 | 探什么 | 失败后果 |
|---|---|---|---|
| `/healthz` | **liveness** | 只看进程是否活着，**不碰任何依赖** | 依赖挂了也**不能**失败：重启 pod 解决不了 MySQL 宕机，只会把 pod 搞成 CrashLoopBackOff，故障面反而更大 |
| `/readyz` | **readiness** | 关键依赖 | 硬依赖挂了返 **503**，K8s 把这个 pod 从 Service 的 endpoint 里摘掉，流量不再往一个什么都做不了的 pod 上打 |

两个端点都用 `srv.Handle` 注册 —— **绕过 Kratos 中间件链**。用
`srv.Route`（proto 服务的注册方式）会走 JWT 鉴权中间件，探针不带 token
会被 401 挡掉。

实现在 **`chatroom/pkg/health`**（跨服务统一语义），各服务在
`internal/data/health.go` 里声明自己的依赖：

```go
c.Add("mysql", true, d.PingMySQL)   // 硬依赖
c.Add("redis", false, d.PingRedis)  // 软依赖
```

### 硬依赖 vs 软依赖

判据只有一条：**这个依赖挂了，服务还能不能正确处理请求**。

| 服务 | 硬依赖 | 软依赖 |
|---|---|---|
| user / friend / conversation / message | MySQL | Redis（限频、黑名单、缓存、广播） |
| ws-gateway | Redis（Pub/Sub 是消息投递的**唯一**通道） | —— |

响应体：

```json
{"status":"ok","checks":{"mysql":{"status":"ok","latency_ms":1},"redis":{"status":"ok","latency_ms":0}}}
```

`status` 三档：`ok` / `degraded` / `failed`。

- `failed` —— 硬依赖挂了，readiness 返 **503**。
- `degraded` —— 只有软依赖挂了，readiness 仍返 **200**，服务继续接流量。

**别把 Redis 标成硬依赖**：Redis 一抖，四个服务的 pod 会被全摘光，故障面比
降级大得多。也别把 MySQL 标成软依赖：那会让流量继续打进一个连不上库的 pod。
这两条判定都有测试守着（`app/<服务>/service/internal/data/health_test.go`）。

> 为什么 ws-gateway 的 Redis 反过来是硬依赖：那边 Redis Pub/Sub 是消息投递到
> WS 客户端的**唯一**通道，断了就真的什么都推不出去。带着一个断掉的订阅继续
> 接新连接，只会让客户端连上来却永远收不到消息 —— 那才是最难排查的故障。
> 快速失败更好。

探测本身走**原生连接池**（`*sql.DB.PingContext` / `redis.Ping`）：ent 生成的
client 没有 Ping 方法，所以 `data` 层自己开一条 `*sql.DB` 交给 ent
（`ent.Driver(entsql.OpenDB(...))`），两者共用同一个连接池，不额外占连接。

### 探针配置

三个探针各管一件事：

```yaml
startupProbe:    # 启动期给足时间，期间不跑 liveness
  httpGet: {path: /healthz, port: http}
  periodSeconds: 5
  failureThreshold: 30      # 最多等 150s
readinessProbe:  # 查依赖
  httpGet: {path: /readyz, port: http}
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3       # 连续失败 3 次（~30s）才摘，避免抖动
livenessProbe:   # 只查进程
  httpGet: {path: /healthz, port: http}
  periodSeconds: 20
  timeoutSeconds: 5
  failureThreshold: 3
```

`timeoutSeconds` 要大于 `pkg/health` 的单项探测超时（默认 2s），否则
探针先超时、服务端还在探测。

## 可观测性

两个出口：**Prometheus metrics**（拉）和 **OTLP trace**（推）。两个都默认关闭，
用环境变量打开。

| 能力 | 开关 | 监听 / 目标 | 打开后 |
|---|---|---|---|
| metrics | `METRICS_ADDR` | `0.0.0.0:9100` | 进程内起一个 HTTP server 暴露 `/metrics` |
| trace | `OTEL_EXPORTER_OTLP_ENDPOINT` | `otel-collector:4317` | gRPC 导出 span 到 collector |

配置项在 `observability` 段（`app/<服务>/service/configs/config.yaml`）：

```yaml
observability:
  metrics_addr: "${METRICS_ADDR:}"                      # 空 = 不开
  tracing_endpoint: '${OTEL_EXPORTER_OTLP_ENDPOINT:""}'  # 空 = 不开
  service_name: "${NACOS_SERVICE_NAME:chatroom-user}"
  unauth_sample_rate: 0.1                                # 401 请求的采样率
```

K8s 里由 `deploy/k8s/01-config.yaml` 的 ConfigMap 注入：

```yaml
METRICS_ADDR: "0.0.0.0:9100"
OTEL_EXPORTER_OTLP_ENDPOINT: ""   # 留空 = 不开 trace（集群里要先有 collector）
```

Pod 上带 Prometheus 自动发现的注解（`deploy/k8s/10-*.yaml`）：

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9100"
  prometheus.io/path: "/metrics"
```

没用 ServiceMonitor —— 那要装 Prometheus Operator 的 CRD；这套注解
裸 Prometheus 的 `kubernetes_sd` 角色也能用。

实现全在 **`chatroom/pkg/observability`**：exporter 的装配、shutdown、
401 采样过滤都在那儿。各服务只在 `internal/observability/` 里做
conf → `pkg/observability.Config` 的翻译。

两个容易踩的点：

- **metrics 端口在容器里固定 9100**，跟业务端口（8000-8004 / 9000-9004）
  分开。改这个端口要同步改 Deployment 的 `containerPort`、Pod 注解的
  `prometheus.io/port` 和 ConfigMap 的 `METRICS_ADDR` —— 漏一个就变成
  「Prometheus 抓不到，且没人报错」。
- **空值 = 关闭**，不是「用默认地址」。本地 `make run` 五个服务同时起，
  默认全开会撞端口；`METRICS_ADDR` 不设就是不开。

## 数据库连接池

`database/sql` 的默认值在生产上是危险的，所以每个服务的配置里都显式写了：

| 配置项 | 环境变量 | 默认 | 不配会怎样 |
|---|---|---|---|
| `max_open_conns` | `DB_MAX_OPEN_CONNS` | 10 | **无限** —— HPA 扩到 10 个副本就是 10 个无限池，足以打爆 MySQL 的 `max_connections`（默认 151） |
| `max_idle_conns` | `DB_MAX_IDLE_CONNS` | 5 | 2 —— 高并发下连接被反复建立 / 销毁 |
| `conn_max_lifetime` | `DB_CONN_MAX_LIFETIME` | 1800s | **永不复用** —— MySQL 代理重启或主从切换后池里的老连接已经死了，表现为「偶发 500」，极难排查 |
| `conn_max_idle_time` | `DB_CONN_MAX_IDLE_TIME` | 300s | 0（不回收） |

解析和兜底在 **`chatroom/pkg/dbpool`**：`<= 0` 和负数都回落到默认值，
不让「配错成 0」变成「连接池无限制」。

⚠️ **连接额度是所有服务共享的一个池子**，不是每个服务各有一份：
5 个服务 × 最多 10 副本 × 10 连接 = 500 条。MySQL 的 `max_connections`
要配得比这大（默认 151 不够）。调大前先算一下总额度。

> Duration 用 **proto 格式**（`1800s` = 30 分钟），不能写 `30m` ——
> protojson 只认 s / ms / us / ns。bool 同理不能走 `${VAR}` 占位符
> （protojson 不接受字符串形式的布尔值）。

## 部署

```bash
cd app/user/service && make docker-build   # 构建本服务镜像
docker compose -f app/user/service/deploy/docker-compose.yaml up -d  # 本地起 MySQL + Redis + Nacos
docker compose -f app/user/service/deploy/docker-compose.yaml down  # 停掉
```

每个服务一个 Dockerfile，在 `app/<服务>/service/Dockerfile`。构建上下文是**仓库根目录** ——
服务依赖同仓的 `pkg/` 和 `api/`，上下文只给服务目录的话这些都不在构建里：

```bash
docker build -t chatroom-user:latest -f app/user/service/Dockerfile .
```

或者用 Makefile（它会自动算好 `-f` 和上下文）：

```bash
cd app/user/service && make docker-build
```

> 镜像里的 go 版本：builder 阶段用 `golang:1.26`（只暴露 major.minor tag，
> 实际拉到的 patch 版本可能比 `go.mod` 要求的旧）。Dockerfile 里设了
> `GOTOOLCHAIN=auto`，让 go 自己下载 `go.mod` 指定的那个工具链。
> 这个错**只在镜像构建时暴露** —— `go build` / `go test` 全都发现不了。

> 敏感字段（DB / Redis / nacos 凭证 / JWT 密钥）走 env 注入，
> 镜像里的 `config.yaml` / `bootstrap.yaml` 只有非敏感默认值。

### 本地基础设施

`app/<服务>/service/deploy/docker-compose.yaml` 一键起 MySQL + Redis +
Nacos + Jaeger（只管底座，不管业务服务）：

```bash
docker compose -f deploy/docker-compose.yaml up -d
docker compose -f deploy/docker-compose.yaml down
```

本机已经在跑原生 Redis/Nacos 的话端口会冲突，先停掉再起这份。

## 常用命令

每个服务在自己的目录里跑：

```bash
cd app/user/service
make build         # 编译到 app/user/service/bin/chatroom-user
make vet
make test          # 本服务的测试
make test-integration  # 需要 MySQL 的集成测试
make clean
```

五个服务 + 公共 module 全跑一遍（仓库根）：

```bash
make build
```
