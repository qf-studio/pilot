package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHooksConfig_Defaults(t *testing.T) {
	config := DefaultHooksConfig()

	if config.Enabled {
		t.Error("Expected hooks to be disabled by default")
	}
	if config.RunTestsOnStop == nil || !*config.RunTestsOnStop {
		t.Error("Expected RunTestsOnStop to default to true when enabled")
	}
	if config.BlockDestructive == nil || !*config.BlockDestructive {
		t.Error("Expected BlockDestructive to default to true when enabled")
	}
	if config.LintOnSave {
		t.Error("Expected LintOnSave to default to false")
	}
}

func TestGenerateClaudeSettings(t *testing.T) {
	tests := []struct {
		name       string
		config     *HooksConfig
		expectKeys int // number of hook types expected
	}{
		{"nil config", nil, 0},
		{"disabled config", &HooksConfig{Enabled: false}, 0},
		{"enabled with defaults", &HooksConfig{Enabled: true}, 2},                // Stop + PreToolUse
		{"enabled with lint", &HooksConfig{Enabled: true, LintOnSave: true}, 3},  // Stop + PreToolUse + PostToolUse
		{"all disabled", &HooksConfig{Enabled: true, RunTestsOnStop: boolPtr(false), BlockDestructive: boolPtr(false)}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateClaudeSettings(tt.config, "/test/scripts")

			if tt.expectKeys == 0 {
				if len(result) != 0 {
					t.Errorf("Expected empty result, got %d keys", len(result))
				}
				return
			}

			hooks, ok := result["hooks"].(map[string][]HookMatcherEntry)
			if !ok {
				t.Fatal("Expected hooks to be map[string][]HookMatcherEntry")
			}
			if len(hooks) != tt.expectKeys {
				t.Errorf("Expected %d hook types, got %d", tt.expectKeys, len(hooks))
			}
		})
	}
}

// TestGenerateClaudeSettingsJSONFormat verifies the JSON output matches Claude Code format:
// - PreToolUse/PostToolUse: "matcher" is a regex string
// - Stop: no "matcher" field
func TestGenerateClaudeSettingsJSONFormat(t *testing.T) {
	config := &HooksConfig{
		Enabled:    true,
		LintOnSave: true,
	}

	settings := GenerateClaudeSettings(config, "/scripts")

	// Marshal to JSON to verify wire format
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal settings: %v", err)
	}

	// Unmarshal to generic map for format validation
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal settings: %v", err)
	}

	hooks := parsed["hooks"].(map[string]interface{})

	// Stop hook: must NOT have "matcher" field
	stopArr := hooks["Stop"].([]interface{})
	if len(stopArr) != 1 {
		t.Fatalf("Stop: expected 1 entry, got %d", len(stopArr))
	}
	stopEntry := stopArr[0].(map[string]interface{})
	if _, hasMatcher := stopEntry["matcher"]; hasMatcher {
		t.Error("Stop hook must NOT have matcher field")
	}
	stopHooks := stopEntry["hooks"].([]interface{})
	stopCmd := stopHooks[0].(map[string]interface{})
	if stopCmd["command"] != "/scripts/pilot-stop-gate.sh" {
		t.Errorf("Stop hook command: expected /scripts/pilot-stop-gate.sh, got %v", stopCmd["command"])
	}

	// PreToolUse hook: matcher must be a string "Bash"
	preArr := hooks["PreToolUse"].([]interface{})
	if len(preArr) != 1 {
		t.Fatalf("PreToolUse: expected 1 entry, got %d", len(preArr))
	}
	preEntry := preArr[0].(map[string]interface{})
	preMatcher, ok := preEntry["matcher"].(string)
	if !ok {
		t.Fatalf("PreToolUse matcher: expected string, got %T: %v", preEntry["matcher"], preEntry["matcher"])
	}
	if preMatcher != "Bash" {
		t.Errorf("PreToolUse matcher: expected 'Bash', got '%s'", preMatcher)
	}

	// PostToolUse hook: single entry with "Edit|Write" regex matcher
	postArr := hooks["PostToolUse"].([]interface{})
	if len(postArr) != 1 {
		t.Fatalf("PostToolUse: expected 1 entry, got %d", len(postArr))
	}
	postEntry := postArr[0].(map[string]interface{})
	postMatcher, ok := postEntry["matcher"].(string)
	if !ok {
		t.Fatalf("PostToolUse matcher: expected string, got %T: %v", postEntry["matcher"], postEntry["matcher"])
	}
	if postMatcher != "Edit|Write" {
		t.Errorf("PostToolUse matcher: expected 'Edit|Write', got '%s'", postMatcher)
	}
}

