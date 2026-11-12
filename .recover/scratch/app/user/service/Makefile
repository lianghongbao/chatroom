# chatroom-user 的服务级 Makefile
#
# 本服务是一个**独立的 Go module**（自己的 go.mod），可以单独 clone 成
# 独立仓库。所有 recipe 都在本目录内执行，不依赖仓库根。
#
# 跨服务的动作（跑全量测试、编排五个服务）走仓库根的 Makefile。

SERVICE := user
BINARY  := chatroom-user

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

# 共享开发环境变量（nacos / DB / Redis / JWT）。
include tools/dev-env.mk

.PHONY: build
# 编译本服务二进制到 bin/chatroom-user
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
# 构建镜像。上下文是服务目录
docker-build:
	docker build -t $(BINARY):latest .

.PHONY: config
# 重新生成本服务的 conf proto(internal/conf/conf.pb.go)
config:
	buf generate --template buf.gen.config.yaml

.PHONY: api
# 重新生成 api proto(业务 grpc.pb.go / http.pb.go / openapi.yaml)
api:
	buf generate --template buf.gen.yaml

.PHONY: service
# 用 kratos CLI 生成 service 层骨架(internal/service/<服务>.go)
#
# 官方模板的做法:service 层文件由 `kratos proto server` 从 proto 生成,
# 业务逻辑再往里填。生成器只在文件不存在时写 —— 已存在的文件会跳过,
# 所以不会覆盖手写的实现。
service:
	kratos proto server api/user/v1/user.proto --target-dir=internal/service

.PHONY: generate
# 跑 go generate(触发 wire 重新生成 wire_gen.go)+ go mod tidy
generate:
	go generate ./...
	go mod tidy

.PHONY: all
# 一键重生 proto + wire + tidy
all: api config generate

.PHONY: nacos-config
# 对比本地 configs/config.yaml 与 nacos 上的 chatroom-user.yaml
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
