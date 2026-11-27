# WebSocket 网关

网关**无状态**：不落库，只维护连接 + 转发。端点 `ws://host:8004/ws?token={JWT}`。

token 放在 query 里而不是 header —— 浏览器的 WebSocket API 不能自定义 header。

相关：[README](../README.md) ｜ [跨服务通信](cross-service.md)

## 协议

上行：

```json
{ "type": "chat", "conversation_id": 1, "msg_type": 1, "content": "hello" }
{ "type": "status", "user_id": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90" }
{ "type": "status", "conversation_id": 1 }
{ "type": "resume", "conversation_id": 1, "last_message_id": 9 }
{ "type": "ping" }
```

下行：

```json
{ "type": "message", "data": { "message_id": 9, "conversation_id": 1, "sender_id": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90", "msg_type": 1, "content": "hello", "created_at": 1700000000 } }
{ "type": "ack", "data": { "message_id": 9, "conversation_id": 1, "created_at": 1700000000 } }
{ "type": "pong" }
{ "type": "online", "data": { "user_id": "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90", "online": true } }
{ "type": "presence", "data": { "conversation_id": 1, "members": { "0192f0a1-7c3d-7b2e-9f41-8a5c6d7e8f90": true, "0192f0a2-7c3d-7b2e-9f41-8a5c6d7e8f90": false } } }
{ "type": "resume_result", "data": { "conversation_id": 1, "count": 3, "last_message_id": 12, "has_more": false } }
{ "type": "error", "data": { "message": "...", "code": "SEND_FAILED" } }
```

`chat` 帧由网关转发到 message 服务落库，落库成功后给发送方回 `ack`；
消息本身**不**通过发送方那条连接推回 —— 它走 Redis Pub/Sub 广播回来，
所有连接（含发送方自己的其它端）都收到，路径统一。

## 三种帧的语义

- **`status` 查在线状态**。带 `user_id` 查一个人，带 `conversation_id` 查整个会话
  （回 `presence` 帧）。在线状态存在 Redis 里，所以**跨实例可见**。
- **`resume` 断线重连补消息**。客户端带上 `last_message_id`，网关调 message 服务的
  `ListMessagesSince` 把 id 更大的消息按**正序**补回来（跟正常推送走同一个 `message` 帧），
  最后回一个 `resume_result` 帧。一次最多补 50 条 —— 写队列默认 64，补太多会挤掉消息。
- **`online` 帧既是响应也是通知**。别人上下线时，会话里的同伴也会收到 `online` 帧。

## 消息流转

```
客户端A ──ws──► ws-gateway ──gRPC──► message 服务 ──► MySQL 落库
                    ▲                                    │
                    │                                    ▼
              Redis Pub/Sub ◄─── chatroom:msg:{convId} ─┘
                    │
                    └──► 目标成员在本实例上的连接 ──► 客户端B
```

网关订阅 `chatroom:msg:*`（模式订阅），收到广播后按会话成员列表算出推送目标。

## 多实例部署

网关是无状态的，多开几个实例直接水平扩。注意两点：

- **端口要错开**。同一台机器上跑多个实例时用 `GATEWAY_HTTP_ADDR` 给每个实例一个
  不同端口（默认 `0.0.0.0:8004`）。
- **实例 id 要错开**。用 `GATEWAY_INSTANCE_ID` 指定（默认取 hostname）。
  同一台机器上 hostname 是一样的，两个实例写进 Redis 的 `online:{uid}` 值会撞。

```bash
GATEWAY_HTTP_ADDR=0.0.0.0:8004 GATEWAY_INSTANCE_ID=gw-1 chatroom-ws-gateway \
  -conf app/ws-gateway/service/configs/config.yaml -bootstrap app/ws-gateway/service/configs/bootstrap.yaml
GATEWAY_HTTP_ADDR=0.0.0.0:8005 GATEWAY_INSTANCE_ID=gw-2 chatroom-ws-gateway \
  -conf app/ws-gateway/service/configs/config.yaml -bootstrap app/ws-gateway/service/configs/bootstrap.yaml
```

每个实例都订阅 `chatroom:msg:*`，都会收到**所有**会话的消息，再按会话成员列表
过滤出「连在本实例上的那些连接」推出去。

> K8s 里上面那两件事不用手工做：`GATEWAY_INSTANCE_ID` 直接取 pod 名
> （`fieldRef: metadata.name`），天然错开；副本数交给 HPA。

## 准入限制

网关是**唯一没有业务鉴权兜底的入口** —— 别的服务进来至少要过 JWT，
网关这边握手失败只是关连接。所以握手上锁了三道闸门（`internal/gateway/limiter.go` + `pool.go`）：

| 闸门 | 环境变量 | 默认 | 拒的时候 |
|---|---|---|---|
| 单 IP 握手限流（令牌桶） | `GATEWAY_HANDSHAKE_RATE` / `GATEWAY_HANDSHAKE_BURST` | 20 次/秒，突发 40 | **429** |
| 本实例连接数上限 | `GATEWAY_MAX_CONNECTIONS` | 5000 | **503** |
| 单用户连接数上限 | `GATEWAY_MAX_CONNS_PER_USER` | 5 | **429** |

顺序是**先限流再验 token**：IP 限流在最外层，不然攻击者可以用假 token
把验签的 CPU 打满。连接配额是**原子预留**的 —— 预留成功才做 Upgrade，
Upgrade 失败必须把配额还回去。

两种连接数超限给不同状态码：实例总连接数满了是**服务端容量问题**（503，
客户端稍后重试），单用户超了是**客户端自己的配额问题**（429，重连可以）。

上限按**单实例**计。扩容后总上限 = 副本数 × `GATEWAY_MAX_CONNECTIONS`。

IP 从 `RemoteAddr` 取。只有网关前面确实有 ingress、且客户端**不能**直连
网关时，才开 `GATEWAY_TRUST_PROXY_HEADER=true` 读 `X-Forwarded-For` ——
开着的话任何人都能伪造这个头绕过 IP 限流。

## 几条规则

- **单写者模型**。只有 `writePump` 调 `WriteMessage`，其它地方往 `send` 队列里塞 ——
  gorilla/websocket 不允许并发写。
- **队列满时丢最老的一条**。不能让一个卡住的客户端把网关内存拖爆。
- **成员身份确认不了就拒绝**。conversation 服务不可达时 message 服务返
  `ErrMessageConversationUnavailable`，不 fail open。
- **广播失败不影响落库结果**。消息已经存下来了，客户端拉历史能拿到。
- **在线状态用 Lua 比较并删**。用户可能在另一个实例重连，GET 再 DEL 会误删新实例写的状态。
- **成员列表带 TTL 缓存**（默认 1 分钟）。每条消息都问一次 conversation 服务太贵；
  代价是刚被踢的人最多多收一分钟消息。
