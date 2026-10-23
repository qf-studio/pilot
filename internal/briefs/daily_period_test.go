package briefs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func dailyPeriodConfig() *BriefConfig {
	config := DefaultBriefConfig()
	config.Schedule = "0 18 * * *"
	config.Timezone = "UTC"
	return config
}

func recordDailySent(t *testing.T, store *memory.Store, at time.Time) {
	t.Helper()
	if err := store.RecordBriefSent(&memory.BriefRecord{SentAt: at, Channel: "telegram", BriefType: string(KindDaily)}); err != nil {
		t.Fatalf("RecordBriefSent: %v", err)
	}
}

// With an 18:00 schedule, a run completed at 15:00 today is in today's brief:
// both the delivered brief and the stored structured payload (GH-5813).
func TestGenerateDailyEnding_IncludesRunCompletedBeforeRunTime(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	end := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	recordDailySent(t, store, end.Add(-24*time.Hour))

	done := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-77", ProjectPath: "/p", Status: "completed", TaskTitle: "Ship it",
		TaskSourceAdapter: "github", CreatedAt: done.Add(-30 * time.Minute), CompletedAt: &done,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	g := NewGenerator(store, dailyPeriodConfig())
	brief, err := g.GenerateDailyEnding(end)
	if err != nil {
		t.Fatalf("GenerateDailyEnding: %v", err)
	}
	if !brief.Period.End.Equal(end) {
		t.Errorf("period end = %v, want run time %v", brief.Period.End, end)
	}
	if len(brief.Completed) != 1 || brief.Completed[0].ID != "GH-77" {
		t.Fatalf("daily brief completed = %+v, want GH-77", brief.Completed)
	}

	s := &Scheduler{generator: g, store: store, logger: defaultTestLogger()}
	s.storeStructured(brief.Period, brief.Kind)
	rows, err := store.ListBriefPayloads(5, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored payloads = %d, err %v", len(rows), err)
	}
	var stored StructuredBrief
	if err := json.Unmarshal(rows[0].Payload, &stored); err != nil {
		t.Fatalf("stored payload invalid: %v", err)
	}
	if len(stored.Shipped) != 1 || stored.Shipped[0].IssueRef != "#77" {
		t.Errorf("stored payload shipped = %+v, want #77", stored.Shipped)
	}
}

func TestGenerateDailyEnding_PeriodStart(t *testing.T) {
	end := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		lastSent  *time.Time
		wantStart time.Time
	}{
		{"no previous run falls back to 24h", nil, end.Add(-24 * time.Hour)},
		{"previous run starts the period", ptr(end.Add(-72 * time.Hour)), end.Add(-72 * time.Hour)},
		{"previous run after end keeps the 24h fallback", ptr(end.Add(time.Hour)), end.Add(-24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			if tt.lastSent != nil {
				recordDailySent(t, store, *tt.lastSent)
			}
			// Other brief types must not move the daily period.
			if err := store.RecordBriefSent(&memory.BriefRecord{SentAt: end.Add(-time.Hour), Channel: "telegram", BriefType: "weekly"}); err != nil {
				t.Fatalf("RecordBriefSent: %v", err)
			}

			brief, err := NewGenerator(store, dailyPeriodConfig()).GenerateDailyEnding(end)
			if err != nil {
				t.Fatalf("GenerateDailyEnding: %v", err)
			}
			if !brief.Period.Start.Equal(tt.wantStart) {
				t.Errorf("period start = %v, want %v", brief.Period.Start, tt.wantStart)
			}
			if !brief.Period.End.Equal(end) {
				t.Errorf("period end = %v, want %v", brief.Period.End, end)
			}
		})
	}
}

// A missed slot is caught up with a brief that starts at the last run time.
func TestSchedulerCatchUp_PeriodStartsAtLastRun(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	lastRun := time.Now().Add(-50 * time.Hour).UTC().Truncate(time.Second)
	recordDailySent(t, store, lastRun)

	config := dailyPeriodConfig()
	g := NewGenerator(store, config)
	s := NewScheduler(g, NewDeliveryService(config), config, defaultTestLogger(), store)

	var got *Brief
	s.catchUp(config.Schedule, string(KindDaily), 48*time.Hour, false, func() {
		brief, err := g.GenerateDaily()
		if err != nil {
			t.Errorf("GenerateDaily: %v", err)
			return
		}
		got = brief
	})
	if got == nil {
		t.Fatal("catch-up did not fire for a missed slot")
	}
	if !got.Period.Start.Equal(lastRun) {
		t.Errorf("catch-up period start = %v, want last run %v", got.Period.Start, lastRun)
	}
}

func TestGetLastBriefSentAny(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if rec, err := store.GetLastBriefSentAny(string(KindDaily)); err != nil || rec != nil {
		t.Fatalf("empty table = %+v, %v; want nil, nil", rec, err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	for _, r := range []memory.BriefRecord{
		{SentAt: base.Add(-3 * time.Hour), Channel: "telegram", BriefType: "daily"},
		{SentAt: base.Add(-time.Hour), Channel: "slack", BriefType: "daily"},
		{SentAt: base, Channel: "telegram", BriefType: "weekly"},
	} {
		if err := store.RecordBriefSent(&r); err != nil {
			t.Fatalf("RecordBriefSent: %v", err)
		}
	}
	rec, err := store.GetLastBriefSentAny("daily")
	if err != nil || rec == nil {
		t.Fatalf("GetLastBriefSentAny = %+v, %v", rec, err)
	}
	if rec.Channel != "slack" || !rec.SentAt.Equal(base.Add(-time.Hour)) {
		t.Errorf("got %+v, want the slack daily row", rec)
	}
}
