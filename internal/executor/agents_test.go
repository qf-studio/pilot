package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentsFile(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string // returns projectPath
		wantContent string
		wantErr     bool
	}{
		{
			name: "reads AGENTS.md when present",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				content := "# Agent Instructions\n\nUse TDD for all changes.\nRun linter before committing."
				if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
					t.Fatalf("failed to write AGENTS.md: %v", err)
				}
				return dir
			},
			wantContent: "# Agent Instructions\n\nUse TDD for all changes.\nRun linter before committing.",
			wantErr:     false,
		},
		{
			name: "returns empty when AGENTS.md missing",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantContent: "",
			wantErr:     false,
		},
		{
			name: "returns empty for empty project path",
			setup: func(t *testing.T) string {
				return ""
			},
			wantContent: "",
			wantErr:     false,
		},
		{
			name: "trims whitespace from content",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				content := "\n\n  # Instructions  \n\n"
				if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
					t.Fatalf("failed to write AGENTS.md: %v", err)
				}
				return dir
			},
			wantContent: "# Instructions",
			wantErr:     false,
		},
		{
			name: "handles large AGENTS.md",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				// Simulate a realistic multi-section AGENTS.md
				content := `# Project Agent Configuration

## Code Standards
- Follow Go conventions
- Use table-driven tests
- Run go fmt before committing

## Architecture
- Keep handlers thin
- Business logic in services
- Use interfaces for dependencies

## Forbidden
- No secrets in code
- No force pushes to main`
				if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
					t.Fatalf("failed to write AGENTS.md: %v", err)
				}
				return dir
			},
			wantContent: `# Project Agent Configuration

## Code Standards
- Follow Go conventions
- Use table-driven tests
- Run go fmt before committing

## Architecture
- Keep handlers thin
- Business logic in services
- Use interfaces for dependencies

## Forbidden
- No secrets in code
- No force pushes to main`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := tt.setup(t)

			got, err := LoadAgentsFile(projectPath)

			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tt.wantContent {
				t.Errorf("content mismatch\ngot:  %q\nwant: %q", got, tt.wantContent)
			}
		})
	}
}

func TestFormatAgentsContext(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty content returns empty",
			content: "",
			want:    "",
		},
		{
			name:    "wraps content in section header",
			content: "Use TDD for all changes.",
			want:    "## Project Agent Instructions (AGENTS.md)\n\nUse TDD for all changes.\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAgentsContext(tt.content)
			if got != tt.want {
				t.Errorf("format mismatch\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}
