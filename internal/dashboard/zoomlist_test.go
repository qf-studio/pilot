package dashboard

import "testing"

func TestZoomListViewportHeight(t *testing.T) {
	tests := []struct {
		name string
		h    int
		want int
	}{
		{"tall panel", 30, 25},
		{"exact overhead", 5, 1},
		{"below overhead clamps to 1", 3, 1},
		{"zero height clamps to 1", 0, 1},
		{"negative height clamps to 1", -10, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := zoomListViewportHeight(tt.h); got != tt.want {
				t.Errorf("zoomListViewportHeight(%d) = %d, want %d", tt.h, got, tt.want)
			}
		})
	}
}

func TestEnsureSelVisible(t *testing.T) {
	tests := []struct {
		name                string
		sel, scroll         int
		total, visible      int
		wantSel, wantScroll int
	}{
		{"empty list resets to zero", 3, 2, 0, 5, 0, 0},
		{"negative total resets to zero", 0, 0, -1, 5, 0, 0},
		{"selection already visible, scroll unchanged", 4, 2, 10, 5, 4, 2},
		{"selection above window scrolls up to it", 1, 5, 10, 5, 1, 1},
		{"selection below window scrolls down to it", 9, 0, 10, 5, 9, 5},
		{"negative selection clamps to zero", -3, 2, 10, 5, 0, 0},
		{"selection past end clamps to last item", 99, 0, 10, 5, 9, 5},
		{"scroll clamps when list shorter than window", 2, 8, 10, 20, 2, 0},
		{"zero visible treated as one row", 3, 0, 10, 0, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSel, gotScroll := ensureSelVisible(tt.sel, tt.scroll, tt.total, tt.visible)
			if gotSel != tt.wantSel || gotScroll != tt.wantScroll {
				t.Errorf("ensureSelVisible(sel=%d, scroll=%d, total=%d, visible=%d) = (%d, %d), want (%d, %d)",
					tt.sel, tt.scroll, tt.total, tt.visible, gotSel, gotScroll, tt.wantSel, tt.wantScroll)
			}
		})
	}
}

// TestEnsureSelVisible_StaysWithinWindow exercises a scroll walk over a
// 20-item list with a 5-row viewport, asserting the invariant that sel is
// always within [scroll, scroll+visible) after each step (the property
// ensureSelVisible exists to guarantee for the eventual zoomed queue/
// autopilot/history panels).
func TestEnsureSelVisible_StaysWithinWindow(t *testing.T) {
	const total, visible = 20, 5
	sel, scroll := 0, 0
	for _, next := range []int{1, 2, 3, 4, 5, 10, 19, 15, 0} {
		sel, scroll = ensureSelVisible(next, scroll, total, visible)
		if sel < scroll || sel >= scroll+visible {
			t.Fatalf("after moving to %d: sel=%d, scroll=%d not within [%d, %d)", next, sel, scroll, scroll, scroll+visible)
		}
	}
}

func TestZoomListIndicator(t *testing.T) {
	tests := []struct {
		name              string
		start, end, total int
		want              string
	}{
		{"first page", 0, 5, 20, "[1-5 of 20]"},
		{"middle page", 10, 15, 20, "[11-15 of 20]"},
		{"empty list", 0, 0, 0, ""},
		{"single item", 0, 1, 1, "[1-1 of 1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := zoomListIndicator(tt.start, tt.end, tt.total); got != tt.want {
				t.Errorf("zoomListIndicator(%d, %d, %d) = %q, want %q", tt.start, tt.end, tt.total, got, tt.want)
			}
		})
	}
}

func TestZoomListSelector(t *testing.T) {
	if got := zoomListSelector(false); got != "  " {
		t.Errorf("zoomListSelector(false) = %q, want two spaces", got)
	}
	want := dimStyle.Render("▸") + " "
	if got := zoomListSelector(true); got != want {
		t.Errorf("zoomListSelector(true) = %q, want %q", got, want)
	}
}
