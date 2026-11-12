# chatroom 仓库根 Makefile
#
# 仓库根**就是** Go module（module chatroom）—— 整仓一个 go.mod，
# 没有 replace 也没有 go.work。目录按官方 go-kratos/beer-shop 的思路分：
#
#   api/<域>/service/    契约（proto + 生成的桩代码），无实现
#   app/<域>/service/    微服务实现（cmd / internal）
#   pkg/                 跨服务共享的公共包
#   third_party/         第三方 proto（离线依赖）
#
# 根 Makefile 只做编排：把每个服务 / 每个契约自己的 target 跑一遍。
# 拆仓时把 api/<域> 抽成独立 module 只需要改版本号 —— 依赖方向不变。

# 五个业务服务。ws-gateway 没有 api 契约（它是聚合网关，不对外暴露业务契约）。
SERVICES := user friend conversation message ws-gateway

# 四个业务域的契约。ws-gateway 没有契约。
CONTRACTS := user friend conversation message

# ---------------------------------------------------------------- 契约

.PHONY: api
# 重新生成全部契约代码（grpc stub / http stub / openapi.yaml）
#
# 走 api/gen.sh —— 用 protoc 而不是 buf，因为契约 proto 要 import
# 仓库根的 third_party/google/api/，而 buf v2 的 module 根是单一目录，
# 跨目录引用不了。protoc 原生支持多个 --proto_path。
api:
	@for d in $(CONTRACTS); do \
		echo "=== api/$$d/service ==="; \
		(cd api/$$d/service && make api) || exit 1; \
	done

# ---------------------------------------------------------------- 服务

.PHONY: build
# 编译全部服务
build:
	@for svc in $(SERVICES); do \
		echo "=== app/$$svc/service ==="; \
		(cd app/$$svc/service && make build) || exit 1; \
	done

.PHONY: test
# 跑全部服务的单元测试
test:
	@for svc in $(SERVICES); do \
		echo "=== app/$$svc/service ==="; \
		(cd app/$$svc/service && make test) || exit 1; \
	done

.PHONY: test-integration
# 跑全部需要 MySQL 的集成测试（data 层 repo + 跨域链路）
# 每个用例自建临时库、跑完自动 DROP
test-integration:
	@for svc in $(SERVICES); do \
		if [ -f app/$$svc/service/Makefile ] && grep -q '^test-integration:' app/$$svc/service/Makefile; then \
			echo "=== app/$$svc/service ==="; \
			(cd app/$$svc/service && make test-integration) || exit 1; \
		fi; \
	done

.PHONY: vet
# 静态检查
vet:
	@for svc in $(SERVICES); do \
		echo "=== app/$$svc/service ==="; \
		(cd app/$$svc/service && make vet) || exit 1; \
	done

.PHONY: config
# 重新生成全部服务的配置 proto（internal/conf/conf.pb.go）
config:
	@for svc in $(SERVICES); do \
		echo "=== app/$$svc/service (conf) ==="; \
		(cd app/$$svc/service && make config) || exit 1; \
	done

.PHONY: proto
# 重新生成全部 proto 代码：契约（protoc）+ 各服务的配置 proto（protoc）
proto: api config

.PHONY: wire
# 重新生成 Wire 注入代码
wire:
	@for svc in $(SERVICES); do \
		echo "=== app/$$svc/service ==="; \
		(cd app/$$svc/service && go generate ./...) || exit 1; \
	done

.PHONY: tidy
# go mod tidy（根 module 一份）
tidy:
	go mod tidy

.PHONY: all
# 一键重生：全部 proto + Wire + tidy
all: proto wire tidy

.PHONY: clean
# 清理 build 产物 + go test 缓存
clean:
	@for svc in $(SERVICES); do \
		(cd app/$$svc/service && make clean) || exit 1; \
	done

# show help
.PHONY: help
help:
	@echo ''
	@echo 'Usage:'
	@echo ' make [target]'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z\-\_0-9]+:/ { \
	helpMessage = match(lastLine, /^# (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
			printf "\033[36m%-22s\033[0m %s\n", helpCommand, helpMessage; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)
	@echo ''

.DEFAULT_GOAL := help
