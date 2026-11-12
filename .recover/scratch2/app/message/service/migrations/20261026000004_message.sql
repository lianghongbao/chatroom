-- migrations/20261026000004_message.sql
-- chatroom-message 空库初始化。表结构权威是 ent schema，改 schema 后重启服务
-- 即自动增量迁移，这里只用于不依赖 Go 运行时建空库。详见 migrations/README.md。

CREATE TABLE IF NOT EXISTS `messages` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键, 自增',
    `conversation_id` BIGINT UNSIGNED NOT NULL COMMENT '会话 id',
    `sender_id`       CHAR(36) NOT NULL COMMENT '发送者 uid',
    `type`            TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '消息类型 1=文本 2=图片',
    `content`         TEXT NOT NULL COMMENT '消息内容/图片 URL',
    `created_at`      TIMESTAMP NOT NULL COMMENT '发送时间',
    PRIMARY KEY (`id`),
    -- 历史消息的主查询路径：某会话的消息按 id 倒序。
    -- id 是主键，InnoDB 会把它挂在二级索引末尾，所以这个索引天然覆盖
    -- conversation_id + id 的排序 —— 游标分页直接走它，不用回表排序。
    KEY `message_conversation_id_id` (`conversation_id`, `id`),
    KEY `message_sender_id` (`sender_id`)
    -- 故意不建外键。conversation_id 指向的会话可能已被解散，消息保留着
    -- （历史可查）。外键会挡住这件事。与 ent 的 schema.WithForeignKeys(false) 一致。
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '消息表';
