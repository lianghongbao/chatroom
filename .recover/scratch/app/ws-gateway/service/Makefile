# chatroom-ws-gateway 的服务级 Makefile
#
# 本服务是一个**独立的 Go module**（自己的 go.mod），可以单独 clone 成
# 独立仓库。所有 recipe 都在本目录内执行，不依赖仓库根。
#
# 跨服务的动作（跑全量测试、编排五个服务）走仓库根的 Makefile。

SERVICE := ws-gateway
BINARY  := chatroom-ws-gateway

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

# 共享开发环境变量（nacos / DB / Redis / JWT）。在 chatroom/pkg/tools/ 下，
# 五个服务共用一份 —— 这些值不一致会导致跨服务调用失败。
# 路径基于本 Makefile 的位置，make 在哪个目录跑都一样。
include $(dir $(lastword $(MAKEFILE_LIST)))../pkg/tools/dev-env.mk

.PHONY: build
# 编译本服务二进制到 bin/chatroom-ws-gateway
build:
	mkdir -p bin
	go build -ldflags "-X main.Version=$(VERSION)" -o ./bin/$(BINARY) ./cmd/server

.PHONY: run
# 本地启动本服务（nacos/DB/Redis 必须已就绪）。
# NACOS_SERVICE_NAME 决定注册到 nacos 的服务名 —— 必须跟别人调用的名字一致。
run:
	$(COMMON_ENV) \
		NACOS_SERVICE_NAME=$(BINARY) \
		go run ./cmd/server

.PHONY: test
# 跑本服务的测试
test:
	go test ./...

.PHONY: test-integration
# 跑需要 MySQL 的集成测试（data 层 repo + 跨域链路）。
# 每个用例自建临时库、跑完自动 DROP,不碰共享的 test 库。
# CHATROOM_TEST_ROOT_DSN 未设置时这些用例会 skip。
test-integration:
	CHATROOM_TEST_ROOT_DSN='$(TEST_ROOT_DSN)' go test ./internal/data/ -v -count=1

.PHONY: vet
# 静态检查
vet:
	go vet ./...

.PHONY: docker-build
# 构建镜像。上下文是**仓库根目录** —— 本服务依赖同仓的 chatroom/pkg，
# 上下文只给服务目录的话 replace 指向的 ../pkg 不在构建里。
docker-build:
	docker build -t $(BINARY):latest -f $(notdir $(CURDIR))/Dockerfile ..

.PHONY: config
# 重新生成本服务的 conf proto(internal/conf/conf.pb.go)
config:
	buf generate --template buf.gen.config.yaml

.PHONY: generate
# 跑 go generate(触发 wire 重新生成 wire_gen.go)+ go mod tidy
generate:
	go generate ./...
	go mod tidy

.PHONY: all
# 一键重生 proto + wire + tidy
all: config generate

.PHONY: nacos-config
# 对比本地 configs/config.yaml 与 nacos 上的 chatroom-ws-gateway.yaml
#
# 有差异时退出码非 0 —— 用来在提交前发现「改了本地没推 nacos」。
#
# 依赖 python3 + PyYAML。PATH 里的 python3 没装 PyYAML 时（uv/conda 管的
# 常见）会报 "No module named 'yaml'" —— 系统的 /usr/bin/python3 有，
# 用 PATH="/usr/bin:$PATH" make nacos-config 跑。
nacos-config:
	tools/nacos-config.sh diff

.PHONY: nacos-config-push
# 把本地 configs/config.yaml 推到 nacos 配置中心
#
# 推的就是 config.yaml 原文 —— registry 段已经拆到 bootstrap.yaml 了。
# bootstrap.yaml 不推：「连哪个 nacos」必须由本地文件决定,不能由 nacos
# 自己决定(本地改了没重推时,nacos 里的旧 registry 会盖掉新值)。
nacos-config-push:
	tools/nacos-config.sh push

.PHONY: infra-up
# 起本地基础设施（MySQL + Redis + Nacos + Jaeger）
# 本机已经在跑原生 Redis/Nacos 的话先停掉 —— 端口一样。
# Jaeger UI: http://127.0.0.1:16686 （trace 用内存存，重启丢）
infra-up:
	docker compose -f deploy/docker-compose.yaml up -d

.PHONY: infra-down
# 停掉本地基础设施。数据卷保留（docker compose down -v 才删）。
infra-down:
	docker compose -f deploy/docker-compose.yaml down

.PHONY: clean
# 清理 build 产物 + go test 缓存
clean:
	rm -rf bin
	go clean -testcache

.DEFAULT_GOAL := build
