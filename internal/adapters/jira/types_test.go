package jira

import (
	"encoding/json"
	"testing"
)

func TestADFText_UnmarshalJSON_PlainString(t *testing.T) {
	var got ADFText
	if err := json.Unmarshal([]byte(`"plain description"`), &got); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if got != "plain description" {
		t.Errorf("UnmarshalJSON() = %q, want %q", got, "plain description")
	}
}

func TestADFText_UnmarshalJSON_ADFDocument(t *testing.T) {
	adf := `{
		"type": "doc",
		"version": 1,
		"content": [
			{
				"type": "paragraph",
				"content": [
					{"type": "text", "text": "Hello world"}
				]
			}
		]
	}`

	var got ADFText
	if err := json.Unmarshal([]byte(adf), &got); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if got != "Hello world" {
		t.Errorf("UnmarshalJSON() = %q, want %q", got, "Hello world")
	}
}

func TestADFText_UnmarshalJSON_InvalidJSON(t *testing.T) {
	var got ADFText
	if err := json.Unmarshal([]byte(`not-json`), &got); err == nil {
		t.Errorf("UnmarshalJSON() expected error for invalid JSON, got nil")
	}
}

func TestFields_Description_UnmarshalsBothShapes(t *testing.T) {
	t.Run("plain string field", func(t *testing.T) {
		var f Fields
		if err := json.Unmarshal([]byte(`{"description":"a simple description"}`), &f); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if f.Description != "a simple description" {
			t.Errorf("Description = %q, want %q", f.Description, "a simple description")
		}
	})

	t.Run("ADF document field", func(t *testing.T) {
		payload := `{
			"description": {
				"type": "doc",
				"version": 1,
				"content": [
					{
						"type": "paragraph",
						"content": [
							{"type": "text", "text": "First paragraph"}
						]
					},
					{
						"type": "paragraph",
						"content": [
							{"type": "text", "text": "Second paragraph"}
						]
					}
				]
			}
		}`
		var f Fields
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		want := "First paragraph\nSecond paragraph"
		if f.Description != ADFText(want) {
			t.Errorf("Description = %q, want %q", f.Description, want)
		}
	})
}
