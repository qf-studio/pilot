---
name: workflow-yaml-requires-front-matter
description: .pilot/workflow.yaml must be YAML front-matter (--- / version 1 / ---) + Markdown body — plain YAML silently falls back to defaults on EVERY run, and unknown keys are silently ignored (pilot-console ran months with a dead config)
type: pitfall
---

# .pilot/workflow.yaml requires front-matter — plain YAML is silently dead

**What happened (found 2026-07-23):** `pilot-console/.pilot/workflow.yaml`
was plain YAML (`quality.gates` with build/test/lint commands). The loader
(`internal/executor/workflow/workflow.go`) requires **YAML front-matter**:
first line `---`, then `version: 1`, closing `---`, then a Markdown body
that is appended to the executor prompt as "## Project Workflow". The file
never parsed — every console run logged
`Failed to load .pilot/workflow.yaml, using defaults` at WARN and ran
without the repo's overrides. Nobody noticed until the A3 dispatch log was
read closely.

**Two silent layers:**
1. Missing front-matter → parse error → **fallback to defaults**, WARN
   only, execution proceeds. A misconfigured repo behaves identically to
   an unconfigured one.
2. Even inside valid front-matter, the v1 schema only knows `version`,
   `agent.{max_turns,reasoning_effort}`,
   `policy.{commit_format,branch_prefix,pr_template}`, `hooks.*` — unknown
   keys (like `quality.gates`) decode laxly and are **ignored**. Intent
   like "run make build/test/lint" belongs in the Markdown body (prompt
   text), not invented config keys.

**How to avoid:** when authoring or reviewing a repo's workflow.yaml,
verify against the v1 schema in `workflow.go`, and grep the daemon log for
`Failed to load .pilot/workflow.yaml` after the first dispatch on any
newly-onboarded repo — one WARN line is the only signal you get.

**Fix:** pilot-console#32 → PR#33 (merged 2026-07-23) rewrote the file to
valid v1 with gates in the body. Consider a future loader change: fail
loudly (or surface in dashboard) instead of WARN-and-default.
