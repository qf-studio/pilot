package memory

import (
	"encoding/json"
	"fmt"
	"time"
)

// ActiveAlert is a serializable snapshot of an alerts.Engine active (still
// firing) alert, persisted to SQLite so a condition that recovers while the
// daemon is down still emits its resolution once the daemon restarts
// (GH-4890, follow-up to #4886's resolution-notifications work). Only the
// fields a resolution needs are stored — including Channels, the set the
// original alert was delivered to, so a rehydrated resolution reaches those
// same channels rather than being re-filtered against its own info
// severity. The memory package carries no alerts import; callers convert
// to/from alerts.Alert/AlertRule as needed.
type ActiveAlert struct {
	RuleName    string
	Source      string
	AlertID     string
	AlertType   string
	Title       string
	Message     string
	ProjectPath string
	Metadata    map[string]string
	Channels    []string
	CreatedAt   time.Time
}

// UpsertActiveAlert persists an active alert, replacing any existing row for
// the same (rule_name, source) pair — the identity alerts.Engine already
// uses as its in-memory activeAlerts map key (activeAlertKey).
// CreatedAt defaults to now when zero.
func (s *Store) UpsertActiveAlert(a *ActiveAlert) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	metaJSON, err := json.Marshal(a.Metadata)
	if err != nil {
		return fmt.Errorf("UpsertActiveAlert: marshal metadata: %w", err)
	}
	channelsJSON, err := json.Marshal(a.Channels)
	if err != nil {
		return fmt.Errorf("UpsertActiveAlert: marshal channels: %w", err)
	}
	return s.withRetry("UpsertActiveAlert", func() error {
		_, err := s.db.Exec(`
			INSERT INTO active_alerts
				(rule_name, source, alert_id, alert_type, title, message, project_path, metadata, channels, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(rule_name, source) DO UPDATE SET
				alert_id     = excluded.alert_id,
				alert_type   = excluded.alert_type,
				title        = excluded.title,
				message      = excluded.message,
				project_path = excluded.project_path,
				metadata     = excluded.metadata,
				channels     = excluded.channels,
				created_at   = excluded.created_at
		`,
			a.RuleName, a.Source, a.AlertID, a.AlertType, a.Title, a.Message,
			a.ProjectPath, string(metaJSON), string(channelsJSON), a.CreatedAt,
		)
		return err
	})
}

// DeleteActiveAlert removes an active alert row on resolution. Delete-on-
// resolve keeps this table bounded to alerts that are actually still
// firing — this is not a history table (alert_history/AlertHistory already
// covers that), so there is no expiry sweep needed on top of it.
func (s *Store) DeleteActiveAlert(ruleName, source string) error {
	return s.withRetry("DeleteActiveAlert", func() error {
		_, err := s.db.Exec(`DELETE FROM active_alerts WHERE rule_name = ? AND source = ?`, ruleName, source)
		return err
	})
}

// LoadActiveAlerts returns every currently-active alert row, ordered by
// creation time ascending. Called once at engine construction to rehydrate
// the in-memory activeAlerts map after a restart.
func (s *Store) LoadActiveAlerts() ([]*ActiveAlert, error) {
	rows, err := s.db.Query(`
		SELECT rule_name, source, alert_id, alert_type, title, message,
			COALESCE(project_path, ''), COALESCE(metadata, ''), COALESCE(channels, ''),
			created_at
		FROM active_alerts
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("LoadActiveAlerts: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*ActiveAlert
	for rows.Next() {
		a, err := scanActiveAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("LoadActiveAlerts: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scanActiveAlert scans a single row from active_alerts.
func scanActiveAlert(row interface {
	Scan(...interface{}) error
}) (*ActiveAlert, error) {
	var a ActiveAlert
	var metaJSON, channelsJSON string
	if err := row.Scan(
		&a.RuleName, &a.Source, &a.AlertID, &a.AlertType, &a.Title, &a.Message,
		&a.ProjectPath, &metaJSON, &channelsJSON, &a.CreatedAt,
	); err != nil {
		return nil, err
	}
	if metaJSON != "" {
		if err := json.Unmarshal([]byte(metaJSON), &a.Metadata); err != nil {
			a.Metadata = nil
		}
	}
	if channelsJSON != "" {
		if err := json.Unmarshal([]byte(channelsJSON), &a.Channels); err != nil {
			a.Channels = nil
		}
	}
	return &a, nil
}
