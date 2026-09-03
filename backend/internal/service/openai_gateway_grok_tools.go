package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

const grokResponsesClientToolMappingContextKey = "grok_responses_client_tool_mapping"
const grokResponsesClientToolStreamStateContextKey = "grok_responses_client_tool_stream_state"

type grokNamespaceTool struct {
	Namespace string
	Name      string
}

type grokResponsesClientToolMapping struct {
	CustomTools    map[string]bool
	ToolSearch     bool
	NamespaceTools map[string]grokNamespaceTool
}

type grokClientToolStreamCall struct {
	kind      string
	name      string
	callID    string
	itemID    string
	output    int
	arguments strings.Builder
}

type grokClientToolStreamState struct {
	mapping grokResponsesClientToolMapping
	calls   map[string]*grokClientToolStreamCall
	byIndex map[int]*grokClientToolStreamCall
	nextSeq int
	seenSeq bool
}

func patchGrokResponsesBodyWithClientTools(body []byte, upstreamModel string) ([]byte, grokResponsesClientToolMapping, error) {
	lowered, mapping, err := lowerGrokResponsesClientTools(body)
	if err != nil {
		return nil, grokResponsesClientToolMapping{}, err
	}
	patched, err := patchGrokResponsesBody(lowered, upstreamModel)
	return patched, mapping, err
}

func lowerGrokResponsesClientTools(body []byte) ([]byte, grokResponsesClientToolMapping, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, grokResponsesClientToolMapping{}, fmt.Errorf("invalid json request body: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, grokResponsesClientToolMapping{}, fmt.Errorf("invalid json request body: multiple JSON values")
		}
		return nil, grokResponsesClientToolMapping{}, fmt.Errorf("invalid json request body: %w", err)
	}

	mapping := grokResponsesClientToolMapping{CustomTools: make(map[string]bool)}
	tools, _ := request["tools"].([]any)
	if promoted, err := promoteGrokToolSearchDiscoveries(request, tools); err != nil {
		return nil, grokResponsesClientToolMapping{}, err
	} else if len(promoted) > 0 {
		tools = append(tools, promoted...)
		request["tools"] = tools
	}
	functionNames := make(map[string]bool)
	customNames := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(grokStringValue(tool["name"]))
		switch strings.ToLower(strings.TrimSpace(grokStringValue(tool["type"]))) {
		case "function":
			if name != "" {
				functionNames[name] = true
			}
		case "custom":
			if name != "" {
				customNames[name] = true
			}
		case "tool_search":
			mapping.ToolSearch = true
		}
	}
	for name := range customNames {
		if functionNames[name] {
			return nil, grokResponsesClientToolMapping{}, fmt.Errorf("custom tool %q conflicts with a function tool of the same name", name)
		}
		mapping.CustomTools[name] = true
	}
	if mapping.ToolSearch && (functionNames["tool_search"] || customNames["tool_search"]) {
		return nil, grokResponsesClientToolMapping{}, fmt.Errorf("tool_search conflicts with a declared tool named tool_search")
	}

	var loweredTools []any
	if len(tools) > 0 {
		loweredTools = make([]any, 0, len(tools))
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.ToLower(strings.TrimSpace(grokStringValue(tool["type"])))
			name := strings.TrimSpace(grokStringValue(tool["name"]))
			switch typ {
			case "custom":
				if name == "" {
					continue
				}
				copyTool := cloneGrokMap(tool)
				copyTool["type"] = "function"
				copyTool["parameters"] = map[string]any{
					"type": "object", "properties": map[string]any{
						"input": map[string]any{"type": "string"},
					}, "required": []any{"input"}, "additionalProperties": false,
				}
				delete(copyTool, "format")
				loweredTools = append(loweredTools, copyTool)
			case "tool_search":
				if len(loweredTools) > 0 && hasGrokToolSearchFunction(loweredTools) {
					continue
				}
				loweredTools = append(loweredTools, map[string]any{
					"type": "function", "name": "tool_search",
					"description": "Search and load tools and connectors for the current task.",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{
						"query": map[string]any{"type": "string"},
						"limit": map[string]any{"type": "integer"},
					}, "required": []any{"query"}},
				})
			default:
				if typ == "namespace" {
					namespace := name
					children, _ := tool["tools"].([]any)
					if len(children) == 0 {
						children, _ = tool["children"].([]any)
					}
					for _, rawChild := range children {
						child, ok := rawChild.(map[string]any)
						if !ok || strings.ToLower(strings.TrimSpace(grokStringValue(child["type"]))) != "function" {
							continue
						}
						childName := strings.TrimSpace(grokStringValue(child["name"]))
						if namespace == "" || childName == "" {
							continue
						}
						flat := namespace + "__" + childName
						if functionNames[flat] {
							return nil, grokResponsesClientToolMapping{}, fmt.Errorf("namespace tool %q/%q conflicts with %q", namespace, childName, flat)
						}
						if previous, exists := mapping.NamespaceTools[flat]; exists && previous != (grokNamespaceTool{Namespace: namespace, Name: childName}) {
							return nil, grokResponsesClientToolMapping{}, fmt.Errorf("namespace tool %q conflicts with %q", flat, previous.Namespace+"/"+previous.Name)
						}
						if mapping.NamespaceTools == nil {
							mapping.NamespaceTools = make(map[string]grokNamespaceTool)
						}
						mapping.NamespaceTools[flat] = grokNamespaceTool{Namespace: namespace, Name: childName}
						flatTool := cloneGrokMap(child)
						flatTool["name"] = flat
						loweredTools = append(loweredTools, flatTool)
					}
					continue
				}
				loweredTools = append(loweredTools, raw)
			}
		}
		request["tools"] = loweredTools
	}
	if _, err := lowerGrokClientToolHistory(request["input"], &mapping); err != nil {
		return nil, grokResponsesClientToolMapping{}, err
	}
	if choice, ok := request["tool_choice"].(map[string]any); ok {
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(choice["type"])))
		name := strings.TrimSpace(grokStringValue(choice["name"]))
		switch typ {
		case "custom":
			if mapping.CustomTools[name] {
				choice["type"] = "function"
			}
		case "tool_search":
			if mapping.ToolSearch {
				request["tool_choice"] = map[string]any{"type": "function", "name": "tool_search"}
			}
		case "function":
			namespace := strings.TrimSpace(grokStringValue(choice["namespace"]))
			if namespace != "" && name != "" {
				flat := namespace + "__" + name
				if _, exists := mapping.NamespaceTools[flat]; exists {
					choice["name"] = flat
					delete(choice, "namespace")
				}
			}
		case "namespace":
			if namespace := strings.TrimSpace(grokStringValue(choice["name"])); namespace != "" {
				request["tool_choice"] = "auto"
			}
		}
	}
	if len(mapping.CustomTools) == 0 {
		mapping.CustomTools = nil
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, grokResponsesClientToolMapping{}, err
	}
	return encoded, mapping, nil
}

