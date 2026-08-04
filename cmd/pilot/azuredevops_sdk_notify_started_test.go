package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	azuredevopsSDK "github.com/qf-studio/studio-sdk/sdk/integrations/azuredevops"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedAzureDevOpsFake is a minimal mock Azure DevOps server covering
// the two calls NotifyTaskStarted makes: GET the work item (to read current
// tags), PATCH it (to add the in-progress tag), and POST a comment. Either
// the tag update or the comment can be configured to fail, to exercise the
// non-fatal error path.
//
// Note: unlike its five siblings (github, linear, jira, gitlab, asana, plane
// all have notifier_test.go upstream in studio-sdk), the azuredevops notifier
// package has no notifier_test.go upstream — this fake is how we verify
// NotifyTaskStarted behavior here; an upstream test is a separate follow-up.
type notifyStartedAzureDevOpsFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	tagsPatched   []string
	commentsAdded []string
	failPatch     bool
	failComment   bool
}

func newNotifyStartedAzureDevOpsFake() *notifyStartedAzureDevOpsFake {
	f := &notifyStartedAzureDevOpsFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/workitems/"):
			_, _ = w.Write([]byte(`{"id":42,"fields":{"System.Tags":""}}`))
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/workitems/"):
			f.mu.Lock()
			fail := f.failPatch
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected tag failure"}`))
				return
			}
			var ops []struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&ops)
			f.mu.Lock()
			for _, op := range ops {
				f.tagsPatched = append(f.tagsPatched, op.Value)
			}
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":42,"fields":{"System.Tags":""}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			f.mu.Lock()
			fail := f.failComment
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected comment failure"}`))
				return
			}
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, body.Text)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestNotifyTaskStartedAzureDevOpsSDK is the GH-4721 acceptance test: the
// SDK-native Notifier's NotifyTaskStarted call adds the in-progress tag and
// posts a "Pilot started" comment on the work item. A tag-update failure
// must block the comment attempt (matches notifier.go's sequential
// implementation), and a comment failure must surface after the tag update
// succeeds. Before GH-4721 no dispatch path ever called this.
func TestNotifyTaskStartedAzureDevOpsSDK(t *testing.T) {
	tests := []struct {
		name        string
		failPatch   bool
		failComment bool
		wantErr     bool
		wantTag     bool
		wantComment bool
	}{
		{
			name:        "success tags and posts comment",
			wantErr:     false,
			wantTag:     true,
			wantComment: true,
		},
		{
			name:        "tag failure surfaces as an error and blocks the comment",
			failPatch:   true,
			wantErr:     true,
			wantTag:     false,
			wantComment: false,
		},
		{
			name:        "comment failure surfaces as an error after the tag succeeds",
			failComment: true,
			wantErr:     true,
			wantTag:     true,
			wantComment: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedAzureDevOpsFake()
			defer f.server.Close()
			f.failPatch = tt.failPatch
			f.failComment = tt.failComment

			client := azuredevopsSDK.NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "proj", f.server.URL)
			notifier := azuredevopsSDK.NewNotifier(client, "pilot")

			err := notifier.NotifyTaskStarted(context.Background(), 42, "AZDO-42")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			gotTag := len(f.tagsPatched) > 0
			if gotTag != tt.wantTag {
				t.Errorf("tag patched = %v, want %v (tagsPatched = %v)", gotTag, tt.wantTag, f.tagsPatched)
			}
			gotComment := len(f.commentsAdded) > 0
			if gotComment != tt.wantComment {
				t.Errorf("comment posted = %v, want %v (commentsAdded = %v)", gotComment, tt.wantComment, f.commentsAdded)
			}
		})
	}
}

// TestAzureDevOpsPollerRegistration_NotifyTaskStartedWired is a source-level
// guard proving azuredevopsPollerRegistration's CreateAndStart calls
// adoNotifier.NotifyTaskStarted( on the dispatch path before
// handleAzureDevOpsIssueWithResult, and that a notify failure is only logged
// (WARN) rather than aborting dispatch — mirroring the established
// source-inspection pattern (see TestJiraPollerRegistration_NotifyTaskStartedWired).
func TestAzureDevOpsPollerRegistration_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "poller_azuredevops.go", "func azuredevopsPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "NotifyTaskStarted(") {
		t.Error("azuredevopsPollerRegistration must call adoNotifier.NotifyTaskStarted to post the started comment on the SDK-dispatch path (GH-4721)")
	}

	notifyIdx := strings.Index(body, "NotifyTaskStarted(")
	handleIdx := strings.Index(body, "handleAzureDevOpsIssueWithResult(issueCtx")
	if notifyIdx < 0 || handleIdx < 0 || notifyIdx >= handleIdx {
		t.Error("NotifyTaskStarted must be called before handleAzureDevOpsIssueWithResult so the comment posts at the start of work")
	}

	// The error must be logged, not propagated — a notify failure must never
	// block dispatch (mirrors the Jira/Linear/GitHub SDK notify patterns).
	if !strings.Contains(body, `logging.WithComponent("azuredevops").Warn("Failed to notify task started"`) {
		t.Error("NotifyTaskStarted errors must be logged as a non-fatal WARN, not propagated")
	}
}

// TestAzureDevOpsPollerRegistration_NotifierWiredFromPackageLevelClient is a
// source-level guard proving the notifier is constructed from a
// package-level azuredevopsSDK client (mirroring poller_gitlab.go's client
// construction pattern), closing the notify-started audit's finding that no
// separate client was constructed for the AzureDevOps registration at all —
// everything went through the SDK poller's internal client.
func TestAzureDevOpsPollerRegistration_NotifierWiredFromPackageLevelClient(t *testing.T) {
	body := githubFuncBody(t, "poller_azuredevops.go", "func azuredevopsPollerRegistration() PollerRegistration {")

	if !strings.Contains(body, "azuredevopsSDK.NewClientWithConfig(") {
		t.Error("azuredevopsPollerRegistration must construct an azuredevopsSDK client for the notifier, mirroring poller_gitlab.go's client-construction pattern")
	}
	if !strings.Contains(body, "azuredevopsSDK.NewNotifier(") {
		t.Error("azuredevopsPollerRegistration must construct an azuredevopsSDK.Notifier")
	}
}
