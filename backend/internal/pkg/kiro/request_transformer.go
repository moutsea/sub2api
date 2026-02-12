// Package kiro provides request transformation from Claude to CodeWhisperer format.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Constants for tool processing
const (
	MaxFunctionTools              = 50  // Max number of function tools
	ToolDocThresholdLength        = 500 // Threshold for moving description to system prompt
)

// generateParameterHints extracts parameter requirements from input_schema and generates hints
// This helps the model understand what parameters are required/optional for each tool
func generateParameterHints(inputSchema map[string]any) string {
	if inputSchema == nil {
		return ""
	}

	properties, ok := inputSchema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return ""
	}

	// Get required parameters
	var requiredParams []string
	if required, ok := inputSchema["required"].([]any); ok {
		for _, r := range required {
			if s, ok := r.(string); ok {
				requiredParams = append(requiredParams, s)
			}
		}
	}
	// Also handle []string type
	if required, ok := inputSchema["required"].([]string); ok {
		requiredParams = required
	}

	requiredSet := make(map[string]bool)
	for _, p := range requiredParams {
		requiredSet[p] = true
	}

	var requiredHints []string
	var optionalHints []string

	for name, prop := range properties {
		propMap, ok := prop.(map[string]any)
		if !ok {
			continue
		}

		// Get type
		propType := "any"
		if t, ok := propMap["type"].(string); ok {
			propType = t
		}

		hint := name + " (" + propType + ")"

		if requiredSet[name] {
			requiredHints = append(requiredHints, hint)
		} else {
			optionalHints = append(optionalHints, hint)
		}
	}

	var parts []string
	if len(requiredHints) > 0 {
		parts = append(parts, "[Required: "+strings.Join(requiredHints, ", ")+"]")
	}
	if len(optionalHints) > 0 {
		parts = append(parts, "[Optional: "+strings.Join(optionalHints, ", ")+"]")
	}

	if len(parts) == 0 {
		return ""
	}

	return "\n" + strings.Join(parts, " ")
}

// TransformContext holds context for the transformation process
type TransformContext struct {
	ModelID      string            // CodeWhisperer model ID
	ConvID       string            // Conversation ID
	AgentContID  string            // Agent continuation ID
	ToolUseIDMap map[string]string // tool_use_id -> tool_name mapping
	MsgCounter   int               // Message counter
	IsOpus46     bool              // Whether the model is opus-4-6 (skip truncation/compression)
}

