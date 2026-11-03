# TASK-585: Wave 2 — research → tickets (P2, P3, C5, C4, C10, U10)

**Status**: ✅ **WAVE-2 CODE COMPLETE 2026-11-03 10:44Z — all seven legs on main.** Daemon: P2 pilot#6010, P3 pilot#6011 → PR#6013. Console (live at prod-0.1.60 = main since 11-02 20:58Z): C5 #459 → PR#463 (revision #465), C10 #460 → PR#461, C4 #469 → PR#472 + PR#473 (revision #474), C11 #475 → PR#476 (+ fast-follow #477 open). UI: U10 #287 → PR#291 (adapter, + fix PR#295), PR#292 (Create tickets, revision ui#296), **PR#293 (Review draft items, revision ui#297) hand-merged 11-03 10:44Z as 9f205169** after a GitHub-side stuck merge ref (close/reopen fixed it; autopilot had timed the PR out at 10:27Z into terminal `failed`). UI deploy after PR#293 pending at save. **Next: wave-2 exit run on the canary** (§ Acceptance), then roadmap rows → ✅ and this doc archives. Lessons → SOP: (1) a multi-part leg (C4, U10) goes out `no-decompose` — epic children on shared files collide (three hand-filed revisions today); (2) two legs with migrations dispatched together collide on the number; (3) fix issues on borrowed branches are not closed by the daemon when their PR merges; (4) `Blocked by: #<fix issue>` deadlocks on (3). Was: 🚀 **ALL SIX LEGS FILED 2026-11-02.** MERGED same day: P2 [pilot#6010](https://github.com/qf-studio/pilot/issues/6010) · P3 [pilot#6011](https://github.com/qf-studio/pilot/issues/6011) → PR#6013 · C10 [console#460](https://github.com/qf-studio/pilot-console/issues/460) → PR#461 (+ post-merge fixes #462/#466; #468 closed not-planned: PR-body-only defect, reviewer loop → TASK-620) · C5 [console#459](https://github.com/qf-studio/pilot-console/issues/459) → PR#463 merged 18:36Z via hand-filed revision [#465](https://github.com/qf-studio/pilot-console/issues/465) (0039 migration collision with #461 → renumbered 0040; wire-contract gate; claim-before-file). **20:25Z state**: C4 ✅ on main (PR#472 + PR#473 via revision #474, merged 20:06Z). C11 → PR#476 green, **awaiting founder approval on Telegram** (pre-PR REQUEST-CHANGES = test-quality only; relay verified in the diff; fast-follow filed, see Refs). U10 children: ui#288 ✅ PR#291 merged 19:46Z (+ post-merge fix PR#295), **UI deployed 20:10Z at 715a83ab0**; ui#289 PR#292 and ui#290 PR#293 conflict with #291 on the adapter files → hand-filed revisions ui#296 (first) and ui#297 (blocked by #296). PR#292's branch also used a wrong route (/api/v1/review/drafts); taking main's adapter fixes it. Console `prod-*` tag lags main (prod-0.1.56 = PR#472; main = PR#473) — auto-release pending. **19:50Z: C11 ADDED → [console#475](https://github.com/qf-studio/pilot-console/issues/475)** (console must relay `action` on chat messages — missed by the plan; found by the PR#291 pre-PR review). C4 epic-split into #470 (merged PR#472) + #471 (PR#473 conflicts with #472 → hand-filed revision [console#474](https://github.com/qf-studio/pilot-console/issues/474), autopilot-fix shape); U10 split into ui#288 (PR#291 awaiting approval on the C11 gap) / #289 (PR#292, vendored wire list refreshed by hand on the branch) / #290 (PR#293 green). **18:45Z: C4 → [console#469](https://github.com/qf-studio/pilot-console/issues/469) · U10 → [ui#287](https://github.com/qf-studio/pilot-console-ui/issues/287) dispatched** (pre-flight green on merged main). **Not reviewed**: PR#463 (migration + public routes, tier-1) and the P2/P3 daemon routes — post-merge reviews pending. Lesson → SOP: two legs with migrations dispatched together collide on the number.
**Created**: 2026-11-02
**Assignee**: Pilot (P/C/U legs) · founder (D1–D4, ui#266)
**Parent**: [TASK-584](TASK-584-pilot-cloud-execution-roadmap.md) Phase C · roadmap rows in `system/project/project_pilot_cloud_roadmap_tickets.md` · scope `system/project/project_pilot_cloud_positioning.md` §8.3 (flow, D1–D4), §8.2 D2 (reject → learning), §4 (research draws from the pool)

---

## Context

**Problem**: a research answer on the console dies as text. The Research page (U9) shows the sub-agent tree and the report; the human then writes the GitHub issue by hand. The positioning doc §8.3 decided the flow: `Create tickets` → N drafts in SOP format → Review needed → Approve files the `pilot` issue → Queued. Reject stores the reason as a learning (§8.2 D2).

**Goal (wave-2 exit, TASK-584 Phase C)**: a research answer becomes an approved GitHub issue from the console with no hand edits.

**What exists (seam map 2026-11-02, navigator-research; console lines = origin/main):**
- Daemon chat wire: pilot internal/gateway/chat.go:48 chatMessageRequest {conversationId, text, isCallback, callbackId, actionId, sender, projectId}; events via pilot internal/adapters/web/messenger.go:53 web.Event {seq, type, text, taskId, phase, progress, prUrl, success, messageRef, occurredAt}. **No `mode` field exists on any wire** (daemon, console, UI adapter) — the UI mode pills are cosmetic (ui#266). A research turn = a read-only chat turn (pilot internal/comms/chat_turn_handler.go:68 → internal/executor/chat_turn.go:87, allowlist, MaxTurns 40, 5 min).
- The report is not stored. It is the last `text` event of the run in a 1 h in-memory buffer (500 events); the UI takes it as the last text event (ui src/views/ChatView.vue:66).
- One-draft issue filing already exists on the daemon: the model emits a ```pilot-issue YAML block (pilot internal/comms/pilot_issue_block.go:21), parsed into IssueDraft (internal/comms/issue_intake.go:14, always adds `pilot`), shown as a confirmation card (chat_turn_handler.go:207), filed on confirm (chat_turn_handler.go:237 → internal/adapters/github/issue_creator.go:45 → CreatePilotIssue, conventional-commit title + repo allowlist). Spec check = pilot internal/ghissue/spec.go:68 ValidateSpec (one H2 from the allowed set + min length). Nothing checks SOP Rule 3 (`#N` deps), 3b (backticked paths exist), 5b (acceptance dialect).
- Epic split exists for tasks, not drafts: pilot internal/executor/epic.go:253 PlanEpic / :1185 CreateSubIssues; the `no-decompose` label or a bare "no-decompose" token in the body (internal/executor/decompose.go:100, :125) suppresses it.
- Daily chat-turn cap: daemon default 200 (pilot internal/adapters/web/config.go:33), org-wide, in-memory (resets on restart), enforced at chat_turn_handler.go:43; the console renders the value from plan data (console internal/fleet/configrender.go:87; migration 0030_daily_caps default 200; allowance status counts chat_turn ledger rows, console internal/allowance/status.go:70). Positioning §8.3 D4 says **50 research turns per day per org**.
- Console relay is byte-faithful (console internal/chatapi/handlers.go:202 → writeRawJSON); nothing is persisted. Review lane: console internal/boardapi/review.go:32 reviewItemDTO {id, type, project, title, why, created_at, card_id, actions}, type `draft` reserved and not emitted (review.go:14). Decision: console internal/boardapi/decision.go:37 {decision, comment}; comment is required for send_back and **dropped on reject**; the daemon call sends only {decision, by} (console internal/approvals/client.go:230 → pilot internal/gateway/approvals.go:93).
- Console creates GitHub issues today through the board dispatch path: console internal/boardapi/dispatch.go:282 dispatchUnlinked → per-org writer (customer PAT, one repo per connection) → writer.CreateIssue(core.IssueDraft{Title, Body, Labels, Priority}) → linkAndSeed. Trigger label constant pilotTriggerLabel = "pilot". No drafts table exists.
- Daemon memory: pilot internal/memory/knowledge.go:77 AddMemory writes SQLite only; SyncToFiles (:290) is a stub; no Go code writes graph.json or `.agent/` memory files (executor/git.go:330 only reads graph.json).
- UI: Research page = src/views/ChatView.vue (report at :66, composer at :144); Review page src/views/ReviewView.vue (reject composer already sends the reason as `comment`, :70); dashboard input already routes to the Research page (src/views/DashboardView.vue:132) — that part of U10 is done. Vendored json-tag check: ui scripts/check-wire-fields.sh against src/lib/api/__contracts__/console-wire-fields.txt.

---

## Known pitfalls (from memory)

- **PITFALL** (90%, console-wire-contract-gate-needs-raw-body-strings-contains-test): every json-tagged field added in a console handler/DTO needs a same-package test that reads rec.Body.String() and strings.Contains the literal field. → every C leg carries that bullet.
- **PITFALL** (95%, base-presence-gate-false-positives / SOP Rule 3b): backticked `x/y.ext` spans in an issue body are prerequisite files on main. → new files and cross-repo files are written in plain text in every leg below; the dispatch script aborts on a miss.
- **PITFALL** (100%, mem-196): flag-gated behaviour verified locally but never enabled in the prod task definition. → C10 is a plan-data change, not a flag; C5 has no flag.
- **PITFALL** (95%, test-fake-daemon-more-permissive-than-real-route-hides-400): a console test fake that accepts what the daemon rejects. → C4/U10 fakes must mirror the daemon structs in this doc byte for byte; probes pin the field names.
- **LEARNING** (mem-203, §8.3): Pilot copies issue claims verbatim, so a wrong claim in a draft ships. → the shippable check rejects evidence-free claims; the human edits before approve (D3).
- **LEARNING** (mem-274): three wave-1 UI PRs bound to fields the console never sent. → U10 decodes only fields named in C5/P2; `probe: present` lines pin them.
- Rule 3d: pin the premise with probe lines (every leg below has them).

---

## Decisions (founder confirms; defaults are the recommendation)

| # | Decision | Options | Default | Why |
|---|---|---|---|---|
| D1 | How drafts travel from daemon to console | (a) UI posts the daemon `drafts` event to the console store · (b) a console sync worker pulls chat events · (c) the daemon pushes to the console | **(a)** | Byte-faithful proxy pattern unchanged; no new worker (mem-196 class); the event buffer holds 1 h so a refresh can re-post; idempotent on (conversation, draft id). |
| D2 | Where a reject reason lives as a "learning" | (a) daemon SQLite AddMemory + injected into the next prompt for that project · (b) a `.agent/` learning file + graph.json via a PR per reject · (c) both | **(a) now, (b) later** | No Go writer for files exists; a PR per reject is noise. SyncToFiles stays the hook for (b). |
| D3 | Research-turn cap | (a) plan default `daily_chat_turn_cap` 200 → 50, drafting turns count · (b) a separate research counter | **(a)** | §8.3 D4 says 50; no `mode` exists on the wire, so research == chat turn; one cap, one meter. |
| D4 | ui#266 mode pills | (a) hide · (b) forward `mode` end to end | **(a)** | Nothing reads it; a wire field with no consumer is the mem-274 class. |

---

## Acceptance (wave-2 exit)

- [ ] On the canary tenant: ask a research question → `Create tickets` → ≥2 drafts appear under Review needed as type `draft` within one chat turn.
- [ ] Edit a draft title in the console, approve it → a GitHub issue with the `pilot` label exists with the edited title, a card is Queued, the draft shows the issue link; the daemon picks the issue up (`pilot-in-progress` label) without a hand edit.
- [ ] Reject a draft with a reason → the daemon has a `learning` memory with that reason; the next chat turn for that project sees it in its prompt context.
- [ ] Reject a PR approval with a reason → same learning path (C4).
- [ ] A draft that fails the shippable check is marked `shippable.ok=false` with reasons; approving it files the issue without `no-decompose` and the daemon's epic decomposition splits it.
- [ ] The 51st research turn of the UTC day on an org at the default plan is refused with the daily-limit message; the allowance page shows "50 research turns a day".

---

## Out of scope

- A stored research report with an id (the report travels inline; a report store is a wave-4 item if the 1 h buffer bites).
- `.agent/` learning files written by the daemon (D2 b). Viewer roles on drafts (U16). Draft analytics.
- A GitHub App token path for issue creation (the customer PAT path stays).
- A separate research mode on the wire (D4 a).

---

## Verify

```bash
# pilot (daemon)
go build ./... && go test -race ./internal/comms/ ./internal/gateway/ ./internal/memory/ ./internal/ghissue/
# pilot-console
make build && make test && make lint && make check-wire-contract-tests && make check-fleet-org-scoping
# pilot-console-ui
npm run build && npm test && npm run lint && bash scripts/check-wire-fields.sh
```

---

## Done

- [ ] P2, P3, C5, C10 merged; C4, U10 merged; console `prod-*` tag cut and the UI deploy run green after the last merge (merged ≠ live).
- [ ] The six Acceptance bullets above hold on the canary tenant; the run is recorded in TASK-584 as the wave-2 exit.
- [ ] Roadmap rows P2, P3, C4, C5, C10, U10 are ✅ in `system/project/project_pilot_cloud_roadmap_tickets.md`.

---

## Legs — pilot (daemon)

### LEG P2 — repo: qf-studio/pilot — title: feat(research): Create tickets — draft N issues from a research report in SOP format, shippable check, epic-candidate marking

## Context
The console Research page shows a report (the last text event of a read-only chat turn). The product flow (Pilot Cloud positioning §8.3) adds `Create tickets`: Pilot drafts N GitHub issues from that report, the human reviews them in the console, and approval files them. One-draft filing already exists through the pilot-issue block (`internal/comms/pilot_issue_block.go`, `internal/comms/issue_intake.go`, `internal/comms/chat_turn_handler.go`); this leg adds batch drafting with a machine check, as a chat action on the existing wire. Nothing is filed by this leg: the console owns approval and filing.

The report is not stored on the daemon, so the client sends it inline. The drafting turn is a chat turn: it counts against the daily chat-turn cap (reserveDailyTurn in `internal/comms/chat_turn_handler.go`) and runs with the read-only allowlist of `internal/executor/chat_turn.go`.

probe: present qf-studio/pilot func[[:space:]]extractPilotIssueBlock
probe: present qf-studio/pilot func[[:space:]]ValidateSpec
probe: present qf-studio/pilot func[[:space:]]ExtractReferencedPaths
probe: absent qf-studio/pilot json:\"drafts

## Scope
- `internal/gateway/chat.go`: chatMessageRequest gains `action` (string, optional; only value `create_tickets`). With `action=create_tickets`, `text` is the report markdown (required, ≤ 64 KB) and `projectId` is required. Other requests are unchanged.
- `internal/comms/chat_turn_handler.go`: a new branch for the action runs one drafting turn with a fixed system prompt that asks for a fenced ```pilot-drafts YAML block: a list of drafts, each `{id, title, labels, body, deps}` where `deps` lists other draft ids. Body format per the issue-authoring SOP (.agent/sops/onboarding/new-project-issue-authoring.md): H2 `## Context`, `## Acceptance`, `## Implementation`, `## Refs`; conventional-commit title; evidence as file:line; deps as draft ids only.
- Parser (new file, plain text: internal/comms/pilot_drafts_block.go) next to the existing pilot-issue block parser; reuse conventionalCommitTitleRE.
- Shippable check (new file, plain text: internal/comms/draft_check.go) per draft, pure Go, no LLM: (1) ValidateSpec from `internal/ghissue/spec.go`; (2) title matches conventionalCommitTitleRE; (3) every backticked path span (ExtractReferencedPaths from `internal/executor/dependency_detector.go`) exists in the project checkout at its default branch — the drafting turn runs inside the project worktree, so a stat against the checkout is enough; (4) `## Acceptance` has at least one bullet in the evidence dialect (SOP Rule 5b: a backticked allowlisted command with "paste the output into the PR body", or a mutation pin with `->` and "fails"); (5) `deps` reference known draft ids only. Result per draft: `shippable: {ok: bool, reasons: []string}` and `kind: "ticket"` when ok, `kind: "epic"` when not. No automatic re-split: an epic-candidate keeps its body; the console files it without `no-decompose` and the existing epic decomposition (`internal/executor/decompose.go`) splits it after approval. A shippable draft is filed with `no-decompose` by the console.
- Events: `internal/adapters/web/messenger.go` web.Event gains `drafts` (array, omitempty) of `{id, title, labels, body, deps, kind, shippable:{ok, reasons}}`; the turn emits progress with phase `drafting`, then one `drafts` event, then the usual completion. On parse failure: a `text` event with the reason and no `drafts` event.
- Counting: the drafting turn is reserved through reserveDailyTurn like any chat turn and recorded through SaveChatTurn (`internal/memory/chat_turns.go`).

## Acceptance
- [ ] `go test -race ./internal/comms/ -run TestDraftsBlock` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/comms/ -run TestDraftCheck` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestChatMessages` passes; paste the output into the PR body.
- [ ] A test posts action create_tickets with a two-draft fixture reply and asserts one `drafts` event whose raw JSON contains the literal keys "drafts", "shippable", "kind" and "deps" (strings.Contains on the body).
- [ ] remove the acceptance-dialect rule from the shippable check -> TestDraftCheck_AcceptanceDialectRequired fails
- [ ] remove the path-exists rule from the shippable check -> TestDraftCheck_BackticketPathMustExist fails
- [ ] A test asserts the drafting turn calls reserveDailyTurn (the 51st on a cap of 50 is refused with the daily-limit message).
- [ ] `go build ./...` passes; paste the output into the PR body.

## Out of scope
Filing issues, storing drafts, any console or UI change, a stored report, LLM-driven re-split.

### LEG P3 — repo: qf-studio/pilot — title: feat(memory): learning write from a console reject — POST /api/v1/learnings and a reason on the approval decision, injected into the next prompt for the project

## Context
Positioning §8.2 D2: a reject with a reason becomes a learning. Today the daemon stores memories in SQLite only (`internal/memory/knowledge.go` AddMemory) and has no endpoint for a reason; the approval decision body has {decision, by} only (`internal/gateway/approvals.go`). The console drops the reject reason (fixed by console leg C4, which calls what this leg adds). Decision D2 of TASK-585: SQLite now, files later.

probe: present qf-studio/pilot func[[:space:]].*AddMemory
probe: absent qf-studio/pilot \"/api/v1/learnings\"

## Scope
- `internal/gateway/approvals.go`: decisionRequestBody gains `reason` (string, optional, ≤ 2 KB). When decision is reject and reason is non-empty, the handler writes a learning (below) before returning.
- New route POST /api/v1/learnings (register next to the approvals routes in `internal/gateway/server.go`): body `{kind, reason, project, by, subject}` where kind ∈ {review_reject, draft_reject}, subject is `{cardId, draftId, taskId, prUrl}` (all optional strings). 400 on missing kind/reason/project; 404 on unknown project. Response `{id}`.
- Writer: AddMemory with type learning, source "console-review", tags [kind, project, subject ids], content = reason plus a one-line subject header. Idempotent on (kind, subject, reason hash) within 24 h.
- Injection: the executor's prompt memory block today comes from graph.json (`internal/executor/git.go` loadMemoryGraph and `internal/memory/graphrecall/recall.go`). Add the SQLite learnings for the project (newest 10, type learning, source console-review) to the same block so the next chat turn and the next task for that project see them. Keep the block bounded (≤ 2 KB of learnings).
- SyncToFiles stays a stub; note the hook in its comment.

## Acceptance
- [ ] `go test -race ./internal/gateway/ -run TestLearnings` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestApprovalDecisionReason` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/memory/ -run TestAddMemory` passes; paste the output into the PR body.
- [ ] A test posts a reject with a reason and asserts a learning row exists with source "console-review" and the project tag.
- [ ] A test builds the prompt memory block for a project with one console-review learning and asserts the reason text is present.
- [ ] drop the learnings source from the prompt memory block -> TestPromptMemoryBlock_IncludesConsoleLearnings fails
- [ ] `go build ./...` passes; paste the output into the PR body.

## Out of scope
Writing `.agent/` files or graph.json, a PR per learning, any console change.

no-decompose

---

## Legs — pilot-console

### LEG C5 — repo: qf-studio/pilot-console — title: feat(drafts): ticket-draft store, review-lane draft items, edit before approve, approve files the pilot issue and queues the card

## Context
The daemon (TASK-585 leg P2) emits a `drafts` chat event: an array of `{id, title, labels, body, deps, kind, shippable:{ok, reasons}}`. The UI posts that array to the console (decision D1). The console stores drafts, lists them in Review needed as type `draft` (reserved in `internal/boardapi/review.go`, never emitted), lets the owner edit, and on approve files the GitHub issue through the existing dispatch writer path (`internal/boardapi/dispatch.go` dispatchUnlinked: per-org writer, `pilot` label constant, linkAndSeed) and queues a card. No draft becomes an issue without approval (§8.3). Multi-part leg: no-decompose.

probe: present qf-studio/pilot-console dispatchUnlinked\(
probe: present qf-studio/pilot-console pilotTriggerLabel
probe: present qf-studio/pilot-console "draft"
probe: absent qf-studio/pilot-console CREATE TABLE drafts

## Scope
- Migration (new file, plain text, next number in internal/db/migrations): table drafts — id, org_id, project_id, conversation_id, daemon_draft_id, title, body, labels jsonb, deps jsonb (daemon draft ids), kind (ticket|epic), shippable jsonb, status (draft|approved|rejected|sent_back), reason, issue_number null, card_id null, created_at, created_by, decided_at null, decided_by null, version. UNIQUE (org_id, conversation_id, daemon_draft_id). Down migration drops the table.
- Routes (org-scoped like siblings, CSRF on writes): POST /api/v1/drafts — body `{conversation_id, project_id, drafts:[…daemon shape…]}`, upsert on the unique key, returns the stored rows; GET /api/v1/drafts?status= ; PATCH /api/v1/drafts/{id} — title, body, labels with `version` optimistic check (409 on mismatch); POST /api/v1/drafts/{id}/decision — `{decision, comment}` with decision ∈ {approve, reject, send_back}; comment required for reject and send_back.
- Approve: resolve deps — each dep must be an approved draft with an issue_number, else 409 `draft_dependency_pending` naming the pending draft ids; rewrite `#{{<daemon id>}}` tokens in the body to `#<issue_number>`; append `Blocked by: #N` lines for deps (SOP Rule 3); labels = draft labels ∪ {pilot} ∪ ({no-decompose} when kind is ticket); create the issue through the dispatchUnlinked writer path; create the card with status queued and link it (linkAndSeed); store issue_number, card_id, status approved, decided_by (actor). Reject: store reason, status rejected (forwarding to the daemon is leg C4). Send back: store comment, status sent_back (the human edits and re-approves; no daemon round trip in v1).
- Review lane: `internal/boardapi/review.go` emits drafts with status draft or sent_back as items of type `draft`: `why` = "Pilot drafted this ticket from research" (or "sent back: <comment>" for sent_back), `card_id` null, `actions` = ["approve", "reject", "send_back", "edit"], plus a new field `draft_id`. Ordering and grouping rules unchanged.
- Journal every decision on the activity feed like card decisions.
- Wire-contract tests: every new json-tagged field in the handlers/DTOs has a same-package test reading rec.Body.String() with strings.Contains on the literal field (pattern: TestHandleListAppliedSpecVersionJSON in `internal/instances/handlers_test.go`).

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] `make check-wire-contract-tests` passes; paste the output into the PR body.
- [ ] `make check-fleet-org-scoping` passes; paste the output into the PR body.
- [ ] A test posts the same two-draft batch twice and asserts two rows (upsert on the unique key).
- [ ] A test approves a draft whose dep is still a draft and asserts 409 with the pending id in the body.
- [ ] A test approves a shippable draft with a fake writer and asserts the writer received labels containing "pilot" and "no-decompose", the body contains "Blocked by: #" for a filed dep, and a card exists with status queued linked to the returned issue number.
- [ ] A test approves an epic-kind draft and asserts "no-decompose" is absent from the labels.
- [ ] A raw-body test on GET /api/v1/review asserts the literal `"type":"draft"` and `"draft_id"`.
- [ ] remove the dependency check from approve -> TestDraftApprove_PendingDependencyIs409 fails
- [ ] `make build` passes; paste the output into the PR body.
- [ ] `make lint` passes; paste the output into the PR body.

## Out of scope
Any daemon call (C4), viewer roles (U16), a GitHub App token path, UI.

no-decompose

### LEG C4 — repo: qf-studio/pilot-console — title: feat(review): reject reason write-through — forward the reason of a card or draft reject to the tenant daemon learnings endpoint

## Context
The review lane sends the reject reason as `comment` but the decision handler drops it for reject (`internal/boardapi/decision.go`: comment required only for send_back) and the daemon call sends {decision, by} only (`internal/approvals/client.go`). Daemon leg P3 (TASK-585) adds `reason` on the approval decision body and POST /api/v1/learnings `{kind, reason, project, by, subject}`. Draft rejects (leg C5) need the same forwarding. Blocked by: #{{C5}}

probe: present qf-studio/pilot-console "send_back"
probe: present qf-studio/pilot \"/api/v1/learnings\"

## Scope
- `internal/boardapi/decision.go`: comment is required for reject as well; on reject, pass `reason` to the daemon decision call (`internal/approvals/client.go` Decide gains a reason parameter; body `{decision, by, reason}`), and journal the reason in the activity feed detail.
- Drafts decision (leg C5 route): on reject, POST /api/v1/learnings on the org's active instance with kind draft_reject, reason, project, by (actor), subject {draftId}. Daemon unreachable → the draft decision still succeeds and the forwarding failure is journaled (ladder as in the decision docblock: 503/401/404/409/502 mapped, never a 500).
- Test fakes mirror the daemon structs byte for byte (field names above).

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] `make check-wire-contract-tests` passes; paste the output into the PR body.
- [ ] A test rejects a card with a reason and asserts the fake daemon received a body containing the literal `"reason":` with that text.
- [ ] A test rejects a draft and asserts the fake daemon received POST /api/v1/learnings with `"kind":"draft_reject"`.
- [ ] A test rejects a draft with the fake daemon returning 503 and asserts the draft is rejected and a journal row records the forwarding failure.
- [ ] drop the reason from the daemon decision body -> TestDecisionReject_ForwardsReason fails
- [ ] `make build` passes; paste the output into the PR body.

## Out of scope
Storing learnings on the console, UI changes (the reject composer already sends the reason).

### LEG C10 — repo: qf-studio/pilot-console — title: chore(allowance): research-turn cap — plan default daily_chat_turn_cap 200 → 50, label it as research turns, verify chat-turn ingestion per tenant

## Context
Positioning §8.3 D4: 50 research turns per day per org, tunable. No `mode` exists on the chat wire, so a research turn is a chat turn: the daemon enforces `adapters.chat.daily_turn_cap` (default 200, org-wide, in-memory) and the console renders that value from plan data (`internal/fleet/configrender.go`; migration 0030_daily_caps, default 200; overrides in org_allowance_overrides, NULL inherits, 0 unlimited). The allowance page shows chat_turns against daily_chat_turn_cap (`internal/allowanceapi/routes.go`). Decision D3 of TASK-585: one cap, set the plan default to 50.

probe: present qf-studio/pilot-console daily_chat_turn_cap
probe: present qf-studio/pilot DefaultDailyTurnCap

## Scope
- New migration (plain text, next number in internal/db/migrations): plan_allowances.daily_chat_turn_cap default 50 and an UPDATE of existing plan rows from 200 to 50; org overrides untouched. Down migration restores 200.
- Config render unchanged; add a test that a plan at 50 renders `daily_turn_cap: 50`.
- Allowance API: no field rename; add `daily_chat_turn_cap_label` = "research turns a day" so the UI never invents copy.
- Verify ingestion: a test that the ledger poller ingests daemon rows of kind chat_turn for a tenant and CountDaily (`internal/allowance/status.go`) counts them for the day; if the poller already does, the test documents it.
- Note in the PR body that the daemon counter is in-memory and resets on a daemon restart (known, accepted for v1).

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] `make check-wire-contract-tests` passes; paste the output into the PR body.
- [ ] A raw-body test on the allowance route asserts the literal `"daily_chat_turn_cap":50` for a fresh plan and `"daily_chat_turn_cap_label"`.
- [ ] A test renders tenant config for a plan at 50 and asserts the line daily_turn_cap: 50.
- [ ] set the migration default back to 200 -> TestPlanAllowanceDefaults_ChatTurnCapIs50 fails
- [ ] `make build` passes; paste the output into the PR body.

