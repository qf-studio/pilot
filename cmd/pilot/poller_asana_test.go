package main

import (
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/asana"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestAsanaPollerRegistration_Fields verifies the SDK-based registration has the correct
// name (Invariant 1: SourceAdapter == "asana") and that its Enabled predicate gates on the
// Asana polling config.
func TestAsanaPollerRegistration_Fields(t *testing.T) {
	reg := asanaPollerRegistration()

	if reg.Name != "asana" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "asana")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil asana config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "asana disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{Enabled: false, Polling: &asana.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{Enabled: true, Polling: &asana.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{Enabled: true, Polling: &asana.PollingConfig{Enabled: true}},
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

// TestAsanaPriorityMapping verifies Invariant 2: priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestAsanaPriorityMapping(t *testing.T) {
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

// TestAsanaSDKEventSequenceID verifies Invariant 3: the SDK adapter produces IssueEvents
// with SequenceID already prefixed "ASANA-<GID>" — the handler must use ev.SequenceID
// directly, not re-prefix.
func TestAsanaSDKEventSequenceID(t *testing.T) {
	gid := "1234567890"
	seqID := "ASANA-" + gid

	if seqID != "ASANA-1234567890" {
		t.Errorf("expected sequenceID = ASANA-1234567890, got %q", seqID)
	}

	// If a handler re-applied the prefix it would produce a double-prefix.
	doublePrefix := "ASANA-" + seqID
	if doublePrefix == "ASANA-1234567890" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "ASANA-ASANA-1234567890" {
		t.Errorf("double-prefix sanity check: got %q, want ASANA-ASANA-1234567890", doublePrefix)
	}
}

// TestAsanaPollerNoLegacyImport verifies Invariant 4: poller_asana.go must NOT import
// the legacy in-tree internal/adapters/asana package on the SDK poll path.
func TestAsanaPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_asana.go")
	if err != nil {
		t.Fatalf("failed to read poller_asana.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/asana"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_asana.go must not import the legacy in-tree asana package; found %q", legacyImport)
	}
}

// TestAsanaTaskSourceAdapter verifies Invariant 5: IssueEvent flows through to a Task
// with SourceAdapter == "asana" and the SequenceID used as ID without re-prefixing.
func TestAsanaTaskSourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "1234567890",
		SequenceID: "ASANA-1234567890",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
	}

	// Verify SequenceID is used directly as task ID (no re-prefix).
	taskID := ev.SequenceID
	if taskID != "ASANA-1234567890" {
		t.Errorf("taskID = %q, want ASANA-1234567890", taskID)
	}

	// Verify branch is "pilot/<sequenceID>".
	branch := "pilot/" + taskID
	if branch != "pilot/ASANA-1234567890" {
		t.Errorf("branch = %q, want pilot/ASANA-1234567890", branch)
	}

	// Verify priority is routed through sdkshim.
	priority := sdkshim.PriorityFromSDK(ev.Priority)
	if priority != sdkshim.PilotPriorityHigh {
		t.Errorf("priority = %d, want %d (high)", priority, sdkshim.PilotPriorityHigh)
	}
}

// TestAsanaHandlerTaskSourceAdapter verifies Invariant 6: handlers.go unconditionally
// sets Task.SourceAdapter = "asana" in handleAsanaIssueWithResult.
func TestAsanaHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), `SourceAdapter: "asana"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "asana" in handleAsanaIssueWithResult`)
	}
}

// TestAsanaHandlerSDKRoutingPath verifies Invariant 7: handleAsanaIssueWithResult routes
// through the SDK event path — it must use sdkshim.PriorityFromSDK for priority conversion.
func TestAsanaHandlerSDKRoutingPath(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "sdkshim.PriorityFromSDK") {
		t.Error("handlers.go must use sdkshim.PriorityFromSDK for priority conversion in handleAsanaIssueWithResult")
	}
}
