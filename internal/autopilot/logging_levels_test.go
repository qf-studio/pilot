package autopilot

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// newCapturingLogger returns a logger that writes text-formatted records into
// the returned buffer, so tests can assert on both level and message content.
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

// GH-3735: a nil alerts engine silently disabled the metrics alerter at Debug
// level, so operators never saw it in default-level logs. Assert it now logs
// at Warn.
func TestMetricsAlerter_Run_NilEngine_LogsWarn(t *testing.T) {
	logger, buf := newCapturingLogger()
	ma := &MetricsAlerter{
		engine:      nil,
		log:         logger,
		tripTracker: newTripTracker(),
	}

	ma.Run(context.Background())

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level log, got: %s", out)
	}
	if !strings.Contains(out, "alerts engine not configured") {
		t.Errorf("expected nil-engine message, got: %s", out)
	}
}

// GH-3735: getBotLogin failures were logged at Debug, hiding that the GH-3417
// recovery-PR human-guard silently disables itself. Assert Warn level and that
// the message names the guard it disables.
func TestGetBotLogin_Failure_LogsWarn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	logger, buf := newCapturingLogger()
	c := &Controller{
		ghClient: ghClient,
		owner:    "owner",
		repo:     "repo",
		log:      logger,
	}

	login := c.getBotLogin(context.Background())

	if login != "" {
		t.Errorf("expected empty login on failure, got %q", login)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level log, got: %s", out)
	}
	if !strings.Contains(out, "GH-3417") {
		t.Errorf("expected message to name the GH-3417 recovery-PR guard, got: %s", out)
	}
}

// GH-3735: handleReleasing silently skipped at Debug when no releaser is
// configured. Assert Warn level so a frozen/disabled release policy is
// visible in default-level logs.
func TestHandleReleasing_NilReleaser_LogsWarn(t *testing.T) {
	logger, buf := newCapturingLogger()
	c := &Controller{
		releaser:       nil,
		log:            logger,
		activePRs:      make(map[int]*PRState),
		prFailures:     make(map[int]*prFailureState),
		recordedMerges: make(map[int]bool),
	}

	prState := &PRState{PRNumber: 42}
	if err := c.handleReleasing(context.Background(), prState); err != nil {
		t.Fatalf("handleReleasing returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level log, got: %s", out)
	}
	if !strings.Contains(out, "releaser not configured") {
		t.Errorf("expected nil-releaser message, got: %s", out)
	}
}

// GH-3735: NewController should log the resolved release policy (enabled/disabled
// plus which config layer it came from) so a frozen policy shows up in startup logs.
func TestNewController_LogsResolvedReleasePolicy(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		wantEnabled   string
		wantSource    string
	}{
		{
			name:          "neither set",
			globalRelease: nil,
			envRelease:    nil,
			wantEnabled:   "enabled=false",
			wantSource:    "source=none",
		},
		{
			name:          "global only enabled",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envRelease:    nil,
			wantEnabled:   "enabled=true",
			wantSource:    "source=global",
		},
		{
			name:          "env only enabled",
			globalRelease: nil,
			envRelease:    &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			wantEnabled:   "enabled=true",
			wantSource:    "source=env:test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease
			if tt.envRelease != nil {
				cfg.activeEnvName = "test"
				cfg.activeEnvConfig = &EnvironmentConfig{Release: tt.envRelease}
			}

			var buf bytes.Buffer
			h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
			prev := slog.Default()
			slog.SetDefault(slog.New(h))
			defer slog.SetDefault(prev)

			NewController(cfg, ghClient, nil, "owner", "repo")

			out := buf.String()
			if !strings.Contains(out, "resolved release policy") {
				t.Errorf("expected resolved release policy log, got: %s", out)
			}
			if !strings.Contains(out, tt.wantEnabled) {
				t.Errorf("expected %q in log, got: %s", tt.wantEnabled, out)
			}
			if !strings.Contains(out, tt.wantSource) {
				t.Errorf("expected %q in log, got: %s", tt.wantSource, out)
			}
		})
	}
}
