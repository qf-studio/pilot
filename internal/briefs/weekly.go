package briefs

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// topShippedLimit is how many shipped runs the weekly brief lists.
const topShippedLimit = 3

// WeeklySchedule derives the weekly cron spec from the daily one: the daily
// schedule's minute and hour, on the given weekday (empty = monday). The
// timezone is the scheduler's, so the weekly brief lands at the tenant's
// daily-brief hour (GH-5792).
func WeeklySchedule(dailySchedule, weeklyDay string) (string, error) {
	dow, err := ParseWeeklyDay(weeklyDay)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(dailySchedule)
	if len(fields) != 5 {
		return "", fmt.Errorf("weekly brief needs a 5-field daily schedule, got %q", dailySchedule)
	}
	return fmt.Sprintf("%s %s * * %d", fields[0], fields[1], dow), nil
}

// ParseWeeklyDay maps a weekday name to its cron day-of-week number
// (sunday=0). Empty means monday.
func ParseWeeklyDay(day string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(day)) {
	case "", "monday":
		return int(time.Monday), nil
	case "tuesday":
		return int(time.Tuesday), nil
	case "wednesday":
		return int(time.Wednesday), nil
	case "thursday":
		return int(time.Thursday), nil
	case "friday":
		return int(time.Friday), nil
	case "saturday":
		return int(time.Saturday), nil
	case "sunday":
		return int(time.Sunday), nil
	}
	return 0, fmt.Errorf("invalid weekly_day %q: want a weekday name such as monday", day)
}

// BuildWeeklyProof computes the proof numbers from the window's inputs. It is
// pure so the arithmetic is testable without a store.
//
//   - tickets_shipped: distinct tickets with a completed run.
//   - median_time_to_pr_minutes: per shipped run that opened a PR, minutes
//     from start to PR creation (the pr_created event, else completion);
//     0 when there are none.
//   - merged_count: PRs merged in the window, from the autopilot ledger.
//   - top_shipped: the three shipped runs with the largest diff.
func BuildWeeklyProof(in *memory.WeeklyProofInputs, shipped []ShippedRun) *WeeklyProof {
	tickets := make(map[string]bool)
	var minutes []float64
	for _, e := range in.Shipped {
		tickets[e.TaskID] = true
		if e.PRUrl == "" {
			continue
		}
		start := e.CreatedAt
		if e.StartedAt != nil {
			start = *e.StartedAt
		}
		end, ok := in.PRCreatedAt[e.ID]
		if !ok && e.CompletedAt != nil {
			end, ok = *e.CompletedAt, true
		}
		if !ok || end.Before(start) {
			continue
		}
		minutes = append(minutes, end.Sub(start).Minutes())
	}

	return &WeeklyProof{
		TicketsShipped:        len(tickets),
		MedianTimeToPRMinutes: median(minutes),
		MergedCount:           in.MergedCount,
		TopShipped:            topByDiff(shipped, topShippedLimit),
	}
}

// median returns the median rounded to one decimal, 0 for no samples.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	m := sorted[mid]
	if len(sorted)%2 == 0 {
		m = (sorted[mid-1] + sorted[mid]) / 2
	}
	return math.Round(m*10) / 10
}

// topByDiff returns up to n runs, largest diff (added + removed) first; ties
// keep their input order. Always non-nil so it marshals as [].
func topByDiff(runs []ShippedRun, n int) []ShippedRun {
	sorted := append([]ShippedRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].LinesAdded+sorted[i].LinesRemoved > sorted[j].LinesAdded+sorted[j].LinesRemoved
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return append([]ShippedRun{}, sorted...)
}

// Summary is the one-line rendering of the proof numbers for text channels.
func (w *WeeklyProof) Summary() string {
	return fmt.Sprintf("%d tickets shipped · %.0f min median time to PR · %d PRs merged",
		w.TicketsShipped, w.MedianTimeToPRMinutes, w.MergedCount)
}

// kindOrDaily treats an unset kind as daily.
func kindOrDaily(kind BriefKind) BriefKind {
	if kind == "" {
		return KindDaily
	}
	return kind
}

// briefTitle is the heading text for a brief of the given kind.
func briefTitle(kind BriefKind) string {
	if kind == KindWeekly {
		return "Pilot Weekly Brief"
	}
	return "Pilot Daily Brief"
}
