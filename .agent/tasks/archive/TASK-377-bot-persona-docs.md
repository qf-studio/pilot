# TASK-377: Bot Module Phase 5 — persona, voice scaffold, docs

**Status**: ✅ SHIPPED 2026-06-26 — #3673 closed `pilot-done`
**Created**: 2026-06-26
**Assignee**: Pilot (queued)
**Parent plan**: `/Users/aleks.petrov/.claude/plans/there-is-a-problem-inherited-fiddle.md`
**Depends on**: TASK-374, TASK-375, TASK-376.

---

## Context

Finalize the bot's identity and document the new fast-vs-executor path. Voice
(Telegram/Slack calls) is deferred — this phase only ensures the seam exists.

---

## Acceptance Criteria

- [ ] `bot.persona` honored across `Chat`/`Answer`/`DraftIssue` system prompts; sensible default if unset.
- [ ] `bot.voice.enabled` flag present (scaffold only). Confirm the comms chokepoint already consumes `IncomingMessage.VoiceText` so transcribed voice flows through the same intent→responder path; no call wiring in v1.
- [ ] `bot.rate_limit` reuses the existing `RateLimiter` to bound per-context LLM calls.
- [ ] Docs: `configs/pilot.example.yaml` `bot:` block fully commented; a `.agent/knowledge/memories/patterns/` entry "fast conversational path vs executor path" registered in `graph.json` (reference the mem-036 test-at-the-seam lesson).
- [ ] `.agent/DEVELOPMENT-README.md` Current State refreshed with the bot module.

---

## Out of Scope
- Actual voice/call transport (future task).

## Verify
```bash
go build ./... && make lint && make test
```
Live regression: set `bot.enabled: false`, restart → chat/question/intake revert to
the executor path (zero-regression guard).

## Done
- [ ] Persona applied; voice flag scaffolded; rate limit active.
- [ ] Knowledge entry + example config + Current State updated.

## Refs
- Parent plan; mem-036 (`pitfalls/bug_sdk_command_action_dropped.md`).

**Last Updated**: 2026-06-26
