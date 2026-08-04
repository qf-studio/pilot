package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	asanaSDK "github.com/qf-studio/studio-sdk/sdk/integrations/asana"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedAsanaFake is a minimal mock Asana server covering the single
// call NotifyTaskStarted makes: POST .../stories (the start comment). The
// endpoint can be configured to fail, to exercise the non-fatal error path.
type notifyStartedAsanaFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	commentsAdded []string
	failComment   bool
}

func newNotifyStartedAsanaFake() *notifyStartedAsanaFake {
	f := &notifyStartedAsanaFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stories"):
			f.mu.Lock()
			fail := f.failComment
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errors":[{"message":"injected comment failure"}]}`))
				return
			}
			var body struct {
				Data struct {
					Text string `json:"text"`
				} `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, body.Data.Text)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{"gid": "story-1", "text": body.Data.Text},
			})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestNotifyTaskStartedAsanaSDK is the GH-4719 acceptance test: the SDK
// notifier's NotifyTaskStarted call posts a "Pilot started" comment on
// success and surfaces (not swallows) the underlying error on failure so the
// caller can log it as a non-fatal WARN. Before GH-4719 no dispatch path
// ever called this — poller_asana.go never constructed an asanaSDK.Notifier.
func TestNotifyTaskStartedAsanaSDK(t *testing.T) {
	tests := []struct {
		name        string
		failComment bool
		wantErr     bool
		wantComment bool
	}{
		{
			name:        "success posts started comment",
			wantErr:     false,
			wantComment: true,
		},
		{
			name:        "comment failure surfaces as an error, not a panic",
			failComment: true,
			wantErr:     true,
			wantComment: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedAsanaFake()
			defer f.server.Close()
			f.failComment = tt.failComment

			client := asanaSDK.NewClientWithBaseURL(f.server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
			notifier := asanaSDK.NewNotifier(client, "pilot")

			err := notifier.NotifyTaskStarted(context.Background(), "123456", "ASANA-123456")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			gotComment := len(f.commentsAdded) > 0
			if gotComment != tt.wantComment {
				t.Errorf("comment posted = %v, want %v (commentsAdded = %v)", gotComment, tt.wantComment, f.commentsAdded)
			}
		})
	}
}

// TestAsanaPollerRegistration_NotifyTaskStartedWired is a source-level guard
// proving asanaPollerRegistration's CreateAndStart calls
// notifier.NotifyTaskStarted( on the dispatch path before
// handleAsanaIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern (see TestLinearPollerRegistration_NotifyTaskStartedWired).
func TestAsanaPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_asana.go", "func asanaPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("asanaPollerRegistration must call notifier.NotifyTaskStarted to post the started comment on the SDK-dispatch path (GH-4719)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleAsanaIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleAsanaIssueWithResult so the comment posts at the start of work")
	}

	// The error must be logged, not propagated — a notify failure must never
	// block dispatch (mirrors the Linear/Jira/GitLab/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("asana").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestAsanaPollerRegistration_NotifierWiredFromSDKClient is a source-level
// guard proving the notifier is constructed from an asanaSDK.Client built
// with the configured AccessToken/WorkspaceID (mirroring poller_jira.go's
// single-purpose client construction pattern) and that it does not use the
// dead in-tree internal/adapters/asana notifier, which would bypass the
// SDK's request signing/error handling and duplicate the poller's own
// pilotTag mechanism (poller.go:297-298).
func TestAsanaPollerRegistration_NotifierWiredFromSDKClient(t *testing.T) {
	body := githubFuncBody(t, "poller_asana.go", "func asanaPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "asanaSDK.NewClient(") {
		t.Error("asanaPollerRegistration must construct an asanaSDK client for the notifier")
	}
	if !strings.Contains(body, "asanaSDK.NewNotifier(asanaClient, pilotTag)") {
		t.Error("asanaPollerRegistration must construct asanaSDK.NewNotifier(asanaClient, pilotTag) from the constructed asanaClient")
	}
	if strings.Contains(body, `"github.com/qf-studio/pilot/internal/adapters/asana"`) {
		t.Error("asanaPollerRegistration must use the SDK-native notifier, not internal/adapters/asana/notifier.go")
	}
}
