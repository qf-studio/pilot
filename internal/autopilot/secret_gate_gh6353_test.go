package autopilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH-6353: the two autopilot push sites run no hook of the session, neither from
// the repository's hooks directory nor from a directory the repository config
// names as core.hooksPath.

// writePushHooks writes a pre-push hook that creates prePush, and a
// reference-transaction hook that creates refTx when its git process is a push.
// The site's other git commands (fetch, worktree, merge, commit) are daemon
// commands outside this issue (TASK-651) and update refs too, so the
// reference-transaction hook looks at the command line of its parent.
func writePushHooks(t *testing.T, dir, prePush, refTx string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := map[string]string{
		"pre-push":              "touch " + prePush,
		"reference-transaction": "cat >/dev/null\ncase \" $(ps -o args= -p $PPID) \" in *\" push \"*) touch " + refTx + ";; esac",
	}
	for name, body := range hooks {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// mutation pin: use the old push arguments at this site -> this test fails
func TestConflictPush_HooksDoNotRunInPush(t *testing.T) {
	for _, where := range []string{"hooks directory", "core.hooksPath"} {
		for _, site := range pushSites {
			t.Run(where+"/"+site.name, func(t *testing.T) {
				prePush := filepath.Join(t.TempDir(), "pre-push-ran")
				refTx := filepath.Join(t.TempDir(), "reference-transaction-ran")
				var before string
				local, branch := site.run(t, func(local string) {
					hooksDir := filepath.Join(gitDirOf(t, local), "hooks")
					if where == "core.hooksPath" {
						hooksDir = t.TempDir()
						runFixtureGit(t, local, "config", "core.hooksPath", hooksDir)
					}
					writePushHooks(t, hooksDir, prePush, refTx)
					before = strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branchOf(site.name)))
				})
				for marker, what := range map[string]string{prePush: "the pre-push hook", refTx: "the reference-transaction hook"} {
					if _, err := os.Stat(marker); err == nil {
						t.Errorf("%s ran in the push: git executed a program the session chose", what)
					}
				}
				runFixtureGit(t, local, "fetch", "origin")
				if after := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/"+branch)); after == before {
					t.Errorf("the push did not reach the remote: origin/%s stays at %s", branch, after)
				}

				// Both hooks run in a plain push from the same repository.
				runFixtureGit(t, local, "push", "origin", "HEAD:refs/heads/pilot-sanity")
				for marker, what := range map[string]string{prePush: "the pre-push hook", refTx: "the reference-transaction hook"} {
					if _, err := os.Stat(marker); err != nil {
						t.Fatalf("fixture: %s does not run in a push from this repository", what)
					}
				}
			})
		}
	}
}
