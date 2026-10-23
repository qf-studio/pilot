package briefs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/robfig/cron/v3"
)

func TestWeeklySchedule(t *testing.T) {
	tests := []struct {
		name    string
		daily   string
		day     string
		want    string
		wantErr bool
	}{
		{"default day is monday", "0 9 * * 1-5", "", "0 9 * * 1", false},
		{"explicit monday", "0 9 * * 1-5", "Monday", "0 9 * * 1", false},
		{"keeps minute and hour", "30 8 * * *", "friday", "30 8 * * 5", false},
		{"sunday is 0", "0 9 * * *", "sunday", "0 9 * * 0", false},
		{"descriptor schedule", "@daily", "", "", true},
		{"unknown day", "0 9 * * *", "someday", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WeeklySchedule(tt.daily, tt.day)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The weekly spec must fire on Monday only, at the daily-brief hour in the
// tenant's timezone — including across a DST change.
func TestWeeklySchedule_FiresOnMondayOnly(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := WeeklySchedule("0 9 * * 1-5", "")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		t.Fatal(err)
	}

	// 5 weeks from Sun 2026-10-25 spans the US DST end on 2026-11-01.
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	until := from.AddDate(0, 0, 35)
	var fires []time.Time
	for next := sched.Next(from); next.Before(until); next = sched.Next(next) {
		fires = append(fires, next.In(loc))
	}

	if len(fires) != 5 {
		t.Fatalf("fired %d times in 5 weeks, want 5: %v", len(fires), fires)
	}
	for _, f := range fires {
		if f.Weekday() != time.Monday || f.Hour() != 9 || f.Minute() != 0 {
			t.Errorf("fired at %v, want Monday 09:00 local", f)
		}
	}
}

func TestSchedulerWeeklyNextRun(t *testing.T) {
	tests := []struct {
		day  string
		want time.Weekday
	}{
		{"", time.Monday},
		{"thursday", time.Thursday},
	}
	for _, tt := range tests {
		t.Run("weekly_day="+tt.day, func(t *testing.T) {
			cfg := &BriefConfig{Enabled: true, Schedule: "0 9 * * 1-5", Timezone: "Europe/Berlin", WeeklyDay: tt.day}
			s := NewScheduler(NewGenerator(nil, cfg), NewDeliveryService(cfg), cfg, nil, nil)
			if !s.WeeklyNextRun().IsZero() {
				t.Error("WeeklyNextRun before Start should be zero")
			}
			if err := s.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Stop()

			loc, _ := time.LoadLocation("Europe/Berlin")
			next := s.WeeklyNextRun().In(loc)
			if next.Weekday() != tt.want || next.Hour() != 9 {
				t.Errorf("next weekly run = %v, want %v 09:00 Berlin", next, tt.want)
			}
			if s.NextRun().IsZero() {
				t.Error("daily brief must stay scheduled")
			}
		})
	}
}

func TestSchedulerStart_UnderivableWeeklySkipsOnlyWeekly(t *testing.T) {
	cfg := &BriefConfig{Enabled: true, Schedule: "@daily", Timezone: "UTC"}
	s := NewScheduler(NewGenerator(nil, cfg), NewDeliveryService(cfg), cfg, nil, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	if s.NextRun().IsZero() {
		t.Error("daily brief should still be scheduled")
	}
	if !s.WeeklyNextRun().IsZero() {
		t.Error("weekly brief should not be scheduled")
	}
}

func execAt(id, task, status, pr string, created time.Time, completed *time.Time, added, removed int) *memory.Execution {
	return &memory.Execution{
		ID: id, TaskID: task, Status: status, PRUrl: pr, TaskTitle: "Title " + task,
		CreatedAt: created, CompletedAt: completed, LinesAdded: added, LinesRemoved: removed,
		KeySource: "platform",
	}
}

func tp(t time.Time) *time.Time { return &t }

func addEvent(t *testing.T, store *memory.Store, execID string, stage memory.Stage, at time.Time) {
	t.Helper()
	if _, err := store.DB().Exec(
		`INSERT INTO execution_events (execution_id, stage, occurred_at, detail) VALUES (?, ?, ?, '')`,
		execID, string(stage), at.UTC()); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

var weekEnd = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) // a Monday

// seedWeek writes the fixture ledger for the 7 days ending weekEnd.
func seedWeek(t *testing.T, store *memory.Store) {
	t.Helper()
	d := func(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, time.UTC) }

	execs := []*memory.Execution{
		// 10 min to PR (pr_created event), merged.
		execAt("a", "GH-1", "completed", "https://x/pull/1", d(13, 10, 0), tp(d(13, 10, 15)), 100, 20),
		// 30 min to PR (pr_created event), merged.
		execAt("b", "GH-2", "completed", "https://x/pull/2", d(14, 10, 0), tp(d(14, 10, 40)), 10, 5),
		// No pr_created event: falls back to completion, 20 min. Not merged.
		execAt("c", "GH-3", "completed", "https://x/pull/3", d(15, 9, 0), tp(d(15, 9, 20)), 500, 0),
		// Shipped without a PR: counts as shipped, not in the median.
		execAt("d", "GH-4", "completed", "", d(16, 9, 0), tp(d(16, 9, 5)), 1, 1),
		// Failed: not shipped.
		execAt("e", "GH-5", "failed", "", d(16, 11, 0), tp(d(16, 11, 5)), 0, 0),
		// Shipped the week before, merged this week: merged_count only.
		execAt("f", "GH-6", "completed", "https://x/pull/6", d(5, 9, 0), tp(d(5, 9, 30)), 999, 0),
		// Canary: ignored everywhere.
		execAt("g", "GH-7", "completed", "https://x/pull/7", d(17, 9, 0), tp(d(17, 9, 10)), 999, 0),
	}
	execs[6].IsCanary = true
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	addEvent(t, store, "a", memory.StagePRCreated, d(13, 10, 10))
	addEvent(t, store, "b", memory.StagePRCreated, d(14, 10, 30))
	addEvent(t, store, "a", memory.StageMerged, d(13, 11, 0))
	addEvent(t, store, "b", memory.StageMerged, d(15, 8, 0))
	addEvent(t, store, "b", memory.StageMerged, d(15, 8, 1)) // duplicate event, one PR
	addEvent(t, store, "f", memory.StageMerged, d(14, 8, 0))
	addEvent(t, store, "g", memory.StageMerged, d(14, 9, 0))  // canary
	addEvent(t, store, "c", memory.StageMerged, d(20, 8, 0))  // after the window
	addEvent(t, store, "d", memory.StageMerged, d(11, 23, 0)) // before the window
}

func weeklyGenerator(store *memory.Store) *Generator {
	return NewGenerator(store, &BriefConfig{Content: ContentConfig{MaxItemsPerSection: 10}})
}

func TestGenerateWeeklyEnding_ProofNumbersFromFixtureLedger(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()
	seedWeek(t, store)

	brief, err := weeklyGenerator(store).GenerateWeeklyEnding(weekEnd)
	if err != nil {
		t.Fatalf("GenerateWeeklyEnding: %v", err)
	}
	if brief.Kind != KindWeekly || brief.Weekly == nil {
		t.Fatalf("kind = %q weekly = %v, want weekly with proof", brief.Kind, brief.Weekly)
	}
	w := brief.Weekly
	if w.TicketsShipped != 4 {
		t.Errorf("tickets_shipped = %d, want 4", w.TicketsShipped)
	}
	if w.MedianTimeToPRMinutes != 20 {
		t.Errorf("median_time_to_pr_minutes = %v, want 20", w.MedianTimeToPRMinutes)
	}
	if w.MergedCount != 3 {
		t.Errorf("merged_count = %d, want 3", w.MergedCount)
	}
	var top []string
	for _, r := range w.TopShipped {
		top = append(top, r.Title)
	}
	if got, want := strings.Join(top, ","), "Title GH-3,Title GH-1,Title GH-2"; got != want {
		t.Errorf("top_shipped = %s, want %s", got, want)
	}
}

func TestGenerateWeeklyEnding_EmptyWeekIsZerosNotError(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	brief, err := weeklyGenerator(store).GenerateWeeklyEnding(weekEnd)
	if err != nil {
		t.Fatalf("empty week must not error: %v", err)
	}
	w := brief.Weekly
	if w == nil || w.TicketsShipped != 0 || w.MedianTimeToPRMinutes != 0 || w.MergedCount != 0 || len(w.TopShipped) != 0 {
		t.Errorf("empty week proof = %+v, want zeros", w)
	}

	sb, err := weeklyGenerator(store).GenerateWeeklyStructured(BriefPeriod{Start: weekEnd.Add(-7 * 24 * time.Hour), End: weekEnd})
	if err != nil {
		t.Fatalf("structured empty week: %v", err)
	}
	raw, err := sb.MarshalPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"top_shipped":[]`) || !strings.Contains(string(raw), `"tickets_shipped":0`) {
		t.Errorf("empty week payload should carry zeros and []: %s", raw)
	}
}

func TestBuildWeeklyProof_MedianEvenCount(t *testing.T) {
	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	mk := func(id string, mins int) *memory.Execution {
		return &memory.Execution{ID: id, TaskID: id, Status: "completed", PRUrl: "https://x/" + id,
			CreatedAt: start, CompletedAt: tp(start.Add(time.Duration(mins) * time.Minute))}
	}
	in := &memory.WeeklyProofInputs{
		Shipped:     []*memory.Execution{mk("a", 10), mk("b", 40), mk("c", 20), mk("d", 31)},
		PRCreatedAt: map[string]time.Time{},
	}
	if got := BuildWeeklyProof(in, nil).MedianTimeToPRMinutes; got != 25.5 {
		t.Errorf("median = %v, want 25.5", got)
	}
}

func TestSchedulerRunWeeklyNow_StoresWeeklyPayload(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()
	seedWeek(t, store)

	cfg := &BriefConfig{Enabled: true, Schedule: "0 9 * * *", Timezone: "UTC", Content: ContentConfig{MaxItemsPerSection: 10}}
	s := NewScheduler(NewGenerator(store, cfg), NewDeliveryService(cfg), cfg, nil, store)

	if _, err := s.RunWeeklyNow(context.Background()); err != nil {
		t.Fatalf("RunWeeklyNow: %v", err)
	}
	rows, err := store.ListBriefPayloads(1, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("payloads = %d, err %v", len(rows), err)
	}
	if rows[0].Kind != "weekly" {
		t.Errorf("stored row Kind = %q, want weekly", rows[0].Kind)
	}
	var got struct {
		Kind   string `json:"kind"`
		Weekly *struct {
			TicketsShipped int `json:"tickets_shipped"`
		} `json:"weekly"`
	}
	if err := json.Unmarshal(rows[0].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "weekly" || got.Weekly == nil {
		t.Errorf("payload kind = %q weekly = %v, want weekly with proof: %s", got.Kind, got.Weekly, rows[0].Payload)
	}
}

func TestDailyPayloadKind(t *testing.T) {
	b := BuildStructured(structuredNow, BriefPeriod{}, nil, nil, nil)
	raw, err := b.MarshalPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"daily"`) || strings.Contains(string(raw), `"weekly"`) {
		t.Errorf("daily payload should be kind=daily with no weekly block: %s", raw)
	}
}

func TestEmailFormatter_WeeklyKindMarker(t *testing.T) {
	proof := &WeeklyProof{TicketsShipped: 4, MedianTimeToPRMinutes: 20, MergedCount: 3,
		TopShipped: []ShippedRun{{IssueRef: "#3", Title: "Big one", LinesAdded: 500}}}
	f := NewEmailFormatter()

	brief := &Brief{Kind: KindWeekly, Weekly: proof, GeneratedAt: structuredNow}
	out, err := f.Format(brief)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`content="weekly"`, "Pilot Weekly Brief", "<strong>4</strong> tickets shipped", "<strong>3</strong> PRs merged", "Big one"} {
		if !strings.Contains(out, want) {
			t.Errorf("weekly email missing %q", want)
		}
	}
	if !strings.HasPrefix(f.Subject(brief), "Pilot Weekly Brief") {
		t.Errorf("subject = %q", f.Subject(brief))
	}

	sb := BuildStructured(structuredNow, BriefPeriod{}, nil, nil, nil)
	sb.Kind, sb.Weekly = KindWeekly, proof
	out, err = f.FormatStructured(sb)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `content="weekly"`) || !strings.Contains(out, "tickets shipped") {
		t.Errorf("structured weekly email missing marker/proof:\n%s", out)
	}

	daily, _ := f.Format(&Brief{GeneratedAt: structuredNow})
	if !strings.Contains(daily, `content="daily"`) || strings.Contains(daily, "Last 7 days") {
		t.Error("daily email should be kind=daily without the weekly block")
	}
}
