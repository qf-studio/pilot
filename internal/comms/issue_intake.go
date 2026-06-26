package comms

import (
	"context"
	"time"
)

// IssueDraft holds the LLM-generated fields for a new GitHub issue.
type IssueDraft struct {
	Title  string
	Body   string
	Labels []string
}

// IssueCreator creates a GitHub issue from a draft.
// Each adapter provides a concrete implementation that resolves
// the active project path to an owner/repo pair.
// Mirrors the MemberResolver DI pattern — keeps comms PM-agnostic.
type IssueCreator interface {
	// CreateIssue creates a GitHub issue and returns its HTML URL.
	// projectPath identifies the active project for owner/repo resolution.
	CreateIssue(ctx context.Context, projectPath string, d IssueDraft) (url string, err error)
}

// PendingIssue is a draft issue awaiting async creation (state machine entry,
// mirroring PendingTask).
type PendingIssue struct {
	Draft     IssueDraft
	ContextID string
	ThreadID  string
	CreatedAt time.Time
}
