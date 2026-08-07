package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PendingApproval is a serialisable snapshot of an approval.Request persisted
// to SQLite so it survives process restarts. Consumers convert to/from
// approval.Request as needed; the memory package carries no approval import.
type PendingApproval struct {
	ID               string
	TaskID           string
	Stage            string
	Title            string
	Description      string
	Metadata         map[string]interface{}
	Approvers        []string
	PreferredChannel string
	CreatedAt        time.Time
	ExpiresAt        time.Time

	// Project is the canonicalized project path this request was submitted
	// for (approval.Request.Project — GH-4773). Empty for pre-migration rows
	// and requests submitted with no project context.
	Project string
}

// InsertPendingApproval persists a new pending approval record.
// Replaces any existing row with the same ID.
// CreatedAt defaults to now when zero; ExpiresAt must be set by the caller (schema NOT NULL).
func (s *Store) InsertPendingApproval(a *PendingApproval) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.ExpiresAt.IsZero() {
		return fmt.Errorf("InsertPendingApproval: ExpiresAt must be set (id=%s)", a.ID)
	}
	metaJSON, err := json.Marshal(a.Metadata)
	if err != nil {
		return fmt.Errorf("InsertPendingApproval: marshal metadata: %w", err)
	}
	approversJSON, err := json.Marshal(a.Approvers)
	if err != nil {
		return fmt.Errorf("InsertPendingApproval: marshal approvers: %w", err)
	}
	return s.withRetry("InsertPendingApproval", func() error {
		_, err := s.db.Exec(`
			INSERT INTO approval_pending
				(id, task_id, stage, title, description, metadata, approvers, preferred_channel, project, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				task_id           = excluded.task_id,
				stage             = excluded.stage,
				title             = excluded.title,
				description       = excluded.description,
				metadata          = excluded.metadata,
				approvers         = excluded.approvers,
				preferred_channel = excluded.preferred_channel,
				project           = excluded.project,
				expires_at        = excluded.expires_at
		`,
			a.ID, a.TaskID, a.Stage, a.Title, a.Description,
			string(metaJSON), string(approversJSON), a.PreferredChannel, a.Project,
			a.CreatedAt, a.ExpiresAt,
		)
		return err
	})
}

// DeletePendingApproval removes a pending approval by ID.
func (s *Store) DeletePendingApproval(id string) error {
	return s.withRetry("DeletePendingApproval", func() error {
		_, err := s.db.Exec(`DELETE FROM approval_pending WHERE id = ?`, id)
		return err
	})
}

// LoadPendingApprovals returns all pending approval records, ordered by creation time ascending.
func (s *Store) LoadPendingApprovals() ([]*PendingApproval, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, stage, title,
			COALESCE(description, ''), COALESCE(metadata, ''),
			COALESCE(approvers, ''), COALESCE(preferred_channel, ''),
			COALESCE(project, ''),
			created_at, expires_at
		FROM approval_pending
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("LoadPendingApprovals: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*PendingApproval
	for rows.Next() {
		a, err := scanPendingApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("LoadPendingApprovals: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PrunePendingApprovals deletes approvals whose expires_at is before
// `cutoff` AND whose preferred_channel is one of `channels`. Channel-scoped
// (GH-4772) so a handler's expiry sweep only ever deletes/decides rows it
// owns — e.g. a Slack sweep must never delete a Telegram-dispatched row's
// expiry before Telegram's own sweep gets to it. Callers pass "" in
// `channels` to include legacy rows with no preferred_channel set. Returns
// 0, nil for an empty `channels` (nothing to scope to, not "everything").
func (s *Store) PrunePendingApprovals(cutoff time.Time, channels []string) (int64, error) {
	if len(channels) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(channels))
	args := make([]interface{}, 0, len(channels)+1)
	args = append(args, cutoff)
	for i, c := range channels {
		placeholders[i] = "?"
		args = append(args, c)
	}
	query := fmt.Sprintf(
		`DELETE FROM approval_pending WHERE expires_at < ? AND preferred_channel IN (%s)`,
		strings.Join(placeholders, ","),
	)
	var result sql.Result
	err := s.withRetry("PrunePendingApprovals", func() error {
		var execErr error
		result, execErr = s.db.Exec(query, args...)
		return execErr
	})
	if err != nil {
		return 0, fmt.Errorf("PrunePendingApprovals: %w", err)
	}
	return result.RowsAffected()
}

// PrunePendingApprovalsOutside deletes approvals whose expires_at is before
// `cutoff` AND whose preferred_channel is NOT one of `knownChannels`. This
// is the documented fallback for rows whose channel matches no registered
// handler (a channel that was removed from config, a typo, etc.) — without
// it such a row is never scoped to any handler's PrunePendingApprovals call
// and becomes unprunable (GH-4772 acceptance 4). Callers should route this
// through exactly one handler (the default legacy claimant) to avoid
// multiple handlers racing to sweep the same orphaned rows; a full
// Manager-level orphan sweep is roadmap leg B4. Returns 0, nil for an empty
// `knownChannels` (matches "outside everything known", which is nothing to
// exclude — same delete-all-expired shape as the pre-GH-4772 behavior).
func (s *Store) PrunePendingApprovalsOutside(cutoff time.Time, knownChannels []string) (int64, error) {
	var query string
	args := make([]interface{}, 0, len(knownChannels)+1)
	args = append(args, cutoff)
	if len(knownChannels) == 0 {
		query = `DELETE FROM approval_pending WHERE expires_at < ?`
	} else {
		placeholders := make([]string, len(knownChannels))
		for i, c := range knownChannels {
			placeholders[i] = "?"
			args = append(args, c)
		}
		query = fmt.Sprintf(
			`DELETE FROM approval_pending WHERE expires_at < ? AND preferred_channel NOT IN (%s)`,
			strings.Join(placeholders, ","),
		)
	}
	var result sql.Result
	err := s.withRetry("PrunePendingApprovalsOutside", func() error {
		var execErr error
		result, execErr = s.db.Exec(query, args...)
		return execErr
	})
	if err != nil {
		return 0, fmt.Errorf("PrunePendingApprovalsOutside: %w", err)
	}
	return result.RowsAffected()
}

// scanPendingApproval scans a single row from approval_pending.
func scanPendingApproval(row interface {
	Scan(...interface{}) error
}) (*PendingApproval, error) {
	var a PendingApproval
	var metaJSON, approversJSON string
	if err := row.Scan(
		&a.ID, &a.TaskID, &a.Stage, &a.Title, &a.Description,
		&metaJSON, &approversJSON, &a.PreferredChannel, &a.Project,
		&a.CreatedAt, &a.ExpiresAt,
	); err != nil {
		return nil, err
	}
	if metaJSON != "" {
		if err := json.Unmarshal([]byte(metaJSON), &a.Metadata); err != nil {
			a.Metadata = nil
		}
	}
	if approversJSON != "" {
		if err := json.Unmarshal([]byte(approversJSON), &a.Approvers); err != nil {
			a.Approvers = nil
		}
	}
	return &a, nil
}
