package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestBriefPayloadsMigrationBackfillsDaily(t *testing.T) {
	dir := t.TempDir()

	// A pre-GH-5839 database: brief_payloads without the kind column.
	db, err := sql.Open("sqlite", filepath.Join(dir, "pilot.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE brief_payloads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		generated_at DATETIME NOT NULL,
		schema_version INTEGER NOT NULL,
		payload TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO brief_payloads (generated_at, schema_version, payload) VALUES (?, 1, '{}')`, at); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	_ = db.Close()

	// Open twice: the migration must backfill and then be idempotent.
	for i := 0; i < 2; i++ {
		store, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore (open %d): %v", i+1, err)
		}
		rows, err := store.ListBriefPayloads(10, "")
		if err != nil {
			t.Fatalf("ListBriefPayloads: %v", err)
		}
		if len(rows) != 1 || rows[0].Kind != "daily" {
			t.Fatalf("open %d: rows = %+v, want 1 row with Kind=daily", i+1, rows)
		}
		_ = store.Close()
	}
}

func TestBriefPayloadKindRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		wantKind string
	}{
		{"weekly", "weekly", "weekly"},
		{"daily", "daily", "daily"},
		{"empty defaults to daily", "", "daily"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, cleanup := newTestStore(t)
			defer cleanup()

			p := &BriefPayload{
				GeneratedAt: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
				Schema:      1,
				Kind:        tt.kind,
				Payload:     []byte(`{"a":1}`),
			}
			if err := s.SaveBriefPayload(p); err != nil {
				t.Fatalf("SaveBriefPayload: %v", err)
			}
			rows, err := s.ListBriefPayloads(1, "")
			if err != nil {
				t.Fatalf("ListBriefPayloads: %v", err)
			}
			if len(rows) != 1 || rows[0].Kind != tt.wantKind {
				t.Fatalf("rows = %+v, want 1 row with Kind=%q", rows, tt.wantKind)
			}
		})
	}
}

func TestListBriefPayloadsKindFilter(t *testing.T) {
	base := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		kind      string
		limit     int
		wantKinds []string
	}{
		{"no filter returns newest first", "", 10, []string{"daily", "weekly"}},
		{"no filter latest is the daily", "", 1, []string{"daily"}},
		{"weekly filter skips later daily", "weekly", 1, []string{"weekly"}},
		{"daily filter", "daily", 10, []string{"daily"}},
		{"unknown kind matches nothing", "monthly", 10, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, cleanup := newTestStore(t)
			defer cleanup()

			// Weekly first, then a later daily.
			for i, kind := range []string{"weekly", "daily"} {
				if err := s.SaveBriefPayload(&BriefPayload{
					GeneratedAt: base.Add(time.Duration(i) * 24 * time.Hour),
					Schema:      1,
					Kind:        kind,
					Payload:     []byte(`{}`),
				}); err != nil {
					t.Fatalf("SaveBriefPayload(%s): %v", kind, err)
				}
			}

			rows, err := s.ListBriefPayloads(tt.limit, tt.kind)
			if err != nil {
				t.Fatalf("ListBriefPayloads: %v", err)
			}
			if len(rows) != len(tt.wantKinds) {
				t.Fatalf("got %d rows, want %d", len(rows), len(tt.wantKinds))
			}
			for i, want := range tt.wantKinds {
				if rows[i].Kind != want {
					t.Errorf("rows[%d].Kind = %q, want %q", i, rows[i].Kind, want)
				}
			}
		})
	}
}
