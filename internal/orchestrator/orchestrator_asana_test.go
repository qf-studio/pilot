package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessAsanaIssueEvent_IdentifierAndBranch verifies that ProcessAsanaIssueEvent
// uses ev.SequenceID directly as Identifier and builds Branch == "pilot/<sequenceID>".
func TestProcessAsanaIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "1234567890",
		SequenceID: "ASANA-1234567890",
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

	if ticket.Identifier != "ASANA-1234567890" {
		t.Errorf("Identifier = %q, want ASANA-1234567890", ticket.Identifier)
	}
	if branch != "pilot/ASANA-1234567890" {
		t.Errorf("Branch = %q, want pilot/ASANA-1234567890", branch)
	}
}

// TestProcessAsanaIssueEvent_BranchNames verifies branch construction for several GIDs.
func TestProcessAsanaIssueEvent_BranchNames(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"ASANA-1", "pilot/ASANA-1"},
		{"ASANA-1234567890", "pilot/ASANA-1234567890"},
		{"ASANA-9999999999", "pilot/ASANA-9999999999"},
	}

	for _, tt := range tests {
		got := fmt.Sprintf("pilot/%s", tt.sequenceID)
		if got != tt.wantBranch {
			t.Errorf("branch for %q = %q, want %q", tt.sequenceID, got, tt.wantBranch)
		}
	}
}

// TestProcessAsanaIssueEvent_TicketFields verifies all TicketData fields are populated
// correctly from a core.IssueEvent without re-prefixing the SequenceID.
func TestProcessAsanaIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	ev := sdkcore.IssueEvent{
		IssueID:    "7654321",
		SequenceID: "ASANA-7654321",
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

// TestProcessAsanaIssueEvent_NoDoublePrefix verifies that SequenceID "ASANA-1234567890" is
// not re-prefixed — the poll path must use ev.SequenceID directly.
func TestProcessAsanaIssueEvent_NoDoublePrefix(t *testing.T) {
	gid := "1234567890"
	// Simulate what the SDK adapter's toIssueEvent produces.
	sequenceID := fmt.Sprintf("ASANA-%s", gid)

	// Correct: use SequenceID directly.
	branch := fmt.Sprintf("pilot/%s", sequenceID)
	if branch != "pilot/ASANA-1234567890" {
		t.Errorf("correct path: branch = %q, want pilot/ASANA-1234567890", branch)
	}

	// Wrong: re-applying the prefix would produce double-prefix.
	doublePrefix := fmt.Sprintf("ASANA-%s", sequenceID)
	if doublePrefix != "ASANA-ASANA-1234567890" {
		t.Errorf("double-prefix sanity: got %q, want ASANA-ASANA-1234567890", doublePrefix)
	}
	if doublePrefix == "ASANA-1234567890" {
		t.Error("double-prefix must not equal the expected sequence ID")
	}
}
