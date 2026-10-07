package quality

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH-5622: a tenant box without `make` must not fail every gate of a repo that
// has a Makefile, and a missing runner (exit 127) must fail fast and distinctly.

func writeProjectFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func newTestRunnerWithLog(cfg *Config, dir string, makeOnPath bool) (*Runner, *bytes.Buffer) {
	r := NewRunner(cfg, dir)
	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.lookPath = func(name string) (string, error) {
		if name == "make" && makeOnPath {
			return "/usr/bin/make", nil
		}
		return "", errors.New("not found")
	}
	return r, &buf
}

func TestResolveCommand_MakeFallback(t *testing.T) {
	goMakefile := map[string]string{
		"go.mod":   "module example.com/x\n\ngo 1.21\n",
		"Makefile": "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n",
	}

	tests := []struct {
		name       string
		files      map[string]string
		gate       *Gate
		makeOnPath bool
		want       string
		wantWarn   bool
	}{
		{"make on PATH keeps make test", goMakefile, &Gate{Name: "test", Type: GateTest, Command: "make test"}, true, "make test", false},
		{"no make falls back to go test", goMakefile, &Gate{Name: "test", Type: GateTest, Command: "make test"}, false, "go test ./...", true},
		{"no make falls back to go build", goMakefile, &Gate{Name: "build", Type: GateBuild, Command: "make build"}, false, "go build ./...", true},
		{"make with flags still falls back", goMakefile, &Gate{Name: "test", Type: GateTest, Command: "make -j4 test"}, false, "go test ./...", true},
		{"non-make command untouched", goMakefile, &Gate{Name: "test", Type: GateTest, Command: "go test -race ./..."}, false, "go test -race ./...", false},
		{"lint has no native equivalent", goMakefile, &Gate{Name: "lint", Type: GateLint, Command: "make lint"}, false, "make lint", false},
		{"no detectable toolchain keeps make", map[string]string{"Makefile": "test:\n\ttrue\n"}, &Gate{Name: "test", Type: GateTest, Command: "make test"}, false, "make test", false},
		{
			"node project falls back to npm test",
			map[string]string{"package.json": "{}", "Makefile": "test:\n\tnpm test\n"},
			&Gate{Name: "test", Type: GateTest, Command: "make test"}, false, "npm test", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeProjectFiles(t, tt.files)
			r, buf := newTestRunnerWithLog(&Config{Enabled: true}, dir, tt.makeOnPath)

			if got := r.resolveCommand(tt.gate); got != tt.want {
				t.Errorf("resolveCommand() = %q, want %q", got, tt.want)
			}
			if gotWarn := strings.Contains(buf.String(), "level=WARN"); gotWarn != tt.wantWarn {
				t.Errorf("WARN logged = %v, want %v; log:\n%s", gotWarn, tt.wantWarn, buf.String())
			}
		})
	}
}

func TestResolveCommand_WarnsOncePerGate(t *testing.T) {
	dir := writeProjectFiles(t, map[string]string{
		"go.mod":   "module example.com/x\n\ngo 1.21\n",
		"Makefile": "test:\n\tgo test ./...\n",
	})
	r, buf := newTestRunnerWithLog(&Config{Enabled: true}, dir, false)
	gate := &Gate{Name: "test", Type: GateTest, Command: "make test"}

	for i := 0; i < 3; i++ {
		if got := r.resolveCommand(gate); got != "go test ./..." {
			t.Fatalf("resolveCommand() = %q", got)
		}
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Errorf("WARN logged %d times, want 1; log:\n%s", n, buf.String())
	}
	if gate.Command != "make test" {
		t.Errorf("shared gate config mutated: %q", gate.Command)
	}
}

