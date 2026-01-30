-- 诊断 Antigravity Provider 日志记录问题

-- 1. 检查 antigravity 账号配置
SELECT
    id,
    name,
    platform,
    enabled,
    deleted_at,
    last_used_at
FROM accounts
WHERE platform = 'antigravity'
ORDER BY id;

-- 2. 检查最近 24 小时所有平台的使用记录分布
SELECT
    a.platform,
    COUNT(*) as request_count,
    COUNT(DISTINCT ul.model) as unique_models,
    STRING_AGG(DISTINCT ul.model, ', ' ORDER BY ul.model) as models_used,
    MIN(ul.created_at) as first_request,
    MAX(ul.created_at) as last_request
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE ul.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY a.platform
ORDER BY request_count DESC;

-- 3. 检查 antigravity 账号的使用记录（最近 7 天）
SELECT
    ul.id,
    ul.created_at,
    ul.model,
    ul.input_tokens,
    ul.output_tokens,
    ul.request_id,
    a.id as account_id,
    a.name as account_name,
    ul.user_id,
    ul.api_key_id
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.platform = 'antigravity'
    AND ul.created_at >= NOW() - INTERVAL '7 days'
ORDER BY ul.created_at DESC
LIMIT 100;

-- 4. 检查是否有 antigravity 账号但没有使用记录
SELECT
    a.id,
    a.name,
    a.platform,
    a.enabled,
    a.last_used_at,
    COUNT(ul.id) as usage_count_last_7d
FROM accounts a
LEFT JOIN usage_logs ul ON a.id = ul.account_id AND ul.created_at >= NOW() - INTERVAL '7 days'
WHERE a.platform = 'antigravity'
    AND a.deleted_at IS NULL
GROUP BY a.id, a.name, a.platform, a.enabled, a.last_used_at
ORDER BY a.id;

-- 5. 检查所有模型的使用分布（最近 24 小时）
SELECT
    ul.model,
    a.platform,
    COUNT(*) as request_count,
    SUM(ul.input_tokens) as total_input_tokens,
    SUM(ul.output_tokens) as total_output_tokens
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE ul.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY ul.model, a.platform
ORDER BY request_count DESC;

-- 6. 检查是否有 API Key 关联到 antigravity 分组
SELECT
    ak.id as api_key_id,
    ak.name as api_key_name,
    g.id as group_id,
    g.name as group_name,
    g.platform,
    ak.enabled,
    ak.deleted_at
FROM api_keys ak
JOIN groups g ON ak.group_id = g.id
WHERE g.platform = 'antigravity'
ORDER BY ak.id;

-- 7. 检查 antigravity 分组配置
SELECT
    id,
    name,
    platform,
    enabled,
    deleted_at
FROM groups
WHERE platform = 'antigravity'
ORDER BY id;

-- 8. 检查最近的错误或异常（如果有 error_logs 表）
-- SELECT * FROM error_logs WHERE created_at >= NOW() - INTERVAL '24 hours' ORDER BY created_at DESC LIMIT 50;
