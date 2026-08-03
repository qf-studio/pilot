package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedFake is a minimal mock GitHub server covering the two calls
// NotifyTaskStarted makes: POST .../labels and POST .../comments. Either
// endpoint can be configured to fail, to exercise the non-fatal error path.
type notifyStartedFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	labelsAdded   []string
	commentsAdded []string
	failLabels    bool
	failComments  bool
}

func newNotifyStartedFake() *notifyStartedFake {
	f := &notifyStartedFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			f.mu.Lock()
			fail := f.failLabels
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
			f.labelsAdded = append(f.labelsAdded, body.Labels...)
			f.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			f.mu.Lock()
			fail := f.failComments
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected comment failure"}`))
				return
			}
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, body.Body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestNotifyTaskStartedSDK is the GH-4687 table-driven acceptance test: the
// SDK-dispatch path's notifyTaskStartedSDK helper (cmd/pilot/handlers.go)
// must apply pilot-in-progress and post the start comment on success, and
// must surface (not swallow) the underlying error on failure so the caller
// can log it as a non-fatal WARN. Before GH-4687 the SDK-poller chain
// performed zero label operations, which silently disabled
// recoverOrphanedIssues and the pilot-done label removal on merge.
func TestNotifyTaskStartedSDK(t *testing.T) {
	tests := []struct {
		name         string
		failLabels   bool
		failComments bool
		wantErr      bool
		wantLabel    bool
	}{
		{
			name:      "success applies pilot-in-progress and posts comment",
			wantErr:   false,
			wantLabel: true,
		},
		{
			name:       "label failure surfaces as an error, not a panic",
			failLabels: true,
			wantErr:    true,
			wantLabel:  false,
		},
		{
			name:         "comment failure surfaces as an error after the label succeeds",
			failComments: true,
			wantErr:      true,
			wantLabel:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedFake()
			defer f.server.Close()
			f.failLabels = tt.failLabels
			f.failComments = tt.failComments

			client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

			err := notifyTaskStartedSDK(context.Background(), client, "pilot", "o", "r", 7, "GH-4687")
			if (err != nil) != tt.wantErr {
				t.Fatalf("notifyTaskStartedSDK() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()

			gotLabel := false
			for _, l := range f.labelsAdded {
				if l == githubSDK.LabelInProgress {
					gotLabel = true
				}
			}
			if gotLabel != tt.wantLabel {
				t.Errorf("pilot-in-progress applied = %v, want %v (labelsAdded = %v)", gotLabel, tt.wantLabel, f.labelsAdded)
			}
		})
	}
}

// TestGithubHandlerSDK_NotifyTaskStartedWired is a source-level guard proving
// handleGithubIssueEventSDK actually calls notifyTaskStartedSDK on the
// dispatch path (before handleIssueGeneric), and that a labeling failure is
// only logged (WARN) rather than aborting dispatch — mirroring the
// established source-inspection pattern for this otherwise-unexercisable
// function (see TestGithubHandlerSDK_SpecGuardWired in spec_guard_sdk_test.go).
func TestGithubHandlerSDK_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	if !strings.Contains(body, "notifyTaskStartedSDK(") {
		t.Error("handleGithubIssueEventSDK must call notifyTaskStartedSDK to apply pilot-in-progress on the SDK-dispatch path (GH-4687)")
	}

	notifyIdx := strings.Index(body, "notifyTaskStartedSDK(")
	handleIssueGenericIdx := strings.Index(body, "handleIssueGeneric(ctx, deps, info, task)")
	if notifyIdx < 0 || handleIssueGenericIdx < 0 || notifyIdx >= handleIssueGenericIdx {
		t.Error("notifyTaskStartedSDK must be called before handleIssueGeneric so pilot-in-progress is applied at the start of work")
	}

	// The error must be logged, not propagated/returned — labeling failure
	// must never block dispatch (mirrors pilot.go:1191-1195 / controller.go:3011-3015).
	if !strings.Contains(body, `logging.WithComponent("github").Warn("Failed to notify task started (SDK path)"`) {
		t.Error("notifyTaskStartedSDK errors must be logged as a non-fatal WARN, not propagated")
	}
}
