package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// remapFunctionCallArgs 修正 Gemini 返回的工具参数以符合 Claude Code 规范
// 参考 Antigravity-Manager 的 streaming.rs 实现
func remapFunctionCallArgs(toolName string, args map[string]any) {
	if args == nil {
		return
	}

	toolNameLower := strings.ToLower(toolName)

	// [FIX] 去除 MCP 工具前缀 (mcp__servername__toolname → toolname)
	// 这样 mcp__filesystem__grep 可以匹配到 grep 的处理逻辑
	if strings.HasPrefix(toolNameLower, "mcp__") {
		// 去除 "mcp__" 前缀
		toolNameLower = strings.TrimPrefix(toolNameLower, "mcp__")
		// 如果还有第二个 "__"，取最后一部分作为工具名
		// 例如 mcp__filesystem__grep → filesystem__grep → grep
		if idx := strings.LastIndex(toolNameLower, "__"); idx != -1 {
			toolNameLower = toolNameLower[idx+2:]
		}
	}

	switch toolNameLower {
	case "grep", "search", "search_files", "searchfiles", "search_code_definitions", "search_code_snippets":
		// [FIX] Gemini 可能使用 "description" 字段而不是 "pattern"
		if desc, ok := args["description"].(string); ok {
			if _, hasPattern := args["pattern"]; !hasPattern {
				args["pattern"] = desc
				delete(args, "description")
				log.Printf("[StreamTransformer] Remapped Grep: description → pattern")
			}
		}

		// Gemini 使用 "query"，Claude Code 使用 "pattern"
		if query, ok := args["query"].(string); ok {
			if _, hasPattern := args["pattern"]; !hasPattern {
				args["pattern"] = query
				delete(args, "query")
				log.Printf("[StreamTransformer] Remapped Grep: query → pattern")
			}
		}

		// [CRITICAL] Claude Code 使用 "path" (字符串)，而不是 "paths" (数组)
		if _, hasPath := args["path"]; !hasPath {
			if paths, ok := args["paths"]; ok {
				var pathStr string
				switch v := paths.(type) {
				case []any:
					if len(v) > 0 {
						if s, ok := v[0].(string); ok {
							pathStr = s
						}
					}
				case []string:
					if len(v) > 0 {
						pathStr = v[0]
					}
				case string:
					pathStr = v
				}
				if pathStr == "" {
					pathStr = "."
				}
				args["path"] = pathStr
				delete(args, "paths")
				log.Printf("[StreamTransformer] Remapped Grep: paths → path(\"%s\")", pathStr)
			} else {
				// 默认使用当前目录
				args["path"] = "."
				log.Printf("[StreamTransformer] Added default path: \".\"")
			}
		}

	case "glob", "list_files", "listfiles":
		// [FIX] Gemini 可能使用 "description" 字段而不是 "pattern"
		if desc, ok := args["description"].(string); ok {
			if _, hasPattern := args["pattern"]; !hasPattern {
				args["pattern"] = desc
				delete(args, "description")
				log.Printf("[StreamTransformer] Remapped Glob: description → pattern")
			}
		}

		// Gemini 使用 "query"，Claude Code 使用 "pattern"
		if query, ok := args["query"].(string); ok {
			if _, hasPattern := args["pattern"]; !hasPattern {
				args["pattern"] = query
				delete(args, "query")
				log.Printf("[StreamTransformer] Remapped Glob: query → pattern")
			}
		}

		// [CRITICAL] Claude Code 使用 "path" (字符串)，而不是 "paths" (数组)
		if _, hasPath := args["path"]; !hasPath {
			if paths, ok := args["paths"]; ok {
				var pathStr string
				switch v := paths.(type) {
				case []any:
					if len(v) > 0 {
						if s, ok := v[0].(string); ok {
							pathStr = s
						}
					}
				case []string:
					if len(v) > 0 {
						pathStr = v[0]
					}
				case string:
					pathStr = v
				}
				if pathStr == "" {
					pathStr = "."
				}
				args["path"] = pathStr
				delete(args, "paths")
				log.Printf("[StreamTransformer] Remapped Glob: paths → path(\"%s\")", pathStr)
			}
			// Glob 的 path 是可选的，不需要默认值
		}

	case "read", "read_file", "readfile":
		// Gemini 可能使用 "path" 而不是 "file_path"
		if path, ok := args["path"].(string); ok {
			if _, hasFilePath := args["file_path"]; !hasFilePath {
				args["file_path"] = path
				delete(args, "path")
				log.Printf("[StreamTransformer] Remapped Read: path → file_path")
			}
		}

	case "ls":
		// LS 工具：确保 "path" 参数存在
		if _, hasPath := args["path"]; !hasPath {
			args["path"] = "."
			log.Printf("[StreamTransformer] Remapped LS: default path → \".\"")
		}

	case "enterplanmode":
		// [IMPORTANT] Claude Code CLI 的 EnterPlanMode 工具禁止携带任何参数
		for k := range args {
			delete(args, k)
		}

	default:
		// 通用处理：如果工具有 "paths" (单元素数组) 但没有 "path"，转换它
		if _, hasPath := args["path"]; !hasPath {
			if paths, ok := args["paths"].([]any); ok && len(paths) == 1 {
				if p, ok := paths[0].(string); ok {
					args["path"] = p
					delete(args, "paths")
					log.Printf("[StreamTransformer] Generic fix for tool '%s': paths[0] → path(\"%s\")", toolName, p)
				}
			}
		}
	}
}

