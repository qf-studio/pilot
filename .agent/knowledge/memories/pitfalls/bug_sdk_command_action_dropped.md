---
name: studio-sdk chat bridge splits /-commands into Action:"command" with empty Text — adapters that only handle "callback" drop them → comms makes an empty task
description: The studio-sdk Slack/chat bridge detects the "/" prefix UPSTREAM and emits core.MessageEvent{Action:"command", Command, Args, Text:""}. Both pilot mapping sites (slack/handler.go HandleMessage + the shared sdkshim.MessageEventToIncomingMessage) handled only Action=="callback" and copied the empty ev.Text into comms.IncomingMessage, so /help reached comms as empty text, fell through intent dispatch, and created a task. Live "@Pilot /help creates a task" bug, 2026-06-25. TASK-372 "fixed" it one layer too low (comms dispatch) and merged green because its tests fed comms already-/-prefixed text and never crossed the adapter→comms seam. Real fix: PR #3661 handles Action=="command" at both boundary sites.
type: pitfall
---
On the **live SDK chat path** (`cmd/pilot/main.go` wires Slack via `sdkSlack.New(...).NewChatBridge(...)` → `slack.Handler.HandleMessage(core.MessageEvent)`, NOT the native `processEvent`), a `/`-prefixed Slack message like `@Pilot /help` is turned into a **code-task confirmation** instead of running the command — even after the `comms` layer was taught to route `IntentCommand`.

**Why:** the studio-sdk chat bridge (`sdk/integrations/slack/bridge.go`) detects the `/` prefix **upstream** and emits a structured command event:
```go
if strings.HasPrefix(text, "/") {
    ev = core.MessageEvent{Action: "command", Command: parts[0], Args: parts[1:]}  // Text is LEFT EMPTY
}
```
Pilot's two `core.MessageEvent → comms.IncomingMessage` mapping sites both handled **only** `Action=="callback"` and copied `ev.Text` verbatim:
- `internal/adapters/slack/handler.go` `HandleMessage` (the live inlined mapping)
- `internal/adapters/sdkshim/chat.go` `MessageEventToIncomingMessage` (shared shim — Discord + future SDK-bridged adapters)

So `/help` arrived at `comms` as **empty text** → not a `/`-command → fell through `detectIntent`/dispatch → `handleTask`. The command content was sitting in `ev.Command`/`ev.Args`, which nobody read.

**Why TASK-372 missed it (fixed the wrong layer):** TASK-372 added a `case intent.IntentCommand` and a safe `default:` **inside `comms.detectIntent`/dispatch** — correct hardening, but the command text **never reached `comms` as `/help`**: the SDK had already split it one layer up. The diagnosis in the TASK-372 doc ("Slack adapter delegates ALL text to comms, never intercepts /-commands") was wrong — the SDK bridge **does** intercept `/`-commands. The binary was confirmed to carry the TASK-372 fix (`go version -m … vcs.revision=9feac99a, vcs.modified=false`) and the bug still reproduced, which is what proved the layer was wrong.

**Why it merged green (the real gap):** TASK-372's tests exercised `comms` **in isolation** with already-`/`-prefixed text (`detectIntent("/help")==IntentCommand`, `CommandHandler.HandleHelp` direct) and never crossed the `core.MessageEvent → comms.IncomingMessage` boundary where the defect lived. Worse, the pre-existing `TestHandler_HandleMessage_CommandAction` set `Command:"/status"` but asserted **only** `Platform`/`IsCallback` — it never checked `got.Text`, so the empty-Text bug satisfied it. "Merged + CI green" gave false confidence.

**How to apply:**
- **Read `ev.Command`/`ev.Args` on command events.** Any `core.MessageEvent → comms.IncomingMessage` mapping must handle `Action=="command"` by reconstructing the command line into `Text` so `comms.detectIntent` sees the `/` prefix and routes `IntentCommand → CommandHandler`:
  ```go
  case "command":
      if ev.Text == "" && ev.Command != "" {
          msg.Text = strings.TrimSpace(ev.Command + " " + strings.Join(ev.Args, " "))
      }
  ```
  Both sites need it: `slack/handler.go` (live) and `sdkshim/chat.go` (shim, covers Discord). The native `processEvent` path is fine — it passes raw `/help` text through and `comms` handles the prefix.
- **Test at the adapter→comms seam, not the layer in isolation.** When a chat bug is reported live, the regression test must feed a real `core.MessageEvent{Action:"command", …}` and assert the resulting `comms.IncomingMessage.Text` / end-to-end output — not just unit-test the inner layer with pre-massaged input. A green isolated test on the wrong layer is how this shipped twice.
- **Weak assertions hide bugs.** A test that constructs a command event but asserts only `Platform`/`IsCallback` (never the field the fix is about) will pass against the broken code. Assert the thing the bug is about.
- **Identify the LIVE path before diagnosing.** `cmd/pilot/main.go` wires Slack through the studio-sdk bridge (`NewChatBridge` → `HandleMessage(core.MessageEvent)`); the native `StartListening`/`processEvent`/`stripBotMention` code in `internal/adapters/slack` is **dead for the daemon**. Grep the wiring in `main.go` first.

**Deploy footnote (same session):** getting the fix live also hit the known binary-path gotcha — `make install` (`go install`) writes `~/go/bin/pilot`, but the daemon runs `~/.local/bin/pilot` (first in PATH), so `go install` alone never updates the daemon; sync with `cp ~/go/bin/pilot ~/.local/bin/pilot` (or `pilot upgrade`) then restart, and verify `go version -m <bin> | grep vcs.revision` + `ps` start-time > binary mtime. See [[learn_restart_vs_rebuild_stale_binary]] and [[learning_pilot_release_and_binary_path]].
