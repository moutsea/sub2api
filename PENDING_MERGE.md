# 待合并提交清单

> 生成时间: 2026-01-20
> 当前分支: `liang` (HEAD: `552206b`)
> 目标分支: `origin/main`
> 待合并提交数: 78

---

## 分类汇总

| 分类 | 数量 | 优先级 |
|------|------|--------|
| Antigravity 核心改进 | 30 | 高 |
| TLS 指纹功能 | 3 | 中 |
| 软删除修复 | 3 | 中 |
| 数据库/清理任务 | 5 | 中 |
| 配置/调度 | 2 | 低 |
| 测试/Lint 修复 | 10 | 低 |
| 其他修复 | 25 | 低 |

---

## 1. Antigravity 核心改进 (30个)

### 1.1 基础功能
| 提交 | 描述 | 状态 |
|------|------|------|
| `4e3476a` | fix: 添加 gemini-3-flash 前缀映射支持 gemini-3-flash-preview | 待合并 |
| `a4a0c0e` | feat(antigravity): 增强请求参数和注入 Antigravity 身份 system prompt | 待合并 |
| `da1f3d6` | feat: antigravity 配额域限流 | 待合并 |
| `c2a6ca8` | chore: 提升 SSE 单行上限到 40MB | 待合并 |
| `7b1cf2c` | chore: 调整 SSE 单行上限到 25MB | 待合并 |

### 1.2 响应处理修复
| 提交 | 描述 | 状态 |
|------|------|------|
| `e1015c2` | fix: 修复 Antigravity 图片生成响应丢失问题 | 待合并 |
| `c9d21d5` | fix: 修复 Antigravity 非流式响应文本丢失问题 | 待合并 |
| `9a22d1a` | refactor: 提取 getOrCreateGeminiParts 减少重复代码 | 待合并 |

### 1.3 Project ID 检测
| 提交 | 描述 | 状态 |
|------|------|------|
| `b4abfae` | fix: Antigravity 测试连接使用最小 token 消耗 | 待合并 |
| `a61042b` | fix: Antigravity project_id 获取优化 | 待合并 |
| `95fe1e8` | fix: Antigravity 刷新 token 时检测 project_id 缺失 | 待合并 |
| `8219689` | feat(antigravity): 手动刷新令牌时自动恢复 missing_project_id 错误账户状态 | 待合并 |
| `4555763` | fix(antigravity): 使用 Contains 匹配 missing_project_id 错误信息 | 待合并 |
| `fba3d21` | fix: 使用 Contains 匹配 missing_project_id 并修复测试 mock | 待合并 |

### 1.4 429 限流策略
| 提交 | 描述 | 状态 |
|------|------|------|
| `cc89274` | fix(antigravity): 429 fallback 改为 5 分钟并限流整个账户 | 已跳过 (liang 分支有配额感知的 handle429WithQuotaCheck) |
| `2055a60` | fix(antigravity): 429 重试3次后限流账户 | 已跳过 (同上) |
| `ac7503d` | fix(antigravity): 429 时也切换 URL 重试 | 已跳过 (同上) |
| `5a6f60a` | fix(antigravity): 区分 URL 级别和账户配额级别的 429 限流 | 已跳过 (同上) |

### 1.5 URL 动态排序和重试
| 提交 | 描述 | 状态 |
|------|------|------|
| `69c4b17` | feat(antigravity): 动态 URL 排序，最近成功的优先使用 | 待合并 |
| `cc0fca3` | feat(antigravity): 同步 Antigravity-Manager 的请求逻辑 | 待合并 |
| `78bccd0` | refactor(antigravity): 提取公共重试循环函数减少重复代码 | 待合并 |

