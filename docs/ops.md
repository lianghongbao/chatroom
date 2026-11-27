# 配置与运维

健康检查、可观测性、连接池、部署这几件事都归运维，本文是它们的合集。

相关：[README](../README.md) ｜ [生产部署手册](../deploy/README.md)

## 配置加载顺序

每个服务读**两个**文件，都必须存在：

| 文件 | 装什么 | 进 nacos 吗 |
|------|--------|------------|
| `app/<服务>/service/configs/bootstrap.yaml` | `registry.nacos` —— 连哪个 nacos、哪条 dataId | **不进** |
| `app/<服务>/service/configs/config.yaml` | `server` / `data` / `auth` / `observability` | **进，原样推** |

拆开是为了断掉循环依赖：业务配置在 nacos 上，所以「连哪个 nacos」必须在读它之前
就知道。两者放一个文件，nacos 上会存着自己地址的一份拷贝，那份陈旧的拷贝会悄悄
盖掉本地刚改的新值。

运行时默认路径是**相对服务目录**的，`make run` 直接用默认值；Docker 里用
`-conf /data/conf/config.yaml -bootstrap /data/conf/bootstrap.yaml` 覆盖。

配置值按 **file < nacos < env** 合并（后面的赢）：

- **file** —— `app/<服务>/service/configs/config.yaml`，业务默认值 + `${ENV}` 占位符
- **nacos** —— `chatroom-<服务>.yaml`，各环境的运行时参数
- **env** —— 密钥、DSN 等部署时注入的东西

yaml 里的 `${VAR}` 由 Kratos 拿合并后的 map 解析，本地文件写的占位符照样生效。
`bootstrap.yaml` 不参与这个合并 —— 它在单独的第一遍里被读，用来定位 nacos。

本地文件和 nacos 那份是同一份配置的两份拷贝，用工具同步：

```bash
cd app/user/service
make nacos-config          # 对比本地和 nacos，有差异时退出码非 0
make nacos-config-push     # 把本地推到 nacos
```

推送脚本只推 `config.yaml`，并拒绝推还带着 `registry` 键的文件。

nacos 上改完配置**不会热更新** —— 服务只在启动时读一次配置，改完要重启。

## 健康检查

每个服务暴露两个端点，语义完全不同 —— 混用会直接制造故障：

| 端点 | 语义 | 探什么 | 失败后果 |
|---|---|---|---|
| `/healthz` | **liveness** | 只看进程是否活着，**不碰任何依赖** | 依赖挂了也**不能**失败：重启 pod 解决不了 MySQL 宕机，只会把 pod 搞成 CrashLoopBackOff |
| `/readyz` | **readiness** | 关键依赖 | 硬依赖挂了返 **503**，K8s 把这个 pod 从 Service 的 endpoint 里摘掉 |

两个端点都用 `srv.Handle` 注册 —— **绕过 Kratos 中间件链**。用 `srv.Route`
（proto 服务的注册方式）会走 JWT 鉴权中间件，探针不带 token 会被 401 挡掉。

实现在 **`chatroom/pkg/health`**（跨服务统一语义），各服务在
`internal/data/health.go` 里声明自己的依赖：

```go
c.Add("mysql", true, d.PingMySQL)   // 硬依赖
c.Add("redis", false, d.PingRedis)  // 软依赖
```

硬软只看一个问题：**这个依赖挂了，服务还能不能正确处理请求**。

| 服务 | 硬依赖 | 软依赖 |
|---|---|---|
| user / friend / conversation / message | MySQL | Redis（限频、黑名单、缓存、广播） |
| ws-gateway | Redis（Pub/Sub 是消息投递到 WS 客户端的**唯一**通道） | —— |

**别把 Redis 标成硬依赖**：Redis 一抖，四个服务的 pod 会被全摘光。也别把 MySQL
标成软依赖：那会让流量继续打进一个连不上库的 pod。这两条判定都有测试守着
（`app/<服务>/service/internal/data/health_test.go`）。

响应体：

```json
{"status":"ok","checks":{"mysql":{"status":"ok","latency_ms":1},"redis":{"status":"ok","latency_ms":0}}}
```

`status` 三档：`ok` / `degraded` / `failed`。

- `failed` —— 硬依赖挂了，readiness 返 **503**。
- `degraded` —— 只有软依赖挂了，readiness 仍返 **200**，服务继续接流量。

探测走**原生连接池**（`*sql.DB.PingContext` / `redis.Ping`）：ent 生成的 client
没有 Ping 方法，所以 `data` 层自己开一条 `*sql.DB` 交给 ent
（`ent.Driver(entsql.OpenDB(...))`），两者共用同一个连接池。

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

K8s 里由 ConfigMap 注入：

```yaml
METRICS_ADDR: "0.0.0.0:9100"
OTEL_EXPORTER_OTLP_ENDPOINT: ""   # 留空 = 不开 trace（集群里要先有 collector）
```

Pod 上带 Prometheus 自动发现的注解：

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

- **metrics 端口在容器里固定 9100**，跟业务端口（8000-8004 / 9000-9004）分开。
  改这个端口要同步改 Deployment 的 `containerPort`、Pod 注解的 `prometheus.io/port`
  和 ConfigMap 的 `METRICS_ADDR`。
- **空值 = 关闭**，不是「用默认地址」。本地 `make run` 五个服务同时起，
  默认全开会撞端口。

## 数据库连接池

`database/sql` 的默认值在生产上不安全，所以每个服务的配置里都显式写了：

| 配置项 | 环境变量 | 默认 | 不配会怎样 |
|---|---|---|---|
| `max_open_conns` | `DB_MAX_OPEN_CONNS` | 10 | **无限** —— 足以打爆 MySQL 的 `max_connections`（默认 151） |
| `max_idle_conns` | `DB_MAX_IDLE_CONNS` | 5 | 2 —— 高并发下连接被反复建立 / 销毁 |
| `conn_max_lifetime` | `DB_CONN_MAX_LIFETIME` | 1800s | **永不复用** —— MySQL 代理重启后池里的老连接已死，表现为「偶发 500」 |
| `conn_max_idle_time` | `DB_CONN_MAX_IDLE_TIME` | 300s | 0（不回收） |

解析和兜底在 **`chatroom/pkg/dbpool`**：`<= 0` 和负数都回落到默认值。

⚠️ **连接额度是所有服务共享的一个池子**：5 个服务 × 最多 10 副本 × 10 连接 = 500 条。
MySQL 的 `max_connections` 要配得比这大（默认 151 不够）。调大前先算总额度。

> Duration 用 **proto 格式**（`1800s` = 30 分钟），不能写 `30m` —— protojson 只认
> s / ms / us / ns。bool 同理不能走 `${VAR}` 占位符。

## 部署

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

生产部署（compose / K8s / CI）的完整手册见
[`deploy/README.md`](../deploy/README.md)。
