# TASK-549: Legacy repo bootstrap — build real Navigator context on first run, persist it, and price it

**Status**: 🚀 **PHASES 2+3 DISPATCHED 2026-10-14** — [#5679](https://github.com/qf-studio/pilot/issues/5679) bootstrap session (trigger config `navigator.bootstrap` default **manual** until D1 is answered) · [#5680](https://github.com/qf-studio/pilot/issues/5680) copy-back of worktree learnings. Phase 1 (cold-start bench) is a Navigator-session job, not filed. Phase 4 waits on console#401; phase 5 waits on D3.
**Created**: 2026-10-13
**Assignee**: Navigator plans · Pilot executes (pilot repo first, console second)
**Parent**: [TASK-405](archive/PILOT-CLOUD-TASKS-MOVED.md) · **Sibling**: [TASK-546](archive/PILOT-CLOUD-TASKS-MOVED.md) (pricing v3; D11 there points here) · [TASK-531](TASK-531-chat-issue-untracked-path-rewrite.md) (chat cites untracked `.agent/`)

---

## Context

**Problem**: Pilot's value is Navigator context (pitfall "Pilot without Navigator = just another Claude Code wrapper"). On a **new** project the first tickets build that context as they go. On a **legacy** repo (tenant onboarding of an existing codebase) the context never gets built, so every ticket and every chat turn re-derives the codebase from zero. Verified 2026-10-13:

- On a tenant clone Pilot scaffolds `.agent/` locally (`internal/executor/navigator.go` `Initialize`), git-ignored, never committed. The scaffold's `DEVELOPMENT-README.md` carries the three sections `loadProjectContext()` injects into every task prompt — **as placeholders**: "_Describe the major components… fill in during the first task session_", "_List the files a new session should read first_", "_Fill in the real source tree_" (`navigator.go` `contextInjectionSections`). Nothing fills them in. The injected "Project Context" on a legacy repo is skeleton text.
- No SOPs, no memories, no `graph.json` content exist, so `findRelevantSOPs` and the chat prompt's "grep graph.json and memories, cite instead of re-deriving" step are no-ops.
- `EnsureNavigatorInWorktree` (`internal/executor/worktree.go:996`) copies `.agent/` **source → worktree** before a task. No copy back was found: what a task learns in its worktree does not return to the clone, so the next task starts from the same skeleton.
- The chat is a Navigator research assistant by design (TASK-518/519). On a legacy repo each turn is a cold research session (up to 40 agent turns, 5 min) with no index to read. That is both the delivery gap and the token gap: cold context = more tokens per ticket, lower first-try green, and chat turns are not metered at all (TASK-546 D10).
- The product onboarding copy already admits it: "⚠️ No Navigator docs found… Pilot will still work, but won't know your patterns" (`product/ONBOARDING.md`).

**Goal**: a one-time, bounded **bootstrap run** on first attach of a legacy repo that writes a real `DEVELOPMENT-README.md` (components, key files, structure), seed SOPs and a starter `graph.json`; a **persistence path** so later tasks and chat turns accumulate memories instead of losing them; and a **price** for the bootstrap that is honest about its token cost.

---

## Known Pitfalls & Patterns

- **PITFALL** (100%) Navigator integration in the executor prompt is the core value; never drop the "Start my Navigator session" prefix in `BuildPrompt`. Reflected: Phase 2 only fills content; it does not touch the prompt chain.
- **PITFALL** `chat-filed-issues-cite-untracked-agent-scaffold` — `.agent/` is untracked on tenant clones; backticked `.agent/` paths get held by the base-presence gate. Reflected: the bootstrap output stays untracked unless D2 says commit; the chat prompt rule from TASK-531 stands.
- **PITFALL** GH-5221 — the installed plugin's README template lacks the three injection headers; `ensureContextSections` appends them. Reflected: Phase 2 writes into those exact headers.
- **LEARNING** `connect-means-wire-the-integration-not-create-work-in-product-repos` — bootstrap must not open PRs or file issues in the customer repo unless D2 says commit the scaffold.

---

## Decisions for the founder

| # | Question | Recommendation | Why |
|---|---|---|---|
| D1 | When does bootstrap run | **Automatically on first attach when `.agent/` is absent or still skeleton**, with a console toggle to defer | Without it the first tickets are the most expensive and least likely to ship; the toggle keeps control with the customer |
| D2 | Where the context lives | **Untracked on the box (as today) + synced to the console per org (S3/DB blob) so a box rebuild or re-provision restores it**; offer "commit `.agent/` to the repo" as an opt-in later | Committing into a customer repo on day one is intrusive; losing it on re-provision is worse |
| D3 | How bootstrap is priced | **One bootstrap counts as 10 tickets of the included allowance (its dollars go to the fair-use ceiling like any ticket); repeat bootstraps on demand cost a pack ticket each × 10** | Measured cost is unknown until Phase 1; 10 tickets ≈ $10–35 of tokens on Sonnet 5.5 is the right order for a 40-turn research session over a mid-size repo. Replace with the measured p50 after the first 10 runs |

Founder answers "D1–D3 ok" or sends changes.

---

## Acceptance Criteria

- [ ] After bootstrap, `DEVELOPMENT-README.md` on the tenant clone has non-placeholder content under `### Key Components`, `## Key Files`, `## Project Structure`, and `loadProjectContext()` injects it (test asserts no `_fill in_` text).
- [ ] `graph.json` holds ≥ 1 node per top-level package/module and ≥ 3 seed memories (patterns) written by the bootstrap; chat turns cite them (test on a fixture repo).
- [ ] Task runs write memories back: a learning recorded in a worktree is present on the clone after the task finalizes (copy-back or shared dir).
- [ ] Bootstrap is one recorded unit in the per-ticket ledger (TASK-546 Phase 1) with its tokens and cost, `kind=bootstrap`.
- [ ] Console shows "Context: bootstrapped <date>, N memories" on the instance page, with "Rebuild" behind a confirmation that states the cost in tickets.
- [ ] A legacy fixture repo (≥ 200 files, no `.agent/`) ships its first ticket first-try green with bootstrap, and the same ticket's token count is lower than without bootstrap (bench note).

---

## Implementation

### Phase 1: Measure the cold-start cost (pilot repo, bench)
- [ ] Pick two legacy fixtures (one Go, one TS, ≥ 200 files). Run 3 tickets each with the skeleton scaffold; record tokens, cost, turns, first-try result (`system/bench/` note).
- [ ] Run one chat research turn each; record tokens. These numbers set D3.

### Phase 2: Bootstrap run (pilot repo)
- [ ] `pilot navigator bootstrap [--project <path>]`: one `ExecuteChatTurn`-style read-only session with a dedicated prompt that **writes only under `.agent/`** (allowlist: read tools + `Write(.agent/**)`), fills the three injection sections, writes `system/architecture.md`, 3–10 pattern memories and a `graph.json` via the embedded `memory_writer` idiom; hard caps: 60 turns, 10 min, model = chat default.
- [ ] Runner calls it from `maybeInitNavigator()` when the README still carries a placeholder section and the project config has `navigator.bootstrap: auto` (default auto; `manual` defers).
- [ ] Mutation that must fail: remove the placeholder check → bootstrap must not re-run on an already-filled README.

### Phase 3: Persistence (pilot repo + console)
- [ ] Copy-back: on task finalize, merge `worktree/.agent/knowledge/**` and `.agent/sops/**` back to the clone (append-only, newest wins per file; never touch tracked files).
- [ ] Console: per-org `.agent/` snapshot (tar) uploaded by the daemon after bootstrap and nightly; restored by the reconciler at provision when the repo has no tracked `.agent/`. Reuse the instance-config snapshot path.
- [ ] Mutation that must fail: drop the copy-back → persistence test sees the memory missing on the clone.

### Phase 4: Metering + console UI
- [ ] Bootstrap rows in the ledger (`kind=bootstrap`, counted per D3); instance page shows context status and the Rebuild action; allowance UI counts bootstrap tickets.

### Phase 5: Copy + docs
- [ ] `product/ONBOARDING.md` "No Navigator detected" → "Pilot will build it: ~10 tickets of your allowance"; site line under "What's included"; `PRICING-MODEL.md` bootstrap note.

**Dispatch order**: 1 → 2 → 3 (pilot leg first, console leg after) → 4 → 5. Phase 4 depends on TASK-546 Phase 1 (ledger).

---

## Out of Scope

- Committing `.agent/` into customer repos by default (D2 opt-in later).
- Installing the Navigator plugin on tenant boxes (the research method stays embedded in prompts, per TASK-519).
- Changing `BuildPrompt` structure or the "Start my Navigator session" prefix.
- Multi-repo orgs sharing one context (one `.agent/` per project path, as today).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Bootstrap engine | separate Python tool on the box, Claude Code session with write allowlist, run `nav-init` plugin | Claude Code session, writes limited to `.agent/**` | Plugin is not on tenant boxes; a session reuses `ExecuteChatTurn` plumbing and the chat prompt's research method |
| Trigger | manual only, auto on first attach, auto on first ticket | Auto on first attach with a deferral toggle | First ticket is the worst moment to pay cold-start; attach has idle time |
| Persistence | commit to repo, box-local only, console snapshot | Console snapshot + box-local | Survives re-provision without touching the customer's git history |
| Pricing unit | free, flat $, N tickets | N tickets (D3) | Fits the ticket vocabulary; no new price object |

---

## Verify

```bash
go test ./internal/executor/ -run 'Bootstrap|ContextSections|NavigatorCopyBack' -count=1
# bench: cold vs bootstrapped on the two fixtures, tokens + first-try result → system/bench/legacy-bootstrap-<date>.md
```

---

## Done

- [ ] Fixture repos ship first ticket first-try green after bootstrap; bench note shows the token delta.
- [ ] Memory written in a worktree is present on the clone after finalize.
- [ ] Ledger shows a `bootstrap` row with cost; allowance UI counts it.
- [ ] Onboarding copy and pricing doc updated.

---

## Refs

- `internal/executor/navigator.go` (`Initialize`, `contextInjectionSections`, `ensureContextSections`), `internal/executor/prompt_builder.go:219` (`loadProjectContext`), `internal/executor/worktree.go:996` (`EnsureNavigatorInWorktree`), `internal/executor/chat_prompt.go`, `internal/executor/chat_turn.go`
- `product/ONBOARDING.md` § "No Navigator Detected"; `system/side-chat-navigator.md`
- TASK-546 D10/D11, TASK-518/519 (chat as Navigator), TASK-531 (untracked `.agent/` citations)

---

**Last Updated**: 2026-10-14
