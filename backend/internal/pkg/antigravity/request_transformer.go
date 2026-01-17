package antigravity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	sessionRand      = rand.New(rand.NewSource(time.Now().UnixNano()))
	sessionRandMutex sync.Mutex
)

// generateStableSessionID 基于用户消息内容生成稳定的 session ID
func generateStableSessionID(contents []GeminiContent) string {
	// 查找第一个 user 消息的文本
	for _, content := range contents {
		if content.Role == "user" && len(content.Parts) > 0 {
			if text := content.Parts[0].Text; text != "" {
				h := sha256.Sum256([]byte(text))
				n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
				return "-" + strconv.FormatInt(n, 10)
			}
		}
	}
	// 回退：生成随机 session ID
	sessionRandMutex.Lock()
	n := sessionRand.Int63n(9_000_000_000_000_000_000)
	sessionRandMutex.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}

type TransformOptions struct {
	EnableIdentityPatch bool
	// IdentityPatch 可选：自定义注入到 systemInstruction 开头的身份防护提示词；
	// 为空时使用默认模板（包含 [IDENTITY_PATCH] 及 SYSTEM_PROMPT_BEGIN 标记）。
	IdentityPatch string
}

func DefaultTransformOptions() TransformOptions {
	return TransformOptions{
		EnableIdentityPatch: true,
	}
}

// TransformClaudeToGemini 将 Claude 请求转换为 v1internal Gemini 格式
func TransformClaudeToGemini(claudeReq *ClaudeRequest, projectID, mappedModel string) ([]byte, error) {
	return TransformClaudeToGeminiWithOptions(claudeReq, projectID, mappedModel, DefaultTransformOptions())
}

// TransformClaudeToGeminiWithOptions 将 Claude 请求转换为 v1internal Gemini 格式（可配置身份补丁等行为）
func TransformClaudeToGeminiWithOptions(claudeReq *ClaudeRequest, projectID, mappedModel string, opts TransformOptions) ([]byte, error) {
	// 用于存储 tool_use id -> name 映射
	toolIDToName := make(map[string]string)

	// 检测是否启用 thinking
	isThinkingEnabled := claudeReq.Thinking != nil && claudeReq.Thinking.Type == "enabled"

	// 只有 Gemini 模型支持 dummy thought workaround
	// Claude 模型通过 Vertex/Google API 需要有效的 thought signatures
	allowDummyThought := strings.HasPrefix(mappedModel, "gemini-")

	// 1. 构建 contents
	contents, strippedThinking, err := buildContents(claudeReq.Messages, toolIDToName, isThinkingEnabled, allowDummyThought)
	if err != nil {
		return nil, fmt.Errorf("build contents: %w", err)
	}

	// 2. 构建 tools（需要在 buildSystemInstruction 之前，以便检测是否有工具）
	tools := buildTools(claudeReq.Tools)
	hasTools := len(tools) > 0

	// 3. 构建 systemInstruction（根据是否有工具动态组装提示词）
	systemInstruction := buildSystemInstruction(claudeReq.System, claudeReq.Model, opts, hasTools)

	// 4. 构建 generationConfig
	reqForConfig := claudeReq
	if strippedThinking {
		// If we had to downgrade thinking blocks to plain text due to missing/invalid signatures,
		// disable upstream thinking mode to avoid signature/structure validation errors.
		reqCopy := *claudeReq
		reqCopy.Thinking = nil
		reqForConfig = &reqCopy
		// 同时移除模型名称中的 "-thinking" 后缀，避免上游根据模型名判断启用 thinking 模式
		if strings.HasSuffix(mappedModel, "-thinking") {
			mappedModel = strings.TrimSuffix(mappedModel, "-thinking")
		}
	}
	generationConfig := buildGenerationConfig(reqForConfig)

	// 5. 构建内部请求
	innerRequest := GeminiRequest{
		Contents: contents,
		// 总是设置 toolConfig，与官方客户端一致
		ToolConfig: &GeminiToolConfig{
			FunctionCallingConfig: &GeminiFunctionCallingConfig{
				Mode: "VALIDATED",
			},
		},
		// 总是生成 sessionId，基于用户消息内容
		SessionID: generateStableSessionID(contents),
	}

	if systemInstruction != nil {
		innerRequest.SystemInstruction = systemInstruction
	}
	if generationConfig != nil {
		innerRequest.GenerationConfig = generationConfig
	}
	if len(tools) > 0 {
		innerRequest.Tools = tools
	}

	// 如果提供了 metadata.user_id，优先使用
	if claudeReq.Metadata != nil && claudeReq.Metadata.UserID != "" {
		innerRequest.SessionID = claudeReq.Metadata.UserID
	}

	// 6. 包装为 v1internal 请求
	v1Req := V1InternalRequest{
		Project:     projectID,
		RequestID:   "agent-" + uuid.New().String(),
		UserAgent:   "antigravity", // 固定值，与官方客户端一致
		RequestType: "agent",
		Model:       mappedModel,
		Request:     innerRequest,
	}

	return json.Marshal(v1Req)
}

