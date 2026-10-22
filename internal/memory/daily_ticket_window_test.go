package memory

import (
	"testing"
	"time"
)

func TestLocalDayWindow(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	tests := []struct {
		name      string
		now       time.Time
		loc       *time.Location
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "UTC day",
			now:       time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC),
			loc:       time.UTC,
			wantStart: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "nil location is UTC",
			now:       time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
			loc:       nil,
			wantStart: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "Berlin day differs from UTC day",
			now:       time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC), // 01:30 CEST Oct 23
			loc:       berlin,
			wantStart: time.Date(2026, 10, 22, 22, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC),
		},
		{
			name:      "Berlin spring-forward day is 23h",
			now:       time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
			loc:       berlin,
			wantStart: time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC),
		},
		{
			name:      "Berlin fall-back day is 25h",
			now:       time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
			loc:       berlin,
			wantStart: time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := LocalDayWindow(tt.now, tt.loc)
			if !start.Equal(tt.wantStart) || !end.Equal(tt.wantEnd) {
				t.Errorf("LocalDayWindow = [%s, %s), want [%s, %s)", start.UTC(), end.UTC(), tt.wantStart, tt.wantEnd)
			}
		})
	}
}

// TestNotTicketExecutionsAreNotCounted: chat-filed tasks and decomposed
// subtasks write an executions row but are not tickets (GH-5808).
func TestNotTicketExecutionsAreNotCounted(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	at := start.Add(time.Hour)

	for _, e := range []*Execution{
		{ID: "ticket", TaskID: "GH-1", ProjectPath: "/p", Status: "completed", CreatedAt: at},
		{ID: "chat", TaskID: "TG-1", ProjectPath: "/p", Status: "completed", CreatedAt: at, NotTicket: true},
		{ID: "sub", TaskID: "GH-1-1", ProjectPath: "/p", Status: "completed", CreatedAt: at, NotTicket: true},
	} {
		if err := s.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}

	if n, err := s.CountDistinctTasksInWindow(start, end); err != nil || n != 1 {
		t.Errorf("CountDistinctTasksInWindow = %d, %v, want 1", n, err)
	}
	for taskID, want := range map[string]bool{"GH-1": true, "TG-1": false, "GH-1-1": false, "GH-9": false} {
		got, err := s.HasCountedTaskInWindow(taskID, start, end)
		if err != nil || got != want {
			t.Errorf("HasCountedTaskInWindow(%s) = %v, %v, want %v", taskID, got, err, want)
		}
	}
	// The same task outside the window is not counted for that window.
	if got, err := s.HasCountedTaskInWindow("GH-1", end, end.AddDate(0, 0, 1)); err != nil || got {
		t.Errorf("HasCountedTaskInWindow(next day) = %v, %v, want false", got, err)
	}
}
