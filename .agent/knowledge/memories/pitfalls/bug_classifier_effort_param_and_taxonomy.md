---
name: LLM intent classifier was doubly broken — same effort-param 400 as the llm client (silent regex fallback) AND a taxonomy that routed "draw/outline/show" as a code TASK
description: internal/intent/classifier.go had TWO bugs that made the bot misroute open-ended Slack input. (1) It sent output_config{effort:low}, which Haiku rejects with HTTP 400 "This model does not support the effort parameter" — so every Classify() failed and detectIntent silently fell back to brittle regex (same root as the llm.Client fix #3700/mem-039; a test asserted output_config present, enforcing it). (2) Its prompt treated any action verb as a code TASK, so "draw the schema in ASCII", "outline the intent flow", "show me how X works" all routed to TASK/PLANNING (heavy executor + a pending task) instead of a fast answer. Fixed v2.200.2 (#3703): removed output_config; added a DELIVERABLE TEST (produce an answer/diagram/explanation → question/chat; change code → task; file a ticket → issue_intake). Haiku is now the NL router; regex only guards /-commands. Validated live: 8/8 sample prompts route correctly.
type: pitfall
---
Regex intent detection cannot classify input you can't predict ("how do we classify
with regex when we have no idea what they'll ask?"). The fix is an LLM classifier as the
**primary** NL router — but in this codebase it was **doubly broken**, so turning it on
in config did nothing until both bugs were fixed.

**Bug 1 — the classifier was silently dead (same class as mem-039).**
`internal/intent/classifier.go` built its request with `output_config{effort:"low"}`.
Haiku (claude-haiku-4-5) **rejects** it: `HTTP 400 "This model does not support the
effort parameter."` So **every** `Classify()` errored and `detectIntent` fell back to
regex — the LLM classifier never actually classified anything. (Mirror of the
`internal/llm/client.go` `max_tokens`/`output_config` bug in #3700. And again a test —
`anthropic_test.go` — **asserted `output_config` present**, enforcing the bug. Two files,
two copies of the same broken request shape, both with a test guarding the break.)

**Bug 2 — the prompt taxonomy misrouted "produce output" as "change code".**
The prompt's only distinction was "action verb → task". So Haiku itself (even once
reachable) classified `draw the schema in ASCII` → task (0.9), `outline the intent flow`
→ task, `show me how X works` → task. These are **answer/diagram** requests — the bot
should just reply — but they hit the executor (TASK/PLANNING), spawned worktrees, and
left dangling confirmation/plan tasks.

**Fix (v2.200.2, #3703):**
- Remove `output_config` (send only `model`/`max_tokens`/`system`/`messages`).
- Add a **DELIVERABLE TEST** to the prompt, applied first:
  - Wants the bot to MODIFY files / produce a PR → `task` (or `issue_intake` if they ask
    to file/open/raise a ticket).
  - Wants an ANSWER, EXPLANATION, DIAGRAM, or OPINION → `question` (about code) or `chat`.
    Verbs draw/diagram/show/outline/sketch/visualize/explain/summarize produce an answer,
    NOT a code change → `question`.
- Fix the test: assert `max_tokens` present + `output_config` ABSENT.

**How to apply:**
- For open-ended chat UX, the LLM classifier must be the primary router; regex is only a
  high-precision pre-filter (`/`-commands, maybe operational/greeting). Don't lean on
  regex action-verb heuristics — they misroute everything you didn't foresee.
- Intent taxonomy needs a **deliverable axis** (does the user want changed code, or a
  text/diagram answer?), not just a verb-list. "draw/show/outline a diagram of existing
  code" is a question, not a task.
- **Validate a classifier prompt against the live model with curl** before shipping — the
  daemon logs to the dashboard TUI (ungreppable), so curl the exact request shape + a
  battery of real phrasings and assert the labels. Two API calls beat reading no logs.
- Search the whole codebase for a bad request shape once you find one: `output_config`
  lived in **both** `internal/llm/client.go` and `internal/intent/classifier.go`.

Relates to [[bug_llm_missing_max_tokens]] (mem-039, same effort/output_config family) and
[[bug_sdk_command_action_dropped]] (mem-036, "assert the thing the bug is about").