func TestWriteClaudeSettings(t *testing.T) {
	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, ".claude", "settings.json")

	bashMatcher := "Bash"
	settings := map[string]interface{}{
		"hooks": map[string][]HookMatcherEntry{
			"PreToolUse": {
				{
					Matcher: &bashMatcher,
					Hooks:   []HookCommand{{Type: "command", Command: "/test/script.sh"}},
				},
			},
			"Stop": {
				{
					// Matcher nil — Stop hooks must not have matcher
					Hooks: []HookCommand{{Type: "command", Command: "/test/stop.sh"}},
				},
			},
		},
	}

	err := WriteClaudeSettings(settingsPath, settings)
	if err != nil {
		t.Fatalf("Failed to write settings: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse written JSON: %v", err)
	}

	hooks := parsed["hooks"].(map[string]interface{})

	// Verify PreToolUse has string matcher
	preArr := hooks["PreToolUse"].([]interface{})
	preEntry := preArr[0].(map[string]interface{})
	if matcher, ok := preEntry["matcher"].(string); !ok || matcher != "Bash" {
		t.Errorf("PreToolUse matcher: expected string 'Bash', got %T %v", preEntry["matcher"], preEntry["matcher"])
	}

	// Verify Stop has no matcher
	stopArr := hooks["Stop"].([]interface{})
	stopEntry := stopArr[0].(map[string]interface{})
	if _, hasMatcher := stopEntry["matcher"]; hasMatcher {
		t.Error("Stop hook should not have matcher field")
	}
}

func TestMergeWithExisting(t *testing.T) {
	tests := []struct {
		name           string
		existingJSON   string
		pilotSettings  map[string]interface{}
		expectError    bool
		validateResult func(t *testing.T, settingsPath string, restoreFunc func() error)
	}{
		{
			name:         "no existing file",
			existingJSON: "",
			pilotSettings: map[string]interface{}{
				"hooks": map[string][]HookMatcherEntry{
					"Stop": {
						{Hooks: []HookCommand{{Type: "command", Command: "/test/stop.sh"}}},
					},
				},
			},
			validateResult: func(t *testing.T, settingsPath string, restoreFunc func() error) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("Failed to read merged file: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Failed to unmarshal: %v", err)
				}
				if _, ok := parsed["hooks"]; !ok {
					t.Error("Expected hooks in merged file")
				}
				// Test restore removes the file
				if err := restoreFunc(); err != nil {
					t.Errorf("Restore failed: %v", err)
				}
				if _, err := os.ReadFile(settingsPath); !os.IsNotExist(err) {
					t.Error("Expected file to be removed after restore")
				}
			},
		},
		{
			name:         "existing file with old format hooks - replace",
			existingJSON: `{"other": "value", "hooks": {"Existing": {"command": "/existing.sh"}}}`,
			pilotSettings: map[string]interface{}{
				"hooks": map[string][]HookMatcherEntry{
					"Stop": {
						{Hooks: []HookCommand{{Type: "command", Command: "/test/stop.sh"}}},
					},
				},
			},
			validateResult: func(t *testing.T, settingsPath string, restoreFunc func() error) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("Failed to read: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Failed to unmarshal: %v", err)
				}
				if parsed["other"] != "value" {
					t.Error("Expected existing 'other' field preserved")
				}
				hooks := parsed["hooks"].(map[string]interface{})
				if _, hasStop := hooks["Stop"]; !hasStop {
					t.Error("Expected Stop hook from pilot")
				}
				if err := restoreFunc(); err != nil {
					t.Errorf("Restore failed: %v", err)
				}
			},
		},
		{
			name:          "empty pilot settings is no-op",
			existingJSON:  `{"other": "value"}`,
			pilotSettings: map[string]interface{}{},
			validateResult: func(t *testing.T, settingsPath string, _ func() error) {
				data, _ := os.ReadFile(settingsPath)
				if string(data) != `{"other": "value"}` {
					t.Error("Expected file unchanged for empty pilot settings")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			settingsPath := filepath.Join(tempDir, ".claude", "settings.json")

			if tt.existingJSON != "" {
				if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
					t.Fatalf("Failed to create dir: %v", err)
				}
				if err := os.WriteFile(settingsPath, []byte(tt.existingJSON), 0644); err != nil {
					t.Fatalf("Failed to write: %v", err)
				}
			}

			restoreFunc, err := MergeWithExisting(settingsPath, tt.pilotSettings)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if !tt.expectError && tt.validateResult != nil {
				tt.validateResult(t, settingsPath, restoreFunc)
			}
		})
	}
}

