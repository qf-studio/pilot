# Pitfall: GitHub primary rate limit is per-USER across all tokens — /rate_limit's per-token view actively misleads

## Summary
2026-07-16/17 incident: the AWS-hosted daemon's pollers 403'd "API rate limit exceeded for user ID 5360806" while its own token's `/rate_limit` showed `5000/5000 used=0`. GitHub ENFORCES the primary core limit aggregated per user (all PATs + OAuth tokens + every machine/session of that user), but the `/rate_limit` endpoint reports a per-token/app view. Consumers sharing the pool that day: the daemon's startup rescans (720h merged-PR window × 11 repos), the operator's laptop gh, two parallel Claude sessions, and an earlier re-pick storm. Result: 67+ min dispatch freeze that looked like a daemon bug.

## Recommended Approach
- Never diagnose rate state via /rate_limit alone — read X-RateLimit-* headers off real API responses.
- Budget-aware client with a poller reserve is the durable fix (GH-4391); GitHub App installation auth (own pools, decoupled from the human user) is the strategic fix — also relevant to hosted tenants (BYO-PAT shares the customer's pool with their own dev activity).
- Every agent/session on the machine drinks from the same pool: prefer sqlite/metrics/SSM surfaces over gh for status (pilot-board exists for exactly this).

## Related
- Issues: GH-4391 (budget-aware client), GH-4392 (the co-occurring orphan-claim freeze)
- Skill: .claude/skills/pilot-aws (rule 3) · Task: TASK-409
