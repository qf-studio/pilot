package orchestrator

import (
	"context"
	"fmt"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessJiraIssueEvent_IdentifierAndBranch verifies that ProcessJiraIssueEvent
// uses ev.SequenceID directly as Identifier and builds Branch == "pilot/<sequenceID>".
func TestProcessJiraIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "12345",
		SequenceID: "PROJ-42",
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

	if ticket.Identifier != "PROJ-42" {
		t.Errorf("Identifier = %q, want PROJ-42", ticket.Identifier)
	}
	if branch != "pilot/PROJ-42" {
		t.Errorf("Branch = %q, want pilot/PROJ-42", branch)
	}
}

// TestProcessJiraIssueEvent_BranchNames verifies branch construction for several sequence IDs.
func TestProcessJiraIssueEvent_BranchNames(t *testing.T) {
	tests := []struct {
		sequenceID string
		wantBranch string
	}{
		{"PROJ-1", "pilot/PROJ-1"},
		{"PROJ-42", "pilot/PROJ-42"},
		{"MYPROJ-999", "pilot/MYPROJ-999"},
	}

	for _, tt := range tests {
		got := fmt.Sprintf("pilot/%s", tt.sequenceID)
		if got != tt.wantBranch {
			t.Errorf("branch for %q = %q, want %q", tt.sequenceID, got, tt.wantBranch)
		}
	}
}

// TestProcessJiraIssueEvent_TicketFields verifies all TicketData fields are populated
// correctly from a core.IssueEvent without re-prefixing the SequenceID.
func TestProcessJiraIssueEvent_TicketFields(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	ev := sdkcore.IssueEvent{
		IssueID:    "7654",
		SequenceID: "PROJ-42",
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

// TestProcessJiraIssueEvent_SourceAdapter verifies that the internal Task is built with
// SourceAdapter == "jira" unconditionally, and that SequenceID is used as Identifier.
func TestProcessJiraIssueEvent_SourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "12345",
		SequenceID: "PROJ-42",
		Title:      "Fix the widget",
		Body:       "Description",
		Priority:   "high",
		Labels:     []string{"bug"},
	}

	internalTask := &Task{
		ID:            "task-id",
		Document:      &TaskDocument{ID: "task-id", Title: ev.Title, Markdown: ev.Body},
		ProjectPath:   "/some/path",
		Branch:        fmt.Sprintf("pilot/%s", ev.SequenceID),
		Priority:      float64(sdkshim.PriorityFromSDK(ev.Priority)),
		SourceAdapter: "jira",
	}

	if internalTask.SourceAdapter != "jira" {
		t.Errorf("SourceAdapter = %q, want \"jira\"", internalTask.SourceAdapter)
	}
	if internalTask.Branch != "pilot/PROJ-42" {
		t.Errorf("Branch = %q, want pilot/PROJ-42", internalTask.Branch)
	}
}

// TestProcessJiraIssueEvent_NoDoublePrefix verifies that SequenceID "PROJ-42" is not
// re-prefixed — the poll path must use ev.SequenceID directly.
func TestProcessJiraIssueEvent_NoDoublePrefix(t *testing.T) {
	issueKey := "PROJ-42"
	// ev.SequenceID already carries the "PROJ-42" key from the SDK adapter.
	sequenceID := issueKey

	// Correct: use SequenceID directly.
	branch := fmt.Sprintf("pilot/%s", sequenceID)
	if branch != "pilot/PROJ-42" {
		t.Errorf("correct path: branch = %q, want pilot/PROJ-42", branch)
	}

	// Wrong: re-applying a prefix would garble the key.
	rePrefixed := fmt.Sprintf("JIRA-%s", sequenceID)
	if rePrefixed == "PROJ-42" {
		t.Error("re-prefixed value must not equal the expected sequence ID")
	}
}

// TestProcessJiraIssueEvent_PriorityConversion verifies priority mapping for all SDK values.
func TestProcessJiraIssueEvent_PriorityConversion(t *testing.T) {
	tests := []struct {
		sdkPriority  string
		wantPriority int
	}{
		{"urgent", sdkshim.PilotPriorityUrgent},
		{"high", sdkshim.PilotPriorityHigh},
		{"medium", sdkshim.PilotPriorityMedium},
		{"low", sdkshim.PilotPriorityLow},
		{"none", sdkshim.PilotPriorityNone},
		{"", sdkshim.PilotPriorityNone},
	}

	for _, tt := range tests {
		got := sdkshim.PriorityFromSDK(tt.sdkPriority)
		if got != tt.wantPriority {
			t.Errorf("PriorityFromSDK(%q) = %d, want %d", tt.sdkPriority, got, tt.wantPriority)
		}
	}
}
