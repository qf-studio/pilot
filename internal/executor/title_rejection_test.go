package executor

import (
	"strings"
	"testing"
)

func TestTitleRejectionTracker_SameTitleIncrements(t *testing.T) {
	tr := newTitleRejectionTracker()
	if got := tr.record("GH-1", "bad title"); got != 1 {
		t.Fatalf("first record = %d, want 1", got)
	}
	if got := tr.record("GH-1", "bad title"); got != 2 {
		t.Fatalf("second record = %d, want 2", got)
	}
	if got := tr.record("GH-1", "bad title"); got != 3 {
		t.Fatalf("third record = %d, want 3", got)
	}
}

func TestTitleRejectionTracker_TitleChangeResets(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title one")
	tr.record("GH-1", "bad title one")
	if got := tr.record("GH-1", "bad title two"); got != 1 {
		t.Fatalf("title change record = %d, want 1 (reset)", got)
	}
}

func TestTitleRejectionTracker_WhitespaceInsensitive(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title")
	if got := tr.record("GH-1", "  bad title  "); got != 2 {
		t.Fatalf("whitespace-only diff record = %d, want 2", got)
	}
}

func TestTitleRejectionTracker_Clear(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title")
	tr.clear("GH-1")
	if got := tr.record("GH-1", "bad title"); got != 1 {
		t.Fatalf("after clear, record = %d, want 1", got)
	}
}

func TestTitleRejectionTracker_PerIssueIsolation(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "title a")
	tr.record("GH-1", "title a")
	if got := tr.record("GH-2", "title a"); got != 1 {
		t.Fatalf("cross-issue record = %d, want 1", got)
	}
}

func TestSuggestConventionalTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		labels  []string
		wantPfx string // prefix we expect the suggestion to start with
	}{
		{"fix verb", "Fix null pointer in handler", nil, "fix"},
		{"add verb", "Add rate limiting", nil, "feat(repo): "},
		{"migrate verb", "Migrate alekspetrov/pilot references", nil, "chore(repo): "},
		{"label bug", "improve validation", []string{"bug"}, "fix: "},
		{"label enhancement", "retries", []string{"enhancement"}, "feat: "},
		{"unknown verb falls back to chore", "xyzzy the thing", nil, "chore(repo): "},
		{"empty title", "", nil, "chore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := suggestConventionalTitle(tt.title, tt.labels)
			if !strings.HasPrefix(got, tt.wantPfx) {
				t.Fatalf("suggest(%q,%v) = %q, want prefix %q", tt.title, tt.labels, got, tt.wantPfx)
			}
			// The suggestion itself should satisfy the conventional-commit regex
			// (otherwise we'd loop users back to the same error).
			if tt.title != "" {
				if err := validatePRTitle(got); err != nil {
					t.Fatalf("suggestion %q does not pass validation: %v", got, err)
				}
			}
		})
	}
}

func TestBuildTitleRejectionComment_ContainsKeyElements(t *testing.T) {
	comment := buildTitleRejectionComment(2175, "Migrate all alekspetrov/pilot references to qf-studio/pilot", nil)

	musts := []string{
		"Pilot can't open a PR",
		"Current title",
		"Migrate all alekspetrov/pilot references to qf-studio/pilot",
		"Suggested rewrite",
		"gh issue edit 2175",
		"--remove-label pilot-failed",
		"--remove-label pilot-title-rejected",
		"--add-label pilot-retry-ready",
		"conventionalcommits.org",
	}
	for _, m := range musts {
		if !strings.Contains(comment, m) {
			t.Errorf("comment missing %q\n---\n%s", m, comment)
		}
	}
}

func TestHashTitle_StableAndTrimmed(t *testing.T) {
	if hashTitle("hello") != hashTitle("  hello  ") {
		t.Error("hashTitle should trim whitespace")
	}
	if hashTitle("hello") == hashTitle("Hello") {
		t.Error("hashTitle should be case-sensitive")
	}
}
