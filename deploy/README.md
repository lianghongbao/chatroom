# chatroom 生产部署

chatroom 是一个基于 Kratos v3 的即时通讯后端，按领域拆成五个独立服务。
这份文档讲怎么把它部署到一台 ECS 上、怎么配置、怎么运维。

> **第一次部署**看第 2 章 ｜ **配置细节**看第 3 章 ｜ **日常操作**看第 4 章
> ｜ **会踩的坑**看第 5 章 ｜ **出问题了**看第 6 章

相关文档：分层契约与跨服务语义见 [`CLAUDE.md`](../CLAUDE.md) ｜ CI 流水线见
[`.github/workflows/README.md`](../.github/workflows/README.md) ｜ 本地开发见
[`README.md`](../README.md)

---

## 1. 总览

### 1.1 服务构成

| 服务 | 职责 |
|---|---|
| `chatroom-user` | 注册登录、token 签发与轮换、用户资料、账号状态 |
| `chatroom-friend` | 好友申请 / 接受 / 拒绝 / 移除、好友列表 |
| `chatroom-conversation` | 单聊 / 群聊、成员增删、成员列表、群主转让 / 解散、未读数 |
| `chatroom-message` | 消息落库、历史分页、已读 / 未读 |
| `chatroom-ws-gateway` | WebSocket 长连接、在线状态、消息广播 |

技术栈：Kratos v3（service / biz / data 三层 + Wire 注入）、Protobuf 契约先行
（`api/<域>/service/v1/`）、ent ORM、Redis（缓存 / 限频 / token 黑名单 /
Pub/Sub）、Nacos 3.x（注册发现 + 配置中心）、JWT 双 token、gorilla/websocket。

五个进程共用 MySQL、Redis、Nacos。其中 **MySQL / Redis 由别的项目独立部署**，
本 compose 只跑 nacos + 五个业务容器。

### 1.2 术语

| 词 | 是什么 | 归谁管 |
|---|---|---|
| 会话（conversation） | 一组人之间的聊天上下文。单聊 2 人、群聊多人群，群聊有群主 | conversation |
| 成员（member） | 会话里的人。**成员身份由 conversation 服务裁定** | conversation |
| 未读数（unread） | 某人在某会话里没读的条数 | conversation |
| 好友（friend） | 好友关系是单向申请记录 + 状态流转，跟会话成员不是一回事 | friend |
| 在线状态（presence） | 用户此刻有没有连着网关 | ws-gateway |
| 广播（broadcast） | 消息落库后往 Redis Pub/Sub 发一条，各网关实例推给本机上的目标连接 | message → ws-gateway |

### 1.3 部署拓扑

```
┌──────────────────────────────────────────────────────────┐
│  data-net（外部网络，由数据存储的 compose 创建）           │
│                                                          │
│    mysql:3306  ──→ chatroom 库（utf8mb4 / utf8mb4_bin）   │
│    redis:6379  ──→ DB 2，key 前缀 chatroom:               │
└──────────────────────────────────────────────────────────┘
                          ▲
        DB_SOURCE / REDIS_ADDR 跨网络访问（MySQL / Redis 由别的项目提供）
                          │
┌─────────────────────────┴────────────────────────────────┐
│  chatroom（compose project: chatroom，目录 deploy/）     │
│                                                          │
│    chatroom-nacos          8848 / 9848 / 9849            │
│    chatroom-user           8000 / 9000                   │
│    chatroom-friend         8001 / 9001                   │
│    chatroom-conversation   8002 / 9002                   │
│    chatroom-message        8003 / 9003                   │
│    chatroom-ws-gateway     8004（只有 HTTP + WS）         │
└──────────────────────────────────────────────────────────┘
```

| 容器名 | 暴露协议 | 宿主端口 | 镜像 |
|---|---|---|---|
| `chatroom-nacos` | 控制台 + gRPC | 8848 / 9848 / 9849 | `${ACR_REGISTRY_HOST}/${ACR_NAMESPACE}:${NACOS_IMAGE_TAG}` |
| `chatroom-user` | HTTP + gRPC | 8000 / 9000 | `${IMAGE_PREFIX}/chatroom-user:${IMAGE_TAG}` |
| `chatroom-friend` | HTTP + gRPC | 8001 / 9001 | `${IMAGE_PREFIX}/chatroom-friend:${IMAGE_TAG}` |
| `chatroom-conversation` | HTTP + gRPC | 8002 / 9002 | `${IMAGE_PREFIX}/chatroom-conversation:${IMAGE_TAG}` |
| `chatroom-message` | HTTP + gRPC | 8003 / 9003 | `${IMAGE_PREFIX}/chatroom-message:${IMAGE_TAG}` |
| `chatroom-ws-gateway` | HTTP + WebSocket | 8004 | `${IMAGE_PREFIX}/chatroom-ws-gateway:${IMAGE_TAG}` |

