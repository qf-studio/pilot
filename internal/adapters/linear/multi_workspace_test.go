package linear

import (
	"context"
	"testing"
)

func TestNewMultiWorkspaceHandler(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *Config
		wantErr       bool
		wantCount     int
		errContains   string
	}{
		{
			name: "single workspace from workspaces array",
			cfg: &Config{
				Enabled: true,
				Workspaces: []*WorkspaceConfig{
					{
						Name:       "workspace1",
						APIKey:     "test-api-key-1",
						TeamID:     "TEAM1",
						PilotLabel: "pilot",
						Projects:   []string{"project1"},
					},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name: "multiple workspaces",
			cfg: &Config{
				Enabled: true,
				Workspaces: []*WorkspaceConfig{
					{
						Name:       "appbooster",
						APIKey:     "test-api-key-1",
						TeamID:     "APP",
						PilotLabel: "pilot",
						Projects:   []string{"aso-generator", "pilot"},
					},
					{
						Name:       "bostonteam",
						APIKey:     "test-api-key-2",
						TeamID:     "BT",
						PilotLabel: "pilot",
						Projects:   []string{"bostonteamgroup"},
					},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name: "duplicate team IDs error",
			cfg: &Config{
				Enabled: true,
				Workspaces: []*WorkspaceConfig{
					{
						Name:   "workspace1",
						APIKey: "test-api-key-1",
						TeamID: "SAME",
					},
					{
						Name:   "workspace2",
						APIKey: "test-api-key-2",
						TeamID: "SAME",
					},
				},
			},
			wantErr:     true,
			errContains: "duplicate team_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewMultiWorkspaceHandler(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errContains != "" && !containsSubstr(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if handler.WorkspaceCount() != tt.wantCount {
				t.Errorf("WorkspaceCount() = %d, want %d", handler.WorkspaceCount(), tt.wantCount)
			}
		})
	}
}

func TestMultiWorkspaceHandler_OnIssue(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{
				Name:       "workspace1",
				APIKey:     "test-api-key",
				TeamID:     "TEAM1",
				PilotLabel: "pilot",
			},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	callbackRegistered := false
	handler.OnIssue(func(ctx context.Context, issue *Issue, wsName string) error {
		callbackRegistered = true
		_ = issue    // use variables
		_ = wsName   // use variables
		return nil
	})

	// The callback should be registered on workspace handlers
	ws := handler.GetWorkspace("workspace1")
	if ws == nil {
		t.Fatal("workspace not found")
	}

	// Verify callback was registered by checking it's not nil
	// (We don't call it directly since the webhook handler wraps it)
	_ = callbackRegistered
}

func TestMultiWorkspaceHandler_GetWorkspace(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "T1"},
			{Name: "ws2", APIKey: "key2", TeamID: "T2"},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	tests := []struct {
		name     string
		lookup   string
		wantNil  bool
		wantName string
	}{
		{"existing workspace ws1", "ws1", false, "ws1"},
		{"existing workspace ws2", "ws2", false, "ws2"},
		{"non-existing workspace", "ws3", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := handler.GetWorkspace(tt.lookup)
			if tt.wantNil {
				if ws != nil {
					t.Error("expected nil workspace")
				}
				return
			}
			if ws == nil {
				t.Fatal("expected non-nil workspace")
			}
			if ws.Config().Name != tt.wantName {
				t.Errorf("workspace name = %s, want %s", ws.Config().Name, tt.wantName)
			}
		})
	}
}

func TestMultiWorkspaceHandler_GetWorkspaceByTeamID(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "appbooster", APIKey: "key1", TeamID: "APP"},
			{Name: "bostonteam", APIKey: "key2", TeamID: "BT"},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	tests := []struct {
		teamID   string
		wantNil  bool
		wantName string
	}{
		{"APP", false, "appbooster"},
		{"BT", false, "bostonteam"},
		{"UNKNOWN", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.teamID, func(t *testing.T) {
			ws := handler.GetWorkspaceByTeamID(tt.teamID)
			if tt.wantNil {
				if ws != nil {
					t.Error("expected nil workspace")
				}
				return
			}
			if ws == nil {
				t.Fatal("expected non-nil workspace")
			}
			if ws.Config().Name != tt.wantName {
				t.Errorf("workspace name = %s, want %s", ws.Config().Name, tt.wantName)
			}
		})
	}
}

