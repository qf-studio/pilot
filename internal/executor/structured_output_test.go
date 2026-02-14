package executor

import (
	"encoding/json"
	"testing"
)

func TestExtractStructuredOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantData string
		wantErr  bool
	}{
		{
			name: "valid wrapper with classification",
			input: `{
				"result": "Some response text",
				"session_id": "test-session",
				"structured_output": {"complexity": "MEDIUM", "reason": "Standard feature work"}
			}`,
			wantData: `{"complexity": "MEDIUM", "reason": "Standard feature work"}`,
			wantErr:  false,
		},
		{
			name: "valid wrapper with effort",
			input: `{
				"result": "Another response",
				"session_id": "test-session-2",
				"structured_output": {"effort": "medium", "reason": "Clear requirements"}
			}`,
			wantData: `{"effort": "medium", "reason": "Clear requirements"}`,
			wantErr:  false,
		},
		{
			name: "valid wrapper with post-execution summary",
			input: `{
				"result": "Git state retrieved",
				"session_id": "test-session-3",
				"structured_output": {
					"branch_name": "pilot/GH-1264",
					"commit_sha": "abc123def456",
					"files_changed": ["file1.go", "file2.go"],
					"summary": "Added structured output support"
				}
			}`,
			wantData: `{"branch_name": "pilot/GH-1264", "commit_sha": "abc123def456", "files_changed": ["file1.go", "file2.go"], "summary": "Added structured output support"}`,
			wantErr:  false,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name:    "missing structured_output field",
			input:   `{"result": "test", "session_id": "test"}`,
			wantErr: true,
		},
		{
			name:    "empty structured_output field",
			input:   `{"result": "test", "session_id": "test", "structured_output": null}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractStructuredOutput([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Normalize JSON for comparison
			var expected, actual interface{}
			if err := json.Unmarshal([]byte(tt.wantData), &expected); err != nil {
				t.Fatalf("test data error: %v", err)
			}
			if err := json.Unmarshal(result, &actual); err != nil {
				t.Fatalf("result parsing error: %v", err)
			}

			expectedBytes, _ := json.Marshal(expected)
			actualBytes, _ := json.Marshal(actual)

			if string(expectedBytes) != string(actualBytes) {
				t.Errorf("expected %s, got %s", expectedBytes, actualBytes)
			}
		})
	}
}

func TestJSONSchemaConstants(t *testing.T) {
	// Test that schema constants are valid JSON
	schemas := map[string]string{
		"ClassificationSchema":      ClassificationSchema,
		"EffortSchema":              EffortSchema,
		"PostExecutionSummarySchema": PostExecutionSummarySchema,
	}

	for name, schema := range schemas {
		var parsed interface{}
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Errorf("%s is not valid JSON: %v", name, err)
		}
	}
}