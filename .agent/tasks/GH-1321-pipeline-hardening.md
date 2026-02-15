# GH-1321: Pipeline Hardening — External Correctness Checks

**Status**: 🚧 In Progress
**Created**: 2026-02-15
**Assignee**: Pilot

---

## Context

All quality mechanisms check internal consistency but nothing checks external correctness. This caused 5 bugs to ship in v1.9.0 (GH-1316). Adding 4 targeted checks at optimal pipeline layers.

## Bug Categories Addressed

| # | Bug Type | Fix | Layer |
|---|----------|-----|-------|
| 1 | Wrong constants | Pre-commit instruction + self-review flag | Prompt |
| 2 | Missing backend parity | Self-review sibling comparison | Prompt |
| 3 | Missing test coverage | Strengthen prompt + coverage-delta gate | Prompt + Gate |
| 4 | Dropped features | Intent judge check + parity | Prompt |

## Files

- `internal/executor/prompt_builder.go` — Pre-commit items 4-6, self-review checks #6-#7
- `internal/executor/intent_judge.go` — Add check #4 to system prompt
- `internal/executor/workflow.go` — Add VERIFY item #5
- `scripts/coverage-delta.sh` — New: coverage delta gate script
- `internal/executor/prompt_builder_test.go` — Tests
- `internal/executor/intent_judge_test.go` — Tests

## Key Design Decisions

- Single issue (not decomposed) because multiple changes touch `prompt_builder.go`
- Coverage delta gate is `GateCustom` type, opt-in via config — no new Go types needed
- Self-review checks are advisory (flag/fix, don't block)
- Intent judge stays within 512 token budget (1 line added to prompt)

---

**Last Updated**: 2026-02-15
