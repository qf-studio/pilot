package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TASK-651: with run_as_user set, the gh commands of the daemon name the
// repository of the daemon's origin remote with its host, and the gh calls of
// epic.go never run in an agent workspace. No test needs root: sudo is the
// mock of the workspace tests.

func TestGhRepoArgKeepsHost(t *testing.T) {
	tests := []struct {
		name, remote, want string
		wantErr            bool
	}{
		{"github.com https", "https://github.com/owner/name.git", "owner/name", false},
		{"github.com https with a user", "https://x-access-token:fake-token@github.com/owner/name", "owner/name", false},
		{"github.com scp-like", "git@github.com:owner/name.git", "owner/name", false},
		{"github.com ssh", "ssh://git@github.com/owner/name.git", "owner/name", false},
		{"another host https", "https://ghe.example.com/owner/name.git", "ghe.example.com/owner/name", false},
		{"another host scp-like", "git@ghe.example.com:owner/name.git", "ghe.example.com/owner/name", false},
		{"another host with a port", "ssh://git@ghe.example.com:2222/owner/name.git", "ghe.example.com/owner/name", false},
		{"github.com with a port", "https://github.com:443/owner/name", "owner/name", false},
		{"a final slash after .git", "https://github.com/owner/name.git/", "owner/name", false},
		{"another host, a final slash after .git", "git@ghe.example.com:owner/name.git/", "ghe.example.com/owner/name", false},
		{"one path segment with credentials", "https://some-user:fake-secret-token@ghe.example.com/name.git", "", true},
		{"scp-like one path segment with a user", "deploy-user@ghe.example.com:name.git", "", true},
		{"a local path", "/srv/repos/owner/name.git", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ghRepoArg(tt.remote)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ghRepoArg(%q) = %q, want an error", tt.remote, got)
				}
				for _, cred := range []string{"some-user", "fake-secret-token", "deploy-user"} {
					if strings.Contains(err.Error(), cred) {
						t.Errorf("error %q holds the credential text %q", err, cred)
					}
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("ghRepoArg(%q) = %q, %v; want %q", tt.remote, got, err, tt.want)
			}
		})
	}
}

// epicWorkspaceGhRemote is the origin remote of the daemon's repository in the
// epic tests: another host, so the --repo argument holds it.
const epicWorkspaceGhRemote = "https://ghe.example.com/daemon-owner/daemon-repo.git"

