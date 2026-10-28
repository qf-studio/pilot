package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushScenario is a clone on a pilot branch (one commit over main) whose
// origin already holds a different commit under the same branch name — the
// shape behind GH-5729, where a rebased continuation cannot fast-forward.
type pushScenario struct {
	dir        string // clone the runner works in
	bare       string // origin
	branch     string
	localHead  string
	remoteHead string // origin/<branch> before the run (not an ancestor of localHead)
	mainSHA    string
	pushLog    string // file the fake git appends one line per `git push` to
}

// newPushScenario builds the scenario and puts a recording `git` wrapper on
// PATH. With failAfterPush the wrapper performs the real push and then exits 1
// (the GH-1389 chdir false positive: data landed, git reports an error).
func newPushScenario(t *testing.T, branch string, failAfterPush bool) *pushScenario {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}

	dir, bare := setupFreshnessRepo(t)
	runGit(t, dir, "commit", "--allow-empty", "-m", "base")
	runGit(t, dir, "push", "origin", "main")
	mainSHA := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))

	// Another clone publishes the branch's "old head" to origin.
	other := t.TempDir()
	runGit(t, other, "clone", bare, ".")
	runGit(t, other, "config", "user.email", "test@test.com")
	runGit(t, other, "config", "user.name", "Test User")
	runGit(t, other, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(other, "old.txt"), []byte("old head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "old.txt")
	runGit(t, other, "commit", "-m", "feat(executor): old head")
	runGit(t, other, "push", "origin", branch)
	remoteHead := strings.TrimSpace(gitOutput(t, other, "rev-parse", "HEAD"))

	// The local branch is cut from main without the old head: a rebase result.
	runGit(t, dir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("rebased\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "new.txt")
	runGit(t, dir, "commit", "-m", "feat(executor): rebased work")
	localHead := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))

	fakeBin := t.TempDir()
	pushLog := filepath.Join(fakeBin, "pushes.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = push ]; then echo \"$@\" >> " + pushLog + "; fi\n"
	if failAfterPush {
		script += "if [ \"$1\" = push ]; then " + realGit + " \"$@\"; echo 'error: chdir after push' >&2; exit 1; fi\n"
	}
	script += "exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // test wrapper must be executable
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	return &pushScenario{dir: dir, bare: bare, branch: branch, localHead: localHead, remoteHead: remoteHead, mainSHA: mainSHA, pushLog: pushLog}
}

func (s *pushScenario) pushes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.pushLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (s *pushScenario) originHead(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, s.bare, "rev-parse", "refs/heads/"+s.branch))
}

