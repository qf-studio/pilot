package ghguard

import (
	"path/filepath"
	"testing"
)

func TestAppendAndReadJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "GH-4671.jsonl")
	ctx := TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"}

	entries, err := ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal on missing file: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries for missing file, got %d", len(entries))
	}

	v1 := deny("closes issue lifecycle state")
	if err := AppendDenyToJournal(path, []string{"issue", "close", "4649"}, ctx, v1); err != nil {
		t.Fatalf("AppendDenyToJournal #1: %v", err)
	}
	v2 := deny("targets issue #4649, task is scoped to issue #4671")
	if err := AppendDenyToJournal(path, []string{"issue", "comment", "4649", "--body", "closing"}, ctx, v2); err != nil {
		t.Fatalf("AppendDenyToJournal #2: %v", err)
	}

	entries, err = ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Reason != v1.Reason || entries[1].Reason != v2.Reason {
		t.Errorf("entries reasons = %q, %q; want %q, %q", entries[0].Reason, entries[1].Reason, v1.Reason, v2.Reason)
	}
	if entries[0].Issue != "4671" || entries[0].Repo != "qf-studio/pilot" || entries[0].Branch != "pilot/GH-4671" {
		t.Errorf("entry context not recorded: %+v", entries[0])
	}

	if err := RemoveJournal(path); err != nil {
		t.Fatalf("RemoveJournal: %v", err)
	}
	entries, err = ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal after remove: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries after remove, got %d", len(entries))
	}
	// Removing an already-absent journal must not error.
	if err := RemoveJournal(path); err != nil {
		t.Fatalf("RemoveJournal on missing file: %v", err)
	}
}

func TestAppendDenyToJournal_EmptyPathIsNoOp(t *testing.T) {
	if err := AppendDenyToJournal("", []string{"issue", "close", "1"}, TaskContext{}, deny("x")); err != nil {
		t.Fatalf("AppendDenyToJournal with empty path: %v", err)
	}
}
