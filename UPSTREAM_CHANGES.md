# Upstream Main 分支更新汇总

> 整理时间: 2026-01-28
> 当前分支: feat/kiro-thinking-mode
> 待合并提交数: ~90+

---

## 一、新功能 (Features)

### 1. 优惠码功能
- `192efb8` feat(promo-code): complete promo code feature implementation
- `8672347` fix(settings): add missing promo_code_enabled field in public settings API

### 2. 订阅管理
- `cc07a0e` feat(subscription): 支持调整订阅时长（延长/缩短）

### 3. TLS 指纹模拟
- `9abda1b` feat(tls): 新增 TLS 指纹模拟功能
- `3dab717` feat: usage接口支持TLS指纹和缓存User-Agent
- `d91e232` test(tlsfingerprint): add multi-profile fingerprint verification test
- `c95a864` docs: add TLS fingerprint tool link

### 4. Antigravity 增强
- `6941315` feat: add antigravity web search support
- `69c4b17` feat(antigravity): 动态 URL 排序，最近成功的优先使用
- `cc0fca3` feat(antigravity): 同步 Antigravity-Manager 的请求逻辑
- `da1f3d6` feat: antigravity 配额域限流
- `a4a0c0e` feat(antigravity): 增强请求参数和注入 Antigravity 身份 system prompt
- `8219689` feat(antigravity): 手动刷新令牌时自动恢复 missing_project_id 错误账户状态

### 5. 前端功能
- `ff74f51` feat(frontend): 账号表格默认排序/持久化 + 自动刷新 + 更多菜单外部关闭
- `45e8598` feat(admin): 添加账号管理和订阅管理的列设置功能
- `771baa6` feat(界面): 优化分页跳转与页大小显示
- `f0ece82` feat: 在 dashboard 右上角添加文档链接
- `dd8d5e2` mod(frontend): 订阅分组下拉显示备注

### 6. 系统配置
- `b1a980f` feat: 添加隐藏CCS导入按钮的设置选项
- `ccfeaeb` feat: 新增会话ID伪装功能，优化日志系统
- `1be3eac` feat(scheduling): 兜底层账户选择策略可配置
- `34d6b0a` feat(gateway): 账户切换次数和 Antigravity 限流时间可配置

### 7. 清理任务
- `ef5a410` feat(usage): 添加清理任务与统计过滤
- `bd18f4b` feat(清理任务): 引入Ent存储并补充日志与测试
- `73e6b16` feat(认证): 启用 OpenAI OAuth HTTP/2 并修复清理任务 lint

---

## 二、Bug 修复 (Fixes)

### 1. 缓存与令牌
- `2665230` fix(token-cache): 修复异步刷新与请求线程的缓存竞态条件
- `6fec141` fix: 修复手动刷新令牌后缓存未清除导致403错误的问题

### 2. Antigravity 相关
- `e756064` fix(antigravity): 修复非流式 Claude To Antigravity 响应内容为空的问题
- `5a6f60a` fix(antigravity): 区分 URL 级别和账户配额级别的 429 限流
- `ac7503d` fix(antigravity): 429 时也切换 URL 重试
- `2055a60` fix(antigravity): 429 重试3次后限流账户
- `cc89274` fix(antigravity): 429 fallback 改为 5 分钟并限流整个账户
- `4555763` fix(antigravity): 使用 Contains 匹配 missing_project_id 错误信息
- `95fe1e8` fix: Antigravity 刷新 token 时检测 project_id 缺失
- `a61042b` fix: Antigravity project_id 获取优化
- `b4abfae` fix: Antigravity 测试连接使用最小 token 消耗
- `c9d21d5` fix: 修复 Antigravity 非流式响应文本丢失问题
- `e1015c2` fix: 修复 Antigravity 图片生成响应丢失问题
- `8b071cc` fix(antigravity): restore signature retry and base order
- `959f6c5` fix(antigravity): remove thinking sanitation
- `22eb72e` fix(antigravity): restore url fallback behavior
- `07ba64c` fix(antigravity): handle url-level 429 without failover

