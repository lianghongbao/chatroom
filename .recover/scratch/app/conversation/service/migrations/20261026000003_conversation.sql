-- migrations/20261026000003_conversation.sql
-- chatroom-conversation 空库初始化。表结构权威是 ent schema，改 schema 后重启服务
-- 即自动增量迁移，这里只用于不依赖 Go 运行时建空库。详见 migrations/README.md。

CREATE TABLE IF NOT EXISTS `conversations` (
    `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键, 自增',
    `type`        TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '会话类型 1=单聊 2=群聊',
    `name`        VARCHAR(128) NOT NULL DEFAULT '' COMMENT '群名, 单聊为空, ≤ 128 字符',
    `owner_id`    CHAR(36) NOT NULL DEFAULT '' COMMENT '群主 uid, 单聊为空',
    -- 单聊的成员对键 "uid1,uid2"（两个 uid 升序）。群聊为 NULL。
    -- 唯一索引就是"单聊唯一"的数据库级约束 —— 并发建同一对成员的单聊时，
    -- 后到的一方被索引挡下。MySQL 唯一索引不约束 NULL，所以群聊互不冲突。
    `pair_key`    VARCHAR(80) DEFAULT NULL COMMENT '单聊成员对键 ''uid1,uid2'', 群聊为 NULL',
    `created_at`  TIMESTAMP NOT NULL COMMENT '创建时间',
    -- datetime(6) 微秒精度：秒精度下同一秒建的会话排序分不出先后。
    `updated_at`  DATETIME(6) NOT NULL COMMENT '最后活动时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `conversation_pair_key` (`pair_key`),
    KEY `conversation_updated_at` (`updated_at`),
    KEY `conversation_owner_id` (`owner_id`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '会话表';

CREATE TABLE IF NOT EXISTS `conversation_members` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键, 自增',
    `user_id`         CHAR(36) NOT NULL COMMENT '成员 uid',
    `unread_count`    INT NOT NULL DEFAULT 0 COMMENT '未读消息数',
    `joined_at`       TIMESTAMP NOT NULL COMMENT '加入时间',
    `conversation_id` BIGINT UNSIGNED NOT NULL COMMENT '会话 id',
    PRIMARY KEY (`id`),
    UNIQUE KEY `conversationmember_conversation_id_user_id` (`conversation_id`, `user_id`),
    KEY `conversationmember_user_id_conversation_id` (`user_id`, `conversation_id`)
    -- 故意不建外键。引用完整性靠应用层保证：成员行由 repo 在同一事务里
    -- 跟着会话一起写 / 删。与 ent 的 schema.WithForeignKeys(false) 一致。
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '会话成员表';
