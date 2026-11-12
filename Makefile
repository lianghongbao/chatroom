# chatroom 仓库根 Makefile
#
# 仓库根**不是** Go module —— 它只做编排：把「每个服务 / 每个契约 module」
# 自己的 make target 跑一遍。各服务仍然是独立的 Go module，可以单独 clone。
#
# 跟官方 go-kratos/beer-shop 的根 Makefile 一个思路（那边是 api/wire/proto
# 三个聚合 target），但这里多一层「契约 module」——契约和实现是分开的。

# 五个业务服务。ws-gateway 没有 api 契约（它是聚合网关，不对外暴露业务契约）。
SERVICES := user friend conversation message ws-gateway

# 四个业务域的契约 module。ws-gateway 没有契约。
CONTRACTS := user friend conversation message

# 公共 module（chatroom/pkg）—— 被所有服务 import，自己没有 proto。
.PHONY: pkg-build
pkg-build:
	cd pkg && go build ./...

.PHONY: pkg-test
pkg-test:
	cd pkg && go test ./...

.PHONY: pkg-vet
pkg-vet:
	cd pkg && go vet ./...

# ---------------------------------------------------------------- 契约

.PHONY: api
# 契约 proto 用 protoc（contracts/gen.sh），因为它们要 import 仓库根的
# third_party/google/api/ —— buf v2 的 module 根是单一目录，跨目录引用不了。
# 重新生成全部契约代码（grpc stub / http stub / openapi.yaml）
api:
	@for d in $(CONTRACTS); do \
		echo "=== contracts/$$d ==="; \
		(cd contracts/$$d && make api) || exit 1; \
	done

# ---------------------------------------------------------------- 服务

.PHONY: build
# 编译全部服务 + 公共 module
build:
	@for svc in $(SERVICES); do \
		echo "=== $$svc-service ==="; \
		(cd $${svc}-service && make build) || exit 1; \
	done
	@$(MAKE) pkg-build

.PHONY: test
# 跑全部服务的单元测试 + 公共 module 的测试
test:
	@for svc in $(SERVICES); do \
		echo "=== $$svc-service ==="; \
		(cd $${svc}-service && make test) || exit 1; \
	done
	@$(MAKE) pkg-test

.PHONY: test-integration
# 跑全部需要 MySQL 的集成测试（data 层 repo + 跨域链路）
# 每个用例自建临时库、跑完自动 DROP
test-integration:
	@for svc in $(SERVICES); do \
		if [ -f $${svc}-service/Makefile ] && grep -q '^test-integration:' $${svc}-service/Makefile; then \
			echo "=== $$svc-service ==="; \
			(cd $${svc}-service && make test-integration) || exit 1; \
		fi; \
	done

.PHONY: vet
# 静态检查全部 module
vet:
	@for svc in $(SERVICES); do \
		echo "=== $$svc-service ==="; \
		(cd $${svc}-service && make vet) || exit 1; \
	done
	@$(MAKE) pkg-vet

.PHONY: proto
# 重新生成全部 proto 代码：契约（protoc）+ 各服务的配置 proto（buf）。
proto: api
	@for svc in $(SERVICES); do \
		if [ -f $${svc}-service/Makefile ] && grep -q '^config:' $${svc}-service/Makefile; then \
			echo "=== $$svc-service (conf) ==="; \
			(cd $${svc}-service && make config) || exit 1; \
		fi; \
	done

.PHONY: wire
# 重新生成 Wire 注入代码
wire:
	@for svc in $(SERVICES); do \
		echo "=== $$svc-service ==="; \
		(cd $${svc}-service && go generate ./...) || exit 1; \
	done

.PHONY: all
# 一键重生：全部 proto + Wire + tidy
all: proto wire
	@for svc in $(SERVICES); do \
		(cd $${svc}-service && go mod tidy) || exit 1; \
	done

.PHONY: clean
# 清理 build 产物 + go test 缓存
clean:
	@for svc in $(SERVICES); do \
		(cd $${svc}-service && make clean) || exit 1; \
	done
	cd pkg && go clean -testcache

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