每个业务服务都是 **HTTP / gRPC 双端口**：`800x` 是 HTTP（对外），`900x` 是
gRPC（服务间调用）。`ws-gateway` 例外 —— 它只起 HTTP server，`9004` 在配置里
但**不监听**，网关对其它服务是客户端而不是服务端。

nacos 的三个端口映射到宿主，只是为了让运维能直接访问控制台、用 curl 查；
业务容器之间一律走 `chatroom-nacos:8848`（容器名），不经过宿主端口。

### 1.4 服务依赖

启动顺序由 compose 的 `depends_on` + `condition: service_healthy` 表达 ——
谁必须先 `/readyz` 通过，谁才能起：

```
nacos        → user, friend, conversation, message, ws-gateway
user         → friend, conversation
conversation → message, ws-gateway
message      → ws-gateway
```

服务之间的调用则是**代码里写死的**（`internal/data/remote_*.go`），不是 compose 配的：

| 调用方 | 被调方（discovery 名） | 用途 | 断了会怎样 |
|---|---|---|---|
| friend | `chatroom-user.grpc` | 拉好友的公开资料（昵称/头像） | 降级：昵称头像为空，好友列表仍返回 |
| conversation | `chatroom-user.grpc` | 补全会话成员昵称/头像 | 降级：成员资料为空，列表仍返回 |
| message | `chatroom-conversation.grpc` | 校验成员身份、取未读数 | **拒绝发消息**（fail closed） |
| ws-gateway | `chatroom-conversation.grpc` | 算广播目标成员列表 | 消息发得出去但**推不出去** |
| ws-gateway | `chatroom-message.grpc` | 转发消息落库 | 客户端收到 `SEND_FAILED` |

失败语义不一致是刻意的：挡住业务比放进幽灵成员更糟，挡住写入比假装成功更糟。
细节见 [`CLAUDE.md`](../CLAUDE.md) 的「跨服务降级契约」。

一条消息从客户端到客户端的完整路径：

```
ws-gateway --/ws?token=<JWT>--> 客户端
     │  握手：先限流 → 再验 JWT → 原子预留连接配额 → Upgrade
     v
  ws-gateway ──gRPC──> message（落库）
                         │
                         v
             Redis Pub/Sub: chatroom:msg:{conversationId}
                         │
                         v
                ws-gateway（PSubscribe chatroom:msg:*）
                         │
                         v
                     推给该会话的其它成员
```

网关**不落库**，只维护连接和转发。WS 端点是 `/ws`，token 走 query 参数
（浏览器 WebSocket API 不能自定义 header）。

### 1.5 前置条件

| 项 | 要求 | 备注 |
|---|---|---|
| ECS | Linux + Docker + `docker compose` **v2** | v1 只有 `docker-compose` 子命令 |
| 数据存储 | MySQL 8.0 + Redis 7.0 容器已起，且接在 `data-net` 上 | 见 §2.1 |
| 镜像仓库 | ACR 里有 nacos 镜像 | `nacos-3.2.4`，或改用 Docker Hub 官方镜像 |
| nacos 命名空间 | 一个自定义 namespace id | 与本地开发用的 uuid 命名空间**分开** |

---

## 2. 首次部署

六步，按顺序做。每一步都有对应的验收命令。

### 2.1 数据存储先就位

MySQL / Redis 是独立部署的服务，由提供它们的项目负责。本项目只连，不管生命周期。

```bash
# 容器名必须是 mysql / redis —— chatroom 的 compose 用这两个名字解析
docker ps --format '{{.Names}}' | grep -E '^(mysql|redis)$'

# 两者都要接在 data-net 上
docker network inspect data-net --format '{{range $k,$v := .Containers}}{{$v.Name}} {{end}}'
# 期望看到：mysql redis
```

起法（在提供数据存储的项目里跑，路径按实际调整）：

```bash
cd ../local-life-api/deploy/mysql
cp .env.example .env && vi .env && chmod 600 .env
docker compose up -d

cd ../redis
cp .env.example .env && vi .env && chmod 600 .env
docker compose up -d
```

### 2.2 填本项目的 `deploy/.env`

```bash
cd deploy
cp .env.example .env
vi .env
chmod 600 .env
```

`.env` 在 `.gitignore` 里，不会进版本库。逐项说明：

