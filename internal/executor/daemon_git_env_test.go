package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-651 (GH-6529): the daemon-side git commands of the delivery of an agent
// workspace drop each inherited GIT_ name, the gate commands get no token, and
// the fsmonitor program of a repository runs for no remote command.

// daemonGitEnvRecord is one git command of the daemon user that the env
// wrapper logged: the GIT_ and token names of its environment, its directory
// and its arguments.
type daemonGitEnvRecord struct {
	names []string
	dir   string
	args  string
}

func (r daemonGitEnvRecord) has(name string) bool { return slices.Contains(r.names, name) }

// installGitEnvWrapper puts a git wrapper first on PATH that, while the flag
// file exists, writes one file per git command of the daemon user (the agent
// side exports handOverPushAgentMark) with the GIT_ names, GITHUB_TOKEN and
// PILOT_GIT_TOKEN of its environment. The paths are in the script, not in a
// variable: the gate commands get no variable of the test.
func installGitEnvWrapper(t *testing.T) (flag, logDir string) {
	t.Helper()
	next, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	flag = filepath.Join(t.TempDir(), "logging")
	logDir = t.TempDir()
	wrapper := fmt.Sprintf(`#!/bin/sh
if [ -e '%[1]s' ] && [ -z "$%[2]s" ]; then
  f=$(mktemp '%[3]s/cmd.XXXXXX')
  { env | grep -E '^(GIT_[A-Za-z0-9_]*|GITHUB_TOKEN|PILOT_GIT_TOKEN)=' | cut -d= -f1; echo '--'; pwd -P; printf '%%s\n' "$*"; } > "$f"
fi
exec '%[4]s' "$@"
`, flag, handOverPushAgentMark, logDir, next)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(wrapper), 0o755); err != nil { //nolint:gosec // wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return flag, logDir
}