func TestRunGate_FallbackPassesWithoutMake(t *testing.T) {
	dir := writeProjectFiles(t, map[string]string{
		"go.mod":    "module example.com/x\n\ngo 1.21\n",
		"x.go":      "package x\n",
		"x_test.go": "package x\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
		"Makefile":  "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n",
	})
	cfg := &Config{Enabled: true, Gates: []*Gate{
		{Name: "build", Type: GateBuild, Command: "make build", Required: true, Timeout: time.Minute},
		{Name: "test", Type: GateTest, Command: "make test", Required: true, Timeout: time.Minute},
	}}
	r, _ := newTestRunnerWithLog(cfg, dir, false)

	results, err := r.RunAll(context.Background(), "GH-5622")
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if !results.AllPassed {
		for _, res := range results.Results {
			t.Logf("%s: status=%s cmd=%q exit=%d err=%s out=%s", res.GateName, res.Status, res.Command, res.ExitCode, res.Error, res.Output)
		}
		t.Fatal("expected gates to pass via native fallback")
	}
	if got := results.Results[1].Command; got != "go test ./..." {
		t.Errorf("test gate ran %q, want go test ./...", got)
	}
}

func TestRunGate_Exit127IsRunnerMissingAndNotRetried(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	cfg := &Config{Enabled: true, Gates: []*Gate{{
		Name:       "test",
		Type:       GateCustom,
		Command:    "echo x >> " + counter + "; exit 127",
		Required:   true,
		Timeout:    time.Minute,
		MaxRetries: 2,
		RetryDelay: 10 * time.Second, // would dominate the runtime if a retry happened
	}}}
	r, _ := newTestRunnerWithLog(cfg, dir, true)

	start := time.Now()
	res := r.runGate(context.Background(), cfg.Gates[0])
	if time.Since(start) > 5*time.Second {
		t.Error("gate waited out a retry delay on exit 127")
	}

	if res.Status != StatusFailed {
		t.Errorf("status = %s, want failed", res.Status)
	}
	if !errors.Is(res.Err, ErrGateRunnerMissing) || !res.RunnerMissing() {
		t.Errorf("Err = %v, want ErrGateRunnerMissing", res.Err)
	}
	if res.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", res.RetryCount)
	}
	if res.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127", res.ExitCode)
	}
	if !strings.Contains(res.Error, cfg.Gates[0].Command) || !strings.Contains(res.Error, "127") {
		t.Errorf("Error %q should name the command and exit code", res.Error)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Errorf("command ran %d times, want 1", n)
	}
}

func TestRunGate_OrdinaryFailureStillRetries(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Enabled: true, Gates: []*Gate{{
		Name: "test", Type: GateCustom, Command: "exit 1", Required: true,
		Timeout: time.Minute, MaxRetries: 1,
	}}}
	r, _ := newTestRunnerWithLog(cfg, dir, true)

	res := r.runGate(context.Background(), cfg.Gates[0])
	if res.RunnerMissing() {
		t.Error("exit 1 must not be classified as runner missing")
	}
	if res.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", res.RetryCount)
	}
}

func TestShouldRetry_RunnerMissing(t *testing.T) {
	missing := &Result{Status: StatusFailed, Err: ErrGateRunnerMissing}
	red := &Result{Status: StatusFailed}
	passed := &Result{Status: StatusPassed}

	cfg := &Config{
		Gates: []*Gate{
			{Name: "build", Required: true},
			{Name: "test", Required: true},
			{Name: "lint", Required: false},
		},
		OnFailure: FailureConfig{Action: ActionRetry, MaxRetries: 2},
	}

	tests := []struct {
		name    string
		results []*Result
		want    bool
	}{
		{"only missing runner: no retry", []*Result{passed, missing, passed}, false},
		{"missing runner plus red test: still fixable", []*Result{missing, red, passed}, true},
		{"red test: retry", []*Result{passed, red, passed}, true},
		{"optional gate missing runner, required red: retry", []*Result{passed, red, missing}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRetry(cfg, &CheckResults{AllPassed: false, Results: tt.results}, 0)
			if got != tt.want {
				t.Errorf("ShouldRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}
