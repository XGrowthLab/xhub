-- 单一语句快照内确定调用方的归属、兄弟保留额和速率上限；只返回构造额度树需要的字段。
WITH target AS (
    SELECT CASE WHEN arg.kind = 'key' THEN k.user_id ELSE arg.id END AS user_id,
           CASE WHEN arg.kind = 'key' AND k.id IS NULL THEN 'missing'
                WHEN arg.kind = 'key' AND NULLIF(k.user_id, '') IS NULL THEN 'invalid'
                ELSE '' END AS fault
    FROM (SELECT CAST(? AS text) AS kind, CAST(? AS text) AS id) arg
    LEFT JOIN api_keys k ON arg.kind = 'key' AND k.id = arg.id
), scope AS (
    SELECT u.user_id, COALESCE(MAX(t.organization_id), '') AS org_id,
           CASE WHEN u.fault <> '' THEN u.fault
                WHEN COUNT(m.team_id) > 1 THEN 'multiple'
                WHEN COUNT(m.team_id) = 1 AND COALESCE(MAX(t.organization_id), '') = '' THEN 'invalid'
                ELSE '' END AS fault
    FROM target u
    LEFT JOIN team_members m ON m.user_id = u.user_id
    LEFT JOIN teams t ON t.id = m.team_id
    GROUP BY u.user_id, u.fault
), scoped_teams AS (
    SELECT t.id, t.organization_id, t.max_budget, t.spend, t.rpm_limit, t.tpm_limit
    FROM teams t JOIN scope s ON s.org_id = t.organization_id AND s.fault = ''
), scoped_users AS (
    SELECT u.id, u.max_budget, u.spend, u.rpm_limit, u.tpm_limit, u.budget_period_start
    FROM users u WHERE u.id IN (
        SELECT user_id FROM scope WHERE org_id = '' AND fault = ''
        UNION ALL
        SELECT m.user_id FROM team_members m JOIN scoped_teams t ON t.id = m.team_id
    )
), parents AS (
    -- 也检查这些用户在其他组织的归属，避免把多团队用户误算在某个父节点上。
    SELECT m.user_id, CASE WHEN COUNT(*) = 1 THEN 'team:' || MIN(m.team_id) ELSE 'ambiguous' END AS parent
    FROM team_members m JOIN scoped_users u ON u.id = m.user_id GROUP BY m.user_id
)
SELECT 'scope' AS kind, '' AS id, fault AS parent, NULL::numeric AS max_budget,
       0::numeric AS spend, NULL::integer AS rpm_limit, NULL::integer AS tpm_limit, NULL::timestamptz AS period_start
FROM scope
UNION ALL
SELECT 'org', o.id, '', o.max_budget, o.spend, o.rpm_limit, o.tpm_limit, o.budget_period_start
FROM organizations o JOIN scope s ON s.org_id = o.id AND s.fault = ''
UNION ALL
SELECT 'team', t.id, 'org:' || t.organization_id, t.max_budget, t.spend, t.rpm_limit, t.tpm_limit, NULL FROM scoped_teams t
UNION ALL
SELECT 'user', u.id, COALESCE(p.parent, ''), u.max_budget, u.spend, u.rpm_limit, u.tpm_limit, u.budget_period_start
FROM scoped_users u LEFT JOIN parents p ON p.user_id = u.id
UNION ALL
SELECT 'key', k.id, 'user:' || k.user_id, k.max_budget, k.spend, k.rpm_limit, k.tpm_limit, NULL
FROM api_keys k JOIN scoped_users u ON u.id = k.user_id
