# Kiro OAuth 的 Responses 接口

Kiro OAuth 账号（Social / IdC）支持 `POST /v1/responses`，也可使用 `/responses` 别名。
使用绑定 Kiro 分组的 Sub2API API Key，模型名从该分组的 `/v1/models` 选择。
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

`max_output_tokens` 会被解析，但当前 Kiro OAuth 上游仍沿用已有的固定 64000 token 预算策略，不承诺严格遵守客户端设置的输出上限。

事件格式参考 [OpenAI Responses API](https://developers.openai.com/api/reference/resources/responses)。
