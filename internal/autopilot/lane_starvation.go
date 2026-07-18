package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
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
// default "pilot"), excludes any already parked with pilot-blocked (a
// deliberately-parked backlog — e.g. the repick-hard-cap stall label this
// same parent task's earlier subtask applies — is a known, intentional idle
// state, not starvation), and compares the remainder against
// laneQueueStatus.QueuedOrRunningCount for this project's lane. Every tick the
// lane looks starved, the in-memory streak counter increments and an event is
// emitted with the running streak as metadata; every tick it doesn't, the
// streak resets to 0 and no event fires. The alerts engine's lane_starvation
// rule (handleLaneStarvation) — not this method — decides whether the streak
// has crossed RuleCondition.LaneStarvationPollCycles and is due (cooldown),
// mirroring how handleAutopilotMetrics owns FailedQueueThreshold/PRStuckTimeout
// instead of the emitting side pre-filtering.
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

	actionable := 0
	for _, issue := range issues {
		if github.HasLabel(issue, github.LabelBlocked) {
			continue
		}
		actionable++
	}

	if actionable == 0 || c.laneQueueStatus.QueuedOrRunningCount(c.projectPath) > 0 {
		c.mu.Lock()
		c.laneStarvationStreak = 0
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.laneStarvationStreak++
	streak := c.laneStarvationStreak
	c.mu.Unlock()

	c.log.Debug("reconcileLaneStarvation: lane idle with open actionable issues",
		slog.String("repo", c.repoKey()), slog.Int("streak", streak), slog.Int("open_issues", actionable))

	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeLaneStarvation,
		Project:   c.repoKey(),
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":                c.repoKey(),
			"project_path":        c.projectPath,
			"open_issue_count":    fmt.Sprintf("%d", actionable),
			"poll_cycles_starved": fmt.Sprintf("%d", streak),
		},
	})
}
