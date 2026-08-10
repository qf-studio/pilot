package approval

// This file is the single normalization point for approval channel names in
// this package (GH-4772), and — as of TASK-459 Phase 4 task 2 — the single
// exported vocabulary for approval-source/channel names consumed outside
// this package too (internal/config's config validation, internal/health's
// registration check). Everything that compares a persisted/requested
// channel string against a registered Handler.Name() — Manager's dispatch
// lookup, each handler's Rehydrate, and the expiry sweep's channel scoping —
// goes through NormalizeChannelName/ownsChannel so the alias table and the
// legacy-row ownership rule are defined exactly once.

// DefaultChannelName is the approval channel that claims pending-approval
// rows persisted with an empty preferred_channel: rows written before
// PreferredChannel existed (pre-GH-4380), or by any future caller that
// leaves it unset. Exactly one handler must own these rows, or Rehydrate/the
// expiry sweep double-process (or silently strand) them once more than one
// channel handler is registered (GH-4772).
//
// Hardcoded to "telegram" rather than read from the autopilot-level
// `approval_source` default (autopilot.ApprovalSourceTelegram,
// internal/autopilot/types.go) because this package cannot import autopilot
// — autopilot already imports approval (e.g. controller.go's
// `PreferredChannel: string(c.config.EffectiveApprovalSource())`), so the
// reverse import would cycle. Telegram was the only approval channel before
// Slack existed (GH-4411) and before PreferredChannel existed (GH-4380), so
// every empty-preferred_channel row in a live database predates Slack
// entirely — Telegram is the correct legacy owner in practice, not just by
// convention.
const DefaultChannelName = "telegram"

// githubReviewAlias is the autopilot-level ApprovalSource config value for
// GitHub PR-review approvals (autopilot.ApprovalSourceGitHubReview =
// "github-review", internal/autopilot/types.go:36). This package's own
// handler for that channel is registered under Name() == "github"
// (GitHubHandler.Name, github.go:62-64) — the two names diverged when the
// GitHub handler was added without an alias, so a request configured with
// `approval_source: github-review` always hit Manager's preferred-channel
// hard error (GH-4380 semantics preserved: unregistered/unresolvable
// channels still fail loudly, never a silent fallback).
const githubReviewAlias = "github-review"

// knownChannelNames lists every channel name a Handler in this package can
// be registered under (i.e. every Handler.Name() value), used to scope the
// expiry sweep's orphan fallback (see ownedChannels / PruneExpired). The
// raw config alias is included too so a row persisted with the unnormalized
// "github-review" string is never mistaken for an orphan. Update this list
// when a new Handler type is added.
var knownChannelNames = []string{"telegram", "slack", "github", githubReviewAlias}

// ApprovalSourceValues is the canonical, exported vocabulary of accepted
// `approval_source` config values (TASK-459 Phase 4 task 2). This used to
// exist three times: this file's own knownChannelNames (a different,
// unexported vocabulary — see above), internal/config's
// validApprovalSourceValues, and internal/health's sourceRegistered switch.
// All three needed to accept exactly the same set or silently drift apart.
//
// "" is included because it is itself a valid approval_source config value
// (meaning "inherit the resolved env/global source") even though it is not
// a channel/handler name — that's why this isn't just knownChannelNames
// re-exported. "github" (the canonical Handler.Name()) is deliberately
// excluded: the only documented config-facing spelling for that handler is
// the "github-review" alias (githubReviewAlias), normalized to "github" by
// NormalizeChannelName at dispatch time. Update this map (and
// knownChannelNames above) together when a new Handler type is added.
var ApprovalSourceValues = map[string]bool{
	"":                true,
	"telegram":        true,
	"slack":           true,
	githubReviewAlias: true,
}

// NormalizeChannelName maps a channel name as it appears on a Request or a
// persisted PendingApproval row to the name its serving Handler is
// registered under. Every value other than the github-review alias passes
// through unchanged. Exported (TASK-459 Phase 4 task 2) so callers outside
// this package — e.g. internal/health's registration check — can normalize
// a raw approval_source value the same way Manager's own dispatch lookup
// does, instead of re-deriving the alias mapping themselves.
func NormalizeChannelName(name string) string {
	if name == githubReviewAlias {
		return "github"
	}
	return name
}

// ownsChannel reports whether the handler registered as handlerName should
// treat a row/request carrying preferredChannel as its own: either the
// (normalized) channel matches directly, or the row predates per-request
// channel routing (empty PreferredChannel) and handlerName is the default
// legacy claimant (GH-4772).
func ownsChannel(handlerName, preferredChannel string) bool {
	if preferredChannel == "" {
		return handlerName == DefaultChannelName
	}
	return NormalizeChannelName(preferredChannel) == handlerName
}

// ownedChannels returns the set of preferred_channel values a channel-scoped
// store sweep (PrunePendingApprovals) should delete for handlerName: its own
// name, plus "" (empty/legacy) when handlerName is the default claimant.
func ownedChannels(handlerName string) []string {
	if handlerName == DefaultChannelName {
		return []string{handlerName, ""}
	}
	return []string{handlerName}
}
