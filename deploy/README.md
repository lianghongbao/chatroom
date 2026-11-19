# chatroom 生产部署

五个服务（user / friend / conversation / message / ws-gateway）+ nacos。
MySQL / Redis 走**共享底座**（`deploy/infra/`），本 compose 只跑业务容器。

```
┌─────────────────────────────────────────────────┐
│  共享底座 (compose project: shared-infra)       │
│    shared-mysql:3306 → chatroom 库               │
│    shared-redis:6379 → chatroom: 前缀的 key      │
└─────────────────────────────────────────────────┘
                    ▲ shared-infra-net
        ┌───────────┴───────────────────────────┐
        │  chatroom (compose project: chatroom) │
        │    nacos:8848                          │
        │    user:8000/9000                      │
        │    friend:8001/9001                    │
        │    conversation:8002/9002              │
        │    message:8003/9003                   │
        │    ws-gateway:8004                      │
        └─────────────────────────────────────────┘
```

## 首次部署

### 1. 先起共享底座

```bash
cd deploy/infra

cp .env.example .env
vi .env          # MYSQL_ROOT_PASSWORD / REDIS_PASSWORD / ACR 仓库
chmod 600 .env

./deploy.sh up
```

脚本会建 `shared-infra-net` 网络、起 `shared-mysql` / `shared-redis`、
等两个容器健康。

确认 `chatroom` 库建出来了：

```bash
docker exec shared-mysql mysql -uroot -p"$(grep MYSQL_ROOT_PASSWORD .env | cut -d= -f2)" \
  -e "SHOW DATABASES;"
```

> 建表脚本挂在 mysql 的 `/docker-entrypoint-initdb.d/`，**只在数据目录为空
> （首次创建）时执行一次**。底座已经跑过的话，用 `./deploy.sh init-db` 补。

### 2. 填本项目的 .env

```bash
cd deploy
cp .env.example .env
vi .env
chmod 600 .env
```

**`MYSQL_ROOT_PASSWORD` / `REDIS_PASSWORD` 必须跟 `deploy/infra/.env` 一致** ——
这两个是连共享底座的凭证。

### 3. 推配置到 nacos

五个服务的业务配置在 nacos 配置中心（`configs/config.yaml` 原文）。
底座刚起、nacos 是空的话，先推一遍：

```bash
cd app/user/service      && make nacos-config-push
cd ../friend/service     && make nacos-config-push
cd ../../conversation/service && make nacos-config-push
cd ../../message/service        && make nacos-config-push
cd ../../ws-gateway/service     && make nacos-config-push
```

`make nacos-config`（不带 push）是对比本地和 nacos，**有差异时退出码非 0** ——
提交前跑一下，能发现「改了本地没推 nacos」。

> 脚本依赖 python3 + PyYAML。PATH 里的 python3 没装 PyYAML 时（uv/conda 管的
> 常见）会报 `No module named "yaml"` —— 系统的 `/usr/bin/python3` 有，
> 用 `PATH="/usr/bin:$PATH" make nacos-config` 跑。

### 4. 起服务

```bash
cd deploy
./deploy.sh up
```

`deploy.sh` 比裸 `docker compose up -d` 多做两件事，**首次部署必做**：

1. **先单独起 nacos**，等它健康 —— 业务服务连不上 nacos 就没法注册。
2. **建 nacos 管理员账号**（`/nacos/v3/auth/user/admin`）并校验登录。

> ⚠️ nacos 3.x 开了 `NACOS_AUTH_ENABLE` 后，**首次启动不会自动建管理员**。
> 没有管理员的话所有 API 都返 401（`User not found!`），业务服务启动时直接
> panic（`main.go` 里读 nacos 配置那一步），整套服务起不来 ——
> 表现为 `docker compose ps` 里容器反复 `Restarting`。
>
> 手动补建：
>
> ```bash
> docker exec chatroom-nacos curl -s -X POST \
>   "http://localhost:8848/nacos/v3/auth/user/admin" \
>   --data-urlencode "username=nacos" \
>   --data-urlencode "password=<你的 NACOS_PASSWORD>"
> ```

业务配置的 nacos dataId 要在服务起之前推好（见上一节），
否则服务会拿不到配置 —— nacos 上没有该 dataId 时是回落用本地文件，
不报错，但线上改配置就不生效了。

## 启动顺序

```
nacos → user → friend → conversation → message → ws-gateway
```

有依赖关系，不能随便改：conversation 要经 nacos discovery 调 user 补全会话成员
资料，message 要调 conversation 的成员身份/未读数端点，ws-gateway 要调
conversation 的成员列表算广播目标。先起被调方 —— 下游启动瞬间 discovery
查不到上游就会报连接错误。

