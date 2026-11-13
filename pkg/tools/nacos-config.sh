#!/usr/bin/env bash
# 把某服务的 configs/config.yaml 同步到 nacos 配置中心。
#
# 本地 config.yaml 和 nacos 上的 chatroom-{svc}.yaml 是同一份配置的两份拷贝，
# 靠人肉记得改就会漂移（改了本地没推 → 服务读的还是 nacos 上的旧值）。
#
# 分层约定：
#   configs/bootstrap.yaml   引导配置：连哪个 nacos、哪条 dataId。只在本地，
#                            永远不进 nacos —— 「连哪个 nacos」不能由 nacos
#                            自己决定，否则会形成循环依赖。
#   configs/config.yaml      业务配置：这份原样进 nacos。
#
# 用法（从服务目录跑）：
#   tools/nacos-config.sh diff    # 对比本地和 nacos，不改任何东西
#   tools/nacos-config.sh push    # 推送
#
# 依赖：python3 + PyYAML。报 "No module named 'yaml'" 时用系统的那个跑：
#   PATH="/usr/bin:$PATH" tools/nacos-config.sh diff

set -euo pipefail

# 本脚本在 chatroom/pkg/tools/ 下，服务目录通过环境变量传入。
SERVICE_DIR="${SERVICE_DIR:?需要指定服务目录: SERVICE_DIR=../user-service tools/nacos-config.sh diff}"
ROOT="$(cd "$SERVICE_DIR" && pwd)"
DEV_ENV="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/dev-env.mk"

# 服务标识，决定 nacos 上的 dataId；由服务目录下的 tools/nacos-config.sh 传入。
SERVICE="${SERVICE:?需要指定服务标识: SERVICE=user ...}"

# 从 tools/dev-env.mk 取默认值（Make 语法 `KEY ?= value`）。
# 已经 export 过的同名环境变量优先。
dev_env() {
	sed -n "s/^$1[[:space:]]*?=[[:space:]]*//p" "$DEV_ENV" | head -1
}

NACOS_SERVERS="${NACOS_SERVERS:-$(dev_env NACOS_SERVERS)}"
NACOS_NAMESPACE="${NACOS_NAMESPACE:-$(dev_env NACOS_NAMESPACE)}"
NACOS_GROUP="${NACOS_GROUP:-$(dev_env NACOS_GROUP)}"
NACOS_USERNAME="${NACOS_USERNAME:-$(dev_env NACOS_USERNAME)}"
NACOS_PASSWORD="${NACOS_PASSWORD:-$(dev_env NACOS_PASSWORD)}"

API="http://${NACOS_SERVERS}/nacos/v3"

# 服务中文名，写进 nacos 的 desc 字段。
DESC="${DESC:?需要指定服务中文名: DESC=用户服务 ...}"

# 取 access token（nacos 3.x 的 v3 API 用它做鉴权）。
access_token() {
	curl -s -m 10 -X POST "$API/auth/user/login" \
		-d "username=$NACOS_USERNAME" -d "password=$NACOS_PASSWORD" |
		python3 -c 'import sys,json; print(json.load(sys.stdin)["accessToken"])'
}

# 从 configs/bootstrap.yaml 读 dataId。服务启动时读的是同一份，所以必须
# 同源。留空时回退到 <service_name>.yaml，跟服务端的回退规则一致
# （chatroom/pkg/nacos.Options.DataIDOrDefault）。
data_id_of() {
	python3 - "$ROOT/configs/bootstrap.yaml" <<'PYEOF'
import sys, yaml
with open(sys.argv[1], encoding="utf-8") as f:
    doc = yaml.safe_load(f) or {}
nacos = (doc.get("registry") or {}).get("nacos") or {}
data_id = (nacos.get("data_id") or "").strip()
if not data_id:
    svc_name = (nacos.get("service_name") or "").strip()
    # service_name 里可能是 ${NACOS_SERVICE_NAME:chatroom-x} 占位符 —— 取默认值。
    if ":" in svc_name and "}" in svc_name:
        svc_name = svc_name.split(":", 1)[1].split("}", 1)[0]
    data_id = (svc_name or "chatroom") + ".yaml"
sys.stdout.write(data_id)
PYEOF
}

