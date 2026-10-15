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

// TestJiraNotifier_EmptyTransitionsUsesCategoryFallback pins the Jira shape of
// the box config (no adapters.jira.transitions block) for GH-5701: with both
// transition IDs empty, jiraSDK.NewNotifier still moves the card by status
// category — "indeterminate" on start, "done" on completion (studio-sdk
// v0.38.3 sdk/integrations/jira/notifier.go: NotifyTaskStarted /
// NotifyTaskCompleted → transitionToCategory). If the SDK drops the fallback,
// Jira silently stops moving and this fails.
func TestJiraNotifier_EmptyTransitionsUsesCategoryFallback(t *testing.T) {
	var mu sync.Mutex
	var posted []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const base = "/rest/api/3/issue/PROJ-42/"
		switch {
		case r.URL.Path == base+"transitions" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"transitions":[` +
				`{"id":"21","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}},` +
				`{"id":"31","name":"Finish","to":{"name":"Done","statusCategory":{"key":"done"}}}]}`))
		case r.URL.Path == base+"transitions" && r.Method == http.MethodPost:
			var body struct {
				Transition struct {
					ID string `json:"id"`
				} `json:"transition"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			posted = append(posted, body.Transition.ID)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == base+"comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := jiraSDK.NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, jiraSDK.PlatformCloud)
	notifier := jiraSDK.NewNotifier(client, "", "")

	if err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "JIRA-PROJ-42"); err != nil {
		t.Fatalf("NotifyTaskStarted: %v", err)
	}
	if err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "https://github.com/o/r/pull/1", ""); err != nil {
		t.Fatalf("NotifyTaskCompleted: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(posted, ","); got != "21,31" {
		t.Errorf("transitions posted = %q, want \"21,31\" (indeterminate on start, done on completion)", got)
	}
}
