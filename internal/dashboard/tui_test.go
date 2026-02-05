package dashboard

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
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

func TestBuildMiniCard(t *testing.T) {
	card := buildMiniCard("TEST", "42", "detail one", "detail two", "▁▂▃▄▅▆▇█▁▂▃▄▅▆▇█•")

	lines := strings.Split(card, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != cardWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, cardWidth, line)
		}
	}

	// Check border characters present
	if !strings.Contains(card, "╭") {
		t.Error("missing top-left border ╭")
	}
	if !strings.Contains(card, "╰") {
		t.Error("missing bottom-left border ╰")
	}
	if !strings.Contains(card, "│") {
		t.Error("missing side border │")
	}
}

func TestRenderMetricsCards(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		TotalTokens:  50000,
		InputTokens:  30000,
		OutputTokens: 20000,
		TotalCostUSD: 1.50,
		CostPerTask:  0.25,
		TotalTasks:   10,
		Succeeded:    8,
		Failed:       2,
		TokenHistory: []int64{100, 200, 300, 400, 500, 600, 700},
		CostHistory:  []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7},
		TaskHistory:  []int{1, 2, 3, 2, 1, 3, 2},
	}

	output := m.renderMetricsCards()

	if !strings.Contains(output, "TOKENS") {
		t.Error("output missing TOKENS card")
	}
	if !strings.Contains(output, "COST") {
		t.Error("output missing COST card")
	}
	if !strings.Contains(output, "TASKS") {
		t.Error("output missing TASKS card")
	}
}

func TestRenderMetricsCards_ZeroState(t *testing.T) {
	m := NewModel("test")
	// metricsCard is zero-value MetricsCardData

	// Must not panic
	output := m.renderMetricsCards()

	if output == "" {
		t.Error("zero-state renderMetricsCards returned empty string")
	}
	if !strings.Contains(output, "TOKENS") {
		t.Error("zero-state output missing TOKENS card")
	}
	if !strings.Contains(output, "COST") {
		t.Error("zero-state output missing COST card")
	}
	if !strings.Contains(output, "TASKS") {
		t.Error("zero-state output missing TASKS card")
	}
}
