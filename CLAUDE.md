# 仓库约定

这是 Kratos 多服务单仓。本文件定的是改模板时必须遵守的分层契约。

## 目录结构

整仓是**一个** Go module（`module chatroom`），一个 `go.mod`，没有 `replace`
也没有 `go.work`。目录按官方 `go-kratos/beer-shop` 的思路分：

```
api/<域>/service/     契约包：proto 源码 + 生成的桩代码，无实现。
  v1/                 proto + 生成的 grpc/http 桩 + openapi.yaml。

app/<服务>/service/   微服务，各自自包含。
  cmd/server/         入口、Wire 注入、main.go。
  configs/            运行时配置（config.yaml），不含密钥。
  internal/conf/      配置 proto，由 `make config` 生成。
  internal/server/    HTTP/gRPC server 装配。
  internal/service/   传输层适配器，一个资源一个文件。
  internal/biz/       领域模型、usecase、repo 接口、错误。
  internal/data/      repo 实现和存储客户端。

third_party/          第三方 proto —— 全仓一份。
  google/api/         annotations.proto + http.proto（离线依赖）。

pkg/                  共享包，所有服务都 import。
  keys/               Redis key 和 Pub/Sub 频道名 —— 跨服务契约。
  errors/             框架级错误原因（COMMON_*）—— 跨服务契约。
  middleware/         JWT、内部凭证、黑名单中间件。
  health/             健康检查（liveness / readiness），统一语义。
  breaker/            跨服务 gRPC 调用的熔断器。
  dbpool/             database/sql 连接池参数与默认值。
  observability/      指标与链路追踪初始化。
  nacos/              Nacos client、注册、发现、配置源。
  testutil/           测试脚手架（DB、Redis、JWT）。
  tools/              构建辅助（dev-env.mk、nacos-config.sh），不是 Go 代码。
```

`ws-gateway` 没有 `api/` 对应目录 —— 它是聚合网关，调用别的服务，不对外暴露
自己的业务契约。

## 分层与依赖规则

三种模型形状穿过三层。`biz` 拥有 DO，`data` 拥有 PO，`service` 只做 DTO ↔ DO 转换。

```
   client ──► DTO ──► service ──► DO ──► biz ──► DO ──► data ──► PO ──► storage
                                  ▲                ▲
                                  │ declares       │ implements
                                  └─── repo IF ────┘

   DTO  Data Transfer Object — proto 请求 / 响应。
   DO   Domain Object        — 纯 biz 模型，无 proto，无存储 tag。
   PO   Persistent Object    — 存储形状，归 data。
```

| 层 | 拥有 | 边界上说什么 | 绝不说 |
|---|---|---|---|
| service | — | DTO ↔ DO | PO、存储客户端 |
| biz | DO | DO | DTO、PO、存储客户端 |
| data | PO | DO ↔ PO | DTO |

- `service` import 契约包（DTO）和 `biz`（DO），绝不 import `data`。
- `biz` 只为错误原因枚举 import 契约包，绝不 import `service` / `data`。
  在这里声明的 repo 接口就是反转接缝。
- `data` import `biz` 来实现 repo 接口，绝不 import `service`，绝不见 DTO。
- `cmd` 是唯一用 Wire 把三层接起来的地方。

跨服务的基础设施（nacos、keys、errors、middleware、observability、testutil）
放在 `chatroom/pkg` —— 是共享包，不是共享目录。跨服务契约必须只有一份定义：
两份 Redis key 或错误原因字符串会悄悄漂移。`pkg` 绝不 import 任何服务，
依赖方向是单向的（service → pkg）。

### 契约包

每个业务域在 `api/<域>/service/`（`chatroom/api/<域>/service/v1`）下有自己的
契约包：proto 源码加生成的桩，**没有实现**。实现方依赖自己的契约包；
调用方依赖契约包，不依赖服务。

这是「独立演进」的前提。调用方要是依赖了服务包，服务内部任何重构（换 ORM、
拆包）都会波及调用方的构建。契约包之下依赖图上只有「service → contract」一条边，
契约不变就不会强迫调用方重新编译。

依赖方向：

```
service  →  contract  →  (nothing)
service  →  pkg       →  (nothing)
```

契约包之间互不依赖（域与域的 proto 不互相 import）。`pkg` 不依赖任何契约。

绝不要从一个服务 import 另一个服务。`internal/` 已经在编译期挡住了明显的情况，
契约包消除了那些「合法」的诱惑。

