# TASK-510: Linear Invoices — move the Pilot integration from the GitHub mirrors to the canonical GitLab repos

**Status**: ✅ LIVE 2026-10-02 08:46Z — **v2.277.1 cut by hand (tag at `7e4d5f167`, GoReleaser 08:43Z)** and running on the box; startup proof: `Registered project MR creator` for `linearinvoices-service` and `linearinvoices-client` (gitlab.com, `project:<path>` keys). Fine-grained PAT (group `linear-invoices`, expires 2026-11-29) in `GITLAB_TOKEN` + git credential store; both repos cloned from GitLab (GitHub clones archived `*.gh-archive-20261002-*`); `adapters.linear.enabled: true` with `team_id: QF` (team key — UUID is broken in sdk v0.38.2, see § Linear poller). **Linear polling confirmed enabled 08:50:30Z, no errors.** Both GitLab `main` branches locked to MR-only (push: No one, merge: Maintainers) — verified with the token. Founder scope 10-02: **configure only; no tickets labelled from this side.** Code legs merged: #5583 → PR#5585, #5584 → PR#5586 (post-merge reviews owed). GitHub mirrors frozen.
**Created**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes · **Supersedes the forge half of**: TASK-509 (its Linear routing + quality-gate findings still apply)

## Why
TASK-509 onboarded `linearinvoices-service` / `linearinvoices-client` from GitHub mirrors (`alekspetrov/*`). The canonical repos, CI and production deploy live on GitLab (`gitlab.com/linear-invoices/*`); the mirrors had diverged from GitLab before Pilot touched them (service: 10 GitLab-only commits vs 3 Pilot commits on GitHub; client: 10 vs 4). Founder decision 2026-10-01 evening: GitHub stays as-is, untouched; Pilot moves to GitLab.

