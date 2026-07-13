package dashboard

import (
	"fmt"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func taskIDs(execs []*memory.Execution) []string {
	out := make([]string, len(execs))
	for i, e := range execs {
		out[i] = e.TaskID
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFirstNDistinctByTask(t *testing.T) {
	ex := func(id, status string) *memory.Execution {
		return &memory.Execution{TaskID: id, Status: status}
	}

	tests := []struct {
		name string
		in   []*memory.Execution
		n    int
		want []string
	}{
		{
			name: "retried task does not crowd out the rest (GH-4100 scenario)",
			// created_at DESC: GH-4100 retried 4×, then distinct tasks.
			in: []*memory.Execution{
				ex("GH-4100", "skipped"), ex("GH-4100", "completed"), ex("GH-4100", "completed"),
				ex("GH-4100", "completed"), ex("GH-4105", "no_op"), ex("GH-4106", "no_op"),
				ex("GH-4107", "failed"), ex("GH-4101", "completed"),
			},
			n:    5,
			want: []string{"GH-4100", "GH-4105", "GH-4106", "GH-4107", "GH-4101"},
		},
		{
			name: "keeps first (latest) occurrence per task",
			in:   []*memory.Execution{ex("A", "skipped"), ex("A", "completed"), ex("B", "completed")},
			n:    5,
			want: []string{"A", "B"},
		},
		{
			name: "caps at n distinct tasks",
			in:   []*memory.Execution{ex("A", "c"), ex("B", "c"), ex("C", "c"), ex("D", "c")},
			n:    2,
			want: []string{"A", "B"},
		},
		{
			name: "fewer distinct than n returns all",
			in:   []*memory.Execution{ex("A", "c"), ex("A", "c")},
			n:    5,
			want: []string{"A"},
		},
		{name: "n<=0 returns nil", in: []*memory.Execution{ex("A", "c")}, n: 0, want: nil},
		{name: "empty input", in: nil, n: 5, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskIDs(firstNDistinctByTask(tt.in, tt.n))
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if !eq(got, tt.want) {
				t.Errorf("firstNDistinctByTask = %v, want %v", got, tt.want)
			}
			// the kept row for a retried task must be the FIRST (latest) one
			if tt.name == "keeps first (latest) occurrence per task" {
				kept := firstNDistinctByTask(tt.in, tt.n)
				if kept[0].Status != "skipped" {
					t.Errorf("kept status = %q, want %q (latest, not an older retry)", kept[0].Status, "skipped")
				}
			}
		})
	}
}

// TestFirstNDistinctByTask_PrefersNonEmptyTitle is the GH-4218/GH-4282
// regression test: a task can retry several times where only one non-latest
// row carries task_title (e.g. backfilled or set on an earlier attempt). The
// kept row for that task must surface the resolved title rather than the
// blank title on the bare-latest row.
func TestFirstNDistinctByTask_PrefersNonEmptyTitle(t *testing.T) {
	exWithTitle := func(id, title string) *memory.Execution {
		return &memory.Execution{TaskID: id, TaskTitle: title}
	}

	// created_at DESC: newest first. GH-4218 retried 4x — only the 3rd-newest
	// row carries the resolved title, the rest (including the latest) are blank.
	in := []*memory.Execution{
		exWithTitle("GH-4218", ""),
		exWithTitle("GH-4218", ""),
		exWithTitle("GH-4218", "fix(dashboard): resolved title"),
		exWithTitle("GH-4218", ""),
		exWithTitle("GH-4105", "some other task"),
	}

	got := firstNDistinctByTask(in, 5)

	if len(got) != 2 {
		t.Fatalf("got %d executions, want 2", len(got))
	}
	if got[0].TaskID != "GH-4218" {
		t.Fatalf("got[0].TaskID = %q, want %q", got[0].TaskID, "GH-4218")
	}
	if got[0].TaskTitle != "fix(dashboard): resolved title" {
		t.Errorf("got[0].TaskTitle = %q, want resolved title to surface despite blank latest row", got[0].TaskTitle)
	}
}

// TestFirstNDistinctByTask_FallsBackToLatestWhenNoTitle verifies that when
// none of a task's rows carry a title, dedup falls back to the plain latest
// row (preserving pre-GH-4282 behavior in the no-title case).
func TestFirstNDistinctByTask_FallsBackToLatestWhenNoTitle(t *testing.T) {
	ex := func(id, status string) *memory.Execution {
		return &memory.Execution{TaskID: id, Status: status}
	}

	in := []*memory.Execution{
		ex("A", "skipped"), ex("A", "completed"), ex("A", "completed"),
	}

	got := firstNDistinctByTask(in, 5)

	if len(got) != 1 {
		t.Fatalf("got %d executions, want 1", len(got))
	}
	if got[0].Status != "skipped" {
		t.Errorf("got[0].Status = %q, want %q (latest row, no title anywhere to prefer)", got[0].Status, "skipped")
	}
}

// TestStoreRefreshCmd_DedupsRetriesAcrossFiveDistinctTasks is the GH-4119
// regression test: storeRefreshCmd (the periodic refresh path) must dedup by
// task_id like hydrateFromStore does, not cap on raw rows — otherwise a
// retried task re-collapses the history panel on the first timer tick after
// #4117 fixed only the startup path.
func TestStoreRefreshCmd_DedupsRetriesAcrossFiveDistinctTasks(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const projectPath = "/proj"

	// 16 older executions, one per distinct task.
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("older-exec-%d", i)
		taskID := fmt.Sprintf("GH-41%02d", i)
		if err := store.SaveExecution(&memory.Execution{
			ID: id, TaskID: taskID, ProjectPath: projectPath, Status: "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
	}

	// created_at has 1-second resolution (SQLite CURRENT_TIMESTAMP default) —
	// cross a second boundary so the newest task's retries reliably sort
	// above the older batch in GetRecentExecutions' created_at DESC order.
	time.Sleep(1100 * time.Millisecond)

	// 4 retries for the newest task (mirrors the live GH-4100 scenario).
	const newestTaskID = "GH-4100"
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("retry-exec-%d", i)
		if err := store.SaveExecution(&memory.Execution{
			ID: id, TaskID: newestTaskID, ProjectPath: projectPath, Status: "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
	}

	msg, ok := storeRefreshCmd(store, projectPath)().(storeRefreshMsg)
	if !ok {
		t.Fatalf("storeRefreshCmd returned unexpected message type")
	}

	if len(msg.completedTasks) != 5 {
		t.Fatalf("completedTasks = %d entries, want 5", len(msg.completedTasks))
	}

	seen := make(map[string]bool, len(msg.completedTasks))
	foundNewest := false
	for _, ct := range msg.completedTasks {
		if seen[ct.ID] {
			t.Errorf("duplicate task ID %q in completedTasks", ct.ID)
		}
		seen[ct.ID] = true
		if ct.ID == newestTaskID {
			foundNewest = true
		}
	}
	if !foundNewest {
		t.Errorf("completedTasks missing newest retried task %q — retries crowded it out (raw-row cap regression)", newestTaskID)
	}
}
