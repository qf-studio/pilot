package sdkshim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
)

// Compile-time proof the shim satisfies the executor contract.
var _ executor.PRCreator = (*GitHubPRCreator)(nil)

func TestGitHubPRCreator_CreatesAndReturnsURL(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			gotBody = string(buf)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/o/r/pull/9"}`))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := NewGitHubPRCreator(githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), "o", "r")
	url, err := c.CreatePR(context.Background(), "pilot/GH-9", "main", "GH-9: fix", "body")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if url != "https://github.com/o/r/pull/9" {
		t.Errorf("url = %q", url)
	}
	for _, want := range []string{`"head":"pilot/GH-9"`, `"base":"main"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s; got %s", want, gotBody)
		}
	}
}

func TestGitHubPRCreator_AlreadyExistsRecoversURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for o:pilot/GH-9."}]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			_, _ = w.Write([]byte(`[{"number":9,"html_url":"https://github.com/o/r/pull/9","state":"open","head":{"ref":"pilot/GH-9"}}]`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	c := NewGitHubPRCreator(githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), "o", "r")
	url, err := c.CreatePR(context.Background(), "pilot/GH-9", "main", "GH-9: fix", "body")
	if err != nil {
		t.Fatalf("CreatePR must recover the existing PR URL, got error: %v", err)
	}
	if url != "https://github.com/o/r/pull/9" {
		t.Errorf("url = %q, want existing PR URL", url)
	}
}

func TestGitHubPRCreator_HardFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"base branch not found"}]}`))
	}))
	defer srv.Close()

	c := NewGitHubPRCreator(githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), "o", "r")
	if _, err := c.CreatePR(context.Background(), "pilot/GH-9", "nope", "GH-9: fix", "body"); err == nil {
		t.Fatal("non-already-exists 422 must propagate as an error")
	}
}
