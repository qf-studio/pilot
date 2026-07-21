package autopilot

import (
	"context"
	"log/slog"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// reconcileUnsourcedBoardIssues is the poll-cycle board-sourcing audit
// (GH-4488). It exists because of an asymmetry in how the studio-sdk poller
// picks dispatch candidates: when project_board.source_enabled is true, the
// board REPLACES label discovery entirely (that switch lives in the
// vendored studio-sdk module's poller, out of reach from this repo) — an
// open issue carrying the trigger label that is absent from the board, or
// whose card sits in a status other than source_status, is simply never
// considered, and nothing logs when that happens. The daemon looks
// identical whether the Todo column is legitimately empty or the poller
// goroutine is dead (GH-4488 evidence: pointer#136, labeled but off-board,
// sat undispatched 09:10Z-10:13Z; a watch session misread the silence as a
// dead poller and restarted the daemon for no effect).
//
// This sweep re-derives both sides of that comparison directly via exported
// studio-sdk primitives — Client.ListIssues for the label side,
// ProjectBoardSource.FindIssuesFromProject for the board side — and warns,
// once per issue per poll-session (not every tick, mirroring
// warnedUnsourcedIssues' dedup), for every labeled open issue the board
// side doesn't cover. It clears (and will warn again on recurrence) the
// instant an issue is no longer open, no longer labeled, or becomes
// sourced. It also republishes the current unsourced count as a gauge
// (pilot_poller_unsourced_labeled_issues{repo}) every tick regardless of
// dedup state, so the metric always reflects the live set even though the
// log line doesn't repeat.
func (c *Controller) reconcileUnsourcedBoardIssues(ctx context.Context) {
	if c.boardSource == nil {
		return
	}

	labeled, err := c.ghClient.ListIssues(ctx, c.owner, c.repo, &github.ListIssuesOptions{
		Labels: []string{c.pilotLabel},
		State:  github.StateOpen,
	})
	if err != nil {
		c.log.Warn("reconcileUnsourcedBoardIssues: failed to list open labeled issues",
			slog.String("repo", c.repoKey()), slog.Any("error", err))
		return
	}

	sourceStatus := c.boardSourceStatus
	if sourceStatus == "" {
		sourceStatus = "Todo"
	}
	sourced, err := c.boardSource.FindIssuesFromProject(ctx, sourceStatus)
	if err != nil {
		c.log.Warn("reconcileUnsourcedBoardIssues: failed to list board-sourced issues",
			slog.String("repo", c.repoKey()), slog.Any("error", err))
		return
	}

	sourcedNumbers := make(map[int]bool, len(sourced))
	for _, issue := range sourced {
		sourcedNumbers[issue.Number] = true
	}

	unsourced := unsourcedLabeledIssueNumbers(labeled, sourcedNumbers)

	c.mu.Lock()
	if c.warnedUnsourcedIssues == nil {
		c.warnedUnsourcedIssues = make(map[int]bool)
	}
	stillUnsourced := make(map[int]bool, len(unsourced))
	for _, num := range unsourced {
		stillUnsourced[num] = true
		if !c.warnedUnsourcedIssues[num] {
			c.warnedUnsourcedIssues[num] = true
			c.mu.Unlock()
			c.log.Warn("labeled issue not board-sourced — add to board or remove label",
				slog.String("repo", c.repoKey()),
				slog.Int("issue", num),
				slog.String("label", c.pilotLabel),
				slog.String("source_status", sourceStatus),
				slog.String("hint", "add to board and set status "+sourceStatus+", or remove the "+c.pilotLabel+" label"))
			c.mu.Lock()
		}
	}
	// Drop dedup entries for issues that are no longer unsourced (closed,
	// unlabeled, or now on the board in source_status) so a later
	// recurrence warns again instead of staying silent forever.
	for num := range c.warnedUnsourcedIssues {
		if !stillUnsourced[num] {
			delete(c.warnedUnsourcedIssues, num)
		}
	}
	c.mu.Unlock()

	c.metrics.SetUnsourcedLabeledIssues(c.repoKey(), int64(len(unsourced)))
}

// unsourcedLabeledIssueNumbers returns the issue numbers from labeled that
// are open (labeled is already state=open filtered by the caller's
// ListIssues call, but pilot-blocked issues are intentionally NOT excluded
// here — unlike reconcileLaneStarvation's "no execution in flight" check,
// board-sourcing invisibility is a config/data-hygiene problem independent
// of whether the issue is currently parked) and absent from sourcedNumbers.
// Extracted as a pure function so the set-logic is table-driven-testable
// without standing up a fake GraphQL server for every case (GH-4488
// acceptance: "table-driven tests for the unsourced-detection set logic").
func unsourcedLabeledIssueNumbers(labeled []*github.Issue, sourcedNumbers map[int]bool) []int {
	var unsourced []int
	for _, issue := range labeled {
		if issue == nil {
			continue
		}
		if sourcedNumbers[issue.Number] {
			continue
		}
		unsourced = append(unsourced, issue.Number)
	}
	return unsourced
}
