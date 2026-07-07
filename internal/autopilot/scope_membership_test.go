package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// scopeMembershipFakeIssue describes one issue served by the fake GitHub
// server used across TestHeldByScope subtests.
type scopeMembershipFakeIssue struct {
	title  string
	body   string
	state  string
	labels []string
}

// newScopeMembershipController wires a Controller with the given effective
// ReleaseConfig against a fake GitHub server serving issues from the
// provided map (keyed by issue number). Missing issue numbers 404.
func newScopeMembershipController(t *testing.T, rel *ReleaseConfig, issues map[int]scopeMembershipFakeIssue) *Controller {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var num int
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d", &num); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		issue, ok := issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		labels := make([]github.Label, 0, len(issue.labels))
		for _, name := range issue.labels {
			labels = append(labels, github.Label{Name: name})
		}
		state := issue.state
		if state == "" {
			state = "open"
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(github.Issue{
			Number: num,
			Title:  issue.title,
			Body:   issue.body,
			State:  state,
			Labels: labels,
		})
	}))
	t.Cleanup(server.Close)

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = rel
	return NewController(cfg, ghClient, nil, "owner", "repo")
}

func TestHeldByScope(t *testing.T) {
	scopeRel := &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:"}

	t.Run("not held: scope release disabled (on_merge)", func(t *testing.T) {
		c := newScopeMembershipController(t, &ReleaseConfig{Enabled: true, Trigger: "on_merge"}, map[int]scopeMembershipFakeIssue{
			10: {title: "child", body: "Parent: GH-1"},
			1:  {title: "epic", state: "open"},
		})
		_, _, held := c.heldByScope(context.Background(), 10)
		if held {
			t.Error("held = true, want false (trigger is on_merge, not on_scope_close)")
		}
	})

	t.Run("not held: issueNum is 0 (standalone/unknown)", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{})
		_, _, held := c.heldByScope(context.Background(), 0)
		if held {
			t.Error("held = true, want false (issueNum 0 is never a scope member)")
		}
	})

	t.Run("held: open epic parent wins", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{
			10: {title: "child", body: "Parent: GH-1", labels: []string{"scope:checkout"}},
			1:  {title: "Checkout epic", state: "open"},
		})
		key, title, held := c.heldByScope(context.Background(), 10)
		if !held {
			t.Fatal("held = false, want true (open epic parent)")
		}
		if key != "epic:1" {
			t.Errorf("scopeKey = %q, want %q", key, "epic:1")
		}
		if title != "Checkout epic" {
			t.Errorf("scopeTitle = %q, want %q", title, "Checkout epic")
		}
	})

	t.Run("not held: epic parent closed falls through, no label present", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{
			10: {title: "child", body: "Parent: GH-1"},
			1:  {title: "epic", state: "closed"},
		})
		_, _, held := c.heldByScope(context.Background(), 10)
		if held {
			t.Error("held = true, want false (parent already closed — late straggler releases per-merge)")
		}
	})

	t.Run("held: scope label, case-insensitive prefix match", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{
			20: {title: "standalone", labels: []string{"priority:high", "SCOPE:Billing"}},
		})
		key, title, held := c.heldByScope(context.Background(), 20)
		if !held {
			t.Fatal("held = false, want true (scope-prefixed label present)")
		}
		if key != "label:Billing" {
			t.Errorf("scopeKey = %q, want %q", key, "label:Billing")
		}
		if title != "SCOPE:Billing" {
			t.Errorf("scopeTitle = %q, want %q", title, "SCOPE:Billing")
		}
	})

	t.Run("not held: no epic parent and no scope label", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{
			30: {title: "standalone", labels: []string{"priority:high"}},
		})
		_, _, held := c.heldByScope(context.Background(), 30)
		if held {
			t.Error("held = true, want false (no scope signal)")
		}
	})

	t.Run("not held: GetIssue error fails open", func(t *testing.T) {
		c := newScopeMembershipController(t, scopeRel, map[int]scopeMembershipFakeIssue{})
		// Issue 99 is not in the map, so the fake server 404s.
		_, _, held := c.heldByScope(context.Background(), 99)
		if held {
			t.Error("held = true, want false (API error must fail open, never hold forever)")
		}
	})
}