// Antigravity 提示词模块（从 resources/anti.txt 同步）
const (
	// 核心身份提示词 - 所有场景都需要
	promptIdentity = `<identity>
You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.
You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.
The USER will send you requests, which you must always prioritize addressing. Along with each USER request, we will attach additional metadata about their current state, such as what files they have open and where their cursor is.
This information may or may not be relevant to the coding task, it is up for you to decide.
</identity>`

	// 工具调用指导 - 仅在有工具时需要
	promptToolCalling = `
<tool_calling>
Call tools as you normally would. The following list provides additional guidance to help you avoid errors:
  - **Absolute paths only**. When using tools that accept file path arguments, ALWAYS use the absolute file path.
</tool_calling>`

	// Web 开发指导 - 可选，用于 Web 开发场景
	promptWebDevelopment = `
<web_application_development>
## Technology Stack,
Your web applications should be built using the following technologies:,
1. **Core**: Use HTML for structure and Javascript for logic.
2. **Styling (CSS)**: Use Vanilla CSS for maximum flexibility and control. Avoid using TailwindCSS unless the USER explicitly requests it; in this case, first confirm which TailwindCSS version to use.
3. **Web App**: If the USER specifies that they want a more complex web app, use a framework like Next.js or Vite. Only do this if the USER explicitly requests a web app.
4. **New Project Creation**: If you need to use a framework for a new app, use ` + "`npx`" + ` with the appropriate script, but there are some rules to follow:,
   - Use ` + "`npx -y`" + ` to automatically install the script and its dependencies
   - You MUST run the command with ` + "`--help`" + ` flag to see all available options first,
   - Initialize the app in the current directory with ` + "`./`" + ` (example: ` + "`npx -y create-vite-app@latest ./`" + `),
   - You should run in non-interactive mode so that the user doesn't need to input anything,
5. **Running Locally**: When running locally, use ` + "`npm run dev`" + ` or equivalent dev server. Only build the production bundle if the USER explicitly requests it or you are validating the code for correctness.

# Design Aesthetics,
1. **Use Rich Aesthetics**: The USER should be wowed at first glance by the design. Use best practices in modern web design (e.g. vibrant colors, dark modes, glassmorphism, and dynamic animations) to create a stunning first impression. Failure to do this is UNACCEPTABLE.
2. **Prioritize Visual Excellence**: Implement designs that will WOW the user and feel extremely premium:
		- Avoid generic colors (plain red, blue, green). Use curated, harmonious color palettes (e.g., HSL tailored colors, sleek dark modes).
   - Using modern typography (e.g., from Google Fonts like Inter, Roboto, or Outfit) instead of browser defaults.
		- Use smooth gradients,
		- Add subtle micro-animations for enhanced user experience,
3. **Use a Dynamic Design**: An interface that feels responsive and alive encourages interaction. Achieve this with hover effects and interactive elements. Micro-animations, in particular, are highly effective for improving user engagement.
4. **Premium Designs**. Make a design that feels premium and state of the art. Avoid creating simple minimum viable products.
4. **Don't use placeholders**. If you need an image, use your generate_image tool to create a working demonstration.,

## Implementation Workflow,
Follow this systematic approach when building web applications:,
1. **Plan and Understand**:,
		- Fully understand the user's requirements,
		- Draw inspiration from modern, beautiful, and dynamic web designs,
		- Outline the features needed for the initial version,
2. **Build the Foundation**:,
		- Start by creating/modifying ` + "`index.css`" + `,
		- Implement the core design system with all tokens and utilities,
3. **Create Components**:,
		- Build necessary components using your design system,
		- Ensure all components use predefined styles, not ad-hoc utilities,
		- Keep components focused and reusable,
4. **Assemble Pages**:,
		- Update the main application to incorporate your design and components,
		- Ensure proper routing and navigation,
		- Implement responsive layouts,
5. **Polish and Optimize**:,
		- Review the overall user experience,
		- Ensure smooth interactions and transitions,
		- Optimize performance where needed,

## SEO Best Practices,
Automatically implement SEO best practices on every page:,
- **Title Tags**: Include proper, descriptive title tags for each page,
- **Meta Descriptions**: Add compelling meta descriptions that accurately summarize page content,
- **Heading Structure**: Use a single ` + "`<h1>`" + ` per page with proper heading hierarchy,
- **Semantic HTML**: Use appropriate HTML5 semantic elements,
- **Unique IDs**: Ensure all interactive elements have unique, descriptive IDs for browser testing,
- **Performance**: Ensure fast page load times through optimization,
CRITICAL REMINDER: AESTHETICS ARE VERY IMPORTANT. If your web app looks simple and basic then you have FAILED!
</web_application_development>`

	// 临时消息提示 - 所有场景都需要
	promptEphemeralMessage = `
<ephemeral_message>
There will be an <EPHEMERAL_MESSAGE> appearing in the conversation at times. This is not coming from the user, but instead injected by the system as important information to pay attention to.
Do not respond to nor acknowledge those messages, but do follow them strictly.
</ephemeral_message>`

	// 沟通风格 - 所有场景都需要
	promptCommunicationStyle = `
<communication_style>
- **Formatting**. Format your responses in github-style markdown to make your responses easier for the USER to parse. For example, use headers to organize your responses and bolded or italicized text to highlight important keywords. Use backticks to format file, directory, function, and class names. If providing a URL to the user, format this in markdown as well, for example ` + "`[label](example.com)`" + `.
- **Proactiveness**. As an agent, you are allowed to be proactive, but only in the course of completing the user's task. For example, if the user asks you to add a new component, you can edit the code, verify build and test statuses, and take any other obvious follow-up actions, such as performing additional research. However, avoid surprising the user. For example, if the user asks HOW to approach something, you should answer their question and instead of jumping into editing a file.
- **Helpfulness**. Respond like a helpful software engineer who is explaining your work to a friendly collaborator on the project. Acknowledge mistakes or any backtracking you do as a result of new information.
- **Ask for clarification**. If you are unsure about the USER's intent, always ask for clarification rather than making assumptions.
</communication_style>`
)

