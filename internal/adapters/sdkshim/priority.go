package sdkshim

// SDK priority constants (mirrors sdk/core.NormalizePriority's output vocab).
const (
	SDKPriorityUrgent = "urgent"
	SDKPriorityHigh   = "high"
	SDKPriorityMedium = "medium"
	SDKPriorityLow    = "low"
	SDKPriorityNone   = "none"
)

// Pilot priority enum values. Mirrors the int constants used by the in-tree
// adapter Priority types (PriorityNone=0, PriorityUrgent=1, ...).
const (
	PilotPriorityNone   = 0
	PilotPriorityUrgent = 1
	PilotPriorityHigh   = 2
	PilotPriorityMedium = 3
	PilotPriorityLow    = 4
)

// PriorityFromSDK converts a normalized SDK priority string (as set on
// core.IssueEvent.Priority by sdk/core.NormalizePriority) into Pilot's int
// priority enum used by orchestrator.TicketData.
//
// Unknown / empty / "none" → PilotPriorityNone (0). Callers should not need
// to special-case the empty string; SDK adapters that have no priority concept
// emit "" and Pilot reads it as None, matching today's behavior.
func PriorityFromSDK(p string) int {
	switch p {
	case SDKPriorityUrgent:
		return PilotPriorityUrgent
	case SDKPriorityHigh:
		return PilotPriorityHigh
	case SDKPriorityMedium:
		return PilotPriorityMedium
	case SDKPriorityLow:
		return PilotPriorityLow
	default:
		// "", "none", and any unknown value: be permissive.
		return PilotPriorityNone
	}
}

// PriorityToSDK is the reverse of PriorityFromSDK, useful for tests and any
// future write-back path. Out-of-range int → "none".
func PriorityToSDK(p int) string {
	switch p {
	case PilotPriorityUrgent:
		return SDKPriorityUrgent
	case PilotPriorityHigh:
		return SDKPriorityHigh
	case PilotPriorityMedium:
		return SDKPriorityMedium
	case PilotPriorityLow:
		return SDKPriorityLow
	default:
		return SDKPriorityNone
	}
}
