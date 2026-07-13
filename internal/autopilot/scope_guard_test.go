package autopilot

import (
	"log/slog"
	"strings"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestExtractTypeScope(t *testing.T) {
	cases := []struct {
		title           string
		wantType, wantS string
	}{
		{"feat(auth): add OAuth", "feat", "auth"},
		{"fix(upgrade): cherry-pick atomic binary replacement", "fix", "upgrade"},
		{"chore: bump deps", "", ""}, // missing scope
		{"refactor(internal/executor): clean", "refactor", "internal/executor"},
		{"feat(auth)!: breaking change", "feat", "auth"},
		{"random title", "", ""},
		{"", "", ""},
		// GH-3827: worker PRs are titled with a leading issue-ref tag.
		{"GH-3785: fix(executor): retry backoff", "fix", "executor"},
		{"GH-3785: feat(auth): add OAuth", "feat", "auth"},
		{"gh-3785: fix(executor): case-insensitive tag", "fix", "executor"},
		{"JIRA-123: chore: bump deps", "", ""}, // missing scope, still no match
		{"GH-3785:fix(executor): no space after colon", "fix", "executor"},
	}
	for _, tc := range cases {
		gotType, gotS := extractTypeScope(tc.title)
		if gotType != tc.wantType || gotS != tc.wantS {
			t.Errorf("extractTypeScope(%q) = (%q,%q), want (%q,%q)",
				tc.title, gotType, gotS, tc.wantType, tc.wantS)
		}
	}
}

func TestScopeDriftReason(t *testing.T) {
	cases := []struct {
		name, prTitle, issueTitle string
		wantContains              string // empty = expect no drift
	}{
		{
			name:         "cascade-2 entry: type+scope mismatch",
			prTitle:      "feat(auth): add OAuth provider integration",
			issueTitle:   "fix(upgrade): cherry-pick atomic binary replacement",
			wantContains: "type",
		},
		{
			name:         "scope mismatch only",
			prTitle:      "feat(memory): X",
			issueTitle:   "feat(executor): Y",
			wantContains: "scope",
		},
		{
			name:       "matching prefix — no drift",
			prTitle:    "fix(executor): tweak",
			issueTitle: "fix(executor): broader description",
		},
		{
			name:       "matching type, scope unset on issue side — abstain",
			prTitle:    "fix(executor): X",
			issueTitle: "fix: X",
		},
		{
			name:       "PR has no conventional prefix — abstain (other validators handle title)",
			prTitle:    "raw title",
			issueTitle: "feat(auth): X",
		},
		// GH-3827: worker PRs are titled "GH-NNNN: type(scope): ..." — the gate
		// must strip the issue-ref tag rather than abstain on every such PR.
		{
			name:         "GH-NNNN prefix on PR title — type mismatch still escalates",
			prTitle:      "GH-3785: feat(auth): add OAuth provider integration",
			issueTitle:   "fix(executor): cherry-pick atomic binary replacement",
			wantContains: "type",
		},
		{
			name:         "GH-NNNN prefix on PR title — scope mismatch still escalates",
			prTitle:      "GH-3785: fix(memory): X",
			issueTitle:   "fix(executor): Y",
			wantContains: "scope",
		},
		{
			name:       "GH-NNNN prefix on PR title — matching type/scope, no drift",
			prTitle:    "GH-3785: fix(executor): retry backoff tweak",
			issueTitle: "fix(executor): retry backoff bug",
		},
		{
			name:       "GH-NNNN prefix on both titles — matching, no drift",
			prTitle:    "GH-3796: fix(executor): retry backoff tweak",
			issueTitle: "GH-3785: fix(executor): retry backoff bug",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeDriftReason(nil, tc.prTitle, tc.issueTitle)
			if tc.wantContains == "" && got != "" {
				t.Errorf("expected no drift, got %q", got)
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("want reason containing %q, got %q", tc.wantContains, got)
			}
		})
	}
}

