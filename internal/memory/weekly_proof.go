package memory

import (
	"fmt"
	"strings"
	"time"
)

// WeeklyProofInputs is the raw ledger data behind the weekly brief's proof
// numbers (GH-5792). The arithmetic lives in the briefs package; this only
// reads.
type WeeklyProofInputs struct {
	// Shipped are the completed executions that finished inside the window.
	Shipped []*Execution
	// PRCreatedAt maps an execution ID to when its PR was opened (earliest
	// pr_created ledger event). Executions without the event are absent.
	PRCreatedAt map[string]time.Time
	// MergedCount is the number of distinct executions whose PR was merged
	// inside the window.
	MergedCount int
}

// GetWeeklyProofInputs reads the executions ledger and the autopilot PR
// stage ledger for the window.
//
// Decision: merged PRs are counted from execution_events (stage "merged"),
// not from autopilot_pr_state, because autopilot deletes a PR's state row
// once it completes (see GetLifetimePRCounters) — the stage ledger is the
// durable record of that state machine.
func (s *Store) GetWeeklyProofInputs(query BriefQuery) (*WeeklyProofInputs, error) {
	execs, err := s.GetExecutionsForReceipts(query)
	if err != nil {
		return nil, err
	}

	in := &WeeklyProofInputs{PRCreatedAt: map[string]time.Time{}}
	for _, e := range execs {
		if e.Status == "completed" {
			in.Shipped = append(in.Shipped, e)
		}
	}

	created, err := s.earliestStageOccurrences(StagePRCreated)
	if err != nil {
		return nil, err
	}
	for _, e := range in.Shipped {
		if at, ok := created[e.ID]; ok {
			in.PRCreatedAt[e.ID] = at
		}
	}

	args := []interface{}{string(StageMerged), query.Start.UTC(), query.End.UTC()}
	where := ""
	if len(query.Projects) > 0 {
		where = " AND e.project_path IN (?" + strings.Repeat(",?", len(query.Projects)-1) + ")"
		for _, p := range query.Projects {
			args = append(args, p)
		}
	}
	err = s.db.QueryRow(`
		SELECT COUNT(DISTINCT ee.execution_id)
		FROM execution_events ee
		JOIN executions e ON e.id = ee.execution_id
		WHERE ee.stage = ? AND ee.occurred_at >= ? AND ee.occurred_at < ?
			AND COALESCE(e.is_canary, 0) = 0`+where, args...).Scan(&in.MergedCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count merged PRs: %w", err)
	}
	return in, nil
}
