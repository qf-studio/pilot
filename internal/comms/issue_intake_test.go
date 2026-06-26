package comms

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

// mockIssueCreator records CreateIssue calls and returns canned results.
type mockIssueCreator struct {
	url   string
	err   error
	calls []mockCreateCall
}

type mockCreateCall struct {
	projectPath string
	draft       IssueDraft
}

func (m *mockIssueCreator) CreateIssue(_ context.Context, projectPath string, d IssueDraft) (string, error) {
	m.calls = append(m.calls, mockCreateCall{projectPath: projectPath, draft: d})
	return m.url, m.err
}

// TestParseIssueDraft_CleanJSON verifies clean JSON parsing.
func TestParseIssueDraft_CleanJSON(t *testing.T) {
	raw := `{"title":"feat(gateway): add rate limiting","body":"## Summary\nAdd rate limiting.","labels":["pilot"]}`
	d, err := parseIssueDraft(raw)
	if err != nil {
		t.Fatalf("parseIssueDraft error: %v", err)
	}
	if d.Title != "feat(gateway): add rate limiting" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.Body != "## Summary\nAdd rate limiting." {
		t.Errorf("Body = %q", d.Body)
	}
	if len(d.Labels) == 0 || d.Labels[0] != "pilot" {
		t.Errorf("Labels = %v, want [pilot]", d.Labels)
	}
}

// TestParseIssueDraft_MarkdownFences strips code fences.
func TestParseIssueDraft_MarkdownFences(t *testing.T) {
	raw := "```json\n{\"title\":\"fix(auth): handle nil token\",\"body\":\"body\",\"labels\":[]}\n```"
	d, err := parseIssueDraft(raw)
	if err != nil {
		t.Fatalf("parseIssueDraft error: %v", err)
	}
	if d.Title != "fix(auth): handle nil token" {
		t.Errorf("Title = %q", d.Title)
	}
	hasPilot := false
	for _, l := range d.Labels {
		if l == "pilot" {
			hasPilot = true
		}
	}
	if !hasPilot {
		t.Errorf("expected 'pilot' in Labels, got %v", d.Labels)
	}
}

// TestParseIssueDraft_AutoAddsPilotLabel verifies pilot label is always injected.
func TestParseIssueDraft_AutoAddsPilotLabel(t *testing.T) {
	raw := `{"title":"chore(deps): update go modules","body":"body","labels":["enhancement"]}`
	d, err := parseIssueDraft(raw)
	if err != nil {
		t.Fatalf("parseIssueDraft error: %v", err)
	}
	hasPilot := false
	for _, l := range d.Labels {
		if l == "pilot" {
			hasPilot = true
		}
	}
	if !hasPilot {
		t.Errorf("pilot label not added to %v", d.Labels)
	}
}

// TestParseIssueDraft_EmptyTitle errors on empty title.
func TestParseIssueDraft_EmptyTitle(t *testing.T) {
	raw := `{"title":"","body":"body","labels":["pilot"]}`
	_, err := parseIssueDraft(raw)
	if err == nil {
		t.Error("expected error for empty title")
	}
}

// TestDraftIssue_CallsLLMAndParsesJSON verifies Responder.DraftIssue end-to-end.
func TestDraftIssue_CallsLLMAndParsesJSON(t *testing.T) {
	reply := `{"title":"feat(comms): add rate limiting","body":"body","labels":["pilot"]}`
	r, a := newMockResponder(reply, "")
	d, err := r.DraftIssue(context.Background(), nil, "add rate limiting to the comms layer")
	if err != nil {
		t.Fatalf("DraftIssue error: %v", err)
	}
	if d.Title != "feat(comms): add rate limiting" {
		t.Errorf("Title = %q", d.Title)
	}
	if len(a.calls) == 0 {
		t.Fatal("expected LLM call")
	}
	if !strings.Contains(a.calls[0].system, "conventional-commit") {
		t.Errorf("system prompt missing conventional-commit guidance")
	}
}