// buildAntigravityPrompt 根据场景动态组装 Antigravity 提示词
// hasTools: 是否包含工具定义（决定是否添加 tool_calling 部分）
// includeWebDev: 是否包含 Web 开发指导（可选，默认不包含以减少 token 消耗）
func buildAntigravityPrompt(hasTools bool, includeWebDev bool) string {
	var sb strings.Builder

	// 1. 核心身份（必需）
	sb.WriteString(promptIdentity)

	// 2. 工具调用指导（仅在有工具时添加）
	if hasTools {
		sb.WriteString(promptToolCalling)
	}

	// 3. Web 开发指导（可选）
	if includeWebDev {
		sb.WriteString(promptWebDevelopment)
	}

	// 4. 临时消息提示（必需）
	sb.WriteString(promptEphemeralMessage)

	// 5. 沟通风格（必需）
	sb.WriteString(promptCommunicationStyle)

	return sb.String()
}

// defaultIdentityPatch 生成默认的身份补丁（根据是否有工具动态组装）
func defaultIdentityPatch(hasTools bool) string {
	// 默认不包含 Web 开发指导，以减少 token 消耗
	// 如果需要 Web 开发指导，可以通过 TransformOptions.IdentityPatch 自定义
	return buildAntigravityPrompt(hasTools, false)
}