func lowerGrokClientToolHistory(value any, mapping *grokResponsesClientToolMapping) (bool, error) {
	changed := false
	var visitErr error
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				visit(child)
			}
		case map[string]any:
			typ := strings.ToLower(strings.TrimSpace(grokStringValue(node["type"])))
			switch typ {
			case "custom_tool_call":
				name := strings.TrimSpace(grokStringValue(node["name"]))
				if name != "" {
					mapping.CustomTools[name] = true
				}
				node["type"] = "function_call"
				input := grokStringValue(node["input"])
				if input == "" && node["input"] != nil {
					input = grokJSONValueString(node["input"])
				}
				encoded, _ := json.Marshal(map[string]string{"input": input})
				node["arguments"] = string(encoded)
				normalizeGrokFunctionItemID(node)
				delete(node, "input")
				changed = true
			case "custom_tool_call_output":
				node["type"] = "function_call_output"
				normalizeGrokToolOutputValue(node)
				changed = true
			case "tool_search_call":
				mapping.ToolSearch = true
				node["type"] = "function_call"
				node["name"] = "tool_search"
				node["arguments"] = grokJSONValueStringOrObject(node["arguments"])
				normalizeGrokFunctionItemID(node)
				delete(node, "execution")
				changed = true
			case "tool_search_output":
				mapping.ToolSearch = true
				if strings.TrimSpace(grokStringValue(node["call_id"])) == "" {
					visitErr = fmt.Errorf("tool_search_output requires a non-empty call_id")
					return
				}
				if _, hasOutput := node["output"]; !hasOutput {
					if _, hasTools := node["tools"]; !hasTools {
						visitErr = fmt.Errorf("tool_search_output requires output or tools")
						return
					}
				}
				node["type"] = "function_call_output"
				if _, exists := node["output"]; !exists {
					if tools, exists := node["tools"]; exists {
						node["output"] = grokJSONValueString(tools)
					}
				}
				normalizeGrokToolOutputValue(node)
				delete(node, "tools")
				delete(node, "execution")
				delete(node, "status")
				changed = true
			case "function_call":
				namespace := strings.TrimSpace(grokStringValue(node["namespace"]))
				name := strings.TrimSpace(grokStringValue(node["name"]))
				if namespace != "" && name != "" {
					flat := namespace + "__" + name
					if mapping.NamespaceTools == nil {
						mapping.NamespaceTools = make(map[string]grokNamespaceTool)
					}
					mapping.NamespaceTools[flat] = grokNamespaceTool{Namespace: namespace, Name: name}
					node["name"] = flat
					delete(node, "namespace")
					changed = true
				}
			}
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(value)
	return changed, visitErr
}

