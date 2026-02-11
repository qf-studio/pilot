package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alekspetrov/pilot/internal/logging"
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

// contains is defined in runner_test.go - reuse it
