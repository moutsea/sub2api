// Package kiro provides response transformation from CodeWhisperer to Claude format.
package kiro

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// awsPayload represents the JSON payload from CodeWhisperer stream
type awsPayload struct {
	Content             *string         `json:"content"`
	FollowupPrompt      json.RawMessage `json:"followupPrompt"`
	ToolUseID           *string         `json:"toolUseId"`
	Name                *string         `json:"name"`
	Input               *string         `json:"input"`
	Stop                *bool           `json:"stop"`
	Usage               *float64        `json:"usage"`
	ContextUsagePercent *float64        `json:"contextUsagePercentage"`
	MeteringEvent       map[string]any  `json:"meteringEvent"`
	ContextUsageEvent   map[string]any  `json:"contextUsageEvent"`
}

// toolAccumulator tracks state for a tool use block
type toolAccumulator struct {
	name       string
	blockIndex uint32
	started    bool
}

// AwsEventStreamParser parses CodeWhisperer AWS EventStream binary format
// and outputs unified StreamEvents.
//
// Design goals:
// - Independent of JSON field order
// - Tool calls bound by toolUseId (supports concurrent/interleaved)
// - Clear memory limits to prevent buffer overflow
// - Aligned with proxycast Kiro parsing semantics
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

	parseErrorCount int
}

const defaultMaxBufferSize = 1024 * 1024

// NewAwsEventStreamParser creates a new parser
func NewAwsEventStreamParser(messageID, model string) *AwsEventStreamParser {
	return &AwsEventStreamParser{
		messageID:        messageID,
		model:            model,
		maxBufferSize:    defaultMaxBufferSize,
		toolAccumulators: make(map[string]*toolAccumulator),
	}
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

func (p *AwsEventStreamParser) nextIndex() uint32 {
	idx := p.nextBlockIndex
	p.nextBlockIndex++
	return idx
}

// Process processes a chunk of data and returns stream events
func (p *AwsEventStreamParser) Process(chunk []byte) []StreamEvent {
	if len(chunk) == 0 {
		return nil
	}

	if len(p.buffer)+len(chunk) > p.maxBufferSize {
		p.parseErrorCount++
		return []StreamEvent{{
			Type:         EventError,
			ErrorType:    "buffer_overflow",
			ErrorMessage: "buffer overflow",
		}}
	}

	p.buffer = append(p.buffer, chunk...)
	return p.parseBuffer()
}

// Finish finalizes parsing and returns any remaining events
func (p *AwsEventStreamParser) Finish() []StreamEvent {
	var events []StreamEvent

	events = append(events, p.parseBuffer()...)

	if p.messageStopped {
		p.buffer = nil
		p.toolAccumulators = make(map[string]*toolAccumulator)
		p.textBlockIndex = nil
		p.inTextBlock = false
		return events
	}

	// Close all unclosed tool blocks
	for toolID, acc := range p.toolAccumulators {
		if acc.started {
			events = append(events,
				StreamEvent{Type: EventToolUseStop, ToolID: toolID},
				StreamEvent{Type: EventContentBlockStop, Index: acc.blockIndex},
			)
		}
	}
	p.toolAccumulators = make(map[string]*toolAccumulator)

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
		}
		events = append(events, StreamEvent{Type: EventMessageStop, StopReason: stopReason})
		p.messageStopped = true
	}

	p.buffer = nil
	return events
}

func (p *AwsEventStreamParser) parseBuffer() []StreamEvent {
	var events []StreamEvent
	pos := 0

	for pos < len(p.buffer) {
		start := p.findJSONStart(pos)
		if start < 0 {
			break
		}

		jsonBytes, endPos, ok := extractJSONObject(p.buffer, start)
		if !ok {
			break
		}

		parsed, err := p.parseJSONEvent(jsonBytes)
		if err != nil {
			p.parseErrorCount++
			events = append(events, StreamEvent{Type: EventError, ErrorType: "parse_error", ErrorMessage: err.Error()})
		} else {
			events = append(events, parsed...)
		}

		pos = endPos
	}

	if pos > 0 {
		p.buffer = p.buffer[pos:]
	}

	return events
}

var jsonStartPatterns = [][]byte{
	[]byte(`{"content":`),
	[]byte(`{"name":`),
	[]byte(`{"toolUseId":`),
	[]byte(`{"input":`),
	[]byte(`{"stop":`),
	[]byte(`{"usage":`),
	[]byte(`{"contextUsagePercentage":`),
	[]byte(`{"followupPrompt":`),
	[]byte(`{"meteringEvent":`),
	[]byte(`{"contextUsageEvent":`),
	[]byte(`{"unit":`),
}

func (p *AwsEventStreamParser) findJSONStart(from int) int {
	if from < 0 || from >= len(p.buffer) {
		return -1
	}

	buf := p.buffer[from:]
	best := -1
	for _, pat := range jsonStartPatterns {
		idx := bytes.Index(buf, pat)
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best {
			best = idx
		}
	}
	if best < 0 {
		return -1
	}
	return from + best
}

