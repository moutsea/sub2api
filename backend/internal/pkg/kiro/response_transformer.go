// Package kiro provides response transformation from CodeWhisperer to Claude format.
package kiro

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Thinking tag constants for parsing thinking blocks from content
const (
	ThinkingStartTag = "<thinking>"
	ThinkingEndTag   = "</thinking>"
)

// awsPayload represents the JSON payload from CodeWhisperer stream
type awsPayload struct {
	Content              *string         `json:"content"`
	FollowupPrompt       json.RawMessage `json:"followupPrompt"`
	ToolUseID            *string         `json:"toolUseId"`
	Name                 *string         `json:"name"`
	Input                *string         `json:"input"`
	Stop                 *bool           `json:"stop"`
	Usage                *float64        `json:"usage"`
	ContextUsagePercent  *float64        `json:"contextUsagePercentage"`
	MeteringEvent        map[string]any  `json:"meteringEvent"`
	ContextUsageEvent    map[string]any  `json:"contextUsageEvent"`
	MessageMetadataEvent map[string]any  `json:"messageMetadataEvent"`
	MetadataEvent        map[string]any  `json:"metadataEvent"`
	TokenUsage           map[string]any  `json:"tokenUsage"`
	Message              *string         `json:"message"`
	ExceptionType        *string         `json:"__type"`
}

// toolAccumulator tracks state for a tool use block
type toolAccumulator struct {
	name        string
	blockIndex  uint32
	started     bool
	inputBuffer strings.Builder // accumulates complete tool input JSON for truncation detection
}

// AwsEventStreamParser parses CodeWhisperer AWS EventStream binary format
// and outputs unified StreamEvents.
//
// Design goals:
// - Independent of JSON field order
// - Tool calls bound by toolUseId (supports concurrent/interleaved)
// - Clear memory limits to prevent buffer overflow
// - Aligned with proxycast Kiro parsing semantics
// - TAG-BASED THINKING PARSING: Parse <thinking> tags from content
type AwsEventStreamParser struct {
	messageID string
	model     string

	buffer        []byte
	maxBufferSize int

	messageStarted bool
	messageStopped bool

	nextBlockIndex uint32
	inTextBlock    bool
	textBlockIndex *uint32

	toolAccumulators map[string]*toolAccumulator
	sawToolUse       bool
	sawText          bool
	sawThinking      bool

	// Thinking tag parsing state
	thinkingEnabled             bool
	inThinkingBlock             bool
	thinkingExtracted           bool // Once thinking is extracted, all subsequent content is text
	thinkingBlockIndex          *uint32
	stripThinkingLeadingNewline bool
	pendingContent              strings.Builder // Buffer for partial tag matching

	parseErrorCount int
	terminalError   bool
}

const defaultMaxBufferSize = 16 * 1024 * 1024

// NewAwsEventStreamParser creates a new parser
func NewAwsEventStreamParser(messageID, model string) *AwsEventStreamParser {
	return &AwsEventStreamParser{
		messageID:        messageID,
		model:            model,
		thinkingEnabled:  IsThinkingModelName(model),
		maxBufferSize:    defaultMaxBufferSize,
		toolAccumulators: make(map[string]*toolAccumulator),
	}
}

// SetThinkingEnabled tells the parser whether this upstream request was made
// in Kiro thinking mode. It must be called before the first Process call.
// When disabled, content is forwarded as plain text and <thinking> tags are
// not parsed into Claude thinking blocks.
func (p *AwsEventStreamParser) SetThinkingEnabled(enabled bool) {
	if len(p.buffer) > 0 || p.messageStarted || p.pendingContent.Len() > 0 || p.inThinkingBlock || p.thinkingExtracted || p.sawThinking {
		log.Printf("[kiro-parser] ignored SetThinkingEnabled(%v) after parsing started", enabled)
		return
	}
	p.thinkingEnabled = enabled
}

// SetMaxBufferSize sets the maximum buffer size
func (p *AwsEventStreamParser) SetMaxBufferSize(n int) {
	if n > 0 {
		p.maxBufferSize = n
	}
}

// ParseErrorCount returns the number of parse errors
func (p *AwsEventStreamParser) ParseErrorCount() int {
	return p.parseErrorCount
}

// SawToolUse returns whether any tool use was seen
func (p *AwsEventStreamParser) SawToolUse() bool {
	return p.sawToolUse
}

func (p *AwsEventStreamParser) MessageStopped() bool {
	return p.messageStopped
}

func (p *AwsEventStreamParser) nextIndex() uint32 {
	idx := p.nextBlockIndex
	p.nextBlockIndex++
	return idx
}

// Process processes a chunk of data and returns stream events
func (p *AwsEventStreamParser) Process(chunk []byte) []StreamEvent {
	if len(chunk) == 0 || p.terminalError {
		return nil
	}

	var events []StreamEvent
	for len(chunk) > 0 {
		available := p.maxBufferSize - len(p.buffer)
		if available <= 0 {
			return append(events, p.failEventStream("buffer_overflow", fmt.Errorf("buffer overflow"))...)
		}

		appendLength := len(chunk)
		if appendLength > available {
			appendLength = available
		}
		p.buffer = append(p.buffer, chunk[:appendLength]...)
		chunk = chunk[appendLength:]

		bufferedBeforeParse := len(p.buffer)
		events = append(events, p.parseBuffer()...)
		if p.terminalError {
			return events
		}
		if len(p.buffer) == bufferedBeforeParse && len(p.buffer) >= p.maxBufferSize {
			return append(events, p.failEventStream("buffer_overflow", fmt.Errorf("buffer overflow"))...)
		}
	}
	return events
}

