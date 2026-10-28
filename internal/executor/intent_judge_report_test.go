package executor

import (
	"fmt"
	"strings"
	"testing"
)

// reportFixtureFile renders one file's unified diff with a body of n added lines.
func reportFixtureFile(path string, lines int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1,1 +1,%d @@\n", path, path, path, path, lines)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "+line %03d of %s padding padding\n", i, path)
	}
	return sb.String()
}

// sevenFileDiff: three large files and four small ones, one of which is bigger
// than an equal per-file share of the 10000-char budget but fits under the
// largest-first level.
func sevenFileDiff() (string, []string, []string) {
	big := []string{"big/a.go", "big/b.go", "big/c.go"}
	small := []string{"s/one.go", "s/two.go", "s/three.go", "s/four.go"}
	var sb strings.Builder
	for _, p := range big {
		sb.WriteString(reportFixtureFile(p, 200))
	}
	sizes := []int{2, 2, 2, 40}
	for i, p := range small {
		sb.WriteString(reportFixtureFile(p, sizes[i]))
	}
	return sb.String(), big, small
}

func TestBuildJudgeDiffPayloadReport_UnderCapIsWhole(t *testing.T) {
	diff, _, _ := sevenFileDiff()
	got, rep := buildJudgeDiffPayloadReport(diff, len(diff))
	if got != diff {
		t.Fatal("diff within the cap must be returned unchanged")
	}
	if len(rep.Trimmed) != 0 || rep.OriginalBytes != len(diff) || rep.SentBytes != len(diff) || rep.Files != 7 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBuildJudgeDiffPayloadReport_LargestFirstKeepsSmallFilesWhole(t *testing.T) {
	diff, big, small := sevenFileDiff()
	const budget = 10000
	got, rep := buildJudgeDiffPayloadReport(diff, budget)

	if len(got) > budget {
		t.Fatalf("payload %d bytes exceeds budget %d", len(got), budget)
	}
	if rep.SentBytes != len(got) || rep.OriginalBytes != len(diff) || rep.Files != 7 {
		t.Fatalf("report = %+v (len=%d)", rep, len(got))
	}
	trimmed := map[string]int{}
	for _, f := range rep.Trimmed {
		trimmed[f.Path] = f.BytesCut
	}
	for _, p := range big {
		if trimmed[p] <= 0 {
			t.Errorf("large file %s not trimmed: %v", p, rep.Trimmed)
		}
	}
	for _, p := range small {
		if _, ok := trimmed[p]; ok {
			t.Errorf("small file %s was trimmed: %v", p, rep.Trimmed)
		}
		for _, s := range splitDiffByFile(diff) {
			if s.path == p && !strings.Contains(got, s.body) {
				t.Errorf("small file %s is not whole in the payload", p)
			}
		}
	}
	if len(rep.Trimmed) != len(big) {
		t.Errorf("trimmed %d files, want %d: %v", len(rep.Trimmed), len(big), rep.Trimmed)
	}
}

func TestBuildJudgeDiffPayloadReport_HeadersSurvive(t *testing.T) {
	diff, _, _ := sevenFileDiff()
	for _, budget := range []int{10000, 4000, 1700, 1500} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			got, _ := buildJudgeDiffPayloadReport(diff, budget)
			for _, s := range splitDiffByFile(diff) {
				// Independent of diffSectionHead: the header block is every line
				// up to and including the first hunk header.
				end := strings.Index(s.body, "\n@@")
				if end < 0 {
					t.Fatalf("fixture file %s has no hunk header", s.path)
				}
				head := s.body[:end+1] + s.body[end+1:][:strings.Index(s.body[end+1:], "\n")]
				if !strings.Contains(got, head) {
					t.Errorf("header of %s missing:\n%s", s.path, head)
				}
			}
		})
	}
}

func TestBuildJudgeDiffPayloadReport_HardCap(t *testing.T) {
	diff, _, _ := sevenFileDiff()
	tests := []struct {
		name   string
		diff   string
		budget int
	}{
		{"ten thousand", diff, 10000},
		{"tight", diff, 3500},
		{"single file", reportFixtureFile("only.go", 500), 2000},
		{"many tiny files", reportFixtureFile("x.go", 1) + reportFixtureFile("y.go", 1) + reportFixtureFile("z.go", 300), 900},
		{"below manifest plus heads", diff, 1200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rep := buildJudgeDiffPayloadReport(tt.diff, tt.budget)
			if len(got) > tt.budget {
				t.Fatalf("payload %d bytes exceeds budget %d", len(got), tt.budget)
			}
			if rep.SentBytes != len(got) {
				t.Errorf("SentBytes = %d, want %d", rep.SentBytes, len(got))
			}
			if len(rep.Trimmed) == 0 {
				t.Error("an over-cap diff must report trimmed files")
			}
		})
	}
}

func TestBuildJudgeDiffPayloadReport_ManifestListsEveryFile(t *testing.T) {
	diff, big, small := sevenFileDiff()
	got, _ := buildJudgeDiffPayloadReport(diff, 10000)
	for _, p := range append(append([]string{}, big...), small...) {
		if !strings.Contains(got, "- "+p+" (+") {
			t.Errorf("manifest missing %s", p)
		}
	}
}

// The intent judge keeps the equal-share behaviour.
func TestBuildJudgeDiffPayload_Unchanged(t *testing.T) {
	diff, _, _ := sevenFileDiff()
	if got := buildJudgeDiffPayload(diff, len(diff)); got != diff {
		t.Fatal("diff within the cap must be returned unchanged")
	}
}
