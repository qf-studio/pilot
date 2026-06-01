# TASK-347: CreatePilotIssue repo allowlist fails open on nil (C7)

## Context

`validateIssueRepo` (`internal/adapters/github/issue_create.go:62`) is the defense-in-depth twin of
`executor.ValidateTargetRepo` for "any direct caller of the GitHub adapter ... future paths". But the
two guardrails have OPPOSITE defaults for a missing allowlist: `executor.ValidateTargetRepo` treats
`allow == nil` as **REFUSE** ("makes the safe default loud rather than silent"), while
`validateIssueRepo` treats `allow == nil` as **ALLOW** with only a `slog.Warn` (`issue_create.go:63-70`).
The two production callers (`autopilot/feedback_loop.go:102,270`) intentionally pass nil with explicit
config-derived owner/repo, so today's risk is low — but the stated purpose is protecting FUTURE callers,
and one that forgets to wire an allowlist gets zero enforcement (the GH-3027 cross-repo-leak class this
guardrail was added to prevent). The `PILOT_ALLOW_UNMANAGED_REPO` bypass is checked only AFTER the
allowlist (`:76`), so the nil short-circuit makes the env irrelevant.

## Approach

Make `validateIssueRepo` fail-closed on `allow == nil` to match the executor guardrail: return an error
unless `PILOT_ALLOW_UNMANAGED_REPO=1` is set. For the two known-safe autopilot callers, pass a real
allowlist (or an explicit `AllowAll` sentinel) rather than nil so the intent is encoded at the call site
instead of relying on a permissive default. Keep the existing bypass-env check, but evaluate it before
the nil refusal so the escape hatch still works.

## Acceptance

- [ ] `validateIssueRepo(nil, ...)` returns an error unless `PILOT_ALLOW_UNMANAGED_REPO=1`.
- [ ] The two `feedback_loop.go` callers pass a non-nil allowlist (or `AllowAll` sentinel), not nil.
- [ ] Test: nil allowlist + no env → error; nil allowlist + `PILOT_ALLOW_UNMANAGED_REPO=1` → allowed; populated allowlist with disallowed repo → error.
- [ ] Behavior now matches `executor.ValidateTargetRepo` (fail-closed default).
- [ ] `make test` green for `internal/adapters/github`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (C7, medium, security)
- Twin guardrail: `internal/executor/repo_guardrail.go:69-99`; SOP `sops/integrations/repo-allowlist-guardrail.md`
- File: `internal/adapters/github/issue_create.go:62-88`; callers `internal/autopilot/feedback_loop.go:102,270`
