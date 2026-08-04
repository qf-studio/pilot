package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedLinearFake is a minimal mock Linear GraphQL server covering the
// single POST NotifyTaskStarted makes (commentCreate mutation). Can be
// configured to fail, to exercise the non-fatal error path.
type notifyStartedLinearFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	commentsAdded []string
	fail          bool
}

func newNotifyStartedLinearFake() *notifyStartedLinearFake {
	f := &notifyStartedLinearFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var reqBody struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		if !strings.Contains(reqBody.Query, "commentCreate") {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		f.mu.Lock()
		fail := f.fail
		f.mu.Unlock()
		if fail {
			_, _ = w.Write([]byte(`{"errors":[{"message":"injected comment failure"}]}`))
			return
		}

		body, _ := reqBody.Variables["body"].(string)
		f.mu.Lock()
		f.commentsAdded = append(f.commentsAdded, body)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true}}}`))
	}))
	return f
}

// TestNotifyTaskStartedLinearSDK is the GH-4717 acceptance test: the SDK
// notifier's NotifyTaskStarted call posts a "Pilot started" comment on
// success, and surfaces (not swallows) the underlying error on failure so the
// caller can log it as a non-fatal WARN. Before GH-4717 no dispatch path ever
// called this — the SDK-native Notifier (studio-sdk/sdk/integrations/linear)
// was tested but wired to nothing in production.
func TestNotifyTaskStartedLinearSDK(t *testing.T) {
	tests := []struct {
		name    string
		fail    bool
		wantErr bool
	}{
		{
			name:    "success posts started comment",
			wantErr: false,
		},
		{
			name:    "comment failure surfaces as an error, not a panic",
			fail:    true,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedLinearFake()
			defer f.server.Close()
			f.fail = tt.fail

			client := linearSDK.NewClientWithBaseURL(testutil.FakeLinearAPIKey, f.server.URL)
			notifier := linearSDK.NewNotifier(client)

			err := notifier.NotifyTaskStarted(context.Background(), "issue-1", "APP-42")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			gotComment := len(f.commentsAdded) > 0
			wantComment := !tt.wantErr
			if gotComment != wantComment {
				t.Errorf("comment posted = %v, want %v (commentsAdded = %v)", gotComment, wantComment, f.commentsAdded)
			}
		})
	}
}

// TestLinearPollerRegistration_NotifyTaskStartedWired is a source-level guard
// proving linearPollerRegistration's CreateAndStart calls
// notifier.NotifyTaskStarted( on the dispatch path before
// handleLinearIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern for otherwise-unexercisable closures (see
// TestGithubHandlerSDK_NotifyTaskStartedWired).
func TestLinearPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_linear.go", "func linearPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("linearPollerRegistration must call notifier.NotifyTaskStarted to post the started comment on the SDK-dispatch path (GH-4717)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleLinearIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleLinearIssueWithResult so the comment posts at the start of work")
	}

	// The error must be logged, not propagated — a comment failure must
	// never block dispatch (mirrors the Plane/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("linear").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestLinearPollerRegistration_NotifierPerWorkspace is a source-level guard
// proving the notifier is built per workspace (keyed by team ID), not as a
// single global notifier — Linear supports multiple workspaces, each with
// its own API key.
func TestLinearPollerRegistration_NotifierPerWorkspace(t *testing.T) {
	body := githubFuncBody(t, "poller_linear.go", "func linearPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "notifiersByTeamID[ws.TeamID] = linearSDK.NewNotifier(linearSDK.NewClient(ws.APIKey))") {
		t.Error("linearPollerRegistration must construct one SDK notifier per workspace client, keyed by team ID, inside the sdkWorkspaces loop")
	}
}