func readGitEnvRecords(t *testing.T, logDir string) []daemonGitEnvRecord {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	var recs []daemonGitEnvRecord
	for _, ent := range entries {
		raw, err := os.ReadFile(filepath.Join(logDir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		head, rest, _ := strings.Cut(string(raw), "--\n")
		dir, args, _ := strings.Cut(rest, "\n")
		recs = append(recs, daemonGitEnvRecord{names: strings.Fields(head), dir: dir, args: strings.TrimSpace(args)})
	}
	return recs
}

// TestDaemonGitEnvDropsInheritedGitNames: with GIT_ names of another
// repository, history, exec path, ssh command, trace and replace base in the
// daemon environment, a Push of an agent workspace lands on the remote of the
// daemon's repository, and no daemon-side git command of the delivery (the
// gate, the push, the probes, the fetch, the count, the co-author read) has
// one of them. The gate commands get no GITHUB_TOKEN and no PILOT_GIT_TOKEN;
// the push keeps the credentials.
// mutation pin: daemonGitEnv drops only the old deny list -> this test fails
// mutation pin: the co-author read without withDaemonGitEnv -> this test fails
// mutation pin: gateRepoKey without the gate environment -> this test fails
func TestDaemonGitEnvDropsInheritedGitNames(t *testing.T) {
	e := newHandOverPushEnv(t)
	SetGitCredentialProvider(func(context.Context) (string, error) { return testutil.FakeGitHubToken, nil })
	t.Cleanup(func() { SetGitCredentialProvider(nil) })
	ctx := context.Background()
	commitFile(t, e.ws, "a.txt", "one")
	head := commitFile(t, e.ws, "b.txt", "two")

	otherRemote := filepath.Join(t.TempDir(), "other-remote.git")
	wsGit(t, filepath.Dir(otherRemote), "init", "-q", "--bare", otherRemote)
	other := filepath.Join(t.TempDir(), "other")
	wsGit(t, filepath.Dir(other), "init", "-q", other)
	wsGit(t, other, "remote", "add", "origin", otherRemote)
	gitTestCommit(t, other, "other")
	wsGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
	otherRefs := wsGit(t, otherRemote, "for-each-ref")
	history := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(history, []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flag, logDir := installGitEnvWrapper(t)

	inherited := map[string]string{
		"GIT_COMMON_DIR":       filepath.Join(other, ".git"),
		"GIT_GRAFT_FILE":       history,
		"GIT_SHALLOW_FILE":     history,
		"GIT_EXEC_PATH":        t.TempDir(),
		"GIT_SSH_COMMAND":      "false",
		"GIT_TRACE":            filepath.Join(t.TempDir(), "trace"),
		"GIT_REPLACE_REF_BASE": "refs/evil/",
	}
	for k, v := range inherited {
		t.Setenv(k, v)
	}
	t.Setenv("GITHUB_TOKEN", testutil.FakeGitHubToken)
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pushErr := e.git.Push(ctx, e.branch, handOverTestTask)
	remoteSHA, remoteOK := e.git.RemoteBranchHead(ctx, e.branch)
	probed, probeErr := e.git.ProbeRemoteBranch(ctx, e.branch)
	count, countErr := e.git.CountNewCommitsAgainstOrigin(ctx, "main")
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	for k := range inherited {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	if pushErr != nil {
		t.Errorf("Push with inherited GIT_ names: %v", pushErr)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want %q", got, head)
	}
	if got := wsGit(t, otherRemote, "for-each-ref"); got != otherRefs {
		t.Errorf("the push reached the remote of the other repository: %s", got)
	}
	if !remoteOK || remoteSHA != head {
		t.Errorf("RemoteBranchHead = %q, %v; want %q", remoteSHA, remoteOK, head)
	}
	if probeErr != nil || !probed {
		t.Errorf("ProbeRemoteBranch = %v, %v; want true", probed, probeErr)
	}
	if countErr != nil || count != 2 {
		t.Errorf("CountNewCommitsAgainstOrigin = %d, %v; want 2", count, countErr)
	}

	seen := map[string]int{}
	kinds := map[string]string{
		"gate":     "rev-parse --git-common-dir",
		"push":     " push ",
		"probe":    " ls-remote ",
		"fetch":    " fetch ",
		"count":    "rev-list --count",
		"coauthor": "--format=%H%x00%B%x00",
	}
	for _, r := range readGitEnvRecords(t, logDir) {
		for kind, part := range kinds {
			if strings.Contains(r.args, part) {
				seen[kind]++
			}
		}
		for name := range inherited {
			if r.has(name) {
				t.Errorf("daemon-side git command has the inherited %s: dir=%s args=%s", name, r.dir, r.args)
			}
		}
		// The gate commands: the gate settings, not the co-author read.
		if strings.Contains(r.args, "log.showRoot=true") && !strings.Contains(r.args, kinds["coauthor"]) {
			for _, name := range []string{"GITHUB_TOKEN", "PILOT_GIT_TOKEN"} {
				if r.has(name) {
					t.Errorf("gate git command has %s: %s", name, r.args)
				}
			}
		}
		if strings.Contains(r.args, kinds["push"]) {
			for _, name := range []string{"GIT_ASKPASS", "PILOT_GIT_TOKEN", "GIT_TERMINAL_PROMPT"} {
				if !r.has(name) {
					t.Errorf("push without the credential variable %s: %v", name, r.names)
				}
			}
		}
	}
	for kind := range kinds {
		if seen[kind] == 0 {
			t.Errorf("the wrapper logged no %s command of the daemon user", kind)
		}
	}
}

// writeFsmonitor sets core.fsmonitor of repo to a program that writes marker.
func writeFsmonitor(t *testing.T, repo, marker string) {
	t.Helper()
	prog := filepath.Join(t.TempDir(), "fsmonitor")
	body := fmt.Sprintf("#!/bin/sh\necho fsmonitor \"$@\" >> '%s'\n", marker)
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil { //nolint:gosec // program must be executable
		t.Fatal(err)
	}
	wsGit(t, repo, "config", "core.fsmonitor", prog)
}

// TestHooksOffFsmonitorDoesNotRunUnderCount: a core.fsmonitor program in the
// config of the repository runs for no command of CountNewCommitsAgainstOrigin
// (its fetch reads the index), with the option set and with it empty.
// mutation pin: HooksOffArgs without core.fsmonitor=false -> this test fails
func TestHooksOffFsmonitorDoesNotRunUnderCount(t *testing.T) {
	t.Run("option set", func(t *testing.T) {
		e := newHandOverPushEnv(t)
		ctx := context.Background()
		commitFile(t, e.ws, "a.txt", "one")
		marker := filepath.Join(t.TempDir(), "marker")
		writeFsmonitor(t, e.repo, marker)

		if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != 1 {
			t.Errorf("CountNewCommitsAgainstOrigin before the push = %d, %v; want 1", n, err)
		}
		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("the fsmonitor program of the daemon's repository ran before the push: %q", raw)
		}
		if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
			t.Fatalf("Push: %v", err)
		}
		_ = os.Remove(marker)
		if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != 1 {
			t.Errorf("CountNewCommitsAgainstOrigin after the push = %d, %v; want 1", n, err)
		}
		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("the fsmonitor program of the daemon's repository ran after the push: %q", raw)
		}
	})
	t.Run("option empty", func(t *testing.T) {
		setRunAsStateForTest(t, nil, "")
		repo, remote := setupTestRepoWithRemote(t)
		t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
		g := newGitOperationsForWorkspace(repo, repo)
		if g.daemonRepo != "" {
			t.Fatalf("option empty: daemonRepo = %q, want empty", g.daemonRepo)
		}
		marker := filepath.Join(t.TempDir(), "marker")
		writeFsmonitor(t, repo, marker)

		if _, err := g.CountNewCommitsAgainstOrigin(context.Background(), "main"); err != nil {
			t.Errorf("CountNewCommitsAgainstOrigin: %v", err)
		}
		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("the fsmonitor program of the repository ran with the option empty: %q", raw)
		}
	})
}
