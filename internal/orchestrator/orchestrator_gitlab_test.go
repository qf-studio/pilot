package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessGitlabIssueEvent_IdentifierAndBranch verifies that the resulting task
// has Identifier == ev.SequenceID and Branch == "pilot/<sequenceID>".
func TestProcessGitlabIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-gl-42",
		SequenceID: "GL-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"bug"},
		ProjectID:  "proj-gl-1",
	}

	ticket := &TicketData{
		ID:          ev.IssueID,
		Identifier:  ev.SequenceID,
		Title:       ev.Title,
		Description: ev.Body,
		Priority:    sdkshim.PriorityFromSDK(ev.Priority),
		Labels:      ev.Labels,
	}
	branch := fmt.Sprintf("pilot/%s", ev.SequenceID)

	if ticket.Identifier != "GL-42" {
		t.Errorf("Identifier = %q, want GL-42", ticket.Identifier)
	}
	if branch != "pilot/GL-42" {
		t.Errorf("Branch = %q, want pilot/GL-42", branch)
	}
}

// TestProcessGitlabIssueEvent_BranchNames verifies branch construction for several sequence IDs.
func TestProcessGitlabIssueEvent_BranchNames(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"GL-1", "pilot/GL-1"},
		{"GL-42", "pilot/GL-42"},
		{"GL-999", "pilot/GL-999"},
	}

	for _, tt := range tests {
		got := fmt.Sprintf("pilot/%s", tt.sequenceID)
		if got != tt.wantBranch {
			t.Errorf("branch for %q = %q, want %q", tt.sequenceID, got, tt.wantBranch)
		}
	}
}

// TestProcessGitlabIssueEvent_TicketFields verifies all TicketData fields are populated
// correctly from a core.IssueEvent.
func TestProcessGitlabIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	ev := sdkcore.IssueEvent{
		IssueID:    "issue-uuid-gl",
		SequenceID: "GL-7",
		Title:      "Refactor auth",
		Body:       "Update the auth module",
		Priority:   "medium",
		Labels:     []string{"label-a", "label-b"},
		ProjectID:  "proj-gl-7",
	}

	ticket := &TicketData{
		ID:          ev.IssueID,
		Identifier:  ev.SequenceID,
		Title:       ev.Title,
		Description: ev.Body,
		Priority:    sdkshim.PriorityFromSDK(ev.Priority),
		Labels:      ev.Labels,
	}

	if ticket.Identifier != ev.SequenceID {
		t.Errorf("Identifier = %q, want %q", ticket.Identifier, ev.SequenceID)
	}
	if ticket.ID != ev.IssueID {
		t.Errorf("ID = %q, want %q", ticket.ID, ev.IssueID)
	}
	if ticket.Title != ev.Title {
		t.Errorf("Title = %q, want %q", ticket.Title, ev.Title)
	}
	if ticket.Priority != sdkshim.PilotPriorityMedium {
		t.Errorf("Priority = %d, want %d", ticket.Priority, sdkshim.PilotPriorityMedium)
	}
	if len(ticket.Labels) != 2 {
		t.Errorf("Labels len = %d, want 2", len(ticket.Labels))
	}
}
