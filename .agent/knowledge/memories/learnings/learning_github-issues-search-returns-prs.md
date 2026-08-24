---
name: github-issues-search-returns-prs
description: GitHub's issues-search API returns PRs too — a "filed an issue" classification can hide an unreviewed open PR for days
type: learning
---

# Issues-search results include pull requests

**What happened (2026-08-24):** external contributor d3rowy's studio-sdk item #137 was classified as "their sdk issue" from a `search/issues?q=author:...` sweep on 08-23. It was actually an open PR — it sat unreviewed for a full day (with fork CI never approved to run) until the founder spotted a fork commit referencing it. The pilot-side leg was blocked on it the whole time.

**Why:** GitHub's REST `search/issues` and the issues timeline treat PRs as issues. Nothing in the default output fields distinguishes them unless you request/inspect `pull_request`.

**How to apply:** when sweeping external contributions with `search/issues` or `gh issue list`-style queries, always check for the `pull_request` key (or run a parallel `gh pr list --author`) before classifying an item as "just an issue". An open PR from an external contributor is time-sensitive in two ways an issue is not: fork CI needs maintainer workflow approval to even run, and the contributor is blocked waiting.

Related: [[external-fork-pr-sweeps-stale-agent-state]] (first external contributor arc).
