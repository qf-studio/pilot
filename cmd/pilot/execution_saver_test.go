package main

import (
	"testing"
	"time"

	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestStoreExecutionSaver_ImplementsExecutionSaverV2 is the GH-4845
// fail-when-unwired guard: storeExecutionSaver must keep satisfying
// core.ExecutionSaverV2 with a VALUE receiver (poller_github.go wires a
// value, not a pointer — a pointer-receiver regression would silently fall
// the SDK poller back to the repo-blind legacy SaveDeclinedExecution path
// with no compile error). This mirrors the package-level `var _
// sdkCore.ExecutionSaverV2 = storeExecutionSaver{}` assertion in main.go,
// but as a test so `go vet`/`go build` failures here are attributed to this
// regression instead of an unrelated build break.
func TestStoreExecutionSaver_ImplementsExecutionSaverV2(t *testing.T) {
	var _ sdkCore.ExecutionSaverV2 = storeExecutionSaver{}
}

// TestStoreExecutionSaver_SaveDeclinedExecutionRecord_StampsCanary is the
// GH-4845 regression test for declined-preflight rows landing is_canary=0:
// storeExecutionSaver.SaveDeclinedExecutionRecord must resolve the owning
// project via rec.RepoOwner/RepoName (FindProjectByRepo) rather than
// ProjectPath alone, so a canary project sharing a local checkout path with
// a non-canary project (the GH-4833 collision) still stamps correctly.
func TestStoreExecutionSaver_SaveDeclinedExecutionRecord_StampsCanary(t *testing.T) {
	sharedPath := "/tmp/pilot-gh-4845-shared-path-does-not-exist"
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{
				Name:   "pilot",
				Path:   sharedPath,
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot"},
				Canary: false,
			},
			{
				Name:   "pilot-canary-sandbox",
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot-canary-sandbox"},
				Canary: true,
			},
		},
	}

	tests := []struct {
		name       string
		rec        sdkCore.DeclinedExecutionRecord
		wantCanary bool
	}{
		{
			name: "RepoOwner/RepoName resolve the canary project despite the shared path",
			rec: sdkCore.DeclinedExecutionRecord{
				TaskID:      "GH-9001",
				ProjectPath: sharedPath,
				Status:      "declined-preflight",
				Reason:      "test decline",
				RepoOwner:   "qf-studio",
				RepoName:    "pilot-canary-sandbox",
			},
			wantCanary: true,
		},
		{
			name: "RepoOwner/RepoName resolve the default (non-canary) project — no false positive",
			rec: sdkCore.DeclinedExecutionRecord{
				TaskID:      "GH-9002",
				ProjectPath: sharedPath,
				Status:      "declined-preflight",
				Reason:      "test decline",
				RepoOwner:   "qf-studio",
				RepoName:    "pilot",
			},
			wantCanary: false,
		},
		{
			name: "empty repo identity falls back to the path lookup (legacy behavior preserved)",
			rec: sdkCore.DeclinedExecutionRecord{
				TaskID:      "GH-9003",
				ProjectPath: sharedPath,
				Status:      "declined-preflight",
				Reason:      "test decline",
			},
			wantCanary: false, // GetProject(sharedPath) matches the first (non-canary) entry
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore failed: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			saver := storeExecutionSaver{store: store, cfg: cfg}
			if err := saver.SaveDeclinedExecutionRecord(tt.rec); err != nil {
				t.Fatalf("SaveDeclinedExecutionRecord failed: %v", err)
			}

			exec, err := store.GetLatestExecutionByTaskID(tt.rec.TaskID, tt.rec.ProjectPath)
			if err != nil {
				t.Fatalf("GetLatestExecutionByTaskID failed: %v", err)
			}
			if exec.IsCanary != tt.wantCanary {
				t.Errorf("exec.IsCanary = %v, want %v", exec.IsCanary, tt.wantCanary)
			}
			if exec.Status != tt.rec.Status {
				t.Errorf("exec.Status = %q, want %q", exec.Status, tt.rec.Status)
			}
		})
	}
}

