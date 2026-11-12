-- migrations/20261026000002_friend.sql
-- chatroom-friend 空库初始化。表结构权威是 ent schema，改 schema 后重启服务
-- 即自动增量迁移，这里只用于不依赖 Go 运行时建空库。详见 migrations/README.md。
--
-- 一条记录代表 user_id 对 friend_id 提过申请 / 已是好友，user_id 是发起方。

CREATE TABLE IF NOT EXISTS `friends` (
    `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键, 自增',
    `user_id`    CHAR(36) NOT NULL COMMENT '发起方 uid',
    `friend_id`  CHAR(36) NOT NULL COMMENT '目标方 uid',
    `status`     TINYINT UNSIGNED NOT NULL DEFAULT '0' COMMENT '状态 0=PENDING 1=ACCEPTED 2=REJECTED',
    `created_at` TIMESTAMP NOT NULL COMMENT '申请发起时间',
    `updated_at` TIMESTAMP NOT NULL COMMENT '状态变更时间',
    `message`    VARCHAR(200) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '申请附言, 可选, ≤ 200 字符',
    PRIMARY KEY (`id`),
    UNIQUE KEY `friend_user_id_friend_id` (`user_id`, `friend_id`),
    KEY `friend_friend_id` (`friend_id`),
    KEY `friend_user_id_id` (`user_id`, `id`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '好友申请表';
