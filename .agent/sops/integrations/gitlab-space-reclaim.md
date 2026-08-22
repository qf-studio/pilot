# SOP: GitLab namespace space reclaim (`quant-flow` on gitlab.com)

**Trigger**: storage-quota warnings/blocks on the `quant-flow` namespace
(gitlab.com), pushes rejected, or CI failing with storage errors. Recurring —
happened ≥2× (last known: #3380 ops backlog, 2026-07; again 2026-07-06).

**Why it recurs**: the docs deploy pipeline
(`.agent/system/references/reference_docs_deploy_pipeline.md`) pushes the full
`docs/` content **plus a unique `prod-X.Y.Z-<unix-ts>` tag to
`quant-flow/pilot-docs` on every release** — at current release cadence
(several/day via autopilot) that is hundreds of tags, repo-object growth, and
one GitLab CI `deploy-prod` pipeline (with artifacts/logs) per tag. Registry
retention was never configured (#3380, access-gated at the time).

---

## Step 0 — Access (historically the blocker)

```bash
glab auth status
# 401 / "No token found" → re-auth (interactive):
glab auth login          # or: glab auth login --token <PAT with api scope>
```

- The `gitlab:` token in `~/.pilot/config.yaml` (~line 79) has been dead since
  ≤2026-07-06 and is NOT a fallback. (Backlog says delete it.)
- PAT needs `api` scope; owner access on `quant-flow` for storage APIs.

## Step 1 — Diagnose: what is eating the quota

```bash
# Namespace-level usage
glab api "namespaces?search=quant-flow" | python3 -m json.tool | grep -E '"id"|"name"|storage'

# Per-project breakdown (statistics=true needs owner)
glab api "groups/<group-id>/projects?statistics=true&per_page=100" \
  | python3 -c "
import sys, json
for p in json.load(sys.stdin):
    s = p.get('statistics', {})
    print(f\"{p['path_with_namespace']:40} repo={s.get('repository_size',0)/1e6:8.1f}MB \"
          f\"artifacts={s.get('job_artifacts_size',0)/1e6:8.1f}MB \"
          f\"registry={s.get('container_registry_size',0)/1e6:8.1f}MB \"
          f\"lfs={s.get('lfs_objects_size',0)/1e6:8.1f}MB\")
"
```

Usual suspects, in expected order: `pilot-docs` repository size (tag/object
accumulation), job artifacts (deploy pipelines), container registry (if the
deploy builds images into GitLab registry).

## Step 2 — Reclaim (by category)

### 2a. Stale `prod-*` tags on `pilot-docs`

Keep the newest N (e.g. 10); delete the rest. Tags pin objects — deleting
them lets housekeeping GC reclaim.

```bash
PROJ="quant-flow%2Fpilot-docs"
glab api "projects/$PROJ/repository/tags?per_page=100&order_by=updated" \
  | python3 -c "import sys,json; [print(t['name']) for t in json.load(sys.stdin)]" \
  | grep '^prod-' | tail -n +11 \
  | while read t; do glab api -X DELETE "projects/$PROJ/repository/tags/$t"; sleep 0.3; done
```

⚠️ Do NOT delete the newest `prod-*` tag — it is what the current deploy is
pinned to. ⚠️ Deleting a tag does not undeploy anything (deploy already ran).

### 2b. Housekeeping / GC after tag deletion

```bash
glab api -X POST "projects/$PROJ/housekeeping"
# repository_size in statistics updates asynchronously (minutes–hours)
```

### 2c. Job artifacts

```bash
# Bulk-delete all artifacts of a project (keeps latest per ref)
glab api -X DELETE "projects/$PROJ/artifacts"
```

### 2d. Container registry (if registry size > 0)

```bash
glab api "projects/$PROJ/registry/repositories?tags_count=true"
# Bulk-delete old tags per repository (keeps 5 newest, older than 7d):
glab api -X DELETE "projects/$PROJ/registry/repositories/<repo-id>/tags" \
  -f name_regex_delete='.*' -f keep_n=5 -f older_than=7d
```

## Step 3 — Prevention (do these once, then this SOP mostly retires)

1. **Artifacts expiry**: in `pilot-docs`'s `.gitlab-ci.yml`, set
   `artifacts: expire_in: 3 days` on the deploy jobs (default keeps forever
   unless instance default applies).
2. **Registry cleanup policy**: Project → Settings → Packages & registries →
   Clean up image tags (keep 5, remove older than 7d) — or via API:
   `PUT /projects/:id` with `container_expiration_policy_attributes`.
3. **Tag pruning in the pipeline**: extend `sync-docs.yml` (GitHub side) to
   delete `prod-*` tags older than the newest ~10 after pushing the new one —
   turns 2a from manual into automatic. (Filed 2026-08-22 as
   [#5132](https://github.com/qf-studio/pilot/issues/5132), together with
   post-deploy `docker image prune` and a registry cleanup job — the deploy
   also strands one uniquely-tagged image per release on BOTH the registry
   and the prod-server disk; see the issue for the full mechanism.)
4. Delete the dead `gitlab:` token block from `~/.pilot/config.yaml` (~line
   79) — it masks the real auth state.

## Verify

- `glab api "namespaces/<id>"` → storage below quota.
- Docs deploy still works: next release's `prod-*` tag triggers `deploy-prod`
  green, site version bumps (see reference_docs_deploy_pipeline.md § recovery
  if not).

## History

- 2026-08-22: recurrence #3 (failing pipelines, image bloat suspected by
  founder — confirmed from in-tree `docs/.gitlab-ci.yml`: unique image tag
  per deploy, `--no-cache`, no prune anywhere). glab 401 blocked diagnosis
  AGAIN (third time). Prevention finally filed: #5132.
- 2026-07-06: SOP created during recurrence #2; glab 401 blocked diagnosis —
  Step 0 written first for a reason.
- #3380: original ops backlog entry (registry retention, access-gated).
