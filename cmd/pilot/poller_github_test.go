package main

import (
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// TestGithubPollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the use_sdk_poller flag (OFF by default —
// the in-tree poller stays the live path unless a config opts in; M7 4b).
func TestGithubPollerRegistration_Fields(t *testing.T) {
	reg := githubPollerRegistration()

	if reg.Name != "github" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "github")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil github config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "github disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: false, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "use_sdk_poller off (default) — in-tree poller stays the live path",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: false, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r"},
			}},
			want: false,
		},
		{
			name: "no default repo — SDK path covers the default repo only (4b)",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "all enabled incl. use_sdk_poller",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
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

// TestGithubPollerRegistered verifies the M7-4b invariant: the GitHub SDK registration IS
// wired into adapterPollerRegistrations() (flag-gated via its Enabled predicate), so daemons
// with use_sdk_poller=true start the SDK poller for the default repo.
func TestGithubPollerRegistered(t *testing.T) {
	for _, reg := range adapterPollerRegistrations() {
		if reg.Name == "github" {
			return
		}
	}
	t.Error("github must be in adapterPollerRegistrations() as of M7 4b (flag-gated by use_sdk_poller)")
}

// TestGithubSDKClientDoesNotImplementPRCreator documents the github behavior delta: unlike the
// GitLab SDK client (which implements executor.PRCreator and is injected via SetPRCreator), the
// studio-sdk GitHub *Client does NOT satisfy executor.PRCreator. The Phase-4a handler relies on
// this — GitHub PRs keep going through the gh CLI, and no PRCreator is injected.
func TestGithubSDKClientDoesNotImplementPRCreator(t *testing.T) {
	var i interface{} = (*githubSDK.Client)(nil)
	if _, ok := i.(executor.PRCreator); ok {
		t.Error("studio-sdk github *Client unexpectedly implements executor.PRCreator; " +
			"the Phase-4a handler assumes it does not (GitHub keeps the gh-CLI PR path)")
	}
}

// TestGithubRepoResolution verifies the Phase-4 github branch of sdkshim.ResolveRepoForEvent:
// it resolves a configured default repo (unlike the still-stubbed sources, which return
// ErrRepoNotResolved), and the SequenceID-derived repo name routes per-project.
func TestGithubRepoResolution(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "qf-studio/pilot"},
		},
	}
	clone, owner, repo, err := sdkshim.ResolveRepoForEvent(cfg, "github", sdkcore.IssueEvent{SequenceID: "GH-42", ProjectID: "pilot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "qf-studio" || repo != "pilot" || clone != "https://github.com/qf-studio/pilot.git" {
		t.Errorf("got (%q,%q,%q), want (https://github.com/qf-studio/pilot.git, qf-studio, pilot)", clone, owner, repo)
	}
}

// TestGithubPollerNoLegacyImport verifies poller_github.go does not directly import the legacy
// in-tree internal/adapters/github package on the SDK poll path (it uses the studio-sdk github
// package). The config dependency carries github.Config transitively, which is fine.
func TestGithubPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_github.go")
	if err != nil {
		t.Fatalf("failed to read poller_github.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/github"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_github.go must not import the legacy in-tree github package; found %q", legacyImport)
	}
}

// TestGithubHandlerSDKFunctionInvariants is a source-level regression guard SCOPED to the
// handleGithubIssueEventSDK function body (not a whole-file grep — the legacy in-tree handler
// shares several of these lines and legitimately uses fmt.Sprintf("GH-%d", issue.Number)).
// The SDK handler must derive taskID from ev.SequenceID verbatim, set SourceAdapter "github",
// and never re-prefix via GH-%d.
func TestGithubHandlerSDKFunctionInvariants(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
	if !strings.Contains(body, "taskID := ev.SequenceID") {
		t.Error("handleGithubIssueEventSDK must derive taskID from ev.SequenceID verbatim")
	}
	if !strings.Contains(body, `SourceAdapter: "github"`) {
		t.Error(`handleGithubIssueEventSDK must set SourceAdapter: "github"`)
	}
	if strings.Contains(body, `"GH-`+`%d"`) {
		t.Error("handleGithubIssueEventSDK must not re-prefix the raw issue number into a GH- sequence (would yield GH-GH form)")
	}
}

// githubFuncBody returns the source of file between funcSignature and the next top-level "func "
// declaration, so assertions can be scoped to one function rather than the whole file.
func githubFuncBody(t *testing.T, file, funcSignature string) string {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(content)
	start := strings.Index(src, funcSignature)
	if start < 0 {
		t.Fatalf("function %q not found in %s", funcSignature, file)
	}
	rest := src[start+len(funcSignature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}
