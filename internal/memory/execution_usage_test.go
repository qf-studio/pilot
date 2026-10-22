package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestNormalizeKeySource(t *testing.T) {
	for in, want := range map[string]string{
		"platform": "platform", "own": "own", "": "unknown", "byok": "unknown", "Platform": "unknown",
	} {
		if got := NormalizeKeySource(in); got != want {
			t.Errorf("NormalizeKeySource(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExecutionKeySourceAndReportedCostRoundTrip: key_source is written at
// insert, reported_cost_usd by SaveExecutionMetrics; nil stays NULL; free
// text never reaches the column (GH-5676).
func TestExecutionKeySourceAndReportedCostRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, e := range []*Execution{
		{ID: "own-1", TaskID: "t1", ProjectPath: "/p", Status: "running", KeySource: "own"},
		{ID: "junk-1", TaskID: "t2", ProjectPath: "/p", Status: "running", KeySource: "sk-whatever"},
		{ID: "none-1", TaskID: "t3", ProjectPath: "/p", Status: "running"},
	} {
		if err := s.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}
	cost := 1.25
	if err := s.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "own-1", ReportedCostUSD: &cost, EstimatedCostUSD: 2}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}
	if err := s.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "none-1"}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	for id, want := range map[string]string{"own-1": "own", "junk-1": "unknown", "none-1": "unknown"} {
		got, err := s.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if got.KeySource != want {
			t.Errorf("%s KeySource = %q, want %q", id, got.KeySource, want)
		}
	}
	got, _ := s.GetExecution("own-1")
	if got.ReportedCostUSD == nil || *got.ReportedCostUSD != 1.25 || got.EstimatedCostUSD != 2 {
		t.Errorf("own-1 reported=%v estimated=%v, want 1.25 / 2", got.ReportedCostUSD, got.EstimatedCostUSD)
	}
	got, _ = s.GetExecution("none-1")
	if got.ReportedCostUSD != nil {
		t.Errorf("none-1 ReportedCostUSD = %v, want nil", *got.ReportedCostUSD)
	}
}

func TestGetExecutionUsageSince_CapCursorAndScope(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	n := MaxExecutionUsageRows + 5
	for i := 0; i < n; i++ {
		done := base.Add(time.Duration(i) * time.Second)
		proj := "/a"
		if i%2 == 1 {
			proj = "/b"
		}
		if err := s.SaveExecution(&Execution{
			ID: fmt.Sprintf("e-%05d", i), TaskID: fmt.Sprintf("t-%d", i), ProjectPath: proj,
			Status: "completed", CreatedAt: done, CompletedAt: &done,
		}); err != nil {
			t.Fatalf("SaveExecution %d: %v", i, err)
		}
	}

	rows, hasMore, err := s.GetExecutionUsageSince(base, "")
	if err != nil {
		t.Fatalf("GetExecutionUsageSince: %v", err)
	}
	if len(rows) != MaxExecutionUsageRows || !hasMore {
		t.Fatalf("got %d rows hasMore=%v, want %d / true", len(rows), hasMore, MaxExecutionUsageRows)
	}
	// Newest first within the page, and the page is the OLDEST rows (no gap on resume).
	for i := 1; i < len(rows); i++ {
		if rows[i].CompletedAt.After(rows[i-1].CompletedAt) {
			t.Fatalf("rows not newest-first at %d", i)
		}
	}
	if rows[len(rows)-1].ExecutionID != "e-00000" || rows[0].ExecutionID != fmt.Sprintf("e-%05d", MaxExecutionUsageRows-1) {
		t.Errorf("page = %s..%s, want the oldest %d rows", rows[len(rows)-1].ExecutionID, rows[0].ExecutionID, MaxExecutionUsageRows)
	}

	// Resume from the page's newest completed_at: boundary row repeats, the rest follow.
	next, hasMore, err := s.GetExecutionUsageSince(rows[0].CompletedAt, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if hasMore || len(next) != n-MaxExecutionUsageRows+1 {
		t.Errorf("resume got %d rows hasMore=%v, want %d / false", len(next), hasMore, n-MaxExecutionUsageRows+1)
	}
	if next[len(next)-1].ExecutionID != rows[0].ExecutionID {
		t.Errorf("resume's oldest row = %s, want boundary row %s", next[len(next)-1].ExecutionID, rows[0].ExecutionID)
	}

	// Project scoping.
	scoped, _, err := s.GetExecutionUsageSince(base, "/b")
	if err != nil {
		t.Fatalf("scoped: %v", err)
	}
	for _, r := range scoped {
		if r.ProjectPath != "/b" {
			t.Fatalf("scoped result leaked project %s", r.ProjectPath)
		}
	}
	if len(scoped) != n/2 {
		t.Errorf("scoped got %d rows, want %d", len(scoped), n/2)
	}

	// Window in the future is empty.
	if none, more, err := s.GetExecutionUsageSince(time.Now().Add(time.Hour), ""); err != nil || len(none) != 0 || more {
		t.Errorf("future since: rows=%d more=%v err=%v, want empty", len(none), more, err)
	}
}

func TestLifetimeCounterBaselines_CostByKeySource(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	seed := func(id, key string, cost float64) {
		if err := s.SaveExecution(&Execution{ID: id, TaskID: id, ProjectPath: "/p", Status: "completed", KeySource: key}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := s.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: id, TokensInput: 10, TokensTotal: 10, EstimatedCostUSD: cost, ModelName: "m1"}); err != nil {
			t.Fatalf("SaveExecutionMetrics: %v", err)
		}
	}
	seed("a", "platform", 1)
	seed("b", "platform", 2)
	seed("c", "own", 4)
	seed("d", "", 8) // pre-GH-5676 row: no key_source

	b, err := s.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines: %v", err)
	}
	want := map[ModelKeySourceKey]float64{
		{Model: "m1", KeySource: "platform"}: 3,
		{Model: "m1", KeySource: "own"}:      4,
		{Model: "m1", KeySource: "unknown"}:  8,
	}
	for k, w := range want {
		if got := b.CostByModelKeySource[k]; got != w {
			t.Errorf("CostByModelKeySource[%+v] = %v, want %v", k, got, w)
		}
	}
	if got := b.CostByModel["m1"]; got != 15 {
		t.Errorf("CostByModel[m1] = %v, want 15 (unchanged total)", got)
	}
}

