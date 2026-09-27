package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesClientTools records the Codex-private shapes that were lowered to
// Claude function tools on the way in, so the outbound converter can restore
// the item types the client actually understands.
type ResponsesClientTools struct {
	// Custom holds freeform tool names whose arguments are a bare string
	// carried as {"input": "..."} rather than a JSON object.
	Custom map[string]bool
	// LocalShell is set when the client declared the built-in local_shell tool.
	LocalShell bool
}

// Restores reports whether any outbound item needs its type rewritten.
func (t ResponsesClientTools) Restores() bool {
	return t.LocalShell || len(t.Custom) > 0
}

// Kind returns the client-side item type for a lowered function name, or "" for
// tools that were genuine functions all along.
func (t ResponsesClientTools) Kind(name string) string {
	switch {
	case t.Custom[name]:
		return "custom"
	case t.LocalShell && name == localShellToolName:
		return "local_shell"
	default:
		return ""
	}
}

const localShellToolName = "local_shell"

// localShellSchema mirrors the Codex local_shell call contract. Claude tools
// need a declared schema, and the model must be told to emit an argv array.
var localShellSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command":                    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Command argv to execute."},
		"workdir":                    map[string]any{"type": "string", "description": "Working directory for the command."},
		"timeout_ms":                 map[string]any{"type": "integer", "description": "Timeout in milliseconds."},
		"with_escalated_permissions": map[string]any{"type": "boolean"},
		"justification":              map[string]any{"type": "string"},
	},
	"required": []any{"command"},
}

// customToolSchema wraps a freeform tool body in a single string field, which is
// the only way to express it over Claude's JSON-schema-only tool contract.
func customToolSchema(description string) map[string]any {
	if strings.TrimSpace(description) == "" {
		description = "Raw tool input."
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"input": map[string]any{"type": "string", "description": description}},
		"required":   []any{"input"},
	}
}

// lowerResponsesClientTools rewrites Codex tool declarations into plain function
// tools. Unknown hosted tools are left untouched so the caller still rejects
// them with an explicit error.
func lowerResponsesClientTools(tools []any) ([]any, ResponsesClientTools, error) {
	mapping := ResponsesClientTools{Custom: map[string]bool{}}
	functionNames := make(map[string]bool)
	customNames := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(responsesStringValue(tool["name"]))
		switch strings.ToLower(strings.TrimSpace(responsesStringValue(tool["type"]))) {
		case "function":
			if name != "" {
				functionNames[name] = true
			}
		case "custom":
			if name != "" {
				customNames[name] = true
			}
		case "local_shell":
			mapping.LocalShell = true
		}
	}
	for name := range customNames {
		if functionNames[name] {
			return nil, ResponsesClientTools{}, fmt.Errorf("custom tool %q conflicts with a function tool of the same name", name)
		}
	}
	if mapping.LocalShell && (functionNames[localShellToolName] || customNames[localShellToolName]) {
		return nil, ResponsesClientTools{}, fmt.Errorf("local_shell conflicts with a declared tool named %q", localShellToolName)
	}
	lowered := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			lowered = append(lowered, raw)
			continue
		}
		name := strings.TrimSpace(responsesStringValue(tool["name"]))
		switch strings.ToLower(strings.TrimSpace(responsesStringValue(tool["type"]))) {
		case "local_shell":
			lowered = append(lowered, map[string]any{
				"type": "function", "name": localShellToolName,
				"description": "Run a shell command on the user's machine.",
				"parameters":  localShellSchema,
			})
		case "custom":
			if name == "" {
				lowered = append(lowered, raw)
				continue
			}
			mapping.Custom[name] = true
			lowered = append(lowered, map[string]any{
				"type": "function", "name": name,
				"description": responsesStringValue(tool["description"]),
				"parameters":  customToolSchema(responsesStringValue(tool["description"])),
			})
		default:
			lowered = append(lowered, raw)
		}
	}
	if len(mapping.Custom) == 0 {
		mapping.Custom = nil
	}
	return lowered, mapping, nil
}

// promoteResponsesAdditionalTools strips the additional_tools carrier that Codex
// embeds in input and merges its tools into the top-level array, preserving
// order and dropping duplicates by type+name.
func promoteResponsesAdditionalTools(req map[string]any) {
	items, ok := req["input"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(items))
	var promoted []any
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(responsesStringValue(item["type"])), "additional_tools") {
			if tools, ok := item["tools"].([]any); ok {
				promoted = append(promoted, tools...)
			}
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) != len(items) {
		req["input"] = kept
	}
	if len(promoted) == 0 {
		return
	}
	existing, _ := req["tools"].([]any)
	req["tools"] = mergeResponsesTools(existing, promoted)
}

