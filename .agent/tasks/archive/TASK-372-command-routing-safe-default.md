# TASK-372: Route `IntentCommand` + make the dispatch default safe (stop unknown→task)

**Status**: ✅ SHIPPED — but did NOT fix the live bug; superseded by PR [#3661](https://github.com/qf-studio/pilot/pull/3661). See **Resolution** below.
**Created**: 2026-06-25 · **Archived**: 2026-06-25
**Assignee**: Pilot · **Labels**: `pilot`, adapter-layer, bug

---

## ⚠️ Resolution (2026-06-25) — read this first

TASK-372 shipped as PR [#3659](https://github.com/qf-studio/pilot/pull/3659) (v2.194.1) and the daemon was rebuilt + restarted on it — **but `@Pilot /help` still created a task.** The binary was confirmed to carry the fix (`go version -m … vcs.revision=9feac99a, vcs.modified=false`), so the fix was real but **in the wrong layer**.

**The diagnosis below (the `default:→task` comms dispatch) was wrong about the LIVE path.** The live Slack path is the **studio-sdk chat bridge** (`cmd/pilot/main.go` → `sdkSlack.New(...).NewChatBridge(...)` → `slack.Handler.HandleMessage(core.MessageEvent)`), NOT the native `processEvent`. The SDK bridge detects the `/` prefix **upstream** and emits `core.MessageEvent{Action:"command", Command, Args, Text:""}`. Both Pilot mapping sites (`slack/handler.go` `HandleMessage` and the shared `sdkshim.MessageEventToIncomingMessage`) handled only `Action=="callback"` and copied the **empty** `ev.Text` → `/help` reached `comms` as empty text → `handleTask`. TASK-372's new `IntentCommand` routing inside `comms` never fired because the command never arrived there as `/help`.

**It merged green** because TASK-372's tests exercised `comms` in isolation with already-`/`-prefixed text and never crossed the `core.MessageEvent → comms` seam; the pre-existing `TestHandler_HandleMessage_CommandAction` even set `Command:"/status"` but asserted only `Platform`/`IsCallback`, never `Text`.

**Real fix — PR [#3661](https://github.com/qf-studio/pilot/pull/3661)** (`main @ f88d76de`): handle `Action=="command"` at both boundary sites, reconstructing `Command + Args` into `Text` so `comms.detectIntent → IntentCommand → CommandHandler` (TASK-372's wiring finally fires). Added boundary tests (slack + sdkshim) and a comms end-to-end `HandleMessage("/help") → help text, no task confirmation`. Live-verified on Slack 2026-06-25: `/help`, `/status`, `/queue` all route to command output; no stray tasks.

**Net:** TASK-372's `default:→clarify` + `IntentCommand` routing are still good hardening (they protect the native/Telegram-via-comms paths and any text that reaches `comms` with a `/` prefix), but they were **necessary-not-sufficient**. The actual seam was the SDK adapter boundary. Knowledge captured in `pitfalls/bug_sdk_command_action_dropped.md` (mem-036).

---

## Context (original — diagnosis partly superseded; see Resolution)

**Problem (observed live on Slack, 2026-06-25):**
`@Pilot Bot /help` replied with **"Task SLACK-… · …/studio-sdk  [Execute ✅] [Cancel ❌]"** — `/help` was turned into a code-task confirmation instead of printing help.

**Root cause as originally believed (two stacked defects):**
1. `comms/handler.go` `detectIntent` returns `IntentCommand` for any `/`-prefixed text.
2. The dispatch switch had **no `case IntentCommand`** → fell through to `default: // treat as task` → `handleTask`.
3. Believed the Slack adapter "delegates all text to comms and never intercepts `/`-commands." — **This was the wrong part.** The studio-sdk bridge DOES intercept `/`-commands upstream (see Resolution).

**Systemic issue (still valid):** `default: // treat as task` is an unsafe default — any unrouted intent collapses into a destructive "create a code task" prompt (same foot-gun behind the v2.193.0 operational regression). TASK-372 fixed this; it remains worth keeping.

## What shipped under TASK-372 (PR #3659, v2.194.1)
- `comms.Handler` gained `cmdHandler *CommandHandler` (wired in `NewHandler`/`BuildHandler`).
- Dispatch switch got `case intent.IntentCommand: h.cmdHandler.HandleCommand(...)`.
- `default:` changed from `handleTask` to a non-destructive clarify reply.
- `strings.TrimSpace` at top of `detectIntent`; nil-guard on `cmdHandler`.
- Six table-driven comms tests (command routing, leading-space trim, safe default, explicit task→handleTask, nil-cmdHandler, unknown `/foo`).

## Out of scope (unchanged)
- Moving NL intent classification to Haiku-primary (separate design spike — this bug is dispatch/boundary, not detection).

## Refs
- Live diagnosis 2026-06-25 (Slack). Follows TASK-370 (unified comms factory) and TASK-371 (operational intent).
- Superseded-by: PR [#3661](https://github.com/qf-studio/pilot/pull/3661) (adapter-seam fix). Pitfall: `bug_sdk_command_action_dropped.md`.