func hasGrokToolSearchFunction(tools []any) bool {
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(grokStringValue(tool["type"])), "function") && strings.EqualFold(strings.TrimSpace(grokStringValue(tool["name"])), "tool_search") {
			return true
		}
	}
	return false
}

func promoteGrokToolSearchDiscoveries(request map[string]any, tools []any) ([]any, error) {
	if len(tools) == 0 || !hasGrokToolSearchDeclaration(tools) {
		return nil, nil
	}
	input, ok := request["input"].([]any)
	if !ok {
		return nil, nil
	}
	known := make(map[string]string)
	for _, raw := range tools {
		registerGrokKnownTool(raw, known)
	}
	var promoted []any
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(grokStringValue(item["type"])), "tool_search_output") {
			continue
		}
		if status, exists := item["status"]; exists && !strings.EqualFold(strings.TrimSpace(grokStringValue(status)), "completed") {
			continue
		}
		discoveries, ok := item["tools"].([]any)
		if !ok {
			continue
		}
		for _, rawDiscovery := range discoveries {
			discovery, ok := rawDiscovery.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.ToLower(strings.TrimSpace(grokStringValue(discovery["type"])))
			switch typ {
			case "function", "custom":
				name := strings.TrimSpace(grokStringValue(discovery["name"]))
				if name == "" {
					continue
				}
				candidate := cloneGrokMap(discovery)
				canonical := cloneGrokMap(candidate)
				if typ == "custom" {
					canonical["type"] = "function"
					canonical["parameters"] = map[string]any{
						"type": "object", "properties": map[string]any{
							"input": map[string]any{"type": "string"},
						}, "required": []any{"input"}, "additionalProperties": false,
					}
					delete(canonical, "format")
				}
				encoded := grokJSONValueString(canonical)
				if previous, exists := known[name]; exists {
					if previous != encoded {
						return nil, fmt.Errorf("discovered tool %q conflicts with an existing declaration", name)
					}
					continue
				}
				known[name] = encoded
				promoted = append(promoted, candidate)
			case "namespace":
				namespace := strings.TrimSpace(grokStringValue(discovery["name"]))
				children, _ := discovery["tools"].([]any)
				if len(children) == 0 {
					children, _ = discovery["children"].([]any)
				}
				if namespace == "" || len(children) == 0 {
					continue
				}
				promotedChildren := make([]any, 0, len(children))
				for _, rawChild := range children {
					child, ok := rawChild.(map[string]any)
					if !ok || !strings.EqualFold(strings.TrimSpace(grokStringValue(child["type"])), "function") {
						continue
					}
					childName := strings.TrimSpace(grokStringValue(child["name"]))
					if childName == "" {
						continue
					}
					flat := namespace + "__" + childName
					candidate := cloneGrokMap(child)
					candidate["name"] = childName
					canonical := cloneGrokMap(candidate)
					canonical["name"] = flat
					encoded := grokJSONValueString(canonical)
					if previous, exists := known[flat]; exists {
						if previous != encoded {
							return nil, fmt.Errorf("discovered tool %q conflicts with an existing declaration", flat)
						}
						continue
					}
					known[flat] = encoded
					promotedChildren = append(promotedChildren, candidate)
				}
				if len(promotedChildren) > 0 {
					promoted = append(promoted, map[string]any{"type": "namespace", "name": namespace, "tools": promotedChildren})
				}
			}
		}
	}
	return promoted, nil
}

