# 架构与分层

本文讲 chatroom 的内部结构约定：三层模型怎么穿、跨服务依赖怎么走、目录怎么分。
新人先读 [README](../README.md) 的「架构概览」，细节回来看这里。

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

### 为什么 service 不依赖 data

`service` 是传输层适配器，它的工作是把 proto 的 DTO 翻译成 DO 再交给 `biz`。
它一旦 import `data`，就等于在协议层直接摸到了存储实现 —— 换 ORM、拆表、
换驱动都会波及 service 层，而 service 层是契约的投影，改动成本最高。

`biz` 声明 repo 接口、`data` 实现它，是经典的反转接缝：依赖箭头从
「上层指向下层」翻成「下层指向上层声明的接口」。这样 `biz` 编译时根本看不到
存储，`data` 可以随便换实现。

### 跨域接口放哪

跨域调用的接口声明放在**调用方**域的 `biz` 里，实现放在**调用方**域的 `data` 里：

```
app/friend/service/internal/biz/       → type FriendProfileFetcher interface
app/friend/service/internal/data/      → type Adapter 实现它（包住 remote_user.go）
```

调对方服务的 gRPC 客户端就在同一个 `data` 包里，文件名 `remote_<对方>.go`
（`remote_user.go`、`remote_conversation.go`、`remote_message.go`）—— `remote_`
前缀标记它是**跨进程**调用。它只做 gRPC 通道，不碰业务模型，是纯传输层。

## 依赖的是契约，不是服务

一个服务不 import 另一个服务的 `internal/`（Go 的 `internal/` 可见性规则在编译期
就挡住了）。跨服务只 import 对方的**契约包**（`api/<域>/service/v1`）。

调用方需要的是「user 域对外的契约」，不是「user 服务的实现」。所以契约单独放在 `api/`：

```
chatroom/api/user/service/v1     契约：proto + 生成的 grpc / http stub。没有实现。
chatroom/app/user/service        实现：biz / data / service。依赖上面的契约包。
```

`app/friend/service` 依赖 `chatroom/api/user/service/v1`，**不依赖**
`chatroom/app/user/service`。好处：user 服务换 ORM、拆表、改内部结构 →
friend 的代码一个字都不用动；契约没变的情况下 user 重新部署，friend 连编译都不需要。

依赖方向永远是**服务 → 契约 → 无**：

- `pkg` 不反向依赖任何服务。
- 契约之间**互不依赖**（每个域的 proto 不 import 别的域的 proto）。

单个 module 意味着这些依赖是**同一个仓库内的 import path**，不需要 `replace`，
本地改完契约立刻生效。要把某个域拆成独立仓库，只需把 `api/<域>` 抽成单独的
module 并改版本号 —— import 路径和依赖箭头不变。

## 服务边界

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

公共代码抽在 `pkg/` 是因为这些是横切关注点，必须所有服务一致。放在 `pkg/` 之后
由编译器保证一致。

`pkg` 不依赖任何服务，依赖方向是单向的：服务 → pkg。

## Protobuf 全局注册表

protobuf 的 descriptor 注册表是**进程全局**的，而整仓是一个 module —— 多个域的
生成代码可能被链进同一个二进制（尤其是测试二进制）。注册表按文件**相对
protoc `--proto_path` 根的路径**做 key，所以两个相对路径相同的 proto 会在
`init()` 撞车：`proto: file "v1/error_reason.proto" is already registered`。

两个生成脚本因此都从**仓库根**跑 protoc，根里带上域名 / 服务名：

| 脚本 | `--proto_path` | 注册路径 |
|---|---|---|
| `api/gen.sh` | `api` | `<域>/service/v1/<文件>.proto` |
| `app/gen-conf.sh` | `app` | `<服务>/service/internal/conf/conf.proto` |

绝不要把脚本的 root 指得更窄 —— 那会把所有域又压回同一个相对路径。
