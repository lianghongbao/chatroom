# third_party —— 第三方 proto 的**唯一副本**

这里放契约 proto 要 import 的外部 proto（当前只有 `google/api/`）。
**全仓库一份** —— 契约从仓库根解析，不各自 vendored 一份副本。

## 为什么在仓库根而不是契约目录里

`google/api/annotations.proto` 是 Google 的公共注解定义（HTTP 路由映射），
不是我们的业务契约。它的版本必须跟 `google.golang.org/genproto` 对齐，
所以它由「谁来生成代码」这个维度决定该放哪 —— 而不是由「哪个域用它」决定。

四个域的契约 proto 都 import 它，所以它是**共享依赖**，不是某个域的私有依赖。
放在根目录，跟 `pkg/`（共享包）一个思路：同一份东西只有一处定义，
不存在「user 域的副本和 friend 域的副本慢慢漂移」的问题。

## 为什么契约用 protoc 而不是 buf

契约 proto 要 import `google/api/annotations.proto`，而 buf v2 的
`modules:` 根是**单一目录** —— module 根之外的路径引用会被拒绝
（`is outside the context directory`），`deps:` 又只接受 registry 名。

protoc 原生支持多个 `--proto_path`，所以契约生成直接用 protoc：

```
protoc --proto_path=api --proto_path=third_party ...
```

这正是官方 `go-kratos/beer-shop` 的做法（它用 `--proto_path=../../../third_party`）。
生成脚本在 `api/gen.sh`，各契约目录的 `make api` 转发过去。

**服务端自己的配置 proto（`internal/conf/conf.proto`）也用 protoc** ——
脚本是 `app/gen-conf.sh`。它不 import 任何第三方 proto，
但两个脚本共用同一套插件版本钉死和缓存机制（`.cache/protoc-plugins/`）。

## `--proto_path` 根必须是 `api/`，不能更窄

protoc 从**仓库根**执行，`--proto_path=api`（契约）或 `--proto_path=app`（配置）。
这样注册进 protobuf 全局注册表的文件路径是：

```
<域>/service/v1/<文件>.proto
<服务>/service/internal/conf/conf.proto
```

路径必须**全局唯一**：整个仓库是单个 Go module，四个域的契约和五个服务的
conf 包可能被链进同一个二进制。protobuf 的注册表按「相对 proto_path 根的
路径」做 key，四份都叫 `v1/error_reason.proto` 会在 init 期 panic
（`proto: file "v1/error_reason.proto" is already registered`）。

把 `--proto_path` 指窄一层（比如直接指 `api/user/service`），
四个域的路径就又塌回 `v1/xxx.proto`，撞回去了。

## 文件

| 文件 | 来源 | 用途 |
|------|------|------|
| `google/api/annotations.proto` | googleapis | `google.api.http` 注解（HTTP 路由映射） |
| `google/api/http.proto` | googleapis | 被 annotations.proto 引用 |

两个文件的 `go_package` 都指向
`google.golang.org/genproto/googleapis/api/annotations` —— 所以生成出来的
Go 代码引用的是 genproto 里的类型，本地这份 proto 只在**编译期**参与，
不会产出重复的 Go 包。

## 更新

替换成本地副本后重新生成契约：

```bash
make api        # 在 api/<域>/service/ 下逐个跑
```

Go 侧对应 `google.golang.org/genproto/googleapis/api` —— 根 `go.mod`
已经 require 了它，改动 proto 时两个版本要一起动。
