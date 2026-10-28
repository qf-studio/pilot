package quality

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stubScope(pkgs []string, full bool, reason string, calls *atomic.Int32) func(context.Context, string, string) ([]string, bool, string) {
	return func(context.Context, string, string) ([]string, bool, string) {
		if calls != nil {
			calls.Add(1)
		}
		return pkgs, full, reason
	}
}

func TestRunner_ExecuteCommandExportsChangedPackagesEnv(t *testing.T) {
	t.Setenv("PILOT_TEST_DAEMON_VAR", "from-daemon")
	// A stale scope in the daemon environment must be overridden, not inherited.
	t.Setenv(EnvChangedFull, "1")
	t.Setenv(EnvChangedPackages, "stale/pkg")

	tests := []struct {
		name   string
		pkgs   []string
		full   bool
		reason string
		want   string
	}{
		{"scoped", []string{"example.com/m/dep", "example.com/m/leaf"}, false, "affected 2 of 10 packages",
			"pkgs=example.com/m/dep example.com/m/leaf full=0 reason=affected 2 of 10 packages daemon=from-daemon"},
		{"full leaves the list empty", []string{"ignored/pkg"}, true, "build or config change: go.mod",
			"pkgs= full=1 reason=build or config change: go.mod daemon=from-daemon"},
		{"docs-only", nil, false, ScopeReasonDocsOnly,
			"pkgs= full=0 reason=docs-only daemon=from-daemon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			r := NewRunner(&Config{Enabled: true, Gates: []*Gate{{
				Name:    "env",
				Type:    GateCustom,
				Command: `printf 'pkgs=%s full=%s reason=%s daemon=%s' "$PILOT_CHANGED_PACKAGES" "$PILOT_CHANGED_FULL" "$PILOT_CHANGED_REASON" "$PILOT_TEST_DAEMON_VAR"`,
				Timeout: 5 * time.Second,
			}}}, t.TempDir())
			r.log = slog.New(slog.NewTextHandler(&logs, nil))
			r.scopeFn = stubScope(tt.pkgs, tt.full, tt.reason, nil)

			res, err := r.RunGate(context.Background(), "env")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != StatusPassed {
				t.Fatalf("status = %s (%s)", res.Status, res.Error)
			}
			if got := strings.TrimSpace(res.Output); got != tt.want {
				t.Errorf("gate saw %q, want %q", got, tt.want)
			}
			if n := strings.Count(logs.String(), "Quality gate changed-package scope"); n != 1 {
				t.Errorf("expected one scope Info line, got %d:\n%s", n, logs.String())
			}
			for _, want := range []string{"level=INFO", "gate=env", "package_count=", "full=", "reason="} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("scope log line lacks %q:\n%s", want, logs.String())
				}
			}
		})
	}
}

func TestRunner_ChangedPackagesComputedOncePerRunAcrossRetries(t *testing.T) {
	var calls atomic.Int32
	cfg := &Config{Enabled: true, Gates: []*Gate{
		{Name: "flaky", Type: GateCustom, Command: "exit 1", Required: true, Timeout: 5 * time.Second, MaxRetries: 2},
		{Name: "other", Type: GateCustom, Command: "exit 1", Required: false, Timeout: 5 * time.Second, MaxRetries: 1},
	}}
	r := NewRunner(cfg, t.TempDir())
	r.scopeFn = stubScope([]string{"example.com/m/leaf"}, false, "affected 1 of 10 packages", &calls)

	res, err := r.RunAll(context.Background(), "GH-5926")
	if err != nil {
		t.Fatal(err)
	}
	if res.Results[0].RetryCount != 2 {
		t.Fatalf("flaky gate retried %d times, want 2 (test needs real retries)", res.Results[0].RetryCount)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("scope computed %d times in one RunAll with retries and two gates, want 1", n)
	}

	// A second run is a new diff and gets its own computation.
	if _, err := r.RunAll(context.Background(), "GH-5926"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("scope computed %d times across two RunAll calls, want 2", n)
	}
}

func TestExecutor_BaseBranchReachesScope(t *testing.T) {
	var gotDir, gotBase string
	e := NewExecutor(&ExecutorConfig{
		Config:      &Config{Enabled: true, Gates: []*Gate{{Name: "g", Type: GateCustom, Command: "true", Timeout: 5 * time.Second}}},
		ProjectPath: t.TempDir(),
		TaskID:      "GH-5926",
		BaseBranch:  "develop",
	})
	e.runner.scopeFn = func(_ context.Context, dir, base string) ([]string, bool, string) {
		gotDir, gotBase = dir, base
		return nil, true, "stub"
	}
	if _, err := e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotBase != "develop" || gotDir != e.ProjectDir() {
		t.Errorf("scope computed for (%q, %q), want (%q, develop)", gotDir, gotBase, e.ProjectDir())
	}
}
