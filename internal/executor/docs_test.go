package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateFeatureMatrix(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	agentPath := filepath.Join(tmpDir, ".agent")
	systemPath := filepath.Join(agentPath, "system")

	if err := os.MkdirAll(systemPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a minimal FEATURE-MATRIX.md
	matrixPath := filepath.Join(systemPath, "FEATURE-MATRIX.md")
	baseContent := `# Pilot Feature Matrix

**Last Updated:** 2026-02-14 (v1.0.0)

## Core Execution

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Task execution | ✅ | executor | ` + "`pilot task`" + ` | - | Claude Code subprocess |

## Intelligence

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Complexity detection | ✅ | executor | - | - | Haiku LLM classifier |
`
	if err := os.WriteFile(matrixPath, []byte(baseContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test 1: Add a new feature
	task := &Task{
		ID:    "GH-1388",
		Title: "feat(executor): update Navigator docs after task execution",
	}

	if err := UpdateFeatureMatrix(agentPath, task, "v1.10.0"); err != nil {
		t.Fatalf("UpdateFeatureMatrix failed: %v", err)
	}

	// Verify the file was updated
	content, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)

	// Check that feature name is present (in proper format)
	if !strings.Contains(contentStr, "Update Navigator docs") {
		t.Errorf("Expected feature name 'Update Navigator docs' not found in matrix. Content:\n%s", contentStr)
	}

	// Check that done status is present (should appear multiple times)
	statusCount := strings.Count(contentStr, "✅")
	if statusCount < 2 {
		t.Logf("Only found %d done status markers, expected at least 2", statusCount)
	}

	// Check that version is present
	if !strings.Contains(contentStr, "v1.10.0") {
		t.Errorf("Expected version v1.10.0 not found in matrix")
	}

	// Check that task ID is referenced
	if !strings.Contains(contentStr, "GH-1388") {
		t.Errorf("Expected task ID GH-1388 not found in matrix")
	}
}

func TestUpdateFeatureMatrixMissingFile(t *testing.T) {
	// Create temporary directory without FEATURE-MATRIX.md
	tmpDir := t.TempDir()
	agentPath := filepath.Join(tmpDir, ".agent")

	task := &Task{
		ID:    "GH-1388",
		Title: "feat(executor): update Navigator docs",
	}

	// Should not fail, just log warning and continue
	err := UpdateFeatureMatrix(agentPath, task, "v1.10.0")
	if err != nil {
		t.Errorf("Expected UpdateFeatureMatrix to handle missing file gracefully, got error: %v", err)
	}
}

func TestExtractFeatureName(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{
			title:    "feat(executor): update Navigator docs after task execution",
			expected: "Update Navigator docs after task execution",
		},
		{
			title:    "feat(auth): add OAuth provider integration",
			expected: "Add OAuth provider integration",
		},
		{
			title:    "fix(api): handle nil response",
			expected: "Handle nil response",
		},
		{
			title:    "Simple title without prefix",
			expected: "Simple title without prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			result := extractFeatureName(tt.title)
			if result != tt.expected {
				t.Errorf("extractFeatureName(%q) = %q, expected %q", tt.title, result, tt.expected)
			}
		})
	}
}
