package dashboard

// Spatial grid layout primitives ported from qf-studio/grom
// internal/app/grid.go (focusMove/overlaps/Rect) and internal/app/model.go
// (cropVertical), adapted for the pilot dashboard's panel registry. See
// TASK-398 for the full navigation/zoom design this module supports.

import "strings"

// Rect is a panel's placement in terminal cells, top-left origin.
type Rect struct{ X, Y, W, H int }

// focusMove returns the index of the panel nearest to cur in direction dir
// ('h' left, 'j' down, 'k' up, 'l' right), or cur when nothing lies that way.
// Horizontal moves only consider panels whose rows overlap the current one
// (and vertical moves, columns), then pick the nearest by edge distance — so
// focus tracks the visual row/column rather than drifting diagonally.
//
// Adaptation over the grom original: when candidates tie on primary edge
// distance, prefer the one nearer on the perpendicular axis. The tall git
// column Y-overlaps every left-stack row, so without this tie-break an `l`
// move from any left-stack row would ambiguously match whichever panel
// happened to iterate first.
func focusMove(rects []Rect, cur int, dir byte) int {
	if cur < 0 || cur >= len(rects) {
		return cur
	}
	c := rects[cur]
	best, bestDist, bestCross := cur, 0, 0
	found := false
	for i, r := range rects {
		if i == cur {
			continue
		}
		var dist, cross int
		switch dir {
		case 'h':
			if r.X >= c.X || !overlaps(c.Y, c.H, r.Y, r.H) {
				continue
			}
			dist = c.X - r.X
			cross = crossDistance(c.Y, c.H, r.Y, r.H)
		case 'l':
			if r.X <= c.X || !overlaps(c.Y, c.H, r.Y, r.H) {
				continue
			}
			dist = r.X - c.X
			cross = crossDistance(c.Y, c.H, r.Y, r.H)
		case 'k':
			if r.Y >= c.Y || !overlaps(c.X, c.W, r.X, r.W) {
				continue
			}
			dist = c.Y - r.Y
			cross = crossDistance(c.X, c.W, r.X, r.W)
		case 'j':
			if r.Y <= c.Y || !overlaps(c.X, c.W, r.X, r.W) {
				continue
			}
			dist = r.Y - c.Y
			cross = crossDistance(c.X, c.W, r.X, r.W)
		default:
			return cur
		}
		if !found || dist < bestDist || (dist == bestDist && cross < bestCross) {
			bestDist, bestCross, best, found = dist, cross, i, true
		}
	}
	return best
}

// overlaps reports whether the spans [a, a+al) and [b, b+bl) intersect.
func overlaps(a, al, b, bl int) bool {
	return a < b+bl && b < a+al
}

// crossDistance measures how far apart two overlapping spans' centers are,
// used as focusMove's tie-break on the axis perpendicular to movement.
func crossDistance(a, al, b, bl int) int {
	ca, cb := a+al/2, b+bl/2
	d := ca - cb
	if d < 0 {
		d = -d
	}
	return d
}

// cropVertical returns the height lines of s starting at offset, for scroll.
func cropVertical(s string, offset, height int) string {
	lines := strings.Split(s, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[offset:end], "\n")
}
