# TASK-494: Docs sync post-AWS-cutover — GitHub-only leg, pilot-docs owns its workflows, GitLab files removed, /getting-started 404

**Status**: 🚧 IN PROGRESS — [#5315](https://github.com/qf-studio/pilot/issues/5315) decomposed by Pilot into #5316 (sync rewrite → **PR#5320 MERGED 12:0x**, first run 12:06 green: pilot-docs `.github/` preserved, tag `prod-2.272.1-20260906120642` built + **Nelya's deploy ran green 12:10**) · #5317 (deletions → PR#5321 **closed on CI**: stale `ci.yml` step validates `docs/.gitlab-ci.yml` by name → autopilot fix **#5322**, body rewritten with root cause: remove the step + `scripts/check-gitlab-ci-yaml.py` + its test) · #5318 (getting-started index → PR#5323 in CI) · #5319 (verification, waits). **Side effect of the split:** pilot-docs currently has `.gitlab-ci.yml` + `docker-compose.prod.yml` restored by the 12:06 sync; the next sync after #5322 merges removes them. Pitfall recorded: `pilot-decomposes-parent-issues-one-pr-instructions-not-honored`. **14:30Z:** #5322/#5325 were silently undispatchable — `Depends on: #5317` with #5317 left open as pilot-superseded (poller skips open deps at debug). Closed #5317/#5319 (superseded) + #5325 (duplicate) by hand → #5322 is the only live fix; pitfall `autopilot-fix-issue-depends-on-open-original-deadlock`; durable fix filed as [#5336](https://github.com/qf-studio/pilot/issues/5336) (pilot-labeled). #5330 (TASK-493 no-op verify child) closed to stop re-dispatch loop. `/getting-started` → **200 live** (deploy 12:44).
**Created**: 2026-09-06
**Assignee**: Pilot

---

## Context

**Problem**:
`pilot.quantflow.studio` is served from AWS since 2026-09-05 (Nelya's handover, 09-06: ECS behind an ALB, image mirrored GHCR → ECR by a `workflow_run` job in `qf-studio/pilot-docs` that fires when our "Build image" workflow succeeds on a `prod-*` tag). GitLab and the VPS no longer receive deploys. Our sync workflow `.github/workflows/sync-docs.yml` still assumes the transition state and is now actively dangerous:

1. **It wipes the target tree.** Both legs do `find "$WORK" -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +` and restore only `docker/` from a backup. `qf-studio/pilot-docs` now carries a workflow Nelya authored (file deploy-quantflow-aws.yml under its workflows dir, commits d231634…053d14d) that does not exist in our `docs/` tree. The next sync run **deletes it**, and the AWS deploy silently stops. The sync fires on any push to `main` touching `docs/**`.
2. **It restores files Nelya removed.** She deleted the GitLab pipeline from pilot-docs (commit cea8b9b); our `docs/.gitlab-ci.yml` and `docs/docker-compose.prod.yml` would put them back.
3. **The GitHub tag depends on the GitLab leg.** `DEPLOY_TAG` is computed inside the "Sync docs/ to GitLab" step and passed as an output; if that leg fails (runner gone, repo archived) the GitHub leg pushes content but no tag, so no image build and no deploy.
4. **Homepage link 404.** `docs/content/index.mdx` line 53 links to `/getting-started/prerequisites` (200), but the sidebar folder entry `/getting-started` has no index page and returns 404. Nelya flagged it; it was 404 on the VPS too.

**Goal**:
Sync becomes GitHub-only, content-only: pilot owns `docs/` content, `qf-studio/pilot-docs` owns everything under its `.github/` and `docker/`. Nothing Nelya committed is deleted or overwritten. Tag derivation lives in the GitHub leg. GitLab artifacts leave `docs/`. `/getting-started` resolves.

---

## Known Pitfalls & Patterns

- **PITFALL** (memory `sync-wipes-target-tree`, TASK-492): the sync's whole-tree wipe is the mechanism that nests/removes target files. Phase 1 changes the preserve list rather than the wipe idiom, so every other behaviour (stale-tag prune, no-op commit skip) stays identical.
- **LEARNING** (`qf-studio-org-deploy-keys-disabled-use-pat-or-app`): pushes to pilot-docs go through `PILOT_DOCS_PAT` (founder's gh OAuth token, **no `workflow` scope**). A push that creates or modifies any file under the target's `.github/workflows/` is rejected. Phase 1 must therefore make the sync never write to `.github/` at all — preserving the target's copy sidesteps the scope problem permanently.
- **PITFALL** (TASK-492 gotcha): `secrets.*` is illegal in step-level `if:`; keep the `env: HAS_PAT` pattern.
- **PITFALL** (TASK-492 gotcha): the sync's `cp -a "$BACKUP/docker" "$WORK/docker"` nested copies in the past (Nelya cleaned five nested `docker/docker/…` copies on 09-05, commit 01878a7). Preserve-and-restore must `rm -rf` the destination before `cp -a`, as the current code does; apply the same to `.github/`.
- **PITFALL** (SOP Rule 3b): the workflow edit and the content deletions ship in ONE PR so the first post-merge sync already runs the new definition (push-event workflows execute from the pushed ref).

---

## Acceptance Criteria

- [ ] `.github/workflows/sync-docs.yml` has no GitLab step, no SSH setup, no `GITLAB_SSH_KEY` reference; name updated to reflect GitHub-only.
- [ ] The GitHub leg preserves the target's `.github/` **and** `docker/` across the wipe (backup → wipe → copy → restore), and never copies a `.github/` from `docs/` into the target.
- [ ] `DEPLOY_TAG` is computed inside the GitHub leg from `git describe --tags --abbrev=0` on the pilot checkout, same `prod-${VERSION}-${BUILD_NUM}` format, same prune-to-last-10 behaviour.
- [ ] `docs/.gitlab-ci.yml`, `docs/docker-compose.prod.yml`, and `docs/.github/workflows/build.yml` (with its now-empty parent dirs) are deleted from this repo. `docs/.dockerignore` unchanged (its `.github` and `.gitlab-ci.yml` lines are harmless).
- [ ] After the first post-merge sync run: `gh api repos/qf-studio/pilot-docs/contents/.github/workflows` still lists both `build.yml` and `deploy-quantflow-aws.yml`; `.gitlab-ci.yml` and `docker-compose.prod.yml` are absent from pilot-docs `main`; a new `prod-*` tag exists on pilot-docs; "Build image" ran green for it; "Deploy QuantFlow AWS" ran.
- [ ] `https://pilot.quantflow.studio/getting-started` returns 200 (index page or Nextra redirect to prerequisites) after deploy.
- [ ] Workflow header comment rewritten: describes the GitHub-only contract and the ownership split (pilot = content; pilot-docs = `.github/` + `docker/`).

---

## Implementation

### Phase 1: sync-docs.yml → GitHub-only, target-owned CI
**Goal**: One leg, content-only, no writes under the target's `.github/`.

**Tasks**:
- [ ] Delete steps "Setup SSH for GitLab" and "Sync docs/ to GitLab".
- [ ] In "Sync docs/ to GitHub": extend the backup to `docker/` and `.github/`; after `cp -a docs/. "$WORK"/` run `rm -rf "$WORK/.github"` before restoring the backup so nothing from `docs/` lands there; restore both dirs with `rm -rf` + `cp -a` (existing idiom).
- [ ] Move the `VERSION`/`BUILD_NUM`/`DEPLOY_TAG` block into the GitHub leg (it reads the pilot checkout via `git -C "$GITHUB_WORKSPACE"`, unchanged). Drop the `DEPLOY_TAG` env input and the `steps.gitlab_sync` reference.
- [ ] "Check GitHub PAT" step: drop `if: always()` (no prior step can fail now); keep the `env: HAS_PAT` pattern.
- [ ] Rename workflow to "Sync Docs (GitHub)" and rewrite the header comment.

**Files**:
- `.github/workflows/sync-docs.yml`

### Phase 2: remove GitLab-era files from docs/
**Tasks**:
- [ ] `git rm docs/.gitlab-ci.yml docs/docker-compose.prod.yml docs/.github/workflows/build.yml` (the pilot-docs copy is byte-identical and is now owned there).
- [ ] Grep `docs/` for remaining references to `docker-compose.prod`, `gitlab-ci`, or `quant-flow/pilot-docs` and update or remove them (docs prose only; do not touch `docs/docker/`).

**Files**:
- `docs/.gitlab-ci.yml` (delete)
- `docs/docker-compose.prod.yml` (delete)
- `docs/.github/workflows/build.yml` (delete)

### Phase 3: /getting-started index
**Tasks**:
- [ ] Add an index page for the getting-started folder (Nextra v4 convention: an index MDX file inside the folder directory, alongside prerequisites/quickstart/installation/configuration) that briefly introduces the section and links the four pages in order. Alternative if the project already uses `_meta.js` redirects elsewhere: a folder-level redirect to prerequisites — pick whichever pattern `docs/content` already uses.
- [ ] Add the new route to `docs/app/sitemap.ts` if that file enumerates routes explicitly.

**Files**:
- new index page under the getting-started content folder (see above)
- `docs/app/sitemap.ts`

### Phase 4: verification run
**Tasks**:
- [ ] After merge, wait for the "Sync Docs (GitHub)" run triggered by the merge; confirm it succeeded and pushed a `prod-*` tag.
- [ ] Confirm on pilot-docs: both workflow files present, GitLab files absent, "Build image" and "Deploy QuantFlow AWS" green for the new tag.
- [ ] `curl -sS -o /dev/null -w '%{http_code}' https://pilot.quantflow.studio/getting-started` → 200.

---

## Out of Scope

- Stopping the sync entirely and editing pilot-docs directly (Nelya's preference). Deferred: docs source stays in `docs/` because `docs-version-sync.yml` bumps the docs version on every pilot release. Revisit as its own task once the AWS chain has run a few releases.
- Compression / Content-Security-Policy on the AWS ALB path (Traefik used to gzip + send CSP-RO; the AWS response today carries only HSTS, nosniff, X-Frame-Options). That is infra-side — raised with Nelya on Slack, not this repo.
- Deleting `GITLAB_SSH_KEY` secret and the dead `gitlab:` block in the hosted daemon config, archiving `quant-flow/pilot-docs` — TASK-492 step 4 (operator).
- qf-website headers (`poweredByHeader`, Referrer-Policy, Permissions-Policy, deploymentId) — separate issue in `qf-studio/qf-website`.
- auth-service per-app audience / TLS_ENABLED no-op — later, separate repo.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Who owns pilot-docs `.github/` | pilot `docs/.github/` sourced by sync · pilot-docs owns it | pilot-docs owns it | Nelya's deploy workflow lives only there; preserving the dir means the sync never needs `workflow` scope and can never delete her work |
| Tag derivation | keep in a separate step with output · inline in GitHub leg | inline | Only one leg remains; no cross-step output to break |
| Sync model | keep sync GitHub-only · stop sync | keep sync | Release version bump chain (`docs-version-sync.yml`) writes into `docs/`; moving the source is a bigger migration |
| /getting-started | index page · redirect | follow existing `docs/content` pattern | Consistency over preference |

---

## Verify

```bash
# workflow is valid YAML and has one sync step
python3 -c "import yaml,sys; d=yaml.safe_load(open('.github/workflows/sync-docs.yml')); print([s['name'] for s in d['jobs']['sync']['steps']])"
grep -c -i gitlab .github/workflows/sync-docs.yml   # expect 0 (header comment may mention the retired GitLab path once — keep it to at most the comment)
test ! -e docs/.gitlab-ci.yml && test ! -e docs/docker-compose.prod.yml && test ! -e docs/.github && echo "gitlab-era files gone"
cd docs && npm run build   # Nextra build must pass with the new index page
```

---

## Done

- [ ] `sync-docs.yml`: single GitHub leg, preserves target `.github/` + `docker/`, computes its own tag
- [ ] Three GitLab-era files removed from `docs/`
- [ ] First post-merge sync green; pilot-docs still has `deploy-quantflow-aws.yml` and `build.yml`; new `prod-*` tag built and deployed
- [ ] `/getting-started` → 200 on the live site

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5315
- qf-website sibling: https://github.com/qf-studio/qf-website/issues/18 (headers + deploymentId + Vercel SOP superseded)
- Nelya's handover, 2026-09-06, Slack `#infrastructure` C0BV37L87C1 msg `1788692051.363079` (artifact "QuantFlow AWS Handover")
- TASK-492 (Navigator task doc, same tasks folder) — parent track; steps 2–3 done by Nelya 09-05
- pilot-docs commits cea8b9b (GitLab files removed), d231634 → 053d14d (deploy workflow), 01878a7 (nested docker cleanup)
- Prior sync PR: [#5312](https://github.com/qf-studio/pilot/issues/5312) → PR#5313 (dual-push, now superseded)

---

**Last Updated**: 2026-09-06
