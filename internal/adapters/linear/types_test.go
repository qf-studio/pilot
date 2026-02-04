package linear

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Enabled != false {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}
	if cfg.PilotLabel != "pilot" {
		t.Errorf("default PilotLabel = %s, want 'pilot'", cfg.PilotLabel)
	}
	if cfg.AutoAssign != true {
		t.Errorf("default AutoAssign = %v, want true", cfg.AutoAssign)
	}
	if cfg.APIKey != "" {
		t.Errorf("default APIKey = %s, want empty", cfg.APIKey)
	}
	if cfg.TeamID != "" {
		t.Errorf("default TeamID = %s, want empty", cfg.TeamID)
	}
}

func TestPriorityName(t *testing.T) {
	tests := []struct {
		priority int
		want     string
	}{
		{PriorityNone, "No Priority"},
		{PriorityUrgent, "Urgent"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
		{-1, "No Priority"},  // Unknown negative
		{5, "No Priority"},   // Unknown positive
		{100, "No Priority"}, // Out of range
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := PriorityName(tt.priority)
			if got != tt.want {
				t.Errorf("PriorityName(%d) = %s, want %s", tt.priority, got, tt.want)
			}
		})
	}
}

func TestPriorityConstants(t *testing.T) {
	tests := []struct {
		name     string
		priority int
		want     int
	}{
		{"PriorityNone", PriorityNone, 0},
		{"PriorityUrgent", PriorityUrgent, 1},
		{"PriorityHigh", PriorityHigh, 2},
		{"PriorityMedium", PriorityMedium, 3},
		{"PriorityLow", PriorityLow, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.priority != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.priority, tt.want)
			}
		})
	}
}

func TestStateTypeConstants(t *testing.T) {
	tests := []struct {
		stateType StateType
		want      string
	}{
		{StateTypeBacklog, "backlog"},
		{StateTypeUnstarted, "unstarted"},
		{StateTypeStarted, "started"},
		{StateTypeCompleted, "completed"},
		{StateTypeCanceled, "canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.stateType) != tt.want {
				t.Errorf("StateType = %s, want %s", tt.stateType, tt.want)
			}
		})
	}
}

func TestConfigStructure(t *testing.T) {
	// Verify Config struct can be properly initialized
	cfg := &Config{
		Enabled:    true,
		APIKey:     "lin_api_key",
		TeamID:     "team-123",
		AutoAssign: false,
		PilotLabel: "custom-label",
	}

	if !cfg.Enabled {
		t.Error("cfg.Enabled should be true")
	}
	if cfg.APIKey != "lin_api_key" {
		t.Errorf("cfg.APIKey = %s, want 'lin_api_key'", cfg.APIKey)
	}
	if cfg.TeamID != "team-123" {
		t.Errorf("cfg.TeamID = %s, want 'team-123'", cfg.TeamID)
	}
	if cfg.AutoAssign {
		t.Error("cfg.AutoAssign should be false")
	}
	if cfg.PilotLabel != "custom-label" {
		t.Errorf("cfg.PilotLabel = %s, want 'custom-label'", cfg.PilotLabel)
	}
}

func TestStateTypeComparison(t *testing.T) {
	// Verify StateType can be used in comparisons
	currentState := StateTypeStarted

	if currentState != StateTypeStarted {
		t.Error("state comparison failed")
	}

	if currentState == StateTypeCompleted {
		t.Error("state should not equal completed")
	}

	// Verify string conversion works
	if string(currentState) != "started" {
		t.Errorf("string(StateTypeStarted) = %s, want 'started'", string(currentState))
	}
}

func TestConfig_GetWorkspaces_Legacy(t *testing.T) {
	// Test legacy single-workspace mode
	cfg := &Config{
		Enabled:    true,
		APIKey:     "test-api-key",
		TeamID:     "TEAM1",
		PilotLabel: "pilot",
		AutoAssign: true,
		ProjectIDs: []string{"proj1", "proj2"},
	}

	workspaces := cfg.GetWorkspaces()
	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}

	ws := workspaces[0]
	if ws.Name != "default" {
		t.Errorf("workspace name = %s, want 'default'", ws.Name)
	}
	if ws.APIKey != "test-api-key" {
		t.Errorf("workspace APIKey = %s, want 'test-api-key'", ws.APIKey)
	}
	if ws.TeamID != "TEAM1" {
		t.Errorf("workspace TeamID = %s, want 'TEAM1'", ws.TeamID)
	}
	if ws.PilotLabel != "pilot" {
		t.Errorf("workspace PilotLabel = %s, want 'pilot'", ws.PilotLabel)
	}
	if !ws.AutoAssign {
		t.Error("workspace AutoAssign should be true")
	}
	if len(ws.ProjectIDs) != 2 {
		t.Errorf("workspace ProjectIDs = %v, want 2 items", ws.ProjectIDs)
	}
}

