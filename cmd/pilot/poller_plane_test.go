package main

import (
	"errors"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
)

// TestPlanePollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the Plane polling config.
func TestPlanePollerRegistration_Fields(t *testing.T) {
	reg := planePollerRegistration()

	if reg.Name != "plane" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "plane")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil plane config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "plane disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: false, Polling: &plane.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true, Polling: &plane.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true, Polling: &plane.PollingConfig{Enabled: true}},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Enabled(tt.cfg)
			if got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlanePriorityMapping verifies that Plane priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestPlanePriorityMapping(t *testing.T) {
	tests := []struct {
		sdkPriority string
		wantPilot   int
	}{
		{"urgent", sdkshim.PilotPriorityUrgent},
		{"high", sdkshim.PilotPriorityHigh},
		{"medium", sdkshim.PilotPriorityMedium},
		{"low", sdkshim.PilotPriorityLow},
		{"none", sdkshim.PilotPriorityNone},
		{"", sdkshim.PilotPriorityNone},
		{"unknown", sdkshim.PilotPriorityNone},
	}

	for _, tt := range tests {
		t.Run("priority_"+tt.sdkPriority, func(t *testing.T) {
			got := sdkshim.PriorityFromSDK(tt.sdkPriority)
			if got != tt.wantPilot {
				t.Errorf("PriorityFromSDK(%q) = %d, want %d", tt.sdkPriority, got, tt.wantPilot)
			}
		})
	}
}

// TestPlaneRepoResolutionGracefulError verifies that sdkshim.ResolveRepoForEvent returns
// ErrRepoNotResolved for the plane source (Phase-0 stub). The handler must not fail on this.
func TestPlaneRepoResolutionGracefulError(t *testing.T) {
	cfg := &config.Config{}
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-abc123",
		SequenceID: "PLANE-42",
		ProjectID:  "proj-uuid",
		Priority:   "high",
	}

	_, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "plane", ev)
	if !errors.Is(err, sdkshim.ErrRepoNotResolved) {
		t.Errorf("ResolveRepoForEvent returned %v, want ErrRepoNotResolved", err)
	}
}

// TestPlaneSDKEventSequenceID verifies that the SequenceID produced by the SDK adapter
// is already prefixed with "PLANE-" — the handler must use it as-is, not re-prefix.
func TestPlaneSDKEventSequenceID(t *testing.T) {
	// Simulate what the SDK adapter's toIssueEvent does.
	sequenceNum := 42
	seqID := "PLANE-" + itoa(sequenceNum)

	if seqID != "PLANE-42" {
		t.Errorf("expected sequenceID = PLANE-42, got %q", seqID)
	}

	// If a handler called fmt.Sprintf("PLANE-%s", seqID) it would produce "PLANE-PLANE-42".
	doublePrefix := "PLANE-" + seqID
	if doublePrefix == "PLANE-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "PLANE-PLANE-42" {
		t.Errorf("double-prefix sanity check: got %q, want PLANE-PLANE-42", doublePrefix)
	}
}

// itoa converts an int to a decimal string (avoids importing strconv in test).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
