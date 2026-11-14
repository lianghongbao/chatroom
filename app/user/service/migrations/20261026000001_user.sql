-- migrations/20261026000001_user.sql
-- chatroom-user 空库初始化。表结构权威是 ent schema，改 schema 后重启服务
-- 即自动增量迁移，这里只用于不依赖 Go 运行时建空库。详见 migrations/README.md。

CREATE TABLE IF NOT EXISTS `users` (
    `id`                   CHAR(36) NOT NULL COMMENT '用户 uid, UUIDv7',
    `username`             VARCHAR(255) COLLATE utf8mb4_bin NOT NULL COMMENT '登录用户名, 唯一',
    `nickname`             VARCHAR(255) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '展示昵称',
    `avatar`               VARCHAR(255) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '头像 URL',
    `password_hash`        VARCHAR(255) COLLATE utf8mb4_bin NOT NULL COMMENT 'bcrypt 哈希, 60 字节',
    `created_at`           TIMESTAMP NOT NULL COMMENT '注册时间',
    `email`                VARCHAR(255) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '邮箱, 可选, 不唯一',
    `phone`                VARCHAR(255) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '手机号, 可选, 全局唯一',
    `is_visible_to_stranger` TINYINT(1) NOT NULL DEFAULT '0' COMMENT '是否对陌生人可见',
    `status`               TINYINT UNSIGNED NOT NULL DEFAULT '1' COMMENT '账号状态 1=正常 0=禁用',
    PRIMARY KEY (`id`),
    UNIQUE KEY `user_username` (`username`),
    UNIQUE KEY `user_phone` (`phone`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '用户表';
