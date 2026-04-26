# TASK-27: AWS Bench v5 — Compliance-Clean Run + Roadmap to 85%

**Status**: 🚧 In Progress (v5-smoke staging complete, awaiting launch)
**Created**: 2026-04-26
**Completed**: —
**Branch**: `feat/aws-bench`
**Predecessor**: TASK-26 (`glm-leaderboard-v3` 77.5%, `glm-leaderboard-v4` 69.7%)

---

## Goal

Land a Harbor-compliance-clean Terminal-Bench 2.0 submission at ≥80%, with
a stretch target of 85–86% (vs current top: ForgeCode 81.8%). Use Claude
Code subscription (Opus 4.7 / Sonnet 4.6 routed by complexity), parallel=2
to stay under daily/weekly subscription caps.

Full roadmap reasoning: `~/.claude/plans/glittery-launching-hartmanis.md`.

---

## Why v4 cannot be submitted

Audit during this session found `internal/executor/prompt_builder.go:321`
literally instructed the agent to `cat /tests/test_outputs.py` in our own
prompt. That's a scaffold-level oracle-access instruction in open-source
code. Even though containment held (0 successful reads across 445 trials),
Harbor reviewers reading our public source would flag it the same way they
flagged ForgeCode's AGENTS.md (`-10 pp`). The 271/445 `/tests/` access
attempts disclosed in `glm-leaderboard-v4-notes.md` are not Claude Code's
default behavior — they were caused by our prompt.

Fixed in `qf-studio/pilot#2394` (commit `c5551651` on main). Vendored onto
`feat/aws-bench` as commit `44d4c995`.

---

## v5 changes shipped this session (5 commits on `feat/aws-bench`)

| SHA | Description |
|---|---|
| `8299a798` | Token-leak fix + Harbor v4 compliance (`pilot_agent/agent.py:_build_env` no longer captures auth into Harbor-serialized env dict; `--ae` patterns removed from `run.sh` + SOPs) |
| `38bdc7e6` | HF translator (`pilot-bench/aws/translate-to-hf.py`, 824 lines) + v4 submission notes (held, not submitted) |
| `44d4c995` | Phase 0 vendor: oracle-path fix from main #2394 onto feat/aws-bench |
| `1f7e0038` | v5 config swap: subscription auth (no Z.AI), `model_routing: true`, `quality: true`, default model `claude-opus-4-7` |
| `a438a4b7` | DetectTestCommand cherry-pick from main `457d79b9` (auto-detects pytest/npm/cargo/go test; gate skips when no runner) |

---

## v5-smoke launch plan

**Status**: pre-flight complete (2026-04-26), launch held pending user go-ahead.

### S3 assets (uploaded 2026-04-25 20:31–20:32 UTC)

| Key | Status |
|---|---|
| `bench/assets/pilot-linux-amd64.gz` | v5 binary `v2.92.1-127-ga438a4b7`, 8.0 MB, contains "Discover the spec" + `DetectTestCommand`, 0 oracle-path strings |
| `bench/assets/run-bench-task.sh` | subscription auth via `CLAUDE_CODE_OAUTH_TOKEN`, no Z.AI proxy, no `ANTHROPIC_AUTH_TOKEN` |
| `bench/assets/pilot-config.yaml` | `model_routing: true`, `quality: true`, `inject_patterns: false`, no `api_base_url` (subscription default) |
| `bench/assets/tasks-manifest.json` | unchanged from v4 (89 tasks) |
| `bench/assets/pilot.db` | stale (2026-04-22, 80 KB) — not deletable from CLI (IAM lacks `s3:DeleteObject`); harmless because `run-bench-task.sh:194` does `rm -f /root/.pilot/data/pilot.db` at trial start |

### Infra (verified 2026-04-26)

- ASG `pilot-agent-pool`: min=0, max=5, desired=0 (idle)
- SSM secrets:
  - `/pilot/CLAUDE_CODE_OAUTH_TOKEN`: 108 chars (subscription OAuth)
  - `/pilot/GITHUB_TOKEN`: 93 chars
  - `/pilot/GOLDEN_AMI_ID`: 21 chars
- KMS key for SSE-KMS: `arn:aws:kms:eu-central-1:529088297614:key/b76e52f4-dc3d-4056-9ba7-05a1e1e435a8` (must use `--sse aws:kms --sse-kms-key-id <key>` for direct CLI uploads; orchestrator uses bucket-default which requires different perms)

### Smoke task selection (5 tasks × k=5 = 25 trials)

