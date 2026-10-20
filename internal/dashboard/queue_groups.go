package dashboard

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const (
	// maxQueueProjects caps how many project groups the grid QUEUE panel shows.
	maxQueueProjects = 3
	// maxQueueTicketsPerProject caps how many tickets each group shows.
	maxQueueTicketsPerProject = 3
	// queueNoProjectName is the display name for tasks with no project info.
	queueNoProjectName = "(no project)"
)

// queueGroup is one project block in the grid QUEUE panel.
type queueGroup struct {
	Key        string        // grouping key: ProjectPath, else ProjectName, else ""
	Name       string        // display name
	Tasks      []TaskDisplay // visible tickets, already ordered and capped
	RightValue string        // header right value ("" when none), unstyled
}

// queueTaskRank orders tickets inside a project: running, queued, pending,
// then every finished status.
func queueTaskRank(s QueueStatus) int {
	switch s {
	case QueueStatusRunning:
		return 0
	case QueueStatusQueued:
		return 1
	case QueueStatusPending:
		return 2
	default:
		return 3
	}
}

// queueStatusActive reports whether a status counts as live work for project
// ordering (running, queued or pending).
func queueStatusActive(s QueueStatus) bool {
	return queueTaskRank(s) < 3
}

// queueProjectKey is the grouping key: ProjectPath, then ProjectName.
func queueProjectKey(t TaskDisplay) string {
	if t.ProjectPath != "" {
		return t.ProjectPath
	}
	return t.ProjectName
}

// queueProjectName is the display name: ProjectName, then the base name of
// ProjectPath (same fallback as syncGitGraphToSelectedTask), then "(no project)".
func queueProjectName(t TaskDisplay) string {
	if t.ProjectName != "" {
		return t.ProjectName
	}
	if t.ProjectPath != "" {
		return filepath.Base(t.ProjectPath)
	}
	return queueNoProjectName
}

// queueRunTime is CompletedAt − StartedAt, and false when either is unknown.
func queueRunTime(t TaskDisplay) (time.Duration, bool) {
	if t.StartedAt.IsZero() || t.CompletedAt.IsZero() || t.CompletedAt.Before(t.StartedAt) {
		return 0, false
	}
	return t.CompletedAt.Sub(t.StartedAt), true
}

