-- Fixed dates keep repeated seeds comparable across backup and anonymization runs.
INSERT INTO `demo_users`.`user`
    (`id`, `email`, `given_name`, `family_name`, `birthday`, `password`, `phone`, `created_at`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 5000 * @pxc_seed_scale
)
SELECT n,
    CONCAT('user', n, '@example.test'),
    CONCAT('Given', n),
    CONCAT('Family', n),
    DATE_ADD('1970-01-01', INTERVAL MOD(n - 1, 15000) DAY),
    CONCAT('demo-password-', LPAD(n, 6, '0')),
    CONCAT('+1-202-555-01', LPAD(MOD(n - 1, 100), 2, '0')),
    DATE_ADD('2024-01-01 00:00:00', INTERVAL n SECOND)
FROM seq;

INSERT INTO `demo_users`.`organisation`
    (`id`, `user_id`, `name`, `name_2`, `vat_id`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 1000 * @pxc_seed_scale
)
SELECT n, MOD(n - 1, 5000 * @pxc_seed_scale) + 1,
    CONCAT('Example Organisation ', n), 'LLC', CONCAT('DE', LPAD(n, 9, '0'))
FROM seq;

INSERT INTO `demo_users`.`address`
    (`id`, `user_id`, `street`, `postal_code`, `city`, `country`, `phone`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 8000 * @pxc_seed_scale
)
SELECT n, u.id, CONCAT('Example Street ', n),
    LPAD(10000 + MOD(n - 1, 90000), 5, '0'),
    CONCAT('Example City ', MOD(n - 1, 50) + 1), 'US', u.phone
FROM seq JOIN `demo_users`.`user` AS u
    ON u.id = MOD(n - 1, 5000 * @pxc_seed_scale) + 1;

INSERT INTO `demo_users`.`email_blacklist` (`id`, `email`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 300 * @pxc_seed_scale
)
SELECT n, u.email
FROM seq JOIN `demo_users`.`user` AS u ON u.id = n;

INSERT INTO `demo_orders`.`commission`
    (`id`, `user_id`, `email`, `given_name`, `family_name`, `total`, `created_at`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 10000 * @pxc_seed_scale
)
SELECT n, u.id, u.email, u.given_name, u.family_name,
    10.00 + MOD(n - 1, 10000) / 100.00,
    DATE_ADD('2024-02-01 00:00:00', INTERVAL n SECOND)
FROM seq JOIN `demo_users`.`user` AS u
    ON u.id = MOD(n - 1, 5000 * @pxc_seed_scale) + 1;

INSERT INTO `demo_orders`.`commission_identity` (`id`, `commission_id`, `email`, `ip`)
SELECT c.id, c.id, c.email, CONCAT('203.0.113.', MOD(c.id - 1, 254) + 1)
FROM `demo_orders`.`commission` AS c;

INSERT INTO `demo_orders`.`commission_details_message` (`id`, `commission_id`, `message`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 3000 * @pxc_seed_scale
)
SELECT n, n, CONCAT('Synthetic note; order ', n)
FROM seq;

INSERT INTO `demo_orders`.`audit_log` (`user_id`, `event_id`, `message`)
VALUES (1, 1, 'Synthetic account event'), (1, 2, 'Synthetic order event');

INSERT INTO `demo_content`.`message_property` (`id`, `key`, `value`)
WITH RECURSIVE seq AS (
    SELECT 1 AS n
    UNION ALL
    SELECT n + 1 FROM seq WHERE n < 50 * @pxc_seed_scale
)
SELECT n,
    CASE WHEN n = 1 THEN 'robots.txt' ELSE CONCAT('property-', n) END,
    CASE WHEN n = 1 THEN CONCAT('User-agent: *', CHAR(10), 'Disallow: /')
        ELSE CONCAT('Synthetic content ', n) END
FROM seq;
