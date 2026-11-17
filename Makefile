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

# 本地启动顺序 —— 跟 SERVICES 不同，是有依赖的。
# conversation 要经 nacos discovery 调 user 补全会话成员资料，
# message 要调 conversation 的成员身份/未读数端点，
# ws-gateway 要调 conversation 的成员列表算广播目标。
# 先起被调方：下游启动瞬间 discovery 查不到上游就会报连接错误。
START_ORDER := user conversation message ws-gateway friend

# 四个业务域的契约。ws-gateway 没有契约。
CONTRACTS := user friend conversation message

# ---------------------------------------------------------------- 契约

.PHONY: api
# 重新生成全部契约代码（grpc stub / http stub / openapi.yaml）
# 走 api/gen.sh —— 用 protoc 而不是 buf，契约 proto 要 import 仓库根的
# third_party/google/api/，而 buf v2 的 module 根是单一目录，跨目录引用不了。
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

.PHONY: run-all
# 启动顺序有依赖：user → conversation → message → ws-gateway。
# 后几个要经 nacos discovery 调前面的服务，先起被调方 ——
# 反序起的话下游启动瞬间 discovery 还查不到上游，报连接错误。
#
# 每个服务在**自己的目录**里跑 `make run` —— 那里的 Makefile 才带
# COMMON_ENV（nacos 账号、DB、Redis、JWT）。手工 go run 少了 env 会
# 因 nacos 鉴权失败 panic（Code: 401, Message: User not found!）。
#
# 每个服务之间 sleep 一下：nacos 注册是异步的，睡 1s 让上一个服务
# 先在服务列表里出现，下一个服务启动时的 discovery 才查得到。
#
# 日志在 /tmp/chatroom-<服务>.log。停止用 make stop-all。
# 一键后台启动全部五个服务（日志在 /tmp/chatroom-<服务>.log）
run-all:
	@for svc in $(START_ORDER); do \
		echo "=== starting app/$$svc/service ==="; \
		(cd app/$$svc/service && nohup make run > /tmp/chatroom-$$svc.log 2>&1 &) ; \
		sleep 1; \
	done
	@echo ''
	@echo '全部服务已启动，日志：'
	@for svc in $(START_ORDER); do \
		echo "  /tmp/chatroom-$$svc.log"; \
	done
	@echo ''
	@echo '停止：make stop-all'

.PHONY: stop-all
# 按名字 pkill 不可靠：go run 会派生出编译出来的临时二进制，
# 进程名是 app/user/service 之类的路径，不叫 chatroom-user。
# 端口才是可靠的锚点 —— 每个服务端口不重。
#
# 端口从 configs/config.yaml 的 http.addr 提取。用 sed 而不是 grep -o
# '[0-9]+'：后者会把 timeout: 30s 或注释里的数字也吸进来。
# 只认 http 段下的第一行 addr（http: 后紧跟的那行）——
# ws-gateway 的 addr 是 "${GATEWAY_HTTP_ADDR:0.0.0.0:8004}"，
# 取最后一对冒号后的数字即端口。
#
# sed 表达式放在变量里而不是直接写进 recipe：make 会吃掉 recipe 里的
# 反斜杠（\1 → 1，\( → ），表达式会被撕成语法错误。
PORT_SED := /^  http:/,/^  [a-z]/ s/.*addr:.*:\([0-9]\{4,5\}\).*/\1/p
# 停掉 run-all 起的全部服务（按端口找 PID 再 kill）
stop-all:
	@for svc in $(START_ORDER); do \
		port=$$(sed -n "$(PORT_SED)" app/$$svc/service/configs/config.yaml 2>/dev/null | head -1); \
		if [ -n "$$port" ]; then \
			pid=$$(ss -lptn "sport = :$$port" 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2); \
			if [ -n "$$pid" ]; then \
				echo "stopping $$svc (port $$port, pid $$pid)"; \
				kill $$pid 2>/dev/null || true; \
			else \
				echo "$$svc: port $$port 没有进程在听"; \
			fi; \
		else \
			echo "$$svc: 没能从 config.yaml 提取到 http 端口"; \
		fi; \
	done

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
