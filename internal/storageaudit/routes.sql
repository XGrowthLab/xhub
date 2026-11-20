-- usage 是调用计数的唯一来源。先收窄时间窗，再连接一对一的日志、采集记录。
WITH events AS (
 SELECT (u.ts AT TIME ZONE 'UTC')::date AS day,
   rtrim(split_part(u.inbound_endpoint,'?',1),'/') AS inbound,
   rtrim(split_part(u.outbound_endpoint,'?',1),'/') AS outbound, u.streamed,
   1 AS calls,
   {{diagnostics}} AS diagnostic_bytes, {{embedded}} AS embedded_body_bytes,
   {{capture}} AS capture_bytes, (c.request_id IS NOT NULL)::int AS captures
 FROM usage_events u
 LEFT JOIN request_logs l USING (request_id) LEFT JOIN request_log_captures c USING (request_id)
 WHERE u.ts >= $1 AND u.ts < $2
 UNION ALL
 -- 采集表允许先于用量落盘。单独保留无归属项，不能凭正文或当前配置推断协议。
 SELECT (c.created_at AT TIME ZONE 'UTC')::date, '', '', NULL, 0, 0, 0,
   {{capture}}, 1
 FROM request_log_captures c WHERE c.created_at >= $1 AND c.created_at < $2
   AND NOT EXISTS (SELECT 1 FROM usage_events u WHERE u.request_id=c.request_id)
)
SELECT day::text, {{inbound}}, {{outbound}}, inbound LIKE '/bypass/%', streamed,
 sum(calls), sum(diagnostic_bytes), sum(embedded_body_bytes), sum(capture_bytes), sum(captures)
FROM events GROUP BY 1,2,3,4,5 ORDER BY 1,2,3,4,5
