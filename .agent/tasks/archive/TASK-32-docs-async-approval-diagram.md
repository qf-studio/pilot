# TASK-32: Docs — Async Approval Flow + Mermaid Diagram

**Status**: 🚧 In Progress
**Created**: 2026-05-05
**Assignee**: Pilot

---

## Context

**Problem**:
The async pre-merge approval pipeline shipped in v2.121.0–v2.128.0 (TASK-26 through TASK-31) is fully working end-to-end (verified live via PR #2702 smoke-test on 2026-05-05). However, `docs/content/features/approval-workflows.mdx` (241 lines) does not yet describe:
- The async dispatch path (Manager.SubmitApprovalRequest → Telegram callback → RecordDecision → PRStateWriter → controller advance)
- Daemon-restart resilience (approval_pending SQLite table rehydrates on startup)
- The `approval.async_dispatch` config knob (default true in v2.125+)

Users reading the docs see only the legacy synchronous behavior.

**Goal**:
Update `approval-workflows.mdx` with a new section that documents the async flow and includes a Mermaid sequence diagram. Cross-link from `features/telegram.mdx`.

**Success Criteria**:
- [ ] New "How async approval works" section in `approval-workflows.mdx`
- [ ] Mermaid sequence diagram renders in Nextra v4 build
- [ ] `approval.async_dispatch` config knob documented
- [ ] Daemon-restart resilience explained (approval_pending table)
- [ ] One-line "see also" link added to `features/telegram.mdx`
- [ ] `cd docs && pnpm build` succeeds without errors
- [ ] No behavior change (docs only)

---

## Implementation Plan

### Phase 1: Add async-flow section with Mermaid diagram
**Goal**: Document the v2.121–v2.128 pipeline visually.

**Tasks**:
- [ ] Insert new section in `docs/content/features/approval-workflows.mdx` (placement: after the existing pre-merge section, before the FAQ/troubleshooting if any)
- [ ] Mermaid sequence diagram covering:
  - Controller → `Manager.SubmitApprovalRequest(ctx, req)` returns request ID immediately
  - Manager dispatches Telegram message with `approve:<id>` / `reject:<id>` callback_data buttons
  - User taps button → `tgApprovalHandler.HandleCallback` parses prefix
  - Handler calls `Manager.RecordDecision(id, decision, by)`
  - `RecordDecision` → `PRStateWriter.SetApprovalDecision` writes `executions.approval_decision`
  - On next controller tick, `controller.handleAwaitApproval` reads `prState.ApprovalDecision`, advances stage
  - Merge → tag → release pipeline
- [ ] Prose surrounding the diagram explaining each step in 1-2 sentences

**Files**:
- `docs/content/features/approval-workflows.mdx` — new section + diagram

### Phase 2: Daemon-restart resilience callout
**Goal**: Explain why taps survive a daemon restart.

**Tasks**:
- [ ] Subsection or callout box noting:
  - `approval_pending` SQLite table persists pending requests across restarts (v2.122)
  - On startup, the daemon rehydrates the in-memory map from this table (v2.123)
  - Result: an Approve tap lands correctly even if the daemon was restarted between the prompt and the tap

**Files**:
- `docs/content/features/approval-workflows.mdx`

### Phase 3: Config documentation
**Goal**: Surface the `approval.async_dispatch` knob.

**Tasks**:
- [ ] Document `approval.async_dispatch` (bool, default `true` since v2.125)
- [ ] Note that the legacy blocking path (`Manager.RequestApproval` channel) is preserved for one release of bake time, then will be removed (TASK-31 follow-up)

**Files**:
- `docs/content/features/approval-workflows.mdx`

### Phase 4: Cross-link from telegram.mdx
**Goal**: Discoverability from the Telegram adapter page.

**Tasks**:
- [ ] Add a one-line "See also" pointing to the new approval-workflows section, somewhere near the existing approval/callback content in `docs/content/features/telegram.mdx`

**Files**:
- `docs/content/features/telegram.mdx`

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Diagram format | Mermaid, ASCII art, image | Mermaid | Nextra v4 supports it natively; renders cleanly; editable in plain markdown |
| Diagram type | sequenceDiagram, flowchart | sequenceDiagram | Async approval is inherently temporal (ordered actor messages); sequence is the natural fit |
| Page placement | New page vs append to existing | Append section | Single discoverable doc avoids fragmentation; existing page already has 241 lines of relevant context |
| Legacy path mention | Hide vs document as deprecated | Document with deprecation note | Honest about transition; user-visible config (`async_dispatch`) requires it |

---

## Dependencies

**Requires**:
- [x] Async approval pipeline merged (TASK-26 → TASK-31, all done as of v2.128.0)
- [x] Smoke-test verification (PR #2702, 2026-05-05)

**Blocks**:
- Future cleanup PR removing legacy `RequestApproval` path (will reference these docs)

---

## Verify

```bash
cd docs
pnpm install
pnpm build
# Expected: build succeeds, no Mermaid render errors, new section visible in output
```

Manual verification:
- Inspect built HTML for Mermaid diagram presence
- Cross-check that all named symbols (SubmitApprovalRequest, RecordDecision, PRStateWriter, etc.) match current code in `internal/approval/manager.go` and `internal/autopilot/state/`

---

## Done

- [ ] `approval-workflows.mdx` contains "How async approval works" section
- [ ] Mermaid `sequenceDiagram` block renders without errors
- [ ] `approval.async_dispatch` config knob documented
- [ ] Daemon-restart resilience subsection present
- [ ] `telegram.mdx` cross-link added
- [ ] `cd docs && pnpm build` succeeds
- [ ] PR opened, CI green, approved, merged, released

---

## Notes

- Public docs site: Nextra v4 at `pilot.quantflow.studio` (per CLAUDE.md, this is authoritative for product behavior)
- Docs deploy pipeline: `v*` tag → docs-version-sync → GitLab `prod-*` (see `.agent/memory/reference_docs_deploy_pipeline.md`)
- This task exercises the docs-deploy pipeline as a side effect, providing additional validation beyond the Go-only PR #2702.

---

**Last Updated**: 2026-05-05
