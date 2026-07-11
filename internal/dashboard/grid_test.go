package dashboard

import "testing"

func TestOverlaps(t *testing.T) {
	tests := []struct {
		name         string
		a, al, b, bl int
		want         bool
	}{
		{"identical spans", 0, 10, 0, 10, true},
		{"partial overlap", 0, 10, 5, 10, true},
		{"adjacent, no overlap", 0, 10, 10, 10, false},
		{"disjoint", 0, 5, 10, 5, false},
		{"b contains a", 5, 2, 0, 20, true},
		{"a contains b", 0, 20, 5, 2, true},
		{"touching at zero width", 0, 0, 0, 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overlaps(tt.a, tt.al, tt.b, tt.bl); got != tt.want {
				t.Errorf("overlaps(%d,%d,%d,%d) = %v, want %v", tt.a, tt.al, tt.b, tt.bl, got, tt.want)
			}
		})
	}
}

func TestCropVertical(t *testing.T) {
	s := "line0\nline1\nline2\nline3\nline4"
	tests := []struct {
		name           string
		offset, height int
		want           string
	}{
		{"middle window", 1, 2, "line1\nline2"},
		{"from start", 0, 3, "line0\nline1\nline2"},
		{"negative offset clamps to zero", -5, 2, "line0\nline1"},
		{"offset past end clamps", 100, 2, ""},
		{"height overruns end clamps", 3, 10, "line3\nline4"},
		{"zero height", 2, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cropVertical(s, tt.offset, tt.height); got != tt.want {
				t.Errorf("cropVertical(offset=%d, height=%d) = %q, want %q", tt.offset, tt.height, got, tt.want)
			}
		})
	}
}

func TestFocusMove_OutOfRange(t *testing.T) {
	rects := []Rect{{X: 0, Y: 0, W: 10, H: 10}}
	if got := focusMove(rects, -1, 'l'); got != -1 {
		t.Errorf("cur=-1 should return unchanged, got %d", got)
	}
	if got := focusMove(rects, 5, 'l'); got != 5 {
		t.Errorf("cur=5 (out of range) should return unchanged, got %d", got)
	}
}

func TestFocusMove_UnknownDirection(t *testing.T) {
	rects := []Rect{
		{X: 0, Y: 0, W: 10, H: 10},
		{X: 10, Y: 0, W: 10, H: 10},
	}
	if got := focusMove(rects, 0, 'x'); got != 0 {
		t.Errorf("unknown direction should return cur unchanged, got %d", got)
	}
}

func TestFocusMove_NothingInDirection(t *testing.T) {
	// A single panel with nothing to its left.
	rects := []Rect{{X: 0, Y: 0, W: 10, H: 10}}
	if got := focusMove(rects, 0, 'h'); got != 0 {
		t.Errorf("no panel to the left should return cur unchanged, got %d", got)
	}
}

// TestFocusMove_SimpleGrid exercises a basic 2x2 grid: nearest neighbor by
// edge distance in each of the four directions.
func TestFocusMove_SimpleGrid(t *testing.T) {
	// Layout:
	//   [0: top-left  ][1: top-right ]
	//   [2: bot-left  ][3: bot-right ]
	rects := []Rect{
		{X: 0, Y: 0, W: 10, H: 5},
		{X: 10, Y: 0, W: 10, H: 5},
		{X: 0, Y: 5, W: 10, H: 5},
		{X: 10, Y: 5, W: 10, H: 5},
	}
	tests := []struct {
		name string
		cur  int
		dir  byte
		want int
	}{
		{"top-left right -> top-right", 0, 'l', 1},
		{"top-left down -> bot-left", 0, 'j', 2},
		{"top-right left -> top-left", 1, 'h', 0},
		{"top-right down -> bot-right", 1, 'j', 3},
		{"bot-left up -> top-left", 2, 'k', 0},
		{"bot-left right -> bot-right", 2, 'l', 3},
		{"bot-right up -> top-right", 3, 'k', 1},
		{"bot-right left -> bot-left", 3, 'h', 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := focusMove(rects, tt.cur, tt.dir); got != tt.want {
				t.Errorf("focusMove(cur=%d, dir=%c) = %d, want %d", tt.cur, tt.dir, got, tt.want)
			}
		})
	}
}

// TestFocusMove_TallColumnTieBreak reproduces the dashboard's left-stack /
// tall-git-column layout: a full-height right column Y-overlaps every row in
// the left stack, so a naive nearest-by-primary-distance search ties across
// rows and the result depends on iteration order. The perpendicular-axis
// tie-break must pick the row whose vertical center is closest to the
// current panel's.
func TestFocusMove_TallColumnTieBreak(t *testing.T) {
	rects := []Rect{
		{X: 0, Y: 0, W: 40, H: 5},   // 0: left stack row 1
		{X: 0, Y: 5, W: 40, H: 5},   // 1: left stack row 2 (current)
		{X: 0, Y: 10, W: 40, H: 5},  // 2: left stack row 3
		{X: 40, Y: 0, W: 20, H: 15}, // 3: tall git column, spans all rows
	}
	// From row 2 (index 1), moving right must land on the git column.
	if got := focusMove(rects, 1, 'l'); got != 3 {
		t.Errorf("focusMove from middle row right = %d, want 3 (git column)", got)
	}
	// From the git column, moving left must land on the row nearest its own
	// vertical center (row 1, index 1), not row 0 or row 2 which tie on the
	// primary (X) distance.
	if got := focusMove(rects, 3, 'h'); got != 1 {
		t.Errorf("focusMove from git column left = %d, want 1 (nearest row by center)", got)
	}
}

func TestFocusMove_PicksNearestNotFarther(t *testing.T) {
	// Three panels in a horizontal row; from the leftmost, moving right must
	// pick the adjacent panel, not the farther one.
	rects := []Rect{
		{X: 0, Y: 0, W: 10, H: 10},
		{X: 10, Y: 0, W: 10, H: 10},
		{X: 30, Y: 0, W: 10, H: 10},
	}
	if got := focusMove(rects, 0, 'l'); got != 1 {
		t.Errorf("focusMove(0, 'l') = %d, want 1 (nearest)", got)
	}
	if got := focusMove(rects, 2, 'h'); got != 1 {
		t.Errorf("focusMove(2, 'h') = %d, want 1 (nearest)", got)
	}
}
