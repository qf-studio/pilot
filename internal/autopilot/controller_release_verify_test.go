package autopilot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// releaseTagsServer serves GET .../releases/tags/<tag> according to a sequence
// of canned responses ("missing" -> 404, "present" -> 200 with a release body),
// consumed one per call so tests can simulate "not yet published, then published".
// Requests past the end of the sequence repeat the last entry.
func releaseTagsServer(t *testing.T, sequence []string) *httptest.Server {
	t.Helper()
	var calls int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/releases/tags/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		idx := calls
		if idx >= len(sequence) {
			idx = len(sequence) - 1
		}
		calls++
		if sequence[idx] == "present" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.2.3", HTMLURL: "https://example.com/releases/v1.2.3"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
}

// TestAfterTagCreated_WorkflowMode_ReleaseAppears verifies that when the release
// publishes before VerifyTimeout elapses, verifyReleaseAfterTag returns without
// firing an alert (and enrichment, when configured, would run against the now-
// published release).
func TestAfterTagCreated_WorkflowMode_ReleaseAppears(t *testing.T) {
	server := releaseTagsServer(t, []string{"missing", "present"})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v", Publish: ReleasePublishWorkflow}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	rel := &ReleaseConfig{Publish: ReleasePublishWorkflow, VerifyRelease: boolPtr(true), VerifyTimeout: time.Second}
	c.verifyReleaseAfterTag(context.Background(), "owner", "repo", "v1.2.3", 42, 7, nil, rel, 5*time.Millisecond, 500*time.Millisecond, "")

	if len(sink.events) != 0 {
		t.Errorf("expected no alert when the release appears in time, got %d events", len(sink.events))
	}
	c.mu.Lock()
	alerted := c.alertedMissingReleases["owner/repo@v1.2.3"]
	c.mu.Unlock()
	if alerted {
		t.Error("tag must not be marked as alerted when the release appears in time")
	}
}

// TestAfterTagCreated_WorkflowMode_Timeout_FiresAlert verifies that when the
// release never appears within VerifyTimeout, exactly one release_missing event
// reaches the alerts engine — and that calling verifyReleaseAfterTag again for
// the same tag does NOT fire a second event (controller-side dedup, since the
// alerts engine's cooldown is keyed by rule name, not by source).
func TestAfterTagCreated_WorkflowMode_Timeout_FiresAlert(t *testing.T) {
	server := releaseTagsServer(t, []string{"missing"})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v", Publish: ReleasePublishWorkflow}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	rel := &ReleaseConfig{Publish: ReleasePublishWorkflow, VerifyRelease: boolPtr(true), VerifyTimeout: 20 * time.Millisecond}
	c.verifyReleaseAfterTag(context.Background(), "owner", "repo", "v9.9.9", 42, 7, nil, rel, 5*time.Millisecond, 20*time.Millisecond, "")

	if len(sink.events) != 1 {
		t.Fatalf("expected exactly 1 release_missing event, got %d", len(sink.events))
	}
	if sink.events[0].Metadata["tag"] != "v9.9.9" {
		t.Errorf("event metadata tag = %q, want v9.9.9", sink.events[0].Metadata["tag"])
	}
	if sink.events[0].Metadata["repo"] != "owner/repo" {
		t.Errorf("event metadata repo = %q, want owner/repo", sink.events[0].Metadata["repo"])
	}

	// Same tag again — dedup must suppress a second event.
	c.verifyReleaseAfterTag(context.Background(), "owner", "repo", "v9.9.9", 42, 7, nil, rel, 5*time.Millisecond, 20*time.Millisecond, "")
	if len(sink.events) != 1 {
		t.Errorf("dedup: expected still exactly 1 event after re-verifying the same tag, got %d", len(sink.events))
	}
}

