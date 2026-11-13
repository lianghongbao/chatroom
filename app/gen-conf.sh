#!/usr/bin/env bash
# 生成某个服务的配置 proto 代码（internal/conf/conf.pb.go）。
#
# 用 protoc 而不是 buf：和契约层一致，插件版本钉死、离线可跑。
#
# 用法：gen-conf.sh <服务名>      例：gen-conf.sh user
# 必须在对应服务目录（app/<服务>/service/）下执行。

set -euo pipefail

SERVICE="${1:?用法: gen-conf.sh <服务名>}"

SERVICE_DIR="$(pwd)"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ ! -f "${SERVICE_DIR}/internal/conf/conf.proto" ]; then
	echo "找不到 ${SERVICE_DIR}/internal/conf/conf.proto —— 请在 app/<服务>/service/ 目录下执行" >&2
	exit 1
fi

# 插件版本钉死，与 api/gen.sh 和 go.mod 里的 protobuf 版本对齐。
PLUGIN_PKG="google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12"

PLUGIN_DIR="${ROOT}/.cache/protoc-plugins"
PLUGIN_KEY="$(printf '%s\n' "${PLUGIN_PKG}" | md5sum | cut -d' ' -f1)"
PLUGIN_DIR="${PLUGIN_DIR}/${PLUGIN_KEY}"

mkdir -p "${PLUGIN_DIR}"

name="$(basename "${PLUGIN_PKG%%@*}")"
if [ ! -x "${PLUGIN_DIR}/${name}" ]; then
	echo "准备 protoc 插件（${PLUGIN_DIR}）..."
	for attempt in 1 2 3; do
		if flock "${PLUGIN_DIR}.lock" env GOBIN="${PLUGIN_DIR}" go install "${PLUGIN_PKG}"; then
			break
		fi
		if [ "${attempt}" -eq 3 ]; then
			echo "编译插件失败：${PLUGIN_PKG}" >&2
			exit 1
		fi
		echo "编译 ${name} 失败，重试（${attempt}/3）..." >&2
		sleep 2
	done
fi

# protoc 从仓库根跑，--proto_path=app —— 注册进 protobuf 全局注册表的
# 路径是 <服务>/service/internal/conf/conf.proto。
# 路径必须全局唯一：整仓是单个 Go module，多个服务的 conf 包会被链进同一
# 个二进制，注册表按「相对 proto_path 根的路径」做 key，同名会在 init 期 panic。
# 加上服务名这一层前缀就天然避开了。
cd "${ROOT}"
protoc \
	--proto_path=app \
	--plugin=protoc-gen-go="${PLUGIN_DIR}/protoc-gen-go" \
	--go_out=paths=source_relative:app \
	"app/${SERVICE}/service/internal/conf/conf.proto"

echo "已生成 app/${SERVICE}/service/internal/conf/conf.pb.go"
