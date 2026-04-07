# TASK-11: Fix Documentation Inaccuracies from Unwired Features Audit

**Status**: ✅ Completed
**Created**: 2026-03-05
**Completed**: 2026-03-05

---

## What Was Built

Fixed 4 documentation inaccuracies found during the unwired features audit. This was the docs-only portion of a larger audit that also produced 4 code-fix issues (GH-2043–2046) and 1 backlog item (GH-2047).

The full audit discovered 7 features that were implemented but never wired into `main.go` or `pilot.go`. All code fixes and docs fixes are now merged.

---

## Implementation

### Code Fixes (Pilot-executed)

| Issue | Title | PR | Merged |
|-------|-------|----|--------|
| GH-2043 | Wire GitLab polling into poller registry | #2049 | 2026-03-05 |
| GH-2044 | Register Asana + Plane webhook handlers | #2048 | 2026-03-05 |
| GH-2045 | Add missing adapters to startup banner | #2050 | 2026-03-05 |
| GH-2046 | Remove dead `--daemon` flag | #2052 | 2026-03-05 |

### Docs Fixes (Pilot-executed)

| Change | File | PR |
|--------|------|----|
| Added notifier caveat (runner doesn't call notifiers yet) | `docs/content/concepts/adapters.mdx:192` | #2053 |
| Removed `--daemon` flag from flags table, example, Daemon Control section | `docs/content/cli/commands.mdx` | #2053 |
| Added `/webhooks/plane` to endpoint table | `docs/content/deployment/networking.mdx:36` | #2053 |
| Added `--gitlab` to CLI flags table | `docs/content/cli/commands.mdx` | #2053 |

### Backlog (not executed)

| Issue | Title | Reason |
|-------|-------|--------|
| GH-2047 | Notifier lifecycle wiring | Architectural decision needed — how runner dispatches to source-specific notifiers |

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Touch integration pages? | Fix now vs wait for code fixes | Wait | GH-2043–2046 fix code to match docs — no docs change needed |
| Remove daemon section entirely? | Remove vs stub | Remove | GH-2046 removes the flag from code; no daemon logic exists |
| Notifier caveat wording | Warning callout vs inline note | Inline note | Less alarming, matches doc tone |

---

## Files Modified

- `docs/content/concepts/adapters.mdx` (modified) — Notifier claim caveat
- `docs/content/cli/commands.mdx` (modified) — Removed --daemon, added --gitlab
- `docs/content/deployment/networking.mdx` (modified) — Added /webhooks/plane
- `cmd/pilot/poller_gitlab.go` (created) — GitLab poller registration
- `cmd/pilot/poller_registry.go` (modified) — Added gitlab to registry
- `internal/pilot/pilot.go` (modified) — Asana + Plane webhook handler registration
- `cmd/pilot/main.go` (modified) — Startup banner + daemon flag removal

---

## Done

- [x] `concepts/adapters.mdx` notifier claim has caveat
- [x] `cli/commands.mdx` has no `--daemon` references
- [x] `deployment/networking.mdx` lists `/webhooks/plane`
- [x] `cli/commands.mdx` flags table includes `--gitlab`
- [x] GitLab polling wired into poller registry
- [x] Asana + Plane webhook handlers registered
- [x] Startup banner shows all enabled adapters
- [x] Dead `--daemon` flag removed from code

---

**Completed**: 2026-03-05
**Implementation Time**: ~1 hour (audit + issue creation + Pilot execution)
