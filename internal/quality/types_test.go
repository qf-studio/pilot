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
			name:        "pnpm project without typescript",
			setupFiles:  []string{"package.json", "pnpm-lock.yaml"},
			expectedCmd: "pnpm run build --if-present",
		},
		{
			name:        "yarn project without typescript",
			setupFiles:  []string{"package.json", "yarn.lock"},
			expectedCmd: "yarn run build --if-present",
		},
		{
			name:        "bun project without typescript (bun.lockb)",
			setupFiles:  []string{"package.json", "bun.lockb"},
			expectedCmd: "bun run build --if-present",
		},
		{
			name:        "bun project without typescript (bun.lock)",
			setupFiles:  []string{"package.json", "bun.lock"},
			expectedCmd: "bun run build --if-present",
		},
		{
			name:        "pnpm project with typescript",
			setupFiles:  []string{"package.json", "tsconfig.json", "pnpm-lock.yaml"},
			expectedCmd: "pnpm run build || npx tsc --noEmit",
		},
		{
			name:        "yarn project with typescript",
			setupFiles:  []string{"package.json", "tsconfig.json", "yarn.lock"},
			expectedCmd: "yarn run build || npx tsc --noEmit",
		},
		{
			name:        "bun project with typescript",
			setupFiles:  []string{"package.json", "tsconfig.json", "bun.lockb"},
			expectedCmd: "bun run build || npx tsc --noEmit",
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

func TestDetectTestCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quality-test-detect-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tests := []struct {
		name        string
		files       map[string]string // relative path → content
		expectedCmd string
	}{
		{
			name:        "make with test target",
			files:       map[string]string{"Makefile": "build:\n\techo build\n\ntest:\n\techo run tests\n"},
			expectedCmd: "make test",
		},
		{
			name:        "make without test target",
			files:       map[string]string{"Makefile": "build:\n\techo build\n", "go.mod": ""},
			expectedCmd: "go test ./...",
		},
		{
			name:        "pytest via py file",
			files:       map[string]string{"app.py": "print('hi')\n"},
			expectedCmd: "pytest -v 2>&1",
		},
		{
			name:        "pytest via pyproject",
			files:       map[string]string{"pyproject.toml": ""},
			expectedCmd: "pytest -v 2>&1",
		},
		{
			name:        "npm test",
			files:       map[string]string{"package.json": "{}"},
			expectedCmd: "npm test",
		},
		{
			name:        "pnpm test",
			files:       map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""},
			expectedCmd: "pnpm test",
		},
		{
			name:        "yarn test",
			files:       map[string]string{"package.json": "{}", "yarn.lock": ""},
			expectedCmd: "yarn test",
		},
		{
			name:        "bun test (bun.lockb)",
			files:       map[string]string{"package.json": "{}", "bun.lockb": ""},
			expectedCmd: "bun test",
		},
		{
			name:        "bun test (bun.lock)",
			files:       map[string]string{"package.json": "{}", "bun.lock": ""},
			expectedCmd: "bun test",
		},
		{
			name:        "cargo test",
			files:       map[string]string{"Cargo.toml": ""},
			expectedCmd: "cargo test",
		},
		{
			name:        "go test",
			files:       map[string]string{"go.mod": "module x\n"},
			expectedCmd: "go test ./...",
		},
		{
			name:        "empty workspace",
			files:       map[string]string{},
			expectedCmd: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(testDir, 0755); err != nil {
				t.Fatalf("failed to create test dir: %v", err)
			}
			for name, content := range tt.files {
				path := filepath.Join(testDir, name)
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatalf("failed to create file %s: %v", name, err)
				}
			}

			got := DetectTestCommand(testDir)
			if got != tt.expectedCmd {
				t.Errorf("DetectTestCommand() = %q, want %q", got, tt.expectedCmd)
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

func TestResolveConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quality-resolve-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	goProjectDir := filepath.Join(tmpDir, "go-project")
	if err := os.MkdirAll(goProjectDir, 0755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goProjectDir, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}

	projectQuality := &Config{
		Enabled: true,
		Gates:   []*Gate{{Name: "build", Type: GateBuild, Command: "pnpm run build", Required: true}},
	}
	globalQuality := &Config{
		Enabled: true,
		Gates:   []*Gate{{Name: "build", Type: GateBuild, Command: "make build", Required: true}},
	}
	disabledGlobal := &Config{Enabled: false}

	tests := []struct {
		name           string
		projectQuality *Config
		globalQuality  *Config
		projectPath    string
		wantConfig     *Config // exact pointer identity expected, or nil to check auto-detect
		wantAutoDetect bool
		wantEnabled    bool
	}{
		{
			name:           "project config takes precedence over global",
			projectQuality: projectQuality,
			globalQuality:  globalQuality,
			projectPath:    goProjectDir,
			wantConfig:     projectQuality,
		},
		{
			name:           "falls back to global when project has no override",
			projectQuality: nil,
			globalQuality:  globalQuality,
			projectPath:    goProjectDir,
			wantConfig:     globalQuality,
		},
		{
			name:           "falls back to global when project quality disabled",
			projectQuality: &Config{Enabled: false},
			globalQuality:  globalQuality,
			projectPath:    goProjectDir,
			wantConfig:     globalQuality,
		},
		{
			name:           "auto-detects when neither project nor global configured",
			projectQuality: nil,
			globalQuality:  nil,
			projectPath:    goProjectDir,
			wantAutoDetect: true,
			wantEnabled:    true,
		},
		{
			name:           "auto-detects when global present but disabled",
			projectQuality: nil,
			globalQuality:  disabledGlobal,
			projectPath:    goProjectDir,
			wantAutoDetect: true,
			wantEnabled:    true,
		},
		{
			name:           "auto-detect yields disabled config when nothing detectable",
			projectQuality: nil,
			globalQuality:  nil,
			projectPath:    emptyDir,
			wantAutoDetect: true,
			wantEnabled:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveConfig(tt.projectQuality, tt.globalQuality, tt.projectPath)
			if got == nil {
				t.Fatal("ResolveConfig() returned nil, want a non-nil Config")
			}
			if tt.wantConfig != nil {
				if got != tt.wantConfig {
					t.Errorf("ResolveConfig() = %p, want %p (exact config)", got, tt.wantConfig)
				}
				return
			}
			if tt.wantAutoDetect {
				if got.Enabled != tt.wantEnabled {
					t.Errorf("ResolveConfig() auto-detected Enabled = %v, want %v", got.Enabled, tt.wantEnabled)
				}
			}
		})
	}
}

func TestAutoDetectConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quality-autodetect-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("go project with tests", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "go-project")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create test dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644); err != nil {
			t.Fatalf("failed to write go.mod: %v", err)
		}

		cfg := AutoDetectConfig(dir)
		if !cfg.Enabled {
			t.Fatal("expected auto-detected config to be enabled")
		}
		if len(cfg.Gates) != 2 {
			t.Fatalf("expected build + test gates, got %d gates", len(cfg.Gates))
		}
		if cfg.Gates[0].Command != "go build ./..." {
			t.Errorf("build command = %q, want %q", cfg.Gates[0].Command, "go build ./...")
		}
		if cfg.Gates[1].Command != "go test ./..." {
			t.Errorf("test command = %q, want %q", cfg.Gates[1].Command, "go test ./...")
		}
	})

	t.Run("undetectable project", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "unknown-project")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create test dir: %v", err)
		}

		cfg := AutoDetectConfig(dir)
		if cfg.Enabled {
			t.Error("expected disabled config when no build command is detectable")
		}
		if len(cfg.Gates) != 0 {
			t.Errorf("expected no gates, got %d", len(cfg.Gates))
		}
	})
}
