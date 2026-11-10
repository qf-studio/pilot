package autopilot

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// The stacked-rebase push must be refused when a replayed commit holds a
// registered value, and the remote branch must stay where it was.
func TestSecretGate_StackedRebasePushRefused(t *testing.T) {
	local := newStackedFixture(t)
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	// "func B() {}" is what the successor's own commit adds.
	secretpatterns.RegisterSecretValues("func B() {}")
	before := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/pilot/GH-2"))

	_, err := rebaseStackedBranch(context.Background(), local, "pilot/GH-2", "main", 2, nil)
	if err == nil || !executor.IsPushRefused(err) {
		t.Fatalf("got %v, want a push refusal", err)
	}
	if strings.Contains(err.Error(), "func B()") {
		t.Errorf("error holds the value: %v", err)
	}
	runFixtureGit(t, local, "fetch", "origin")
	if after := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/pilot/GH-2")); after != before {
		t.Errorf("remote branch moved from %s to %s", before, after)
	}
}