| 变量 | 填什么 | 约束 |
|---|---|---|
| `MYSQL_ROOT_PASSWORD` | 与数据存储 `.env` **完全一致** | 不一致 → 建库失败 / 连不上 |
| `REDIS_PASSWORD` | 与数据存储 `.env` **完全一致** | Redis 开了 `requirepass` |
| `REDIS_DB` | `2` | chatroom 独占这个号，跟同实例上别的应用隔离 |
| `NACOS_SERVERS` | `chatroom-nacos:8848` | 容器名，不是 localhost |
| `NACOS_NAMESPACE` | 命名空间 id | 必须**已在 nacos 控制台建好**。留 `CHANGE_ME` 不会立刻报错，但控制台里看不到这批服务 |
| `NACOS_GROUP` | `DEFAULT_GROUP` | 跟 `bootstrap.yaml` 的默认值一致 |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | nacos 账号 | 跟 nacos 容器里的管理员一致 |
| `NACOS_AUTH_TOKEN` | `openssl rand -hex 32` | **必须换掉默认值**，否则等于没设防 |
| `AUTH_JWT_SECRET` | `openssl rand -hex 32` | **五个服务必须一致** |
| `AUTH_INTERNAL_SECRET` | `openssl rand -hex 32` | **五个服务必须一致**，不一致 → 内部端点一律 403 |
| `IMAGE_PREFIX` / `IMAGE_TAG` | `chatroom` / `server-latest` | CI 按这两个值 re-tag 镜像 |
| `ACR_REGISTRY_HOST` / `ACR_NAMESPACE` | ACR 控制台的值 | 留着占位符 → compose 报 `invalid reference format` |
| `NACOS_IMAGE_TAG` | `nacos-3.2.4` | 换 Docker Hub 官方镜像时改成 `v3.2.4` |
| `NACOS_HOST_PORT` / `NACOS_GRPC_HOST_PORT` / `NACOS_GRPC2_HOST_PORT` | `8848` / `9848` / `9849` | 宿主端口冲突时改这里，容器内端口不变 |
| `REGISTRY_USERNAME` / `REGISTRY_PASSWORD` | ACR 的 RAM 账号 | **ECS 拉镜像**用；跟 CI 推镜像那套是两套凭证 |

两个密钥各自生成：

```bash
openssl rand -hex 32
```

`deploy.sh` 用 `source .env` 读值，值里有空格或特殊字符时要用引号包住。
`openssl rand -hex 32` 出来的是纯 hex，不需要引号。

### 2.3 业务配置推进 nacos

五个服务的业务配置在 nacos 配置中心（`configs/config.yaml` 原文）。nacos 上还没有
对应 dataId 时，服务会**静默回落到镜像里的本地文件** —— 不报错，但线上改配置
不生效。所以这一步不能跳。

```bash
cd app/user/service           && make nacos-config-push
cd ../../friend/service       && make nacos-config-push
cd ../../conversation/service && make nacos-config-push
cd ../../message/service      && make nacos-config-push
cd ../../ws-gateway/service   && make nacos-config-push
```

dataId 命名规则是 `chatroom-<服务名>.yaml`（`chatroom-user.yaml`、
`chatroom-friend.yaml` …）。服务端和脚本用的是同一套回退规则：
`bootstrap.yaml` 里 `data_id` 留空 → 用 `<service_name>.yaml`。

推之前可以先对比：

```bash
cd app/user/service && make nacos-config
# 有差异时退出码非 0 —— 用来在提交前发现「改了本地没推 nacos」
```

脚本依赖 python3 + PyYAML。PATH 里的 python3 没装 PyYAML 时（uv / conda 管的
常见）会报 `No module named "yaml"` —— 系统的 `/usr/bin/python3` 有：

```bash
PATH="/usr/bin:$PATH" make nacos-config-push
```

### 2.4 起服务

```bash
cd deploy
./deploy.sh up
```

**不要用裸 `docker compose up -d`** —— 脚本比它多做了四件事，每件都是踩过坑的：

1. **幂等补建 `data-net`**。外部网络不会随 compose 自动创建。
2. **建 `chatroom` 库**。ent 的 `auto_migrate` 只建表不建 schema，库得先在。
   字符集 `utf8mb4`、排序规则 `utf8mb4_bin`（大小写敏感）。
3. **先单独起 nacos 并等它健康**。业务服务连不上 nacos 就注册不了，
   下游 discovery 查不到上游，直接启动失败。
   等待上限 **30 分钟**（600 × 3s）—— nacos 3.x 是 Spring Boot 应用，
   2 核 4G 机器上实测要 9 分多钟才开始监听端口，早期版本等 60 秒太短。
4. **建 nacos 管理员并校验登录**。nacos 3.x 开了 `NACOS_AUTH_ENABLE` 后
   **首次启动不会自动建管理员** —— 没有管理员时所有 API 返 401
   （`User not found!`），业务服务读 nacos 配置那一步直接 panic，
   表现为容器反复 `Restarting`。脚本建完还会再登录校验一次，
   登录不上就直接退出，不带着坏状态往下走。