# 把本地 config.yaml 转成 nacos 上要放的内容。
# 断言 config.yaml 里没有 registry 段 —— 万一有人写回去，推上去就会形成
# 「nacos 决定连哪个 nacos」的循环依赖。宁可推失败也不要静默推坏。
build_content() {
	python3 - "$1" <<'PYEOF'
import sys, yaml
with open(sys.argv[1], encoding="utf-8") as f:
    doc = yaml.safe_load(f) or {}
if "registry" in doc:
    sys.exit("registry 段不应出现在 config.yaml —— 它属于 bootstrap.yaml")
sys.stdout.write(yaml.safe_dump(doc, allow_unicode=True, sort_keys=False, default_flow_style=False).rstrip("\n") + "\n")
PYEOF
}

# 从 nacos 拉一份配置内容。dataId 不存在时服务端返回 data 为字符串，
# 这里统一当成空内容。
fetch_content() {
	local data_id="$1"
	curl -s -m 10 "$API/admin/cs/config?accessToken=$TOKEN&dataId=$data_id&groupName=$NACOS_GROUP&namespaceId=$NACOS_NAMESPACE" |
		python3 -c '
import sys, json
data = json.load(sys.stdin).get("data")
if not isinstance(data, dict):
    sys.exit(0)
sys.stdout.write((data.get("content") or "").replace("\r\n", "\n"))
'
}

publish() {
	local data_id="$1" desc="$2" content_file="$3"
	local code
	code=$(curl -s -m 15 -o /tmp/nacos-publish.out -w '%{http_code}' -X POST "$API/admin/cs/config" \
		--data-urlencode "accessToken=$TOKEN" \
		--data-urlencode "dataId=$data_id" \
		--data-urlencode "groupName=$NACOS_GROUP" \
		--data-urlencode "namespaceId=$NACOS_NAMESPACE" \
		--data-urlencode "type=yaml" \
		--data-urlencode "desc=$desc" \
		--data-urlencode "content@$content_file")
	if [ "$code" != "200" ]; then
		echo "  ✗ $data_id 发布失败 HTTP=$code: $(cat /tmp/nacos-publish.out)" >&2
		return 1
	fi
	echo "  ✓ $data_id 已发布"
}

cmd_diff() {
	local local_file="$ROOT/configs/config.yaml"
	local data_id="chatroom-$SERVICE.yaml"
	local tmp
	tmp=$(mktemp)
	build_content "$local_file" >"$tmp"
	local remote
	remote=$(fetch_content "$data_id")
	if [ "$remote" = "$(cat "$tmp")" ]; then
		echo "  = $data_id 一致"
	else
		echo "  ! $data_id 与本地不一致"
		diff -u <(printf '%s' "$remote") "$tmp" | sed 's/^/      /' || true
		return 1
	fi
	rm -f "$tmp"
}

cmd_push() {
	local local_file="$ROOT/configs/config.yaml"
	local data_id="chatroom-$SERVICE.yaml"
	if [ ! -f "$local_file" ]; then
		echo "  ✗ 找不到 $local_file" >&2
		return 1
	fi
	local tmp
	tmp=$(mktemp)
	build_content "$local_file" >"$tmp"
	publish "$data_id" "$DESC" "$tmp"
	rm -f "$tmp"
}

TOKEN=$(access_token)
if [ -z "$TOKEN" ]; then
	echo "nacos 登录失败：$NACOS_SERVERS 用户 $NACOS_USERNAME" >&2
	exit 1
fi

case "${1:-diff}" in
diff)
	cmd_diff
	;;
push)
	cmd_push
	;;
*)
	echo "用法: $0 {diff|push}" >&2
	exit 2
	;;
esac
