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
// POST NotifyTaskStarted makes (add comment / "story" on a task). Can be
// configured to fail, to exercise the non-fatal error path.
type notifyStartedAsanaFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	commentsAdded []string
	fail          bool
}

func newNotifyStartedAsanaFake() *notifyStartedAsanaFake {
	f := &notifyStartedAsanaFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if !(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stories")) {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		f.mu.Lock()
		fail := f.fail
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
		_, _ = w.Write([]byte(`{"data":{"gid":"1"}}`))
	}))
	return f
}

// TestNotifyTaskStartedAsanaSDK is the GH-4719 acceptance test: the SDK
// notifier's NotifyTaskStarted call posts a "Pilot started" comment on
// success, and surfaces (not swallows) the underlying error on failure so the
// caller can log it as a non-fatal WARN. Before GH-4719 no dispatch path ever
// called this — the SDK-native Notifier (studio-sdk/sdk/integrations/asana)
// was tested but wired to nothing in production.
func TestNotifyTaskStartedAsanaSDK(t *testing.T) {
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
			f := newNotifyStartedAsanaFake()
			defer f.server.Close()
			f.fail = tt.fail

			client := asanaSDK.NewClientWithBaseURL(f.server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
			notifier := asanaSDK.NewNotifier(client, "pilot")

			err := notifier.NotifyTaskStarted(context.Background(), "1234567890", "ASANA-1234567890")
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

// TestAsanaPollerRegistration_NotifyTaskStartedWired is a source-level guard
// proving asanaPollerRegistration's CreateAndStart calls
// asanaNotifier.NotifyTaskStarted( on the dispatch path before
// handleAsanaIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern for otherwise-unexercisable closures (see
// TestLinearPollerRegistration_NotifyTaskStartedWired).
func TestAsanaPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_asana.go", "func asanaPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("asanaPollerRegistration must call asanaNotifier.NotifyTaskStarted to post the started comment on the SDK-dispatch path (GH-4719)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleAsanaIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleAsanaIssueWithResult so the comment posts at the start of work")
	}

	// The error must be logged, not propagated — a comment failure must
	// never block dispatch (mirrors the Plane/Linear/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("asana").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestAsanaPollerRegistration_NotifierUsesSDKClient is a source-level guard
// proving the notifier is built from the SDK-native asana.NewClient/NewNotifier,
// not the dead in-tree internal/adapters/asana notifier (see
// TestAsanaPollerNoLegacyImport in poller_asana_test.go for the import-level
// guard on the same invariant).
func TestAsanaPollerRegistration_NotifierUsesSDKClient(t *testing.T) {
	body := githubFuncBody(t, "poller_asana.go", "func asanaPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "asanaSDK.NewClient(") {
		t.Error("asanaPollerRegistration must construct the notifier's client via asanaSDK.NewClient")
	}
	if !strings.Contains(body, "asanaSDK.NewNotifier(asanaClient, pilotTag)") {
		t.Error("asanaPollerRegistration must construct asanaNotifier via asanaSDK.NewNotifier(asanaClient, pilotTag)")
	}
}
