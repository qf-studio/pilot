package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/qf-studio/pilot/internal/gateway"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestGithubWebhookWiring_PRReviewReachesCallback_IssueIsNoOp is a GH-4156
// regression test mirroring the gateway wiring in main.go (~L1978-1997): a
// pull_request_review "submitted" webhook must reach the OnReviewRequested
// bridge, while an issues webhook must be a no-op because no
// SDK->core.IssueEvent bridge is wired (OnIssue is intentionally left unset;
// out of scope per GH-4156).
func TestGithubWebhookWiring_PRReviewReachesCallback_IssueIsNoOp(t *testing.T) {
	router := gateway.NewRouter()

	type reviewCall struct {
		prNumber int
		action   string
		state    string
		reviewer string
	}
	var got *reviewCall

	ghClient := githubSDK.NewClient("fake-token")
	ghWH := githubSDK.NewWebhookHandler(ghClient, "", "pilot")
	ghWH.OnPRReview(func(ctx context.Context, prNumber int, action, state, reviewer string, repo *githubSDK.Repository) error {
		if action == "submitted" {
			got = &reviewCall{prNumber: prNumber, action: action, state: state, reviewer: reviewer}
		}
		return nil
	})
	router.RegisterWebhookHandler("github", func(payload map[string]interface{}) {
		eventType, _ := payload["_event_type"].(string)
		_ = ghWH.Handle(context.Background(), eventType, payload)
	})

	// PR review "submitted" must reach the callback.
	prPayload := map[string]interface{}{}
	rawPR := `{"action":"submitted","pull_request":{"number":42},"review":{"state":"approved","user":{"login":"alice"}},"repository":{"name":"pilot","full_name":"qf-studio/pilot","owner":{"login":"qf-studio"}}}`
	if err := json.Unmarshal([]byte(rawPR), &prPayload); err != nil {
		t.Fatalf("unmarshal PR review payload: %v", err)
	}
	prPayload["_event_type"] = "pull_request_review"
	router.HandleWebhook("github", prPayload)

	if got == nil {
		t.Fatal("expected OnPRReview callback to fire for a submitted review")
	}
	if got.prNumber != 42 || got.action != "submitted" || got.state != "approved" || got.reviewer != "alice" {
		t.Errorf("unexpected review callback data: %+v", got)
	}

	// Issue webhook must be a no-op: no OnIssue bridge is wired, so it must
	// not invoke the PR-review callback or anything else observable.
	got = nil
	issuePayload := map[string]interface{}{}
	rawIssue := `{"action":"opened","issue":{"number":7,"title":"x","state":"open","html_url":"http://x","labels":[]},"repository":{"name":"pilot","full_name":"qf-studio/pilot","html_url":"http://x","owner":{"login":"qf-studio"}}}`
	if err := json.Unmarshal([]byte(rawIssue), &issuePayload); err != nil {
		t.Fatalf("unmarshal issue payload: %v", err)
	}
	issuePayload["_event_type"] = "issues"
	router.HandleWebhook("github", issuePayload)

	if got != nil {
		t.Fatalf("issue webhook must be a no-op, but review callback fired: %+v", got)
	}
}