// Finish finalizes parsing and returns any remaining events
func (p *AwsEventStreamParser) Finish() []StreamEvent {
	if p.terminalError {
		return nil
	}

	var events []StreamEvent

	events = append(events, p.parseBuffer()...)
	if p.terminalError {
		return events
	}
	if len(p.buffer) > 0 {
		return append(events, p.failEventStream(
			"event_stream_decode_error",
			fmt.Errorf("truncated event stream frame: %d buffered bytes", len(p.buffer)),
		)...)
	}

	if p.messageStopped {
		p.buffer = nil
		p.toolAccumulators = make(map[string]*toolAccumulator)
		p.textBlockIndex = nil
		p.inTextBlock = false
		p.thinkingBlockIndex = nil
		p.inThinkingBlock = false
		p.thinkingExtracted = false
		p.stripThinkingLeadingNewline = false
		p.pendingContent.Reset()
		return events
	}

	// Close all unclosed tool blocks
	for toolID, acc := range p.toolAccumulators {
		if acc.started {
			accumulated := acc.inputBuffer.String()
			if shouldReportToolInputTruncation(acc.name, accumulated) {
				log.Printf("[kiro-truncation] stream-finish tool=%s TRUNCATED", acc.name)
			}
			events = append(events,
				StreamEvent{Type: EventToolUseStop, ToolID: toolID},
				StreamEvent{Type: EventContentBlockStop, Index: acc.blockIndex},
			)
		}
	}
	p.toolAccumulators = make(map[string]*toolAccumulator)

	// Flush pending thinking content at stream end.
	// Aligned with kiro.rs generate_final_events: handle residual thinking buffer.
	pendingStr := p.pendingContent.String()
	p.pendingContent.Reset()

	if p.inThinkingBlock {
		// Check if buffer ends with </thinking> (boundary case: no \n\n at stream end)
		combined := pendingStr
		combined = p.stripThinkingLeadingNewlineIfNeeded(combined)
		if endPos := findRealThinkingEndTagAtBufferEnd(combined); endPos >= 0 {
			// Emit thinking content before the tag
			thinkingContent := combined[:endPos]
			if thinkingContent != "" {
				if p.thinkingBlockIndex == nil {
					idx := p.nextIndex()
					p.thinkingBlockIndex = &idx
					events = append(events, StreamEvent{
						Type: EventContentBlockStart, Index: idx,
						BlockType: ContentBlockType{Kind: BlockThinking},
					})
				}
				events = append(events, StreamEvent{Type: EventThinkingDelta, Text: thinkingContent})
			}
			// Close thinking block
			if p.thinkingBlockIndex != nil {
				idx := *p.thinkingBlockIndex
				events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
				p.thinkingBlockIndex = nil
			}
			p.inThinkingBlock = false
			p.thinkingExtracted = true
			p.stripThinkingLeadingNewline = false
			// Remaining after tag as text
			afterPos := endPos + len(ThinkingEndTag)
			remaining := strings.TrimLeft(combined[afterPos:], " \t\n\r")
			if remaining != "" {
				events = append(events, p.emitTextDelta(remaining)...)
			}
		} else {
			// No end tag found — emit remaining as thinking delta
			if combined != "" {
				if p.thinkingBlockIndex == nil {
					idx := p.nextIndex()
					p.thinkingBlockIndex = &idx
					events = append(events, StreamEvent{
						Type: EventContentBlockStart, Index: idx,
						BlockType: ContentBlockType{Kind: BlockThinking},
					})
				}
				events = append(events, StreamEvent{Type: EventThinkingDelta, Text: combined})
			}
		}
	} else if pendingStr != "" {
		// Not in thinking block — emit pending as text.
		// Aligned with kiro.rs generate_final_events: if no real <thinking>
		// tag was seen, buffered content is a normal answer, not hidden thinking.
		events = append(events, p.emitTextDelta(pendingStr)...)
	}

	// Close thinking block if still open
	if p.thinkingBlockIndex != nil {
		idx := *p.thinkingBlockIndex
		events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
		p.thinkingBlockIndex = nil
		p.inThinkingBlock = false
		p.stripThinkingLeadingNewline = false
	}

	// Close text block
	if p.textBlockIndex != nil {
		idx := *p.textBlockIndex
		events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
		p.textBlockIndex = nil
		p.inTextBlock = false
	}

	if p.messageStarted {
		stopReason := StopReasonEndTurn
		if p.sawToolUse {
			stopReason = StopReasonToolUse
		} else if p.sawThinking && !p.sawText {
			// Thinking-only: model spent entire token budget on thinking.
			stopReason = StopReasonMaxTokens

			// Aligned with kiro.rs: emit a space text block so that the response
			// content array contains at least one text block. Some clients (e.g.
			// Claude Code) cannot handle responses with only thinking blocks.
			idx := p.nextIndex()
			events = append(events,
				StreamEvent{
					Type:  EventContentBlockStart,
					Index: idx,
					BlockType: ContentBlockType{
						Kind: BlockText,
					},
				},
				StreamEvent{Type: EventTextDelta, Text: " "},
				StreamEvent{Type: EventContentBlockStop, Index: idx},
			)
		}
		events = append(events, StreamEvent{Type: EventMessageStop, StopReason: stopReason})
		p.messageStopped = true
	}

	p.buffer = nil
	p.pendingContent.Reset()
	p.stripThinkingLeadingNewline = false
	return events
}

func contextUsageFromTokenUsage(tokenUsage map[string]any) float64 {
	if tokenUsage == nil {
		return 0
	}
	for _, k := range []string{"contextUsagePercentage", "contextUsagePercent", "usagePercent", "percent", "contextPercent"} {
		if v, ok := tokenUsage[k].(float64); ok && v > 0 {
			return v
		}
	}
	return 0
}

