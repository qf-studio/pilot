package autopilot

import (
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestRefreshDailyTicketCapDispatchedToday (GH-5808): the gauge counts distinct
// tickets in the org-local day, not the UTC day, and ignores chat-filed rows.
func TestRefreshDailyTicketCapDispatchedToday(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	for _, e := range []*memory.Execution{
		// 21:30Z Oct 22 = 23:30 Berlin Oct 22 (yesterday in Berlin at `now`).
		{ID: "e1", TaskID: "T1", ProjectPath: "/p", Status: "completed", CreatedAt: time.Date(2026, 10, 22, 21, 30, 0, 0, time.UTC)},
		// 22:30Z Oct 22 = 00:30 Berlin Oct 23.
		{ID: "e2", TaskID: "T2", ProjectPath: "/p", Status: "completed", CreatedAt: time.Date(2026, 10, 22, 22, 30, 0, 0, time.UTC)},
		{ID: "e3", TaskID: "T2", ProjectPath: "/p", Status: "completed", CreatedAt: time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)},
		{ID: "e4", TaskID: "TG-1", ProjectPath: "/p", Status: "completed", CreatedAt: time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC), NotTicket: true},
	} {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}

	now := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC) // 01:30 Berlin Oct 23
	tests := []struct {
		name string
		loc  *time.Location
		want int
	}{
		{"Berlin day", berlin, 1},
		{"UTC day", time.UTC, 2}, // T1 and T2 (two generations count once); chat excluded
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMetrics()
			if err := RefreshDailyTicketCapDispatchedToday(store, m, tt.loc, now); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if got := m.Snapshot().DailyTicketCapDispatchedToday; got != tt.want {
				t.Errorf("gauge = %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("nil args are a no-op", func(t *testing.T) {
		if err := RefreshDailyTicketCapDispatchedToday(nil, NewMetrics(), time.UTC, now); err != nil {
			t.Errorf("nil store: %v", err)
		}
		if err := RefreshDailyTicketCapDispatchedToday(store, nil, time.UTC, now); err != nil {
			t.Errorf("nil metrics: %v", err)
		}
	})
}
