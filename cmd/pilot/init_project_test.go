package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectTemplate(t *testing.T) {
	tests := []struct {
		name     string
		files    []string // files to create in temp dir
		expected ProjectTemplate
	}{
		{
			name:     "detects Go from go.mod",
			files:    []string{"go.mod"},
			expected: TemplateGo,
		},
		{
			name:     "detects TypeScript from tsconfig.json",
			files:    []string{"tsconfig.json"},
			expected: TemplateTypeScript,
		},
		{
			name:     "detects TypeScript from package.json",
			files:    []string{"package.json"},
			expected: TemplateTypeScript,
		},
		{
			name:     "detects Python from pyproject.toml",
			files:    []string{"pyproject.toml"},
			expected: TemplatePython,
		},
		{
			name:     "detects Python from setup.py",
			files:    []string{"setup.py"},
			expected: TemplatePython,
		},
		{
			name:     "detects Python from requirements.txt",
			files:    []string{"requirements.txt"},
			expected: TemplatePython,
		},
		{
			name:     "falls back to Generic when no markers present",
			files:    []string{"README.md"},
			expected: TemplateGeneric,
		},
		{
			name:     "Go takes priority over TypeScript",
			files:    []string{"go.mod", "package.json"},
			expected: TemplateGo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0o644); err != nil {
					t.Fatalf("failed to create %s: %v", f, err)
				}
			}

			got := detectProjectTemplate(dir)
			if got != tt.expected {
				t.Errorf("detectProjectTemplate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestProjectTemplateString(t *testing.T) {
	tests := []struct {
		tmpl     ProjectTemplate
		expected string
	}{
		{TemplateGo, "Go"},
		{TemplateTypeScript, "TypeScript/Node"},
		{TemplatePython, "Python"},
		{TemplateGeneric, "Generic"},
	}
	for _, tt := range tests {
		if got := tt.tmpl.String(); got != tt.expected {
			t.Errorf("ProjectTemplate(%d).String() = %q, want %q", tt.tmpl, got, tt.expected)
		}
	}
}

func TestDefaultCommands(t *testing.T) {
	tests := []struct {
		tmpl      ProjectTemplate
		lintWant  string
		testWant  string
		buildWant string
	}{
		{TemplateGo, "golangci-lint run ./...", "go test ./...", "go build ./..."},
		{TemplateTypeScript, "npm run lint", "npm test", "npm run build"},
		{TemplatePython, "ruff check .", "pytest", ""},
		{TemplateGeneric, "", "", ""},
	}
	for _, tt := range tests {
		if got := defaultLintCmd(tt.tmpl); got != tt.lintWant {
			t.Errorf("defaultLintCmd(%v) = %q, want %q", tt.tmpl, got, tt.lintWant)
		}
		if got := defaultTestCmd(tt.tmpl); got != tt.testWant {
			t.Errorf("defaultTestCmd(%v) = %q, want %q", tt.tmpl, got, tt.testWant)
		}
		if got := defaultBuildCmd(tt.tmpl); got != tt.buildWant {
			t.Errorf("defaultBuildCmd(%v) = %q, want %q", tt.tmpl, got, tt.buildWant)
		}
	}
}

func TestBuildClaudeMD(t *testing.T) {
	tests := []struct {
		name     string
		data     *initProjectData
		contains []string
		absent   []string
	}{
		{
			name: "Go template with all fields",
			data: &initProjectData{
				ProjectName:  "myapp",
				Template:     TemplateGo,
				Conventions:  []string{"Keep it simple", "No globals"},
				CommitFormat: "feat(scope): description",
				Reviewers:    []string{"alice", "bob"},
				LintCmd:      "golangci-lint run ./...",
				TestCmd:      "go test ./...",
				BuildCmd:     "go build ./...",
			},
			contains: []string{
				"# myapp",
				"go fmt",
				"golangci-lint",
				"Table-driven tests",
				"Keep it simple",
				"No globals",
				"feat(scope): description",
				"@alice",
				"@bob",
				"golangci-lint run ./...",
				"go test ./...",
				"go build ./...",
			},
		},
		{
			name: "TypeScript template",
			data: &initProjectData{
				ProjectName: "webapp",
				Template:    TemplateTypeScript,
				LintCmd:     "npm run lint",
				TestCmd:     "npm test",
				BuildCmd:    "npm run build",
			},
			contains: []string{
				"# webapp",
				"TypeScript",
				"ESLint",
				"npm run lint",
				"npm test",
			},
		},
		{
			name: "Python template",
			data: &initProjectData{
				ProjectName: "myservice",
				Template:    TemplatePython,
				LintCmd:     "ruff check .",
				TestCmd:     "pytest",
			},
			contains: []string{
				"# myservice",
				"PEP 8",
				"pytest",
				"ruff check .",
			},
		},
		{
			name: "Generic template minimal",
			data: &initProjectData{
				ProjectName:  "tool",
				Template:     TemplateGeneric,
				CommitFormat: "type: msg",
			},
			contains: []string{
				"# tool",
				"type: msg",
			},
		},
		{
			name: "No reviewers section when empty",
			data: &initProjectData{
				ProjectName:  "svc",
				Template:     TemplateGo,
				CommitFormat: "feat: msg",
				Reviewers:    nil,
			},
			absent: []string{"## Review Assignments"},
		},
		{
			name: "No quality gates section when all empty",
			data: &initProjectData{
				ProjectName:  "svc",
				Template:     TemplateGeneric,
				CommitFormat: "feat: msg",
				LintCmd:      "",
				TestCmd:      "",
				BuildCmd:     "",
			},
			absent: []string{"## Quality Gates"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildClaudeMD(tt.data)

			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("buildClaudeMD() missing %q\nGot:\n%s", want, got)
				}
			}

			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("buildClaudeMD() should not contain %q\nGot:\n%s", absent, got)
				}
			}
		})
	}
}

func TestCreateNavigatorStructure(t *testing.T) {
	dir := t.TempDir()
	data := &initProjectData{
		ProjectName: "testproject",
		ProjectPath: dir,
	}

	if err := createNavigatorStructure(dir, data); err != nil {
		t.Fatalf("createNavigatorStructure() error: %v", err)
	}

	// Verify directories exist
	for _, sub := range []string{".agent", ".agent/tasks", ".agent/sops"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}

	// Verify DEVELOPMENT-README.md exists and contains project name
	readmePath := filepath.Join(dir, ".agent", "DEVELOPMENT-README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read DEVELOPMENT-README.md: %v", err)
	}
	if !strings.Contains(string(content), "testproject") {
		t.Errorf("DEVELOPMENT-README.md does not contain project name")
	}
}

func TestAddProjectToConfig_NoDuplicate(t *testing.T) {
	// Write a temp config
	dir := t.TempDir()
	if err := os.Setenv("HOME", dir); err != nil { // redirect config path
		t.Fatalf("failed to set HOME: %v", err)
	}
	defer func() { _ = os.Unsetenv("HOME") }()

	data := &initProjectData{
		ProjectName: "proj",
		ProjectPath: dir,
	}

	// First call should succeed
	if err := addProjectToConfig(data); err != nil {
		t.Fatalf("addProjectToConfig() first call error: %v", err)
	}

	// Second call with same path should not error and not duplicate
	if err := addProjectToConfig(data); err != nil {
		t.Fatalf("addProjectToConfig() second call error: %v", err)
	}
}
