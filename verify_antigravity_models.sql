-- 验证 Antigravity Provider 的模型记录情况

-- 1. 检查所有 antigravity 账号的使用记录
SELECT
    ul.model,
    COUNT(*) as request_count,
    SUM(ul.input_tokens) as total_input_tokens,
    SUM(ul.output_tokens) as total_output_tokens,
    MIN(ul.created_at) as first_request,
    MAX(ul.created_at) as last_request
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.platform = 'antigravity'
    AND a.deleted_at IS NULL
    AND ul.created_at >= NOW() - INTERVAL '7 days'
GROUP BY ul.model
ORDER BY request_count DESC;

-- 2. 检查最近的 antigravity 请求详情
SELECT
    ul.id,
    ul.created_at,
    ul.model,
    ul.input_tokens,
    ul.output_tokens,
    ul.request_id,
    a.name as account_name
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.platform = 'antigravity'
    AND a.deleted_at IS NULL
    AND ul.created_at >= NOW() - INTERVAL '24 hours'
ORDER BY ul.created_at DESC
LIMIT 50;

-- 3. 检查是否有其他模型的请求（包括可能失败的）
SELECT
    ul.model,
    ul.status_code,
    COUNT(*) as count
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.platform = 'antigravity'
    AND a.deleted_at IS NULL
    AND ul.created_at >= NOW() - INTERVAL '7 days'
GROUP BY ul.model, ul.status_code
ORDER BY ul.model, ul.status_code;

-- 4. 对比不同平台的模型分布
SELECT
    a.platform,
    ul.model,
    COUNT(*) as request_count
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.deleted_at IS NULL
    AND ul.created_at >= NOW() - INTERVAL '7 days'
GROUP BY a.platform, ul.model
ORDER BY a.platform, request_count DESC;
