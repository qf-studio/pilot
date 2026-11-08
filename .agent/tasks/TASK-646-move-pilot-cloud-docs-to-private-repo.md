# TASK-646: Move Pilot Cloud docs out of the public `pilot` repo into `pilot-console/.agent`

**Status**: ✅ **Phases 1–4 EXECUTED 2026-11-08** (founder round: D1 move+split · D2 move all · D3 replace the name with a role · D4 Navigator session · D5 now). 180 tracked files (incl. 7 `.agent/product/` docs found late) + 51 local markers moved to `pilot-console/.agent`; 5 stubs + `tasks/archive/PILOT-CLOUD-TASKS-MOVED.md` left; README pruned (9 rows, 1 bullet); 123 graph nodes moved; 17 files name-scrubbed; denylist gained the account id, the IAM user and the contractor name (negative test fired); `check-secret-patterns.sh` green, `go build` + config/gateway tests green. **Open: Phase 0 (founder — MFA + key rotation on the operator IAM user).** This doc moves to pilot-console at archive time.
**Created**: 2026-11-08
**Assignee**: Navigator session (docs-only, two repos) + founder operator steps

---

## Context

**Problem**: `qf-studio/pilot` is PUBLIC. Its `.agent/` tree carries the Pilot Cloud control-plane
internals: the fleet design, the SaaS roadmap, ~40 console task docs, console memories, six tracked
console session markers, and the AWS project files. Scan of the tracked tree 2026-11-08:

| Item in the public tree | Count | Risk |
|---|---|---|
| EC2 instance IDs (founder box, 3 tenants, runners) | 16 | none without AWS creds; no public IPs, SSH closed |
| AWS account ID + IAM user name + profile name | 7 files | **targets credential attacks**; the IAM user has no MFA |
| SSM parameter names (`/pilot-fleet/console/*`, `/pilot-fleet/founder-box/*`) | ~20 | names only; useless without IAM |
| S3 bucket names | 2 | both have all four public-access blocks on |
| Fleet design (SG layout, SSM secrets layout, tenant lifecycle) | 5 system docs | IP exposure, not an attack surface |
| Contractor name | 59 files | privacy, founder call |
| Secret values | 0 | the denylist gate works |

Founder 2026-11-08: inbound emails already reference "pilot console" and "pilotcloud" — recon on the
public repo and domain. Decision: **option 2 — move the SaaS docs into the private `pilot-console`
repo, leave pointers, keep the daemon-side docs public.**

**Goal**: after this task the public `pilot/.agent/` describes the daemon only. Every Pilot Cloud
doc, task, memory and marker lives in `qf-studio/pilot-console/.agent/` (PRIVATE), with Navigator
initialised there so console sessions run from that cwd. The AWS account ID and IAM user name are
gone from the public working tree (history stays — see Out of Scope).

---

## Known Pitfalls & Patterns

- **DECISION** (100%, `secrets-live-in-doppler-pilot-console-project`): third-party keys live in Doppler, never in docs or issue bodies. → Phase 3 scrub confirms the moved docs still carry names only, never values.
- **LEARNING** (95%, mem-200): the operator IAM user has no `kms:Decrypt`; Claude verifies, the founder saves. → the IAM hardening in Phase 0 is founder-UI work, not a session step.
- **PITFALL** (project, 2026-09-24 incident): client material in this public repo → private denylist gate. → Phase 3 adds the account ID and IAM user name to the same denylist so they cannot return.

---

## Acceptance Criteria