func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func tokenUsageIntFromAny(v any) (int, bool) {
	f, ok := asFloat64(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func firstIntByKeys(m map[string]any, keys ...string) int {
	if m == nil {
		return 0
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if v, ok := tokenUsageIntFromAny(m[key]); ok && v >= 0 {
			return v
		}
	}
	return 0
}

func extractTokenUsage(tokenUsage map[string]any) (inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, has bool) {
	if tokenUsage == nil {
		return 0, 0, 0, 0, false
	}

	inputTokens = firstIntByKeys(tokenUsage,
		"inputTokens", "input_tokens",
		"promptTokens", "prompt_tokens",
		"inputTokenCount", "input_token_count",
		"totalInputTokens", "total_input_tokens",
	)
	outputTokens = firstIntByKeys(tokenUsage,
		"outputTokens", "output_tokens",
		"completionTokens", "completion_tokens",
		"outputTokenCount", "output_token_count",
		"generatedTokens", "generated_tokens",
		"totalOutputTokens", "total_output_tokens",
	)
	cacheCreationTokens = firstIntByKeys(tokenUsage,
		"cacheCreationInputTokens", "cache_creation_input_tokens",
		"cacheWriteInputTokens", "cache_write_input_tokens",
	)
	cacheReadTokens = firstIntByKeys(tokenUsage,
		"cacheReadInputTokens", "cache_read_input_tokens",
		"cachedInputTokens", "cached_input_tokens",
		"cachedTokens", "cached_tokens",
	)

	// Handle nested token details formats (e.g. cached_tokens under details maps).
	if cacheReadTokens == 0 {
		for _, detailKey := range []string{"inputTokenDetails", "input_tokens_details", "promptTokenDetails", "prompt_tokens_details", "details"} {
			if details, ok := tokenUsage[detailKey].(map[string]any); ok && details != nil {
				cacheReadTokens = firstIntByKeys(details, "cached_tokens", "cachedTokens", "cache_read_input_tokens", "cacheReadInputTokens")
				if cacheReadTokens > 0 {
					break
				}
			}
		}
	}

	// Normalize to Claude usage semantics when upstream appears to report total input.
	if inputTokens > 0 && (cacheCreationTokens > 0 || cacheReadTokens > 0) {
		normalized := normalizeClaudeInputTokens(inputTokens, cacheCreationTokens, cacheReadTokens)
		if normalized > 0 || inputTokens >= cacheCreationTokens+cacheReadTokens {
			inputTokens = normalized
		}
	}

	has = inputTokens > 0 || outputTokens > 0 || cacheCreationTokens > 0 || cacheReadTokens > 0
	return inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens, has
}

func tokenUsageFromMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	if tokenUsage, ok := meta["tokenUsage"].(map[string]any); ok {
		return tokenUsage
	}
	return meta
}

func (p *AwsEventStreamParser) parseJSONEvent(eventType string, jsonBytes []byte) ([]StreamEvent, error) {
	switch eventType {
	case "assistantResponseEvent", "toolUseEvent", "meteringEvent", "contextUsageEvent", "messageMetadataEvent", "metadataEvent", "tokenUsageEvent":
	default:
		return nil, nil
	}

	var payload awsPayload
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		return nil, err
	}
	if eventType == "toolUseEvent" && payload.ToolUseID == nil {
		return nil, fmt.Errorf("toolUseEvent missing toolUseId")
	}

	var events []StreamEvent

	isRenderableEvent := eventType == "assistantResponseEvent" || eventType == "toolUseEvent"
	if isRenderableEvent && !p.messageStarted {
		p.messageStarted = true
		events = append(events, StreamEvent{Type: EventMessageStart, MessageID: p.messageID, Model: p.model})
	}

	// Only assistantResponseEvent payloads are allowed to become assistant text.
	if eventType == "assistantResponseEvent" {
		if payload.Content != nil {
			contentEvents := p.parseContentWithThinking(*payload.Content)
			events = append(events, contentEvents...)
		}
		return events, nil
	}

	// Tool input is accepted only from toolUseEvent frames.
	if eventType == "toolUseEvent" && payload.ToolUseID != nil {
		// Flush thinking buffer before tool_use (aligned with kiro.rs process_tool_use).
		// tool_use must happen after thinking ends. If </thinking> is at buffer end
		// without \n\n, flush it now.
		events = append(events, p.flushThinkingBeforeToolUse()...)

		// Close text block first if exists
		if p.textBlockIndex != nil {
			idx := *p.textBlockIndex
			p.textBlockIndex = nil
			p.inTextBlock = false
			events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
		}

		toolID := *payload.ToolUseID
		acc, exists := p.toolAccumulators[toolID]
		if !exists {
			acc = &toolAccumulator{}
			p.toolAccumulators[toolID] = acc
		}

		name := ""
		if payload.Name != nil {
			name = *payload.Name
		}
		if name == "" && acc.name != "" {
			name = acc.name
		}
		if name == "" {
			name = "unknown"
		}

		// Ensure start event is sent only once
		if !acc.started {
			acc.started = true
			acc.name = name
			acc.blockIndex = p.nextIndex()
			p.sawToolUse = true

			events = append(events,
				StreamEvent{
					Type:  EventContentBlockStart,
					Index: acc.blockIndex,
					BlockType: ContentBlockType{
						Kind:     BlockToolUse,
						ToolID:   toolID,
						ToolName: name,
					},
				},
				StreamEvent{Type: EventToolUseStart, ToolID: toolID, ToolName: name},
			)
		}

		if payload.Input != nil && *payload.Input != "" {
			acc.inputBuffer.WriteString(*payload.Input)
			events = append(events, StreamEvent{Type: EventToolUseInputDelta, ToolID: toolID, PartialJSON: *payload.Input})
		}

		isStop := payload.Stop != nil && *payload.Stop
		if isStop {
			accumulated := acc.inputBuffer.String()
			if shouldReportToolInputTruncation(acc.name, accumulated) {
				log.Printf("[kiro-truncation] tool=%s id=%s TRUNCATED at stop", acc.name, toolID)
			}
			delete(p.toolAccumulators, toolID)
			events = append(events,
				StreamEvent{Type: EventToolUseStop, ToolID: toolID},
				StreamEvent{Type: EventContentBlockStop, Index: acc.blockIndex},
			)
		}

		return events, nil
	}

	// Usage events are parsed for accounting only and can never produce text.
	credits := 0.0
	ctxPct := 0.0
	hasTokenUsage := false
	inputTokens := 0
	outputTokens := 0
	cacheCreationTokens := 0
	cacheReadTokens := 0

	if payload.Usage != nil {
		credits = *payload.Usage
	}
	if payload.ContextUsagePercent != nil {
		ctxPct = *payload.ContextUsagePercent
	}

	if credits == 0 && payload.MeteringEvent != nil {
		for _, k := range []string{"credit", "credits", "usage", "cost", "value"} {
			if v, ok := payload.MeteringEvent[k].(float64); ok && v > 0 {
				credits = v
				break
			}
		}
	}
	if ctxPct == 0 && payload.ContextUsageEvent != nil {
		for _, k := range []string{"contextUsagePercentage", "usagePercent", "percent", "usage", "contextPercent"} {
			if v, ok := payload.ContextUsageEvent[k].(float64); ok && v > 0 {
				ctxPct = v
				break
			}
		}
	}
	if ctxPct == 0 {
		ctxPct = contextUsageFromTokenUsage(payload.TokenUsage)
	}
	if ctxPct == 0 {
		ctxPct = contextUsageFromTokenUsage(tokenUsageFromMetadata(payload.MessageMetadataEvent))
	}
	if ctxPct == 0 {
		ctxPct = contextUsageFromTokenUsage(tokenUsageFromMetadata(payload.MetadataEvent))
	}

	for _, usageMap := range []map[string]any{
		payload.TokenUsage,
		tokenUsageFromMetadata(payload.MessageMetadataEvent),
		tokenUsageFromMetadata(payload.MetadataEvent),
	} {
		in, out, cacheCreation, cacheRead, has := extractTokenUsage(usageMap)
		if !has {
			continue
		}
		hasTokenUsage = true
		inputTokens = in
		outputTokens = out
		cacheCreationTokens = cacheCreation
		cacheReadTokens = cacheRead
		break
	}

	if credits > 0 || ctxPct > 0 || hasTokenUsage {
		events = append(events, StreamEvent{
			Type:                     EventBackendUsage,
			Credits:                  credits,
			ContextPercentage:        ctxPct,
			HasTokenUsage:            hasTokenUsage,
			InputTokens:              inputTokens,
			OutputTokens:             outputTokens,
			CacheCreationInputTokens: cacheCreationTokens,
			CacheReadInputTokens:     cacheReadTokens,
		})
	}

	return events, nil
}

// ==================== Claude SSE Event Builders ====================

// ClaudeSSEEvent represents a Claude SSE event
type ClaudeSSEEvent struct {
	EventType string
	Data      map[string]any
}

