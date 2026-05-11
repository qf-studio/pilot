---
name: GitHub rate-limit system reminder is unreliable
description: The "GitHub API rate limit exceeded" system reminder fires on heuristics, not actual buckets — verify with gh api rate_limit before pausing
type: feedback
originSessionId: b76c9f9a-f417-4bb5-81bf-b10dc8a044a4
---
A system reminder claiming `GitHub API rate limit exceeded (5,000/hr
shared across all tools and agents)` fires under conditions other
than actual bucket exhaustion. Observed during 2026-05-11 session
after a couple of `gh pr list --search ...` calls; `gh api
rate_limit --jq .resources` returned core 5000/5000 remaining,
graphql 4991/5000 — essentially zero usage.

**Why:** the warning appears to be triggered by query patterns
(particularly search-style queries against the lower search bucket
of 30/hr) or other heuristics, not by the actual rate-limit headers
GitHub returns. The shared-across-tools note is misleading.

**How to apply:** if the system reminder fires, run
`gh api rate_limit --jq .resources` to confirm. Only stop polling
if a real bucket (`core`, `graphql`, `search`, `code_search`) is
genuinely depleted. Do NOT relay the warning to the user as a
factual block — it's a heuristic alert.