以后把某个域拆成独立仓库，只是改版本号 —— import 路径和依赖箭头不变。

跨错箭头的改动是分层 bug，改设计而不是加 import。

### Protobuf 全局注册表

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

### 跨服务降级契约

远端客户端放在**调用方**的 `data` 层（`remote_<服务>.go`）。下游不可达、
未配置或缺内部凭证时，必须返**错误** —— 绝不能返空列表或空切片。

因为有一批接口靠「请求了但没回来」判定「用户不存在」。降级调用返空结果跟
「用户真不存在」分不开，下游一挂就变成误导性的 `USER_NOT_FOUND` 拒绝，
而不是一条降级但可用的路径。

- 下游不可用 → error → 由调用方决定：存在性校验**放行**（挡不住业务比放进
  幽灵成员更糟），资料查询记 warn 并返回不带昵称的列表。
- 用户真不存在 → 明确的 missing 列表 → 拒绝。
- 批量端点超过单次 id 上限会静默截断（user 服务上限 200）。调用方要自己分片，
  否则被截断的尾部会被当成「不存在」。
- 一片失败则整批失败。部分结果比错误更糟：调用方分不清「没有」和「没拉到」。

### 熔断契约

跨服务 gRPC 客户端套 `pkg/breaker` —— 熔断中间件必须放在客户端中间件链的
**最内层**，紧挨着 RPC 调用：

```go
grpc.WithMiddleware(
    authForwarding(),
    internalForwarding(),
    oteltrace.Client(),
    breaker.Client(),   // 最内层
)
```

放最内层是有意的：被熔断拒绝的请求仍然过得去 tracing 中间件，trace 里能看到
这次拒绝。放外面一层的话，症状就是「上游突然大面积报错但没有 trace 能解释」。

- 只有 5xx（500 / 503 / 504）算失败。404「用户不存在」是业务结果，不是下游不健康。
- **不用** Kratos 内置的 SRE 熔断器：它窗口只有 3s，且要求窗口内至少 20 个请求
  才评估。5 QPS 的服务窗口内才 15 个请求，永远达不到 20，熔断器等于没有。
  `pkg/breaker` 窗口 10s，并把「样本太少」当作「不熔断」。
- 熔断拒绝对调用方表现为 **error**，正好落在上面的降级契约里：绝不能被压成空结果。
- **状态切换一律记日志**（`pkg/breaker` 的 `OnStateChange`）：打开用 Warn，
  半开和关闭用 Info，日志带 `operation` 字段。熔断打开是「下游什么时候开始
  挂的」的第一手线索 —— 没有它，线上只看到一堆 503，看不出是哪个端点、
  什么时候挂的。调用方自己传了 `Settings.OnStateChange` 不会被顶掉，两个回调都跑。

### 配置分两份（bootstrap vs config）

每个服务读**两个**文件，都必须存在：

| 文件 | 放什么 | 绝不放 |
|---|---|---|
| `configs/bootstrap.yaml` | 只有 `registry.nacos` | 业务参数 |
| `configs/config.yaml` | 业务参数 | `registry` |

这个拆分是为了打破循环依赖：配置中心里的业务配置取决于「连哪个 nacos」，
这个事实就不能由配置中心自己决定。`bootstrap.yaml` **先**读（文件 + 环境变量）
来建 nacos 客户端，业务配置再按 文件 → nacos → 环境变量 的优先级合并。

**两个文件都是必需的。** `cmd/server/main.go` 先读 bootstrap，缺了就 `panic` ——
kratos 的 file source 会做 `os.Stat`，文件缺失是硬错误，不是空配置：

```
panic: stat configs/bootstrap.yaml: no such file or directory
```

`bootstrap.yaml` 里的 `service_name` 必须是 `<服务>` 专属的
（`chatroom-user`、`chatroom-friend` 等）—— 其他服务靠它发现这个进程。
照抄别的服务的文件会让这个进程注册成错的名字。

### 数据库连接池契约

每个拥有数据库的服务都要在 `data.database` 里显式设池
（`max_open_conns`、`max_idle_conns`、`conn_max_lifetime`、`conn_max_idle_time`）。
`database/sql` 的默认值在生产上不安全：`MaxOpenConns` 默认无限，
`ConnMaxLifetime` 默认「永不复用」—— 后者在 MySQL 代理重启后表现为零星 500。

`pkg/dbpool` 应用这些值，`<= 0` 和负数回退到默认值，配置漏填成 `0` 不会变成无限池。

