package memory

import (
	"database/sql"
	"time"
)

// DefaultFailureThreshold is the failure rate above which escalation is recommended.
const DefaultFailureThreshold = 0.30

// DefaultOutcomeWindow is the number of recent outcomes considered for failure rate.
const DefaultOutcomeWindow = 10

// EscalationPath defines the model upgrade sequence.
var EscalationPath = []string{"haiku", "sonnet", "opus"}

// ModelOutcomeTracker tracks per-model success/failure outcomes and recommends escalation.
type ModelOutcomeTracker struct {
	store            *Store
	failureThreshold float64
	outcomeWindow    int
}

// NewModelOutcomeTracker creates a tracker with default settings.
func NewModelOutcomeTracker(store *Store) *ModelOutcomeTracker {
	return &ModelOutcomeTracker{
		store:            store,
		failureThreshold: DefaultFailureThreshold,
		outcomeWindow:    DefaultOutcomeWindow,
	}
}

// WithFailureThreshold sets a custom failure threshold (0.0–1.0).
func (t *ModelOutcomeTracker) WithFailureThreshold(threshold float64) *ModelOutcomeTracker {
	t.failureThreshold = threshold
	return t
}

// RecordOutcome persists a model execution outcome.
func (t *ModelOutcomeTracker) RecordOutcome(taskType, model, outcome string, tokens int, duration time.Duration) error {
	return t.store.withRetry("RecordModelOutcome", func() error {
		_, err := t.store.db.Exec(
			`INSERT INTO model_outcomes (task_type, model, outcome, tokens_used, duration_ms) VALUES (?, ?, ?, ?, ?)`,
			taskType, model, outcome, tokens, duration.Milliseconds(),
		)
		return err
	})
}

// GetFailureRate returns the failure rate for a task type + model over the last N outcomes.
// Returns 0.0 if no outcomes exist.
func (t *ModelOutcomeTracker) GetFailureRate(taskType, model string) float64 {
	var total, failures int
	rows, err := t.store.db.Query(
		`SELECT outcome FROM model_outcomes WHERE task_type = ? AND model = ? ORDER BY id DESC LIMIT ?`,
		taskType, model, t.outcomeWindow,
	)
	if err != nil {
		return 0.0
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			continue
		}
		total++
		if outcome == "failure" {
			failures++
		}
	}
	if total == 0 {
		return 0.0
	}
	return float64(failures) / float64(total)
}

// ShouldEscalate returns true and the recommended model if the failure rate exceeds threshold.
// Returns false if no escalation is needed or the model is already at the top of the path.
func (t *ModelOutcomeTracker) ShouldEscalate(taskType, model string) (bool, string) {
	rate := t.GetFailureRate(taskType, model)
	if rate <= t.failureThreshold {
		return false, ""
	}

	// Find next model in escalation path
	for i, m := range EscalationPath {
		if m == model && i+1 < len(EscalationPath) {
			return true, EscalationPath[i+1]
		}
	}
	return false, ""
}

// GetOutcomeStats returns aggregate stats for a task type + model.
func (t *ModelOutcomeTracker) GetOutcomeStats(taskType, model string) (total int, failures int, avgTokens float64, err error) {
	row := t.store.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN outcome='failure' THEN 1 ELSE 0 END),0), COALESCE(AVG(tokens_used),0)
		 FROM (SELECT outcome, tokens_used FROM model_outcomes WHERE task_type = ? AND model = ? ORDER BY id DESC LIMIT ?)`,
		taskType, model, t.outcomeWindow,
	)
	err = row.Scan(&total, &failures, &avgTokens)
	if err == sql.ErrNoRows {
		return 0, 0, 0, nil
	}
	return
}
