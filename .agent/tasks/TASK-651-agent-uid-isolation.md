# TASK-651: Agent isolation — agent sessions under a separate Unix user, read-only GitHub token per run, secrets from SSM

**Status**: 🚀 **U0, U1 and F1 merged; F2 and the second run of B1 dispatched; U2–U5 dispatch-ready (2026-11-13 pm).** F1 [#6344](https://github.com/qf-studio/pilot/issues/6344) → PR#6349 merged 17:09Z after a run-it review with a real sudo (GO-WITH-NOTES: unchanged with the option empty at twelve spawn sites). **The option must stay empty:** two blockers → **F2 [#6354](https://github.com/qf-studio/pilot/issues/6354)**: the kill misses the agent when sudo uses a terminal (`use_pty` and a daemon in tmux — the box case) → sudo in a new session without a terminal, plus a preflight probe; the API backends were refused for bash only → refused at `Execute`, `executeTool` and `NewBackend`. B1 [#6345](https://github.com/qf-studio/pilot/issues/6345): first run parked by the pre-PR review (one blocking defect in undo, eight minor; the fix round had no time left); body amended with the defect list and the hold label cleared 11-13, the run continues on branch `pilot/GH-6345`. **O4 must not run on the box before B1 merges and passes a container review.** Order now: F2 → U2 → U3 → U4 → U5, one at a time. **O1 done 2026-11-12 09:14Z.** Waiting for the founder: O2 (GitHub App) and O3 (secrets to SSM). Design: `.agent/system/agent-isolation-design.md`.
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

- [x] **U0** (PR#6325 merged; fixes in #6328 before any host run) host setup script with dry-run and undo, tested with a mocked PATH (dispatch-ready, below).
- [ ] **F2** ([#6354](https://github.com/qf-studio/pilot/issues/6354), dispatched 11-13) blockers from the run-it review of F1: kill under a sudo terminal, API backends refused as a whole, gh guard journal. Must merge before U2.
- [x] **F1** (PR#6349 merged 11-13) ([#6344](https://github.com/qf-studio/pilot/issues/6344), dispatched 11-13) fixes from the run-it review of U1: cancel through the process group in the constructor, anthropic-api refused, `run_as_group` required, guard files fail closed, name check. Must merge before U2 (same file `agent_command.go`).
- [ ] **B1** ([#6345](https://github.com/qf-studio/pilot/issues/6345), dispatched 11-13) host script, second run; replaces #6328.
- [x] **U1** (PR#6335 + PR#6342 merged 11-12) config option, one spawn constructor for every model process, allowlist environment, kill through sudo, fail-closed preflight, group-readable guard files (dispatch-ready, below).
- [ ] **U2** (dispatch-ready, below; needs U1 on main) workspace as an agent-owned clone under the workspace root (full fetch of the base branch, no alternates), one constructor for every git command in the workspace, cleanup and sweeps as the agent user, Navigator copy without a followed link, preflight for the root, `use_worktree` and the pool.
- [ ] **U3** (dispatch-ready, below; needs U2) hand-over as a bundle that the agent writes and the daemon fetches into `refs/pilot/handover/<task>`; the secret gate and the push run only in the daemon's repository, with replace refs ignored and hooks off; PR creation, recovery ref and the preserve commit follow. Closes the gate-trust class of mem-308 when the option is set (seven shapes; the tamper table is the test list of the leg).
- [ ] **U4** (dispatch-ready, below; needs U2) quality gates, workflow hooks, evidence commands and the lint fix as the agent user with the allowlist environment; `run_as_env` for values such as the Go caches.
- [ ] **U5** (dispatch-ready, below; needs U3 and, on the box, O2) read-only installation token per run in a file for the `gh` shim and a git credential helper (also serves the fetch of a private module); per-project override of the option.
- **Order (founder call 11-12):** U1 → F1 → F2 → U2 → U3 → U4 → U5, one at a time. The poller holds legs that touch the same directory, so a parallel dispatch gains nothing. Check for an open post-merge-review issue on the same files before each dispatch (mem-301).

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

### LEG U2 — repo: qf-studio/pilot — title: feat(executor): run_as_user workspace — an agent-owned clone per run, one constructor for every git command in the workspace, cleanup and sweeps as the agent user (off by default)

probe: present qf-studio/pilot func[[:space:]]agentCommand\(
probe: present qf-studio/pilot RunAsUser[[:space:]]+string
probe: present qf-studio/pilot func[[:space:]]CleanupOrphanedWorktrees\(
probe: present qf-studio/pilot func[[:space:]]NewGitOperations\(
probe: absent  qf-studio/pilot func[[:space:]]createAgentWorkspac[e]\(
probe: absent  qf-studio/pilot RunAsWorkspaceDi[r][[:space:]]+string

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). The option run_as_user and the constructor agentCommand exist (earlier leg). The workspace is still a git worktree that the daemon user owns: CreateWorktreeWithBranch in `internal/executor/worktree.go` creates it under the temp directory and it shares the .git directory of the daemon's repository. An agent that runs as a second user cannot write there. A shared .git that the agent can write would let a hook or a config value run as the daemon user.

GitOperations in `internal/executor/git.go` is a struct with one path; about 70 call sites in `internal/executor` build a git command inline with exec.CommandContext (also `internal/executor/lint.go`, `internal/executor/git_freshness.go`, `internal/executor/simplify.go`, `internal/executor/preflight.go`, `internal/executor/worktree.go`). The cleanup is cleanupWorktreeAndBranch; the sweeps are CleanupOrphanedWorktrees and PruneOrphanedWorktreeForTask in `internal/executor/worktree.go` and findOrphanedWorktreeDirs in `internal/executor/finish_tripwires.go`. The sweeps recognise only a .git FILE with a gitdir line; a clone has a .git DIRECTORY and is skipped without a log line. The Navigator copy is EnsureNavigatorInWorktree (into the workspace) and CopyNavigatorBackFromWorktree (out of it).

This leg changes where the workspace lives and who runs commands in it. With the option empty nothing changes. The push still runs in the workspace after this leg, and the agent user has no credential, so the option must stay empty on every host until the hand-over leg merges; keep the sentence in the config example.

## Scope

1. Config: one string field run_as_workspace_dir next to run_as_user in the ClaudeCodeConfig struct of `internal/executor/backend.go`. With run_as_user set and this field empty the value is /var/lib/pilot/agent-workspaces. Document it in `configs/pilot.example.yaml`.
2. One git constructor. Add a method on GitOperations, gitCmd(ctx, args...), and a free function for the sites that have no GitOperations value. With the option empty, or when the directory is not inside the workspace root, the result is exactly exec.CommandContext(ctx, "git", args...) with the directory set as today. With the option set and the directory inside the workspace root, the command comes from agentCommand with the allowlist environment of the earlier leg. Replace every inline git command in the non-test files of `internal/executor` with the constructor. Add a source test that reads the package files and fails when a non-test file other than the constructor file holds the text exec.CommandContext(ctx, "git" or exec.Command("git".
3. Workspace creation, new file, plain text: internal/executor/agent_workspace.go. A function createAgentWorkspace(ctx, repoPath, taskID, branch, baseRef) that runs every step as the agent user. The directory is a direct child of the workspace root with the name pilot-ws-TASK-NANOSECONDS. Steps: git init; fetch the base ref (and, for a fix continuation, the task branch) from the daemon's repository through the file transport with safe.directory set for that one command only; create the task branch at the same commit that CreateWorktreeWithBranch would use; set the origin URL to the origin URL of the daemon's repository with any user or password part removed. The clone must have no alternates file and no hard link into the daemon's repository, and it must hold only the fetched refs. The daemon still runs fetchOriginBranchForWorktree in its own repository first, as today.
4. Use it: at the place in `internal/executor/runner.go` where CreateWorktreeWithBranch is called for a task run, call createAgentWorkspace when the option is set. Every later step of the run receives the workspace path as it receives the worktree path today.
5. Cleanup: with the option set, the removal of a workspace runs `rm -rf` as the agent user through agentCommand, with a 60 second timeout. Before the call, check that the cleaned path is a direct child of the workspace root, that its name starts with pilot-ws-, and that it holds no symbolic-link component (filepath.EvalSymlinks equals the path). A path that fails the check is not removed; log one Error line. The task branch of the daemon's repository is deleted as today.
6. Sweeps: CleanupOrphanedWorktrees, PruneOrphanedWorktreeForTask and findOrphanedWorktreeDirs also list the direct children of the workspace root with the pilot-ws- prefix when the option is set. They apply to these the same orphan rule that they apply to worktree directories today and remove them through item 5. Log one Info line per removed workspace with the task ID.
7. Navigator copy with the option set. Into the workspace: the copy runs as the agent user (for example `cp -R` through agentCommand); the daemon does not write into the workspace. Out of the workspace: the daemon reads data only. Skip every entry that is not a regular file (os.Lstat, no symbolic link is followed), skip a file over 5 MB with one Warn line, and resolve each destination inside the Navigator directory of the daemon's repository (reject a path that leaves it).
8. Preflight, in the function of the earlier leg: with the option set, fail closed when the workspace root does not exist, when its owner is not the agent user, when use_worktree is false, or when worktree_pool_size is over 0. One Error line names the failed check.
9. Tests, table-driven, no root needed, with the mock sudo script on PATH that the earlier leg uses (the mock runs the command as the present user): the constructor returns the plain command for an empty option and for a directory outside the root, and a sudo command inside it; createAgentWorkspace produces a repository with the task branch at the base commit, no alternates file, and an origin URL without a credential; cleanup refuses a path outside the root, a wrong prefix, and a symbolic link; the sweep removes an orphan clone (.git directory) and keeps the worktree behaviour; the copy out skips a symbolic link that points at a file outside the workspace; each preflight failure of item 8.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'GitCmd|AgentWorkspace|OrphanedWorktree|NavigatorCopy|RunAsUser'` passes; paste the output into the PR body.
- [ ] Add one inline exec.CommandContext(ctx, "git", ...) to a non-test file -> the source test fails.
- [ ] Remove the symbolic-link check of item 5 -> the cleanup test fails.
- [ ] Let the copy out follow a symbolic link -> the copy test fails.
- [ ] With the option empty, the worktree tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The push, the secret gate, the PR creation, the recovery ref and the co-author strip (next leg; they still run in the workspace). Quality gates, workflow hooks, evidence commands and the lint fix. The GitHub token for the agent. The post-merge reviewer checkout. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG U3 — repo: qf-studio/pilot — title: feat(executor): run_as_user hand-over — the daemon takes the branch from the agent workspace as a bundle, runs the secret gate on its own refs and pushes from its own repository with hooks off

probe: present qf-studio/pilot func[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]CheckSecretGate\(
probe: present qf-studio/pilot func[[:space:]]withGitCredentials\(
probe: absent  qf-studio/pilot func[[:space:]]handOverBranc[h]\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2 items 4 and 5). With run_as_user set, the workspace is a clone that the agent user owns (earlier leg). Today the Push, PushForceWithLease and PushToMain methods of GitOperations in `internal/executor/git.go` run three steps inside the workspace: stripCoAuthorBeforePush, CheckSecretGate (`internal/executor/secret_gate.go`, scan in `internal/executor/secretpatterns/pushrange.go`) and git push with the credential environment from withGitCredentials (`internal/executor/git_credentials.go`). The session can write every ref, config value and hook of that repository. A review found seven ways to get a secret past the gate through refs that the session writes (a forged remote-tracking ref, a remote that the session adds, a replace ref). A config value such as a push URL can also send the credential to another host.

The rule of this leg: with the option set, the daemon runs no git command with the workspace as its repository, and no credential enters a command that runs in the workspace.

## Scope

1. Hand-over, new file, plain text: internal/executor/handover.go. A function handOverBranch(ctx, workspace, repoPath, taskID, branch, baseRef) returns the commit SHA.
   a. The agent user (through the git constructor) writes a bundle of the branch, without the commits of the base ref, to a file next to the workspace directory inside the workspace root, and prints the head SHA.
   b. The daemon copies the bundle to a file in its own temp directory. It opens the source with O_NOFOLLOW, accepts a regular file only, and stops with an error over 2 GB.
   c. Inside the daemon's repository the daemon fetches the branch from its copy into the ref refs/pilot/handover/TASK. The fetch sets fetch.fsckObjects to true, sets core.hooksPath to an empty directory, and has GIT_NO_REPLACE_OBJECTS=1 in the environment.
   d. The SHA of the fetched ref must equal the SHA of step a. A difference is an error; nothing is pushed.
   e. Remove both bundle files in every outcome.
2. Gate: with the option set, CheckSecretGate runs in the daemon's repository on the hand-over ref. Before the scan the daemon runs git fetch origin for the base branch and the task branch in its own repository. "Already published" comes only from refs/remotes/origin of the daemon's repository. Every git command of the gate has GIT_NO_REPLACE_OBJECTS=1 and core.hooksPath set to an empty directory.
3. Push: with the option set, Push, PushForceWithLease and PushToMain push the hand-over ref from the daemon's repository to the branch name on origin. The command has core.hooksPath set to an empty directory, takes the origin URL from the daemon's repository, and gets the credential from withGitCredentials as today. The lease variant passes the expected remote SHA explicitly. Keep the three public method signatures, so that the call sites in `internal/executor/runner.go`, `internal/executor/runner_decompose.go` and `internal/executor/git_freshness.go` do not change; GitOperations gets the path of the daemon's repository as a second field when it is built for an agent workspace.
4. Co-author strip: with the option set it runs in the workspace as the agent user before the hand-over (it rewrites the agent's own commits; it is not a security control). After the hand-over the daemon checks the commit messages of the pushed range in its own repository; a trailer that is still present stops the push with the present error.
5. PR creation by gh in CreatePR: with the option set, run gh with the directory of the daemon's repository and pass the repository, the head branch and the base branch as explicit flags. No gh command of the daemon has the workspace as its directory.
6. Recovery and preserve: CreateRecoveryRef with the option set creates the ref refs/pilot-recovery/TASK in the daemon's repository from the handed-over SHA. preserveDirtyWorktreeAsWIP commits in the workspace as the agent user, then uses the hand-over and the daemon push. A workspace is removed only after a successful hand-over or after an Error line that names the task and the reason.
7. Delete the hand-over ref after a successful push and in the cleanup of the run.
8. Tests with real temporary repositories (a bare origin, a daemon clone, an agent workspace; the fixtures newRemoteAndLocalRepo and newPushRefusedFixture show the pattern) and the mock sudo script. Use the fake secret shape that the present gate tests use. Each of these cases must end with a blocked push and no new commit on the bare origin when the option is set: a secret in a new commit; the same with a forged ref under refs/remotes/origin in the workspace; with a remote that the workspace adds; with a replace ref in the workspace; with a shallow file in the workspace; with a pushurl and an insteadOf value in the workspace config; with a commit in the encoding IBM037; with a pre-push hook in the workspace; with a submodule and push.recurseSubmodules; with a gpg.program and log.showSignature; with a textconv driver in the workspace config; with log.showRoot false and an orphan root commit; with a file in the .git directory of the workspace that shadows the branch name; with push.followTags true and an annotated tag (the tag must be absent on the origin). One more case: a pre-push hook and a core.sshCommand value in the workspace do not run (they would create a marker file; the file must not exist). A clean branch is pushed, and the origin head equals the workspace head. A bundle path that is a symbolic link is refused.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'HandOver|SecretGate|PushRefused|RecoveryRef|RunAsUser'` passes; paste the output into the PR body.
- [ ] Compute "already published" in the workspace again -> the forged-ref test fails.
- [ ] Remove GIT_NO_REPLACE_OBJECTS=1 from the gate -> the replace-ref test fails.
- [ ] Push from the workspace directory -> the pushurl test and the hook test fail.
- [ ] Remove the SHA comparison of item 1d -> its test fails.
- [ ] With the option empty, the push and gate tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The behaviour with the option empty (the gate still runs in the worktree there; known limit, recorded in Navigator). Read commands after the session (diff, counts, changed files): they stay in the workspace through the git constructor and return data only. Quality gates, workflow hooks and evidence commands. The GitHub token for the agent. The post-merge reviewer checkout.

## Refs

- Navigator TASK-651 in this repo · TASK-650 (parent) · design section 4.2

### LEG U4 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates — quality gates, workflow hooks, evidence commands and the lint fix run as the agent user with an allowlist environment

probe: present qf-studio/pilot func[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]sanitizedAcceptanceEnv\(
probe: present qf-studio/pilot func[[:space:]]runScript\(
probe: absent  qf-studio/pilot RunAsEn[v][[:space:]]+map

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2 item 3). With run_as_user set, the model process and git in the workspace run as the agent user (earlier legs). Four groups of commands still run repository content as the daemon user, with the full daemon environment:

- Quality gates: executeCommand in `internal/quality/runner.go` runs `sh -c` with os.Environ() plus the scope variables; `internal/quality/scope.go` runs `go list` and git in the workspace.
- Workflow hooks: runScript in `internal/executor/workflow/hooks.go` runs `bash -lc` for after_create, before_run, after_run and before_remove; the callers in `internal/executor/runner.go` pass an environment built from os.Environ().
- Evidence commands: `internal/executor/acceptance_evidence_run.go` runs allowlisted commands with sanitizedAcceptanceEnv.
- Lint fix: autoFixLint in `internal/executor/lint.go` runs golangci-lint with --fix, then stages and amends.

A test file or a Makefile target that the agent wrote can read every secret of the daemon from that environment. This leg moves the four groups to the agent user. With the option empty nothing changes.

## Scope

1. A command builder that the quality and workflow packages can use without an import of the executor package: each package gets an exported builder variable or a field with the signature func(ctx, dir, name string, args ...string) *exec.Cmd and an environment function. The default is the present behaviour. The executor sets both at start when the option is set, from agentCommand and the allowlist environment.
2. Quality gates: executeCommand and the two commands of `internal/quality/scope.go` use the builder. With the option set the environment is the allowlist plus the scope variables; os.Environ() is not used. The kill of the gate process group on a timeout uses the sudo kill of the earlier leg when the option is set.
3. Workflow hooks: runScript uses the builder. With the option set the environment is the allowlist plus the hook variables that the callers add today. All four hooks run as the agent user; before_remove runs before the workspace cleanup.
4. Evidence commands use agentCommand; the environment is the intersection of sanitizedAcceptanceEnv and the allowlist.
5. Lint fix: both golangci-lint commands use agentCommand; confirm that the stage and amend commands go through the git constructor.
6. Config: a map field run_as_env (name to value) next to run_as_user in `internal/executor/backend.go`, for values that are not secret and that agent processes need, added last to the allowlist environment. Document it in `configs/pilot.example.yaml` with GOCACHE and GOMODCACHE as the example. A name that holds TOKEN, SECRET, KEY or PASSWORD (any case) fails the preflight with one Error line.
7. Tests with the mock sudo script: for each of the four groups, with the option set the first argv element is sudo, and a variable DAEMON_ONLY_SECRET set in the test process is absent from the environment of the command (the command prints its environment into a file); with the option empty the argv and the environment equal the present ones. A gate that sleeps past its timeout is killed through the sudo kill. The preflight rejects a run_as_env name with TOKEN.

## Acceptance

- [ ] `go test -race -count=1 ./internal/quality/ ./internal/executor/... -run 'RunAsUser|GateEnv|HookEnv|EvidenceEnv|LintFix'` passes; paste the output into the PR body.
- [ ] Use os.Environ() for a gate with the option set -> the gate environment test fails.
- [ ] Build one hook with exec.CommandContext directly -> the hook test fails.
- [ ] With the option empty, the gate and hook tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The GitHub token for the agent (a gate that fetches a private module has no credential until the next leg). The push and the hand-over. The post-merge reviewer. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG U5 — repo: qf-studio/pilot — title: feat(executor): run_as_user GitHub access — a read-only installation token per run in a file for the gh shim and git, and a per-project override of the option

probe: present qf-studio/pilot func[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]handOverBranch\(
probe: present qf-studio/pilot func[[:space:]]setupGhGuardShim\(
probe: absent  qf-studio/pilot func[[:space:]]mintAgentReadToke[n]\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.4). With run_as_user set, the agent environment holds no GitHub name (allowlist of the earlier leg), so the agent cannot read issues, fetch from origin or fetch a private module. The daemon's token has write rights and must never reach the agent. The GitHub App token source is TokenSource in `internal/adapters/github/apptoken.go`; the gh shim directory comes from setupGhGuardShim in `internal/executor/ghguard_spawn.go`; the per-run guard directory comes from setupRunGuards in `internal/executor/read_guard.go`. Projects are ProjectConfig in `internal/config/config.go`.

## Scope

1. Token: a function mintAgentReadToken(ctx, owner, repo) asks GitHub for an installation token limited to the one repository with the permissions contents read, issues read, pull_requests read and metadata read. Add the request with a repository list and a permission map to `internal/adapters/github/apptoken.go` when it does not exist. Without a configured App the function returns an empty token; log one Warn line per daemon start, and the run goes on without a token. The daemon's own token is never used as a substitute.
2. Token file: with the option set and a token present, write it to a file with the name github-token in the per-run guard directory: owner the daemon user, group the agent group, mode 0640, written through a temporary file and a rename. Refresh it 15 minutes before it expires while the run is alive. Remove it at the end of the run. The token value is registered for redaction as the other secret values are.
3. gh: the shim script reads the file at each call and sets GH_TOKEN for the real gh. With no file it sets nothing.
4. git and module fetch: a credential helper script in the guard directory prints the user name x-access-token and the token from the file. The agent environment gets the git config entries through GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n, so that the helper applies to github.com over https only. Add these three name patterns to the allowlist. No token value is in an environment variable or in argv.
5. Per-project override: an optional field run_as_user in ProjectConfig. Absent means the global value. A value sets the user for runs of that project. The literal value "off" switches the option off for that project. The preflight checks every distinct user. Every decision "option set or not" that the earlier legs made from the global value now takes the project of the run. Document the field in `configs/pilot.example.yaml`.
6. Tests: the permission map and the repository list of the request (HTTP test server); no configured App gives an empty token and the daemon's token is absent from the agent environment and from the guard directory; file mode and rename; the refresh replaces the content; the shim sets GH_TOKEN from the file (run the script with a mock gh that prints its environment); the helper output; the config entries carry no token value; the override table (absent, a user, "off") for a spawn, a git command and a gate.

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/github/ ./internal/executor/... ./internal/config/ -run 'AgentReadToken|GhGuard|CredentialHelper|RunAsUser'` passes; paste the output into the PR body.
- [ ] Ask for contents write -> the permission test fails.
- [ ] Fall back to the daemon's token without an App -> its test fails.
- [ ] Put the token into the agent environment -> the config entries test fails.
- [ ] Ignore the project value "off" -> the override test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The creation of the GitHub App and its key (operator step). A token for other hosts (GitLab, Azure DevOps). A module proxy. Any change of behaviour with the option empty and no project override.

## Refs

- Navigator TASK-651 in this repo · TASK-461 · design section 4.4

### LEG F1 — repo: qf-studio/pilot — title: fix(executor): run_as_user — a context timeout stops the agent process at every spawn site, the anthropic-api backend is refused, run_as_group is required, the gh shim fails closed

probe: present qf-studio/pilot func[[:space:]]agentCommand\(
probe: present qf-studio/pilot func[[:space:]]killAgentProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]PreflightRunAsUser\(
probe: present qf-studio/pilot func[[:space:]]execBash\(
probe: absent  qf-studio/pilot func[[:space:]]validRunAsNam[e]\(

## Context

Navigator TASK-651. The option run_as_user starts every model process as a second Unix user through sudo; the constructor is agentCommand in `internal/executor/agent_command.go`, the preflight is PreflightRunAsUser in the same file (called once from `cmd/pilot/main.go`), and the kill through sudo is killAgentProcessGroup. The option is empty on every host. A run-it review with a real sudo found three defects that must be fixed before the option is set anywhere, and some minor ones. With the option empty nothing may change.

1. A context timeout does not stop the model. Three sites set a process group and a cancel function that kills the group (`internal/executor/backend_claudecode.go`, `internal/executor/backend_qwencode.go`, `internal/executor/parallel.go`). Seven sites keep the default cancel of os/exec, which signals only the sudo process: `internal/executor/epic.go`, `internal/executor/subtask_parser.go`, `internal/executor/effort_classifier.go`, `internal/executor/complexity_classifier.go`, runCmdInHome in `internal/executor/intent_judge.go` (also used by the post-merge reviewer), and the summary and evidence calls in `internal/executor/runner.go`. In the review a command with a 1 second timeout returned after 12 seconds and the agent process stayed alive. The stop of the server in `internal/executor/backend_opencode.go` calls Process.Kill on sudo and has the same defect.
2. The anthropic-api backend runs commands that the model chose with bash in the daemon process (execBash in `internal/executor/backend_anthropic.go`), as the daemon user with the full environment. Nothing refuses that backend when the option is set.
3. Without run_as_group the guard files stay in the group of the daemon user and the agent user cannot read the settings file. A failed change of owner of the gh shim directory only logs a warning in `internal/executor/backend_claudecode.go`, and the run goes on without the gh guard.

## Scope

1. Cancel in the constructor. With the option set, agentCommand itself sets the process group (configureProcessGroup), sets cmd.Cancel to the kill of the group through killAgentProcessGroup with SIGKILL, and sets cmd.WaitDelay to 10 seconds, so that every site that uses the constructor is covered. A site that sets its own cancel after the call keeps working. With the option empty the constructor still returns exactly exec.CommandContext(ctx, name, args...) with no field changed. The stop of the opencode server uses the same kill of the group when the option is set.
2. Backend check in the preflight: with the option set, a backend type that runs model-chosen commands inside the daemon process fails the preflight with the line "run_as_user is not supported with the <type> backend". This is anthropic-api for certain; read `internal/executor/backend_openai.go` and the backend list in `internal/executor/backend_factory.go`, and add every other backend that executes a tool call in-process. Give PreflightRunAsUser the backend type (a new argument or a second function called at the same place). As a second barrier, execBash returns an error and runs nothing when the option is set.
3. run_as_group: with run_as_user set and run_as_group empty, the preflight fails with "run_as_group is required with run_as_user". The preflight also checks that the group exists and that the daemon user is a member of it.
4. Guard files fail closed: with the option set, a failed change of group or mode of the guard directory, of the settings file or of the gh shim directory is an error that stops the spawn ("run_as_user: guard files not readable by the agent: <path>"). With the option empty the present warning stays.
5. Name check: a function validRunAsName accepts only a name that starts with a letter a to z or an underscore and continues with a to z, 0 to 9, underscore or dash, at most 32 characters. The preflight applies it to the user and the group before any lookup, and fails for the name root, for a user with uid 0 and for a group with gid 0.
6. Minors from the same review: an error from currentUID fails the daemon-user check (today it passes); setAgentEnv and agentCommand use one snapshot of the option values for one command, so that a config reload between them cannot give a preserve list that differs from cmd.Env; a failed kill logs one Error line, not two; the four tests EverySpawnSiteGoesThroughSudo, PreserveListEqualsEnvNames, KillProcessGroupGoesThroughSudo and KillFailureLogsOneErrorWithPgid stub the platform (runAsGOOS) so that they pass on macOS.
7. Tests, table-driven, with the mock sudo script on PATH that the present tests use; the mock must start the command in the background and survive a signal to itself, so that it shows the defect of Context item 1. One test per spawn site of that item: a context with a 1 second timeout and a command that sleeps 30 seconds returns in under 5 seconds, and the argv of the recorded kill holds the negative process group id. Preflight tests for items 2, 3 and 5 (values root, "-u root", "a b", "#0", a 33 character name, a missing group, a group without the daemon user). A spawn with a guard directory that cannot be changed returns the error of item 4 and starts nothing. With the option empty: the constructor result has no Cancel, no WaitDelay and no SysProcAttr set by it, and execBash runs as today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentCommand|RunAsUser|ModelEnv|ReadGuard|GhGuard'` passes on Linux and on macOS; paste the output into the PR body.
- [ ] Remove the cancel from the constructor -> the timeout test of every site of Context item 1 fails.
- [ ] Remove the backend check -> its preflight test fails, and the execBash test still fails closed.
- [ ] Let an empty run_as_group pass -> its test fails.
- [ ] Turn the guard file error back into a warning -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The workspace, git commands, quality gates, hooks and the GitHub token (later legs). The rule that names with the prefix CLAUDE_ or ANTHROPIC_ pass to the agent. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review of PR #6335 and PR #6342

### LEG F2 — repo: qf-studio/pilot — title: fix(executor): run_as_user — the kill reaches the agent when sudo uses a terminal, API backends are refused as a whole, the gh guard journal is writable for the agent

probe: present qf-studio/pilot func[[:space:]]killAgentProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]sudoProbeOK\(
probe: present qf-studio/pilot func[[:space:]]RunAsUserPreflightError\(
probe: absent  qf-studio/pilot func[[:space:]]runAsBackendErro[r]\(
probe: absent  qf-studio/pilot Setsi[d]:[[:space:]]+true

## Context

Navigator TASK-651. The option run_as_user starts every model process as a second Unix user: agentCommand in `internal/executor/agent_command.go` builds a sudo command, sets a process group (Setpgid) and sets a cancel that calls killAgentProcessGroup; that function runs kill as the agent user on the negative pid of the sudo process. The option is empty on every host. A run-it review with a real sudo found two defects that must be fixed before the option is set anywhere. With the option empty nothing may change.

1. The kill misses the agent when sudo uses a terminal. With `Defaults use_pty` in sudoers (the default of several distributions) and a daemon that has a controlling terminal (a daemon in tmux), sudo runs the command in a new session: the tree is sudo (group P), a second sudo (session S), the agent (group A). The kill of group P fails with exit 1. Sites with a context return only after the wait delay; kills without a context (the watchdog paths in `internal/executor/backend_claudecode.go`, `internal/executor/backend_qwencode.go`, `internal/executor/parallel.go` and `internal/executor/runner.go`, and StopServer in `internal/executor/backend_opencode.go`) leave the agent processes alive. In the same review, a sudo with use_pty and NO terminal on the daemon side used no pty, and the kill worked.
2. The API backends are refused for bash only. The preflight refuses the types anthropic-api and openai-api, but that refusal only stops agentCommand. Execute in `internal/executor/backend_anthropic.go` and in `internal/executor/backend_openai.go` does not read it, and executeTool still runs read_file, write_file and edit_file as the daemon user. The preflight log line "model processes will not start" is false for these backends.

Three more points from the same review: three sites call configureProcessGroup again after agentCommand (`internal/executor/backend_claudecode.go`, `internal/executor/backend_qwencode.go`, `internal/executor/parallel.go`), which replaces the SysProcAttr of the constructor and sets a wait delay of 5 seconds; a run_as_user value of spaces only counts as empty, so the model runs as the daemon user without a message; the agent cannot create the journal file of the gh guard, because setupGhGuardShim in `internal/executor/ghguard_spawn.go` makes the directory 0750 and the journal write in `internal/executor/ghguard/policy.go` creates the file itself, so denials are not recorded.

## Scope

1. No terminal for sudo. With the option set, agentCommand starts sudo in a new session (SysProcAttr with Setsid true, and without Setpgid, because both together fail) and with standard input from the null device when the caller sets none. Then sudo has no controlling terminal, pid, group and session of the sudo process are equal, and the kill of the negative pid reaches the agent. With the option empty the constructor result is unchanged.
2. The three sites that call configureProcessGroup after agentCommand must not replace the SysProcAttr or shorten the wait delay when the option is set. Give them one helper, or let configureProcessGroup keep a session that is already set. With the option empty these sites behave as today.
3. Preflight probe: sudoProbeOK runs its probe with the same SysProcAttr and standard input as item 1. Add a second probe that runs, as the agent user through the same sudo argv, a command that prints whether it has a controlling terminal (for example `sh -c 'ps -o tty= -p $$'`), and expect none. When the agent process has a terminal, the preflight fails closed with "run_as_user: sudo gives the agent a terminal (use_pty); the kill would not reach it".
4. Kill result: when killAgentProcessGroup gets a non-zero exit from the kill and the process is still alive after 2 seconds, log one Error line with the pid and return the error to the caller; callers without a context must not report a successful stop.
5. API backends: a function runAsBackendError returns an error when the option is set and the backend type runs tools inside the daemon process ("run_as_user is not supported with the <type> backend"). Call it as the first statement of Execute in both API backends and at the start of executeTool, so that no tool runs, and in NewBackend in `internal/executor/backend_factory.go`, so that the daemon fails at start with that message. Correct the preflight log line so that it is true for these backends.
6. A run_as_user or run_as_group value that is not empty and holds only white space fails the preflight ("run_as_user is blank"); it does not count as empty.
7. gh guard journal: with the option set, setupGhGuardShim creates the journal file itself, empty, with mode 0660 and the agent group. The directory stays 0750 and owned by the daemon user. With the option empty nothing changes.
8. Tests, table-driven, with the mock sudo script on PATH that the present tests use: with the option set, the command of every spawn site has Setsid true, Setpgid false and a standard input; the three sites of item 2 keep them and a wait delay of 10 seconds; with the option empty the SysProcAttr, standard input, cancel and wait delay of every site equal the present ones; the terminal probe fails the preflight when the mock prints a terminal name and passes for "?"; a failed kill with a live process returns an error and logs one Error line; Execute, executeTool (each of the four tools) and NewBackend refuse both API backend types with the option set and work with it empty; a blank value fails the preflight; the journal file exists with mode 0660 and the directory keeps 0750.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... ./cmd/pilot/ -run 'AgentCommand|RunAsUser|ProcessGroup|GhGuard|Backend'` passes on Linux and on macOS; paste the output into the PR body.
- [ ] Set Setpgid in place of Setsid in the constructor -> the session test fails.
- [ ] Let one of the three sites replace the SysProcAttr -> its test fails.
- [ ] Remove the check from executeTool -> the tool test fails for read_file, write_file and edit_file.
- [ ] Let a blank value count as empty -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The workspace, git commands, quality gates and the GitHub token (later legs). A kill by user name. A wrapper process that records the group of the agent. The heartbeat and the resource limits that read the pid of the sudo process. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review of PR #6349

### LEG B1 — repo: qf-studio/pilot — title: fix(box): agent user setup script, second run — argument validation, shared read on git directories only with a complete undo, proven sudo removal as the last step, firewall without a gap

probe: present qf-studio/pilot restore_cloud_init
probe: present qf-studio/pilot shared_read_ok
probe: absent  qf-studio/pilot --confirm-root-pat[h]\)
probe: absent  qf-studio/pilot --repos-lin[k]\)

## Context

Navigator TASK-651, leg U0 follow-up. The host setup script for the agent user is `scripts/box/setup-agent-user.sh`; its test is `scripts/box/setup-agent-user_test.sh` with mocks in `scripts/box/testdata/mock-host-cmds.sh`. A run of the script in an Amazon Linux 2023 container with systemd showed: with default arguments it is correct and idempotent; three major defects and several minor ones must be fixed before an operator runs it on a host.

This issue replaces #6328. The first run of #6328 was stopped by the pre-PR review; its work is on the branch pilot/GH-6328-run1 for reference only. Do not continue that branch: it conflicts with main in 14 places. Start from main. Since then main got these parts (do not redo them, keep their tests green): the helper shared_read_ok for the idempotence check of the shared read, the guarded removal of the temp sudoers file in dry-run mode, a fresh backup of the cloud-init file with restore_cloud_init, and the firewall match on local addresses.

## Scope

1. Argument validation, before any change:
   - --data-dir: canonicalise with realpath; it must exist, be a directory owned by the daemon user, and not be a system directory or an ancestor of one (/, /usr, /etc, /var, /var/lib, /home, /bin and so on); reject a value with a `..` part or a leading dash. With --data-dir /usr the script changed the group of /usr/bin and removed the setuid bit of sudo.
   - The same containment check applies to every path that the script changes after it resolves a symbolic link: the repos directory, the bin directory and the binary. A resolved path outside the canonical data dir is refused before any change.
   - --agent-user and --daemon-user: reject root and any user with uid below 1000 for the daemon user; the agent user must not exist with uid 0, must differ from the daemon user, and, when it exists already, must have a no-login shell and must not be a member of wheel or sudo.
   - --group: reject root, wheel, sudo, adm and the primary group of the daemon user.
   - All names: only the characters a to z, 0 to 9, underscore and dash, starting with a letter.
2. Shared read without exposure of private files:
   - The bin directory and the binary get group read and execute. Under repos only the top directory, each directory on the way to a repository (a repository can be one or two levels down, for example repos/org/repo: the middle directory gets group traverse only, no read of other entries beyond what it had), and, for each repository, its git directory (objects, refs, packed-refs, HEAD, config) get the group and group read. Never the working tree.
   - Never add a permission bit to a file or directory that has no permission for "other" unless it is inside a git directory, or is one of the directories named above, where the only added bits are group traverse (and group read for the repos top directory and the bin directory).
   - Record every path whose group or mode the script changes, with its earlier group and mode, in a state file under the data dir BEFORE the change. --undo restores exactly those, the binary included, and prints each restored path.
   - Use chgrp -h and chmod only on paths that are not symbolic links at the moment of the change (test with -L directly before each change).
3. Path chain: when the repos directory is reached through a symlink chain whose directories the agent user cannot traverse, print one line per directory with the exact command to grant traverse (a setfacl or chmod suggestion) and count the step as failed. Check every directory of the chain and the final target directory. Add an argument --repos-link <path> (repeatable) for such chains.
4. The cloud-init step must prove its result:
   - Comment out only lines whose user list is exactly the daemon user AND whose run-as part is ALL or root or is absent. Leave lines with several users, aliases, Defaults lines and lines that run as another user only (for example a rule with the run-as part "(deploy)") untouched, and report them.
   - After the edit, run sudo -n -l -U for the daemon user. When that command itself fails, the step fails with its output. When the output still holds a NOPASSWD rule that is not the agent rule, the step fails and prints the file and line of the remaining rule.
5. Backups of the cloud-init file: keep the older backups with a numbered suffix; --undo restores the newest one and says which.
6. Firewall: the idempotence check must verify that the jump to the chain is the first rule of OUTPUT, and move it there when it is not. Match the gateway port on the loopback interface for any local address (IPv4 and IPv6). When ip6tables is missing, apply the IPv4 rules, install the boot unit for IPv4 only, and report the IPv6 part as skipped, not failed. A rebuild of the chain must leave no moment without the reject rules: build a new chain with a temporary name, switch the jump in OUTPUT to it, then remove the old chain (or use one iptables-restore call with --noflush).
7. Order and safety: do the removal of the daemon user's general sudo LAST, after the firewall and the boot unit. Before that removal, require the argument --confirm-root-path, and refuse without it with a message that the operator must hold another root session.
8. --dry-run must not delete or change anything.
9. --undo stops and disables the boot unit, and prints the group and mode changes it restored from the state file of item 2.
10. Tests: make the mocked test run on macOS too (no GNU-only flags of find and stat), and add cases for items 1 to 9, among them: a repository two levels down with a middle directory of mode 0700; a binary that is a symbolic link to a path outside the data dir (refused); a rule line with the run-as part "(deploy)" (untouched and reported); a mocked sudo -l that exits non-zero (step failed); a file with mode 0600 in a working tree (unchanged after apply). Add one container test script, new file, plain text: scripts/box/setup-agent-user_container_test.sh, with the execute bit set (git mode 100755), which runs apply, a second apply, undo and three invalid-argument cases for real in an amazonlinux:2023 container when docker is available and exits 0 with a "skipped" line when it is not. In the container it also checks the real output of sudo -l -U and of iptables -S against what the script expects. Do not add it to CI.

## Acceptance

- [ ] The output of the mocked test (all cases pass) and of shellcheck for the three scripts (no finding) is in the PR body.
- [ ] Remove the system-directory check of item 1 -> its test fails.
- [ ] Make the repos step recursive over the working tree again -> the private-file test fails.
- [ ] Remove the containment check for the resolved binary path -> its test fails.
- [ ] Swallow the error of sudo -l again -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

SELinux contexts. A call of the script from Go code. A run on a real host. A race with a process that changes the same paths during the run, beyond the link test of item 2.

## Refs

- #6328 (first run, closed) · #6320 · PR #6325 · PR #6338 · Navigator TASK-651 in this repo

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
| Workspace | shared worktree with group write; agent-owned clone (`--shared`, `--reference`, full) | agent-owned full clone of the base branch, no alternates (founder call 11-12) | a shared `.git` lets a hook or config value run as the daemon user; alternates let the agent read every object and let a GC of the daemon's repository break a live clone |
| Hand-over transport | daemon runs `git fetch <workspace>`; bundle written by the agent | bundle, copied by the daemon, then fetched in the daemon's repository | the daemon runs no git command with an agent-owned repository; no ownership exception is needed |
| Daemon push hooks | keep pre-push; hooks off | hooks off (founder call 11-12) | no repository script runs with the write credential; the Go secret gate still runs before every push |
| Private module fetch in gates | no credential; read-only token | the read-only token of the run (founder call 11-12) | gates keep working; a leak gives read access for one hour at most |
| Leg order | gate fix before U2; after U2 | U3 after U2 (founder call 11-12) | one design; #6318 already adds the replace-ref ignore on main |
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

**Last Updated**: 2026-11-13

## Refs

- U0 → https://github.com/qf-studio/pilot/issues/6320
- U1 → https://github.com/qf-studio/pilot/issues/6321
- F1 → https://github.com/qf-studio/pilot/issues/6344
- B1 → https://github.com/qf-studio/pilot/issues/6345
- F2 → https://github.com/qf-studio/pilot/issues/6354
