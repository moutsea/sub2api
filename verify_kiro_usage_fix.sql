-- 验证 Kiro 用量日志修复效果
-- 执行时间：修复部署后

-- 1. 检查最近 Kiro 请求的 token 分布（修复前后对比）
SELECT
    DATE(created_at AT TIME ZONE 'Asia/Shanghai') as date,
    COUNT(*) as total_requests,
    -- 平均 input_tokens（修复前会偏低）
    ROUND(AVG(input_tokens), 2) as avg_input_tokens,
    -- 平均缓存 tokens
    ROUND(AVG(cache_creation_tokens + cache_read_tokens), 2) as avg_cache_tokens,
    -- 平均总输入（修复后 input_tokens 应该接近这个值）
    ROUND(AVG(input_tokens + cache_creation_tokens + cache_read_tokens), 2) as avg_total_input,
    -- 有缓存的请求数
    COUNT(CASE WHEN cache_creation_tokens > 0 OR cache_read_tokens > 0 THEN 1 END) as cached_requests,
    -- 缓存命中率
    ROUND(100.0 * COUNT(CASE WHEN cache_read_tokens > 0 THEN 1 END) / NULLIF(COUNT(*), 0), 2) as cache_hit_rate_pct
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '3 days'
GROUP BY DATE(created_at AT TIME ZONE 'Asia/Shanghai')
ORDER BY date DESC;

-- 2. 检查异常低的 input_tokens 记录（可能是修复前的数据）
SELECT
    id,
    created_at AT TIME ZONE 'Asia/Shanghai' as created_at_cst,
    model,
    input_tokens,
    cache_creation_tokens,
    cache_read_tokens,
    (input_tokens + cache_creation_tokens + cache_read_tokens) as total_input,
    -- 如果 input_tokens 远小于 total_input，说明是修复前的数据
    CASE
        WHEN (cache_creation_tokens + cache_read_tokens) > 0
             AND input_tokens < (cache_creation_tokens + cache_read_tokens) * 0.5
        THEN 'LIKELY_BUG'
        ELSE 'OK'
    END as status
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '2 days'
    AND (cache_creation_tokens > 0 OR cache_read_tokens > 0)
ORDER BY created_at DESC
LIMIT 50;

-- 3. 对比今天和昨天的用量统计（检查是否有显著差异）
WITH daily_stats AS (
    SELECT
        DATE(created_at AT TIME ZONE 'Asia/Shanghai') as date,
        COUNT(*) as requests,
        SUM(input_tokens) as sum_input,
        SUM(cache_creation_tokens + cache_read_tokens) as sum_cache,
        SUM(input_tokens + cache_creation_tokens + cache_read_tokens) as sum_total_input,
        SUM(output_tokens) as sum_output
    FROM usage_logs
    WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
        AND created_at >= NOW() - INTERVAL '3 days'
    GROUP BY DATE(created_at AT TIME ZONE 'Asia/Shanghai')
)
SELECT
    date,
    requests,
    sum_input,
    sum_cache,
    sum_total_input,
    sum_output,
    -- 计算 input_tokens 占总输入的比例（修复后应该接近 100%）
    ROUND(100.0 * sum_input / NULLIF(sum_total_input, 0), 2) as input_pct_of_total
FROM daily_stats
ORDER BY date DESC;

-- 4. 检查账号级别的用量统计（找出受影响最大的账号）
SELECT
    a.id as account_id,
    a.name as account_name,
    DATE(ul.created_at AT TIME ZONE 'Asia/Shanghai') as date,
    COUNT(*) as requests,
    SUM(ul.input_tokens) as sum_input,
    SUM(ul.cache_creation_tokens + ul.cache_read_tokens) as sum_cache,
    SUM(ul.input_tokens + ul.cache_creation_tokens + ul.cache_read_tokens) as sum_total_input,
    -- 如果这个比例远小于 100%，说明该账号受影响严重
    ROUND(100.0 * SUM(ul.input_tokens) / NULLIF(SUM(ul.input_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0), 2) as input_pct
FROM usage_logs ul
JOIN accounts a ON ul.account_id = a.id
WHERE a.platform = 'kiro'
    AND a.deleted_at IS NULL
    AND ul.created_at >= NOW() - INTERVAL '2 days'
GROUP BY a.id, a.name, DATE(ul.created_at AT TIME ZONE 'Asia/Shanghai')
HAVING SUM(ul.cache_creation_tokens + ul.cache_read_tokens) > 0
ORDER BY date DESC, input_pct ASC
LIMIT 20;

-- 5. 检查修复后的数据（部署后运行）
-- 修复后，input_tokens 应该等于 total_input，cache tokens 单独记录
SELECT
    '修复后数据检查' as check_type,
    COUNT(*) as total_records,
    COUNT(CASE
        WHEN (cache_creation_tokens > 0 OR cache_read_tokens > 0)
             AND input_tokens = (input_tokens + cache_creation_tokens + cache_read_tokens)
        THEN 1
    END) as correct_records,
    COUNT(CASE
        WHEN (cache_creation_tokens > 0 OR cache_read_tokens > 0)
             AND input_tokens < (input_tokens + cache_creation_tokens + cache_read_tokens)
        THEN 1
    END) as incorrect_records
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '1 hour';