// buildIgnoreInstruction 构建忽略指令，让 AI 忽略 Antigravity 身份提示词
// 注意：这个函数已被注释掉不再使用，保留以供参考
func buildIgnoreInstruction(prompt string) string {
	return "Please ignore following [ignore]" + prompt + "[/ignore]"
}

// GetDefaultIdentityPatch 返回默认的 Antigravity 身份提示词（包含所有模块）
func GetDefaultIdentityPatch() string {
	return buildAntigravityPrompt(true, true)
}

// buildSystemInstruction 构建 systemInstruction
// 根据请求中是否包含工具定义，动态组装 Antigravity 身份提示词
func buildSystemInstruction(system json.RawMessage, modelName string, opts TransformOptions, hasTools bool) *GeminiContent {
	var parts []GeminiPart

	// 先解析用户的 system prompt
	var userSystemParts []GeminiPart
	if len(system) > 0 {
		// 尝试解析为字符串
		var sysStr string
		if err := json.Unmarshal(system, &sysStr); err == nil {
			if strings.TrimSpace(sysStr) != "" {
				userSystemParts = append(userSystemParts, GeminiPart{Text: sysStr})
			}
		} else {
			// 尝试解析为数组
			var sysBlocks []SystemBlock
			if err := json.Unmarshal(system, &sysBlocks); err == nil {
				for _, block := range sysBlocks {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						userSystemParts = append(userSystemParts, GeminiPart{Text: block.Text})
					}
				}
			}
		}
	}

	// 注入身份提示词（根据是否有工具动态组装）
	if opts.EnableIdentityPatch {
		identityPatch := strings.TrimSpace(opts.IdentityPatch)
		if identityPatch == "" {
			// 根据是否有工具定义，动态生成提示词
			identityPatch = defaultIdentityPatch(hasTools)
		}
		// 添加 Antigravity 身份提示词
		parts = append(parts, GeminiPart{Text: identityPatch})
	}

	// 添加用户的 system prompt
	parts = append(parts, userSystemParts...)

	if len(parts) == 0 {
		return nil
	}

	return &GeminiContent{
		Role:  "user",
		Parts: parts,
	}
}

