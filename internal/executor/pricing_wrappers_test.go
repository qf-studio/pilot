package executor

import (
	"math"
	"testing"

	"github.com/qf-studio/pilot/internal/pricing"
)

// TestPricingWrappers_ReturnPackageValue proves that modelPricing,
// modelCacheReadPrice and estimateCostWithCache return the internal/pricing
// value for the 5.5 models (GH-6355). The expected dollars are written out so
// that a wrapper with its own table fails even when the package agrees.
func TestPricingWrappers_ReturnPackageValue(t *testing.T) {
	tests := []struct {
		model         string
		wantInput     float64
		wantOutput    float64
		wantCacheRead float64
		// 1M each of input, output, 5-minute cache write and cache read.
		wantCost float64
	}{
		{"claude-opus-5-5", 4.00, 20.00, 0.20, 4.00 + 20.00 + 5.00 + 0.20},
		{"claude-sonnet-5-5", 2.00, 10.00, 0.10, 2.00 + 10.00 + 2.50 + 0.10},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			in, out := modelPricing(tt.model)
			pkgIn, pkgOut := pricing.ModelRates(tt.model)
			if in != pkgIn || out != pkgOut || math.Abs(in-tt.wantInput) > 1e-9 || math.Abs(out-tt.wantOutput) > 1e-9 {
				t.Errorf("modelPricing(%q) = %v/%v, package %v/%v, want %v/%v", tt.model, in, out, pkgIn, pkgOut, tt.wantInput, tt.wantOutput)
			}

			cr := modelCacheReadPrice(tt.model, in)
			if pkg := pricing.CacheReadRate(tt.model); cr != pkg || math.Abs(cr-tt.wantCacheRead) > 1e-9 {
				t.Errorf("modelCacheReadPrice(%q) = %v, package %v, want %v", tt.model, cr, pkg, tt.wantCacheRead)
			}

			const m = 1_000_000
			cost := estimateCostWithCache(m, m, m, m, tt.model)
			if pkg := pricing.Cost(tt.model, m, m, m, m, 0); math.Abs(cost-pkg) > 1e-9 || math.Abs(cost-tt.wantCost) > 1e-9 {
				t.Errorf("estimateCostWithCache(%q) = %v, package %v, want %v", tt.model, cost, pkg, tt.wantCost)
			}
		})
	}
}

// TestEstimateCostWithCacheTTL_Prices1hWriteAtTwiceInput pins the 1-hour
// cache write split (GH-6355): the 1-hour share of the cache write total is
// priced at 2x input, the rest at 1.25x.
func TestEstimateCostWithCacheTTL_Prices1hWriteAtTwiceInput(t *testing.T) {
	tests := []struct {
		name            string
		cacheCreation   int64
		cacheCreation1h int64
		want            float64
	}{
		{"total only is priced as 5-minute", 1_000_000, 0, 5.00},
		{"all 1-hour", 1_000_000, 1_000_000, 8.00},
		{"split 600K 5m + 400K 1h", 1_000_000, 400_000, 0.6*5.00 + 0.4*8.00},
		{"1-hour above total is capped at total", 1_000_000, 2_000_000, 8.00},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateCostWithCacheTTL(0, 0, tt.cacheCreation, tt.cacheCreation1h, 0, "claude-opus-5-5")
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("estimateCostWithCacheTTL = %v, want %v", got, tt.want)
			}
		})
	}
	if a, b := estimateCostWithCache(1, 2, 3, 4, "claude-opus-5-5"), estimateCostWithCacheTTL(1, 2, 3, 0, 4, "claude-opus-5-5"); a != b {
		t.Errorf("estimateCostWithCache = %v, want the TTL form with no 1-hour share %v", a, b)
	}
}

// TestClaudeCodeParseUsage_Reads1hCacheWrite checks that the 1-hour cache
// write count of the usage cache_creation object reaches the backend event,
// and that a usage object with only the total leaves it at zero (GH-6355).
func TestClaudeCodeParseUsage_Reads1hCacheWrite(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	ev := backend.parseStreamEvent(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":300,"cache_read_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}`)
	if ev.CacheCreationInputTokens != 300 || ev.CacheCreation1hInputTokens != 200 {
		t.Fatalf("cache write = %d total / %d 1h, want 300 / 200", ev.CacheCreationInputTokens, ev.CacheCreation1hInputTokens)
	}

	ev = backend.parseStreamEvent(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":300}}`)
	if ev.CacheCreationInputTokens != 300 || ev.CacheCreation1hInputTokens != 0 {
		t.Fatalf("total-only cache write = %d total / %d 1h, want 300 / 0", ev.CacheCreationInputTokens, ev.CacheCreation1hInputTokens)
	}
}

// TestProcessBackendEvent_Accumulates1hCacheWrite checks that the 1-hour
// share reaches the progress state the run cost is computed from (GH-6355).
func TestProcessBackendEvent_Accumulates1hCacheWrite(t *testing.T) {
	r := NewRunner()
	state := &progressState{}
	r.processBackendEvent("T-1", BackendEvent{Type: EventTypeText, CacheCreationInputTokens: 300, CacheCreation1hInputTokens: 200}, state)
	r.processBackendEvent("T-1", BackendEvent{Type: EventTypeText, CacheCreationInputTokens: 100, CacheCreation1hInputTokens: 50}, state)
	if state.cacheCreationInputTokens != 400 || state.cacheCreation1hTokens != 250 {
		t.Fatalf("state cache write = %d total / %d 1h, want 400 / 250", state.cacheCreationInputTokens, state.cacheCreation1hTokens)
	}
}