- [ ] `git grep -E '<account-id>|user/<iam-user>'` on `pilot` main returns 0 files (graph.json included). The literal values live in the private denylist, not in this doc.
- [ ] `git ls-files .agent/.context-markers | grep -c 2026-10` on `pilot` main is 0 (the January/March markers are daemon history and may stay).
- [ ] `pilot-console/.agent/` exists with `DEVELOPMENT-README.md`, `system/`, `tasks/`, `tasks/archive/`, `knowledge/graph.json`, `sops/`, `.context-markers/` and `.nav-config.json`; `/nav-start` works from `pilot-console` cwd.
- [ ] Every moved file is reachable from `pilot-console/.agent/DEVELOPMENT-README.md`.
- [ ] The five `saas-*.md` paths in `pilot/.agent/system/` resolve to one-paragraph pointer stubs (code comments and 47 `.agent` cross-links keep working).
- [ ] `pilot/.agent/DEVELOPMENT-README.md` has no console/fleet task rows; one "Pilot Cloud docs" row points at the private repo.
- [ ] The private denylist read by `scripts/check-secret-patterns.sh` contains the account ID and the IAM user name; a test commit with either is refused.
- [ ] `make test` green in `pilot` (the `docs_test.go` fixture is a filename only; unchanged).
- [ ] Founder: MFA device on the operator IAM user; the 2026-04-02 access key rotated.

---

## Implementation

### Phase 0: Operator hardening (founder, AWS console) — first, independent of the move
**Goal**: the only real exposure (account ID + user name, no MFA) is closed before anything else.

**Tasks**:
- [ ] IAM → Users → operator user → Security credentials → Assign MFA device.
- [ ] Create a new access key, update the `quantflow` profile in `~/.aws/credentials`, verify `aws sts get-caller-identity`, delete the April key.
- [ ] Optional: CloudTrail / GuardDuty check for unusual activity on that user in the last 30 days.

### Phase 1: Initialise Navigator in `pilot-console`
**Goal**: a landing place with its own index and graph.

**Tasks**:
- [ ] In a `pilot-console` worktree off `origin/main`: `/nav-init` (project_management none, task_prefix `TASK`, knowledge_graph on). Task numbering stays **continuous with pilot**: `task_id_source: local` counts the files present, so the moved docs seed the counter above 646.
- [ ] `.gitignore`: add the Navigator runtime ignores from pilot **except** `.agent/.context-markers/` — the repo is private; markers are tracked there.
- [ ] `CLAUDE.md`: add the Navigator + Pilot pipeline block (copy from pilot; drop the "this repo is PUBLIC" rule; keep the client-material rule).

**Files**: `pilot-console/.agent/*`, `pilot-console/.gitignore`, `pilot-console/CLAUDE.md`, `pilot-console/.agent/.nav-config.json`

### Phase 2: Move the files (`git mv` out of pilot, `git add` into pilot-console, same session)
**Goal**: one commit per repo; content byte-identical; pointers left behind.

**Move list (tracked in pilot today):**

| Group | Files | Destination in `pilot-console/.agent/` |
|---|---|---|
| System docs | `system/saas-architecture.md`, `saas-roadmap.md`, `saas-fleet-design.md`, `saas-kanban-sync-design.md`, `saas-asset-research.md` | `system/` (same names) |
| Project files | `system/project/aws-minimal-console-deploy-request.md`, `project_aws_infra.md`, `project_pilot_cloud_positioning.md`, `project_pilot_cloud_roadmap_tickets.md`, `project_distribution_plan.md`, `project_competitive_landscape.md`, `project_github_org_plan.md` | `system/project/` — **D2 decides the last four** |
| Active tasks | TASK-405, 466, 478, 485, 495, 497, 515, 546, 583, 585, 629, 637, 639, 645; `tasks/drafts/console-*.md` | `tasks/` |
| Archived tasks | the ~35 under `tasks/archive/` matching `console\|fleet\|tenant\|infra\|ami\|s4-\|s6-\|hosted`, plus 409, 499, 511, 640 | `tasks/archive/` |
| Memories | 4 decisions + 8 learnings + 12 pitfalls from the 2026-11-08 inventory (console/fleet/tenant/ssm/ami/paddle) | `knowledge/memories/{type}s/` |
| SOPs | `sops/billing/paddle-sandbox-smoke.md`, `sops/email/console-ses-smoke.md`, `sops/operations/paddle-live-secrets-rotation-and-ssm-handoff.md`, `sops/operations/tenant-binary-upgrade-ssm.md` | `sops/` (same subdirs) |
| Tracked markers | the six `2026-10-1[5-9]_*.md` | `.context-markers/` (tracked there) |
| Local-only markers | every untracked console marker in `pilot/.agent/.context-markers/` (`console\|fleet\|tenant`) | `.context-markers/` — copy, then delete locally so the next console `/nav-start-active` finds them in the right cwd |

