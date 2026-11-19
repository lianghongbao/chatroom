#!/usr/bin/env bash
# ============================================================
# chatroom 部署脚本
# ============================================================
# 起 nacos + 五个业务服务。MySQL / Redis 走共享底座（deploy/infra/），
# 本脚本不碰那两个容器。
#
#   ./deploy.sh up       首次部署 / 改配置后应用
#   ./deploy.sh status   看状态
#   ./deploy.sh down     全停（数据卷保留）
#   ./deploy.sh logs     跟日志
#
# set -euo pipefail：任何一步失败就停，别带着半成品往下跑。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

PROJECT=chatroom
NACOS_CONTAINER=chatroom-nacos

load_env() {
	if [ ! -f .env ]; then
		echo "❌ 缺少 .env —— 先 cp .env.example .env 并填好凭证" >&2
		exit 1
	fi
	set -a
	# shellcheck disable=SC1091
	source .env
	set +a
}

# 确保 nacos 里有管理员账号。
#
# nacos 3.x 开了 NACOS_AUTH_ENABLE 后，**首次启动不会自动建管理员**。
# 没有管理员的话所有 API 都返 401（"User not found!"），业务服务启动时
# 直接 panic，整套服务起不来。所以这里补建一次。
#
# 已存在的话这个接口会返非 0 code，忽略即可 —— 不能因此中断部署。
ensure_nacos_admin() {
	echo "▶ 确保 nacos 管理员账号存在..."
	local resp
	resp=$(docker exec "$NACOS_CONTAINER" \
		curl -s -m 10 -X POST "http://localhost:8848/nacos/v3/auth/user/admin" \
		--data-urlencode "username=${NACOS_USERNAME}" \
		--data-urlencode "password=${NACOS_PASSWORD}" 2>/dev/null || true)
	if echo "$resp" | grep -q '"code":0'; then
		echo "✅ nacos 管理员 ${NACOS_USERNAME} 已就绪"
	else
		# 已存在（重复创建）也走这里 —— 不中断
		echo "✅ nacos 管理员已存在（跳过创建）"
	fi
}

# 确认管理员能登录 —— 登录不成功的话业务服务一定起不来。
verify_nacos_login() {
	echo "▶ 校验 nacos 登录..."
	local resp
	resp=$(docker exec "$NACOS_CONTAINER" \
		curl -s -m 10 -X POST "http://localhost:8848/nacos/v3/auth/user/login" \
		--data-urlencode "username=${NACOS_USERNAME}" \
		--data-urlencode "password=${NACOS_PASSWORD}" 2>/dev/null || true)
	if echo "$resp" | grep -q 'accessToken'; then
		echo "✅ nacos 登录正常"
	else
		echo "❌ nacos 登录失败 —— 业务服务会 panic。响应：$resp" >&2
		echo "   检查 deploy/.env 里的 NACOS_USERNAME / NACOS_PASSWORD" >&2
		exit 1
	fi
}

cmd_up() {
	load_env

	# 1. nacos 先单独起 —— 它要先有管理员，业务服务才连得上
	echo "▶ 启动 nacos..."
	docker compose -p "$PROJECT" up -d nacos

	# 2. 等 nacos 健康（healthcheck 打 /nacos/v1/ns/operator/metrics）
	echo "⏳ 等待 nacos 健康..."
	local ok=0
	for _ in $(seq 1 30); do
		local status
		status=$(docker inspect -f '{{.State.Health.Status}}' "$NACOS_CONTAINER" 2>/dev/null || echo "starting")
		if [ "$status" = "healthy" ]; then
			ok=1
			break
		fi
		sleep 2
	done
	if [ "$ok" != "1" ]; then
		echo "❌ nacos 未通过健康检查 —— docker logs $NACOS_CONTAINER 排查" >&2
		exit 1
	fi
	echo "✅ nacos 健康"

	# 3. 建管理员 + 校验登录
	ensure_nacos_admin
	verify_nacos_login

	# 4. 业务服务（compose 的 depends_on 保证启动顺序）
	echo "▶ 启动业务服务..."
	docker compose -p "$PROJECT" up -d

	echo ""
	echo "✅ chatroom 已部署"
	docker compose -p "$PROJECT" ps
	echo ""
	echo "各服务端口：user 8000 / friend 8001 / conversation 8002 / message 8003 / ws-gateway 8004"
}

cmd_status() {
	docker compose -p "$PROJECT" ps
}

cmd_down() {
	docker compose -p "$PROJECT" down
	echo "✅ 已停止（数据卷保留）"
}

cmd_logs() {
	docker compose -p "$PROJECT" logs -f "${@:-}"
}

case "${1:-up}" in
up)
	cmd_up
	;;
status)
	cmd_status
	;;
down)
	cmd_down
	;;
logs)
	shift
	cmd_logs "$@"
	;;
*)
	echo "用法: $0 {up|status|down|logs [服务名]}" >&2
	exit 2
	;;
esac
