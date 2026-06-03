package sdkshim

import "testing"

func TestPriorityFromSDK(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{SDKPriorityUrgent, PilotPriorityUrgent},
		{SDKPriorityHigh, PilotPriorityHigh},
		{SDKPriorityMedium, PilotPriorityMedium},
		{SDKPriorityLow, PilotPriorityLow},
		{SDKPriorityNone, PilotPriorityNone},
		{"", PilotPriorityNone},
		{"unknown-value", PilotPriorityNone},
	}
	for _, c := range cases {
		if got := PriorityFromSDK(c.in); got != c.want {
			t.Errorf("PriorityFromSDK(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPriorityToSDK_RoundTrip(t *testing.T) {
	// Round-trip every defined Pilot priority through SDK and back.
	for _, p := range []int{
		PilotPriorityNone,
		PilotPriorityUrgent,
		PilotPriorityHigh,
		PilotPriorityMedium,
		PilotPriorityLow,
	} {
		s := PriorityToSDK(p)
		back := PriorityFromSDK(s)
		if back != p {
			t.Errorf("round-trip %d → %q → %d", p, s, back)
		}
	}
}

func TestPriorityToSDK_OutOfRange(t *testing.T) {
	if got := PriorityToSDK(99); got != SDKPriorityNone {
		t.Errorf("PriorityToSDK(99) = %q, want %q", got, SDKPriorityNone)
	}
}
