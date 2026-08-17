package memory

import (
	"os"
	"testing"
)

func newTestStoreForEval(t *testing.T) (*Store, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-eval-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	store, err := NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("NewStore: %v", err)
	}
	return store, func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

func TestExtractEvalTask(t *testing.T) {
	tests := []struct {
		name         string
		input        EvalInput
		wantSuccess  bool
		wantCriteria int
	}{
		{
			name: "successful execution with quality gates",
			input: EvalInput{
				TaskID:     "exec-1",
				Success:    true,
				DurationMs: 5000,
				GateResults: []EvalGateResult{
					{Name: "build", Passed: true},
					{Name: "test", Passed: true},
					{Name: "lint", Passed: true},
				},
				Repo:         "org/repo",
				IssueNumber:  42,
				IssueTitle:   "Add feature X",
				FilesChanged: []string{"main.go", "main_test.go"},
			},
			wantSuccess:  true,
			wantCriteria: 3,
		},
		{
			name: "failed execution",
			input: EvalInput{
				TaskID:     "exec-2",
				Success:    false,
				DurationMs: 2000,
				GateResults: []EvalGateResult{
					{Name: "build", Passed: true},
					{Name: "test", Passed: false},
				},
				Repo:         "org/repo",
				IssueNumber:  43,
				IssueTitle:   "Fix bug Y",
				FilesChanged: []string{"bug.go"},
			},
			wantSuccess:  false,
			wantCriteria: 2,
		},
		{
			name: "nil quality gates",
			input: EvalInput{
				TaskID:      "exec-3",
				Success:     true,
				DurationMs:  1000,
				GateResults: nil,
				Repo:        "org/repo",
				IssueNumber: 44,
				IssueTitle:  "Docs update",
			},
			wantSuccess:  true,
			wantCriteria: 0,
		},
		{
			name: "empty files changed",
			input: EvalInput{
				TaskID:       "exec-4",
				Success:      true,
				DurationMs:   500,
				Repo:         "org/other",
				IssueNumber:  1,
				IssueTitle:   "Init",
				FilesChanged: []string{},
			},
			wantSuccess:  true,
			wantCriteria: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := ExtractEvalTask(tt.input)

			if task.ID == "" {
				t.Error("expected non-empty ID")
			}
			if task.ExecutionID != tt.input.TaskID {
				t.Errorf("ExecutionID = %q, want %q", task.ExecutionID, tt.input.TaskID)
			}
			if task.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", task.Success, tt.wantSuccess)
			}
			if len(task.PassCriteria) != tt.wantCriteria {
				t.Errorf("PassCriteria len = %d, want %d", len(task.PassCriteria), tt.wantCriteria)
			}
			if task.IssueNumber != tt.input.IssueNumber {
				t.Errorf("IssueNumber = %d, want %d", task.IssueNumber, tt.input.IssueNumber)
			}
			if task.Repo != tt.input.Repo {
				t.Errorf("Repo = %q, want %q", task.Repo, tt.input.Repo)
			}
			if task.DurationMs != tt.input.DurationMs {
				t.Errorf("DurationMs = %d, want %d", task.DurationMs, tt.input.DurationMs)
			}
		})
	}
}

func TestSaveAndListEvalTasks(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	task1 := &EvalTask{
		ID:          "eval-aaa",
		ExecutionID: "exec-1",
		IssueNumber: 10,
		IssueTitle:  "Feature A",
		Repo:        "org/repo",
		Success:     true,
		PassCriteria: []PassCriteria{
			{Type: "build", Passed: true},
			{Type: "test", Passed: true},
		},
		FilesChanged: []string{"a.go", "b.go"},
		DurationMs:   3000,
	}
	task2 := &EvalTask{
		ID:           "eval-bbb",
		ExecutionID:  "exec-2",
		IssueNumber:  11,
		IssueTitle:   "Feature B",
		Repo:         "org/repo",
		Success:      false,
		PassCriteria: []PassCriteria{{Type: "test", Passed: false}},
		FilesChanged: []string{"c.go"},
		DurationMs:   1500,
	}
	task3 := &EvalTask{
		ID:          "eval-ccc",
		ExecutionID: "exec-3",
		IssueNumber: 20,
		IssueTitle:  "Other repo task",
		Repo:        "org/other",
		Success:     true,
		DurationMs:  500,
	}

	for _, task := range []*EvalTask{task1, task2, task3} {
		if err := store.SaveEvalTask(task); err != nil {
			t.Fatalf("SaveEvalTask(%s): %v", task.ID, err)
		}
	}

	tests := []struct {
		name      string
		filter    EvalTaskFilter
		wantCount int
		wantIDs   []string
	}{
		{
			name:      "all tasks",
			filter:    EvalTaskFilter{},
			wantCount: 3,
		},
		{
			name:      "filter by repo",
			filter:    EvalTaskFilter{Repo: "org/repo"},
			wantCount: 2,
		},
		{
			name:      "filter by repo other",
			filter:    EvalTaskFilter{Repo: "org/other"},
			wantCount: 1,
			wantIDs:   []string{"eval-ccc"},
		},
		{
			name:      "success only",
			filter:    EvalTaskFilter{SuccessOnly: true},
			wantCount: 2,
		},
		{
			name:      "failed only",
			filter:    EvalTaskFilter{FailedOnly: true},
			wantCount: 1,
			wantIDs:   []string{"eval-bbb"},
		},
		{
			name:      "repo + success",
			filter:    EvalTaskFilter{Repo: "org/repo", SuccessOnly: true},
			wantCount: 1,
			wantIDs:   []string{"eval-aaa"},
		},
		{
			name:      "limit",
			filter:    EvalTaskFilter{Limit: 1},
			wantCount: 1,
		},
		{
			name:      "no matches",
			filter:    EvalTaskFilter{Repo: "nonexistent/repo"},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tasks, err := store.ListEvalTasks(tt.filter)
			if err != nil {
				t.Fatalf("ListEvalTasks: %v", err)
			}
			if len(tasks) != tt.wantCount {
				t.Errorf("got %d tasks, want %d", len(tasks), tt.wantCount)
			}
			if tt.wantIDs != nil {
				for i, wantID := range tt.wantIDs {
					if i < len(tasks) && tasks[i].ID != wantID {
						t.Errorf("tasks[%d].ID = %q, want %q", i, tasks[i].ID, wantID)
					}
				}
			}
		})
	}
}

