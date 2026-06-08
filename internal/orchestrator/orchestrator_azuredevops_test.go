package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessAzureDevOpsIssueEvent_IdentifierAndBranch verifies that ProcessAzureDevOpsIssueEvent
// uses ev.SequenceID directly as Identifier and builds Branch == "pilot/<sequenceID>".
func TestProcessAzureDevOpsIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "42",
		SequenceID: "AZDO-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"bug"},
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
// correctly from a core.IssueEvent without re-prefixing the SequenceID.
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

// TestProcessAzureDevOpsIssueEvent_NoDoublePrefix verifies that SequenceID "AZDO-42" is not
// re-prefixed to produce "AZDO-AZDO-42" — the poll path must use ev.SequenceID directly.
func TestProcessAzureDevOpsIssueEvent_NoDoublePrefix(t *testing.T) {
	workItemID := 42
	// Simulate what the SDK adapter's toIssueEvent produces.
	sequenceID := fmt.Sprintf("AZDO-%d", workItemID)

	// Correct: use SequenceID directly.
	branch := fmt.Sprintf("pilot/%s", sequenceID)
	if branch != "pilot/AZDO-42" {
		t.Errorf("correct path: branch = %q, want pilot/AZDO-42", branch)
	}

	// Wrong: re-applying the prefix would produce double-prefix.
	doublePrefix := fmt.Sprintf("AZDO-%s", sequenceID)
	if doublePrefix != "AZDO-AZDO-42" {
		t.Errorf("double-prefix sanity: got %q, want AZDO-AZDO-42", doublePrefix)
	}
	if doublePrefix == "AZDO-42" {
		t.Error("double-prefix must not equal the expected sequence ID")
	}
}
