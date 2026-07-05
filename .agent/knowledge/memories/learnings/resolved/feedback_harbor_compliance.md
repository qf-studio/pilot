> **RESOLVED/SUPERSEDED (2026-07-05):** Codified in sops/benchmark-integrity-audit.md (H1-H3); prompt fix merged #2392

---
name: Harbor compliance — no oracle access ever
description: NEVER give bench agent access to test files before or during execution. Harbor flagged us once, cannot happen again.
type: feedback
originSessionId: aeaf0f5e-b335-4bda-bd0e-ec1e09d65a61
---
NEVER allow the bench agent to access oracle test files (test_outputs.py, test.sh, filter.py from /tests/) during execution. Harbor flagged Pilot for test-peeking — this is a hard rule.

**Why:** Harbor disqualified our 82.9% #1 submission because the agent could read oracle tests. Three violation vectors: (1) test files copied to /tests/ before execution, (2) quality gates running pytest during execution, (3) bootstrap context dumping test file contents. All three had to be removed.

**Plus a fourth vector found 2026-04-25:** the **prompt itself** can be a violation. `internal/executor/prompt_builder.go:321` (`buildLocalModePrompt`) instructed the agent to `cat /tests/test_outputs.py`. Even when containment removed the file at runtime, the prompt language itself is a scaffold-level oracle hint comparable to ForgeCode's AGENTS.md (which got their score adjusted -10pp). Pilot's source code is public — Harbor reviewers WILL read it. Fixed in qf-studio/pilot#2392 (Apr 2026).

**How to apply:** Before ANY bench run, verify:
1. Test files are NOT copied into the container before pilot runs
2. Quality gates that reference /tests/ are disabled
3. Bootstrap context does NOT dump test file contents
4. Docker image's /app/ has oracle files (test_outputs.py, test.sh) REMOVED before agent starts
5. Canary-marked files (`terminal-bench-canary`) grepped and deleted
6. Test files are copied ONLY for the verifier step AFTER pilot exits
7. **Source-grep**: `grep -rE "/tests|test_outputs|test\.sh|conftest" internal/executor/` returns 0 hits in any prompt-construction code. Pilot's code is public; the prompt template counts as a scaffold instruction.

Check `run-bench-task.sh` for any `docker cp` to container before the pilot execution block. If found, it's a violation.
