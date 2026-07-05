> **RESOLVED/SUPERSEDED (2026-07-05):** Codified in sops/integrations/prompt-leak-fix-checklist.md + invariant test (#2592)

---
name: Check ALL prompts for leaks, not just the one that surfaced
description: When fixing a prompt-leak bug, the cascade #2 lesson is that fixing one site (the planner) is not enough — the same example/template often lives in multiple embedded prompts (executor, decomposer, parsers).
type: feedback
originSessionId: a45a0b36-53c9-4751-93ff-3cd0d8b24386
---
When fixing a prompt-leak bug, scan EVERY embedded prompt string in the codebase, not only the one that produced the symptom.

**Why:** Cascade #2 (2026-05-04). PR #2562 patched `internal/executor/epic.go` (planner side). Same OAuth example string also lived in `internal/executor/workflow.go:163` (executor side, different code path). Daemon resumed, executor still leaked, second cascade merged 512 LoC of OAuth contamination before being reverted.

**How to apply (before merging any prompt-leak fix):**
1. Grep across `internal/executor/`, `internal/autopilot/`, decomposer, parsers — every package that builds prompts.
2. Look at multi-line raw-string literals (backtick strings) AND `fmt.Sprintf`-built prompts.
3. Run the cross-prompt invariant test against the **pre-fix** code: `go test ./internal/executor/ -run TestNoPromptLeakStrings`. If it doesn't fail on the pre-fix tree, your test scope is too narrow — extend the forbidden-literal list or the file walk.
4. Use the SOP: `.agent/sops/integrations/prompt-leak-fix-checklist.md` (commit 70553a72) walks the full procedure.
5. The cross-prompt invariant test is now the build-break safety net (#2592). Don't disable it; if it false-positives, narrow the literal list rather than the walk.

Cross-references: `incident_oauth_cascade_series.md`, `pattern_squash_merge_mergedat_null.md`.
