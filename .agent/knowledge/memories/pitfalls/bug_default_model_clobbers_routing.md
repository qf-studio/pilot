---
name: default_model clobbers model_routing for CC backend
description: model_routing config is silently ignored for claude-code backend when default_model is set; --model flag never passed, CC falls back to its own (Opus) default
type: project
originSessionId: c2a5de08-af53-4f48-99ef-e624717e9f52
---
`internal/executor/runner.go:1384-1391` and `:3324-3329` clear `selectedModel` to `""` when `BackendTypeClaudeCode` AND `DefaultModel` is set. Result: `model_routing.{simple,medium,complex}` does nothing for the CC backend; CC uses its own settings/OAuth default (Opus).

**Why:** The CC-type guard was added to avoid overriding CC's built-in model selection, but predates `model_routing`. Now it silently blocks intentional routing.

**How to apply:** When debugging Pilot model selection, do NOT trust config alone. Verify actual model via:
```
sqlite3 ~/.pilot/data/pilot.db "SELECT model_name, estimated_cost_usd FROM executions ORDER BY created_at DESC LIMIT 5;"
```
Cost math is the strongest signal — Opus output is ~5× Sonnet ($75 vs $15 per Mtok). The "Opus plans, Sonnet executes" v2.100.5 promise is unrealized until issue #2448 ships.

Tracking: GH-2448. Investigation 2026-04-30.
