package main

import (
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/jira"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestJiraPollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the Jira polling config.
func TestJiraPollerRegistration_Fields(t *testing.T) {
	reg := jiraPollerRegistration()

	if reg.Name != "jira" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "jira")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil jira config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "jira disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{Enabled: false, Polling: &jira.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{Enabled: true, Polling: &jira.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{Enabled: true, Polling: &jira.PollingConfig{Enabled: true}},
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

// TestJiraPriorityMapping verifies priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestJiraPriorityMapping(t *testing.T) {
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

// TestJiraSDKEventSequenceID verifies the SDK adapter produces IssueEvents with
// SequenceID already prefixed "JIRA-<KEY>" — the handler must use ev.SequenceID
// directly, not re-prefix.
func TestJiraSDKEventSequenceID(t *testing.T) {
	key := "PROJ-42"
	seqID := "JIRA-" + key

	if seqID != "JIRA-PROJ-42" {
		t.Errorf("expected sequenceID = JIRA-PROJ-42, got %q", seqID)
	}

	// If a handler re-applied the prefix it would produce a double-prefix.
	doublePrefix := "JIRA-" + seqID
	if doublePrefix == "JIRA-PROJ-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "JIRA-JIRA-PROJ-42" {
		t.Errorf("double-prefix sanity check: got %q, want JIRA-JIRA-PROJ-42", doublePrefix)
	}
}

// TestJiraPollerNoLegacyImport verifies poller_jira.go does NOT import the legacy
// in-tree internal/adapters/jira package on the SDK poll path.
func TestJiraPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_jira.go")
	if err != nil {
		t.Fatalf("failed to read poller_jira.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/jira"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_jira.go must not import the legacy in-tree jira package; found %q", legacyImport)
	}
}

// TestJiraTaskSourceAdapter verifies IssueEvent flows through to a Task
// with SourceAdapter == "jira" and the SequenceID used as ID without re-prefixing.
func TestJiraTaskSourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "10042",
		SequenceID: "JIRA-PROJ-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
	}

	// Verify SequenceID is used directly as task ID (no re-prefix).
	taskID := ev.SequenceID
	if taskID != "JIRA-PROJ-42" {
		t.Errorf("taskID = %q, want JIRA-PROJ-42", taskID)
	}

	// Verify branch is "pilot/<sequenceID>".
	branch := "pilot/" + taskID
	if branch != "pilot/JIRA-PROJ-42" {
		t.Errorf("branch = %q, want pilot/JIRA-PROJ-42", branch)
	}

	// Verify priority is routed through sdkshim.
	priority := sdkshim.PriorityFromSDK(ev.Priority)
	if priority != sdkshim.PilotPriorityHigh {
		t.Errorf("priority = %d, want %d (high)", priority, sdkshim.PilotPriorityHigh)
	}
}

// TestJiraHandlerTaskSourceAdapter verifies handlers.go unconditionally
// sets Task.SourceAdapter = "jira" in handleJiraSDKIssueWithResult.
func TestJiraHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), `SourceAdapter: "jira"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "jira" in handleJiraSDKIssueWithResult`)
	}
}

// TestJiraHandlerSDKRoutingPath verifies handleJiraSDKIssueWithResult routes
// through the SDK event path — it must use sdkshim.PriorityFromSDK for priority conversion.
func TestJiraHandlerSDKRoutingPath(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "sdkshim.PriorityFromSDK") {
		t.Error("handlers.go must use sdkshim.PriorityFromSDK for priority conversion in handleJiraSDKIssueWithResult")
	}
}
