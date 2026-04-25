package main

import (
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

// GH-2402: classifyFailureLabel routes permanent failures to LabelBlocked
// (no auto-retry) and transient failures to LabelFailed (auto-retriable).
func TestClassifyFailureLabel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"transient generic", errors.New("network blip"), github.LabelFailed},
		{"transient rate-limit", errors.New("hit your limit, resets 6am (UTC)"), github.LabelFailed},
		{"permanent cross-project", errors.New("cross-project execution blocked: foo"), github.LabelBlocked},
		{"permanent permission denied", errors.New("permission denied"), github.LabelBlocked},
		{"permanent permission check", errors.New("permission check failed: missing role"), github.LabelBlocked},
		{"permanent preflight", errors.New("pre-flight check failed: dirty repo"), github.LabelBlocked},
		{"permanent worktree", errors.New("worktree creation failed: disk full"), github.LabelBlocked},
		{"permanent navigator", errors.New("navigator worktree setup failed: missing dir"), github.LabelBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFailureLabel(tt.err)
			if got != tt.want {
				t.Errorf("classifyFailureLabel(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