// BuildClaudeMessageStart builds a message_start event
func BuildClaudeMessageStart(messageID string, model string, inputTokens, cacheCreationTokens, cacheReadTokens int) ClaudeSSEEvent {
	usage := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": 0,
	}
	if cacheCreationTokens > 0 {
		usage["cache_creation_input_tokens"] = cacheCreationTokens
	}
	if cacheReadTokens > 0 {
		usage["cache_read_input_tokens"] = cacheReadTokens
	}
	return ClaudeSSEEvent{
		EventType: "message_start",
		Data: map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            messageID,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         usage,
			},
		},
	}
}

// BuildClaudePing builds a ping event
func BuildClaudePing() ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "ping",
		Data: map[string]any{
			"type": "ping",
		},
	}
}

// BuildClaudeContentBlockStart builds a content_block_start event
func BuildClaudeContentBlockStart(index int, blockType ContentBlockType) ClaudeSSEEvent {
	contentBlock := make(map[string]any)

	switch blockType.Kind {
	case BlockText:
		contentBlock["type"] = "text"
		contentBlock["text"] = ""
	case BlockToolUse:
		contentBlock["type"] = "tool_use"
		contentBlock["id"] = blockType.ToolID
		contentBlock["name"] = blockType.ToolName
		contentBlock["input"] = map[string]any{}
	case BlockThinking:
		contentBlock["type"] = "thinking"
		contentBlock["thinking"] = ""
	}

	return ClaudeSSEEvent{
		EventType: "content_block_start",
		Data: map[string]any{
			"type":          "content_block_start",
			"index":         index,
			"content_block": contentBlock,
		},
	}
}

// BuildClaudeTextDelta builds a content_block_delta event for text
func BuildClaudeTextDelta(index int, text string) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "content_block_delta",
		Data: map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{
				"type": "text_delta",
				"text": text,
			},
		},
	}
}

// BuildClaudeThinkingDelta builds a content_block_delta event for thinking
func BuildClaudeThinkingDelta(index int, thinking string) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "content_block_delta",
		Data: map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{
				"type":     "thinking_delta",
				"thinking": thinking,
			},
		},
	}
}

// BuildClaudeSignatureDelta builds a content_block_delta event for thinking signatures.
func BuildClaudeSignatureDelta(index int, signature string) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "content_block_delta",
		Data: map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": signature,
			},
		},
	}
}

// BuildClaudeInputJSONDelta builds a content_block_delta event for tool input
func BuildClaudeInputJSONDelta(index int, partialJSON string) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "content_block_delta",
		Data: map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": partialJSON,
			},
		},
	}
}

// BuildClaudeContentBlockStop builds a content_block_stop event
func BuildClaudeContentBlockStop(index int) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "content_block_stop",
		Data: map[string]any{
			"type":  "content_block_stop",
			"index": index,
		},
	}
}

// BuildClaudeMessageDelta builds a message_delta event
func BuildClaudeMessageDelta(stopReason string, outputTokens, inputTokens int, contextUsagePercent float64, cacheCreationTokens, cacheReadTokens int) ClaudeSSEEvent {
	usage := map[string]any{
		"output_tokens": outputTokens,
		"input_tokens":  inputTokens,
	}
	if contextUsagePercent > 0 {
		usage["context_usage_percent"] = contextUsagePercent
	}
	// Add cache tokens if present (Claude standard fields)
	if cacheCreationTokens > 0 {
		usage["cache_creation_input_tokens"] = cacheCreationTokens
	}
	if cacheReadTokens > 0 {
		usage["cache_read_input_tokens"] = cacheReadTokens
	}
	return ClaudeSSEEvent{
		EventType: "message_delta",
		Data: map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
			"usage": usage,
		},
	}
}

// BuildClaudeMessageStop builds a message_stop event
func BuildClaudeMessageStop() ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "message_stop",
		Data: map[string]any{
			"type": "message_stop",
		},
	}
}

// BuildClaudeError builds an error event
func BuildClaudeError(errorType, message string) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "error",
		Data: map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errorType,
				"message": message,
			},
		},
	}
}

