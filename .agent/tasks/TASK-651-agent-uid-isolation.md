# TASK-651: Agent isolation — agent sessions under a separate Unix user, read-only GitHub token per run, secrets from SSM

**Status**: 🚀 **U0 and U1 DISPATCHED 2026-11-12** (`pilot` + `no-decompose`): U0 [#6320](https://github.com/qf-studio/pilot/issues/6320) host setup script · U1 [#6321](https://github.com/qf-studio/pilot/issues/6321) run_as_user spawn mechanics, off by default. **O1 DONE 2026-11-12 09:14Z**: the general sudo rule of the daemon user is commented out on the box (backup `/root/90-cloud-init-users.bak-task651-o1`; daemon healthy). Design: `.agent/system/agent-isolation-design.md`. Planned after U1: U2 workspace clone and hand-over · U3 gates and evidence as the agent user · U4 read-only GitHub token per run · U5 per-project override. Waiting for the founder: O2 (GitHub App) and O3 (new secret values in SSM). Review every PR with a run-it agent before the approval.
**Created**: 2026-11-12
**Assignee**: Pilot (one leg at a time) + operator steps by a Navigator session with founder consent
**Parent**: TASK-650 leg H1

---

## Context

**Problem**: the daemon and every agent session run as one Unix user. On the box that user has passwordless root, the config holds seven literal secrets, the start script exports a personal GitHub token with write rights that every agent inherits, the instance role is readable from the metadata service, and the gateway on port 9091 has no auth. The read guard is a substring matcher and stays one (mem-305). Quality gates run agent-written code as the daemon user.

**Goal**: an operating-system boundary. Every process that runs model output or repository code runs as a second user without sudo and without write credentials. The daemon user keeps every write credential and never executes anything inside a directory the agent can write. Nothing breaks on the way: each step is off by default, checked, and reversible.

**Founder decisions 2026-11-12**: sudo rule from the daemon user to the agent user only · the daemon user loses general root sudo · GitHub App first, then a read-only installation token per run · secrets from SSM through the start script · "make it work secure, do not break anything".

---

## Known Pitfalls & Patterns

- **PATTERN** (mem-303): the box has no git credential outside the daemon; a branch delete through `git push` runs the full pre-push gate. → The hand-over in U2 fetches into the daemon's repository and pushes with hooks off; no credential ever reaches the agent side.
- **PITFALL** (mem-300): a same-user agent reads every secret; never ask an executor ticket to diagnose the live box. → All box steps (O1–O4, S1–S2) are operator steps, not Pilot tickets.
- **PITFALL** (mem-302): a security PR was approved before the run-it review finished. → Every leg here is reported with a hold line; the review agent runs the code before a go.
- **PITFALL** (mem-301): follow-ups race the automatic post-merge fix issues on the same files. → One leg at a time; check for an open post-merge-review issue before the next dispatch.
- **PITFALL** (`runselfreview-runs-in-repo-root-phantom-reimplementation`): a helper spawn ran in the wrong directory. → U1 routes every model spawn through one constructor, with a test per site.
- **PITFALL** (`ci-fix-continuation-rebuilds-branch-from-main-drops-original-commits`): branch continuation by name loses commits. → U2 hands over by commit SHA and verifies the fetched head equals the workspace head.
- **LEARNING** (mem-304): a spec must state the fallback and the limits. → Preflight is fail-closed with one Error line; every leg states the behaviour when the option is empty (unchanged).

---

## Acceptance Criteria

- [ ] With `executor.claude_code.run_as_user` empty, every spawn has the same argv and environment as before each leg (asserted by tests).
- [ ] With it set on the box: `ps` shows every claude, git-in-workspace, gate and evidence process under the agent user; no process of the agent user has a GitHub token with write rights in its environment.
- [ ] As the agent user: read of the daemon config, logs, recordings and database fails with a permission error; `sudo -n true` fails; the metadata address and port 9091 are refused; `git push` to the task repo is refused by GitHub.
- [ ] A complete ticket run on a sandbox project ends with a PR, and the push came from the daemon's repository after the secret gate.
- [ ] The config file holds no literal secret; `secret values registered for redaction` shows the same count as before O3.
- [ ] Rollback of each step is exercised once (O1, O4 undo mode, option cleared).

---

## Implementation

### Phase O — operator steps on the box (no Pilot ticket; founder go for each)

- [x] **O1** (done 2026-11-12 09:14Z) Remove the general sudo rule of the daemon user (file `90-cloud-init-users`); keep a root-only copy. Check before: 2 of 13,213 recordings in 14 days hold a sudo command; no daemon code calls sudo. Check after: daemon healthy, one ticket run completes. Rollback: restore the file from a root session.
- [ ] **O2** GitHub App provisioning (TASK-461 operator steps): App key to SSM, `adapters.github.app` in the config, remove the personal token export from the start script, full restart, verify a poll and an autopilot action after the one-hour token boundary.
- [ ] **O3** Secrets to SSM: for each of the seven literal keys (Slack bot and app tokens, the Slack classifier key, Telegram, Jira, Discord, the bot key) a SecureString and a `${VAR}` reference; the start script reads them; rotate the exposed values in the same step (Anthropic key, Slack tokens, second Telegram rotation). Full restart (a hot restart keeps the old environment).
- [ ] **O4** Run the U0 script: agent user, group, home, caches, Claude plugin install for the agent user, firewall rules, sudoers rule. Inert until the option is set.

### Phase C — code legs (Pilot), each merged with the option empty

- [ ] **U0** host setup script with dry-run and undo, tested with a mocked PATH (dispatch-ready, below).
- [ ] **U1** config option, one spawn constructor for every model process, allowlist environment, kill through sudo, fail-closed preflight, group-readable guard files (dispatch-ready, below).
- [ ] **U2** workspace as an agent-owned clone; every in-workspace command through the constructor; hand-over by fetch into the daemon's repository; cleanup as the agent user. Needs a focused research pass on `GitOperations` and the worktree call sites first.
- [ ] **U3** quality gates, evidence commands, workflow hooks and the auto-preserve commit run as the agent user; the daemon push runs with hooks off.
- [ ] **U4** read-only installation token per run, refreshed in a file that the `gh` shim and a git credential helper read; the GitHub names leave the keep list of the agent environment when the option is set. Depends on O2.
- [ ] **U5** per-project override of the option, for the sandbox switch.

### Phase S — switch

- [ ] **S1** one sandbox project; 20 tasks with no new failure class.
- [ ] **S2** every project; archive this task after one week without a rollback.

---

## Legs — pilot (daemon)

### LEG U0 — repo: qf-studio/pilot — title: feat(box): host setup script for the agent user — user, group, home, caches, sudoers rule, firewall rules, with dry-run, undo and a mocked-PATH test

probe: present qf-studio/pilot pilot-backup-s3_test\.sh
probe: absent  qf-studio/pilot visudo[[:space:]]-cf[[:space:]]"\$

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`) puts agent sessions under a second Unix user on the Linux host. This leg ships only the host script. No daemon code calls it; an operator runs it by hand. The directory of box scripts already holds `scripts/box/pilot-backup-s3.sh` with a plain-bash test that uses a PATH of mock commands, and CI runs ShellCheck and the tests of that directory.

## Scope

1. New file, plain text: scripts/box/setup-agent-user.sh. Bash, `set -euo pipefail`, must run as root, idempotent. Arguments: --daemon-user (default ec2-user), --agent-user (default pilot-agent), --group (default pilot), --data-dir (default /var/lib/pilot), --dry-run, --undo, --keep-daemon-sudo.
2. Apply mode, in this order, each step skipped when already done:
   - create the group; create the agent user as a system user with no login shell and home DATA_DIR/agent-home; add the daemon user to the group;
   - create DATA_DIR/agent-home (owner agent, mode 0750), DATA_DIR/agent-workspaces (owner agent, group as given, mode 2770), DATA_DIR/agent-home/go/cache and go/mod (owner agent);
   - set group read and traverse on DATA_DIR/repos and DATA_DIR/bin without a change of owner, and no write bit for the group;
   - write one sudoers file for the rule "daemon user may run any command as the agent user, NOPASSWD, SETENV" and validate it with `visudo -cf` BEFORE it is moved into place; a failed validation leaves the system unchanged and exits non-zero;
   - unless --keep-daemon-sudo is given: comment out, in the cloud-init sudoers file, only the lines that name the daemon user, after a copy to a root-only backup next to it, and validate with `visudo -c`; on a failed validation restore the backup;
   - firewall, owner match on the agent user, in a dedicated chain so that undo can remove exactly these rules: reject the metadata address 169.254.169.254 and its IPv6 form, reject TCP to 127.0.0.1 port 9091; install a oneshot systemd unit that applies the chain at boot.
3. --dry-run prints every command and changes nothing. --undo removes the firewall chain and unit, the sudoers file of this script, restores the cloud-init file from the backup when one exists, and leaves the user, the group and the directories in place (it prints the commands to delete them, and does not run them).
4. A final summary line per step: done, skipped or failed.
5. New file, plain text: scripts/box/setup-agent-user_test.sh, in the style of the backup test: a temp root, a PATH of mock commands (useradd, groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown, chmod, id, getent) that record their arguments. Cases: dry run changes nothing; apply runs the steps in order; a second apply skips every step; a failing `visudo` mock leaves no sudoers file and exits non-zero; undo removes what apply added; --keep-daemon-sudo leaves the cloud-init file untouched.
6. Make the script take the paths of the sudoers directory and the systemd directory from environment variables with the real paths as defaults, so that the test can point them at the temp root.

## Acceptance

- [ ] `bash scripts/box/setup-agent-user_test.sh` passes; paste the output into the PR body.
- [ ] `shellcheck scripts/box/setup-agent-user.sh scripts/box/setup-agent-user_test.sh` reports nothing; paste the output into the PR body.
- [ ] Move the `visudo` validation after the install of the sudoers file -> the failing-visudo case fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Any call of this script from Go code or from another script. The Claude plugin install for the agent user (operator step). A run on a real host.

## Refs

- Navigator TASK-651 in this repo · TASK-650 (parent)

### LEG U1 — repo: qf-studio/pilot — title: feat(executor): run_as_user — start every model process as a separate Unix user through sudo, with an allowlist environment, a kill through sudo and a fail-closed preflight (off by default)

probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnvFor\(
probe: present qf-studio/pilot func[[:space:]]configureProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]setupRunGuards\(
probe: absent  qf-studio/pilot RunAsUse[r][[:space:]]+string
probe: absent  qf-studio/pilot func[[:space:]]agentComman[d]\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, sections 4.1 and 4.3). Today every model process is built inline with exec.CommandContext and runs as the daemon user: the main backend in `internal/executor/backend_claudecode.go`, epic planning in `internal/executor/epic.go`, the judge and reviewers in `internal/executor/intent_judge.go`, the parsers and classifiers (`internal/executor/subtask_parser.go`, `internal/executor/effort_classifier.go`, `internal/executor/complexity_classifier.go`), research subagents in `internal/executor/parallel.go`, the summary and evidence calls in `internal/executor/runner.go`, and the other backends (`internal/executor/backend_opencode.go`, `internal/executor/backend_qwencode.go`). Each takes its environment from modelSubprocessEnvFor in `internal/executor/model_env.go`, a deny-list scrub that keeps the GitHub and Claude names. The process group is set by configureProcessGroup and killed by killProcessGroup in `internal/executor/process_group.go`. The per-run guard directory and settings file come from setupRunGuards in `internal/executor/read_guard.go` with modes 0700 and 0600; the gh shim directory comes from `internal/executor/ghguard_spawn.go`.

This leg adds the option and the mechanics only. With the option empty nothing changes. The workspace, the gates and the GitHub token follow in later legs, so the option must stay empty on every host until they merge; say so in the config example.

## Scope

1. Config: in the ClaudeCodeConfig struct of `internal/executor/backend.go`, two string fields next to ReadGuard: run_as_user and run_as_group (yaml names; empty by default). Document both in `configs/pilot.example.yaml` with the sentence "Linux only; leave empty until the agent workspace legs of TASK-651 are released".
2. One constructor, new file, plain text: internal/executor/agent_command.go. A function agentCommand(ctx, name, args...) returns the exec.Cmd for a model process. With the option empty it returns exactly exec.CommandContext(ctx, name, args...). With the option set, on Linux, it returns the command `sudo -n -u <user> [-g <group>] --preserve-env=<comma list of the names in cmd.Env> -- <name> <args...>`; the list is filled by a second function that the caller uses to set the environment, so that the names in the flag and the entries of cmd.Env always match. No value of an environment variable is ever placed in argv. On a platform other than Linux a set option fails the preflight (item 6) and the constructor behaves as with an empty option.
3. Use the constructor at every model spawn site named in Context. A site that is missed is a defect: add one test per site that sets the option and asserts the first argv element is sudo and that the model binary follows the `--`.
4. Environment with the option set: an allowlist, not the deny-list. Keep: PATH, LANG, LC_ALL, TERM, TZ, TMPDIR, the Go cache and proxy names (GOCACHE, GOMODCACHE, GOFLAGS, GOTOOLCHAIN, GOPROXY, GONOSUMDB, GOPRIVATE), NODE_OPTIONS, PILOT_EXECUTOR, every name with the prefix CLAUDE_ or ANTHROPIC_ that the present function keeps or injects (the billing credential of the execution included), and HOME and CLAUDE_CONFIG_DIR set to the home of the agent user (resolve it once with os/user). Drop everything else, the GitHub names included; the GitHub token for the agent comes in a later leg. With the option empty the present deny-list result is unchanged, byte for byte.
5. Kill: with the option set, killProcessGroup runs `sudo -n -u <user> kill -s <signal> -- -<pgid>` with a 5 second timeout, because the daemon user may not signal the agent user or sudo. Keep the direct kill for the empty option. When the sudo kill fails, log one Error line with the pgid.
6. Preflight, called where PreflightReadGuard is called at daemon start: with the option set, check Linux, the user exists and is not the user of the daemon, `sudo -n -u <user> true` exits 0 within 5 seconds, and the home of the user exists. On any failure log one Error line that names the failed check and set a process-wide flag; while the flag is set, every model spawn returns an error "run_as_user preflight failed: <check>" and no process starts. It must not fall back to a run as the daemon user. With the option empty the preflight does nothing.
7. Guard files with the option set: the directory from setupRunGuards and the gh shim directory get mode 0750 and the files in them 0640 (scripts 0750), and the group of run_as_group when it is set (os.Chown with the gid; the daemon user is a member). They stay owned by the daemon user. With the option empty the modes stay 0700 and 0600.
8. Tests, table-driven, no root needed, in new test files next to the code: the constructor with the option empty returns the plain command; with it set the argv is as in item 2 and holds no environment value; the preserve list equals the names in cmd.Env; the allowlist drops GITHUB_TOKEN, GH_TOKEN, AWS names and an arbitrary FOO_TOKEN and keeps the names of item 4; the kill builds the sudo command; the preflight fails closed for a missing user, for the user of the daemon, and for a sudo that exits non-zero (use a PATH with a mock sudo script), and a spawn after a failed preflight returns the error and starts nothing; the guard file modes for both states of the option.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentCommand|RunAsUser|ModelEnv|ReadGuard'` passes; paste the output into the PR body.
- [ ] Use exec.CommandContext directly at one spawn site -> the test of that site fails.
- [ ] Let a failed preflight fall back to the plain command -> the fail-closed test fails.
- [ ] Add GITHUB_TOKEN to the allowlist -> the allowlist test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The workspace and git operations (still a worktree owned by the daemon user, so the option must stay empty on hosts). Quality gates, evidence commands and workflow hooks. The GitHub token for the agent. A per-project override. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · TASK-650 (parent) · design section 4.1

---

## Out of Scope

- Isolation between two agent sessions that run at the same time (one agent user for all; a user per project is a later option).
- Network egress control beyond the metadata address and port 9091.
- `systemd-run` per session, containers, namespaces.
- A change of the read guard (stays a substring matcher, mem-305).
- macOS hosts.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Privilege path | sudoers rule; `CAP_SETUID` on the binary; setuid helper; `systemd-run` | sudoers rule, daemon user → agent user only | survives self-upgrade, no new privileged code, one file to audit and undo |
| Workspace | shared worktree with group write; agent-owned clone with fetch hand-over | agent-owned clone (`git clone --shared`), hand-over by `git fetch` | a shared `.git` lets a hook or config value run as the daemon user |
| Agent GitHub access | daemon token; stored read-only token; no token; App token per run | read-only App installation token per run, one hour, refreshed in a file | no stored second secret; a leak cannot write; needs the App (O2) |
| Daemon user root | keep; remove | remove | agents ran with passwordless root; 2 of 13,213 recordings used sudo |
| Secret source | `ssm:` resolver in the loader; start script exports from SSM | start script | the loader already expands `${VAR}`; no new code or dependency |
| Preflight failure | fall back to the daemon user; refuse runs | refuse runs, one Error line and an alert | the operator asked for the boundary; a silent fallback removes it |
| Agent environment | deny-list scrub; allowlist | allowlist when the option is set | the deny-list keeps tokens on purpose |

---

## Verify

```bash
# code legs
go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentCommand|RunAsUser|ModelEnv|ReadGuard'
bash scripts/box/setup-agent-user_test.sh

# on the box, after S1 (root session)
ps -eo user:20,comm | grep -E 'claude|node' | awk '{print $1}' | sort | uniq -c
sudo -n -u pilot-agent cat /home/ec2-user/.pilot/config.yaml      # must fail: Permission denied
sudo -n -u pilot-agent sudo -n true                                # must fail
sudo -n -u pilot-agent curl -s -m 2 http://169.254.169.254/latest/meta-data/   # must fail
sudo -n -u pilot-agent curl -s -m 2 http://127.0.0.1:9091/health   # must fail
```

---

## Done

- [ ] Design doc and this task doc merged; TASK-650 H1 points here.
- [ ] O1–O4 done with a recorded check and a rollback test each.
- [ ] U0–U5 merged, each reviewed by a run-it review agent before the merge.
- [ ] S1 and S2 done; the box checks of "Verify" pass.
- [ ] SOP for the agent user (rotation, rollback, a new host) written.

---

## Refs

- `.agent/system/agent-isolation-design.md`
- `.agent/tasks/TASK-650-secret-exposure-hardening.md` · `.agent/tasks/TASK-461-app-cutover-sdk-token-wiring.md`
- `.agent/sops/operations/secret-leak-response.md` · `.agent/sops/development/dispatch-task-legs-to-pilot.md`
- Memories mem-300, mem-301, mem-302, mem-303, mem-304, mem-305

---

**Last Updated**: 2026-11-12

## Refs

- U0 → https://github.com/qf-studio/pilot/issues/6320
- U1 → https://github.com/qf-studio/pilot/issues/6321
