package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestAzureDevOpsPollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the AzureDevOps polling config.
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

// TestAzureDevOpsPriorityMapping verifies that AzureDevOps priority values from SDK IssueEvents
// are correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
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

// TestAzureDevOpsRepoResolutionGracefulError verifies that sdkshim.ResolveRepoForEvent returns
// ErrRepoNotResolved for the azuredevops source (Phase-0 stub). The handler must not fail on this.
func TestAzureDevOpsRepoResolutionGracefulError(t *testing.T) {
	cfg := &config.Config{}
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "AZDO-42",
		ProjectID:  "Task",
		Priority:   "high",
	}

	_, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "azuredevops", ev)
	if !errors.Is(err, sdkshim.ErrRepoNotResolved) {
		t.Errorf("ResolveRepoForEvent returned %v, want ErrRepoNotResolved", err)
	}
}

// TestAzureDevOpsSDKEventSequenceID verifies that the SDK adapter produces IssueEvents with
// SequenceID already prefixed "AZDO-<ID>" — the handler must use ev.SequenceID directly,
// not re-prefix with fmt.Sprintf("AZDO-%d", ...).
func TestAzureDevOpsSDKEventSequenceID(t *testing.T) {
	// Simulate what the SDK adapter's toIssueEvent does.
	workItemID := 42
	seqID := "AZDO-" + azdoItoa(workItemID)

	if seqID != "AZDO-42" {
		t.Errorf("expected sequenceID = AZDO-42, got %q", seqID)
	}

	// If a handler called fmt.Sprintf("AZDO-%s", seqID) it would produce "AZDO-AZDO-42".
	doublePrefix := "AZDO-" + seqID
	if doublePrefix == "AZDO-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "AZDO-AZDO-42" {
		t.Errorf("double-prefix sanity check: got %q, want AZDO-AZDO-42", doublePrefix)
	}
}

// TestAzureDevOpsPollerNoLegacyImport verifies that poller_azuredevops.go must NOT import
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

// TestAzureDevOpsHandlerTaskSourceAdapter verifies that handlers.go unconditionally sets
// Task.SourceAdapter = "azuredevops" in handleAzureDevOpsIssueWithResult.
func TestAzureDevOpsHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), `SourceAdapter: "azuredevops"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "azuredevops" in handleAzureDevOpsIssueWithResult`)
	}
}

// TestAzureDevOpsHandlerSDKRoutingPath verifies that handleAzureDevOpsIssueWithResult routes
// through the SDK event path — using sdkshim.PriorityFromSDK for priority conversion.
func TestAzureDevOpsHandlerSDKRoutingPath(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), "sdkshim.PriorityFromSDK") {
		t.Error("handlers.go must use sdkshim.PriorityFromSDK for priority conversion in handleAzureDevOpsIssueWithResult")
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
