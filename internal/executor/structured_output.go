package executor

import (
	"encoding/json"
	"fmt"
)

// JSON Schema constants for Claude Code --json-schema structured output

// ClassificationSchema for complexity classifier
const ClassificationSchema = `{"type":"object","properties":{"complexity":{"type":"string","enum":["TRIVIAL","SIMPLE","MEDIUM","COMPLEX","EPIC"]},"reason":{"type":"string"}},"required":["complexity","reason"]}`

// EffortSchema for effort classifier
const EffortSchema = `{"type":"object","properties":{"effort":{"type":"string","enum":["low","medium","high"]},"reason":{"type":"string"}},"required":["effort","reason"]}`

// PostExecutionSummarySchema for branch/SHA/files extraction
const PostExecutionSummarySchema = `{"type":"object","properties":{"branch_name":{"type":"string"},"commit_sha":{"type":"string"},"files_changed":{"type":"array","items":{"type":"string"}},"summary":{"type":"string"}},"required":["branch_name","commit_sha"]}`

// claudeCodeWrapper represents the wrapper format returned by Claude Code with --json-schema
type claudeCodeWrapper struct {
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// extractStructuredOutput parses the Claude Code --json-schema wrapper format
// and returns the structured_output field.
// Input format: {"result":"...","session_id":"...","structured_output":{...}}
// Returns: the contents of the structured_output field
func extractStructuredOutput(jsonResponse []byte) (json.RawMessage, error) {
	var wrapper claudeCodeWrapper
	if err := json.Unmarshal(jsonResponse, &wrapper); err != nil {
		return nil, fmt.Errorf("parse claude code wrapper: %w", err)
	}

	if len(wrapper.StructuredOutput) == 0 || string(wrapper.StructuredOutput) == "null" {
		return nil, fmt.Errorf("empty structured_output field in response")
	}

	return wrapper.StructuredOutput, nil
}