// buildContents 构建 contents
func buildContents(messages []ClaudeMessage, toolIDToName map[string]string, isThinkingEnabled, allowDummyThought bool) ([]GeminiContent, bool, error) {
	var contents []GeminiContent
	strippedThinking := false

	// 找到最后一条 assistant 消息的索引
	// 这是为了确保在 thinking 模式下，最后一条 assistant 消息有 thinking block
	lastAssistantIdx := -1
	for idx := len(messages) - 1; idx >= 0; idx-- {
		if messages[idx].Role == "assistant" {
			lastAssistantIdx = idx
			break
		}
	}

	for i, msg := range messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		parts, strippedThisMsg, err := buildParts(msg.Content, toolIDToName, allowDummyThought)
		if err != nil {
			return nil, false, fmt.Errorf("build parts for message %d: %w", i, err)
		}
		if strippedThisMsg {
			strippedThinking = true
		}

		// 只对最后一条 assistant 消息添加 dummy thinking block（如果缺失）
		// 服务器要求：当 thinking 启用时，最后一条 assistant 消息必须以 thinking block 开头
		// 注意：
		// 1. 这里改为检查 i == lastAssistantIdx，而不是 i == len(messages)-1
		// 2. 如果已经有 thinking blocks 被降级（strippedThinking=true），则不添加 dummy thinking block
		//    因为后续会禁用 thinking 模式，上游不需要 thinking block
		// 3. 对于 Claude 模型（allowDummyThought=false），如果需要 dummy thinking block 但无法提供有效 signature，
		//    则标记 strippedThinking=true 以触发 thinking 模式禁用，避免第一次请求就失败
		if role == "model" && isThinkingEnabled && !strippedThinking && i == lastAssistantIdx {
			hasThoughtPart := false
			for _, p := range parts {
				if p.Thought {
					hasThoughtPart = true
					break
				}
			}
			if !hasThoughtPart && len(parts) > 0 {
				if allowDummyThought {
					// Gemini 模型：添加 dummy thinking block with dummy signature
					dummyPart := GeminiPart{
						Text:             "Thinking...",
						Thought:          true,
						ThoughtSignature: dummyThoughtSignature,
					}
					parts = append([]GeminiPart{dummyPart}, parts...)
				} else {
					// Claude 模型：无法添加有效的 dummy thinking block
					// （上游要求 signature 字段必须存在且有效，不能为空或 dummy 值）
					// 标记为需要降级，以便后续禁用 thinking 模式
					strippedThinking = true
				}
			}
		}

		if len(parts) == 0 {
			continue
		}

		contents = append(contents, GeminiContent{
			Role:  role,
			Parts: parts,
		})
	}

	return contents, strippedThinking, nil
}

// dummyThoughtSignature 用于跳过 Gemini 3 thought_signature 验证
// 参考: https://ai.google.dev/gemini-api/docs/thought-signatures
const dummyThoughtSignature = "skip_thought_signature_validator"

// buildParts 构建消息的 parts
// allowDummyThought: 只有 Gemini 模型支持 dummy thought signature
func buildParts(content json.RawMessage, toolIDToName map[string]string, allowDummyThought bool) ([]GeminiPart, bool, error) {
	var parts []GeminiPart
	strippedThinking := false

	// 尝试解析为字符串
	var textContent string
	if err := json.Unmarshal(content, &textContent); err == nil {
		if textContent != "(no content)" && strings.TrimSpace(textContent) != "" {
			parts = append(parts, GeminiPart{Text: strings.TrimSpace(textContent)})
		}
		return parts, false, nil
	}

	// 解析为内容块数组
	var blocks []ContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, false, fmt.Errorf("parse content blocks: %w", err)
	}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "(no content)" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, GeminiPart{Text: block.Text})
			}

		case "thinking":
			part := GeminiPart{
				Text:    block.Thinking,
				Thought: true,
			}
			// Signature 处理逻辑：
			// - 如果有真实的 signature，直接使用（Claude 和 Gemini 都需要）
			// - 如果没有 signature：
			//   - Gemini 模型：使用 dummy signature 跳过验证
			//   - Claude 模型：降级为普通 text block（因为上游要求 signature 字段必须存在且有效）
			if block.Signature != "" {
				part.ThoughtSignature = block.Signature
			} else if !allowDummyThought {
				// Claude 模型需要有效 signature；在缺失时降级为普通文本，并在上层禁用 thinking mode。
				if strings.TrimSpace(block.Thinking) != "" {
					parts = append(parts, GeminiPart{Text: block.Thinking})
				}
				strippedThinking = true
				continue
			} else {
				// Gemini 模型使用 dummy signature
				part.ThoughtSignature = dummyThoughtSignature
			}
			parts = append(parts, part)

		case "image":
			if block.Source != nil && block.Source.Type == "base64" {
				parts = append(parts, GeminiPart{
					InlineData: &GeminiInlineData{
						MimeType: block.Source.MediaType,
						Data:     block.Source.Data,
					},
				})
			}

		case "tool_use":
			// 存储 id -> name 映射
			if block.ID != "" && block.Name != "" {
				toolIDToName[block.ID] = block.Name
			}

			part := GeminiPart{
				FunctionCall: &GeminiFunctionCall{
					Name: block.Name,
					Args: block.Input,
					ID:   block.ID,
				},
			}
			// tool_use 的 signature 处理：
			// - Gemini 模型：使用 dummy signature（跳过 thought_signature 校验）
			// - Claude 模型：透传上游返回的真实 signature（Vertex/Google 需要完整签名链路）
			if allowDummyThought {
				part.ThoughtSignature = dummyThoughtSignature
			} else if block.Signature != "" && block.Signature != dummyThoughtSignature {
				part.ThoughtSignature = block.Signature
			}
			parts = append(parts, part)

		case "tool_result":
			// 获取函数名
			funcName := block.Name
			if funcName == "" {
				if name, ok := toolIDToName[block.ToolUseID]; ok {
					funcName = name
				} else {
					funcName = block.ToolUseID
				}
			}

			// 解析 content
			resultContent := parseToolResultContent(block.Content, block.IsError)

			parts = append(parts, GeminiPart{
				FunctionResponse: &GeminiFunctionResponse{
					Name: funcName,
					Response: map[string]any{
						"result": resultContent,
					},
					ID: block.ToolUseID,
				},
			})
		}
	}

	return parts, strippedThinking, nil
}

