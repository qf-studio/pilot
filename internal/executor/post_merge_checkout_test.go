package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initCheckoutRepo makes a git repo with a producer file and returns its path
// and the HEAD commit.
func initCheckoutRepo(t *testing.T) (repo, sha string) {
	t.Helper()
	repo = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	producer := "package server\n\ntype Resp struct {\n\tOrganizationID string `json:\"organization_id\"`\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "server", "handler.go"), []byte(producer), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	return repo, git("rev-parse", "HEAD")
}

func worktreeCount(t *testing.T, repo string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v: %s", err, out)
	}
	return strings.Count(string(out), "worktree ")
}

// flagValue returns the value following flag in args.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

const checkoutDiff = "diff --git a/client/api.go b/client/api.go\n+\tOrgID string `json:\"org_id\"`\n"

func checkoutInput(repo, sha string) PostMergeReviewInput {
	return PostMergeReviewInput{
		PRNumber: 9, IssueTitle: "feat: org id", IssueBody: "bind org_id",
		Diff: checkoutDiff, ProjectPath: repo, MergeCommitSHA: sha,
	}
}

func TestPostMergeReviewCheckout_ReadOnlyTools(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var gotArgs []string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	})
	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	for _, flag := range []string{"--tools", "--allowedTools"} {
		v, ok := flagValue(gotArgs, flag)
		if !ok {
			t.Fatalf("%s missing from args", flag)
		}
		if v != "Read,Grep,Glob" {
			t.Errorf("%s = %q, want exactly Read,Grep,Glob", flag, v)
		}
	}
	// No flag value may name a mutating or executing tool.
	for i, a := range gotArgs {
		if a == "-p" || i == 0 {
			continue
		}
		if strings.HasPrefix(gotArgs[i-1], "--") && gotArgs[i-1] != "--print" {
			for _, banned := range []string{"Bash", "Edit", "Write", "NotebookEdit", "WebFetch"} {
				if strings.Contains(a, banned) {
					t.Errorf("flag %s carries %s", gotArgs[i-1], banned)
				}
			}
		}
	}
	if v, _ := flagValue(gotArgs, "--max-turns"); v != "30" {
		t.Errorf("--max-turns = %q, want 30", v)
	}
	if v, _ := flagValue(gotArgs, "--output-format"); v != "stream-json" {
		t.Errorf("--output-format = %q, want stream-json", v)
	}
}

func TestPostMergeReviewCheckout_WorkingDirectory(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var gotDir string
	var producerSeen bool
	var prompt string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, dir string, args ...string) ([]byte, error) {
		gotDir = dir
		_, err := os.Stat(filepath.Join(dir, "server", "handler.go"))
		producerSeen = err == nil
		prompt, _ = flagValue(args, "-p")
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	})
	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if gotDir == "" || gotDir == repo {
		t.Fatalf("cwd = %q, want a separate worktree of %s", gotDir, repo)
	}
	if !strings.HasPrefix(gotDir, os.TempDir()) {
		t.Errorf("cwd = %q, want it under the temp root %s", gotDir, os.TempDir())
	}
	if !producerSeen {
		t.Errorf("checkout at %s does not contain the merge commit's files", gotDir)
	}
	if !strings.Contains(prompt, "quote the line that produces or consumes the field") {
		t.Errorf("checklist item 2 not extended for a checkout")
	}
	if !strings.Contains(prompt, "read-only checkout of the merge commit") {
		t.Errorf("prompt does not tell the reviewer it has a checkout")
	}
}

func TestPostMergeReviewCheckout_Cleanup(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var gotDir string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, dir string, _ ...string) ([]byte, error) {
		gotDir = dir
		if n := worktreeCount(t, repo); n != 2 {
			t.Errorf("worktrees during review = %d, want 2", n)
		}
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	})
	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if _, err := os.Stat(gotDir); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after the review (stat err = %v)", gotDir, err)
	}
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktrees after review = %d, want 1 (the repo itself)", n)
	}
}

func TestPostMergeReviewCheckout_CleanupOnRunnerError(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var gotDir string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, dir string, _ ...string) ([]byte, error) {
		gotDir = dir
		return nil, context.DeadlineExceeded
	})
	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err == nil {
		t.Fatal("Review: want the runner error")
	}
	if _, err := os.Stat(gotDir); !os.IsNotExist(err) {
		t.Errorf("worktree %s survived a failed review", gotDir)
	}
}