`deploy.sh` 会**连跑两次** `up -d` —— 首次创建时上游容器刚建好、healthcheck
还没通过，下游会停在 `Created`，compose 不会自己重试判定，再跑一次就全通了。

### 2.5 验收

下面的命令要用到 `.env` 里的凭证，先加载进当前 shell：

```bash
cd deploy
set -a && source .env && set +a
```

```bash
# 1. 六个容器都 Up
docker compose ps

# 2. 两个端点语义要对：/healthz 只报活着，/readyz 探依赖
docker exec chatroom-user curl -sf http://localhost:8000/healthz
docker exec chatroom-user curl -sf http://localhost:8000/readyz
docker exec chatroom-ws-gateway curl -sf http://localhost:8004/readyz
```

`/readyz` 返回体里 `status` 三态：

| status | 含义 | 容器会怎样 |
|---|---|---|
| `ok` | 依赖全好 | 正常接流量 |
| `degraded` | 只有**软**依赖挂了 | 仍然 200，照常接流量 |
| `failed` | **硬**依赖挂了 | 503，容器退出 healthy |

返回体（普通 JSON，不是 protojson）：

```json
{
  "status": "ok",
  "checks": {
    "mysql": { "status": "ok", "latency_ms": 1 },
    "redis": { "status": "ok", "latency_ms": 0 }
  }
}
```

```bash
# 3. nacos 上注册了 9 个服务名
TOKEN=$(docker exec chatroom-nacos curl -s -X POST \
  "http://localhost:8848/nacos/v3/auth/user/login" \
  --data-urlencode "username=$NACOS_USERNAME" --data-urlencode "password=$NACOS_PASSWORD" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")

docker exec chatroom-nacos curl -s \
  "http://localhost:8848/nacos/v3/admin/ns/service/list?pageNo=1&pageSize=30&namespaceId=$NACOS_NAMESPACE" \
  -H "accessToken: $TOKEN"
```

期望 9 条，命名带**协议后缀**：

```
chatroom-user.http          chatroom-user.grpc
chatroom-friend.http        chatroom-friend.grpc
chatroom-conversation.http  chatroom-conversation.grpc
chatroom-message.http       chatroom-message.grpc
chatroom-ws-gateway.http                          ← 网关只有 HTTP
```

后缀是 kratos 的 nacos registry 加的（服务名 + 端点协议）。跨服务调用因此
必须写成 `discovery:///chatroom-user.grpc` —— 少了 `.grpc` 后缀，
gRPC 会报 `produced zero addresses`。

```bash
# 4. Redis 隔离：chatroom 独占 DB 2，key 再带 chatroom: 前缀
docker exec redis redis-cli -a "$REDIS_PASSWORD" -n 2 --scan --pattern 'chatroom:*' | head

# 别的应用在别的 DB 号上，两边互不干扰
docker exec redis redis-cli -a "$REDIS_PASSWORD" -n 0 --scan --pattern '*' | sort | head
# 期望扫不到 chatroom: 的 key
```

### 2.6 端到端冒烟

`/readyz` 只证明依赖探得到，证明不了业务链路通。下面这段把 user → friend →
conversation → message 四个服务和三条跨服务调用全走一遍。

```bash
API=http://localhost:8000; FRIEND=http://localhost:8001
CONV=http://localhost:8002; MSG=http://localhost:8003
PWD='Smoke@123456'

reg() { curl -sf -X POST $API/api/user/register -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$PWD\",\"nickname\":\"$1\"}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])'; }
login() { curl -sf -X POST $API/api/user/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$PWD\"}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["accessToken"])'; }

# ① 注册两个用户（uid 落 MySQL），登录拿 token（refresh token 落 Redis）
UID_A=$(reg smoke_a); UID_B=$(reg smoke_b)
TOKEN_A=$(login smoke_a); TOKEN_B=$(login smoke_b)

# ② A 向 B 发好友申请 —— 打 friend 服务
REQ_ID=$(curl -sf -X POST $FRIEND/api/friend/apply \
  -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
  -d "{\"target_id\":\"$UID_B\"}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["requestId"])')

# ③ B 同意
curl -sf -X POST $FRIEND/api/friend/accept \
  -H "Authorization: Bearer $TOKEN_B" -H 'Content-Type: application/json' \
  -d "{\"request_id\":$REQ_ID}" > /dev/null

# ④ A 的好友列表 —— nickname 是 friend 跨服务调 user 补的，有值就说明链路通
curl -sf $FRIEND/api/friend/list -H "Authorization: Bearer $TOKEN_A"

# ⑤ 建单聊 —— conversation 跨服务调 user 补成员资料
CONV_ID=$(curl -sf -X POST $CONV/api/conversation/create \
  -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
  -d "{\"member_ids\":[\"$UID_B\"]}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["conversation"]["id"])')

# ⑥ 发消息 —— message 跨服务调 conversation 校验成员身份
curl -sf -X POST $MSG/api/message/send \
  -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' \
  -d "{\"conversation_id\":$CONV_ID,\"content\":\"冒烟\"}"
```

