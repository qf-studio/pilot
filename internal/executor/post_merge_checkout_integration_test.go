//go:build integration

package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real checkout, a real subprocess and a real read: the stand-in claude
// binary runs in the review's working directory, reads the producer file from
// the merge commit's checkout and answers in stream-json.
func TestPostMergeReviewCheckout_Integration(t *testing.T) {
	repo, sha := initCheckoutRepo(t)

	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
line=$(grep -n organization_id server/handler.go | head -1 | cut -d: -f1)
file="$(pwd)/server/handler.go"
printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"%s"}}]}}\n' "$file"
printf '{"type":"result","subtype":"success","result":"VERDICT: REQUEST-CHANGES\\nDEFECTS:\\n- [blocking] client/api.go:2, binds org_id nobody sends, server/handler.go:%s emits only organization_id\\nNOTES:\\n"}\n' "$line"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewPostMergeReviewer(bin, "", time.Minute)
	got, err := r.Review(context.Background(), checkoutInput(repo, sha))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeRequestChanges || len(got.Defects) != 1 {
		t.Fatalf("verdict=%q defects=%+v", got.Verdict, got.Defects)
	}
	if !strings.Contains(got.Defects[0].Evidence, "server/handler.go:4") {
		t.Errorf("evidence = %q, want the producer file and line", got.Defects[0].Evidence)
	}
	if !got.Checkout || got.FilesRead != 1 {
		t.Errorf("Checkout=%v FilesRead=%d, want true and 1", got.Checkout, got.FilesRead)
	}
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktrees after review = %d, want 1", n)
	}
}

// A real checkout, the real hook script and a real subprocess: the stand-in
// claude binary runs the hook command from the worktree's settings.json for a
// Read outside and a Read inside, and reports its own HOME and environment.
func TestPostMergeCheckoutConfinement_Integration(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the confinement hook needs jq")
	}
	repo, sha := initCheckoutRepo(t)
	t.Setenv("GITHUB_TOKEN", "test-github-token")
	t.Setenv("ANTHROPIC_API_KEY", "fake-api-key")

	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
cmd=$(jq -r '.hooks.PreToolUse[0].hooks[0].command' .claude/settings.json)
printf '{"tool_name":"Read","tool_input":{"file_path":"/etc/passwd"}}' | sh -c "$cmd" 2>/dev/null; denied=$?
printf '{"tool_name":"Read","tool_input":{"file_path":"%s/server/handler.go"}}' "$(pwd)" | sh -c "$cmd"; allowed=$?
printf '{"type":"result","subtype":"success","result":"VERDICT: APPROVE\\nDEFECTS:\\nnone\\nNOTES:\\ndenied=%s allowed=%s home=%s token=%s key=%s"}\n' "$denied" "$allowed" "$HOME" "${GITHUB_TOKEN:-unset}" "${ANTHROPIC_API_KEY:-unset}"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewPostMergeReviewer(bin, "", time.Minute)
	got, err := r.Review(context.Background(), checkoutInput(repo, sha))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeApprove {
		t.Fatalf("verdict = %q, notes = %q", got.Verdict, got.Notes)
	}
	for _, want := range []string{"denied=2", "allowed=0", "token=unset", "key=fake-api-key", "home=" + os.TempDir()} {
		if !strings.Contains(got.Notes, want) {
			t.Errorf("notes %q lack %q", got.Notes, want)
		}
	}
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktrees after review = %d, want 1", n)
	}
}