## Facts that shape the plan (verified 2026-10-01)
- GitLab projects: service id `75576419`, client id `75576437`, group `linear-invoices` (id `117860933`, **Free plan**, owners `alekspetrov1` + `nelya4dev`). `main` protected (Maintainers push/merge), merge method `merge`, no MR pipeline requirement, no labels, no issues (tasks come from Linear only).
- **GitLab CI deploys `main` straight to prod** (`.gitlab-ci.yml`: build+deploy on `main`, `merge_request_event: never`). There are no MR pipelines. Therefore **merging an MR = production deploy**; Pilot must never auto-merge here. Autopilot has no GitLab support anyway (`internal/autopilot` is `*github.Client`-typed), so merges stay manual in the GitLab UI — this is the desired state, not a gap.
- Open on GitLab: service MR !1 `FIX-01: Go test build green + redact rotated key` (branch `fix/FIX-01-go-test-build-green`, by the founder's other coding agent). Client GitLab `main` has no Makefile — irrelevant now that #5582 (per-project `quality:` honoured in worktrees) is on main.
- Pilot gaps found by nav-research (full map in the #5583 body): `ProjectConfig` has no `gitlab:` block; PR creator is chosen by `SourceAdapter`, not by project (Linear→GitLab falls to `gh pr create`); PR-number extraction switches on adapter; Linear poller registers every PR into the **default** autopilot controller (`poller_linear.go`) — a live bug for any multi-project Linear routing, GitHub or not.
- Config loader: unknown YAML keys are silently dropped (plain `yaml.Unmarshal`), so the `gitlab:` project blocks are safe on the current binary. A **sensitive key referencing an unset env var fails startup** (`config.go` env pre-scan) — the `adapters.gitlab.token: "${GITLAB_TOKEN}"` line goes in only together with the export.
- Box state now: both projects' config entries carry `gitlab: {project: linear-invoices/…}` instead of `github:`; `ci_checks` removed (GitHub-only); `linear.project_id` + client `quality:` kept; `pilot config validate` OK. Clones still point at GitHub until the credential exists (re-clone script below is idempotent). No Pilot work in flight on either project; no `pilot`-labelled GitHub issues remain.

## Credential (founder step — the only blocker)
GitLab Free refuses **group** and **project** access tokens for this group (`400 User does not have permission to create … access token`, verified via API). The laptop `glab` login is an OAuth session token (not reusable on the box). A **personal access token** is the only option:
- https://gitlab.com/-/user_settings/personal_access_tokens → name `pilot-founder-box`, scopes `api` + `write_repository`, expiry ≤ 90 days.
- Delivery: either an interactive SSM session (`aws ssm start-session … → sudo su - ec2-user`) and run the flip script yourself, or hand it to the Navigator session (accepted-exposure path, same as the Linear key on 10-01; rotate on the expiry date).
- Posture: pushes/MRs appear as the founder's GitLab user (same as GitHub today via `gh auth token`). Revoke = one click.

## Flip script (box, as ec2-user; needs `TOK`) — idempotent, re-runnable
```bash
S=/home/ec2-user/start-pilot.sh
grep -q '^export GITLAB_TOKEN=' $S && sed -i "s|^export GITLAB_TOKEN=.*|export GITLAB_TOKEN=$TOK|" $S || sed -i "/^export LINEAR_API_KEY=/a export GITLAB_TOKEN=$TOK" $S
git config --global credential.https://gitlab.com.helper store
touch ~/.git-credentials && chmod 600 ~/.git-credentials && sed -i '/gitlab\.com/d' ~/.git-credentials
echo "https://oauth2:$TOK@gitlab.com" >> ~/.git-credentials
cd /var/lib/pilot/repos/startups
for r in service client; do d=linearinvoices-$r
  if [ -d $d ] && git -C $d remote get-url origin | grep -q github.com; then git -C $d worktree prune; mv $d $d.gh-archive-$(date +%Y%m%d-%H%M%S); fi
  [ -d $d ] || git clone -q https://gitlab.com/linear-invoices/$d.git $d
  git -C $d push --dry-run origin main 2>&1 | tail -1      # proves push auth
done
# config: token line (only now — an unset env var on a sensitive key fails startup). enabled/polling stay false: issue intake is Linear, not GitLab.
python3 - <<'PY'
import re; C="/home/ec2-user/.pilot/config.yaml"; s=open(C).read()
s2=re.sub(r"(\n  gitlab:\n    enabled: false\n    token:)[^\n]*", r'\1 "${GITLAB_TOKEN}"', s, count=1); assert s2!=s; open(C,"w").write(s2)
PY
/usr/local/bin/pilot config validate
```
Then restart per `pilot-aws` (tmux C-c → wait → `tmux new-session -d -s pilot … start-pilot.sh`). Startup proof once #5583 is on the box: one INFO line per project `registered project PR creator … gitlab …`.

## Legs
1. **[#5583](https://github.com/qf-studio/pilot/issues/5583)** — per-project `gitlab:` block + startup `project:<path>` creator registration + runner `resolvePRCreator` (3 sites) + MR body without `Closes #` for non-GitLab sources + MR-iid extraction by URL. Pilot. `pilot`,`no-decompose`.
2. **[#5584](https://github.com/qf-studio/pilot/issues/5584)** — Linear `OnPRCreated` resolves the controller by the PR URL's repo via `deps.AutopilotControllers`; skips non-GitHub URLs. Pilot. `pilot`,`no-decompose`,`bug`. Independent of leg 1; blocks the Linear flip for any multi-project routing.
3. **Credential + flip** (operator, above) — after the PAT; clones + token line + restart. Can precede leg 1 (config keys are ignored by the old binary; nothing dispatches while `adapters.linear.enabled: false`).
4. **Linear flip** (TASK-509 checklist, unchanged): v2.277.1+ on the box with #5573/#5579/#5582 **and** legs 1–2 → `adapters.linear.api_key: "${LINEAR_API_KEY}"`, `enabled: true`, restart → label Linear issues `repo:linearinvoices-service|client` then `pilot`.
5. **First tickets on GitLab** — service: coordinate with MR !1 (FIX-01) so Pilot's first task branches from a green `main`; client: `quality.test` stays `required: false` until the vitest suite is green on GitLab `main` (the GitHub fix PR#5 is not on GitLab — re-derive, do not cherry-pick across the divergence).

## Review watch-list for the PRs
- #5583: resolver order is project → github-sdk → shared → gh-cli with byte-identical behaviour for tiers 2–4; registration keyed on `EvalSymlinks(projects[].path)` and looked up with `task.ProjectPath` (never the worktree path, #5582); no `Closes #` on Linear-sourced MRs; `hr.PRNumber` = MR iid; registration independent of `adapters.gitlab.enabled`; startup error names the project when the token is empty.
- #5584: default controller used **only** when the parsed repo equals the default repo; GitLab URL → nil + INFO; sibling pollers audited.

## Decided / rejected
- Rejected: syncing GitLab into GitHub or cherry-picking Pilot's GitHub commits onto GitLab — founder froze GitHub; the histories diverged before Pilot arrived.
- Rejected: SSH deploy keys — covers push only; MR creation needs an API token anyway. One PAT does both.
- Rejected: shipping the token through SSM Parameter Store — the box role `pilot-agent` has no `ssm:GetParameter`.
- Open: client GitLab `main` has 136 failing vitest cases and service tests need a DB — same gate decisions as TASK-509, now against GitLab `main`.

## Token probe 2026-10-02 (local, before install)
Passed: project read (both), clone, `pilot/*` push, branch delete, MR create/read/update/close (!2, closed). MR note → 403 (Note category not granted; Pilot does not note MRs for Linear-sourced tasks). **Incident:** the "push to protected main must fail" check SUCCEEDED — protection allows Maintainers and the PAT acts as the group Owner — leaving empty commit `2134169` on service `main` and starting pipeline #75 (`build`); cancelled in the build stage; `deploy-prod` only runs on `prod-*` tags, prod untouched. **Recommendation (founder):** set protected `main` on both repos to "Allowed to push and merge: No one" so every change goes through an MR. Flip script updated: credential proof is `ls-remote`, never a push.

## Linear poller dead on arrival → team-key workaround (2026-10-02 08:50Z)
First tick failed: `Linear poller failed … Variable "$teamId" of type "String!" used in position expecting type "ID"` — studio-sdk v0.38.2 `GetLabelByName` switches to the `team.id` filter for a UUID `TeamID` but keeps the `String!` declaration; `ListIssues`/`ListIssuesSince` filter `team.key` for any value, so a UUID never lists issues either. `Poller.Start` returns on the label-cache error and Pilot never retries → Linear intake silently dead until restart. **Workaround live:** `adapters.linear.team_id: QF` (team key; backup `config.yaml.bak-*-linear-teamkey`), wrapper restart 08:50Z → `Linear polling enabled`, no errors since. Filed: [sdk#144](https://github.com/qf-studio/studio-sdk/issues/144) (declare `ID!` on the id branch + UUID dispatch in the list queries) · [pilot#5588](https://github.com/qf-studio/pilot/issues/5588) (supervised retry + alert + `pilot_poller_up` metric). Config rule reinforced: never put a `$name` token in a YAML comment — the loader's env pre-scan flags it as an unset variable.

## Restart lesson 2026-10-02
The daemon's self-upgrade (08:44:23Z, 2.277.0 → v2.277.1) re-execs with the OLD process env, so the new `${GITLAB_TOKEN}` reference in config killed it at load ("environment variable GITLAB_TOKEN … unset") — procs=0 until the wrapper restart at 08:45:5xZ loaded `start-pilot.sh`. Rule: any new sensitive env var in config needs a wrapper restart BEFORE the next release lands, or the self-upgrade restart fails closed.

## Incident log (this session)
- 21:50Z: first cutover script ran with an empty token (shell globbing broke the mint): service clone archived, re-clone failed, loop aborted before client. Restored 21:58Z from `linearinvoices-service.gh-archive-20261001-215023`; empty `export GITLAB_TOKEN=` and empty credential entry removed. Lesson: never let a remote mutation script run without asserting the secret is non-empty **before** sending it (`[ -n "$TOK" ] || exit`).
- Slack `bot_token`/`app_token` were printed into the session transcript by a config grep whose mask missed them → **rotate** (same policy as the Anthropic key on 07-16).