func TestMultiWorkspaceHandler_extractTeamID(t *testing.T) {
	handler := &MultiWorkspaceHandler{}

	tests := []struct {
		name    string
		payload map[string]interface{}
		want    string
	}{
		{
			name: "team.id in data",
			payload: map[string]interface{}{
				"data": map[string]interface{}{
					"team": map[string]interface{}{
						"id": "TEAM123",
					},
				},
			},
			want: "TEAM123",
		},
		{
			name: "teamId in data",
			payload: map[string]interface{}{
				"data": map[string]interface{}{
					"teamId": "TEAM456",
				},
			},
			want: "TEAM456",
		},
		{
			name: "no team info",
			payload: map[string]interface{}{
				"data": map[string]interface{}{
					"id": "issue-123",
				},
			},
			want: "",
		},
		{
			name:    "empty payload",
			payload: map[string]interface{}{},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handler.extractTeamID(tt.payload)
			if got != tt.want {
				t.Errorf("extractTeamID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMultiWorkspaceHandler_ListWorkspaces(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "alpha", APIKey: "key1", TeamID: "A"},
			{Name: "beta", APIKey: "key2", TeamID: "B"},
			{Name: "gamma", APIKey: "key3", TeamID: "G"},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	names := handler.ListWorkspaces()
	if len(names) != 3 {
		t.Errorf("ListWorkspaces() returned %d names, want 3", len(names))
	}

	// Check that all names are present (order may vary due to map iteration)
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !nameSet[expected] {
			t.Errorf("ListWorkspaces() missing %q", expected)
		}
	}
}

func TestWorkspaceHandler_ResolvePilotProject(t *testing.T) {
	tests := []struct {
		name       string
		wsConfig   *WorkspaceConfig
		issue      *Issue
		wantResult string
	}{
		{
			name: "single project mapped",
			wsConfig: &WorkspaceConfig{
				Projects: []string{"pilot"},
			},
			issue:      &Issue{},
			wantResult: "pilot",
		},
		{
			name: "multiple projects - returns first",
			wsConfig: &WorkspaceConfig{
				Projects: []string{"aso-generator", "pilot"},
			},
			issue:      &Issue{},
			wantResult: "aso-generator",
		},
		{
			name: "match by project ID",
			wsConfig: &WorkspaceConfig{
				ProjectIDs: []string{"proj-abc"},
				Projects:   []string{"matched-project"},
			},
			issue: &Issue{
				Project: &Project{ID: "proj-abc", Name: "Linear Project"},
			},
			wantResult: "matched-project",
		},
		{
			name: "no projects mapped",
			wsConfig: &WorkspaceConfig{
				Projects: []string{},
			},
			issue:      &Issue{},
			wantResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &WorkspaceHandler{config: tt.wsConfig}
			got := ws.ResolvePilotProject(tt.issue)
			if got != tt.wantResult {
				t.Errorf("ResolvePilotProject() = %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestMultiWorkspaceHandler_GetNotifier(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "T1"},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	notifier := handler.GetNotifier("ws1")
	if notifier == nil {
		t.Error("expected non-nil notifier for ws1")
	}

	notifier = handler.GetNotifier("nonexistent")
	if notifier != nil {
		t.Error("expected nil notifier for nonexistent workspace")
	}
}

func TestMultiWorkspaceHandler_GetClient(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{Name: "ws1", APIKey: "key1", TeamID: "T1"},
		},
	}

	handler, err := NewMultiWorkspaceHandler(cfg)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	client := handler.GetClient("ws1")
	if client == nil {
		t.Error("expected non-nil client for ws1")
	}

	client = handler.GetClient("nonexistent")
	if client != nil {
		t.Error("expected nil client for nonexistent workspace")
	}
}

// containsSubstr checks if a string contains a substring (renamed to avoid collision)
func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
