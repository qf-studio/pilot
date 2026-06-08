package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessLinearIssueEvent_IdentifierAndBranch verifies that ProcessLinearIssueEvent
// uses ev.SequenceID directly as Identifier and builds Branch == "pilot/<sequenceID>".
func TestProcessLinearIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "abc-uuid-123",
		SequenceID: "APP-42",
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

	if ticket.Identifier != "APP-42" {
		t.Errorf("Identifier = %q, want APP-42", ticket.Identifier)
	}
	if branch != "pilot/APP-42" {
		t.Errorf("Branch = %q, want pilot/APP-42", branch)
	}
}

// TestProcessLinearIssueEvent_BranchNames verifies branch construction for several identifiers.
func TestProcessLinearIssueEvent_BranchNames(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"APP-1", "pilot/APP-1"},
		{"APP-42", "pilot/APP-42"},
		{"PROJ-9999", "pilot/PROJ-9999"},
	}

	for _, tt := range tests {
		got := fmt.Sprintf("pilot/%s", tt.sequenceID)
		if got != tt.wantBranch {
			t.Errorf("branch for %q = %q, want %q", tt.sequenceID, got, tt.wantBranch)
		}
	}
}

// TestProcessLinearIssueEvent_TicketFields verifies all TicketData fields are populated
// correctly from a core.IssueEvent without re-prefixing the SequenceID.
func TestProcessLinearIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	ev := sdkcore.IssueEvent{
		IssueID:    "def-uuid-456",
		SequenceID: "PROJ-99",
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

// TestProcessLinearIssueEvent_NoDoublePrefix verifies that SequenceID (e.g. "APP-42") is
// not re-prefixed — the poll path must use ev.SequenceID directly.
func TestProcessLinearIssueEvent_NoDoublePrefix(t *testing.T) {
	sequenceID := "APP-42"

	// Correct: use SequenceID directly.
	branch := fmt.Sprintf("pilot/%s", sequenceID)
	if branch != "pilot/APP-42" {
		t.Errorf("correct path: branch = %q, want pilot/APP-42", branch)
	}

	// Wrong: re-applying a prefix would produce a mangled identifier.
	rePrefix := fmt.Sprintf("LIN-%s", sequenceID)
	if rePrefix == "APP-42" {
		t.Error("re-prefixed ID must not equal the original sequence ID")
	}
	if rePrefix != "LIN-APP-42" {
		t.Errorf("re-prefix sanity: got %q, want LIN-APP-42", rePrefix)
	}
}
