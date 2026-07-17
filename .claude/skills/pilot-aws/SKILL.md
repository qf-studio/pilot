---
name: pilot-aws
description: Operate the AWS-hosted Pilot daemon (founder box) — status, dashboard, logs, queue queries, start/stop/restart, rebuild/upgrade, metrics tunnel, troubleshooting. Auto-invoke when user says "pilot aws", "check the box", "aws daemon", "pilot on aws", "box status", "restart pilot on aws", "box logs", or any operation against the hosted daemon.
allowed-tools: Bash, Read
version: 1.0.0
---

# Pilot AWS Operations (founder box)

Since the S6-lite cutover (2026-07-16, TASK-409) the Pilot daemon runs on an
EC2 box, NOT locally. **Local `pgrep pilot` returning 0 is correct.**

## Constants

```
INSTANCE   i-0e0c1ca34e7b561f9        # "pilot-founder-box", t3.xlarge, eu-central-1a
PROFILE    quantflow                   # export AWS_PROFILE=quantflow for every aws call
REGION     eu-central-1
RUNNER     i-0147f5c24d234cdbb         # mgmt runner (AdministratorAccess) — IAM/privileged ops only
DAEMON     tmux session "pilot" as ec2-user, wrapper /home/ec2-user/start-pilot.sh
BINARY     /usr/local/bin/pilot        # built on-box from source (go + make present)
STATE      /home/ec2-user/.pilot → /var/lib/pilot/pilot-home (200GB data volume)
REPOS      /Users/aleks.petrov/Projects → /var/lib/pilot/repos (path shim — ledger keys
           on the macOS-era absolute paths; NEVER "fix" these symlinks)
DB         /home/ec2-user/.pilot/data/pilot.db
LOG        /home/ec2-user/.pilot/logs/daemon.log (+ daemon-stderr.log)
```

## The SSM command pattern (used by everything below)

```bash
export AWS_PROFILE=quantflow AWS_DEFAULT_REGION=eu-central-1
CMD=$(aws ssm send-command --instance-ids i-0e0c1ca34e7b561f9 \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["<shell here>"]' \
  --query Command.CommandId --output text)
sleep 10
aws ssm get-command-invocation --command-id $CMD \
  --instance-id i-0e0c1ca34e7b561f9 --query StandardOutputContent --output text
```
For multi-line/quote-heavy payloads write a JSON file and use
`--parameters file:///tmp/x.json`; for shipping FILES to the box, base64 the
content into the command (`echo <b64> | base64 -d > target`) — inline heredocs
with `\n` escapes DO NOT survive SSM JSON (verified failure mode).

## Operations

### Status (first move, zero GitHub quota)
```bash
~/bin/pilot-board            # daemon health, queue, autopilot, log tail
~/bin/pilot-board --gh       # + GitHub issues/PRs (costs shared user quota — sparingly)
```
Remote half lives at `/usr/local/bin/pilot-board-remote` on the box (edit there).

### Live TUI dashboard
```bash
~/bin/pilot-dash             # SSM interactive → tmux attach -t pilot
```
Detach: `Ctrl-B` then `D`. **NEVER press `q` or `Ctrl-C` inside the TUI** — that
stops the daemon. Fallback: plain ssm session → `sudo su - ec2-user` →
`tmux attach -t pilot`.

### Logs
```bash
# tail (adjust -n / add grep):
SSM: tail -50 /home/ec2-user/.pilot/logs/daemon.log
# common filters: 'rate limit', 'claim lost', 'approval', 'ERROR', a task id
```

### Queue / ledger queries (read-only)
```bash
SSM: sqlite3 -column /home/ec2-user/.pilot/data/pilot.db \
  "SELECT task_id,status,datetime(created_at) FROM executions \
   WHERE status IN ('running','queued') ORDER BY created_at;"
```
Key tables: `executions`, `execution_claims` (task_id, project_path, generation),
`autopilot_pr_state`, `autopilot_scope_release`, `instance_events`.
⚠️ Timestamp trap: pre-2026-07-16 rows may carry legacy `…+02:00` string format —
never filter by string time ranges across eras; use rowid or exact ids.

### Stop / start / restart  ⚠️ OPERATOR-CONSENT ACTIONS
Never do these on your own judgment during watch/autonomous modes; in
interactive sessions get explicit user go-ahead. In-flight executions die
(they retry via generation claims — proven safe, but wasteful).
```bash
# STOP    (graceful; wait, then verify 0):
SSM: sudo -iu ec2-user tmux send-keys -t pilot C-c   # TUI quit = graceful shutdown
     sleep 15; ps -eo comm | grep -c '^pilot$'        # must be 0
# START:
SSM: sudo -iu ec2-user tmux new-session -d -s pilot -x 220 -y 50 /home/ec2-user/start-pilot.sh
     sleep 20; ps -eo comm | grep -c '^pilot$'        # must be 1; then check pilot-board
# RESTART = STOP, verify, START, verify (banner version + no Telegram 409 + poller ticks).
```
After ANY start: verify version (`/usr/local/bin/pilot version`), exactly 1
process, `curl -s localhost:9091/metrics | grep pilot_queue_depth` (via SSM).

