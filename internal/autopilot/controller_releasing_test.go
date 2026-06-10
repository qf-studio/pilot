package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// tagAt builds a github.Tag pointing a tag name at a commit SHA. It avoids
// spelling out the anonymous Commit struct literal at each call site.
func tagAt(name, sha string) github.Tag {
	tag := github.Tag{Name: name}
	tag.Commit.SHA = sha
	return tag
}

// TestHandleReleasing_AlreadyPublishedReleaseDrains verifies that when a published
// GitHub Release already exists for HeadSHA, handleReleasing drains the PR without
// creating a new tag. This is the fast-path "existing published release" drain. (GH-3558)
func TestHandleReleasing_AlreadyPublishedReleaseDrains(t *testing.T) {
	const headSHA = "mergecommitabc"
	const tagName = "v3.1.0"
	tagCreateCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			// Return a merged PR with MergeCommitSHA == headSHA.
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:         300,
				MergeCommitSHA: headSHA,
				Merged:         true,
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// GetTagForSHA exhaustive search finds the tag.
			tags := []github.Tag{tagAt(tagName, headSHA)}
			_ = json.NewEncoder(w).Encode(tags)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			// A published release already exists for this tag.
			_ = json.NewEncoder(w).Encode(github.Release{
				ID:      12345,
				TagName: tagName,
				Draft:   false,
				HTMLURL: "https://github.com/owner/repo/releases/tag/" + tagName,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagCreateCalled = true
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
		t.Fatalf("expected nil error when published release exists, got: %v", err)
	}
	if tagCreateCalled {
		t.Error("CreateGitTag must NOT be called when a published release already exists")
	}
	c.mu.RLock()
	_, stillTracked := c.activePRs[300]
	c.mu.RUnlock()
	if stillTracked {
		t.Error("PR must be drained when published release exists")
	}
}

// TestHandleReleasing_RetryCapEscalatesStageFailed verifies that once
// ReleasingAttempts reaches maxReleasingAttempts, a transient tag-lookup error
// causes StageFailed (not another retry). (GH-3558)
func TestHandleReleasing_RetryCapEscalatesStageFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			// Return a merged PR with MergeCommitSHA so HeadSHA refresh succeeds.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:         301,
				MergeCommitSHA: "sha301",
				Merged:         true,
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// Simulate a persistent transient failure on tag lookup.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{
		PRNumber:          301,
		HeadSHA:           "sha301",
		Stage:             StageReleasing,
		ReleasingAttempts: maxReleasingAttempts - 1, // one more attempt will hit the cap
	}
	c.mu.Lock()
	c.activePRs[301] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("expected nil error on cap escalation, got: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %v, want StageFailed", prState.Stage)
	}
	if prState.Error == "" {
		t.Error("prState.Error must be set on StageFailed escalation")
	}
	// StageFailed keeps the PR in activePRs (terminal but visible for observability),
	// consistent with how handleMerging handles its attempt cap.
	c.mu.RLock()
	tracked, ok := c.activePRs[301]
	c.mu.RUnlock()
	if !ok || tracked == nil {
		t.Error("PR should remain in activePRs (in StageFailed state) for observability")
	}
}

// TestHandleReleasing_DivergedSHARefused verifies that a HeadSHA which is not
// reachable from the default branch (compare status "diverged") causes StageFailed
// rather than tagging a commit that was never actually merged. (GH-3558)
func TestHandleReleasing_DivergedSHARefused(t *testing.T) {
	const mergeCommit = "divergedcommit"
	tagCreateCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:         302,
				MergeCommitSHA: mergeCommit,
				Merged:         true,
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// No existing tag for this SHA.
			_ = json.NewEncoder(w).Encode([]github.Tag{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			// The compare of mergeCommit against the default branch returns "diverged"
			// — the commit is not on the default branch.
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "diverged"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagCreateCalled = true
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{PRNumber: 302, HeadSHA: "original-head", Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[302] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("expected nil error on reachability failure, got: %v", err)
	}
	if tagCreateCalled {
		t.Error("CreateGitTag must NOT be called for an unreachable SHA")
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %v, want StageFailed", prState.Stage)
	}
}

// TestHandleReleasing_AncestorTagDedup verifies handleReleasing treats a PR
// whose HeadSHA is already covered by an existing tag — tagged exactly, or an
// ANCESTOR of a tag's commit — as already released: the PR drains from
// activePRs WITHOUT cutting a new tag. Only a genuinely uncovered commit
// (diverged/behind the existing tags) proceeds to a release. This guards the
// spurious-v2.178.0 case where an ancestor commit cut a redundant release.
func TestHandleReleasing_AncestorTagDedup(t *testing.T) {
	const headSHA = "headsha000"
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
			name:          "diverged commit cuts a release",
			tags:          []github.Tag{tagAt("v2.0.0", "unrelatedsha")},
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
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{"status": tt.compareStatus})
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

// TestHandleReleasing_DurationCapStageFailed verifies that a PR stuck in
// StageReleasing longer than maxReleasingDuration escalates to StageFailed via
// releasingErrorOrFail even if the attempt count is below maxReleasingAttempts.
// This guards the daemon-restart scenario where the attempt counter resets but the
// underlying API failure persists. GH-3558.
func TestHandleReleasing_DurationCapStageFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			// Simulate a transient tag-lookup error to trigger releasingErrorOrFail.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{
		PRNumber: 600,
		HeadSHA:  "durshatagsig",
		Stage:    StageReleasing,
		// Attempt count is low but duration exceeds maxReleasingDuration.
		ReleasingAttempts: 2,
		ReleasingFirstAt:  time.Now().Add(-(maxReleasingDuration + time.Second)),
	}
	c.mu.Lock()
	c.activePRs[600] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("expected nil on duration cap escalation, got: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("expected StageFailed after duration cap, got %q", prState.Stage)
	}
	if prState.Error == "" {
		t.Error("expected non-empty Error on StageFailed transition")
	}
}
