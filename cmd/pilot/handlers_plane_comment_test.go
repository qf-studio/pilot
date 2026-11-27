package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

// TestPlaneResultComment pins the one-comment-per-event rules of the Plane
// handler (TASK-658, GH-6741): a run with a pull request posts no comment of
// its own, since the SDK poller posts the pull request comment.
func TestPlaneResultComment(t *testing.T) {
	const branch = "pilot/PLANE-42"
	const prURL = "https://github.com/org/repo/pull/7"

	tests := []struct {
		name         string
		execErr      error
		hr           *HandlerResult
		wantPost     bool
		wantContains string
		wantNoChange bool
	}{
		{
			name:         "execution error posts the failure comment",
			execErr:      errors.New("dispatch blew up"),
			hr:           &HandlerResult{},
			wantPost:     true,
			wantContains: "Pilot execution failed:</p><pre>dispatch blew up</pre>",
		},
		{
			name: "success with no commit and no pull request posts the no-change comment",
			hr: &HandlerResult{Success: true, Result: &executor.ExecutionResult{
				Success: true, Duration: 3 * time.Second,
			}},
			wantPost:     true,
			wantContains: "no changes were made",
			wantNoChange: true,
		},
		{
			name: "success with a pull request posts no comment",
			hr: &HandlerResult{Success: true, PRNumber: 7, PRURL: prURL, Result: &executor.ExecutionResult{
				Success: true, CommitSHA: "abc123", PRUrl: prURL,
			}},
			wantPost: false,
		},
		{
			name: "success with a commit and no pull request posts the success comment",
			hr: &HandlerResult{Success: true, Result: &executor.ExecutionResult{
				Success: true, CommitSHA: "abc123",
			}},
			wantPost:     true,
			wantContains: "Pilot execution completed successfully",
		},
		{
			name: "epic posts the success comment",
			hr: &HandlerResult{Success: true, Result: &executor.ExecutionResult{
				Success: true, IsEpic: true,
			}},
			wantPost:     true,
			wantContains: "Branch: <code>" + branch + "</code>",
		},
		{
			name: "failed result posts the failure comment",
			hr: &HandlerResult{Result: &executor.ExecutionResult{
				Success: false, Error: "tests failed",
			}},
			wantPost:     true,
			wantContains: "<pre>tests failed</pre>",
		},
		{
			name: "terminal-by-design result posts no comment",
			hr: &HandlerResult{TerminalByDesign: true, Result: &executor.ExecutionResult{
				Success: false, Error: "superseded",
			}},
			wantPost: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment, post := planeResultComment(tt.execErr, tt.hr, branch)
			if post != tt.wantPost {
				t.Fatalf("planeResultComment post = %v, want %v (comment %q)", post, tt.wantPost, comment)
			}
			if !post && comment != "" {
				t.Errorf("planeResultComment comment = %q, want empty when nothing is posted", comment)
			}
			if tt.wantContains != "" && !strings.Contains(comment, tt.wantContains) {
				t.Errorf("planeResultComment comment = %q, want it to contain %q", comment, tt.wantContains)
			}
			if got := planeNoChanges(tt.execErr, tt.hr); got != tt.wantNoChange {
				t.Errorf("planeNoChanges = %v, want %v", got, tt.wantNoChange)
			}
		})
	}
}
