# Changelog

所有 sub2api Docker 镜像版本的变更记录。

版本格式遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)：`主版本号.次版本号.修订号`

---

## [1.0.2] - 2026-01-21

### ✨ 新功能 (New Features)

#### 高优先级核心功能（基于 Antigravity-Manager 分析）

- **[高优] 消息合并逻辑** (`MergeConsecutiveMessages`)
  - **问题**: Gemini API 严格要求 user/assistant 角色必须交替出现，否则返回 400 错误
  - **实现**: 自动合并连续相同角色的消息
  - **支持**: String + String, Array + Array, Array + String, String + Array 多种组合
  - **触发场景**: 上下文清理后产生的连续消息、用户快速连续发送、工具调用中断等
  - **影响**: ⭐⭐⭐⭐⭐ CRITICAL - 防止 Gemini API 拒绝请求
  - **参考**: Antigravity-Manager `merge_consecutive_messages`

- **[高优] Thinking 块三阶段排序** (`sortBlocksThreeStage`)
  - **问题**: Gemini/Claude 要求 assistant 消息必须以 thinking block 开头
  - **实现**: 对 assistant 消息内容块进行三阶段排序：`[Thinking, Text, ToolUse]`
  - **智能检测**: 只有当顺序不正确时才重排序，避免不必要的操作
  - **影响**: ⭐⭐⭐⭐⭐ CRITICAL - 确保 thinking 模式协议合规性
  - **参考**: Antigravity-Manager `sort_thinking_blocks_first`

- **[高优] Warmup 请求拦截器** (`isWarmupRequest` + `simulateWarmupResponse`)
  - **功能**: 检测并拦截 Claude Code 的 warmup/heartbeat 请求
  - **检测特征**:
    - 单条 user 消息，内容为 "Say hi"、"ping" 或 "hi"
    - max_tokens ≤ 10
    - 消息数量为 1
  - **返回**: 完整的 Claude API SSE 事件流（模拟响应）
  - **收益**:
    - ✅ 零 Token 消耗（避免真实 API 调用）
    - ✅ 降低延迟（模拟响应 < 10ms）
    - ✅ 节省配额（减少不必要的 API 调用）
  - **调试**: 响应头包含 `X-Warmup-Response: true` 标记
  - **影响**: ⭐⭐⭐⭐ HIGH - 优化性能和成本
  - **参考**: Antigravity-Manager warmup detection

### 🔄 优化 (Improvements)

#### 上下文管理增强

- **消息合并集成到请求预处理流程**
  - 在 `PrepareRequestForClaude` 函数中自动执行
  - 处理流程：Sanitize → Merge → Purify
  - 确保所有请求都经过消息合并处理

- **Thinking 块排序集成到清理流程**
  - 在 `sanitizeThinkingBlocks` 函数中自动执行
  - 处理流程：Cache Control 清理 → 历史扁平化 → 三阶段排序
  - 仅对 assistant 消息且 len(blocks) > 1 时执行

#### 系统提示词回滚

- **移除动态加载系统**: 回滚 v1.0.1 的动态提示词加载机制
  - **原因**: 动态加载导致 TODO 功能异常（工具定义泄漏）
  - **解决方案**: 恢复内联常量方式
  - **新增**: 将 anti.txt 的所有段落添加为内联常量
  - **段落**: identity, user_information, tool_calling, web_development, user_rules, workflows, knowledge_discovery, persistent_context, ephemeral_message, communication_style

### 📝 新增文件

- `backend/internal/service/context_manager.go`:
  - `MergeConsecutiveMessages()`: 消息合并函数
  - `mergeContent()`: 内容合并辅助函数
  - `PurifyHistory()`: 上下文清理函数
  - `PrepareRequest()`: 综合预处理入口

### 🔄 变更文件

- `backend/internal/service/antigravity_gateway_service.go`:
  - 新增 `isWarmupRequest()`: Warmup 请求检测
  - 新增 `simulateWarmupResponse()`: 模拟响应生成
  - 新增 `sortBlocksThreeStage()`: 三阶段块排序
  - 修改 `Forward()`: 集成 Warmup 拦截器（入口处）
  - 修改 `sanitizeThinkingBlocks()`: 集成三阶段排序

