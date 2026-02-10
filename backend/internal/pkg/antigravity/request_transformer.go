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

// webSearchFallbackModel web_search 请求使用的降级模型
const webSearchFallbackModel = "gemini-2.5-flash"

// MaxTokensBudgetPadding max_tokens 自动调整时在 budget_tokens 基础上增加的额度
// Claude API 要求 max_tokens > thinking.budget_tokens，否则返回 400 错误
const MaxTokensBudgetPadding = 1000

// Gemini 2.5 Flash thinking budget 上限
const Gemini25FlashThinkingBudgetLimit = 24576

// ensureMaxTokensGreaterThanBudget 确保 max_tokens > budget_tokens
// Claude API 要求启用 thinking 时，max_tokens 必须大于 thinking.budget_tokens
// 返回调整后的 maxTokens 和是否进行了调整
func ensureMaxTokensGreaterThanBudget(maxTokens, budgetTokens int) (int, bool) {
	if budgetTokens > 0 && maxTokens <= budgetTokens {
		return budgetTokens + MaxTokensBudgetPadding, true
	}
	return maxTokens, false
}

// TransformClaudeToGemini 将 Claude 请求转换为 v1internal Gemini 格式
func TransformClaudeToGemini(claudeReq *ClaudeRequest, projectID, mappedModel string) ([]byte, error) {
	return TransformClaudeToGeminiWithOptions(claudeReq, projectID, mappedModel, DefaultTransformOptions())
}

// TransformClaudeToGeminiWithOptions 将 Claude 请求转换为 v1internal Gemini 格式（可配置身份补丁等行为）
func TransformClaudeToGeminiWithOptions(claudeReq *ClaudeRequest, projectID, mappedModel string, opts TransformOptions) ([]byte, error) {
	// 用于存储 tool_use id -> name 映射
	toolIDToName := make(map[string]string)

	// 检测是否有 web_search 工具
	hasWebSearchTool := hasWebSearchTool(claudeReq.Tools)
	requestType := "agent"
	targetModel := mappedModel
	if hasWebSearchTool {
		requestType = "web_search"
		if targetModel != webSearchFallbackModel {
			targetModel = webSearchFallbackModel
		}
	}

	// 检测是否启用 thinking
	isThinkingEnabled := claudeReq.Thinking != nil && claudeReq.Thinking.Type == "enabled"

	// 只有 Gemini 模型支持 dummy thought workaround
	// Claude 模型通过 Vertex/Google API 需要有效的 thought signatures
	allowDummyThought := strings.HasPrefix(targetModel, "gemini-")

	// 1. 构建 contents
	contents, strippedThinking, err := buildContents(claudeReq.Messages, toolIDToName, isThinkingEnabled, allowDummyThought)
	if err != nil {
		return nil, fmt.Errorf("build contents: %w", err)
	}

	// 2. 构建 tools（需要在 buildSystemInstruction 之前，以便检测是否有工具）
	tools := buildTools(claudeReq.Tools)

	// 3. 构建 systemInstruction（根据是否有工具动态组装提示词，并检测 MCP 工具）
	systemInstruction := buildSystemInstruction(claudeReq.System, claudeReq.Model, opts, claudeReq.Tools)

	// 4. 构建 generationConfig
	reqForConfig := claudeReq
	if strippedThinking {
		// If we had to downgrade thinking blocks to plain text due to missing/invalid signatures,
		// disable upstream thinking mode to avoid signature/structure validation errors.
		reqCopy := *claudeReq
		reqCopy.Thinking = nil
		reqForConfig = &reqCopy
		// 同时移除模型名称中的 "-thinking" 后缀，避免上游根据模型名判断启用 thinking 模式
		// 注意：Opus 模型只有 thinking 版本，不能移除后缀
		if strings.HasSuffix(mappedModel, "-thinking") {
			// 只对 Sonnet 等有非 thinking 版本的模型移除后缀
			if strings.Contains(mappedModel, "sonnet") || strings.Contains(mappedModel, "gemini") {
				mappedModel = strings.TrimSuffix(mappedModel, "-thinking")
			}
			// Opus 等只有 thinking 版本的模型：保持后缀
		}
	}
	if targetModel != "" && targetModel != reqForConfig.Model {
		reqCopy := *reqForConfig
		reqCopy.Model = targetModel
		reqForConfig = &reqCopy
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
		RequestType: requestType,
		Model:       targetModel,
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

// GetDefaultIdentityPatch 返回默认的 Antigravity 身份提示词（包含所有模块）
func GetDefaultIdentityPatch() string {
	return buildAntigravityPrompt(true, true)
}

// mcpXMLProtocol MCP XML 工具调用协议（与 Antigravity-Manager 保持一致）
const mcpXMLProtocol = `
==== MCP XML 工具调用协议 (Workaround) ====
当你需要调用名称以 ` + "`mcp__`" + ` 开头的 MCP 工具时：
1) 优先尝试 XML 格式调用：输出 ` + "`<mcp__tool_name>{\"arg\":\"value\"}</mcp__tool_name>`" + `。
2) 必须直接输出 XML 块，无需 markdown 包装，内容为 JSON 格式的入参。
3) 这种方式具有更高的连通性和容错性，适用于大型结果返回场景。
===========================================`

// hasMCPTools 检测是否有 mcp__ 前缀的工具
func hasMCPTools(tools []ClaudeTool) bool {
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "mcp__") {
			return true
		}
	}
	return false
}