### 1.6 Signature Retry 相关
| 提交 | 描述 | 状态 |
|------|------|------|
| `8b071cc` | fix(antigravity): restore signature retry and base order | 待合并 |
| `959f6c5` | fix(antigravity): remove thinking sanitation | 待合并 |
| `217b3b5` | fix(antigravity): drop MarkUnavailable | 待合并 |
| `ec916a3` | fix(antigravity): remove signature retry | 待合并 |
| `22eb72e` | fix(antigravity): restore url fallback behavior | 待合并 |
| `07ba64c` | fix(antigravity): handle url-level 429 without failover | 待合并 |
| `f22bc59` | fix(antigravity): route signature retry through url fallback | 待合并 |
| `0ce8666` | Revert "Revert "fix(antigravity): Claude 模型透传 tool_use 的 signature"" | 待合并 |
| `5427a9e` | Revert "fix(antigravity): Claude 模型透传 tool_use 的 signature" | 待合并 |
| `5e9f5ef` | chore: log antigravity signature retry 429 | 待合并 |
| `a7a0017` | chore: gofmt antigravity gateway service | 待合并 |
| `9078b17` | test: add antigravity rate limit coverage | 待合并 |
| `14a3694` | chore: set antigravity fallback cooldown default to 1 | 待合并 |

---

## 2. TLS 指纹功能 (3个)

| 提交 | 描述 | 状态 | 依赖 |
|------|------|------|------|
| `9abda1b` | feat(tls): 新增 TLS 指纹模拟功能 | 待合并 | 基础 |
| `3dab717` | feat: usage接口支持TLS指纹和缓存User-Agent | 待合并 | 依赖 9abda1b |
| `ccfeaeb` | feat: 新增会话ID伪装功能，优化日志系统 | 待合并 | 依赖 9abda1b |

---

## 3. 软删除修复 (3个)

| 提交 | 描述 | 状态 |
|------|------|------|
| `fb839ae` | fix(软删除): 修复删除钩子调用链并跳过无Docker测试 | 待合并 |
| `150b315` | fix(软删除): 修复软删除钩子的客户端调用逻辑，确保正确处理变更 | 待合并 |
| `2a94cc7` | fix(软删除): 增强错误处理，确保软删除操作中的错误类型正确 | 待合并 |

---

## 4. 数据库/清理任务 (5个)

| 提交 | 描述 | 状态 |
|------|------|------|
| `ef5a410` | feat(usage): 添加清理任务与统计过滤 | 待合并 |
| `bd18f4b` | feat(清理任务): 引入Ent存储并补充日志与测试 | 待合并 |
| `bf7b79f` | fix(数据库): 优化任务状态更新查询，使用别名提高可读性 | 待合并 |
| `8391d48` | fix(数据库): 补充分组列回填前置保护 | 待合并 |
| `d17f853` | fix(数据库): 补充允许分组列兼容迁移 | 待合并 |

---

## 5. 配置/调度 (2个)

| 提交 | 描述 | 状态 |
|------|------|------|
| `1be3eac` | feat(scheduling): 兜底层账户选择策略可配置 | 待合并 |
| `34d6b0a` | feat(gateway): 账户切换次数和 Antigravity 限流时间可配置 | 待合并 |

---

## 6. 测试/Lint 修复 (10个)

| 提交 | 描述 | 状态 |
|------|------|------|
| `c8fb9ef` | style(dto): 修复 gofmt 格式问题 | 待合并 |
| `568d6ee` | fix: 修复测试缺少新增设置字段 | 待合并 |
| `a54852e` | fix: 补充API契约测试中缺失的hide_ccs_import_button字段 | 待合并 |
| `668118d` | fix: 修复遗漏的测试文件更新和lint错误 | 待合并 |
| `4c12799` | fix: 补充测试桩缺失的接口方法 | 待合并 |
| `46ae08e` | fix: 补充测试桩缺失的接口方法 | 待合并 |
| `c115c9e` | fix: address lint errors | 待合并 |
| `31933c8` | fix: 删除未使用的字段修复 lint 错误 | 待合并 |
| `f6360e0` | fix: 移除未使用的 extractSessionUUID 函数 | 待合并 |
| `6941315` | feat: add antigravity web search support | 待合并 |

---

## 7. 其他修复 (25个)

