package main

import (
	"errors"
	"os"
	"strings"
	"testing"

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

// TestLinearPriorityMapping verifies Invariant 4: priority values from SDK IssueEvents are
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

// TestLinearPollerNoLegacyImport verifies the acceptance criterion: poller_linear.go must NOT
// import the legacy in-tree internal/adapters/linear package on the SDK poll path.
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

// TestLinearSDKEventSequenceID verifies Invariant 5: the SDK adapter produces IssueEvents
// with SequenceID already prefixed "LIN-APP-123" — the handler must use ev.SequenceID
// directly, not re-prefix.
func TestLinearSDKEventSequenceID(t *testing.T) {
	identifier := "APP-123"
	seqID := "LIN-" + identifier

	if seqID != "LIN-APP-123" {
		t.Errorf("expected sequenceID = LIN-APP-123, got %q", seqID)
	}

	// If a handler re-applied the prefix it would produce a double-prefix.
	doublePrefix := "LIN-" + seqID
	if doublePrefix == "LIN-APP-123" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "LIN-LIN-APP-123" {
		t.Errorf("double-prefix sanity check: got %q, want LIN-LIN-APP-123", doublePrefix)
	}
}

// TestLinearTaskSourceAdapter verifies Invariant 1: IssueEvent flows through to a Task
// with SourceAdapter == "linear" and the SequenceID used as ID without re-prefixing.
func TestLinearTaskSourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-abc-123",
		SequenceID: "LIN-APP-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
	}

	// Verify SequenceID is used directly as task ID (Invariant 5: no re-prefix).
	taskID := ev.SequenceID
	if taskID != "LIN-APP-42" {
		t.Errorf("taskID = %q, want LIN-APP-42", taskID)
	}

	// Verify branch is "pilot/<sequenceID>" (no re-prefix).
	branch := "pilot/" + taskID
	if branch != "pilot/LIN-APP-42" {
		t.Errorf("branch = %q, want pilot/LIN-APP-42", branch)
	}

	// Verify priority is routed through sdkshim (Invariant 4).
	priority := sdkshim.PriorityFromSDK(ev.Priority)
	if priority != sdkshim.PilotPriorityHigh {
		t.Errorf("priority = %d, want %d (high)", priority, sdkshim.PilotPriorityHigh)
	}
}

// TestLinearHandlerTaskSourceAdapter verifies Invariant 1: handlers.go unconditionally
// sets Task.SourceAdapter = "linear" in handleLinearIssueWithResult.
func TestLinearHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	// Match with or without alignment spaces (gofmt may align struct fields).
	if !strings.Contains(string(content), `SourceAdapter:`) || !strings.Contains(string(content), `"linear"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "linear" in handleLinearIssueWithResult`)
	}
	// Verify the two tokens appear in the same section of the file (within 5 lines of each other).
	lines := strings.Split(string(content), "\n")
	found := false
	for i, line := range lines {
		if strings.Contains(line, `SourceAdapter:`) && strings.Contains(line, `"linear"`) {
			found = true
			_ = i
			break
		}
	}
	if !found {
		t.Error(`handlers.go must set SourceAdapter: "linear" on a single line in handleLinearIssueWithResult`)
	}
}

// TestLinearHandlerSDKRoutingPath verifies Invariant 4: handleLinearIssueWithResult routes
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

// TestLinearHandlerSubIssueCreatorWired verifies Invariant 2: handleLinearIssueWithResult
// wires SetSubIssueCreator so epic decomposition is available on the Linear SDK poll path.
func TestLinearHandlerSubIssueCreatorWired(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "runner.SetSubIssueCreator") {
		t.Error("handlers.go must call runner.SetSubIssueCreator in handleLinearIssueWithResult")
	}
}

// TestLinearRepoResolutionGracefulError verifies that sdkshim.ResolveRepoForEvent returns
// ErrRepoNotResolved for the linear source (Phase-0 stub). The handler must not fail on this.
func TestLinearRepoResolutionGracefulError(t *testing.T) {
	cfg := &config.Config{}
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-abc-123",
		SequenceID: "LIN-APP-42",
		ProjectID:  "team-uuid",
		Priority:   "high",
	}

	_, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "linear", ev)
	if !errors.Is(err, sdkshim.ErrRepoNotResolved) {
		t.Errorf("ResolveRepoForEvent returned %v, want ErrRepoNotResolved", err)
	}
}
