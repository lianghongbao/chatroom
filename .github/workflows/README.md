# GitHub Actions 部署配置

`deploy.yml` 是 CI/CD 流水线：五个服务（user / friend / conversation / message /
ws-gateway）各 build 一个镜像推到 ACR，再部署到 ECS 的 self-hosted runner。

MySQL / Redis 走**共享底座**（`deploy/infra/`），本流水线**不碰**那两个容器 ——
底座的生命周期独立，不随业务代码发布重启。

## 架构

两个 job 串联：

```
push main
    │
    ├─▶ build (GitHub-hosted runner, matrix 五路并行)
    │     └─ docker buildx build + push → ACR
    │        每次推两个 tag:
    │          chatroom-<服务>-<sha>    immutable,精确版本
    │          chatroom-<服务>-latest   mutable,deploy job 拉这个
    │
    ▼ needs: build
    deploy (self-hosted runner on ECS)
      ├─ docker pull × 5 + re-tag
      ├─ 同步 compose / deploy.sh 到 ${DEPLOY_DIR}
      ├─ ./deploy.sh up
      │    ├─ 起 nacos，等健康
      │    ├─ 建 nacos 管理员账号
      │    └─ 按 depends_on 健康门禁起五个业务服务
      └─ 五个服务各探一次 /readyz
```

- **build 跑在 GitHub-hosted runner**（出网方便，适合 buildx + 推镜像）
- **deploy 跑在 self-hosted runner**（在 ECS 内，持有部署目录的写权限）

## 为什么不用 `--no-deps` 一次全起

chatroom 五个服务有启动顺序依赖（见 `deploy/docker-compose.yml` 的 `depends_on`）：

```
nacos → user → friend → conversation → message → ws-gateway
```

`depends_on` 用的是 `condition: service_healthy`，即下游等上游 `/readyz` 通过
才起。`docker compose up -d --no-deps` 会**绕过**这层门禁 —— 下游服务在
上游还没注册到 nacos 时就去 discovery，报连接错误。

所以部署走 `deploy/deploy.sh`，它按 compose 的依赖序编排。

## Vars（明文，每个项目改这些）

路径：`repo Settings → Secrets and variables → Actions → Variables`

| Var | 含义 |
| ---- | ---- |
| `ACR_REGISTRY_HOST` | 镜像仓库公网端点域名，例 `crpi-XXXX.personal.cr.aliyuncs.com` |
| `ACR_NAMESPACE` | 仓库 namespace，例 `lhb-ns/lhb-hub` |
| `IMAGE_PREFIX` | 本地镜像名前缀，compose 里是 `${IMAGE_PREFIX}/chatroom-<服务>`，默认 `chatroom` |
| `DEPLOY_DIR` | ECS 上部署目录（包含 `docker-compose.yml` + `deploy.sh` + `.env`） |
| `RUNNER_LABELS` | self-hosted runner 标签，逗号分隔 |
| `CONCURRENCY_GROUP` | 部署并发组名，例 `deploy-chatroom` |

> local-life-api 的 `BASE_GOLANG_TAG` / `BASE_ALPINE_TAG` 在这里**不需要** ——
> chatroom 的 Dockerfile 直接写死 `FROM golang:1.26` / `FROM debian:stable-slim`，
> 没有 ARG 参数。

## Secrets（加密）

路径：`repo Settings → Secrets and variables → Actions → New repository secret`

| Secret | 用途 |
|--------|------|
| `ACR_USERNAME` | 镜像仓库的 RAM 用户名（build job 推镜像用） |
| `ACR_PASSWORD` | 镜像仓库的 RAM 密码 |

> deploy job 拉镜像用的是 **ECS 上 `${DEPLOY_DIR}/.env` 里的
> `REGISTRY_USERNAME` / `REGISTRY_PASSWORD`** —— 跟 local-life-api 一致，
> 在 ACR 控制台单独开 RAM 账号。build 和 deploy 用的是两套凭证。

## ECS 上 `deploy/` 目录的首次准备

**流水线不管这个**，必须先手工备好：

| 文件 | 作用 |
|------|------|
| `.env` | 应用凭证 + 镜像仓库拉取凭证。**至少**含 `MYSQL_ROOT_PASSWORD` / `REDIS_PASSWORD`（跟共享底座同一个值）/ `NACOS_USERNAME` / `NACOS_PASSWORD` / `AUTH_JWT_SECRET` / `AUTH_INTERNAL_SECRET` / `REGISTRY_USERNAME` / `REGISTRY_PASSWORD` |

`.env` 不在仓库里（`.gitignore` 里有），流水线只在**缺失时**从 `.env.example`
生成一份并**直接退出** —— 不拿着占位值往下部署。

共享底座（`deploy/infra/`）要先单独起好，见 `deploy/README.md`。

## 首次启用步骤

1. 在 GitHub 上配好上面的 Vars 和 Secrets。
2. ECS 上手工建好 `${DEPLOY_DIR}/.env`（**先填凭证**，别用占位值）。
3. ECS 上起共享底座：`cd deploy/infra && ./deploy.sh up`。
4. 把五个服务的业务配置推进 nacos（各服务 `make nacos-config-push`）。
5. push 到 main 触发流水线。

第 4 步不做的话，服务会回落到读镜像里的本地 `config.yaml`（不报错），
但线上改配置就不生效了。

## 镜像命名

```
${ACR_REGISTRY_HOST}/${ACR_NAMESPACE}:chatroom-<服务>-latest
```

deploy job 拉下来 re-tag 成 compose 期望的名字：

```
chatroom/chatroom-<服务>:server-latest
```

（对应 compose 里的 `${IMAGE_PREFIX:-chatroom}/chatroom-<服务>:${IMAGE_TAG:-server-latest}`）
