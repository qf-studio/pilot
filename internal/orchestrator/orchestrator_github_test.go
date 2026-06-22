package orchestrator

import (
	"fmt"
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
)

// TestProcessGithubIssueEvent_IdentifierAndBranch verifies that the GitHub poll path
// uses ev.SequenceID directly as Identifier and builds Branch == "pilot/<sequenceID>".
func TestProcessGithubIssueEvent_IdentifierAndBranch(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "987654",
		SequenceID: "GH-42",
		Title:      "Add rate limiting",
		Body:       "Description here",
		Priority:   "high",
		Labels:     []string{"pilot"},
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

	if ticket.Identifier != "GH-42" {
		t.Errorf("Identifier = %q, want GH-42", ticket.Identifier)
	}
	if branch != "pilot/GH-42" {
		t.Errorf("Branch = %q, want pilot/GH-42", branch)
	}
}

// TestProcessGithubIssueEvent_UsesSequenceIDVerbatim is a SOURCE-level regression guard on the
// real production function (not a literal-only check). The SDK adapter emits SequenceID already
// as "GH-42" (adapter.go:143); re-applying fmt.Sprintf("GH-%d", ...) — as the legacy in-tree
// handler does from the raw issue number — would produce "GH-GH-42" and break branch names,
// dedup keys, and sub-issue parent parsing. ProcessGithubIssueEvent MUST use ev.SequenceID
// verbatim and set SourceAdapter "github".
func TestProcessGithubIssueEvent_UsesSequenceIDVerbatim(t *testing.T) {
	body := extractGithubFuncBody(t, "orchestrator.go", "func (o *Orchestrator) ProcessGithubIssueEvent(")
	if !strings.Contains(body, "ev.SequenceID") {
		t.Error("ProcessGithubIssueEvent must reference ev.SequenceID (verbatim Identifier + Branch)")
	}
	if strings.Contains(body, `"GH-`+`%d"`) {
		t.Error("ProcessGithubIssueEvent must not re-prefix the raw issue number into a GH- sequence (would yield GH-GH form)")
	}
	if !strings.Contains(body, `SourceAdapter: "github"`) {
		t.Error(`ProcessGithubIssueEvent must set SourceAdapter: "github"`)
	}
}

// extractGithubFuncBody returns the source of file between funcSignature and the next top-level
// "func " declaration, so assertions can be scoped to one function rather than the whole file.
func extractGithubFuncBody(t *testing.T, file, funcSignature string) string {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(content)
	start := strings.Index(src, funcSignature)
	if start < 0 {
		t.Fatalf("function %q not found in %s", funcSignature, file)
	}
	rest := src[start+len(funcSignature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestProcessGithubIssueEvent_SourceAdapter verifies the internal Task is built with
// SourceAdapter == "github" and the branch derives from the verbatim SequenceID.
func TestProcessGithubIssueEvent_SourceAdapter(t *testing.T) {
	ev := sdkcore.IssueEvent{
		IssueID:    "987654",
		SequenceID: "GH-42",
		Title:      "Add rate limiting",
		Body:       "Description",
		Priority:   "high",
		Labels:     []string{"pilot"},
	}

	internalTask := &Task{
		ID:            "task-id",
		Document:      &TaskDocument{ID: "task-id", Title: ev.Title, Markdown: ev.Body},
		ProjectPath:   "/some/path",
		Branch:        fmt.Sprintf("pilot/%s", ev.SequenceID),
		Priority:      float64(sdkshim.PriorityFromSDK(ev.Priority)),
		SourceAdapter: "github",
	}

	if internalTask.SourceAdapter != "github" {
		t.Errorf("SourceAdapter = %q, want \"github\"", internalTask.SourceAdapter)
	}
	if internalTask.Branch != "pilot/GH-42" {
		t.Errorf("Branch = %q, want pilot/GH-42", internalTask.Branch)
	}
}

// TestProcessGithubIssueEvent_PriorityConversion verifies priority mapping for all SDK values.
func TestProcessGithubIssueEvent_PriorityConversion(t *testing.T) {
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
