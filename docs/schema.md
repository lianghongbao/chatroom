# 表结构变更

**权威是 ent schema**（每个服务各有一份，在 `app/<服务>/service/internal/data/ent/schema/`）。
改完 schema 重启对应服务，ent 会比对现状，
只发必要的 `ALTER TABLE`（加列 / 改列宽 / 补索引）把库对齐到 schema。不动已有数据。

```bash
vim app/<服务>/service/internal/data/ent/schema/xxx.go
make -C app/<服务>/service run   # 重启即生效
```

注意：**ent 不删列** —— 废弃的列要手工 `ALTER TABLE ... DROP COLUMN`。

**不建外键约束**（`schema.WithForeignKeys(false)`）。引用完整性靠应用层保证：
成员行由 repo 在同一个事务里跟着会话一起写 / 删，不会出现孤儿行。
代价是数据库不再挡脏数据，要靠应用层保证写路径正确。

> ent 的 auto-migrate **不删已有约束**。已经建过外键的库要手工
> `ALTER TABLE conversation_members DROP FOREIGN KEY <约束名>`。

> **多副本下的并发 DDL** —— auto-migrate 每个进程启动时各自执行。
> 「镜像里带了新 schema」撞上「HPA 同时扩容」，多个副本可能同时发 `ALTER TABLE`，
> 撞上 `Duplicate column name`，新副本 CrashLoopBackOff。
> 规避：发版窗口内先把副本数压到 1，等 schema 对齐、pod 正常起来后再放开。

`app/<服务>/service/migrations/*.sql` 是「空库初始化快照」，用来在不依赖 Go 运行时的情况下建出空库
（干净的测试库、手工初始化）。文件名 `<YYYYMMDDHHMMSS>_<服务名>.sql`，按字典序即创建时间序。

> `CREATE TABLE IF NOT EXISTS` 在表已存在时整条跳过，所以这些脚本**不能**用来对齐已有的库。

改完 schema 建议顺手核对脚本与 ent 产物是否一致：

```bash
make -C app/<服务>/service test -count=1
```

## 单聊唯一性

同一对成员的单聊必须唯一。`conversations.pair_key` 存归一化后的成员对
（`"uid1,uid2"`，小的在前），**唯一索引**是唯一的仲裁者 —— 并发建单聊时两个请求
都会尝试插入同一个 `pair_key`，后到的一方撞索引，被 repo 捕获后回查并复用已有会话。

群聊的 `pair_key` 是 `NULL`，MySQL 唯一索引不约束 NULL，所以群聊互不冲突。

> 归一化只在 repo 的写入路径做一次。绕过 repo 直接写库会写入未归一化的 key，
> 索引就挡不住重复单聊了。

## 用户标识（uid）

用户标识是**字符串 UUIDv7**，不是自增整数。

```
01a19d58-f992-7ca9-807e-dc1cb20c5ef7
```

- **定宽 36 字符**，DB 列类型 `CHAR(36)`。
- **UUIDv7**：前 48 位是毫秒时间戳，字符串字典序 = 生成时间序。代码里需要一致全序的地方
  （单聊 `pair_key` 归一化、成员列表排序）直接按字符串排。
- **不可枚举**：自增 id 会被遍历，uid 不会。
- **不透明**：客户端不解析、不依赖其结构。

JWT 的 `sub` claim 存的就是 uid 字符串。

> **uid ≠ 记录主键。** `users.id` 是 uid（字符串），但 `friends.id`、`conversations.id`、
> `messages.id` 仍是自增整数 —— 那是记录标识，只在本服务内部用，不跨服务传。
> 跨服务传的永远是 uid。

「不存在」的哨兵值是空串 `""`，不是 `0`。
