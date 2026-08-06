package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/dashboard"
)

// newGitGraphFixtureRepo creates a hermetic git repository in a t.TempDir(),
// seeded with a deterministic commit/branch topology — including a merge —
// so TestGetGitGraph_* exercise `--graph` connector lines without depending
// on the real working repo's history or network access (GH-4758).
func newGitGraphFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Fixture",
			"GIT_AUTHOR_EMAIL=fixture@example.com",
			"GIT_COMMITTER_NAME=Fixture",
			"GIT_COMMITTER_EMAIL=fixture@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeCommit := func(name, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		runGit("add", name)
		runGit("commit", "-q", "-m", msg)
	}

	runGit("init", "-q", "-b", "main")
	writeCommit("a.txt", "initial commit")

	runGit("checkout", "-q", "-b", "feature")
	writeCommit("b.txt", "feature commit")

	runGit("checkout", "-q", "main")
	writeCommit("c.txt", "main commit")

	runGit("merge", "--no-ff", "-q", "-m", "Merge branch 'feature'", "feature")

	return dir
}

// configureFixtureProject isolates HOME to a fresh temp dir and writes a
// ~/.pilot/config.yaml whose sole project points at repoPath, so
// App.GetGitGraph (which resolves cfg.Projects[0].Path — see app.go) reads
// the same hermetic fixture repo as a direct dashboard.FetchGitGraph call.
func configureFixtureProject(t *testing.T, repoPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".pilot")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	yamlContent := fmt.Sprintf("projects:\n  - name: fixture\n    path: %q\n", repoPath)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestGetGitGraph_DefaultLimit verifies that passing limit=0 falls back to 100
// and that the returned GitGraphData mirrors dashboard.GitGraphState fields.
func TestGetGitGraph_DefaultLimit(t *testing.T) {
	repoPath := newGitGraphFixtureRepo(t)
	configureFixtureProject(t, repoPath)

	state := dashboard.FetchGitGraph(repoPath, 100)
	if state == nil {
		t.Skip("git not available in test environment")
	}

	app := &App{}
	// limit=0 should default to 100 — same result as explicit 100.
	got := app.GetGitGraph(0)
	if got.TotalCount != state.TotalCount {
		t.Errorf("TotalCount mismatch: got %d, want %d", got.TotalCount, state.TotalCount)
	}
	if len(got.Lines) != len(state.Lines) {
		t.Errorf("Lines length mismatch: got %d, want %d", len(got.Lines), len(state.Lines))
	}
}

// TestGetGitGraph_LinesMapping verifies each GitGraphLine field is copied correctly.
func TestGetGitGraph_LinesMapping(t *testing.T) {
	repoPath := newGitGraphFixtureRepo(t)
	configureFixtureProject(t, repoPath)

	state := dashboard.FetchGitGraph(repoPath, 5)
	if state == nil || len(state.Lines) == 0 {
		t.Skip("no git commits available in test environment")
	}

	app := &App{}
	got := app.GetGitGraph(5)

	for i, want := range state.Lines {
		if i >= len(got.Lines) {
			t.Fatalf("missing line at index %d", i)
		}
		gl := got.Lines[i]
		if gl.GraphChars != want.GraphChars {
			t.Errorf("line[%d].GraphChars = %q, want %q", i, gl.GraphChars, want.GraphChars)
		}
		if gl.SHA != want.SHA {
			t.Errorf("line[%d].SHA = %q, want %q", i, gl.SHA, want.SHA)
		}
		if gl.Message != want.Message {
			t.Errorf("line[%d].Message = %q, want %q", i, gl.Message, want.Message)
		}
	}
}

func TestGetServerStatus_DaemonRunning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"version": "1.40.1",
			"running": true,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	app := &App{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		gatewayURL: srv.URL,
	}

	status := app.GetServerStatus()
	if !status.Running {
		t.Fatal("expected Running=true when daemon is healthy")
	}
	if status.Version != "1.40.1" {
		t.Fatalf("expected version 1.40.1, got %q", status.Version)
	}
	if status.GatewayURL != srv.URL {
		t.Fatalf("expected GatewayURL=%q, got %q", srv.URL, status.GatewayURL)
	}
}