## Out of scope
A separate research counter, daemon changes, UI copy (U10 reads the label).

no-decompose

---

### LEG C11 — repo: qf-studio/pilot-console — title: feat(chat): relay the action field on POST /api/v1/chat/messages so Create tickets reaches the daemon

## Context
Daemon leg P2 (merged 2026-11-02, pilot#6010) added `action` to the chat message request: with `action: "create_tickets"` the `text` is the report and the daemon runs a drafting turn. The console relay builds its own request from parsed fields (`internal/chatapi/handlers.go` sendMessageRequest → `internal/chat/client.go` SendMessageRequest) and has no `action` field, so the console silently drops it and the UI's Create tickets (ui#287 / PR#291) never reaches the daemon. The pre-PR review of ui PR#291 flagged exactly this. The events relay is byte-faithful already, so the `drafts` event needs nothing.

probe: present qf-studio/pilot json:\"action,omitempty
probe: present qf-studio/pilot-console sendMessageRequest

## Scope
- `internal/chatapi/handlers.go`: sendMessageRequest gains `Action string` with json tag action, omitempty. Allowed values: empty or `create_tickets`; anything else → 400 `invalid_action`. With `create_tickets`, `text` is required (the report) and `projectId` is required; reuse the existing validation shape.
- `internal/chat/client.go`: SendMessageRequest gains Action (json action, omitempty) and the relay copies it through unchanged.
- No new persistence, no mode field, no change to the events route.
- Wire-contract test: a same-package test captures the request body sent to the fake daemon and asserts the literal "action":"create_tickets" (strings.Contains on the raw body), plus the 400 path for `action: bogus`.

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] `make check-wire-contract-tests` passes; paste the output into the PR body.
- [ ] `make build` passes; paste the output into the PR body.
- [ ] A test posts action create_tickets and asserts the fake daemon received a body containing "action":"create_tickets" and the same text and projectId.
- [ ] A test posts action bogus and asserts 400 with error invalid_action.
- [ ] drop the Action copy in the relay -> TestSendMessage_RelaysAction fails

