package ghguard

import (
	"os"
	"path/filepath"
	"testing"
)

func testIdentity() Identity {
	return Identity{
		TaskIssue:  "4671",
		TaskRepo:   "qf-studio/pilot",
		TaskBranch: "pilot/GH-4671",
		RealGh:     "/usr/bin/gh",
	}
}

func TestClassify(t *testing.T) {
	id := testIdentity()

	tests := []struct {
		name        string
		args        []string
		wantVerdict Verdict
	}{
		// --- unconditional reads ---
		{"issue view own", []string{"issue", "view", "4671"}, VerdictAllow},
		{"issue view other issue same repo", []string{"issue", "view", "1"}, VerdictAllow},
		{"issue list", []string{"issue", "list"}, VerdictAllow},
		{"issue list with flags", []string{"issue", "list", "--state", "open", "--label", "pilot"}, VerdictAllow},
		{"pr view", []string{"pr", "view", "42"}, VerdictAllow},
		{"pr list", []string{"pr", "list"}, VerdictAllow},
		{"pr checks", []string{"pr", "checks", "42"}, VerdictAllow},
		{"pr diff", []string{"pr", "diff", "42"}, VerdictAllow},
		{"run view", []string{"run", "view", "12345"}, VerdictAllow},
		{"run list", []string{"run", "list"}, VerdictAllow},
		{"auth status", []string{"auth", "status"}, VerdictAllow},
		{"issue view with matching -R short", []string{"issue", "view", "1", "-R", "qf-studio/pilot"}, VerdictAllow},
		{"issue view with matching --repo long", []string{"issue", "view", "1", "--repo", "qf-studio/pilot"}, VerdictAllow},
		{"issue view with matching --repo=value", []string{"issue", "view", "1", "--repo=qf-studio/pilot"}, VerdictAllow},

		// --- gh api reads ---
		{"api GET implicit", []string{"api", "repos/qf-studio/pilot/issues/1"}, VerdictAllow},
		{"api GET explicit -X", []string{"api", "repos/qf-studio/pilot/issues/1", "-X", "GET"}, VerdictAllow},
		{"api GET explicit --method lowercase", []string{"api", "repos/qf-studio/pilot/issues/1", "--method", "get"}, VerdictAllow},

		// --- own-artifact allows ---
		{"pr create no head (current branch)", []string{"pr", "create", "--title", "t", "--body", "b"}, VerdictAllow},
		{"pr create head matches task branch", []string{"pr", "create", "--head", "pilot/GH-4671", "--title", "t"}, VerdictAllow},
		{"issue comment own issue", []string{"issue", "comment", "4671", "--body", "hi"}, VerdictAllow},
		{"issue comment own issue via URL", []string{"issue", "comment", "https://github.com/qf-studio/pilot/issues/4671", "--body", "hi"}, VerdictAllow},
		{"pr comment no target (current branch)", []string{"pr", "comment", "--body", "hi"}, VerdictAllow},
		{"pr comment target matches task branch", []string{"pr", "comment", "pilot/GH-4671", "--body", "hi"}, VerdictAllow},

		// --- hard denies: issue lifecycle mutation ---
		{"issue close", []string{"issue", "close", "4671"}, VerdictDeny},
		{"issue reopen", []string{"issue", "reopen", "1"}, VerdictDeny},
		{"issue edit", []string{"issue", "edit", "4671", "--title", "x"}, VerdictDeny},
		{"issue lock", []string{"issue", "lock", "4671"}, VerdictDeny},
		{"issue transfer", []string{"issue", "transfer", "4671", "other/repo"}, VerdictDeny},
		{"issue delete", []string{"issue", "delete", "4671"}, VerdictDeny},

		// --- hard denies: label mutation ---
		{"issue edit add-label", []string{"issue", "edit", "4671", "--add-label", "pilot-superseded"}, VerdictDeny},
		{"issue edit remove-label", []string{"issue", "edit", "4671", "--remove-label", "bug"}, VerdictDeny},
		{"pr edit add-label", []string{"pr", "edit", "42", "--add-label", "x"}, VerdictDeny},

		// --- hard denies: cross-issue/PR number on own-artifact commands ---
		{"issue comment on sibling issue", []string{"issue", "comment", "1", "--body", "hi"}, VerdictDeny},
		{"pr create head mismatched branch", []string{"pr", "create", "--head", "some-other-branch", "--title", "t"}, VerdictDeny},
		{"pr comment on numeric target", []string{"pr", "comment", "99", "--body", "hi"}, VerdictDeny},
		{"pr comment on URL target", []string{"pr", "comment", "https://github.com/qf-studio/pilot/pull/99", "--body", "hi"}, VerdictDeny},

		// --- hard denies: -R/--repo mismatch (even on read commands) ---
		{"issue view wrong repo short flag", []string{"issue", "view", "1", "-R", "qf-studio/upstream"}, VerdictDeny},
		{"issue view wrong repo long flag", []string{"issue", "view", "1", "--repo", "qf-studio/upstream"}, VerdictDeny},
		{"issue view wrong repo equals form", []string{"issue", "view", "1", "--repo=qf-studio/upstream"}, VerdictDeny},
		{"pr list wrong repo", []string{"pr", "list", "-R", "someone/else"}, VerdictDeny},

		// --- hard denies: gh api non-GET / implicit mutation ---
		{"api POST explicit -X", []string{"api", "repos/o/r/issues/1/labels", "-X", "POST", "-f", "labels[]=x"}, VerdictDeny},
		{"api PATCH via --method", []string{"api", "repos/o/r/issues/1", "--method", "PATCH"}, VerdictDeny},
		{"api DELETE", []string{"api", "repos/o/r/issues/1", "-X", "DELETE"}, VerdictDeny},
		{"api with -f data flag even without -X", []string{"api", "repos/o/r/issues/1", "-f", "state=closed"}, VerdictDeny},
		{"api with -F data flag", []string{"api", "graphql", "-F", "query=x"}, VerdictDeny},
		{"api with --input flag", []string{"api", "repos/o/r/issues", "--input", "payload.json"}, VerdictDeny},

		// --- hard denies: whole command families ---
		{"release create", []string{"release", "create", "v1.0.0"}, VerdictDeny},
		{"release delete", []string{"release", "delete", "v1.0.0"}, VerdictDeny},
		{"repo delete", []string{"repo", "delete", "qf-studio/pilot"}, VerdictDeny},
		{"repo edit", []string{"repo", "edit", "--description", "x"}, VerdictDeny},
		{"secret set", []string{"secret", "set", "FOO"}, VerdictDeny},
		{"variable set", []string{"variable", "set", "FOO"}, VerdictDeny},
		{"workflow run", []string{"workflow", "run", "deploy.yml"}, VerdictDeny},
		{"workflow disable", []string{"workflow", "disable", "deploy.yml"}, VerdictDeny},

		// --- default deny for anything unlisted ---
		{"gist create", []string{"gist", "create", "file.txt"}, VerdictDeny},
		{"empty argv", []string{}, VerdictDeny},
		{"issue comment missing target", []string{"issue", "comment", "--body", "hi"}, VerdictDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(id, tt.args)
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("Classify(%v) = %s (reason: %q), want %s", tt.args, got.Verdict, got.Reason, tt.wantVerdict)
			}
			if got.Verdict == VerdictDeny {
				if got.Reason == "" {
					t.Errorf("deny decision missing Reason")
				}
				if got.Allowed == "" {
					t.Errorf("deny decision missing Allowed hint")
				}
			}
		})
	}
}