func registerGrokKnownTool(raw any, known map[string]string) {
	tool, ok := raw.(map[string]any)
	if !ok {
		return
	}
	typ := strings.ToLower(strings.TrimSpace(grokStringValue(tool["type"])))
	switch typ {
	case "function", "custom":
		name := strings.TrimSpace(grokStringValue(tool["name"]))
		if name == "" {
			return
		}
		canonical := cloneGrokMap(tool)
		if typ == "custom" {
			canonical["type"] = "function"
			canonical["parameters"] = map[string]any{
				"type": "object", "properties": map[string]any{
					"input": map[string]any{"type": "string"},
				}, "required": []any{"input"}, "additionalProperties": false,
			}
			delete(canonical, "format")
		}
		known[name] = grokJSONValueString(canonical)
	case "namespace":
		namespace := strings.TrimSpace(grokStringValue(tool["name"]))
		children, _ := tool["tools"].([]any)
		if len(children) == 0 {
			children, _ = tool["children"].([]any)
		}
		if namespace == "" {
			return
		}
		for _, rawChild := range children {
			child, ok := rawChild.(map[string]any)
			if !ok || !strings.EqualFold(strings.TrimSpace(grokStringValue(child["type"])), "function") {
				continue
			}
			childName := strings.TrimSpace(grokStringValue(child["name"]))
			if childName == "" {
				continue
			}
			canonical := cloneGrokMap(child)
			canonical["name"] = namespace + "__" + childName
			known[namespace+"__"+childName] = grokJSONValueString(canonical)
		}
	}
}

func hasGrokToolSearchDeclaration(tools []any) bool {
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(grokStringValue(tool["type"])), "tool_search") {
			return true
		}
	}
	return false
}

func normalizeGrokToolOutputValue(node map[string]any) {
	if output, exists := node["output"]; exists {
		if _, ok := output.(string); !ok {
			node["output"] = grokJSONValueString(output)
		}
	}
}

func normalizeGrokFunctionItemID(node map[string]any) {
	id := strings.TrimSpace(grokStringValue(node["id"]))
	if id == "" || strings.HasPrefix(id, "fc_") {
		return
	}
	for _, prefix := range []string{"ctc_", "tsc_"} {
		if strings.HasPrefix(id, prefix) {
			node["id"] = "fc_" + strings.TrimPrefix(id, prefix)
			return
		}
	}
	delete(node, "id")
}

func grokJSONValueString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return ""
	}
	return string(encoded)
}

func grokJSONValueStringOrObject(value any) string {
	if value == nil {
		return "{}"
	}
	return grokJSONValueString(value)
}

func cloneGrokMap(source map[string]any) map[string]any {
	copyValue := make(map[string]any, len(source))
	for key, value := range source {
		copyValue[key] = value
	}
	return copyValue
}

func setGrokResponsesClientToolMapping(c *gin.Context, mapping grokResponsesClientToolMapping) {
	if c == nil {
		return
	}
	if len(mapping.CustomTools) == 0 && !mapping.ToolSearch && len(mapping.NamespaceTools) == 0 {
		c.Set(grokResponsesClientToolMappingContextKey, grokResponsesClientToolMapping{})
		return
	}
	c.Set(grokResponsesClientToolMappingContextKey, mapping)
	c.Set(grokResponsesClientToolStreamStateContextKey, &grokClientToolStreamState{
		mapping: mapping, calls: make(map[string]*grokClientToolStreamCall), byIndex: make(map[int]*grokClientToolStreamCall),
	})
}

func clearGrokResponsesClientToolMapping(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(grokResponsesClientToolMappingContextKey, grokResponsesClientToolMapping{})
	c.Set(grokResponsesClientToolStreamStateContextKey, (*grokClientToolStreamState)(nil))
}

func grokResponsesClientToolMappingForContext(c *gin.Context) (grokResponsesClientToolMapping, bool) {
	if c == nil {
		return grokResponsesClientToolMapping{}, false
	}
	value, ok := c.Get(grokResponsesClientToolMappingContextKey)
	if !ok {
		return grokResponsesClientToolMapping{}, false
	}
	mapping, ok := value.(grokResponsesClientToolMapping)
	return mapping, ok && (len(mapping.CustomTools) > 0 || mapping.ToolSearch || len(mapping.NamespaceTools) > 0)
}

func restoreGrokResponsesClientToolPayload(c *gin.Context, payload []byte) ([]byte, error) {
	mapping, ok := grokResponsesClientToolMappingForContext(c)
	if !ok || !json.Valid(payload) {
		return payload, nil
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return payload, err
	}
	if restoreGrokClientToolValue(value, mapping) {
		return json.Marshal(value)
	}
	return payload, nil
}

