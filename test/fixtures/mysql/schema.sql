CREATE DATABASE IF NOT EXISTS `demo_users`;
CREATE DATABASE IF NOT EXISTS `demo_orders`;
CREATE DATABASE IF NOT EXISTS `demo_content`;

CREATE TABLE IF NOT EXISTS `demo_users`.`user` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `email` VARCHAR(254) NOT NULL,
    `given_name` VARCHAR(100) NOT NULL,
    `family_name` VARCHAR(100) NOT NULL,
    `birthday` DATE NOT NULL,
    `password` VARCHAR(64) NOT NULL,
    `phone` VARCHAR(32) NOT NULL,
    `created_at` DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_users`.`organisation` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `user_id` INT UNSIGNED NOT NULL,
    `name` VARCHAR(160) NOT NULL,
    `name_2` VARCHAR(100) NOT NULL,
    `vat_id` VARCHAR(32) NOT NULL,
    INDEX (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_users`.`address` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `user_id` INT UNSIGNED NOT NULL,
    `street` VARCHAR(200) NOT NULL,
    `postal_code` VARCHAR(20) NOT NULL,
    `city` VARCHAR(100) NOT NULL,
    `country` CHAR(2) NOT NULL,
    `phone` VARCHAR(32) NOT NULL,
    INDEX (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_users`.`email_blacklist` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `email` VARCHAR(254) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_orders`.`commission` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `user_id` INT UNSIGNED NOT NULL,
    `email` VARCHAR(254) NOT NULL,
    `given_name` VARCHAR(100) NOT NULL,
    `family_name` VARCHAR(100) NOT NULL,
    `total` DECIMAL(12,2) NOT NULL,
    `created_at` DATETIME(6) NOT NULL,
    INDEX (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_orders`.`commission_identity` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `commission_id` INT UNSIGNED NOT NULL,
    `email` VARCHAR(254) NOT NULL,
    `ip` VARCHAR(45) NOT NULL,
    INDEX (`commission_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_orders`.`commission_details_message` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `commission_id` INT UNSIGNED NOT NULL,
    `message` TEXT NOT NULL,
    INDEX (`commission_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_orders`.`audit_log` (
    `user_id` INT UNSIGNED NOT NULL,
    `event_id` INT UNSIGNED NOT NULL,
    `message` VARCHAR(255) NOT NULL,
    PRIMARY KEY (`user_id`, `event_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_content`.`message_property` (
    `id` INT UNSIGNED NOT NULL PRIMARY KEY,
    `key` VARCHAR(100) NOT NULL,
    `value` TEXT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `demo_users`.`seed_meta` (
    `id` TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    `profile` VARCHAR(32) NOT NULL,
    `scale` INT UNSIGNED NOT NULL,
    `seeded_at` DATETIME(6) NOT NULL,
    CONSTRAINT `seed_meta_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