// NewTransformContext creates a new transformation context
// If ginCtx is provided, generates stable conversation IDs based on client characteristics
// for better cache hit rates on AWS CodeWhisperer.
func NewTransformContext(model string, ginCtx *gin.Context) *TransformContext {
	modelID := GetModelID(model)

	var convID, agentContID string
	if ginCtx != nil {
		convID = GenerateStableConversationID(ginCtx)
		agentContID = GenerateStableAgentContinuationID(ginCtx)
	} else {
		convID = uuid.New().String()
		agentContID = uuid.New().String()
	}

	isOpus46 := strings.Contains(model, "opus-4-6") || strings.Contains(model, "opus-4.6")

	return &TransformContext{
		ModelID:      modelID,
		ConvID:       convID,
		AgentContID:  agentContID,
		ToolUseIDMap: make(map[string]string),
		MsgCounter:   0,
		IsOpus46:     isOpus46,
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
// ginCtx is optional - if provided, enables stable conversation ID generation for better caching
func TransformClaudeToCodeWhisperer(claudeReq *ClaudeRequest, profileArn string, ginCtx *gin.Context) (*CodeWhispererRequest, error) {
	if claudeReq == nil {
		return nil, fmt.Errorf("claude request is nil")
	}

	if len(claudeReq.Messages) == 0 {
		return nil, fmt.Errorf("messages cannot be empty")
	}

	// Create transformation context with stable IDs if ginCtx is provided
	ctx := NewTransformContext(claudeReq.Model, ginCtx)
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

	// Process tools (truncate long descriptions, skip limits for opus-4-6)
	processedTools := processTools(claudeReq.Tools, ctx.IsOpus46)

	// If last message is assistant, it becomes part of history
	if currentMsg.Role == "assistant" {
		historyMsgs = append(historyMsgs, currentMsg)
	}

	// Build history first (needed to check for placeholder tools)
	history := buildHistory(ctx, historyMsgs, claudeReq.System, currentMsg, claudeReq)

	// Ensure all tools referenced in history are defined in the tools list.
	// AWSQ API requires every tool_use name in history to have a matching tool definition,
	// otherwise returns 400 "Improperly formed request".
	// Aligned with kiro.rs create_placeholder_tool logic.
	// Must be done BEFORE building currentMessage, since tools are embedded in it.
	processedTools = ensureHistoryToolsDefined(history, processedTools)

	// Build currentMessage
	var currentMessage CurrentMessage
	var hasHistory bool

	if currentMsg.Role == "assistant" {
		// Last message was assistant (already added to history above), create "Continue" message
		currentMessage = buildContinueMessage(ctx, processedTools)
		hasHistory = true
	} else {
		hasHistory = len(historyMsgs) > 0
		currentMessage = buildCurrentMessage(ctx, currentMsg, processedTools, claudeReq, !hasHistory)
	}

	// Assemble final request
	cwReq := &CodeWhispererRequest{
		ConversationState: ConversationState{
			AgentTaskType:   "vibe",
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
		content, images := extractToolResultContent(blockMap["content"])
		um.ToolResults = append(um.ToolResults, ToolResultData{
			ToolUseID: toolUseID,
			Content:   content,
			Images:    images,
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
		if block.Source != nil && block.Source.Data != "" {
			um.Images = append(um.Images, CodeWhispererImage{
				Format: MediaTypeToFormat(block.Source.MediaType),
				Source: ImageSource{Bytes: block.Source.Data},
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
		var content string
		var images []CodeWhispererImage
		if block.Content != nil {
			content, images = extractToolResultContent(block.Content)
		}
		um.ToolResults = append(um.ToolResults, ToolResultData{
			ToolUseID: toolUseID,
			Content:   content,
			Images:    images,
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

// extractToolResultContent extracts text content and images from tool_result
func extractToolResultContent(content any) (string, []CodeWhispererImage) {
	var images []CodeWhispererImage

	switch c := content.(type) {
	case string:
		return c, nil

	case []any:
		var parts []string
		for _, item := range c {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}

			blockType, _ := itemMap["type"].(string)
			switch blockType {
			case "text":
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			case "image":
				if source, ok := itemMap["source"].(map[string]any); ok {
					mediaType, _ := source["media_type"].(string)
					data, _ := source["data"].(string)
					if data != "" {
						images = append(images, CodeWhispererImage{
							Format: MediaTypeToFormat(mediaType),
							Source: ImageSource{Bytes: data},
						})
					}
				}
			}
		}
		return strings.Join(parts, "\n"), images

	case map[string]any:
		if text, ok := c["text"].(string); ok {
			return text, nil
		}
	}

	if content != nil {
		return fmt.Sprintf("%v", content), nil
	}
	return "", nil
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

// MaxThinkingBudgetTokens is the maximum allowed budget_tokens for thinking mode.
// Reference: kiro.rs types.rs MAX_BUDGET_TOKENS = 24576
const MaxThinkingBudgetTokens = 24576

// DefaultThinkingBudgetTokens is the default budget_tokens when not specified.
const DefaultThinkingBudgetTokens = 20000

// generateThinkingPrefix generates the thinking mode XML prefix for AWSQ.
// Reference: kiro.rs converter.rs generate_thinking_prefix
func generateThinkingPrefix(claudeReq *ClaudeRequest) string {
	if claudeReq == nil || claudeReq.Thinking == nil {
		return ""
	}

	thinkingType, _ := claudeReq.Thinking["type"].(string)
	if thinkingType == "" {
		return ""
	}

	switch thinkingType {
	case "enabled":
		budgetTokens := DefaultThinkingBudgetTokens
		if bt, ok := claudeReq.Thinking["budget_tokens"].(float64); ok && bt > 0 {
			budgetTokens = int(bt)
		}
		if budgetTokens > MaxThinkingBudgetTokens {
			budgetTokens = MaxThinkingBudgetTokens
		}
		return fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", budgetTokens)

	case "adaptive":
		effort := "high"
		// Check for thinking_effort in the thinking config itself
		if e, ok := claudeReq.Thinking["thinking_effort"].(string); ok && e != "" {
			effort = e
		}
		return fmt.Sprintf("<thinking_mode>adaptive</thinking_mode><thinking_effort>%s</thinking_effort>", effort)
	}

	return ""
}

// hasThinkingTags checks if content already contains thinking mode tags.
func hasThinkingTags(content string) bool {
	return strings.Contains(content, "<thinking_mode>") || strings.Contains(content, "<max_thinking_length>")
}

// buildHistory builds the history entries
func buildHistory(ctx *TransformContext, messages []*UnifiedMessage, systemRaw any, currentMsg *UnifiedMessage, claudeReq *ClaudeRequest) []HistoryEntry {
	// Build system prompt
	systemPrompt := buildSystemPrompt(systemRaw, claudeReq)

	// Generate thinking prefix
	thinkingPrefix := generateThinkingPrefix(claudeReq)

	// Inject thinking prefix into system prompt
	if thinkingPrefix != "" && !hasThinkingTags(systemPrompt) {
		if systemPrompt != "" {
			systemPrompt = thinkingPrefix + "\n" + systemPrompt
		} else {
			systemPrompt = thinkingPrefix
		}
	}

	if len(messages) == 0 {
		// No history messages, but we may need to inject thinking prefix
		// as a standalone user+assistant pair if there's a thinking config
		if systemPrompt != "" && thinkingPrefix != "" {
			// This case is handled by buildCurrentMessage's injectSystemPrompt path
			// when there's no history. The thinking prefix is already in systemPrompt.
		}
		return nil
	}

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
		if len(tool.Description) > ToolDocThresholdLength {
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

		// Build content array with text and images
		var content []map[string]any
		if tr.Content != "" {
			content = append(content, map[string]any{"text": tr.Content})
		}
		for _, img := range tr.Images {
			content = append(content, map[string]any{
				"image": map[string]any{
					"format": img.Format,
					"source": map[string]any{
						"bytes": img.Source.Bytes,
					},
				},
			})
		}
		// Ensure at least one content item
		if len(content) == 0 {
			content = append(content, map[string]any{"text": ""})
		}

		toolResults = append(toolResults, ToolResult{
			ToolUseID: tr.ToolUseID,
			Content:   content,
			Status:    status,
		})
	}
	return toolResults
}

// buildAssistantHistoryEntry builds an assistant history entry
func buildAssistantHistoryEntry(ctx *TransformContext, msg *UnifiedMessage) *HistoryEntry {
	text := msg.GetText()

	// AWSQ requires non-empty content for all messages
	if text == "" {
		text = " "
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

// cleanOrphanToolUses validates tool_use/tool_result pairing across all history entries
// and removes orphaned tool_uses that have no corresponding tool_result.
// AWSQ API requires every tool_use to have a matching tool_result, otherwise returns 400.
// Aligned with kiro.rs validate_tool_pairing + remove_orphaned_tool_uses.
func cleanOrphanToolUses(history []HistoryEntry, currentMsg *UnifiedMessage) []HistoryEntry {
	if len(history) == 0 {
		return history
	}

	// 1. Collect all tool_use_ids from assistant messages in history
	allToolUseIDs := make(map[string]bool)
	for _, entry := range history {
		if entry.Type == "assistant" && entry.Assistant != nil {
			for _, tu := range entry.Assistant.ToolUses {
				allToolUseIDs[tu.ToolUseID] = true
			}
		}
	}

	if len(allToolUseIDs) == 0 {
		return history
	}

	// 2. Collect all tool_result tool_use_ids from user messages in history
	pairedIDs := make(map[string]bool)
	for _, entry := range history {
		if entry.Type == "user" && entry.User != nil && entry.User.UserInputMessageContext != nil {
			for _, tr := range entry.User.UserInputMessageContext.ToolResults {
				pairedIDs[tr.ToolUseID] = true
			}
		}
	}

	// 3. Also count tool_results from currentMsg
	if currentMsg != nil {
		for _, tr := range currentMsg.ToolResults {
			pairedIDs[tr.ToolUseID] = true
		}
	}

	// 4. Find orphaned tool_use_ids (have tool_use but no tool_result anywhere)
	orphanedIDs := make(map[string]bool)
	for id := range allToolUseIDs {
		if !pairedIDs[id] {
			orphanedIDs[id] = true
		}
	}

	if len(orphanedIDs) == 0 {
		return history
	}

	// 5. Remove orphaned tool_uses from all assistant messages
	for i := range history {
		entry := &history[i]
		if entry.Type != "assistant" || entry.Assistant == nil || len(entry.Assistant.ToolUses) == 0 {
			continue
		}

		filtered := make([]ToolUseEntry, 0, len(entry.Assistant.ToolUses))
		for _, tu := range entry.Assistant.ToolUses {
			if !orphanedIDs[tu.ToolUseID] {
				filtered = append(filtered, tu)
			}
		}

		if len(filtered) == 0 {
			entry.Assistant.ToolUses = nil
			// Backfill empty content to avoid sending {"content":""} without tool_uses
			if entry.Assistant.Content == "" {
				entry.Assistant.Content = "I understand."
			}
		} else {
			entry.Assistant.ToolUses = filtered
		}
	}

	return history
}

// ensureHistoryToolsDefined checks that all tool names referenced in history tool_uses
// have a corresponding definition in the tools list. If not, creates a placeholder tool.
// AWSQ API requires this, otherwise returns 400 "Improperly formed request".
// Aligned with kiro.rs create_placeholder_tool.
func ensureHistoryToolsDefined(history []HistoryEntry, tools []ToolItem) []ToolItem {
	if len(history) == 0 {
		return tools
	}

	// Collect existing tool names (case-insensitive)
	existingNames := make(map[string]bool)
	for _, t := range tools {
		if t.Standard != nil {
			existingNames[strings.ToLower(t.Standard.ToolSpecification.Name)] = true
		}
	}

	// Collect tool names from history tool_uses
	seen := make(map[string]bool)
	for _, entry := range history {
		if entry.Type == "assistant" && entry.Assistant != nil {
			for _, tu := range entry.Assistant.ToolUses {
				nameLower := strings.ToLower(tu.Name)
				if !existingNames[nameLower] && !seen[nameLower] {
					seen[nameLower] = true
					tools = append(tools, ToolItem{
						Standard: &CodeWhispererTool{
							ToolSpecification: ToolSpecification{
								Name:        tu.Name,
								Description: "Tool used in conversation history",
								InputSchema: InputSchema{
									JSON: map[string]any{
										"type":       "object",
										"properties": map[string]any{},
									},
								},
							},
						},
					})
				}
			}
		}
	}

	return tools
}

// fixHistoryAlternation ensures proper user/assistant alternation
// AWSQ requires: history starts with user, ends with assistant, strict alternation
func fixHistoryAlternation(ctx *TransformContext, history []HistoryEntry) []HistoryEntry {
	if len(history) == 0 {
		return history
	}

	fixed := make([]HistoryEntry, 0, len(history)*2)

	// Ensure history starts with user message
	if history[0].Type == "assistant" {
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

	var lastRole string
	if len(fixed) > 0 {
		lastRole = fixed[len(fixed)-1].Type
	}

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

		// Inject thinking prefix into system prompt
		thinkingPrefix := generateThinkingPrefix(claudeReq)
		if thinkingPrefix != "" && !hasThinkingTags(systemPrompt) {
			if systemPrompt != "" {
				systemPrompt = thinkingPrefix + "\n" + systemPrompt
			} else {
				systemPrompt = thinkingPrefix
			}
		}

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

// processTools processes tools, truncating long descriptions and applying compression if needed.
// When skipLimits is true (opus-4-6), dynamic compression is bypassed.
// Tool count limit (50) and name length limit (64 chars) are always enforced as AWSQ hard constraints.
func processTools(tools []ClaudeTool, skipLimits bool) []ToolItem {
	if len(tools) == 0 {
		return nil
	}

	// Build short name map for all tools first
	var toolNames []string
	for _, tool := range tools {
		if tool.Name != "" && !isWebSearchTool(tool.Name) && !isWebSearchToolByType(tool) {
			toolNames = append(toolNames, tool.Name)
		}
	}
	shortNameMap := BuildToolNameMap(toolNames)

	var cwTools []ToolItem
	functionCount := 0

	for _, tool := range tools {
		// Convert Claude's built-in web_search to standard toolSpecification format
		// Claude's built-in web_search has Type field like "web_search_20250305"
		// This allows the model to call WebSearch, which will be intercepted by the agentic loop
		if isWebSearchToolByType(tool) {
			cwTools = append(cwTools, ToolItem{
				Standard: &CodeWhispererTool{
					ToolSpecification: ToolSpecification{
						Name:        "WebSearch",
						Description: "Search the web for information. Use this tool when you need to find current information, facts, or data from the internet.",
						InputSchema: InputSchema{
							JSON: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"query": map[string]any{
										"type":        "string",
										"description": "The search query to look up on the web",
									},
								},
								"required": []string{"query"},
							},
						},
					},
				},
			})
			functionCount++
			continue
		}

		// Skip tools without a name
		if tool.Name == "" {
			continue
		}

		// Limit to max function tools (AWSQ hard limit, always enforced)
		if functionCount >= MaxFunctionTools {
			continue
		}
		functionCount++

		description := tool.Description

		if isWriteOrEditTool(tool.Name) {
			description += "\n\n<constraint>Content per operation MUST NOT exceed 400 lines or 12000 characters. For larger content, split into multiple operations.</constraint>"
			description += "\n<instruction>ALWAYS use Write/Edit tools for file modifications. Ensure all required parameters are provided correctly.</instruction>"
		}

		inputSchema := tool.InputSchema
		if inputSchema == nil || inputSchema["type"] == nil {
			inputSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		// Generate parameter hints from schema and append to description
		// This helps the model understand required/optional parameters
		paramHints := generateParameterHints(inputSchema)
		if paramHints != "" {
			description = description + paramHints
		}

		// Truncate individual tool descriptions that exceed Kiro API limit
		// (after adding parameter hints) — enforced by upstream kiro.rs (10000 chars)
		if len(description) > KiroMaxToolDescLen {
			runes := []rune(description)
			if len(runes) > KiroMaxToolDescLen-3 {
				description = string(runes[:KiroMaxToolDescLen-3]) + "..."
			}
		}

		// Apply shortened name (AWSQ has 64-char tool name limit, always enforced)
		toolName := tool.Name
		if short, ok := shortNameMap[tool.Name]; ok {
			toolName = short
		}

		cwTools = append(cwTools, ToolItem{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        toolName,
					Description: description,
					InputSchema: InputSchema{
						JSON: inputSchema,
					},
				},
			},
		})
	}

	// Apply dynamic compression if total tools size exceeds threshold
	// This prevents 500 errors when Claude Code sends too many tools
	// Skip for opus-4-6 which supports larger context
	if !skipLimits {
		cwTools = compressToolsIfNeeded(cwTools, true)
	}

	return cwTools
}

// isWebSearchToolByType checks if the tool is a web_search tool by its type field
// Claude web_search tools have a top-level "type" field like "web_search_20250305"
// Aligned with kiro4api: check exact matches first, then prefix
func isWebSearchToolByType(tool ClaudeTool) bool {
	if tool.Type == "" {
		return false
	}
	return tool.Type == "web_search" ||
		tool.Type == "web_search_20250305" ||
		strings.HasPrefix(tool.Type, "web_search_")
}

// isWriteOrEditTool checks if the tool is Write or Edit tool
func isWriteOrEditTool(name string) bool {
	return name == "Write" || name == "Edit"
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
