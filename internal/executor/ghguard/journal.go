package ghguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// JournalEntry is one denied gh invocation, recorded to the guard journal
// (a newline-delimited JSON file) so the daemon can pick it up as evidence
// after the run — mirroring the #4670 sideeffect_audit.go pattern, whose
// event type and alert this journal feeds (see ingestGHGuardJournal in the
// executor package).
type JournalEntry struct {
	Time   time.Time `json:"time"`
	Args   []string  `json:"args"`
	Reason string    `json:"reason"`
	Issue  string    `json:"issue,omitempty"`
	Repo   string    `json:"repo,omitempty"`
	Branch string    `json:"branch,omitempty"`
}

// AppendDenyToJournal records one denied invocation to path, a
// newline-delimited JSON file. Creates the parent directory and the file if
// they don't exist yet. A single JSON line is well under PIPE_BUF (4096
// bytes on Linux/macOS), so O_APPEND writes from concurrent gh-guard
// processes within the same task don't interleave — no extra locking.
func AppendDenyToJournal(path string, argv []string, ctx TaskContext, verdict Verdict) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gh-guard journal: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("gh-guard journal: open: %w", err)
	}
	defer f.Close()

	entry := JournalEntry{
		Time:   time.Now().UTC(),
		Args:   argv,
		Reason: verdict.Reason,
		Issue:  ctx.Issue,
		Repo:   ctx.Repo,
		Branch: ctx.Branch,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("gh-guard journal: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("gh-guard journal: write: %w", err)
	}
	return nil
}

// ReadJournal reads all entries from a guard journal file. A missing file
// (no denies were ever recorded for this task) returns a nil slice and no
// error. Malformed lines are skipped rather than failing the whole read —
// this is best-effort evidence for a human operator, not a correctness-
// critical data path.
func ReadJournal(path string) ([]JournalEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gh-guard journal: read: %w", err)
	}
	var entries []JournalEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// RemoveJournal deletes the journal file at path, if present. Called by the
// daemon after ingesting denies for a completed task so a retried task_id
// doesn't re-report stale entries from an earlier attempt.
func RemoveJournal(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gh-guard journal: remove: %w", err)
	}
	return nil
}
