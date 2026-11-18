package executor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// TASK-651: with run_as_user set, the delivery of an agent workspace after the
// push: the gh commands and the recovery ref use the daemon's repository, and
// the workspace stays until its commits are safe outside it. No test needs
// root: sudo is the mock of the workspace tests.

const handOverDaemonRemote = "https://github.com/daemon-owner/daemon-repo.git"

// mockGhRecorder puts a fake gh first on PATH that appends "DIR| ARGS" per
// call to the returned file and answers each subcommand of the four gh
// commands of GitOperations.
func mockGhRecorder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh-calls")
	body := `#!/bin/sh
echo "$(pwd -P)| $*" >> '` + logPath + `'
case "$1 $2" in
"pr create") echo "https://github.com/daemon-owner/daemon-repo/pull/7" ;;
"pr list") echo '[]' ;;
"pr view") echo OPEN ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// runFourGhCommands runs the four gh commands of g and fails on an error.
func runFourGhCommands(t *testing.T, g *GitOperations, branch string) {
	t.Helper()
	ctx := context.Background()
	if url, err := g.CreatePR(ctx, "GH-1: feat(executor): hand-over delivery", "body", "main"); err != nil || !strings.Contains(url, "/pull/7") {
		t.Fatalf("CreatePR = %q, %v", url, err)
	}
	if url, err := g.FindMergedPRByBranch(ctx, branch); err != nil || url != "" {
		t.Fatalf("FindMergedPRByBranch = %q, %v", url, err)
	}
	if url, err := g.FindOpenPRByBranch(ctx, branch); err != nil || url != "" {
		t.Fatalf("FindOpenPRByBranch = %q, %v", url, err)
	}
	if state, err := g.GetPRStateByURL(ctx, "https://github.com/daemon-owner/daemon-repo/pull/7"); err != nil || state != "OPEN" {
		t.Fatalf("GetPRStateByURL = %q, %v", state, err)
	}
}

func TestHandOverGhCommandsRunInTheDaemonRepo(t *testing.T) {
	e := newHandOverPushEnv(t)
	wsGit(t, e.repo, "remote", "set-url", "origin", handOverDaemonRemote)
	// The agent plants a remote and moves HEAD to a branch of its choice.
	wsGit(t, e.ws, "remote", "set-url", "origin", "https://github.com/attacker/evil.git")
	wsGit(t, e.ws, "remote", "add", "upstream", "https://github.com/attacker/evil-upstream.git")
	wsGit(t, e.ws, "checkout", "-q", "-b", "agent/chosen-head")
	ghLog := mockGhRecorder(t)

	runFourGhCommands(t, e.git, e.branch)

	calls := sudoCalls(t, ghLog)
	if len(calls) != 4 {
		t.Fatalf("gh calls = %d, want 4:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	daemonDir := resolvedTestPath(t, e.repo)
	for _, c := range calls {
		dir, args, _ := strings.Cut(c, "| ")
		if dir != daemonDir {
			t.Errorf("gh ran in %s, want the daemon's repository %s: %s", dir, daemonDir, args)
		}
		if !strings.Contains(args, "--repo daemon-owner/daemon-repo") {
			t.Errorf("gh without --repo of the daemon's remote: %s", args)
		}
		if strings.Contains(args, "attacker") || strings.Contains(args, "evil") {
			t.Errorf("gh took the remote of the workspace: %s", args)
		}
	}
	create := calls[0]
	if !strings.Contains(create, " pr create ") || !strings.Contains(create, "--head "+e.branch) || !strings.Contains(create, "--base main") {
		t.Errorf("pr create = %s, want --head %s and --base main", create, e.branch)
	}
	if strings.Contains(create, "agent/chosen-head") {
		t.Errorf("pr create took the HEAD of the workspace: %s", create)
	}
}

func TestCreatePROptionEmptyKeepsTheCallsOfToday(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	repo, _ := setupTestRepoWithRemote(t)
	wsGit(t, repo, "checkout", "-q", "-b", "pilot/GH-2")
	ghLog := mockGhRecorder(t)

	g := newGitOperationsForWorkspace(repo, repo)
	runFourGhCommands(t, g, "pilot/GH-2")

	calls := sudoCalls(t, ghLog)
	if len(calls) != 4 {
		t.Fatalf("gh calls = %d, want 4:\n%s", len(calls), strings.Join(calls, "\n"))
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
	if !strings.Contains(calls[0], "--head pilot/GH-2") {
		t.Errorf("option empty: pr create = %s, want --head from HEAD", calls[0])
	}
}

// logLines are the lines of logs at level that hold msg.
func logLines(logs, level, msg string) []string {
	var hits []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "level="+level) && strings.Contains(l, msg) {
			hits = append(hits, l)
		}
	}
	return hits
}

func TestHandOverRecoveryRefAfterARefusedGate(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	secretpatterns.RegisterSecretValues(secretGateValue)
	commitFile(t, e.ws, "a.txt", "one")
	head := commitFile(t, e.ws, "notes.md", "token = "+secretGateValue+"\n")

	pushErr := e.git.Push(ctx, e.branch, handOverTestTask)
	if !IsPushRefused(pushErr) {
		t.Fatalf("Push of a registered secret: %v, want a refusal", pushErr)
	}
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Branch: e.branch, ProjectPath: e.repo, SourceAdapter: "linear"}
	result := &ExecutionResult{TaskID: handOverTestTask}
	r.failPushRefused(ctx, task, e.git, result, r.log, pushErr)

	ref := recoveryRefName(handOverTestTask)
	if !strings.Contains(result.Error, "recovery_ref="+ref) {
		t.Errorf("result error %q names no recovery ref", result.Error)
	}
	if got := wsGit(t, e.repo, "rev-parse", ref); got != head {
		t.Fatalf("recovery ref in the daemon's repository = %s, want the workspace head %s", got, head)
	}
	if n := wsGit(t, e.repo, "rev-list", "--count", e.base+".."+ref); n != "2" {
		t.Errorf("recovery ref holds %s commits above the base, want 2", n)
	}
	if refs := wsGit(t, e.ws, "for-each-ref", "refs/pilot-recovery/"); refs != "" {
		t.Errorf("recovery ref written in the workspace: %s", refs)
	}

	e.cleanup()
	if _, err := os.Stat(e.ws); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("workspace not removed with a recovery ref of its commits: %v", err)
	}
	e.assertNoHandOverRef(t)
	if got := wsGit(t, e.repo, "rev-parse", ref); got != head {
		t.Errorf("recovery ref after the cleanup = %s, want %s", got, head)
	}
}

func TestHandOverRecoveryRefWithoutAHandOverRef(t *testing.T) {
	for _, fromRef := range []string{"HEAD", "refs/heads/pilot/GH-1"} {
		t.Run(fromRef, func(t *testing.T) {
			e := newHandOverPushEnv(t)
			head := commitFile(t, e.ws, "a.txt", "one")
			// A hand-over ref of an earlier run of the task is not this run's.
			wsGit(t, e.repo, "update-ref", handOverRef(handOverTestTask), e.base)
			// The agent moves HEAD away from the task branch.
			wsGit(t, e.ws, "checkout", "-q", "--detach", e.base)

			ref, err := e.git.CreateRecoveryRef(context.Background(), handOverTestTask, fromRef)
			if err != nil {
				t.Fatalf("CreateRecoveryRef: %v", err)
			}
			if got := wsGit(t, e.repo, "rev-parse", ref); got != head {
				t.Errorf("recovery ref = %s, want the head %s of the task branch", got, head)
			}
			if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != head {
				t.Errorf("hand-over ref = %s, want %s", got, head)
			}
		})
	}
}

func TestHandOverFailedHandOverKeepsTheWorkspace(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	logs := captureLog(t)
	commitFile(t, e.ws, "a.txt", "one")
	e.git.handOverBase = ""

	if err := e.git.Push(ctx, e.branch, handOverTestTask); err == nil {
		t.Fatal("Push without a base succeeded")
	}
	if _, err := e.git.CreateRecoveryRef(ctx, handOverTestTask, "HEAD"); err == nil {
		t.Fatal("CreateRecoveryRef after a failed hand-over succeeded")
	}
	e.cleanup()

	if _, err := os.Stat(filepath.Join(e.ws, "a.txt")); err != nil {
		t.Fatalf("workspace removed after a failed hand-over: %v", err)
	}
	if refs := wsGit(t, e.repo, "for-each-ref", "refs/pilot-recovery/"); refs != "" {
		t.Errorf("recovery ref without a hand-over: %s", refs)
	}
	rec := logLines(logs.String(), "ERROR", "Recovery ref of the agent workspace failed")
	if len(rec) != 1 || !strings.Contains(rec[0], "task_id="+handOverTestTask) || !strings.Contains(rec[0], "workspace="+e.ws) {
		t.Errorf("recovery Error lines %q: want one naming the task and the workspace", rec)
	}
	kept := logLines(logs.String(), "ERROR", "agent workspace kept")
	if len(kept) != 1 || !strings.Contains(kept[0], "path="+e.ws) {
		t.Errorf("kept Error lines %q: want one naming the path %s", kept, e.ws)
	}
}

func TestHandOverPushedRunRemovesTheWorkspaceAndTheHandOverRef(t *testing.T) {
	e := newHandOverPushEnv(t)
	head := commitFile(t, e.ws, "a.txt", "one")
	if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != head {
		t.Fatalf("hand-over ref before the cleanup = %s, want %s", got, head)
	}
	e.cleanup()
	if _, err := os.Stat(e.ws); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("workspace not removed after the push: %v", err)
	}
	e.assertNoHandOverRef(t)
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want %q", got, head)
	}
}

func TestHandOverCleanupRemovesAWorkspaceWithoutCommits(t *testing.T) {
	e := newHandOverEnv(t)
	logs := captureLog(t)
	e.cleanup()
	if _, err := os.Stat(e.ws); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("workspace with no commit above the base not removed: %v", err)
	}
	if kept := logLines(logs.String(), "ERROR", "agent workspace kept"); len(kept) != 0 {
		t.Errorf("kept Error lines for a workspace without commits: %q", kept)
	}
}

// writeWorkspaceGraph writes graph.json with one memory node and its doc.
func writeWorkspaceGraph(t *testing.T, ws string) {
	t.Helper()
	dir := filepath.Join(ws, ".agent", "knowledge", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	graph := `{"nodes":{"memories":{"x":{"file":"memories/x.md"}}}}`
	if err := os.WriteFile(filepath.Join(ws, graphJSONRelPath), []byte(graph), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
}

func TestHandOverMemoryGraphReadsAsTheAgentUser(t *testing.T) {
	e := newHandOverPushEnv(t)
	writeWorkspaceGraph(t, e.ws)

	indexed, err := e.git.indexedMemoryPaths()
	if err != nil {
		t.Fatalf("indexedMemoryPaths: %v", err)
	}
	if !indexed[".agent/knowledge/memories/x.md"] || len(indexed) != 1 {
		t.Errorf("indexed = %v, want the one memory doc", indexed)
	}
	var graphRead, docRead bool
	for _, c := range sudoCalls(t, e.sudoLog) {
		if strings.Contains(c, "cat --") {
			graphRead = graphRead || strings.Contains(c, graphJSONRelPath)
			docRead = docRead || strings.Contains(c, "memories/x.md")
		}
	}
	if !graphRead || !docRead {
		t.Errorf("reads as the agent user: graph.json=%v memory doc=%v, want both", graphRead, docRead)
	}
}

func TestHandOverMemoryGraphReadErrorIsAMissingFile(t *testing.T) {
	e := newHandOverPushEnv(t)
	writeWorkspaceGraph(t, e.ws)
	mockSudoAgent(t, "graph.json", "")
	logs := captureLog(t)

	graph, err := e.git.loadMemoryGraph()
	if graph != nil || err != nil {
		t.Fatalf("loadMemoryGraph with a failed read = %v, %v; want nil, nil (a missing file)", graph, err)
	}
	if w := logLines(logs.String(), "WARN", "memory-doc guard"); len(w) != 1 {
		t.Errorf("Warn lines %q, want one", w)
	}
}
