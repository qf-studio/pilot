package dashboard

import (
	"testing"
	"unicode/utf8"
)

func TestFormatCompact(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{57300, "57.3K"},
		{1000000, "1.0M"},
		{1234567, "1.2M"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatCompact(tt.input)
			if got != tt.want {
				t.Errorf("formatCompact(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeToSparkline(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		width  int
		want   []int
	}{
		{
			name:   "empty input returns all zeros",
			values: nil,
			width:  7,
			want:   []int{0, 0, 0, 0, 0, 0, 0},
		},
		{
			name:   "single value maps to midpoint",
			values: []float64{42},
			width:  7,
			want:   []int{0, 0, 0, 0, 0, 0, 4},
		},
		{
			name:   "all same values map to midpoint",
			values: []float64{5, 5, 5, 5, 5, 5, 5},
			width:  7,
			want:   []int{4, 4, 4, 4, 4, 4, 4},
		},
		{
			name:   "ascending values span full range",
			values: []float64{0, 1, 2, 3, 4, 5, 6, 7, 8},
			width:  9,
			want:   []int{0, 1, 2, 3, 4, 5, 6, 7, 8},
		},
		{
			name:   "fewer values than width left-pads with zeros",
			values: []float64{0, 100},
			width:  5,
			want:   []int{0, 0, 0, 0, 8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeToSparkline(tt.values, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %d, want %d (full: %v)", i, got[i], tt.want[i], got)
					break
				}
			}
		})
	}
}

func TestRenderSparkline(t *testing.T) {
	tests := []struct {
		name    string
		levels  []int
		pulsing bool
	}{
		{
			name:    "pulsing includes dot",
			levels:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6},
			pulsing: true,
		},
		{
			name:    "not pulsing has space",
			levels:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6},
			pulsing: false,
		},
		{
			name:    "all zeros",
			levels:  []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			pulsing: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderSparkline(tt.levels, tt.pulsing)

			// Visual width must equal cardInnerWidth (17)
			runeCount := utf8.RuneCountInString(got)
			if runeCount != cardInnerWidth {
				t.Errorf("visual width = %d runes, want %d (got %q)", runeCount, cardInnerWidth, got)
			}

			// Check pulsing indicator
			runes := []rune(got)
			lastRune := runes[len(runes)-1]
			if tt.pulsing && lastRune != '•' {
				t.Errorf("pulsing=true but last rune = %q, want '•'", lastRune)
			}
			if !tt.pulsing && lastRune != ' ' {
				t.Errorf("pulsing=false but last rune = %q, want ' '", lastRune)
			}
		})
	}
}
