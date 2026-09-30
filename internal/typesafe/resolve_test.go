package typesafe

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		regex       string
		ans         *Answer
		min         float64
		shadow      bool
		wantVerdict string
		wantReason  Reason
		wantShadow  Reason
	}{
		{"nil answer", "other", nil, 0.8, false, "other", ReasonError, ReasonError},
		{"nil answer shadow", "other", nil, 0.8, true, "other", ReasonError, ReasonError},
		{"low confidence", "other", &Answer{Choice: "mutation", Confidence: 0.5}, 0.8, false, "other", ReasonLowConfidence, ReasonLowConfidence},
		{"low confidence shadow", "other", &Answer{Choice: "mutation", Confidence: 0.5}, 0.8, true, "other", ReasonLowConfidence, ReasonLowConfidence},
		{"agreed", "other", &Answer{Choice: "other", Confidence: 0.9}, 0.8, false, "other", ReasonAgreed, ReasonAgreed},
		{"agreed shadow", "other", &Answer{Choice: "other", Confidence: 0.9}, 0.8, true, "other", ReasonAgreed, ReasonAgreed},
		{"overrode", "other", &Answer{Choice: "mutation", Confidence: 0.9}, 0.8, false, "mutation", ReasonOverrode, ReasonOverrode},
		{"shadow keeps regex", "other", &Answer{Choice: "mutation", Confidence: 0.9}, 0.8, true, "other", ReasonShadow, ReasonOverrode},
		{"confidence at threshold overrides", "other", &Answer{Choice: "mutation", Confidence: 0.8}, 0.8, false, "mutation", ReasonOverrode, ReasonOverrode},
		{"low confidence equal verdict", "other", &Answer{Choice: "other", Confidence: 0.1}, 0.8, false, "other", ReasonLowConfidence, ReasonLowConfidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, r := Resolve(tt.regex, tt.ans, tt.min, tt.shadow)
			if v != tt.wantVerdict || r != tt.wantReason {
				t.Errorf("Resolve = (%q, %q), want (%q, %q)", v, r, tt.wantVerdict, tt.wantReason)
			}
			if got := ResolveShadowReason(tt.regex, tt.ans, tt.min); got != tt.wantShadow {
				t.Errorf("ResolveShadowReason = %q, want %q", got, tt.wantShadow)
			}
		})
	}
}

func TestResolve_LowConfidenceKeepsRegex(t *testing.T) {
	v, r := Resolve("paste_output", &Answer{Choice: "mutation", Confidence: 0.79}, 0.8, false)
	if v != "paste_output" || r != ReasonLowConfidence {
		t.Fatalf("got (%q, %q)", v, r)
	}
}

func TestReasonConstants(t *testing.T) {
	want := map[Reason]string{
		ReasonRegexOnly: "regex_only", ReasonAgreed: "agreed", ReasonOverrode: "overrode",
		ReasonLowConfidence: "low_confidence", ReasonShadow: "shadow", ReasonError: "error",
	}
	for r, s := range want {
		if string(r) != s {
			t.Errorf("%q != %q", r, s)
		}
	}
}
