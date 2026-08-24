package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AlertCounter is a persisted checkpoint of the last-seen value of a
// windowed/cumulative stats counter (e.g. autopilot's circuit_breaker_trips)
// for a given (rule, source) pair. alerts.Engine uses it to make
// level-triggered stats-event rules edge-triggered — firing only when the
// counter increases since this checkpoint, never on a standing nonzero
// value — and to survive restarts without replaying the pre-restart backlog
// as fresh alerts (GH-5209).
type AlertCounter struct {
	RuleName  string
	Source    string
	LastValue int
	UpdatedAt time.Time
}

// UpsertAlertCounter persists the last-seen counter value for (ruleName,
// source), replacing any prior checkpoint. UpdatedAt is set to now.
func (s *Store) UpsertAlertCounter(ruleName, source string, value int) error {
	return s.withRetry("UpsertAlertCounter", func() error {
		_, err := s.db.Exec(`
			INSERT INTO alert_counters (rule_name, source, last_value, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(rule_name, source) DO UPDATE SET
				last_value = excluded.last_value,
				updated_at = excluded.updated_at
		`, ruleName, source, value, time.Now().UTC())
		return err
	})
}

// GetAlertCounter returns the last persisted checkpoint for (ruleName,
// source), or found=false if none exists yet.
func (s *Store) GetAlertCounter(ruleName, source string) (value int, found bool, err error) {
	row := s.db.QueryRow(`SELECT last_value FROM alert_counters WHERE rule_name = ? AND source = ?`, ruleName, source)
	if scanErr := row.Scan(&value); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("GetAlertCounter: scan: %w", scanErr)
	}
	return value, true, nil
}

// LoadAlertCounters returns every persisted checkpoint. Called once at
// engine construction to rehydrate the in-memory last-seen map after a
// restart, mirroring LoadActiveAlerts.
func (s *Store) LoadAlertCounters() ([]*AlertCounter, error) {
	rows, err := s.db.Query(`SELECT rule_name, source, last_value, updated_at FROM alert_counters`)
	if err != nil {
		return nil, fmt.Errorf("LoadAlertCounters: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*AlertCounter
	for rows.Next() {
		var c AlertCounter
		if err := rows.Scan(&c.RuleName, &c.Source, &c.LastValue, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("LoadAlertCounters: scan: %w", err)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}