// BlockType 内容块类型
type BlockType int

const (
	BlockTypeNone BlockType = iota
	BlockTypeText
	BlockTypeThinking
	BlockTypeFunction
)

// StreamingProcessor 流式响应处理器
type StreamingProcessor struct {
	blockType           BlockType
	blockIndex          int
	messageStartSent    bool
	messageStopSent     bool
	usedTool            bool
	hadNonThinkingBlock bool // [FIX] 跟踪是否已发送过非 thinking 块（text 或 tool_use）
	pendingSignature    string
	trailingSignature   string
	originalModel       string
	webSearchQueries    []string
	groundingChunks     []GeminiGroundingChunk

	// 累计 usage
	inputTokens     int
	outputTokens    int
	cacheReadTokens int
}

// NewStreamingProcessor 创建流式响应处理器
func NewStreamingProcessor(originalModel string) *StreamingProcessor {
	return &StreamingProcessor{
		blockType:     BlockTypeNone,
		originalModel: originalModel,
	}
}

// ProcessLine 处理 SSE 行，返回 Claude SSE 事件
func (p *StreamingProcessor) ProcessLine(line string) []byte {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return nil
	}

	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}

	// 解包 v1internal 响应
	var v1Resp V1InternalResponse
	if err := json.Unmarshal([]byte(data), &v1Resp); err != nil {
		// 尝试直接解析为 GeminiResponse
		var directResp GeminiResponse
		if err2 := json.Unmarshal([]byte(data), &directResp); err2 != nil {
			return nil
		}
		v1Resp.Response = directResp
		v1Resp.ResponseID = directResp.ResponseID
		v1Resp.ModelVersion = directResp.ModelVersion
	}

	geminiResp := &v1Resp.Response

	var result bytes.Buffer

	// 发送 message_start
	if !p.messageStartSent {
		_, _ = result.Write(p.emitMessageStart(&v1Resp))
	}

	// 更新 usage
	// 注意：Gemini 的 promptTokenCount 包含 cachedContentTokenCount，
	// 但 Claude 的 input_tokens 不包含 cache_read_input_tokens，需要减去
	if geminiResp.UsageMetadata != nil {
		cached := geminiResp.UsageMetadata.CachedContentTokenCount
		p.inputTokens = geminiResp.UsageMetadata.PromptTokenCount - cached
		p.outputTokens = geminiResp.UsageMetadata.CandidatesTokenCount
		p.cacheReadTokens = cached
	}

	// 处理 parts
	if len(geminiResp.Candidates) > 0 && geminiResp.Candidates[0].Content != nil {
		for _, part := range geminiResp.Candidates[0].Content.Parts {
			_, _ = result.Write(p.processPart(&part))
		}
	}

	// 捕获 groundingMetadata（Web搜索结果）
	if len(geminiResp.Candidates) > 0 {
		p.captureGrounding(geminiResp.Candidates[0].GroundingMetadata)
	}

	// 检查是否结束
	if len(geminiResp.Candidates) > 0 {
		finishReason := geminiResp.Candidates[0].FinishReason
		if finishReason != "" {
			_, _ = result.Write(p.emitFinish(finishReason))
		}
	}

	return result.Bytes()
}

