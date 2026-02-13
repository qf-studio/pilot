package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSimplifyConfig(t *testing.T) {
	cfg := DefaultSimplifyConfig()

	if cfg.Enabled {
		t.Error("Expected Enabled to be false by default")
	}
	if cfg.Trigger != "post-implementation" {
		t.Errorf("Expected Trigger to be 'post-implementation', got %q", cfg.Trigger)
	}
	if cfg.Scope != "modified" {
		t.Errorf("Expected Scope to be 'modified', got %q", cfg.Scope)
	}
	if cfg.MaxFileSize != 50000 {
		t.Errorf("Expected MaxFileSize to be 50000, got %d", cfg.MaxFileSize)
	}
	if !cfg.PreserveComments {
		t.Error("Expected PreserveComments to be true by default")
	}
	if len(cfg.SkipPatterns) == 0 {
		t.Error("Expected SkipPatterns to have default entries")
	}
}

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()

	if len(rules) < 3 {
		t.Errorf("Expected at least 3 rules, got %d", len(rules))
	}

	// Check that each rule has required fields
	for _, rule := range rules {
		if rule.Name == "" {
			t.Error("Rule has empty name")
		}
		if rule.Description == "" {
			t.Errorf("Rule %q has empty description", rule.Name)
		}
		if rule.Check == nil {
			t.Errorf("Rule %q has nil Check function", rule.Name)
		}
		if rule.Apply == nil {
			t.Errorf("Rule %q has nil Apply function", rule.Name)
		}
	}
}

func TestRemoveTrailingWhitespaceRule(t *testing.T) {
	rules := DefaultRules()

	// Find the trailing whitespace rule
	var rule *SimplificationRule
	for i := range rules {
		if rules[i].Name == "remove_trailing_whitespace" {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		t.Fatal("remove_trailing_whitespace rule not found")
	}

	tests := []struct {
		name     string
		input    string
		hasIssue bool
		expected string
	}{
		{
			name:     "no trailing whitespace",
			input:    "line1\nline2\nline3",
			hasIssue: false,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "trailing spaces",
			input:    "line1   \nline2  \nline3",
			hasIssue: true,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "trailing tabs",
			input:    "line1\t\nline2\t\t\nline3",
			hasIssue: true,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "mixed trailing whitespace",
			input:    "line1 \t\nline2\t \nline3",
			hasIssue: true,
			expected: "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rule.Check(tt.input) != tt.hasIssue {
				t.Errorf("Check() = %v, want %v", !tt.hasIssue, tt.hasIssue)
			}
			if tt.hasIssue {
				result := rule.Apply(tt.input)
				if result != tt.expected {
					t.Errorf("Apply() = %q, want %q", result, tt.expected)
				}
			}
		})
	}
}

func TestNormalizeEmptyLinesRule(t *testing.T) {
	rules := DefaultRules()

	// Find the normalize empty lines rule
	var rule *SimplificationRule
	for i := range rules {
		if rules[i].Name == "normalize_empty_lines" {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		t.Fatal("normalize_empty_lines rule not found")
	}

	tests := []struct {
		name     string
		input    string
		hasIssue bool
		expected string
	}{
		{
			name:     "no multiple empty lines",
			input:    "line1\n\nline2",
			hasIssue: false,
			expected: "line1\n\nline2",
		},
		{
			name:     "three empty lines",
			input:    "line1\n\n\nline2",
			hasIssue: true,
			expected: "line1\n\nline2",
		},
		{
			name:     "many empty lines",
			input:    "line1\n\n\n\n\nline2",
			hasIssue: true,
			expected: "line1\n\nline2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rule.Check(tt.input) != tt.hasIssue {
				t.Errorf("Check() = %v, want %v", !tt.hasIssue, tt.hasIssue)
			}
			if tt.hasIssue {
				result := rule.Apply(tt.input)
				if result != tt.expected {
					t.Errorf("Apply() = %q, want %q", result, tt.expected)
				}
			}
		})
	}
}

func TestSimplifyFile(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create test file with trailing whitespace
	testFile := filepath.Join(tmpDir, "test.go")
	content := "package main\n\nfunc main() {   \n\tprintln(\"hello\")  \n}\n"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	config := &SimplifyConfig{
		Enabled:          true,
		SkipPatterns:     []string{"*.test.go"},
		MaxFileSize:      50000,
		PreserveComments: true,
	}

	rules := DefaultRules()
	modified, err := SimplifyFile(testFile, rules, config)
	if err != nil {
		t.Fatal(err)
	}

	if !modified {
		t.Error("Expected file to be modified")
	}

	// Read back and verify
	result, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}

	expected := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if string(result) != expected {
		t.Errorf("Got:\n%q\n\nExpected:\n%q", string(result), expected)
	}
}

func TestSimplifyFileSkipPatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test file
	testFile := filepath.Join(tmpDir, "test_test.go")
	content := "package main   \n"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	config := &SimplifyConfig{
		Enabled:      true,
		SkipPatterns: []string{"*_test.go"},
		MaxFileSize:  50000,
	}

	rules := DefaultRules()
	modified, err := SimplifyFile(testFile, rules, config)
	if err != nil {
		t.Fatal(err)
	}

	if modified {
		t.Error("Expected file to be skipped due to pattern match")
	}

	// Verify content unchanged
	result, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != content {
		t.Errorf("File was modified when it should have been skipped")
	}
}

func TestSimplifyFileMaxSize(t *testing.T) {
	tmpDir := t.TempDir()

	// Create large test file
	testFile := filepath.Join(tmpDir, "large.go")
	content := "package main   \n" // Has trailing whitespace
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	config := &SimplifyConfig{
		Enabled:      true,
		SkipPatterns: []string{},
		MaxFileSize:  5, // Very small limit
	}

	rules := DefaultRules()
	modified, err := SimplifyFile(testFile, rules, config)
	if err != nil {
		t.Fatal(err)
	}

	if modified {
		t.Error("Expected file to be skipped due to size limit")
	}
}

func TestSimplifyFileNoChanges(t *testing.T) {
	tmpDir := t.TempDir()

	// Create clean test file
	testFile := filepath.Join(tmpDir, "clean.go")
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	config := DefaultSimplifyConfig()
	config.SkipPatterns = []string{} // Don't skip any patterns

	rules := DefaultRules()
	modified, err := SimplifyFile(testFile, rules, config)
	if err != nil {
		t.Fatal(err)
	}

	if modified {
		t.Error("Expected file to not be modified (already clean)")
	}
}

func TestConsolidateImportsRule(t *testing.T) {
	rules := DefaultRules()

	var rule *SimplificationRule
	for i := range rules {
		if rules[i].Name == "consolidate_imports" {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		t.Fatal("consolidate_imports rule not found")
	}

	tests := []struct {
		name     string
		input    string
		hasIssue bool
	}{
		{
			name:     "no duplicates",
			input:    "import \"fmt\"\nimport \"os\"",
			hasIssue: false,
		},
		{
			name:     "duplicate imports",
			input:    "import \"fmt\"\nimport \"fmt\"",
			hasIssue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rule.Check(tt.input) != tt.hasIssue {
				t.Errorf("Check() = %v, want %v", !tt.hasIssue, tt.hasIssue)
			}
		})
	}
}
