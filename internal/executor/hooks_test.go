package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHooksConfig_Defaults(t *testing.T) {
	config := DefaultHooksConfig()

	// Verify default values
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
		name     string
		config   *HooksConfig
		expected map[string]interface{}
	}{
		{
			name:     "nil config",
			config:   nil,
			expected: map[string]interface{}{},
		},
		{
			name: "disabled config",
			config: &HooksConfig{
				Enabled: false,
			},
			expected: map[string]interface{}{},
		},
		{
			name: "enabled with defaults",
			config: &HooksConfig{
				Enabled: true,
			},
			expected: map[string]interface{}{
				"hooks": map[string]HookDefinition{
					"Stop":            {Command: "/test/scripts/pilot-stop-gate.sh"},
					"PreToolUse:Bash": {Command: "/test/scripts/pilot-bash-guard.sh"},
				},
			},
		},
		{
			name: "enabled with lint on save",
			config: &HooksConfig{
				Enabled:    true,
				LintOnSave: true,
			},
			expected: map[string]interface{}{
				"hooks": map[string]HookDefinition{
					"Stop":              {Command: "/test/scripts/pilot-stop-gate.sh"},
					"PreToolUse:Bash":   {Command: "/test/scripts/pilot-bash-guard.sh"},
					"PostToolUse:Edit":  {Command: "/test/scripts/pilot-lint.sh"},
					"PostToolUse:Write": {Command: "/test/scripts/pilot-lint.sh"},
				},
			},
		},
		{
			name: "enabled with disabled features",
			config: &HooksConfig{
				Enabled:          true,
				RunTestsOnStop:   boolPtr(false),
				BlockDestructive: boolPtr(false),
			},
			expected: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateClaudeSettings(tt.config, "/test/scripts")

			// Compare the results
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d keys, got %d", len(tt.expected), len(result))
			}

			if len(tt.expected) > 0 {
				hooks, ok := result["hooks"]
				if !ok {
					t.Fatal("Expected hooks key in result")
				}

				expectedHooks := tt.expected["hooks"].(map[string]HookDefinition)
				actualHooks, ok := hooks.(map[string]HookDefinition)
				if !ok {
					t.Fatal("Expected hooks to be map[string]HookDefinition")
				}

				if len(actualHooks) != len(expectedHooks) {
					t.Errorf("Expected %d hooks, got %d", len(expectedHooks), len(actualHooks))
				}

				for key, expected := range expectedHooks {
					actual, exists := actualHooks[key]
					if !exists {
						t.Errorf("Expected hook %s not found", key)
						continue
					}
					if actual.Command != expected.Command {
						t.Errorf("Hook %s: expected command %s, got %s", key, expected.Command, actual.Command)
					}
				}
			}
		})
	}
}

func TestWriteClaudeSettings(t *testing.T) {
	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, ".claude", "settings.json")

	settings := map[string]interface{}{
		"hooks": map[string]HookDefinition{
			"Stop": {Command: "/test/script.sh"},
		},
	}

	// Write settings
	err := WriteClaudeSettings(settingsPath, settings)
	if err != nil {
		t.Fatalf("Failed to write settings: %v", err)
	}

	// Verify file exists and content is correct
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse written JSON: %v", err)
	}

	hooks, ok := parsed["hooks"]
	if !ok {
		t.Error("Expected hooks key in parsed JSON")
	}

	// JSON unmarshaling converts our HookDefinition to map[string]interface{}
	hooksMap := hooks.(map[string]interface{})
	stopHook := hooksMap["Stop"].(map[string]interface{})
	if stopHook["command"] != "/test/script.sh" {
		t.Errorf("Expected Stop hook command '/test/script.sh', got %v", stopHook["command"])
	}
}