响应是 protojson，字段名是 **lowerCamelCase**（`requestId` / `conversation.id` /
`accessToken`），不是 proto 里的 snake_case。注册约束：用户名非空且 ≤ 36 字符，
密码 6–72 字符 —— 超过 72 会被 bcrypt 拒。

哪一步失败就说明哪条链路没通：

| 失败点 | 大概率原因 |
|---|---|
| ① 注册 | user 服务连不上 MySQL；或用户名已存在 |
| ④ 好友列表 nickname 为空 | friend → user 跨服务调用降级了（friend 侧日志有「好友资料补全降级」的 warn） |
| ⑤ 建会话失败 | conversation → user 调用失败；或 member_ids 少于 2 人 |
| ⑥ 发消息失败 | message → conversation 校验成员身份失败（**不是**降级，是拒绝） |

ws-gateway 只有一个 WS 端点，没有 HTTP 业务接口 —— 它要真实客户端才能验，
见 `chatroom-web` 仓库的前端。

> `smoke_a` / `smoke_b` 是真实用户，会留在 `users` 表里。验收完想清掉：
> `docker exec mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "DELETE FROM chatroom.users WHERE username IN ('smoke_a','smoke_b')"`
> （外键不建，关联的 `friends` / `conversations` / `messages` 记录要一并清。）

---

## 3. 配置与运行时

### 3.1 两份配置文件

每个服务读**两个**配置文件，缺一个就 panic：

| 文件 | 内容 | 在哪 |
|---|---|---|
| `configs/bootstrap.yaml` | 只回答「连哪个 nacos、哪条 dataId」 | 镜像 `/data/conf/bootstrap.yaml`，**永远不进 nacos** |
| `configs/config.yaml` | 业务参数 | 镜像 `/data/conf/config.yaml`，**同时**在 nacos 上 |

拆成两个是为了打破循环依赖：配置中心里的业务配置依赖「连哪个 nacos」，
这个事实不能由配置中心自己决定。`bootstrap.yaml` 里的 `service_name`
必须是本服务专属的（`chatroom-user` 等）—— 照抄别的服务的文件，
这个进程就会注册成错的名字。

加载分两阶段（`cmd/server/main.go`）：

```
1. 读 bootstrap.yaml + env            →  拿到 nacos 地址 / namespace / group / dataId
2. 读 config.yaml + nacos(同一条 dataId) + env  →  合并成最终配置
```

优先级从低到高：**文件 < nacos < 环境变量**。合并是叶子级的深合并，
后加载的源覆盖先加载的同名叶子。

配置里写 `${VAR}` 或 `${VAR:默认值}` 占位符，由 Kratos 在合并完成后统一展开：

- 取值来自**进程环境变量**；取不到就用 `:` 后面的默认值，取不到默认就是空串。
- **Duration 必须用 proto 格式**（`1800s`），不能写 `30m` —— protojson 只认
  s / ms / us / ns。
- **bool 不能用 `${VAR}` 占位符** —— protojson 拒绝字符串形式的布尔值。
- 敏感字段（DB / Redis / nacos 凭证 / JWT 密钥）全部走 env 注入，
  镜像里的 yaml 只有非敏感默认值。

### 3.2 可观测性

生产 compose 没有注入 `OTEL_EXPORTER_OTLP_ENDPOINT`，所以 **trace 和 metrics
默认都不开**。两者的开启方式不一样：

| 能力 | 配置项 | 怎么开 |
|---|---|---|
| trace | `observability.tracing_endpoint` | 是 `${OTEL_EXPORTER_OTLP_ENDPOINT:""}` 占位符 —— 注入 env 即可 |
| metrics | `observability.metrics_addr` | **写死的空串，不是占位符** —— env 改不了，只能改 nacos 上的配置 |

开 metrics 要三处同步，漏一个就变成「Prometheus 抓不到，且没人报错」：
`metrics_addr` 的监听地址、compose 里的容器端口映射、Prometheus 的 pod 注解。

exporter 起来后 `/metrics` 挂在 `metrics_addr` 指定的地址上（约定 9100，
跟业务端口 8000-8004 / 9000-9004 分开）。

### 3.3 健康检查

两个端点都用 `srv.Handle` 注册，**绕过 Kratos 中间件链** —— 探针不带 token，
走 JWT 中间件会被 401 掉。

