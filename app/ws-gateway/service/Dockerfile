# chatroom-ws-gateway 镜像
#
# 构建上下文是**仓库根目录** —— 整仓是一个 Go module，本服务依赖同仓的 pkg/ 和 api/。
#   docker build -t chatroom-ws-gateway -f app/ws-gateway/service/Dockerfile .
FROM golang:1.26 AS builder

# 镜像里的 go 可能比 go.mod 要求的 patch 版本旧（golang:1.26 会浮到最新的 1.26.x）。
# GOTOOLCHAIN=auto 让 go 自己下载 go.mod 指定的那个版本 —— 钉死 FROM 的 patch
# 版本不可行，镜像仓库只暴露 major.minor tag。
ENV GOTOOLCHAIN=auto \
    GOPROXY=https://goproxy.cn,direct

# go.mod / go.sum 先 COPY —— 依赖没变时这一层能命中缓存。
COPY go.mod go.sum /src/
WORKDIR /src

RUN go mod download

# 再 COPY 全部源码。
COPY . /src

RUN go build -ldflags "-X main.Version=$(cat VERSION 2>/dev/null || echo dev)" \
    -o /src/bin/chatroom-ws-gateway ./app/ws-gateway/service/cmd/server

FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
				ca-certificates  \
        netbase \
    && rm -rf /var/lib/apt/lists/* \
    && apt-get autoremove -y && apt-get autoclean -y

COPY --from=builder /src/bin/chatroom-ws-gateway /app/server
# 本服务的配置文件。敏感字段（Redis / nacos 凭证 / JWT 密钥）走 env 注入，
# 这里只放非敏感的业务默认值。
COPY app/ws-gateway/service/configs/config.yaml /data/conf/config.yaml
# 引导配置：连哪个 nacos、哪条 dataId。**不进 nacos** ——
# 「连哪个 nacos」不能由 nacos 自己决定，否则本地改了地址不生效。
COPY app/ws-gateway/service/configs/bootstrap.yaml /data/conf/bootstrap.yaml

WORKDIR /app

VOLUME /data/conf

CMD ["./server", "-conf", "/data/conf/config.yaml", "-bootstrap", "/data/conf/bootstrap.yaml"]

# 网关只起 HTTP server（WS 端点 /ws），不提供 gRPC 服务。
EXPOSE 8004
