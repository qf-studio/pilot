package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	jiraSDK "github.com/qf-studio/studio-sdk/sdk/integrations/jira"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedJiraFake is a minimal mock Jira Cloud server covering the two
// calls NotifyTaskStarted can make: POST .../transitions (workflow status
// change) and POST .../comment (start comment). GET .../transitions backs the
// SDK's by-name fallback lookup when no transition ID is configured. Either
// endpoint can be configured to fail, to exercise the non-fatal error paths.
type notifyStartedJiraFake struct {
	server           *httptest.Server
	mu               sync.Mutex
	transitionCalled bool
	commentsAdded    []string
	failTransition   bool
	failComment      bool
	// transitionsAvailable seeds the GET .../transitions response used by the
	// SDK's by-name fallback (empty transitions.in_progress config).
	transitionsAvailable []jiraSDK.Transition
}

func newNotifyStartedJiraFake() *notifyStartedJiraFake {
	f := &notifyStartedJiraFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/transitions"):
			f.mu.Lock()
			transitions := f.transitionsAvailable
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(jiraSDK.TransitionsResponse{Transitions: transitions})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transitions"):
			f.mu.Lock()
			fail := f.failTransition
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errorMessages":["injected transition failure"]}`))
				return
			}
			f.mu.Lock()
			f.transitionCalled = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comment"):
			f.mu.Lock()
			fail := f.failComment
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errorMessages":["injected comment failure"]}`))
				return
			}
			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, r.URL.Path)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(jiraSDK.Comment{ID: "1"})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestNotifyTaskStartedJiraSDK is the GH-4718 table-driven acceptance test:
// the SDK-native Notifier's NotifyTaskStarted call posts a "Pilot started"
// comment and (when an in_progress transition ID is configured) transitions
// the issue's native workflow status. A transition failure must never block
// the comment attempt (or vice versa) — degrade gracefully per surface. Before
// GH-4718 no dispatch path ever called this, and the SDK config's Transitions
// field was plumbed but never read by any constructed Notifier (dead wiring).
func TestNotifyTaskStartedJiraSDK(t *testing.T) {
	tests := []struct {
		name           string
		inProgress     string
		failTransition bool
		failComment    bool
		wantErr        bool
		wantComment    bool
		wantTransition bool
	}{
		{
			name:           "success transitions and posts comment",
			inProgress:     "21",
			wantErr:        false,
			wantComment:    true,
			wantTransition: true,
		},
		{
			name:           "transition failure does not block the comment and is not returned",
			inProgress:     "21",
			failTransition: true,
			wantErr:        false,
			wantComment:    true,
			wantTransition: false,
		},
		{
			name:           "comment failure surfaces as an error",
			inProgress:     "21",
			failComment:    true,
			wantErr:        true,
			wantComment:    false,
			wantTransition: true,
		},
		{
			name:           "no transition configured is not an error — comment only",
			inProgress:     "",
			wantErr:        false,
			wantComment:    true,
			wantTransition: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedJiraFake()
			defer f.server.Close()
			f.failTransition = tt.failTransition
			f.failComment = tt.failComment

			client := jiraSDK.NewClient(f.server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, jiraSDK.PlatformCloud)
			notifier := jiraSDK.NewNotifier(client, tt.inProgress, "")

			err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "JIRA-PROJ-42")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			gotComment := len(f.commentsAdded) > 0
			if gotComment != tt.wantComment {
				t.Errorf("comment posted = %v, want %v (commentsAdded = %v)", gotComment, tt.wantComment, f.commentsAdded)
			}
			if f.transitionCalled != tt.wantTransition {
				t.Errorf("transition applied = %v, want %v", f.transitionCalled, tt.wantTransition)
			}
		})
	}
}

// TestJiraPollerRegistration_NotifyTaskStartedWired is a source-level guard
// proving jiraPollerRegistration's CreateAndStart calls
// notifier.NotifyTaskStarted( on the dispatch path before
// handleJiraSDKIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern (see TestLinearPollerRegistration_NotifyTaskStartedWired).
func TestJiraPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_jira.go", "func jiraPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("jiraPollerRegistration must call notifier.NotifyTaskStarted to post the started comment and transition on the SDK-dispatch path (GH-4718)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleJiraSDKIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleJiraSDKIssueWithResult so the comment/transition happen at the start of work")
	}

	// The error must be logged, not propagated — a notify failure must never
	// block dispatch (mirrors the Linear/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("jira").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestJiraPollerRegistration_NotifierWiredFromTransitionsConfig is a
// source-level guard proving the notifier is constructed from a
// package-level jiraSDK client (mirroring poller_gitlab.go's client
// construction pattern) using cfg.Adapters.Jira.Transitions, closing the
// notify-started audit's "dead config wiring" finding: sdkCfg.Transitions
// was plumbed into jiraSDK.Config but never read by the SDK poller package —
// the only consumer is jiraSDK.NewNotifier, which nothing constructed before
// GH-4718.
func TestJiraPollerRegistration_NotifierWiredFromTransitionsConfig(t *testing.T) {
	body := githubFuncBody(t, "poller_jira.go", "func jiraPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "jiraSDK.NewClient(") {
		t.Error("jiraPollerRegistration must construct a jiraSDK client for the notifier, mirroring poller_gitlab.go's client-construction pattern")
	}
	if !strings.Contains(body, "jiraSDK.NewNotifier(") {
		t.Error("jiraPollerRegistration must construct a jiraSDK.Notifier")
	}
	if !strings.Contains(body, "deps.Cfg.Adapters.Jira.Transitions.InProgress") || !strings.Contains(body, "deps.Cfg.Adapters.Jira.Transitions.Done") {
		t.Error("jiraSDK.NewNotifier must be wired from cfg.Adapters.Jira.Transitions.InProgress/.Done, not left unused")
	}
}
