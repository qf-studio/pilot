package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestProjectReleaseConfig_Apply covers the overlay-resolution semantics
// (project > env > global) that ProjectReleaseConfig.Apply implements: only
// fields the overlay actually sets are overridden, everything else is
// inherited from base. GH-3929.
func TestProjectReleaseConfig_Apply(t *testing.T) {
	base := &ReleaseConfig{
		Enabled:           true,
		Trigger:           "on_merge",
		VersionStrategy:   "conventional_commits",
		TagPrefix:         "v",
		GenerateChangelog: true,
		NotifyOnRelease:   true,
		RequireCI:         true,
		Publish:           "workflow",
	}

	t.Run("nil base, overlay does not enable", func(t *testing.T) {
		o := &ProjectReleaseConfig{Publish: "api"}
		if got := o.Apply(nil); got != nil {
			t.Errorf("Apply(nil) = %+v, want nil (overlay never turned releasing on)", got)
		}
	})

	t.Run("nil base, overlay enables", func(t *testing.T) {
		o := &ProjectReleaseConfig{Enabled: boolPtr(true), Publish: "api"}
		got := o.Apply(nil)
		if got == nil {
			t.Fatal("Apply(nil) = nil, want a config synthesized from DefaultReleaseConfig")
		}
		if !got.Enabled {
			t.Error("Enabled = false, want true")
		}
		if got.Publish != "api" {
			t.Errorf("Publish = %q, want api", got.Publish)
		}
		if got.TagPrefix != DefaultReleaseConfig().TagPrefix {
			t.Errorf("TagPrefix = %q, want default %q", got.TagPrefix, DefaultReleaseConfig().TagPrefix)
		}
	})

	t.Run("publish-only overlay inherits the rest", func(t *testing.T) {
		o := &ProjectReleaseConfig{Publish: "api"}
		got := o.Apply(base)
		if got.Publish != "api" {
			t.Errorf("Publish = %q, want api", got.Publish)
		}
		if !got.Enabled {
			t.Error("Enabled must be inherited from base (true), got false")
		}
		if got.TagPrefix != "v" {
			t.Errorf("TagPrefix = %q, want inherited %q", got.TagPrefix, "v")
		}
		if base.Publish != "workflow" {
			t.Error("Apply must not mutate base")
		}
	})

	t.Run("enabled override", func(t *testing.T) {
		o := &ProjectReleaseConfig{Enabled: boolPtr(false)}
		got := o.Apply(base)
		if got.Enabled {
			t.Error("Enabled = true, want false (overlay override)")
		}
		if got.Publish != "workflow" {
			t.Errorf("Publish = %q, want inherited workflow", got.Publish)
		}
	})

	t.Run("tag_prefix override", func(t *testing.T) {
		o := &ProjectReleaseConfig{TagPrefix: "release-"}
		got := o.Apply(base)
		if got.TagPrefix != "release-" {
			t.Errorf("TagPrefix = %q, want release-", got.TagPrefix)
		}
		if !got.Enabled {
			t.Error("Enabled must be inherited, got false")
		}
	})

	t.Run("nil overlay is a no-op", func(t *testing.T) {
		var o *ProjectReleaseConfig
		got := o.Apply(base)
		if got != base {
			t.Errorf("Apply on nil overlay must return base unchanged, got %+v", got)
		}
	})
}

