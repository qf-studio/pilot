package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PendingApproval is a row in the approval_pending table, capturing enough
// state to reconstruct an in-flight Telegram approval after a daemon restart.
type PendingApproval struct {
	RequestID   string
	TaskID      string
	Stage       string
	Title       string
	Description string
	ChatID      string
	MessageID   int64
	Approvers   []string
	Metadata    map[string]interface{}
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// InsertPendingApproval persists a pending approval row. Replaces an existing
// row with the same request_id (UPSERT), so re-sends after restart are safe.
func (s *Store) InsertPendingApproval(ctx context.Context, p PendingApproval) error {
	approversJSON, err := json.Marshal(p.Approvers)
	if err != nil {
		return fmt.Errorf("marshal approvers: %w", err)
	}
	metadataJSON, err := json.Marshal(p.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	return s.withRetry("InsertPendingApproval", func() error {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO approval_pending
				(request_id, task_id, stage, title, description, chat_id, message_id,
				 approvers, metadata, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(request_id) DO UPDATE SET
				task_id=excluded.task_id, stage=excluded.stage, title=excluded.title,
				description=excluded.description, chat_id=excluded.chat_id,
				message_id=excluded.message_id, approvers=excluded.approvers,
				metadata=excluded.metadata, expires_at=excluded.expires_at`,
			p.RequestID, p.TaskID, p.Stage, p.Title, p.Description,
			p.ChatID, p.MessageID,
			string(approversJSON), string(metadataJSON),
			p.ExpiresAt.Unix(), p.CreatedAt.Unix(),
		)
		return err
	})
}

// DeletePendingApproval removes the row for requestID. It is idempotent:
// deleting a row that no longer exists is not an error.
func (s *Store) DeletePendingApproval(ctx context.Context, requestID string) error {
	return s.withRetry("DeletePendingApproval", func() error {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM approval_pending WHERE request_id = ?`, requestID)
		return err
	})
}

// LoadPendingApprovals returns all rows from approval_pending. Callers are
// responsible for filtering expired entries (ExpiresAt < now).
func (s *Store) LoadPendingApprovals(ctx context.Context) ([]PendingApproval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT request_id, task_id, stage, title, description, chat_id, message_id,
		       approvers, metadata, expires_at, created_at
		FROM approval_pending
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("query approval_pending: %w", err)
	}
	defer rows.Close()

	var result []PendingApproval
	for rows.Next() {
		var p PendingApproval
		var approversJSON, metadataJSON string
		var expiresUnix, createdUnix int64
		if err := rows.Scan(
			&p.RequestID, &p.TaskID, &p.Stage, &p.Title, &p.Description,
			&p.ChatID, &p.MessageID,
			&approversJSON, &metadataJSON,
			&expiresUnix, &createdUnix,
		); err != nil {
			return nil, fmt.Errorf("scan approval_pending row: %w", err)
		}
		if err := json.Unmarshal([]byte(approversJSON), &p.Approvers); err != nil {
			p.Approvers = nil
		}
		if metadataJSON != "" && metadataJSON != "null" {
			if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
				p.Metadata = nil
			}
		}
		p.ExpiresAt = time.Unix(expiresUnix, 0)
		p.CreatedAt = time.Unix(createdUnix, 0)
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating approval_pending rows: %w", err)
	}
	return result, nil
}

// PrunePendingApprovals deletes rows whose expires_at is before the given
// time. Returns the number of rows deleted.
func (s *Store) PrunePendingApprovals(ctx context.Context, before time.Time) (int, error) {
	var n int64
	err := s.withRetry("PrunePendingApprovals", func() error {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM approval_pending WHERE expires_at < ?`, before.Unix())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// approvalPendingMigration is the SQL that creates the approval_pending table.
// It is referenced from store.go's migrate() slice.
const approvalPendingMigration = `CREATE TABLE IF NOT EXISTS approval_pending (
	request_id   TEXT PRIMARY KEY,
	task_id      TEXT NOT NULL DEFAULT '',
	stage        TEXT NOT NULL DEFAULT '',
	title        TEXT NOT NULL DEFAULT '',
	description  TEXT NOT NULL DEFAULT '',
	chat_id      TEXT NOT NULL DEFAULT '',
	message_id   INTEGER NOT NULL DEFAULT 0,
	approvers    TEXT NOT NULL DEFAULT '[]',
	metadata     TEXT,
	expires_at   INTEGER NOT NULL,
	created_at   INTEGER NOT NULL
)`