func TestSaveEvalTaskDuplicatePrevention(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	task := &EvalTask{
		ID:          "eval-dup",
		ExecutionID: "exec-1",
		IssueNumber: 10,
		IssueTitle:  "Feature A",
		Repo:        "org/repo",
		Success:     false,
		DurationMs:  1000,
	}
	if err := store.SaveEvalTask(task); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Save again with same repo+issue_number but updated fields
	updated := &EvalTask{
		ID:           "eval-dup-v2",
		ExecutionID:  "exec-2",
		IssueNumber:  10,
		IssueTitle:   "Feature A",
		Repo:         "org/repo",
		Success:      true,
		PassCriteria: []PassCriteria{{Type: "build", Passed: true}},
		DurationMs:   2000,
	}
	if err := store.SaveEvalTask(updated); err != nil {
		t.Fatalf("upsert save: %v", err)
	}

	tasks, err := store.ListEvalTasks(EvalTaskFilter{Repo: "org/repo"})
	if err != nil {
		t.Fatalf("ListEvalTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after upsert, got %d", len(tasks))
	}
	if !tasks[0].Success {
		t.Error("expected success=true after upsert")
	}
	if tasks[0].ExecutionID != "exec-2" {
		t.Errorf("expected execution_id=exec-2 after upsert, got %s", tasks[0].ExecutionID)
	}
	if tasks[0].DurationMs != 2000 {
		t.Errorf("expected duration_ms=2000 after upsert, got %d", tasks[0].DurationMs)
	}
}

func TestListEvalTasksProjectPathFilter(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	taskA1 := &EvalTask{
		ID: "eval-proj-a1", ExecutionID: "exec-pa1", IssueNumber: 101,
		IssueTitle: "Alpha Feature 1", Repo: "org/repo",
		ProjectPath: "/projects/alpha", Success: true, DurationMs: 1000,
	}
	taskA2 := &EvalTask{
		ID: "eval-proj-a2", ExecutionID: "exec-pa2", IssueNumber: 102,
		IssueTitle: "Alpha Feature 2", Repo: "org/repo",
		ProjectPath: "/projects/alpha", Success: false, DurationMs: 2000,
	}
	taskB1 := &EvalTask{
		ID: "eval-proj-b1", ExecutionID: "exec-pb1", IssueNumber: 201,
		IssueTitle: "Beta Feature 1", Repo: "org/other",
		ProjectPath: "/projects/beta", Success: true, DurationMs: 500,
	}

	for _, task := range []*EvalTask{taskA1, taskA2, taskB1} {
		if err := store.SaveEvalTask(task); err != nil {
			t.Fatalf("SaveEvalTask(%s): %v", task.ID, err)
		}
	}

	// ProjectPath "A" returns only A's rows.
	alphaRows, err := store.ListEvalTasks(EvalTaskFilter{ProjectPath: "/projects/alpha"})
	if err != nil {
		t.Fatalf("ListEvalTasks alpha: %v", err)
	}
	if len(alphaRows) != 2 {
		t.Errorf("alpha: got %d tasks, want 2", len(alphaRows))
	}
	for _, row := range alphaRows {
		if row.ProjectPath != "/projects/alpha" {
			t.Errorf("expected ProjectPath=/projects/alpha, got %q", row.ProjectPath)
		}
	}

	// ProjectPath "B" returns only B's rows.
	betaRows, err := store.ListEvalTasks(EvalTaskFilter{ProjectPath: "/projects/beta"})
	if err != nil {
		t.Fatalf("ListEvalTasks beta: %v", err)
	}
	if len(betaRows) != 1 {
		t.Fatalf("beta: got %d tasks, want 1", len(betaRows))
	}
	if betaRows[0].ID != "eval-proj-b1" {
		t.Errorf("beta[0].ID = %q, want eval-proj-b1", betaRows[0].ID)
	}
	if betaRows[0].ProjectPath != "/projects/beta" {
		t.Errorf("beta[0].ProjectPath = %q, want /projects/beta", betaRows[0].ProjectPath)
	}

	// Empty ProjectPath returns all rows.
	allRows, err := store.ListEvalTasks(EvalTaskFilter{})
	if err != nil {
		t.Fatalf("ListEvalTasks all: %v", err)
	}
	if len(allRows) != 3 {
		t.Errorf("all: got %d tasks, want 3", len(allRows))
	}

	// ProjectPath + SuccessOnly intersection.
	alphaSuccess, err := store.ListEvalTasks(EvalTaskFilter{ProjectPath: "/projects/alpha", SuccessOnly: true})
	if err != nil {
		t.Fatalf("ListEvalTasks alpha+success: %v", err)
	}
	if len(alphaSuccess) != 1 {
		t.Fatalf("alpha+success: got %d tasks, want 1", len(alphaSuccess))
	}
	if alphaSuccess[0].ID != "eval-proj-a1" {
		t.Errorf("alpha+success[0].ID = %q, want eval-proj-a1", alphaSuccess[0].ID)
	}
}