// TestAfterTagCreated_WorkflowMode_VerifyDisabled_NoAlert verifies that an
// explicit VerifyRelease=false suppresses the alert even on timeout.
func TestAfterTagCreated_WorkflowMode_VerifyDisabled_NoAlert(t *testing.T) {
	server := releaseTagsServer(t, []string{"missing"})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	rel := &ReleaseConfig{Publish: ReleasePublishWorkflow, VerifyRelease: boolPtr(false), VerifyTimeout: 20 * time.Millisecond}
	c.verifyReleaseAfterTag(context.Background(), "owner", "repo", "v5.5.5", 1, 0, nil, rel, 5*time.Millisecond, 20*time.Millisecond, "")

	if len(sink.events) != 0 {
		t.Errorf("expected no alert when VerifyRelease is explicitly false, got %d events", len(sink.events))
	}
}

// TestAfterTagCreated_TagOnly_NoPolling verifies that "tag_only" mode never
// launches verification: zero requests to GET .../releases/tags/....
func TestAfterTagCreated_TagOnly_NoPolling(t *testing.T) {
	var releaseTagRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/") {
			releaseTagRequests++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	rel := &ReleaseConfig{Publish: ReleasePublishTagOnly, VerifyRelease: boolPtr(true), VerifyTimeout: time.Second}
	c.afterTagCreated("owner", "repo", "v1.0.0", 1, 0, nil, rel, "")

	// afterTagCreated returns synchronously without launching a goroutine for
	// tag_only, so no sleep/wait is needed before asserting.
	if releaseTagRequests != 0 {
		t.Errorf("tag_only mode must not poll for the release, got %d GET .../releases/tags/... requests", releaseTagRequests)
	}
}

// TestScanRecentlyMergedPRs_BackstopAlert verifies the scanner backstop
// (backstopCheckReleaseMissing): a merge older than VerifyTimeout with a covering
// tag but no GitHub Release fires exactly one alert; a merge with a release
// present, tag_only mode, or a merge still within the timeout window fire none.
func TestScanRecentlyMergedPRs_BackstopAlert(t *testing.T) {
	tests := []struct {
		name        string
		publish     string
		mergedAgo   time.Duration
		releaseName string // "" = GetReleaseByTag returns nil (no release)
		wantAlert   bool
	}{
		{name: "workflow mode, past timeout, no release fires", publish: ReleasePublishWorkflow, mergedAgo: time.Hour, wantAlert: true},
		{name: "api mode, past timeout, no release fires", publish: ReleasePublishAPI, mergedAgo: time.Hour, wantAlert: true},
		{name: "release present suppresses alert", publish: ReleasePublishWorkflow, mergedAgo: time.Hour, releaseName: "v1.0.0", wantAlert: false},
		{name: "tag_only never alerts", publish: ReleasePublishTagOnly, mergedAgo: time.Hour, wantAlert: false},
		{name: "within verify window does not alert yet", publish: ReleasePublishWorkflow, mergedAgo: time.Second, wantAlert: false},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag := "vbackstop"
			c := &Controller{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			// Minimal fake ghClient via httptest is overkill here — exercise the
			// unit directly since ScanRecentlyMergedPRs's full HTTP plumbing is
			// already covered elsewhere; backstopCheckReleaseMissing is the new
			// logic under test.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/") {
					if tt.releaseName == "" {
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"message":"Not Found"}`))
						return
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(github.Release{TagName: tt.releaseName})
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			c.ghClient = ghClient
			c.owner = "owner"
			c.repo = "repo"
			c.alertedMissingReleases = make(map[string]bool)
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			rel := &ReleaseConfig{
				Publish:       tt.publish,
				VerifyRelease: boolPtr(true),
				VerifyTimeout: 10 * time.Minute,
			}
			mergedAt := time.Now().Add(-tt.mergedAgo)
			c.backstopCheckReleaseMissing(context.Background(), rel, 100+i, 0, tag+string(rune('a'+i)), mergedAt)

			gotAlert := len(sink.events) == 1
			if gotAlert != tt.wantAlert {
				t.Errorf("alert fired = %v (events=%d), want %v", gotAlert, len(sink.events), tt.wantAlert)
			}
		})
	}
}
