// Package kiro provides request transformation from Claude to CodeWhisperer format.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Constants for tool processing
const (
	ToolDescriptionMaxLength = 500 // Max tool description length (aligned with proxycast)
	MaxFunctionTools         = 50  // Max number of function tools
)

// TransformContext holds context for the transformation process
type TransformContext struct {
	ModelID      string            // CodeWhisperer model ID
	ConvID       string            // Conversation ID
	AgentContID  string            // Agent continuation ID
	ToolUseIDMap map[string]string // tool_use_id -> tool_name mapping
	MsgCounter   int               // Message counter
}

// NewTransformContext creates a new transformation context
func NewTransformContext(model string) *TransformContext {
	modelID := GetModelID(model)

	return &TransformContext{
		ModelID:      modelID,
		ConvID:       uuid.New().String(),
		AgentContID:  uuid.New().String(),
		ToolUseIDMap: make(map[string]string),
		MsgCounter:   0,
	}
}

// NextMsgID returns the next message ID and increments the counter
func (ctx *TransformContext) NextMsgID() int {
	ctx.MsgCounter++
	return ctx.MsgCounter
}

// RegisterToolUse registers a tool_use_id -> tool_name mapping
func (ctx *TransformContext) RegisterToolUse(toolUseID, toolName string) {
	if toolUseID != "" {
		ctx.ToolUseIDMap[toolUseID] = toolName
	}
}

