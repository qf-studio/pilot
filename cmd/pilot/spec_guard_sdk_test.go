package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/ghissue"
	"github.com/qf-studio/pilot/internal/testutil"
)

type specGuardFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	existing      []string // comment bodies returned by ListIssueComments
	labelsAdded   []string
	commentsAdded []string
}

func newSpecGuardFake(existingComments ...string) *specGuardFake {
	f := &specGuardFake{existing: existingComments}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			type c struct {
				Body string `json:"body"`
			}
			out := make([]c, len(f.existing))
			for i, b := range f.existing {
				out[i] = c{Body: b}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.labelsAdded = append(f.labelsAdded, body.Labels...)
			f.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
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

func TestApplySpecGuardSDK_FirstStrike(t *testing.T) {
	f := newSpecGuardFake()
	defer f.server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "thin", Body: "too thin"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on first strike")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelSpecIncomplete {
		t.Errorf("labels added = %v, want [%s]", f.labelsAdded, githubSDK.LabelSpecIncomplete)
	}
	if len(f.commentsAdded) != 1 || !strings.Contains(f.commentsAdded[0], ghissue.SpecCommentMarker) {
		t.Errorf("first-strike comment missing marker; comments = %d", len(f.commentsAdded))
	}
}

func TestApplySpecGuardSDK_SecondStrikeEscalates(t *testing.T) {
	f := newSpecGuardFake("earlier strike\n" + ghissue.SpecCommentMarker + "\ndetails")
	defer f.server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "still thin", Body: "still too thin"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on second strike")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelBlocked {
		t.Errorf("labels added = %v, want [%s]", f.labelsAdded, githubSDK.LabelBlocked)
	}
	if len(f.commentsAdded) != 0 {
		t.Errorf("second strike must not post another comment; got %d", len(f.commentsAdded))
	}
}

// TestGithubHandlerSDK_SpecGuardWired is a source-level guard: the SDK handler
// must run ghissue.ValidateSpec before dispatch (M7 4d.3 — the canary-caught
// spec-gate class must not regress when the SDK poll path goes live).
func TestGithubHandlerSDK_SpecGuardWired(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read handlers.go: %v", err)
	}
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
	_ = content
	if !strings.Contains(body, "ghissue.ValidateSpec") {
		t.Error("handleGithubIssueEventSDK must run ghissue.ValidateSpec before dispatch")
	}
	if !strings.Contains(body, "applySpecGuardSDK") {
		t.Error("handleGithubIssueEventSDK must apply the spec guard on validation failure")
	}
}