// TestResolvedRelease_Precedence verifies project overlay > per-environment
// config > global config when NewController resolves the effective
// ReleaseConfig at construction time. GH-3929.
func TestResolvedRelease_Precedence(t *testing.T) {
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")

	t.Run("global only", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Environment = EnvStage
		cfg.Release = &ReleaseConfig{Enabled: true, TagPrefix: "v", Publish: "workflow"}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		rel := c.resolvedRelease()
		if rel == nil || rel.PublishMode() != "workflow" {
			t.Fatalf("resolvedRelease() = %+v, want global workflow config", rel)
		}
	})

	t.Run("env overrides global", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, TagPrefix: "v", Publish: "workflow"}
		cfg.Environments = map[string]*EnvironmentConfig{
			"stage": {Release: &ReleaseConfig{Enabled: true, TagPrefix: "env-v", Publish: "tag_only"}},
		}
		if err := cfg.SetActiveEnvironment("stage"); err != nil {
			t.Fatalf("SetActiveEnvironment: %v", err)
		}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		rel := c.resolvedRelease()
		if rel == nil || rel.PublishMode() != "tag_only" || rel.TagPrefix != "env-v" {
			t.Fatalf("resolvedRelease() = %+v, want env-scoped tag_only/env-v config", rel)
		}
	})

	t.Run("project overlay overrides env and global", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, TagPrefix: "v", Publish: "workflow"}
		cfg.Environments = map[string]*EnvironmentConfig{
			"stage": {Release: &ReleaseConfig{Enabled: true, TagPrefix: "env-v", Publish: "tag_only"}},
		}
		if err := cfg.SetActiveEnvironment("stage"); err != nil {
			t.Fatalf("SetActiveEnvironment: %v", err)
		}
		c := NewController(cfg, ghClient, nil, "owner", "repo",
			WithReleaseOverride(&ProjectReleaseConfig{Publish: "api"}))
		rel := c.resolvedRelease()
		if rel == nil {
			t.Fatal("resolvedRelease() = nil, want overlaid config")
		}
		if rel.PublishMode() != "api" {
			t.Errorf("Publish = %q, want api (project overlay wins)", rel.Publish)
		}
		if rel.TagPrefix != "env-v" {
			t.Errorf("TagPrefix = %q, want env-v (inherited from env, not overridden by project)", rel.TagPrefix)
		}
	})
}

// releaseSuccessPathServer returns an httptest server that answers every
// GitHub API call handleReleasing needs to reach a successful tag creation:
// no covering/exact tag, reachability guard passes, a prior release exists
// for version-bump math, and the PR has one feat commit. releaseCalls counts
// POST /releases (GitHub Release creation) invocations; tagCreated reports
// whether POST /git/refs (tag creation) was called.
func releaseSuccessPathServer(t *testing.T, releaseCalls *int32, releaseStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "mainsha"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			atomic.AddInt32(releaseCalls, 1)
			w.WriteHeader(releaseStatus)
			if releaseStatus < 300 {
				_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.1.0", HTMLURL: "https://example.test/release"})
			} else {
				_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Release","code":"already_exists","field":"tag_name"}]}`))
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// TestHandleReleasing_PublishModes verifies each publish mode's GitHub Release
// side effect: "api" creates a release via POST /releases exactly once,
// "workflow" and "tag_only" never call it, and the PR drains from activePRs
// in all three modes (the tag itself is always created). GH-3929.
func TestHandleReleasing_PublishModes(t *testing.T) {
	tests := []struct {
		name            string
		publish         string
		wantReleaseCall bool
	}{
		{name: "workflow leaves publishing to CI", publish: "workflow", wantReleaseCall: false},
		{name: "api publishes via GitHub Releases API", publish: "api", wantReleaseCall: true},
		{name: "tag_only stops at the tag", publish: "tag_only", wantReleaseCall: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var releaseCalls int32
			server := releaseSuccessPathServer(t, &releaseCalls, http.StatusCreated)
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvStage
			cfg.Release = &ReleaseConfig{
				Enabled:           true,
				Trigger:           "on_merge",
				TagPrefix:         "v",
				GenerateChangelog: true,
				Publish:           tt.publish,
			}
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			prState := &PRState{PRNumber: 900, HeadSHA: "pmsha", Stage: StageReleasing}
			c.mu.Lock()
			c.activePRs[900] = prState
			c.mu.Unlock()

			if err := c.handleReleasing(context.Background(), prState); err != nil {
				t.Fatalf("handleReleasing returned error: %v", err)
			}

			gotCalled := atomic.LoadInt32(&releaseCalls) > 0
			if gotCalled != tt.wantReleaseCall {
				t.Errorf("POST /releases called = %v, want %v", gotCalled, tt.wantReleaseCall)
			}
			if tt.wantReleaseCall && atomic.LoadInt32(&releaseCalls) != 1 {
				t.Errorf("POST /releases call count = %d, want exactly 1", releaseCalls)
			}

			c.mu.RLock()
			_, tracked := c.activePRs[900]
			c.mu.RUnlock()
			if tracked {
				t.Error("PR must drain from activePRs after a successful release in every publish mode")
			}
		})
	}
}

// TestHandleReleasing_APIModeDuplicateRelease verifies that a 422
// already_exists response from CreateRelease (e.g. a racing pass already
// published the release) is treated as success rather than a retryable
// failure. GH-3929.
func TestHandleReleasing_APIModeDuplicateRelease(t *testing.T) {
	var releaseCalls int32
	server := releaseSuccessPathServer(t, &releaseCalls, http.StatusUnprocessableEntity)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v", Publish: "api"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{PRNumber: 901, HeadSHA: "dupsha", Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[901] = prState
	c.mu.Unlock()

	if err := c.handleReleasing(context.Background(), prState); err != nil {
		t.Fatalf("422 already_exists must be treated as released, got error: %v", err)
	}
	c.mu.RLock()
	_, tracked := c.activePRs[901]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR must drain from activePRs when the release already exists")
	}
}