// TestScopeDriftReason_AbstainLogsReason verifies GH-3827: a genuinely
// unparseable title (no conventional-commit shape at all) must be logged at
// INFO instead of abstaining silently.
func TestScopeDriftReason_AbstainLogsReason(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	got := ScopeDriftReason(logger, "raw title with no conventional shape", "also raw")
	if got != "" {
		t.Fatalf("expected abstain (empty reason), got %q", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "abstained") {
		t.Errorf("expected abstention to be logged at INFO, got log output: %q", logged)
	}
	if !strings.Contains(logged, "level=INFO") {
		t.Errorf("expected log level INFO, got: %q", logged)
	}
}

func TestSizeFloorReason(t *testing.T) {
	cases := []struct {
		name  string
		files []*github.PRFile
		want  bool // true if a reason is expected
	}{
		{
			name: "small PR — no floor",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 50, Deletions: 10},
			},
		},
		{
			name: "500 LoC exactly — no floor (strict >)",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 500},
			},
		},
		{
			name: "501 LoC — floor fires",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 501},
			},
			want: true,
		},
		{
			name: "routine multi-file fix with tests (#3559 class, ~450) — no floor",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 110},
				{Filename: "internal/autopilot/controller_test.go", Status: "modified", Additions: 340},
			},
		},
		{
			// GH-4284: with test additions now excluded (see productionAdditions),
			// this fixture's 272 production lines no longer cross the 500
			// threshold on its own — cascade-2 was actually caught by
			// ScopeDriftReason (title type/scope mismatch, see file header),
			// which remains the primary gate for this incident class.
			name: "cascade-2 contaminating PR (~512 LoC incl. 240 test LoC) — no floor once tests excluded",
			files: []*github.PRFile{
				{Filename: "internal/gateway/oauth.go", Status: "added", Additions: 244},
				{Filename: "internal/gateway/oauth_test.go", Status: "added", Additions: 240},
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 24},
				{Filename: "cmd/pilot/main.go", Status: "modified", Additions: 3},
				{Filename: "internal/config/config.go", Status: "modified", Additions: 1},
			},
		},
		{
			name: "cascade-2 shape with genuinely large production code (~512 LoC, no tests) — floor fires",
			files: []*github.PRFile{
				{Filename: "internal/gateway/oauth.go", Status: "added", Additions: 484},
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 24},
				{Filename: "cmd/pilot/main.go", Status: "modified", Additions: 3},
				{Filename: "internal/config/config.go", Status: "modified", Additions: 1},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SizeFloorReason(tc.files)
			if tc.want && got == "" {
				t.Errorf("expected size-floor to fire, got empty reason")
			}
			if !tc.want && got != "" {
				t.Errorf("expected no size-floor fire, got %q", got)
			}
		})
	}
}

// TestIsBookkeepingPath covers GH-4055 path classification: Navigator
// bookkeeping/generated content under .agent/** (including nested paths and
// the knowledge graph) must be recognized separately from shipped code.
func TestIsBookkeepingPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/autopilot/controller.go", false},
		{"cmd/pilot/main.go", false},
		{".agent/tasks/foo.md", true},
		{".agent/knowledge/graph.json", true},
		{".agent", true},
		{".agentfoo/bar.go", false}, // must not match by prefix-of-name alone
	}
	for _, tc := range cases {
		if got := isBookkeepingPath(tc.path); got != tc.want {
			t.Errorf("isBookkeepingPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestSizeFloorReason_BookkeepingExclusion covers GH-4055: .agent/** additions
// (Navigator task docs, knowledge graph regen) must not count toward the
// size-floor gate, while ordinary code additions still do.
func TestSizeFloorReason_BookkeepingExclusion(t *testing.T) {
	cases := []struct {
		name  string
		files []*github.PRFile
		want  bool // true if a reason is expected
	}{
		{
			// PR #4047 scenario: 586 total additions, 305 code + 281 bookkeeping.
			// Previously escalated on the 586 net-additions total; must not now.
			name: "PR #4047 scenario: 305 code + 281 bookkeeping — no escalation",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 305},
				{Filename: ".agent/knowledge/graph.json", Status: "modified", Additions: 281},
			},
		},
		{
			name: "305 code + 400 code — escalates",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 305},
				{Filename: "internal/autopilot/scope_guard.go", Status: "modified", Additions: 400},
			},
			want: true,
		},
		{
			name: "0 code + 800 bookkeeping — no escalation",
			files: []*github.PRFile{
				{Filename: ".agent/tasks/TASK-999-foo.md", Status: "added", Additions: 800},
			},
		},
		{
			name: "threshold boundary: 500 code — no escalation",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 500},
			},
		},
		{
			name: "threshold boundary: 501 code — escalates",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 501},
			},
			want: true,
		},
		{
			name: "mixed nested bookkeeping paths correctly excluded",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 300},
				{Filename: ".agent/tasks/foo.md", Status: "added", Additions: 250},
				{Filename: ".agent/knowledge/graph.json", Status: "modified", Additions: 250},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SizeFloorReason(tc.files)
			if tc.want && got == "" {
				t.Errorf("expected size-floor to fire, got empty reason")
			}
			if !tc.want && got != "" {
				t.Errorf("expected no size-floor fire, got %q", got)
			}
		})
	}
}