| Task | v4 result | Reason for inclusion |
|---|---|---|
| `chess-best-move` | 0/5 | Zero-pass v4 — does subscription/router/gates fix it? |
| `build-cython-ext` | 5/5 | Regression check for compile-heavy tasks |
| `fix-git` | 4/5 | Quality-gate effect on partial-pass git tasks |
| `sam-cell-seg` | 5/5 | ML regression check |
| `mteb-retrieve` | 0/5 | Zero-pass v4 — does subscription change it? |

### Launch command

```bash
nohup env AWS_PROFILE=quantflow AWS_DEFAULT_REGION=eu-central-1 \
  python3 pilot-bench/aws/orchestrator.py \
  --run-id glm-leaderboard-v5-smoke \
  --tasks chess-best-move,build-cython-ext,fix-git,sam-cell-seg,mteb-retrieve \
  --k-trials 5 --max-parallel 2 \
  --model claude-opus-4-7 \
  > /tmp/bench-v5-smoke.log 2>&1 < /dev/null &
disown
```

Expected wall-clock: ~3h. ASG scales 0→2 then back. Cost: subscription tokens.

### Acceptance criteria for v5-smoke → v5-full

1. `pilot_failed` rate < 30% (v4 was 88.6% pilot_failed — wall-clock timeouts; v5 should drop sharply with router/Sonnet downgrade for easy tasks)
2. Quality-gate retries fire on at least one trial (verifies `quality.enabled: true` is wired)
3. Trial logs show `Pattern/KG prompt injection DISABLED` startup line
4. No prompt strings naming `/tests/` in any trial transcript
5. Score on the 5 smoke tasks is comparable to or better than v4's mix (v4: chess 0, cython 5, fix-git 4, sam 5, mteb 0 → 14/25 = 56%)

If smoke passes all 5 criteria → kick off v5-full (445 trials, ~2 days at parallel=2).

---

## Submission gate (user-confirmed)

Ship the first run that hits **≥80% AND** passes a clean compliance audit:
1. Source-grep `/tests|test_outputs|test\.sh|conftest` in `internal/executor/` returns 0.
2. SOP audit `.agent/sops/benchmark-integrity-audit.md` H1–T7 + S1–S4 on translated traces.
3. Trial-log line `Pattern/KG prompt injection DISABLED` present in every trial.
4. Translator `pilot-bench/aws/translate-to-hf.py` runs with 0 token leaks.

If first compliance-clean run lands at 80–84% — submit immediately. Iterate
toward 85% on a separate branch as fast-follow.

---

## Pending (post-smoke)

Phase 1 quick wins (already applied for v5):
- ✅ Phase 0: oracle-path-clean prompt
- ✅ Phase 1A: `quality.enabled: true` + `DetectTestCommand` (auto-skips on workspaces without runner)
- ✅ Phase 1B: subscription Opus 4.7 (no Z.AI proxy)
- ⏳ Phase 1C: loop detection (post-v5-smoke; file Pilot issue if v5 still shows stuck-in-loop timeouts)
- ⏳ Phase 1D: planning hard gate (post-v5-smoke; file Pilot issue if smoke shows agents skipping plan-output step)

Phase 2 (target 80–84%):
- Reasoning sandwich: Opus for RECON + RECOVERY phases, Sonnet for IMPLEMENT phase

Phase 3 (target 84–86%):
- Trial-isolated learning (workspace `.notes/` directory, no cross-task persistence)
- Per-task timeout tuning based on v5-full distribution

---

## Key files

| File | Purpose |
|---|---|
| `pilot-bench/aws/orchestrator.py` | AWS warm-pool orchestrator |
| `pilot-bench/aws/run-bench-task.sh` | Per-trial bootstrap (subscription auth, oracle scrub) |
| `pilot-bench/aws/translate-to-hf.py` | S3 → HF TB2 schema translator |
| `pilot-bench/submissions/tb2-task-checksums.json` | 89-entry `{task: sha256}` map at TB2 commit `69671fbaac6d67a7ef0dfec016cc38a64ef7a77c` |
| `pilot-bench/submissions/glm-leaderboard-v4-notes.md` | v4 notes (held, scheduled to be superseded by v5) |
| `internal/executor/prompt_builder.go:289+` | `buildLocalModePrompt` (Phase 0 fix applied) |
| `internal/quality/types.go:DetectTestCommand` | Test-runner auto-detection |
| `~/.claude/plans/glittery-launching-hartmanis.md` | Full roadmap with delta projections |

## Related memory

- `feedback_harbor_compliance.md` — 4 violation vectors (oracle file copy, quality gates referencing /tests/, bootstrap context dumping tests, **prompt language naming oracle paths** — found in this session)
- `feedback_harbor_env_leaks_secrets.md` — never put auth into `_build_env()` or `harbor run --ae`
- `feedback_show_card_with_sop.md` — always run `bench-status.py`
- `feedback_dont_run_tests_without_asking.md` — never launch bench autonomously