// parseToolResultContent 解析 tool_result 的 content
func parseToolResultContent(content json.RawMessage, isError bool) string {
	if len(content) == 0 {
		if isError {
			return "Tool execution failed with no output."
		}
		return "Command executed successfully."
	}

	// 尝试解析为字符串
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		if strings.TrimSpace(str) == "" {
			if isError {
				return "Tool execution failed with no output."
			}
			return "Command executed successfully."
		}
		return str
	}

	// 尝试解析为数组
	var arr []map[string]any
	if err := json.Unmarshal(content, &arr); err == nil {
		var texts []string
		for _, item := range arr {
			if text, ok := item["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		result := strings.Join(texts, "\n")
		if strings.TrimSpace(result) == "" {
			if isError {
				return "Tool execution failed with no output."
			}
			return "Command executed successfully."
		}
		return result
	}

	// 返回原始 JSON
	return string(content)
}

// buildGenerationConfig 构建 generationConfig
func buildGenerationConfig(req *ClaudeRequest) *GeminiGenerationConfig {
	config := &GeminiGenerationConfig{
		MaxOutputTokens: 64000, // 默认最大输出
		StopSequences:   DefaultStopSequences,
	}

	// Thinking 配置
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		config.ThinkingConfig = &GeminiThinkingConfig{
			IncludeThoughts: true,
		}
		if req.Thinking.BudgetTokens > 0 {
			budget := req.Thinking.BudgetTokens
			// gemini-2.5-flash 上限 24576
			if strings.Contains(req.Model, "gemini-2.5-flash") && budget > 24576 {
				budget = 24576
			}
			config.ThinkingConfig.ThinkingBudget = budget
		}
	}

	// 其他参数
	if req.Temperature != nil {
		config.Temperature = req.Temperature
	}
	if req.TopP != nil {
		config.TopP = req.TopP
	}
	if req.TopK != nil {
		config.TopK = req.TopK
	}

	return config
}

