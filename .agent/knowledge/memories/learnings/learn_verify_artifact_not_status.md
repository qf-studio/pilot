---
name: Verify the artifact, not the status — Pilot done-states are claims
description: GH-3513 read pilot-done while main had 0% of the feature; the TUI showed children "running 100%" with orphaned PRs; v2.181.0 was tagged off a sibling branch. Before trusting any completion signal, grep main for the expected change.
type: learning
originTask: TASK-361
---

During the GH-3513 incident every status surface lied simultaneously:

- Issue #3513: closed `pilot-done` ("✅ PR merged successfully! Time to merge 52s")
- TUI queue: children "● running … 100%"
- Release: `v2.181.0` published with full assets

Ground truth at that moment: `main` had **zero** of the feature. The one
merged PR (#3520) had `baseRefName: pilot/GH-3515` — merged into a **sibling
branch** — and the release tag (`aa3b1e82`) was `diverged` from main
(`gh api repos/OWNER/REPO/compare/main...TAG` → status ≠ identical/behind).

**Verification recipe (cheap, definitive):**

1. Pick a signature change the feature must make (e.g. a function gaining a
   param) and grep main's blob:
   `gh api repos/O/R/contents/<file> --jq .content | base64 -d | grep -c '<new sig>'`
2. For any suspicious merged PR: `gh pr view N --json baseRefName` — base must
   be `main`.
3. For any suspicious tag: `compare/main...<sha>` — `diverged`/`ahead` means
   the release contains code main doesn't have (phantom release).

**Why:** status labels, dashboard percentages, and even "merged" are produced
by the same machinery that may be broken. The artifact (code on main) is the
only signal that can't lie.

Related: [[bug_false_supersession_label_trust]], [[learn_verify_write_callsite_before_fix]]