// Finish 结束处理，返回最终事件和用量
func (p *StreamingProcessor) Finish() ([]byte, *ClaudeUsage) {
	var result bytes.Buffer

	if !p.messageStopSent {
		_, _ = result.Write(p.emitFinish(""))
	}

	usage := &ClaudeUsage{
		InputTokens:          p.inputTokens,
		OutputTokens:         p.outputTokens,
		CacheReadInputTokens: p.cacheReadTokens,
	}

	return result.Bytes(), usage
}

// emitMessageStart 发送 message_start 事件
func (p *StreamingProcessor) emitMessageStart(v1Resp *V1InternalResponse) []byte {
	if p.messageStartSent {
		return nil
	}

	usage := ClaudeUsage{}
	if v1Resp.Response.UsageMetadata != nil {
		cached := v1Resp.Response.UsageMetadata.CachedContentTokenCount
		usage.InputTokens = v1Resp.Response.UsageMetadata.PromptTokenCount - cached
		usage.OutputTokens = v1Resp.Response.UsageMetadata.CandidatesTokenCount
		usage.CacheReadInputTokens = cached
	}

	responseID := v1Resp.ResponseID
	if responseID == "" {
		responseID = v1Resp.Response.ResponseID
	}
	if responseID == "" {
		responseID = "msg_" + generateRandomID()
	}

	message := map[string]any{
		"id":            responseID,
		"type":          "message",
		"role":          "assistant",
		"content":       []any{},
		"model":         p.originalModel,
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         usage,
	}

	event := map[string]any{
		"type":    "message_start",
		"message": message,
	}

	p.messageStartSent = true
	return p.formatSSE("message_start", event)
}

// processPart 处理单个 part
func (p *StreamingProcessor) processPart(part *GeminiPart) []byte {
	var result bytes.Buffer
	signature := part.ThoughtSignature

	// 1. FunctionCall 处理
	if part.FunctionCall != nil {
		// 先处理 trailingSignature
		// [FIX] 只有当还没有发送过非 thinking 块时，才能发送 thinking 块
		if p.trailingSignature != "" {
			_, _ = result.Write(p.endBlock())
			if !p.hadNonThinkingBlock {
				_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
			}
			p.trailingSignature = ""
		}

		_, _ = result.Write(p.processFunctionCall(part.FunctionCall, signature))
		return result.Bytes()
	}

	// 2. Text 处理
	if part.Text != "" || part.Thought {
		if part.Thought {
			_, _ = result.Write(p.processThinking(part.Text, signature))
		} else {
			_, _ = result.Write(p.processText(part.Text, signature))
		}
	}

	// 3. InlineData (Image) 处理
	if part.InlineData != nil && part.InlineData.Data != "" {
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)",
			part.InlineData.MimeType, part.InlineData.Data)
		_, _ = result.Write(p.processText(markdownImg, ""))
	}

	return result.Bytes()
}

func (p *StreamingProcessor) captureGrounding(grounding *GeminiGroundingMetadata) {
	if grounding == nil {
		return
	}

	if len(grounding.WebSearchQueries) > 0 && len(p.webSearchQueries) == 0 {
		p.webSearchQueries = append([]string(nil), grounding.WebSearchQueries...)
	}

	if len(grounding.GroundingChunks) > 0 && len(p.groundingChunks) == 0 {
		p.groundingChunks = append([]GeminiGroundingChunk(nil), grounding.GroundingChunks...)
	}
}

// processThinking 处理 thinking
func (p *StreamingProcessor) processThinking(text, signature string) []byte {
	var result bytes.Buffer

	// 处理之前的 trailingSignature
	// [FIX] 只有当还没有发送过非 thinking 块时，才能发送 thinking 块
	if p.trailingSignature != "" {
		_, _ = result.Write(p.endBlock())
		if !p.hadNonThinkingBlock {
			_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		}
		p.trailingSignature = ""
	}

	// 开始或继续 thinking 块
	if p.blockType != BlockTypeThinking {
		_, _ = result.Write(p.startBlock(BlockTypeThinking, map[string]any{
			"type":     "thinking",
			"thinking": "",
		}))
	}

	if text != "" {
		_, _ = result.Write(p.emitDelta("thinking_delta", map[string]any{
			"thinking": text,
		}))
	}

	// 暂存签名
	if signature != "" {
		p.pendingSignature = signature
	}

	return result.Bytes()
}