func restoreGrokClientToolValue(value any, mapping grokResponsesClientToolMapping) bool {
	changed := false
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			changed = restoreGrokClientToolValue(child, mapping) || changed
		}
	case map[string]any:
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(node["type"])))
		if typ == "function_call" {
			name := strings.TrimSpace(grokStringValue(node["name"]))
			if mapping.CustomTools[name] {
				node["type"] = "custom_tool_call"
				if id := strings.TrimSpace(grokStringValue(node["id"])); strings.HasPrefix(id, "fc_") {
					node["id"] = "ctc_" + strings.TrimPrefix(id, "fc_")
				}
				node["input"] = extractGrokCustomInput(grokStringValue(node["arguments"]))
				delete(node, "arguments")
				delete(node, "namespace")
				changed = true
			} else if mapping.ToolSearch && name == "tool_search" {
				node["type"] = "tool_search_call"
				if id := strings.TrimSpace(grokStringValue(node["id"])); strings.HasPrefix(id, "fc_") {
					node["id"] = "tsc_" + strings.TrimPrefix(id, "fc_")
				}
				node["execution"] = "client"
				if args, err := decodeGrokJSONValue(node["arguments"]); err == nil {
					node["arguments"] = args
				}
				delete(node, "name")
				delete(node, "namespace")
				changed = true
			} else if entry, exists := mapping.NamespaceTools[name]; exists {
				node["namespace"] = entry.Namespace
				node["name"] = entry.Name
				changed = true
			}
		}
		for _, child := range node {
			changed = restoreGrokClientToolValue(child, mapping) || changed
		}
	}
	return changed
}

func extractGrokCustomInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var object map[string]any
	if json.Unmarshal([]byte(arguments), &object) == nil {
		if input, ok := object["input"].(string); ok {
			return input
		}
	}
	return arguments
}

func decodeGrokJSONValue(value any) (any, error) {
	if text, ok := value.(string); ok {
		var decoded any
		err := json.Unmarshal([]byte(text), &decoded)
		return decoded, err
	}
	return value, nil
}

