#!/usr/bin/env bash
# 把本服务的 configs/config.yaml 同步到 nacos 配置中心。
#
# 为什么要这个脚本：本地 config.yaml 和 nacos 上的 chatroom-{svc}.yaml 是
# 同一份配置的两份拷贝，靠人肉记得改就会漂移（改了本地没推 → 服务读的还是
# nacos 上的旧值，症状是「改了配置不生效」）。这个脚本让两份拷贝由同一次
# 生成产生。
#
# 分层约定（每个服务两份本地文件）：
#   configs/bootstrap.yaml   引导配置：连哪个 nacos、哪条 dataId。只在本地，
#                            永远不进 nacos —— 「连哪个 nacos」不能由 nacos
#                            自己决定，否则 nacos 里的旧 registry 会盖掉本地
#                            刚改的地址，形成循环依赖。
#   configs/config.yaml      业务配置：基础设施连接信息 + 业务默认值 +
#                            env 占位符。**这份原样进 nacos**。
#
# 用法：
#   tools/nacos-config.sh diff    # 对比本地和 nacos，不改任何东西
#   tools/nacos-config.sh push    # 推送本服务
#
# 环境变量 NACOS_SERVERS / NACOS_NAMESPACE / NACOS_GROUP / NACOS_USERNAME /
# NACOS_PASSWORD 没设时从 tools/dev-env.mk 读（那才是唯一事实源）。
#
# 依赖：python3 + PyYAML。PyYAML 不一定装在 PATH 里的那个 python3 上 ——
# uv/conda 管的 python3 常常没有，而系统自带的 /usr/bin/python3 有。
# 报 "ModuleNotFoundError: No module named 'yaml'" 就是这个原因，
# 用系统的那个跑：PATH="/usr/bin:$PATH" tools/nacos-config.sh diff

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEV_ENV="$ROOT/tools/dev-env.mk"

SERVICE="user"

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

# 服务中文名，写进 nacos 的 desc 字段，运维在控制台看得懂。
DESC="用户服务"

# 取 access token（nacos 3.x 的 v3 API 用它做鉴权）。
access_token() {
	curl -s -m 10 -X POST "$API/auth/user/login" \
		-d "username=$NACOS_USERNAME" -d "password=$NACOS_PASSWORD" |
		python3 -c 'import sys,json; print(json.load(sys.stdin)["accessToken"])'
}

# 从 configs/bootstrap.yaml 读 dataId。
#
# dataId 写在 bootstrap.yaml 里（那是引导信息），服务启动时读的也是同一份，
# 所以这里必须从同一个源取 —— 两边各写一份迟早对不上，"推上去了但服务读的是
# 另一条"这种症状极难查。
#
# 留空时回退到 <service_name>.yaml，跟服务端的回退规则一致
# （pkg/nacos.Options.DataIDOrDefault）。
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
#
# config.yaml 现在**原样**进 nacos —— registry 段已经拆到 bootstrap.yaml 了。
# 但这里仍然做一次断言：万一有人又把 registry 写回 config.yaml，推上去就会
# 形成「nacos 决定连哪个 nacos」的循环依赖 —— 本地改了 nacos 地址不生效。
# 宁可推失败也不要静默推坏。
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
