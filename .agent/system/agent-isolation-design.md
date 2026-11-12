# Agent isolation: agent sessions under a separate Unix user

**Status**: design accepted 2026-11-12 (founder decisions below). Plan and legs: `.agent/tasks/TASK-651-agent-uid-isolation.md`.
**Scope**: the Linux host that runs the Pilot daemon. macOS and local development keep the present behaviour; every part is off by default.

## 1. Why

The daemon and every agent session run as one Unix user. Facts from the box on 2026-11-12:

- That user has `(ALL) NOPASSWD: ALL` in sudoers. Every agent session can become root. The bash guard blocks only a `sudo rm` substring.
- The daemon config holds seven secrets as literal text (Slack bot and app tokens, a classifier key, Telegram, Jira and Discord tokens, a bot key). The start script exports a personal GitHub token with write rights, and every agent inherits it (`internal/executor/model_env.go` keeps the GitHub names on purpose).
- The instance role is readable from the metadata service by any local process.
- The gateway on port 9091 has no auth configured; its API can read project files.
- The read guard (`internal/executor/read_guard.go`) is a substring matcher against accidental reads. The founder decision of 2026-11-11 (mem-305) keeps it that way; the boundary must be the operating system.
- The quality gates, evidence commands and git hooks execute code that the agent wrote, as the daemon user, with the full daemon environment.

## 2. Founder decisions (2026-11-12)

1. Privilege path: a sudoers rule that lets the daemon user run commands as the agent user, and nothing else. No root daemon, no setuid helper.
2. The daemon user loses its general root sudo.
3. GitHub: finish the GitHub App provisioning (TASK-461 operator steps). The daemon mints a read-only installation token for each run and gives only that to the agent.
4. Secrets: the config holds `${VAR}` references; the start script reads the values from SSM SecureString.
5. Constraint: nothing may break. Every step is off by default, has a check before and after, and has a rollback.

## 3. Threat model

In scope: an agent session, or code that it writes, that reads Pilot secrets, pushes or calls the GitHub API with write rights, reads cloud credentials, changes the daemon or its guards, or gets root — by accident or on purpose through ordinary commands.

Out of scope: kernel exploits; isolation between two agent sessions that run at the same time (they share the agent user in this design); network egress control in general.

## 4. Target state

Two users and one rule:

- **Daemon user** (today `ec2-user`): owns the daemon home, the config, the database, logs, recordings, the binary and the daemon's repositories. Sudo right: run commands as the agent user only.
- **Agent user** (`pilot-agent`): no sudo, no login shell, its own home on the data volume, its own Claude config directory, plugin cache and Go and Node caches.
- **Invariant**: every process that runs model output or repository code runs as the agent user. Every credential with write rights stays in processes of the daemon user. The daemon user never executes git, a hook, a build or a test inside a directory that the agent user can write.

### 4.1 Start of an agent process

`sudo -n -u pilot-agent --preserve-env=<names> -- <command>`. The environment is an allowlist built for the run (PATH, HOME of the agent, locale, cache paths, the Claude credential for the run, `PILOT_EXECUTOR`, the read-only GitHub token). Values travel in the environment, never in argv. The sudoers rule carries `SETENV` so that the named variables pass.

Stop of an agent process: the daemon cannot signal a process of a different user, and it cannot signal `sudo` itself. The kill goes through the same rule: `sudo -n -u pilot-agent kill -s <sig> -- -<pgid>`.

Preflight at daemon start when the option is set: the agent user exists and differs from the daemon user, `sudo -n -u <agent> true` works, the agent home and the workspace root exist with the right owner. A failed preflight is fail-closed: the daemon logs one Error line, raises an alert, and refuses to start agent runs. It must not fall back to runs as the daemon user, because the operator asked for the boundary.

### 4.2 Workspace: an agent-owned clone, not a shared worktree

Today the daemon creates a git worktree under the temp directory; the worktree shares the `.git` directory of the daemon's repository. A shared `.git` that the agent can write defeats the boundary: a hook, `core.fsmonitor`, `core.sshCommand`, a filter or a credential helper in that directory runs as the daemon user the next time the daemon runs git there. Group-write plus `core.sharedRepository` is therefore rejected.

Target:

1. The daemon's repository stays owned by the daemon user and is read-only for the agent user (group read).
2. For each run the agent user creates its own clone under a workspace root that it owns: `git init` and a fetch of the base branch from the daemon's repository, then the task branch. The clone has no alternates file (founder call 2026-11-12): alternates let the agent read every object of the daemon's repository, and a GC there can break a live clone.
3. Every command with the workspace as its directory runs as the agent user through 4.1: git status, the auto-preserve commit, diff counts, Navigator copy, quality gates, evidence commands, workflow hooks.
4. Hand-over: when the session ends and no agent process of the run is alive, the agent user writes a bundle of the task branch. The daemon copies the bundle (regular file only, no followed link) and fetches it inside its own repository into `refs/pilot/handover/<task>`, with object checks on and replace refs ignored. The daemon runs no git command with the workspace as its repository.
5. The secret gate and the push run in the daemon's repository, as today, with the daemon's credential. Hooks are off for that push; the gates already ran in step 3.
6. Cleanup of the workspace runs as the agent user (a read-only module cache or a mode-0555 directory would stop the daemon user).

