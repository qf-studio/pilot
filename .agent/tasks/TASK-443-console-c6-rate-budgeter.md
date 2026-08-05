# feat(syncthrottle): C6 — per-connection rate budgeter: read/write token buckets, write preemption, adaptive cooldown

## Context

Blocked by: console#95 (C5 statusmap leg — serialized to avoid `internal/syncingest` collisions).

S4 board track, leg C6 ("before tenant #20"). Verified at `pilot-console` HEAD `f1658e3`: **no rate limiting of any kind exists**. Grep for `budget|throttl|limiter|ratelimit` hits only three doc comments naming C6 as future work (`syncingest/worker.go:17`, `syncoutbound/worker.go:16`, `boardapi/routes.go:6`) plus typed-error references in `syncoutbound/execute.go`.

Current behavior this leg must change:

- **Ingest** (`internal/syncingest/worker.go`): fixed `time.NewTicker(PollInterval)` created once in `Run`, never reset; per tick it fans out one goroutine per connection (unbounded, `dispatch` at `:203`); ANY `ListUpdatedSince` error → log + abandon pass (`:274-278`); no typed-error inspection, no connection backoff. A 429 storm just retries at full cadence every 60s.
- **Outbound** (`internal/syncoutbound/worker.go`): `classifyFailure` (`execute.go:370`) extracts `RetryAfter` from typed errors, but the result only reschedules the ONE failing op. Nothing pauses the connection; sibling ops in the same tick still fire; rate-limit events are never counted anywhere.
- No client exposes remaining-quota headers (verified in `studio-sdk` v0.31.2 — `X-RateLimit-Remaining` is read once internally in GitHub's 403 branch and discarded). **The only signals available are: our own request rate, and typed `RateLimitError`s after the fact.** The budgeter is therefore local pacing + reactive cooldown, not quota tracking.

Do NOT read or clone sibling repos — everything needed is embedded.

### Typed errors you classify on (all in the module cache today)

`github.RateLimitError{StatusCode int; RetryAfter time.Duration; Message string}` · `jira.RateLimitError{RetryAfter time.Duration; Message string}` · `retry.RateLimitError{RetryAfter time.Duration; Message string}` (package `github.com/qf-studio/studio-sdk/sdk/util/retry`). **Linear produces NO typed errors** — both workers wrap Linear in `retry.WithRetry` with a conservative classifier (`syncingest/connector.go:155`, `syncoutbound/writer.go:159`), so a Linear 429 surfaces as an untyped string error after retries. Mirror the `isLikelyLinearAuthError` precedent (`syncoutbound/execute.go:410`) with an `isLikelyRateLimit` substring check (`"429"`, `"rate limit"`, `"ratelimited"`, case-insensitive) used ONLY for cooldown signaling, never for parking.

### Worker integration points (exact)

- Ingest page loop: `worker.go:273` `source.ListUpdatedSince(...)` inside `deltaPoll`; sweep loop: `worker.go:565` `source.ListAll(...)` inside `collectAllNatives`.
- Outbound per-op: `processOp` (`worker.go:263`) marks inflight (`:264`) BEFORE any network call, then executes; failure path `failOp` (`worker.go:398`).
- Both workers are built in `main.go` (`startSyncIngest` `:310`, `startSyncOutbound` `:352`) from the same `cfg.Sync`; their consumer-side interfaces are unexported per package.

## Acceptance

1. **Package `internal/syncthrottle`** (flat). One concrete type shared by both workers:

   ```go
   func New(cfg Config, now func() time.Time) *Budgeter   // now == nil ⇒ time.Now
   func (b *Budgeter) AllowRead(connectionID uuid.UUID) bool
   func (b *Budgeter) AllowWrite(connectionID uuid.UUID) bool
   func (b *Budgeter) ReportRateLimit(connectionID uuid.UUID, retryAfter time.Duration)
   func (b *Budgeter) ReportSuccess(connectionID uuid.UUID)
   ```

   Mutex-guarded lazy per-connection state (buckets refill by elapsed clock — no background goroutine). A nil `*Budgeter` must be safe everywhere and mean "unlimited" (both workers treat it so), preserving the repo invariant that a bare binary runs unchanged.

2. **Token buckets, 70/30 read/write with write preemption**: per connection, a budget of `RequestsPerMinute` tokens (default 60) split into a read bucket (70%) and a write bucket (30%), each refilling continuously at its share. `AllowWrite` draws from the write bucket first and **may borrow from the read bucket** when the write bucket is dry; `AllowRead` draws only from the read bucket. That asymmetry IS the preemption: a customer's drag is latency-sensitive, a poll is not (design §7). Burst capacity = one full share (no multi-minute accumulation).

3. **Adaptive cooldown**: `ReportRateLimit` opens a per-connection cooldown of `max(retryAfter, base·2^(consecutive−1))` — base 30s, capped at `CooldownMax` (default 10m). During cooldown `AllowRead` is always false; `AllowWrite` is false only until the provider's `retryAfter` has elapsed, then true (writes resume first — preemption again). `ReportSuccess` resets the consecutive counter and ends any cooldown. Log state transitions once per transition (`syncthrottle: connection cooling`, `...resumed`), not per denied call.

4. **Ingest integration** (`internal/syncingest`): before EVERY provider page call (`deltaPoll` loop and sweep loop), consult `AllowRead`; denied → abandon the pass exactly like the existing error path (log with a distinct message, watermark untouched, sweep abandoned wholesale — partial sweeps must not mark orphans, which the existing code already guarantees). In the error branches of both loops, classify: typed rate-limit (`errors.As` the three types) or Linear-heuristic → `ReportRateLimit(connID, retryAfter)`; on a fully-committed page → `ReportSuccess`. The worker gains a `budget *syncthrottle.Budgeter` field via `NewWorker` (add the parameter; update `main.go` and tests — nil stays legal).

5. **Outbound integration** (`internal/syncoutbound`): in `dispatchOp`, consult `AllowWrite` BEFORE reserving the card or marking inflight; denied → skip silently (op stays `pending`, picked up next tick — no attempt consumed, no reschedule write). In `failOp`, when `classifyFailure` sees a rate-limit type (extend its return or inspect separately — state which and why in the PR), also `ReportRateLimit(conn.ID, retryAfter)`. On `applied` → `ReportSuccess`. `NewWorker` gains the budgeter parameter; nil legal.

6. **Every op an outbound tick executes costs read tokens too**: `processOp` performs `GetIssue` (compare-before-write) plus the write. Account the whole op as one write acquisition — do NOT double-charge reads for the embedded `GetIssue`; document this granularity choice in the package doc (budget unit = provider round-trip-ish, coarse by design; refining to per-HTTP-call is a later concern).

7. **Config** (`internal/config`, extending `SyncConfig` + `loadSyncConfig` in the exact house style — empty env ⇒ default, eager validation, `config: invalid <ENV> %q: %w` / `below minimum` error shapes):
   - `PILOT_CONSOLE_SYNC_BUDGET_RPM` — int, default `60`, min `6`.
   - `PILOT_CONSOLE_SYNC_BUDGET_COOLDOWN_MAX` — duration, default `10m`, min `30s`.
   No enable flag: the budgeter is always constructed in `main.go` when either sync worker starts (one shared instance passed to both), and its zero-cost path (nil) exists only for tests/callers that opt out. Startup log lines for both workers gain the two knob values.

8. **Tests** (table-driven, stdlib, hand-rolled fakes; injected `now` — no sleeps): bucket refill math · read exhausted while write still allowed (reserve) · write borrows from read when write bucket dry · read never borrows from write · cooldown opens on report, read denied write allowed after retryAfter, full reset on success · consecutive reports escalate toward cap · nil budgeter allows everything. Worker tests (existing `fakeBoardStore`/`staticConnector`/`staticWriterFactory` patterns, driving unexported steps directly per house idiom): ingest pass abandoned on denied read with watermark untouched · typed 429 from a page → cooldown → next pass skipped · outbound denied write leaves op pending with attempts unchanged · outbound rate-limited op reports and the next tick's sibling op is denied.

9. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/syncthrottle/budgeter.go` + `budgeter_test.go`, `internal/syncingest/worker.go` (+tests), `internal/syncoutbound/worker.go` + `execute.go` (classification surface) (+tests), `internal/config/config.go` (+test), `main.go`.

Sequencing: budgeter (pure, clock-injected, fully tested) → outbound integration (typed errors already flow there) → ingest integration (add classification) → config + main wiring.

**Verify-before-relying**: (a) `classifyFailure`'s exact return contract (`execute.go:370`) before extending it; (b) that ingest's Linear wrapper (`retryingSyncSource`, `connector.go:155`) re-wraps or passes through error text — the Linear heuristic must match what actually reaches `deltaPoll`.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** metrics/counters export (C9 reads budgeter state next leg — expose nothing yet beyond what logging needs) · adaptive *poll-interval* mutation (the ticker stays fixed; cooldown-skip achieves the same backoff without touching ticker lifecycle) · per-connection worker leases · any boardapi/HTTP change · SDK changes (Linear typed errors are an upstream follow-up, journaled in the roadmap).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/96 (labels: pilot, no-decompose; gated on #95)
- Depends on: console#87–#94 (merged) · console#95 C5 statusmap leg (chained ahead of this issue)
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §7 (rate-limit failure mode: token bucket 70/30, write preemption, 10m adaptive cap) — embedded above
- Console facts verified 2026-08-05 at `f1658e3`; SDK facts at `studio-sdk` `acee519` / v0.31.2
