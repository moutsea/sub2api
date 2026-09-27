# Kiro OAuth 的 Responses 接口

Kiro OAuth 账号（Social / IdC）支持 `POST /v1/responses`，也可使用 `/responses` 别名。
使用绑定 Kiro 分组的 Sub2API API Key，模型名从该分组的 `/v1/models` 选择。
GPT-5.6 系列需要写全变体名：`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`。裸 `gpt-5.6` 不在模型表内，会 fallback 到默认模型。
请求由网关转换为 Kiro 原生请求，复用 OAuth 刷新、账号调度、重试、模型映射和用量统计。
Kiro API Key 类型账号不在本次支持范围内。

```bash
curl "$SUB2API_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-6",
    "input": "你好，请介绍一下自己",
    "stream": true,
    "store": false
  }'
```

## 支持的请求和返回

- `input` 支持字符串或消息数组；支持 `instructions` 和 system / developer / user / assistant 消息。
- 消息内容支持 `input_text`、`output_text`、`input_image`；图片使用 `image_url`（URL 或 base64 data URL）。
- 支持 function 工具定义、`function_call` 历史和文本 `function_call_output`，包括多个交错返回的工具调用。
- `reasoning.effort` 映射到 Kiro thinking 设置，沿用 Kiro 对对应模型的能力限制。
- `stream:false`（默认）返回 `object:"response"`、`output` 和 `usage`。
- `stream:true` 返回 `response.created`、`response.in_progress`、输出项和内容增量事件，以及 `response.completed`；不会返回 Chat Completions 的 `[DONE]`。
- 输出耗尽时返回 `response.incomplete`；流开始后上游失败返回 `response.failed`，不会再发送成功完成事件。流开始前失败保留现有账号重试逻辑。
- 用量字段沿用 Kiro 当前估算方式，包含缓存信息；不是 OpenAI 官方的 token 计量。

## 多轮与工具结果回传

这是无服务端状态的兼容接口。将之前的 `output` 项和工具结果放回下一次请求的 `input`，并携带完整历史：

```json
{
  "model": "claude-sonnet-4-6",
  "store": false,
  "input": [
    {"role": "user", "content": "读取 a.go"},
    {"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{\"path\":\"a.go\"}"},
    {"type": "function_call_output", "call_id": "call_1", "output": "package main"}
  ],
  "tools": [
    {"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}
  ]
}
```

不支持 `previous_response_id`、`conversation`、`item_reference`、`store:true`、`background:true`、托管工具（例如 `web_search` / `file_search`）、`file_id` 图片或结构化 `text.format`。这些请求返回明确的 400 错误。`store` 省略时按 false 处理；没有新增响应查询、删除或 WebSocket 接口。

## Codex 兼容

Codex CLI 的请求形状与标准 Responses 略有差异，网关会自动归一化，无需客户端改配置：

- `input` 内的 `additional_tools` 载体会被剥离，其 `tools` 按顺序合并进顶层 `tools` 并按 `type`+`name` 去重。
- `local_shell` 和 `custom`（freeform）工具降级为 Claude function 工具。`local_shell` 使用固定的 argv schema；freeform 工具包成单个 `input` 字符串字段，这是 Claude 工具契约唯一能表达的形状。
- 返回时按客户端原本声明的类型还原：`local_shell_call`（argv 放回 `action`）和 `custom_tool_call`（拆出 `input` 原文），并发送对应的 `response.custom_tool_call_input.*` 事件。未声明为私有形状的工具保持 `function_call`。
- 回传历史里的 `local_shell_call`、`custom_tool_call`、`shell_call`、`apply_patch_call` 及各自的 `_output` 变体会转成 `function_call` / `function_call_output`。
- `compaction` item 拆成一个惰性 `reasoning` item 和一条包在 `<conversation_summary>` 标签里的 user 消息，与 Grok 链路口径一致。
- `tool_choice` 接受 Codex 的 `{"type":"local_shell"}` 和 `{"type":"custom","name":...}` 选择形式。

freeform 工具经 Claude 协议往返属于有损转换：模型看到的是一个字符串字段而非原生 freeform 契约，复杂 patch 的成功率可能低于原生 OpenAI 上游。

`max_output_tokens` 会被解析，但当前 Kiro OAuth 上游仍沿用已有的固定 64000 token 预算策略，不承诺严格遵守客户端设置的输出上限。

事件格式参考 [OpenAI Responses API](https://developers.openai.com/api/reference/resources/responses)。
