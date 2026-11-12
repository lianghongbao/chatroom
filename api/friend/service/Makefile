# chatroom/api-friend —— friend 域的**契约 module**
#
# 这里只有两样东西：proto 定义 + 生成的桩代码（grpc / http / openapi）。
# **没有任何实现** —— 实现住在 friend-service 里。
#
# 为什么单独一个 module：调用方（friend / message / ws-gateway …）需要
# 的是「friend 域对外的契约」，不是「friend 服务的实现」。契约独立成 module 之后，
# 调用方 compile 期依赖的是契约，friend 服务的重构（换 ORM、拆表、加中间件）
# 不会波及调用方 —— 这正是微服务「独立部署、独立演进」的前提。
#
# 这个 module 可以单独发版（打 tag / 发到内部 registry），调用方按版本引。

DOMAIN := friend

.PHONY: api
# 重新生成契约代码（grpc stub / http stub / openapi.yaml）
#
# 走 contracts/gen.sh —— 用 protoc 而不是 buf，因为契约 proto 要 import
# 仓库根的 third_party/google/api/，而 buf v2 的 module 根是单一目录。
api:
	../gen.sh $(DOMAIN)

.PHONY: build
build:
	go build ./...

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: clean
clean:
	go clean -testcache

.DEFAULT_GOAL := api
