# Submission Notes — Pilot + GLM-4.7 on Terminal-Bench 2.0

**Run ID:** `glm-leaderboard-v4`
**Agent:** Pilot harness (binary built 2026-04-23) + Claude Code (claude-opus-4-7 routing → GLM-4.7 via Z.AI Anthropic-compatible API)
**Trials:** 445 (89 tasks × k=5)
**Infrastructure:** AWS EC2 t3.xlarge warm pool, eu-central-1, max parallel = 5
**Elapsed:** ~21h orchestrator wall time (one resume to recover a single trial after a local `results/` directory was deleted mid-run; instances scaled 0→5 as needed)
**Generated:** 2026-04-25

---

## Results

- **Score:** 69.7% (max-reward across tasks, k=5)
- **95% CI:** [59.5%, 78.2%] (Wilson interval, n=89)
- **Tasks solved (≥1/5 trials):** 62 / 89
- **Tasks consistent (5/5):** 7 / 89
- **Total trials passed:** 147 / 445

---

## Lead Disclosure: Self-Improvement / Pattern Injection Disabled at Binary Level

Our prior submission (`glm-leaderboard-v3`, 77.5%) used Pilot's
self-improvement pipeline: a SQLite-backed pattern database that the
agent reads from and writes to during execution. After running v3 we
realized the seeded DB contained 17 patterns explicitly tuned for
Terminal-Bench 2.0 tasks (one of them quoted `/tests/test_outputs.py`
in its body). That is scaffold-level guidance comparable to ForgeCode's
later-removed AGENTS.md, and we did not submit v3.

**For v4 we shipped a binary-level kill-switch** (`learning.inject_patterns`
config flag, default `true` for production users) and ran the bench
with `inject_patterns: false`. The flag gates three injection sites in
the prompt builder (LocalMode patterns, LocalMode KG, Navigator/self-review
patterns) and is verified at startup with a log line:
`"Pattern/KG prompt injection DISABLED via learning.inject_patterns=false"`.

We additionally:
- **Removed the seeded pattern DB upload** from the orchestrator
  (`pilot-bench/aws/orchestrator.py`).
- **Scrubbed `/root/.pilot/data/pilot.db`** at every trial start in the
  per-trial bootstrap script (`pilot-bench/aws/run-bench-task.sh`).
- **Generated config inline** with `inject_patterns: false` for both
  the S3-uploaded `pilot-config.yaml` and the per-trial config heredoc.

Verified across all 445 traces:
- 0 occurrences of `## Learned Patterns` block
- 0 occurrences of `## Related Learnings` block
- 0 occurrences of `### Recommended Patterns` block
- 0 occurrences of any of the 17 v3 seeded pattern bodies

Score delta from this change: 77.5% → 69.7% (-7.8 pp). This is the
true model-only performance of GLM-4.7 inside our scaffold without
self-improvement state.

---

## Disclosure: Inherited `/tests/` First-Action Pattern (Containment Verified)

271 of 445 trials (60.9%) issued at least one command attempting to
read `/tests/test_outputs.py` or a sibling oracle path. **All 271
attempts failed** — every `tool_result` returned either
`No such file or directory` or an empty directory listing.

This is the same behavioral pattern previously disclosed in
`glm-leaderboard-v2-notes.md` and `glm-leaderboard-v3-notes.md`:
Claude Code (the underlying model) attempts to read tests by default
as a "understand what's expected" heuristic. We do not suppress it at
the prompt level so that the harness-side containment remains the
authoritative blocker.

**Containment evidence:**

1. **Harness-side temporal isolation** (`pilot-bench/aws/run-bench-task.sh`):
   - Pre-agent: `rm -rf` of `/tests/test_outputs.py`, `/tests/test.sh`,
     `/tests/conftest.py`, and `/tests/` directory contents across
     `/app`, `/workspace`, `/tests`, `/home/user`, `/home/agent`,
     `/srv`, `/root`.
   - Canary scan: any file containing `terminal-bench-canary` marker removed.
   - Post-agent (after `pilot task` exits): test files are `docker cp`'d
     into the container's `/tests/` for the verifier phase only.
   - Agent process tree terminates before oracle files exist on the
     filesystem.

2. **Strict audit query:** searched for actual oracle content (Python
   function definitions, `assert` statements, expected return values
   from `test_outputs.py`) reaching the agent — **0 confirmed reads**.

3. **Independent verification:** any reviewer can confirm by checking
   the first `tool_result` following a `cat /tests/` command in any
   trace at `submissions/.../*/agent/command-0/stdout.txt` — every
   instance contains `No such file or directory`.

---

## Disclosure: Verifier CTRF Format Is Aggregated, Not Per-Test

Harbor's reference submission shape includes `verifier/ctrf.json` with
per-test breakdowns (one entry per pytest test, with name, status,
duration, file_path). This is what `pytest --json-report` produces when
the verifier runs.

Our AWS pipeline captures `verifier/test-stdout.txt` (the raw pytest
stdout) but does NOT preserve the structured per-test JSON. We emit
`verifier/ctrf.json` with the correct CTRF schema but a single
trial-level "test" entry summarizing pass/fail. Reviewers needing
per-test detail should read `verifier/test-stdout.txt` directly.