func TestMergeWithExisting(t *testing.T) {
	tempDir := t.TempDir()
	settingsPath := filepath.Join(tempDir, ".claude", "settings.json")

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
				"hooks": map[string]HookDefinition{
					"Stop": {Command: "/test/stop.sh"},
				},
			},
			expectError: false,
			validateResult: func(t *testing.T, settingsPath string, restoreFunc func() error) {
				// Verify file was created
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("Failed to read merged file: %v", err)
				}

				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Failed to unmarshal merged file: %v", err)
				}
				if _, ok := parsed["hooks"]; !ok {
					t.Error("Expected hooks in merged file")
				}

				// Test restore
				if err := restoreFunc(); err != nil {
					t.Errorf("Restore function failed: %v", err)
				}

				// File should be removed
				if _, err := os.ReadFile(settingsPath); !os.IsNotExist(err) {
					t.Error("Expected file to be removed after restore")
				}
			},
		},
		{
			name:         "existing file with other settings",
			existingJSON: `{"other": "value", "hooks": {"Existing": {"command": "/existing.sh"}}}`,
			pilotSettings: map[string]interface{}{
				"hooks": map[string]HookDefinition{
					"Stop": {Command: "/test/stop.sh"},
				},
			},
			expectError: false,
			validateResult: func(t *testing.T, settingsPath string, restoreFunc func() error) {
				// Verify merge happened correctly
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("Failed to read merged file: %v", err)
				}

				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("Failed to unmarshal merged file: %v", err)
				}

				// Should have both "other" and "hooks"
				if parsed["other"] != "value" {
					t.Error("Expected existing 'other' field to be preserved")
				}

				hooks := parsed["hooks"].(map[string]interface{})
				if len(hooks) != 2 {
					t.Errorf("Expected 2 hooks, got %d", len(hooks))
				}

				// Test restore
				if err := restoreFunc(); err != nil {
					t.Errorf("Restore function failed: %v", err)
				}

				// Original content should be restored
				data, err = os.ReadFile(settingsPath)
				if err != nil {
					t.Fatalf("Failed to read restored file: %v", err)
				}
				var restored map[string]interface{}
				if err := json.Unmarshal(data, &restored); err != nil {
					t.Fatalf("Failed to unmarshal restored file: %v", err)
				}

				restoredHooks := restored["hooks"].(map[string]interface{})
				if len(restoredHooks) != 1 {
					t.Error("Expected only original hook after restore")
				}
			},
		},
		{
			name:          "empty pilot settings",
			existingJSON:  `{"other": "value"}`,
			pilotSettings: map[string]interface{}{},
			expectError:   false,
			validateResult: func(t *testing.T, settingsPath string, restoreFunc func() error) {
				// Should be no-op, file unchanged
				data, _ := os.ReadFile(settingsPath)
				if string(data) != `{"other": "value"}` {
					t.Error("Expected file to remain unchanged for empty pilot settings")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup existing file if specified
			if tt.existingJSON != "" {
				if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
				if err := os.WriteFile(settingsPath, []byte(tt.existingJSON), 0644); err != nil {
					t.Fatalf("Failed to write test file: %v", err)
				}
			} else {
				// Clean up any existing file
				_ = os.RemoveAll(settingsPath) // Ignore error - file may not exist
			}

			restoreFunc, err := MergeWithExisting(settingsPath, tt.pilotSettings)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
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

	// Verify scripts were written
	expectedScripts := []string{
		"pilot-stop-gate.sh",
		"pilot-bash-guard.sh",
		"pilot-lint.sh",
	}

	for _, script := range expectedScripts {
		scriptPath := filepath.Join(tempDir, script)
		info, err := os.Stat(scriptPath)
		if err != nil {
			t.Errorf("Script %s not found: %v", script, err)
			continue
		}

		// Verify executable permissions
		if info.Mode()&0111 == 0 {
			t.Errorf("Script %s is not executable", script)
		}

		// Verify content is not empty
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
			result := GetBoolPtrValue(tt.ptr, tt.defaultValue)
			if result != tt.expected {
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
		{
			name:     "nil config",
			config:   nil,
			expected: nil,
		},
		{
			name: "disabled config",
			config: &HooksConfig{
				Enabled: false,
			},
			expected: nil,
		},
		{
			name: "enabled with defaults",
			config: &HooksConfig{
				Enabled: true,
			},
			expected: []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh"},
		},
		{
			name: "enabled with all features",
			config: &HooksConfig{
				Enabled:    true,
				LintOnSave: true,
			},
			expected: []string{"pilot-stop-gate.sh", "pilot-bash-guard.sh", "pilot-lint.sh"},
		},
		{
			name: "enabled with disabled features",
			config: &HooksConfig{
				Enabled:          true,
				RunTestsOnStop:   boolPtr(false),
				BlockDestructive: boolPtr(false),
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetScriptNames(tt.config)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d scripts, got %d", len(tt.expected), len(result))
			}

			for i, expected := range tt.expected {
				if i >= len(result) || result[i] != expected {
					t.Errorf("Expected script %s at index %d, got %v", expected, i, result)
				}
			}
		})
	}
}

// Helper function to create bool pointers
func boolPtr(b bool) *bool {
	return &b
}