// TestSizeFloorReason_TestExclusion covers GH-4284: `_test.go` additions must
// be excluded from the size-floor gate the same way `.agent/**` bookkeeping
// already is (GH-4055) — a well-tested PR should not escalate on test bulk alone.
func TestSizeFloorReason_TestExclusion(t *testing.T) {
	cases := []struct {
		name  string
		files []*github.PRFile
		want  bool // true if a reason is expected
	}{
		{
			// #4279 shape: 90 production, 290 test, 28 bookkeeping — well
			// under the 500-line SizeFloorThreshold on production alone.
			name: "GH-4279 shape: 90 production + 290 test + 28 bookkeeping — no escalation",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/dispatcher.go", Status: "modified", Additions: 90},
				{Filename: "internal/autopilot/dispatcher_gh4276_test.go", Status: "added", Additions: 114},
				{Filename: "internal/memory/store_test.go", Status: "modified", Additions: 103},
				{Filename: "internal/autopilot/dispatcher_test.go", Status: "modified", Additions: 73},
				{Filename: ".agent/knowledge/memories/pitfalls/gh4276.md", Status: "added", Additions: 28},
			},
		},
		{
			name: "501 production + 1000 test — escalates on production alone",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller.go", Status: "modified", Additions: 501},
				{Filename: "internal/autopilot/controller_test.go", Status: "modified", Additions: 1000},
			},
			want: true,
		},
		{
			name: "0 production + 900 test — no escalation",
			files: []*github.PRFile{
				{Filename: "internal/autopilot/controller_test.go", Status: "added", Additions: 900},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SizeFloorReason(tc.files)
			if tc.want && got == "" {
				t.Errorf("expected size-floor to fire, got empty reason")
			}
			if !tc.want && got != "" {
				t.Errorf("expected no size-floor fire, got %q", got)
			}
		})
	}
}

// TestIsTestPath covers GH-4284 test-file classification.
func TestIsTestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/autopilot/controller.go", false},
		{"internal/autopilot/controller_test.go", true},
		{".agent/tasks/foo.md", false},
		{"internal/foo/bar_test.go", true},
		{"internal/foo/testdata/fixture.go", false},
	}
	for _, tc := range cases {
		if got := isTestPath(tc.path); got != tc.want {
			t.Errorf("isTestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestProductionAdditions covers GH-4284: the shared helper used by both
// SizeFloorReason and the CI-fix size guard must classify each file into
// exactly one bucket (production, bookkeeping, or test).
func TestProductionAdditions(t *testing.T) {
	files := []*github.PRFile{
		{Filename: "internal/autopilot/dispatcher.go", Additions: 90},
		{Filename: "internal/autopilot/dispatcher_test.go", Additions: 65},
		{Filename: ".agent/tasks/foo.md", Additions: 28},
	}
	production, bookkeeping, test := productionAdditions(files)
	if production != 90 {
		t.Errorf("production = %d, want 90", production)
	}
	if bookkeeping != 28 {
		t.Errorf("bookkeeping = %d, want 28", bookkeeping)
	}
	if test != 65 {
		t.Errorf("test = %d, want 65", test)
	}
}
