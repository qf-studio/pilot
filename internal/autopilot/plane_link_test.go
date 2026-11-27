package autopilot

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newFileStateStore opens a state store on a database file, so a second
// store on the same path sees what the first one wrote (a daemon restart).
func newFileStateStore(t *testing.T, path string) *StateStore {
	t.Helper()
	store, err := NewStateStoreFromPath(path)
	if err != nil {
		t.Fatalf("NewStateStoreFromPath(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	return store
}

// tableColumns returns the column names of a table.
func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return cols
}

var planeLinkColumns = []string{"plane_project_id", "plane_work_item_id", "plane_done_notified"}

// TestPlaneLink_StateStoreRoundTrip: the three Plane fields (GH-6739) survive
// a save and a load from a new store on the same database file.
func TestPlaneLink_StateStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.db")
	const repo = "owner/repo"

	first := newFileStateStore(t, path)
	pr := &PRState{
		PRNumber:          77,
		PRURL:             "https://github.com/owner/repo/pull/77",
		BranchName:        "pilot/PLANE-12",
		Stage:             StageWaitingCI,
		CIStatus:          CIPending,
		CreatedAt:         time.Now(),
		PlaneProjectID:    "plane-project-uuid",
		PlaneWorkItemID:   "plane-work-item-uuid",
		PlaneDoneNotified: true,
	}
	if err := first.SavePRState(repo, pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	second := newFileStateStore(t, path)
	got, err := second.GetPRState(repo, 77)
	if err != nil || got == nil {
		t.Fatalf("GetPRState = (%v, %v), want the saved row", got, err)
	}
	if got.PlaneProjectID != pr.PlaneProjectID || got.PlaneWorkItemID != pr.PlaneWorkItemID || !got.PlaneDoneNotified {
		t.Fatalf("GetPRState Plane fields = (%q, %q, %v), want (%q, %q, true)",
			got.PlaneProjectID, got.PlaneWorkItemID, got.PlaneDoneNotified, pr.PlaneProjectID, pr.PlaneWorkItemID)
	}

	all, err := second.LoadAllPRStates(repo)
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates = (%d rows, %v), want 1 row", len(all), err)
	}
	if all[0].PlaneProjectID != pr.PlaneProjectID || all[0].PlaneWorkItemID != pr.PlaneWorkItemID || !all[0].PlaneDoneNotified {
		t.Fatalf("LoadAllPRStates Plane fields = (%q, %q, %v), want (%q, %q, true)",
			all[0].PlaneProjectID, all[0].PlaneWorkItemID, all[0].PlaneDoneNotified, pr.PlaneProjectID, pr.PlaneWorkItemID)
	}
}

