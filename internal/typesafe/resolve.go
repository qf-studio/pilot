package typesafe

// Reason names why Resolve returned the verdict it did. Every TypeSafe-backed
// gate logs the same six reasons so the shadow-to-live flip reads the same way
// for all of them.
type Reason string

const (
	// ReasonRegexOnly: no model was consulted; the regex verdict stands.
	ReasonRegexOnly Reason = "regex_only"
	// ReasonAgreed: the model and the regex chose the same verdict.
	ReasonAgreed Reason = "agreed"
	// ReasonOverrode: the model's verdict replaced the regex verdict.
	ReasonOverrode Reason = "overrode"
	// ReasonLowConfidence: the model disagreed but below the minimum confidence.
	ReasonLowConfidence Reason = "low_confidence"
	// ReasonShadow: the model was consulted but shadow mode kept the regex verdict.
	ReasonShadow Reason = "shadow"
	// ReasonError: the model call failed or returned no answer; regex stands.
	ReasonError Reason = "error"
)

// Resolve merges a regex verdict with a model answer. Rules, in order:
//
//  1. nil answer                     -> regex, ReasonError
//  2. confidence < minConfidence     -> regex, ReasonLowConfidence
//  3. model choice == regex verdict  -> regex, ReasonAgreed
//  4. shadow                         -> regex, ReasonShadow
//  5. otherwise                      -> model choice, ReasonOverrode
//
// In shadow mode the reason the live path would have produced is available
// from ResolveShadowReason.
func Resolve(regexVerdict string, a *Answer, minConfidence float64, shadow bool) (string, Reason) {
	if a == nil {
		return regexVerdict, ReasonError
	}
	if a.Confidence < minConfidence {
		return regexVerdict, ReasonLowConfidence
	}
	if a.Choice == regexVerdict {
		return regexVerdict, ReasonAgreed
	}
	if shadow {
		return regexVerdict, ReasonShadow
	}
	return a.Choice, ReasonOverrode
}

// ResolveShadowReason returns the reason Resolve would produce with shadow
// disabled. Shadow counters use it to count would-be overrides.
func ResolveShadowReason(regexVerdict string, a *Answer, minConfidence float64) Reason {
	_, reason := Resolve(regexVerdict, a, minConfidence, false)
	return reason
}
