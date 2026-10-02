package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// goTestRun builds a `go test ./...`-shaped output: failure noise first, then
// pkgCount per-package status lines and the final verdict.
func goTestRun(noiseLines, pkgCount int, fail bool) string {
	var b strings.Builder
	for i := 0; i < noiseLines; i++ {
		fmt.Fprintf(&b, "    noise line %d\n", i)
	}
	for i := 0; i < pkgCount; i++ {
		if fail && i == pkgCount-1 {
			fmt.Fprintf(&b, "FAIL\tgithub.com/x/pkg%d\t0.1s\n", i)
			continue
		}
		fmt.Fprintf(&b, "ok  \tgithub.com/x/pkg%d\t0.1s\n", i)
	}
	if fail {
		b.WriteString("FAIL\n")
	} else {
		b.WriteString("PASS\n")
	}
	return b.String()
}

func TestFinalizeEvidenceOutput_KeepsTailStatusLines(t *testing.T) {
	out := finalizeEvidenceOutput(goTestRun(2700, 59, true))

	for i := 0; i < 59; i++ {
		want := fmt.Sprintf("github.com/x/pkg%d\t", i)
		if !strings.Contains(out, want) {
			t.Errorf("per-package status line for pkg%d was truncated away", i)
		}
	}
	if !strings.HasSuffix(out, "FAIL") {
		t.Errorf("final FAIL verdict must be the last line, got tail %q", out[max(0, len(out)-30):])
	}
	if !strings.HasPrefix(out, "    noise line 0\n") {
		t.Errorf("head must be kept too, got %q", out[:30])
	}
	if !strings.Contains(out, " lines omitted ...") {
		t.Errorf("omission marker missing: %q", out)
	}
	if n := len(strings.Split(out, "\n")); n != maxEvidenceOutputLines+1 {
		t.Errorf("got %d lines, want %d (head+marker+tail)", n, maxEvidenceOutputLines+1)
	}
}

func TestFinalizeEvidenceOutput_UnderCapUntouched(t *testing.T) {
	in := goTestRun(5, 10, false)
	if got := finalizeEvidenceOutput(in); got != strings.TrimRight(in, "\n") {
		t.Errorf("under-cap output must be unchanged, got %q", got)
	}
}

func TestFinalizeEvidenceOutput_HeadingReflectsFailure(t *testing.T) {
	render := func(text, raw string) string {
		return RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{{
			Item:     AcceptanceItem{Text: text, Kind: AcceptanceItemPasteOutput},
			Commands: []AcceptanceCommandResult{{Command: "go test ./...", Output: finalizeEvidenceOutput(raw)}},
		}})
	}

	t.Run("FAIL in output rewrites passes", func(t *testing.T) {
		got := render("`go test ./...` passes", goTestRun(2700, 59, true))
		if strings.Contains(got, "### `go test ./...` passes") {
			t.Errorf("heading still claims passes:\n%s", got)
		}
		if !strings.Contains(got, "FAILS (FAIL lines in output)") {
			t.Errorf("heading must say FAIL, got:\n%s", got)
		}
	})
	t.Run("FAIL in output without verdict word appends note", func(t *testing.T) {
		got := render("output of go test", goTestRun(0, 3, true))
		if !strings.Contains(got, "### output of go test (FAIL lines in output)") {
			t.Errorf("got:\n%s", got)
		}
	})
	t.Run("clean output keeps heading", func(t *testing.T) {
		got := render("`go test ./...` passes", goTestRun(0, 3, false))
		if !strings.Contains(got, "### `go test ./...` passes\n") {
			t.Errorf("clean heading must be unchanged, got:\n%s", got)
		}
	})
}

