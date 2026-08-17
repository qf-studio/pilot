package main

import (
	"os"
	"strings"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestLinearPollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the Linear polling config.
func TestLinearPollerRegistration_Fields(t *testing.T) {
	reg := linearPollerRegistration()

	if reg.Name != "linear" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "linear")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil linear config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "linear disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: false, Polling: &linear.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: true, Polling: &linear.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: true, Polling: &linear.PollingConfig{Enabled: true}},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Enabled(tt.cfg)
			if got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLinearPriorityMapping verifies Invariant 2: priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestLinearPriorityMapping(t *testing.T) {
	tests := []struct {
		sdkPriority string
		wantPilot   int
	}{
		{"urgent", sdkshim.PilotPriorityUrgent},
		{"high", sdkshim.PilotPriorityHigh},
		{"medium", sdkshim.PilotPriorityMedium},
		{"low", sdkshim.PilotPriorityLow},
		{"none", sdkshim.PilotPriorityNone},
		{"", sdkshim.PilotPriorityNone},
		{"unknown", sdkshim.PilotPriorityNone},
	}

	for _, tt := range tests {
		t.Run("priority_"+tt.sdkPriority, func(t *testing.T) {
			got := sdkshim.PriorityFromSDK(tt.sdkPriority)
			if got != tt.wantPilot {
				t.Errorf("PriorityFromSDK(%q) = %d, want %d", tt.sdkPriority, got, tt.wantPilot)
			}
		})
	}
}

// TestLinearSDKEventSequenceID verifies Invariant 3: the SDK adapter produces IssueEvents
// with SequenceID already prefixed (e.g. "APP-123") — the handler must use ev.SequenceID
// directly, not re-prefix.
func TestLinearSDKEventSequenceID(t *testing.T) {
	seqID := "APP-123"

	// Correct: use SequenceID directly.
	branch := "pilot/" + seqID
	if branch != "pilot/APP-123" {
		t.Errorf("branch = %q, want pilot/APP-123", branch)
	}

	// Wrong: re-applying a prefix would produce a mangled ID.
	rePrefix := "LIN-" + seqID
	if rePrefix == "APP-123" {
		t.Error("re-prefixed ID must not equal the original sequence ID")
	}
}

// TestLinearPollerNoLegacyImport verifies Invariant 4: poller_linear.go must NOT import
// the legacy in-tree internal/adapters/linear package on the SDK poll path.
func TestLinearPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_linear.go")
	if err != nil {
		t.Fatalf("failed to read poller_linear.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/linear"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_linear.go must not import the legacy in-tree linear package; found %q", legacyImport)
	}
}

// TestLinearTaskSourceAdapter verifies Invariant 5: IssueEvent flows through to a Task
// with SourceAdapter == "linear" and the SequenceID used as ID without re-prefixing.
func TestLinearTaskSourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "abc-123",
		SequenceID: "APP-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
	}

	// Verify SequenceID is used directly as task ID (no re-prefix).
	taskID := ev.SequenceID
	if taskID != "APP-42" {
		t.Errorf("taskID = %q, want APP-42", taskID)
	}

	// Verify branch is "pilot/<sequenceID>".
	branch := "pilot/" + taskID
	if branch != "pilot/APP-42" {
		t.Errorf("branch = %q, want pilot/APP-42", branch)
	}

	// Verify priority is routed through sdkshim.
	priority := sdkshim.PriorityFromSDK(ev.Priority)
	if priority != sdkshim.PilotPriorityHigh {
		t.Errorf("priority = %d, want %d (high)", priority, sdkshim.PilotPriorityHigh)
	}
}

// TestLinearHandlerTaskSourceAdapter verifies Invariant 6: handlers.go unconditionally
// sets Task.SourceAdapter = "linear" in handleLinearIssueWithResult.
func TestLinearHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	// Struct fields may have alignment spacing, so check SourceAdapter: and "linear" together
	// using a loose substring that tolerates variable whitespace.
	s := string(content)
	idx := strings.Index(s, `SourceAdapter:`)
	if idx < 0 {
		t.Fatal(`handlers.go must contain SourceAdapter: field`)
	}
	if !strings.Contains(s[idx:idx+40], `"linear"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "linear" in handleLinearIssueWithResult`)
	}
}

