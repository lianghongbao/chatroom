#!/usr/bin/env bash
# 生成某个域的契约代码（grpc stub / http stub / openapi.yaml）。
#
# 用 protoc 而不是 buf：契约 proto 要 import 仓库根的
# third_party/google/api/annotations.proto，而 buf v2 的 module 根是
# 单一目录 —— 跨目录引用要么报错，要么把整个项目降到 buf v1。
# protoc 原生支持多个 --proto_path，这正是官方 beer-shop 的做法。
#
# 用法：gen.sh <域名>      例：gen.sh user
# 必须在对应契约目录（api/<域>/service/）下执行。

set -euo pipefail

DOMAIN="${1:?用法: gen.sh <域名>}"

# 契约目录固定是 <仓库根>/api/<域>/service/，所以仓库根在三层之上。
# 用脚本自身位置推导，不依赖调用时的 cwd。
CONTRACT_DIR="$(pwd)"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
THIRD_PARTY="${ROOT}/third_party"

if [ ! -d "${THIRD_PARTY}" ]; then
	echo "找不到 ${THIRD_PARTY} —— 请在 api/<域>/service/ 目录下执行" >&2
	exit 1
fi

# 插件版本钉死 —— 不随 PATH 里的 protoc-gen-* 漂移。
# 与 go.mod 里的 protobuf 版本对齐。
#
# protoc 的 --plugin 只认「一个可执行文件路径」，不能直接塞
# `go run pkg@version`（带参数的命令）。所以先把这些版本编译成
# 二进制，再把 --plugin 指向编译出来的二进制。
PLUGIN_PKG="google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12"
PLUGIN_GRPC="google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2"
PLUGIN_HTTP="github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v3@v3.0.0-20260526000039-30da04b769dc"
PLUGIN_OPENAPI="github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.1"

cd "${CONTRACT_DIR}"

# 本域的 proto（v1 目录下的全部 .proto，含 error_reason.proto）。
mapfile -t PROTOS < <(find v1 -name '*.proto' | sort)

if [ "${#PROTOS[@]}" -eq 0 ]; then
	echo "找不到 v1/*.proto —— 请在 api/${DOMAIN}/service/ 目录下执行" >&2
	exit 1
fi

echo "生成契约代码（${DOMAIN}）：${PROTOS[*]}"

# 把四个插件编译到缓存目录（版本由上面的 @version 钉死）。
#
# 用固定缓存目录而不是每次 mktemp：插件编译一次要几十秒，四个契约各编一遍
# 太慢。缓存目录按插件版本组合命名 —— 版本变了自动重建。
#
# 并发跑（多个契约同时 make api）时 go install 会争 module cache 的锁，
# 偶发失败。所以加锁 + 重试。
PLUGIN_DIR="${ROOT}/.cache/protoc-plugins"
PLUGIN_KEY="$(printf '%s\n' "${PLUGIN_PKG}" "${PLUGIN_GRPC}" "${PLUGIN_HTTP}" "${PLUGIN_OPENAPI}" | md5sum | cut -d' ' -f1)"
PLUGIN_DIR="${PLUGIN_DIR}/${PLUGIN_KEY}"

mkdir -p "${PLUGIN_DIR}"

echo "准备 protoc 插件（${PLUGIN_DIR}）..."
# 一个一个装 —— go install 不接受「不同版本的多个包」同一次调用。
for pkg in "${PLUGIN_PKG}" "${PLUGIN_GRPC}" "${PLUGIN_HTTP}" "${PLUGIN_OPENAPI}"; do
	name="$(basename "${pkg%%@*}")"
	[ -x "${PLUGIN_DIR}/${name}" ] && continue

	# 串行化：同一时刻只一个 go install 在写 module cache。
	# 失败重试 —— 并发争锁和模块下载抖动都是暂时的。
	for attempt in 1 2 3; do
		if flock "${PLUGIN_DIR}.lock" env GOBIN="${PLUGIN_DIR}" go install "${pkg}"; then
			break
		fi
		if [ "${attempt}" -eq 3 ]; then
			echo "编译插件失败：${pkg}" >&2
			exit 1
		fi
		echo "编译 ${name} 失败，重试（${attempt}/3）..." >&2
		sleep 2
	done
done

# protoc 从仓库根跑，--proto_path 指向 api/ —— 这样注册进 protobuf 全局
# 注册表的文件路径是 <域>/service/v1/<文件>.proto。
#
# 路径必须**全局唯一**：整个仓库是单个 Go module，四个域的契约会被链进
# 同一个二进制。protobuf 的注册表按「相对 proto_path 根的路径」做 key，
# 四份都叫 v1/error_reason.proto 会在 init 期 panic
# （proto: file "v1/error_reason.proto" is already registered）。
# 加上域名这一层前缀就天然避开了。
#
# paths=source_relative —— 生成的 .pb.go 跟 proto 待在一起
# （api/<域>/service/v1/）。
cd "${ROOT}"
protoc \
	--proto_path=api \
	--proto_path="${THIRD_PARTY}" \
	--plugin=protoc-gen-go="${PLUGIN_DIR}/protoc-gen-go" \
	--plugin=protoc-gen-go-grpc="${PLUGIN_DIR}/protoc-gen-go-grpc" \
	--plugin=protoc-gen-go-http="${PLUGIN_DIR}/protoc-gen-go-http" \
	--plugin=protoc-gen-openapi="${PLUGIN_DIR}/protoc-gen-openapi" \
	--go_out=paths=source_relative:api \
	--go-grpc_out=paths=source_relative:api \
	--go-http_out=paths=source_relative:api \
	--openapi_out=api \
	--openapi_opt=fq_schema_naming=true,default_response=false \
	"${PROTOS[@]/#/api/${DOMAIN}/service/}"