### 4.3 Files the daemon gives to the agent

The per-run guard directory, the settings file and the `gh` shim directory are created by the daemon with modes 0700 and 0600 today. In the target they are owned by the daemon user, group-readable for the agent group, and not writable by the agent. This is stronger than today, where the session could edit its own guards.

### 4.4 Credentials of the agent

- Claude: the credential for the run, in the environment (an OAuth token or, in API-key billing mode, the key of the execution). The agent needs it by design.
- GitHub: an installation token of the GitHub App, minted for the run with read rights on contents, issues and pull requests of the one repository. The token lives one hour; the `gh` shim and a git credential helper read it from a file in the guard directory that the daemon refreshes. Without a configured App the agent gets no GitHub token (log one Warn); it never gets the daemon's token.
- Result: `git push` and write calls of the agent fail at GitHub. This closes the gap "a push that the agent runs itself bypasses the secret gate" from TASK-650.

### 4.5 Host rules (no daemon code)

- Firewall, owner match on the agent user: reject the metadata address (IPv4 and IPv6) and local port 9091.
- Daemon home and daemon user home stay mode 0700.
- Sudoers: one file for the rule of 4.1; the general rule of the daemon user is removed. Host maintenance uses a root session of the management channel, as it does today.

### 4.6 Secrets out of the config file

Each literal secret in the config becomes a `${VAR}` reference (the loader already expands them and fails on an empty sensitive value). The start script reads each value from SSM SecureString with the instance role and exports it. The values then exist only in the daemon's environment, which the agent user cannot read. A hot restart keeps the old environment, so a rotation needs a full restart; the rotation SOP says so.

## 5. Rollout order and rollback

Each step is useful alone. No step needs a later one.

| Step | What | Check before | Rollback |
|---|---|---|---|
| O1 | Remove the general sudo rule of the daemon user | 2 of 13,213 recordings in 14 days hold a sudo command; no daemon code calls sudo | restore the saved sudoers file from a root session |
| O2 | GitHub App on the box (TASK-461 steps); the personal token leaves the start script | App token mints and refreshes past the one-hour mark on a sandbox repo | put the export line back, restart |
| O3 | Secrets to SSM, `${VAR}` in the config, rotation of the exposed values | config loads with every reference set (dry run of the loader) | keep the old config file as a root-only backup for one week |
| O4 | Create the agent user, home, caches, plugin install, firewall rules, sudoers rule (script from leg U0) | script dry run | script undo mode |
| C1–C4 | Code legs U1–U4, each merged with the option off | unit tests on built commands; no behaviour change with the option empty | option stays empty |
| S1 | Switch one sandbox project (per-project override) | preflight green | clear the override |
| S2 | Switch every project | S1 ran 20 tasks with no new failure class | clear the option |

## 6. What can break, and the guard for each

- **Self-upgrade**: unchanged. The binary directory stays owned by the daemon user; the agent group only reads and executes the binary (the `gh` shim needs it).
- **Path shim**: the agent user needs traverse rights on the symlink chain to the repositories.
- **Cleanup sweeps** at start: they must call the agent-user removal for the workspace root.
- **Process liveness**: `/proc/<pid>/stat`, `status` and `cmdline` stay world-readable; do not mount `/proc` with `hidepid` while the daemon uses them.
- **Caches**: the agent user gets its own Go and Node caches on the data volume (disk use roughly doubles for those caches).
- **Runs longer than one hour**: the read-only token file is refreshed; a `gh` call after the refresh reads the new value.
- **Concurrent runs** share the agent user: one run can read another run's workspace. Accepted for now; a user per project is a later option.
- **Tools that need root inside a task** (package install): they fail after O1. Two recordings in 14 days used sudo.

## 7. Not chosen

- Shared worktree with a common group and `core.sharedRepository`: the daemon would run git in a directory the agent controls (4.2).
- `CAP_SETUID` on the binary: lost on every self-upgrade.
- A setuid-root helper: new privileged code.
- `systemd-run` per session: stronger (cgroups, network rules per unit) but changes process control and start-up; a later option on top of this design.
- A resolver for `ssm:` values inside the config loader: more code at 48 load sites or a new dependency, for no gain over the start script on one host.
- A second stored read-only token: the App mints short-lived ones.

## Refs

- `.agent/tasks/TASK-651-agent-uid-isolation.md` (plan, legs, operator steps)
- `.agent/tasks/TASK-650-secret-exposure-hardening.md` (L1–L6, the held H1 notes)
- `.agent/tasks/TASK-461-app-cutover-sdk-token-wiring.md` (GitHub App)
- `.agent/sops/operations/secret-leak-response.md`
- Memories mem-300, mem-303, mem-305
