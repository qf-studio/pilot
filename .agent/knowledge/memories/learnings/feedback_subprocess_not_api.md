---
name: LLM judge primitives must use Claude Code subprocess, not direct API
description: Pilot's value prop is Claude Code subscription. Any LLM-using primitive that calls api.anthropic.com directly silently fails for subscription users. Subprocess pattern from EffortClassifier is canonical.
type: feedback
originSessionId: 89fe3897-6bc2-4725-a1f2-8635b79860b3
---
**Rule**: Any LLM-using primitive in Pilot (judge, classifier, parser) MUST spawn `claude` as a subprocess, NOT call `https://api.anthropic.com/v1/messages` directly. Bench mode is the only exception (and only because `ANTHROPIC_API_KEY` is required there per `bench_cost_safety.md`).

**Why**: Pilot's entire value proposition is Claude Code subscription as the execution engine — operators pay through their CC subscription, not through raw API tokens. Direct API calls require `ANTHROPIC_API_KEY` which most users don't have set. When the env var is missing, the HTTP call returns 401, the caller's fail-open path catches the error, and the primitive silently no-ops. Operators see "config: enabled=true" but the feature does nothing — undetectable from user-facing behavior.

**Empirical evidence (2026-05-07)**: TASK-45 / v2.132.0 shipped a pre-flight intent judge using direct API. GH-2816 (deliberately vague test issue) should have been rejected. Instead, the judge 401'd silently, the poller fail-opened, and Pilot dispatched a CC subprocess that burned compute on a vague task. The bug existed because TASK-45's plan reused the existing `IntentJudge` HTTP pattern (which had been silently broken too). TASK-47 / GH-2817 / v2.133.1 / PR #2819 fixed by switching to subprocess pattern matching `EffortClassifier`'s.

**Canonical pattern** (`internal/executor/effort_classifier.go:305-321`):
```go
exec.CommandContext(ctx, claudeCmd, "--print", "-p", prompt, "--model", "claude-haiku-4-5-20251001", "--output-format", "text")
// or with structured output:
exec.CommandContext(ctx, claudeCmd, "--print", "-p", prompt, "--model", model, "--output-format", "json", "--json-schema", schema)
```

Read `claudeCmd` from `config.ClaudeCode.Command` (defaults to `"claude"`). Use `cmd.Output()`. Wrap in `context.WithTimeout`.

**How to apply**: When implementing a new LLM primitive — or auditing an existing one — grep for `api.anthropic.com` and `os.Getenv("ANTHROPIC_API_KEY")` in `internal/executor/`. Any hit outside `bench/` is suspect. The right pattern is `cmdRunner func(ctx, args ...string) ([]byte, error)` field with a default subprocess runner and a test-injectable mock.

**Known violators still pending fix (as of 2026-05-07)**: `internal/executor/subtask_parser.go:15-40` has the same direct-API bug. Track as TASK-48.

**Verification**: After any LLM primitive change, file a test issue that should trigger it and verify in `executions` table (e.g., `status='declined-preflight'` or similar). Don't just read code — confirm runtime behavior. The TASK-45 silent-disable went undetected from ship to next-day testing.