// TestHandleReleasing_APIModeRetryAfterCreateReleaseFailure pins the GH-3929
// idempotence fix: a transient CreateRelease failure after the tag was
// already created must NOT lose the release forever. Pass 1 creates the tag
// but CreateRelease 500s, so the PR stays tracked for retry. Pass 2 sees the
// same commit as already tagged (via the exhaustive GetTagForSHA path — the
// bounded tagCoveringCommit window is made to miss it, mirroring
// TestHandleReleasing_ExhaustiveTagDrain) and must still attempt
// CreateRelease before draining, since GetReleaseByTag reports none exists.
func TestHandleReleasing_APIModeRetryAfterCreateReleaseFailure(t *testing.T) {
	const headSHA = "retrysha"
	const tagName = "v1.1.0"

	var releaseCalls int32
	var tagExists atomic.Bool
	var releaseExists atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			if r.URL.Query().Get("per_page") == "100" {
				// GetTagForSHA exhaustive path: reflects reality once the tag exists.
				if tagExists.Load() {
					tag := github.Tag{Name: tagName}
					tag.Commit.SHA = headSHA
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]*github.Tag{&tag})
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			} else {
				// tagCoveringCommit's bounded window: deliberately made to miss the
				// tag, so this test exercises the exact-tag drain specifically
				// (not the earlier tagCoveringCommit drain).
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":   "main",
				"commit": map[string]string{"sha": "mainsha"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			if releaseExists.Load() {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Release{TagName: tagName})
			} else {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			}
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			tagExists.Store(true)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			atomic.AddInt32(&releaseCalls, 1)
			if releaseExists.Load() {
				w.WriteHeader(http.StatusCreated)
				return
			}
			// Pass 1 always fails; pass 2 succeeds and marks the release created.
			if atomic.LoadInt32(&releaseCalls) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"server error"}`))
				return
			}
			releaseExists.Store(true)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: tagName})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v", Publish: "api"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{PRNumber: 902, HeadSHA: headSHA, Stage: StageReleasing}
	c.mu.Lock()
	c.activePRs[902] = prState
	c.mu.Unlock()

	// Pass 1: tag creation succeeds, CreateRelease 500s — must return a
	// retryable error and keep the PR tracked.
	err := c.handleReleasing(context.Background(), prState)
	if err == nil {
		t.Fatal("pass 1: expected retryable error after CreateRelease failure, got nil")
	}
	c.mu.RLock()
	_, tracked := c.activePRs[902]
	c.mu.RUnlock()
	if !tracked {
		t.Fatal("pass 1: PR must remain tracked after a transient CreateRelease failure")
	}
	if !tagExists.Load() {
		t.Fatal("pass 1: tag must have been created despite the release publish failure")
	}

	// Pass 2: commit is now seen as already tagged (exhaustive lookup); the
	// idempotence fix must attempt CreateRelease again before draining.
	err = c.handleReleasing(context.Background(), prState)
	if err != nil {
		t.Fatalf("pass 2: expected drain after recovered CreateRelease, got error: %v", err)
	}
	c.mu.RLock()
	_, tracked = c.activePRs[902]
	c.mu.RUnlock()
	if tracked {
		t.Error("pass 2: PR must drain once the release is successfully published")
	}
	if got := atomic.LoadInt32(&releaseCalls); got != 2 {
		t.Errorf("POST /releases call count = %d, want 2 (1 failed + 1 recovered)", got)
	}
}
