package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gitlabSDK "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedGitlabFake is a minimal mock GitLab server covering the calls
// NotifyTaskStarted makes: GET .../issues/{iid} + PUT .../issues/{iid} (the
// label merge round-trip behind AddIssueLabels) and POST .../notes (the start
// note). Either surface can be configured to fail, to exercise the non-fatal
// error path.
type notifyStartedGitlabFake struct {
	server     *httptest.Server
	mu         sync.Mutex
	labelsPut  []string
	notesAdded []string
	failLabel  bool
	failNote   bool
}

func newNotifyStartedGitlabFake() *notifyStartedGitlabFake {
	f := &notifyStartedGitlabFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/"):
			_ = json.NewEncoder(w).Encode(gitlabSDK.Issue{IID: 42, Labels: []string{"pilot"}})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/issues/"):
			f.mu.Lock()
			fail := f.failLabel
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected label failure"}`))
				return
			}
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.labelsPut = body.Labels
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(gitlabSDK.Issue{IID: 42, Labels: body.Labels})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			f.mu.Lock()
			fail := f.failNote
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected note failure"}`))
				return
			}
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.notesAdded = append(f.notesAdded, body.Body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestNotifyTaskStartedGitlabSDK is the GH-4720 acceptance test: the SDK
// notifier's NotifyTaskStarted call adds the in-progress label and posts a
// "Pilot started" note on success, and surfaces (not swallows) the
// underlying error on failure so the caller can log it as a non-fatal WARN.
// Before GH-4720 no dispatch path ever called this — poller_gitlab.go
// constructed a *gitlabSDK.Client but never wrapped it in a Notifier.
func TestNotifyTaskStartedGitlabSDK(t *testing.T) {
	tests := []struct {
		name      string
		failLabel bool
		failNote  bool
		wantErr   bool
		wantNote  bool
	}{
		{
			name:     "success adds label and posts started note",
			wantErr:  false,
			wantNote: true,
		},
		{
			name:      "label failure surfaces as an error, not a panic",
			failLabel: true,
			wantErr:   true,
			wantNote:  false,
		},
		{
			name:     "note failure surfaces as an error after the label succeeds",
			failNote: true,
			wantErr:  true,
			wantNote: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedGitlabFake()
			defer f.server.Close()
			f.failLabel = tt.failLabel
			f.failNote = tt.failNote

			client := gitlabSDK.NewClientWithBaseURL(testutil.FakeGitLabToken, "1", f.server.URL)
			notifier := gitlabSDK.NewNotifier(client, "pilot")

			err := notifier.NotifyTaskStarted(context.Background(), 42, "GL-42")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			gotNote := len(f.notesAdded) > 0
			if gotNote != tt.wantNote {
				t.Errorf("note posted = %v, want %v (notesAdded = %v)", gotNote, tt.wantNote, f.notesAdded)
			}
		})
	}
}

// TestGitlabPollerRegistration_NotifyTaskStartedWired is a source-level guard
// proving gitlabPollerRegistration's CreateAndStart calls
// gitlabNotifier.NotifyTaskStarted( on the dispatch path before
// handleGitlabIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern (see TestLinearPollerRegistration_NotifyTaskStartedWired).
func TestGitlabPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_gitlab.go", "func gitlabPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("gitlabPollerRegistration must call gitlabNotifier.NotifyTaskStarted to post the started note on the SDK-dispatch path (GH-4720)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleGitlabIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleGitlabIssueWithResult so the note posts at the start of work")
	}

	// The error must be logged, not propagated — a notify failure must never
	// block dispatch (mirrors the Linear/Jira/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("gitlab").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestGitlabPollerRegistration_NotifierFromExistingClient is a source-level
// guard proving the notifier is constructed by wrapping the *gitlabSDK.Client
// that poller_gitlab.go already builds for handler calls (AddIssueNote,
// SetPRCreator) — not a second, independently-constructed client — and that
// it does not use the in-tree internal/adapters/gitlab notifier, which would
// double-apply the label the SDK poller already guarantees (poller.go:520).
func TestGitlabPollerRegistration_NotifierFromExistingClient(t *testing.T) {
	body := githubFuncBody(t, "poller_gitlab.go", "func gitlabPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "gitlabSDK.NewNotifier(gitlabClient, pilotLabel)") {
		t.Error("gitlabPollerRegistration must construct gitlabSDK.NewNotifier(gitlabClient, pilotLabel) from the existing gitlabClient, not a new client")
	}
	if strings.Contains(body, `"github.com/qf-studio/pilot/internal/adapters/gitlab"`) {
		t.Error("gitlabPollerRegistration must use the SDK-native notifier, not internal/adapters/gitlab/notifier.go")
	}
}