compose 的 `depends_on` + `condition: service_healthy` 表达这个顺序：
每个业务服务的 healthcheck 打 `/readyz`（探依赖），下游等上游 readyz 通了才起。

## 验证

```bash
# 五个服务都活着
docker compose ps
docker exec chatroom-user curl -sf http://localhost:8000/healthz
docker exec chatroom-ws-gateway curl -sf http://localhost:8004/healthz

# nacos 上五个服务都注册了（管理接口要鉴权，先拿 token）
TOKEN=$(docker exec chatroom-nacos curl -s -X POST \
  "http://localhost:8848/nacos/v3/auth/user/login" \
  --data-urlencode "username=nacos" --data-urlencode "password=<pwd>" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")

docker exec chatroom-nacos curl -s \
  "http://localhost:8848/nacos/v3/admin/ns/service/list?pageNo=1&pageSize=30&namespaceId=<你的 namespace>" \
  -H "accessToken: $TOKEN"
# 期望看到 9 条：5 个服务 × http/grpc（ws-gateway 只有 http）

# Redis key 前缀隔离（跟 local-life-api 共用一个 redis 实例）
docker exec shared-redis redis-cli -a "<pwd>" --scan --pattern 'chatroom:*' | head
# 期望扫到 chatroom: 前缀的 key

# 两个应用的 key 共存、互不干扰
docker exec shared-redis redis-cli -a "<pwd>" --scan --pattern '*' | sort
# 期望只有 chatroom:* 和 local-life:* 两种前缀
```

## 日常操作

```bash
cd deploy

./deploy.sh up                           # 起（或改完配置后应用）
./deploy.sh status                       # 状态
./deploy.sh logs user                    # 跟日志
./deploy.sh down                         # 全停（数据卷保留）

docker compose restart user              # 重启单个服务
docker compose logs -f user              # 跟日志
```

## 镜像

五个服务各一个镜像。CI 里 build 完 re-tag 成
`${IMAGE_PREFIX}/${服务名}:${IMAGE_TAG}`（默认 `chatroom/chatroom-user:server-latest`）。

本地手工构建：

```bash
cd app/user/service && make docker-build
# 上下文是仓库根 —— 整仓一个 go module，本服务依赖同仓的 pkg/ 和 api/
```

**chatroom 的 GitHub Actions** —— 五个服务各 build 一个镜像推到 ACR，
再部署到 ECS 的 self-hosted runner。

- workflow：`.github/workflows/deploy.yml`
- 配置项（Vars / Secrets / 首次准备）：`.github/workflows/README.md`

部署走 `deploy/deploy.sh`（不是裸 `docker compose up -d`）—— 它负责
nacos 管理员初始化和按依赖序起服务。

手工部署（不经过 CI）时：

```bash
cd app/user/service && make docker-build
# 上下文是仓库根 —— 整仓一个 go module，本服务依赖同仓的 pkg/ 和 api/
```

五个服务都要 build。

## 表结构

**ent schema 是权威**，服务启动时 auto_migrate 增量对齐（只 ALTER，不删列）。

`app/<服务>/service/migrations/*.sql` 是空库初始化快照，给「不依赖 Go 运行时
也能建空库」的场景用。加新表时改 ent schema，不用手写 SQL。

## 配置分两份

每个服务读两个文件，**缺一个启动就 panic**：

| 文件 | 内容 | 在哪 |
|---|---|---|
| `configs/bootstrap.yaml` | 只回答「连哪个 nacos、哪条 dataId」 | 镜像里 `/data/conf/bootstrap.yaml` |
| `configs/config.yaml` | 业务参数 | 镜像里 `/data/conf/config.yaml`，**同时**在 nacos 上 |

`bootstrap.yaml` **永远不进 nacos** —— 「连哪个 nacos」不能由 nacos 自己决定，
否则会形成循环依赖。

加载优先级：文件 < nacos < 环境变量。

## 共享底座相关的坑

- **Redis key 前缀**：chatroom 用 `chatroom:` 前缀（`pkg/keys`），
  local-life-api 用 `local-life:` 前缀。共用一个实例靠这个隔开。
  Redis Pub/Sub **不按 DB 隔离** —— 两个应用的频道名不能重。
- **MySQL 连接额度**：五个服务共享一个实例。默认 `max_connections=151` 不够
  （5 服务 × 10 副本 × 10 连接 = 500），共享底座的 mysql 已经调到 1000。
- **Redis 密码**：共享底座设了 `requirepass`，所以 conf 的 `Redis` message
  多了 `password` 字段（`conf.proto`），值从 `REDIS_PASSWORD` env 注入。
