package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory/graphrecall"
)

const (
	memBlockHeading   = "## Known pitfalls from project memory (review context)"
	memChecklistLine  = "6) For each listed project memory, say whether the diff repeats that pitfall; cite the file and line when it does."
	memReviewerOutput = "VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"
)

// reviewWithMemories runs Review with a runner that records the prompt it is
// handed, and returns that prompt with the parsed review.
func reviewWithMemories(t *testing.T, memories []graphrecall.MemorySummary) (string, *PostMergeReview) {
	t.Helper()
	var (
		mu     sync.Mutex
		prompt string
	)
	r := newPostMergeReviewerWithRunner(func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		for i, a := range args {
			if a == "-p" && i+1 < len(args) {
				prompt = args[i+1]
			}
		}
		return []byte(memReviewerOutput), nil
	})
	review, err := r.Review(context.Background(), PostMergeReviewInput{
		PRNumber: 9, IssueTitle: "ISSUE-TITLE", IssueBody: "ISSUE-BODY",
		Diff: "diff --git a/x b/x\n+DIFF-LINE", Memories: memories,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if prompt == "" {
		t.Fatal("runner never saw a -p prompt")
	}
	return prompt, review
}

func TestPostMergeReviewPromptMemories_RendersBlockAndChecklist(t *testing.T) {
	mems := []graphrecall.MemorySummary{
		{ID: "mem-1", Type: "pitfall", Summary: "doc-vs-wire binding", Confidence: 0.9},
		{ID: "mem-2", Type: "pattern", Summary: "unwired config field", Confidence: 0.75},
	}
	prompt, review := reviewWithMemories(t, mems)

	for _, want := range []string{
		memBlockHeading,
		"- [pitfall] doc-vs-wire binding (confidence 0.90)",
		"- [pattern] unwired config field (confidence 0.75)",
		memChecklistLine,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// Block sits between the issue and the diff.
	issueAt, blockAt, diffAt := strings.Index(prompt, "ISSUE-BODY"), strings.Index(prompt, memBlockHeading), strings.Index(prompt, "## Merged Diff")
	if issueAt >= blockAt || blockAt >= diffAt {
		t.Errorf("block order wrong: issue=%d block=%d diff=%d", issueAt, blockAt, diffAt)
	}
	// The checklist line follows item 5 and precedes the data below the checklist.
	if strings.Index(prompt, memChecklistLine) < strings.Index(prompt, "5) Anything") || strings.Index(prompt, memChecklistLine) > strings.Index(prompt, "IMPORTANT — truncation") {
		t.Error("checklist line 6 is not directly after item 5")
	}
	if review.Memories != 2 {
		t.Errorf("review.Memories = %d, want 2", review.Memories)
	}
}

func TestPostMergeReviewPromptMemories_EmptyRendersNothing(t *testing.T) {
	tests := []struct {
		name string
		mems []graphrecall.MemorySummary
	}{
		{"nil", nil},
		{"empty slice", []graphrecall.MemorySummary{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, review := reviewWithMemories(t, tt.mems)
			for _, absent := range []string{memBlockHeading, memChecklistLine, "Known pitfalls"} {
				if strings.Contains(prompt, absent) {
					t.Errorf("prompt carries %q without memories", absent)
				}
			}
			if review.Memories != 0 {
				t.Errorf("review.Memories = %d, want 0", review.Memories)
			}
		})
	}
}

func TestPostMergeReviewPromptMemories_CapHolds(t *testing.T) {
	long := strings.Repeat("x", 400)
	mems := make([]graphrecall.MemorySummary, 5)
	for i := range mems {
		mems[i] = graphrecall.MemorySummary{ID: fmt.Sprintf("mem-%d", i), Type: "pitfall", Summary: fmt.Sprintf("%d-%s", i, long), Confidence: 0.9}
	}
	prompt, review := reviewWithMemories(t, mems)

	start := strings.Index(prompt, memBlockHeading)
	if start < 0 {
		t.Fatal("block missing")
	}
	end2 := strings.Index(prompt[start+len(memBlockHeading):], "\n## ")
	if end2 < 0 {
		t.Fatal("block is not followed by another section")
	}
	block := prompt[start : start+len(memBlockHeading)+end2+1]
	if len(block) > memoryInjectionCharCap {
		t.Errorf("block is %d chars, cap is %d", len(block), memoryInjectionCharCap)
	}
	if review.Memories <= 0 || review.Memories >= len(mems) {
		t.Errorf("rendered %d of %d entries, want some dropped whole by the cap", review.Memories, len(mems))
	}
	if got := strings.Count(block, "\n- ["); got != review.Memories {
		t.Errorf("block has %d entries, review reports %d", got, review.Memories)
	}
	if strings.Contains(block, "4-xxx") {
		t.Error("an entry past the cap was rendered")
	}
}

func TestRenderMemoryBlock(t *testing.T) {
	mems := []graphrecall.MemorySummary{{Type: "pitfall", Summary: "s", Confidence: 0.5}}
	tests := []struct {
		name      string
		header    string
		footer    string
		mems      []graphrecall.MemorySummary
		want      string
		wantCount int
	}{
		{"no memories", "H\n\n", "F\n", nil, "", 0},
		{"entry format", "H\n\n", "F\n", mems, "H\n\n- [pitfall] s (confidence 0.50)\nF\n", 1},
		{"entry larger than the cap is dropped", "H\n\n", "", []graphrecall.MemorySummary{{Type: "pitfall", Summary: strings.Repeat("y", 2000)}}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := renderMemoryBlock(tt.header, tt.footer, tt.mems)
			if got != tt.want || n != tt.wantCount {
				t.Errorf("renderMemoryBlock = (%q, %d), want (%q, %d)", got, n, tt.want, tt.wantCount)
			}
		})
	}
}