func TestPostMergeReviewCheckout_DiffOnlyFallbacks(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	tests := []struct {
		name string
		in   PostMergeReviewInput
	}{
		{"no project path", PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff, MergeCommitSHA: sha}},
		{"no merge commit", PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff, ProjectPath: repo}},
		{"commit not in the repo", PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff, ProjectPath: repo, MergeCommitSHA: strings.Repeat("a", 40)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotDir string
			var gotArgs []string
			r := newPostMergeReviewerWithDirRunner(func(_ context.Context, dir string, args ...string) ([]byte, error) {
				gotDir, gotArgs = dir, args
				return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
			})
			got, err := r.Review(context.Background(), tt.in)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if gotDir != "" {
				t.Errorf("cwd = %q, want none for a diff-only review", gotDir)
			}
			if _, ok := flagValue(gotArgs, "--allowedTools"); ok {
				t.Errorf("diff-only review must not carry a tool allowlist")
			}
			if v, _ := flagValue(gotArgs, "--output-format"); v != "text" {
				t.Errorf("--output-format = %q, want text", v)
			}
			if got.Checkout || got.FilesRead != 0 {
				t.Errorf("Checkout=%v FilesRead=%d, want no checkout", got.Checkout, got.FilesRead)
			}
			if n := worktreeCount(t, repo); n != 1 {
				t.Errorf("worktrees = %d, want 1", n)
			}
		})
	}
}

// streamLine renders one claude stream-json event.
func streamLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func streamToolUse(name, path string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "name": name, "input": map[string]any{"file_path": path}},
	}}}
}

// A diff that binds org_id while the producer in the checkout only emits
// organization_id: the reviewer opens the producer and reports the defect with
// the producer file and line. The fake reviewer does the real read.
func TestPostMergeReviewCheckout_ReportsFieldWithoutProducer(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, dir string, _ ...string) ([]byte, error) {
		producer := filepath.Join(dir, "server", "handler.go")
		body, err := os.ReadFile(producer)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(body), `json:"org_id"`) {
			t.Errorf("fixture producer unexpectedly emits org_id")
		}
		line := 0
		for i, l := range strings.Split(string(body), "\n") {
			if strings.Contains(l, "organization_id") {
				line = i + 1
			}
		}
		answer := fmt.Sprintf("VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] client/api.go:2, binds org_id that no producer sends, server/handler.go:%d emits only organization_id\nNOTES:\n", line)
		return []byte(streamLine(t, map[string]any{"type": "system", "subtype": "init"}) +
			streamLine(t, streamToolUse("Grep", "")) +
			streamLine(t, streamToolUse("Read", producer)) +
			streamLine(t, streamToolUse("Read", producer)) +
			streamLine(t, map[string]any{"type": "result", "subtype": "success", "result": answer})), nil
	})
	got, err := r.Review(context.Background(), checkoutInput(repo, sha))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeRequestChanges || len(got.Defects) != 1 {
		t.Fatalf("verdict=%q defects=%+v, want one REQUEST-CHANGES defect", got.Verdict, got.Defects)
	}
	if !strings.Contains(got.Defects[0].Evidence, "server/handler.go:4") {
		t.Errorf("evidence %q does not cite the producer file and line", got.Defects[0].Evidence)
	}
	if !got.Checkout || got.FilesRead != 1 {
		t.Errorf("Checkout=%v FilesRead=%d, want checkout with 1 distinct file read", got.Checkout, got.FilesRead)
	}
}

func TestParsePostMergeStream(t *testing.T) {
	t.Run("plain text passes through", func(t *testing.T) {
		s := parsePostMergeStream([]byte("VERDICT: APPROVE\nDEFECTS:\nnone"))
		if s.Text != "VERDICT: APPROVE\nDEFECTS:\nnone" || s.FilesRead != 0 || s.Exhausted {
			t.Errorf("stream = %+v", s)
		}
	})
	t.Run("result wins and reads are distinct", func(t *testing.T) {
		out := streamLine(t, streamToolUse("Read", "/a")) + streamLine(t, streamToolUse("Read", "/b")) + streamLine(t, streamToolUse("Read", "/a")) +
			streamLine(t, map[string]any{"type": "result", "result": "VERDICT: APPROVE"})
		s := parsePostMergeStream([]byte(out))
		if s.Text != "VERDICT: APPROVE" || s.FilesRead != 2 || s.Turns != 3 {
			t.Errorf("stream = %+v, want APPROVE, 2 files, 3 turns", s)
		}
	})
	t.Run("no result falls back to last assistant text", func(t *testing.T) {
		out := streamLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
			{"type": "text", "text": "VERDICT: APPROVE"},
		}}})
		if s := parsePostMergeStream([]byte(out)); s.Text != "VERDICT: APPROVE" || s.Exhausted {
			t.Errorf("stream = %+v", s)
		}
	})
	t.Run("no result and narration is exhausted", func(t *testing.T) {
		out := streamLine(t, streamToolUse("Read", "/a")) +
			streamLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
				{"type": "text", "text": "Let me look at one more file."},
			}}}) +
			streamLine(t, map[string]any{"type": "result", "subtype": "error_max_turns", "num_turns": 13})
		s := parsePostMergeStream([]byte(out))
		if !s.Exhausted || s.Text != "" || s.FilesRead != 1 || s.Turns != 13 {
			t.Errorf("stream = %+v, want exhausted with no text, 1 file, 13 turns", s)
		}
	})
}