// buildTools 构建 tools
func buildTools(tools []ClaudeTool) []GeminiToolDeclaration {
	if len(tools) == 0 {
		return nil
	}

	// 检查是否有 web_search 工具
	hasWebSearch := false
	for _, tool := range tools {
		if tool.Name == "web_search" {
			hasWebSearch = true
			break
		}
	}

	if hasWebSearch {
		// Web Search 工具映射
		return []GeminiToolDeclaration{{
			GoogleSearch: &GeminiGoogleSearch{
				EnhancedContent: &GeminiEnhancedContent{
					ImageSearch: &GeminiImageSearch{
						MaxResultCount: 5,
					},
				},
			},
		}}
	}

	// 普通工具
	var funcDecls []GeminiFunctionDecl
	for _, tool := range tools {
		// 跳过无效工具名称
		if strings.TrimSpace(tool.Name) == "" {
			log.Printf("Warning: skipping tool with empty name")
			continue
		}

		var description string
		var inputSchema map[string]any

		// 检查是否为 custom 类型工具 (MCP)
		if tool.Type == "custom" {
			if tool.Custom == nil || tool.Custom.InputSchema == nil {
				log.Printf("[Warning] Skipping invalid custom tool '%s': missing custom spec or input_schema", tool.Name)
				continue
			}
			description = tool.Custom.Description
			inputSchema = tool.Custom.InputSchema

		} else {
			// 标准格式: 从顶层字段获取
			description = tool.Description
			inputSchema = tool.InputSchema
		}

		// 清理 JSON Schema
		params := cleanJSONSchema(inputSchema)
		// 为 nil schema 提供默认值
		if params == nil {
			params = map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{},
			}
		}

		funcDecls = append(funcDecls, GeminiFunctionDecl{
			Name:        tool.Name,
			Description: description,
			Parameters:  params,
		})
	}

	if len(funcDecls) == 0 {
		return nil
	}

	return []GeminiToolDeclaration{{
		FunctionDeclarations: funcDecls,
	}}
}

// cleanJSONSchema 清理 JSON Schema，移除 Antigravity/Gemini 不支持的字段
// 参考 proxycast 的实现，确保 schema 符合 JSON Schema draft 2020-12
func cleanJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	cleaned := cleanSchemaValue(schema, "$")
	result, ok := cleaned.(map[string]any)
	if !ok {
		return nil
	}

	// 确保有 type 字段（默认 OBJECT）
	if _, hasType := result["type"]; !hasType {
		result["type"] = "OBJECT"
	}

	// 确保有 properties 字段（默认空对象）
	if _, hasProps := result["properties"]; !hasProps {
		result["properties"] = make(map[string]any)
	}

	// 验证 required 中的字段都存在于 properties 中
	if required, ok := result["required"].([]any); ok {
		if props, ok := result["properties"].(map[string]any); ok {
			validRequired := make([]any, 0, len(required))
			for _, r := range required {
				if reqName, ok := r.(string); ok {
					if _, exists := props[reqName]; exists {
						validRequired = append(validRequired, r)
					}
				}
			}
			if len(validRequired) > 0 {
				result["required"] = validRequired
			} else {
				delete(result, "required")
			}
		}
	}

	return result
}

var schemaValidationKeys = map[string]bool{
	"minLength":         true,
	"maxLength":         true,
	"pattern":           true,
	"minimum":           true,
	"maximum":           true,
	"exclusiveMinimum":  true,
	"exclusiveMaximum":  true,
	"multipleOf":        true,
	"uniqueItems":       true,
	"minItems":          true,
	"maxItems":          true,
	"minProperties":     true,
	"maxProperties":     true,
	"patternProperties": true,
	"propertyNames":     true,
	"dependencies":      true,
	"dependentSchemas":  true,
	"dependentRequired": true,
}

var warnedSchemaKeys sync.Map