// filterOpenCodePrompt 过滤 OpenCode 默认提示词，只保留用户自定义指令
func filterOpenCodePrompt(text string) string {
	if !strings.Contains(text, "You are an interactive CLI tool") {
		return text
	}
	// 提取 "Instructions from:" 及之后的部分
	if idx := strings.Index(text, "Instructions from:"); idx >= 0 {
		return text[idx:]
	}
	// 如果没有自定义指令，返回空
	return ""
}

// buildSystemInstruction 构建 systemInstruction
// 根据请求中是否包含工具定义，动态组装 Antigravity 身份提示词
// 如果有 MCP 工具，注入 XML 调用协议
func buildSystemInstruction(system json.RawMessage, modelName string, opts TransformOptions, tools []ClaudeTool) *GeminiContent {
	hasTools := len(tools) > 0
	userHasAntigravityIdentity := false
	var parts []GeminiPart

	// 先解析用户的 system prompt
	var userSystemParts []GeminiPart
	if len(system) > 0 {
		// 尝试解析为字符串
		var sysStr string
		if err := json.Unmarshal(system, &sysStr); err == nil {
			if strings.TrimSpace(sysStr) != "" {
				if strings.Contains(sysStr, "You are Antigravity") {
					userHasAntigravityIdentity = true
				}
				// 过滤 OpenCode 默认提示词
				filtered := filterOpenCodePrompt(sysStr)
				if filtered != "" {
					userSystemParts = append(userSystemParts, GeminiPart{Text: filtered})
				}
			}
		} else {
			// 尝试解析为数组
			var sysBlocks []SystemBlock
			if err := json.Unmarshal(system, &sysBlocks); err == nil {
				for _, block := range sysBlocks {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						if strings.Contains(block.Text, "You are Antigravity") {
							userHasAntigravityIdentity = true
						}
						// 过滤 OpenCode 默认提示词
						filtered := filterOpenCodePrompt(block.Text)
						if filtered != "" {
							userSystemParts = append(userSystemParts, GeminiPart{Text: filtered})
						}
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

	// 检测是否有 MCP 工具，如有则注入 XML 调用协议
	if hasMCPTools(tools) {
		parts = append(parts, GeminiPart{Text: mcpXMLProtocol})
	}

	// 如果用户没有提供 Antigravity 身份，添加结束标记
	if !userHasAntigravityIdentity {
		parts = append(parts, GeminiPart{Text: "\n--- [SYSTEM_PROMPT_END] ---"})
	}

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
// 注意: 这个 sentinel value 只适用于普通 Gemini API，Vertex AI 不接受
const dummyThoughtSignature = "skip_thought_signature_validator"

// minSignatureLength 最小签名长度
// 有效的 thought signature 通常长度 >= 50
// 太短的 signature 很可能是无效的或伪造的
const minSignatureLength = 50

// isValidSignature 验证 signature 是否有效
// 返回 true 如果 signature 看起来是有效的（非空、非 dummy、长度足够）
func isValidSignature(sig string) bool {
	if sig == "" {
		return false
	}
	if sig == dummyThoughtSignature {
		return false
	}
	if len(sig) < minSignatureLength {
		return false
	}
	return true
}

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
			// Signature 处理逻辑：
			// 1. 验证 signature 是否有效（非空、非 dummy、长度 >= 50）
			// 2. 有效 signature：直接使用
			// 3. 无效 signature：
			//    - Gemini 模型：使用 dummy signature 跳过验证
			//    - Claude 模型（Vertex AI）：降级为普通 text block
			if isValidSignature(block.Signature) {
				// 有效 signature，创建 thinking part
				part := GeminiPart{
					Text:             block.Thinking,
					Thought:          true,
					ThoughtSignature: block.Signature,
				}
				parts = append(parts, part)
			} else if allowDummyThought {
				// Gemini 模型：使用 dummy signature
				part := GeminiPart{
					Text:             block.Thinking,
					Thought:          true,
					ThoughtSignature: dummyThoughtSignature,
				}
				parts = append(parts, part)
			} else {
				// Claude 模型（Vertex AI）：无效 signature，降级为普通文本
				// 因为 Vertex AI 不接受 dummy signature
				if strings.TrimSpace(block.Thinking) != "" {
					parts = append(parts, GeminiPart{Text: block.Thinking})
				}
				strippedThinking = true
				continue
			}

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
			// 1. 验证 signature 是否有效
			// 2. 有效 signature：直接使用
			// 3. 无效 signature：
			//    - Gemini 模型：使用 dummy signature 跳过验证
			//    - Claude 模型（Vertex AI）：不添加 signature 字段
			//      （让上游根据消息结构自动处理，避免 dummy signature 被拒绝）
			if isValidSignature(block.Signature) {
				part.ThoughtSignature = block.Signature
			} else if allowDummyThought {
				// Gemini 模型：使用 dummy signature
				part.ThoughtSignature = dummyThoughtSignature
			}
			// Claude 模型（Vertex AI）且无有效 signature：不设置 ThoughtSignature
			// 这样可以避免 "Invalid signature in thinking block" 错误
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
			// gemini-2.5-flash 上限
			if strings.Contains(req.Model, "gemini-2.5-flash") && budget > Gemini25FlashThinkingBudgetLimit {
				budget = Gemini25FlashThinkingBudgetLimit
			}
			config.ThinkingConfig.ThinkingBudget = budget

			// 自动修正：max_tokens 必须大于 budget_tokens
			if adjusted, ok := ensureMaxTokensGreaterThanBudget(config.MaxOutputTokens, budget); ok {
				log.Printf("[Antigravity] Auto-adjusted max_tokens from %d to %d (must be > budget_tokens=%d)",
					config.MaxOutputTokens, adjusted, budget)
				config.MaxOutputTokens = adjusted
			}
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

func hasWebSearchTool(tools []ClaudeTool) bool {
	for _, tool := range tools {
		if isWebSearchTool(tool) {
			return true
		}
	}
	return false
}

func isWebSearchTool(tool ClaudeTool) bool {
	if strings.HasPrefix(tool.Type, "web_search") || tool.Type == "google_search" {
		return true
	}

	name := strings.TrimSpace(tool.Name)
	switch name {
	case "web_search", "google_search", "web_search_20250305":
		return true
	default:
		return false
	}
}

// buildTools 构建 tools
func buildTools(tools []ClaudeTool) []GeminiToolDeclaration {
	if len(tools) == 0 {
		return nil
	}

	hasWebSearch := hasWebSearchTool(tools)

	// 普通工具
	var funcDecls []GeminiFunctionDecl
	for _, tool := range tools {
		if isWebSearchTool(tool) {
			continue
		}
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
		if !hasWebSearch {
			return nil
		}

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

	// [工具冲突检查] Gemini v1internal API 不支持在一次请求中混用 googleSearch 和 functionDeclarations
	// 因为这会导致 400 错误 "searching files error" 等问题
	// 参考: Antigravity-Manager/src-tauri/src/proxy/mappers/common_utils.rs:133-142
	if hasWebSearch {
		log.Printf("[ISSUE-TOOLS-CONFLICT] Detected incompatible tools: both functionDeclarations (%d tools) and googleSearch cannot be used together in Gemini v1internal. Removing googleSearch to prevent 400 errors.", len(funcDecls))
		// 只返回 functionDeclarations，移除 googleSearch
		return []GeminiToolDeclaration{{
			FunctionDeclarations: funcDecls,
		}}
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
