# TASK-492: pilot-docs migration — GitLab → GitHub `qf-studio/pilot-docs` + AWS deploy (Nelya)

✅ **STEP 1 COMPLETE + VERIFIED E2E 2026-09-05** — repo mirrored · GHCR build green · dual-push sync shipped ([#5312](https://github.com/qf-studio/pilot/issues/5312) → PR#5313, post-merge review APPROVE-w-notes) · `PILOT_DOCS_PAT` reset 09-05 · dispatch run 33962990711 pushed content + `prod-2.272.1-20260905111856` to BOTH remotes; GitHub tag build green, GHCR tags `latest`/`main`/`prod-…`/`sha-b6a7c1c`. **Step 2 blocked on Nelya's answers** (Slack `#infrastructure` C0BV37L87C1, msg `1788529505.580119`, no reply as of 09-05). **Caveat: the secret currently holds the founder's gh CLI OAuth token WITHOUT `workflow` scope** — fine while `docs/.github/workflows/build.yml` is unchanged; the first edit to it (Step 2 deploy job) will be rejected until `gh auth refresh -s workflow` completes and the secret is re-piped (see step 1d).

## Why

Founder directive 2026-09-04: consolidate on GitHub `qf-studio` and move all hosting to AWS under Nelya (infra owner). GitLab `quant-flow/pilot-docs` was only ever a deploy vehicle (self-hosted `devops` runner + docker compose + Traefik on the legacy host) and a recurring storage-quota headache (`sops/integrations/gitlab-space-reclaim.md`).

## Current chain (authoritative until cutover)

`pilot/docs/` → `.github/workflows/sync-docs.yml` (push to `main` touching `docs/**`, or `workflow_dispatch`) → clone GitLab over SSH (`GITLAB_SSH_KEY` = `~/.ssh/pilot-docs-sync`), **replace the whole tree except `.git` and `docker/`**, commit, push `main`, push unique `prod-X.Y.Z-<ts>` tag, prune to newest 10 → GitLab CI (`docs/.gitlab-ci.yml`, lives in `pilot/docs/`, runner tag `devops`) builds image to the GitLab registry and `docker compose -f docker-compose.prod.yml up` on the prod host → `pilot.quantflow.studio`. Full detail + fragile points: `system/references/reference_docs_deploy_pipeline.md`.

Facts verified 2026-09-04: GitLab project id 78286253, 515 commits, `main` only, 11 `prod-*` tags, registry 408 MB, last deploy `prod-2.272.0-20260903144149` succeeded. The `devops` runner is NOT visible in project/group runner APIs — location unknown to us; asked Nelya.

## Target state

- Source of truth stays `pilot/docs/`. `qf-studio/pilot-docs` is the deploy-unit repo (tree = `docs/` + `docker/`).
- Image: `ghcr.io/qf-studio/pilot-docs` (or ECR — Nelya's call), built by `.github/workflows/build.yml` **whose source lives at `docs/.github/workflows/build.yml` in this repo** (the sync wipes anything not sourced from `docs/` — pitfall `docs-sync-wipes-target-tree-workflow-must-originate-in-docs`).
- Deploy: AWS, shape decided by Nelya (container on instance / ECS), trigger = `prod-*` tag on GitHub.
- GitLab project, runner, registry retired after cutover is verified.

## Steps

| # | Step | Status |
|---|------|--------|
| 1a | Create `qf-studio/pilot-docs` (private), mirror history. HEAD `ba193a0` = GitLab HEAD. | ✅ 09-04 |
| 1b | Seed `.github/workflows/build.yml` → GHCR on `main` + `prod-*`. Run 33879726176 green; tags `main`, `sha-8233e2c`. | ✅ 09-04 (commit `8233e2c`) |
| 1c | Dual-push sync in this repo: GitLab leg unchanged + GitHub leg via `PILOT_DOCS_PAT`, `docs/.github/workflows/build.yml` sourced from `docs/`, `.github` in `docs/.dockerignore`. | 🚀 dispatched [#5312](https://github.com/qf-studio/pilot/issues/5312) |
| 1d | **Operator**: `PILOT_DOCS_PAT` reset. Found 09-04: the fine-grained token `pilot-docs-plot` had **EXPIRED** (release docs chain was already broken). Three failed re-sets before success — `!`-prefixed `gh secret set` with no stdin wrote an EMPTY secret; `pbpaste \|` carried a newline (curl "Malformed input"); a wrong clipboard payload gave curl "Port number was not a decimal number". Working path (09-05): `gh auth token \| gh secret set PILOT_DOCS_PAT --repo qf-studio/pilot` — no clipboard. **Residual: that token lacks `workflow` scope** (device flow was interrupted) → run `gh auth refresh -h github.com -s workflow` to completion, re-pipe, before Step 2 touches the workflow file. Deploy keys are **disabled by org policy** — learning `qf-studio-org-deploy-keys-disabled-use-pat-or-app`. Durable option (not filed): GitHub App + `gh secret set … < key.pem`. | ✅ 09-05 (scope caveat) |
| 1e | PR#5313 reviewed (APPROVE-w-notes: GitHub tag derives from the GitLab step output; `always()` → prefer `!cancelled()`). Dispatch 33962990711: both remotes at the same tree, same `prod-*` tag, GHCR tag build green, GitLab deploy unaffected (5 prod tags pushed during the token saga, every one deployed). | ✅ 09-05 |
| 2 | Deploy leg on AWS — needs Nelya's answers: hosting shape (Next standalone container, port 3000) · registry GHCR vs ECR · trigger (OIDC role for Actions vs pull-side) · where the `devops`/Traefik host lives + decommission date. Then add the deploy job to `docs/.github/workflows/build.yml` (or an infra PR in `pilot-cloud-infra`, Nelya's call). Traefik-equivalent must keep the security headers + compress middleware from `docs/docker-compose.prod.yml`. | ⏳ Nelya |
| 3 | Cutover: DNS `pilot.quantflow.studio` → AWS target · verify headers/version on the live site · sync becomes GitHub-only (drop GitLab leg + `GITLAB_SSH_KEY`) · delete `docs/.gitlab-ci.yml` + `docs/docker-compose.prod.yml` if unused. | 📋 |
| 4 | Retire GitLab: archive/delete `quant-flow/pilot-docs`, remove `devops` runner, drop the dead `gitlab:` block in `~/.pilot/config.yaml` (~line 76), archive `sops/integrations/gitlab-space-reclaim.md`, rewrite `reference_docs_deploy_pipeline.md` steps 4–5 for the AWS chain, update `system/docs-and-history.md`. | 📋 |

## Verify (cutover acceptance)

```bash
# both remotes on the same tree
git ls-remote git@gitlab.com:quant-flow/pilot-docs.git main
git ls-remote git@github.com:qf-studio/pilot-docs.git main
# image built for the last prod tag
gh api /orgs/qf-studio/packages/container/pilot-docs/versions --jq '.[0].metadata.container.tags'
# live site headers unchanged after cutover (HSTS, nosniff, CSP-RO, gzip)
curl -sS -D - -o /dev/null -H 'Accept-Encoding: gzip,br' https://pilot.quantflow.studio/
```

## Gotchas for future sessions

- Never `git tag prod-*` on GitHub `qf-studio/pilot` — the tag contract belongs to `pilot-docs` (GitLab today, GitHub after 1c).
- `secrets.*` is illegal in step-level `if:`; use the `env: HAS_PAT` pattern (reference doc).
- The GitHub build workflow reads `docker/Dockerfile` — `docker/` is the one dir the sync preserves from the target, so Dockerfile edits go to **both** `pilot/docs/docker/` and the target (the sync restores the target's copy over ours).
- Issue bodies for this track: backtick only paths that exist on `pilot` main (base-presence gate, SOP Rule 3b) — `docs/.github/...` did not exist when #5312 was filed.
- Setting `PILOT_DOCS_PAT`: never via `!`-prefixed `gh secret set` (empty stdin ⇒ empty secret) and never via bare `pbpaste` (newline). Pipe from a command (`gh auth token`) or a file (`< key.pem`). Diagnose from the sync run: `skipped` = empty secret · auth failure = expired/wrong token · "Malformed input"/"Port number" = whitespace or wrong payload · "refusing to allow … workflow" = missing `workflow` scope.
- A push that leaves `.github/workflows/*` byte-identical does NOT need `workflow` scope — that is why 1e passed; do not read it as proof the scope is present.

## Refs

- Slack `#infrastructure` C0BV37L87C1 (Nelya + Aleks; created 09-04 for the AWS consolidation — also lists `quantflow.studio` → `qf-studio/qf-website`). DM copy: D0ADQMEBR6Y `1788529375.623299`.
- GitHub: https://github.com/qf-studio/pilot-docs · GitLab: https://gitlab.com/quant-flow/pilot-docs
- Docs commit `ffeaf0ff` (transition notes in reference/docs-and-history/SOP).