func extractJSONObject(buf []byte, start int) ([]byte, int, bool) {
	if start < 0 || start >= len(buf) || buf[start] != '{' {
		return nil, 0, false
	}

	braceCount := 0
	inString := false
	escapeNext := false

	for i := start; i < len(buf); i++ {
		b := buf[i]
		if escapeNext {
			escapeNext = false
			continue
		}

		if inString {
			if b == '\\' {
				escapeNext = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{':
			braceCount++
		case '}':
			braceCount--
			if braceCount == 0 {
				end := i + 1
				return buf[start:end], end, true
			}
		}
	}

	return nil, 0, false
}

func (p *AwsEventStreamParser) parseJSONEvent(jsonBytes []byte) ([]StreamEvent, error) {
	var payload awsPayload
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		return nil, err
	}

	var events []StreamEvent

	if !p.messageStarted {
		p.messageStarted = true
		events = append(events, StreamEvent{Type: EventMessageStart, MessageID: p.messageID, Model: p.model})
	}

	// 1) content text delta (skip followupPrompt)
	if payload.Content != nil {
		if len(payload.FollowupPrompt) == 0 || string(payload.FollowupPrompt) == "null" {
			if !p.inTextBlock {
				p.inTextBlock = true
				idx := p.nextIndex()
				p.textBlockIndex = &idx
				events = append(events, StreamEvent{
					Type:  EventContentBlockStart,
					Index: idx,
					BlockType: ContentBlockType{
						Kind: BlockText,
					},
				})
			}

			events = append(events, StreamEvent{Type: EventTextDelta, Text: *payload.Content})
		}
		return events, nil
	}

	// 2) toolUseId tool event
	if payload.ToolUseID != nil {
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
			events = append(events, StreamEvent{Type: EventToolUseInputDelta, ToolID: toolID, PartialJSON: *payload.Input})
		}

		isStop := payload.Stop != nil && *payload.Stop
		if isStop {
			delete(p.toolAccumulators, toolID)
			events = append(events,
				StreamEvent{Type: EventToolUseStop, ToolID: toolID},
				StreamEvent{Type: EventContentBlockStop, Index: acc.blockIndex},
			)
		}

		return events, nil
	}

	// 3) Standalone input event (no toolUseId)
	if payload.Input != nil {
		input := *payload.Input
		if input == "" {
			return nil, nil
		}

		// Select any active tool call (usually only one)
		for toolID, acc := range p.toolAccumulators {
			if !acc.started {
				// Fallback: even without explicit start, add start to ensure downstream has index
				acc.started = true
				acc.name = "unknown"
				acc.blockIndex = p.nextIndex()
				p.sawToolUse = true

				events = append(events,
					StreamEvent{Type: EventContentBlockStart, Index: acc.blockIndex, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: toolID, ToolName: acc.name}},
					StreamEvent{Type: EventToolUseStart, ToolID: toolID, ToolName: acc.name},
				)
			}

			events = append(events, StreamEvent{Type: EventToolUseInputDelta, ToolID: toolID, PartialJSON: input})
			return events, nil
		}

		// No active tool: ignore
		return nil, nil
	}

	// 4) stop event (no toolUseId)
	if payload.Stop != nil && *payload.Stop {
		for toolID, acc := range p.toolAccumulators {
			if acc.started {
				events = append(events,
					StreamEvent{Type: EventToolUseStop, ToolID: toolID},
					StreamEvent{Type: EventContentBlockStop, Index: acc.blockIndex},
				)
			}
		}
		p.toolAccumulators = make(map[string]*toolAccumulator)

		if p.textBlockIndex != nil {
			idx := *p.textBlockIndex
			p.textBlockIndex = nil
			p.inTextBlock = false
			events = append(events, StreamEvent{Type: EventContentBlockStop, Index: idx})
		}

		stopReason := StopReasonEndTurn
		if p.sawToolUse {
			stopReason = StopReasonToolUse
		}
		events = append(events, StreamEvent{Type: EventMessageStop, StopReason: stopReason})
		p.messageStopped = true
		return events, nil
	}

	// 5) usage / context usage (nested and flat)
	credits := 0.0
	ctxPct := 0.0

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

	if credits > 0 || ctxPct > 0 {
		events = append(events, StreamEvent{Type: EventBackendUsage, Credits: credits, ContextPercentage: ctxPct})
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
func BuildClaudeMessageStart(messageID string, model string, inputTokens int) ClaudeSSEEvent {
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
				"usage": map[string]any{
					"input_tokens":  inputTokens,
					"output_tokens": 0,
				},
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
func BuildClaudeMessageDelta(stopReason string, outputTokens, inputTokens int) ClaudeSSEEvent {
	return ClaudeSSEEvent{
		EventType: "message_delta",
		Data: map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"output_tokens": outputTokens,
				"input_tokens":  inputTokens,
			},
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
	toolIDToBlockIndex       map[string]uint32
	blockIndexToToolID       map[uint32]string

	sawToolUse        bool
	totalOutputTokens int
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

// SawToolUse returns whether any tool use was seen
func (c *StreamEventConverter) SawToolUse() bool {
	return c.sawToolUse
}

// TotalOutputTokens returns the estimated output tokens
func (c *StreamEventConverter) TotalOutputTokens() int {
	return c.totalOutputTokens
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
	switch e.BlockType.Kind {
	case BlockText:
		idx := e.Index
		c.activeTextBlockIndex = &idx

	case BlockToolUse:
		c.sawToolUse = true
		c.toolIDToBlockIndex[e.BlockType.ToolID] = e.Index
		c.blockIndexToToolID[e.Index] = e.BlockType.ToolID

	case BlockThinking:
		idx := e.Index
		c.activeThinkingBlockIndex = &idx
	}

	return []ClaudeSSEEvent{BuildClaudeContentBlockStart(int(e.Index), e.BlockType)}
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
	if c.activeTextBlockIndex != nil && *c.activeTextBlockIndex == e.Index {
		c.activeTextBlockIndex = nil
	}
	if c.activeThinkingBlockIndex != nil && *c.activeThinkingBlockIndex == e.Index {
		c.activeThinkingBlockIndex = nil
	}
	if toolID, ok := c.blockIndexToToolID[e.Index]; ok {
		delete(c.blockIndexToToolID, e.Index)
		delete(c.toolIDToBlockIndex, toolID)
	}

	return []ClaudeSSEEvent{BuildClaudeContentBlockStop(int(e.Index))}
}

// BuildInitialEvents builds the initial SSE events for a stream
func (c *StreamEventConverter) BuildInitialEvents() []ClaudeSSEEvent {
	return []ClaudeSSEEvent{
		BuildClaudeMessageStart(c.messageID, c.model, c.inputTokens),
		BuildClaudePing(),
	}
}

// BuildFinalEvents builds the final SSE events for a stream
func (c *StreamEventConverter) BuildFinalEvents() []ClaudeSSEEvent {
	stopReason := "end_turn"
	if c.sawToolUse {
		stopReason = "tool_use"
	}

	outputTokens := c.totalOutputTokens
	if outputTokens < 1 {
		outputTokens = 1
	}

	return []ClaudeSSEEvent{
		BuildClaudeMessageDelta(stopReason, outputTokens, c.inputTokens),
		BuildClaudeMessageStop(),
	}
}

// ==================== Non-Streaming Response Builder ====================

// CompleteResponse represents a parsed complete response
type CompleteResponse struct {
	Text      string
	ToolCalls []ToolCallData
}

// ToolCallData represents a tool call in the response
type ToolCallData struct {
	ID           string
	Name         string
	ArgumentsRaw string
}

// ParseCompleteResponse parses a complete (non-streaming) response
func ParseCompleteResponse(data []byte) *CompleteResponse {
	parser := NewAwsEventStreamParser("", "")
	events := parser.Process(data)
	events = append(events, parser.Finish()...)

	resp := &CompleteResponse{}
	var textParts []string
	toolInputs := make(map[string]string) // toolID -> accumulated input JSON

	for _, e := range events {
		switch e.Type {
		case EventTextDelta:
			textParts = append(textParts, e.Text)
		case EventContentBlockStart:
			if e.BlockType.Kind == BlockToolUse {
				resp.ToolCalls = append(resp.ToolCalls, ToolCallData{
					ID:   e.BlockType.ToolID,
					Name: e.BlockType.ToolName,
				})
			}
		case EventToolUseInputDelta:
			toolInputs[e.ToolID] += e.PartialJSON
		}
	}

	// Combine text parts
	for _, part := range textParts {
		resp.Text += part
	}

	// Assign accumulated inputs to tool calls
	for i := range resp.ToolCalls {
		if input, ok := toolInputs[resp.ToolCalls[i].ID]; ok {
			resp.ToolCalls[i].ArgumentsRaw = input
		}
	}

	return resp
}

// BuildClaudeNonStreamResponse builds a complete Claude response from parsed data
func BuildClaudeNonStreamResponse(messageID, model string, inputTokens int, resp *CompleteResponse) map[string]any {
	var content []map[string]any

	if resp.Text != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": resp.Text,
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

	// Estimate output tokens
	outputTokens := 0
	if resp.Text != "" {
		outputTokens += (len(resp.Text) + 3) / 4
	}
	for _, tool := range resp.ToolCalls {
		outputTokens += (len(tool.Name) + len(tool.ArgumentsRaw) + 3) / 4
	}
	if outputTokens < 1 && len(content) > 0 {
		outputTokens = 1
	}

	stopReason := "end_turn"
	if len(resp.ToolCalls) > 0 {
		stopReason = "tool_use"
	}

	return map[string]any{
		"id":            messageID,
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
}