func TestAcceptanceEvidence_LiveSmokeNotVerifiedHoldsPR(t *testing.T) {
	t.Run("gate-rendered live-smoke bullet carries a reason and holds", func(t *testing.T) {
		section := RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{{
			Item:              AcceptanceItem{Text: "Live smoke on four labelled excerpts", Kind: AcceptanceItemOther},
			NotVerifiedReason: "no commands parsed from acceptance item (expected an inline `command`)",
		}})
		if !strings.Contains(section, liveSmokeHoldNote) {
			t.Errorf("reason must explain the hold, got:\n%s", section)
		}
		if got := LiveSmokeNotVerifiedBullets("## Summary\n\nx\n\n" + section); len(got) != 1 {
			t.Fatalf("want 1 held bullet, got %v", got)
		}
	})

	t.Run("hand-written bullet with no reason holds", func(t *testing.T) {
		body := "## Summary\n\nx\n\n## Not verified\n\n- Live smoke on four labelled excerpts\n- unit thing\n\n## Other\n\n- live smoke (ignored, other section)\n"
		got := LiveSmokeNotVerifiedBullets(body)
		if len(got) != 1 || got[0] != "Live smoke on four labelled excerpts" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("each table row matches", func(t *testing.T) {
		for _, text := range []string{
			"live smoke on the corpus", "needs a real API key", "run against the box",
			"hit the external service", "valid credentials required", "smoke test in prod",
		} {
			if !IsLiveSmokeBullet(text) {
				t.Errorf("%q should match the live-smoke table", text)
			}
		}
	})

	t.Run("ordinary not-verified bullets do not hold", func(t *testing.T) {
		body := "## Not verified\n\n- **freeform mutation**\n  Reason: freeform mutation, run manually\n- **path**\n  Reason: path escapes worktree\n"
		if got := LiveSmokeNotVerifiedBullets(body); len(got) != 0 {
			t.Fatalf("unexpected hold: %v", got)
		}
		if got := LiveSmokeNotVerifiedBullets("## Summary\n\nno section"); got != nil {
			t.Fatalf("unexpected hold: %v", got)
		}
	})

	t.Run("appendAcceptanceEvidence output is held end to end", func(t *testing.T) {
		r := &Runner{config: DefaultBackendConfig(), acceptanceRunner: &fakeAcceptanceCommandRunner{}}
		task := &Task{ID: "GH-1", AcceptanceCriteria: []string{"paste the output of a live smoke against the box"}}
		body := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary\n\nx")
		if got := LiveSmokeNotVerifiedBullets(body); len(got) == 0 {
			t.Fatalf("live-smoke Not-verified bullet must hold the PR, body:\n%s", body)
		}
	})

	// GH-5597: the note is appended to the reason, which autopilot also
	// matches — it must not match the table itself.
	t.Run("hold note does not self-match", func(t *testing.T) {
		if IsLiveSmokeBullet(liveSmokeHoldNote) {
			t.Fatalf("liveSmokeHoldNote must not match liveSmokePatterns: %q", liveSmokeHoldNote)
		}
		body := "## Not verified\n\n- **corpus replay**\n  Reason: freeform mutation, run manually; " + liveSmokeHoldNote + "\n"
		if got := LiveSmokeNotVerifiedBullets(body); len(got) != 0 {
			t.Fatalf("unrelated bullet carrying the note must not be held, got %v", got)
		}
	})

	t.Run("reason-only match gets the hold note", func(t *testing.T) {
		section := RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{{
			Item:              AcceptanceItem{Text: "Replay the corpus", Kind: AcceptanceItemOther},
			NotVerifiedReason: "needs a real API key",
		}})
		if !strings.Contains(section, liveSmokeHoldNote) {
			t.Errorf("reason-only match must carry the hold note, got:\n%s", section)
		}
		if got := LiveSmokeNotVerifiedBullets(section); len(got) != 1 {
			t.Errorf("want 1 held bullet, got %v", got)
		}
	})

	t.Run("unrelated bullet gets no note", func(t *testing.T) {
		section := RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{{
			Item:              AcceptanceItem{Text: "Replay the corpus", Kind: AcceptanceItemOther},
			NotVerifiedReason: "freeform mutation, run manually",
		}})
		if strings.Contains(section, liveSmokeHoldNote) {
			t.Errorf("unrelated bullet must not carry the note, got:\n%s", section)
		}
	})
}