// TestStoreExecutionSaver_SaveDeclinedExecution_DelegatesWithEmptyRepoIdentity
// is the legacy-path regression test: the narrower core.ExecutionSaver
// method must keep behaving exactly as before GH-4845 — it never carried
// repo identity, so it must always resolve via the path-only lookup, never
// via FindProjectByRepo.
func TestStoreExecutionSaver_SaveDeclinedExecution_DelegatesWithEmptyRepoIdentity(t *testing.T) {
	sharedPath := "/tmp/pilot-gh-4845-legacy-path-does-not-exist"
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{
				Name:   "pilot-canary-sandbox",
				Path:   sharedPath,
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot-canary-sandbox"},
				Canary: true,
			},
		},
	}

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	saver := storeExecutionSaver{store: store, cfg: cfg}
	taskID := "GH-9004"
	if err := saver.SaveDeclinedExecution(taskID, sharedPath, "declined-preflight", "legacy call site"); err != nil {
		t.Fatalf("SaveDeclinedExecution failed: %v", err)
	}

	exec, err := store.GetLatestExecutionByTaskID(taskID, sharedPath)
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID failed: %v", err)
	}
	// The legacy path resolves via GetProject(sharedPath), which DOES match
	// here (single project registered at sharedPath) — this asserts the
	// path-only fallback still works and stamps canary when the path itself
	// is registered as canary, unchanged from pre-GH-4845 behavior.
	if !exec.IsCanary {
		t.Error("expected path-only lookup to stamp is_canary=1 for a canary-registered path")
	}
}

// TestGetLifetimeTaskCounts_FoldsInDeclinedPreflight and
// TestGetWindowedStats_FoldsInDeclinedPreflight are the GH-4845 fold-in
// regression tests: 'declined-preflight' rows must land in the same
// Declined/AttemptDeclined bucket as 'declined' rows, and canary rows must
// be excluded from fleet volume entirely (GetLifetimeTaskCounts.Total /
// WindowedStats.AttemptTotal).
func TestGetLifetimeTaskCounts_FoldsInDeclinedPreflight(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	projectPath := "/tmp/gh-4845-lifetime-counts"
	mustSave := func(id, status string, isCanary bool) {
		t.Helper()
		if err := store.SaveExecution(&memory.Execution{
			ID:          id,
			TaskID:      id,
			ProjectPath: projectPath,
			Status:      status,
			CreatedAt:   now,
			CompletedAt: &now,
			IsCanary:    isCanary,
		}); err != nil {
			t.Fatalf("SaveExecution(%s) failed: %v", id, err)
		}
	}

	mustSave("t-declined", "declined", false)
	mustSave("t-declined-preflight", "declined-preflight", false)
	mustSave("t-declined-preflight-canary", "declined-preflight", true)

	counts, err := store.GetLifetimeTaskCounts(projectPath)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts failed: %v", err)
	}
	if counts.Declined != 2 {
		t.Errorf("Declined = %d, want 2 (one 'declined' + one non-canary 'declined-preflight')", counts.Declined)
	}
	if counts.Total != 2 {
		t.Errorf("Total = %d, want 2 (canary row excluded entirely)", counts.Total)
	}
}

func TestGetWindowedStats_FoldsInDeclinedPreflight(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	projectPath := "/tmp/gh-4845-windowed-stats"
	mustSave := func(id, status string, isCanary bool) {
		t.Helper()
		if err := store.SaveExecution(&memory.Execution{
			ID:          id,
			TaskID:      id,
			ProjectPath: projectPath,
			Status:      status,
			CreatedAt:   now,
			CompletedAt: &now,
			IsCanary:    isCanary,
		}); err != nil {
			t.Fatalf("SaveExecution(%s) failed: %v", id, err)
		}
	}

	mustSave("w-declined", "declined", false)
	mustSave("w-declined-preflight", "declined-preflight", false)
	mustSave("w-declined-preflight-canary", "declined-preflight", true)

	ws, err := store.GetWindowedStats(projectPath, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetWindowedStats failed: %v", err)
	}
	if ws.AttemptDeclined != 2 {
		t.Errorf("AttemptDeclined = %d, want 2 (one 'declined' + one non-canary 'declined-preflight')", ws.AttemptDeclined)
	}
	if ws.AttemptTotal != 2 {
		t.Errorf("AttemptTotal = %d, want 2 (canary row excluded from fleet volume)", ws.AttemptTotal)
	}
}