func mergeResponsesTools(existing, promoted []any) []any {
	merged := make([]any, 0, len(existing)+len(promoted))
	seen := make(map[string]struct{}, len(existing)+len(promoted))
	for _, raw := range append(append([]any{}, existing...), promoted...) {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := strings.TrimSpace(responsesStringValue(tool["type"])) + "\x00" + strings.TrimSpace(responsesStringValue(tool["name"]))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, tool)
	}
	return merged
}

// normalizeResponsesCodexItems rewrites Codex tool-call aliases to the
// function_call / function_call_output pair the adapter understands, and folds
// compaction items into a reasoning placeholder plus a summary message. Item
// order is preserved so replayed tool calls stay adjacent to their results.
func normalizeResponsesCodexItems(items []any) []any {
	converted := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			converted = append(converted, raw)
			continue
		}
		switch kind := strings.ToLower(strings.TrimSpace(responsesStringValue(item["type"]))); {
		case kind == "compaction" || kind == "compaction_summary":
			converted = append(converted, foldResponsesCompaction(item)...)
		case isResponsesCodexCallAlias(kind):
			converted = append(converted, normalizeResponsesCodexCall(item, kind))
		case isResponsesCodexOutputAlias(kind):
			converted = append(converted, normalizeResponsesCodexOutput(item))
		default:
			converted = append(converted, item)
		}
	}
	return converted
}

func isResponsesCodexCallAlias(kind string) bool {
	switch kind {
	case "local_shell_call", "custom_tool_call", "shell_call", "apply_patch_call":
		return true
	default:
		return false
	}
}

func isResponsesCodexOutputAlias(kind string) bool {
	switch kind {
	case "local_shell_call_output", "custom_tool_call_output", "shell_call_output", "apply_patch_call_output":
		return true
	default:
		return false
	}
}

// normalizeResponsesCodexCall converts a replayed Codex call into a
// function_call. Freeform input becomes {"input": "..."} so it round-trips
// through the JSON-object arguments the Claude adapter requires.
func normalizeResponsesCodexCall(item map[string]any, kind string) map[string]any {
	name := strings.TrimSpace(responsesStringValue(item["name"]))
	arguments := strings.TrimSpace(responsesStringValue(item["arguments"]))
	if kind == "local_shell_call" || kind == "shell_call" {
		name = localShellToolName
		// Codex carries the argv under action, not arguments.
		if action, ok := item["action"].(map[string]any); ok {
			if encoded, err := json.Marshal(action); err == nil {
				arguments = string(encoded)
			}
		}
	}
	// apply_patch_call and shell_call identify the tool by item type, not name.
	if name == "" && kind == "apply_patch_call" {
		name = "apply_patch"
	}
	if kind == "custom_tool_call" {
		input := responsesStringValue(item["input"])
		if encoded, err := json.Marshal(map[string]string{"input": input}); err == nil {
			arguments = string(encoded)
		}
	}
	if arguments == "" || !json.Valid([]byte(arguments)) {
		arguments = "{}"
	}
	return map[string]any{
		"type": "function_call", "name": name, "arguments": arguments,
		"call_id": responsesCallID(item),
	}
}

func normalizeResponsesCodexOutput(item map[string]any) map[string]any {
	output := item["output"]
	if output == nil {
		output = item["result"]
	}
	if output == nil {
		output = item["content"]
	}
	return map[string]any{
		"type": "function_call_output", "call_id": responsesCallID(item),
		"output": responsesOutputText(output),
	}
}

func responsesCallID(item map[string]any) string {
	for _, field := range []string{"call_id", "id"} {
		if value := strings.TrimSpace(responsesStringValue(item[field])); value != "" {
			return value
		}
	}
	return ""
}

// responsesOutputText flattens a Codex tool result to text. Structured results
// are re-encoded rather than dropped so the model still sees the payload.
func responsesOutputText(value any) any {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["output"].(string); ok {
			return text
		}
		if text, ok := typed["text"].(string); ok {
			return text
		}
	case []any:
		// Content-part arrays are already in the shape the adapter accepts.
		if len(typed) > 0 {
			if _, ok := typed[0].(map[string]any); ok {
				return typed
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// foldResponsesCompaction mirrors the Grok path: keep the encrypted blob as an
// inert reasoning item and surface the summary as tagged user text.
func foldResponsesCompaction(item map[string]any) []any {
	var folded []any
	if encrypted := strings.TrimSpace(responsesStringValue(item["encrypted_content"])); encrypted != "" {
		folded = append(folded, map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": encrypted})
	}
	if summary := responsesSummaryText(item["summary"]); summary != "" {
		folded = append(folded, map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "<conversation_summary>\n" + summary + "\n</conversation_summary>"},
		}})
	}
	return folded
}

func responsesSummaryText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		var parts []string
		for _, raw := range typed {
			switch part := raw.(type) {
			case string:
				if text := strings.TrimSpace(part); text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				if text := strings.TrimSpace(responsesStringValue(part["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func responsesStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		return ""
	}
}
