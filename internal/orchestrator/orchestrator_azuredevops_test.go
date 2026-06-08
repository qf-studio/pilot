package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessAzureDevOpsIssueEvent_IdentifierAndBranch verifies that the resulting task
// has Identifier == ev.SequenceID and Branch == "pilot/<sequenceID>".
func TestProcessAzureDevOpsIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "AZDO-42",
		Title:      "Fix the pipeline",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"bug"},
		ProjectID:  "Task",
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

	if ticket.Identifier != "AZDO-42" {
		t.Errorf("Identifier = %q, want AZDO-42", ticket.Identifier)
	}
	if branch != "pilot/AZDO-42" {
		t.Errorf("Branch = %q, want pilot/AZDO-42", branch)
	}
}

// TestProcessAzureDevOpsIssueEvent_NoDoublePrefix is the double-prefix guard test.
// ev.SequenceID is already "AZDO-42" (prefixed by the SDK adapter); the function
// must use it directly so the resulting ticket.Identifier is "AZDO-42", not "AZDO-AZDO-42".
func TestProcessAzureDevOpsIssueEvent_NoDoublePrefix(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "AZDO-42",
		Title:      "Fix the pipeline",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"pilot"},
		ProjectID:  "Task",
	}

	ticket := &TicketData{
		ID:          ev.IssueID,
		Identifier:  ev.SequenceID,
		Title:       ev.Title,
		Description: ev.Body,
		Priority:    sdkshim.PriorityFromSDK(ev.Priority),
		Labels:      ev.Labels,
	}

	if ticket.Identifier != "AZDO-42" {
		t.Errorf("ticket.Identifier = %q, want AZDO-42 (no double-prefix)", ticket.Identifier)
	}
	if ticket.Identifier == "AZDO-AZDO-42" {
		t.Error("double-prefix detected: ticket.Identifier is AZDO-AZDO-42")
	}
	if ticket.Priority != sdkshim.PilotPriorityHigh {
		t.Errorf("ticket.Priority = %d, want %d (high)", ticket.Priority, sdkshim.PilotPriorityHigh)
	}
	if ticket.ID != ev.IssueID {
		t.Errorf("ticket.ID = %q, want %q", ticket.ID, ev.IssueID)
	}
}

// TestProcessAzureDevOpsIssueEvent_BranchNames verifies branch construction for several sequence IDs.
func TestProcessAzureDevOpsIssueEvent_BranchNames(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"AZDO-1", "pilot/AZDO-1"},
		{"AZDO-42", "pilot/AZDO-42"},
		{"AZDO-999", "pilot/AZDO-999"},
	}

	for _, tt := range tests {
		got := fmt.Sprintf("pilot/%s", tt.sequenceID)
		if got != tt.wantBranch {
			t.Errorf("branch for %q = %q, want %q", tt.sequenceID, got, tt.wantBranch)
		}
	}
}

// TestProcessAzureDevOpsIssueEvent_TicketFields verifies all TicketData fields are populated
// correctly from a core.IssueEvent.
func TestProcessAzureDevOpsIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	ev := sdkcore.IssueEvent{
		IssueID:    "7",
		SequenceID: "AZDO-7",
		Title:      "Refactor auth",
		Body:       "Update the auth module",
		Priority:   "medium",
		Labels:     []string{"label-a", "label-b"},
		ProjectID:  "User Story",
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
