package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "Hello World", "hello-world"},
		{"with numbers", "Task 123", "task-123"},
		{"special chars", "Add OAuth2.0 Provider!", "add-oauth20-provider"},
		{"multiple spaces", "Multiple   Spaces", "multiple-spaces"},
		{"leading trailing", " Leading Trailing ", "leading-trailing"},
		{"already slug", "already-a-slug", "already-a-slug"},
		{"empty", "", ""},
		{"unicode", "Café au Lait", "caf-au-lait"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := slugify(tt.input)
			if result != tt.expected {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNewTemplateRenderer(t *testing.T) {
	t.Run("with empty path uses embedded", func(t *testing.T) {
		r := NewTemplateRenderer("")
		if !r.useEmbedded {
			t.Error("expected useEmbedded to be true for empty path")
		}
	})

	t.Run("with path uses filesystem", func(t *testing.T) {
		r := NewTemplateRenderer("/some/path")
		if r.useEmbedded {
			t.Error("expected useEmbedded to be false for non-empty path")
		}
		if r.templatesDir != "/some/path" {
			t.Errorf("templatesDir = %q, want %q", r.templatesDir, "/some/path")
		}
	})
}

func TestTemplateRenderer_RenderString(t *testing.T) {
	r := NewTemplateRenderer("")

	tests := []struct {
		name     string
		template string
		data     *TemplateData
		contains []string
		wantErr  bool
	}{
		{
			name:     "basic substitution",
			template: "Hello {{.Title}}!",
			data:     &TemplateData{Title: "World"},
			contains: []string{"Hello World!"},
		},
		{
			name:     "date function",
			template: "Today: {{date}}",
			data:     &TemplateData{},
			contains: []string{"Today:"},
		},
		{
			name:     "slugify function",
			template: "Slug: {{slugify .Title}}",
			data:     &TemplateData{Title: "My Task Title"},
			contains: []string{"Slug: my-task-title"},
		},
		{
			name:     "lower function",
			template: "Lower: {{lower .Title}}",
			data:     &TemplateData{Title: "UPPERCASE"},
			contains: []string{"Lower: uppercase"},
		},
		{
			name:     "upper function",
			template: "Upper: {{upper .Title}}",
			data:     &TemplateData{Title: "lowercase"},
			contains: []string{"Upper: LOWERCASE"},
		},
		{
			name:     "range over criteria",
			template: "{{range .AcceptanceCriteria}}- {{.}}\n{{end}}",
			data:     &TemplateData{AcceptanceCriteria: []string{"First", "Second"}},
			contains: []string{"- First", "- Second"},
		},
		{
			name:     "custom fields",
			template: "Custom: {{.Custom.key}}",
			data:     &TemplateData{Custom: map[string]string{"key": "value"}},
			contains: []string{"Custom: value"},
		},
		{
			name:     "invalid template",
			template: "{{.InvalidSyntax",
			data:     &TemplateData{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := r.RenderString(tt.template, tt.data)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("result %q does not contain %q", result, want)
				}
			}
		})
	}
}

func TestTemplateRenderer_RenderTemplate_Embedded(t *testing.T) {
	r := NewTemplateRenderer("")

	t.Run("task template", func(t *testing.T) {
		data := &TemplateData{
			ID:          "TASK-42",
			Title:       "Test Task",
			Description: "Test description",
			AcceptanceCriteria: []string{
				"Build passes",
				"Tests pass",
			},
		}

		result, err := r.RenderTemplate("task.md", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		checks := []string{
			"# TASK-42",
			"Test description",
			"- [ ] Build passes",
			"- [ ] Tests pass",
		}

		for _, check := range checks {
			if !strings.Contains(result, check) {
				t.Errorf("result does not contain %q", check)
			}
		}
	})

	t.Run("sop template", func(t *testing.T) {
		data := &TemplateData{
			Title:    "Deploy to Production",
			Category: "deployment",
		}

		result, err := r.RenderTemplate("sop.md", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		checks := []string{
			"# SOP: Deploy to Production",
			"**Category:** deployment",
		}

		for _, check := range checks {
			if !strings.Contains(result, check) {
				t.Errorf("result does not contain %q", check)
			}
		}
	})

	t.Run("nonexistent template", func(t *testing.T) {
		_, err := r.RenderTemplate("nonexistent.md", &TemplateData{})
		if err == nil {
			t.Error("expected error for nonexistent template")
		}
	})
}

func TestTemplateRenderer_RenderTemplate_Filesystem(t *testing.T) {
	// Create temp directory with test template
	tmpDir := t.TempDir()
	templateContent := `# {{.Title}}

Created: {{.Date}}
Slug: {{slugify .Title}}
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.md"), []byte(templateContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test template: %v", err)
	}

	r := NewTemplateRenderer(tmpDir)

	data := &TemplateData{
		Title: "My Custom Template",
	}

	result, err := r.RenderTemplate("test.md", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"# My Custom Template",
		"Slug: my-custom-template",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("result does not contain %q", check)
		}
	}
}

func TestTemplateRenderer_RenderTaskDoc(t *testing.T) {
	r := NewTemplateRenderer("")

	result, err := r.RenderTaskDoc("GH-123", "Add Feature X", "Description of feature", []string{"Criterion 1", "Criterion 2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"# GH-123",
		"Description of feature",
		"- [ ] Criterion 1",
		"- [ ] Criterion 2",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("result does not contain %q", check)
		}
	}
}

func TestTemplateRenderer_RenderSOP(t *testing.T) {
	r := NewTemplateRenderer("")

	result, err := r.RenderSOP("Database Backup", "operations")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"# SOP: Database Backup",
		"**Category:** operations",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("result does not contain %q", check)
		}
	}
}

func TestTemplateData_DateDefaulting(t *testing.T) {
	r := NewTemplateRenderer("")

	// Empty dates should be auto-populated
	data := &TemplateData{Title: "Test"}

	result, err := r.RenderString("Date: {{.Date}}, Year: {{.Year}}", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have been populated with current date
	if strings.Contains(result, "Date: ,") {
		t.Error("Date was not auto-populated")
	}

	if strings.Contains(result, "Year: ,") {
		t.Error("Year was not auto-populated")
	}
}
