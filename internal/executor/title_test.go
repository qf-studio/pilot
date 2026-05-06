package executor

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePRTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr bool
	}{
		{"plain fix", "fix: handle nil response", false},
		{"feat with scope", "feat(api): add rate limiting", false},
		{"breaking change", "feat(api)!: drop v1 endpoint", false},
		{"docs", "docs: update README", false},
		{"refactor", "refactor(executor): extract title validation", false},
		{"with issue prefix", "GH-2325: fix(git): validate PR title", false},
		{"linear-style prefix", "APP-123: feat(auth): oauth flow", false},
		{"no prefix at all", "add rate limiting", true},
		{"analysis-style title", `Dispatcher recoverStaleTasks() already marks orphans as "failed", not "completed"`, true},
		{"empty title", "", true},
		{"whitespace only", "   ", true},
		{"type without colon", "fix handle nil", true},
		{"unknown type", "wip: something", true},
		{"type with no subject", "fix:", true},
		{"type with only space", "fix: ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePRTitle(tt.title)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.title)
				}
				if !errors.Is(err, ErrNonConventionalTitle) {
					t.Fatalf("expected ErrNonConventionalTitle, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.title, err)
			}
		})
	}
}

func TestAutoPrefixTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		labels  []string
		want    string
		wantOK  bool
	}{
		{"bug label", "handle nil response", []string{"bug"}, "fix: handle nil response", true},
		{"enhancement label", "add rate limiting", []string{"enhancement"}, "feat: add rate limiting", true},
		{"docs label", "update README", []string{"docs"}, "docs: update README", true},
		{"refactor label", "extract parser", []string{"refactor"}, "refactor: extract parser", true},
		{"documentation variant", "update guide", []string{"documentation"}, "docs: update guide", true},
		{"first matching label wins", "fix X", []string{"pilot", "bug", "enhancement"}, "fix: fix X", true},
		{"case insensitive", "fix X", []string{"Bug"}, "fix: fix X", true},
		{"no matching label", "handle nil", []string{"pilot", "triage"}, "handle nil", false},
		{"empty labels", "handle nil", nil, "handle nil", false},
		{"empty title", "", []string{"bug"}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := autoPrefixTitle(tt.title, tt.labels)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		labels  []string
		diff    GitDiff
		want    string
		wantErr bool
	}{
		{
			name:  "already conventional is returned as-is",
			title: "fix(git): validate PR title",
			want:  "fix(git): validate PR title",
		},
		{
			name:   "auto-prefixed from bug label",
			title:  "handle nil response",
			labels: []string{"bug"},
			want:   "fix: handle nil response",
		},
		{
			name:   "auto-prefixed from enhancement label",
			title:  "add rate limiting",
			labels: []string{"enhancement"},
			want:   "feat: add rate limiting",
		},
		{
			name:   "label-miss + diff fallback produces chore",
			title:  "add rate limiting",
			labels: []string{"pilot", "triage"},
			diff:   GitDiff{},
			want:   "chore: add rate limiting",
		},
		{
			name:   "diff fallback: all md files → docs",
			title:  "update README",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{"README.md", "docs/guide.md"}},
			want:   "docs: update README",
		},
		{
			name:   "diff fallback: all test files → test",
			title:  "add coverage for parser",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{"internal/foo/bar_test.go"}},
			want:   "test: add coverage for parser",
		},
		{
			name:   "diff fallback: ci files → ci",
			title:  "add lint step",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{".github/workflows/ci.yml"}},
			want:   "ci: add lint step",
		},
		{
			name:   "diff fallback: build files → build",
			title:  "bump go version",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{"go.mod", "go.sum"}},
			want:   "build: bump go version",
		},
		{
			name:   "diff fallback: large net addition → feat",
			title:  "add new dashboard widget",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{"internal/dashboard/widget.go"}, Added: 200, Removed: 10},
			want:   "feat: add new dashboard widget",
		},
		{
			name:   "diff fallback: balanced changes → refactor",
			title:  "reorganise approval flow",
			labels: []string{"pilot"},
			diff:   GitDiff{Files: []string{"internal/executor/runner.go"}, Added: 50, Removed: 48},
			want:   "refactor: reorganise approval flow",
		},
		{
			name:    "empty title aborts",
			title:   "",
			labels:  []string{"bug"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTitle(tt.title, tt.labels, tt.diff)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if !errors.Is(err, ErrNonConventionalTitle) {
					t.Fatalf("expected ErrNonConventionalTitle, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != "" && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			// Result must itself validate.
			if verr := validatePRTitle(got); verr != nil {
				t.Fatalf("normalized title failed validation: %v", verr)
			}
		})
	}
}