// ClaudeRequest represents the incoming Claude API request
type ClaudeRequest struct {
	Model       string         `json:"model"`
	Messages    []ClaudeMessage `json:"messages"`
	System      any            `json:"system,omitempty"`
	Tools       []ClaudeTool   `json:"tools,omitempty"`
	ToolChoice  any            `json:"tool_choice,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
	Thinking    map[string]any `json:"thinking,omitempty"`
}

// TransformClaudeToCodeWhisperer transforms a Claude request to CodeWhisperer format
func TransformClaudeToCodeWhisperer(claudeReq *ClaudeRequest, profileArn string) (*CodeWhispererRequest, error) {
	if claudeReq == nil {
		return nil, fmt.Errorf("claude request is nil")
	}

	if len(claudeReq.Messages) == 0 {
		return nil, fmt.Errorf("messages cannot be empty")
	}

	// Create transformation context
	ctx := NewTransformContext(claudeReq.Model)
	if ctx.ModelID == "" {
		return nil, fmt.Errorf("unsupported model: %s", claudeReq.Model)
	}

	// Parse all messages to unified format
	messages, err := parseAllMessages(claudeReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("parse messages failed: %w", err)
	}

	// Preprocess messages (merge adjacent same-role messages)
	messages = preprocessMessages(messages)
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages empty after preprocessing")
	}

	// Separate last message as currentMessage
	currentMsg := messages[len(messages)-1]
	historyMsgs := messages[:len(messages)-1]

	// Process tools (truncate long descriptions)
	processedTools := processTools(claudeReq.Tools)

	// Build currentMessage
	var currentMessage CurrentMessage
	var hasHistory bool

	if currentMsg.Role == "assistant" {
		// If last message is assistant, add to history and create "Continue" message
		historyMsgs = append(historyMsgs, currentMsg)
		currentMessage = buildContinueMessage(ctx, processedTools)
		hasHistory = true
	} else {
		hasHistory = len(historyMsgs) > 0
		currentMessage = buildCurrentMessage(ctx, currentMsg, processedTools, claudeReq, !hasHistory)
	}

	// Build history
	history := buildHistory(ctx, historyMsgs, claudeReq.System, currentMsg, claudeReq)

	// Assemble final request
	cwReq := &CodeWhispererRequest{
		ConversationState: ConversationState{
			ChatTriggerType: determineChatTriggerType(claudeReq),
			ConversationID:  ctx.ConvID,
			CurrentMessage:  currentMessage,
			History:         history,
		},
	}

	if profileArn != "" {
		cwReq.ProfileArn = profileArn
	}

	if ctx.AgentContID != "" {
		cwReq.ConversationState.AgentContinuationID = ctx.AgentContID
	}

	return cwReq, nil
}

// parseAllMessages parses all Claude messages to unified format
func parseAllMessages(messages []ClaudeMessage) ([]*UnifiedMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages cannot be empty")
	}

	result := make([]*UnifiedMessage, 0, len(messages))
	for i, msg := range messages {
		um, err := parseClaudeMessage(msg, i)
		if err != nil {
			return nil, err
		}
		result = append(result, um)
	}

	return result, nil
}

// parseClaudeMessage parses a single Claude message to unified format
func parseClaudeMessage(msg ClaudeMessage, msgIdx int) (*UnifiedMessage, error) {
	if msg.Role == "" {
		return nil, fmt.Errorf("message[%d]: missing role field", msgIdx)
	}

	um := &UnifiedMessage{
		Role:        msg.Role,
		TextParts:   make([]string, 0),
		Images:      make([]CodeWhispererImage, 0),
		ToolUses:    make([]ToolUseData, 0),
		ToolResults: make([]ToolResultData, 0),
	}

	// Handle content based on type
	switch content := msg.Content.(type) {
	case string:
		if content != "" {
			um.TextParts = append(um.TextParts, content)
		}
	case []any:
		for blockIdx, block := range content {
			if err := parseContentBlock(um, block, msgIdx, blockIdx); err != nil {
				// Continue processing other blocks, don't abort
				continue
			}
		}
	case []ContentBlock:
		for blockIdx, block := range content {
			if err := parseTypedContentBlock(um, &block, msgIdx, blockIdx); err != nil {
				continue
			}
		}
	}

	return um, nil
}

// parseContentBlock parses a content block from any type
func parseContentBlock(um *UnifiedMessage, block any, msgIdx, blockIdx int) error {
	blockMap, ok := block.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid content block type")
	}

	blockType, _ := blockMap["type"].(string)
	if blockType == "" {
		return fmt.Errorf("content block missing type field")
	}

	switch blockType {
	case "text":
		if text, ok := blockMap["text"].(string); ok && text != "" {
			um.TextParts = append(um.TextParts, text)
		}

	case "image":
		if source, ok := blockMap["source"].(map[string]any); ok {
			mediaType, _ := source["media_type"].(string)
			data, _ := source["data"].(string)
			if data != "" {
				um.Images = append(um.Images, CodeWhispererImage{
					Format: MediaTypeToFormat(mediaType),
					Source: ImageSource{Bytes: data},
				})
			}
		}

	case "tool_use":
		id, _ := blockMap["id"].(string)
		name, _ := blockMap["name"].(string)
		input := make(map[string]any)
		if inputRaw, ok := blockMap["input"].(map[string]any); ok {
			input = inputRaw
		}
		if name != "" {
			um.ToolUses = append(um.ToolUses, ToolUseData{
				ID:    id,
				Name:  name,
				Input: input,
			})
		}

	case "tool_result":
		toolUseID, _ := blockMap["tool_use_id"].(string)
		isError, _ := blockMap["is_error"].(bool)
		content := extractToolResultContent(blockMap["content"])
		um.ToolResults = append(um.ToolResults, ToolResultData{
			ToolUseID: toolUseID,
			Content:   content,
			IsError:   isError,
		})

	case "thinking":
		// Ignore thinking blocks for CodeWhisperer
		if text, ok := blockMap["thinking"].(string); ok && text != "" {
			um.Thinking = &ThinkingData{Content: text}
		}
	}

	return nil
}

// parseTypedContentBlock parses a typed ContentBlock
func parseTypedContentBlock(um *UnifiedMessage, block *ContentBlock, msgIdx, blockIdx int) error {
	switch block.Type {
	case "text":
		if block.Text != nil && *block.Text != "" {
			um.TextParts = append(um.TextParts, *block.Text)
		}

	case "image":
		if block.Source != nil && block.Source.Bytes != "" {
			um.Images = append(um.Images, CodeWhispererImage{
				Format: MediaTypeToFormat(FormatToMediaType(block.Source.Bytes)),
				Source: *block.Source,
			})
		}

	case "tool_use":
		if block.Name != nil && *block.Name != "" {
			id := ""
			if block.ID != nil {
				id = *block.ID
			}
			input := make(map[string]any)
			if block.Input != nil {
				if inputMap, ok := block.Input.(map[string]any); ok {
					input = inputMap
				}
			}
			um.ToolUses = append(um.ToolUses, ToolUseData{
				ID:    id,
				Name:  *block.Name,
				Input: input,
			})
		}

	case "tool_result":
		toolUseID := ""
		if block.ToolUseID != nil {
			toolUseID = *block.ToolUseID
		}
		isError := false
		if block.IsError != nil {
			isError = *block.IsError
		}
		content := ""
		if block.Content != nil {
			content = extractToolResultContent(block.Content)
		}
		um.ToolResults = append(um.ToolResults, ToolResultData{
			ToolUseID: toolUseID,
			Content:   content,
			IsError:   isError,
		})

	case "thinking":
		if block.Thinking != nil && *block.Thinking != "" {
			signature := ""
			if block.Signature != nil {
				signature = *block.Signature
			}
			um.Thinking = &ThinkingData{
				Content:   *block.Thinking,
				Signature: signature,
			}
		}
	}

	return nil
}

// extractToolResultContent extracts text content from tool_result
func extractToolResultContent(content any) string {
	switch c := content.(type) {
	case string:
		return c

	case []any:
		var parts []string
		for _, item := range c {
			if itemMap, ok := item.(map[string]any); ok {
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")

	case map[string]any:
		if text, ok := c["text"].(string); ok {
			return text
		}
	}

	if content != nil {
		return fmt.Sprintf("%v", content)
	}
	return ""
}

// preprocessMessages merges adjacent same-role messages
func preprocessMessages(messages []*UnifiedMessage) []*UnifiedMessage {
	if len(messages) == 0 {
		return messages
	}

	// Remove trailing assistant message with only "{"
	lastMsg := messages[len(messages)-1]
	if lastMsg.Role == "assistant" && isOnlyOpenBrace(lastMsg) {
		messages = messages[:len(messages)-1]
		if len(messages) == 0 {
			return messages
		}
	}

	// Merge adjacent same-role messages
	merged := make([]*UnifiedMessage, 0, len(messages))
	for _, msg := range messages {
		if len(merged) == 0 {
			merged = append(merged, msg)
			continue
		}

		lastMerged := merged[len(merged)-1]
		if msg.Role == lastMerged.Role {
			mergeMessages(lastMerged, msg)
		} else {
			merged = append(merged, msg)
		}
	}

	return merged
}

// isOnlyOpenBrace checks if message contains only "{"
func isOnlyOpenBrace(msg *UnifiedMessage) bool {
	if len(msg.TextParts) != 1 {
		return false
	}
	return strings.TrimSpace(msg.TextParts[0]) == "{"
}

// mergeMessages merges source message into target
func mergeMessages(target, source *UnifiedMessage) {
	target.TextParts = append(target.TextParts, source.TextParts...)
	target.Images = append(target.Images, source.Images...)
	target.ToolUses = append(target.ToolUses, source.ToolUses...)
	target.ToolResults = append(target.ToolResults, source.ToolResults...)
	if source.Thinking != nil {
		target.Thinking = source.Thinking
	}
}

// buildHistory builds the history entries
func buildHistory(ctx *TransformContext, messages []*UnifiedMessage, systemRaw any, currentMsg *UnifiedMessage, claudeReq *ClaudeRequest) []HistoryEntry {
	if len(messages) == 0 {
		return nil
	}

	// Build system prompt
	systemPrompt := buildSystemPrompt(systemRaw, claudeReq)

	// Inject system prompt into first user message
	if systemPrompt != "" && len(messages) > 0 {
		firstMsg := messages[0]
		if firstMsg.Role == "user" {
			originalContent := firstMsg.GetText()
			if originalContent != "" {
				firstMsg.TextParts = []string{systemPrompt + "\n\n" + originalContent}
			} else {
				firstMsg.TextParts = []string{systemPrompt}
			}
		}
	}

	// Build history entries
	var history []HistoryEntry
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			history = append(history, buildUserHistoryEntry(ctx, msg))
		case "assistant":
			if entry := buildAssistantHistoryEntry(ctx, msg); entry != nil {
				history = append(history, *entry)
			}
		}
	}

	// Clean orphan tool_uses
	history = cleanOrphanToolUses(history, currentMsg)

	// Fix history alternation
	history = fixHistoryAlternation(ctx, history)

	return history
}

// buildSystemPrompt builds the complete system prompt
func buildSystemPrompt(systemRaw any, claudeReq *ClaudeRequest) string {
	var parts []string

	// Process original system prompt
	switch sys := systemRaw.(type) {
	case string:
		if sys != "" {
			parts = append(parts, sys)
		}
	case []any:
		for _, sysMsg := range sys {
			if sysMsgMap, ok := sysMsg.(map[string]any); ok {
				if text, ok := sysMsgMap["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
	}

	// Build tool documentation for long descriptions
	if claudeReq != nil && len(claudeReq.Tools) > 0 {
		if toolDoc := buildToolDocumentation(claudeReq.Tools); toolDoc != "" {
			parts = append(parts, toolDoc)
		}
	}

	// Handle tool_choice=required
	if claudeReq != nil && isToolChoiceRequired(claudeReq.ToolChoice) && len(claudeReq.Tools) > 0 {
		toolInstruction := "\n\n[CRITICAL INSTRUCTION] You MUST use one of the provided tools to respond. Do not respond with plain text - you must call a tool. This is a strict requirement that cannot be ignored."
		parts = append(parts, toolInstruction)
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "\n\n")
}

// isToolChoiceRequired checks if tool_choice is "required"
func isToolChoiceRequired(toolChoice any) bool {
	if toolChoice == nil {
		return false
	}

	switch tc := toolChoice.(type) {
	case string:
		return tc == "required"
	case map[string]any:
		if tcType, ok := tc["type"].(string); ok {
			return tcType == "required"
		}
	}
	return false
}

// buildToolDocumentation builds documentation for tools with long descriptions
func buildToolDocumentation(tools []ClaudeTool) string {
	var docParts []string

	for _, tool := range tools {
		if len(tool.Description) > ToolDescriptionMaxLength {
			docParts = append(docParts, fmt.Sprintf("## Tool: %s\n\n%s", tool.Name, tool.Description))
		}
	}

	if len(docParts) == 0 {
		return ""
	}

	return "---\n# Tool Documentation\nThe following tools have detailed documentation that couldn't fit in the tool definition.\n\n" +
		strings.Join(docParts, "\n\n---\n\n")
}

// buildUserHistoryEntry builds a user history entry
func buildUserHistoryEntry(ctx *TransformContext, msg *UnifiedMessage) HistoryEntry {
	content := msg.GetText()

	userMsg := &HistoryUserMessage{
		Content: content,
		ModelID: ctx.ModelID,
		Origin:  "AI_EDITOR",
	}

	// Add images
	if len(msg.Images) > 0 {
		userMsg.Images = msg.Images
	}

	// Add tool_results
	if msg.HasToolResults() {
		toolResults := buildToolResults(msg.ToolResults)
		if len(toolResults) > 0 {
			userMsg.UserInputMessageContext = &UserInputMessageContext{
				ToolResults: toolResults,
			}
			if userMsg.Content == "" {
				userMsg.Content = "Tool results provided."
			}
		}
	}

	return HistoryEntry{
		MessageID: fmt.Sprintf("msg-%03d", ctx.NextMsgID()),
		Type:      "user",
		User:      userMsg,
	}
}

// buildToolResults builds tool results array
func buildToolResults(results []ToolResultData) []ToolResult {
	// Deduplicate by toolUseId
	seen := make(map[string]bool)
	var unique []ToolResultData

	for _, result := range results {
		if !seen[result.ToolUseID] {
			seen[result.ToolUseID] = true
			unique = append(unique, result)
		}
	}

	toolResults := make([]ToolResult, 0, len(unique))
	for _, tr := range unique {
		status := "success"
		if tr.IsError {
			status = "error"
		}
		toolResults = append(toolResults, ToolResult{
			ToolUseID: tr.ToolUseID,
			Content:   []map[string]any{{"text": tr.Content}},
			Status:    status,
		})
	}
	return toolResults
}

// buildAssistantHistoryEntry builds an assistant history entry
func buildAssistantHistoryEntry(ctx *TransformContext, msg *UnifiedMessage) *HistoryEntry {
	text := msg.GetText()

	// Empty content uses "I understand."
	if text == "" && !msg.HasToolUses() {
		text = "I understand."
	}

	assistantMsg := &HistoryAssistantMessage{
		Content: text,
	}

	// Add tool_uses
	if msg.HasToolUses() {
		toolUses := make([]ToolUseEntry, 0, len(msg.ToolUses))
		for _, tu := range msg.ToolUses {
			ctx.RegisterToolUse(tu.ID, tu.Name)
			toolUses = append(toolUses, ToolUseEntry{
				ToolUseID: tu.ID,
				Name:      tu.Name,
				Input:     tu.Input,
			})
		}
		assistantMsg.ToolUses = toolUses
	}

	return &HistoryEntry{
		MessageID: fmt.Sprintf("msg-%03d", ctx.NextMsgID()),
		Type:      "assistant",
		Assistant: assistantMsg,
	}
}

// cleanOrphanToolUses removes tool_uses without corresponding tool_results
func cleanOrphanToolUses(history []HistoryEntry, currentMsg *UnifiedMessage) []HistoryEntry {
	if len(history) == 0 || currentMsg == nil {
		return history
	}

	lastMsg := &history[len(history)-1]
	if lastMsg.Type != "assistant" || lastMsg.Assistant == nil {
		return history
	}

	if len(lastMsg.Assistant.ToolUses) == 0 {
		return history
	}

	if currentMsg.HasToolResults() {
		return history
	}

	// No tool_result, clear toolUses
	lastMsg.Assistant.ToolUses = nil
	return history
}

// fixHistoryAlternation ensures proper user/assistant alternation
func fixHistoryAlternation(ctx *TransformContext, history []HistoryEntry) []HistoryEntry {
	if len(history) == 0 {
		return history
	}

	fixed := make([]HistoryEntry, 0, len(history)*2)
	var lastRole string

	for _, entry := range history {
		currentRole := entry.Type

		if lastRole != "" && lastRole == currentRole {
			if currentRole == "user" {
				// Insert "I understand." assistant message
				fixed = append(fixed, HistoryEntry{
					MessageID: fmt.Sprintf("msg-%03d", ctx.NextMsgID()),
					Type:      "assistant",
					Assistant: &HistoryAssistantMessage{
						Content: "I understand.",
					},
				})
			} else if currentRole == "assistant" {
				// Insert "Continue" user message
				fixed = append(fixed, HistoryEntry{
					MessageID: fmt.Sprintf("msg-%03d", ctx.NextMsgID()),
					Type:      "user",
					User: &HistoryUserMessage{
						Content: "Continue",
						ModelID: ctx.ModelID,
						Origin:  "AI_EDITOR",
					},
				})
			}
		}

		fixed = append(fixed, entry)
		lastRole = currentRole
	}

	// Ensure history ends with assistant
	if len(fixed) > 0 {
		lastEntry := fixed[len(fixed)-1]
		if lastEntry.Type == "user" {
			fixed = append(fixed, HistoryEntry{
				MessageID: fmt.Sprintf("msg-%03d", ctx.NextMsgID()),
				Type:      "assistant",
				Assistant: &HistoryAssistantMessage{
					Content: "I understand.",
				},
			})
		}
	}

	return fixed
}

// buildCurrentMessage builds the current message
func buildCurrentMessage(ctx *TransformContext, msg *UnifiedMessage, tools []ToolItem, claudeReq *ClaudeRequest, injectSystemPrompt bool) CurrentMessage {
	content := msg.GetText()

	// Inject system prompt if no history
	if injectSystemPrompt && claudeReq != nil {
		systemPrompt := buildSystemPrompt(claudeReq.System, claudeReq)
		if systemPrompt != "" {
			wrappedSystemPrompt := fmt.Sprintf("--- SYSTEM PROMPT BEGIN ---\n%s\n--- SYSTEM PROMPT END ---", systemPrompt)
			if content != "" {
				content = wrappedSystemPrompt + "\n\n" + content
			} else {
				content = wrappedSystemPrompt
			}
		}
	}

	if content == "" {
		content = "Continue"
	}

	userInputMsg := UserInputMessage{
		Content: content,
		ModelID: ctx.ModelID,
		Origin:  "AI_EDITOR",
	}

	// Add images
	if len(msg.Images) > 0 {
		userInputMsg.Images = msg.Images
	}

	// Build context
	var msgContext *UserInputMessageContext

	// Add tools
	if len(tools) > 0 {
		msgContext = &UserInputMessageContext{
			Tools: tools,
		}
	}

	// Add tool results
	if msg.HasToolResults() {
		toolResults := buildToolResults(msg.ToolResults)
		if len(toolResults) > 0 {
			if msgContext == nil {
				msgContext = &UserInputMessageContext{}
			}
			msgContext.ToolResults = toolResults
			if content == "" || content == "Continue" {
				userInputMsg.Content = "Tool results provided."
			}
		}
	}

	if msgContext != nil {
		userInputMsg.UserInputMessageContext = msgContext
	}

	return CurrentMessage{
		UserInputMessage: userInputMsg,
	}
}

// buildContinueMessage builds a "Continue" message
func buildContinueMessage(ctx *TransformContext, tools []ToolItem) CurrentMessage {
	userInputMsg := UserInputMessage{
		Content: "Continue",
		ModelID: ctx.ModelID,
		Origin:  "AI_EDITOR",
	}

	if len(tools) > 0 {
		userInputMsg.UserInputMessageContext = &UserInputMessageContext{
			Tools: tools,
		}
	}

	return CurrentMessage{
		UserInputMessage: userInputMsg,
	}
}

// processTools processes tools, truncating long descriptions
func processTools(tools []ClaudeTool) []ToolItem {
	if len(tools) == 0 {
		return nil
	}

	var cwTools []ToolItem
	functionCount := 0

	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}

		// Check for web_search tool
		if isWebSearchTool(tool.Name) {
			cwTools = append(cwTools, ToolItem{
				WebSearch: &WebSearchTool{
					Type: "web_search",
				},
			})
			continue
		}

		// Limit to max function tools
		if functionCount >= MaxFunctionTools {
			continue
		}
		functionCount++

		description := tool.Description
		// Truncate long descriptions
		if len(description) > ToolDescriptionMaxLength {
			runes := []rune(description)
			if len(runes) > ToolDescriptionMaxLength-3 {
				description = string(runes[:ToolDescriptionMaxLength-3]) + "..."
			}
		}

		inputSchema := tool.InputSchema
		if inputSchema == nil || inputSchema["type"] == nil {
			inputSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		cwTools = append(cwTools, ToolItem{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        tool.Name,
					Description: description,
					InputSchema: InputSchema{
						JSON: inputSchema,
					},
				},
			},
		})
	}

	return cwTools
}

// isWebSearchTool checks if the tool is a web_search tool
func isWebSearchTool(name string) bool {
	return name == "web_search" ||
		name == "web_search_20250305" ||
		strings.HasPrefix(name, "web_search_")
}

// determineChatTriggerType determines the chat trigger type
func determineChatTriggerType(claudeReq *ClaudeRequest) string {
	if claudeReq == nil || len(claudeReq.Tools) == 0 {
		return "MANUAL"
	}

	if tc, ok := claudeReq.ToolChoice.(map[string]any); ok {
		if tcType, _ := tc["type"].(string); tcType == "any" || tcType == "tool" {
			return "AUTO"
		}
	}
	return "MANUAL"
}

// ParseClaudeRequestFromJSON parses a Claude request from JSON bytes
func ParseClaudeRequestFromJSON(data []byte) (*ClaudeRequest, error) {
	var req ClaudeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse claude request failed: %w", err)
	}
	return &req, nil
}