// FormatClaudeSSE formats a ClaudeSSEEvent as SSE string
func FormatClaudeSSE(event ClaudeSSEEvent) (string, error) {
	data, err := json.Marshal(event.Data)
	if err != nil {
		return "", fmt.Errorf("marshal event data failed: %w", err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event.EventType, string(data)), nil
}

// ==================== Stream Event to Claude SSE Converter ====================

// StreamEventConverter converts StreamEvents to Claude SSE events
type StreamEventConverter struct {
	messageID   string
	model       string
	inputTokens int

	activeTextBlockIndex     *uint32
	activeThinkingBlockIndex *uint32
	thinkingBuffer           strings.Builder
	toolIDToBlockIndex       map[string]uint32
	blockIndexToToolID       map[uint32]string

	sawToolUse        bool
	sawText           bool
	sawThinking       bool
	totalOutputTokens int
	contextPct        float64 // Context usage percentage from backend

	// Cache token estimation
	cacheCreationTokens int
	cacheReadTokens     int

	// Tool name restoration map (shortened -> original)
	toolNameReverseMap map[string]string

	// Content block index offset for injected blocks (e.g. web search)
	contentBlockOffset int
}

// NewStreamEventConverter creates a new converter
func NewStreamEventConverter(messageID, model string, inputTokens int) *StreamEventConverter {
	return &StreamEventConverter{
		messageID:          messageID,
		model:              model,
		inputTokens:        inputTokens,
		toolIDToBlockIndex: make(map[string]uint32),
		blockIndexToToolID: make(map[uint32]string),
	}
}

// SetToolNameReverseMap sets the tool name reverse map for restoring original names
func (c *StreamEventConverter) SetToolNameReverseMap(reverseMap map[string]string) {
	c.toolNameReverseMap = reverseMap
}

// restoreToolName restores the original tool name from shortened name
func (c *StreamEventConverter) restoreToolName(shortName string) string {
	if c.toolNameReverseMap == nil {
		return shortName
	}
	if original, ok := c.toolNameReverseMap[shortName]; ok {
		return original
	}
	return shortName
}

// SawToolUse returns whether any tool use was seen
func (c *StreamEventConverter) SawToolUse() bool {
	return c.sawToolUse
}

// TotalOutputTokens returns the estimated output tokens
func (c *StreamEventConverter) TotalOutputTokens() int {
	return c.totalOutputTokens
}

// SetContextPercentage sets the context usage percentage from backend
func (c *StreamEventConverter) SetContextPercentage(pct float64) {
	c.contextPct = pct
}

// SetCacheTokens sets the cache token estimation for billing
func (c *StreamEventConverter) SetCacheTokens(cacheCreation, cacheRead int) {
	c.cacheCreationTokens = cacheCreation
	c.cacheReadTokens = cacheRead
}

func (c *StreamEventConverter) SetUpstreamUsage(inputTokens, outputTokens, cacheCreation, cacheRead int) {
	upstreamTotal := inputTokens + cacheCreation + cacheRead
	if upstreamTotal > 0 {
		// Upstream provided real usage — override all local estimates,
		// including cache tokens (even if 0) to avoid stale local values
		// inflating the subtraction in BuildFinalEvents.
		c.inputTokens = upstreamTotal
		c.cacheCreationTokens = cacheCreation
		c.cacheReadTokens = cacheRead
	}
	if outputTokens > 0 {
		c.totalOutputTokens = outputTokens
	}
}

// SetContentBlockOffset sets the starting index offset for content blocks.
// Used when web search blocks are injected before the CW response stream.
func (c *StreamEventConverter) SetContentBlockOffset(offset int) {
	c.contentBlockOffset = offset
}

// ConvertEvent converts a StreamEvent to Claude SSE events
func (c *StreamEventConverter) ConvertEvent(e StreamEvent) []ClaudeSSEEvent {
	switch e.Type {
	case EventMessageStart:
		// message_start is handled separately by initial events
		return nil

	case EventContentBlockStart:
		return c.handleBlockStart(e)

	case EventTextDelta:
		return c.handleTextDelta(e)

	case EventThinkingDelta:
		return c.handleThinkingDelta(e)

	case EventToolUseInputDelta:
		return c.handleToolInputDelta(e)

	case EventContentBlockStop:
		return c.handleBlockStop(e)

	case EventMessageStop:
		// message_stop is handled separately by final events
		return nil

	case EventBackendUsage:
		// Usage events don't produce SSE output
		return nil

	case EventError:
		return []ClaudeSSEEvent{BuildClaudeError("overloaded_error", e.ErrorMessage)}

	default:
		return nil
	}
}

func (c *StreamEventConverter) handleBlockStart(e StreamEvent) []ClaudeSSEEvent {
	// Apply offset for injected blocks (e.g. web search)
	adjustedIndex := e.Index + uint32(c.contentBlockOffset)

	switch e.BlockType.Kind {
	case BlockText:
		c.sawText = true
		c.activeTextBlockIndex = &adjustedIndex

	case BlockToolUse:
		c.sawToolUse = true
		c.toolIDToBlockIndex[e.BlockType.ToolID] = adjustedIndex
		c.blockIndexToToolID[adjustedIndex] = e.BlockType.ToolID

	case BlockThinking:
		c.sawThinking = true
		c.activeThinkingBlockIndex = &adjustedIndex
	}

	// Restore original tool name if shortened
	blockType := e.BlockType
	if blockType.Kind == BlockToolUse {
		blockType.ToolName = c.restoreToolName(blockType.ToolName)
	}

	return []ClaudeSSEEvent{BuildClaudeContentBlockStart(int(adjustedIndex), blockType)}
}

func (c *StreamEventConverter) handleTextDelta(e StreamEvent) []ClaudeSSEEvent {
	if c.activeTextBlockIndex == nil {
		return nil
	}
	idx := *c.activeTextBlockIndex

	// Estimate tokens (rough: 4 chars per token)
	c.totalOutputTokens += (len(e.Text) + 3) / 4

	return []ClaudeSSEEvent{BuildClaudeTextDelta(int(idx), e.Text)}
}

func (c *StreamEventConverter) handleThinkingDelta(e StreamEvent) []ClaudeSSEEvent {
	if c.activeThinkingBlockIndex == nil {
		return nil
	}
	idx := *c.activeThinkingBlockIndex

	// Estimate tokens
	c.totalOutputTokens += (len(e.Text) + 3) / 4
	c.thinkingBuffer.WriteString(e.Text)

	return []ClaudeSSEEvent{BuildClaudeThinkingDelta(int(idx), e.Text)}
}

func (c *StreamEventConverter) handleToolInputDelta(e StreamEvent) []ClaudeSSEEvent {
	blockIndex, ok := c.toolIDToBlockIndex[e.ToolID]
	if !ok {
		return nil
	}

	// Estimate tokens
	c.totalOutputTokens += (len(e.PartialJSON) + 3) / 4

	return []ClaudeSSEEvent{BuildClaudeInputJSONDelta(int(blockIndex), e.PartialJSON)}
}

func (c *StreamEventConverter) handleBlockStop(e StreamEvent) []ClaudeSSEEvent {
	adjustedIndex := e.Index + uint32(c.contentBlockOffset)

	if c.activeTextBlockIndex != nil && *c.activeTextBlockIndex == adjustedIndex {
		c.activeTextBlockIndex = nil
	}
	if c.activeThinkingBlockIndex != nil && *c.activeThinkingBlockIndex == adjustedIndex {
		signature := syntheticThinkingSignature(c.thinkingBuffer.String())
		c.thinkingBuffer.Reset()
		c.activeThinkingBlockIndex = nil
		return []ClaudeSSEEvent{
			BuildClaudeSignatureDelta(int(adjustedIndex), signature),
			BuildClaudeContentBlockStop(int(adjustedIndex)),
		}
	}
	if toolID, ok := c.blockIndexToToolID[adjustedIndex]; ok {
		delete(c.blockIndexToToolID, adjustedIndex)
		delete(c.toolIDToBlockIndex, toolID)
	}

	return []ClaudeSSEEvent{BuildClaudeContentBlockStop(int(adjustedIndex))}
}

// BuildInitialEvents builds the initial SSE events for a stream
func (c *StreamEventConverter) BuildInitialEvents() []ClaudeSSEEvent {
	// Subtract cache tokens to match Anthropic's definition (input_tokens excludes cache)
	inputTokens := c.inputTokens
	if c.cacheReadTokens > 0 {
		inputTokens -= c.cacheReadTokens
	}
	if c.cacheCreationTokens > 0 {
		inputTokens -= c.cacheCreationTokens
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	inflatedTokens := InflateInputTokens(inputTokens)
	return []ClaudeSSEEvent{
		BuildClaudeMessageStart(c.messageID, c.model, inflatedTokens, c.cacheCreationTokens, c.cacheReadTokens),
		BuildClaudePing(),
	}
}

// BuildFinalEvents builds the final SSE events for a stream
func (c *StreamEventConverter) BuildFinalEvents() []ClaudeSSEEvent {
	stopReason := "end_turn"
	if c.sawToolUse {
		stopReason = "tool_use"
	} else if c.sawThinking && !c.sawText {
		// Thinking-only: model spent entire token budget on thinking
		stopReason = "max_tokens"
	}
	if c.contextPct >= 100 {
		stopReason = "model_context_window_exceeded"
	}

	outputTokens := c.totalOutputTokens
	if outputTokens < 1 {
		outputTokens = 1
	}

	// Use estimated input tokens directly (aligned with kiro4api)
	inputTokens := c.inputTokens

	// Subtract cache tokens from input_tokens to match Anthropic's definition:
	// - input_tokens = non-cached input tokens (excludes cache_read and cache_creation)
	// - Total = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
	if c.cacheReadTokens > 0 {
		inputTokens -= c.cacheReadTokens
	}
	if c.cacheCreationTokens > 0 {
		inputTokens -= c.cacheCreationTokens
	}
	if inputTokens < 0 {
		inputTokens = 0
	}

	// Apply inflation to trigger client-side context compression earlier
	inflatedTokens := InflateInputTokens(inputTokens)

	return []ClaudeSSEEvent{
		BuildClaudeMessageDelta(stopReason, outputTokens, inflatedTokens, c.contextPct, c.cacheCreationTokens, c.cacheReadTokens),
		BuildClaudeMessageStop(),
	}
}

// ==================== Non-Streaming Response Builder ====================

// CompleteResponse represents a parsed complete response
type CompleteResponse struct {
	Text          string
	Thinking      string
	ToolCalls     []ToolCallData
	ContextPct    float64 // Context usage percentage from backend
	HasTokenUsage bool
	InputTokens   int
	OutputTokens  int

	// Cache token estimation (set externally before building response)
	CacheCreationTokens int
	CacheReadTokens     int
}

// ToolCallData represents a tool call in the response
type ToolCallData struct {
	ID           string
	Name         string
	ArgumentsRaw string
	IsTruncated  bool
}

// ParseCompleteResponse parses a complete (non-streaming) response
func ParseCompleteResponse(data []byte) *CompleteResponse {
	return ParseCompleteResponseWithNameRestore(data, nil)
}

// ParseCompleteResponseWithNameRestore parses a complete response and restores tool names
func ParseCompleteResponseWithNameRestore(data []byte, toolNameReverseMap map[string]string) *CompleteResponse {
	return ParseCompleteResponseWithNameRestoreAndThinking(data, toolNameReverseMap, false)
}

// ParseCompleteResponseWithNameRestoreAndThinking parses a complete response
// and restores tool names with explicit thinking-mode awareness.
func ParseCompleteResponseWithNameRestoreAndThinking(data []byte, toolNameReverseMap map[string]string, thinkingEnabled bool) *CompleteResponse {
	resp, _ := ParseCompleteResponseWithNameRestoreAndThinkingStrict(data, toolNameReverseMap, thinkingEnabled)
	if resp == nil {
		return &CompleteResponse{}
	}
	return resp
}

// ParseCompleteResponseWithNameRestoreAndThinkingStrict parses a complete
// response and returns event-stream framing and upstream error events.
func ParseCompleteResponseWithNameRestoreAndThinkingStrict(data []byte, toolNameReverseMap map[string]string, thinkingEnabled bool) (*CompleteResponse, error) {
	parser := NewAwsEventStreamParser("", "")
	parser.SetThinkingEnabled(thinkingEnabled)
	events := parser.Process(data)
	events = append(events, parser.Finish()...)
	for _, event := range events {
		if event.Type != EventError {
			continue
		}
		errorType := event.ErrorType
		if errorType == "" {
			errorType = "upstream_error"
		}
		return nil, fmt.Errorf("%s: %s", errorType, event.ErrorMessage)
	}

	resp := &CompleteResponse{}
	var textParts []string
	var thinkingParts []string
	toolInputs := make(map[string]string) // toolID -> accumulated input JSON

	restoreName := func(name string) string {
		if toolNameReverseMap == nil {
			return name
		}
		if original, ok := toolNameReverseMap[name]; ok {
			return original
		}
		return name
	}

	for _, e := range events {
		switch e.Type {
		case EventTextDelta:
			textParts = append(textParts, e.Text)
		case EventThinkingDelta:
			thinkingParts = append(thinkingParts, e.Text)
		case EventContentBlockStart:
			if e.BlockType.Kind == BlockToolUse {
				resp.ToolCalls = append(resp.ToolCalls, ToolCallData{
					ID:   e.BlockType.ToolID,
					Name: restoreName(e.BlockType.ToolName),
				})
			}
		case EventToolUseInputDelta:
			toolInputs[e.ToolID] += e.PartialJSON
		case EventBackendUsage:
			if e.ContextPercentage > 0 {
				resp.ContextPct = e.ContextPercentage
			}
			if e.HasTokenUsage {
				resp.HasTokenUsage = true
				resp.InputTokens = e.InputTokens
				resp.OutputTokens = e.OutputTokens
				resp.CacheCreationTokens = e.CacheCreationInputTokens
				resp.CacheReadTokens = e.CacheReadInputTokens
			}
		}
	}

	// Combine text parts
	for _, part := range textParts {
		resp.Text += part
	}
	for _, part := range thinkingParts {
		resp.Thinking += part
	}

	// Assign accumulated inputs to tool calls
	for i := range resp.ToolCalls {
		if input, ok := toolInputs[resp.ToolCalls[i].ID]; ok {
			if shouldReportToolInputTruncation(resp.ToolCalls[i].Name, input) {
				log.Printf("[kiro-truncation] non-stream tool=%s TRUNCATED", resp.ToolCalls[i].Name)
				resp.ToolCalls[i].IsTruncated = true
			}
			resp.ToolCalls[i].ArgumentsRaw = input
		}
	}

	return resp, nil
}

func shouldReportToolInputTruncation(toolName, rawInput string) bool {
	if strings.TrimSpace(rawInput) == "" {
		return false
	}
	return DetectToolInputTruncation(toolName, rawInput)
}

// BuildClaudeNonStreamResponse builds a complete Claude response from parsed data
func BuildClaudeNonStreamResponse(messageID, model string, inputTokens int, resp *CompleteResponse) map[string]any {
	content := make([]map[string]any, 0)

	if resp.Text != "" {
		if resp.Thinking != "" {
			content = append(content, map[string]any{
				"type":      "thinking",
				"thinking":  resp.Thinking,
				"signature": syntheticThinkingSignature(resp.Thinking),
			})
		}
		content = append(content, map[string]any{
			"type": "text",
			"text": resp.Text,
		})
	} else if resp.Thinking != "" {
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  resp.Thinking,
			"signature": syntheticThinkingSignature(resp.Thinking),
		})
	}

	for _, tool := range resp.ToolCalls {
		input := map[string]any{}
		if tool.ArgumentsRaw != "" {
			_ = json.Unmarshal([]byte(tool.ArgumentsRaw), &input)
		}
		name := tool.Name
		if name == "" {
			name = "unknown"
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    tool.ID,
			"name":  name,
			"input": input,
		})
	}

	// Output tokens: prefer upstream value, fall back to local estimate
	outputTokens := resp.OutputTokens
	if outputTokens <= 0 {
		if resp.Thinking != "" {
			outputTokens += (len(resp.Thinking) + 3) / 4
		}
		if resp.Text != "" {
			outputTokens += (len(resp.Text) + 3) / 4
		}
		for _, tool := range resp.ToolCalls {
			outputTokens += (len(tool.Name) + len(tool.ArgumentsRaw) + 3) / 4
		}
		if outputTokens < 1 && len(content) > 0 {
			outputTokens = 1
		}
	}

	stopReason := "end_turn"
	if len(resp.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	if resp.ContextPct >= 100 {
		stopReason = "model_context_window_exceeded"
	}

	// Subtract cache tokens from input_tokens to match Anthropic's definition:
	// - input_tokens = non-cached input tokens (excludes cache_read and cache_creation)
	// - Total = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
	adjustedInputTokens := inputTokens
	if resp.CacheReadTokens > 0 {
		adjustedInputTokens -= resp.CacheReadTokens
	}
	if resp.CacheCreationTokens > 0 {
		adjustedInputTokens -= resp.CacheCreationTokens
	}
	if adjustedInputTokens < 0 {
		adjustedInputTokens = 0
	}

	// Apply inflation to trigger client-side context compression earlier
	inflatedTokens := InflateInputTokens(adjustedInputTokens)

	usage := map[string]any{
		"input_tokens":  inflatedTokens,
		"output_tokens": outputTokens,
	}
	if resp.ContextPct > 0 {
		usage["context_usage_percent"] = resp.ContextPct
	}
	// Add cache tokens if present (Claude standard fields)
	if resp.CacheCreationTokens > 0 {
		usage["cache_creation_input_tokens"] = resp.CacheCreationTokens
	}
	if resp.CacheReadTokens > 0 {
		usage["cache_read_input_tokens"] = resp.CacheReadTokens
	}

	return map[string]any{
		"id":            messageID,
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

// ==================== Thinking Tag Parsing ====================

// findCharBoundary finds the largest byte index <= targetLen that is a valid
// UTF-8 character boundary. This prevents splitting multi-byte characters
// (e.g. Chinese characters are 3 bytes in UTF-8).
// Aligned with kiro.rs find_char_boundary.
func findCharBoundary(s string, targetLen int) int {
	if targetLen >= len(s) {
		return len(s)
	}
	if targetLen <= 0 {
		return 0
	}
	// Walk backwards from targetLen to find a valid UTF-8 start byte
	for i := targetLen; i > targetLen-4 && i > 0; i-- {
		// In UTF-8, continuation bytes start with 10xxxxxx (0x80-0xBF)
		// Start bytes are 0xxxxxxx, 110xxxxx, 1110xxxx, 11110xxx
		if s[i]&0xC0 != 0x80 {
			return i
		}
	}
	return 0
}
func isQuoteChar(s string, pos int) bool {
	if pos < 0 || pos >= len(s) {
		return false
	}
	c := s[pos]
	return c == '`' || c == '"' || c == '\''
}

// findRealThinkingEndTag finds the position of a real </thinking> end tag.
// A "real" end tag must:
// 1. Not be wrapped in quotes or backticks
// 2. Be followed by \n\n (AWSQ always emits \n\n after </thinking>)
// Returns -1 if not found.
// Aligned with kiro.rs find_real_thinking_end_tag.
func findRealThinkingEndTag(buffer string) int {
	searchStart := 0
	for {
		idx := strings.Index(buffer[searchStart:], ThinkingEndTag)
		if idx < 0 {
			return -1
		}
		absPos := searchStart + idx

		// Check if wrapped in quotes/backticks
		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer, absPos-1)
		afterPos := absPos + len(ThinkingEndTag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)

		if hasQuoteBefore || hasQuoteAfter {
			searchStart = absPos + 1
			continue
		}

		// Check for \n\n after the tag
		afterContent := buffer[afterPos:]
		if len(afterContent) < 2 {
			// Not enough content to determine — wait for more
			return -1
		}
		if strings.HasPrefix(afterContent, "\n\n") {
			return absPos
		}

		// Not followed by \n\n, skip
		searchStart = absPos + 1
	}
}

// findRealThinkingEndTagAtBufferEnd finds </thinking> at the end of buffer.
// Used for boundary cases: stream end or tool_use start where \n\n may not follow.
// Only matches if everything after </thinking> is whitespace.
// Aligned with kiro.rs find_real_thinking_end_tag_at_buffer_end.
func findRealThinkingEndTagAtBufferEnd(buffer string) int {
	searchStart := 0
	for {
		idx := strings.Index(buffer[searchStart:], ThinkingEndTag)
		if idx < 0 {
			return -1
		}
		absPos := searchStart + idx

		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer, absPos-1)
		afterPos := absPos + len(ThinkingEndTag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)

		if hasQuoteBefore || hasQuoteAfter {
			searchStart = absPos + 1
			continue
		}

		// Only match if everything after the tag is whitespace
		if strings.TrimSpace(buffer[afterPos:]) == "" {
			return absPos
		}

		searchStart = absPos + 1
	}
}