func TestInferConventionalPrefix(t *testing.T) {
	tests := []struct {
		name   string
		diff   GitDiff
		labels []string
		want   string
	}{
		{
			name: "all md files → docs",
			diff: GitDiff{Files: []string{"README.md", "docs/api.md"}},
			want: "docs",
		},
		{
			name: "mdx files → docs",
			diff: GitDiff{Files: []string{"docs/index.mdx"}},
			want: "docs",
		},
		{
			name: "all _test.go → test",
			diff: GitDiff{Files: []string{"internal/foo/bar_test.go"}},
			want: "test",
		},
		{
			name: "all .test. files → test",
			diff: GitDiff{Files: []string{"src/component.test.ts"}},
			want: "test",
		},
		{
			name:   "label match overrides nothing (md files win first)",
			diff:   GitDiff{Files: []string{"docs/guide.md"}},
			labels: []string{"bug"},
			want:   "docs",
		},
		{
			name:   "label match when no file heuristic hits",
			diff:   GitDiff{Files: []string{"internal/foo/bar.go", "internal/baz/quux.go"}, Added: 5, Removed: 5},
			labels: []string{"enhancement"},
			want:   "feat",
		},
		{
			name: "ci workflow → ci",
			diff: GitDiff{Files: []string{".github/workflows/test.yml"}},
			want: "ci",
		},
		{
			name: "gitlab ci → ci",
			diff: GitDiff{Files: []string{".gitlab-ci.yml"}},
			want: "ci",
		},
		{
			name: "build files → build",
			diff: GitDiff{Files: []string{"go.mod", "go.sum"}},
			want: "build",
		},
		{
			name: "Makefile → build",
			diff: GitDiff{Files: []string{"Makefile"}},
			want: "build",
		},
		{
			name: "large net addition with code → feat",
			diff: GitDiff{Files: []string{"internal/foo/widget.go"}, Added: 100, Removed: 2},
			want: "feat",
		},
		{
			name: "balanced change with code → refactor",
			diff: GitDiff{Files: []string{"internal/foo/runner.go"}, Added: 50, Removed: 48},
			want: "refactor",
		},
		{
			name: "empty diff → chore",
			diff: GitDiff{},
			want: "chore",
		},
		{
			name:   "no useful label, no files → chore",
			diff:   GitDiff{},
			labels: []string{"pilot"},
			want:   "chore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferConventionalPrefix(tt.diff, tt.labels)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsPermanentFailure_TitleStringsNoLongerMatch(t *testing.T) {
	// GH-2735: these were removed from permanentFailurePatterns because
	// normalizeTitle now falls back to diff heuristics instead of erroring.
	removed := []string{
		"title is not a conventional commit",
		"could not auto-correct",
	}
	for _, s := range removed {
		if IsPermanentFailure(s) {
			t.Errorf("IsPermanentFailure(%q) = true; expected false after GH-2735 removal", s)
		}
	}
	// Broader umbrella pattern must still match.
	if !IsPermanentFailure("PR creation refused: some reason") {
		t.Error("IsPermanentFailure should still match 'PR creation refused'")
	}
}

func TestValidatePRTitle_GH2315Regression(t *testing.T) {
	// Real incident from GH-2315: LLM analysis text leaked into an issue
	// title which became a PR title and then the squash-merge commit (70c14dc5).
	bad := `Dispatcher recoverStaleTasks() (line 188) already marks orphans as "failed", not "completed". The status appears correct in the current code.`

	if err := validatePRTitle(bad); err == nil {
		t.Fatal("expected validation to reject analysis-style title")
	} else if !strings.Contains(err.Error(), "conventional commit") {
		t.Fatalf("error should mention conventional commit, got %v", err)
	}
}
