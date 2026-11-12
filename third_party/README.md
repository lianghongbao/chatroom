# third_party —— 第三方 proto 的**唯一副本**

这里放契约 proto 要 import 的外部 proto（当前只有 `google/api/`）。
**全仓库一份** —— 契约 module 从这里解析，不各自 vendored 一份副本。

## 为什么在仓库根而不是契约目录里

`google/api/annotations.proto` 是 Google 的公共注解定义（HTTP 路由映射），
不是我们的业务契约。它的版本必须跟 `google.golang.org/genproto` 对齐，
所以它由「谁来生成代码」这个维度决定该放哪 —— 而不是由「哪个域用它」决定。

四个域的契约 proto 全都 import 它，所以它是**共享依赖**，不是某个域的私有依赖。
放在根目录，跟 `pkg/`（公共 module）一个思路：同一份东西只有一处定义，
不存在「user 域的副本和 friend 域的副本慢慢漂移」的问题。

## 为什么契约用 protoc 而不是 buf

契约 proto 要 import `google/api/annotations.proto`，而 buf v2 的
`modules:` 根是**单一目录** —— module 根之外的路径引用会被拒绝
（`is outside the context directory`），`deps:` 又只接受 registry 名。

protoc 原生支持多个 `--proto_path`，所以契约生成直接用 protoc：

```
protoc --proto_path=. --proto_path=../../third_party ...
```

这正是官方 `go-kratos/beer-shop` 的做法（它用 `--proto_path=../../../third_party`）。
生成脚本在 `contracts/gen.sh`，各契约 module 的 `make api` 转发过去。

**服务端自己的配置 proto（`internal/conf/conf.proto`）仍然用 buf** ——
它不 import 任何第三方 proto，不需要跨目录解析。

## 文件

| 文件 | 来源 | 用途 |
|------|------|------|
| `google/api/annotations.proto` | googleapis | `google.api.http` 注解（HTTP 路由映射） |
| `google/api/http.proto` | googleapis | 被 annotations.proto 引用 |

## 更新

替换成本地副本后重新生成契约：

```bash
make api        # 在 contracts/<域>/ 下逐个跑
```

Go 侧对应 `google.golang.org/genproto/googleapis/api` —— 契约 module 的
`go.mod` 已经 require 了它，改动 proto 时两个版本要一起动。
