package dashboard

import (
	"strings"
	"testing"
)

func TestPanelByID(t *testing.T) {
	if def, ok := panelByID(panelQueue); !ok || def.ID != panelQueue {
		t.Errorf("panelByID(panelQueue) = %+v, %v; want registered queue def", def, ok)
	}
	if _, ok := panelByID(panelID(99)); ok {
		t.Errorf("panelByID(99) should be unregistered")
	}
}

func TestPanelIndex(t *testing.T) {
	for i, p := range panelRegistry {
		if got := panelIndex(p.ID); got != i {
			t.Errorf("panelIndex(%v) = %d, want %d", p.ID, got, i)
		}
	}
	if got := panelIndex(panelID(99)); got != -1 {
		t.Errorf("panelIndex(unregistered) = %d, want -1", got)
	}
}

func TestPanelIDString(t *testing.T) {
	tests := []struct {
		id   panelID
		want string
	}{
		{panelQueue, "queue"},
		{panelAutopilot, "autopilot"},
		{panelHistory, "history"},
		{panelLogs, "logs"},
		{panelGit, "git"},
		{panelID(99), "?"},
	}
	for _, tt := range tests {
		if got := tt.id.String(); got != tt.want {
			t.Errorf("panelID(%d).String() = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// TestComputeLayout_GitHidden verifies the left column stretches fluidly to
// fill the terminal when the git panel is hidden — previously it stayed
// pinned at panelTotalWidth regardless of a wider terminal.
func TestComputeLayout_GitHidden(t *testing.T) {
	h := layoutHeights{Queue: 5, Autopilot: 4, History: 6, Logs: 8}

	for _, termW := range []int{60, panelTotalWidth, 140, 200} {
		rects := computeLayout(termW, 50, h, false)
		wantW := termW
		if wantW < panelTotalWidth {
			wantW = panelTotalWidth
		}
		qi := panelIndex(panelQueue)
		if rects[qi].W != wantW {
			t.Errorf("termW=%d: queue width = %d, want %d", termW, rects[qi].W, wantW)
		}
		if rects[qi].X != 0 || rects[qi].Y != 0 {
			t.Errorf("termW=%d: queue rect = %+v, want X=0 Y=0", termW, rects[qi])
		}
		// Git rect stays the zero value when hidden.
		gi := panelIndex(panelGit)
		if rects[gi] != (Rect{}) {
			t.Errorf("termW=%d: git rect = %+v, want zero value when hidden", termW, rects[gi])
		}
	}
}

// TestComputeLayout_LeftColumnStack verifies the left-column panels stack
// vertically in registry order with no gaps, each panel honoring its
// measured height (or MinH floor, whichever is larger).
func TestComputeLayout_LeftColumnStack(t *testing.T) {
	h := layoutHeights{Queue: 5, Autopilot: 1, History: 6, Logs: 8} // Autopilot below MinH=3
	rects := computeLayout(100, 50, h, false)

	qi, ai, hi, li := panelIndex(panelQueue), panelIndex(panelAutopilot), panelIndex(panelHistory), panelIndex(panelLogs)
	if rects[qi].Y != 0 || rects[qi].H != 5 {
		t.Errorf("queue rect = %+v, want Y=0 H=5", rects[qi])
	}
	if rects[ai].Y != 5 || rects[ai].H != 3 { // clamped to MinH
		t.Errorf("autopilot rect = %+v, want Y=5 H=3 (MinH floor)", rects[ai])
	}
	if rects[hi].Y != 8 || rects[hi].H != 6 {
		t.Errorf("history rect = %+v, want Y=8 H=6", rects[hi])
	}
	if rects[li].Y != 14 || rects[li].H != 8 {
		t.Errorf("logs rect = %+v, want Y=14 H=8", rects[li])
	}
}

// TestComputeLayout_GitSideBySide verifies a wide terminal places the git
// panel beside a fixed-width left column, spanning the left column's full
// height, with the remaining terminal width.
func TestComputeLayout_GitSideBySide(t *testing.T) {
	h := layoutHeights{Queue: 5, Autopilot: 3, History: 6, Logs: 8}
	termW, termH := 140, 50
	rects := computeLayout(termW, termH, h, true)

	qi := panelIndex(panelQueue)
	if rects[qi].W != panelTotalWidth {
		t.Errorf("side-by-side: queue width = %d, want %d (fixed)", rects[qi].W, panelTotalWidth)
	}

	leftTotalH := 5 + 3 + 6 + 8
	gi := panelIndex(panelGit)
	git := rects[gi]
	if git.X != panelTotalWidth+1 {
		t.Errorf("git.X = %d, want %d", git.X, panelTotalWidth+1)
	}
	if git.Y != 0 || git.H != leftTotalH {
		t.Errorf("git rect = %+v, want Y=0 H=%d", git, leftTotalH)
	}
	wantW := termW - (panelTotalWidth + 1)
	if git.W != wantW {
		t.Errorf("git.W = %d, want %d", git.W, wantW)
	}
}

// TestComputeLayout_GitStackedBelow verifies a narrow terminal (below the
// side-by-side threshold) stacks the git panel below the left column at
// full terminal width, using remaining terminal height minus the help
// footer line.
func TestComputeLayout_GitStackedBelow(t *testing.T) {
	h := layoutHeights{Queue: 5, Autopilot: 3, History: 6, Logs: 8}
	termW, termH := 60, 50
	rects := computeLayout(termW, termH, h, true)

	if termW >= stackedLayoutThreshold {
		t.Fatalf("test setup: termW=%d must be below stackedLayoutThreshold=%d", termW, stackedLayoutThreshold)
	}

	leftTotalH := 5 + 3 + 6 + 8
	qi := panelIndex(panelQueue)
	if rects[qi].W != panelTotalWidth {
		t.Errorf("stacked: queue width = %d, want %d (narrower than panelTotalWidth stays fixed)", rects[qi].W, panelTotalWidth)
	}

	gi := panelIndex(panelGit)
	git := rects[gi]
	if git.X != 0 || git.Y != leftTotalH {
		t.Errorf("git rect = %+v, want X=0 Y=%d", git, leftTotalH)
	}
	if git.W != termW {
		t.Errorf("git.W = %d, want %d (full terminal width)", git.W, termW)
	}
	wantH := termH - leftTotalH - 1
	if git.H != wantH {
		t.Errorf("git.H = %d, want %d", git.H, wantH)
	}
}

// TestComputeLayout_GitStackedBelowMinHFloor verifies the stacked git panel
// never shrinks below its MinH even when there's no vertical room left.
func TestComputeLayout_GitStackedBelowMinHFloor(t *testing.T) {
	h := layoutHeights{Queue: 20, Autopilot: 20, History: 20, Logs: 20} // 80 lines, way over termH
	rects := computeLayout(60, 50, h, true)

	gi := panelIndex(panelGit)
	gitDef, _ := panelByID(panelGit)
	if rects[gi].H != gitDef.MinH {
		t.Errorf("git.H = %d, want MinH floor %d", rects[gi].H, gitDef.MinH)
	}
}

func TestSafeRender(t *testing.T) {
	def := panelDef{ID: panelQueue, MinW: 40, MinH: 3}

	t.Run("meets minimum renders content", func(t *testing.T) {
		got := safeRender(def, Rect{W: 40, H: 3}, "content")
		if got != "content" {
			t.Errorf("safeRender() = %q, want %q", got, "content")
		}
	})

	t.Run("too narrow renders blank box", func(t *testing.T) {
		got := safeRender(def, Rect{W: 10, H: 3}, "garbled")
		if strings.Contains(got, "garbled") {
			t.Errorf("safeRender() with W below MinW leaked content: %q", got)
		}
		lines := strings.Split(got, "\n")
		if len(lines) != 3 {
			t.Errorf("blank box line count = %d, want 3", len(lines))
		}
		for _, l := range lines {
			if l != strings.Repeat(" ", 10) {
				t.Errorf("blank box line = %q, want 10 spaces", l)
			}
		}
	})

	t.Run("too short renders blank box", func(t *testing.T) {
		got := safeRender(def, Rect{W: 40, H: 1}, "garbled")
		if strings.Contains(got, "garbled") {
			t.Errorf("safeRender() with H below MinH leaked content: %q", got)
		}
	})
}

func TestBlankBox(t *testing.T) {
	if got := blankBox(0, 0); got != "" {
		t.Errorf("blankBox(0,0) = %q, want empty", got)
	}
	if got := blankBox(5, 0); got != "" {
		t.Errorf("blankBox(5,0) = %q, want empty (H<=0)", got)
	}
	got := blankBox(3, 2)
	want := "   \n   "
	if got != want {
		t.Errorf("blankBox(3,2) = %q, want %q", got, want)
	}
}