- `backend/internal/service/context_manager.go`:
  - 修改 `PrepareRequestForClaude()`: 集成消息合并

- `backend/internal/pkg/antigravity/request_transformer.go`:
  - 新增 10 个内联提示词常量（从 anti.txt 提取）
  - 修改 `buildAntigravityPrompt()`: 动态组装所有段落

### ❌ 删除文件

- `backend/internal/pkg/antigravity/prompt_loader.go`: 动态提示词加载器（已回滚）
- `backend/internal/pkg/antigravity/prompt_loader_test.go`: 提示词加载器测试（已回滚）

### 🎯 影响范围

- **协议兼容性**: 显著提升与 Gemini API 的兼容性
  - 解决角色交替问题（消息合并）
  - 解决 thinking 块顺序问题（三阶段排序）
- **性能优化**: Warmup 拦截器减少不必要的 API 调用和 Token 消耗
- **稳定性**: 修复动态提示词导致的 TODO 功能异常
- **可维护性**: 使用内联常量确保提示词稳定可靠

### 📊 技术指标

- **消息合并**: 自动处理 4 种内容组合方式
- **三阶段排序**: [Thinking, Text, ToolUse] 固定顺序
- **Warmup 响应时间**: < 10ms（vs 真实 API ~500ms+）
- **Warmup Token 消耗**: 0 tokens（vs 真实 API ~2 tokens）
- **提示词段落数**: 10 个完整段落（内联常量）

### 🔍 日志标记

新增以下日志标记便于排查问题：

- `[Warmup]`: Warmup 请求检测和响应相关
- `[ContextManager]`: 消息合并和上下文管理相关
- `[Antigravity]`: 三阶段排序和清理相关

### 🧪 测试验证

- ✅ 编译通过: `go build ./...`
- ✅ 测试通过: `go test ./...`
- ✅ 所有现有测试保持通过

### 📚 参考文档

- `docs/ANTIGRAVITY_MANAGER_ANALYSIS.md`: Antigravity-Manager 深度分析报告
  - 详细实现参考
  - 优先级排序
  - Go 代码示例

---

## [1.0.1] - 2026-01-21
- **[高优] 工具冲突检测**: 修复 Gemini v1internal API 不支持同时使用 `googleSearch` 和 `functionDeclarations` 的问题
  - 添加自动冲突检测逻辑，当检测到冲突时优先保留 `functionDeclarations`，移除 `googleSearch`
  - 添加详细的 `[ISSUE-TOOLS-CONFLICT]` 日志标记，方便排查问题
  - 解决了 "searching files error" 400 错误问题

- **[高优] 请求体大小限制**: 添加 10MB 硬性限制，防止超长上下文触发 400 错误
  - 实现详细的请求体大小日志记录（原始大小、转换后大小、压缩比）
  - 返回 `413 Request Entity Too Large` 错误，包含详细的错误信息
  - 添加 `[REQUEST-SIZE]` 和 `[REQUEST-SIZE-ERROR]` 日志标记

- **[高优] SSE 行大小优化**: 将 `maxLineSize` 最小值从 1MB 提升到 2MB
  - 提高对大型流式响应的处理能力
  - 减少 scanner buffer 溢出错误

#### 系统提示词管理
- **动态提示词加载系统**: 实现从 `resources/anti.txt` 动态加载系统提示词
  - 支持基于 XML 标签的模块化提示词管理
  - 仅在需要时加载对应模块（如工具调用、MCP 工具、Web 开发等）
  - 实现提示词缓存机制，提升性能
  - 支持热重载功能 (`ReloadPromptFile()`)
  - 提供降级回退机制，确保服务稳定性