| 端点 | 语义 | 探什么 |
|---|---|---|
| `/healthz` | liveness | 只报进程活着，**不碰任何依赖** |
| `/readyz` | readiness | 探依赖，硬依赖挂了返 503 |

硬 / 软的划分只看一个问题：**这个依赖挂了，服务还能不能正确处理请求？**

| 服务 | MySQL | Redis | 理由 |
|---|---|---|---|
| user / friend / conversation / message | **硬** | 软 | 没 MySQL 什么都读不了写不了；Redis 只影响限频、黑名单、缓存、广播，都是降级放行 |
| ws-gateway | —— | **硬** | Pub/Sub 是消息投递给客户端的唯一通道。带着死掉的订阅继续接连接，只会让客户端永远等不到消息 |

Redis 对前四个服务标软依赖是有意的：标硬的话，Redis 一抖就把所有服务的 pod
全摘光，故障面比降级本身大得多。分类有测试守着（`internal/data/health_test.go`）——
改成硬依赖是回归，不是微调。

探针超时必须**大于** `pkg/health` 的单项超时（2s），否则探针先放弃、服务端
还在探。compose 里配的是 `timeout: 3s`。

### 3.4 MySQL

| 项 | 值 |
|---|---|
| 库名 | `chatroom`（由 `deploy.sh` 建，`utf8mb4` / `utf8mb4_bin`） |
| 表 | `users` / `friends` / `conversations` / `conversation_members` / `messages` |
| 表结构权威 | **ent schema**。服务启动时 `auto_migrate` 增量对齐（只 ALTER，不删列） |
| 空库快照 | `app/<服务>/service/migrations/*.sql`、`deploy/db/01-create-tables.sql` |

空库快照用于「不依赖 Go 运行时也能建空库」的场景，**不会自动执行** ——
chatroom 的 compose 没有把它挂进 mysql 容器。要手工用：

```bash
docker exec -i mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" < deploy/db/01-create-tables.sql
```

正常路径不用管这个：`deploy.sh` 建库，服务启动时 ent 自动把表建齐。

字符集注意：库默认排序规则是 `utf8mb4_bin`（**大小写敏感**），
跟同实例上 `local_life` 库的 `utf8mb4_unicode_ci` 不同。用户名这种需要
大小写敏感的字段，表级也显式声明了 `COLLATE utf8mb4_bin`。

**连接额度是所有服务共享的一个池子**，不是每个服务各有一份：

```
5 个服务 × 最多 10 副本 × max_open_conns 10 = 500 条
```

MySQL 的 `max_connections` 必须配得比这大（数据存储的 compose 里已经是
`--max-connections=1000`；默认 151 不够）。每个服务的池参数在
`configs/config.yaml` 的 `data.database` 下显式写死，不依赖 `database/sql` 的
默认值 —— 默认的 `MaxOpenConns` 是无限、`ConnMaxLifetime` 是永不复用，
后者在 MySQL 代理重启后表现为零星 500。

### 3.5 Redis

| 项 | 值 |
|---|---|
| DB 号 | `2`（`REDIS_DB`），跟同实例上别的应用隔离 |
| key 前缀 | 全部 `chatroom:`（定义在 `pkg/keys`） |
| Pub/Sub | 频道名也带前缀：`chatroom:msg:{conversationId}`、`chatroom:member:invalidated` |

**Redis Pub/Sub 不按 DB 隔离** —— 频道名不带前缀的话，别的应用的广播会串进来。
这是 key 前缀必须覆盖频道名的原因。

Redis 对前四个服务是**软依赖**：不可达时限流放行、黑名单放行、缓存降级。
限流是保护措施不是正确性要求 —— Redis 抖一下把全部登录拒掉比不限流更糟。

### 3.6 对象存储（当前是关闭的）

头像、群头像、聊天图片走阿里云 OSS 直传。但**生产 compose 目前没有注入
`OSS_*` 环境变量**，所以 `storage` 段解析出来是空的
（`pkg/oss` 的 `validate` 要求 bucket / region / role_arn / AK / SK 齐全），
结果是**上传能力自动关闭**，其它功能不受影响。

要开启需要两步：

1. `deploy/.env` 里加 `OSS_ENDPOINT` / `OSS_BUCKET` / `OSS_REGION` /
   `OSS_ROLE_ARN` / `OSS_ACCESS_KEY_ID` / `OSS_ACCESS_KEY_SECRET` /
   `OSS_UPLOAD_HOST` / `OSS_URL_PREFIX`；
2. `deploy/docker-compose.yml` 的 `x-service-common.environment` 里把同样的
   变量传给容器（`user` / `conversation` / `message` 三个服务用得到）。

