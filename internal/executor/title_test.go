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
			name:    "non-conventional with no useful labels aborts",
			title:   "add rate limiting",
			labels:  []string{"pilot", "triage"},
			wantErr: true,
		},
		{
			name:    "analysis-style title aborts",
			title:   `Dispatcher recoverStaleTasks() already marks orphans as "failed"`,
			labels:  []string{"pilot"},
			wantErr: true,
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
			got, err := normalizeTitle(tt.title, tt.labels)
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
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			// Result must itself validate.
			if verr := validatePRTitle(got); verr != nil {
				t.Fatalf("normalized title failed validation: %v", verr)
			}
		})
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
