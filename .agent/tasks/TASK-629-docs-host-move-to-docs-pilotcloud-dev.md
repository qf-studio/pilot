# TASK-629: Docs host move — pilot.quantflow.studio → docs.pilotcloud.dev (move, post, 301, retire)

**Status**: 🚧 2026-11-01 — inventory done (navigator-research + org code search). **Founder decision (revised 11-01 19:5x CET): M1 + M2 merge TODAY, before the Monday 10:00 301.** M2 = [site#28](https://github.com/qf-studio/pilotcloud-site/pull/28) merged (squash aaca127, deploy running). M1 = [pilot#6004](https://github.com/qf-studio/pilot/pull/6004) merges tonight when its Go check passes; PILOT WEBSITE verifies sitemap.xml + og:url on docs.pilotcloud.dev after the docs deploy. M1 + M2 are owned by the PILOT WEBSITE session (`local_dd757edb-3eaa-4925-a3a3-707d6efea192`): M1 = branch `docs/base-url-docs-pilotcloud-dev` (10 files, 13 lines, identical to the inventory), M2 = site `docsUrl`; PRs today, merge Monday 09:00. This session owns M3–M5, P1, P4 and the Slack reply. No Pilot issues for M1/M2.
**Created**: 2026-11-01 · **Owner**: Navigator plans, Pilot executes the two code legs, Nelya owns the edge · **Related**: TASK-492 (pilot-docs on AWS), TASK-515 (pilotcloud-site), TASK-584 (Pilot Cloud roadmap)

## Why

Pilot Cloud lives on `pilotcloud.dev`; the docs still answer on `pilot.quantflow.studio`. Nelya shipped phase 1 on 11-01 (new edge cert on `quantflow-cdn`, ALB host rule, Route 53 records): **both hosts serve byte-identical pages today**, so every reference can move before the redirect exists. Phase 2 (the 301) is booked for **Mon 2 Nov 10:00 CET**. Doing the code move first means the canonical, sitemap and og:url already name the new host when crawlers meet the 301, and the old host keeps working if anything slips.

## Inventory (2026-11-01, all repos + external)

Live product surfaces (must move):

| Surface | Where | Reaches prod via | Leg |
|---|---|---|---|
| Canonical, og:url, og:image ×2, twitter image | pilot `docs/app/layout.tsx` 10/19/22/31 | merge → `sync-docs.yml` → pilot-docs prod tag → image → Nelya's deploy (invalidates `/*`) | M1 |
| robots `sitemap:` line, sitemap `BASE` (67 URLs) | pilot `docs/app/robots.ts` 9, `docs/app/sitemap.ts` 3 | same | M1 |
| README docs badge, issue chooser "Documentation", installer echo | pilot `README.md` 25, `.github/ISSUE_TEMPLATE/config.yml` 4, `getting-started/setup.sh` 84 | live on merge | M1 |
| brew `homepage`, helm `home`, helm NOTES link, next.config comment | pilot `.goreleaser.yaml` 103, `deploy/helm/pilot/Chart.yaml` 13, `templates/NOTES.txt` 54, `docs/next.config.mjs` 8 | next `v*` release / chart publish | M1 (same PR; ships later, harmless) |
| Header + footer "Docs" link | pilotcloud-site `lib/site.ts` 3 (`docsUrl`) | merge → Nelya's deploy workflow (PR#27) | M2 |
| Two docs links | navigator-docs `content/community/index.mdx` 7, `content/integrations/pilot.mdx` 3 | merge → `build.yml` → **manual `prod-*` tag** → deploy | M3 (manual PR) |
| Project card link on quantflow.studio | qf-website `lib/projects.ts` 48 | merge → next website `prod-7.x` release | M4 (manual PR, rides the next release) |
| Deploy probe + CloudFront alias lookup | pilot-docs `.github/workflows/deploy-quantflow-aws.yml` 34/129/132 (`PUBLIC_HOST`) | takes effect on the next sync | M5 (manual PR; Nelya's file, she reviews) |
| GitHub `pilot` homepage, `pilot-docs` description | GitHub metadata | `gh repo edit` | P1 |
| Search Console | `sc-domain:quantflow.studio` covers `pilot.`; **`pilotcloud.dev` has no property** | founder, Search Console UI | P2 |

Not moving (history / unreachable): 19 `.agent/` files in pilot, QuantFlow research notes, pilot-pr drafts, a published LinkedIn post. No Go source carries the host (checked `internal/`, `cmd/`); no pilot workflow probes it.

## Phases

**A — MOVE (today, before the 301).** M1 ∥ M2 (PILOT WEBSITE session) ∥ M3 ∥ M4 ∥ M5 (this session, one-line PRs). Gate to phase C: `curl -s https://docs.pilotcloud.dev/sitemap.xml` lists only the new host; `og:url` on `/` is the new host; `/refunds`-class probe not needed here.
**B — POST.** P1 GitHub metadata · P2 Search Console property for `docs.pilotcloud.dev` + submit the new sitemap · P3 Slack reply to Nelya (thread `1793286994.415889`): base URL switched, Monday confirmed, M5 PR for her review · P4 Navigator docs: rewrite `system/references/reference_docs_deploy_pipeline.md` (still describes the GitLab chain) for the GitHub → pilot-docs → AWS chain and the new host; `system/docs-and-history.md` host line.
**C — 301 (Nelya, Mon 2 Nov 10:00 CET).** Precondition: phase A gate green. Verify after: `curl -sI 'https://pilot.quantflow.studio/cli/commands/?x=1'` → 301, `location: https://docs.pilotcloud.dev/cli/commands/?x=1`; `/` → 301; HSTS still present on both; new host still `Hit from cloudfront`. P2 step two: Search Console **Change of Address** from the old property to the new one (available once the 301 is live).
**D — RETIRE.** Keep the 301 **≥ 12 months** (Google's guidance for moves; the published LinkedIn post and the Homebrew formula of every pre-move release point at the old host). Retire = remove the last references, not the redirect: M5 merged, P1 done, TASK-492 step 4 (archive GitLab `quant-flow/pilot-docs`, drop the dead config block, delete `GITLAB_SSH_KEY`), update the two `.agent/system` docs, archive this task. Dropping the old alias and cert is a separate founder decision no earlier than 2027-11.

## Manual legs (not Pilot projects)

- **M3 navigator-docs** (two mdx links): one-line PR from a worktree, then `prod-*` tag → deploy (same chain as 10-07).
- **M4 qf-website** (`lib/projects.ts` 48): one-line PR; ships with the next `prod-7.x` website release. Redirect hop until then, acceptable.
- **M5 pilot-docs** (`deploy-quantflow-aws.yml` `PUBLIC_HOST`): one-line PR, Nelya reviews (her file). Must land before the old alias is ever removed; should land before the 301 so the post-deploy probe hits a 200, not a 301.
- **P1** `gh repo edit qf-studio/pilot --homepage https://docs.pilotcloud.dev`; `gh repo edit qf-studio/pilot-docs --description "Pilot documentation site (docs.pilotcloud.dev) — synced from qf-studio/pilot docs/"`.
- **P2** founder: Search Console property `docs.pilotcloud.dev`, submit `https://docs.pilotcloud.dev/sitemap.xml`; after the 301, Change of Address from the `pilot.quantflow.studio` URL-prefix property.

## Verify

```bash
# phase A gate (before the 301)
curl -s https://docs.pilotcloud.dev/sitemap.xml | grep -oE 'https://[a-z.]+/' | sort | uniq -c   # only docs.pilotcloud.dev
curl -s https://docs.pilotcloud.dev/ | grep -o '<meta property="og:url"[^>]*>'
# phase C (after Mon 10:00 CET)
curl -sI 'https://pilot.quantflow.studio/cli/commands/?x=1' | grep -iE '^(HTTP|location)'
curl -sI https://docs.pilotcloud.dev/ | grep -iE '^(HTTP|x-cache|strict-transport)'
```

## Done

- [x] M2 merged 11-01 (site#28). · [x] M1 pilot#6004 merged (c6e792bf8), docs deployed via prod-2.307.0-20261101185551 at 18:58Z; **phase A gate GREEN 19:02Z** (68/68 sitemap locs, og:url, og:image, robots all on docs.pilotcloud.dev; re-checked from this session).
- [ ] M3 merged + tagged; M4 merged (release may follow); M5 merged.
- [ ] P1 done; P2 property created + sitemap submitted; Slack reply posted.
- [ ] 301 live and verified; Change of Address filed.
- [ ] Navigator docs updated (deploy pipeline reference, docs-and-history); TASK-492 step 4 closed; task archived. 301 stays ≥ 12 months.

## Legs

### M1 — qf-studio/pilot — NOT DISPATCHED: owned by the PILOT WEBSITE session, branch `docs/base-url-docs-pilotcloud-dev` (spec kept below as the review checklist)
probe: present qf-studio/pilot metadataBase: new URL\('https://pilot\.quantflow\.studio'\)
probe: present qf-studio/pilot const BASE = 'https://pilot\.quantflow\.studio'

## Context

The Pilot docs now answer on two hosts with identical content: the old `https://pilot.quantflow.studio` and the new `https://docs.pilotcloud.dev` (edge cert, ALB rule and DNS shipped 2026-11-01). A 301 from the old host to the new one, path and query preserved, goes live Mon 2026-11-02 10:00 CET. Before that, every URL the site publishes about itself must already name the new host, so crawlers meet a matching canonical, not a redirect loop of stale metadata. Today `og:url` and all 67 `sitemap.xml` entries on the new host still say `pilot.quantflow.studio`.

The docs reach production through `.github/workflows/sync-docs.yml`: a merge to main touching docs/ syncs the tree to qf-studio/pilot-docs, pushes a prod tag, the image builds and deploys, and the deploy invalidates the edge. No manual step.

## Implementation

Replace `https://pilot.quantflow.studio` with `https://docs.pilotcloud.dev` in these tracked files, nothing else:

- `docs/app/layout.tsx`: `metadataBase`, `openGraph.url`, the two absolute image URLs (og and twitter).
- `docs/app/robots.ts`: the `sitemap:` URL.
- `docs/app/sitemap.ts`: the `BASE` constant.
- `docs/next.config.mjs`: the comment on line 8 (name the new host; the CloudFront fact stays true).
- `README.md` docs link, `.github/ISSUE_TEMPLATE/config.yml` Documentation link, `getting-started/setup.sh` echo line.
- `.goreleaser.yaml` brew `homepage`, `deploy/helm/pilot/Chart.yaml` `home`, `deploy/helm/pilot/templates/NOTES.txt` link.

Do not touch any file under .agent (history). Do not change `docs-version-sync.yml`. Keep the HSTS and compression comments as they are.

Add one Vitest or Node test under docs that imports the sitemap module and asserts every entry starts with `https://docs.pilotcloud.dev/`, and reads `docs/app/layout.tsx` as text and asserts it does not contain `pilot.quantflow.studio`. If the docs package has no test runner today, add a plain `node --test` script instead of a new dependency.

## Acceptance

- `git grep -l pilot.quantflow.studio -- . ':!.agent'` returns nothing on the branch; paste the empty result as a line in the PR body.
- The new sitemap test passes; `bun run test` in docs, paste the output into the PR body.
- change the sitemap `BASE` constant back to the old host -> the sitemap host test fails
- Post-merge, checked by the reviewer: `curl -s https://docs.pilotcloud.dev/sitemap.xml` contains only `https://docs.pilotcloud.dev/` locs; `curl -s https://docs.pilotcloud.dev/` has `og:url` = `https://docs.pilotcloud.dev`.

### M2 — qf-studio/pilotcloud-site — NOT DISPATCHED: owned by the PILOT WEBSITE session (spec kept below as the review checklist)
probe: present qf-studio/pilotcloud-site export const docsUrl = "https://pilot\.quantflow\.studio"

## Context

The Pilot docs moved to `https://docs.pilotcloud.dev` (live since 2026-11-01, identical content; a 301 from the old host follows on 2026-11-02). The site's header and footer "Docs" links read `docsUrl` from `lib/site.ts`, which still names `https://pilot.quantflow.studio`. The deploy workflow publishes main on green CI, so the merge is the release.

## Implementation

In `lib/site.ts` change `docsUrl` to `https://docs.pilotcloud.dev`. Nothing else changes: `components/site-header.tsx`, `components/site-footer.tsx` and `lib/content/docs.ts` already read the constant. Add one assertion to `tests/pages.test.tsx` (or a new small test) that `docsUrl` starts with `https://docs.pilotcloud.dev`.

## Acceptance

- `bun run test` passes; paste the output into the PR body.
- change `docsUrl` back to the old host -> the docsUrl assertion fails
- Post-merge, checked by the reviewer: the deployed `https://pilotcloud.dev/` header Docs link points at `https://docs.pilotcloud.dev`.

## Refs

- Nelya's phase-1 report + Monday slot: #infrastructure thread `1793286994.415889` (replies 2–3, 2026-11-01)
- Research sweep: navigator-research agent 2026-11-01 (six local repos + org code search)
