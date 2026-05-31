---
name: Pilot intake spec-guard requires a recognized ## section header
description: Filing a pilot-labeled GitHub issue without one of the ValidateSpec section headers gets it auto-labeled pilot-spec-incomplete then pilot-blocked within two poll cycles. Use ## Context/Approach/Acceptance/etc.
type: learning
---

When you `gh issue create --label pilot ...`, Pilot's intake **spec-guard** (`internal/adapters/github/spec_validator.go` `ValidateSpec`, applied via `cmd/pilot/spec_guard.go applySpecGuard`) validates the body **before** dispatch. It is a **two-strike** gate:
1. First poll with a failing body → adds `pilot-spec-incomplete` + posts a marker comment (`<!-- pilot-spec-incomplete -->`).
2. Next poll (marker present, still failing) → escalates to `pilot-blocked`.

Both can land within ~2 minutes, so a freshly-filed issue silently ends up `pilot, pilot-spec-incomplete, pilot-blocked` and Pilot looks "idle with nothing in the queue."

**The three rules (`ValidateSpec`):**
- body ≥ **100 chars** (after trim),
- not solely a `Parent: GH-NNN` line,
- **must contain a header matching** `(?im)^##\s+(Acceptance|Implementation|Context|Background|Approach|Design|Refs)\b`.

The third one bit a whole batch (TASK-335..340 / #3326-3331, 2026-05-31): bodies used `## Problem / ## Fix / ## Test / ## Scope` — none match the regex → all 6 blocked despite being detailed. Headers like `## Problem`, `## Fix`, `## Summary`, `## Goal` are NOT recognized.

**How to apply — when filing any `pilot` issue, lead the body with recognized headers:**
```
## Context        <!-- the problem + file:lines -->
## Approach       <!-- the fix -->
## Acceptance     <!-- - [ ] checkboxes from the test strategy -->
## Refs           <!-- audit/task-doc links, coordination notes -->
```
`## Context`, `## Approach`, `## Acceptance`, `## Implementation`, `## Background`, `## Design`, `## Refs` all pass; at least one is required.

**Recovery if already blocked:** edit the body to add a recognized header, then remove BOTH `pilot-spec-incomplete` and `pilot-blocked` (`gh issue edit N --remove-label ...`). The marker comment is inert once the body validates (the guard only fires on invalid bodies). Escape hatch: the `pilot-skip-spec-check` label bypasses validation entirely — use sparingly, prefer fixing the body. Related: [[learning_flaky_briefs_generator_test]].