## Out of scope
UI, daemon, the drafts store (C5), reject forwarding (C4).

no-decompose

---

## Legs — pilot-console-ui

### LEG U10 — repo: qf-studio/pilot-console-ui — title: feat(research): Create tickets under the report → drafts posted to the console → Review needed draft items with edit, approve, reject, send back

## Context
The Research page (`src/views/ChatView.vue`) renders the report as the last text event. Daemon leg P2 (TASK-585) accepts `action: "create_tickets"` on the chat message request with the report as `text` and emits one `drafts` event `{drafts:[{id, title, labels, body, deps, kind, shippable:{ok, reasons}}]}`. Console leg C5 stores drafts (POST /api/v1/drafts), lists them in GET /api/v1/review as type `draft` with `draft_id` and actions approve/reject/send_back/edit, and exposes PATCH /api/v1/drafts/{id} and POST /api/v1/drafts/{id}/decision. The dashboard input already hands off to this page (`src/views/DashboardView.vue` askPilot). Decision D1: the UI posts the drafts to the console. Decision D4: the mode pills are hidden (ui#266 a).

probe: present qf-studio/pilot-console-ui data-research-report
probe: present qf-studio/pilot-console-ui WireReviewItem
probe: present qf-studio/pilot-console json:"draft_id"
probe: present qf-studio/pilot json:\"drafts

## Scope
- Adapter (`src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`): ChatEvent gains optional `drafts` (WireDraft[]); ChatSendOptions gains `action?: 'create_tickets'`; new methods createTickets(conversationId, projectId, report), postDrafts(conversationId, projectId, drafts), listDrafts(status), patchDraft(id, {title, body, labels, version}), decideDraft(id, decision, comment). WireReviewItem gains `draft_id` (nullable). Refresh the vendored field list with scripts/sync-console-wire-fields.sh so scripts/check-wire-fields.sh passes.
- Research page: a `Create tickets` button under the report (disabled while a turn runs). Click → createTickets with the rendered report source; progress line shows the `drafting` phase; on the `drafts` event → postDrafts → toast "N drafts in Review needed" linking to the Review page; parse failure (`text` event, no drafts) → the text is shown as the reply.
- Review page (`src/views/ReviewView.vue`): items of type `draft` render the title, the why line, a `shippable` chip (ok → "shippable", else "epic candidate" with the reasons on hover), and actions Edit / Approve / Reject / Send back. Edit opens an inline editor (title, body textarea, labels) saved with PATCH and the version; a 409 reloads the item. Approve shows the issue link on success and removes the item; a 409 draft_dependency_pending names the pending drafts. Reject requires a reason; Send back requires a comment.
- Hide the RESEARCH / DOCS mode pills in src/design-system/components/ResearchComposer.vue (keep the prop for a later wire; closes ui#266 by decision a).
- Mock adapter: fixtures that are the exact JSON of the console and daemon shapes above.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.
- [ ] A wire-contract test decodes a fixture that is the exact JSON of a daemon `drafts` event into ChatEvent and asserts drafts[0].shippable.ok and kind.
- [ ] A view test clicks Create tickets, feeds a drafts event through the mock adapter, and asserts postDrafts was called once with the two drafts and the toast names 2.
- [ ] A view test on the Review page renders a draft item, edits the title, and asserts patchDraft received the new title and the version.
- [ ] A view test approves a draft and asserts the issue link renders and the item leaves the list.
- [ ] remove the postDrafts call from the drafts event handler -> ChatView_PostsDraftsOnDraftsEvent fails
- [ ] A test asserts the mode pills are not rendered.

## Out of scope
A drafts lane on the board (approved drafts appear as Queued cards through the existing board), viewer roles, report storage.

---

## Held legs (filed when upstream merges)

| Leg | Repo | After | Body source |
|---|---|---|---|
| C4 reject write-through | pilot-console | ✅ filed 11-02 as console#469 | §8.2 D2 |
| U10 Create tickets + draft review | pilot-console-ui | ✅ filed 11-02 as ui#287 | §8.3 |

Dispatch order: P2, P3, C5, C10 now (`nav-dispatch-legs.py` with `--only P2,P3,C5,C10`), then C4, then U10. Review every merge pre-merge where it touches a migration or a public route (C5, C10); the rest per the tier-1 policy.

---

## Refs

- Revisions/fast-follows 11-02: console#474 (PR#473) · ui#296 (PR#292) · ui#297 (PR#293, after #296) · console fast-follow to PR#476 (test-quality) — see git log for the number
- P2 → https://github.com/qf-studio/pilot/issues/6010
- P3 → https://github.com/qf-studio/pilot/issues/6011
- C5 → https://github.com/qf-studio/pilot-console/issues/459
- C10 → https://github.com/qf-studio/pilot-console/issues/460
- C4 → https://github.com/qf-studio/pilot-console/issues/469
- U10 → https://github.com/qf-studio/pilot-console-ui/issues/287
- C11 → https://github.com/qf-studio/pilot-console/issues/475
- TASK-584 Phase C (wave 2) · positioning §8.3, §8.2 D2 · roadmap rows P2, P3, C4, C5, C10, U10
- ui#266 (mode pills decision → D4) · pilot#5810 (P17 chat events, separate)
- Seam map 2026-11-02 (navigator-research, this doc § Context)

---

**Last Updated**: 2026-11-02
