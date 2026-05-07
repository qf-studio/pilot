# TASK-29: Wire Telegram approve/reject callback dispatch

**Status:** Planned
**Priority:** P0 (blocks pre-merge approval re-enable)
**Type:** Bug fix (Day-1 wiring gap)
**Effort:** S (~25 LoC + test)

## Problem

`internal/adapters/telegram/handler.go:handleCallback` (lines 372-421) only
handles `execute`, `cancel`, `switch_*`, `voice_check_status`. There is **no
case** for callbacks with the `approve:` or `reject:` prefix.

`*approval.TelegramHandler` (defined at `internal/approval/telegram.go:38`)
exposes `HandleCallback(ctx, callbackID, data, userID, username) bool` which
parses `approve:<requestID>` / `reject:<requestID>` correctly — but this
object is constructed at `cmd/pilot/main.go:423` and `:1304`, registered
with the approval manager, and **never injected into the telegram poller**.

Result: every approval button tap is acknowledged by `AnswerCallback`
(loading spinner clears — illusion of success) then falls through the
switch default and is silently dropped. `<-responseCh` in
`internal/approval/manager.go:205-213` then blocks for 24h, and
`processAllPRs` (`controller.go:2059-2114`) is sequential, so one stuck PR
starves the entire queue.

This was the actual mechanism behind every "I clicked Merge but nothing
happened" episode. Manual `gh pr merge` (handled by
`controller.checkExternalMergeOrClose`) is the current workaround.

References:
- Memory: `bug_telegram_approval_callback_unwired.md` (full RCA)
- Memory: `pattern_approval_chat_id_bootstrap.md` (bootstrap deadlock)
- `.agent/system/OPERATIONAL-ISSUES.md` "Telegram Pre-Merge Approval Dispatch Unwired (2026-05-05)"
- TASK-26 / TASK-27 / TASK-28 (archived) — approval architecture context

## Fix Shape (tactical)

Inject the existing `*approval.TelegramHandler` into `telegram.Handler` at
construction, then route `approve:`/`reject:` callbacks to it.

### Changes

**1. `internal/adapters/telegram/handler.go`**

- Add interface for the approval handler (avoid hard dep on approval pkg if
  it would create a cycle — approval defines `TelegramClient` itself, so
  direct import should be safe; verify):

```go
// ApprovalCallbackHandler dispatches approve:/reject: callbacks.
// Implemented by *approval.TelegramHandler.
type ApprovalCallbackHandler interface {
    HandleCallback(ctx context.Context, callbackID, data, userID, username string) bool
}
```

- Add field to `Handler`:
  ```go
  approvalHandler ApprovalCallbackHandler
  ```

- Add field to `HandlerConfig`:
  ```go
  ApprovalHandler ApprovalCallbackHandler
  ```

- Wire in `NewHandler`:
  ```go
  approvalHandler: config.ApprovalHandler,
  ```

- Add case in `handleCallback` switch (before existing cases — avoids
  fall-through; after the `AnswerCallback` is fine because the approval
  handler will edit the message itself):

  ```go
  case strings.HasPrefix(data, "approve:") || strings.HasPrefix(data, "reject:"):
      if h.approvalHandler == nil {
          return
      }
      userID := ""
      username := ""
      if callback.From != nil {
          userID = strconv.FormatInt(callback.From.ID, 10)
          username = callback.From.Username
          if username == "" {
              username = callback.From.FirstName
          }
      }
      h.approvalHandler.HandleCallback(ctx, callback.ID, data, userID, username)
  ```

  Note: the existing `_ = h.client.AnswerCallback(...)` at line 382 already
  fires for all callbacks, which is fine — `approval.TelegramHandler.HandleCallback`
  edits the message via `EditMessage` separately. (Verify against `approval/telegram.go:144+` — current code calls `client.EditMessage` post-decision; the early `AnswerCallback` is harmless.)

**2. `cmd/pilot/main.go`**

