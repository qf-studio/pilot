# Submission Notes — Pilot + GLM-5.1 on Terminal-Bench 2.0

**Run ID:** `glm-leaderboard-v2`
**Agent:** Pilot harness + Claude Code (GLM-5.1 via Z.AI Anthropic-compatible API)
**Trials:** 445 (89 tasks × k=5)
**Infrastructure:** AWS EC2 t3.xlarge warm pool, eu-central-1

---

## Disclosure: `/tests/test_outputs.py` First-Action Pattern

During trace audit we observed that **151 of 445 trials (34%)** begin with the
agent issuing `cat /tests/test_outputs.py` as its first Bash action. This
mirrors a behavioral pattern flagged in a prior Pilot submission that was
invalidated for oracle-test access.

**We are disclosing this proactively because containment holds and scores are
uncontaminated.** Evidence:

1. **All 151 attempts failed.** Pairing each attempt with its tool_result
   shows every invocation returned `No such file or directory` (exit code 1).
   Zero bytes of oracle test content reached the agent.

2. **Harness-side containment (run-bench-task.sh):**
   - Before the agent phase: `rm -rf /app/tests/` and removes any file
     containing the `terminal-bench-canary` marker.
   - Oracle test files (`test_outputs.py`, `test.sh`) are **only** copied
     into the container (to `/tests/`) **after** `pilot task` has exited
     and returned (see `run-bench-task.sh` around line 567,
     `docker cp "$TASK_DIR/tests/*" "$CONTAINER_NAME:/tests/"`).
   - Agent container and verifier phase share one container but are
     separated temporally by process exit; the agent's process tree
     terminates before test files are present on the filesystem.

3. **Strict audit query** — looking only at root `/tests/test_outputs.py`,
   `/tests/test.sh`, `/tests/conftest.py` paths accessed during the agent
   phase, partitioned by tool_result: **blocked=160, successful=0**.

4. **Independent verification suggested:** reviewers can confirm by grepping
   any trace in `results/glm-leaderboard-v2/*/*/pilot-stdout.log` for the
   first `tool_result` following a `cat /tests/` — every instance contains
   `No such file or directory`.

The behavior appears to be an inherited prior from Claude Code / the
underlying model attempting a common "understand the tests" heuristic. We
have not attempted to suppress the behavior at the agent prompt level so
that the harness-side containment remains the authoritative blocker and
this audit is reproducible.

---

## Integrity Audit Summary

Full audit checklist: `.agent/sops/benchmark-integrity-audit.md`
(Berkeley RDI "7 Deadly Patterns" + DebugML "Meerkat" derived).

| Check | Result |
|-------|--------|
| H1. /tests/ access | PASS — 151 attempts, 0 successful |
| H2. Answer-key injection | PASS — manifest/config clean |
| H3. RAG/solution injection | PASS — no RAG configured |
| H4. Environment isolation | PASS — tests copied post-agent-exit only |
| T1. Protected file reads | PASS — 0 genuine protected-file reads |
| T2. Git history mining | PASS — 17 on git-related tasks (legit); non-git tasks hit empty histories (single baseline commit) |
| T3. Web solution lookup | PASS — 526 external URLs, all source-code/package mirrors |
| T4. Hardcoded answers | PASS — 0 hardcode/special-case markers |
| T5. Verifier manipulation | PASS — 0 traces |
| T6. Test infra mods | PASS — 0 traces |
| T7. Reward file tampering | PASS — 0 traces |
| S1. Score distribution | PASS — natural gradient (62 all-pass, 23 all-fail, 4 partial) |
| S2. First-action clustering | NOTED — `cat /tests/test_outputs.py` pattern (see above) |

---

## Results

- **Total trials:** 445 / 445 (100%)
- **Pass rate:** 74.2% (95% CI: 64.2–82.1%)
- **Tasks solved (≥1 trial):** 66 / 89
- **Tasks consistent (5/5):** 62 / 89
- **Elapsed:** 52h 24m (initial + resume + targeted prove-plus-comm batch)

### Run recovery notes

The initial run died mid-way after completing 303 trials. Resume was
performed via `orchestrator.py --resume` (skips trials with existing
`reward.txt` in S3, never re-dispatches completed work). The resumed batch
dispatched the remaining 142 trials under byte-identical config
(`pilot-config.yaml` size 1165 B, verified against the pre-resume S3
version). One task (`prove-plus-comm`) required a WORKDIR compatibility fix
to the task runner (image used `/workspace` rather than `/app`); the fix is
a symlink fallback that does not alter Harbor containment semantics
(oracle-file removal and canary scanning operate through the symlink on
the real workdir contents).