// findRealThinkingStartTag finds the position of a real <thinking> start tag.
// Skips tags wrapped in quotes or backticks.
// Aligned with kiro.rs find_real_thinking_start_tag.
func findRealThinkingStartTag(buffer string) int {
	searchStart := 0
	for {
		idx := strings.Index(buffer[searchStart:], ThinkingStartTag)
		if idx < 0 {
			return -1
		}
		absPos := searchStart + idx

		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer, absPos-1)
		afterPos := absPos + len(ThinkingStartTag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)

		if hasQuoteBefore || hasQuoteAfter {
			searchStart = absPos + 1
			continue
		}

		return absPos
	}
}

// emitTextDelta is a helper that emits text delta events, opening a text block if needed.
func (p *AwsEventStreamParser) emitTextDelta(text string) []StreamEvent {
	var events []StreamEvent
	if !p.inTextBlock {
		p.inTextBlock = true
		idx := p.nextIndex()
		p.textBlockIndex = &idx
		events = append(events, StreamEvent{
			Type: EventContentBlockStart, Index: idx,
			BlockType: ContentBlockType{Kind: BlockText},
		})
	}
	p.sawText = true
	events = append(events, StreamEvent{Type: EventTextDelta, Text: text})
	return events
}

// flushThinkingBeforeToolUse flushes the thinking buffer before a tool_use event.
// Aligned with kiro.rs process_tool_use boundary handling.
func (p *AwsEventStreamParser) flushThinkingBeforeToolUse() []StreamEvent {
	var events []StreamEvent

	// Handle in-thinking-block case: </thinking> may be at buffer end without \n\n
	if p.inThinkingBlock {
		pending := p.pendingContent.String()
		p.pendingContent.Reset()
		pending = p.stripThinkingLeadingNewlineIfNeeded(pending)

		if endPos := findRealThinkingEndTagAtBufferEnd(pending); endPos >= 0 {
			thinkingContent := pending[:endPos]
			if thinkingContent != "" {
				if p.thinkingBlockIndex == nil {
					idx := p.nextIndex()
					p.thinkingBlockIndex = &idx
					events = append(events, StreamEvent{
						Type: EventContentBlockStart, Index: idx,
						BlockType: ContentBlockType{Kind: BlockThinking},
					})
				}
				events = append(events, StreamEvent{Type: EventThinkingDelta, Text: thinkingContent})
			}
			// Close thinking block
			if p.thinkingBlockIndex != nil {
				idx := *p.thinkingBlockIndex
				events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
				p.thinkingBlockIndex = nil
			}
			p.inThinkingBlock = false
			p.thinkingExtracted = true
			p.stripThinkingLeadingNewline = false

			// Remaining after tag as text
			afterPos := endPos + len(ThinkingEndTag)
			remaining := strings.TrimLeft(pending[afterPos:], " \t\n\r")
			if remaining != "" {
				events = append(events, p.emitTextDelta(remaining)...)
			}
		}
	}

	// Flush pending content that was buffered for <thinking> tag detection
	if !p.inThinkingBlock && !p.thinkingExtracted {
		pending := p.pendingContent.String()
		p.pendingContent.Reset()
		if pending != "" {
			events = append(events, p.emitTextDelta(pending)...)
		}
	}

	return events
}

