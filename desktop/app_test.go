package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/dashboard"
)

// TestGetGitGraph_DefaultLimit verifies that passing limit=0 falls back to 100
// and that the returned GitGraphData mirrors dashboard.GitGraphState fields.
func TestGetGitGraph_DefaultLimit(t *testing.T) {
	// Use "." as project path (current dir is inside a git repo during tests).
	state := dashboard.FetchGitGraph(".", 100)
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
	state := dashboard.FetchGitGraph(".", 5)
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
