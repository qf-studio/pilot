# feat(statusmap): per-tracker status-map editor on the merged C5 CRUD API (adapter + mock + ConnectionsView panel)

## Context

Blocked by: ui#44 (httpAdapter un-stub leg — both issues touch `types.ts`/`httpAdapter.ts`/`mockAdapter.ts`).

S4 board track, UI lane — the "wizard UI" half of console C5, whose API **merged today** (`pilot-console` PR#99, HEAD `b98873e`). The console auto-seeds `status_maps` (statically for GitHub, from observed tracker states for Linear/Jira during ingest) and exposes CRUD; this leg gives the customer the editor the design doc promises ("Customer can re-map any row in the dashboard (e.g. point `in_review` at Jira's 'Code Review' status)").

Do NOT read or clone sibling repos — the full wire contract is embedded verbatim below from `pilot-console internal/boardapi/{statusmap.go,dto.go}` at `b98873e`.

### The wire contract (verbatim from the merged console source)

```go
type statusMapRowDTO struct {
	CanonicalStatus string `json:"canonicalStatus"`  // backlog|todo|queued|in_progress|in_review|done|canceled
	ProviderState   string `json:"providerState"`    // provider-native state name, case preserved
	IsPrimary       bool   `json:"isPrimary"`        // reverse-map winner when N canonical → 1 provider state
}
```

| Verb + path | Success | Errors |
|---|---|---|
| `GET /api/v1/board/statusmaps/{tracker}` | 200 `{"tracker","connectionId","rows":[statusMapRowDTO]}` — rows `[]` not null; at most one row per canonicalStatus (PK) | 404: unknown tracker string OR tracker never connected (house rule: never 400/403) |
| `PUT /api/v1/board/statusmaps/{tracker}` body `{"rows":[statusMapRowDTO]}` | 200 full updated map (same envelope) — **full replace**, not merge | 400 `{"error":"invalid canonical status or empty provider state"}` · 409 `{"error":"duplicate primary for provider state"}` · 404 as above |
| `DELETE /api/v1/board/statusmaps/{tracker}/{canonicalStatus}` | 204 | 404 (unknown row / tracker) |
| `POST /api/v1/board/statusmaps/{tracker}/seed` | 200 updated map (GitHub only: insert-if-absent static 7-row taxonomy, never overwrites edits) | 409 `{"error":"seeding for this tracker is observation-based","code":"seed_observed_only"}` for linear/jira · 404 as above |

Mutations need the CSRF header (`httpClient.request` sets it). Semantics that shape the UI:

- The map is **forward-keyed**: ≤1 providerState per canonical status. Multiple canonicals may share one providerState (GitHub: 5 canonicals → `open`); `isPrimary` picks which canonical an *incoming* provider state resolves to — at most one primary per distinct providerState value (DB partial unique index; violating it via PUT → the 409).
- Linear/Jira rows appear over time as ingest observes real tracker states; an empty or partial map is a normal state, not an error.
- An unmapped state never moves a card (both directions guard) — so an incomplete map degrades safely; the editor's job is coverage + correction, not gating.

## Acceptance

1. **Domain types + adapter contract** (`src/lib/api/types.ts`): `StatusMapRow { canonicalStatus: BoardCardStatus; providerState: string; isPrimary: boolean }` and `StatusMap { tracker: Tracker; rows: StatusMapRow[] }`. Four new `ConsoleAdapter` methods — the single-interface seam forces both implementations:
   ```ts
   getStatusMap(tracker: Tracker): Promise<StatusMap>
   replaceStatusMap(tracker: Tracker, rows: StatusMapRow[]): Promise<StatusMap>
   deleteStatusMapRow(tracker: Tracker, canonicalStatus: BoardCardStatus): Promise<void>
   seedStatusMap(tracker: Tracker): Promise<StatusMap>   // GitHub only; others reject ApiError(409)
   ```

2. **httpAdapter**: `WireStatusMapRow` + `WireStatusMapEnvelope{tracker, connectionId, rows}` with hand mapping per the root-cause guard (drop `connectionId` — no domain use). Error passthrough is plain `ApiError` (400/404/409) — no new typed error class; the editor surfaces `err.message` inline per the ConnectionsView convention.

3. **mockAdapter** (house conventions: `seedStatusMaps` const → mutable copy, `await delay()`, immutable replace, validation mirroring the server): seed GitHub with the real static 7-row taxonomy (`open` ×5 with todo primary, `closed` ×2 with done primary), Linear with a partial observed map (e.g. 4 rows: `Todo`/`In Progress`/`Done`/`Backlog`, todo/in_progress/done/backlog primaries) so the wizard's fill-the-gaps state is exercisable, Jira with an empty map. Mock `replaceStatusMap` validates like the server: invalid canonical or empty providerState → `ApiError(400, 'invalid canonical status or empty provider state')`; two primary rows sharing one providerState → `ApiError(409, 'duplicate primary for provider state')`. Mock `seedStatusMap`: GitHub inserts-if-absent; linear/jira → `ApiError(409, 'seeding for this tracker is observation-based')`. Unconnected tracker (per the seeded connection states: linear `unconfigured`) → `ApiError(404, 'not found')`.

4. **Store** (`src/stores/statusmap.ts`, setup style): per-tracker cache `Record<Tracker, StatusMap | null>`, `loadStatusMap(tracker)` (load-once) / `refreshStatusMap(tracker)`, `saveStatusMap(tracker, rows)` → adapter replace, splice result into cache, rethrow on error (store never swallows — house contract). `seed(tracker)` likewise.

5. **Editor UI — a "Status map" panel per tracker row in `ConnectionsView.vue`** (the research-confirmed natural home: per-tracker config lives there and connection status is known; no new route, no nav change). Follow the existing inline-expanding-form pattern exactly (`activeTracker`-style state, `border-hairline bg-surface` panel, `BaseButton` ghost trigger — visible only when the connection's status is `connected`):
   - **7 fixed rows in `BOARD_STATUSES` order** with `STATUS_LABELS` labels. Per row: a `TextInput` for `providerState` (empty = unmapped — rendered as the muted placeholder "unmapped"; deleting the text deletes the row on save) and the primary control (below).
   - **Primary is derived, not free-form**: group rows by identical non-empty `providerState`; each group of ≥2 renders one radio group choosing the primary canonical ("Incoming ⟨state⟩ moves the card to: ○ To Do ○ Queued"); a group of exactly 1 is silently primary (checkbox hidden — matches seeder behavior and makes the 409 unconstructible from the UI). This turns the API's invariant into UI structure instead of an error message.
   - **Save** = diff against loaded rows → `replaceStatusMap` with the full non-empty row set (PUT is full-replace; rows cleared in the form are simply omitted). Buttons/`:loading`/inline `role="alert"` error handling per the `onSave` idiom; 409/400 render `err.message`.
   - **Seed button, GitHub row only**: "Restore defaults" → `seedStatusMap('github')` → refresh. Linear/Jira instead show the static hint "Rows appear automatically as issue states are observed from ⟨tracker⟩."
   - Empty-map state (Jira): the 7 rows all unmapped + the hint — not an error screen.

6. **Fixtures**: `statusmap-github.json`, `statusmap-empty.json` under `src/lib/api/__tests__/fixtures/`, derived field-for-field from the DTO above (README note: source-derived, upgraded by the operator's real-stack verify).

7. **Tests**: adapter — URL/verb/CSRF per method, envelope unwrap, 404/400/409 → `ApiError` with server message, seed 409 passthrough. Mock — validation parity (400/409/404 paths), seed insert-if-absent never overwrites an edited row. Store — load-once vs refresh, save splices, rethrow. Component (`ConnectionsView` pattern: `mount` + `flushPromises` + `vi.spyOn(mockAdapter, ...)`) — panel renders 7 rows in column order · shared-providerState group renders one primary radio group and save produces exactly one `isPrimary` per group · clearing a row omits it from the PUT · save error renders inline `role="alert"` · seed button only on GitHub · panel hidden for non-connected trackers.

8. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`, `src/stores/statusmap.ts` (+test), `src/views/ConnectionsView.vue` (+test), fixtures (+README note), adapter tests.

Sequencing: types + adapter contract → httpAdapter + fixtures → mock with validation → store → panel UI (rows → primary grouping → save/seed) → tests throughout.

**Verify-before-relying**: the un-stub leg (chained ahead) may have touched `httpAdapter.ts` imports/`Wire*` conventions and `TRACKER_LABELS` reuse — build on its merged state, not on this spec's assumptions about file layout.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** a multi-step modal wizard (the inline panel IS the v1 wizard; `ModalDialog` stays out of this) · a standalone route or nav entry · onboarding-flow integration · editing `connectionId`/tracker identity · any board-screen change · `useForm` extension (string-only constraint — use plain `reactive` per the ConnectionsView precedent).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot-console-ui/issues/45 (labels: pilot, no-decompose; gated on ui#44)
- Console wire facts verified 2026-08-05 at `pilot-console` `b98873e` (C5 merged PR#99 same day); UI facts at `pilot-console-ui` `6c04455`
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §5 (status mapping, re-map UX quote, lossiness rules) — embedded above
- Console-side counterpart: `qf-studio/pilot` `.agent/tasks/archive/TASK-442-console-c5-statusmap.md`
