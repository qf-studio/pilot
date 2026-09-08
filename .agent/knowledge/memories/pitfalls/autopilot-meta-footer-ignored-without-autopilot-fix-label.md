---
name: autopilot-meta-footer-ignored-without-autopilot-fix-label
description: cmd/pilot/handlers.go parses the <!-- autopilot-meta branch/pr/sha --> footer ONLY when the issue carries the literal autopilot-fix label — a hand-filed revision issue (pilot label + footer, per the never-reads-comments decision) gets branch pilot/GH-<issue>, no --from-pr, no SHA fallback, and leaves a stray branch
type: pitfall
---

# autopilot-meta footer ignored without the `autopilot-fix` label

**Evidence (2026-09-08):** `cmd/pilot/handlers.go:807-828` — `branchName =
pilot/GH-<n>` unless `label == "autopilot-fix"`, in which case
`parseAutopilotBranch/PR/SHA(ev.Body)` apply. Revision issues #5361/#5374
(footer `branch:pilot/GH-5351 pr:5356`) were filed with `pilot` only →
poller logged `branch=pilot/GH-5374`; a stray `pilot/GH-5374` branch at
main's HEAD was created (deleted by hand); the executor pushed to the right
branch only because the prose said so.

**Until pilot#5379 lands:** when hand-filing a revision issue, ALSO add the
`autopilot-fix` label? — NO: that label carries CI-fix continuation
semantics. Instead keep the prose instruction ("check out branch X at its
head, commit on top, no new PR") explicit, and delete any stray
`pilot/GH-<issue>` branch afterwards. Related decision:
`pilot-never-reads-gh-comments-by-design`.
