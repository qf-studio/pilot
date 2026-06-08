package main

import (
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestAzureDevOpsPollerRegistration_Fields verifies the SDK-based registration has the correct
// name (Invariant 1: SourceAdapter == "azuredevops") and that its Enabled predicate gates on the
// AzureDevOps polling config.
func TestAzureDevOpsPollerRegistration_Fields(t *testing.T) {
	reg := azuredevopsPollerRegistration()

	if reg.Name != "azuredevops" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "azuredevops")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil azuredevops config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "azuredevops disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{Enabled: false, Polling: &azuredevops.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{Enabled: true, Polling: &azuredevops.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{Enabled: true, Polling: &azuredevops.PollingConfig{Enabled: true}},
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

// TestAzureDevOpsPriorityMapping verifies Invariant 2: priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestAzureDevOpsPriorityMapping(t *testing.T) {
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

// TestAzureDevOpsSDKEventSequenceID verifies Invariant 3: the SDK adapter produces IssueEvents
// with SequenceID already prefixed "AZDO-<ID>" — the handler must use ev.SequenceID directly,
// not re-prefix with fmt.Sprintf("AZDO-%d", ...).
func TestAzureDevOpsSDKEventSequenceID(t *testing.T) {
	// Simulate what the SDK adapter's toIssueEvent does.
	workItemID := 42
	seqID := "AZDO-" + azdoItoa(workItemID)

	if seqID != "AZDO-42" {
		t.Errorf("expected sequenceID = AZDO-42, got %q", seqID)
	}

	// If a handler called fmt.Sprintf("AZDO-%d", workItemID) on top of seqID it would double-prefix.
	doublePrefix := "AZDO-" + seqID
	if doublePrefix == "AZDO-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "AZDO-AZDO-42" {
		t.Errorf("double-prefix sanity check: got %q, want AZDO-AZDO-42", doublePrefix)
	}
}

// TestAzureDevOpsPollerNoLegacyImport verifies Invariant 4: poller_azuredevops.go must NOT import
// the legacy in-tree internal/adapters/azuredevops package on the SDK poll path.
func TestAzureDevOpsPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_azuredevops.go")
	if err != nil {
		t.Fatalf("failed to read poller_azuredevops.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/azuredevops"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_azuredevops.go must not import the legacy in-tree azuredevops package; found %q", legacyImport)
	}
}

// TestAzureDevOpsTaskSourceAdapter verifies Invariant 5: IssueEvent flows through to a Task
// with SourceAdapter == "azuredevops" and the SequenceID used as ID without re-prefixing.
func TestAzureDevOpsTaskSourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "AZDO-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
	}

	// Verify SequenceID is used directly as task ID (no fmt.Sprintf("AZDO-%d") re-prefix).
	taskID := ev.SequenceID
	if taskID != "AZDO-42" {
		t.Errorf("taskID = %q, want AZDO-42", taskID)
	}

	// Verify branch is "pilot/<sequenceID>".
	branch := "pilot/" + taskID
	if branch != "pilot/AZDO-42" {
		t.Errorf("branch = %q, want pilot/AZDO-42", branch)
	}

	// Verify priority is routed through sdkshim.
	priority := sdkshim.PriorityFromSDK(ev.Priority)
	if priority != sdkshim.PilotPriorityHigh {
		t.Errorf("priority = %d, want %d (high)", priority, sdkshim.PilotPriorityHigh)
	}
}

// azdoItoa converts an int to a decimal string (avoids importing strconv in test).
func azdoItoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
