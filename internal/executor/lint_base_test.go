package executor

import (
	"slices"
	"strings"
	"testing"
)

// TestLintBaseRefFollowsTaskBaseBranch covers GH-5695: golangci-lint compares
// against origin/<task base branch>, not a hardcoded origin/main.
func TestLintBaseRefFollowsTaskBaseBranch(t *testing.T) {
	tests := []struct {
		name       string
		baseBranch string
		wantFlag   string
	}{
		{name: "mainline", baseBranch: "mainline", wantFlag: "--new-from-rev=origin/mainline"},
		{name: "slashed branch", baseBranch: "release/1.x", wantFlag: "--new-from-rev=origin/release/1.x"},
		{name: "empty falls back to main", baseBranch: "", wantFlag: "--new-from-rev=origin/main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if args := goLintArgs(tt.baseBranch); !slices.Contains(args, tt.wantFlag) {
				t.Errorf("goLintArgs(%q) = %v, want it to contain %q", tt.baseBranch, args, tt.wantFlag)
			}

			r := &Runner{}
			prompt := r.buildSelfReviewPrompt(&Task{ID: "GH-1", Title: "t", BaseBranch: tt.baseBranch})
			if !strings.Contains(prompt, "golangci-lint run "+tt.wantFlag+" ./...") {
				t.Errorf("self-review prompt missing lint command with %q", tt.wantFlag)
			}
		})
	}
}
