package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertResponsesToClaude accepts stateless Responses requests. History must be
// supplied in input because Kiro has no OpenAI response store or item lookup.
func ConvertResponsesToClaude(body []byte) (*ClaudeRequest, error) {
	req, _, err := ConvertResponsesToClaudeWithTools(body)
	return req, err
}

// ConvertResponsesToClaudeWithTools also reports the Codex-private tool shapes
// that were lowered to plain functions, so the response path can restore them.
func ConvertResponsesToClaudeWithTools(body []byte) (*ClaudeRequest, ResponsesClientTools, error) {
	request, tools, err := convertResponsesToClaude(body)
	if err != nil {
		return nil, ResponsesClientTools{}, err
	}
	return request, tools, nil
}

func convertResponsesToClaude(body []byte) (*ClaudeRequest, ResponsesClientTools, error) {
	var clientTools ResponsesClientTools
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, clientTools, fmt.Errorf("invalid JSON: %w", err)
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if value := req[field]; value != nil && value != "" {
			return nil, clientTools, fmt.Errorf("%s is not supported by Kiro; supply full history in input", field)
		}
	}
	for _, field := range []string{"store", "background"} {
		if value, exists := req[field]; exists && value != nil && value != false {
			return nil, clientTools, fmt.Errorf("Kiro requires %s=false", field)
		}
	}
	if text, ok := req["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && format["type"] != nil && format["type"] != "text" {
			return nil, clientTools, fmt.Errorf("structured text formats are not supported by Kiro")
		}
	}
	model, _ := req["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, clientTools, fmt.Errorf("model is required")
	}
	cc := map[string]any{"model": model}
	for _, field := range []string{"stream", "temperature", "thinking"} {
		if value, ok := req[field]; ok {
			cc[field] = value
		}
	}
	if stream, exists := req["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			return nil, clientTools, fmt.Errorf("stream must be a boolean")
		}
	}
	if value, exists := req["max_output_tokens"]; exists && value != nil {
		n, ok := value.(float64)
		if !ok || n <= 0 || n != float64(int(n)) {
			return nil, clientTools, fmt.Errorf("max_output_tokens must be a positive integer")
		}
		cc["max_completion_tokens"] = value
	}
	var messages []any
	if value := req["instructions"]; value != nil {
		instructions, ok := value.(string)
		if !ok {
			return nil, clientTools, fmt.Errorf("instructions must be a string")
		}
		if instructions != "" {
			messages = append(messages, map[string]any{"role": "system", "content": instructions})
		}
	}
	// Codex carries tool declarations inside input; promote them before the
	// item loop so they are validated with the top-level tools array.
	promoteResponsesAdditionalTools(req)
	var items []any
	switch input := req["input"].(type) {
	case string:
		if input != "" {
			items = []any{map[string]any{"role": "user", "content": input}}
		}
	case []any:
		items = input
	default:
		return nil, clientTools, fmt.Errorf("input must be a string or an array")
	}
	if len(items) == 0 {
		return nil, clientTools, fmt.Errorf("input must not be empty")
	}
	items = normalizeResponsesCodexItems(items)
	if len(items) == 0 {
		return nil, clientTools, fmt.Errorf("input must contain conversation messages or function calls")
	}
	for i, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, clientTools, fmt.Errorf("input[%d] must be an object", i)
		}
		kind, _ := item["type"].(string)
		switch kind {
		case "", "message":
			role, _ := item["role"].(string)
			if role == "developer" {
				role = "system"
			}
			if role != "system" && role != "user" && role != "assistant" {
				return nil, clientTools, fmt.Errorf("unsupported input role %q", role)
			}
			content, err := responsesContentToChat(item["content"])
			if err != nil {
				return nil, clientTools, fmt.Errorf("input[%d]: %w", i, err)
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		case "function_call":
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			var object map[string]any
			if callID == "" || name == "" || json.Unmarshal([]byte(args), &object) != nil || object == nil {
				return nil, clientTools, fmt.Errorf("function_call requires call_id, name and JSON object arguments")
			}
			messages = append(messages, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}})
		case "function_call_output":
			callID, _ := item["call_id"].(string)
			if callID == "" {
				return nil, clientTools, fmt.Errorf("function_call_output requires call_id")
			}
			content, err := responsesContentToChat(item["output"])
			if err != nil {
				return nil, clientTools, fmt.Errorf("function_call_output: %w", err)
			}
			// The shared Chat adapter only supports text tool results.
			if parts, ok := content.([]any); ok {
				for _, part := range parts {
					if part.(map[string]any)["type"] != "text" {
						return nil, clientTools, fmt.Errorf("Kiro function_call_output supports text only")
					}
				}
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": content})
		case "reasoning":
			// Client-replayed reasoning items are not conversation messages.
		default:
			return nil, clientTools, fmt.Errorf("unsupported input item type %q; supply full messages and function calls", kind)
		}
	}
	cc["messages"] = messages
	if value := req["tools"]; value != nil {
		tools, ok := value.([]any)
		if !ok {
			return nil, clientTools, fmt.Errorf("tools must be an array")
		}
		// Lower Codex local_shell/custom tools to functions and remember the
		// mapping; anything still non-function is rejected below.
		var lowerErr error
		tools, clientTools, lowerErr = lowerResponsesClientTools(tools)
		if lowerErr != nil {
			return nil, clientTools, lowerErr
		}
		converted := make([]any, 0, len(tools))
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || tool["type"] != "function" {
				return nil, clientTools, fmt.Errorf("Kiro Responses supports function tools only")
			}
			name, _ := tool["name"].(string)
			if name == "" {
				return nil, clientTools, fmt.Errorf("function tool name is required")
			}
			converted = append(converted, map[string]any{"type": "function", "function": tool})
		}
		cc["tools"] = converted
	}
	if value := req["tool_choice"]; value != nil {
		switch choice := value.(type) {
		case string:
			if choice != "auto" && choice != "none" && choice != "required" {
				return nil, clientTools, fmt.Errorf("unsupported tool_choice %q", choice)
			}
			cc["tool_choice"] = choice
		case map[string]any:
			name, _ := choice["name"].(string)
			// Codex selects its built-in shell by bare type, with no name.
			if kind, _ := choice["type"].(string); kind == localShellToolName && clientTools.LocalShell {
				name = localShellToolName
			} else if kind == "custom" && clientTools.Custom[name] {
				// A lowered freeform tool is now a function of the same name.
			} else if kind != "function" || name == "" {
				return nil, clientTools, fmt.Errorf("tool_choice must select a named function")
			}
			cc["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
		default:
			return nil, clientTools, fmt.Errorf("invalid tool_choice")
		}
	}
	if reasoning, ok := req["reasoning"].(map[string]any); ok {
		if effort, _ := reasoning["effort"].(string); effort != "" {
			budget := 0
			switch effort {
			case "none":
				cc["thinking"] = map[string]any{"type": "disabled"}
			case "minimal", "low":
				budget = 1024
			case "medium":
				budget = 4096
			case "high", "xhigh":
				budget = 16384
			default:
				return nil, clientTools, fmt.Errorf("unsupported reasoning effort %q", effort)
			}
			if budget > 0 {
				cc["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			}
		}
	}
	encoded, err := json.Marshal(cc)
	if err != nil {
		return nil, clientTools, err
	}
	result, err := ConvertOpenAIToClaude(encoded)
	if err != nil {
		return nil, clientTools, err
	}
	if len(result.Messages) == 0 {
		return nil, clientTools, fmt.Errorf("input must contain conversation messages or function calls")
	}
	return result, clientTools, nil
}

func responsesContentToChat(value any) (any, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("content must be a string or an array")
	}
	converted := make([]any, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("content part must be an object")
		}
		switch part["type"] {
		case "input_text", "output_text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("text part requires text")
			}
			converted = append(converted, map[string]any{"type": "text", "text": text})
		case "input_image":
			url, ok := part["image_url"].(string)
			if !ok || url == "" {
				return nil, fmt.Errorf("input_image requires image_url; file_id is not supported")
			}
			converted = append(converted, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		default:
			return nil, fmt.Errorf("unsupported content part type %v", part["type"])
		}
	}
	return converted, nil
}