// TestClassify_IncompleteIdentity verifies that when TaskRepo/TaskBranch/
// TaskIssue are empty (identity incomplete), repo-mismatch checks are
// skipped (unenforceable) but own-artifact checks still deny rather than
// guess, and hard denies are unaffected.
func TestClassify_IncompleteIdentity(t *testing.T) {
	id := Identity{} // fully empty

	// Reads still work — the allowlist doesn't require identity.
	if got := Classify(id, []string{"issue", "view", "1"}); got.Verdict != VerdictAllow {
		t.Errorf("read with empty identity: got %s, want allow", got.Verdict)
	}

	// Own-artifact still denies — can't confirm ownership without identity.
	if got := Classify(id, []string{"issue", "comment", "1", "--body", "hi"}); got.Verdict != VerdictDeny {
		t.Errorf("issue comment with empty identity: got %s, want deny", got.Verdict)
	}
	if got := Classify(id, []string{"pr", "create", "--head", "some-branch"}); got.Verdict != VerdictDeny {
		t.Errorf("pr create --head with empty identity: got %s, want deny", got.Verdict)
	}

	// pr create with no --head at all still allows (defaults to current branch).
	if got := Classify(id, []string{"pr", "create", "--title", "t"}); got.Verdict != VerdictAllow {
		t.Errorf("pr create no --head with empty identity: got %s, want allow", got.Verdict)
	}
}