func (p *AwsEventStreamParser) stripThinkingLeadingNewlineIfNeeded(content string) string {
	if !p.stripThinkingLeadingNewline {
		return content
	}
	if strings.HasPrefix(content, "\n") {
		p.stripThinkingLeadingNewline = false
		return strings.TrimPrefix(content, "\n")
	}
	if content != "" {
		p.stripThinkingLeadingNewline = false
	}
	return content
}

// parseContentWithThinking parses content for <thinking> tags and emits appropriate events.
// Aligned with kiro.rs process_content_with_thinking.
//
// Key differences from naive implementation:
// 1. </thinking> must be followed by \n\n to be a real end tag
// 2. Tags wrapped in quotes/backticks are skipped
// 3. thinkingExtracted flag: once thinking is done, all content is text
// 4. Partial tag buffering for cross-chunk tag detection
func (p *AwsEventStreamParser) parseContentWithThinking(contentDelta string) []StreamEvent {
	var events []StreamEvent
	if contentDelta == "" {
		return nil
	}

	if !p.thinkingEnabled {
		if p.pendingContent.Len() > 0 {
			pending := p.pendingContent.String()
			p.pendingContent.Reset()
			contentDelta = pending + contentDelta
		}
		return p.emitTextDelta(contentDelta)
	}

	// Combine pending content with new content for processing
	p.pendingContent.WriteString(contentDelta)
	processContent := p.pendingContent.String()
	p.pendingContent.Reset()

	for len(processContent) > 0 {
		if p.inThinkingBlock {
			processContent = p.stripThinkingLeadingNewlineIfNeeded(processContent)

			// Inside thinking block — look for real </thinking>\n\n
			endIdx := findRealThinkingEndTag(processContent)
			if endIdx >= 0 {
				// Found real end tag — emit thinking content before it
				thinkingText := processContent[:endIdx]
				if thinkingText != "" {
					if p.thinkingBlockIndex == nil {
						idx := p.nextIndex()
						p.thinkingBlockIndex = &idx
						events = append(events, StreamEvent{
							Type: EventContentBlockStart, Index: idx,
							BlockType: ContentBlockType{Kind: BlockThinking},
						})
					}
					events = append(events, StreamEvent{Type: EventThinkingDelta, Text: thinkingText})
				}
				// Close thinking block
				if p.thinkingBlockIndex != nil {
					idx := *p.thinkingBlockIndex
					events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
					p.thinkingBlockIndex = nil
				}
				p.inThinkingBlock = false
				p.thinkingExtracted = true
				p.stripThinkingLeadingNewline = false
				// Skip </thinking>\n\n
				processContent = processContent[endIdx+len(ThinkingEndTag)+2:]
			} else {
				// No real end tag found — buffer tail for partial tag matching.
				// Must retain enough for "</thinking>\n\n" (13 bytes) to avoid
				// emitting tag chars as thinking content when tag spans chunks.
				reserveLen := len("</thinking>\n\n")
				if len(processContent) > reserveLen {
					safeLen := findCharBoundary(processContent, len(processContent)-reserveLen)
					safeContent := processContent[:safeLen]
					if safeContent != "" {
						if p.thinkingBlockIndex == nil {
							idx := p.nextIndex()
							p.thinkingBlockIndex = &idx
							events = append(events, StreamEvent{
								Type: EventContentBlockStart, Index: idx,
								BlockType: ContentBlockType{Kind: BlockThinking},
							})
						}
						events = append(events, StreamEvent{Type: EventThinkingDelta, Text: safeContent})
					}
					p.pendingContent.WriteString(processContent[safeLen:])
				} else {
					// Entire content is shorter than reserve — buffer all
					p.pendingContent.WriteString(processContent)
				}
				processContent = ""
			}
		} else if !p.thinkingExtracted {
			// Not in thinking block and thinking not yet extracted — look for <thinking>
			startIdx := findRealThinkingStartTag(processContent)
			if startIdx >= 0 {
				// Found start tag — emit non-whitespace content before it as text,
				// matching kiro.rs. If no real <thinking> tag appears yet, the
				// prefix cannot be safely distinguished from normal assistant text.
				textBefore := processContent[:startIdx]
				if textBefore != "" && strings.TrimSpace(textBefore) != "" {
					events = append(events, p.emitTextDelta(textBefore)...)
				}
				// Close text block before entering thinking
				if p.inTextBlock && p.textBlockIndex != nil {
					idx := *p.textBlockIndex
					events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
					p.textBlockIndex = nil
					p.inTextBlock = false
				}
				p.inThinkingBlock = true
				p.stripThinkingLeadingNewline = true
				p.sawThinking = true
				processContent = processContent[startIdx+len(ThinkingStartTag):]
			} else {
				// No start tag — check for partial match at end
				reserveLen := len(ThinkingStartTag) - 1
				if len(processContent) > reserveLen {
					safeLen := findCharBoundary(processContent, len(processContent)-reserveLen)
					safeContent := processContent[:safeLen]
					// Skip whitespace-only content when thinking not yet extracted
					// (avoids creating text block before thinking block)
					if safeContent != "" && strings.TrimSpace(safeContent) != "" {
						events = append(events, p.emitTextDelta(safeContent)...)
					}
					p.pendingContent.WriteString(processContent[safeLen:])
				} else {
					p.pendingContent.WriteString(processContent)
				}
				processContent = ""
			}
		} else {
			// Thinking already extracted — all remaining content is text
			events = append(events, p.emitTextDelta(processContent)...)
			processContent = ""
		}
	}

	return events
}

// pendingTagSuffix detects if the buffer ends with a partial prefix of the given tag.
// Returns the length of the partial match (0 if no match).
func pendingTagSuffix(buffer, tag string) int {
	if buffer == "" || tag == "" {
		return 0
	}
	maxLen := len(buffer)
	if maxLen > len(tag)-1 {
		maxLen = len(tag) - 1
	}
	for length := maxLen; length > 0; length-- {
		if len(buffer) >= length && buffer[len(buffer)-length:] == tag[:length] {
			return length
		}
	}
	return 0
}
