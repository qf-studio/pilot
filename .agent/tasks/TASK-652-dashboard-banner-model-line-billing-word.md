# TASK-652: Dashboard banner — model line and a plain billing word, both inside the banner

**Status**: 🚀 **D1 DISPATCHED 2026-11-13** → [#6350](https://github.com/qf-studio/pilot/issues/6350) (`pilot` + `no-decompose`); display change, normal review
**Created**: 2026-11-13
**Assignee**: Pilot

---

## Context

**Problem**: after the box config got two models (`model_routing.complex: claude-opus-5-5`, `default_model: claude-sonnet-5-5`) the model text left the banner: the fit rule on line 1 drops a segment that does not fit, and "opus-5-5 / sonnet-5-5" needs 51 columns where 49 are free. The text "billing subscription" under the banner is unclear to the operator. It is drawn outside the banner, so the `b` key does not hide it.

**Goal**: a banner of two lines. Line 2 holds the model text on the left and one plain word for the billing mode on the right. The `b` key hides both lines.

```
 pilot ● · v2.320.1 · stage                       up 10m · 16:36 utc
 opus-5-5 / sonnet-5-5                                  subscription
╭─ tokens ────────────╮╭─ cost ──────────────╮╭─ queue · outcomes ──╮
```

**Founder decisions 2026-11-13**: the model text goes under the name and version, on the left · the billing text is one word by mode, "subscription" or "api tokens", with no word "billing" · both are part of the banner.

**Defaults taken by Navigator (not asked; change by a comment on the issue)**: an unset mode shows the credential in use · an override keeps the one word and gets the warning colour · the models are the complex model and the default model, as today · the model text is set at start only, as today · the update card and the stale-ledger card stay visible with the banner hidden, as today.

---

## Known Pitfalls & Patterns

- **PITFALL** (research 2026-11-13): the dashboard width is 69 columns only outside stacked mode; with the git graph shown and a window of 70 to 89 columns the banner stretches. → D1 tests both 69 and 80.
- **PITFALL** (research 2026-11-13): `padLeftRightLine` drops the left part on overflow. → D1 cuts the left part with an ellipsis instead.
- **LEARNING** (founder correction 2026-11-13): check the width source before a statement about layout; "make the window wider" was wrong for a fixed-width panel.
- **DECISION** (mem-148): the dashboard grid code is a port of the grom layout. → D1 keeps the header height in the grid calculation correct (scope item 6).

---

## Acceptance Criteria

- [ ] With two different models configured, the banner shows both on line 2 at width 69.
- [ ] The text "billing" is absent from the dashboard header; the mode reads "subscription" or "api tokens".
- [ ] The `b` key hides both banner lines; no model text and no billing word stay on screen.
- [ ] Every rendered line keeps the dashboard width (69, and the stacked widths up to 89).

---

## Implementation

### Phase 1: leg D1 (Pilot)

- [ ] **D1** two-line banner → [#6350](https://github.com/qf-studio/pilot/issues/6350), dispatched 11-13.

---

## Legs — pilot (daemon)

### LEG D1 — repo: qf-studio/pilot — title: feat(dashboard): two-line banner — model text on line 2 left, the billing mode as one plain word on line 2 right, both hidden with the banner

probe: present qf-studio/pilot func[[:space:]]billingChipText\(
probe: present qf-studio/pilot func[[:space:]]\(m[[:space:]]Model\)[[:space:]]renderChromeHeader\(
probe: present qf-studio/pilot func[[:space:]]wireBillingChip\(
probe: absent  qf-studio/pilot func[[:space:]]billingModeLabe[l]\(

## Context

The terminal dashboard is in `internal/dashboard/tui.go`. The banner is one line from renderBanner: the name, a dot, the version, then the environment name and the model text while they fit, and the uptime and the clock on the right. The model text comes from applyDashboardBannerMeta in `cmd/pilot/main.go` (the complex model and the default model, joined with " / " when they differ). When the model text does not fit, the fit rule drops it; with two models it never fits at the normal width of 69 columns.

A second text, "billing MODE", comes from billingChipText and is drawn by renderChromeHeader as its own line under the banner, the update card and the stale-ledger card. The `b` key (showBanner) does not hide it. The daemon sends the mode in a BillingModeMsg from wireBillingChip in `cmd/pilot/billing_mode.go`.

Target at width 69 (one space of indent on the left, one space of pad on the right, as line 1 has):

```
 pilot ● · v2.320.1 · stage                       up 10m · 16:36 utc
 opus-5-5 / sonnet-5-5                                  subscription
```

## Scope

1. renderBanner returns two lines. Line 1 is as today without the model text; the environment name keeps its fit rule. Line 2 has the model text on the left, lower case, in the style it has today, and the billing word on the right.
2. A new function billingModeLabel replaces billingChipText. It returns "subscription" for the mode subscription and "api tokens" for the mode api_key. The word "billing" is not shown. With an override in force (a source other than empty or "config") the word is the same and gets the warning style; the source is not shown.
3. Unset mode: the message from wireBillingChip carries the mode in use. When the configured mode is unset, send the result of DerivedBillingMode (`internal/executor/key_source.go`), so the label is "subscription" or "api tokens" and never "unset". When no mode can be derived, the right side of line 2 is empty.
4. Remove the separate billing line from renderChromeHeader. With showBanner false, neither line of the banner is drawn. The update card and the stale-ledger card stay as they are.
5. Line 2 rules: with no model text the left side is empty; before the first BillingModeMsg the right side is empty; line 2 is always drawn while the banner is shown, so the layout does not jump when the message arrives. When left and right do not fit with a gap of two columns, cut the left text and end it with "…"; the right text is never cut.
6. The height that the grid uses for the header (the code that calls renderChromeHeader for the layout) follows the new line count; no panel is cut by one row.
7. The line "model stack" of the config panel and the output of `pilot status` do not change.
8. Tests in `internal/dashboard/tui_test.go` and `internal/dashboard/grom_chrome_test.go`, table-driven: two models and subscription at width 69 (both texts present, every line 69 wide); the same at a stacked width of 80; mode api_key gives "api tokens"; an override gives the same word; an unset mode with a derived value; no model text; no BillingModeMsg yet (two lines, right side empty); a model text of 70 characters (cut with "…", the billing word complete); showBanner false (the rendered header holds neither the model text nor the billing word); no rendered header line holds the text "billing". Update TestRenderBanner, TestRenderBannerNarrowDropsSegments and TestTUI_HeaderBillingChip to the new layout.

## Acceptance

- [ ] `go test -race -count=1 ./internal/dashboard/ ./cmd/pilot/ -run 'Banner|Billing|WidthInvariants|ChromeHeader'` passes; paste the output into the PR body.
- [ ] Paste the two rendered banner lines for both modes (from a test log) into the PR body.
- [ ] Draw the billing word outside renderBanner again -> the showBanner test fails.
- [ ] Return "unset" from the label -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A key that changes the billing mode. More models on the line (trivial, simple, medium, planning). A refresh of the model text on a config reload. The cost card and the price tables. The desktop app and the console.

## Refs

- Navigator TASK-652 in this repo · PR #6061 (the billing line) · GH-2455 (banner)

---

## Out of Scope

- A billing switch key in the dashboard (founder chose display only, 2026-11-13).
- The price table defects found on 2026-11-13 (Sonnet cache read rate, `memory.EstimateCost`, replay recorder); not approved yet.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Billing text | "billing subscription"; one word by mode; both words with the active one lit; a toggle key | one word by mode (founder call 11-13) | plain, short, no control that starts per-token charges |
| Place of model text | line 1 with a fit rule; line 2 left | line 2 left (founder call 11-13) | two models never fit on line 1 at 69 columns |
| Unset mode | show "unset"; show the credential in use | the credential in use | the operator asks "who pays", not "what is configured" |
| Overflow on line 2 | drop the left text; cut it with an ellipsis | cut with an ellipsis | a dropped model text is how this task started |

---

## Verify

```bash
go test -race -count=1 ./internal/dashboard/ ./cmd/pilot/ -run 'Banner|Billing|WidthInvariants|ChromeHeader'
```

---

## Done

- [ ] D1 merged and released; the box header shows `opus-5-5 / sonnet-5-5` and `subscription` on line 2.
- [ ] The `b` key hides both lines on the box.

---

## Refs

- `internal/dashboard/tui.go` · `cmd/pilot/billing_mode.go` · `cmd/pilot/main.go`

---

**Last Updated**: 2026-11-13

## Refs

- D1 → https://github.com/qf-studio/pilot/issues/6350