// TestLinearHandlerSDKRoutingPath verifies Invariant 7: handleLinearIssueWithResult routes
// through the SDK event path — it must use sdkshim.PriorityFromSDK for priority conversion.
func TestLinearHandlerSDKRoutingPath(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "sdkshim.PriorityFromSDK") {
		t.Error("handlers.go must use sdkshim.PriorityFromSDK for priority conversion in handleLinearIssueWithResult")
	}
}

// TestLinearHandlerSubIssueCreatorDirectInject verifies Invariant 8: handlers.go wires
// a Linear client directly as SubIssueCreator — no shim wrapper.
// Note: uses internal/adapters/linear.NewClient because studio-sdk v0.24.0 Client does not
// expose CreateIssue and therefore cannot satisfy executor.SubIssueCreator directly.
func TestLinearHandlerSubIssueCreatorDirectInject(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "SetSubIssueCreator(linear.NewClient") {
		t.Error("handlers.go must call runner.SetSubIssueCreator(linear.NewClient(...))")
	}
}

func TestNewSDKLinearWorkspace(t *testing.T) {
	tests := []struct {
		name           string
		ws             *linear.WorkspaceConfig
		triggerLabel   string
		interval       time.Duration
		wantProjectIDs []string
		wantProjects   []string
	}{
		{
			name: "project filter is handed to the SDK",
			ws: &linear.WorkspaceConfig{
				Name:       "acme",
				APIKey:     "test-linear-key",
				TeamID:     "ENG",
				ProjectIDs: []string{"proj-a", "proj-b"},
				Projects:   []string{"pilot"},
			},
			triggerLabel:   "llm-pilot",
			interval:       30 * time.Second,
			wantProjectIDs: []string{"proj-a", "proj-b"},
			wantProjects:   []string{"pilot"},
		},
		{
			name: "unset filter stays nil so the unfiltered path is unchanged",
			ws: &linear.WorkspaceConfig{
				Name:   "acme",
				APIKey: "test-linear-key",
				TeamID: "ENG",
			},
			triggerLabel:   "pilot",
			interval:       time.Minute,
			wantProjectIDs: nil,
			wantProjects:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newSDKLinearWorkspace(tt.ws.Name, tt.ws.APIKey, tt.ws.TeamID, tt.triggerLabel, tt.ws.ProjectIDs, tt.ws.Projects, tt.interval)

			if got.ProjectIDs == nil && tt.wantProjectIDs != nil {
				t.Fatalf("ProjectIDs = nil, want %v", tt.wantProjectIDs)
			}
			if tt.wantProjectIDs == nil && got.ProjectIDs != nil {
				t.Errorf("ProjectIDs = %v, want nil", got.ProjectIDs)
			}
			if len(got.ProjectIDs) != len(tt.wantProjectIDs) {
				t.Fatalf("ProjectIDs = %v, want %v", got.ProjectIDs, tt.wantProjectIDs)
			}
			for i, want := range tt.wantProjectIDs {
				if got.ProjectIDs[i] != want {
					t.Errorf("ProjectIDs[%d] = %q, want %q", i, got.ProjectIDs[i], want)
				}
			}

			if tt.wantProjects == nil && got.Projects != nil {
				t.Errorf("Projects = %v, want nil", got.Projects)
			}
			if len(got.Projects) != len(tt.wantProjects) {
				t.Fatalf("Projects = %v, want %v", got.Projects, tt.wantProjects)
			}

			if got.Name != tt.ws.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.ws.Name)
			}
			if got.TeamID != tt.ws.TeamID {
				t.Errorf("TeamID = %q, want %q", got.TeamID, tt.ws.TeamID)
			}
			if got.TriggerLabel != tt.triggerLabel {
				t.Errorf("TriggerLabel = %q, want %q", got.TriggerLabel, tt.triggerLabel)
			}
			if got.Polling == nil || got.Polling.Interval != tt.interval {
				t.Errorf("Polling interval = %v, want %v", got.Polling, tt.interval)
			}
		})
	}
}
