# Drafts — for Pilot pickup

Half-baked code dropped here during Wave 4/5 reconciliation. **Don't import — these compile against an older codebase and need adaptation before they're useful.**

Each subdirectory corresponds to a GH issue that hands the work to Pilot. The issue references the drafts as starting points so Pilot doesn't reimplement from scratch.

## Conventions

- `task-NNN-slug/` per draft, named after the task ID
- Include the task doc (`TASK-NNN-*.md`) alongside the code so context travels with it
- Once Pilot ships the real implementation, **delete the corresponding subdirectory** in the same PR that lands the code

## Current contents

- `task-312-engine/` — Go OpenRouter execution engine. ~33KB. All non-smoke tests pass standalone; needs `backend_factory.go` wire-up (~5 LOC) for `TestBackendFactory_RegistersOpenRouter` to pass.
- `task-305-hooks/` — workflow lifecycle hook execution. Compile-breaks against current `internal/executor/workflow/workflow.go` because `HookConfig` fields are now `interface{}` on main and `HookValue` type was never landed. Needs adaptation: define `HookValue`, write a normalizer from `interface{}`, wire `RunHook`.
