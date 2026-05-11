---
name: Harbor agent.env serializes secrets into trial output
description: Anything in Harbor's AgentConfig.env (--ae flag or _build_env() return) is written verbatim into config.json/result.json and ends up in published submissions
type: feedback
originSessionId: fef64976-4845-475b-ac32-02096b3b2b04
---
Harbor's `AgentConfig` model has `env: dict[str, str]` with **no masking**.
Whatever ends up in that dict is serialized verbatim into both
`config.json` and `result.json` for every trial — files which then get
committed to leaderboard submission repos. We leaked 4,379 local files
and 5+ distinct OAuth tokens this way before noticing.

**Why:** `pilot_agent/agent.py:_build_env()` was capturing
`CLAUDE_CODE_OAUTH_TOKEN` from `os.environ` and returning it. Harbor
serialized that dict into output. Same hazard with `harbor run --ae KEY=VAL`
on the CLI.

**How to apply:**
- NEVER add auth secrets (`*_API_KEY`, `*_TOKEN`, `*_OAUTH_*`) to
  `_build_env()` in `pilot-bench/pilot_agent/agent.py`.
- NEVER use `harbor run --ae` for tokens — pass them via the ambient
  process environment (`source .env`, `export VAR=...`) and let the
  agent subprocess inherit them.
- The `pilot-bench/aws/translate-to-hf.py` writer has defensive
  `redact_secrets()` on every disk write as belt-and-suspenders. Keep it.
- `pilot-bench/jobs/` is gitignored, but a translator that copies its
  contents to a public submission would re-publish the leak — always
  scrub before publishing.

**Fixed in:** `agent.py` (removed token keys), `pilot-bench/run.sh`,
`pilot-bench/README.md`, `.agent/sops/daytona-bench-operations.md`,
`.agent/sops/development/pilot-bench-real-binary.md` (all `--ae` token
patterns replaced with ambient-env guidance + warning comment).
