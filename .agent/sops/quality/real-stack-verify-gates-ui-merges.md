# Operator Real-Stack Verify Gates UI Merges

**Category**: quality
**Created**: 2026-07-30
**Status**: ADOPTED (founder nod 2026-07-30, after the 07-29/30 overnight drift-class scoreboard)

---

## The rule

A `pilot-console-ui` change — and any `pilot-console` change that alters API surface
(routes, response shapes, envelopes, auth/session semantics) — is **not DONE at PR
merge**. It is done when an operator has verified the affected flow **against the live
local stack** (`make local-up` + vite in `VITE_API_MODE=http`). The daemon's quality
gates cannot provide this: worktrees have no docker, so its "real-stack" ACs pass on
fixtures and the mock adapter resolves everything in-process.

Autopilot self-merge stays ON (velocity is the point). The gate is operational, not
mechanical: **verify promptly after merge, before the issue is treated as shipped**;
for changes touching auth/session or wire contracts, prefer verifying the PR branch
from a worktree *before* merge.

## Why (evidence — one night + one morning, 5 defects, 0 caught by daemon gates)

| Defect | Class | How caught |
|---|---|---|
| ui#19 | httpAdapter cast flat BFF JSON to mock-shaped `Session` → every login detoured to /onboarding | operator browser login |
| ui#22 | readiness checklist used mock requirement tokens, not the real contract | operator browser |
| ui#32 | list endpoints return envelopes (`{"connections":[]}`), adapter expected bare arrays → dashboard error state | operator browser + request log |
| console#81 | dashboard's third call had no read path — write-only PUT design, `GET /credentials` missing | operator curl (`GET (unmatched) 404` in console#79 request log) |
| ui#34 | router guard runs at `app.use(router)` install, before session bootstrap → every reload bounces to /login | operator browser reload |

Common thread: mock-mode fixtures encode the *assumed* contract; only the real BFF
exposes the actual one. The mock adapter can never surface wire drift or
timing/lifecycle races (it resolves `getMe` synchronously — ui#34 is invisible by
construction).

## Procedure

### 1. Stack up (skip if already running)

```bash
cd ~/Projects/startups/pilot-console && make local-up   # 4 containers healthy
make local-seed                                          # idempotent; demo@pilot.local
cd ../pilot-console-ui && VITE_API_MODE=http bun dev     # :5173, http mode — NOT mock
```

Login: `demo@pilot.local` / `PilotDemoPass2026!`

### 2. Verify — always-run baseline (~2 min)

- [ ] Login → lands on dashboard (no /onboarding detour)
- [ ] **Hard reload on an authenticated page** → stays on that page (ui#34 class)
- [ ] Dashboard renders real data (org card, connections strip, instances) — no error states
- [ ] Browser console: no uncaught errors; network tab: no 4xx on page-load calls

### 3. Verify — per-surface, for whatever the PR touched

- **New/changed endpoint**: curl it through the BFF with the session cookie +
  `X-Requested-With: pilot-console` header. Check the *shape* (envelope vs bare array,
  field names) against what the adapter parses — shape drift is the #1 class.
- **Auth/session**: login, reload, deep-link reload (`/instances/<id>`), logout,
  post-logout redirect, expired-session behavior.
- **Forms/CRUD**: submit real data, verify persistence across reload, verify error
  path (submit invalid → visible error, not silent swallow).
- **Console request log** (`console#79`, on in local stack): tail it while clicking —
  `(unmatched) 404` lines are missing read paths (console#81 class).

### 4. On defect

File to the owning repo with the `pilot` label, same day. Body must include the wire
evidence (actual JSON vs expected, or the exact repro sequence) and an AC that the
regression test fails against current code. Pattern examples: ui#19, ui#34.

### 5. Record

Note "real-stack verified" (or the found defect) on the issue/PR or the day's context
marker. An unverified merge is an open item, not a shipped one.

## Limits / exit condition

This SOP is compensating for a tooling gap, not a permanent ceremony. It retires (or
shrinks to auth-surface-only) when the daemon's gates can run the compose stack —
i.e., a worktree-compatible dockerized e2e leg that executes the §2 baseline
headlessly. Until then, no UI-surface issue closes on green fixtures alone.

## Related

- Roadmap: `.agent/system/saas-roadmap.md` v9.5 (drift-class scoreboard)
- Night log: `.agent/.context-markers/lead-watch-2026-07-29.md`
- Local stack: pilot-console `make local-up` / `local-down` / `local-nuke`
