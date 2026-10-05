# TASK-511: console-ui Plan & Billing — included allowance, exhausted state, own-key prompt, provision gating (TASK-502 leg 4)

**Status**: ✅ **ALL THREE MERGED + REVIEWED 2026-10-05** — ui#197 → PR#200 (15:45Z) · ui#198 → PR#201 (15:59Z) · ui#199 → PR#202 (16:08Z); merge commits verified at the head of main; post-merge reviews APPROVE-w-notes on all three (~16:30Z). **LIVE 16:11Z**: "Deploy console UI" now runs automatically on CI success on main (ui#178); live `build-sha.txt` = 07f57a7 = PR#202 merge, leg 4 strings confirmed in the served `SettingsBillingView` and `InstancesView` chunks. Remaining Phase 4: founder real-stack verify of the three billing states + Connections row (screenshots). Review notes: two windows in the "Last 30 days" card · soft-cap subtitle edge · RouterLink-wrapping-BaseButton a11y · Configure link stays when a pool key is assigned · "required" badge only when already satisfied. Gate finding: `bun run test …` acceptance bullets rendered "command not in allowlist" on all three PRs (SOP says bun is allowed) — see Notes.
**Created**: 2026-10-05
**Assignee**: Pilot (console-ui) · Navigator plans · founder reviews copy + dispatches the UI deploy
**Parent**: TASK-502 (leg 4) · **Plan**: approved 2026-10-05 (3 nav-research agents + plan critique)

---

## Context

**Problem**: Legs 0–2 shipped the plan allowance, metering and `GET /api/v1/billing/allowance`; leg 1 shipped the platform key pool and `platform_key` on the credentials listing. The SPA reads none of it: Plan & Billing still says "Model tokens are yours — billed by Anthropic, never marked up" (false under TASK-502), shows "Your Anthropic tokens —", Connections calls the Anthropic key "required", and the client-side provision pre-check blocks an org the console would serve from the pool.

**Goal**: The customer sees "X of N tickets · $Y of $Z used" for the current billing period, an exhausted state with terms-of-plan copy and an own-key prompt; Connections/Instances show the lent Pilot key, the own key is optional, and Provision defers to the server's 412. Numbers come only from the endpoint, so a `consolectl billing set-allowance` edit is live without a UI deploy.

**Founder decisions 2026-10-05**: slice C (provision gating) is part of leg 4; two visual states only (normal, exhausted) — no client-side 80 % tone; thresholds/alerts stay in leg 3.

---

## Known Pitfalls & Patterns

- **PITFALL** (98%, mem-195): a production UI build once shipped the mock adapter (`VITE_API_MODE` unset → mock); every browser milestone must carry one wire proof. → Phase 4 real-stack verify runs vite with `VITE_API_MODE=http` and asserts a `/api/v1/billing/allowance` request in the network log.
- **PITFALL** (TASK-460 class, consumer-docblock-trusted-over-producer-wire-source): field semantics come from the producer. → Every DTO field in the issues is quoted with its type from the Go structs; fixtures are substring-pinned to the Go raw-body tests.
- **PITFALL** (plan critique): `listCredentials` has 21 `vi.spyOn` spies in view specs; bypassing it from the session store breaks them silently. → `platform_key` is read by a separate `getPlatformKey()` verb (the `getPushRejected` idiom), `listCredentials` untouched.
- **PITFALL** (plan critique): the server does nothing on exhaustion until leg 3 (`allowanceapi` package doc: "nothing here suspends an instance, switches keys, or alerts"). → Exhausted copy states plan terms, never a switch; the factual `key_source` line stays beside it.
- **PITFALL** (plan critique): Onboarding auto-provisions on "all steps met" and swallows the 412. → Onboarding stays copy-only; the Anthropic step remains required there.
- **PATTERN** (mem-202): merged ≠ live; the UI deploys only on dispatch. → Done requires the deploy dispatch + real-stack SOP, not the merge.

---

## Acceptance Criteria

