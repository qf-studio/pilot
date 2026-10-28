package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// memGraphJSON has two memories on the "wire field" and "client" concepts and
// one on an unrelated concept.
const memGraphJSON = `{
  "nodes": {
    "concepts": {
      "wire-field": {"label": "wire field"},
      "client": {"label": "client"},
      "slack-socket": {"label": "slack socket"}
    },
    "memories": {
      "mem-1": {"type": "pitfall", "summary": "doc-vs-wire binding", "concepts": ["wire-field"], "confidence": 0.9},
      "mem-2": {"type": "pattern", "summary": "client adapters need a real server test", "concepts": ["client"], "confidence": 0.8},
      "mem-3": {"type": "pitfall", "summary": "slack socket reconnect loop", "concepts": ["slack-socket"], "confidence": 0.99}
    }
  }
}`

func memProject(t *testing.T, graph string) string {
	t.Helper()
	dir := t.TempDir()
	if graph == "" {
		return dir
	}
	kdir := filepath.Join(dir, ".agent", "knowledge")
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, "graph.json"), []byte(graph), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func memIDs(in executor.PostMergeReviewInput) []string {
	ids := make([]string, 0, len(in.Memories))
	for _, m := range in.Memories {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestGatherPostMergeReviewInput_Memories_TwoMatching(t *testing.T) {
	fake := newGatherFake("pilot/GH-12")
	fake.issues[12] = &github.Issue{Number: 12, Title: "bind the wire field", Body: "body"}
	fake.diff = "diff --git a/internal/foo/client.go b/internal/foo/client.go\n+x\n"

	got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", memProject(t, memGraphJSON), 5902, 12)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	// "wire field" comes from the issue title, "client" only from the diff header.
	if ids := memIDs(got.Input); strings.Join(ids, ",") != "mem-1,mem-2" {
		t.Fatalf("memories = %v, want [mem-1 mem-2] (mem-3 is unrelated)", ids)
	}
	if got.Input.Memories[0].Summary == "" || got.Input.Memories[0].Type == "" {
		t.Errorf("memory lost its summary or type: %+v", got.Input.Memories[0])
	}
}

func TestGatherPostMergeReviewInput_Memories_Gating(t *testing.T) {
	const diff = "diff --git a/internal/foo/client.go b/internal/foo/client.go\n+x\n"
	tests := []struct {
		name    string
		project func(t *testing.T) string
		opts    []GatherOption
		want    string
	}{
		{"no graph returns none and no error", func(t *testing.T) string { return memProject(t, "") }, nil, ""},
		{"malformed graph returns none and no error", func(t *testing.T) string { return memProject(t, "{not json") }, nil, ""},
		{"no project path returns none", func(*testing.T) string { return "" }, nil, ""},
		{"memory injection off returns none", func(t *testing.T) string { return memProject(t, memGraphJSON) }, []GatherOption{WithoutMemoryInjection()}, ""},
		{"memory injection on is the default", func(t *testing.T) string { return memProject(t, memGraphJSON) }, nil, "mem-1,mem-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newGatherFake("pilot/GH-12")
			fake.issues[12] = &github.Issue{Number: 12, Title: "bind the wire field", Body: "body"}
			fake.diff = diff

			got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", tt.project(t), 5902, 12, tt.opts...)
			if err != nil {
				t.Fatalf("gather: %v", err)
			}
			if fake.gotIssue != 12 {
				t.Errorf("issue fetched = %d, want 12", fake.gotIssue)
			}
			if ids := strings.Join(memIDs(got.Input), ","); ids != tt.want {
				t.Errorf("memories = %q, want %q", ids, tt.want)
			}
		})
	}
}

func TestPostMergeDiffFiles(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+x\ndiff --git a/old/b.go b/new/b.go\n+y\ndiff --git a/a.go b/a.go\n+z\n"
	got := postMergeDiffFiles(diff)
	if strings.Join(got, ",") != "a.go,new/b.go" {
		t.Errorf("files = %v, want [a.go new/b.go]", got)
	}
	if len(postMergeDiffFiles("")) != 0 {
		t.Error("empty diff must list no files")
	}
}

// The controller path: the reviewer receives the recalled memories, and the
// memory_injection switch turns them off.
func TestPostMergeReview_ControllerMemories(t *testing.T) {
	tests := []struct {
		name     string
		graph    string
		disabled bool
		want     int
	}{
		{"graph with matches", memGraphJSON, false, 1},
		{"no graph", "", false, 0},
		{"injection disabled", memGraphJSON, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gh := newPMRGitHub(t)
			reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
			c := newPMRController(t, gh, reviewer, true)
			c.projectPath = memProject(t, tt.graph)
			c.SetPostMergeMemoryInjection(!tt.disabled)

			if err := c.handleMerged(context.Background(), mergedPRState(42, 10)); err != nil {
				t.Fatalf("handleMerged: %v", err)
			}
			c.waitPostMergeReviews()
			if reviewer.callCount() != 1 {
				t.Fatalf("reviewer calls = %d, want 1", reviewer.callCount())
			}
			// The fixture diff touches internal/foo/client.go -> the "client" concept.
			if got := len(reviewer.inputs[0].Memories); got != tt.want {
				t.Errorf("memories passed to the reviewer = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPostMergeReviewCommentDiffLine_Memories(t *testing.T) {
	tests := []struct {
		name   string
		review *executor.PostMergeReview
		want   string
	}{
		{"untrimmed with memories", &executor.PostMergeReview{Verdict: executor.PostMergeApprove, DiffBytes: 10, DiffFiles: 1, Memories: 3}, "Diff: 10 bytes, 1 files, trimmed: no, memories: 3\n"},
		{"untrimmed without graph", &executor.PostMergeReview{Verdict: executor.PostMergeApprove, DiffBytes: 10, DiffFiles: 1}, "trimmed: no, memories: 0\n"},
		{"trimmed keeps memories last", &executor.PostMergeReview{Verdict: executor.PostMergeApprove, DiffBytes: 10, DiffFiles: 1, DiffTrimmed: true, TrimmedFiles: []string{"a.go"}, Memories: 2}, "trimmed: yes (1 files: a.go), memories: 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildPostMergeReviewComment(tt.review); !strings.Contains(got, tt.want) {
				t.Errorf("comment missing %q:\n%s", tt.want, got)
			}
		})
	}
}
