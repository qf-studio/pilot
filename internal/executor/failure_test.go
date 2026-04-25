package executor

import (
	"errors"
	"testing"
)

func TestIsPermanentFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"empty message", errors.New(""), false},
		{"random transient", errors.New("connection reset by peer"), false},
		{"rate limit not permanent", errors.New("hit your limit, resets 6am (UTC)"), false},

		{"cross-project blocked", errors.New("cross-project execution blocked: repo mismatch"), true},
		{"permission check failed", errors.New("permission check failed: missing role"), true},
		{"permission denied bare", errors.New("permission denied"), true},
		{"preflight failed", errors.New("pre-flight check failed: dirty git state"), true},
		{"worktree creation failed", errors.New("worktree creation failed: disk full"), true},
		{"navigator worktree setup failed", errors.New("navigator worktree setup failed: missing config"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsPermanentFailure(tt.err)
			if got != tt.want {
				t.Errorf("IsPermanentFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
