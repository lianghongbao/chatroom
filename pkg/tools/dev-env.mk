# 本地开发环境变量（nacos / DB / Redis / JWT）。所有服务共用这一份。
#
# 跨服务共享的值（nacos 地址、DB、Redis、JWT 密钥、内部调用凭证）必须一致 ——
# 不一致的症状是「服务注册上了但跨服务调用超时」或内部端点一律 403。
# 生产部署不要复用这些值，必须走 K8s Secret 注入。
NACOS_SERVERS ?= 127.0.0.1:8848
NACOS_NAMESPACE ?= 3dbbf765-11cf-4622-817f-d890cd670ba0
NACOS_GROUP ?= DEFAULT_GROUP
NACOS_USERNAME ?= nacos
NACOS_PASSWORD ?= 123456
REDIS_ADDR ?= 127.0.0.1:6379
DB_SOURCE ?= root:123456@tcp(127.0.0.1:3306)/test?parseTime=true&loc=Local&charset=utf8mb4
# 集成测试用的 root DSN（无库名）。测试自己建临时库、跑完 DROP。
TEST_ROOT_DSN ?= root:123456@tcp(127.0.0.1:3306)/?parseTime=True&loc=Local
AUTH_JWT_SECRET ?= chatroom-dev-jwt-secret-please-change-in-production-32bytes
# 服务间内部调用凭证。所有服务必须一致：friend 调 user 补全好友资料，
# conversation 调 user 补全会话成员资料，message 调 conversation 的成员
# 身份 / 未读数端点，ws-gateway 调 conversation 的会话成员列表算广播目标。
AUTH_INTERNAL_SECRET ?= chatroom-dev-internal-secret-please-change-in-production

# OTLP gRPC 接收端（Jaeger all-in-one）。留空 = 不开 trace。
# 用 `make infra-up` 起，UI 在 http://127.0.0.1:16686 。
OTEL_EXPORTER_OTLP_ENDPOINT ?= 127.0.0.1:4317

# 通用 env 块，每个 run-* target 都会用一次。
# 用 := + reference 而不是 export，避免污染子 make 的全局 env。
COMMON_ENV = \
	NACOS_SERVERS='$(NACOS_SERVERS)' \
	NACOS_NAMESPACE='$(NACOS_NAMESPACE)' \
	NACOS_GROUP='$(NACOS_GROUP)' \
	NACOS_USERNAME='$(NACOS_USERNAME)' \
	NACOS_PASSWORD='$(NACOS_PASSWORD)' \
	REDIS_ADDR='$(REDIS_ADDR)' \
	DB_SOURCE='$(DB_SOURCE)' \
	AUTH_JWT_SECRET='$(AUTH_JWT_SECRET)' \
	AUTH_INTERNAL_SECRET='$(AUTH_INTERNAL_SECRET)' \
	OTEL_EXPORTER_OTLP_ENDPOINT='$(OTEL_EXPORTER_OTLP_ENDPOINT)'
