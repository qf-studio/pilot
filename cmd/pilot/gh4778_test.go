package main

import (
	"os"
	"strings"
	"testing"
)

// TestPollingModeMergeWaiterCleanerClient_UsesTokenFunc is a GH-4778
// regression test: the GitHub client that runPollingMode hands to
// NewMergeWaiter (daemon-lifetime merge-wait callback) and NewCleaner
// (periodic stale-label loop) must come from newGitHubClient(cfg) — which
// re-resolves the token on every request and invalidates its cache on a 401
// — not a static github.NewClient(token) built once and held for the rest
// of the daemon's life. A frozen token here 401s after an App-token
// rotation (same defect class as GH-4755/PR#4764, found in post-merge
// review as PR#4764's residual case).
func TestPollingModeMergeWaiterCleanerClient_UsesTokenFunc(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(content)

	if !strings.Contains(src, "client := newGitHubClient(cfg)") {
		t.Error("runPollingMode must build the merge-waiter/cleaner GitHub client via newGitHubClient(cfg), " +
			"not a static github.NewClient(token) held past construction")
	}
	if strings.Contains(src, "client := github.NewClient(token)") {
		t.Error("runPollingMode must not construct its long-lived GitHub client via github.NewClient(token) — " +
			"that freezes the token for the daemon's lifetime; use newGitHubClient(cfg) instead")
	}
}

// TestGatewayModeGithubToken_ValidatedAtStartup is a GH-4778 regression
// test: webhook/gateway mode (the `pilot start --linear`/`--jira`/no-flags
// daemon path, as opposed to runPollingMode) must validate its GitHub token
// at startup the same way runPollingMode does via validateGitHubToken —
// otherwise a dead App key fails silently at first webhook delivery instead
// of loudly at boot (GH-3769 preflight parity).
func TestGatewayModeGithubToken_ValidatedAtStartup(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(content)

	const startMarker = "// Build Pilot options for gateway mode (GH-349)"
	const endMarker = "// GH-392: Create shared infrastructure for polling adapters in gateway mode"

	start := strings.Index(src, startMarker)
	if start < 0 {
		t.Fatalf("marker %q not found in main.go — gateway mode setup moved, update this test's markers", startMarker)
	}
	rest := src[start:]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("marker %q not found in main.go — gateway mode setup moved, update this test's markers", endMarker)
	}
	gatewaySetup := rest[:end]

	if !strings.Contains(gatewaySetup, "pilot.WithGitHubClient(") {
		t.Fatal("gateway mode must build a GitHub client and inject it via pilot.WithGitHubClient — this test's markers may be stale")
	}
	if !strings.Contains(gatewaySetup, "validateGitHubToken(") {
		t.Error("gateway/webhook mode must call validateGitHubToken at startup, mirroring runPollingMode's GH-3769 preflight — " +
			"a dead App key must surface as a loud 401 log line at boot, not fail silently on the first webhook")
	}
}
