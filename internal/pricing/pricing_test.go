package pricing

import (
	"math"
	"testing"
)

const tolerance = 1e-9

func TestModelRatesAndCacheReadRate(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantInput     float64
		wantOutput    float64
		wantCacheRead float64
	}{
		// Rates of GH-6355 (Anthropic first-party list, checked 2026-11-03).
		{"opus 5.5", "claude-opus-5-5", 4.00, 20.00, 0.20},
		{"sonnet 5.5", "claude-sonnet-5-5", 2.00, 10.00, 0.10},
		{"sonnet 5", "claude-sonnet-5", 2.00, 10.00, 0.20},
		{"haiku 5.5 (no explicit cache read: 10% of input)", "claude-haiku-5-5", 0.10, 0.50, 0.01},
		// Provider prefix and date suffix still match the 5.5 row.
		{"opus 5.5 with provider prefix", "us.anthropic.claude-opus-5-5", 4.00, 20.00, 0.20},
		{"opus 5.5 with date suffix", "claude-opus-5-5-20261101", 4.00, 20.00, 0.20},
		{"opus 5.5 upper case", "Claude-OPUS-5-5", 4.00, 20.00, 0.20},
		{"sonnet 5.5 with provider prefix and date suffix", "anthropic/claude-sonnet-5-5-20261101", 2.00, 10.00, 0.10},
		// Rows moved from the executor table.
		{"fable 5.1", "claude-fable-5-1", 10.00, 50.00, 0.25},
		{"fable 5 (no explicit cache read)", "claude-fable-5", 10.00, 50.00, 1.00},
		{"mythos 5", "claude-mythos-5", 10.00, 50.00, 1.00},
		{"opus 5", "claude-opus-5", 5.00, 25.00, 0.50},
		{"opus 4.6", "claude-opus-4-6", 5.00, 25.00, 0.50},
		{"legacy opus 4.1", "claude-opus-4-1-20250805", 15.00, 75.00, 1.50},
		{"legacy opus 4.0", "claude-opus-4-0", 15.00, 75.00, 1.50},
		{"legacy opus 4 exact", "claude-opus-4", 15.00, 75.00, 1.50},
		{"sonnet 4.6 (no explicit cache read)", "claude-sonnet-4-6", 3.00, 15.00, 0.30},
		{"haiku 4.5", "claude-haiku-4-5-20251001", 1.00, 5.00, 0.10},
		{"qwen coder plus", "qwen3-coder-plus", 1.00, 5.00, 0.10},
		{"qwen coder 480b", "qwen3-coder-480b", 1.00, 5.00, 0.10},
		{"qwen coder flash", "qwen3-coder-flash", 0.30, 1.50, 0.03},
		{"qwen coder next", "qwen3-coder-next", 0.07, 0.30, 0.007},
		// Unknown model keeps the default (Sonnet 4.x) rate.
		{"unknown model", "some-unknown-model", 3.00, 15.00, 0.30},
		{"empty model", "", 3.00, 15.00, 0.30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, out := ModelRates(tt.model)
			if math.Abs(in-tt.wantInput) > tolerance {
				t.Errorf("ModelRates(%q) input = %v, want %v", tt.model, in, tt.wantInput)
			}
			if math.Abs(out-tt.wantOutput) > tolerance {
				t.Errorf("ModelRates(%q) output = %v, want %v", tt.model, out, tt.wantOutput)
			}
			if got := CacheReadRate(tt.model); math.Abs(got-tt.wantCacheRead) > tolerance {
				t.Errorf("CacheReadRate(%q) = %v, want %v", tt.model, got, tt.wantCacheRead)
			}
		})
	}
}

func TestCost(t *testing.T) {
	tests := []struct {
		name                                 string
		model                                string
		input, output, cacheRead, cw5m, cw1h int64
		want                                 float64
	}{
		{
			// 0.1M*4 + 0.05M*20 + 1M*0.20 + 0.2M*4*1.25 + 0.1M*4*2
			// = 0.40 + 1.00 + 0.20 + 1.00 + 0.80
			name: "opus 5.5 all five token kinds", model: "claude-opus-5-5",
			input: 100_000, output: 50_000, cacheRead: 1_000_000, cw5m: 200_000, cw1h: 100_000,
			want: 3.40,
		},
		{
			// 0.1M*2 + 0.05M*10 + 1M*0.10 + 0.2M*2*1.25 + 0.1M*2*2
			// = 0.20 + 0.50 + 0.10 + 0.50 + 0.40
			name: "sonnet 5.5 all five token kinds", model: "claude-sonnet-5-5",
			input: 100_000, output: 50_000, cacheRead: 1_000_000, cw5m: 200_000, cw1h: 100_000,
			want: 1.70,
		},
		{
			name: "1-hour cache write is 2x input", model: "claude-sonnet-4-6",
			cw1h: 1_000_000,
			want: 6.00,
		},
		{
			name: "5-minute cache write is 1.25x input", model: "claude-sonnet-4-6",
			cw5m: 1_000_000,
			want: 3.75,
		},
		{
			name: "unknown model at the default rate", model: "some-unknown-model",
			input: 1_000_000, output: 1_000_000,
			want: 18.00,
		},
		{name: "zero tokens", model: "claude-opus-5-5", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Cost(tt.model, tt.input, tt.output, tt.cacheRead, tt.cw5m, tt.cw1h)
			if math.Abs(got-tt.want) > tolerance {
				t.Errorf("Cost(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
