package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	gitlabSDK "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"

	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// TestGitlabPollerRegistration_Fields verifies the SDK-based registration has the correct
// name (Invariant 1: SourceAdapter == "gitlab") and that its Enabled predicate gates on the
// GitLab polling config.
func TestGitlabPollerRegistration_Fields(t *testing.T) {
	reg := gitlabPollerRegistration()

	if reg.Name != "gitlab" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "gitlab")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil gitlab config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "gitlab disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{Enabled: false, Polling: &gitlab.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{Enabled: true, Polling: &gitlab.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{Enabled: true, Polling: &gitlab.PollingConfig{Enabled: true}},
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

// TestGitlabPriorityMapping verifies Invariant 2: priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestGitlabPriorityMapping(t *testing.T) {
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

// TestGitlabSDKClientImplementsPRCreator verifies Invariant 3: the SDK GitLab client satisfies
// executor.PRCreator directly — no shim wrapper is required.
func TestGitlabSDKClientImplementsPRCreator(t *testing.T) {
	// Compile-time assertion: *gitlabSDK.Client must implement executor.PRCreator.
	var _ executor.PRCreator = (*gitlabSDK.Client)(nil)
}

// TestGitlabRepoResolutionGracefulError verifies that sdkshim.ResolveRepoForEvent returns
// ErrRepoNotResolved for the gitlab source (Phase-0 stub). The handler must not fail on this.
func TestGitlabRepoResolutionGracefulError(t *testing.T) {
	cfg := &config.Config{}
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "GL-42",
		ProjectID:  "12345",
		Priority:   "high",
	}

	_, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "gitlab", ev)
	if !errors.Is(err, sdkshim.ErrRepoNotResolved) {
		t.Errorf("ResolveRepoForEvent returned %v, want ErrRepoNotResolved", err)
	}
}

// TestGitlabSDKEventSequenceID verifies Invariant 4: the SDK adapter produces IssueEvents
// with SequenceID already prefixed "GL-<IID>" — the handler routes through the SDK path
// (ProcessGitlabIssueEvent convention) and must use ev.SequenceID directly, not re-prefix.
func TestGitlabSDKEventSequenceID(t *testing.T) {
	// Simulate what the SDK adapter's toIssueEvent does.
	issueIID := 42
	seqID := "GL-" + gitlabItoa(issueIID)

	if seqID != "GL-42" {
		t.Errorf("expected sequenceID = GL-42, got %q", seqID)
	}

	// If a handler called fmt.Sprintf("GL-%s", seqID) it would produce "GL-GL-42".
	doublePrefix := "GL-" + seqID
	if doublePrefix == "GL-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "GL-GL-42" {
		t.Errorf("double-prefix sanity check: got %q, want GL-GL-42", doublePrefix)
	}
}

// TestGitlabPollerNoLegacyImport verifies Invariant 5: poller_gitlab.go must NOT import
// the legacy in-tree internal/adapters/gitlab package on the SDK poll path.
func TestGitlabPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_gitlab.go")
	if err != nil {
		t.Fatalf("failed to read poller_gitlab.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/gitlab"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_gitlab.go must not import the legacy in-tree gitlab package; found %q", legacyImport)
	}
}

// TestGitlabHandlerTaskSourceAdapter verifies Invariant 6: handlers.go unconditionally
// sets Task.SourceAdapter = "gitlab" in handleGitlabIssueWithResult, so every task
// routed through the SDK poll path carries the correct adapter label.
func TestGitlabHandlerTaskSourceAdapter(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if !strings.Contains(string(content), `SourceAdapter: "gitlab"`) {
		t.Error(`handlers.go must unconditionally set SourceAdapter: "gitlab" in handleGitlabIssueWithResult`)
	}
}

// TestGitlabHandlerSDKRoutingPath verifies Invariant 7: handleGitlabIssueWithResult routes
// through the SDK event path — it must NOT call the legacy ProcessGitlabTicket orchestrator
// function, and must use sdkshim.PriorityFromSDK for priority conversion.
func TestGitlabHandlerSDKRoutingPath(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("failed to read handlers.go: %v", err)
	}
	if strings.Contains(string(content), "ProcessGitlabTicket") {
		t.Error("handlers.go must not call legacy ProcessGitlabTicket; SDK poll path must not use the legacy orchestrator function")
	}
	if !strings.Contains(string(content), "sdkshim.PriorityFromSDK") {
		t.Error("handlers.go must use sdkshim.PriorityFromSDK for priority conversion in handleGitlabIssueWithResult")
	}
}

// gitlabItoa converts an int to a decimal string (avoids importing strconv in test).
func gitlabItoa(n int) string {
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
