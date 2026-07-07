package autopilot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestEnrichScopeReleaseNotes verifies the "workflow" publish-mode composition
// order for a scope carrier: LLM "## What's New" + deterministic scope notes
// + GoReleaser's original body — and that the deterministic scope notes still
// ship when the LLM step is disabled or unconfigured (GH-3992 edge cases).
func TestEnrichScopeReleaseNotes(t *testing.T) {
	const scopeNotes = "# Checkout epic\n\n## Features\n- add coupon field (#101, GH-201)"
	const originalBody = "* commit 1\n* commit 2 (GoReleaser-generated)"
	const llmSummary = "## What's New in v1.1.0\n\n- Coupons at checkout"

	tests := []struct {
		name            string
		generateSummary bool
		withGenerator   bool
		wantSummary     bool
	}{
		{name: "LLM enabled and configured", generateSummary: true, withGenerator: true, wantSummary: true},
		{name: "generate_summary false skips LLM but ships scope notes", generateSummary: false, withGenerator: true, wantSummary: false},
		{name: "no generator configured (no ANTHROPIC_API_KEY) skips LLM but ships scope notes", generateSummary: true, withGenerator: false, wantSummary: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var updatedBody string
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"id":42,"tag_name":"v1.1.0","body":%q}`, originalBody)
				case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/releases/42"):
					var input map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&input)
					updatedBody, _ = input["body"].(string)
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprint(w, `{"id":42,"tag_name":"v1.1.0"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ghServer.Close()

			anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"content":[{"text":%q}]}`, llmSummary)
			}))
			defer anthropicServer.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ghServer.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

			if tt.withGenerator {
				gen := &ReleaseSummaryGenerator{
					ghClient:   ghClient,
					apiKey:     testutil.FakeAnthropicKey,
					httpClient: http.DefaultClient,
					apiURL:     anthropicServer.URL,
					log:        slog.Default(),
				}
				c.SetReleaseSummaryGenerator(gen)
			}

			rel := &ReleaseConfig{GenerateSummary: tt.generateSummary}
			commits := []*github.Commit{makeCommit("feat(checkout): add coupon field")}

			c.enrichScopeReleaseNotes("owner", "repo", "v1.1.0", commits, rel, scopeNotes)

			if !strings.Contains(updatedBody, scopeNotes) {
				t.Errorf("updated body must contain scope notes; got %q", updatedBody)
			}
			if !strings.Contains(updatedBody, originalBody) {
				t.Errorf("updated body must preserve the original workflow body; got %q", updatedBody)
			}
			if tt.wantSummary != strings.Contains(updatedBody, "What's New") {
				t.Errorf("What's New presence = %v, want %v; got %q",
					strings.Contains(updatedBody, "What's New"), tt.wantSummary, updatedBody)
			}

			scopeIdx := strings.Index(updatedBody, scopeNotes)
			origIdx := strings.Index(updatedBody, originalBody)
			if scopeIdx == -1 || origIdx == -1 || scopeIdx > origIdx {
				t.Errorf("expected scope notes before original body; got %q", updatedBody)
			}
			if tt.wantSummary {
				summaryIdx := strings.Index(updatedBody, "What's New")
				if summaryIdx == -1 || summaryIdx > scopeIdx {
					t.Errorf("expected LLM summary before scope notes; got %q", updatedBody)
				}
			}
		})
	}
}