**Pointers left in pilot:**
- Each of the five `system/saas-*.md` becomes a 3-line stub: title; "moved 2026-11-08 to `qf-studio/pilot-console` `.agent/system/<same>`"; why (public repo).
- One `tasks/archive/PILOT-CLOUD-TASKS-MOVED.md` lists the moved IDs with the private path. No per-task stubs.
- `DEVELOPMENT-README.md`: remove the 22 console task rows and the console history bullets; add one row "Pilot Cloud docs → `qf-studio/pilot-console/.agent/`". Prune, do not append (CLAUDE.md § Documentation Maintenance).
- `internal/config/config.go:1098` and `:1133`: comment now reads "see pilot-console `.agent/system/saas-fleet-design.md` §4".
- `.agent/knowledge/graph.json` in pilot: drop the moved memory/task nodes and their edges (`task_to_graph.py --action remove` per task; memory nodes by id). Re-add them in the pilot-console graph (`--action add` over the moved files).

### Phase 3: Scrub the remaining public tree
**Goal**: nothing identifying the account stays, and it cannot come back.

**Tasks**:
- [ ] `git grep -nE '<account-id>|user/<iam-user>|Profile.*quantflow'` on the remaining tree → edit each hit to a neutral form ("the fleet account", "the operator IAM user"). Expected residue after the move: `graph.json` text, `TASK-409` if D2 keeps it.
- [ ] Add the account ID and the IAM user ARN to the private denylist read by `scripts/check-secret-patterns.sh`; prove it with a throwaway commit that the hook refuses.
- [ ] Apply D3 (contractor name) with `sed` over the remaining daemon docs if "scrub".
- [ ] `make test` + `make lint` in the pilot worktree (comment edits only in Go).

### Phase 4: Workflow switch for console sessions
**Goal**: new console knowledge lands in the private repo from now on.

**Tasks**:
- [ ] `nav-dispatch-legs.py` from `pilot-console` cwd with `--checkout qf-studio/pilot-console=.`; verify a dry run on one existing leg.
- [ ] Record in `pilot-console/.agent/DEVELOPMENT-README.md`: "console/ui/fleet/site sessions start here; the daemon repo is for the daemon".
- [ ] Learning memory in **pilot** (public-safe): "public-repo `.agent/` is for the public product only; SaaS docs live in the private control-plane repo" — the rule, not the identifiers.
- [ ] Marker in pilot-console closing the move; marker in pilot pointing at it.

---

## Out of Scope

- **Git history rewrite.** The account ID and instance IDs stay in `pilot` history. A rewrite breaks every fork, open PR and the release tags; the mitigation is Phase 0, which removes the value of the leaked identifiers.
- Making `pilot` private, or moving the docs site.
- `pilotcloud-site/.agent/` (already private, own Navigator) and `pilot-console-ui/.agent/sops` (stay).
- Scrubbing hostnames (`console.pilotcloud.dev`, `docs.pilotcloud.dev`) — public by nature.
- Any infra change on the account beyond the IAM user (SG and bucket posture verified fine 2026-11-08).

---

## Technical Decisions (one founder round)