// groupQueueTasks groups tasks by project and returns at most
// maxQueueProjects groups of at most maxQueueTicketsPerProject tickets, plus
// the number of projects that did not fit. Pure: now is injected so header
// right values are testable.
//
// Project order: projects with a running/queued/pending ticket first, then by
// newest activity (latest StartedAt/CompletedAt) descending, then name.
// Ticket order: running, queued (input order), pending (input order), then
// finished by CompletedAt descending; ID is the tiebreak.
func groupQueueTasks(tasks []TaskDisplay, now time.Time) (groups []queueGroup, hidden int) {
	type bucket struct {
		key      string
		name     string
		tasks    []TaskDisplay
		active   bool
		activity time.Time
	}
	var buckets []*bucket
	byKey := make(map[string]*bucket)
	for _, t := range tasks {
		key := queueProjectKey(t)
		b := byKey[key]
		if b == nil {
			b = &bucket{key: key, name: queueProjectName(t)}
			byKey[key] = b
			buckets = append(buckets, b)
		}
		b.tasks = append(b.tasks, t)
		if queueStatusActive(t.Status) {
			b.active = true
		}
		if t.StartedAt.After(b.activity) {
			b.activity = t.StartedAt
		}
		if t.CompletedAt.After(b.activity) {
			b.activity = t.CompletedAt
		}
	}

	sort.SliceStable(buckets, func(i, j int) bool {
		a, b := buckets[i], buckets[j]
		if a.active != b.active {
			return a.active
		}
		if !a.activity.Equal(b.activity) {
			return a.activity.After(b.activity)
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.key < b.key
	})

	for i, b := range buckets {
		if i >= maxQueueProjects {
			hidden++
			continue
		}
		ordered := make([]TaskDisplay, len(b.tasks))
		copy(ordered, b.tasks)
		sort.SliceStable(ordered, func(x, y int) bool {
			ta, tb := ordered[x], ordered[y]
			ra, rb := queueTaskRank(ta.Status), queueTaskRank(tb.Status)
			if ra != rb {
				return ra < rb
			}
			switch ra {
			case 1, 2: // queued, pending: keep queue position order
				return false
			}
			if ra == 3 && !ta.CompletedAt.Equal(tb.CompletedAt) {
				return ta.CompletedAt.After(tb.CompletedAt)
			}
			return ta.ID < tb.ID
		})

		g := queueGroup{Key: b.key, Name: b.name, RightValue: queueGroupRightValue(b.tasks, now)}
		if len(ordered) > maxQueueTicketsPerProject {
			ordered = ordered[:maxQueueTicketsPerProject]
		}
		g.Tasks = ordered
		groups = append(groups, g)
	}
	return groups, hidden
}

// queueGroupRightValue builds the project header's right-aligned value over
// all of the project's tasks (not only the visible ones).
func queueGroupRightValue(tasks []TaskDisplay, now time.Time) string {
	var running *TaskDisplay
	var lastDone, lastFailed time.Time
	for i := range tasks {
		t := &tasks[i]
		switch t.Status {
		case QueueStatusRunning:
			if running == nil || runningStartsFirst(t, running) {
				running = t
			}
		case QueueStatusDone:
			if t.CompletedAt.After(lastDone) {
				lastDone = t.CompletedAt
			}
		case QueueStatusFailed:
			if t.CompletedAt.After(lastFailed) {
				lastFailed = t.CompletedAt
			}
		}
	}

	if running != nil {
		var parts []string
		if p := strings.ToLower(running.Phase); p != "" {
			parts = append(parts, p)
		}
		if !running.StartedAt.IsZero() {
			parts = append(parts, formatDurationShort(now.Sub(running.StartedAt)))
		}
		return strings.Join(parts, " · ")
	}
	if !lastDone.IsZero() {
		return "last ✓ " + formatTimeAgo(lastDone)
	}
	if !lastFailed.IsZero() {
		return "last ✗ " + formatTimeAgo(lastFailed)
	}
	return ""
}

// runningStartsFirst reports whether a started before b; a known start beats
// an unknown one.
func runningStartsFirst(a, b *TaskDisplay) bool {
	if a.StartedAt.IsZero() {
		return false
	}
	if b.StartedAt.IsZero() {
		return true
	}
	return a.StartedAt.Before(b.StartedAt)
}

// renderQueueGroupHeader renders "  name        right value" in dimStyle,
// fitting iw-2 columns like the rows' right edge so nothing overflows.
func renderQueueGroupHeader(g queueGroup, iw int) string {
	avail := iw - 2
	value := g.RightValue
	if value != "" {
		if maxV := avail - 2 - 1 - 8; maxV < 1 {
			value = ""
		} else {
			value = truncateVisual(value, maxV)
		}
	}
	valueW := lipgloss.Width(value)
	gap := 0
	if value != "" {
		gap = 1
	}
	nameW := avail - 2 - valueW - gap
	if nameW < 1 {
		nameW = 1
	}
	name := truncateVisual(g.Name, nameW)
	pad := avail - 2 - lipgloss.Width(name) - valueW
	if pad < 0 {
		pad = 0
	}
	line := "  " + dimStyle.Render(name)
	if value != "" {
		line += strings.Repeat(" ", pad) + dimStyle.Render(value)
	}
	return line
}

// queueFinishedSlot is the 16-column text that replaces the progress bar on
// finished rows: "<run time> · <time ago>", with missing parts dropped.
func queueFinishedSlot(t TaskDisplay) string {
	var parts []string
	if d, ok := queueRunTime(t); ok {
		parts = append(parts, formatDurationShort(d))
	}
	if !t.CompletedAt.IsZero() {
		parts = append(parts, formatTimeAgo(t.CompletedAt))
	}
	if len(parts) == 0 {
		return "–"
	}
	return strings.Join(parts, " · ")
}

// queueMoreProjectsLine is the "+N projects" overflow line.
func queueMoreProjectsLine(hidden int) string {
	return "  " + dimStyle.Render(fmt.Sprintf("+%d projects", hidden))
}
