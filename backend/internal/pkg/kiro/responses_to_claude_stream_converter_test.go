package kiro

import (
	"strings"
	"testing"
)

func TestResponsesStreamConverter_ItemIDDoneAdvancesIndex(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	ev1 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"item":{"type":"function_call","call_id":"call_read","name":"Read"}}`))
	ev2 := conv.ConvertResponsesEvent("response.function_call_arguments.delta", []byte(`{"item_id":"fc_1","delta":"{\"file_path\":\"run-codex-register.sh\"}"}`))
	ev3 := conv.ConvertResponsesEvent("response.function_call_arguments.done", []byte(`{"item_id":"fc_1","arguments":"{\"file_path\":\"run-codex-register.sh\"}"}`))
	ev4 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"item":{"type":"function_call","call_id":"call_edit","name":"Edit"}}`))

	if !strings.Contains(ev1, `"index":0`) {
		t.Fatalf("first tool_use should start at index=0, got: %s", ev1)
	}
	if !strings.Contains(ev2, `"index":0`) {
		t.Fatalf("first tool delta should target index=0, got: %s", ev2)
	}
	if !strings.Contains(ev3, `"type":"content_block_stop"`) || !strings.Contains(ev3, `"index":0`) {
		t.Fatalf("first tool done should stop index=0, got: %s", ev3)
	}
	if !strings.Contains(ev4, `"index":1`) {
		t.Fatalf("second tool_use should start at index=1 after done, got: %s", ev4)
	}
}

func TestResponsesStreamConverter_DoneCarriesArgumentsWithoutDelta(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	_ = conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"item":{"type":"function_call","call_id":"call_edit","name":"Edit"}}`))
	evDone := conv.ConvertResponsesEvent("response.function_call_arguments.done", []byte(`{"item_id":"fc_1","arguments":"{\"file_path\":\"a.txt\",\"old_string\":\"x\",\"new_string\":\"y\"}"}`))

	if !strings.Contains(evDone, `"type":"input_json_delta"`) {
		t.Fatalf("done with arguments should emit input_json_delta, got: %s", evDone)
	}
	if !strings.Contains(evDone, `"partial_json":"{\"file_path\":\"a.txt\",\"old_string\":\"x\",\"new_string\":\"y\"}"`) {
		t.Fatalf("done should preserve full arguments payload, got: %s", evDone)
	}
	if !strings.Contains(evDone, `"type":"content_block_stop"`) {
		t.Fatalf("done should also close tool block, got: %s", evDone)
	}
}

func TestResponsesStreamConverter_OutputItemDoneClosesBlock(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	ev1 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_read","name":"Read"}}`))
	ev2 := conv.ConvertResponsesEvent("response.output_item.done", []byte(`{"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_read","name":"Read","arguments":"{\"file_path\":\"a.txt\"}"}}`))
	ev3 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_edit","name":"Edit"}}`))

	if !strings.Contains(ev1, `"index":0`) {
		t.Fatalf("first start should be index=0, got: %s", ev1)
	}
	if !strings.Contains(ev2, `"type":"content_block_stop"`) || !strings.Contains(ev2, `"index":0`) {
		t.Fatalf("output_item.done should close index=0, got: %s", ev2)
	}
	if !strings.Contains(ev3, `"index":1`) {
		t.Fatalf("second start should advance to index=1, got: %s", ev3)
	}
}

func TestResponsesStreamConverter_InterleavingToolCalls(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	evAdd1 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_read","name":"Read"}}`))
	evAdd2 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_edit","name":"Edit"}}`))
	evDelta2 := conv.ConvertResponsesEvent("response.function_call_arguments.delta", []byte(`{"item_id":"fc_2","delta":"{\"file_path\":\"a.txt\",\"old_string\":\"x\"}"}`))
	evDelta1 := conv.ConvertResponsesEvent("response.function_call_arguments.delta", []byte(`{"item_id":"fc_1","delta":"{\"file_path\":\"a.txt\"}"}`))
	evDone1 := conv.ConvertResponsesEvent("response.function_call_arguments.done", []byte(`{"item_id":"fc_1","arguments":"{\"file_path\":\"a.txt\"}"}`))
	evDone2 := conv.ConvertResponsesEvent("response.function_call_arguments.done", []byte(`{"item_id":"fc_2","arguments":"{\"file_path\":\"a.txt\",\"old_string\":\"x\",\"new_string\":\"y\"}"}`))

	if !strings.Contains(evAdd1, `"index":0`) {
		t.Fatalf("first added call should start index=0, got: %s", evAdd1)
	}
	if !strings.Contains(evAdd2, `"index":1`) {
		t.Fatalf("second added call should start index=1, got: %s", evAdd2)
	}
	if strings.Contains(evAdd2, `"type":"content_block_stop"`) {
		t.Fatalf("second added call should not force-close previous tool block, got: %s", evAdd2)
	}
	if !strings.Contains(evDelta2, `"index":1`) {
		t.Fatalf("delta for fc_2 should target index=1, got: %s", evDelta2)
	}
	if !strings.Contains(evDelta1, `"index":0`) {
		t.Fatalf("delta for fc_1 should target index=0, got: %s", evDelta1)
	}
	if !strings.Contains(evDone1, `"index":0`) {
		t.Fatalf("done for fc_1 should stop index=0, got: %s", evDone1)
	}
	if !strings.Contains(evDone2, `"index":1`) {
		t.Fatalf("done for fc_2 should stop index=1, got: %s", evDone2)
	}
}

