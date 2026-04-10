package main

import (
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// =============================================================================
// GH-2134: formatDuration / formatDurationShort tests
// =============================================================================

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero", 0, "0s"},
		{"sub-minute", 45_000, "45.0s"},
		{"one minute", 60_000, "1.0m"},
		{"sub-hour", 300_000, "5.0m"},
		{"one hour", 3_600_000, "1.0h"},
		{"two hours", 7_200_000, "2.0h"},
		{"half second", 500, "0.5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.ms)
			if got != tt.want {
				t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero", 0, "0s"},
		{"sub-minute", 45_000, "45s"},
		{"one minute", 60_000, "1m"},
		{"sub-hour", 300_000, "5m"},
		{"one hour", 3_600_000, "1.0h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationShort(tt.ms)
			if got != tt.want {
				t.Errorf("formatDurationShort(%d) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: formatTokens / formatTokensShort tests
// =============================================================================

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		name   string
		tokens int64
		want   string
	}{
		{"zero", 0, "0"},
		{"small", 500, "500"},
		{"thousands", 1_500, "1.5K"},
		{"millions", 2_500_000, "2.50M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokens(tt.tokens)
			if got != tt.want {
				t.Errorf("formatTokens(%d) = %q, want %q", tt.tokens, got, tt.want)
			}
		})
	}
}

func TestFormatTokensShort(t *testing.T) {
	tests := []struct {
		name   string
		tokens int64
		want   string
	}{
		{"zero", 0, "0"},
		{"small", 999, "999"},
		{"thousands", 2_500, "2K"},
		{"millions", 1_500_000, "1.5M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokensShort(tt.tokens)
			if got != tt.want {
				t.Errorf("formatTokensShort(%d) = %q, want %q", tt.tokens, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: lastN / extractHostFromURL tests
// =============================================================================

func TestLastN(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"shorter than n", "abc", 5, "abc"},
		{"exact length", "abc", 3, "abc"},
		{"longer than n", "abcdef", 3, "def"},
		{"empty", "", 3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastN(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("lastN(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}

func TestExtractHostFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"https", "https://example.com/path/to/resource", "example.com"},
		{"http", "http://example.com/path", "example.com"},
		{"no scheme", "example.com/path", "example.com"},
		{"with port", "https://example.com:8080/path", "example.com:8080"},
		{"bare host", "example.com", "example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHostFromURL(tt.url)
			if got != tt.want {
				t.Errorf("extractHostFromURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: progressBar tests
// =============================================================================

func TestProgressBar(t *testing.T) {
	tests := []struct {
		name  string
		pct   int
		width int
		want  string
	}{
		{"0%", 0, 10, "[░░░░░░░░░░]"},
		{"50%", 50, 10, "[█████░░░░░]"},
		{"100%", 100, 10, "[██████████]"},
		{"25% width 4", 25, 4, "[█░░░]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := progressBar(tt.pct, tt.width)
			if got != tt.want {
				t.Errorf("progressBar(%d, %d) = %q, want %q", tt.pct, tt.width, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: parseInt64 tests
// =============================================================================

func TestParseInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"valid positive", "123", 123, false},
		{"valid negative", "-42", -42, false},
		{"zero", "0", 0, false},
		{"invalid", "abc", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInt64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseInt64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseInt64(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: evalPassRate tests
// =============================================================================

func TestEvalPassRate_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*memory.EvalTask
		want  float64
	}{
		{"empty", nil, 0},
		{"all pass", []*memory.EvalTask{{Success: true}, {Success: true}}, 100},
		{"all fail", []*memory.EvalTask{{Success: false}, {Success: false}}, 0},
		{"half pass", []*memory.EvalTask{{Success: true}, {Success: false}}, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evalPassRate(tt.tasks)
			if got != tt.want {
				t.Errorf("evalPassRate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: convertTaskStatesToDisplay tests
// =============================================================================

func TestConvertTaskStatesToDisplay(t *testing.T) {
	now := time.Now().Add(-5 * time.Minute)

	states := []*executor.TaskState{
		{ID: "GH-1", Title: "Fix bug", Status: executor.StatusRunning, Phase: "impl", Progress: 50, StartedAt: &now},
		{ID: "GH-2", Title: "Add feature", Status: executor.StatusQueued},
		{ID: "GH-3", Title: "Done task", Status: executor.StatusCompleted, IssueURL: "https://github.com/org/repo/issues/3"},
		{ID: "GH-4", Title: "Failed task", Status: executor.StatusFailed},
	}

	displays := convertTaskStatesToDisplay(states)

	if len(displays) != 4 {
		t.Fatalf("expected 4 displays, got %d", len(displays))
	}

	// Check status mapping
	expected := []struct {
		id     string
		status string
	}{
		{"GH-1", "running"},
		{"GH-2", "queued"},
		{"GH-3", "done"},
		{"GH-4", "failed"},
	}

	for i, exp := range expected {
		if displays[i].ID != exp.id {
			t.Errorf("display[%d].ID = %q, want %q", i, displays[i].ID, exp.id)
		}
		if displays[i].Status != exp.status {
			t.Errorf("display[%d].Status = %q, want %q", i, displays[i].Status, exp.status)
		}
	}

	// Running task should have duration
	if displays[0].Duration == "" {
		t.Error("expected non-empty duration for running task")
	}

	// Queued task should have empty duration (no StartedAt)
	if displays[1].Duration != "" {
		t.Errorf("expected empty duration for queued task, got %q", displays[1].Duration)
	}

	// Issue URL preserved
	if displays[2].IssueURL != "https://github.com/org/repo/issues/3" {
		t.Errorf("display[2].IssueURL = %q", displays[2].IssueURL)
	}
}

func TestConvertTaskStatesToDisplay_Dedup(t *testing.T) {
	// GH-1220: Duplicate task IDs should be deduplicated
	states := []*executor.TaskState{
		{ID: "GH-1", Title: "First", Status: executor.StatusRunning},
		{ID: "GH-1", Title: "Duplicate", Status: executor.StatusQueued},
		{ID: "GH-2", Title: "Second", Status: executor.StatusCompleted},
	}

	displays := convertTaskStatesToDisplay(states)

	if len(displays) != 2 {
		t.Fatalf("expected 2 displays after dedup, got %d", len(displays))
	}
	if displays[0].Title != "First" {
		t.Errorf("expected first occurrence to win, got %q", displays[0].Title)
	}
	if displays[1].ID != "GH-2" {
		t.Errorf("expected second unique task, got %q", displays[1].ID)
	}
}

func TestConvertTaskStatesToDisplay_DefaultStatus(t *testing.T) {
	// Unknown status should map to "pending"
	states := []*executor.TaskState{
		{ID: "GH-1", Title: "Unknown", Status: executor.TaskStatus("unknown")},
	}

	displays := convertTaskStatesToDisplay(states)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display, got %d", len(displays))
	}
	if displays[0].Status != "pending" {
		t.Errorf("expected 'pending' for unknown status, got %q", displays[0].Status)
	}
}

func TestConvertTaskStatesToDisplay_Empty(t *testing.T) {
	displays := convertTaskStatesToDisplay(nil)
	if len(displays) != 0 {
		t.Errorf("expected 0 displays for nil input, got %d", len(displays))
	}
}

// Compile-time check that HandlerDeps and IssueInfo structs are usable from tests
func TestHandlerDeps_StructLayout(t *testing.T) {
	deps := HandlerDeps{
		ProjectPath: "/tmp/test",
	}
	if deps.ProjectPath != "/tmp/test" {
		t.Errorf("unexpected ProjectPath")
	}
}

func TestIssueInfo_StructLayout(t *testing.T) {
	info := IssueInfo{
		TaskID:  "GH-1",
		Title:   "Test",
		Adapter: "github",
	}
	if info.TaskID != "GH-1" {
		t.Errorf("unexpected TaskID")
	}
}

// Verify TaskDisplay fields are compatible with our output
func TestConvertTaskStatesToDisplay_FieldMapping(t *testing.T) {
	now := time.Now()
	states := []*executor.TaskState{
		{
			ID:          "GH-5",
			Title:       "Test task",
			Status:      executor.StatusRunning,
			Phase:       "verify",
			Progress:    80,
			IssueURL:    "https://github.com/org/repo/issues/5",
			PRUrl:       "https://github.com/org/repo/pull/10",
			StartedAt:   &now,
			ProjectPath: "/home/user/projects/pilot",
			ProjectName: "pilot",
		},
	}

	displays := convertTaskStatesToDisplay(states)
	d := displays[0]

	// Verify all fields map correctly
	want := dashboard.TaskDisplay{
		ID:          "GH-5",
		Title:       "Test task",
		Status:      "running",
		Phase:       "verify",
		Progress:    80,
		IssueURL:    "https://github.com/org/repo/issues/5",
		PRURL:       "https://github.com/org/repo/pull/10",
		ProjectPath: "/home/user/projects/pilot",
		ProjectName: "pilot",
	}

	if d.ID != want.ID {
		t.Errorf("ID = %q, want %q", d.ID, want.ID)
	}
	if d.Title != want.Title {
		t.Errorf("Title = %q, want %q", d.Title, want.Title)
	}
	if d.Status != want.Status {
		t.Errorf("Status = %q, want %q", d.Status, want.Status)
	}
	if d.Phase != want.Phase {
		t.Errorf("Phase = %q, want %q", d.Phase, want.Phase)
	}
	if d.Progress != want.Progress {
		t.Errorf("Progress = %d, want %d", d.Progress, want.Progress)
	}
	if d.IssueURL != want.IssueURL {
		t.Errorf("IssueURL = %q, want %q", d.IssueURL, want.IssueURL)
	}
	if d.PRURL != want.PRURL {
		t.Errorf("PRURL = %q, want %q", d.PRURL, want.PRURL)
	}
	if d.ProjectPath != want.ProjectPath {
		t.Errorf("ProjectPath = %q, want %q", d.ProjectPath, want.ProjectPath)
	}
	if d.ProjectName != want.ProjectName {
		t.Errorf("ProjectName = %q, want %q", d.ProjectName, want.ProjectName)
	}
}
