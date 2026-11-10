package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6263: a comment body is masked before it leaves the process.
func TestSecretGate_AddCommentMasksBody(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)
	shaped := "1234567890" + ":AA" + strings.Repeat("d", 33)

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		got = body["body"]
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Comment{ID: 1})
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	if _, err := client.AddComment(context.Background(), "o", "r", 1, "value "+secret+" and "+shaped+" end"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if strings.Contains(got, secret) || strings.Contains(got, shaped) {
		t.Errorf("comment carried a secret: %q", got)
	}
	if want := "value <redacted> and <redacted> end"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
