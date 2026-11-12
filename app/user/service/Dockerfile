# chatroom-user 镜像
#
# 构建上下文是**服务目录**（本服务是独立 module，go.mod 在这里）：
#   docker build -t chatroom-user .
FROM golang:1.26 AS builder

COPY . /src
WORKDIR /src

RUN GOPROXY=https://goproxy.cn go build -ldflags "-X main.Version=$(cat VERSION 2>/dev/null || echo dev)" \
    -o /src/bin/chatroom-user ./cmd/server

FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
			ca-certificates  \
        netbase \
        && rm -rf /var/lib/apt/lists/ \
        && apt-get autoremove -y && apt-get autoclean -y

COPY --from=builder /src/bin/chatroom-user /app/server
# 本服务的配置文件。敏感字段（DB / Redis / nacos 凭证 / JWT 密钥）走 env 注入，
# 这里只放非敏感的业务默认值。
COPY configs/config.yaml /data/conf/config.yaml
# 引导配置：连哪个 nacos、哪条 dataId。**不进 nacos** ——
# "连哪个 nacos" 不能由 nacos 自己决定，否则本地改了地址不生效。
COPY configs/bootstrap.yaml /data/conf/bootstrap.yaml

WORKDIR /app

VOLUME /data/conf

CMD ["./server", "-conf", "/data/conf/config.yaml", "-bootstrap", "/data/conf/bootstrap.yaml"]

EXPOSE 8000
EXPOSE 9000
