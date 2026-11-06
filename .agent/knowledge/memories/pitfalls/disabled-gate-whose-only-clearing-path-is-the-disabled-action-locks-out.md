---
name: disabled-gate-whose-only-clearing-path-is-the-disabled-action-locks-out
description: A UI gate that disables the one action which can clear it is a lockout. TASK-640 K2 (ui PR#379): chat composer disabled while keyInvalid is set; the only clearing paths were a successful send (impossible, the composer is disabled), reset() on auth loss, or a page reload; saving a new key on Connections never touched the chat store. The pre-PR enforce round had fixed the inverse bug (the poll cleared the flag every tick); the Navigator review approved; the post-merge review caught it → ui#380.
type: pitfall
---

# A disabled gate whose only clearing path is the disabled action is a lockout

**Case (2026-11-06, TASK-640 K2, ui#378 → PR#379):** the Research and Home composers are disabled while `chat.keyInvalid` is set (409 `anthropic_key_invalid`). The pre-PR enforce review found the poll tick cleared the flag every 2 s and the fix made only a successful send (or `reset()`) clear it. A send is impossible while the composer is disabled; `reset()` runs only on auth loss; `saveCredential` on Connections calls `session.upsertCredential` and never touches the chat store. A user who fixes the key stays locked out until a reload or logout. Navigator's pre-merge review approved with notes and missed it. The post-merge review (REQUEST-CHANGES 12:04Z) caught it → autopilot fix ui#380.

## Rules
- Every "disabled until X" needs a clearing path that does not go through the disabled control: the action that fixes the cause (here: save a key), a fresh read of the source of truth on mount (here: `GET /api/v1/credentials` `health_status`), or a route change.
- When a fix round narrows the clearing paths of a flag (to stop a flap), re-check that at least one remaining path is reachable while the gate is on.
- Review checklist line for a gate: "list the clearing paths; strike the ones the gate itself blocks; at least one must remain".
- Spec the clearing path: a view spec that only sets the flag by hand and asserts the disabled state never catches a lockout.