func TestWriteEmbeddedScripts(t *testing.T) {
	tempDir := t.TempDir()

	err := WriteEmbeddedScripts(tempDir)
	if err != nil {
		t.Fatalf("Failed to write embedded scripts: %v", err)
	}

	for _, script := range []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh", "pilot-lint.sh"} {
		scriptPath := filepath.Join(tempDir, script)
		info, err := os.Stat(scriptPath)
		if err != nil {
			t.Errorf("Script %s not found: %v", script, err)
			continue
		}
		if info.Mode()&0111 == 0 {
			t.Errorf("Script %s is not executable", script)
		}
		content, err := os.ReadFile(scriptPath)
		if err != nil {
			t.Errorf("Failed to read script %s: %v", script, err)
		}
		if len(content) == 0 {
			t.Errorf("Script %s is empty", script)
		}
	}
}

func TestGetBoolPtrValue(t *testing.T) {
	tests := []struct {
		name         string
		ptr          *bool
		defaultValue bool
		expected     bool
	}{
		{"nil ptr, default true", nil, true, true},
		{"nil ptr, default false", nil, false, false},
		{"false ptr, default true", boolPtr(false), true, false},
		{"true ptr, default false", boolPtr(true), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := GetBoolPtrValue(tt.ptr, tt.defaultValue); result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetScriptNames(t *testing.T) {
	tests := []struct {
		name     string
		config   *HooksConfig
		expected []string
	}{
		{"nil config", nil, nil},
		{"disabled", &HooksConfig{Enabled: false}, nil},
		{"defaults", &HooksConfig{Enabled: true}, []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh"}},
		{"all features", &HooksConfig{Enabled: true, LintOnSave: true}, []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh", "pilot-lint.sh"}},
		{"all disabled", &HooksConfig{Enabled: true, RunTestsOnStop: boolPtr(false), BlockDestructive: boolPtr(false)}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetScriptNames(tt.config)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d scripts, got %d", len(tt.expected), len(result))
			}
		})
	}
}

func TestMergeNewFormatHooks_DeduplicatesStalePilotEntries(t *testing.T) {
	// Simulate the crash scenario: settings.json has stale pilot entries
	// from a previous run's temp dir, plus a user-defined hook.
	// Fresh pilot hooks should replace all stale pilot entries.

	// Create two temp dirs to simulate old and new pilot runs
	oldDir := t.TempDir()
	newDir := t.TempDir()

	// Write scripts to both dirs so hookFileExists returns true
	for _, dir := range []string{oldDir, newDir} {
		for _, name := range []string{"pilot-bash-guard.sh", "pilot-stop-gate.sh"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Also create a user hook script
	userDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(userDir, "my-custom-hook.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Existing settings: 3 stale pilot entries + 1 user entry for PreToolUse
	existing := map[string]interface{}{
		"PreToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Bash",
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": filepath.Join(oldDir, "pilot-bash-guard.sh")},
				},
			},
			map[string]interface{}{
				"matcher": "Bash",
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": filepath.Join(userDir, "my-custom-hook.sh")},
				},
			},
		},
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": filepath.Join(oldDir, "pilot-stop-gate.sh")},
				},
			},
		},
	}

	// Fresh pilot hooks (new temp dir)
	bashMatcher := "Bash"
	pilot := map[string][]HookMatcherEntry{
		"PreToolUse": {
			{
				Matcher: &bashMatcher,
				Hooks:   []HookCommand{{Type: "command", Command: filepath.Join(newDir, "pilot-bash-guard.sh")}},
			},
		},
		"Stop": {
			{
				Hooks: []HookCommand{{Type: "command", Command: filepath.Join(newDir, "pilot-stop-gate.sh")}},
			},
		},
	}

	merged := mergeNewFormatHooks(existing, pilot)

	// PreToolUse should have exactly 2 entries: fresh pilot + user hook
	preEntries := merged["PreToolUse"]
	var preCount int
	switch v := preEntries.(type) {
	case []HookMatcherEntry:
		preCount = len(v)
	case []interface{}:
		preCount = len(v)
	}
	if preCount != 2 {
		t.Errorf("PreToolUse: expected 2 entries (1 fresh pilot + 1 user), got %d", preCount)
	}

	// Stop should have exactly 1 entry: fresh pilot only
	stopEntries := merged["Stop"]
	var stopCount int
	switch v := stopEntries.(type) {
	case []HookMatcherEntry:
		stopCount = len(v)
	case []interface{}:
		stopCount = len(v)
	}
	if stopCount != 1 {
		t.Errorf("Stop: expected 1 entry (fresh pilot only), got %d", stopCount)
	}
}

