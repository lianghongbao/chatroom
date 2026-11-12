# chatroom-message 的服务级 Makefile
#
# 本目录是 app/message/service/ —— 整仓是**一个** Go module（module chatroom），
# 所以这里的 target 都直接跑在根 module 上，不需要 go.mod / replace。
#
# 跨服务的动作（跑全量测试、编排五个服务）走仓库根的 Makefile。

SERVICE := message
BINARY  := chatroom-message

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

# 共享开发环境变量（nacos / DB / Redis / JWT）。五个服务共用一份 ——
# 路径基于本 Makefile 的位置，make 在哪个目录跑都一样。
include $(dir $(lastword $(MAKEFILE_LIST)))../../../pkg/tools/dev-env.mk

.PHONY: build
# 编译本服务二进制到 bin/chatroom-message
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
# 构建镜像。上下文是**仓库根目录** —— 本服务依赖同仓的 pkg/，
# 上下文只给服务目录的话依赖不在构建里。
docker-build:
	docker build -t $(BINARY):latest -f $(notdir $(CURDIR))/Dockerfile ../..

.PHONY: config
# 重新生成本服务的 conf proto(internal/conf/conf.pb.go)
#
# 走 app/gen-conf.sh —— protoc 从仓库根跑，注册路径是
# message/service/internal/conf/conf.proto，加上服务名这一层前缀避开
# protobuf 全局注册表的路径撞车。
config:
	bash ../../gen-conf.sh $(SERVICE)

.PHONY: generate
# 跑 go generate(触发 wire 重新生成 wire_gen.go)
generate:
	go generate ./...

.PHONY: all
# 一键重生 proto + wire
all: config generate

.PHONY: nacos-config
# 对比本地 configs/config.yaml 与 nacos 上的 chatroom-message.yaml
#
# 有差异时退出码非 0 —— 用来在提交前发现「改了本地没推 nacos」。
#
# 依赖 python3 + PyYAML。PATH 里的 python3 没装 PyYAML 时（uv/conda 管的
# 常见）会报 "No module named 'yaml'" —— 系统的 /usr/bin/python3 有，
# 用 PATH="/usr/bin:$PATH" make nacos-config 跑。
nacos-config:
	SERVICE_DIR=$(CURDIR) SERVICE=$(SERVICE) bash ../../../pkg/tools/nacos-config.sh diff

.PHONY: nacos-config-push
# 把本地 configs/config.yaml 推到 nacos 配置中心
#
# 推的就是 config.yaml 原文 —— registry 段已经拆到 bootstrap.yaml 了。
# bootstrap.yaml 不推：「连哪个 nacos」必须由本地文件决定,不能由 nacos
# 自己决定(本地改了没重推时,nacos 里的旧 registry 会盖掉新值)。
nacos-config-push:
	SERVICE_DIR=$(CURDIR) SERVICE=$(SERVICE) bash ../../../pkg/tools/nacos-config.sh push

.PHONY: clean
# 清理 build 产物 + go test 缓存
clean:
	rm -rf bin
	go clean -testcache

.DEFAULT_GOAL := build