func TestGetServerStatus_DaemonNotRunning(t *testing.T) {
	app := &App{
		httpClient: &http.Client{Timeout: 1 * time.Second},
		gatewayURL: "http://127.0.0.1:1", // nothing listening
	}

	status := app.GetServerStatus()
	if status.Running {
		t.Fatal("expected Running=false when daemon is unreachable")
	}
}

func TestGetServerStatus_EmptyGatewayURL(t *testing.T) {
	app := &App{
		httpClient: &http.Client{Timeout: 1 * time.Second},
		gatewayURL: "",
	}

	status := app.GetServerStatus()
	if status.Running {
		t.Fatal("expected Running=false when gatewayURL is empty")
	}
}

func TestQueueTaskBetter(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)

	tests := []struct {
		name      string
		candidate QueueTask
		existing  QueueTask
		want      bool
	}{
		{
			name:      "running beats done",
			candidate: QueueTask{Status: "running", CreatedAt: earlier},
			existing:  QueueTask{Status: "done", CreatedAt: now},
			want:      true,
		},
		{
			name:      "done beats failed",
			candidate: QueueTask{Status: "done", CreatedAt: earlier},
			existing:  QueueTask{Status: "failed", CreatedAt: now},
			want:      true,
		},
		{
			name:      "failed does not beat done",
			candidate: QueueTask{Status: "failed", CreatedAt: now},
			existing:  QueueTask{Status: "done", CreatedAt: earlier},
			want:      false,
		},
		{
			name:      "same status newer wins",
			candidate: QueueTask{Status: "done", CreatedAt: now},
			existing:  QueueTask{Status: "done", CreatedAt: earlier},
			want:      true,
		},
		{
			name:      "same status older loses",
			candidate: QueueTask{Status: "done", CreatedAt: earlier},
			existing:  QueueTask{Status: "done", CreatedAt: now},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queueTaskBetter(tt.candidate, tt.existing)
			if got != tt.want {
				t.Errorf("queueTaskBetter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHistoryEntryBetter(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)

	tests := []struct {
		name      string
		candidate HistoryEntry
		existing  HistoryEntry
		want      bool
	}{
		{
			name:      "completed beats failed",
			candidate: HistoryEntry{Status: "completed", CompletedAt: earlier},
			existing:  HistoryEntry{Status: "failed", CompletedAt: now},
			want:      true,
		},
		{
			name:      "failed does not beat completed",
			candidate: HistoryEntry{Status: "failed", CompletedAt: now},
			existing:  HistoryEntry{Status: "completed", CompletedAt: earlier},
			want:      false,
		},
		{
			name:      "with PR URL beats without",
			candidate: HistoryEntry{Status: "completed", PRURL: "https://pr/1", CompletedAt: earlier},
			existing:  HistoryEntry{Status: "completed", CompletedAt: now},
			want:      true,
		},
		{
			name:      "without PR URL does not beat with",
			candidate: HistoryEntry{Status: "completed", CompletedAt: now},
			existing:  HistoryEntry{Status: "completed", PRURL: "https://pr/1", CompletedAt: earlier},
			want:      false,
		},
		{
			name:      "same status same PR newer wins",
			candidate: HistoryEntry{Status: "failed", CompletedAt: now},
			existing:  HistoryEntry{Status: "failed", CompletedAt: earlier},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := historyEntryBetter(tt.candidate, tt.existing)
			if got != tt.want {
				t.Errorf("historyEntryBetter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetServerStatus_HealthOK_StatusUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	app := &App{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		gatewayURL: srv.URL,
	}

	status := app.GetServerStatus()
	if !status.Running {
		t.Fatal("expected Running=true even when /api/v1/status returns 401")
	}
	if status.Version != "" {
		t.Fatalf("expected empty version when status is unauthorized, got %q", status.Version)
	}
}