只改 `.env` 不改 compose 没用 —— 容器里没有这些环境变量。

---

## 4. 日常运维与发布

### 4.1 常用命令

```bash
cd deploy

./deploy.sh up        # 起（或改完配置后应用）
./deploy.sh status    # 状态
./deploy.sh logs      # 跟全部日志
./deploy.sh logs user # 跟单个服务
./deploy.sh down      # 全停（数据卷保留）
```

单个服务：

```bash
docker compose restart user          # 重启
docker compose logs -f user          # 跟日志
```

### 4.2 改配置

```bash
# 1. 改 app/<服务>/service/configs/config.yaml
# 2. 推进 nacos
cd app/user/service && make nacos-config-push
# 3. 重启让服务重新拉配置
cd ../../../deploy && docker compose restart user
```

`bootstrap.yaml` 不进 nacos，改了它要重启服务才生效（且它只决定「连哪个
nacos」，改动前先确认新地址可达）。

### 4.3 镜像与发布

五个服务各一个镜像，CI 里 build 完推 ACR，再部署到 ECS 的 self-hosted runner。
流水线细节（Vars / Secrets / 两个 job 的分工）见
[`.github/workflows/README.md`](../.github/workflows/README.md)。

镜像命名：

```
CI 推上去的：${ACR_REGISTRY_HOST}/${ACR_NAMESPACE}:chatroom-<服务>-<sha7>
              ${ACR_REGISTRY_HOST}/${ACR_NAMESPACE}:chatroom-<服务>-latest
ECS 上 compose 用的：${IMAGE_PREFIX}/chatroom-<服务>:${IMAGE_TAG}
```

deploy job 拉 `latest` 再 re-tag 成 compose 期望的名字。

构建上下文是**仓库根目录** —— 整仓一个 go module，服务依赖同仓的 `pkg/` 和
`api/`。本地手工构建：

```bash
cd app/user/service && make docker-build
# 等价于 docker build -t chatroom-user -f app/user/service/Dockerfile .
```

五个服务都要 build。

### 4.4 回滚

```bash
cd deploy
docker compose down
# 手动把镜像 tag 换回上一版后
docker compose up -d
```

ACR 上保留了 `chatroom-<服务>-<sha7>` 的不可变 tag，要回滚就把它 re-tag 成
compose 期望的 `server-latest`。

---

## 5. 注意事项

### 5.1 nacos 的数据在卷里

nacos 跑在 **standalone** 模式，元数据用**内嵌 derby**，不依赖 MySQL。
数据落在 `nacos-data` 卷，配置中心里推的业务配置也在里面。

```bash
docker volume ls | grep nacos-data
```

`./deploy.sh down` 保留卷。**`docker compose down -v` 会把它一起删掉** ——
注册表和配置中心的内容全丢，业务服务会静默回落到镜像里的本地配置
（不报错，但线上改配置不生效），得重新 `make nacos-config-push`。

### 5.2 容器日志没有轮转

compose 没配 `logging`，Docker 默认用 json-file 驱动且**不轮转** ——
日志量大的服务（ws-gateway 每次握手/断开都打 Info）跑久了会把磁盘撑满。
盯着磁盘：

```bash
docker ps -q --filter "name=chatroom-" | while read -r id; do
  name=$(docker inspect -f '{{.Name}}' "$id")
  size=$(du -h "$(docker inspect -f '{{.LogPath}}' "$id")" | cut -f1)
  echo "$name $size"
done
```

要治本就在 compose 的 `x-service-common` 里加 `logging`：

```yaml
  logging:
    driver: json-file
    options:
      max-size: "50m"
      max-file: "3"
```

### 5.3 网关多实例

ws-gateway 无状态，多实例靠 Redis Pub/Sub 广播，不共享连接。
同一台机器上跑多个网关实例时，端口必须错开 —— 改 compose 的两处：

```yaml
  ws-gateway:
    environment:
      GATEWAY_HTTP_ADDR: 0.0.0.0:8005   # 容器内监听端口
    ports:
      - "8005:8005"                      # 宿主映射跟着改
```

> `configs/config.yaml` 里写的是 `${GATEWAY_HTTP_ADDR:0.0.0.0:8004}`，但 compose
 的 `GATEWAY_HTTP_ADDR` 是**硬编码字面量**，不是占位符 —— 命令行传
 `GATEWAY_HTTP_ADDR=... docker compose up` 不会生效，compose 文件里的值优先。

多个实例建议用 compose override 文件，别直接改主文件。

---

## 6. 故障排查

### 6.1 症状速查

