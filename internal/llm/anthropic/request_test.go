package anthropic

import (
	"encoding/json"
	"testing"
)

func TestNewRequest_RejectsEmptyModel(t *testing.T) {
	_, err := NewRequest("", 1024, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for empty model, got nil")
	}
}

func TestNewRequest_RejectsNonPositiveMaxTokens(t *testing.T) {
	tests := []int{0, -1, -100}
	for _, mt := range tests {
		if _, err := NewRequest("claude-haiku-4-5", mt, nil); err == nil {
			t.Errorf("max_tokens=%d: expected error, got nil", mt)
		}
	}
}

func TestNewRequest_ContractShape(t *testing.T) {
	req, err := NewRequest("claude-haiku-4-5", 2048, []Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req.WithSystem("be helpful")

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// model must always be present and non-empty.
	model, ok := body["model"].(string)
	if !ok || model == "" {
		t.Errorf("model: got %v, want non-empty string", body["model"])
	}

	// max_tokens is REQUIRED by the Anthropic Messages API — omitting it 400s.
	mt, ok := body["max_tokens"].(float64)
	if !ok || mt <= 0 {
		t.Errorf("max_tokens: got %v, want positive number", body["max_tokens"])
	}

	// system must round-trip when set via WithSystem.
	if body["system"] != "be helpful" {
		t.Errorf("system: got %v, want %q", body["system"], "be helpful")
	}

	// output_config/effort are non-standard top-level fields that have caused
	// 400 regressions in the past (PR #3703) — the builder must never emit them.
	for _, forbidden := range []string{"output_config", "effort"} {
		if _, present := body[forbidden]; present {
			t.Errorf("%s must not be a top-level field in the request body", forbidden)
		}
	}
}

func TestRequest_SystemOmittedWhenUnset(t *testing.T) {
	req, err := NewRequest("claude-haiku-4-5", 1024, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, present := body["system"]; present {
		t.Errorf("system must be omitted from the body when never set")
	}
}

func TestRequest_MessagesRoundTrip(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "previous question"},
		{Role: "assistant", Content: "previous answer"},
		{Role: "user", Content: "new question"},
	}
	req, err := NewRequest("claude-haiku-4-5", 1024, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs, ok := body["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages not an array")
	}
	if len(msgs) != len(messages) {
		t.Errorf("messages: got %d entries, want %d", len(msgs), len(messages))
	}
}