| # | Decision | Options Considered | Recommended | Reasoning |
|---|---|---|---|---|
| D1 | Memories and graph | (a) move console memories + split the graph · (b) leave memories here, scrub identifiers only | **(a)** | the memories carry SSM paths, tenant IDs, Doppler layout; scrubbing them removes their value |
| D2 | Strategy files in `system/project/` (positioning, roadmap tickets, distribution plan, competitive landscape, GitHub org plan) and archived `TASK-409` (founder-box cutover) | (a) move all · (b) move only the AWS files, keep strategy public | **(a)** | business strategy of a paid product has no place in the public tree; `TASK-409` names the account |
| D3 | Contractor's name in the remaining daemon docs (~45 files after the move) | (a) leave · (b) replace with "the infra contractor" | (a) unless she asks | she is credited in PRs already; a rename churns 45 files with no security gain |
| D4 | Execution | (a) Navigator session, manual docs commits in two worktrees · (b) Pilot legs | **(a)** | a cross-repo `git mv` is one atomic session, not two issues; no code besides two comments |
| D5 | Timing of Phase 0 | now, before the move · after | **now** | the only item with real risk; 10 minutes of founder UI |

---

## Verify

```bash
# pilot (public) — after Phase 3 (values from the private denylist)
cd /Users/aleks.petrov/Projects/startups/pilot
git grep -cE "$ACCT_ID|user/$IAM_USER" -- . ; echo "expect: no output"
git ls-files .agent/.context-markers | grep -c '2026-10' ; echo "expect: 0"
git ls-files .agent/tasks | grep -ciE 'console|fleet|tenant|paddle|saas' ; echo "expect: 0 (plus the MOVED pointer)"
make test && make lint

# pilot-console (private) — after Phase 2
cd /Users/aleks.petrov/Projects/startups/pilot-console
ls .agent/system/saas-*.md | wc -l ; echo "expect: 5"
git ls-files .agent/tasks | wc -l ; echo "expect: >= 50"
python3 "$NAV/skills/nav-graph/functions/graph_stats.py" --graph-path .agent/knowledge/graph.json

# denylist proof (in a pilot worktree)
printf "acct $ACCT_ID\n" > .agent/x.md && git add .agent/x.md && git commit -m test ; echo "expect: hook refuses"
git reset -q HEAD .agent/x.md; rm .agent/x.md
```

---

## Done

- [ ] Phase 0 done by the founder (MFA shown on the IAM user; new key active, old key deleted).
- [ ] `pilot` main: pointer stubs in place, README pruned, scrub grep empty, tests green, denylist refuses the account ID.
- [ ] `pilot-console` main: `.agent/` with every moved file, `/nav-start` renders from that cwd, graph stats > 0 memories.
- [ ] Both markers written; this doc archived in **pilot-console** (it is a Pilot Cloud doc too).

---

## Refs

- Founder decision 2026-11-08 (this session): option 2 of {keep · move · make private}.
- Scan results 2026-11-08 11:50–12:10Z (instance IDs, SSM names, account posture, S3 blocks, IAM MFA) — this session.
- Precedent: 2026-09-24 client-material incident → private denylist gate (`scripts/check-secret-patterns.sh`).
- `.agent/.context-markers/2026-11-07_pm-v2.315.1-task643-a1c1d1-shipped-l4-merged-b1-rearmed.md` — session context.

---

## Notes

- AWS treats account IDs as non-secret; the risk is the **pair** account ID + user name + no MFA. Phase 0 is the fix; the move stops the pair from spreading in public.
- The tenant sees its own instance ID and parameters in the console by design. Cross-tenant IDs in the public repo are a minor privacy point, closed by the move.
- `internal/gateway/docs_test.go` uses `system/saas-roadmap.md` as a fixture filename only — no change.
- This doc itself names no account ID or IAM user: it is public until Phase 2 moves it.
- Navigator hook defect seen while planning: `/nav-task` ran twice via the Skill tool; the task-doc guard's `skills.nav_task_ran` flag did not survive to the Write hook (session-scoped section dropped). Written with `strict_block` off for one write, then restored. → navigator session.

**Last Updated**: 2026-11-08