// processText 处理普通 text
func (p *StreamingProcessor) processText(text, signature string) []byte {
	var result bytes.Buffer

	// 空 text 带签名 - 暂存
	if text == "" {
		if signature != "" {
			p.trailingSignature = signature
		}
		return nil
	}

	// 处理之前的 trailingSignature
	// [FIX] 只有当还没有发送过非 thinking 块时，才能发送 thinking 块
	// 否则直接丢弃签名，因为 Claude 协议不允许在 text/tool_use 块之后追加 thinking 块
	if p.trailingSignature != "" {
		_, _ = result.Write(p.endBlock())
		if !p.hadNonThinkingBlock {
			_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		}
		p.trailingSignature = ""
	}

	// 非空 text 带签名 - 特殊处理
	// [FIX] 不再在 text 块后发送 thinking 块，只暂存签名供后续使用
	// Claude 协议不允许在 text 块之后追加 thinking 块
	if signature != "" {
		_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
			"type": "text",
			"text": "",
		}))
		_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
			"text": text,
		}))
		_, _ = result.Write(p.endBlock())
		// 不再发送 thinking 块，签名被丢弃
		return result.Bytes()
	}

	// 普通 text (无签名)
	if p.blockType != BlockTypeText {
		_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
			"type": "text",
			"text": "",
		}))
	}

	_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
		"text": text,
	}))

	return result.Bytes()
}

// processFunctionCall 处理 function call
func (p *StreamingProcessor) processFunctionCall(fc *GeminiFunctionCall, signature string) []byte {
	var result bytes.Buffer

	p.usedTool = true

	toolID := fc.ID
	if toolID == "" {
		toolID = fmt.Sprintf("%s-%s", fc.Name, generateRandomID())
	}

	// 工具名称规范化：search → grep
	toolName := fc.Name
	if strings.ToLower(toolName) == "search" {
		toolName = "grep"
		log.Printf("[StreamTransformer] Normalizing tool name: search → grep")
	}

	toolUse := map[string]any{
		"type":  "tool_use",
		"id":    toolID,
		"name":  toolName,
		"input": map[string]any{},
	}

	if signature != "" {
		toolUse["signature"] = signature
	}

	_, _ = result.Write(p.startBlock(BlockTypeFunction, toolUse))

	// 发送 input_json_delta
	if fc.Args != nil {
		// [FIX] 重映射参数以符合 Claude Code 规范
		// 首先需要将 Args 转换为 map[string]any
		var remappedArgs map[string]any
		switch v := fc.Args.(type) {
		case map[string]any:
			remappedArgs = make(map[string]any)
			for k, val := range v {
				remappedArgs[k] = val
			}
		default:
			// 如果不是 map 类型，尝试通过 JSON 序列化/反序列化转换
			argsBytes, err := json.Marshal(fc.Args)
			if err == nil {
				if err := json.Unmarshal(argsBytes, &remappedArgs); err != nil {
					remappedArgs = nil
				}
			}
		}

		if remappedArgs != nil {
			remapFunctionCallArgs(toolName, remappedArgs)
			argsJSON, _ := json.Marshal(remappedArgs)
			_, _ = result.Write(p.emitDelta("input_json_delta", map[string]any{
				"partial_json": string(argsJSON),
			}))
		} else {
			// 无法转换，直接使用原始参数
			argsJSON, _ := json.Marshal(fc.Args)
			_, _ = result.Write(p.emitDelta("input_json_delta", map[string]any{
				"partial_json": string(argsJSON),
			}))
		}
	}

	_, _ = result.Write(p.endBlock())

	return result.Bytes()
}

// startBlock 开始新的内容块
func (p *StreamingProcessor) startBlock(blockType BlockType, contentBlock map[string]any) []byte {
	var result bytes.Buffer

	if p.blockType != BlockTypeNone {
		_, _ = result.Write(p.endBlock())
	}

	event := map[string]any{
		"type":          "content_block_start",
		"index":         p.blockIndex,
		"content_block": contentBlock,
	}

	_, _ = result.Write(p.formatSSE("content_block_start", event))
	p.blockType = blockType

	// [FIX] 跟踪是否已发送过非 thinking 块
	// 一旦发送了 text 或 tool_use 块，就不能再发送 thinking 块
	if blockType == BlockTypeText || blockType == BlockTypeFunction {
		p.hadNonThinkingBlock = true
	}

	return result.Bytes()
}

