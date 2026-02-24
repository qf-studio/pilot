package comms

import (
	"testing"
	"time"
)

func TestCleanInternalSignals(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no signals", "hello world", "hello world"},
		{"exit signal", "result\nEXIT_SIGNAL: true\ndone", "result\ndone"},
		{"nav complete", "output\n[NAV_COMPLETE]\nfinal", "output\nfinal"},
		{"navigator block", "before\nNAVIGATOR_STATUS\nPhase: IMPL\n━━━━━━━━━━\nafter", "before\nafter"},
		{"leading blanks stripped", "\n\nhello", "hello"},
		{"trailing blanks stripped", "hello\n\n", "hello"},
		{"multiple signals", "[EXIT_SIGNAL]\n[IMPL_DONE]\nreal output", "real output"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanInternalSignals(tt.in)
			if got != tt.want {
				t.Errorf("CleanInternalSignals(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChunkContent(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   int // expected number of chunks
	}{
		{"short", "hello", 100, 1},
		{"exact", "hello", 5, 1},
		{"split", "hello world foo bar", 10, 2},
		{"newline break", "line1\nline2\nline3", 12, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := ChunkContent(tt.text, tt.maxLen)
			if len(chunks) != tt.want {
				t.Errorf("ChunkContent(%q, %d) returned %d chunks, want %d", tt.text, tt.maxLen, len(chunks), tt.want)
			}
			// Verify no chunk exceeds maxLen
			for i, c := range chunks {
				if len(c) > tt.maxLen {
					t.Errorf("chunk[%d] length %d exceeds maxLen %d", i, len(c), tt.maxLen)
				}
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   string
	}{
		{"short", "hi", 10, "hi"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 8, "hello..."},
		{"tiny max", "hello", 2, "he"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateText(tt.text, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateText(%q, %d) = %q, want %q", tt.text, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestGenerateProgressBar(t *testing.T) {
	tests := []struct {
		progress int
		want     string
	}{
		{0, "░░░░░░░░░░"},
		{50, "█████░░░░░"},
		{100, "██████████"},
		{-10, "░░░░░░░░░░"},
		{150, "██████████"},
		{33, "███░░░░░░░"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := GenerateProgressBar(tt.progress)
			if got != tt.want {
				t.Errorf("GenerateProgressBar(%d) = %q, want %q", tt.progress, got, tt.want)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-2 * 24 * time.Hour), "2d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTimeAgo(tt.t)
			if got != tt.want {
				t.Errorf("FormatTimeAgo() = %q, want %q", got, tt.want)
			}
		})
	}
}
