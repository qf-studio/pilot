---
name: check-state-before-designing
description: When asked to design a recurring check, monitor, or workflow, FIRST run the check once and look at the result. If the result is empty/trivial, lead with that fact, not with the design.
type: feedback
---

When the user asks to design a recurring check, monitor, polling loop, or similar workflow, **run the check once first and look at the actual result**. Then decide whether the workflow is even worth building.

**Why:** Stated on 2026-05-27. User asked about setting up a recurring Pilot queue check. I'd already run the queries earlier in the session and seen the queue was empty (0 open PRs, all Wave 4 shipped, daemon down). Instead of leading with "queue is empty, loop is pointless," I produced paragraphs of loop-body design — work generated for a non-problem. User had to push back twice.

**How to apply:**

- Before describing what a recurring check would *do*, run the check once and report the current result
- If the result is empty / trivial / "nothing happening," lead with that. Don't proceed to design unless asked
- Treat "design me a monitor" as "tell me whether monitoring is needed right now" first, design second
- Apply the same to any "let's automate X" request — check whether X is happening at all before automating it
