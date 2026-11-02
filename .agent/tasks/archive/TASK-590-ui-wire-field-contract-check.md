# TASK-590: UI wire-field contract check — CI fails when a Wire* field has no producer json tag

**Status**: ✅ MERGED 2026-10-26 — ui#279 → PR#281 (wire-field check live in the UI test job; per-route scoping is the follow-up) → qf-studio/pilot-console-ui, labels pilot + no-decompose.
**Spec refs**: pitfall mem-274 (UI legs bind to wire fields the counterpart API never sends — 4 instances in wave 1); SOP new-project-issue-authoring Rule 3d (probe lines).
**Origin**: founder call 2026-10-26 — stop blanket post-merge reviews; make CI catch the wire-contract class.

## Context

Three of six wave-1 UI PRs (ui PR#252, #259/#261, #255) bound to fields the console never sends; CI stayed green because unit tests mock the adapter. The console side has a producer gate (scripts/check-wire-contract-tests.sh) but nothing guards the consumer. In pilot-console-ui the wire types live mostly in `src/lib/api/httpAdapter.ts` (57 unexported interface Wire* declarations) and a few in `src/lib/api/types.ts`; producer files are named only in prose docblocks. Fixtures under `src/lib/api/__tests__/fixtures/README.md` are source-derived from console DTOs at a named console commit with no automated sync. CI (`.github/workflows/ci.yml`) runs make build, make test, make lint only.

## Legs — pilot-console-ui

### LEG W1 — repo: qf-studio/pilot-console-ui — title: ci(contract): wire-field check — fail when an interface Wire* field has no json tag in the vendored console DTO field list

## Context
Wave-1 UI PRs #252, #259/#261 and #255 declared wire fields (orgName, poolPercentUsed, timeToPrMinutes, project, parentId) that the pilot-console DTOs never emit; unit tests mock the adapter so CI stayed green and every tenant rendered dashes. The wire types are the 57 unexported interface Wire* declarations in `src/lib/api/httpAdapter.ts` plus the few in `src/lib/api/types.ts`. The producer is pilot-console: json tags in its internal/*/dto.go and internal/*/handlers.go files. CI in `.github/workflows/ci.yml` runs make build, make test and make lint. There is no scripts directory and no contract check today. The check must not need a cross-repo token at CI time.

probe: present qf-studio/pilot-console-ui interface WireDashboard
probe: present qf-studio/pilot-console-ui make test
probe: absent  qf-studio/pilot-console-ui check-wire-fields
probe: present qf-studio/pilot-console json:"needsYou"

## Scope
- Vendored producer field list: new file src/lib/api/__contracts__/console-wire-fields.txt, one json tag name per line, sorted, generated from pilot-console at a pinned commit; new file src/lib/api/__contracts__/console-ref.txt holding that commit SHA. Both are committed. The initial list is generated from pilot-console main at the time of the PR and the PR body names the SHA.
- Sync script: new file scripts/sync-console-wire-fields.sh. Given a local pilot-console checkout path (argument) and the ref in console-ref.txt, it extracts every json tag name from that repo's internal/*/dto.go and internal/*/handlers.go with the same grep the console gate uses (json:"name) and rewrites console-wire-fields.txt. A developer or Pilot runs it when binding to a newly merged console field and bumps console-ref.txt in the same PR.
- Check script: new file scripts/check-wire-fields.sh, bash plus grep only. It scans every interface Wire* block in `src/lib/api/httpAdapter.ts` and `src/lib/api/types.ts`, collects field names (a per-line name followed by optional question mark and colon, between the braces), normalises both sides to lowercase with underscores removed (poolPercentUsed matches pool_percent_used), and exits 1 listing every UI field with no match in console-wire-fields.txt. A field may be exempted with a same-line comment wire-allow: followed by a reason; the script prints exemptions so they stay visible. Nested object fields count like top-level ones.
- Wiring: a check-wire-fields target in the Makefile and one step in the existing test job of `.github/workflows/ci.yml` that runs it after make test.
- Docs: one paragraph in `src/lib/api/__tests__/fixtures/README.md` — when you bind to a console field, run the sync script, bump console-ref.txt, commit both.
- Known limit, stated in the script header: the match is global across all console DTOs, not per route; a name that exists on another DTO passes. Per-route scoping is a later leg.

## Acceptance
- [ ] `make check-wire-fields` passes on the PR head; paste the output into the PR body.
- [ ] Add a field named bogusField to interface WireDashboard in httpAdapter.ts -> make check-wire-fields fails and names bogusField.
- [ ] Add a field named pool_percent_used to any Wire interface -> make check-wire-fields fails (the console never emits it); add the wire-allow comment on that line -> it passes and prints the exemption.
- [ ] A unit test under src/lib/api/__tests__ runs the check script on a fixture file containing the four PR#252 names orgName, lastShippedAt, poolPercentUsed, timeToPrMinutes and asserts all four are reported.
- [ ] The PR body names the pilot-console SHA in console-ref.txt and shows the sync script run output.
- [ ] `npm run build`, `npm test`, `npm run lint` pass — paste the output into the PR body.

## Out of scope
- Per-route scoping of the match (follow-up once the global check is in).
- Fetching the console repo at CI time (needs a cross-repo token; the vendored list avoids it).
- Changes to package.json (use the Makefile target).

## Refs

## Refs

- W1 → https://github.com/qf-studio/pilot-console-ui/issues/279
- Pilot issue: https://github.com/qf-studio/pilot-console-ui/issues/279

**Last Updated**: 2026-10-26
