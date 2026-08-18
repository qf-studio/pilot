package jira

import (
	"encoding/json"
	"testing"
)

func TestADFText_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want ADFText
	}{
		{
			name: "plain string (Server/DC)",
			json: `"plain description"`,
			want: "plain description",
		},
		{
			name: "nested ADF object: paragraph",
			json: `{
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
			}`,
			want: "Hello world",
		},
		{
			name: "nested ADF object: heading + listItem",
			json: `{
				"type": "doc",
				"version": 1,
				"content": [
					{
						"type": "heading",
						"content": [
							{"type": "text", "text": "Title"}
						]
					},
					{
						"type": "bulletList",
						"content": [
							{
								"type": "listItem",
								"content": [
									{
										"type": "paragraph",
										"content": [
											{"type": "text", "text": "First item"}
										]
									}
								]
							},
							{
								"type": "listItem",
								"content": [
									{
										"type": "paragraph",
										"content": [
											{"type": "text", "text": "Second item"}
										]
									}
								]
							}
						]
					}
				]
			}`,
			// Each block-level node (heading/paragraph/listItem) appends its own
			// trailing newline, so a listItem wrapping a paragraph yields a
			// double newline before the next item — this is pre-existing walker
			// behavior (unchanged by the package-level promotion).
			want: "Title\nFirst item\n\nSecond item",
		},
		{
			name: "null description",
			json: `null`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ADFText
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatalf("UnmarshalJSON() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("UnmarshalJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestADFText_UnmarshalJSON_Absent covers the case where the description
// field is entirely absent from the payload (not just null), exercised via
// the containing Fields struct since a bare ADFText has no zero-value
// unmarshal call to observe.
func TestADFText_UnmarshalJSON_Absent(t *testing.T) {
	var f Fields
	if err := json.Unmarshal([]byte(`{"summary":"no description field"}`), &f); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if f.Description != "" {
		t.Errorf("Description = %q, want empty string", f.Description)
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
