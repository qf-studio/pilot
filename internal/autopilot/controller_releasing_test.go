package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// newReleasingControllerWithPublish is like newReleasingController but lets the
// test choose the release publish mode (GH-3926).
func newReleasingControllerWithPublish(t *testing.T, serverURL, publish string) *Controller {
	t.Helper()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverURL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{
		Enabled:           true,
		Trigger:           "on_merge",
		TagPrefix:         "v",
		NotifyOnRelease:   false,
		GenerateSummary:   false,
		GenerateChangelog: true,
		Publish:           publish,
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	if c.releaser == nil {
		t.Fatal("releaser not initialized — check ReleaseConfig wiring")
	}
	return c
}

// publishModeTestServer serves the full happy path through tag creation (no
// covering tags, a reachable SHA, and a feat commit to trigger a version
// bump), delegating any request whose path contains "/releases" (other than
// GET .../releases/latest, served here so every mode gets a base version) to
// onReleases — so each publish-mode test only has to describe the release
// side without duplicating the tag/version/commit plumbing.
func publishModeTestServer(onReleases http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "publishmodemainsha"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/releases"):
			onReleases(w, r)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// TestHandleReleasing_PublishModes verifies GH-3926's per-mode behavior at the
// point a tag has just been created: "api" publishes a GitHub Release via the
// API exactly once (tag_name + generated changelog body); "workflow" and
// "tag_only" never call POST /releases. All three modes drain the PR.
func TestHandleReleasing_PublishModes(t *testing.T) {
	tests := []struct {
		name        string
		publish     string
		wantPublish bool
	}{
		{name: "workflow leaves publishing to the repo's CI", publish: "workflow", wantPublish: false},
		{name: "tag_only publishes nothing", publish: "tag_only", wantPublish: false},
		{name: "api publishes the GitHub Release itself", publish: "api", wantPublish: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				releasePosts int
				gotTagName   string
				gotBody      string
			)
			server := publishModeTestServer(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
					releasePosts++
					var input github.ReleaseInput
					raw, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(raw, &input)
					gotTagName = input.TagName
					gotBody = input.Body
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(github.Release{
						TagName: input.TagName,
						HTMLURL: "https://github.com/owner/repo/releases/tag/" + input.TagName,
					})
				default:
					w.WriteHeader(http.StatusOK)
				}
			})
			defer server.Close()

			c := newReleasingControllerWithPublish(t, server.URL, tt.publish)
			prNumber := 600 + i
			prState := &PRState{PRNumber: prNumber, HeadSHA: fmt.Sprintf("sha%d", prNumber), Stage: StageReleasing}
			c.mu.Lock()
			c.activePRs[prNumber] = prState
			c.mu.Unlock()

			if err := c.handleReleasing(context.Background(), prState); err != nil {
				t.Fatalf("handleReleasing returned error: %v", err)
			}

			if (releasePosts > 0) != tt.wantPublish {
				t.Errorf("POST /releases called %d times, want called=%v", releasePosts, tt.wantPublish)
			}
			if tt.wantPublish {
				if releasePosts != 1 {
					t.Errorf("POST /releases called %d times, want exactly 1", releasePosts)
				}
				if gotTagName != prState.ReleaseVersion {
					t.Errorf("release tag_name = %q, want %q", gotTagName, prState.ReleaseVersion)
				}
				if gotBody == "" {
					t.Error("release body should carry the generated changelog")
				}
			}
			c.mu.RLock()
			_, tracked := c.activePRs[prNumber]
			c.mu.RUnlock()
			if tracked {
				t.Error("PR must drain from activePRs in every publish mode")
			}
		})
	}
}

