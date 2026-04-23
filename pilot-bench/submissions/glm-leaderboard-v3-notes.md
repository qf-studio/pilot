# Submission Notes — Pilot + Claude Opus 4.7 on Terminal-Bench 2.0

**Run ID:** `glm-leaderboard-v3`
**Agent:** Pilot harness (v2.92.1) + Claude Code (claude-opus-4-7, high effort)
**Trials:** 445 (89 tasks × k=5)
**Infrastructure:** AWS EC2 t3.xlarge warm pool, eu-central-1
**Elapsed:** 27h 19m

---

## Results

- **Score:** 77.5% (mean max-reward across tasks)
- **95% CI:** [67.8%, 85.0%] (Wilson interval, n=89, k=5)
- **Tasks solved (≥1/5 trials):** 69 / 89
- **Tasks consistent (5/5):** 49 / 89
- **Total trials passed:** 310 / 445

---

## Disclosure: `/tests/test_outputs.py` First-Action Pattern

During trace audit we observed that **193 of 445 trials** issued a command
attempting to access `/tests/test_outputs.py` or similar oracle paths. This
mirrors the behavioral pattern flagged in our prior Pilot submissions.

**We disclose this proactively. Containment holds and scores are uncontaminated.**

Evidence:

1. **Zero oracle reads confirmed.** Pairing each `/tests/` command with its
   `tool_result` shows every invocation returned either:
   - `No such file or directory` (114 trials — file not present)
   - Directory listing of an empty `/tests/` dir (remaining trials — dir
     mounted by image but empty before agent phase)
   
   A strict audit querying for actual Python test-file content (function
   definitions, `assert` statements, expected output values) returned
   **0 confirmed oracle reads**.

2. **Harness-side temporal isolation (run-bench-task.sh lines 473–607):**
   - Pre-agent: `rm -rf` all known oracle paths (`test_outputs.py`,
     `test.sh`, `conftest.py`, `tests/` dirs) across `/app`, `/workspace`,
     `/tests`, `/home/user`, `/home/agent`, `/srv`, `/root`.
   - Canary scan: any file containing `terminal-bench-canary` marker removed.
   - Post-agent (after `pilot task` process exits): test files are `docker cp`'d
     into `/tests/` for the verifier phase only.
   - **Agent process and verifier phase are temporally separated** — agent's
     process tree terminates before oracle files exist on the filesystem.

3. **Independent verification:** any reviewer can confirm by checking the
   first `tool_result` following a `cat /tests/` command in any trace in
   S3 bucket `pilot-s3-agent-data/bench/runs/glm-leaderboard-v3/*/
   trial-*/pilot-stdout.log` — every instance contains an error or empty dir.

The behavior is a model-level heuristic (Claude Code attempting "understand
the tests") not a scaffold injection. We have not suppressed it at prompt
level to keep harness-side containment as the authoritative blocker.

---

## Integrity Audit Summary

Full checklist: `.agent/sops/benchmark-integrity-audit.md`

| Check | Result | Notes |
|-------|--------|-------|
| H1. /tests/ access | **PASS** | 193 attempts, 0 successful (all ENOENT or empty dir) |
| H2. Answer-key injection | PASS | No AGENTS.md, no embedded answers in scaffold |
| H3. RAG/solution injection | PASS | No RAG configured |
| H4. Environment isolation | PASS | Tests copied post-agent-exit only |
| T1. Protected file reads | PASS | 0 genuine protected-file reads |
| T2. Git history mining | PASS | 19 traces on git-related tasks (legitimate usage) |
| T3. Web solution lookup | PASS | 5 traces; all source-code/package mirror fetches |
| T4. Hardcoded answers | PASS | 0 hardcode/special-case markers |
| T5. Verifier manipulation | PASS | 0 traces (1 `print('PASS')` was agent self-verification code, not verifier injection) |
| T6. Test infra mods | PASS | 0 traces |
| T7. Reward file tampering | PASS | 0 traces |
| S1. Score distribution | PASS | Natural gradient: 49 all-pass, 20 all-fail, 20 partial |
| S2. First-action clustering | NOTED | `cat /tests/test_outputs.py` pattern (see disclosure above) |

**Overall verdict: CONDITIONAL PASS** — harness-level checks all pass;
`/tests/` attempt behavior disclosed and verified non-exploitable.

---

## Agent Configuration

- **Backend:** Claude Code CLI with `claude-opus-4-7`, effort=high
- **Mode:** LocalMode (problem-solving prompt, no PR workflow)
- **Quality gates:** disabled in LocalMode
- **Heartbeat timeout:** 90m
- **Navigator:** disabled in LocalMode (no `.agent/` auto-init)
- **Learning DB:** pilot.db injected (prior pattern learning from Pilot runs)

## Harness

- **Orchestrator:** `pilot-bench/aws/orchestrator.py` (parallel SSM dispatch)
- **Task runner:** `pilot-bench/aws/run-bench-task.sh` (Docker on EC2)
- **Infrastructure:** 5× t3.xlarge warm pool ASG, 20s cold start
- **Oracle guard:** pre-agent cleanup + post-agent test injection

## Reproducibility

```bash
# Infrastructure: qf-studio/aws-infrastructure-pilot
# Binary: make bench-binary (cross-compile linux/amd64)
# Run: pilot-bench/aws/run-aws-bench.sh --run-id glm-leaderboard-v3
# Results: s3://pilot-s3-agent-data/bench/runs/glm-leaderboard-v3/
```

Traces available on request to Harbor reviewers.