func TestResponsesStreamConverter_ToolUseIDFallbackWhenMissing(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	ev1 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":0,"item":{"type":"function_call","name":"Read"}}`))
	ev2 := conv.ConvertResponsesEvent("response.output_item.added", []byte(`{"output_index":1,"item":{"type":"function_call","name":"Search"}}`))

	if strings.Contains(ev1, `"id":""`) {
		t.Fatalf("first tool_use id should not be empty, got: %s", ev1)
	}
	if strings.Contains(ev2, `"id":""`) {
		t.Fatalf("second tool_use id should not be empty, got: %s", ev2)
	}
	if !strings.Contains(ev1, `"index":0`) {
		t.Fatalf("first tool_use should be index=0, got: %s", ev1)
	}
	if !strings.Contains(ev2, `"index":1`) {
		t.Fatalf("second tool_use should be index=1, got: %s", ev2)
	}
}

func TestResponsesStreamConverter_MessageDeltaIncludesInputTokens(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	evCompleted := conv.ConvertResponsesEvent("response.completed", []byte(`{"response":{"status":"completed","usage":{"input_tokens":123,"output_tokens":45}}}`))

	if !strings.Contains(evCompleted, `"type":"message_delta"`) {
		t.Fatalf("completed should emit message_delta, got: %s", evCompleted)
	}
	if !strings.Contains(evCompleted, `"input_tokens":123`) {
		t.Fatalf("message_delta usage should include input_tokens, got: %s", evCompleted)
	}
	if !strings.Contains(evCompleted, `"output_tokens":45`) {
		t.Fatalf("message_delta usage should include output_tokens, got: %s", evCompleted)
	}
}

func TestResponsesStreamConverter_MessageDeltaIncludesCacheTokens(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	evCompleted := conv.ConvertResponsesEvent("response.completed", []byte(`{"response":{"status":"completed","usage":{"input_tokens":123,"output_tokens":45,"input_tokens_details":{"cached_tokens":20}}}}`))

	if !strings.Contains(evCompleted, `"cache_read_input_tokens":20`) {
		t.Fatalf("message_delta usage should include cache_read_input_tokens, got: %s", evCompleted)
	}
	if !strings.Contains(evCompleted, `"input_tokens":103`) {
		t.Fatalf("input_tokens should be normalized (123-20), got: %s", evCompleted)
	}
}

func TestResponsesStreamConverter_ReasoningTextEvents(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	evDelta := conv.ConvertResponsesEvent("response.reasoning_text.delta", []byte(`{"delta":"Let me think..."}`))
	evDone := conv.ConvertResponsesEvent("response.reasoning_text.done", []byte(`{}`))

	if !strings.Contains(evDelta, `"type":"thinking_delta"`) {
		t.Fatalf("reasoning_text.delta should emit thinking_delta, got: %s", evDelta)
	}
	if !strings.Contains(evDelta, `"type":"content_block_start"`) {
		t.Fatalf("reasoning_text.delta should start thinking block, got: %s", evDelta)
	}
	if !strings.Contains(evDone, `"type":"content_block_stop"`) {
		t.Fatalf("reasoning_text.done should close thinking block, got: %s", evDone)
	}
	if !strings.Contains(evDone, `"type":"signature_delta"`) {
		t.Fatalf("reasoning_text.done should emit signature_delta before stop, got: %s", evDone)
	}
}

func TestResponsesStreamConverter_ReasoningSummaryTextEvents(t *testing.T) {
	conv := NewResponsesStreamConverter("claude-sonnet-4-20250514", "msg_test")

	evDelta := conv.ConvertResponsesEvent("response.reasoning_summary_text.delta", []byte(`{"delta":"summary..."}`))
	evDone := conv.ConvertResponsesEvent("response.reasoning_summary_text.done", []byte(`{}`))

	if !strings.Contains(evDelta, `"type":"thinking_delta"`) {
		t.Fatalf("reasoning_summary_text.delta should emit thinking_delta, got: %s", evDelta)
	}
	if !strings.Contains(evDone, `"type":"content_block_stop"`) {
		t.Fatalf("reasoning_summary_text.done should close thinking block, got: %s", evDone)
	}
	if !strings.Contains(evDone, `"type":"signature_delta"`) {
		t.Fatalf("reasoning_summary_text.done should emit signature_delta before stop, got: %s", evDone)
	}
}
