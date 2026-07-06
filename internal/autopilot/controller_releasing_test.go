package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
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
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			// Reachability guard: return a branch so CompareStatus is called.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "mainsha101"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			// Reachability guard: HeadSHA is reachable from main.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
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

// tagAt builds a github.Tag pointing a tag name at a commit SHA. It avoids
// spelling out the anonymous Commit struct literal at each call site.
func tagAt(name, sha string) github.Tag {
	tag := github.Tag{Name: name}
	tag.Commit.SHA = sha
	return tag
}

// TestHandleReleasing_AncestorTagDedup verifies handleReleasing treats a PR
// whose HeadSHA is already covered by an existing tag — tagged exactly, or an
// ANCESTOR of a tag's commit — as already released: the PR drains from
// activePRs WITHOUT cutting a new tag. Only a genuinely uncovered commit
// (diverged/behind the existing tags) proceeds to a release. This guards the
// spurious-v2.178.0 case where an ancestor commit cut a redundant release.
func TestHandleReleasing_AncestorTagDedup(t *testing.T) {
	const headSHA = "headsha000"
	const mainBranchSHA = "mainsha200" // fixed SHA served by the branch endpoint
	tests := []struct {
		name          string
		tags          []github.Tag
		compareStatus string // status the compare API returns for headSHA...tagSHA
		wantTagCreate bool   // expect POST /git/refs (a new release tag)
		wantTracked   bool   // expect the PR still tracked afterward
	}{
		{
			name:          "exact SHA match skips release",
			tags:          []github.Tag{tagAt("v2.0.0", headSHA)},
			wantTagCreate: false,
			wantTracked:   false,
		},
		{
			name: "ancestor of existing tag skips release",
			tags: []github.Tag{tagAt("v2.0.0", "descendantsha")},
			// base=headSHA...head=tagSHA is "ahead": the tag contains headSHA
			// plus more commits, so headSHA is already shipped inside the tag.
			compareStatus: "ahead",
			wantTagCreate: false,
			wantTracked:   false,
		},
		{
			name: "diverged commit cuts a release",
			tags: []github.Tag{tagAt("v2.0.0", "unrelatedsha")},
			// tagCoveringCommit compare: headSHA...tagSHA → diverged (not covered by tag)
			// reachability guard compare: headSHA...mainBranchSHA → handled separately → ahead
			compareStatus: "diverged",
			wantTagCreate: true,
			wantTracked:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tagCreated := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(tt.tags)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
					// Reachability guard: return a fixed main branch SHA.
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"name":   "main",
						"commit": map[string]string{"sha": mainBranchSHA},
					})
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
					// Reachability guard compare (headSHA...mainBranchSHA) always returns
					// "ahead" so the guard passes. tagCoveringCommit compare
					// (headSHA...tagSHA) returns tt.compareStatus to exercise the dedup.
					status := tt.compareStatus
					if strings.HasSuffix(r.URL.Path, mainBranchSHA) {
						status = "ahead"
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(github.Release{TagName: "v2.0.0"})
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
					tagCreated = true
					w.WriteHeader(http.StatusCreated)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			c := newReleasingController(t, server.URL)
			prState := &PRState{PRNumber: 200, HeadSHA: headSHA, Stage: StageReleasing}
			c.mu.Lock()
			c.activePRs[200] = prState
			c.mu.Unlock()

			if err := c.handleReleasing(context.Background(), prState); err != nil {
				t.Fatalf("handleReleasing returned error: %v", err)
			}
			if tagCreated != tt.wantTagCreate {
				t.Errorf("tag created = %v, want %v", tagCreated, tt.wantTagCreate)
			}
			c.mu.RLock()
			_, tracked := c.activePRs[200]
			c.mu.RUnlock()
			if tracked != tt.wantTracked {
				t.Errorf("PR tracked = %v, want %v", tracked, tt.wantTracked)
			}
		})
	}
}