### 7.1 用户接口安全
| 提交 | 描述 | 状态 |
|------|------|------|
| `6aef1af` | fix(redeem): 用户兑换历史不返回备注 | 待合并 |
| `31cde6c` | fix(subscriptions): 用户订阅不返回分配信息 | 待合并 |
| `00d9fbd` | fix(user): 普通用户接口不返回备注 | 待合并 |
| `4f4c967` | fix(groups): 用户分组不下发内部路由信息 | 待合并 |
| `2f6f758` | fix(usage): 用户使用记录不下发账号计费倍率 | 待合并 |

### 7.2 功能修复
| 提交 | 描述 | 状态 |
|------|------|------|
| `6fec141` | fix: 修复手动刷新令牌后缓存未清除导致403错误的问题 | 待合并 |
| `b1a980f` | feat: 添加隐藏CCS导入按钮的设置选项 | 待合并 |
| `090c898` | fix: 更新Claude OAuth授权配置以匹配最新规范 | 待合并 |
| `fbb5729` | fix: 修复会话数量查询使用错误的超时配置 | 待合并 |
| `a652b51` | fix: handle 400 error for disabled organization | 待合并 |
| `de6797c` | fix: 修复5小时窗口费用不重置的问题 | 待合并 |
| `2028cc2` | fix: 修复多个管理后台问题 | 待合并 |
| `a07174c` | fix: 修复会话限制功能并在创建账号时支持配额控制 | 待合并 |
| `771baa6` | feat(界面): 优化分页跳转与页大小显示 | 待合并 |
| `45e8598` | feat(admin): 添加账号管理和订阅管理的列设置功能 | 待合并 |
| `bc1d7ed` | fix(ops): 统一 request-errors 和 SLA 的错误分类逻辑 | 待合并 |
| `a61cc2c` | fix(openai): 增强 Codex 工具过滤和参数标准化 | 待合并 |
| `ae21db7` | fix(openai): 使用 prompt_cache_key 兜底粘性会话 | 待合并 |
| `a7165b0` | fix(group): SIMPLE 模式启动补齐默认分组 | 待合并 |
| `28e46e0` | fix(gemini): 更新 Gemini 模型列表配置 | 待合并 |
| `f0ece82` | feat: 在 dashboard 右上角添加文档链接 | 待合并 |

---

## 完整提交列表 (按时间倒序)

