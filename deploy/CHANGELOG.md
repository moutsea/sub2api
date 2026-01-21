# Changelog

所有 sub2api Docker 镜像版本的变更记录。

版本格式遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)：`主版本号.次版本号.修订号`

---

## [1.0.1] - 2026-01-21

### 🔧 修复 (Bug Fixes)

#### 请求处理优化
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

# 主版本锁定
cfjwlchangji/sub2api:v1
```

### 构建命令示例

```bash
# 构建并推送 1.0.1 版本
docker buildx build --platform linux/amd64 \
  -t cfjwlchangji/sub2api:v1.0.1 \
  -t cfjwlchangji/sub2api:v1 \
  -t cfjwlchangji/sub2api:latest \
  --push .
```

### 版本选择建议

- **生产环境**: 使用明确的版本号（如 `v1.0.1`），避免意外更新
- **测试环境**: 可使用主版本标签（如 `v1`），获取最新的修复
- **开发环境**: 可使用 `latest`，始终使用最新版本

---

## 问题反馈

如遇到问题，请查看日志中的以下标记：

- `[ISSUE-TOOLS-CONFLICT]`: 工具冲突问题
- `[REQUEST-SIZE-ERROR]`: 请求体过大
- `[REQUEST-SIZE]`: 请求大小统计
- `[PromptLoader]`: 提示词加载相关
- `[ContextManager]`: 上下文管理相关

完整日志路径: `/var/log/sub2api/` (Docker 容器内)