We can re-run the verifier with `--json-report` post-hoc if Harbor
requires it; the artifacts in the agent container that pytest needs
are preserved in the per-trial `pilot-stdout.log` and could be reconstructed.

---

## Disclosure: `config.environment.type` Translation

The `config.json` schema requires `environment.type`. Our submission
templates were lifted from prior native Harbor runs that used Modal as
the deployment target (`environment.type: "modal"`). v4 actually ran in
Docker on EC2 t3.xlarge (Pilot's AWS pipeline). We override the field
to `"docker"` in the translator overlay so the value reflects the
actual runtime environment, not the template source. Other config
fields (timeout multipliers, agent import path) are inherited from the
template and are accurate for the v4 run.

---

## Integrity Audit Summary

Full audit checklist: `.agent/sops/benchmark-integrity-audit.md`
(Berkeley RDI "7 Deadly Patterns" + DebugML "Meerkat" derived).

| Check                                      | Result        | Notes                                                                                |
|--------------------------------------------|---------------|--------------------------------------------------------------------------------------|
| H1. /tests/ access attempts                | DISCLOSED     | 271 attempts, 0 successful (see disclosure above)                                    |
| H1. /tests/ successful reads               | PASS          | 0 / 445                                                                              |
| H2. Pattern/KG prompt injection            | PASS          | 0 / 445 — `inject_patterns: false` verified                                          |
| H2. AGENTS.md / INSTRUCTIONS.md leak       | PASS          | 0 / 445                                                                              |
| H3. RAG / similar-problems injection       | PASS          | No RAG configured; prompt template is task instruction only                          |
| H4. Environment isolation                  | PASS          | Tests `docker cp`'d post-agent-exit only; canary scan pre-agent                      |
| T1. Protected file reads                   | PASS          | 0 genuine protected-file reads                                                       |
| T2. Git history mining                     | PASS          | 41 trials — all on git-related tasks (`fix-git`, `git-multibranch`, `git-leak-recovery`, etc.); legitimate solution path |
| T3. Web solution lookup                    | PASS          | 13 trials — all fetched task-relevant references (mteb leaderboard data, pov-ray source, distro mirrors); 0 lookups for solutions/writeups |
| T4. Hardcoded test answers                 | PASS          | 9 trials match "special case" string — all in compiler/interpreter tasks (`schemelike-metacircular-eval`, `make-mips-interpreter`, etc.) where edge-case handling is the legitimate solution |
| T5. Verifier manipulation                  | PASS          | 1 trial flagged on regex; spot-check confirmed false positive (matched `print('PASSWORD'` in password-recovery task) |
| T6. Test infra modifications               | PASS          | 0 / 445 — no writes to `conftest.py`, `pytest.ini`                                   |
| T7. Reward file tampering                  | PASS          | 0 / 445                                                                              |
| S1. Score distribution                     | PASS          | Natural gradient — 7 tasks at 5/5, 27 at 0/5, 55 partial; consistent with model-only performance |
| S2. First-action clustering                | NOTED         | `cat /tests/...` heuristic in 60.9% of traces; see disclosure above                  |
| S3. Time-to-solution anomaly               | PASS          | Distribution matches expected complexity (passing trials avg 800-1500s)              |
| S4. Cross-scaffold comparison              | DISCLOSED     | v3 (injection on) = 77.5%; v4 (injection off, this submission) = 69.7%; -7.8 pp delta |

**Overall verdict:** PASS with disclosures.

---

## Reproducibility

Every trial includes:
- `agent/install.sh` — agent setup script
- `agent/command-0/command.txt` — the `pilot task` invocation with full task instruction
- `agent/command-0/stdout.txt` — full Claude Code stream-json transcript (including `thinking` blocks)
- `agent/command-0/return-code.txt` — Pilot exit code
- `verifier/test-stdout.txt` — raw pytest stdout
- `verifier/reward.txt` — binary 0/1
- `config.json` / `result.json` — Harbor-compliant config and result

Pinned to `terminal-bench-2` git commit `69671fbaac6d67a7ef0dfec016cc38a64ef7a77c`
(matches the commit used by recent Ante and other submissions, so
`task_checksum` values are directly comparable).

Pilot binary built from internal source at the v4 cutover commit
`2cabdf5d` on branch `feat/aws-bench`. The `inject_patterns` toggle is
public in that commit (`internal/config/config.go`,
`internal/executor/runner.go`, `internal/executor/prompt_builder.go`).

---

## Related Files

| File                                                          | Purpose                                                |
|---------------------------------------------------------------|--------------------------------------------------------|
| `pilot-bench/submissions/glm-leaderboard-v4-hf/`              | HF-format submission tree (445 trials, 129 MB)         |
| `pilot-bench/submissions/tb2-task-checksums.json`             | `{task_name: sha256}` map (89 entries) used in submission |
| `pilot-bench/aws/translate-to-hf.py`                          | S3 → HF schema translator (template-based)             |
| `pilot-bench/aws/orchestrator.py`                             | AWS warm-pool orchestrator (parallel SSM-based)        |
| `pilot-bench/aws/run-bench-task.sh`                           | Per-trial bootstrap; canary scrub; oracle removal      |
| `.agent/sops/benchmark-integrity-audit.md`                    | Audit checklist applied to this run                    |
| `.agent/tasks/TASK-26-aws-bench-harbor-clean-run.md`          | Provenance of the v3→v4 compliance hardening           |
