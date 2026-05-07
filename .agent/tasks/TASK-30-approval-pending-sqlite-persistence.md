# TASK-30: Persist pending approval requests in SQLite

**Status:** Planned
**Priority:** P1 (UX correctness — restarts lose in-flight approvals)
**Type:** Feature (durability)
**Effort:** M (~80 LoC + migration + tests)

## Problem

`*approval.TelegramHandler.pending` (`internal/approval/telegram.go:42`) is
an in-memory `map[string]*pendingRequest` keyed by request ID. When the
daemon restarts (hot-upgrade or crash):

1. `pending` is empty.
2. The Telegram message and inline keyboard from before the restart still
   sit in the user's chat.
3. User taps Approve / Reject — `HandleCallback` cannot find the request →
   returns "Request expired or already processed" (the actual message in
   `approval/telegram.go`).
4. Stage release that triggered the approval already has its blocking
   `<-responseCh` orphaned in `manager.requestApproval`; that waits 24h
   then defaults to `rejected`.

This pairs badly with TASK-29's tactical dispatch fix: now taps reach the
handler — but only for the current process lifetime. Hot-upgrades happen
routinely (every release).

## Fix Shape

Add an `approval_pending` table to the SQLite store; persist on
`SendApprovalRequest`, delete on decision/cancel/timeout; rehydrate the
`pending` map on `NewTelegramHandler` startup.

### Schema (new migration)

```sql
CREATE TABLE IF NOT EXISTS approval_pending (
    request_id    TEXT PRIMARY KEY,
    pr_number     INTEGER NOT NULL,
    repo          TEXT NOT NULL,
    stage         TEXT NOT NULL,
    chat_id       TEXT NOT NULL,
    message_id    INTEGER NOT NULL,
    approvers     TEXT NOT NULL,         -- JSON array of approver IDs
    requested_by  TEXT,
    metadata      TEXT,                  -- JSON blob (Request.Metadata)
    expires_at    INTEGER NOT NULL,      -- unix seconds
    created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_approval_pending_expires ON approval_pending(expires_at);
```

Place migration in whichever migration mechanism `internal/memory/store.go`
uses (verify pattern from existing tables). Use Go-side migration if no
formal migrator exists.

### Code changes

**1. `internal/memory/store.go`** (or new `approval_store.go`)
- `InsertPendingApproval(ctx, p PendingApproval) error`
- `DeletePendingApproval(ctx, requestID string) error`
- `LoadPendingApprovals(ctx) ([]PendingApproval, error)` — used at startup
- `PrunePendingApprovals(ctx, before time.Time) (int, error)` — drops expired rows; called periodically

**2. `internal/approval/telegram.go`**
- Add optional `store PendingApprovalStore` interface field to `TelegramHandler`:
  ```go
  type PendingApprovalStore interface {
      InsertPendingApproval(ctx context.Context, p PendingApproval) error
      DeletePendingApproval(ctx context.Context, requestID string) error
      LoadPendingApprovals(ctx context.Context) ([]PendingApproval, error)
  }
  ```
- `NewTelegramHandler(client, chatID)` keeps existing signature; add
  `WithStore(store PendingApprovalStore)` builder method to keep the
  constructor backward-compatible.
- In `SendApprovalRequest`: after inserting into `pending` map, also
  `store.InsertPendingApproval` (best-effort; log on error, don't fail
  the request).
- In `HandleCallback` decision branch: `store.DeletePendingApproval`
  alongside `delete(h.pending, ...)`.
- In `CancelRequest`: same delete-from-store.
- Add `Rehydrate(ctx)` method called once at startup: loads rows,
  populates `pending` map. Skip rows where `expires_at < now` (caller
  prunes those).
- Note: response channels on rehydrated requests must be reconstructed —
  there is no caller blocking on them post-restart, so the channel can be
  a buffered channel that nobody reads. This is the bridge to TASK-31
  (callback-driven stage advance) which removes the blocker entirely.

**3. `cmd/pilot/main.go`**
- Both construction sites (`:423`, `:1304`):
  ```go
  tgApprovalHandler := approval.NewTelegramHandler(...).WithStore(memoryStore)
  if err := tgApprovalHandler.Rehydrate(ctx); err != nil {
      logger.Warn("approval rehydrate failed", "err", err)
  }
  ```

### Tests

- `TestTelegramHandler_PersistsOnSend` — `SendApprovalRequest` calls `InsertPendingApproval`
- `TestTelegramHandler_DeletesOnApprove` / `OnReject` / `OnCancel`
- `TestTelegramHandler_Rehydrate_RestoresPending` — preload store, construct handler, verify `pending` populated, `HandleCallback` succeeds for restored entry
- `TestTelegramHandler_Rehydrate_SkipsExpired` — past expiry rows ignored
- Store layer: insert/delete/load round-trip + prune

### Verification

- `go test ./internal/memory/... ./internal/approval/...`
- `go build ./...`
- Manual smoke: trigger approval → hot-upgrade daemon → tap button → PR advances (currently fails post-restart with "Request expired")

## Out of scope

- Callback-driven stage advance (separate task): see TASK-31.
- Slack/GitHub handler persistence — file as further follow-ups if needed.

## Acceptance Criteria

- [ ] `approval_pending` table created via migration
- [ ] `SendApprovalRequest` persists row
- [ ] Decision / cancel deletes row
- [ ] `Rehydrate` repopulates `pending` map at startup; skips expired
- [ ] After daemon restart, tap on pre-restart Telegram message succeeds
- [ ] `WithStore` is optional — handler still works without it (in-memory only)
- [ ] Tests cover persist / delete / rehydrate / expired-skip
- [ ] `go build ./...` + `go vet ./...` clean
- [ ] Conventional PR title: `feat(approval): persist pending approvals in SQLite (GH-NNNN)`

## References

- `internal/approval/telegram.go:38-150` — `TelegramHandler` shape
- `internal/approval/manager.go:205-213` — blocking `<-responseCh` (will be replaced in TASK-31)
- Memory: `bug_telegram_approval_callback_unwired.md` § "Outstanding follow-ups"
- TASK-29 (archived) — tactical dispatch fix that this depends on
