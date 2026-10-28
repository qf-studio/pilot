//go:build integration

package executor

import (
	"context"
	"os"
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
printf '{"type":"result","subtype":"success","result":"VERDICT: REQUEST-CHANGES\\nDEFECTS:\\n- client/api.go:2, binds org_id nobody sends, server/handler.go:%s emits only organization_id\\nNOTES:\\n"}\n' "$line"
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