// run executes a task on the scenario's branch with a fake gh that reports an
// open PR for it, so a swallowed push error would surface as an adopted PR.
func (s *pushScenario) run(t *testing.T, id string, fromPR int, fixFromSHA string) *ExecutionResult {
	t.Helper()
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[{"url":"https://github.com/o/r/pull/408"}]`))

	runner := NewRunnerWithBackend(&mockFixedBackend{result: &BackendResult{Success: true, Output: "implementation complete"}})
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{UseWorktree: boolPtr(false), SkipSelfReview: true}

	task := &Task{
		ID:          id,
		Title:       "fix(executor): push guard",
		Description: "GH-5729 push guard scenario",
		ProjectPath: s.dir,
		Branch:      s.branch,
		CreatePR:    true,
		FromPR:      fromPR,
		FixFromSHA:  fixFromSHA,
	}
	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	return result
}

// TestPushRejectedNonFastForward_NotSwallowed (GH-5729): origin has the branch
// at a different commit, so the push is rejected non-fast-forward. The old
// "branch exists on remote" shortcut cleared that error and adopted the stale
// PR as success.
func TestPushRejectedNonFastForward_NotSwallowed(t *testing.T) {
	s := newPushScenario(t, "pilot/GH-5729-a", false)

	result := s.run(t, "GH-5729-a", 0, "")

	if result.Success {
		t.Fatalf("Success = true, want the rejected push to fail the task (PR=%q)", result.PRUrl)
	}
	if result.PRUrl != "" {
		t.Errorf("PRUrl = %q, want no PR adoption", result.PRUrl)
	}
	// GH-5762: the rejection is reported as the deterministic non-fast-forward
	// escalation, naming the remote head, rather than git's raw stderr line.
	if !strings.Contains(result.Error, nonFastForwardTaskBranchMarker) || !strings.Contains(result.Error, s.remoteHead) {
		t.Errorf("Error = %q, want the non-fast-forward escalation naming %s", result.Error, s.remoteHead)
	}
	if got := s.pushes(t); len(got) != 1 {
		t.Errorf("push invocations = %d (%q), want 1: a non-fast-forward rejection is not retried", len(got), got)
	}
	if got := s.originHead(t); got != s.remoteHead {
		t.Errorf("origin head = %s, want it untouched at %s", got, s.remoteHead)
	}
}

// TestPushChdirFalsePositive_AcceptedWhenHeadsMatch (GH-1389): push reports an
// error but the data landed, so origin's head equals local HEAD.
func TestPushChdirFalsePositive_AcceptedWhenHeadsMatch(t *testing.T) {
	s := newPushScenario(t, "pilot/GH-5729-b", true)
	// Make the branch fast-forwardable so the wrapper's real push lands.
	runGit(t, s.dir, "fetch", "origin", s.branch)
	runGit(t, s.dir, "reset", "--hard", s.remoteHead)
	if err := os.WriteFile(filepath.Join(s.dir, "next.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, s.dir, "add", "next.txt")
	runGit(t, s.dir, "commit", "-m", "feat(executor): next")
	localHead := strings.TrimSpace(gitOutput(t, s.dir, "rev-parse", "HEAD"))

	result := s.run(t, "GH-5729-b", 0, "")

	if !result.Success {
		t.Fatalf("Success = false (%q), want the false positive accepted", result.Error)
	}
	if got := s.originHead(t); got != localHead {
		t.Errorf("origin head = %s, want %s", got, localHead)
	}
	if got := s.pushes(t); len(got) != 1 {
		t.Errorf("push invocations = %d, want 1", len(got))
	}
}

// TestPushAutopilotFixContinuation_ForceWithLeaseOnFooterSHA (GH-5729): a
// rebase continuation whose origin head is still the footer SHA is pushed once
// more with a lease on that SHA — never a plain force.
func TestPushAutopilotFixContinuation_ForceWithLeaseOnFooterSHA(t *testing.T) {
	s := newPushScenario(t, "pilot/GH-5729-c", false)

	result := s.run(t, "GH-5729-c", 403, s.remoteHead)

	if !result.Success {
		t.Fatalf("Success = false (%q), want the lease push to land", result.Error)
	}
	if got := s.originHead(t); got != s.localHead {
		t.Errorf("origin head = %s, want the rebased head %s", got, s.localHead)
	}
	pushes := s.pushes(t)
	if len(pushes) != 2 {
		t.Fatalf("push invocations = %q, want a plain push then exactly one lease push", pushes)
	}
	wantLease := "--force-with-lease=refs/heads/" + s.branch + ":" + s.remoteHead
	if strings.Contains(pushes[0], "force") {
		t.Errorf("first push = %q, want a plain push", pushes[0])
	}
	if !strings.Contains(pushes[1], wantLease) {
		t.Errorf("second push = %q, want it to carry %q", pushes[1], wantLease)
	}
	for _, p := range pushes {
		for _, f := range strings.Fields(p) {
			if f == "--force" || f == "-f" {
				t.Errorf("push %q uses a plain force", p)
			}
		}
	}
}

// TestPushAutopilotFixContinuation_NoForceWhenRemoteMoved (GH-5729): origin's
// head is neither the local HEAD nor the footer SHA, so something landed since
// the footer was recorded; the push is not forced and the error names the SHAs.
func TestPushAutopilotFixContinuation_NoForceWhenRemoteMoved(t *testing.T) {
	s := newPushScenario(t, "pilot/GH-5729-d", false)

	result := s.run(t, "GH-5729-d", 403, s.mainSHA) // footer SHA != origin's head

	if result.Success {
		t.Fatalf("Success = true, want failure (PR=%q)", result.PRUrl)
	}
	if got := s.pushes(t); len(got) != 1 {
		t.Errorf("push invocations = %q, want only the plain push (no force)", got)
	}
	if got := s.originHead(t); got != s.remoteHead {
		t.Errorf("origin head = %s, want it untouched at %s", got, s.remoteHead)
	}
	for _, sha := range []string{s.remoteHead, s.localHead, s.mainSHA} {
		if !strings.Contains(result.Error, sha) {
			t.Errorf("Error = %q, want it to name %s", result.Error, sha)
		}
	}
}
