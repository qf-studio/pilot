package comms

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

// mockIssueCreator records calls to CreateIssue and returns a canned URL/error.
type mockIssueCreator struct {
	url   string
	err   error
	calls []issueCreatorCall
}

type issueCreatorCall struct {
	projectPath string
	draft       IssueDraft
}

func (m *mockIssueCreator) CreateIssue(_ context.Context, projectPath string, d IssueDraft) (string, error) {
	m.calls = append(m.calls, issueCreatorCall{projectPath: projectPath, draft: d})
	return m.url, m.err
}

// newDraftResponder builds a Responder whose Answer call returns JSON that
// DraftIssue can parse. Labels may be nil (JSON becomes []).
func newDraftResponder(title, body string, labels []string) (*Responder, *mockAnswerer) {
	labelsJSON := "[]"
	if len(labels) > 0 {
		quoted := make([]string, len(labels))
		for i, l := range labels {
			quoted[i] = `"` + l + `"`
		}
		labelsJSON = "[" + strings.Join(quoted, ",") + "]"
	}
	reply := `{"title":"` + title + `","body":"` + body + `","labels":` + labelsJSON + `}`
	a := &mockAnswerer{reply: reply}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	return r, a
}

// buildIssueIntakeHandler creates a Handler wired for issue-intake tests.
func buildIssueIntakeHandler(m *handlerMock, responder *Responder, creator IssueCreator, autoLabel bool) *Handler {
	h := NewHandler(&HandlerConfig{
		Messenger:      m,
		Responder:      responder,
		IssueCreator:   creator,
		AutoLabelPilot: autoLabel,
		TaskIDPrefix:   "TEST",
	})
	return h
}

// TestHandleIssueIntake_CallsCreateIssueOnce verifies that handleIssueIntake
// calls IssueCreator.CreateIssue exactly once with the pilot label present.
func TestHandleIssueIntake_CallsCreateIssueOnce(t *testing.T) {
	responder, _ := newDraftResponder(
		"fix(auth): handle nil session token",
		"Nil session tokens cause a panic in the auth middleware.",
		[]string{"pilot"},
	)
	creator := &mockIssueCreator{url: "https://github.com/owner/repo/issues/42"}
	m := &handlerMock{}
	h := buildIssueIntakeHandler(m, responder, creator, true)

	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue to fix nil session token panics")

	if len(creator.calls) != 1 {
		t.Fatalf("expected CreateIssue called once, got %d", len(creator.calls))
	}
	call := creator.calls[0]
	if !containsLabel(call.draft.Labels, "pilot") {
		t.Errorf("expected 'pilot' label, got %v", call.draft.Labels)
	}
	if call.draft.Title == "" {
		t.Error("expected non-empty title")
	}
}

// TestHandleIssueIntake_NoResponder sends a graceful message when bot is unconfigured.
func TestHandleIssueIntake_NoResponder(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		TaskIDPrefix: "TEST",
	})
	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue")
	texts := m.getTexts()
	if len(texts) == 0 || !strings.Contains(texts[0].text, "bot.enabled") {
		t.Errorf("expected bot-not-configured message, got %v", texts)
	}
}

// TestHandleIssueIntake_NoIssueCreator sends a graceful message when GitHub is unconfigured.
func TestHandleIssueIntake_NoIssueCreator(t *testing.T) {
	responder, _ := newDraftResponder("feat(x): y", "body", []string{"pilot"})
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		Responder:    responder,
		TaskIDPrefix: "TEST",
	})
	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue")
	texts := m.getTexts()
	if len(texts) == 0 || !strings.Contains(texts[0].text, "not configured") {
		t.Errorf("expected not-configured message, got %v", texts)
	}
}

// TestHandleIssueIntake_DraftError propagates LLM errors gracefully.
func TestHandleIssueIntake_DraftError(t *testing.T) {
	a := &mockAnswerer{err: errors.New("timeout")}
	responder := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	creator := &mockIssueCreator{}
	m := &handlerMock{}
	h := buildIssueIntakeHandler(m, responder, creator, true)

	h.handleIssueIntake(context.Background(), "ctx1", "", "file a ticket for broken login")

	if len(creator.calls) != 0 {
		t.Error("CreateIssue should not be called when DraftIssue fails")
	}
}

// TestHandleIssueIntake_AutoLabelPilot_False verifies pilot label is NOT force-added.
func TestHandleIssueIntake_AutoLabelPilot_False(t *testing.T) {
	// LLM returns no labels; autoLabelPilot=false so no "pilot" is added.
	a := &mockAnswerer{reply: `{"title":"fix(x): y","body":"body","labels":[]}`}
	responder := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	creator := &mockIssueCreator{url: "https://github.com/owner/repo/issues/1"}
	m := &handlerMock{}
	h := buildIssueIntakeHandler(m, responder, creator, false)

	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue to fix X")

	if len(creator.calls) != 1 {
		t.Fatalf("expected 1 CreateIssue call, got %d", len(creator.calls))
	}
	if containsLabel(creator.calls[0].draft.Labels, "pilot") {
		t.Error("pilot label should not be added when autoLabelPilot=false")
	}
}

