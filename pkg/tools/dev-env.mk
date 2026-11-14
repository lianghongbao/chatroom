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

# 阿里云 OSS（头像存储）。
#
# AK/SK **不写在这里** —— 这个文件是入库的。只从 shell 环境读：
#   export OSS_ACCESS_KEY_ID=xxx
#   export OSS_ACCESS_KEY_SECRET=xxx
#   make run-user
# 没设的话服务启动时会因为 storage 配置不完整而报错（见 pkg/oss.Config.validate）。
#
# 角色 ARN 只用于调 STS AssumeRole，拿到的是临时凭证，客户端拿不到长期 AK。
OSS_ENDPOINT ?= oss-cn-guangzhou.aliyuncs.com
OSS_BUCKET ?= chatroomoss
OSS_REGION ?= cn-guangzhou
OSS_ROLE_ARN ?= acs:ram::1471593004552671:role/chatroom-oss-sts-role
OSS_ACCESS_KEY_ID ?= $(OSS_ACCESS_KEY_ID)
OSS_ACCESS_KEY_SECRET ?= $(OSS_ACCESS_KEY_SECRET)
# 客户端直传目标 —— 走 OSS 域名，不走 CDN：签名更干净，也不依赖 CDN 缓存刷新。
OSS_UPLOAD_HOST ?= https://chatroomoss.oss-cn-guangzhou.aliyuncs.com
# 对外公开访问前缀 —— 走 ESA 加速域名。桶是私有的，只有 CDN 能匿名读。
# 域名是 ESA 站点 lamborai.com 下的一级子域，能被现有的 *.lamborai.com
# 通配符证书覆盖（static.chatroom.lamborai.com 这种二级子域不行，通配符不递归）。
OSS_URL_PREFIX ?= https://static-chat.lamborai.com

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
	OTEL_EXPORTER_OTLP_ENDPOINT='$(OTEL_EXPORTER_OTLP_ENDPOINT)' \
	OSS_ENDPOINT='$(OSS_ENDPOINT)' \
	OSS_BUCKET='$(OSS_BUCKET)' \
	OSS_REGION='$(OSS_REGION)' \
	OSS_ROLE_ARN='$(OSS_ROLE_ARN)' \
	OSS_ACCESS_KEY_ID='$(OSS_ACCESS_KEY_ID)' \
	OSS_ACCESS_KEY_SECRET='$(OSS_ACCESS_KEY_SECRET)' \
	OSS_UPLOAD_HOST='$(OSS_UPLOAD_HOST)' \
	OSS_URL_PREFIX='$(OSS_URL_PREFIX)'
