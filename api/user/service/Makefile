# chatroom/api/user/service —— user 域的**契约**
#
# 这里只有两样东西：proto 定义 + 生成的桩代码（grpc / http / openapi）。
# **没有任何实现** —— 实现住在 app/user/service/。
#
# 契约与实现同仓但不同包路径：调用方（friend / message / ws-gateway …）只
# import `chatroom/api/<域>/service/v1`，拿不到服务的实现。
# Go 的 internal/ 可见性规则在编译期就卡死了越界引用。

DOMAIN := user

.PHONY: api
# 重新生成契约代码（grpc stub / http stub / openapi.yaml）
#
# 走 api/gen.sh —— 用 protoc 而不是 buf，因为契约 proto 要 import
# 仓库根的 third_party/google/api/，而 buf v2 的 module 根是单一目录。
api:
	../../gen.sh $(DOMAIN)

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
