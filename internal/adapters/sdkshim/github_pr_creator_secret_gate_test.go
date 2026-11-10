package sdkshim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6263: the PR title and body are masked before they reach GitHub.
func TestSecretGate_CreatePRMasksTitleAndBody(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)

	var got struct{ Title, Body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/o/r/pull/9"}`))
	}))
	defer srv.Close()

	c := NewGitHubPRCreator(githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), "o", "r")
	if _, err := c.CreatePR(context.Background(), "pilot/GH-9", "main", "fix: "+secret, "body "+secret); err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if got.Title != "fix: <redacted>" || got.Body != "body <redacted>" {
		t.Errorf("got title %q body %q", got.Title, got.Body)
	}
}
