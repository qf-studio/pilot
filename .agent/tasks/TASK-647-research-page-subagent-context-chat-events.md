# TASK-647: Research page `subagent` + `context` chat events — the daemon reports sub-agent start/done with durations and the turn's Navigator read counts (pilot#5810, roadmap P17 / U9)

**Status**: 🚀 **A1 MERGED 2026-11-08 15:07Z → [PR#6212](https://github.com/qf-studio/pilot/pull/6212) (second run after a 14:02Z pre-PR hold: fan-out recorded a stream line N times; body amended, branch parked `pilot-hold/GH-6210-b16ff1c`, retry 12m24s to merge). B1 DISPATCHED 15:2xZ → [pilot#6214](https://github.com/qf-studio/pilot/issues/6214) (`pilot` + `no-decompose`).** Next: B1 pre-PR verdict → post-merge reviews of PR#6212 and B1's PR → next daemon tag on the tenant → live check on the console Research page.
**Created**: 2026-11-08
**Assignee**: Navigator (plan, reviews, live check) → Pilot (legs)
**Parent**: console program wave 1, roadmap row P17 (daemon side) → unblocks U9 (`ui#244 → PR#246`, live). The console task docs and the roadmap live in the private `qf-studio/pilot-console` repo under `.agent/` since 2026-11-08 (TASK-646); this doc stays here because the change is daemon code.

---

## Context

**Problem**: the console Research page (`pilot-console-ui`, `src/views/ChatView.vue`) renders a sub-agent tree with DONE badges and the per-run line `Read N docs · M memories · Created K memories`. It binds to chat events of type `subagent` (`taskId`, `text`, `phase` = `running` | `done`, `durationMs`) and `context` (`docsRead`, `memoriesRead`, `memoriesCreated`). The daemon's web messenger (`internal/adapters/web/messenger.go`) emits `text`, `confirmation`, `progress`, `result`, `drafts` only, so every tenant shows `—` and an empty tree. The UI side is done: `WireChatEvent` in the UI's `httpAdapter.ts` already carries all four fields (allow-listed in `console-wire-fields.txt`), the store copies them, `ChatView` keys the tree by `taskId` (a later event with the same id replaces the row) and **sums `docsRead`/`memoriesRead`/`memoriesCreated` over every `context` event of the run** (`total()`), showing `—` only when no carrier exists. The console relays chat events byte-faithfully (`internal/chat/client.go` returns `json.RawMessage`), so unknown types and fields pass without a console change.

**Goal**: the daemon emits both event kinds during a web chat turn, truthfully, from what the Claude Code stream already tells it.

**Answers to pilot#5810's three questions (research 11-08, navigator-research over the executor):**
1. **Sub-agent start/end/duration** come from the Claude Code `stream-json` stream the chat turn already parses: an `assistant` message whose content holds a `tool_use` block named `Task` (older CLI) or `Agent` (current CLI) is the start; the `user` message whose `tool_result` block carries the same `tool_use_id` is the end; `durationMs` is the wall-clock difference between the two receipts. No Navigator hook runs on tenant boxes (chat turns are prompt-driven, `internal/executor/chat_prompt.go`), so hooks are not a source.
2. **`docsRead` / `memoriesRead`** come from `Read` tool_use inputs: distinct `file_path`s under the project's `.agent/` tree, split into `.agent/knowledge/memories/` (memories) and the rest of `.agent/` (docs). **`memoriesCreated` is structurally 0**: chat turns run with `WriteBoundaryDenyAll` and `chatDeniedTools` (Write/Edit/MultiEdit/NotebookEdit), so nothing can be created; the daemon reports `0`, not `—` (recalled pitfall: the allow-list is prompt-level under `--dangerously-skip-permissions`; the deny list is what holds, so `0` is truthful).
3. **No persistence.** The chat wire is a poll-drain ring (500 events per conversation, 1 h expiry, wiped on restart — `messenger.go`); even `result` events are live-only today. `recordChatTurn` (`internal/comms/chat_turn_handler.go`) persists usage rows; adding the counts there is a separate migration and not needed for the page.

**Gaps found that the legs must close (file facts on origin/main 11-08):**
- `parseStreamEvent` in `internal/executor/backend_claudecode.go` (near line 1454) loops over every content block but writes into **one** `BackendEvent`, so an assistant message with two parallel `Task` calls collapses to the last one. `ToolCalls` and the progress lines already undercount.
- `ContentBlock` (`internal/executor/runner.go`, near line 668) has no `id`; `StreamEvent` (near line 584) has no `parent_tool_use_id`; the `user` case reads only the flat `tool_use_result` field and never the `tool_result` blocks in `message.content`, so no `tool_use_id` reaches a `BackendEvent`.
- `chatProgressLine` and `ChatReadOnlyTools` (`internal/executor/chat_turn.go`) know `Task` but not `Agent`.
- `ExecuteChatTurn(ctx, turn, onProgress func(string))` has a string-only callback through a 500 ms throttle (`chatProgressThrottle`); sub-agent events must not pass through it (a `running` dropped by the throttle leaves a row that never appears).

---

## Known Pitfalls & Patterns

- **PITFALL** (95%): the chat tool allow-list is prompt-level under skip-permissions; only `--disallowedTools` holds → `memoriesCreated` is 0 by construction; do not derive it from a graph diff.
- **PATTERN** (85%, this research): a new web chat event kind = an `EventType` constant + an optional messenger interface (`DraftsSender` in `internal/comms/types.go`, implemented in `messenger.go`, type-asserted in the handler with a text fallback). Telegram/Slack must never see these as text → the optional-interface pattern, with **no** fallback for `subagent`/`context` (silently ignored).
- **PITFALL** (this research): `omitempty` on a plain `int` drops a real `0`, and the UI renders a missing field as `—`. Counters are pointers (`*int`, `*int64`), like `Progress *float64` and `Success *bool` already are.

---

## Acceptance Criteria

- [ ] A web chat turn whose stream holds a `Task`/`Agent` tool_use and its tool_result produces, on `GET /api/v1/chat/conversations/{id}/events`, a `subagent` event with `phase:"running"` and then one with `phase:"done"` and a `durationMs` ≥ 0, both with the same `taskId` (the tool_use id) and `text` = the sub-agent `description`.
- [ ] Two `Task` tool_use blocks in one assistant message → two `running` events with two different `taskId`s.
- [ ] Every chat turn ends with exactly one `context` event carrying `docsRead`, `memoriesRead` and `memoriesCreated:0` as JSON numbers (a turn that read nothing serializes `"docsRead":0`), emitted before the final `text` event.
- [ ] Non-web messengers receive no extra text for these events.
- [ ] `.agent/system/side-chat-navigator.md` lists the six event types with their fields.
- [ ] Live: after the daemon tag reaches the tenant, a research question on the console Research page shows sub-agent rows with DONE badges and durations, and the run line reads `Read N docs · M memories · Created 0` with N, M ≥ 0 (not `—`).

---

## Dispatch

Serial: A1 → (post-merge round) → B1 (`Depends on: #{{A1}}`). Both legs in `qf-studio/pilot`; `pilot` + `no-decompose`. No console or UI leg: the UI contract is already live and the console passes bytes through.

### LEG A1 — repo: qf-studio/pilot — title: feat(executor): chat turns observe sub-agents and Navigator reads — one event per tool_use block, tool_use ids on tool_use and tool_result events, ChatTurn reports sub-agent running/done with durationMs and counts distinct .agent docs and memories read

## Context
`parseStreamEvent` in `internal/executor/backend_claudecode.go` (near line 1454) iterates `streamEvent.Message.Content` and writes every block into the same `BackendEvent`, so an assistant message with two `tool_use` blocks yields one `EventTypeToolUse` event (the last block wins); its single call site is the stdout loop near line 1017. `ContentBlock` (`internal/executor/runner.go`, near line 668) has `Type`, `Text`, `Content`, `Name`, `Input`, `IsError` but no `id` and no `tool_use_id`; `StreamEvent` (near line 584) has no `parent_tool_use_id`; the `user` case of the parser reads only the flat `tool_use_result` field into `ToolResultContent`. `BackendEvent` (`internal/executor/backend.go`) has `ToolName`, `ToolInput`, `ToolResult`, `IsError` and no tool_use id. The chat turn (`internal/executor/chat_turn.go`) counts `toolCalls` on `EventTypeToolUse` and offers `chatProgressLine(ev)` to a 500 ms throttle; `chatProgressLine` has a `case "Task":` (key `description`) and `ChatReadOnlyTools` lists `"Task"`; the current Claude Code CLI names the sub-agent tool `Agent`. `ChatTurn` has `ConversationID`, `ProjectPath`, `Text`, `ResumeSessionID`, `MaxTurns`, `Timeout`; `ChatTurnResult` has `Text`, `SessionID`, `Partial`, `SessionReset`, `ToolCalls` and the usage fields. Chat turns are read-only (`WriteBoundaryDenyAll`, `chatDeniedTools`). Tests: `internal/executor/chat_turn_test.go` has `chatFakeBackend`, `toolUse(name, input)`, `disableChatProgressThrottle(t)` and `TestChatTurn_ProgressForwardsToolUse`; `internal/executor/backend_claudecode_test.go` holds the parser tests.

probe: present qf-studio/pilot for _, block := range streamEvent\.Message\.Content
probe: present qf-studio/pilot case "Task":
probe: present qf-studio/pilot func toolUse\(name string
probe: absent qf-studio/pilot OnSubagent[[:space:]]+func
probe: absent qf-studio/pilot DocsRead[[:space:]]+int
probe: absent qf-studio/pilot ParentToolUseID[[:space:]]+string

## Scope
- `internal/executor/runner.go`: `ContentBlock` gains `ID string` (json `id`) and `ToolUseID string` (json `tool_use_id`, set on `tool_result` blocks); `StreamEvent` gains `ParentToolUseID string` (json `parent_tool_use_id`). `internal/executor/backend.go`: `BackendEvent` gains `ToolUseID string` (the block id on tool_use events, the referenced id on tool_result events) and `ParentToolUseID string` (copied from the stream event on every event; empty for the main agent).
- `internal/executor/backend_claudecode.go`: the Claude Code parser returns **one `BackendEvent` per content block** in message order — rename to a slice-returning function (for example `parseStreamEvents(line) []BackendEvent`) and loop at the call site near line 1017, or keep `parseStreamEvent` as a thin wrapper for the single-event cases; text blocks and tool_use blocks in the same message each get their own event. The `user` case: when `message.content` holds `tool_result` blocks, emit one `EventTypeToolResult` per block with `ToolUseID` and `IsError` from the block; the flat `tool_use_result` keeps filling `ToolResult`/`IsError` on the first of them (or on a single event when there are no blocks) exactly as today. `stream_event`, `system` and `result` cases unchanged. Qwen backend untouched.
- `internal/executor/chat_turn.go`: `ChatTurn` gains `OnSubagent func(SubagentEvent)` (nil-safe); new exported `SubagentEvent struct { TaskID, Text, Phase string; DurationMs int64 }` with `Phase` = `running` | `done`. In the event handler: a tool_use named `Task` or `Agent` with an empty `ParentToolUseID` records `startedAt` by `ToolUseID` and calls `OnSubagent{TaskID: ToolUseID, Text: description}` with `running` (text fallback order: input `description`, then the first 80 runes of input `prompt`, then the tool name); a tool_result whose `ToolUseID` is a recorded sub-agent calls `done` with `DurationMs` = elapsed since its start and forgets it; when the backend returns, every still-open sub-agent gets a `done` with the elapsed time (no row may stay spinning). These calls bypass `chatProgressThrottle`; `progress.Offer(chatProgressLine(ev))` stays as today. Add `"Agent"` to `ChatReadOnlyTools` right after `"Task"` and to the `chatProgressLine` case (`case "Task", "Agent":`, key `description`). **Read counts**: on every `Read` tool_use (any `ParentToolUseID`), take input `file_path`, strip a leading `turn.ProjectPath` + `/` when present, then: a path containing `.agent/knowledge/memories/` joins the memories set; otherwise a path starting with `.agent/` (or containing `/.agent/`) joins the docs set; everything else is ignored (Grep/Glob hits are not reads). `ChatTurnResult` gains `DocsRead int` and `MemoriesRead int` = the two set sizes for this turn (a resumed session counts this turn only; say so in the docblock).
- Tests. `internal/executor/backend_claudecode_test.go`: an assistant line with two `tool_use` blocks (ids `toolu_a`, `toolu_b`) → two `EventTypeToolUse` events in order with those `ToolUseID`s (mutation pin: collapse back to one event → fails); a `user` line with a `tool_result` block `tool_use_id: toolu_a` → `EventTypeToolResult` with `ToolUseID == "toolu_a"`; a line with top-level `parent_tool_use_id` → `ParentToolUseID` set. `internal/executor/chat_turn_test.go`: a turn whose fake backend emits `toolUse("Task", {description: "map the executor"})` with `ToolUseID "toolu_1"` and then a tool_result for `toolu_1` → `OnSubagent` sees `running` then `done` with the same `TaskID`, `Text == "map the executor"`, `DurationMs >= 0`; the same with `"Agent"`; a Task with no tool_result → one `done` after the turn ends; a sub-agent's own tool_use (`ParentToolUseID` set) opens no row; `Read` of the paths /repo/.agent/DEVELOPMENT-README.md, .agent/tasks/TASK-1.md, .agent/knowledge/memories/pitfalls/x.md, the same memory again and src/main.go (test fixtures, not repo files) with `ProjectPath "/repo"` → `DocsRead == 2`, `MemoriesRead == 1` (mutation pin: drop the de-duplication → fails); a `Grep` on `.agent/` adds nothing.

## Acceptance
- [ ] `go test -race ./internal/executor/ -run TestChatTurn` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestParseStream` passes; paste the output into the PR body.
- [ ] Collapse the per-block loop back to one event -> the two-tool_use parser test fails
- [ ] Remove the de-duplication of Read paths -> the DocsRead/MemoriesRead test fails
- [ ] `go vet ./internal/executor/` passes; paste the output into the PR body.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope
The wire: no `comms` interface, no web messenger event, no handler change (leg B1). Persisting counts in `chat_turns`. The Qwen backend parser. Changing the 500 ms progress throttle or the progress line format beyond the `Agent` case. The runner's own `parseStreamEvent(taskID, line, state)` for execution progress.

## Refs
- #5810 (request) · Navigator TASK-647 in this repo · console roadmap row P17 / U9 (private repo)

### LEG B1 — repo: qf-studio/pilot — title: feat(web): subagent and context chat events on the wire — WebMessenger emits them, the chat handler forwards the executor's sub-agent observations and the turn's read counts, other messengers ignore them

## Context
After A1 (#{{A1}}) `executor.ChatTurn` has `OnSubagent func(SubagentEvent)` and `ChatTurnResult` has `DocsRead` / `MemoriesRead`. The web messenger `internal/adapters/web/messenger.go` defines `EventType` constants `text`, `confirmation`, `progress`, `result`, `drafts` and an `Event` struct with `seq`, `type`, `text`, `taskId`, `phase`, `progress` (`*float64`), `prUrl`, `success` (`*bool`), `messageRef`, `drafts`, `occurredAt`; `SendDrafts` appends one event and satisfies `comms.DraftsSender` (`internal/comms/types.go`), the pattern for a new event kind; a compile-time assertion for it sits near line 91. `handleChatTurn` in `internal/comms/chat_turn_handler.go` (near line 116) calls `h.runner.ExecuteChatTurn` with a progress closure that calls `SendProgress(ctx, contextID, "", taskID, "research", 0, line)` where `taskID` = `"chat-" + conversation`; the reply goes out through `SendChunked`; `handleCreateTickets` (near line 217) is the second caller and type-asserts `DraftsSender` with a text fallback. The events endpoint `handleChatEvents` in `internal/gateway/chat.go` serializes `[]web.Event` as is. The UI sums `docsRead`/`memoriesRead`/`memoriesCreated` over every `context` event of a run and keys the sub-agent tree by `taskId`; it shows `—` for a missing field, so a `0` must serialize. Tests: `internal/comms/chat_turn_handler_test.go` has `webChatBackend` and `webChatMessenger` (embeds `mockMessenger`) and `TestWebChat_ProgressForwarded`; `internal/adapters/web/api_test.go` has `TestMessenger_SendDraftsAppendsOneDraftsEvent`; `internal/adapters/web/messenger_test.go` asserts on `Events(contextID, 0)`. `.agent/system/side-chat-navigator.md` line 11 lists "Event types: `text`, `progress` …, `confirmation`, `result`" (already missing `drafts`).

probe: present qf-studio/pilot type DraftsSender interface
probe: present qf-studio/pilot EventDrafts[[:space:]]+EventType
probe: absent qf-studio/pilot ^\tEventSubagent[[:space:]]
probe: absent qf-studio/pilot ^type SubagentSender interface
probe: absent qf-studio/pilot ^func \(m \*WebMessenger\) SendContext\(

## Scope
- `internal/comms/types.go`: `SubagentUpdate struct { TaskID, Text, Phase string; DurationMs int64 }`, `ContextCounts struct { DocsRead, MemoriesRead, MemoriesCreated int }`; optional interfaces `SubagentSender { SendSubagent(ctx, contextID, threadID string, u SubagentUpdate) error }` and `ContextSender { SendContext(ctx, contextID, threadID string, c ContextCounts) error }`, documented like `DraftsSender`.
- `internal/adapters/web/messenger.go`: constants `EventSubagent EventType = "subagent"` and `EventContext EventType = "context"`; `Event` gains `DurationMs *int64` (json `durationMs,omitempty`), `DocsRead *int` (`docsRead,omitempty`), `MemoriesRead *int` (`memoriesRead,omitempty`), `MemoriesCreated *int` (`memoriesCreated,omitempty`) — pointers so a real `0` serializes. `SendSubagent` appends `Event{Type: EventSubagent, TaskID: u.TaskID, Text: u.Text, Phase: u.Phase}` and sets `DurationMs` only when `Phase == "done"`. `SendContext` appends `Event{Type: EventContext, TaskID: "chat-" + conversation (same as the progress taskId), DocsRead, MemoriesRead, MemoriesCreated}` all set. Both prune like the other senders; compile-time assertions next to the `DraftsSender` one.
- `internal/comms/chat_turn_handler.go` `handleChatTurn`: build the `executor.ChatTurn` with `OnSubagent` set only when `h.messenger` implements `SubagentSender` (forward 1:1, no throttle, no text fallback); after `ExecuteChatTurn` returns and `recordChatTurn` ran, when the messenger implements `ContextSender`, send one `ContextCounts{DocsRead: result.DocsRead, MemoriesRead: result.MemoriesRead, MemoriesCreated: 0}` **before** the reply text goes out (also on a partial turn; not when the turn errored before the backend ran). `handleCreateTickets` sends no `context` event and no sub-agent events (its drafts are the result).
- `.agent/system/side-chat-navigator.md` line 11: the event-type list becomes `text`, `progress` (…as today), `confirmation`, `result`, `drafts`, `subagent` (`taskId` = tool_use id, `text`, `phase` running|done, `durationMs` on done), `context` (one per turn, `docsRead`, `memoriesRead`, `memoriesCreated` = 0 on read-only turns; the UI sums them per run). One sentence that non-web messengers ignore both kinds.
- Tests. `internal/adapters/web/messenger_test.go` (or `api_test.go` next to the drafts test): `SendSubagent` running → the JSON of the event has no `durationMs` key; done with `DurationMs 1234` → `"durationMs":1234`; `SendContext` with zeros → the JSON contains `"docsRead":0`, `"memoriesRead":0`, `"memoriesCreated":0` (mutation pin: make `DocsRead` a plain `int` with omitempty → fails). `internal/comms/chat_turn_handler_test.go`: `webChatBackend` emits a `Task` tool_use (`ToolUseID "toolu_1"`, description "map the executor"), a `Read` of `.agent/DEVELOPMENT-README.md`, the matching tool_result and a text → `webChatMessenger` records, in order, `subagent running`, `subagent done` (same id, `DurationMs >= 0`), `context {1,0,0}`, then the text (mutation pin: send the context event after the text → fails); a handler with a plain `mockMessenger` (no `SubagentSender`/`ContextSender`) records no extra `SendText`. `internal/gateway/chat_test.go`: one events-endpoint case where the stored events include a `context` event and the response body contains `"memoriesCreated":0`.

## Acceptance
- [ ] `go test -race ./internal/adapters/web/ -run TestMessenger` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/comms/ -run TestWebChat` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestChat` passes; paste the output into the PR body.
- [ ] Change DocsRead to a plain int with omitempty -> the zero-serialization messenger test fails
- [ ] Send the context event after the reply text -> the handler ordering test fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope
Executor parsing and counting (A1). Persisting the counts in `chat_turns`. Telegram/Slack rendering of sub-agents. Console and UI changes (contract already live; `console-wire-fields.txt` lists the fields). Throttling or coalescing sub-agent events.

## Refs
- #5810 (request) · #{{A1}} · Navigator TASK-647 in this repo

---

## Out of Scope (doc-level)

- Persisting sub-agent rows or counts for run history (the issue's third question: not needed; a later `chat_turns` migration if the Run Story wants it).
- `memoriesCreated` > 0: chat turns cannot write. If a future turn kind writes memories (TASK-585 P3 writes learnings to SQLite on reject, outside the turn), that producer emits its own count.
- Sub-agent *inner* tool activity in the tree (`parent_tool_use_id` is parsed and ignored by the tree; their `Read`s do count toward docs/memories when the CLI streams them).

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Source of sub-agent lifecycle | stream tool_use/tool_result · `system task_started/task_notification` · Navigator hook | stream tool_use/tool_result | Hooks do not run on tenant boxes; `task_*` system events cover backgrounded tasks only |
| Source of read counts | Read tool_use paths · graph diff · `nav-stats` | Read tool_use paths | Read-only turn: graph diff is always empty, `nav-stats` does not exist in the daemon |
| `memoriesCreated` | omit (UI `—`) · 0 | 0 via `*int` | Truthful; the UI shows `Created 0` instead of a dash |
| Event granularity | one `context` per turn · cumulative per run | one per turn | The UI sums `context` events over the run |
| Wire shape | pointers with omitempty · plain ints | pointers | A real `0` must reach the UI |
| Delivery | through `chatProgressThrottle` · direct | direct | A dropped `running` leaves a row that never appears |

## Verify

```bash
go test -race ./internal/executor/ -run 'TestChatTurn|TestParseStream'
go test -race ./internal/adapters/web/ ./internal/comms/ ./internal/gateway/
```

Live (after the next daemon tag reaches the tenant, `~/bin/pilot-board` for the version): ask a research question on the console Research page; the tree shows rows with DONE and durations; the run line shows numbers, not dashes.

## Done

- [x] A1 merged (PR#6212, 15:07Z) · [ ] post-merge review read
- [ ] B1 merged · post-merge review read
- [ ] Daemon tag on the tenant; live check recorded here
- [ ] Roadmap row P17 → ✅ and TASK-583 status line (both in the private `pilot-console/.agent`); pilot#5810 closed with a pointer to the legs
- [ ] `.agent/system/side-chat-navigator.md` lists six event types

## Refs

- [pilot#5810](https://github.com/qf-studio/pilot/issues/5810) (request, "needs design" — answered above) · UI review D2 on `ui PR#246`
- Roadmap row P17 / U9 — private `pilot-console/.agent/system/project/` roadmap tickets file (moved 2026-11-08, TASK-646)
- `.agent/system/side-chat-navigator.md` (wire) · marker `2026-11-08_am-console-loose-ends-inventory-ticket-page-landed-5810-next.md`
- Dispatched legs:
- A1 → https://github.com/qf-studio/pilot/issues/6210
- B1 → https://github.com/qf-studio/pilot/issues/6214

---

**Last Updated**: 2026-11-08
