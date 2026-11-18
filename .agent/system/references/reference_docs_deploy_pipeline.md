---
name: Docs deploy pipeline — chain self-heals via PILOT_DOCS_PAT
description: How a v* binary release deploys docs.pilotcloud.dev end to end (pilot → pilot-docs → GHCR → AWS), the fragile points, and the manual backstop
type: reference
originSessionId: 401056a8-07c0-4512-a11f-12384dcaa532
---
> **Updated 2026-11-18 (TASK-629 phase D).** The docs repo is GitHub `qf-studio/pilot-docs`
> and the site runs on AWS since 2026-09-05. The public host is `docs.pilotcloud.dev`
> since 2026-11-01. The old host `pilot.quantflow.studio` answers with a 301 to the new
> host (kept for 12 months or more). GitLab is not in the chain.

`docs.pilotcloud.dev` deploys on a `prod-X.Y.Z-<ts>` tag pushed to
**`qf-studio/pilot-docs`** — not to this repo. The chain is
self-healing across binary releases.

### Full chain (post v2.100.4)

1. `v*` tag pushed → [release.yml](.github/workflows/release.yml) GoReleaser
   builds binaries + GitHub Release.
2. Same `v*` tag → [docs-version-sync.yml](.github/workflows/docs-version-sync.yml)
   bumps `docs/app/layout.tsx` header + `.agent/*.md` versions, opens PR
   using `secrets.PILOT_DOCS_PAT` (`||` fallback to `GITHUB_TOKEN`),
   auto-merges the PR with the same token.
3. The auto-merge commits with PAT identity, which **does** trigger
   downstream `push`-event workflows (unlike `GITHUB_TOKEN`, which
   doesn't).
4. [sync-docs.yml](.github/workflows/sync-docs.yml) fires on the
   docs/-touching merge → clones `qf-studio/pilot-docs` with
   `PILOT_DOCS_PAT`, replaces the content from `docs/` (the target's
   `.github/` and `docker/` stay as they are), pushes `main` and a unique
   `prod-X.Y.Z-<timestamp>` tag. It keeps the newest 10 `prod-*` tags.
5. In pilot-docs, the "Build image" workflow builds
   `ghcr.io/qf-studio/pilot-docs` on the `prod-*` tag.
6. In pilot-docs, the "Deploy QuantFlow AWS" workflow (`workflow_run`
   after "Build image", self-hosted runner `aws-quantflow-deployer`)
   mirrors the image to ECR, updates the ECS service on the `quantflow`
   cluster, waits for a stable service and invalidates `/*` on the
   CloudFront distribution that carries `PUBLIC_HOST`
   (`docs.pilotcloud.dev`). The infra contractor owns this workflow;
   changes go to her as a PR for review.

### Fragile points

- `PILOT_DOCS_PAT` is a token (Contents r/w + Pull-requests r/w on qf-studio/pilot,
  Contents r/w + Workflows r/w on qf-studio/pilot-docs). When it expires, `docs-version-sync.yml`'s
  fallback to `GITHUB_TOKEN` keeps the auto-merge working but breaks
  step 3 — sync-docs no longer chains. Symptom: docs site stuck on the
  prior version while docs.pilotcloud.dev still shows the old header.
  Calendar reminder ~11 months out from creation.
- The PAT-missing fallback emits `::warning::PILOT_DOCS_PAT not set —
  …sync-docs.yml will not chain` in the workflow log. Watch for it on
  release runs.
- **`secrets.*` is illegal in step-level `if:` conditions** (parse-time
  rejection: `Unrecognized named-value: 'secrets'`). This silently broke
  the workflow for v2.100.5 → v2.102.1 (commit `79f25717` introduced
  `if: ${{ !secrets.PILOT_DOCS_PAT }}`). Tag pushes produced no run
  records; main pushes produced empty zero-job failed runs. Fixed in
  `9e22014f` by moving the check into `env:` (legal) +
  shell `if`. Pattern for future PAT-presence checks:
  ```yaml
  env:
    HAS_PAT: ${{ secrets.PILOT_DOCS_PAT != '' }}
  run: |
    if [ "$HAS_PAT" != "true" ]; then echo "::warning::..."; fi
  ```

### Manual backstop (`workflow_dispatch` exists since GH-2423)

If sync-docs.yml didn't fire after a release:

```bash
gh workflow run sync-docs.yml --repo qf-studio/pilot
```

That re-pushes `docs/` + a fresh `prod-X.Y.Z-<ts>` tag to pilot-docs using
the latest `v*` tag found via `git describe`. Idempotent; safe to
re-run.

### Anti-patterns (do not do)

- Do not push a `prod-X.Y.Z` tag to this repo (`qf-studio/pilot`).
  The deploy listens on `prod-*` tags of `qf-studio/pilot-docs` — a
  prod tag here does nothing.
- Do not edit the deploy workflow of pilot-docs by hand. Open a PR and
  ask the infra contractor for review.
- Do not `git push --force` on `docs/version-sync-v*` branches; the
  workflow recreates them per release.

### Org-level prerequisite (one-time)

`Settings → Actions → General → "Allow GitHub Actions to create and
approve pull requests"` must be enabled at **org** level (`qf-studio`),
not just repo level. Without it, repo-level checkbox shows greyed-out
disabled and PR creation 403s. Enabled 2026-04-27.

### Cleanup history

- GH-2426 (commit `79f25717`, 2026-04-27): replaced `Resolve token` step
  that wrote secret to `$GITHUB_OUTPUT` with native `||` fallback at use
  sites. Same commit accidentally introduced the `secrets`-in-`if:` bug
  above; fixed in `9e22014f` (2026-04-29).
- 2026-09-05/06 (TASK-492, TASK-494): site moved to AWS; sync is
  GitHub-only; the GitLab pipeline files are gone from both repos.
- 2026-11-01/09 (TASK-629): base URL moved to `docs.pilotcloud.dev`
  (pilot#6004); `PUBLIC_HOST` moved in pilot-docs#2; the 301 from the old
  host is live.
- Still owed (operator): delete the repo secret `GITLAB_SSH_KEY` (no
  workflow reads it) and archive the GitLab project `quant-flow/pilot-docs`.