- [ ] `getAllowance()` and `getPlatformKey()` exist on `ConsoleAdapter`, http + mock in lockstep, with fixtures substring-pinned to the Go tests (`"usage":{"tickets":12,"usd_cents":1375,"key_source":"own","as_of":"2026-10-01T09:00:00Z"}`, `"remaining_usd_cents":8625`, `"platform_key":{"assigned":true,"label":"pool-03","hint":"…igAA"}`, `"platform_key":{"assigned":false}`).
- [ ] Plan & Billing renders the "Included this period" card from the endpoint: tickets and dollars, dollar-based meter, period reset date + source label, as-of/no-usage caption, factual key line; `hard_cap:false` → "No hard cap on this plan"; any fetch failure → card absent and subtitle fallback.
- [ ] Exhausted state: warn chip, red meter, callout with terms-of-plan copy; two variants (own key configured / not), the latter linking to Connections.
- [ ] Stale copy replaced: subtitle, "Your Anthropic tokens" tile (now `usage.usd_cents`), docblocks; Connections row copy; Onboarding step copy. Plan name "Team" and Includes row untouched (leg 5).
- [ ] Connections Model row: badge "optional" whenever no own key is configured; assigned pool key shown as `pool-NN (…last4)`; Save path unchanged.
- [ ] Instances: Provision disabled only by `github`; anthropic ladder row warning-toned/optional, satisfied when a pool key is assigned; 412 with `anthropic` renders "Pilot couldn't lend an Anthropic key for this box — add your own on Connections."
- [ ] `make build`, `make test`, `make lint` green on each PR; spec `✓` lines pasted; mutation pins hold.
- [ ] Real-stack verify (SOP) with one wire proof per state; screenshots of normal / exhausted-no-key / exhausted-with-key on the PR; founder copy review before the UI deploy dispatch.

---

## Implementation

### Phase 1: leg 4a — adapter, types, fixtures (issue 1, S)
**Goal**: the data layer, no view change.

**Tasks**:
- [ ] `Allowance`, `AllowanceSource`, `BillingPeriodSource`, `KeySource`, `PlatformKey` types; `getAllowance()` + `getPlatformKey()` on `ConsoleAdapter`
- [ ] `httpAdapter`: `WireAllowance` + `toAllowance`; `WireCredentialsEnvelope.platform_key?` + `toPlatformKey` (absent → `{assigned:false}`)
- [ ] `mockAdapter` parity (allowance = the Go `_Usage` figures; platform key unassigned by default)
- [ ] `session` store: `platformKey` + `loadPlatformKey()` mirroring `loadPushRejected`
- [ ] Fixtures `allowance.json`, `allowance-no-snapshot.json`, `credentials-platform-key.json` + README bullets; `httpAdapter.spec.ts` + `mockAdapter.spec.ts` cases and two mutation pins

**Files**: `src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`, `src/stores/session.ts`, `src/lib/api/__tests__/httpAdapter.spec.ts`, `src/lib/api/__tests__/mockAdapter.spec.ts`, `src/lib/api/__tests__/fixtures/README.md`

### Phase 2: leg 4b — Plan & Billing card (issue 2, M, depends on 4a)
**Goal**: the customer-facing allowance surface.

**Tasks**:
- [ ] "Included this period" card: headline, dollar-based meter (`bg-rail` track, `bg-success`/`bg-error` fill; no bar when `includedUsdCents === 0` or `hardCap === false`), caption (reset date UTC, period source label, as-of), key line
- [ ] Exhausted state: chip `warn` "allowance used up", callout (terms-of-plan copy, two variants), link to `/connections`
- [ ] Copy: subtitle from `includedTickets` (fallback "One plan, one box."), tokens tile → "Model spend this period", docblocks, design citation + `no mock — vocabulary extension`
- [ ] Spec: `makeAllowance()` factory, all states incl. fetch rejected; update the "Your Anthropic tokens" pin

**Files**: `src/views/SettingsBillingView.vue`, `src/views/__tests__/SettingsBillingView.spec.ts`

### Phase 3: leg 4c — Connections / Instances / Onboarding (issue 3, M, depends on 4a)
**Goal**: the own key is optional; the server decides.

**Tasks**:
- [ ] `provisionRequirements.ts`: `getMissingRequirements` unchanged (mock ladder mirror); add `isClientBlocking(token)` (github only); re-document `REQUIRED_CREDENTIALS` as badge semantics; fix the stale handler cite
- [ ] `InstancesView.vue`: disable on `missing.some(isClientBlocking)`; anthropic row warning/optional, satisfied when `platformKey.assigned`; `MissingConnectionsError` with `anthropic` → the pool message; `loadPlatformKey()`
- [ ] `ConnectionsView.vue`: badge rule, row copy, assigned sub-line, `loadPlatformKey()`, design divergence docblock
- [ ] `OnboardingView.vue`: copy only; step stays required
- [ ] Specs: Instances enabled + warning row + 412 branch; Connections `'optional'`; mock spec parity holds

**Files**: `src/lib/provisionRequirements.ts`, `src/views/InstancesView.vue`, `src/views/ConnectionsView.vue`, `src/views/OnboardingView.vue`, their specs