### 3. 调度与会话
- `91f0130` fix(调度): 完善粘性会话清理与账号调度刷新
- `7a83db6` fix(调度): 完善粘性会话清理与账号调度刷新
- `ae21db7` fix(openai): 使用 prompt_cache_key 兜底粘性会话
- `fbb5729` fix: 修复会话数量查询使用错误的超时配置
- `a07174c` fix: 修复会话限制功能并在创建账号时支持配额控制

### 4. 软删除
- `2a94cc7` fix(软删除): 增强错误处理，确保软删除操作中的错误类型正确
- `150b315` fix(软删除): 修复软删除钩子的客户端调用逻辑，确保正确处理变更
- `fb839ae` fix(软删除): 修复删除钩子调用链并跳过无Docker测试

### 5. 数据库
- `477a9a1` fix: 修复 schema 清理逻辑
- `bf7b79f` fix(数据库): 优化任务状态更新查询，使用别名提高可读性
- `8391d48` fix(数据库): 补充分组列回填前置保护
- `d17f853` fix(数据库): 补充允许分组列兼容迁移

### 6. API 与接口
- `6aef1af` fix(redeem): 用户兑换历史不返回备注
- `31cde6c` fix(subscriptions): 用户订阅不返回分配信息
- `00d9fbd` fix(user): 普通用户接口不返回备注
- `4f4c967` fix(groups): 用户分组不下发内部路由信息
- `2f6f758` fix(usage): 用户使用记录不下发账号计费倍率

### 7. 其他修复
- `a7165b0` fix(group): SIMPLE 模式启动补齐默认分组
- `28e46e0` fix(gemini): 更新 Gemini 模型列表配置
- `4e3476a` fix: 添加 gemini-3-flash 前缀映射支持 gemini-3-flash-preview
- `090c898` fix: 更新Claude OAuth授权配置以匹配最新规范
- `a652b51` fix: handle 400 error for disabled organization
- `de6797c` fix: 修复5小时窗口费用不重置的问题
- `2028cc2` fix: 修复多个管理后台问题
- `bc1d7ed` fix(ops): 统一 request-errors 和 SLA 的错误分类逻辑
- `a61cc2c` fix(openai): 增强 Codex 工具过滤和参数标准化

---

## 三、重构 (Refactor)

- `da48df0` refactor(antigravity): 提取并同步 Schema 清理逻辑至 schema_cleaner.go
- `78bccd0` refactor(antigravity): 提取公共重试循环函数减少重复代码
- `9a22d1a` refactor: 提取 getOrCreateGeminiParts 减少重复代码

---

## 四、其他 (Chore/Style/Build)

- `c2a6ca8` chore: 提升 SSE 单行上限到 40MB
- `7b1cf2c` chore: 调整 SSE 单行上限到 25MB
- `14a3694` chore: set antigravity fallback cooldown default to 1
- `88b6358` build(frontend): vite 加载开发环境变量
- `5e5d4a5` feat: 移动镜像脚本位置
- `53534d3` style(admin): 统一列设置按钮位置到刷新按钮右侧

---

## 五、需要关注的重点

### 高优先级
1. **缓存竞态条件修复** - 可能影响令牌刷新稳定性
2. **Antigravity 429 限流逻辑** - 多次迭代，需要仔细测试
3. **软删除钩子修复** - 影响数据完整性

### 可能冲突
1. 前端 i18n 文件可能有冲突
2. 数据库迁移文件需要按顺序执行

### 新增配置项
1. `promo_code_enabled` - 优惠码开关
2. `hide_ccs_import_button` - 隐藏 CCS 导入按钮
3. TLS 指纹相关配置
4. Antigravity 限流时间配置
5. 账户切换次数配置

---

## 六、合并建议

1. 先备份当前数据库
2. 按顺序执行数据库迁移
3. 测试 Antigravity 相关功能
4. 验证前端功能正常
5. 检查新增配置项是否需要设置
