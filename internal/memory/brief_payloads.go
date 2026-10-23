package memory

import (
	"encoding/json"
	"time"
)

// BriefPayload is one stored structured brief (GH-5791): the JSON document the
// console renders, with its generation time and schema version lifted out so
// listing never has to parse the payload.
type BriefPayload struct {
	ID          int64
	GeneratedAt time.Time
	Schema      int
	// Kind is the brief cadence ("daily" or "weekly"); empty saves as daily.
	Kind    string
	Payload json.RawMessage
}

const defaultBriefKind = "daily"

// SaveBriefPayload persists a structured brief's JSON payload.
func (s *Store) SaveBriefPayload(p *BriefPayload) error {
	kind := p.Kind
	if kind == "" {
		kind = defaultBriefKind
	}
	return s.withRetry("SaveBriefPayload", func() error {
		result, err := s.db.Exec(`
			INSERT INTO brief_payloads (generated_at, schema_version, kind, payload)
			VALUES (?, ?, ?, ?)
		`, p.GeneratedAt.UTC(), p.Schema, kind, string(p.Payload))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		p.ID = id
		p.Kind = kind
		return nil
	})
}

// ListBriefPayloads returns up to limit stored briefs, newest first. A
// non-empty kind restricts the result to that cadence; empty returns all.
// Latest-of-a-kind is ListBriefPayloads(1, kind).
func (s *Store) ListBriefPayloads(limit int, kind string) ([]*BriefPayload, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.Query(`
		SELECT id, generated_at, schema_version, kind, payload
		FROM brief_payloads
		WHERE (? = '' OR kind = ?)
		ORDER BY generated_at DESC, id DESC
		LIMIT ?
	`, kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*BriefPayload
	for rows.Next() {
		var p BriefPayload
		var payload string
		if err := rows.Scan(&p.ID, &p.GeneratedAt, &p.Schema, &p.Kind, &payload); err != nil {
			return nil, err
		}
		p.Payload = json.RawMessage(payload)
		out = append(out, &p)
	}
	return out, rows.Err()
}