func restoreGrokResponsesClientToolStreamPayload(c *gin.Context, payload []byte) ([][]byte, error) {
	mapping, ok := grokResponsesClientToolMappingForContext(c)
	if !ok || !json.Valid(payload) {
		return [][]byte{payload}, nil
	}
	state, _ := c.Get(grokResponsesClientToolStreamStateContextKey)
	restorer, _ := state.(*grokClientToolStreamState)
	if restorer == nil {
		restorer = &grokClientToolStreamState{mapping: mapping, calls: make(map[string]*grokClientToolStreamCall), byIndex: make(map[int]*grokClientToolStreamCall)}
		c.Set(grokResponsesClientToolStreamStateContextKey, restorer)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		return [][]byte{payload}, nil
	}
	typ := strings.TrimSpace(grokStringValue(event["type"]))
	outputIndex := int(grokNumberValue(event["output_index"]))
	itemID := strings.TrimSpace(grokStringValue(event["item_id"]))
	callID := strings.TrimSpace(grokStringValue(event["call_id"]))
	lookup := func() *grokClientToolStreamCall {
		if itemID != "" && restorer.calls[itemID] != nil {
			return restorer.calls[itemID]
		}
		if callID != "" && restorer.calls[callID] != nil {
			return restorer.calls[callID]
		}
		return restorer.byIndex[outputIndex]
	}
	item, _ := event["item"].(map[string]any)
	if item != nil && strings.EqualFold(grokStringValue(item["type"]), "function_call") {
		name := strings.TrimSpace(grokStringValue(item["name"]))
		kind := ""
		if mapping.CustomTools[name] {
			kind = "custom"
		} else if mapping.ToolSearch && name == "tool_search" {
			kind = "tool_search"
		}
		if entry, exists := mapping.NamespaceTools[name]; exists {
			item["name"] = entry.Name
			item["namespace"] = entry.Namespace
		}
		if kind != "" {
			call := lookup()
			if call == nil {
				call = &grokClientToolStreamCall{kind: kind, name: name, callID: strings.TrimSpace(grokStringValue(item["call_id"])), itemID: strings.TrimSpace(grokStringValue(item["id"])), output: outputIndex}
				restorer.calls[call.itemID] = call
				if call.callID != "" {
					restorer.calls[call.callID] = call
				}
				restorer.byIndex[call.output] = call
			}
			if typ == "response.output_item.added" {
				if kind == "custom" {
					item["type"] = "custom_tool_call"
					if id := strings.TrimSpace(grokStringValue(item["id"])); strings.HasPrefix(id, "fc_") {
						item["id"] = "ctc_" + strings.TrimPrefix(id, "fc_")
					}
					item["input"] = ""
					delete(item, "arguments")
				} else {
					item["type"] = "tool_search_call"
					if id := strings.TrimSpace(grokStringValue(item["id"])); strings.HasPrefix(id, "fc_") {
						item["id"] = "tsc_" + strings.TrimPrefix(id, "fc_")
					}
					item["execution"] = "client"
					delete(item, "name")
					item["arguments"] = map[string]any{}
				}
			} else if typ == "response.output_item.done" {
				args := strings.TrimSpace(call.arguments.String())
				if raw := strings.TrimSpace(grokStringValue(item["arguments"])); raw != "" {
					args = raw
				}
				if kind == "custom" {
					item["type"] = "custom_tool_call"
					if id := strings.TrimSpace(grokStringValue(item["id"])); strings.HasPrefix(id, "fc_") {
						item["id"] = "ctc_" + strings.TrimPrefix(id, "fc_")
					}
					item["input"] = extractGrokCustomInput(args)
					delete(item, "arguments")
				} else {
					item["type"] = "tool_search_call"
					if id := strings.TrimSpace(grokStringValue(item["id"])); strings.HasPrefix(id, "fc_") {
						item["id"] = "tsc_" + strings.TrimPrefix(id, "fc_")
					}
					item["execution"] = "client"
					item["arguments"], _ = decodeGrokJSONValue(args)
					delete(item, "name")
				}
				delete(restorer.calls, call.itemID)
				delete(restorer.calls, call.callID)
				delete(restorer.byIndex, call.output)
			}
		}
	}

	if typ == "response.function_call_arguments.delta" || typ == "response.function_call_arguments.done" {
		call := lookup()
		if call != nil && (call.kind == "custom" || call.kind == "tool_search") {
			if delta := grokStringValue(event["delta"]); delta != "" {
				_, _ = call.arguments.WriteString(delta)
			}
			if typ == "response.function_call_arguments.done" && grokStringValue(event["arguments"]) != "" {
				call.arguments.Reset()
				_, _ = call.arguments.WriteString(grokStringValue(event["arguments"]))
			}
			if call.kind == "custom" && typ == "response.function_call_arguments.done" {
				input := extractGrokCustomInput(call.arguments.String())
				outputs := []map[string]any{}
				sequence := event["sequence_number"]
				if input != "" {
					outputs = append(outputs, map[string]any{"type": "response.custom_tool_call_input.delta", "sequence_number": sequence, "output_index": outputIndex, "item_id": call.itemID, "delta": input})
				}
				outputs = append(outputs, map[string]any{"type": "response.custom_tool_call_input.done", "sequence_number": sequence, "output_index": outputIndex, "item_id": call.itemID, "call_id": call.callID, "name": call.name, "input": input})
				return restorer.sequence(outputs)
			}
			return nil, nil
		}
		if entry, exists := mapping.NamespaceTools[strings.TrimSpace(grokStringValue(event["name"]))]; exists {
			event["name"] = entry.Name
			delete(event, "namespace")
		}
	}
	if typ != "response.function_call_arguments.delta" && typ != "response.function_call_arguments.done" {
		restoreGrokClientToolValue(event, mapping)
	}
	return restorer.sequence([]map[string]any{event})
}

func grokNumberValue(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case json.Number:
		parsed, _ := number.Float64()
		return parsed
	default:
		return 0
	}
}

func (state *grokClientToolStreamState) sequence(events []map[string]any) ([][]byte, error) {
	if len(events) == 0 {
		return nil, nil
	}
	result := make([][]byte, 0, len(events))
	for _, event := range events {
		if sequence, ok := event["sequence_number"]; ok {
			if !state.seenSeq {
				state.nextSeq = int(grokNumberValue(sequence))
				state.seenSeq = true
			}
			event["sequence_number"] = state.nextSeq
			state.nextSeq++
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		result = append(result, encoded)
	}
	return result, nil
}
