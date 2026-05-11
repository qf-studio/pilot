---
name: Bench run env var discipline
description: orchestrator.py _generate_pilot_config reads ANTHROPIC_BASE_URL at runtime; a stray shell value silently swaps GLM→Anthropic and breaks runs
type: feedback
originSessionId: 960d8148-1f70-4b92-899b-0662ff0d8ae6
---
When launching `pilot-bench/aws/orchestrator.py` for a GLM run, ALWAYS set
`ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic` inline on the command.
Never rely on shell state.

**Why:** `_generate_pilot_config()` reads the env var with fallback to z.ai.
A shell with `ANTHROPIC_BASE_URL=https://api.anthropic.com` (common when working
on Claude Code itself) silently rewrites the bench config to route to Anthropic,
which doesn't serve `glm-5.1` — resumed trials would 100% fail with API errors
AND contaminate the submission by mixing providers. Near-miss on 2026-04-21
during glm-leaderboard-v2 resume: caught by byte-level diff against the
1165-byte config that trials 1-303 ran under. Zero trials dispatched.

**How to apply:**
- For GLM runs, prefix launches with `ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic`.
- Before any resume, diff the regenerated config against a known-good version
  (use S3 bucket versioning — `pilot-s3-agent-data` has versioning enabled,
  old versions recoverable via `aws s3api list-object-versions`).
- S3 config should be 1165 bytes for GLM-5.1 runs. Anything else is drift.
- `_upload_assets()` re-uploads all assets on every run (binary, db, config,
  runner script, manifest) — this is expected and safe as long as local
  content matches what the prior trials ran under.