```
c8fb9ef style(dto): 修复 gofmt 格式问题
568d6ee fix: 修复测试缺少新增设置字段
6aef1af fix(redeem): 用户兑换历史不返回备注
a54852e fix: 补充API契约测试中缺失的hide_ccs_import_button字段
668118d fix: 修复遗漏的测试文件更新和lint错误
6fec141 fix: 修复手动刷新令牌后缓存未清除导致403错误的问题
31cde6c fix(subscriptions): 用户订阅不返回分配信息
b1a980f feat: 添加隐藏CCS导入按钮的设置选项
00d9fbd fix(user): 普通用户接口不返回备注
4f4c967 fix(groups): 用户分组不下发内部路由信息
3dab717 feat: usage接口支持TLS指纹和缓存User-Agent
2f6f758 fix(usage): 用户使用记录不下发账号计费倍率
090c898 fix: 更新Claude OAuth授权配置以匹配最新规范
fbb5729 fix: 修复会话数量查询使用错误的超时配置
a652b51 fix: handle 400 error for disabled organization
ccfeaeb feat: 新增会话ID伪装功能，优化日志系统
4c12799 fix: 补充测试桩缺失的接口方法
de6797c fix: 修复5小时窗口费用不重置的问题
46ae08e fix: 补充测试桩缺失的接口方法
2028cc2 fix: 修复多个管理后台问题
f6360e0 fix: 移除未使用的 extractSessionUUID 函数
9abda1b feat(tls): 新增 TLS 指纹模拟功能
2a94cc7 fix(软删除): 增强错误处理，确保软删除操作中的错误类型正确
150b315 fix(软删除): 修复软删除钩子的客户端调用逻辑，确保正确处理变更
a07174c fix: 修复会话限制功能并在创建账号时支持配额控制
fb839ae fix(软删除): 修复删除钩子调用链并跳过无Docker测试
771baa6 feat(界面): 优化分页跳转与页大小显示
bd18f4b feat(清理任务): 引入Ent存储并补充日志与测试
bf7b79f fix(数据库): 优化任务状态更新查询，使用别名提高可读性
45e8598 feat(admin): 添加账号管理和订阅管理的列设置功能
8391d48 fix(数据库): 补充分组列回填前置保护
d17f853 fix(数据库): 补充允许分组列兼容迁移
ef5a410 feat(usage): 添加清理任务与统计过滤
c115c9e fix: address lint errors
6941315 feat: add antigravity web search support
8b071cc fix(antigravity): restore signature retry and base order
959f6c5 fix(antigravity): remove thinking sanitation
217b3b5 fix(antigravity): drop MarkUnavailable
ec916a3 fix(antigravity): remove signature retry
22eb72e fix(antigravity): restore url fallback behavior
07ba64c fix(antigravity): handle url-level 429 without failover
f22bc59 fix(antigravity): route signature retry through url fallback
0ce8666 Revert "Revert "fix(antigravity): Claude 模型透传 tool_use 的 signature""
5427a9e Revert "fix(antigravity): Claude 模型透传 tool_use 的 signature"
5e9f5ef chore: log antigravity signature retry 429
a7a0017 chore: gofmt antigravity gateway service
9078b17 test: add antigravity rate limit coverage
14a3694 chore: set antigravity fallback cooldown default to 1
bc1d7ed fix(ops): 统一 request-errors 和 SLA 的错误分类逻辑
5a6f60a fix(antigravity): 区分 URL 级别和账户配额级别的 429 限流
a61cc2c fix(openai): 增强 Codex 工具过滤和参数标准化
31933c8 fix: 删除未使用的字段修复 lint 错误
78bccd0 refactor(antigravity): 提取公共重试循环函数减少重复代码
ae21db7 fix(openai): 使用 prompt_cache_key 兜底粘性会话
ac7503d fix(antigravity): 429 时也切换 URL 重试
69c4b17 feat(antigravity): 动态 URL 排序，最近成功的优先使用
a7165b0 fix(group): SIMPLE 模式启动补齐默认分组
cc0fca3 feat(antigravity): 同步 Antigravity-Manager 的请求逻辑
28e46e0 fix(gemini): 更新 Gemini 模型列表配置
1be3eac feat(scheduling): 兜底层账户选择策略可配置
34d6b0a feat(gateway): 账户切换次数和 Antigravity 限流时间可配置
2055a60 fix(antigravity): 429 重试3次后限流账户
cc89274 fix(antigravity): 429 fallback 改为 5 分钟并限流整个账户
fba3d21 fix: 使用 Contains 匹配 missing_project_id 并修复测试 mock
4555763 fix(antigravity): 使用 Contains 匹配 missing_project_id 错误信息
8219689 feat(antigravity): 手动刷新令牌时自动恢复 missing_project_id 错误账户状态
95fe1e8 fix: Antigravity 刷新 token 时检测 project_id 缺失
a61042b fix: Antigravity project_id 获取优化
b4abfae fix: Antigravity 测试连接使用最小 token 消耗
9a22d1a refactor: 提取 getOrCreateGeminiParts 减少重复代码
c9d21d5 fix: 修复 Antigravity 非流式响应文本丢失问题
e1015c2 fix: 修复 Antigravity 图片生成响应丢失问题
f0ece82 feat: 在 dashboard 右上角添加文档链接
c2a6ca8 chore: 提升 SSE 单行上限到 40MB
7b1cf2c chore: 调整 SSE 单行上限到 25MB
da1f3d6 feat: antigravity 配额域限流
a4a0c0e feat(antigravity): 增强请求参数和注入 Antigravity 身份 system prompt
4e3476a fix: 添加 gemini-3-flash 前缀映射支持 gemini-3-flash-preview
```

---

## 备注

- 部分提交可能在当前分支已有等效实现（通过不同的 commit hash）
- Antigravity 相关改动较多，建议整体合并或放弃
- TLS 指纹功能有依赖链，需按顺序合并
- 软删除修复涉及 Ent schema，可能有冲突