- **工具定义泄漏修复**: 修复 TODO 功能异常问题
  - 添加工具定义过滤逻辑，在 "# Tools" 标记处截断内容
  - 添加 `excludedSections` 黑名单机制（过滤 `function_calls`、`example` 等）
  - 防止 Claude Code 工具定义被误注入到 systemInstruction
  - 成功将加载的提示词段落从 11 个减少到 9 个核心段落

#### 上下文管理
- **上下文清理增强**: 实现 `PrepareRequestForClaude` 函数
  - Token 估算（约 3.5 字符/token）
  - 三级清理策略：None（无清理）/ Soft（保留最近 4 条消息）/ Aggressive（清理所有 thinking blocks）
  - 自动清理缓存控制标记
  - Thinking block 扁平化处理

### 📝 新增文件

- `backend/internal/pkg/antigravity/prompt_loader.go`: 动态提示词加载器
- `backend/internal/pkg/antigravity/prompt_loader_test.go`: 提示词加载器测试套件
- `backend/internal/service/context_manager.go`: 上下文管理器实现

### 🔄 变更文件

- `backend/internal/pkg/antigravity/request_transformer.go`: 集成动态提示词，添加工具冲突检测
- `backend/internal/service/antigravity_gateway_service.go`: 添加请求体大小检查和详细日志
- `backend/internal/config/config.go`: 提升 maxLineSize 最小值
- `backend/internal/config/config_test.go`: 更新测试用例

### 🎯 影响范围

- **请求处理**: 提升了对超长上下文和大型流式响应的处理能力
- **错误处理**: 提供更清晰的错误信息和日志，便于问题排查
- **系统稳定性**: 修复工具冲突和工具定义泄漏问题，提升服务稳定性
- **可维护性**: 实现模块化提示词管理，无需重新编译即可更新提示词

### 📊 技术指标

- 请求体大小限制: **10 MB**
- SSE 最小行大小: **2 MB** (从 1 MB 提升)
- Token 估算比率: **3.5 字符/token**
- 提示词段落数: **9 个核心段落** (从 11 个优化)

---

## [1.0.0] - 2024-01-11

### 🎉 初始版本

- 完整的 Claude API → Gemini API 转换服务
- 支持流式响应 (SSE)
- 用户认证和授权系统
- 请求限流和并发控制
- 仪表板统计功能
- Docker 容器化部署
- PostgreSQL + Redis 数据存储
- LinuxDo OAuth 集成
- 计费和使用量统计

---

## 版本说明

### Docker 镜像标签格式

```bash
# 最新稳定版
cfjwlchangji/sub2api:latest

# 指定版本
cfjwlchangji/sub2api:v1.0.0
cfjwlchangji/sub2api:v1.0.1
cfjwlchangji/sub2api:v1.0.2

# 主版本锁定
cfjwlchangji/sub2api:v1
```

### 构建命令示例

```bash
# 构建并推送 1.0.2 版本
docker buildx build --platform linux/amd64 \
  -t cfjwlchangji/sub2api:v1.0.2 \
  -t cfjwlchangji/sub2api:v1 \
  -t cfjwlchangji/sub2api:latest \
  --push .
```

### 版本选择建议

- **生产环境**: 使用明确的版本号（如 `v1.0.2`），避免意外更新
- **测试环境**: 可使用主版本标签（如 `v1`），获取最新的修复
- **开发环境**: 可使用 `latest`，始终使用最新版本

---

## 问题反馈

如遇到问题，请查看日志中的以下标记：

### v1.0.2 新增标记
- `[Warmup]`: Warmup 请求检测和响应相关
- `[ContextManager]`: 消息合并和上下文管理相关
- `[Antigravity]`: 三阶段排序和 Thinking 块清理相关

### v1.0.1 标记
- `[ISSUE-TOOLS-CONFLICT]`: 工具冲突问题
- `[REQUEST-SIZE-ERROR]`: 请求体过大
- `[REQUEST-SIZE]`: 请求大小统计
- `[PromptLoader]`: 提示词加载相关（已在 v1.0.2 移除）

完整日志路径: `/var/log/sub2api/` (Docker 容器内)
