#!/usr/bin/env bash
# ============================================================
# chatroom 部署脚本
# ============================================================
# 起 nacos + 五个业务服务。MySQL / Redis 是独立部署的服务
# （deploy/mysql、deploy/redis），本脚本不碰那两个容器。
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
# 数据存储的网络 —— 业务容器和数据存储都接进来
NETWORK=data-net
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

	# 0. data-net 是外部网络，多个项目的容器接进来互相访问。
	# 外部网络不会自动创建，幂等地补一次。
	if ! docker network inspect "${NETWORK}" >/dev/null 2>&1; then
		echo "▶ 创建网络 ${NETWORK}"
		docker network create "${NETWORK}" >/dev/null
	else
		echo "✓ 网络 ${NETWORK} 已存在"
	fi

	# 1. 建库
	#
	# MySQL 是独立部署的服务，chatroom 用自己的 schema。
	# ent 的 auto_migrate 只建表不建 schema，所以库要先建出来。
	# 表结构由服务启动时的 auto_migrate 对齐。
	if ! docker ps --format '{{.Names}}' | grep -qx mysql; then
		echo "❌ 找不到 mysql 容器 —— 数据存储是独立部署的服务，要先起"
		echo "   cd ../local-life-api/deploy/mysql && docker compose up -d"
		exit 1
	fi
	echo "▶ 确保 chatroom 库存在..."
	# 建库输出只有 mysql 客户端的 password warning 时算成功;
	# 有别的输出就是失败(密码不对之类)。
	CREATE_DB_OUT=$(docker exec mysql mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" -e "
		CREATE DATABASE IF NOT EXISTS \`chatroom\`
			DEFAULT CHARACTER SET utf8mb4
			COLLATE utf8mb4_bin;" 2>&1 | grep -v "^mysql: \[Warning\]" || true)
	if [ -n "${CREATE_DB_OUT}" ]; then
		echo "❌ 建库失败: ${CREATE_DB_OUT}" >&2
		echo "   检查 MYSQL_ROOT_PASSWORD 跟数据存储是否一致" >&2
		exit 1
	fi
	echo "✅ chatroom 库就绪"

	# 2. nacos 先单独起 —— 它要先有管理员，业务服务才连得上
	echo "▶ 启动 nacos..."
	docker compose -p "$PROJECT" up -d nacos

	# 3. 等 nacos 健康（healthcheck 打 /nacos/v1/ns/operator/metrics）
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

	# 4. 建管理员 + 校验登录
	ensure_nacos_admin
	verify_nacos_login

	# 5. 业务服务（compose 的 depends_on 保证启动顺序）
	#
	# 首次创建时有个竞态：依赖链上游的服务（user）刚建好，healthcheck
	# 还没通过，下游就都停在 Created。compose 不会自己重试判定，
	# 再跑一次 up 就全通了。
	echo "▶ 启动业务服务..."
	docker compose -p "$PROJECT" up -d
	# 再跑一次 —— 让 compose 重新判定 depends_on 的健康门禁。
	# 已经起来的容器不会被重建。
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
