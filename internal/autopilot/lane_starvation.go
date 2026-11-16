package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/ghissue"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// reconcileLaneStarvation is the poll-cycle lane-starvation sweep (GH-4454).
// It's distinct from every other health check in this package — deadlock
// detection, PR-stuck-waiting-CI, failed-queue-depth — because those all
// require an active PR or execution to exist in the first place before they
// can measure lack of progress against it. This detector catches the case
// where NOTHING is even in flight for a lane that still has open, actionable
// work: a wedged head issue stuck behind studio-sdk's scope-overlap defer, a
// repick-hard-cap-stalled issue nobody re-armed, a crashed/misconfigured
// poller, or a dispatcher that silently stopped picking up this project —
// none of which leave a PR or execution row behind for the existing alerts to
// watch (GH-4454: exactly this starved a lane for 7h with no alert firing).
//
// It lists this repo's open issues carrying the trigger label (c.pilotLabel,
// default "pilot") and counts only those the GitHub poller would admit
// (ghissue.PollerAdmits, GH-6391): pull requests the issues list returns,
// and issues parked by a label the poller path never admits (pilot-blocked,
// pilot-needs-human, pilot-spec-incomplete, ...), are a known, intentional
// idle state, not starvation. The count is compared against
// laneQueueStatus.QueuedOrRunningCount for this project's lane. Every tick the
// lane looks starved, the in-memory streak counter increments and an event is
// emitted with the running streak and the counted issue numbers as metadata;
// every tick it doesn't, the streak resets to 0 and no event fires. The alerts
// engine's lane_starvation rule (handleLaneStarvation) — not this method —
// decides whether the streak has crossed RuleCondition.LaneStarvationPollCycles
// and is due (cooldown, per lane), mirroring how handleAutopilotMetrics owns
// FailedQueueThreshold/PRStuckTimeout instead of the emitting side
// pre-filtering.
func (c *Controller) reconcileLaneStarvation(ctx context.Context) {
	if c.alertsEngine == nil || c.laneQueueStatus == nil {
		return
	}

	issues, err := c.ghClient.ListIssues(ctx, c.owner, c.repo, &github.ListIssuesOptions{
		Labels: []string{c.pilotLabel},
		State:  github.StateOpen,
	})
	if err != nil {
		c.log.Warn("reconcileLaneStarvation: failed to list open pilot-labeled issues",
			slog.String("repo", c.repoKey()), slog.Any("error", err))
		return
	}

	var counted []*github.Issue
	for _, issue := range issues {
		if ghissue.PollerAdmits(issue, c.pilotLabel) {
			counted = append(counted, issue)
		}
	}
	sort.Slice(counted, func(i, j int) bool { return counted[i].Number < counted[j].Number })

	if len(counted) == 0 || c.laneQueueStatus.QueuedOrRunningCount(c.projectPath) > 0 {
		c.mu.Lock()
		c.laneStarvationStreak = 0
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.laneStarvationStreak++
	streak := c.laneStarvationStreak
	c.mu.Unlock()

	numbers := laneStarvationIssueNumbers(counted)

	c.log.Debug("reconcileLaneStarvation: lane idle with open actionable issues",
		slog.String("repo", c.repoKey()), slog.Int("streak", streak), slog.Int("open_issues", len(counted)))

	// Evidence line (GH-6391): once when the streak reaches the threshold,
	// then once per alert that fires. Asked before ProcessEvent, which
	// updates the cooldown clock asynchronously.
	if gate, ok := c.alertsEngine.(laneStarvationAlertGate); ok {
		if reached, due := gate.LaneStarvationAlertStatus(c.repoKey(), streak); reached || due {
			c.log.Info("reconcileLaneStarvation: lane starved with open admissible issues",
				slog.String("repo", c.repoKey()),
				slog.Int("streak", streak),
				slog.String("open_issue_numbers", numbers),
				slog.Any("issue_labels", laneStarvationIssueLabels(counted)),
				slog.Bool("alert_due", due))
		}
	}

	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeLaneStarvation,
		Project:   c.repoKey(),
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":                c.repoKey(),
			"project_path":        c.projectPath,
			"open_issue_count":    fmt.Sprintf("%d", len(counted)),
			"open_issue_numbers":  numbers,
			"poll_cycles_starved": fmt.Sprintf("%d", streak),
		},
	})
}

// laneStarvationAlertGate is the optional surface of the alert sink the
// sweep uses to log the evidence of an alert. Satisfied by *alerts.Engine;
// a sink without it (test fakes) gets no evidence line.
type laneStarvationAlertGate interface {
	LaneStarvationAlertStatus(project string, streak int) (reached, due bool)
}

// laneStarvationMaxNumbers caps the issue numbers named in the event
// metadata and the alert text (GH-6391).
const laneStarvationMaxNumbers = 10

// laneStarvationIssueNumbers renders the numbers of issues (already in
// ascending order) as "#1, #2", at most laneStarvationMaxNumbers of them,
// then "+N more".
func laneStarvationIssueNumbers(issues []*github.Issue) string {
	parts := make([]string, 0, laneStarvationMaxNumbers+1)
	for i, issue := range issues {
		if i == laneStarvationMaxNumbers {
			parts = append(parts, fmt.Sprintf("+%d more", len(issues)-laneStarvationMaxNumbers))
			break
		}
		parts = append(parts, fmt.Sprintf("#%d", issue.Number))
	}
	return strings.Join(parts, ", ")
}

// laneStarvationIssueLabels maps each counted issue number to its label
// names, for the evidence log line.
func laneStarvationIssueLabels(issues []*github.Issue) map[string][]string {
	out := make(map[string][]string, len(issues))
	for _, issue := range issues {
		names := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			names = append(names, l.Name)
		}
		out[fmt.Sprintf("#%d", issue.Number)] = names
	}
	return out
}