// TestHandleReleasing_ExhaustiveTagDrain verifies that a PR whose HeadSHA is tagged
// beyond position 10 (outside tagCoveringCommit's bounded window) is still drained
// as "already released" via the exhaustive GetTagForSHA pagination path.
func TestHandleReleasing_ExhaustiveTagDrain(t *testing.T) {
	const headSHA = "oldsha001"
	// Build a page of 10 tags that don't match headSHA (simulates tagCoveringCommit's window).
	coveringTags := make([]github.Tag, 10)
	for i := range coveringTags {
		coveringTags[i] = tagAt(fmt.Sprintf("v3.0.%d", i), fmt.Sprintf("othersha%03d", i))
	}
	// Build a per_page=100 batch where headSHA appears at position 42.
	exhaustiveTags := make([]*github.Tag, 100)
	for i := range exhaustiveTags {
		t2 := &github.Tag{Name: fmt.Sprintf("v4.0.%d", i)}
		t2.Commit.SHA = fmt.Sprintf("bulksha%03d", i)
		exhaustiveTags[i] = t2
	}
	exhaustiveTags[42].Commit.SHA = headSHA
	exhaustiveTags[42].Name = "v4.0.42-target"

	tagCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			if r.URL.Query().Get("per_page") == "100" {
				// GetTagForSHA exhaustive path — headSHA is here.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(exhaustiveTags)
			} else {
				// tagCoveringCommit bounded window — does NOT contain headSHA.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(coveringTags)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagCreated = true
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{PRNumber: 300, HeadSHA: headSHA, Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[300] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleReleasing returned unexpected error: %v", err)
	}
	if tagCreated {
		t.Error("CreateGitTag must NOT be called — SHA was already tagged (beyond position 10)")
	}
	c.mu.RLock()
	_, stillTracked := c.activePRs[300]
	c.mu.RUnlock()
	if stillTracked {
		t.Error("PR must be drained once SHA is found in exhaustive tag lookup")
	}
}

// TestHandleReleasing_RetryCapEscalates verifies that when handleReleasing fails
// persistently and ReleasingAttempts reaches MaxReleasingAttempts, the PR transitions
// to StageFailed (never retried) instead of returning an error for the next poll.
func TestHandleReleasing_RetryCapEscalates(t *testing.T) {
	issueCommentPosted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// Persistent API failure on every tag lookup.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			issueCommentPosted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxReleasingAttempts = 3
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{
		PRNumber:    400,
		HeadSHA:     "sha400",
		IssueNumber: 40,
		Stage:       StageReleasing,
		// Pre-set to 2; next call increments to 3 == cap.
		ReleasingAttempts: 2,
	}
	c.mu.Lock()
	c.activePRs[400] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("at cap, handleReleasing must return nil (not error), got: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want StageFailed after retry cap", prState.Stage)
	}
	if prState.ReleasingAttempts != 3 {
		t.Errorf("ReleasingAttempts = %d, want 3", prState.ReleasingAttempts)
	}
	if !issueCommentPosted {
		t.Error("escalation comment must be posted on the linked issue")
	}
}

// TestHandleReleasing_RetryCapBelowCapReturnsError verifies that a failure when
// ReleasingAttempts is still below MaxReleasingAttempts returns a retryable error
// rather than escalating to StageFailed.
func TestHandleReleasing_RetryCapBelowCapReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxReleasingAttempts = 5
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{
		PRNumber:          401,
		HeadSHA:           "sha401",
		Stage:             StageReleasing,
		ReleasingAttempts: 1, // incremented to 2, below cap of 5
	}
	c.mu.Lock()
	c.activePRs[401] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err == nil {
		t.Fatal("below-cap failure must return a retryable error, got nil")
	}
	if prState.Stage == StageFailed {
		t.Error("Stage must remain StageReleasing (retryable), not StageFailed")
	}
}

// TestHandleReleasing_DivergedSHARefused verifies that a PR whose HeadSHA is not
// reachable from the default branch is immediately escalated to StageFailed without
// creating a release tag.
func TestHandleReleasing_DivergedSHARefused(t *testing.T) {
	tests := []struct {
		name          string
		compareStatus string
	}{
		{name: "diverged", compareStatus: "diverged"},
		{name: "behind", compareStatus: "behind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tagCreated := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[]`))
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"name":   "main",
						"commit": map[string]string{"sha": "mainsha500"},
					})
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{"status": tt.compareStatus})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
					tagCreated = true
					w.WriteHeader(http.StatusCreated)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			c := newReleasingController(t, server.URL)
			prState := &PRState{PRNumber: 500, HeadSHA: "divergedsha", Stage: StageReleasing}
			c.mu.Lock()
			c.activePRs[500] = prState
			c.mu.Unlock()

			err := c.handleReleasing(context.Background(), prState)
			if err != nil {
				t.Fatalf("diverged SHA must return nil (StageFailed not retryable error), got: %v", err)
			}
			if tagCreated {
				t.Error("CreateGitTag must NOT be called for a diverged SHA")
			}
			if prState.Stage != StageFailed {
				t.Errorf("Stage = %s, want StageFailed for unreachable SHA", prState.Stage)
			}
		})
	}
}
