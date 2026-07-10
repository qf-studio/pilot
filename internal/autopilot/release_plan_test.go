package autopilot

import (
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// TestReleasePlanMessage is the GH-4164 table-driven test covering the three
// release-trigger ack-card variants (on_merge, on_schedule, disabled/absent)
// plus an invalid-cron edge case that must fall back to safe phrasing rather
// than propagating a parse error into the approval card.
func TestReleasePlanMessage(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		rel  *ReleaseConfig
		want string
	}{
		{
			name: "nil release config — disabled/absent",
			rel:  nil,
			want: "No releaser configured for this repo (merge only).",
		},
		{
			name: "release config present but disabled",
			rel:  &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			want: "No releaser configured for this repo (merge only).",
		},
		{
			name: "on_merge trigger",
			rel:  &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			want: "Will release immediately after merge.",
		},
		{
			name: "empty trigger defaults to on_merge behavior",
			rel:  &ReleaseConfig{Enabled: true, Trigger: ""},
			want: "Will release immediately after merge.",
		},
		{
			name: "manual trigger — falls back to merge-only wording",
			rel:  &ReleaseConfig{Enabled: true, Trigger: "manual"},
			want: "No releaser configured for this repo (merge only).",
		},
		{
			name: "on_scope_close trigger — falls back to merge-only wording",
			rel:  &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"},
			want: "No releaser configured for this repo (merge only).",
		},
		{
			name: "on_schedule with invalid cron — safe fallback phrasing",
			rel:  &ReleaseConfig{Enabled: true, Trigger: "on_schedule", Schedule: "not a cron expression"},
			want: "Rides the next release train (schedule unavailable — check release config).",
		},
		{
			name: "on_schedule with invalid timezone — safe fallback phrasing",
			rel:  &ReleaseConfig{Enabled: true, Trigger: "on_schedule", Schedule: "0 16 * * 1-5", ScheduleTimezone: "Not/AZone"},
			want: "Rides the next release train (schedule unavailable — check release config).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := releasePlanMessage(tt.rel, now)
			if got != tt.want {
				t.Errorf("releasePlanMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReleasePlanMessage_OnSchedule_RendersActualNextRun verifies the
// on_schedule branch renders the real next-run wall-clock time computed from
// the configured cron schedule + timezone, not a placeholder.
func TestReleasePlanMessage_OnSchedule_RendersActualNextRun(t *testing.T) {
	rel := &ReleaseConfig{
		Enabled:          true,
		Trigger:          "on_schedule",
		Schedule:         "0 16 * * 1-5",
		ScheduleTimezone: "Europe/Berlin",
	}
	// A Friday — the next weekday 16:00 slot is the same day.
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	got := releasePlanMessage(rel, now)

	if !strings.HasPrefix(got, "Rides the next release train: ") {
		t.Fatalf("expected on_schedule prefix, got: %s", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("expected trailing period, got: %s", got)
	}

	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("failed to load Europe/Berlin: %v", err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(rel.Schedule)
	if err != nil {
		t.Fatalf("failed to parse schedule: %v", err)
	}
	want := schedule.Next(now.In(loc)).Format("2006-01-02 15:04 MST")

	if !strings.Contains(got, want) {
		t.Errorf("expected message to contain computed next-run %q, got: %s", want, got)
	}
}

// TestNextScheduledRunString covers nextScheduledRunString directly: valid
// schedule/timezone, timezone defaulting to local when unset, and both
// invalid-schedule and invalid-timezone error paths.
func TestNextScheduledRunString(t *testing.T) {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	t.Run("valid schedule and timezone", func(t *testing.T) {
		rel := &ReleaseConfig{Schedule: "0 16 * * 1-5", ScheduleTimezone: "Europe/Berlin"}
		got, err := nextScheduledRunString(rel, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Error("expected non-empty next-run string")
		}
		if !strings.Contains(got, "CEST") && !strings.Contains(got, "CET") {
			t.Errorf("expected a Europe/Berlin zone abbreviation, got: %s", got)
		}
	})

	t.Run("empty timezone defaults to local", func(t *testing.T) {
		rel := &ReleaseConfig{Schedule: "0 16 * * 1-5"}
		got, err := nextScheduledRunString(rel, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Error("expected non-empty next-run string")
		}
	})

	t.Run("invalid schedule returns error", func(t *testing.T) {
		rel := &ReleaseConfig{Schedule: "not a cron expression"}
		if _, err := nextScheduledRunString(rel, now); err == nil {
			t.Error("expected an error for an invalid cron schedule")
		}
	})

	t.Run("invalid timezone returns error", func(t *testing.T) {
		rel := &ReleaseConfig{Schedule: "0 16 * * 1-5", ScheduleTimezone: "Not/AZone"}
		if _, err := nextScheduledRunString(rel, now); err == nil {
			t.Error("expected an error for an invalid timezone")
		}
	})
}