func TestExtractIssueOrPRNumber(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"42", "42", true},
		{"0", "0", true},
		{"https://github.com/qf-studio/pilot/issues/4671", "4671", true},
		{"https://github.com/qf-studio/pilot/pull/99", "99", true},
		{"https://github.com/qf-studio/pilot/pull/99/", "99", true},
		{"https://github.com/qf-studio/pilot/pull/99?tab=files", "99", true},
		{"some-branch-name", "", false},
		{"", "", false},
		{"pilot/GH-4671", "", false},
	}
	for _, tt := range tests {
		got, ok := extractIssueOrPRNumber(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("extractIssueOrPRNumber(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestParseArgs_FlagAliases(t *testing.T) {
	p := parseArgs([]string{"issue", "view", "1", "-R", "o/r"})
	if !p.repoGiven || p.repo != "o/r" {
		t.Errorf("-R short flag not parsed: %+v", p)
	}

	p = parseArgs([]string{"issue", "view", "1", "--repo", "o/r"})
	if !p.repoGiven || p.repo != "o/r" {
		t.Errorf("--repo long flag not parsed: %+v", p)
	}

	p = parseArgs([]string{"api", "x", "--method", "POST"})
	if p.method != "POST" {
		t.Errorf("--method not parsed: %+v", p)
	}

	p = parseArgs([]string{"api", "x", "-X", "DELETE"})
	if p.method != "DELETE" {
		t.Errorf("-X short flag not parsed: %+v", p)
	}
}

func TestJournal_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")

	// Missing file reads as empty, not an error.
	entries, err := ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal on missing file: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}

	e1 := JournalEntry{Verdict: VerdictDeny, Reason: "test deny 1", Args: []string{"issue", "close", "1"}, TaskIssue: "4671", TaskRepo: "qf-studio/pilot"}
	e2 := JournalEntry{Verdict: VerdictDeny, Reason: "test deny 2", Args: []string{"release", "create", "v1"}, TaskIssue: "4671", TaskRepo: "qf-studio/pilot"}

	if err := AppendJournal(path, e1); err != nil {
		t.Fatalf("AppendJournal e1: %v", err)
	}
	if err := AppendJournal(path, e2); err != nil {
		t.Fatalf("AppendJournal e2: %v", err)
	}

	entries, err = ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Reason != e1.Reason || entries[1].Reason != e2.Reason {
		t.Errorf("journal entries out of order or corrupted: %+v", entries)
	}
}

func TestResolveFallbackGh_ExcludesShimDir(t *testing.T) {
	dir := t.TempDir()
	shimDir := filepath.Join(dir, "shim")
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A "gh" in the shim dir (simulating the shim itself sitting on PATH).
	writeExecutable(t, filepath.Join(shimDir, "gh"), "#!/bin/sh\necho shim\n")
	// The "real" gh elsewhere on PATH.
	writeExecutable(t, filepath.Join(realDir, "gh"), "#!/bin/sh\necho real\n")

	oldPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", oldPath) }()
	_ = os.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)

	got, err := ResolveFallbackGh(shimDir)
	if err != nil {
		t.Fatalf("ResolveFallbackGh: %v", err)
	}
	if got != filepath.Join(realDir, "gh") {
		t.Errorf("ResolveFallbackGh = %q, want %q (should skip shim dir)", got, filepath.Join(realDir, "gh"))
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
