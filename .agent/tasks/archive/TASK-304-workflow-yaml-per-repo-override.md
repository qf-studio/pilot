> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-304: `.pilot/workflow.yaml` per-repo prompt + policy override

**Status**: queued
**Created**: 2026-05-26
**Severity**: P1
**Effort**: L (~1 day)
**Job (JTD)**: J2 Hand-off
**Source**: Symphony research, Wave 5 / `~/.claude/plans/let-s-plan-that-use-staged-seal.md`

---

## Context

**Problem**: Pilot's executor prompt and execution policy live entirely on the Pilot side. Customers cannot customize agent behavior per repo without forking Pilot. Different teams have different conventions (commit format, branch naming, definition-of-done, test-running) that today must be implicit knowledge.

**Goal**: A `.pilot/workflow.yaml` file (YAML front-matter + Markdown body) checked into the target repo, read by Pilot at the start of each run. Sections:

```yaml
---
version: 1
agent:
  max_turns: 20
  reasoning_effort: high
policy:
  commit_format: conventional
  branch_prefix: pilot/GH-
  pr_template: .github/pull_request_template.md
hooks:                       # placeholder; full hooks in TASK-305
  after_create: null
  before_run: null
  after_run: null
  before_remove: null
---

# Workflow

[Markdown prompt body — appended to executor system prompt, gives the agent
project-specific instructions, definition-of-done, conventions, etc.]
```

Borrowed from Symphony's `WORKFLOW.md` (`/tmp/symphony/elixir/WORKFLOW.md`).

**Why now**: J2 Hand-off is P1 both personas. Per-repo override is the path to multi-team adoption — same insight that made Codex's repo-local `.codex/skills/` work for Symphony.

---

## Acceptance Criteria

- [ ] Pilot reads `.pilot/workflow.yaml` from the target repo's worktree before executor run.
- [ ] If file missing, fall back to current behavior (zero regression).
- [ ] Front-matter `agent.*` overrides corresponding executor profile fields.
- [ ] Front-matter `policy.*` is exposed to executor prompt as structured fields.
- [ ] Markdown body is appended to executor system prompt under a clear header (e.g., `## Project Workflow`).
- [ ] Schema is versioned (`version: 1`); unknown top-level keys are ignored with a warning, not an error.
- [ ] `hooks:` is parsed but not yet executed in this task (TASK-305 does that).

---

## Implementation

### Phase 1: Loader + schema
**Tasks**:
- [ ] Create `internal/executor/workflow/workflow.go` with `Workflow` struct + `Load(repoPath string) (*Workflow, error)`.
- [ ] Parse YAML front-matter delimited by `---` (use `gopkg.in/yaml.v3`).
- [ ] Markdown body stored as `PromptAppendix string`.
- [ ] Validate version field; warn on unknown keys.

**Files**:
- `internal/executor/workflow/workflow.go` (new)
- `internal/executor/workflow/workflow_test.go` (new)

### Phase 2: Executor integration
**Tasks**:
- [ ] In `internal/executor/runner.go`, after worktree setup and before prompt construction:
  1. Call `workflow.Load(worktreePath)`.
  2. If found: apply `agent.*` overrides to executor profile; merge `policy.*` into prompt context; append `PromptAppendix` to system prompt.
  3. If not found: log debug, proceed with defaults.

**Files**:
- `internal/executor/runner.go`
- `internal/executor/prompt_builder.go` (or equivalent)

### Phase 3: Documentation + example
**Tasks**:
- [ ] Add `.pilot/workflow.yaml` example to `configs/` directory.
- [ ] Document schema in `docs/` (workflow.yaml reference page).
- [ ] **Dogfood**: convert Pilot's own repo to use `.pilot/workflow.yaml` and measure how much of the hardcoded executor prompt absorbs.

**Files**:
- `configs/workflow.example.yaml`
- `docs/` (workflow reference)
- `.pilot/workflow.yaml` (Pilot's own — dogfood)

---

## Out of Scope

- Hook execution (`after_create`, `before_run`, etc.) — implemented in TASK-305.
- Workflow inheritance / includes (v1 is single file).
- Live reload during a run — workflow is loaded once at run start.
- Workflow validation CLI (`pilot workflow validate`) — defer if dogfood finds bugs.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| File location | `.pilot/workflow.yaml`, `pilot.yaml`, `.github/pilot.yaml` | `.pilot/workflow.yaml` | Matches Symphony naming, room for siblings (`.pilot/skills/`) |
| Front-matter format | YAML, TOML, JSON | YAML | Consistent with `~/.pilot/config.yaml`; humans read it |
| Body format | Markdown, plaintext | Markdown | Same as Symphony; renders nicely in repo |
| Version handling | strict, lax, schema-validated | Lax (warn on unknown keys, fail only on bad version) | Forward-compatible |

---

## Files Affected (estimate)

- `internal/executor/workflow/` (new package)
- `internal/executor/runner.go`
- `internal/executor/prompt_builder.go`
- `configs/workflow.example.yaml` (new)
- `.pilot/workflow.yaml` (new, in Pilot's own repo)
- `docs/` (workflow reference page)

---

## Verify

```bash
go test ./internal/executor/workflow/...
go test ./internal/executor/...

# Dogfood: spawn Pilot run on a fork using .pilot/workflow.yaml override; confirm
# prompt appendix appears in executor logs; confirm agent.max_turns honored.
make test
```

**Dogfood gate** (from master plan verification §3): if `.pilot/workflow.yaml` doesn't naturally absorb >50% of currently-hardcoded executor prompt content, the abstraction is wrong — revisit before TASK-305.

---

## Done

- [ ] Loader package shipped with unit tests
- [ ] Executor reads + applies overrides
- [ ] Example file in `configs/`
- [ ] Schema documented
- [ ] Pilot's own repo dogfoods the override (50%+ absorption signal)
- [ ] Zero regression when file absent

---

## Refs

- Master plan: `~/.claude/plans/let-s-plan-that-use-staged-seal.md`
- Symphony evidence: `/tmp/symphony/elixir/WORKFLOW.md` (entire file)
- Symphony spec: `/tmp/symphony/SPEC.md` §3 (workflow), §11 (extensions)
- Related: `TASK-305` (workspace hooks consume `hooks:` block)

---

**Last Updated**: 2026-05-26
