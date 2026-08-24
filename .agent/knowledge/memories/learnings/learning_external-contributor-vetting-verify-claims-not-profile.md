---
name: external-contributor-vetting-verify-claims-not-profile
description: Scam-vetting external reporters — verify technical claims against code, scan for attack vectors; profile sparseness is not a signal
type: learning
---

# Vet external contributors by verifying claims, not judging profiles

**What happened (2026-08-24):** new account `d3rowy` (6 months old, 3 repos, near-blank profile) batch-filed 4 issues in 2 minutes with leftover "Issue draft N" paste headers — every surface signal said LLM-generated spam. Founder asked twice whether it was a scam. Verification: all four technical claims checked out line-exact against the code (down to regex contents), and a full artifact scan found zero URLs, attachments, scripts, or install commands across everything they posted. Outcome: 4/4 real defects — two fixed by in-house epics, two by the contributor's own merged PRs (sdk#137 → v0.38.0, pilot PR#5207). Second legitimate external contributor after lkshrk (also initially surprising).

**How to apply — the vetting sequence that works:**
1. Artifact scan first (cheap): grep all their issue/comment bodies for URLs, attachment links, executables, base64, install commands, link shorteners. Text-only reports have no attack vector by themselves. (Watch false positives: `.exe` matches inside ".execution".)
2. Verify every technical claim against the code at file:line before acting. Accuracy is the strongest legitimacy discriminator — spam doesn't cite your regex verbatim.
3. Treat their CODE (PRs) with full review rigor regardless of report quality — report accuracy earns trust for claims, not for diffs.
4. Do NOT weight: account age, follower count, profile completeness, LLM-assisted authoring artifacts. Real deployers of a niche tool often have all of these "red flags".

Related: [[external-fork-pr-sweeps-stale-agent-state]], [[github-issues-search-returns-prs]].
