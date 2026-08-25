package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory/graphrecall"
)

func TestDetectProjectInfo_GoProject(t *testing.T) {
	// Create temp directory with go.mod
	tmpDir := t.TempDir()
	goMod := `module github.com/example/myproject

go 1.21

require (
	github.com/gin-gonic/gin v1.9.0
	gorm.io/gorm v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	info, err := initializer.DetectProjectInfo(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != "myproject" {
		t.Errorf("expected name 'myproject', got %q", info.Name)
	}

	if info.DetectedFrom != "go.mod" {
		t.Errorf("expected detected_from 'go.mod', got %q", info.DetectedFrom)
	}

	// Should contain Go and Gin
	if info.TechStack == "" || info.TechStack == "Unknown" {
		t.Errorf("expected tech stack to be detected, got %q", info.TechStack)
	}
}

func TestDetectProjectInfo_NodeProject(t *testing.T) {
	tmpDir := t.TempDir()
	pkgJSON := `{
  "name": "my-react-app",
  "dependencies": {
    "react": "^18.0.0",
    "typescript": "^5.0.0"
  }
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	info, err := initializer.DetectProjectInfo(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != "my-react-app" {
		t.Errorf("expected name 'my-react-app', got %q", info.Name)
	}

	if info.DetectedFrom != "package.json" {
		t.Errorf("expected detected_from 'package.json', got %q", info.DetectedFrom)
	}
}

func TestDetectProjectInfo_Fallback(t *testing.T) {
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	info, err := initializer.DetectProjectInfo(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if info.DetectedFrom != "directory_name" {
		t.Errorf("expected detected_from 'directory_name', got %q", info.DetectedFrom)
	}

	if info.TechStack != "Unknown" {
		t.Errorf("expected tech stack 'Unknown', got %q", info.TechStack)
	}
}

func TestIsInitialized(t *testing.T) {
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	// Should not be initialized initially
	if initializer.IsInitialized(tmpDir) {
		t.Error("expected project to not be initialized")
	}

	// Create .agent/ structure
	agentDir := filepath.Join(tmpDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Still not initialized (missing DEVELOPMENT-README.md)
	if initializer.IsInitialized(tmpDir) {
		t.Error("expected project to not be initialized without README")
	}

	// Create README
	if err := os.WriteFile(filepath.Join(agentDir, "DEVELOPMENT-README.md"), []byte("# Test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Now should be initialized
	if !initializer.IsInitialized(tmpDir) {
		t.Error("expected project to be initialized")
	}
}

func TestInitialize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	// Create a go.mod for project detection
	goMod := `module github.com/test/myapp

go 1.21
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	// Initialize
	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify structure
	agentDir := filepath.Join(tmpDir, ".agent")

	// Check directories exist
	dirs := []string{
		"tasks",
		"system",
		"sops",
		"sops/integrations",
		"sops/debugging",
		"sops/development",
		"sops/deployment",
		"knowledge",
		"knowledge/memories",
		"knowledge/memories/patterns",
		"knowledge/memories/pitfalls",
		"knowledge/memories/decisions",
		"knowledge/memories/learnings",
	}

	for _, dir := range dirs {
		path := filepath.Join(agentDir, dir)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", dir)
		}
	}

	// Check DEVELOPMENT-README.md exists and has content
	readmePath := filepath.Join(agentDir, "DEVELOPMENT-README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README: %v", err)
	}

	if len(content) == 0 {
		t.Error("README is empty")
	}

	// Should contain project name
	if !contains(string(content), "myapp") {
		t.Error("README should contain project name 'myapp'")
	}

	// Check .nav-config.json exists and is valid JSON
	configPath := filepath.Join(agentDir, ".nav-config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read .nav-config.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("invalid JSON in .nav-config.json: %v", err)
	}

	if config["project_name"] != "myapp" {
		t.Errorf("expected project_name 'myapp', got %v", config["project_name"])
	}

	// Re-initialize should be a no-op
	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("re-Initialize should not fail: %v", err)
	}
}

func TestInitialize_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	// Initialize twice
	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("first Initialize failed: %v", err)
	}

	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("second Initialize failed: %v", err)
	}

	// Should still be valid
	if !initializer.IsInitialized(tmpDir) {
		t.Error("expected project to be initialized after double init")
	}
}

func TestCustomizeTemplate(t *testing.T) {
	initializer := &NavigatorInitializer{}

	info := &ProjectInfo{
		Name:         "My Test Project",
		TechStack:    "Go, SQLite",
		DetectedFrom: "go.mod",
	}

	template := `# [Project Name]
Tech: [Your tech stack]
Date: [Date]
Detected: ${DETECTED_FROM}
`

	result := initializer.customizeTemplate(template, info)

	if !contains(result, "My Test Project") {
		t.Error("template should contain project name")
	}

	if !contains(result, "Go, SQLite") {
		t.Error("template should contain tech stack")
	}

	if !contains(result, "go.mod") {
		t.Error("template should contain detected_from")
	}

	// Date should be replaced (not contain [Date])
	if contains(result, "[Date]") {
		t.Error("template should have date placeholder replaced")
	}
}

// TestInitialize_ProjectContextInjectionActivates is the GH-5216 regression
// test for the permanent no-op described in GH-1387: loadProjectContext()
// extracts "### Key Components", "## Key Files" and "## Project Structure"
// from the generated DEVELOPMENT-README.md. Before this fix, the shipped
// template contained none of those headers, so this always returned "".
func TestInitialize_ProjectContextInjectionActivates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	agentDir := filepath.Join(tmpDir, ".agent")
	got := loadProjectContext(agentDir)
	if got == "" {
		t.Fatal("expected loadProjectContext to return non-empty context after auto-init, got empty string")
	}

	for _, header := range []string{"Key Files", "Project Structure"} {
		if !strings.Contains(got, header) {
			t.Errorf("expected injected context to include a %q section, got:\n%s", header, got)
		}
	}
}

// TestInitialize_FeatureMatrixSeededAndUpdatable is the GH-5216 regression
// test for GH-1388: UpdateFeatureMatrix (docs.go) is a permanent no-op when
// system/FEATURE-MATRIX.md doesn't exist. This asserts the row lands inside
// the "## Core Execution" table (anchor-based insertion), not appended past
// unrelated trailing content — i.e. not the fallback-append branch.
func TestInitialize_FeatureMatrixSeededAndUpdatable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	agentDir := filepath.Join(tmpDir, ".agent")
	matrixPath := filepath.Join(agentDir, "system", "FEATURE-MATRIX.md")

	before, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatalf("expected system/FEATURE-MATRIX.md to be seeded by Initialize: %v", err)
	}
	if !strings.Contains(string(before), "## Core Execution") {
		t.Fatalf("seeded FEATURE-MATRIX.md missing '## Core Execution' anchor:\n%s", before)
	}

	// Append an unrelated trailing section. If UpdateFeatureMatrix ever falls
	// back to naive end-of-file append, the new row will land after this
	// canary instead of inside the Core Execution table.
	canary := "\n## Unrelated Trailing Section\n\nSome other content.\n"
	if err := os.WriteFile(matrixPath, append(before, []byte(canary)...), 0644); err != nil {
		t.Fatal(err)
	}

	task := &Task{ID: "GH-9999", Title: "feat(executor): add a widget"}
	if err := UpdateFeatureMatrix(agentDir, task, "v9.9.9"); err != nil {
		t.Fatalf("UpdateFeatureMatrix failed: %v", err)
	}

	after, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(after), "\n")
	rowIdx, canaryIdx := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "GH-9999") {
			rowIdx = i
		}
		if strings.Contains(line, "Unrelated Trailing Section") {
			canaryIdx = i
		}
	}

	if rowIdx == -1 {
		t.Fatalf("expected a row referencing GH-9999 in updated FEATURE-MATRIX.md:\n%s", after)
	}
	if canaryIdx == -1 {
		t.Fatal("canary trailing section disappeared from FEATURE-MATRIX.md")
	}
	if rowIdx >= canaryIdx {
		t.Errorf("expected new feature row (line %d) to be inserted inside the Core Execution table, before the trailing section (line %d) — got fallback append past it", rowIdx, canaryIdx)
	}
}

// TestInitialize_KnowledgeGraphSeededForRecall is the GH-5216 regression test
// for the knowledge tree never being seeded: graphrecall.RecallRelevant reads
// .agent/knowledge/graph.json on every execution. Before this fix, auto-init
// created neither the file nor its parent dirs, so recall silently took the
// read-error fail-open path on every session. This asserts the seeded file
// takes the parse path instead (still returns empty, since there are no
// concepts/memories yet, but via a successful unmarshal).
func TestInitialize_KnowledgeGraphSeededForRecall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	graphPath := filepath.Join(tmpDir, ".agent", "knowledge", "graph.json")
	data, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatalf("expected knowledge/graph.json to be seeded by Initialize: %v", err)
	}

	var doc struct {
		Nodes struct {
			Concepts map[string]interface{} `json:"concepts"`
			Memories map[string]interface{} `json:"memories"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("seeded graph.json does not unmarshal into the recall schema: %v", err)
	}
	if doc.Nodes.Concepts == nil || doc.Nodes.Memories == nil {
		t.Errorf("expected seeded graph.json to have (possibly empty) concepts/memories maps, got %+v", doc.Nodes)
	}

	got := graphrecall.RecallRelevant(tmpDir, "add a widget to the executor", 5)
	if got == nil {
		t.Error("expected RecallRelevant to return a non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected no memories from a freshly seeded empty graph, got %d", len(got))
	}
}

// TestGeneratedReadme_NoNavCommandInstructions is the GH-5216 regression test
// for the template still telling sessions to run /nav-start, /nav-task,
// /nav-loop, /nav-compact — Navigator-plugin slash commands that don't exist
// in a plain executor session (removed from public docs in commit 4b47e7a3).
func TestGeneratedReadme_NoNavCommandInstructions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	if err := initializer.Initialize(tmpDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".agent", "DEVELOPMENT-README.md"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(content), "/nav-") {
		t.Errorf("generated DEVELOPMENT-README.md still contains a /nav- command instruction:\n%s", content)
	}
}

func TestFindTemplatesPath_VersionSelection(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{
			name:     "numeric comparison beats lexicographic within major",
			versions: []string{"6.9.0", "6.18.1", "6.2.0"},
			want:     "6.18.1",
		},
		{
			name:     "next major version is discovered, not pinned to 6.x",
			versions: []string{"6.18.1", "7.0.0"},
			want:     "7.0.0",
		},
		{
			name:     "non-version directories are ignored",
			versions: []string{"6.1.0", "not-a-version", "6.2.0"},
			want:     "6.2.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			navDir := filepath.Join(home, ".claude", "plugins", "cache", "navigator-marketplace", "navigator")
			if err := os.MkdirAll(navDir, 0755); err != nil {
				t.Fatal(err)
			}

			for _, v := range tt.versions {
				dir := filepath.Join(navDir, v, "templates")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("HOME", home)

			got, err := FindTemplatesPath()
			if err != nil {
				t.Fatalf("FindTemplatesPath failed: %v", err)
			}

			want := filepath.Join(navDir, tt.want, "templates")
			if got != want {
				t.Errorf("FindTemplatesPath() = %q, want %q", got, want)
			}
		})
	}
}

// TestInitialize_ContextSectionsGuaranteedForPluginTemplate is the GH-5221
// regression test: the installed Navigator plugin's own
// DEVELOPMENT-README.md template (verified against 6.18.1) carries none of
// the three headers loadProjectContext() extracts, so on any machine with
// the plugin cache present (FindTemplatesPath wins over the embedded
// fallback), context injection stayed a permanent no-op even after the
// GH-5216 fix to the embedded template. This fabricates a plugin cache
// whose template lacks all three headers and asserts Initialize() still
// guarantees them via ensureContextSections.
func TestInitialize_ContextSectionsGuaranteedForPluginTemplate(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	pluginTemplatesDir := filepath.Join(home, ".claude", "plugins", "cache", "navigator-marketplace", "navigator", "9.0.0", "templates")
	if err := os.MkdirAll(pluginTemplatesDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Fabricated plugin template missing "### Key Components", "## Key
	// Files" and "## Project Structure" — mirrors the real plugin template's
	// shape.
	pluginReadme := `# [Project Name] - Development Navigator

**Project**: [Brief project description]
**Tech Stack**: [Your tech stack]

## Quick Start

Some quick start instructions with no context-injection anchors.
`
	if err := os.WriteFile(filepath.Join(pluginTemplatesDir, "DEVELOPMENT-README.md"), []byte(pluginReadme), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)

	initializer, err := NewNavigatorInitializer(logging.WithComponent("test"))
	if err != nil {
		t.Fatal(err)
	}

	if initializer.templatesPath == "" {
		t.Fatal("expected NewNavigatorInitializer to pick up the fabricated plugin templates path, got embedded fallback")
	}

	if err := initializer.Initialize(projectDir); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	agentDir := filepath.Join(projectDir, ".agent")
	got := loadProjectContext(agentDir)
	if got == "" {
		t.Fatal("expected loadProjectContext to return non-empty context even when the winning template lacks context-injection headers, got empty string")
	}

	for _, header := range []string{"Key Files", "Project Structure"} {
		if !strings.Contains(got, header) {
			t.Errorf("expected injected context to include a %q section, got:\n%s", header, got)
		}
	}
}

// TestEnsureContextSections_IdempotentWhenHeadersPresent asserts
// ensureContextSections is a true no-op — byte-for-byte — when a README
// already carries all three context-injection headers, so it never
// duplicates or reorders existing content on repeat auto-init runs.
func TestEnsureContextSections_IdempotentWhenHeadersPresent(t *testing.T) {
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "DEVELOPMENT-README.md")

	content := `# Project

## Quick Start

Some instructions.

### Key Components

Some components.

## Key Files

Some files.

## Project Structure

Some structure.
`
	if err := os.WriteFile(readmePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureContextSections(readmePath); err != nil {
		t.Fatalf("ensureContextSections failed: %v", err)
	}

	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != content {
		t.Errorf("expected ensureContextSections to be a byte-for-byte no-op when all headers are already present\nwant:\n%s\ngot:\n%s", content, got)
	}
}

// contains is defined in runner_test.go - reuse it