// TestHandleReleasing_APIModeRetryAfterCreateReleaseFailure pins the
// idempotence fix (GH-3926): a transient CreateRelease failure right after a
// successful tag push must not permanently lose the release. Pass 1 tags
// successfully but the release POST 500s (retryable, PR stays tracked). Pass
// 2 finds the tag already exists (drain path) and — because no release exists
// yet for it — publishes the release there instead of silently draining.
func TestHandleReleasing_APIModeRetryAfterCreateReleaseFailure(t *testing.T) {
	const headSHA = "sha700retry"
	var (
		releasePosts   int
		tagCreated     bool
		createdTagName string
	)
	pass := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			if pass == 1 || createdTagName == "" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			// Pass 2: the tag created in pass 1 now exists at HeadSHA.
			_ = json.NewEncoder(w).Encode([]github.Tag{tagAt(createdTagName, headSHA)})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "retrymainsha"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagCreated = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			// No release ever exists yet in this test — that's the point.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			releasePosts++
			var input github.ReleaseInput
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &input)
			createdTagName = input.TagName
			if pass == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"server error"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Release{
				TagName: input.TagName,
				HTMLURL: "https://github.com/owner/repo/releases/tag/" + input.TagName,
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingControllerWithPublish(t, server.URL, "api")
	prState := &PRState{PRNumber: 700, HeadSHA: headSHA, Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[700] = prState
	c.mu.Unlock()

	// Pass 1: tag creation succeeds, release publish fails transiently.
	err := c.handleReleasing(context.Background(), prState)
	if err == nil {
		t.Fatal("pass 1: expected a retryable error after CreateRelease failure, got nil")
	}
	if !tagCreated {
		t.Fatal("pass 1: tag should have been created")
	}
	if createdTagName == "" {
		t.Fatal("pass 1: CreateRelease should have been attempted for the new tag")
	}
	c.mu.RLock()
	_, tracked := c.activePRs[700]
	c.mu.RUnlock()
	if !tracked {
		t.Error("pass 1: PR must remain tracked for retry after a release-publish failure")
	}

	// Pass 2: the tag now exists — handleReleasing must reach the drain path's
	// idempotence check (ensureReleasePublished) and successfully publish the
	// release instead of silently draining with no release ever created.
	pass = 2
	tagCreated = false
	err = c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("pass 2: expected nil (drained), got error: %v", err)
	}
	if tagCreated {
		t.Error("pass 2: CreateGitTag must NOT be called again — the tag already exists")
	}
	if releasePosts != 2 {
		t.Errorf("POST /releases called %d times across both passes, want 2 (pass 1 failed, pass 2 succeeded)", releasePosts)
	}
	c.mu.RLock()
	_, tracked = c.activePRs[700]
	c.mu.RUnlock()
	if tracked {
		t.Error("pass 2: PR must drain from activePRs once the release is published")
	}
}

// TestHandleReleasing_APIModeDuplicateRelease verifies that a 422
// "already_exists" response from POST /releases is treated as success: the
// release already exists (e.g. a racing retry created it first), so the PR
// drains instead of looping on an error it can never recover from.
func TestHandleReleasing_APIModeDuplicateRelease(t *testing.T) {
	server := publishModeTestServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Release","code":"already_exists","field":"tag_name"}]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	c := newReleasingControllerWithPublish(t, server.URL, "api")
	prState := &PRState{PRNumber: 800, HeadSHA: "sha800duplicate", Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[800] = prState
	c.mu.Unlock()

	err := c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("422 already_exists must be treated as released, got error: %v", err)
	}
	c.mu.RLock()
	_, tracked := c.activePRs[800]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR must drain once the release is confirmed to already exist")
	}
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

// TestHandleReleasing_ResyncsHeadSHAFromPostMergeSHA verifies GH-4146: a plain
// (non-scope) PR whose branch-head HeadSHA is stale/diverged (as it always is
// after a squash merge) still releases successfully once PostMergeSHA — the
// post-merge-CI-validated main commit — is set. The copy-back used to be
// gated on scope carriers only (GH-3990), so every plain PR's reachability
// check compared the stale branch head against main, always saw "diverged",
// and escalated to StageFailed forever.
func TestHandleReleasing_ResyncsHeadSHAFromPostMergeSHA(t *testing.T) {
	tagCreated := false
	var comparedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "mainsha700"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			comparedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagCreated = true
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newReleasingController(t, server.URL)
	prState := &PRState{
		PRNumber:     700,
		HeadSHA:      "staledivergedsha",
		PostMergeSHA: "mergedsha700",
		Stage:        StageReleasing,
	}
	c.mu.Lock()
	c.activePRs[700] = prState
	c.mu.Unlock()

	if err := c.handleReleasing(context.Background(), prState); err != nil {
		t.Fatalf("handleReleasing returned error: %v", err)
	}
	if prState.Stage == StageFailed {
		t.Error("Stage must not be StageFailed once HeadSHA is resynced from PostMergeSHA")
	}
	if !tagCreated {
		t.Error("tag must be created once HeadSHA is resynced from PostMergeSHA")
	}
	if prState.HeadSHA != "mergedsha700" {
		t.Errorf("HeadSHA = %s, want resynced to PostMergeSHA mergedsha700", prState.HeadSHA)
	}
	if !strings.Contains(comparedPath, "mergedsha700") || strings.Contains(comparedPath, "staledivergedsha") {
		t.Errorf("reachability compare must use the resynced merge SHA, got path %s", comparedPath)
	}
}
