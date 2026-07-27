# TASK-426: Fix 4 lint findings blocking the local pre-push gate

**Created**: 2026-07-27 · **Status**: ✅ Shipped 2026-07-27 (#4575 → PR #4578, merged 13:48Z; released v2.247.0 — --no-verify habit retired) · **Last Updated**: 2026-07-27

## Problem

`scripts/pre-push-gate.sh` (installed as the pre-push hook by
`make install-hooks`) fails on a clean main checkout: local golangci-lint
reports 4 findings that CI's pinned `v2.8.0` (ci.yml:92) does not. The gate
is supposed to pass on a clean tree; today every push needs `--no-verify`.

Findings:

1. `internal/autopilot/scope_schedule_test.go:96` — errcheck: return value
   of `c.scheduleReleaseTick(context.Background(), scheduledAt)` unchecked.
2. `internal/autopilot/scope_schedule_test.go:129` — same.
3. `internal/autopilot/scope_schedule_test.go:149` — same.
4. `internal/dashboard/grom_chrome.go:50` — unused: `func renderPanel` has
   no callers (all render paths call `renderPanelStyled` directly, see
   `internal/dashboard/zoom.go`).

## Required behavior

1. For each `scheduleReleaseTick` test call site: handle the returned error
   according to that test's intent — if the test expects success, fail the
   test on non-nil error (`if err := …; err != nil { t.Fatalf(…) }`); if it
   deliberately exercises an error path, assert on the error instead. Do
   not blanket `_ =` without checking intent.
2. Delete the unused `renderPanel` wrapper. If that leaves
   `renderPanelInfo` (grom_chrome.go:55) without callers, delete it too —
   remove the whole dead chain, verified by a clean `golangci-lint run`.
3. No behavior changes; test assertions may only get stricter.

## Acceptance criteria

- [ ] `make lint` green locally AND `golangci-lint run` reports 0 issues in
      `internal/autopilot/scope_schedule_test.go` and
      `internal/dashboard/grom_chrome.go`.
- [ ] `go test ./internal/autopilot/... ./internal/dashboard/...` green.
- [ ] CI lint (v2.8.0) stays green.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4575
- Gate: `scripts/pre-push-gate.sh`, `scripts/install-hooks.sh`
- CI pin: `.github/workflows/ci.yml:92` (`golangci-lint v2.8.0`)
- Context: TASK-425 (#4574) made the pre-push gate load-bearing for graph
  drift; a gate that cries wolf on a clean tree trains `--no-verify` habits.