// TestDraftIssue_LLMError propagates errors.
func TestDraftIssue_LLMError(t *testing.T) {
	a := &mockAnswerer{err: errors.New("timeout")}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	_, err := r.DraftIssue(context.Background(), nil, "create an issue")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

// TestHandleIssueIntake_CreateIssue asserts CreateIssue called once with pilot label.
func TestHandleIssueIntake_CreateIssue(t *testing.T) {
	reply := `{"title":"feat(api): add endpoint","body":"body","labels":["pilot"]}`
	responder, _ := newMockResponder(reply, "")
	issueCreator := &mockIssueCreator{url: "https://github.com/owner/repo/issues/42"}
	messenger := &mockMessenger{}

	h := &Handler{
		messenger:     messenger,
		responder:     responder,
		issueCreator:  issueCreator,
		pendingIssues: make(map[string]*IssueDraft),
		log:           slog.Default(),
	}

	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue to add a new API endpoint")

	if len(issueCreator.calls) != 1 {
		t.Fatalf("CreateIssue called %d times, want 1", len(issueCreator.calls))
	}
	call := issueCreator.calls[0]
	hasPilot := false
	for _, l := range call.draft.Labels {
		if l == "pilot" {
			hasPilot = true
		}
	}
	if !hasPilot {
		t.Errorf("CreateIssue called without pilot label: %v", call.draft.Labels)
	}
	if call.draft.Title != "feat(api): add endpoint" {
		t.Errorf("Title = %q", call.draft.Title)
	}
}

// TestHandleIssueIntake_NoResponder returns graceful message.
func TestHandleIssueIntake_NoResponder(t *testing.T) {
	messenger := &mockMessenger{}
	h := &Handler{
		messenger:     messenger,
		responder:     nil,
		issueCreator:  &mockIssueCreator{},
		pendingIssues: make(map[string]*IssueDraft),
		log:           slog.Default(),
	}
	h.handleIssueIntake(context.Background(), "ctx1", "", "file a ticket")
	if len(messenger.messages) == 0 {
		t.Fatal("expected a text message")
	}
	if !strings.Contains(messenger.messages[0], "bot module") {
		t.Errorf("expected bot module hint in: %q", messenger.messages[0])
	}
}

// TestHandleIssueIntake_NoIssueCreator returns graceful message.
func TestHandleIssueIntake_NoIssueCreator(t *testing.T) {
	responder, _ := newMockResponder(`{"title":"feat(x): y","body":"b","labels":[]}`, "")
	messenger := &mockMessenger{}
	h := &Handler{
		messenger:     messenger,
		responder:     responder,
		issueCreator:  nil,
		pendingIssues: make(map[string]*IssueDraft),
		log:           slog.Default(),
	}
	h.handleIssueIntake(context.Background(), "ctx1", "", "file a ticket")
	if len(messenger.messages) == 0 {
		t.Fatal("expected a text message")
	}
	if !strings.Contains(messenger.messages[0], "GitHub") {
		t.Errorf("expected GitHub hint in: %q", messenger.messages[0])
	}
}

// TestHandleIssueIntake_CreateError surfaces create error to user.
func TestHandleIssueIntake_CreateError(t *testing.T) {
	reply := `{"title":"feat(api): add endpoint","body":"body","labels":["pilot"]}`
	responder, _ := newMockResponder(reply, "")
	issueCreator := &mockIssueCreator{err: errors.New("API error 422")}
	messenger := &mockMessenger{}

	h := &Handler{
		messenger:     messenger,
		responder:     responder,
		issueCreator:  issueCreator,
		pendingIssues: make(map[string]*IssueDraft),
		log:           slog.Default(),
	}
	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue")

	found := false
	for _, txt := range messenger.messages {
		if strings.Contains(txt, "Failed") || strings.Contains(txt, "failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected failure message in: %v", messenger.messages)
	}
}

// TestIsIssueIntakeRequest covers recognition patterns.
func TestIsIssueIntakeRequest(t *testing.T) {
	positive := []string{
		"create an issue to add rate limiting",
		"file a ticket for the login bug",
		"open an issue about the dashboard crash",
		"raise a ticket for missing validation",
		"log an issue for slow queries",
		"report an issue with auth",
	}
	for _, text := range positive {
		if !intent.IsIssueIntakeRequest(text) {
			t.Errorf("IsIssueIntakeRequest(%q) = false, want true", text)
		}
	}

	negative := []string{
		"fix the login bug",
		"add a rate limiter to the gateway",
		"what issues are open?",
	}
	for _, text := range negative {
		if intent.IsIssueIntakeRequest(text) {
			t.Errorf("IsIssueIntakeRequest(%q) = true, want false", text)
		}
	}
}