// TestPlaneLink_OldDatabaseLoads: a database made before GH-6739 gets the
// three columns from the migration, and its rows load with empty values.
func TestPlaneLink_OldDatabaseLoads(t *testing.T) {
	const repo = "owner/repo"

	t.Run("repo-scoped table without the plane columns", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pilot.db")

		// Build the current schema, then remove the three columns and write
		// a row the way a pre-GH-6739 daemon did.
		old := newFileStateStore(t, path)
		for _, col := range planeLinkColumns {
			if _, err := old.db.Exec(`ALTER TABLE autopilot_pr_state DROP COLUMN ` + col); err != nil {
				t.Fatalf("drop %s: %v", col, err)
			}
		}
		if _, err := old.db.Exec(`
			INSERT INTO autopilot_pr_state (pr_number, repo, pr_url, branch_name, stage, ci_status, created_at)
			VALUES (5, ?, 'https://github.com/owner/repo/pull/5', 'pilot/GH-5', 'waiting_ci', 'pending', CURRENT_TIMESTAMP)
		`, repo); err != nil {
			t.Fatalf("insert old row: %v", err)
		}
		cols := tableColumns(t, old.db, "autopilot_pr_state")
		for _, col := range planeLinkColumns {
			if cols[col] {
				t.Fatalf("seed still has column %s", col)
			}
		}
		_ = old.db.Close()

		assertOldRowLoads(t, newFileStateStore(t, path), repo, 5)
	})

	t.Run("pre-GH-3903 table rebuilt by the repo scoping migration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pilot.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := db.Exec(`
			CREATE TABLE autopilot_pr_state (
				pr_number INTEGER PRIMARY KEY,
				pr_url TEXT NOT NULL,
				issue_number INTEGER DEFAULT 0,
				branch_name TEXT NOT NULL DEFAULT '',
				head_sha TEXT DEFAULT '',
				stage TEXT NOT NULL,
				ci_status TEXT NOT NULL DEFAULT 'pending',
				last_checked DATETIME,
				ci_wait_started_at DATETIME,
				merge_attempts INTEGER DEFAULT 0,
				error TEXT DEFAULT '',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				release_version TEXT DEFAULT '',
				release_bump_type TEXT DEFAULT ''
			)
		`); err != nil {
			t.Fatalf("seed legacy autopilot_pr_state: %v", err)
		}
		if _, err := db.Exec(`
			INSERT INTO autopilot_pr_state (pr_number, pr_url, branch_name, stage, ci_status, created_at)
			VALUES (6, 'https://github.com/owner/repo/pull/6', 'pilot/GH-6', 'waiting_ci', 'pending', CURRENT_TIMESTAMP)
		`); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		_ = db.Close()

		assertOldRowLoads(t, newFileStateStore(t, path), repo, 6)
	})
}

func assertOldRowLoads(t *testing.T, store *StateStore, repo string, prNumber int) {
	t.Helper()
	cols := tableColumns(t, store.db, "autopilot_pr_state")
	for _, col := range planeLinkColumns {
		if !cols[col] {
			t.Fatalf("migration did not add column %s", col)
		}
	}
	got, err := store.GetPRState(repo, prNumber)
	if err != nil || got == nil {
		t.Fatalf("GetPRState = (%v, %v), want the old row", got, err)
	}
	if got.PlaneProjectID != "" || got.PlaneWorkItemID != "" || got.PlaneDoneNotified {
		t.Fatalf("old row Plane fields = (%q, %q, %v), want empty", got.PlaneProjectID, got.PlaneWorkItemID, got.PlaneDoneNotified)
	}
	all, err := store.LoadAllPRStates(repo)
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates = (%d rows, %v), want the old row", len(all), err)
	}
}

// TestPlaneLink_SetPlaneLink: SetPlaneLink (GH-6739) stores the link of a
// tracked PR in memory and in the store, and does nothing for an unknown PR
// or an empty ID.
func TestPlaneLink_SetPlaneLink(t *testing.T) {
	const (
		projectID  = "plane-project-uuid"
		workItemID = "plane-work-item-uuid"
	)

	tests := []struct {
		name       string
		register   bool
		prNumber   int
		projectID  string
		workItemID string
		want       bool
	}{
		{name: "known pull request", register: true, prNumber: 31, projectID: projectID, workItemID: workItemID, want: true},
		{name: "unknown pull request", register: false, prNumber: 32, projectID: projectID, workItemID: workItemID, want: false},
		{name: "empty project ID", register: true, prNumber: 33, projectID: "", workItemID: workItemID, want: false},
		{name: "empty work item ID", register: true, prNumber: 34, projectID: projectID, workItemID: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pilot.db")
			c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
			c.SetStateStore(newFileStateStore(t, path))
			if tt.register {
				c.OnPRCreated(tt.prNumber, "https://github.com/owner/repo/pull/1", 0, "sha", "pilot/PLANE-3", "")
			}

			if got := c.SetPlaneLink(tt.prNumber, tt.projectID, tt.workItemID); got != tt.want {
				t.Fatalf("SetPlaneLink = %v, want %v", got, tt.want)
			}

			reloaded := newFileStateStore(t, path)
			row, err := reloaded.GetPRState("owner/repo", tt.prNumber)
			if err != nil {
				t.Fatalf("GetPRState: %v", err)
			}

			if !tt.register {
				if row != nil {
					t.Fatalf("unknown PR got a row: %+v", row)
				}
				return
			}
			live, ok := c.GetPRState(tt.prNumber)
			if !ok || row == nil {
				t.Fatalf("registered PR missing: live=%v row=%v", ok, row)
			}

			wantProject, wantItem := "", ""
			if tt.want {
				wantProject, wantItem = tt.projectID, tt.workItemID
			}
			if live.PlaneProjectID != wantProject || live.PlaneWorkItemID != wantItem {
				t.Fatalf("live Plane fields = (%q, %q), want (%q, %q)", live.PlaneProjectID, live.PlaneWorkItemID, wantProject, wantItem)
			}
			if row.PlaneProjectID != wantProject || row.PlaneWorkItemID != wantItem {
				t.Fatalf("stored Plane fields after reload = (%q, %q), want (%q, %q)", row.PlaneProjectID, row.PlaneWorkItemID, wantProject, wantItem)
			}
			if row.PlaneDoneNotified {
				t.Fatal("PlaneDoneNotified set; SetPlaneLink must not touch it")
			}
		})
	}
}
