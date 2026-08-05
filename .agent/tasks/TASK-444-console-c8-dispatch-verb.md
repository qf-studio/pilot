# feat(boardapi): C8 — dispatch verb: create-in-tracker for unlinked cards + execution join endpoint

## Context

Blocked by: console#96 (C6 rate-budgeter leg — serialized to avoid `main.go`/`internal/boardapi` collisions).

S4 board track, leg C8. Verified at `pilot-console` HEAD `f1658e3`: C7 already ships **half** of dispatch — `POST /api/v1/board/cards/{id}/status` moving a LINKED card to `queued` additively enqueues the `pilot` trigger-label op through `guardPilotLabels`/`addTriggerLabel` (`internal/boardapi/handlers.go:561-601`), and the tenant daemon's own poller picks the label up out-of-band (tracker-as-message-bus — there is deliberately NO console→daemon write path). An UNLINKED card dragged to `queued` gets **412 `{"code":"card_unlinked"}`** and no mutation (`handlers.go:561-567`).

This leg closes the two gaps (design §6):

1. **Create-in-tracker**: "dispatch requires a link. Dragging an unlinked card to Queued prompts 'create this issue in ⟨tracker⟩ first?' — one click runs `CreateIssue` on the chosen home tracker, links the card, then applies the label."
2. **Execution join**: "the dashboard joins card → `sequence_id` → instance-proxy `/api/v1/{status,history,logs}` at render time." The proxy exists and is GET-only (`internal/proxy/proxy.go:32` `GET /api/v1/instances/{instanceID}/pilot/{tail...}`, allowlist includes `status|queue|history|tasks|logs|metrics`); what's missing is the endpoint telling the UI WHICH instance to join against.

Do NOT read or clone sibling repos — everything needed is embedded.

### What you build on (all merged, exact)

- `syncoutbound.WriterFactory` (`internal/syncoutbound/writer.go:39`): `func(ctx, orgs.Connection) (SyncWriter, string /*projectID*/, error)`; built by `NewWriterFactory(credentials secrets.CredentialReader)` (`:64`). Its `projectID` return is currently **discarded** at the sole call site (`worker.go:275`) — kept "for a future CreateIssue caller" (`writer.go:33-38`). **This issue is that caller.** `syncoutbound.SyncWriter` (`writer.go:27`) = `core.SyncWriter` + `GetIssue`.
- `core.SyncWriter.CreateIssue(ctx, projectID string, draft IssueDraft) (IssueSnapshot, error)`; `IssueDraft{Title, Body string; Labels []string; Priority string}`. Provider facts (studio-sdk v0.31.2): **`draft.Priority` is silently dropped by GitHub and Jira**; Jira hardcodes issue type `"Task"`; Linear resolves the team, `GetOrCreateLabel`s each draft label. `IssueSnapshot` gives back `NativeID`, `SequenceID`, `URL`, `UpdatedAt`.
- Board store (via the `boardapi.BoardStore` interface, `routes.go:31-47`): extend it with `LinkCard(ctx, p board.LinkCardParams) (board.CardLink, error)` and `PutShadow(ctx, cardID uuid.UUID, snapshot json.RawMessage) error` (both exist on `*board.Store`: `cards.go:290`, `:439`). `LinkCardParams{CardID, ConnectionID, Provider, NativeIssueID, SequenceID, NativeURL, ProviderUpdatedAt}`. `LinkCard` errors: `ErrAlreadyLinked` (card has a link), `ErrDuplicateLink` (native already linked on this connection).
- Shadow shape: `syncengine.Snapshot{Title, Body, Status, ProviderState, Priority string; Labels []string}` marshaled as JSON — see `syncoutbound/worker.go:360` `buildShadowSnapshot` for the idiom. Seeding the shadow from the post-create snapshot is what makes the next ingest pass diff clean instead of re-adopting (R8/echo-suppression — same reason `ingestNewCard` seeds via `Base == nil`).
- Connection lookup: `orgs.Store.GetConnection(ctx, orgID, tracker)` (`internal/orgs/store.go:321`). C5 (chained earlier) already extends boardapi's `OrgStore` with it — reuse.
- Instances: `internal/instances` `FleetStore.ListInstancesByOrg` (used by `GET /api/v1/instances`, `handlers.go:172`); instance DTO carries `{id, region, status, specVersion, driftFromSpec}`.
- House rules: 404-never-403 · camelCase DTOs · `{"error":...}` envelope · `bff.CSRFGuard` on mutations · version-conflict 409 carrying the current card (`handleUpdateCardError`, `handlers.go:429`) · idem-key helper `computeIdemKey` (`idemkey.go:29`).

## Acceptance

