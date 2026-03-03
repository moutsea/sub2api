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
