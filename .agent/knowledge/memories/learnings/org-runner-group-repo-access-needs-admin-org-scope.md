---
name: org-runner-group-repo-access-needs-admin-org-scope
description: qf-studio self-hosted runner groups are org-level; the default gh token (read:org) gets 403 on their API — refresh with admin:org, then PUT orgs/qf-studio/actions/runner-groups/5/repositories/<repo_id>; every new repo deploying via Nelya's runner needs this grant on our side
type: learning
---

# Runner-group repo access: org admin + `admin:org` scope, group 5

**What happened (2026-10-19, TASK-515):** Nelya: "Runner rights are in place;
repo access for the runner group is on your side." `gh api
orgs/qf-studio/actions/runner-groups` → 403 even as org admin, because the gh
token had only `read:org`. `gh auth refresh -h github.com -s admin:org`
(device flow, must run to completion — an interrupted flow leaves the scope
off, same class as the `workflow`-scope caveat in TASK-492 1d) fixed it.

## Facts (verified 2026-10-19)
| Group | Name | Runner / label | Repos |
|---|---|---|---|
| 1 | Default | — | all |
| 3 | pointer-deployer | — | selected |
| 4 | quantflow-deployer | — | selected |
| 5 | pilot-fleet-deployer | `pilot-fleet-deployer`, labels `self-hosted,Linux,X64,aws-pilot-fleet-deployer` | pilot-console, pilot-console-ui, **pilotcloud-site** (added 10-19) |

## Procedure
```bash
gh auth refresh -h github.com -s admin:org            # once per token
gh api orgs/qf-studio/actions/runner-groups/5/repositories -q '.repositories[].full_name'
REPO_ID=$(gh api repos/qf-studio/<repo> -q .id)
gh api -X PUT orgs/qf-studio/actions/runner-groups/5/repositories/$REPO_ID
```
A deploy job on `runs-on: [self-hosted, aws-pilot-fleet-deployer]` in a repo
outside the group queues forever with no error — check the group before
debugging the workflow.

## Addendum 2026-11-06 — the group's workflow pin blocks tag-triggered deploys

Group 4 `quantflow-deployer` has `restricted_to_workflows: true` with every entry pinned `@refs/heads/main`. A workflow started by a `prod-*` tag push runs at `refs/tags/<tag>` and never matches, so the job sits `queued` with the runner online and idle (qf-website prod-7.1.10, run 42236403888, 12 min; a runner-service restart changed nothing). Fix without touching the group: `gh workflow run deploy-quantflow-aws.yml --ref main -f confirm=deploy -f ref=<tag>`. Check `gh api orgs/qf-studio/actions/runner-groups/<id>` → `selected_workflows` before debugging the runner. Repos whose deploy is `workflow_run` on main (navigator-docs, pilot-docs) are unaffected.
