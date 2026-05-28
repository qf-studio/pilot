package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

func TestIsDuplicateTagError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{
			name: "github 422 reference already exists",
			err:  errors.New(`API error (status 422): {"message":"Reference already exists"}`),
			want: true,
		},
		{
			name: "uppercase variant",
			err:  errors.New("failed to create tag v1.2.3: Reference Already Exists"),
			want: true,
		},
		{
			name: "generic 422 validation failure is not swallowed",
			err:  errors.New(`API error (status 422): {"message":"Validation Failed"}`),
			want: false,
		},
		{
			name: "transient 500 is not duplicate",
			err:  errors.New("API error (status 500): internal server error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDuplicateTagError(tt.err); got != tt.want {
				t.Errorf("isDuplicateTagError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// newReleasingController builds a controller wired to a test GitHub server with
// auto-release enabled, ready to exercise handleReleasing.
func newReleasingController(t *testing.T, serverURL string) *Controller {
	t.Helper()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverURL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{
		Enabled:         true,
		Trigger:         "on_merge",
		TagPrefix:       "v",
		NotifyOnRelease: false,
		GenerateSummary: false,
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	if c.releaser == nil {
		t.Fatal("releaser not initialized — check ReleaseConfig wiring")
	}
	return c
}

// TestHandleReleasing_GetTagForSHAError verifies that a tag-lookup failure makes
// handleReleasing return an error (retry next poll) WITHOUT attempting to create
// a tag and WITHOUT removing the PR from tracking. (TASK-316, path 1)
func TestHandleReleasing_GetTagForSHAError(t *testing.T) {
	createCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// GetTagForSHA lookup fails transiently.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			createCalled = true
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{PRNumber: 100, HeadSHA: "deadbeef", Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[100] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err == nil {
		t.Fatal("expected error when GetTagForSHA fails, got nil")
	}
	if createCalled {
		t.Error("CreateGitTag must NOT be called after a tag-lookup failure")
	}
	c.mu.RLock()
	_, stillTracked := c.activePRs[100]
	c.mu.RUnlock()
	if !stillTracked {
		t.Error("PR must remain tracked for retry after a lookup failure")
	}
}

// TestHandleReleasing_DuplicateTagTreatedAsReleased verifies that a duplicate-tag
// error from CreateGitTag is treated as success: the PR is removed from tracking
// and handleReleasing returns nil instead of looping forever. (TASK-316, path 2)
func TestHandleReleasing_DuplicateTagTreatedAsReleased(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// No existing tag visible for this SHA.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			// Tag already exists (racing release tagged this commit first).
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{PRNumber: 101, HeadSHA: "cafebabe", Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[101] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("duplicate-tag error should be treated as released, got error: %v", err)
	}
	c.mu.RLock()
	_, stillTracked := c.activePRs[101]
	c.mu.RUnlock()
	if stillTracked {
		t.Error("PR must be removed from tracking once the tag is confirmed to exist")
	}
}
