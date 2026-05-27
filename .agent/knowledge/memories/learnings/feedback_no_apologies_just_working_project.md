---
name: no-apologies-just-working-project
description: User does not want apologies or reassurance. User wants a perfectly working project. Verify build/tests after every change, never leave the project broken, no apology theater.
type: feedback
---

User does not want apologies or reassurance language. **The deliverable is a perfectly working project.**

**Why:** Stated explicitly on 2026-05-27 after I broke the build with an unverified wipe and responded with apologies. Apologies don't undo damage; verification before/after each change does.

**How to apply:**

- After any change to the project, run `go build ./...` (and tests where relevant) and confirm exit 0 before reporting done
- Never leave the working tree, main repo, or any branch in a broken state — if a change breaks something, fix it or revert it before yielding control
- Skip "sorry", "apologies", "my mistake" framing. State the technical fact (what broke, what fixed it) and move on
- Verify pre-conditions before destructive ops (pwd, branch, status, build) — don't trust the picture from earlier in the session
- If something breaks, the priority is restoration + verification, not explanation
