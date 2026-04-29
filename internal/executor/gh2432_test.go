package executor

import (
	"reflect"
	"strings"
	"testing"
)

// GH-2432: "Opus plans, Sonnet executes" — verify the Sonnet/Opus split,
// AllowedTools/MCPConfig wiring, and Stop-hook default flip.

func TestDefaultBackendConfig_SonnetForComplex(t *testing.T) {
	cfg := DefaultBackendConfig()
	if got, want := cfg.ModelRouting.Complex, "claude-sonnet-4-6"; got != want {
		t.Errorf("ModelRouting.Complex = %q, want %q (GH-2432: Opus reserved for planning only)", got, want)
	}
}

func TestDefaultPlanningConfig_OpusModel(t *testing.T) {
	cfg := DefaultPlanningConfig()
	if cfg.Model != "claude-opus-4-7" {
		t.Errorf("Planning.Model default = %q, want claude-opus-4-7", cfg.Model)
	}
}

func TestDefaultBackendConfig_PlanningWired(t *testing.T) {
	cfg := DefaultBackendConfig()
	if cfg.Planning == nil {
		t.Fatal("DefaultBackendConfig() should populate Planning (GH-2432)")
	}
	if cfg.Planning.Model != "claude-opus-4-7" {
		t.Errorf("DefaultBackendConfig.Planning.Model = %q, want claude-opus-4-7", cfg.Planning.Model)
	}
}

func TestDefaultClaudeCodeConfig_AllowedToolsExecution(t *testing.T) {
	cfg := DefaultBackendConfig()
	if cfg.ClaudeCode == nil {
		t.Fatal("DefaultBackendConfig.ClaudeCode is nil")
	}
	want := DefaultAllowedToolsExecution()
	if !reflect.DeepEqual(cfg.ClaudeCode.AllowedTools, want) {
		t.Errorf("ClaudeCode.AllowedTools = %v, want %v", cfg.ClaudeCode.AllowedTools, want)
	}
	if cfg.ClaudeCode.MCPConfigPath != "" {
		t.Errorf("ClaudeCode.MCPConfigPath = %q, want empty (no MCPs by default)", cfg.ClaudeCode.MCPConfigPath)
	}
}

func TestDefaultAllowedTools_PlanningIsReadOnly(t *testing.T) {
	tools := DefaultAllowedToolsPlanning()
	for _, banned := range []string{"Write", "Edit", "Bash"} {
		for _, tool := range tools {
			if tool == banned {
				t.Errorf("planning tools contain %q — planning must be read-only", banned)
			}
		}
	}
	for _, required := range []string{"Read", "Grep", "Glob"} {
		found := false
		for _, tool := range tools {
			if tool == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("planning tools missing required tool %q", required)
		}
	}
}

func TestExecuteOptions_HasAllowedToolsAndMCPConfigPath(t *testing.T) {
	// Compile-time check via field access.
	opts := ExecuteOptions{
		AllowedTools:  []string{"Read", "Bash"},
		MCPConfigPath: "/tmp/mcp.json",
	}
	if len(opts.AllowedTools) != 2 || opts.MCPConfigPath != "/tmp/mcp.json" {
		t.Error("ExecuteOptions.AllowedTools/MCPConfigPath not wired (GH-2432)")
	}
}

func TestRunner_ExecutionToolOptions_FromConfig(t *testing.T) {
	r := &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				AllowedTools:  []string{"Read", "Edit"},
				MCPConfigPath: "/path/to/mcp.json",
			},
		},
	}
	allowed, mcp := r.executionToolOptions()
	if !reflect.DeepEqual(allowed, []string{"Read", "Edit"}) {
		t.Errorf("allowed = %v, want [Read Edit]", allowed)
	}
	if mcp != "/path/to/mcp.json" {
		t.Errorf("mcp = %q, want /path/to/mcp.json", mcp)
	}
}

func TestRunner_ExecutionToolOptions_NilSafe(t *testing.T) {
	r := &Runner{config: nil}
	allowed, mcp := r.executionToolOptions()
	if allowed != nil || mcp != "" {
		t.Errorf("nil config: allowed=%v mcp=%q, want nil/empty", allowed, mcp)
	}
}

func TestDefaultHooksConfig_RunTestsOnStop_FlippedToFalse(t *testing.T) {
	cfg := DefaultHooksConfig()
	if cfg.RunTestsOnStop == nil {
		t.Fatal("RunTestsOnStop is nil")
	}
	if *cfg.RunTestsOnStop {
		t.Error("RunTestsOnStop should default to false (GH-2432: cut subprocess token spend)")
	}
}

// Verify the planning tools list contains exactly what we ship by default —
// guards against accidental write-tool additions.
func TestDefaultAllowedToolsPlanning_Exact(t *testing.T) {
	got := strings.Join(DefaultAllowedToolsPlanning(), ",")
	want := "Read,Grep,Glob"
	if got != want {
		t.Errorf("planning tools = %q, want %q", got, want)
	}
}
