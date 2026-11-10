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

// GH-6272: the go.mod/go.sum mechanical-resolution push goes through the gate
// too. The replayed "add depa" commit holds a registered value, so the push is
// refused and the remote branch stays where it was.
func TestSecretGate_GoModResolutionPushRefused(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)

	newLocalReplaceModule(t, local, "depa", "depa")
	newLocalReplaceModule(t, local, "depb", "depb")
	writeFixtureFile(t, local, "go.mod", strings.Join([]string{
		"module fixture", "", "go 1.25", "",
		"replace example.com/depa => ./localdeps/depa", "",
		"replace example.com/depb => ./localdeps/depb", "",
	}, "\n"))
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "scaffold local replace modules")
	runFixtureGit(t, local, "push", "origin", "main")

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a")
	appendFixtureFile(t, local, "go.mod", "require example.com/depa v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "usea.go", "package fixture\n\nimport \"example.com/depa\"\n\nvar UseDepA = depa.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depa")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a")

	runFixtureGit(t, local, "checkout", "main")
	appendFixtureFile(t, local, "go.mod", "require example.com/depb v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "useb.go", "package fixture\n\nimport \"example.com/depb\"\n\nvar UseDepB = depb.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depb")
	runFixtureGit(t, local, "push", "origin", "main")

	// What the push would publish on top of origin/main: the depa commit.
	secretpatterns.RegisterSecretValues("var UseDepA = depa.Hello()")
	before := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/feature/dep-a"))

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/dep-a", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if !isGoModSumOnlyConflict(result.ConflictedFiles) {
		t.Fatalf("expected a go.mod/go.sum-only conflict, got %v", result.ConflictedFiles)
	}

	err = resolveGoModSumConflict(ctx, result.WorktreePath, "feature/dep-a", result.ConflictedFiles)
	if err == nil || !executor.IsPushRefused(err) {
		t.Fatalf("got %v, want a push refusal", err)
	}
	if strings.Contains(err.Error(), "UseDepA") {
		t.Errorf("error holds the value: %v", err)
	}
	runFixtureGit(t, local, "fetch", "origin")
	if after := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/feature/dep-a")); after != before {
		t.Errorf("remote branch moved from %s to %s", before, after)
	}
}
