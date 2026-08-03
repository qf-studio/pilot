# TASK-409: S6-lite — move the founder daemon to AWS (atomic cutover, TUI preserved)

**Status**: ✅ RECOVERED 2026-07-17 08:02Z from split-brain incident [#4393](https://github.com/qf-studio/pilot/issues/4393) — daemon live on **v2.241.1-3-g70b28845** (incl. #4388), open DB FD verified = `/var/lib/pilot/pilot-home/data/pilot.db`. Recovery (founder-authorized ~07:45Z): (1) shadow ledger merged into canonical (95 executions, 94 claims INSERT OR IGNORE, autopilot_pr_state REPLACE, events/logs renumbered; backup `pilot.db.pre-4393-merge.bak`), (2) shim added `/Users/aleks.petrov/.pilot → /var/lib/pilot/pilot-home` (shadow archived at `.pilot.shadow-archived-20260717`), (3) #4388 squash-merged + rebuild on box. Post-start proof: "Skipping re-dispatch — completed execution exists" (GH-85) = merged history honored. #4393 code-hardening items (banner DB path, refuse silent fresh ledger) remain open. Pitfall: `.agent/knowledge/memories/pitfalls/absolute-state-paths-bypass-cutover-shim.md`. Incident window: daemon stopped 01:27–08:01Z. — Cutover itself was 2026-07-16 22:32Z (tmux `pilot`, wrapper /home/ec2-user/start-pilot.sh). Local ~/.pilot FROZEN (rollback tar ~/pilot-state-rollback-20260716.tgz, 7 days). NEVER `pilot start` locally again without the rollback procedure. Decisions (founder, 2026-07-16 ~21:05Z): **t3.xlarge (16GB)** · **Claude subscription login** (device flow on box) · **Created**: 2026-07-16 ~21:00Z
**Driver**: local machine swap exhaustion (daemon + Claude sessions + dev work on one laptop); always-on wanted. NOT a speed play (time_to_pr is Claude-API-bound).
**Prime directives**: (1) NEVER dual-serve — no second daemon may poll a repo the local one owns, at any instant; (2) config moves VERBATIM — no refactors, no "improvements" mid-migration; (3) TUI dashboard preserved (tmux on the box, attach via SSM); (4) local `~/.pilot` stays frozen 7 days as instant rollback.

## The three landmines (and their disarms)

| Landmine | Why it detonates | Disarm |
|---|---|---|
| **Dual-serve** | Every dedup guard incl. `execution_claims` is per-SQLite-DB — two daemons on the same repos = cross-machine duplicate class with ZERO guard. Telegram double-poll also 409s | Phased plan: remote daemon runs with **zero projects + no telegram** until cutover; cutover = stop local → verify 0 procs → move DB → start remote. One-way gate with checklist |
| **Ledger path-keys** | `executions.project_path`, `execution_claims(task_id, project_path, generation)` key on ABSOLUTE local paths (`/Users/aleks.petrov/Projects/...`). Different paths on the box → claims/HasTerminalCompletion lookups miss ALL history → mass re-pick of already-done issues (the GH-91 storm, fleet-wide) | **Identical paths on Linux**: `mkdir -p /Users/aleks.petrov/Projects` (legal on Linux) as a symlink → `/var/lib/pilot/repos`; clone repos to identical absolute paths. ZERO DB surgery. (`canonicalizeProjectPath` #4367 helps but do not rely on it) |
| **Claude auth** | macOS Claude Code auth lives in Keychain — not file-portable. Executor spawns `claude --dangerously-skip-permissions` headless | One-time `claude login` (device-code flow) inside an SSM session on the box (~2 min, founder present). Alternative = `ANTHROPIC_API_KEY` (metered billing — rejected unless founder prefers) |

## Live state (2026-07-16 21:12Z)

- ✅ Phase 0 complete. ✅ Phase 1 infra complete: instance **i-0e0c1ca34e7b561f9** (t3.xlarge, IMDSv2, pilot-agent profile, sandbox PrivA subnet, agent SG) + data volume **vol-068f78767202b20aa** (200GB gp3 @ /var/lib/pilot, fstab by UUID) + 8GB swapfile. Provisioned via the mgmt runner (i-0147f5c24d234cdbb) — user `aleks` lacks iam:PassRole/ec2:CreateVolume directly; SSM RunCommand on the runner is the sanctioned privileged path.
- ✅ Toolchain: claude 2.1.104 (AMI-baked), node v22.22.2, go 1.25.8 (installed), gh 2.96.0 (installed — AMI gap, fleet-design correction #1 confirmed live), tmux 3.2a, rsync.
- ✅ Path shims verified: `/Users/aleks.petrov/Projects → /var/lib/pilot/repos` (write-tested as ec2-user), `/home/ec2-user/.pilot → /var/lib/pilot/pilot-home`.
- ⏳ NEXT: founder auth session (claude login + gh auth login) → repo clones + pilot build → Phase 2 dry run.

## Phase 0 — Preflight (read-only, zero risk) — ~15 min
- `AWS_PROFILE=quantflow` works; account `529088297614` eu-central-1; sandbox stack values (subnet, SG, `iam-pilot-agent` instance profile), golden AMI `ami-0bb00da3a38b9c176` present.
- Inventory: `du -sh ~/.pilot` (db/logs/recordings sizes), repo list from config `projects:` (8 repos incl. pointer), gh auth method, tmux availability on AMI.
- Confirm the AMI's baked `claude` CLI version; plan is to install latest on the box regardless (one box, no AMI rebake).

## Phase 1 — Provision + prepare (reversible; local daemon untouched) — ~45 min
- Launch **1× instance** (size = decision Q1) from golden AMI: sandbox subnet/SG/instance-profile, **IMDSv2 required**, gp3 root 30GB + **data volume 200GB** at `/var/lib/pilot` (repos + worktrees are heavy — 800MB+/worktree, GH-2168), 8GB swapfile (belt + suspenders), tags `pilot:founder-box`.
- Install: pilot binary `v2.241.0-4-g1bb83f7f` (scp via SSM / S3), latest `claude` CLI, verify `gh`/`git`/node from AMI, `tmux`.
- **Path shim**: `/Users/aleks.petrov/Projects → /var/lib/pilot/repos` (symlink), then clone all configured repos to identical absolute paths. `~/.pilot → /var/lib/pilot/pilot-home` symlink too (data volume owns all state).
- One-time auth (founder present, one SSM session): `claude login` (Q2), `gh auth login` with PAT (or GITHUB_TOKEN in the tmux env), `git config` identity. Verify headless: `claude -p 'ok'` and `gh api user`.

## Phase 2 — Dry run (still zero dual-serve risk) — ~20 min
- Config on box = **verbatim copy EXCEPT**: `projects: []` (empty) and telegram/slack adapters disabled (prevents 409 + any poll overlap while local still runs).
- Start in tmux: daemon boots, TUI renders over SSM attach, `:9091/metrics` responds, logs clean. Kill it. This proves everything except polling — with zero chance of a duplicate.

## Phase 3 — CUTOVER (one-way gate; target ~15 min downtime; founder present)
1. **Quiesce**: wait for empty running/queued (or accept killing an in-flight run — it retries via generation claims).
2. **Stop local**: `pkill -f "pilot start"` → verify 0 (ps -p, not pgrep self-match) → `tar czf ~/pilot-state-backup-$(date).tgz ~/.pilot` (rollback artifact).
3. **Move state**: rsync `~/.pilot/data/pilot.db` + `config.yaml` (verbatim, full adapters + all projects) + `~/.pilot/recordings` (optional) to the box.
4. **Start remote** in tmux with the SAME flags: `GITHUB_TOKEN=$(gh auth token) pilot start --github --telegram --env stage --dashboard --slack`.
5. **Verify (the same discipline as today's restarts)**: banner `v2.241.0-4-g1bb83f7f`, exactly 1 process, no Telegram 409, `pilot_queue_depth` gauge live, poller ticks in log, **one trivial execution completes E2E** (a canary-sandbox scenario counts).
6. **Freeze local**: local `~/.pilot` untouched for 7 days; no local `pilot start` ever again without reading the rollback section.

## Phase 4 — Post-cutover hardening (day 2, non-blocking)
- DLM daily snapshot policy on the data volume (tag-targeted).
- Watch-loop adaptation: hourly watch checks go over `aws ssm send-command` (I adapt at fire time — a cutover marker file tells the watch where the daemon lives).
- Optional: Tailscale for one-command `ssh box` + local `:9091`; CloudWatch agent for daemon.log.
- Release train: verify the next 16:00 CET train cuts from the box (gh auth + tag push).

## Rollback (any time in first 7 days)
Stop remote daemon (SSM) → verify 0 → copy `pilot.db` back from box (it has the newest state) → start local with old command. If box-side DB is suspect: restore from the pre-cutover tarball and accept the gap (re-picks are claim-guarded).

## Explicitly NOT in scope
Config refactors (env-refs, secret moves — the Anthropic-key rotation is a separate standing item), B6 provisioner dogfood (hand-provision this box; B6 serves tenants), multi-box, fleet VPC (sandbox stacks suffice for one founder box), auto-restart-on-upgrade (separate decision).

## Refs
- Marker section "Pilot-on-AWS question" (rationale) · fleet design §2/§5 (posture) · SOP safe-daemon-restart (verify discipline) · mem-150 (verify PID/banner ALWAYS)
