package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateFeatureMatrix(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := filepath.Join(tmpDir, ".agent")
	systemPath := filepath.Join(agentPath, "system")

	if err := os.MkdirAll(systemPath, 0755); err != nil {
		t.Fatal(err)
	}

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

	task := &Task{
		ID:    "GH-1388",
		Title: "feat(executor): update Navigator docs after task execution",
	}

	if err := UpdateFeatureMatrix(agentPath, task, "v1.10.0"); err != nil {
		t.Fatalf("UpdateFeatureMatrix failed: %v", err)
	}

	content, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(content)

	// Feature name present
	if !strings.Contains(contentStr, "Update Navigator docs") {
		t.Errorf("Expected feature name not found. Content:\n%s", contentStr)
	}

	// Version present
	if !strings.Contains(contentStr, "v1.10.0") {
		t.Errorf("Expected version v1.10.0 not found")
	}

	// Task ID referenced
	if !strings.Contains(contentStr, "GH-1388") {
		t.Errorf("Expected task ID GH-1388 not found")
	}

	// New row inserted BEFORE ## Intelligence (in Core Execution table)
	newRowIdx := strings.Index(contentStr, "Update Navigator docs")
	intellIdx := strings.Index(contentStr, "## Intelligence")
	if newRowIdx > intellIdx {
		t.Errorf("New row inserted after ## Intelligence, should be before")
	}

	// New row inserted AFTER the existing Core Execution data row
	taskExecIdx := strings.Index(contentStr, "Task execution")
	if newRowIdx < taskExecIdx {
		t.Errorf("New row inserted before existing data row, should be after")
	}
}

func TestUpdateFeatureMatrixNoIntelligenceSection(t *testing.T) {
	tmpDir := t.TempDir()
	agentPath := filepath.Join(tmpDir, ".agent")
	systemPath := filepath.Join(agentPath, "system")

	if err := os.MkdirAll(systemPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Matrix with Core Execution only, no other sections
	matrixPath := filepath.Join(systemPath, "FEATURE-MATRIX.md")
	baseContent := `# Pilot Feature Matrix

## Core Execution

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Task execution | ✅ | executor | ` + "`pilot task`" + ` | - | Claude Code subprocess |
`
	if err := os.WriteFile(matrixPath, []byte(baseContent), 0644); err != nil {
		t.Fatal(err)
	}

	task := &Task{
		ID:    "GH-2000",
		Title: "feat(gateway): add health endpoint",
	}

	if err := UpdateFeatureMatrix(agentPath, task, "v2.0.0"); err != nil {
		t.Fatalf("UpdateFeatureMatrix failed: %v", err)
	}

	content, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, "Add health endpoint") {
		t.Errorf("Expected feature name not found. Content:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "v2.0.0") {
		t.Errorf("Expected version v2.0.0 not found")
	}
}

func TestUpdateFeatureMatrixMissingFile(t *testing.T) {
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
