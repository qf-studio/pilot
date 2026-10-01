package executor

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// GH-5553: the per-run WARN must count only denials ingestGhGuardDenials
// will journal; test-origin denials are suppressed there and must not
// inflate (or trigger) the WARN.
func TestWarnGhGuardDenials_ExcludesTestOrigin(t *testing.T) {
	now := time.Now()
	deny := func(testOrigin bool) ghguard.JournalEntry {
		return ghguard.JournalEntry{Time: now, Verdict: ghguard.VerdictDeny, Reason: "x", Args: []string{"issue", "close", "1"}, TestOrigin: testOrigin}
	}
	tests := []struct {
		name      string
		denials   []ghguard.JournalEntry
		wantCount int // 0 = no WARN expected
	}{
		{"model only", []ghguard.JournalEntry{deny(false), deny(false)}, 2},
		{"mixed", []ghguard.JournalEntry{deny(true), deny(true), deny(false)}, 1},
		{"test origin only", []ghguard.JournalEntry{deny(true), deny(true)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			b := &ClaudeCodeBackend{log: slog.New(slog.NewJSONHandler(&buf, nil))}
			b.warnGhGuardDenials(tt.denials)

			if tt.wantCount == 0 {
				if buf.Len() != 0 {
					t.Fatalf("unexpected log output: %s", buf.String())
				}
				return
			}
			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("decode %q: %v", buf.String(), err)
			}
			if rec["level"] != "WARN" || rec["count"] != float64(tt.wantCount) {
				t.Errorf("record = %v, want WARN count=%d", rec, tt.wantCount)
			}
		})
	}
}
