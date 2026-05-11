---
name: Pilot intake requires specific section headers
description: Pilot's intake judge auto-applies pilot-blocked + pilot-spec-incomplete unless the issue body contains one of a fixed set of H2 headers
type: feedback
originSessionId: a8872db5-b4ff-43ee-ae17-f38dcfa4023a
---
Pilot issue bodies MUST contain at least one of these H2 section headers, or the intake judge auto-applies `pilot-spec-incomplete` + `pilot-blocked` within seconds of creation and the issue won't be picked up.

**Accepted headers:**
- `## Acceptance`
- `## Implementation`
- `## Context`
- `## Background`
- `## Approach`
- `## Design`
- `## Refs`

**Rejected headers (caused the block on #2987):**
- `## Problem`
- `## Plan`
- `## Constraints`
- `## Done`
- `## Investigation pointers`

**Why:** Pilot's intake judge runs a structural-section check on every newly-labeled `pilot` issue. The check is regex-based against the accepted list. Issue body content is otherwise unparsed — section names matter more than content.

**How to apply:**
- When filing any Pilot issue (`gh issue create --label pilot`), use at minimum `## Context`, `## Implementation`, and `## Acceptance` as the structural skeleton — fill the rest of the spec under those headers.
- After filing, verify via `gh issue view <N> --json labels` that only `pilot` is set; if `pilot-blocked` / `pilot-spec-incomplete` appear, rewrite the body with approved headers and `gh issue edit <N> --remove-label "pilot-blocked,pilot-spec-incomplete"`.
- The intake judge re-runs on label changes, so removing the blocking labels after the body is fixed is sufficient — no need to re-create the issue.

**Incident:** TASK-60 / #2987 filed 2026-05-11T12:18:11Z with headers `## Problem`, `## Plan`, etc. Pilot tagged blocked at 12:18:33Z (22 seconds later). Fixed by rewriting body with `## Context` / `## Approach` / `## Implementation` / `## Acceptance` / `## Refs`.
