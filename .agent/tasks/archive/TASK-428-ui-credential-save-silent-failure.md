# fix(connections): credential/connection save failures are silent — a 500 reads as success, no error surfaced

**Created**: 2026-07-30 · **Status**: ✅ Delivered (ui PR#37 merged) + **SOP real-stack verified 2026-07-31**: failed save renders inline error, input retained · **Last Updated**: 2026-07-31
**Repo**: `qf-studio/pilot-console-ui` · Drift-defect **#6** (real-stack-only class; mock adapter cannot reproduce by construction)

## Problem

On the real local stack, `PUT /api/v1/credentials/{key}` failed with **500**
(console has no AWS access — SSM write dies on the IMDS credential chain,
~6–10s timeout; see companion console issue). The operator saw the save
spinner finish and read it as success; navigating away and back showed
"no key added" because `GET /api/v1/credentials` truthfully returned
not-configured.

The UI never surfaced the failure:

- `saveCredential` (`src/views/ConnectionsView.vue`, ~line 104) is
  `try { await session.upsertCredential(...) } finally { ... }` — **no catch,
  no error state, nothing rendered on rejection**. The rejection escapes as an
  unhandled promise rejection.
- `onSave` for tracker connections (same file, ~line 84) has the identical
  hole around `session.upsertConnection(...)`.
- Contrast `src/views/LoginView.vue`, which catches, maps typed API errors to
  a message, and renders `loginError` inline — that is the house pattern.

The mock adapter never rejects, so no fixture-gated test ever sees this.

## Fix (mandated shape)

1. Catch in **both** save paths (`saveCredential`, `onSave`).
2. Render an inline error following the `LoginView` `loginError` pattern:
   message from the typed API error when available, generic
   "Something went wrong. Try again." otherwise. Clear the error when the user
   edits the field or retries.
3. On failure, **keep the typed secret in the input** (today the clear at
   `credentialSecrets[key] = ''` is correctly skipped on rejection — preserve
   that; do not blanket-clear in `finally`).
4. No unhandled promise rejection may escape either path.

## Tests

Regression tests that **fail against the current code**:

- Credential save: adapter mock rejects `upsertCredential` → error message
  visible, input retains its value, `savingCredential` cleared.
- Connection save: adapter mock rejects `upsertConnection` → error visible,
  form stays open (no `closeForm()` on failure).

## Verify (SOP)

Per `sops/quality/real-stack-verify-gates-ui-merges.md`: on the live local
stack (:5173, `VITE_API_MODE=http`), attempting a credential save today
reproduces the 500 — after this fix the error must render inline instead of
silently unspinning. Operator real-stack verify gates the merge.

## Refs

- Evidence: console request log 2026-07-30 18:28Z — `PUT
  /api/v1/credentials/{key}` → 500 ×2 (`orgs: write secret failed … no EC2
  IMDS role found`), followed by truthful `GET` 200 not-configured.
- Known backend limitation: `pilot-console/docker-compose.yml` (~lines
  137–151, GH-77 non-goals).
- Companion (independent, no dependency):
  [pilot-console#83](https://github.com/qf-studio/pilot-console/issues/83)
  env-gated local secrets driver (drift-defect #7) — makes the happy path
  locally verifiable; this issue's unit regressions stand regardless.
- Pilot issue: https://github.com/qf-studio/pilot-console-ui/issues/36