1. **`POST /api/v1/board/cards/{id}/dispatch`** — body `{"tracker": "github|linear|jira" (optional), "version": N}`. The one-click verb:
   - **Linked card**: ignore `tracker`; behave exactly like `POST .../status` with `{"status":"queued"}` — same version check, same status op + additive-pilot-label op enqueueing, same 409 shape. Share the internals with `handleStatus` (extract a helper; do not duplicate the guarded label logic).
   - **Unlinked card**: `tracker` required (missing → 400 `{"code":"tracker_required"}`; org has no such connection → 404). Flow, in this order:
     a. `UpdateCardFields` status→`queued` with the caller's `version` (optimistic check FIRST — a concurrent edit 409s before any provider write; this also makes double-clicks race-safe: the second click fails the version check).
     b. Build the writer via the injected `WriterFactory`; `CreateIssue(projectID, IssueDraft{Title, Body, Labels: card.Labels, Priority: string(card.Priority)})` — do NOT add the `pilot` label to the draft; the label arrives via the guarded label op (step d), keeping one dispatch path and one crash-repair story.
     c. `LinkCard` from the returned snapshot (`NativeID`, `SequenceID`, `URL`, `ProviderUpdatedAt: &snap.UpdatedAt`), then seed the shadow: marshal a `syncengine.Snapshot` from the snapshot (Status: `queued`'s provider mapping is NOT yet true remotely — use the snapshot's `State` as `ProviderState` and map via the status map as ingest does; keep it faithful to what the tracker actually holds) and `PutShadow`.
     d. Enqueue the `status` op and the additive `pilot` `labels` op against the fresh card version (re-`GetCard` after the update — `EnqueueOp` idem keys bind to version).
   - **Crash repair is a re-drag**: if the process dies between (a) and (c), the card is `queued`-and-unlinked with no tracker issue — a state the normal status route 412s on. `dispatch` on a queued-unlinked card must therefore be legal and simply resume at (b). State this in the handler doc.
   - `CreateIssue` fails → 502 `{"error":"tracker create failed","code":"tracker_create_failed"}`, card stays queued-unlinked (repairable). `ErrDuplicateLink` from (c) → treat as repair-in-progress: re-fetch the existing link's card — if it is THIS card proceed to (d), else 409.

2. **Writer wiring**: `boardapi.Deps` gains `Writers syncoutbound.WriterFactory` (nil-legal). `main.go`'s `registerBoard` builds it with the same `secrets.CredentialReader` dance as `startSyncOutbound` (`main.go:352-371`) **when `cfg.Secrets.Driver` supports read-back**; when `Writers == nil`, dispatch of UNLINKED cards responds 503 `{"code":"dispatch_unavailable"}` (linked-card dispatch still works — it needs no writer). The bare binary must still run with no env set.

3. **`GET /api/v1/board/cards/{id}/execution`** — the join endpoint. Response:
   ```json
   {"linked": true, "provider": "github", "sequenceId": "#42", "nativeUrl": "...", "instanceId": "i-...", "instanceStatus": "running"}
   ```
   `linked:false` (rest omitted) for unlinked cards — 200, not 404 (the card exists; it just has no execution surface). Instance resolution: `boardapi.Deps` gains a narrow `Instances` interface with `ListInstancesByOrg` (implemented by the fleet store; nil-legal → `instanceId` omitted); pick the org's single active instance (bind-once model — if >1 non-terminated, pick newest and log a warning). The UI composes this with the existing proxy routes (`/api/v1/instances/{instanceId}/pilot/status|history|logs`) — **no new proxy tail, no console→daemon write path, ever** (tracker is the message bus).

4. **Label hygiene unchanged and re-asserted**: dispatch reuses the existing `guardPilotLabels` path; add a test that a dispatch-created card's label op never strips a `pilot-*` label (belt-and-suspenders — the invariant is the poller's re-arm protection).

5. **Tests** (house pattern: `fakeBoardStore`/`fakeOrgStore`/`newTestMux`, plus a `fakeWriterFactory` returning a scripted `SyncWriter`): linked dispatch == status-queued semantics incl. 409 · unlinked without tracker → 400 · unknown tracker connection → 404 · happy path calls CreateIssue once, links, seeds shadow, enqueues status+label ops (assert order: version-checked update precedes CreateIssue) · version conflict blocks before CreateIssue (no provider call — the double-click guarantee) · CreateIssue failure → 502, card queued-unlinked, ops absent · re-dispatch of queued-unlinked resumes without a second status write · duplicate-link repair path · Writers nil → 503 unlinked, linked still works · execution endpoint: linked+instance, linked+no-instance, unlinked `linked:false`, cross-org 404 · CSRF/auth.

6. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/boardapi/dispatch.go` + `dispatch_test.go`, `internal/boardapi/handlers.go` (extract shared queued-flow helper), `routes.go` (2 routes + Deps), `dto.go`, `main.go` (`registerBoard` writer/instances wiring).

Sequencing: extract queued-flow helper (behavior-preserving, existing tests stay green) → execution endpoint (read-only, small) → dispatch verb + fakes → main.go wiring.

**Verify-before-relying**: (a) what `handleStatus` does between version check and op enqueue at HEAD — the helper extraction must preserve it exactly; (b) the fleet store's instance-listing method name/signature used by `internal/instances` (`handlers.go:30-35`) before declaring the narrow interface; (c) `syncengine.Snapshot` field semantics for the shadow seed (`syncoutbound/worker.go:360` idiom).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** approval mirror / "Needs You" / decision endpoint (C14) · WebSocket or live-log proxying · any new proxy tail or console→daemon write · per-card timeline from `execution_events` (later leg; the join endpoint is its foundation) · create-in-tracker WITHOUT dispatch (plain link verb — defer until a user asks) · Jira issue-type/priority configurability (SDK drops them; journal, don't fix).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/97 (labels: pilot, no-decompose; gated on #96)
- Depends on: console#87–#94 (merged) · console#95 (C5) + console#96 (C6) chained ahead
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §6 (Pilot as just-another-writer, dispatch-requires-a-link, execution enrichment via proxy) — embedded above
- Console facts verified 2026-08-05 at `f1658e3`; SDK facts at `studio-sdk` `acee519` / v0.31.2 (CreateIssue quirks: priority dropped on GitHub/Jira, Jira type hardcoded `Task`)