YAML 里的 duration 用 **proto** 格式（`1800s`），不是 `30m` —— protojson 只认
s/ms/us/ns。bool 也不能用 `${VAR}` 占位符传，protojson 拒绝字符串形式。

连接额度是**所有服务共享**的：5 个服务 × 最多 10 副本 × 10 连接 = 500。
MySQL 的 `max_connections` 要调得比这大，默认 151 不够。

### 可观测性契约

指标和追踪默认关闭，用环境变量打开（`METRICS_ADDR`、
`OTEL_EXPORTER_OTLP_ENDPOINT`）。空值表示「关」，不是「用默认地址」——
五个服务本地并排跑，用默认端口会撞。

exporter 的装配在 `chatroom/pkg/observability`；服务只负责把自己的配置翻译成
`pkg/observability.Config`（`internal/observability/`）。容器里的 metrics 端口
是 9100，有三处必须同步：ConfigMap（`METRICS_ADDR`）、容器端口、Prometheus pod 注解。

### 健康检查契约

每个服务暴露两个端点，都用 `srv.Handle` 注册（绕过 Kratos 中间件链 ——
探针不带 token，JWT 中间件会把它们 401 掉）：

- `/healthz` —— **liveness**。只报进程活着，**不碰任何依赖**。依赖挂了绝不能
  让它失败：重启 pod 解决不了 MySQL 宕机，只会把降级变成 CrashLoopBackOff，
  故障面更大。
- `/readyz` —— **readiness**。探依赖，**硬**依赖挂了返 503，让 K8s 把这个 pod
  从 Service 的 endpoint 里摘掉。

共享实现在 `chatroom/pkg/health`；各服务在 `internal/data/health.go` 里声明自己的依赖：

```go
c.Add("mysql", true, d.PingMySQL)   // 硬依赖
c.Add("redis", false, d.PingRedis)  // 软依赖
```

硬软只看一个问题：**这个依赖挂了，服务还能不能正确处理请求？**

- 硬 → readiness 返 503（pod 离开 endpoints）。
- 软 → readiness 仍返 200，状态是 `degraded`。
- MySQL 对 user / friend / conversation / message 是**硬**依赖：没有它什么都读不了写不了。
- Redis 对这四个服务是**软**依赖：限频、黑名单、缓存、广播都是降级放行。
  标成硬依赖会让 Redis 一抖就把所有服务的 pod 全摘光，故障面比降级本身大得多。
- Redis 对 ws-gateway 是**硬**依赖：Pub/Sub 是消息投递给 WS 客户端的唯一通道。
  带着死掉的订阅继续接新连接，只会让客户端永远等不到消息。快速失败更好。

分类有测试守着（`<服务>/internal/data/health_test.go`）—— 把 Redis 改成硬依赖
是回归，不是微调。

清单里的探针：`readinessProbe` → `/readyz`，`livenessProbe` 和 `startupProbe`
→ `/healthz`。探针的 `timeoutSeconds` 要大于 `pkg/health` 的单项超时（2s），
否则探针先放弃、服务端还在探。

### 各层职责

**service（DTO ↔ DO）**

- `convert<资源>` 把进来的 proto 解析成 DO。反方向在 return 处就地构造 ——
  返回类型按 proto 声明：一般是资源本身（`return &v1.<资源>{...}, nil`），
  有时是列表包装（`*v1.<资源s>Set`），删除是 `&emptypb.Empty{}`。
  就地构造让每个 handler 自包含。
- 内嵌 `Unimplemented<资源>ServiceServer`。
- 列表请求用 `filtering` / `ordering` / `pagination` 解析；部分更新用
  `fieldmask.Update`。
- 在 service 边界校验输入，再交给 usecase。
- 返 `biz` 的错误。不写业务规则，不碰存储，不碰 PO。

**biz（只有 DO）**

- 拥有 DO（`type <资源> struct` —— 无 proto，无存储 tag）、usecase、
  repo 接口（`type <资源>Repo interface`）。
- 拥有用 `errors.NotFound` / `errors.BadRequest` 加 API 错误原因枚举造的 typed error。
- 拥有 `ListOption` 辅助 —— `ListFilter`、`ListOrderBy`、`ListOffset`、
  `ListLimit` —— 调用方组查询时不会漏出存储原语。

**data（DO ↔ PO）**