func TestConfig_GetWorkspaces_Multi(t *testing.T) {
	// Test multi-workspace mode
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "T1"},
			{Name: "ws2", APIKey: "key2", TeamID: "T2"},
		},
		// Legacy fields should be ignored when Workspaces is set
		APIKey: "ignored",
		TeamID: "IGNORED",
	}

	workspaces := cfg.GetWorkspaces()
	if len(workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(workspaces))
	}

	if workspaces[0].Name != "ws1" {
		t.Errorf("first workspace name = %s, want 'ws1'", workspaces[0].Name)
	}
	if workspaces[1].Name != "ws2" {
		t.Errorf("second workspace name = %s, want 'ws2'", workspaces[1].Name)
	}
}

func TestConfig_GetWorkspaces_Empty(t *testing.T) {
	// Test empty config
	cfg := &Config{Enabled: true}

	workspaces := cfg.GetWorkspaces()
	if workspaces != nil {
		t.Errorf("expected nil workspaces for empty config, got %v", workspaces)
	}
}

func TestConfig_GetWorkspaces_DefaultPilotLabel(t *testing.T) {
	// Test that default pilot label is set when not specified
	cfg := &Config{
		Enabled: true,
		APIKey:  "test-key",
		TeamID:  "T1",
		// PilotLabel not set
	}

	workspaces := cfg.GetWorkspaces()
	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}

	if workspaces[0].PilotLabel != "pilot" {
		t.Errorf("default PilotLabel = %s, want 'pilot'", workspaces[0].PilotLabel)
	}
}

func TestConfig_Validate_DuplicateTeamID(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "SAME"},
			{Name: "ws2", APIKey: "key2", TeamID: "SAME"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate team IDs")
	}

	dupErr, ok := err.(*DuplicateTeamIDError)
	if !ok {
		t.Fatalf("expected DuplicateTeamIDError, got %T", err)
	}

	if dupErr.TeamID != "SAME" {
		t.Errorf("error TeamID = %s, want 'SAME'", dupErr.TeamID)
	}
}

func TestConfig_Validate_Disabled(t *testing.T) {
	cfg := &Config{
		Enabled: false,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", TeamID: "SAME"},
			{Name: "ws2", TeamID: "SAME"},
		},
	}

	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error when disabled, got: %v", err)
	}
}

func TestConfig_Validate_UniqueTeamIDs(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "T1"},
			{Name: "ws2", APIKey: "key2", TeamID: "T2"},
			{Name: "ws3", APIKey: "key3", TeamID: "T3"},
		},
	}

	err := cfg.Validate()
	if err != nil {
		t.Errorf("unexpected error for unique team IDs: %v", err)
	}
}

func TestDuplicateTeamIDError_Error(t *testing.T) {
	err := &DuplicateTeamIDError{
		TeamID:     "APP",
		Workspace1: "appbooster",
		Workspace2: "another",
	}

	expected := "duplicate team_id 'APP' in workspaces 'appbooster' and 'another'"
	if err.Error() != expected {
		t.Errorf("error message = %q, want %q", err.Error(), expected)
	}
}

func TestPriorityOrdering(t *testing.T) {
	// Verify priority ordering: Urgent > High > Medium > Low > None
	// In Linear, lower number = higher priority
	if PriorityUrgent >= PriorityHigh {
		t.Error("Urgent should have lower value than High")
	}
	if PriorityHigh >= PriorityMedium {
		t.Error("High should have lower value than Medium")
	}
	if PriorityMedium >= PriorityLow {
		t.Error("Medium should have lower value than Low")
	}
	// PriorityNone (0) is special - means no priority set
	if PriorityNone != 0 {
		t.Error("PriorityNone should be 0")
	}
}