func TestCountDistinctTasksInWindow(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// Berlin spring-forward 2026-03-29: that local day is 23h long
	// (UTC 2026-03-28T23:00Z .. 2026-03-29T22:00Z).
	start := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	end := time.Date(2026, 3, 30, 0, 0, 0, 0, loc)
	if got := end.Sub(start); got != 23*time.Hour {
		t.Fatalf("test premise: DST day = %v, want 23h", got)
	}

	save := func(id, task string, at time.Time, canary bool) {
		t.Helper()
		if err := s.SaveExecution(&Execution{
			ID: id, TaskID: task, ProjectPath: "/p", Status: "completed", CreatedAt: at, IsCanary: canary,
		}); err != nil {
			t.Fatalf("SaveExecution(%s): %v", id, err)
		}
	}

	// Two generations of GH-1 count once.
	save("gen0", "GH-1", start.Add(time.Hour), false)
	save("gen1", "GH-1", start.Add(2*time.Hour), false)
	save("other", "GH-2", start.Add(3*time.Hour), false)
	// Inclusive start, exclusive end.
	save("at-start", "GH-3", start, false)
	save("at-end", "GH-4", end, false)
	// 23:30 Berlin on the 29th is inside the DST day.
	save("late", "GH-5", end.Add(-30*time.Minute), false)
	save("just-before", "GH-6", start.Add(-time.Minute), false)
	// start+23h30m is past the DST day's end (a naive start+24h window would include it).
	save("naive-24h", "GH-7", start.Add(23*time.Hour+30*time.Minute), false)
	save("canary", "GH-8", start.Add(time.Hour), true)

	// Chat turns are a separate table and never count.
	done := start.Add(time.Hour)
	if err := s.SaveChatTurn(chatRow("chat-1", "/p", "platform", 0.1, 1, 1, done)); err != nil {
		t.Fatalf("SaveChatTurn: %v", err)
	}

	got, err := s.CountDistinctTasksInWindow(start, end)
	if err != nil {
		t.Fatalf("CountDistinctTasksInWindow: %v", err)
	}
	// GH-1, GH-2, GH-3, GH-5 (GH-4 at end, GH-6 before, GH-7 after, GH-8 canary excluded).
	if got != 4 {
		t.Errorf("distinct tasks = %d, want 4", got)
	}

	// The day after the transition holds GH-4 and GH-7 only.
	next, err := s.CountDistinctTasksInWindow(end, time.Date(2026, 3, 31, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("next day: %v", err)
	}
	if next != 2 {
		t.Errorf("next-day distinct tasks = %d, want 2", next)
	}

	empty, err := s.CountDistinctTasksInWindow(end.AddDate(1, 0, 0), end.AddDate(1, 0, 1))
	if err != nil || empty != 0 {
		t.Errorf("empty window = %d, %v, want 0, nil", empty, err)
	}
}