- _Repo 形状_：实现 `biz.<资源>Repo`。构造函数返回接口，不返回具体类型：
  `func New<资源>Repo(d *Data) biz.<资源>Repo`。
- _PO 与转换_：存储形状跟 DO 不一致时才定义 PO。PO 类型留在 `data` 里。
  用自由函数 `new<资源>`（DO → PO，写）和 `toBiz`（PO → DO，读）。驱动专有的
  builder 类型不出 `data`。
- _共享客户端_：`*Data`（在 `internal/data/data.go` 声明）持有长生命周期存储客户端。
  repo 接收 `*Data`，绝不自己建客户端。
- _查询_：在 repo 里把 `ListOptions.Filter` 和 `ListOptions.OrderBy` 翻译成
  存储驱动的查询语言。
- _错误_：把驱动错误映射成 `biz` typed error，上层永远不按驱动分支。

**server**

- 建 HTTP/gRPC server、装中间件、注册服务。不做转换，不写业务逻辑。

### 新增一个资源的清单

1. **DTO**：在域的契约包里（`api/<域>/service/v1/`）定义 `Create<资源>` /
   `Get<资源>` / `List<资源s>` / `Update<资源>` / `Delete<资源>`，然后 `make api`
   （或在契约目录里跑 `make api`）。
2. **DO + repo 接口**：都在 `biz` 里声明，usecase 建在接口之上。
3. **repo 实现**：在 `data` 里实现并返回 `biz.<资源>Repo`；存储形状跟 DO 不一致时
   加上 PO 和对应的转换函数。
4. **接线**：repo 构造函数进 `data.ProviderSet`，usecase 进 `biz.ProviderSet`，
   service 进 `service.ProviderSet`；HTTP/gRPC 服务在 `internal/server` 注册。
5. **重新生成**：`make all` 刷新 Wire 和 `go.mod`。

### 测试切口

测试放在被测代码旁边（`*_test.go`）。各层单独测：service 测试 fake usecase，
biz 测试 fake repo，data 测试在存储边界上跑 repo 实现。

## 生成与生成物

用 `make api`、`make config` 或 `make all` 重新生成；绝不要手改 `*.pb.go`、
`*_grpc.pb.go`、`*_http.pb.go`、`wire_gen.go`。

- 契约 proto 用 **protoc** 生成，不用 buf（`cd api/<域>/service && make api`，
  调 `api/gen.sh`）。契约要从仓库根 import
  `third_party/google/api/annotations.proto`，而 buf v2 的 module 根是单一目录，
  跨目录引用会被拒（`is outside the context directory`），`deps:` 又只接受 registry 名。
  protoc 支持多个 `--proto_path`，官方 `go-kratos/beer-shop` 就是这么做的。
  插件版本在 `api/gen.sh` 里用 `@version` 钉死，编译产物按版本组合缓存到
  `.cache/protoc-plugins/`。
- 配置 proto 也用 **protoc**（`cd app/<服务>/service && make config`，调
  `app/gen-conf.sh`）。两个脚本都从仓库根跑 protoc，注册路径带上域名 / 服务名 ——
  为什么必须这样见上面「Protobuf 全局注册表」。
- `make api` 在**契约包**目录（`api/<域>/service/`），不在服务目录。
- 仓库根的 `Makefile` 聚合所有服务和契约的
  `api` / `build` / `test` / `vet` / `config` / `proto` / `wire` / `all`。
- kratos CLI（`kratos proto server`）在**服务目录里**跑，指向契约 proto 路径 ——
  从 `app/<服务>/service/` 往上三层：
  `kratos proto server ../../../api/<域>/service/v1/<域>.proto`。
  它写 `internal/service/<域>.go` 骨架（文件已存在时跳过）。

## 命名与错误原因

- 资源：`<资源>`（如 `Todo`）；集合 RPC：`List<资源s>`。
- 类型：repo `<资源>Repo`，usecase `<资源>Usecase`，service `<资源>Service`。
  PO 类型放在 `internal/data/` 里，名字贴合存储驱动，用
  `new<资源>(do)` / `toBiz(po)` 自由函数转换。
- 错误原因：在契约包 `api/<域>/service/v1/error_reason.proto` 里声明，
  在 `biz` 里以 `Err<资源><原因>` 暴露。

## 提交与安全

- Conventional Commits：`feat:`、`fix:`、`refactor:`、`chore(deps):`、
  `docs:`、`test:`。生成物跟源文件放同一个 commit。
- 绝不在 `configs/config.yaml` 里提交真实凭证。