// endBlock 结束当前内容块
func (p *StreamingProcessor) endBlock() []byte {
	if p.blockType == BlockTypeNone {
		return nil
	}

	var result bytes.Buffer

	// Thinking 块结束时发送暂存的签名
	if p.blockType == BlockTypeThinking && p.pendingSignature != "" {
		_, _ = result.Write(p.emitDelta("signature_delta", map[string]any{
			"signature": p.pendingSignature,
		}))
		p.pendingSignature = ""
	}

	event := map[string]any{
		"type":  "content_block_stop",
		"index": p.blockIndex,
	}

	_, _ = result.Write(p.formatSSE("content_block_stop", event))

	p.blockIndex++
	p.blockType = BlockTypeNone

	return result.Bytes()
}

// emitDelta 发送 delta 事件
func (p *StreamingProcessor) emitDelta(deltaType string, deltaContent map[string]any) []byte {
	delta := map[string]any{
		"type": deltaType,
	}
	for k, v := range deltaContent {
		delta[k] = v
	}

	event := map[string]any{
		"type":  "content_block_delta",
		"index": p.blockIndex,
		"delta": delta,
	}

	return p.formatSSE("content_block_delta", event)
}

// emitEmptyThinkingWithSignature 发送空 thinking 块承载签名
func (p *StreamingProcessor) emitEmptyThinkingWithSignature(signature string) []byte {
	var result bytes.Buffer

	_, _ = result.Write(p.startBlock(BlockTypeThinking, map[string]any{
		"type":     "thinking",
		"thinking": "",
	}))
	_, _ = result.Write(p.emitDelta("thinking_delta", map[string]any{
		"thinking": "",
	}))
	_, _ = result.Write(p.emitDelta("signature_delta", map[string]any{
		"signature": signature,
	}))
	_, _ = result.Write(p.endBlock())

	return result.Bytes()
}

// emitFinish 发送结束事件
func (p *StreamingProcessor) emitFinish(finishReason string) []byte {
	var result bytes.Buffer

	// 关闭最后一个块
	_, _ = result.Write(p.endBlock())

	// 处理 trailingSignature
	// [FIX] 根据 Claude 协议，如果已经发送过非 thinking 块，就不能再追加 thinking 块
	// 参考 antigravity-manager 的实现：只存储签名，不再发送非法的末尾 Thinking 块
	if p.trailingSignature != "" {
		if !p.hadNonThinkingBlock {
			// 还没有发送过任何块，可以安全地发送 thinking 块承载签名
			_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		}
		// 无论是否发送，都要清空 trailingSignature
		p.trailingSignature = ""
	}

	// 处理 grounding metadata（Web搜索结果）-> 转换为 Markdown 文本块
	if len(p.webSearchQueries) > 0 || len(p.groundingChunks) > 0 {
		groundingText := buildGroundingText(&GeminiGroundingMetadata{
			WebSearchQueries: p.webSearchQueries,
			GroundingChunks:  p.groundingChunks,
		})
		if groundingText != "" {
			_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
				"type": "text",
				"text": "",
			}))
			_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
				"text": groundingText,
			}))
			_, _ = result.Write(p.endBlock())
		}
	}

	// 确定 stop_reason
	stopReason := "end_turn"
	if p.usedTool {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	usage := ClaudeUsage{
		InputTokens:          p.inputTokens,
		OutputTokens:         p.outputTokens,
		CacheReadInputTokens: p.cacheReadTokens,
	}

	deltaEvent := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": usage,
	}

	_, _ = result.Write(p.formatSSE("message_delta", deltaEvent))

	if !p.messageStopSent {
		stopEvent := map[string]any{
			"type": "message_stop",
		}
		_, _ = result.Write(p.formatSSE("message_stop", stopEvent))
		p.messageStopSent = true
	}

	return result.Bytes()
}

// formatSSE 格式化 SSE 事件
func (p *StreamingProcessor) formatSSE(eventType string, data any) []byte {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}