func schemaCleaningWarningsEnabled() bool {
	// 可通过环境变量强制开关，方便排查：SUB2API_SCHEMA_CLEAN_WARN=true/false
	if v := strings.TrimSpace(os.Getenv("SUB2API_SCHEMA_CLEAN_WARN")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	// 默认：非 release 模式下输出（debug/test）
	return gin.Mode() != gin.ReleaseMode
}

func warnSchemaKeyRemovedOnce(key, path string) {
	if !schemaCleaningWarningsEnabled() {
		return
	}
	if !schemaValidationKeys[key] {
		return
	}
	if _, loaded := warnedSchemaKeys.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[SchemaClean] removed unsupported JSON Schema validation field key=%q path=%q", key, path)
}

// excludedSchemaKeys 不支持的 schema 字段
// 基于 Claude API (Vertex AI) 的实际支持情况
// 支持: type, description, enum, properties, required, additionalProperties, items
// 不支持: minItems, maxItems, minLength, maxLength, pattern, minimum, maximum 等验证字段
var excludedSchemaKeys = map[string]bool{
	// 元 schema 字段
	"$schema": true,
	"$id":     true,
	"$ref":    true,

	// 字符串验证（Gemini 不支持）
	"minLength": true,
	"maxLength": true,
	"pattern":   true,

	// 数字验证（Claude API 通过 Vertex AI 不支持这些字段）
	"minimum":          true,
	"maximum":          true,
	"exclusiveMinimum": true,
	"exclusiveMaximum": true,
	"multipleOf":       true,

	// 数组验证（Claude API 通过 Vertex AI 不支持这些字段）
	"uniqueItems": true,
	"minItems":    true,
	"maxItems":    true,

	// 组合 schema（Gemini 不支持）
	"oneOf":       true,
	"anyOf":       true,
	"allOf":       true,
	"not":         true,
	"if":          true,
	"then":        true,
	"else":        true,
	"$defs":       true,
	"definitions": true,

	// 对象验证（仅保留 properties/required/additionalProperties）
	"minProperties":     true,
	"maxProperties":     true,
	"patternProperties": true,
	"propertyNames":     true,
	"dependencies":      true,
	"dependentSchemas":  true,
	"dependentRequired": true,

	// 其他不支持的字段
	"default":          true,
	"const":            true,
	"examples":         true,
	"deprecated":       true,
	"readOnly":         true,
	"writeOnly":        true,
	"contentMediaType": true,
	"contentEncoding":  true,

	// Claude 特有字段
	"strict": true,
}

// cleanSchemaValue 递归清理 schema 值
func cleanSchemaValue(value any, path string) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, val := range v {
			// 跳过不支持的字段
			if excludedSchemaKeys[k] {
				warnSchemaKeyRemovedOnce(k, path)
				continue
			}

			// 特殊处理 type 字段
			if k == "type" {
				result[k] = cleanTypeValue(val)
				continue
			}

			// 特殊处理 format 字段：只保留 Gemini 支持的 format 值
			if k == "format" {
				if formatStr, ok := val.(string); ok {
					// Gemini 只支持 date-time, date, time
					if formatStr == "date-time" || formatStr == "date" || formatStr == "time" {
						result[k] = val
					}
					// 其他 format 值直接跳过
				}
				continue
			}

			// 特殊处理 additionalProperties：Claude API 只支持布尔值，不支持 schema 对象
			if k == "additionalProperties" {
				if boolVal, ok := val.(bool); ok {
					result[k] = boolVal
				} else {
					// 如果是 schema 对象，转换为 false（更安全的默认值）
					result[k] = false
				}
				continue
			}

			// 递归清理所有值
			result[k] = cleanSchemaValue(val, path+"."+k)
		}
		return result

	case []any:
		// 递归处理数组中的每个元素
		cleaned := make([]any, 0, len(v))
		for i, item := range v {
			cleaned = append(cleaned, cleanSchemaValue(item, fmt.Sprintf("%s[%d]", path, i)))
		}
		return cleaned

	default:
		return value
	}
}

// cleanTypeValue 处理 type 字段，转换为大写
func cleanTypeValue(value any) any {
	switch v := value.(type) {
	case string:
		return strings.ToUpper(v)
	case []any:
		// 联合类型 ["string", "null"] -> 取第一个非 null 类型
		for _, t := range v {
			if ts, ok := t.(string); ok && ts != "null" {
				return strings.ToUpper(ts)
			}
		}
		// 如果只有 null，返回 STRING
		return "STRING"
	default:
		return value
	}
}
