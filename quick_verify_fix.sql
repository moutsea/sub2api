-- 快速验证 Kiro 用量修复效果
-- 部署后执行此脚本，检查最近 1 小时的数据

\echo '=== 1. 检查最近 1 小时的 Kiro 请求 ==='
SELECT
    COUNT(*) as total_requests,
    COUNT(CASE WHEN cache_creation_tokens > 0 OR cache_read_tokens > 0 THEN 1 END) as cached_requests,
    ROUND(AVG(input_tokens), 2) as avg_input_tokens,
    ROUND(AVG(cache_creation_tokens + cache_read_tokens), 2) as avg_cache_tokens,
    -- 修复后这个比例应该是 100%
    ROUND(100.0 * AVG(input_tokens) / NULLIF(AVG(input_tokens + cache_creation_tokens + cache_read_tokens), 0), 2) as input_coverage_pct
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '1 hour';

\echo ''
\echo '=== 2. 检查是否还有异常记录（应该为 0）==='
SELECT
    COUNT(*) as abnormal_records
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '1 hour'
    AND (cache_creation_tokens > 0 OR cache_read_tokens > 0)
    AND input_tokens < (input_tokens + cache_creation_tokens + cache_read_tokens);

\echo ''
\echo '=== 3. 最近 10 条记录详情 ==='
SELECT
    created_at AT TIME ZONE 'Asia/Shanghai' as time_cst,
    model,
    input_tokens,
    cache_creation_tokens as cache_create,
    cache_read_tokens as cache_read,
    (input_tokens + cache_creation_tokens + cache_read_tokens) as total_input,
    CASE
        WHEN cache_creation_tokens > 0 OR cache_read_tokens > 0 THEN
            CASE
                WHEN input_tokens = (input_tokens + cache_creation_tokens + cache_read_tokens) THEN '✓ 正确'
                ELSE '✗ 异常'
            END
        ELSE '无缓存'
    END as status
FROM usage_logs
WHERE account_id IN (SELECT id FROM accounts WHERE platform = 'kiro' AND deleted_at IS NULL)
    AND created_at >= NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 10;

\echo ''
\echo '=== 验证完成 ==='
\echo '如果 input_coverage_pct = 100% 且 abnormal_records = 0，说明修复成功！'