func TestEvalTaskPassCriteriaRoundTrip(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	criteria := []PassCriteria{
		{Type: "build", Command: "go build ./...", Passed: true},
		{Type: "test", Command: "go test ./...", Passed: true},
		{Type: "lint", Command: "golangci-lint run", Passed: false},
	}
	task := &EvalTask{
		ID:           "eval-criteria",
		ExecutionID:  "exec-c",
		IssueNumber:  99,
		IssueTitle:   "Criteria test",
		Repo:         "org/repo",
		Success:      false,
		PassCriteria: criteria,
		FilesChanged: []string{"x.go", "y.go", "z.go"},
		DurationMs:   4000,
	}
	if err := store.SaveEvalTask(task); err != nil {
		t.Fatalf("SaveEvalTask: %v", err)
	}

	tasks, err := store.ListEvalTasks(EvalTaskFilter{Repo: "org/repo"})
	if err != nil {
		t.Fatalf("ListEvalTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	got := tasks[0]
	if len(got.PassCriteria) != 3 {
		t.Fatalf("expected 3 pass criteria, got %d", len(got.PassCriteria))
	}
	if got.PassCriteria[0].Command != "go build ./..." {
		t.Errorf("criteria[0].Command = %q, want %q", got.PassCriteria[0].Command, "go build ./...")
	}
	if got.PassCriteria[2].Passed {
		t.Error("criteria[2] should be failed")
	}
	if len(got.FilesChanged) != 3 {
		t.Errorf("expected 3 files, got %d", len(got.FilesChanged))
	}
}

// TestGetEvalTaskCounts verifies the fleet-wide pass/fail aggregate backing
// the pilot_eval_tasks_total / pilot_eval_pass_ratio Prometheus gauges
// (GH-4922), including the empty-table case (both counts 0, no error).
func TestGetEvalTaskCounts(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	t.Run("empty table returns zero counts", func(t *testing.T) {
		counts, err := store.GetEvalTaskCounts()
		if err != nil {
			t.Fatalf("GetEvalTaskCounts: %v", err)
		}
		if counts.Passed != 0 || counts.Failed != 0 {
			t.Errorf("expected zero counts on empty table, got %+v", counts)
		}
	})

	// Mixed pass/fail across multiple project paths — counts must be
	// fleet-wide (no ProjectPath scoping), unlike ListEvalTasks.
	tasks := []*EvalTask{
		{ID: "eval-c1", Repo: "org/repo", IssueNumber: 1, Success: true, ProjectPath: "/projects/alpha"},
		{ID: "eval-c2", Repo: "org/repo", IssueNumber: 2, Success: true, ProjectPath: "/projects/beta"},
		{ID: "eval-c3", Repo: "org/repo", IssueNumber: 3, Success: false, ProjectPath: "/projects/alpha"},
	}
	for _, task := range tasks {
		if err := store.SaveEvalTask(task); err != nil {
			t.Fatalf("SaveEvalTask(%s): %v", task.ID, err)
		}
	}

	t.Run("aggregates across all projects", func(t *testing.T) {
		counts, err := store.GetEvalTaskCounts()
		if err != nil {
			t.Fatalf("GetEvalTaskCounts: %v", err)
		}
		if counts.Passed != 2 {
			t.Errorf("Passed = %d, want 2", counts.Passed)
		}
		if counts.Failed != 1 {
			t.Errorf("Failed = %d, want 1", counts.Failed)
		}
	})
}