### Phase 4: ship (founder)
- [ ] Post-merge review on each PR (verdict as a COMMENT review, first line = verdict)
- [ ] UI deploy dispatch; real-stack verify per `sops/quality/real-stack-verify-gates-ui-merges.md` with `consolectl billing set-allowance` edits and `consolectl platform-key assign`; wire proof in the network log
- [ ] Archive this doc; update TASK-502 leg 4 status

---

## Out of Scope

- Leg 3 enforcement, 80 %/100 % alerts, key switching (server); any client-side threshold tone
- Leg 5: plan name "Pilot Console", $499, Paddle catalog/description (page and catalog must move together)
- Allowance editing from the UI; usage history
- Making the Onboarding Anthropic step optional (needs a visible 412 branch in auto-provision first — file separately if wanted)
- Any console (Go) change; `routes.go` staying outside the console's wire-contract CI gate is a known gap (UI fixture is the only cross-repo pin)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| How the UI reads `platform_key` | `listCredentials` returns an envelope · delegate to a new overview verb · separate `getPlatformKey()` | **`getPlatformKey()`** (re-GET, read one field, like `getPushRejected`) | The other two bypass `listCredentials` from the store and break 21 spec spies silently; one extra GET on mount is the price `getPushRejected` already pays |
| "No cap" detection | `remaining_usd_cents` null · `hard_cap === false` | **`hard_cap`** | Producer floors remaining at 0 with no unlimited form |
| Meter basis | tickets · dollars · max of both | **dollars** | `exhausted` is dollar-only on the server; tickets never flip it |
| Exhausted copy | describe the switch · terms-of-plan | **terms-of-plan** | Server does nothing on exhaustion until leg 3 |
| Allowance fetch failure | inline note on 500, hide on 404/503 · hide on any failure | **hide on any failure** | Matches the page's sibling-fetch degrade idiom |
| Provision gating | client mirrors the ladder · client blocks on github only, server 412 decides anthropic | **github-only client block** | Pool availability is not on the wire; the server ladder is the source of truth |
| Onboarding | make step optional · copy only | **copy only** | Auto-provision + swallowed 412 would dead-end |
| Visual states | normal/warn(80 %)/exhausted · normal/exhausted | **two states** | Founder call; thresholds belong to leg 3 |

---

## Verify

```bash
# in pilot-console-ui, per PR
make build && make test && make lint
bun run test src/lib/api/__tests__/httpAdapter.spec.ts src/lib/api/__tests__/mockAdapter.spec.ts
bun run test src/views/__tests__/SettingsBillingView.spec.ts
bun run test src/views/__tests__/InstancesView.spec.ts src/views/__tests__/ConnectionsView.spec.ts
```

---

## Done

- [ ] Three console-ui PRs merged and reviewed; CI green on each
- [ ] Fixtures pin the Go wire substrings; mutation pins fail as specified
- [ ] UI deployed; real-stack verify: `set-allowance --tickets 5 --usd 1` reflected live, exhausted state reproduced, `platform-key assign` shows `pool-NN` and enables Provision without an own key, fresh org with pool off gets the 412 copy
- [ ] Screenshots on the PRs; founder copy review done

---

## Refs

- Parent: `.agent/tasks/TASK-502-included-tokens-platform-key.md` (leg 4)
- Producer contract: pilot-console @ f4c9ff4 — allowanceapi routes.go:86-122 + routes_test.go `TestAllowanceWireContract*`; orgs handlers.go:748-784 + handlers_test.go `TestListCredentialsWireContract_PlatformKey`; instances handlers.go:521-536, 582-585 (412)
- UI surface: pilot-console-ui @ c9966b0 — `SettingsBillingView.vue`, `ConnectionsView.vue`, `InstancesView.vue`, `OnboardingView.vue`, `provisionRequirements.ts`, `httpAdapter.ts`, `session.ts`
- Issues: ui#197 (4a), ui#198 (4b), ui#199 (4c) — filed 2026-10-05 ~15:30Z; bodies carry the Go wire substrings verbatim (no sibling-repo reads)

---

## Notes

- 2026-10-05 evidence-gate finding: all nine `bun run test <spec>` paste-output bullets on PR#200/#201/#202 were classified "command not in allowlist". Root cause is code, not config: the "paste the output" phrase branch collects EVERY inline code span of the bullet as a command, so the first non-command span (`warning`, `pool-03`, …) fails the allowlist and the valid command never runs; GH-5514 fixed this only for the "`cmd` passes" shape. Fix filed as pilot#5617 (#5616 refiled: it named a non-existent test file). Authoring rule until it ships: a paste-output bullet may contain no inline code span other than the command(s); write other identifiers in plain text or quotes (SOP Rule 5b updated).

---

**Last Updated**: 2026-10-05
