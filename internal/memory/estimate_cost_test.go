package memory

import (
	"math"
	"testing"

	"github.com/qf-studio/pilot/internal/pricing"
)

// TestEstimateCost_ReturnsPackageValue proves that EstimateCost prices the
// 5.5 models through internal/pricing (GH-6355): before it, claude-opus-5-5
// fell to the generic opus $5/$25 and claude-sonnet-5-5 to the $3/$15 default.
func TestEstimateCost_ReturnsPackageValue(t *testing.T) {
	tests := []struct {
		model string
		want  float64 // 1M input + 1M output
	}{
		{"claude-opus-5-5", 4.00 + 20.00},
		{"claude-sonnet-5-5", 2.00 + 10.00},
		{"claude-opus-4-6", 5.00 + 25.00},
		{"claude-opus-4-1", 15.00 + 75.00},
		{"claude-haiku-4-5", 1.00 + 5.00},
		{"some-unknown-model", 3.00 + 15.00},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := EstimateCost(1_000_000, 1_000_000, tt.model)
			pkg := pricing.Cost(tt.model, 1_000_000, 1_000_000, 0, 0, 0)
			if math.Abs(got-pkg) > 1e-9 || math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("EstimateCost(%q) = %v, package %v, want %v", tt.model, got, pkg, tt.want)
			}
		})
	}
}
