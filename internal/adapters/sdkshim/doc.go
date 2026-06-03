// Package sdkshim is the bridge between studio-sdk normalized contracts and
// Pilot's host-domain types. It exists because the SDK's normalized
// core.IssueEvent / core.MessageEvent intentionally omit host concerns
// (clone URLs, Pilot's int Priority enum, comms.IncomingMessage's extra
// Telegram-specific fields) — those gaps live here, in one place, on
// purpose.
//
// Scope:
//   - priority.go       SDK string priority → Pilot int priority.
//   - repo_resolver.go  (SourceAdapter, ev.ProjectID) → CloneURL/RepoOwner via cfg.
//   - chat.go           core.MessageEvent → comms.IncomingMessage shim.
//   - outbound.go       comms.Messenger string refs ↔ core.MessageRef structs.
//
// Out of scope:
//   - PRCreator / SubIssueCreator wrappers: the SDK clients implement these
//     interfaces by signature (gitlab.Client.CreatePR, plane.Client.CreateIssue);
//     inject the SDK client directly with runner.SetPRCreator / SetSubIssueCreator.
//   - internal/adapters/github: stays in tree for autopilot's PR/CI/release/branch
//     surface (M7 retires only its issue-poller role).
//
// Phase: scaffolded in Phase 0 of M7 (2026-06-03). See .agent/research/2026-06-03-m7-integration-plan.md.
package sdkshim
