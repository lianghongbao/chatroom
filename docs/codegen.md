# 代码生成

`*.pb.go`、`*_grpc.pb.go`、`*_http.pb.go`、`wire_gen.go` 都是生成物，**不要手改**。

相关：[README](../README.md) ｜ [架构与分层](architecture.md)

## 在自己目录里跑

```bash
# 契约：生成 grpc / http stub + openapi.yaml
cd api/user/service && make api

# 配置 + Wire
cd app/user/service
make config    # 重新生成 conf proto
make generate  # 跑 go generate（Wire）
make all       # config + generate
```

生成分两处跑 —— **契约**在契约目录，**配置**在服务目录。也可以直接调脚本：

```bash
cd api/user/service
../../gen.sh user                           # 契约 proto（grpc / http / openapi）

cd ../../app/user/service
../../gen-conf.sh user                      # conf proto
```

## 全部一次跑完（仓库根）

```bash
make api     # 四个契约包重新生成
make config  # 五个服务的 conf proto
make proto   # 契约 + 各服务的 conf proto
make all     # 契约 + conf proto + Wire + go mod tidy
make build   # 五个服务
make test    # 五个服务的单元测试
```

## 生成脚本的约定

用 **protoc** 而不是 buf：契约 proto 要 import 仓库根的
`third_party/google/api/annotations.proto`，而 buf v2 的 module 根是单一目录，
跨目录引用会被拒（`is outside the context directory`），`deps:` 又只接受 registry 名。
protoc 原生支持多个 `--proto_path`。

两个脚本都从**仓库根**跑，注册路径带上域名 / 服务名 —— protobuf 的注册表按
「相对 proto_path 根的路径」做 key，注册路径太窄会让多个域的 proto 在 `init()` 撞车：

| 脚本 | `--proto_path` | 注册路径 |
|---|---|---|
| `api/gen.sh` | `api` | `<域>/service/v1/<文件>.proto` |
| `app/gen-conf.sh` | `app` | `<服务>/service/internal/conf/conf.proto` |

插件版本在脚本里用 `@version` 钉死，编译产物缓存在 `.cache/protoc-plugins/`
（按版本组合命名），并发跑不会互相踩。

`paths=source_relative` 让 `v1/xxx.proto` 生成到 `v1/xxx.pb.go` —— pb 代码跟 proto 待在一起。

## service 层也是生成的

`app/<服务>/service/internal/service/<服务>.go` 由 **kratos CLI** 生成，不是手写的。
要在**服务目录**里跑 —— 它要吃契约 proto 的路径：

```bash
cd app/user/service
kratos proto server ../../../api/conversation/service/v1/conversation.proto \
    --target-dir=internal/service
```

`user` / `friend` / `conversation` / `message` 四个域有业务 proto，
路径要带上 `../../../api/`。

生成器**只在文件不存在时写** —— 已存在的会报 `<Service> already exists` 并跳过，
所以重跑不会覆盖已实现的版本。

CLI 生成的是**骨架**：内嵌 `pb.Unimplemented<Service>Server`、无参构造函数、
以及每个 RPC 一个 `return &pb.<Reply>{}, nil` 的空方法。往里填
`*biz.<资源>Usecase` 字段、构造函数参数、真实实现 —— 方法**签名**保持生成器
写出来的样子，那是跟 `*_grpc.pb.go` 的契约。

CLI 装法：`go install github.com/go-kratos/kratos/cmd/kratos/v3@latest`。