| 现象 | 原因 | 处理 |
|---|---|---|
| 容器反复 `Restarting`，日志是 nacos `401 User not found!` | nacos 3.x 开了鉴权但没建管理员 | `docker exec chatroom-nacos curl -s -X POST "http://localhost:8848/nacos/v3/auth/user/admin" --data-urlencode "username=..." --data-urlencode "password=..."`，然后重启业务服务 |
| 服务起不来，日志报 `stat configs/bootstrap.yaml: no such file` | 镜像里配置文件缺失 | 确认镜像是本仓库构建的；`bootstrap.yaml` 和 `config.yaml` 都在 `/data/conf/` |
| 业务容器起来了但配置改了不生效 | nacos 上没有对应 dataId，静默回落本地文件 | `make nacos-config-push` |
| `deploy.sh` 卡在「等待 nacos 健康」 | nacos 启动慢（2 核 4G 上要 9 分钟+） | 脚本已给到 30 分钟上限；`docker logs chatroom-nacos` 看是否真在起 |
| 建库失败 / `Access denied` | `MYSQL_ROOT_PASSWORD` 与数据存储不一致 | 对齐两边的 `.env` |
| 连不上 mysql / 容器起不来 | 不在同一个网络 | `docker network inspect data-net --format '{{range $k,$v := .Containers}}{{$v.Name}} {{end}}'` |
| 跨服务调用超时 / 内部端点全 403 | `AUTH_INTERNAL_SECRET` 五个服务不一致，或 `AUTH_JWT_SECRET` 不一致 | 对齐 `deploy/.env` 后重启全部服务 |
| 跨服务调用报 `produced zero addresses` | discovery 名对不上 —— 调用方把服务名**写死在代码里**（`discovery:///chatroom-user.grpc`） | 改了 `NACOS_SERVICE_NAME` 的话，调用方代码里的名字也要同步改并重新构建 |
| 消息发得出去但客户端收不到 | ws-gateway 的 Redis 订阅断了，或拿不到成员列表 | `docker exec chatroom-ws-gateway curl -sf http://localhost:8004/readyz` 看 redis 这个硬依赖是否挂了；查 ws-gateway 日志里的「查会话成员失败」 |
| 客户端连不上 WS，返 503 | 实例总连接数满了（5000） | 扩容网关实例；返 429 则是单用户连接数满了（5） |
| `docker pull` 报 exit 137 | 内存不足被 OOM kill | 镜像拉取有重试；仍失败就手工 `docker pull` 后重试 |
| 改 nacos 上的值，重启后又变回去 | nacos 的值覆盖了本地文件 | 改 `configs/config.yaml` 后 `make nacos-config-push`，别直接改 nacos |

### 6.2 上线检查清单

```
数据存储
[ ] mysql / redis 容器已起，容器名是 mysql / redis
[ ] 两者都接在 data-net 上
[ ] MySQL max_connections >= 500（数据存储 compose 里已是 1000）
[ ] Redis 密码与 deploy/.env 一致，chatroom 用 DB 2

本项目 deploy/.env
[ ] MYSQL_ROOT_PASSWORD / REDIS_PASSWORD 与数据存储一致
[ ] NACOS_NAMESPACE 是正式命名空间，不是 CHANGE_ME
[ ] NACOS_AUTH_TOKEN 已换成随机值（openssl rand -hex 32）
[ ] AUTH_JWT_SECRET / AUTH_INTERNAL_SECRET 已生成，五服务共用同一份
[ ] ACR_REGISTRY_HOST / ACR_NAMESPACE 是真实值
[ ] REGISTRY_USERNAME / REGISTRY_PASSWORD 是 ECS 拉镜像用的账号
[ ] chmod 600 .env

nacos
[ ] 五个服务的 config.yaml 都推进去了（make nacos-config-push）
[ ] 管理员账号已创建且能登录
[ ] nacos-data 卷在（配置中心内容在里面，别用 down -v）

起服务
[ ] ./deploy.sh up 成功
[ ] 六个容器都 Up（nacos + 5 个业务服务）
[ ] 五个 /readyz 都返回 ok
[ ] nacos 上有 9 个服务名（含 .http / .grpc 后缀）
[ ] Redis DB 2 里有 chatroom: 前缀的 key
[ ] 端到端冒烟过了（§2.6）

可观测性（按需）
[ ] 要 metrics：nacos 上改 metrics_addr + compose 开端口 + Prometheus 注解
[ ] 要 trace：给容器注入 OTEL_EXPORTER_OTLP_ENDPOINT

CI（如果走流水线发布）
[ ] GitHub Vars / Secrets 配齐（见 .github/workflows/README.md）
[ ] ECS 上 deploy/.env 已就位 —— 流水线只同步 compose 和 deploy.sh
[ ] 第一次 push 会因 .env 缺失而退出，补好 .env 后再 push
```