// TestHandleIssueIntake_SuccessMessage verifies the URL is reported to the user.
func TestHandleIssueIntake_SuccessMessage(t *testing.T) {
	responder, _ := newDraftResponder("feat(ui): new button", "Adds a save button.", []string{"pilot"})
	creator := &mockIssueCreator{url: "https://github.com/owner/repo/issues/99"}
	m := &handlerMock{}
	h := buildIssueIntakeHandler(m, responder, creator, true)

	h.handleIssueIntake(context.Background(), "ctx1", "", "create an issue to add a save button")

	texts := m.getTexts()
	found := false
	for _, st := range texts {
		if strings.Contains(st.text, "https://github.com/owner/repo/issues/99") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected issue URL in reply, got texts: %v", texts)
	}
}

// TestResponder_DraftIssue_ParsesJSON verifies DraftIssue parses the LLM JSON response.
func TestResponder_DraftIssue_ParsesJSON(t *testing.T) {
	a := &mockAnswerer{
		reply: `{"title":"feat(auth): add OAuth support","body":"Adds OAuth2 login flow.","labels":["pilot","enhancement"]}`,
	}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}

	draft, err := r.DraftIssue(context.Background(), nil, "add OAuth login", true)
	if err != nil {
		t.Fatalf("DraftIssue returned error: %v", err)
	}
	if draft.Title != "feat(auth): add OAuth support" {
		t.Errorf("title = %q", draft.Title)
	}
	if draft.Body != "Adds OAuth2 login flow." {
		t.Errorf("body = %q", draft.Body)
	}
	if !containsLabel(draft.Labels, "pilot") {
		t.Errorf("expected pilot label, got %v", draft.Labels)
	}
}

// TestResponder_DraftIssue_StripsFences verifies DraftIssue handles markdown-wrapped JSON.
func TestResponder_DraftIssue_StripsFences(t *testing.T) {
	a := &mockAnswerer{
		reply: "```json\n{\"title\":\"fix(x): y\",\"body\":\"b\",\"labels\":[\"pilot\"]}\n```",
	}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}

	draft, err := r.DraftIssue(context.Background(), nil, "fix X", true)
	if err != nil {
		t.Fatalf("DraftIssue returned error: %v", err)
	}
	if draft.Title != "fix(x): y" {
		t.Errorf("title = %q", draft.Title)
	}
}

// TestResponder_DraftIssue_AddsPilotLabel verifies auto-label adds pilot when absent.
func TestResponder_DraftIssue_AddsPilotLabel(t *testing.T) {
	a := &mockAnswerer{
		reply: `{"title":"feat(ui): new button","body":"body","labels":["enhancement"]}`,
	}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}

	draft, err := r.DraftIssue(context.Background(), nil, "add a button", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsLabel(draft.Labels, "pilot") {
		t.Errorf("expected pilot label to be added, got %v", draft.Labels)
	}
}

// TestResponder_DraftIssue_ErrorPropagates verifies LLM errors propagate.
func TestResponder_DraftIssue_ErrorPropagates(t *testing.T) {
	a := &mockAnswerer{err: errors.New("api error")}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	_, err := r.DraftIssue(context.Background(), nil, "create issue", true)
	if err == nil {
		t.Error("expected error from DraftIssue, got nil")
	}
}

// TestDraftIssueCommand_RoutesToIssueIntake verifies /draft-issue dispatches to
// handleIssueIntake via the CommandHandler wiring.
func TestDraftIssueCommand_RoutesToIssueIntake(t *testing.T) {
	responder, _ := newDraftResponder("fix(login): broken login", "Login is broken.", []string{"pilot"})
	creator := &mockIssueCreator{url: "https://github.com/o/r/issues/7"}
	m := &handlerMock{}
	h := buildIssueIntakeHandler(m, responder, creator, true)

	// Trigger via /draft-issue command path.
	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "u1",
		Text:      "/draft-issue fix the broken login flow",
	})

	if len(creator.calls) != 1 {
		t.Fatalf("expected 1 CreateIssue call via /draft-issue, got %d", len(creator.calls))
	}
	if !containsLabel(creator.calls[0].draft.Labels, "pilot") {
		t.Errorf("expected pilot label via /draft-issue, got %v", creator.calls[0].draft.Labels)
	}
}

// TestIsIssueIntake_Patterns verifies known issue-intake phrases are detected.
func TestIsIssueIntake_Patterns(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"create an issue to fix the login bug", true},
		{"file a ticket for the API timeout", true},
		{"open an issue about auth", true},
		{"draft an issue", true},
		{"create a ticket for this", true},
		{"report an issue", true},
		{"raise an issue", true},
		// Non-intake phrases
		{"create a login feature", false},
		{"fix the login bug", false},
		{"add a button", false},
		{"what's in the queue?", false},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			m := &handlerMock{}
			h := newTestHandler(m)
			got := h.detectIntent(context.Background(), "ctx", tc.msg)
			isIntake := got == intent.IntentIssueIntake
			if isIntake != tc.want {
				t.Errorf("detectIntent(%q) issue_intake=%v, want %v (got intent %s)", tc.msg, isIntake, tc.want, got)
			}
		})
	}
}
