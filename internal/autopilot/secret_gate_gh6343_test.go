package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6343: the two autopilot push sites send the arguments of the executor's
// pushArgs, so a hook the session wrote and push.followTags do not act in them.

// pushSite is one autopilot push site; run builds the fixture, calls setup with
// the local clone, runs the site and returns the clone and the pushed branch.
type pushSite struct {
	name string
	run  func(t *testing.T, setup func(local string)) (local, branch string)
}

func gitDirOf(t *testing.T, local string) string {
	t.Helper()
	return filepath.Join(local, ".git")
}

func writeHook(t *testing.T, local, name, body string) {
	t.Helper()
	dir := filepath.Join(gitDirOf(t, local), "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newGoModConflictFixture is the repository of the go.mod/go.sum conflict:
// feature/dep-a and main each add a require line.
func newGoModConflictFixture(t *testing.T) string {
	t.Helper()
	local := newFixtureRepo(t)
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
	return local
}

var pushSites = []pushSite{
	{
		name: "GoModResolution",
		run: func(t *testing.T, setup func(local string)) (string, string) {
			local := newGoModConflictFixture(t)
			setup(local)
			ctx := context.Background()
			result, cleanup, err := attemptLocalMerge(ctx, local, "feature/dep-a", "main")
			defer cleanup()
			if err != nil {
				t.Fatalf("attemptLocalMerge: %v", err)
			}
			if !isGoModSumOnlyConflict(result.ConflictedFiles) {
				t.Fatalf("expected a go.mod/go.sum-only conflict, got %v", result.ConflictedFiles)
			}
			if err := resolveGoModSumConflict(ctx, result.WorktreePath, "feature/dep-a", result.ConflictedFiles); err != nil {
				t.Fatalf("resolveGoModSumConflict: %v", err)
			}
			return local, "feature/dep-a"
		},
	},
	{
		name: "StackedRebase",
		run: func(t *testing.T, setup func(local string)) (string, string) {
			local := newStackedFixture(t)
			setup(local)
			if _, err := rebaseStackedBranch(context.Background(), local, "pilot/GH-2", "main", 2, nil); err != nil {
				t.Fatalf("rebaseStackedBranch: %v", err)
			}
			return local, "pilot/GH-2"
		},
	},
}

func remoteOf(t *testing.T, local string) string {
	t.Helper()
	return strings.TrimSpace(runFixtureGit(t, local, "remote", "get-url", "origin"))
}

// mutation pin: use the old push arguments at this site -> this test fails
func TestConflictPush_PrePushHookDoesNotRun(t *testing.T) {
	for _, site := range pushSites {
		t.Run(site.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "hook-ran")
			var before string
			local, branch := site.run(t, func(local string) {
				writeHook(t, local, "pre-push", "touch "+marker)
				before = strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branchOf(site.name)))
			})
			if _, err := os.Stat(marker); err == nil {
				t.Error("the pre-push hook ran: git executed a program the session chose")
			}
			runFixtureGit(t, local, "fetch", "origin")
			if after := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branch)); after == before {
				t.Errorf("the push did not reach the remote: origin/%s stays at %s", branch, after)
			}
		})
	}
}

func branchOf(site string) string {
	if site == "GoModResolution" {
		return "feature/dep-a"
	}
	return "pilot/GH-2"
}

// mutation pin: use the old push arguments at this site -> this test fails
func TestConflictPush_AnnotatedTagIsNotFollowed(t *testing.T) {
	for _, site := range pushSites {
		t.Run(site.name, func(t *testing.T) {
			var local string
			site.run(t, func(l string) {
				local = l
				runFixtureGit(t, l, "config", "push.followTags", "true")
				// The commit the site creates in its scratch worktree gets an annotated
				// tag, so the tag points at a commit the push publishes.
				writeHook(t, l, "post-commit", "git tag -a -f v-secret -m 'release' HEAD")
			})
			if tags := runFixtureGit(t, local, "tag", "--list"); !strings.Contains(tags, "v-secret") {
				t.Fatalf("fixture: the site did not create the annotated tag (hook did not run): %q", tags)
			}
			if tags := strings.TrimSpace(runFixtureGit(t, remoteOf(t, local), "tag", "--list")); tags != "" {
				t.Errorf("the annotated tag reached the remote: %q", tags)
			}
		})
	}
}

// A clean run of each site still pushes the branch.
func TestConflictPush_CleanPushStillPasses(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	for _, site := range pushSites {
		t.Run(site.name, func(t *testing.T) {
			var before string
			local, branch := site.run(t, func(local string) {
				before = strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branchOf(site.name)))
			})
			runFixtureGit(t, local, "fetch", "origin")
			if after := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branch)); after == before {
				t.Errorf("origin/%s did not move from %s", branch, before)
			}
		})
	}
}