func TestIsPilotManagedHook(t *testing.T) {
	tests := []struct {
		cmd      string
		expected bool
	}{
		{"/var/folders/xx/T/pilot-hooks-123/pilot-bash-guard.sh", true},
		{"/var/folders/xx/T/pilot-hooks-456/pilot-stop-gate.sh", true},
		{"/var/folders/xx/T/pilot-hooks-789/pilot-lint.sh", true},
		{"/home/user/.config/my-custom-hook.sh", false},
		{"/usr/local/bin/lint.sh", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := isPilotManagedHook(tt.cmd); got != tt.expected {
				t.Errorf("isPilotManagedHook(%q) = %v, want %v", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestCleanStalePilotHooks(t *testing.T) {
	type testCase struct {
		name         string
		buildJSON    func(scriptDir string) string
		validate     func(t *testing.T, settingsPath string, scriptDir string)
	}

	tests := []testCase{
		{
			name: "no-op when settings file does not exist",
			buildJSON: func(scriptDir string) string { return "" },
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				if _, err := os.ReadFile(settingsPath); !os.IsNotExist(err) {
					t.Error("Expected no file to exist")
				}
			},
		},
		{
			name: "removes stale pilot entries with dead script paths",
			buildJSON: func(scriptDir string) string {
				// scriptDir exists but scripts are NOT written — dead paths
				return `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` +
					filepath.Join(scriptDir, "pilot-bash-guard.sh") + `"}]}],"Stop":[{"hooks":[{"type":"command","command":"` +
					filepath.Join(scriptDir, "pilot-stop-gate.sh") + `"}]}]}}`
			},
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				if _, hasHooks := parsed["hooks"]; hasHooks {
					t.Error("Expected hooks key removed after all stale entries cleaned")
				}
			},
		},
		{
			name: "removes entries with old object-format matchers",
			buildJSON: func(scriptDir string) string {
				// Write the script so it exists — only the matcher format should trigger removal
				_ = os.WriteFile(filepath.Join(scriptDir, "pilot-bash-guard.sh"), []byte("#!/bin/sh\n"), 0755)
				return `{"hooks":{"PreToolUse":[{"matcher":{"tools":["Bash"]},"hooks":[{"type":"command","command":"` +
					filepath.Join(scriptDir, "pilot-bash-guard.sh") + `"}]}]}}`
			},
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				if _, hasHooks := parsed["hooks"]; hasHooks {
					t.Error("Expected hooks key removed after object-format entry cleaned")
				}
			},
		},
		{
			name: "preserves user (non-pilot) hooks even when hooks disabled",
			buildJSON: func(scriptDir string) string {
				// Write user hook and stale pilot hook
				_ = os.WriteFile(filepath.Join(scriptDir, "my-hook.sh"), []byte("#!/bin/sh\n"), 0755)
				// pilot-bash-guard.sh NOT written — stale
				return `{"hooks":{"PreToolUse":[` +
					`{"matcher":"Bash","hooks":[{"type":"command","command":"` + filepath.Join(scriptDir, "pilot-bash-guard.sh") + `"}]},` +
					`{"matcher":"Bash","hooks":[{"type":"command","command":"` + filepath.Join(scriptDir, "my-hook.sh") + `"}]}` +
					`]}}`
			},
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				hooks, ok := parsed["hooks"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected hooks to remain")
				}
				preArr, ok := hooks["PreToolUse"].([]interface{})
				if !ok || len(preArr) != 1 {
					t.Errorf("Expected 1 PreToolUse entry (user hook), got %v", hooks["PreToolUse"])
				}
			},
		},
		{
			name: "removes hooks key entirely when all entries are stale",
			buildJSON: func(scriptDir string) string {
				return `{"other":"value","hooks":{"Stop":[{"hooks":[{"type":"command","command":"` +
					filepath.Join(scriptDir, "pilot-stop-gate.sh") + `"}]}]}}`
			},
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				if _, hasHooks := parsed["hooks"]; hasHooks {
					t.Error("Expected hooks key removed")
				}
				if parsed["other"] != "value" {
					t.Error("Expected other fields preserved")
				}
			},
		},
		{
			name: "no-op when settings has no stale entries",
			buildJSON: func(scriptDir string) string {
				_ = os.WriteFile(filepath.Join(scriptDir, "pilot-stop-gate.sh"), []byte("#!/bin/sh\n"), 0755)
				return `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` +
					filepath.Join(scriptDir, "pilot-stop-gate.sh") + `"}]}]}}`
			},
			validate: func(t *testing.T, settingsPath string, scriptDir string) {
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				hooks, ok := parsed["hooks"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected hooks to remain")
				}
				if _, hasStop := hooks["Stop"]; !hasStop {
					t.Error("Expected Stop hook preserved when script exists")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			settingsPath := filepath.Join(tempDir, ".claude", "settings.json")
			scriptDir := t.TempDir()

			settingsJSON := tc.buildJSON(scriptDir)
			if settingsJSON != "" {
				if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			if err := CleanStalePilotHooks(settingsPath); err != nil {
				t.Fatalf("CleanStalePilotHooks: %v", err)
			}

			tc.validate(t, settingsPath, scriptDir)
		})
	}
}

func TestCleanStalePilotHooks_DualPathCleanup(t *testing.T) {
	// Simulates GH-1884: stale entries exist in both project root and worktree.
	// CleanStalePilotHooks called on each path independently should clean both.
	projectDir := t.TempDir()
	worktreeDir := t.TempDir()

	// Both paths have stale pilot hooks (scripts don't exist on disk)
	staleJSON := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/tmp/gone/pilot-stop-gate.sh"}]}]}}`
	for _, dir := range []string{projectDir, worktreeDir} {
		settingsPath := filepath.Join(dir, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settingsPath, []byte(staleJSON), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Clean project root
	if err := CleanStalePilotHooks(filepath.Join(projectDir, ".claude", "settings.json")); err != nil {
		t.Fatalf("CleanStalePilotHooks (project root): %v", err)
	}
	// Clean worktree
	if err := CleanStalePilotHooks(filepath.Join(worktreeDir, ".claude", "settings.json")); err != nil {
		t.Fatalf("CleanStalePilotHooks (worktree): %v", err)
	}

	// Both should have hooks key removed
	for _, dir := range []string{projectDir, worktreeDir} {
		data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if _, hasHooks := parsed["hooks"]; hasHooks {
			t.Errorf("Expected hooks removed in %s, but still present", dir)
		}
	}
}

func TestRestoreUsesCleanupInsteadOfBlindRestore(t *testing.T) {
	// Verifies GH-1884: after hooks are installed, cleanup should use
	// CleanStalePilotHooks rather than restoring originalData which may
	// itself contain stale entries from a previous crash.

	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, ".claude", "settings.json")

	// Simulate "original" settings that already have stale pilot entries
	// (from a previous crash — scripts don't exist on disk)
	staleOriginal := `{"other":"keep","hooks":{"Stop":[{"hooks":[{"type":"command","command":"/tmp/old-crash/pilot-stop-gate.sh"}]}]}}`
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(staleOriginal), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fresh pilot hooks (scripts exist)
	scriptDir := t.TempDir()
	for _, name := range []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh"} {
		if err := os.WriteFile(filepath.Join(scriptDir, name), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	config := &HooksConfig{Enabled: true}
	hookSettings := GenerateClaudeSettings(config, scriptDir)

	// Merge (this captures the stale originalData internally)
	_, mergeErr := MergeWithExisting(settingsPath, hookSettings)
	if mergeErr != nil {
		t.Fatalf("MergeWithExisting: %v", mergeErr)
	}

	// Now simulate what the new hookRestoreFunc does: CleanStalePilotHooks
	// instead of calling the blind restoreFunc
	if err := CleanStalePilotHooks(settingsPath); err != nil {
		t.Fatalf("CleanStalePilotHooks: %v", err)
	}

	// Remove script dir to make current pilot entries stale too
	if err := os.RemoveAll(scriptDir); err != nil {
		t.Fatal(err)
	}

	// Run cleanup again (simulates what happens after scriptDir removal)
	if err := CleanStalePilotHooks(settingsPath); err != nil {
		t.Fatalf("CleanStalePilotHooks (second pass): %v", err)
	}

	// Verify: no stale pilot hooks remain, but "other" field is preserved
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, hasHooks := parsed["hooks"]; hasHooks {
		t.Error("Expected all stale pilot hooks removed, but hooks key still present")
	}
	if parsed["other"] != "keep" {
		t.Error("Expected non-hook fields preserved")
	}
}

func boolPtr(b bool) *bool {
	return &b
}
