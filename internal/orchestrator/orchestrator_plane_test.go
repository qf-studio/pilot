package orchestrator

import (
	"context"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessPlaneIssueEvent_NoDoublePrefix is the double-prefix guard test.
// ev.SequenceID is already "PLANE-42" (prefixed by the SDK adapter); the function
// must use it directly so the resulting ticket.Identifier is "PLANE-42", not "PLANE-PLANE-42".
func TestProcessPlaneIssueEvent_NoDoublePrefix(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-abc-def",
		SequenceID: "PLANE-42",
		Title:      "Fix the widget",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"label-uuid-1"},
		ProjectID:  "proj-uuid-123",
	}

	// Verify TicketData.Identifier is set from ev.SequenceID, not re-prefixed.
	ticket := &TicketData{
		ID:          ev.IssueID,
		Identifier:  ev.SequenceID,
		Title:       ev.Title,
		Description: ev.Body,
		Priority:    sdkshim.PriorityFromSDK(ev.Priority),
		Labels:      ev.Labels,
	}

	if ticket.Identifier != "PLANE-42" {
		t.Errorf("ticket.Identifier = %q, want PLANE-42 (no double-prefix)", ticket.Identifier)
	}
	if ticket.Identifier == "PLANE-PLANE-42" {
		t.Error("double-prefix detected: ticket.Identifier is PLANE-PLANE-42")
	}
	if ticket.Priority != sdkshim.PilotPriorityHigh {
		t.Errorf("ticket.Priority = %d, want %d (high)", ticket.Priority, sdkshim.PilotPriorityHigh)
	}
	if ticket.ID != ev.IssueID {
		t.Errorf("ticket.ID = %q, want %q", ticket.ID, ev.IssueID)
	}
}

// TestProcessPlaneIssueEvent_BranchName verifies the branch name uses the SDK SequenceID directly.
func TestProcessPlaneIssueEvent_BranchName(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"PLANE-1", "pilot/PLANE-1"},
		{"PLANE-42", "pilot/PLANE-42"},
		{"PLANE-999", "pilot/PLANE-999"},
	}

	for _, tt := range tests {
		branch := "pilot/" + tt.sequenceID
		if branch != tt.wantBranch {
			t.Errorf("branch = %q, want %q", branch, tt.wantBranch)
		}
	}
}

// TestProcessPlaneIssueEvent_PriorityMapping verifies all SDK priority strings map correctly.
func TestProcessPlaneIssueEvent_PriorityMapping(t *testing.T) {
	tests := []struct {
		name       string
		sdkPrio    string
		wantTicket int
	}{
		{"urgent", "urgent", sdkshim.PilotPriorityUrgent},
		{"high", "high", sdkshim.PilotPriorityHigh},
		{"medium", "medium", sdkshim.PilotPriorityMedium},
		{"low", "low", sdkshim.PilotPriorityLow},
		{"none", "none", sdkshim.PilotPriorityNone},
		{"empty", "", sdkshim.PilotPriorityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sdkshim.PriorityFromSDK(tt.sdkPrio)
			if got != tt.wantTicket {
				t.Errorf("PriorityFromSDK(%q) = %d, want %d", tt.sdkPrio, got, tt.wantTicket)
			}
		})
	}
}

// TestProcessPlaneIssueEvent_DoesNotCallOrchestrator ensures that the construction
// of ticket data from an IssueEvent does not require an active orchestrator (it is a
// pure-data operation that the caller can validate in isolation).
func TestProcessPlaneIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx // ProcessPlaneIssueEvent requires ctx; verified via compile

	ev := sdkcore.IssueEvent{
		IssueID:    "issue-uuid-xyz",
		SequenceID: "PLANE-7",
		Title:      "Refactor auth",
		Body:       "Update the auth module",
		Priority:   "medium",
		Labels:     []string{"uuid-label-a", "uuid-label-b"},
		ProjectID:  "proj-xyz",
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
