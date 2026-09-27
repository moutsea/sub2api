package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const kiroThinkingTTL = 24 * time.Hour

// KiroThinkingCache keeps the original Claude assistant turn across OpenAI
// Chat Completions tool calls. OpenAI's message shape cannot carry signatures.
type KiroThinkingCache interface {
	Put(ctx context.Context, key string, content []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, error)
}

type kiroThinkingTurn struct {
	Content []json.RawMessage `json:"content"`
}

func kiroThinkingKey(clientKey, model, toolID string) string {
	sum := sha256.Sum256([]byte(clientKey + "\x00" + model + "\x00" + toolID))
	return "kiro:thinking:" + hex.EncodeToString(sum[:])
}

func toolUsesFromContent(content []any) (map[string]map[string]any, error) {
	uses := make(map[string]map[string]any)
	for _, block := range content {
		m, ok := block.(map[string]any)
		if !ok || m["type"] != "tool_use" {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			return nil, errors.New("tool_use has no id")
		}
		uses[id] = m
	}
	return uses, nil
}

func (s *KiroGatewayService) restoreKiroThinkingTurn(ctx context.Context, clientKey string, req *kiro.ClaudeRequest) error {
	// Only the most recent assistant tool turn needs its thinking blocks echoed.
	// Earlier completed tool turns may omit them under Anthropic's API rules.
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "assistant" {
			continue
		}
		blocks, ok := req.Messages[i].Content.([]any)
		if !ok {
			return nil
		}
		uses, err := toolUsesFromContent(blocks)
		if err != nil || len(uses) == 0 {
			return err
		}
		// A completed assistant answer after a tool loop needs no restoration.
		if i == len(req.Messages)-1 {
			return nil
		}
		hasToolResult := false
		for _, message := range req.Messages[i+1:] {
			content, ok := message.Content.([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				if m, ok := block.(map[string]any); ok && m["type"] == "tool_result" {
					if id, ok := m["tool_use_id"].(string); ok && uses[id] != nil {
						hasToolResult = true
					}
				}
			}
		}
		if !hasToolResult {
			return nil
		}
		if s.thinkingCache == nil {
			return errors.New("Kiro thinking cache unavailable for Opus 5.5 tool continuation")
		}
		var cached []byte
		for id := range uses {
			candidate, getErr := s.thinkingCache.Get(ctx, kiroThinkingKey(clientKey, req.Model, id))
			if getErr != nil {
				return fmt.Errorf("load Opus 5.5 tool continuation: %w", getErr)
			}
			if len(candidate) > 0 {
				cached = candidate
				break
			}
		}
		if len(cached) == 0 {
			return errors.New("Opus 5.5 tool continuation expired; restart this tool turn")
		}
		var turn kiroThinkingTurn
		if err := json.Unmarshal(cached, &turn); err != nil {
			return fmt.Errorf("decode Opus 5.5 tool continuation: %w", err)
		}
		var original []any
		for _, raw := range turn.Content {
			var block map[string]any
			if err := json.Unmarshal(raw, &block); err != nil {
				return fmt.Errorf("decode Opus 5.5 content block: %w", err)
			}
			original = append(original, block)
		}
		storedUses, err := toolUsesFromContent(original)
		if err != nil || len(storedUses) != len(uses) {
			return errors.New("Opus 5.5 tool continuation does not match the prior response")
		}
		for id, requested := range uses {
			stored, ok := storedUses[id]
			if !ok || requested["name"] != stored["name"] || !reflect.DeepEqual(requested["input"], stored["input"]) {
				return errors.New("Opus 5.5 tool continuation was modified; restart this tool turn")
			}
		}
		req.Messages[i].Content = original
		return nil
	}
	return nil
}

func (s *KiroGatewayService) saveKiroThinkingTurn(ctx context.Context, clientKey, model string, content []json.RawMessage) error {
	if len(content) == 0 {
		return nil
	}
	var blocks []any
	for _, raw := range content {
		var block map[string]any
		if err := json.Unmarshal(raw, &block); err != nil {
			return err
		}
		blocks = append(blocks, block)
	}
	uses, err := toolUsesFromContent(blocks)
	if err != nil || len(uses) == 0 {
		return err
	}
	if s.thinkingCache == nil {
		return errors.New("Kiro thinking cache unavailable for Opus 5.5 tool continuation")
	}
	data, err := json.Marshal(kiroThinkingTurn{Content: content})
	if err != nil {
		return err
	}
	if len(data) > 10<<20 {
		return errors.New("Opus 5.5 tool continuation exceeds cache limit")
	}
	for id := range uses {
		if err := s.thinkingCache.Put(ctx, kiroThinkingKey(clientKey, model, id), data, kiroThinkingTTL); err != nil {
			return err
		}
	}
	return nil
}

// kiroClaudeContentCollector reconstructs the exact Claude content blocks
// needed when a streaming tool call is continued in the next OpenAI request.
type kiroClaudeContentCollector struct {
	blocks   map[int]map[string]any
	partials map[int]string
}

func (c *kiroClaudeContentCollector) Add(data []byte) error {
	var event struct {
		Type         string         `json:"type"`
		Index        int            `json:"index"`
		ContentBlock map[string]any `json:"content_block"`
		Delta        map[string]any `json:"delta"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if c.blocks == nil {
		c.blocks = make(map[int]map[string]any)
		c.partials = make(map[int]string)
	}
	switch event.Type {
	case "content_block_start":
		c.blocks[event.Index] = event.ContentBlock
	case "content_block_delta":
		block := c.blocks[event.Index]
		if block == nil {
			return fmt.Errorf("Claude content block %d delta without start", event.Index)
		}
		switch event.Delta["type"] {
		case "text_delta":
			block["text"] = stringValue(block["text"]) + stringValue(event.Delta["text"])
		case "thinking_delta":
			block["thinking"] = stringValue(block["thinking"]) + stringValue(event.Delta["thinking"])
		case "signature_delta":
			block["signature"] = stringValue(block["signature"]) + stringValue(event.Delta["signature"])
		case "input_json_delta":
			c.partials[event.Index] += stringValue(event.Delta["partial_json"])
		}
	case "content_block_stop":
		if partial := c.partials[event.Index]; partial != "" {
			var input any
			if err := json.Unmarshal([]byte(partial), &input); err != nil {
				return fmt.Errorf("decode Claude tool input: %w", err)
			}
			c.blocks[event.Index]["input"] = input
			delete(c.partials, event.Index)
		}
	}
	return nil
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func (c *kiroClaudeContentCollector) Content() ([]json.RawMessage, error) {
	indices := make([]int, 0, len(c.blocks))
	for index := range c.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	content := make([]json.RawMessage, 0, len(indices))
	for _, index := range indices {
		block, err := json.Marshal(c.blocks[index])
		if err != nil {
			return nil, err
		}
		content = append(content, block)
	}
	return content, nil
}
