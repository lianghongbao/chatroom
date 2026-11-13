# chatroom-friend 镜像
#
# 构建上下文是**仓库根目录** —— 整仓是一个 Go module，本服务依赖同仓的 pkg/ 和 api/。
#   docker build -t chatroom-friend -f app/friend/service/Dockerfile .
FROM golang:1.26 AS builder

# 镜像里的 go 可能比 go.mod 要求的 patch 版本旧。
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
    -o /src/bin/chatroom-friend ./app/friend/service/cmd/server

FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
				ca-certificates  \
        netbase \
    && rm -rf /var/lib/apt/lists/* \
    && apt-get autoremove -y && apt-get autoclean -y

COPY --from=builder /src/bin/chatroom-friend /app/server
# 本服务的配置文件。敏感字段（DB / Redis / nacos 凭证 / JWT 密钥）走 env 注入，
# 这里只放非敏感的业务默认值。
COPY app/friend/service/configs/config.yaml /data/conf/config.yaml
# 引导配置：连哪个 nacos、哪条 dataId。**不进 nacos** ——
# 「连哪个 nacos」不能由 nacos 自己决定。
COPY app/friend/service/configs/bootstrap.yaml /data/conf/bootstrap.yaml

WORKDIR /app

VOLUME /data/conf

CMD ["./server", "-conf", "/data/conf/config.yaml", "-bootstrap", "/data/conf/bootstrap.yaml"]

EXPOSE 8001
EXPOSE 9001
