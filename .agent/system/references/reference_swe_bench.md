---
name: SWE-bench Benchmark Research
description: Comprehensive SWE-bench research — variants, leaderboard scores (March 2026), submission process, scoring, controversies, competitive analysis for Pilot.
type: reference
---

## SWE-bench Overview

Created by Princeton NLP (ICLR 2024 Oral). Real GitHub issues → generate patch → verified by repo test suite in Docker.

## Variants

| Variant | Instances | Notes |
|---------|-----------|-------|
| Full | 2,294 | 12 Python repos, noisy |
| Lite | 300 | Curated subset, most common for initial submissions |
| Verified | 500 | Human-validated by 3 annotators (with OpenAI), most-cited |
| Pro | harder | Less saturated, where differentiation happens |
| Multilingual | 300 | 9 languages, 42 repos |
| Multimodal | 617 | Issues with screenshots/UI |

## Leaderboard (Verified, March 2026)

- Claude Opus 4.5: 80.9%
- Claude Opus 4.6: 80.8%
- Gemini 3.1 Pro: 80.6%
- MiniMax M2.5: 80.2%
- GPT-5.2: 80.0%
- Claude Sonnet 4.6: 79.6%
- Claude Code standalone: ~58% (Lite)

Scaffold adds 4-10 points. Augment's Auggie beats Claude Code by 17 instances on same model.

## Scoring

- **pass@1**: 1 prediction per instance (standard)
- **Resolved**: ALL fail-to-pass tests flip AND zero regressions in pass-to-pass
- **best@k**: Multiple attempts + selection module (accepted)
- **pass@k**: Best of k with test oracle (NOT accepted)

## Repos (12 Python)

Django 37%, sympy 19%, scikit-learn 8%. Top 3 = 64% of all tasks.
Also: sphinx, matplotlib, pytest, xarray, astropy, pylint, requests, seaborn, flask.

## Submission Process

1. Generate JSONL: `{"instance_id", "model_patch", "model_name_or_path"}`
2. Evaluate via `sb-cli` or Docker harness
3. Write technical report (mandatory, strict quality)
4. Include reasoning traces (mandatory since July 2024)
5. `metadata.yaml` + `README.md`
6. PR to `SWE-bench/experiments` repo
7. Give John Yang (@john-b-yang) push access

## Controversies

- OpenAI abandoned Verified (Feb 2026): contamination in ALL frontier models
- "SWE-Bench Illusion" paper: models identify buggy files 76% from issue text alone
- 60% of remaining unsolved tasks had broken tests
- Verified leaderboard still active and widely cited despite this

## Competitive Analysis for Pilot

- Pilot's realistic score: ~62-68% on Lite (CC baseline 58% + scaffold delta 4-10)
- Top entries 80%+ — purpose-built agents optimized for months
- Pilot's strengths (orchestration, epic decomp, autopilot) don't map to SWE-bench
- **Best ROI**: Single baseline run for marketing number, not leaderboard grinding
- **Differentiation**: SWE-bench Pro or Multilingual (less saturated)
- Having both Terminal Bench 82% + SWE-bench score positions Pilot as well-rounded

## Key URLs

- Leaderboard: swebench.com
- GitHub: github.com/SWE-bench/SWE-bench
- Submissions: github.com/SWE-bench/experiments
- sb-cli: swebench.com/sb-cli/submit-to-leaderboard/
