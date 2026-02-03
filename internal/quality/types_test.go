package quality

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGate_DefaultTimeout(t *testing.T) {
	tests := []struct {
		name     string
		gate     *Gate
		expected time.Duration
	}{
		{
			name:     "custom timeout",
			gate:     &Gate{Type: GateBuild, Timeout: 10 * time.Minute},
			expected: 10 * time.Minute,
		},
		{
			name:     "build default",
			gate:     &Gate{Type: GateBuild},
			expected: 5 * time.Minute,
		},
		{
			name:     "test default",
			gate:     &Gate{Type: GateTest},
			expected: 10 * time.Minute,
		},
		{
			name:     "lint default",
			gate:     &Gate{Type: GateLint},
			expected: 2 * time.Minute,
		},
		{
			name:     "coverage default",
			gate:     &Gate{Type: GateCoverage},
			expected: 10 * time.Minute,
		},
		{
			name:     "security default",
			gate:     &Gate{Type: GateSecurity},
			expected: 5 * time.Minute,
		},
		{
			name:     "typecheck default",
			gate:     &Gate{Type: GateTypeCheck},
			expected: 3 * time.Minute,
		},
		{
			name:     "custom gate default",
			gate:     &Gate{Type: GateCustom},
			expected: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.gate.DefaultTimeout()
			if got != tt.expected {
				t.Errorf("DefaultTimeout() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestResult_Passed(t *testing.T) {
	tests := []struct {
		name     string
		status   GateStatus
		expected bool
	}{
		{"passed", StatusPassed, true},
		{"failed", StatusFailed, false},
		{"pending", StatusPending, false},
		{"running", StatusRunning, false},
		{"skipped", StatusSkipped, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{Status: tt.status}
			if got := r.Passed(); got != tt.expected {
				t.Errorf("Passed() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCheckResults_GetFailedGates(t *testing.T) {
	results := &CheckResults{
		Results: []*Result{
			{GateName: "build", Status: StatusPassed},
			{GateName: "test", Status: StatusFailed},
			{GateName: "lint", Status: StatusFailed},
			{GateName: "coverage", Status: StatusSkipped},
		},
	}

	failed := results.GetFailedGates()
	if len(failed) != 2 {
		t.Errorf("expected 2 failed gates, got %d", len(failed))
	}

	names := make(map[string]bool)
	for _, f := range failed {
		names[f.GateName] = true
	}
	if !names["test"] || !names["lint"] {
		t.Error("expected test and lint to be in failed gates")
	}
}

func TestConfig_GetGate(t *testing.T) {
	config := &Config{
		Gates: []*Gate{
			{Name: "build", Type: GateBuild},
			{Name: "test", Type: GateTest},
		},
	}

	// Found
	gate := config.GetGate("build")
	if gate == nil {
		t.Fatal("expected to find build gate")
	}
	if gate.Type != GateBuild {
		t.Errorf("expected type %s, got %s", GateBuild, gate.Type)
	}

	// Not found
	gate = config.GetGate("nonexistent")
	if gate != nil {
		t.Error("expected nil for nonexistent gate")
	}
}

func TestConfig_GetRequiredGates(t *testing.T) {
	config := &Config{
		Gates: []*Gate{
			{Name: "build", Required: true},
			{Name: "test", Required: true},
			{Name: "lint", Required: false},
		},
	}

	required := config.GetRequiredGates()
	if len(required) != 2 {
		t.Errorf("expected 2 required gates, got %d", len(required))
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				Gates: []*Gate{
					{Name: "build", Command: "make build"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			config: &Config{
				Gates: []*Gate{
					{Name: "", Command: "make build"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing command",
			config: &Config{
				Gates: []*Gate{
					{Name: "build", Command: ""},
				},
			},
			wantErr: true,
		},
		{
			name: "empty gates",
			config: &Config{
				Gates: []*Gate{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Enabled {
		t.Error("expected disabled by default")
	}

	if len(config.Gates) != 3 {
		t.Errorf("expected 3 default gates, got %d", len(config.Gates))
	}

	// Check default gates exist
	gates := make(map[string]*Gate)
	for _, g := range config.Gates {
		gates[g.Name] = g
	}

	if gates["build"] == nil {
		t.Error("expected build gate")
	}
	if gates["test"] == nil {
		t.Error("expected test gate")
	}
	if gates["lint"] == nil {
		t.Error("expected lint gate")
	}

	// lint should not be required by default
	if gates["lint"].Required {
		t.Error("expected lint to not be required by default")
	}

	// build and test should be required
	if !gates["build"].Required {
		t.Error("expected build to be required")
	}
	if !gates["test"].Required {
		t.Error("expected test to be required")
	}
}

func TestMinimalBuildGate(t *testing.T) {
	config := MinimalBuildGate()

	if !config.Enabled {
		t.Error("expected minimal build gate to be enabled")
	}

	if len(config.Gates) != 1 {
		t.Errorf("expected 1 gate, got %d", len(config.Gates))
	}

	gate := config.Gates[0]
	if gate.Name != "build" {
		t.Errorf("expected gate name 'build', got '%s'", gate.Name)
	}
	if gate.Type != GateBuild {
		t.Errorf("expected gate type %s, got %s", GateBuild, gate.Type)
	}
	if !gate.Required {
		t.Error("expected build gate to be required")
	}
	if gate.Timeout != 3*time.Minute {
		t.Errorf("expected 3 minute timeout, got %v", gate.Timeout)
	}
	if gate.MaxRetries != 1 {
		t.Errorf("expected 1 max retry, got %d", gate.MaxRetries)
	}

	// Check failure config
	if config.OnFailure.Action != ActionRetry {
		t.Errorf("expected ActionRetry, got %s", config.OnFailure.Action)
	}
	if config.OnFailure.MaxRetries != 1 {
		t.Errorf("expected 1 max retry in failure config, got %d", config.OnFailure.MaxRetries)
	}
}

func TestDetectBuildCommand(t *testing.T) {
	// Create temp directory for tests
	tmpDir, err := os.MkdirTemp("", "quality-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tests := []struct {
		name        string
		setupFiles  []string
		expectedCmd string
	}{
		{
			name:        "go project",
			setupFiles:  []string{"go.mod"},
			expectedCmd: "go build ./...",
		},
		{
			name:        "typescript project",
			setupFiles:  []string{"package.json", "tsconfig.json"},
			expectedCmd: "npm run build || npx tsc --noEmit",
		},
		{
			name:        "node project without typescript",
			setupFiles:  []string{"package.json"},
			expectedCmd: "npm run build --if-present",
		},
		{
			name:        "rust project",
			setupFiles:  []string{"Cargo.toml"},
			expectedCmd: "cargo check",
		},
		{
			name:        "python project with pyproject",
			setupFiles:  []string{"pyproject.toml"},
			expectedCmd: "python -m py_compile $(find . -name '*.py' -not -path './venv/*' -not -path './.venv/*' 2>/dev/null | head -100)",
		},
		{
			name:        "python project with setup.py",
			setupFiles:  []string{"setup.py"},
			expectedCmd: "python -m py_compile $(find . -name '*.py' -not -path './venv/*' -not -path './.venv/*' 2>/dev/null | head -100)",
		},
		{
			name:        "unknown project",
			setupFiles:  []string{},
			expectedCmd: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a subdirectory for this test
			testDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(testDir, 0755); err != nil {
				t.Fatalf("failed to create test dir: %v", err)
			}

			// Create the setup files
			for _, f := range tt.setupFiles {
				filePath := filepath.Join(testDir, f)
				if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
					t.Fatalf("failed to create file %s: %v", f, err)
				}
			}

			// Test detection
			got := DetectBuildCommand(testDir)
			if got != tt.expectedCmd {
				t.Errorf("DetectBuildCommand() = %q, want %q", got, tt.expectedCmd)
			}
		})
	}
}

func TestDetectBuildCommand_Priority(t *testing.T) {
	// Test that Go takes priority when multiple indicators exist
	tmpDir, err := os.MkdirTemp("", "quality-priority-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create both go.mod and package.json
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(""), 0644)

	// Go should take priority
	got := DetectBuildCommand(tmpDir)
	if got != "go build ./..." {
		t.Errorf("expected Go build command to take priority, got %q", got)
	}
}