Both construction sites need updating. Currently:
```go
tgApprovalHandler := approval.NewTelegramHandler(&telegramApprovalAdapter{...}, ...)
approvalMgr.RegisterHandler(tgApprovalHandler)
// ... later ...
tgHandler = telegram.NewHandler(tgConfig, runner)
```

Change to: construct `tgApprovalHandler` BEFORE `tgConfig`, then pass via
config:
```go
tgConfig.ApprovalHandler = tgApprovalHandler
tgHandler = telegram.NewHandler(tgConfig, runner)
```

Both call sites at `main.go:423` and `:1304` need this re-ordering. Check
which `main.go:1707` site (`telegram.NewHandler(...)`) corresponds to which
construction site — the `start` command flow is the load-bearing one.

**3. Unit test — `internal/adapters/telegram/handler_test.go`**

```go
func TestHandleCallback_ApproveRejectDispatches(t *testing.T) {
    var called bool
    var gotCallbackID, gotData, gotUserID string
    fakeApproval := &fakeApprovalHandler{
        fn: func(ctx context.Context, callbackID, data, userID, username string) bool {
            called = true
            gotCallbackID = callbackID
            gotData = data
            gotUserID = userID
            return true
        },
    }
    h := &Handler{
        client:          newFakeClient(),
        approvalHandler: fakeApproval,
    }
    cb := &CallbackQuery{
        ID:      "cb-1",
        Data:    "approve:req-42",
        Message: &Message{Chat: Chat{ID: 1}},
        From:    &User{ID: 99, Username: "alice"},
    }
    h.handleCallback(context.Background(), cb)

    if !called {
        t.Fatal("approve: callback should dispatch to ApprovalCallbackHandler")
    }
    if gotCallbackID != "cb-1" || gotData != "approve:req-42" || gotUserID != "99" {
        t.Errorf("unexpected args: cb=%q data=%q uid=%q", gotCallbackID, gotData, gotUserID)
    }
}
```

Plus a parallel `reject:` case and a `nil approvalHandler` no-panic case.

## Verification

- `go test ./internal/adapters/telegram/... -run Approve`
- `go build ./...`
- `go vet ./...`
- Manual smoke (operator post-merge): re-enable `approval.pre_merge.enabled` + `release.environments.stage.require_approval` in `~/.pilot/config.yaml`, trigger a stage release, tap Approve, verify PR advances within 60s.

## Out of scope (separate tasks)

- **M:** Persist pending approval requests in SQLite (`approval_pending`
  table); rehydrate `tgApprovalHandler.pending` on restart so taps on
  pre-restart messages don't return "Request expired or already
  processed".
- **L:** Replace blocking `<-responseCh` in `requestApproval` with
  callback-driven stage advance — handler writes decision to PR state on
  tap, next controller tick picks up; eliminates 24h queue starvation.

## Re-enable checklist (post-merge of this PR)

After PR is merged + smoke-tested, follow `.agent/system/OPERATIONAL-ISSUES.md`:
1. `~/.pilot/config.yaml` line 481: `approval.pre_merge.enabled: true`
2. `~/.pilot/config.yaml` line 185: `release.environments.stage.require_approval: true`
3. `pilot upgrade` (or hot-upgrade if daemon supports) to picked-up version
4. Smoke-test a stage release end-to-end before considering closed.

## Acceptance Criteria

- [ ] `approve:<id>` callback in `handleCallback` dispatched to `ApprovalCallbackHandler.HandleCallback`
- [ ] `reject:<id>` callback dispatched the same way
- [ ] `nil` approval handler is a no-op (no panic)
- [ ] Unit tests pass for approve, reject, nil-handler cases
- [ ] `cmd/pilot/main.go` injects `tgApprovalHandler` into `tgConfig` at both construction sites
- [ ] Existing `execute`/`cancel`/`switch_`/`voice_check_status` callbacks still work (no regressions)
- [ ] PR title is conventional: `fix(adapters/telegram): wire approve/reject callback dispatch (GH-NNNN)`