// mockEpicGh puts a fake gh first on PATH that appends "DIR| ARGS" per call to
// the returned file and answers each issue subcommand of epic.go.
func mockEpicGh(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh-calls")
	body := `#!/bin/sh
echo "$(pwd -P)| $*" >> '` + logPath + `'
case "$1 $2" in
"issue list") echo '[]' ;;
"issue create") echo "https://ghe.example.com/daemon-owner/daemon-repo/issues/9" ;;
"issue view")
  case "$*" in
  *--jq*) echo OPEN ;;
  *) echo '{"state":"OPEN","labels":[]}' ;;
  esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// runSixEpicGhCalls runs the six gh calls of epic.go with path as the
// execution path and returns their errors (nil for a call that returns none).
func runSixEpicGhCalls(t *testing.T, path string) map[string]error {
	t.Helper()
	ctx := context.Background()
	r := newSilentRunnerTask359()
	errs := map[string]error{}
	_, errs["queryRecentSubIssues"] = queryRecentSubIssues(ctx, path, "GH-1")
	_, errs["recoverExistingSubIssues"] = recoverExistingSubIssues(ctx, path, "GH-1")
	_, errs["runGHIssueCreateWithRetry"] = r.runGHIssueCreateWithRetry(ctx, []string{"issue", "create", "--title", "t", "--body", "b"}, path, 1)
	errs["UpdateIssueProgress"] = r.UpdateIssueProgress(ctx, path, "1", "progress")
	errs["CloseIssueWithComment"] = r.CloseIssueWithComment(ctx, path, "1", "done")
	_ = queryParentDoneViaGitHub("GH-1", path)
	return errs
}

func TestEpicGhCallsOfAWorkspaceRunInTheDaemonRepo(t *testing.T) {
	e := newHandOverPushEnv(t)
	wsGit(t, e.repo, "remote", "set-url", "origin", epicWorkspaceGhRemote)
	// The agent plants a remote in the workspace.
	wsGit(t, e.ws, "remote", "set-url", "origin", "https://github.com/attacker/evil.git")
	ghLog := mockEpicGh(t)

	for name, err := range runSixEpicGhCalls(t, e.ws) {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	calls := sudoCalls(t, ghLog)
	// The close call reads the state of the issue first: seven gh commands.
	want := []string{"issue list", "issue list", "issue create", "issue comment", "issue view", "issue close", "issue view"}
	if len(calls) != len(want) {
		t.Fatalf("gh calls = %d, want %d:\n%s", len(calls), len(want), strings.Join(calls, "\n"))
	}
	daemonDir := resolvedTestPath(t, e.repo)
	for i, c := range calls {
		dir, args, _ := strings.Cut(c, "| ")
		if !strings.HasPrefix(args, want[i]+" ") {
			t.Errorf("gh call %d = %s, want %s", i, args, want[i])
		}
		if dir != daemonDir {
			t.Errorf("gh ran in %s, want the daemon's repository %s: %s", dir, daemonDir, args)
		}
		if !strings.Contains(args, "--repo ghe.example.com/daemon-owner/daemon-repo") {
			t.Errorf("gh without --repo HOST/OWNER/NAME of the daemon's remote: %s", args)
		}
		if strings.Contains(args, "attacker") || strings.Contains(args, "evil") {
			t.Errorf("gh took the remote of the workspace: %s", args)
		}
	}
}

func TestEpicGhCallsOfAWorkspaceWithNoRecordRunNoGh(t *testing.T) {
	e := newHandOverPushEnv(t)
	stray := filepath.Join(e.root, "pilot-ws-no-record")
	if err := os.MkdirAll(stray, 0o755); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	ghLog := mockEpicGh(t)

	errs := runSixEpicGhCalls(t, stray)
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s with a workspace path with no record: nil error", name)
		}
	}
	if calls := sudoCalls(t, ghLog); len(calls) != 0 {
		t.Errorf("gh ran for a workspace path with no record:\n%s", strings.Join(calls, "\n"))
	}
}

func TestEpicGhCallsOptionEmptyKeepTheCallsOfToday(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	repo, _ := setupTestRepoWithRemote(t)
	ghLog := mockEpicGh(t)

	for name, err := range runSixEpicGhCalls(t, repo) {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	calls := sudoCalls(t, ghLog)
	if len(calls) != 7 {
		t.Fatalf("gh calls = %d, want 7:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	want := resolvedTestPath(t, repo)
	for _, c := range calls {
		dir, args, _ := strings.Cut(c, "| ")
		if dir != want {
			t.Errorf("gh ran in %s, want %s", dir, want)
		}
		if strings.Contains(args, "--repo") {
			t.Errorf("option empty: gh got --repo: %s", args)
		}
	}
}

// assertMemoryGraphMissingInTime loads the graph of e and fails unless it
// returns the result of a missing file with one Warn line in under 15 seconds.
func assertMemoryGraphMissingInTime(t *testing.T, e *handOverPushEnv) {
	t.Helper()
	logs := captureLog(t)
	start := time.Now()
	graph, err := e.git.loadMemoryGraph(context.Background())
	if took := time.Since(start); took >= 15*time.Second {
		t.Errorf("loadMemoryGraph took %s, want under 15s", took)
	}
	if graph != nil || err != nil {
		t.Fatalf("loadMemoryGraph = %v, %v; want nil, nil (a missing file)", graph, err)
	}
	if w := logLines(logs.String(), "WARN", "memory-doc guard"); len(w) != 1 {
		t.Errorf("Warn lines %q, want one", w)
	}
}

func TestMemoryGraphFIFOIsAMissingFile(t *testing.T) {
	e := newHandOverPushEnv(t)
	path := filepath.Join(e.ws, graphJSONRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	assertMemoryGraphMissingInTime(t, e)
}

func TestMemoryGraphReadTimeLimitIsAMissingFile(t *testing.T) {
	e := newHandOverPushEnv(t)
	writeWorkspaceGraph(t, e.ws)
	// A mock sudo, first on PATH, for which the read of graph.json as the
	// agent user never ends. Unlike mockSudoAgent it runs the kill of the
	// process group (`sudo -n -u USER kill -s SIG -- -PGID`), as sudo does.
	dir := t.TempDir()
	body := `#!/bin/sh
if [ "$4" = kill ]; then shift 3; exec "$@"; fi
while [ $# -gt 0 ]; do
  a=$1
  shift
  [ "$a" = "--" ] && break
done
case "$*" in *graph.json*) exec sleep 60 ;; esac
exec "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	assertMemoryGraphMissingInTime(t, e)
}