### Rebuild / upgrade the box binary
```bash
SSM (as ec2-user): cd /Users/aleks.petrov/Projects/startups/pilot && \
  git fetch -q --tags origin main && git checkout -q <tag-or-origin/main> && \
  make build && sudo install -m 0755 bin/pilot /usr/local/bin/pilot && \
  git checkout -q main && /usr/local/bin/pilot version
```
Then RESTART (above) to activate. Prefer building from a released tag.
Expect ~1 quiet hour post-restart until #4391 ships: startup rescans can burn
the GitHub user-aggregate rate pool (see Troubleshooting).

### Metrics tunnel (grafterm / local Prometheus tools)
```bash
~/bin/pilot-tunnel     # box:9091 → localhost:9091, keep running; then `pilot-tui` (grafterm alias)
```

### Box shell (interactive)
```bash
aws ssm start-session --target i-0e0c1ca34e7b561f9 --profile quantflow --region eu-central-1
sudo su - ec2-user
```

### Privileged infra ops (IAM, volumes, instance-level)
User `aleks` lacks iam:PassRole/ec2:CreateVolume etc. Route through the mgmt
runner via SSM RunShellScript on `i-0147f5c24d234cdbb` (it has admin). Scope
every grant minimally; never print policy docs containing secrets.

## Hard rules

1. **One daemon, ever.** Never start pilot locally while the box serves the
   repos (dual-serve = cross-machine duplicate class; claims are per-DB).
   Rollback procedure lives in `.agent/tasks/TASK-409-s6-lite-aws-cutover.md`.
2. **Never touch the path shims** (`/Users/aleks.petrov/...` symlinks on the
   box) — ledger + claims key on those exact strings.
3. **GitHub API is one shared per-USER pool (5000/hr)** across every token,
   session, and the daemon. Prefer sqlite/metrics over `gh` for status. If the
   daemon logs "rate limit exceeded for user ID …" — it self-recovers on the
   rolling window; do not thrash retries. (#4391 tracks the durable fix.)
4. Secrets: never echo tokens/keys; never `ps` full args of processes holding
   tokens; the wrapper script pattern exists precisely to keep tokens out of
   argv. Config on the box is verbatim-from-laptop; changes = operator consent
   + restart.
5. Trust the ledger over dashboard panels (known mislabel: awaiting_approval
   rendered as "rebase" until the GH-4383 fix is in the running binary).

## Troubleshooting quick table

| Symptom | Likely cause | Move |
|---|---|---|
| Board says queue empty but daemon log shows executions; log claim generations exceed claims table | split-brain shadow ledger (#4393 class — daemon opened a DB at an unshimmed path) | `sudo readlink /proc/$(pgrep -x pilot)/fd/*` must include `/var/lib/pilot/pilot-home/data/pilot.db`; if not: STOP daemon, locate+merge shadow DB, fix shim |
| ALL task executions fail `unknown: exit status 1` after ~3m, 0 tokens, stream shows `api_retry`/`fetch failed`; judge/preflight children work | RLIMIT_AS cap on executor children (#4401 class — GH-3028 "RSS cap", darwin no-op) | `grep 'address space' /proc/<claude-child>/limits` must be `unlimited`; config `subprocess_limits` is `enabled: false` since 2026-07-17 (backup `config.yaml.bak-4396`) — OOM cap off until #4401 |
| Queue frozen, pollers 403 "rate limit … user ID" | user-aggregate GitHub pool exhausted (startup rescans, parallel sessions) | wait for rolling window; stop nonessential gh usage; see #4391 |
| Queue frozen, "dispatch claim lost" every poll, no 403s | dead-owner non-terminal rows holding gen-N claims (post-restart/cutover) | see #4392; workaround = mark orphan rows `stalled` with audit note (exact-id UPDATE, never string time-ranges) |
| tmux session gone, no pilot process | wrapper/script error at spawn | check `daemon-stderr.log`; verify wrapper intact (`cat start-pilot.sh`); restart per above |
| TUI monochrome | TERM captured at daemon start | `~/.tmux.conf` already sets 256color; colors return on next restart — do not bounce for paint |
| PR stuck "rebase N/3" in panel | awaiting_approval mislabel | ledger: `SELECT stage FROM autopilot_pr_state WHERE pr_number=N` |
| Box unreachable via SSM | agent/instance down | `aws ec2 describe-instances --instance-ids i-0e0c…` → LOUD escalate to operator; never assume |

## Refs

- Cutover plan + rollback: `.agent/tasks/TASK-409-s6-lite-aws-cutover.md`
- Restart discipline: `.agent/sops/operations/safe-daemon-restart.md` (verify PID+banner ALWAYS)
- Open hardening: #4391 (rate-budget client), #4392 (orphan reconciliation)
