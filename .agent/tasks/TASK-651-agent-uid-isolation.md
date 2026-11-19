# TASK-651: Agent isolation — agent sessions under a separate Unix user, read-only GitHub token per run, secrets from SSM

**Status**: 🚀 **State 2026-11-19 08:50Z. With Pilot: H7 → [pilot#6501](https://github.com/qf-studio/pilot/issues/6501) (push fixes; fifth run), H14 → [pilot#6514](https://github.com/qf-studio/pilot/issues/6514) (hand-over: no lazy fetch, object size limit), B12 → [pilot#6515](https://github.com/qf-studio/pilot/issues/6515) (host script notes), P7 → [pilot#6516](https://github.com/qf-studio/pilot/issues/6516) (firewall probe notes); all `pilot` + `no-decompose`. Parked: H13 → [pilot#6503](https://github.com/qf-studio/pilot/issues/6503) (secret gate: quoted file paths), `pilot-blocked` + `pilot-needs-human`, no PR.** **Box finding 11-19 (mem-326):** the test gate of the box has a time limit of 10 minutes and runs almost the full Go suite for a change in `internal/executor`; the suite needs about 10 minutes there, so the gate is killed (`make test-changed exited with code -1`; no test fails). H9 passed on its third gate run; H7 and H13 failed three runs each. The box holds 133 orphan process trees of old agent runs (load average 17 to 21 on 4 CPUs). Two box steps wait for the founder: the stop of the orphan processes (script ready, dry run passed) and a gate limit of 20 minutes in the box config. H13 is re-armed after one of them. Merged on 11-17 and 11-18 (the checklist has each PR): H1 to H5, H8 (the hand-over transport is a plain pack since PR#6497; founder call 11-18, way A), H9 (environment filter of the daemon-side git, PR#6511, 20:12Z; run-it review 11-19: no `GIT_` name reaches git, but each other name does, tokens too → H15), H10, H11 (workspace clean-up rule, PR#6506; run-it review 11-19: no blocker, the notes go to H12), P2, P4, P5, P6, B8 to B11 (all host script legs), N7. Earlier: U0, U1, F1, F2, F3, B1 to B7, G1, U2, N2 to N6, P1, P3. Written, after H14 (same file): H15 (the daemon-side git gets only named variables, no global and no system git config). To write: H12 after H7 (gh commands: host of the remote, epic calls, FIFO reads, life of the hand-over ref, the notes of the PR#6506 review), then U4 in three parts (the last one removes the preflight refusal) and U5 in two parts; both need a new specification, see the spec check line of 11-17. **Must merge before the refusal is removed:** H14, H15, H7, H12, P7, U4. `run_as_user` is refused by the preflight on every host until then. **O4 (host script on the box):** all host script legs are merged and reviewed, no blocker. The run does not go through SSM by hand: by the rule of the infrastructure owner (11-17) it goes into the infra repository as a PR (the script under `scripts/founder-box/`, the removal of the daemon user from the group wheel as its first step, a workflow on demand, dry run first, then the run in a window with no active task); answer of the infrastructure owner 11-18: nothing uses sudo as the daemon user, a reboot does not restore the group, a new image does (override planned in the box image). **Merge rule (founder, 11-17, mem-319):** no draft hold. The review of Pilot and green CI are sufficient for the merge; the daemon merges. The run-it review runs after the merge and its findings become follow-up legs. The founder is asked only for work that leaves the plan and for a step on the box. Proposed and not yet confirmed: a hold for the one PR that removes the preflight refusal. Legs stay small: the task limit of Pilot is one hour. Work rules from 11-18: after a retry of a parked ticket, compare the commit of the PR with the commit of the hold comment; before a dispatch, wait for an open daemon-filed ticket on the same files. **O1 done 2026-11-12.** Waiting for the founder: the two box steps above, O2 (GitHub App), O3 (secrets to SSM), the infra repository PR for the host script, three decisions for U4 and U5 (override for each project; `run_as_env`; the Go proxy variables). Design: `.agent/system/agent-isolation-design.md`.
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
- **Box facts for O4, read 11-18 10:50Z through SSM (read-only, founder go):** sudo 1.9.15p5 (the version of CI; the rule list has the entry format with a path); Amazon Linux 2023, bash 5.2.15; each tool of the script is present; no agent user and no shared group yet. `sudo -ll -U` for the daemon user shows ONE entry, from `/etc/sudoers`, run-as users ALL, commands ALL: the daemon user is in the group wheel (also adm, systemd-journal, docker). The cloud-init file gives no active rule (O1). **So the apply stops at the check of the sudo rules with nothing changed, until the daemon user leaves the group wheel (or the wheel rule goes).** The docker group stays a path to root and needs `--confirm-root-path`: the infrastructure owner answered on 11-17 that the box needs Docker for the daemon user, and the group stays as a known risk until rootless Docker. **Rule of the infrastructure owner (11-17, accepted by the founder): no change of the box by hand while Pilot runs on it; the box configuration goes into the infra repository, with a review, applied in a window with no active task. So O4 is not a hand run through SSM: the script run goes through that path.** Asked on 11-18 12:30Z (no answer yet): does something use sudo as the daemon user or need the group wheel; does cloud-init or an image rebuild restore the group or the cloud-init line; in which form the infra repository takes the script run. All host script legs are merged (last: B11, run-it review with no blocker).
- [ ] **O4** Run the U0 script: agent user, group, home, caches, Claude plugin install for the agent user, firewall rules, sudoers rule. Inert until the option is set.

### Phase C — code legs (Pilot), each merged with the option empty

- [x] **U0** (PR#6325 merged; fixes in #6328 before any host run) host setup script with dry-run and undo, tested with a mocked PATH (dispatch-ready, below).
- [x] **F3** (PR#6369 merged 11-15 13:36Z) ([#6366](https://github.com/qf-studio/pilot/issues/6366), dispatched 11-15) test fix: the opencode spawn-site subtest waits for its server command; the kill-failure test passes on macOS.
- [x] **F2** (PR#6361 merged 11-15 12:59Z) ([#6354](https://github.com/qf-studio/pilot/issues/6354), dispatched 11-13) blockers from the run-it review of F1: kill under a sudo terminal, API backends refused as a whole, gh guard journal. Must merge before U2.
- [x] **F1** (PR#6349 merged 11-13) ([#6344](https://github.com/qf-studio/pilot/issues/6344), dispatched 11-13) fixes from the run-it review of U1: cancel through the process group in the constructor, anthropic-api refused, `run_as_group` required, guard files fail closed, name check. Must merge before U2 (same file `agent_command.go`).
- [x] **B1** (PR#6359 merged 11-14; **no host run yet**, see status) ([#6345](https://github.com/qf-studio/pilot/issues/6345), dispatched 11-13) host script, second run; replaces #6328.
- [ ] **B3** ([#6389](https://github.com/qf-studio/pilot/issues/6389), dispatched 11-16; the first attempt pushed one commit and opened no PR, the second could not push over it; founder call 11-16: draft PR#6401 opened by hand from that branch, CI and run-it review pending; blocks O4; founder calls 11-16: agent root at DATA_DIR/agent, the daemon binary reaches the gh shim as a copy for each run) host script, fourth run, part 1: agent root owned by root, no root step in an agent-owned directory, the daemon's entries closed with printed chmod lines, a proof as the agent user.
- **B3 result:** PR#6401 merged 11-16 17:36Z on the founder's instruction with one open blocker from the run-it review (NO-GO as it was): the daemon user can forge a line of the mode record, and undo prints it as a `chmod` line. B5 fixes it; no host runs the script before B5 and B4 merge.
- [ ] **B5** ([#6407](https://github.com/qf-studio/pilot/issues/6407), dispatched 11-16; PR held as a draft; blocks O4) the mode record and the printed chmod lines accept only plain paths inside the data directory; undo skips a line with a setuid mode.
- **B5 result:** PR#6409 merged 11-16 20:07Z after a run-it review (GO-WITH-NOTES) and the founder's "merge". **N3 result:** PR#6410 merged 11-16 20:07Z in the same way.
- [x] **B6** (PR#6415 merged 11-16 21:54Z after a run-it review with no blocker; five notes go to B8) ([#6412](https://github.com/qf-studio/pilot/issues/6412), dispatched 11-16) the printed chmod lines run as the daemon user; a link to the data directory or the agent root is refused; LC_ALL=C.
- [x] **N6** (PR#6414 merged 11-16 21:57Z after a run-it review with no blocker; six minor notes in the PR comment: no test for the umask window, the stop also covers a settings file that cannot be parsed, two kill calls when the sudo kill fails, an empty file stays when chmod fails; the restore after the session still only warns, which goes to U4) ([#6411](https://github.com/qf-studio/pilot/issues/6411), dispatched 11-16) size limit for a read as the agent user; the settings clean-up failure stops the run; file mode; the rename helper removed. N4 follows it.
- [x] **B4** (PR#6421 merged 11-16 22:51Z after a run-it review with no blocker; notes go to B9) ([#6416](https://github.com/qf-studio/pilot/issues/6416), dispatched 11-16 ~21:55Z) host script: a sudo proof that also reads the rules, a warning for the groups of the daemon user. The old B4 block had five items and a damaged scope list; it was split on 11-16 into B4 and B7 so that each part stays inside the one-hour task limit.
- [x] **B7** (PR#6426 merged 11-17 09:51Z; the run-it review reports after the merge) ([#6422](https://github.com/qf-studio/pilot/issues/6422), dispatched 11-16 ~22:55Z) host script: the firewall before the sudo rule, a missing ip6tables fails the apply, undo that goes on and stops the agent processes, a connection check over IPv6 loopback.
- **Notes from the run-it review of PR#6449 (comment on the PR; not written as a leg; host script file, so after B10 and B8):** the check does what the issue says (644 passed on macOS and in CI; no change before the rule list read; 41 of 43 attack cases correct). **For O4:** a dry run prints the rule list command and does not run it, so it cannot predict the refusal: run `sudo -ll -U DAEMON_USER` by hand before the apply, and read `sudo -V` (an older sudo that prints `Sudoers entry:` with no path makes the script refuse each first apply; CI used 1.9.15p5). For a later leg: the dry run runs the read-only check; a sudo rule for the shared group `pilot` passes the early check and fails the proof after the cloud-init edit (the check runs before the daemon user joins the group); the pass-over counts lines, not the entries that sudo lists (a line for another host plus a `wheel` line in the cloud-init file); a refusal can name the wrong entry, never names the group and prints no summary; no test for the guard "run-as user ALL or root" and for `--keep-daemon-sudo` with a foreign rule; a later rule to the agent user without NOPASSWD wins by last match (read only). Not run: Linux and real sudo (CI log only).
- [x] **B9** (PR#6449 merged 11-17 19:09Z; run-it review done, see the notes line above) ([#6430](https://github.com/qf-studio/pilot/issues/6430), dispatched 11-17 ~09:55Z; blocks O4) (the first run of Pilot from 09:52Z ended with no PR, the second from 13:45Z stalled; the third started the agent at 17:58Z and opened the PR at 18:56Z) from the review of PR#6421: the script reads the sudo rule list before the first change of a sudoers file (the daemon user of the box is in wheel; a rule for that group fails the proof after the cloud-init file is changed); tests for a run-as list and for a failed rule list command.
- [x] **B11** (PR#6479 merged 11-18 03:49Z; the run-it review reports after the merge; **O4 waits for that review**) ([#6473](https://github.com/qf-studio/pilot/issues/6473), dispatched 11-18 ~01:10Z; blocks O4) from the run-it review of PR#6458 (comment on the PR): when the removal of the sudo rule fails, undo still removes the firewall (end state seen: rule file present, no firewall); undo restores the cloud-init root line of the daemon user before the agent processes stop, so the daemon can start an agent process again during the undo; the removal message is false while a root line of the daemon user is active; a process list command that exits 0 with no output fails the step with no reason line; three branches with no test.
- **Notes from the review of PR#6458 that B11 does not cover:** the fault matrix (94 runs: each mocked command fails in turn, over six apply scenarios) found no end state with the rule file and an incomplete firewall on the apply side; a second apply that stops before the plan (exit 2: bad arguments, or no `--confirm-root-path` with an active cloud-init line) keeps the rule file whatever the firewall state is, and prints nothing about it; each failed step before the sudoers-rule step removes the rule also with a complete firewall (a new 0644 file in the data directory fails the daemon-private step on a second apply, the rule goes, and the cloud-init line stays commented out: the daemon user has no sudo until an apply passes) — **for O4 and the runbook: a second apply on a working host can stop the agent path**; no signal trap (an apply interrupted in a firewall step of a second run leaves the old rule file); the PR body lacks the two summary lines. **For O4:** stop the daemon before `--undo`. Not run: Linux, real sudo, iptables, systemd (CI log only: 696 and 204 passed).
- [x] **B10** (PR#6458 merged 11-17 22:08Z; run-it review done, see B11) ([#6450](https://github.com/qf-studio/pilot/issues/6450), dispatched 11-17 ~19:12Z; blocked O4) from the review of PR#6426: on a second run a failed firewall step leaves the sudo rule of the earlier run (seen twice in the mocked harness), so the script removes the rule on such a failure; undo removes the sudo rule first; a failed process list fails the undo step. (The `run_as_data_dir` line of the summary moved to issue 6432.)
- **Notes from the run-it review of PR#6472 (comment on the PR; not written as a leg; host script file, so after B11):** the quote function is correct (255 single bytes and 37 hostile strings: no output byte outside 0x20 to 0x7e, exact round trip in bash and sh; 711 passed on macOS and in CI); an open entry of root fails the step before a change. **One test gap: no test fails when the escape of 0xa0 to 0xff is removed** (both names of `shq_c1_case` hold 0x9b; add a name such as `a\xc3\xa9b`). The escaped form `$'...'` is for bash only: in dash a quoted name with a newline and a quote runs a command (no line made for a paste has this form). A nested `--agent-root DATA/outer/agent` is a dead end: no mode of `outer` lets the apply pass, and the message does not say so (the default agent root is not affected). With one chmod line and one hand-look entry the last sentence names only the chmod. The owner and group names of the new line are printed without the quote function. The owner check runs only for an entry that is open by mode: an entry of the agent user at 0700 passes the daemon-private step and fails only at the last step, after each change step ran. Not run: Linux, bash 5.
- [x] **B8** (PR#6472 merged 11-18 01:04Z; run-it review done, see the notes line above) ([#6459](https://github.com/qf-studio/pilot/issues/6459), dispatched 11-17 ~22:15Z) notes from the review of PR#6415: the quote function escapes every byte outside printable ASCII (with LC_ALL=C the class cntrl misses the bytes 0x80 to 0x9f), a test for the locale line, no chmod line for an entry of root, the closing sentence.
- [x] **P1** (PR#6425 merged 11-17 09:50Z; the run-it review reports after the merge) ([#6419](https://github.com/qf-studio/pilot/issues/6419), dispatched 11-16 ~22:05Z) Go side of the host layout, part 1: the option `run_as_data_dir`, the workspace default below the agent root, a mode check of the daemon's entries at the preflight and before each run, Go cache paths in the agent home.
- [x] **P2** (PR#6437 merged 11-17 15:33Z; the run-it review reports after the merge) ([#6431](https://github.com/qf-studio/pilot/issues/6431), dispatched 11-17 ~09:55Z) the preflight probes the firewall as the agent user: a control connection, the metadata address, the gateway port.
- **Issue 6432** (filed by the daemon on 11-17: post-merge minors of PR#6425; not a leg of this document): a top-level link that leaves the data directory fails the Go mode check, also a dangling one; the summary of the host script prints `run_as_data_dir`. Pilot wrote one commit, reached the task time limit and parked the issue (`pilot-needs-human`). The parked issue made the daemon defer P4 for scope overlap. Founder answer 11-17: finish it by hand. The planning session merged main into the branch (one conflict in `agent_command.go`, both added blocks kept), ran the tests, and pushed. **PR#6442 merged 11-17 17:45Z.** The run-it review after the merge (comment on PR#6442): all five items are fixed and each mutation pin fails without its change; no finding weakens the check.
- **Notes from the review of PR#6442 (not written as a leg; all fail closed):** the Go check is now stricter than the host script: the script skips a dangling top-level link before it reads the target (`[ -e "$e" ] || continue` in `step_daemon_private`), so a dangling link out of the data directory, a dangling link into the agent root and a link loop pass the script and fail the Go preflight; the script must flag them too (same file as B9, B10 and B8, so after them). The upper bound of `runAsMaxLinkHops` has no test (no test has a link loop). Older than this PR: the script refuses an entry name outside `[A-Za-z0-9._/-]` that Go accepts. Not run: Linux, the default `--data-dir`, the group rule.
- **Notes from the run-it review of PR#6453 (comment on the PR; all minor; not written as a leg; `agent_command.go`, so with the first gates leg):** the rule "the workspace root is not the data directory or a parent of it" runs at the preflight only: the check for each run calls the mode check and not the rule, and a second preflight with the same config strings returns the kept result (a link of the root moved to the data directory after the start is not seen; it needs root or the daemon user); `pathForms` removes `..` by text before it resolves links, so a root `X/l/..` with `l` a link into the data directory passes the rule (the owner check of a real host should refuse it; read only); no test for the hand-over of the run context from executeWithOptions through BuildPromptContext to the README read, and none for the removal of the binary copy after a failed close or failed permissions; a root that is a direct child of the data directory is accepted and its whole top-level entry is skipped by the mode check (the host script refuses such a layout); the test for a failed copy counts shim directories in the shared temp directory (can fail under parallel tests). Not run: Linux, real sudo.
- [x] **P4** (PR#6453 merged 11-17 20:09Z; run-it review done, see the notes line above) ([#6438](https://github.com/qf-studio/pilot/issues/6438), dispatched 11-17 ~15:40Z) from the reviews of PR#6425 and PR#6427: the workspace root cannot be the data directory or its parent; tests for the mode masks, for the group of the gh shim binary copy and for a failed copy; the README read takes the context of the run.
- [x] **P6** (PR#6494 merged 11-18 12:44Z, second run; the run-it review reports after the merge) ([#6480](https://github.com/qf-studio/pilot/issues/6480), dispatched 11-18 ~04:00Z; **PR#6486 (first run) closed 11-18 10:19Z for a merge conflict; second run on the new main: PR#6494 open since 12:26Z, mergeable, new commit 24bdeca8b** (PR#6485, the minors of PR#6475, merged into the same files at 08:51Z); must merge before the removal of the preflight refusal) from the run-it review of PR#6475 (comment on the PR): a gateway probe that ends fast with an error and no connection attempt (the probe process is killed, sudo fails for the agent user) counts as "blocked", so the preflight passes with no rule; the probe uses the first non-loopback address of the machine and not the address that the gateway binds; no test protects the order of the preflight for `pilot start --replace`; a gateway port of 0 at the daemon start skips the probe with a Debug line; the gateway port and host can leave the kept-result compare with no failed test.
- **Notes from the review of PR#6475 that P6 does not cover:** the four changes of P5 hold (timeout is a refusal; a failed bind is a refusal; a failed result is not kept; 60 calls over five result kinds leave no open port and no goroutine; 12 of 16 mutations fail a test); the old daemon releases its lock before its process ends, so with `--replace` the bind of the new daemon can fail in a small window and nothing runs the preflight again inside a running daemon (read only); on a loaded host where sudo needs more than 3 s the control connection fails and the text blames the probe tool (fail closed); on a host with no route to the metadata address the metadata probe passes by timeout (nothing answers there); IPv6 is not probed (out of scope of P5); with `--no-gateway` the port is still probed. Not run: real iptables, a real second user, Linux.
- [x] **P5** (PR#6475 merged 11-18 01:45Z; run-it review done, see P6) ([#6455](https://github.com/qf-studio/pilot/issues/6455), dispatched 11-17 ~20:15Z; had to merge before the removal of the preflight refusal) from the review of PR#6437: a timeout at the gateway port is a refusal (seen: a slow sudo gives a passed preflight with no rule); only the daemon start opens the gateway port (today each subcommand opens it, so a subcommand is refused while a daemon runs, and `pilot start --replace` is always refused); a failed result is not kept; a probe for a gateway host that is not loopback; tests for the port wiring in `cmd/pilot`.
- **Notes for U4 from the reviews of 11-17:** workflow hooks run as the daemon user with the daemon environment (`workflow/hooks.go`); GOFLAGS and GOPROXY of the daemon still reach the agent (GOPROXY can hold credentials); the env context file `.pilot-env-context.txt` is still read by the daemon process.
- [x] **P3** (PR#6427 merged 11-17 09:51Z; the run-it review reports after the merge) ([#6420](https://github.com/qf-studio/pilot/issues/6420), dispatched 11-16 ~22:05Z) the gh shim runs a copy of the daemon binary for each run; the workflow file and the project context are read as the agent user.
- **P1 decisions 11-16 (defaults from the research, no founder call asked; each one can change before the option is turned on):** the data directory is a config field with the script's default, not derived from the workspace path; the mode check runs at the preflight and before each run; for the gateway probe the daemon opens the gateway port for the time of the probe, because the gateway starts after the preflight; the binary copy is for each run (founder call 11-16), about 30 MB of writes. Not checked on the box: the mount type of the temporary directory (a tmpfs makes each copy cost memory), and that bash is on the path of the agent user. The config default of the gateway port is 9090 and the script closes 9091; the probe uses the configured port, so a difference shows as a refusal.
- **P1 research note of 11-16 (replaced by P1, P2, P3):** the daemon copies its binary into the gh shim directory of each run (today the shim runs the daemon binary as the agent user, and the script closes that binary); the preflight probes the firewall as the agent user (a connection to the metadata address and to the gateway port must fail) because the daemon starts from tmux and no unit can require the firewall; the preflight checks the modes of the daemon's entries at each start; the default of `run_as_workspace_dir` follows the agent root; the Go cache variables of the daemon do not reach the agent.
- **Box fact 11-16 (read-only check):** the daemon user is in the group docker, which is a path to root, so O1 did not fully remove root from the daemon user. Founder discusses the removal with the infrastructure owner.
- [x] **B2** (merged 11-15 21:02Z) (PR#6378: real job green on ubuntu-24.04 with the classic sudo, 94 passed; run-it review GO-WITH-NOTES for the merge, NOT ready for a host run; marked ready 11-15) ([#6373](https://github.com/qf-studio/pilot/issues/6373), dispatched 11-15) host script, third run: undo that the agent user cannot block, shared read removed, sudo proof by behaviour, macOS test fix, real iptables and systemd test in CI. O4 waits for it.
- [x] **U1** (PR#6335 + PR#6342 merged 11-12) config option, one spawn constructor for every model process, allowlist environment, kill through sudo, fail-closed preflight, group-readable guard files (dispatch-ready, below).
- [x] **G1** (PR#6375 merged 11-15 17:19Z after a run-it review, GO-WITH-NOTES; the notes go to U2 item 10 and to U4; the post-merge minors issue 6377 was closed as not planned on 11-15: its branch ran the secret gate through sudo as the agent user, which is the opposite of the hand-over design, and the pre-PR review held it; its useful items are in U2 items 10 and 11) ([#6372](https://github.com/qf-studio/pilot/issues/6372), dispatched 11-15; split from U2 on 11-15 after a spec check against `origin/main` 1e99ccbe0) one constructor for every git command of the executor package (110 inline sites), with the route to the agent user for a directory inside the workspace root; config field `run_as_workspace_dir`; fail-closed; a source test.
- [x] **U2** (PR#6383 merged 11-16 06:43Z after a run-it review, GO-WITH-NOTES; notes go to N1 items 7 and 8) ([#6380](https://github.com/qf-studio/pilot/issues/6380), dispatched 11-15 ~20:00Z) workspace as an agent-owned clone under the workspace root (base commit as a bundle that the daemon sends, no alternates, no read of the daemon's repository), cleanup and sweeps as the agent user with the project in the directory name, preflight for the root, `use_worktree` and the pool.
- **N1** (issue 6385, dispatched 11-16) ran into the one-hour task limit twice and pushed no branch; closed as not planned and split into N2, N3, N4 on 11-16.
- [x] **N2** (PR#6397 merged 11-16 15:14Z by the daemon two minutes after it was marked ready; run-it review GO-WITH-NOTES; three notes go to N5) ([#6387](https://github.com/qf-studio/pilot/issues/6387), dispatched 11-16 ~10:10Z)
- [ ] **N5** ([#6403](https://github.com/qf-studio/pilot/issues/6403), dispatched 11-16; PR held as a draft; small) a test for the production default of the refusal, no workspace sweep after a refused preflight, the config example sentence. the preflight refuses the option until the gates leg merges; the corrections of the workspace leg from its review; the macOS test fix.
- **N5 result:** PR#6405 merged 11-16 17:35Z after a run-it review (GO-WITH-NOTES) and the founder's "merge".
- [ ] **N3** ([#6406](https://github.com/qf-studio/pilot/issues/6406), dispatched 11-16; PR held as a draft; needs N2) the daemon writes the task document, the Navigator files and the settings file through a helper that runs as the agent user.
- [x] **N4** (PR#6424 merged 11-17 09:39Z; the run-it review found one narrow blocker: the workspace directory itself is opened with link following and by its path for each file; founder answer 11-17: merge, then fix; N7 fixes it) ([#6417](https://github.com/qf-studio/pilot/issues/6417), dispatched 11-16 ~22:00Z)
- [x] **N7** (PR#6433 merged 11-17 11:38Z; the run-it review reports after the merge) ([#6428](https://github.com/qf-studio/pilot/issues/6428), dispatched 11-17 ~09:45Z) from the review of PR#6424: one descriptor of the workspace for the whole copy out, no followed link in the copy in, a size limit, a hard link refusal. Navigator copy in as a tar stream, copy out without a followed link at any path element.
- **N7 review notes (PR#6433, for one small later leg):** no bound on the directory walk of the copy out and no time limit; the file limit is off by one when no graph file exists; no test pins O_NOFOLLOW at the workspace name, the bounded read, or O_NOFOLLOW of the copy-in open; a file that vanishes between Lstat and open fails the whole copy in.
- [x] **H5** (PR#6469 merged 11-18 00:24Z; the run-it review reports after the merge) ([#6447](https://github.com/qf-studio/pilot/issues/6447), dispatched 11-17 ~18:45Z; had to merge before the removal of the preflight refusal) from the run-it review of PR#6443 (comment on the PR): a stream that is not a bundle is refused before the fetch (seen: a one-line `gitdir:` stream makes the daemon-side `git fetch` run `git-upload-pack` as the daemon user in a repository the agent names, and the hand-over ref then holds commits the agent user cannot read); the tip must be a commit above the base; a cancelled context stops the daemon-side git with its children; the daemon-side git ignores `GIT_DIR` and related variables of the daemon environment.
- **Notes from the review of PR#6443 that H5 does not cover (not written as a leg):** a forged bundle with an empty pack can name a commit that only the daemon's repository has and that is built on the base (full fix: index the pack into a quarantine object directory and require each new object there; the gate must not assume that `BASE..sha` holds only objects of the agent); each failed or removed hand-over leaves packs or `tmp_pack_*` files in the daemon's repository (up to 2 GB each; the quarantine directory or the recovery-ref part); no test fails without `fetch.fsckObjects`, without `GIT_NO_REPLACE_OBJECTS`, without the hooks-off arguments on the agent side, without `check-ref-format`, without the three path checks, or without the ref removal after a failure; the task IDs `GH/1` and `GH.1` give the same ref; no lock per task; fsck accepts a symlink entry and a 1 MB commit header (for the gate and H4); check the git version of the box (an older git may not apply `fetch.fsckObjects` to a bundle; not verified). Not run: real sudo, Linux, a stream over 2 GB.
- [x] **H1** (PR#6443 merged 11-17 18:15Z; run-it review done, see H5) ([#6434](https://github.com/qf-studio/pilot/issues/6434), dispatched 11-17 ~12:00Z) hand-over, part 1: handOverBranch takes the branch of the workspace as a bundle stream into `refs/pilot/handover/TASK` of the daemon's repository; no caller yet. The old leg U3 was eight items and about fifteen call sites; a spec check against `origin/main` 1310e68d1 on 11-17 split it into H1 to H4.
- **Founder call 11-18 ~10:30Z: way A — the hand-over transport becomes a plain pack that is not thin, read by `git index-pack` with no `--fix-thin`; no bundle and no `git fetch` on the daemon side.** It replaces H6 (issue 6470 closed, branch `pilot/GH-6470` kept for reference). Reasons, all from reviews of 11-18: the thin-bundle bypass (pre-PR review of H6); a stream with the bundle signature and a broken header still starts `git-upload-pack` on the temporary file; a v3 bundle with a `@filter` capability is accepted, and in a partial-clone daemon repository git then fetches objects that the agent names from the remote (run-it review of PR#6469).
- [ ] **B12** ([#6515](https://github.com/qf-studio/pilot/issues/6515), dispatched 11-19 ~08:25Z) from the open notes of the run-it reviews of PR#6442, PR#6449, PR#6472 and PR#6479: the dry run runs the read-only check of the sudo rule list; a failed stop of the agent processes in the undo keeps the firewall; a dangling link or a link loop in the data directory is flagged as the Go preflight does; a summary line when the daemon user is left with no sudo rule; a test name with a byte of 0xa0 to 0xff for the quote function.
- [ ] **P7** ([#6516](https://github.com/qf-studio/pilot/issues/6516), dispatched 11-19 ~08:25Z; must merge before the removal of the preflight refusal) from the partial run-it review of PR#6494: a metadata probe with no answer counts as blocked only when the probe printed its start line; `bash --norc --noprofile`; the marker status is exactly `0` or `1`; an output limit; a port above 65535 is refused at once.
- [x] **H14** (PR#6518 merged 11-19 10:46Z, first run, gate passed in 3m31s after the stop of the orphan processes; the run-it review reports after the merge) ([#6514](https://github.com/qf-studio/pilot/issues/6514), dispatched 11-19 ~08:20Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6497 (comment on the PR): in a daemon repository that is a partial clone, the object check itself makes git fetch a missing object from the promisor remote into the quarantine, and the check then passes: an object that the agent only named reaches the hand-over ref (reproduced; with `GIT_NO_LAZY_FETCH=1` the same case is refused and no fetch starts). Also: no test for the check of trees and blobs and for the format check of the tip; the temporary pack file of a killed daemon is never removed; the loser of two hand-overs of one task deletes the ref of the winner; the size limit covers the stream and not what it inflates to.
- **Notes from the review of PR#6497 that H14 does not cover:** in a normal daemon repository about 60 forged streams found no way for an object of the daemon's repository to the hand-over ref (thin packs and deltas against a private object fail at index-pack; a named private parent, tree or blob fails at verifyHandOverObjects, which is the line of defence: `--strict` accepts links into the alternate); each failure leaves no file and no ref; no false refusal for a merge commit, 2000 files, a second hand-over, re-created content; a gitlink that names a private commit is accepted (a push does not send it); the sweep uses the mtime of the quarantine directory, so a hand-over that runs for more than one hour can lose its quarantine (fails closed); with GIT_COMMON_DIR in the daemon environment the quarantine is made in the other repository (H9). Not run: a real second user, Linux, SHA-256 repositories, a shallow daemon repository.
- [x] **H8** (PR#6497 merged 11-18 13:49Z; run-it review done, see H14; the run-it review reports after the merge) ([#6488](https://github.com/qf-studio/pilot/issues/6488), dispatched 11-18 ~10:40Z; replaces H6; must merge before the removal of the preflight refusal) hand-over by a pack stream: the agent user sends `git pack-objects --stdout --revs` (not thin); the daemon checks the pack signature, runs `git index-pack --strict` with no `--fix-thin` into a quarantine object directory, requires each object of the range in the quarantine alone, then moves the pack and sets the ref; a failure leaves nothing; a branch with nothing above the base gives errHandOverNothingNew.
- [ ] **H15** ([#6519](https://github.com/qf-studio/pilot/issues/6519), dispatched 11-19 ~10:55Z, after the merge of H14; must merge before the removal of the preflight refusal) from the run-it review of PR#6511 (comment on the PR, 11-19): the filter of the daemon-side git removes names by the prefix `GIT_` and passes each other name, so `GITHUB_TOKEN` and each other token reach all nine git processes of a hand-over (reproduced; the contract of the issue kept the other names); the global git config of the daemon user still acts (a trace2 target writes a file); no test for the `GIT_NO_REPLACE_OBJECTS=1` line of the update-ref; the wait delay test skips on a host with no `setsid`.
- **Notes from the run-it review of PR#6511 that H15 does not cover:** the secret gate runs nine git commands with the full daemon environment (`secretpatterns/pushrange.go`); with a foreign `GIT_DIR` it fails closed, no bypass was built; item 5 of H7 (daemonGitEnv) covers these commands with a list of denied names, so after H7 check that tokens are out there too (for H12). Held: 43 hostile `GIT_` names, none reaches one of the nine processes; both mutations of the acceptance fail the named tests. Not run: Linux, a real second user, the system git config.
- [x] **H9** (PR#6511 merged 11-18 20:12Z on the third gate run; run-it review done 11-19, see H15) ([#6500](https://github.com/qf-studio/pilot/issues/6500), dispatched 11-18 ~15:25Z) the daemon-side git of the hand-over gets an environment by allow list (run-it review of PR#6469: `GIT_COMMON_DIR` moves the object store to another repository with a success result, `GIT_EXEC_PATH` runs programs of a named directory, `GIT_GRAFT_FILE` makes the ancestor check accept an orphan history); tests for the guards that no test pins.
- **Notes from the run-it review of PR#6469 that H8 and H9 do not cover:** the four fixes of H5 hold (seven bad tips refused with the ref removed; the return comes 15 ms after a cancel with a 300 MB bundle and no process survives); a merge commit whose second parent is an unrelated history is a legal tip, so the whole of that history is in the push range and only the secret gate stands before the remote; a header line of 100 MB takes 1 m 37 s (gone with the pack transport). Not run: real sudo, Linux, a partial-clone daemon repository through handOverBranch.
- [x] **H6** (REPLACED by H8 on 11-18; [#6470](https://github.com/qf-studio/pilot/issues/6470) closed with no PR), dispatched 11-18 ~00:30Z; **parked by Pilot (`pilot-needs-human`), branch `pilot/GH-6470` at d3a4d94d7, no PR:** the pre-PR review names two blocking defects. (1) A forged thin bundle gets a commit of the daemon's repository past verifyHandOverObjects: the fetch unbundles with `index-pack --fix-thin`, a delta in the stream can name the private commit as its base, and index-pack copies that base out of the alternate into the quarantine pack, where the check then finds it (read from the git source by the reviewer, not run). (2) A failed update-ref after the move of the objects leaves the packs in the daemon's repository. **Design consequence: "the object is in the quarantine" is no proof while a thin pack is accepted. Options: the agent side sends a pack that is not thin (`git pack-objects --stdout --revs` with no `--thin`) and the daemon runs `git index-pack --strict --stdin` with no `--fix-thin`; or the check compares each object of the range with the daemon's object store before the hand-over. Waits for a founder call (it changes the transport of design section 4.2).** Must merge before the removal of the preflight refusal) from the run-it reviews of PR#6443 and PR#6461: the hand-over accepts only commits whose objects came in the bundle. Seen through the push of H2: a workspace commit whose parent is a commit that only the daemon's repository has (listed in `.git/shallow` of the workspace) was handed over, passed the gate (it scans for secrets only) and was **pushed to the remote**. Also: a failed hand-over leaves no pack; a branch with no commit above the base gives a typed "nothing to hand over" result, not a bundle error.
- [ ] **H7** ([#6501](https://github.com/qf-studio/pilot/issues/6501), dispatched 11-18 ~15:30Z; its probe for ghDirForDaemonRepo was corrected to the method form; must merge before the removal of the preflight refusal) from the run-it review of PR#6461: the "already published" forgiveness after a failed push compares the remote head with the HEAD of the workspace, which the agent controls, so a failed hand-over reads as a delivered push; the hand-over SHA is recorded before the gate, so the count of new commits uses a refused commit; a push with nothing above the base is retried as an infra failure; the fetch of the count and two `ls-remote` probes run with credentials and without the hooks-off arguments; four guards with no test.
- **Notes from the review of PR#6461 that H6 and H7 do not cover:** no daemon-user git command ran in the workspace in any driven case (20 daemon git calls over the delivery methods, 0 in the workspace; 0 marker lines from the daemon side with a hostile workspace config of 20 keys and 8 hooks); the gate refused each secret case (later removed, merged side branch, merge commit, orphan history, commit message, binary attribute, replace ref, moved remote-tracking refs); branch names with `:`, `..`, `^`, `*`, spaces are refused and names such as `--mirror` or `+plus` go to `refs/heads/NAME` only; the refused commit stays under `refs/pilot/handover/TASK` until the next hand-over (the lifetime of the ref is in H3); with `GIT_DIR` in the daemon environment the gate, the push with credentials and the probes would follow it (H5 covers `handover.go` only: **H7 takes the same environment cleanup for the delivery commands**); a token in the environment of the agent-side bundle command is not caught by a test under the mock sudo (real `env_reset` not exercised). Not run: real sudo, Linux, the `default_branch` setting of the gate.
- [x] **H2** (PR#6461 merged 11-17 22:47Z; run-it review done, see H6 and H7) ([#6445](https://github.com/qf-studio/pilot/issues/6445), dispatched 11-17 ~18:20Z) hand-over, part 2: the secret gate, the push and the remote probes run in the daemon's repository on the hand-over ref.
- **Notes from the run-it review of PR#6506 (comment on the PR, 11-19; no blocker; all for H12):** after a failed push the kept workspace is removed by the next sweep or prune, because the hand-over ref counts as safe, and the next hand-over of the task deletes that ref: the commit is then in no ref (reproduced; the fix is a recovery ref before the removal, or no hand-over ref in the safe list of the sweep); only HEAD and `refs/heads/` are read, so a commit only in a stash, a tag or a custom ref goes with no Error line (`rev-parse HEAD --all`); with no record, a workspace on a base that only a local branch of the daemon holds is kept seven days and then logged as lost; two sweeps at once count one removal twice; an age that cannot be read keeps a workspace for ever; the sweep does not skip a workspace with a live record (older than the PR). Guards with no test: the unknown age, the `.git` check for a link and a file, the hex check of the tips, the sweep with no record. Held: no removal outside the workspace, both mutations of the acceptance fail the named tests.
- [x] **H11** (PR#6506 merged 11-18 17:32Z after a second attempt (the quality gates failed once); run-it review done 11-19, see the notes line above) ([#6492](https://github.com/qf-studio/pilot/issues/6492), dispatched 11-18 ~11:40Z) from the run-it review of PR#6482 (comment on the PR): the clean-up removes a workspace as "pushed" also when the agent made a commit after the last push, with no log line; the sweep at the daemon start and the prune of a task remove a kept workspace with no check, so its commits are lost; no test for the rule "keep the workspace after a failed push" (a mutation that records "pushed" before the push passes the suite).
- **Notes from the review of PR#6482 for a later leg (H12, to write after H7: `git.go` and `epic.go`):** ghDirForDaemonRepo keeps only the last two path segments of the daemon's remote, so a GitHub Enterprise or GitLab remote gives `--repo owner/name` with no host and `gh` resolves it against github.com with the daemon token (derivation reproduced, real gh not run); a remote that ends in `.git/` gives `owner/name.git`; the parse error for a one-segment remote with credentials prints the credential; three more `gh` commands run in the workspace (`epic.go`: sub-issue, label, comment calls take the execution path); a FIFO in place of `graph.json` stalls each read for 30 s or more and the reads use a background context; `refs/pilot/handover/TASK` stays for ever after a sweep and the next hand-over of the task deletes it, also when it is the only copy of the commits. Held in the review: the four `gh` commands ran in the daemon's repository with the daemon's `--repo` and `--head` against a hostile workspace config; the recovery ref is made in the daemon's repository; "pushed" is the exit status of the daemon's own push. Not run: a real second user, real sudo, real gh.
- [x] **H3** (PR#6482 merged 11-18 04:31Z; run-it review done, see H11 and the notes line above) ([#6462](https://github.com/qf-studio/pilot/issues/6462), dispatched 11-17 ~22:55Z) hand-over, part 3: the four `gh` commands and the recovery ref use the daemon's repository; the workspace stays until its commits are safe; the graph reads go through the helper.
- [ ] **H13** ([#6503](https://github.com/qf-studio/pilot/issues/6503), dispatched 11-18 ~16:10Z) from the run-it review of PR#6499 (comment on the PR): the three header leaks are closed in the error, the log line, the alert and the comment. One leak is left in older code, the file path print: git prints a path with `"`, `\` or a control byte in its quoted form, and the redaction does not know that form when the value also holds `<`, `>` or `&` or a control byte. Such a registered value as a file name is printed whole in the refusal; and a file with that name and clean content is **not refused, and the push goes out** with the value in the file name. This code runs for each user of the product.
- **Notes from the review of PR#6499 that H13 does not cover:** `header line N` is the right line of `git cat-file -p` in each case tried; no false refusal for signed commits; 2000 commits cost 0.10 s; an encoding value that is a part of 40 or fewer characters of a longer registered value is printed (the object holds only that part); an empty new file whose name is a registered value that holds ` b/` is not refused; the stderr of git is printed with no redaction for a ref that does not resolve, only through CheckPushRange and not through the product path. Not run: Linux, invalid UTF-8 in a path, a rename with a quoted path.
- [x] **H10** (PR#6499 merged 11-18 15:18Z; run-it review done, see H13; released in v2.324.0; the run-it review reports after the merge) ([#6490](https://github.com/qf-studio/pilot/issues/6490), dispatched 11-18 ~11:15Z) from the run-it review of PR#6484 (comment on the PR): the refusal text of the secret gate prints up to 40 characters of a secret in three cases — the name of an extra header line (a registered value of 41 or more characters, or a shape value longer than 40), the value of an `encoding` header longer than 40 (older code, GH-6343), and a value with a space written as one header line. The text goes to the error, the log line, the alert and the issue comment. **This code runs for each user of the product, with the option empty too.** PR#6484 merged the commit cc24cf2fe that the hold comment named: the retry of 11-18 did not run the agent again, so neither point of the retry comment landed (the label removal alone made the daemon open the PR for the pushed branch).
- **Notes from the review of PR#6484 that H10 does not cover:** the header scan refuses each place tried (author, committer, a second encoding line, an invented header, continuation lines, a header after gpgsig, mergetag, CRLF, a NUL byte, the root commit, a merge commit, the first and last commit of 2000); no false refusal for 150 signed commits (GPG ed25519, GPG rsa4096, SSH ed25519), tree and parent lines, token-like emails, a long non-ASCII name; 2000 commits cost 0.10 s and 6 git processes; the identity of the agent works with no identity in the workspace, and a newline or `<>` in user.name of the daemon's repository injects no header; **not refused:** a value split over a header line and its continuation, the base64 form of a value in gpgsig, the message of an annotated tag (tag objects are not scanned; read: no push sends a tag); a `user.name` of only `<>` in the daemon's repository makes each commit of the agent user fail. Not run: Linux, real sudo.
- [x] **H4** (PR#6484 merged 11-18 06:03Z; run-it review done, see H10) ([#6463](https://github.com/qf-studio/pilot/issues/6463), dispatched 11-17 ~22:55Z; parked by Pilot 11-18 02:23Z: the pre-PR review found one blocking defect (a part of a secret in the name of an extra header line can show in the refusal) and the fix round had no time left; branch `pilot/GH-6463` at cc24cf2fe; started again 11-18 ~03:55Z with a comment that narrows the retry to that defect and one comment fix) a git identity for the commits of the agent user; the secret gate also scans the header lines of a commit.
- **Spec check 11-17 for U4 and U5 (their blocks below are from 11-12 and are stale; write them again before a dispatch):** U4 becomes three parts: (a) a command builder with configure and kill hooks in `internal/quality` (the quality runner replaces the process attributes of an agent command, so a constructor alone is not sufficient), wired at the gate command and both scope commands; (b) workflow hooks, evidence commands, both lint commands, the env context file read and the settings restore failure; (c) the removal of the preflight refusal, alone, as the last PR. U4 changes `agent_command.go`, so it follows P2 and P4. `run_as_env` is stale: the Go paths are set since P1. U5 becomes two parts: (a) a scoped token (repositories and permissions in the mint request; the mint of today sends no body), a token file with refresh, `GH_TOKEN` set in the gh-guard subcommand, the credential helper entries also for the gates; (b) the override for each project, which is a refactor (the run-as state is one value for the process: 21 reads, 16 spawn callers, none with a project).
- **Open founder decisions from the spec check 11-17:** (1) is the override for each project needed, or does the first host use one daemon with the option for all projects; (2) drop `run_as_env`; (3) GOPROXY, GOFLAGS, GONOSUMDB, GOPRIVATE of the daemon reach the agent: keep, drop, or refuse a value with credentials; (4) a hold for the one PR that removes the refusal. Defaults taken without a question: the gate scans commit headers and the agent gets a git identity from the environment (H4); after a failed hand-over the workspace stays (H3).
- [ ] **U4** (NOT dispatch-ready: stale block, see the spec check line above; needs H1 to H3, P2, P4) quality gates, workflow hooks, evidence commands and the lint fix as the agent user with the allowlist environment; `run_as_env` for values such as the Go caches.
- [ ] **U5** (NOT dispatch-ready: stale block, see the spec check line above; needs H1 and, on the box, O2) read-only installation token per run in a file for the `gh` shim and a git credential helper (also serves the fetch of a private module); per-project override of the option.
- **Merge rule (founder call 11-16, mem-319):** on the stage environment the daemon merges a ready PR with green CI by itself; a security PR of this task stays in draft after the verdict until the founder writes "merge".
- **Order (founder call 11-12; U2 split into G1 → U2 → N1 on 11-15):** U1 → F1 → F2 → G1 → U2 → N2 → N5 → N3 → N4 → U3 → U4 → U5, one at a time. `run_as_user` stays empty on every host until U3 **and** U4 merge: until then the gate, the quality gates and the hooks run as the daemon inside the agent-owned clone. The poller holds legs that touch the same directory, so a parallel dispatch gains nothing. Check for an open post-merge-review issue on the same files before each dispatch (mem-301).

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

### LEG G1 — repo: qf-studio/pilot — title: refactor(executor): run_as_user — one constructor for every git command of the executor package, with a route to the agent user for a directory inside the workspace root (off by default)

probe: present qf-studio/pilot func[[:space:]]agentCommand\(
probe: present qf-studio/pilot func[[:space:]]setAgentEnv\(
probe: present qf-studio/pilot func[[:space:]]NewGitOperations\(
probe: present qf-studio/pilot func[[:space:]]\(g[[:space:]]\*GitOperations\)[[:space:]]gitHardened\(
probe: absent  qf-studio/pilot func[[:space:]]\(g[[:space:]]\*GitOperations\)[[:space:]]gitCm[d]\(
probe: absent  qf-studio/pilot RunAsWorkspaceDi[r][[:space:]]+string

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). The option run_as_user and the constructor agentCommand in `internal/executor/agent_command.go` exist: with the option set, a model process starts as a second Unix user through sudo with an allowlist environment (setAgentEnv). The next legs give the agent a workspace that the agent user owns, under one workspace root. Every git command that runs in that workspace must then run as the agent user, or a hook, a filter or a config value of the workspace runs as the daemon user.

At origin/main 1e99ccbe0 the non-test files of the package `internal/executor` build a git command inline at 110 places (55 in `git.go`, 33 in `worktree.go`, the rest in files such as `lint.go`, `git_freshness.go`, `simplify.go`, `preflight.go`, `base_presence.go`, `repo_guardrail.go`). The forms are exec.CommandContext(ctx, "git", exec.CommandContext(fctx, "git" and exec.Command("git". Many sites set no cmd.Dir and pass `-C <dir>` in the arguments. The method gitHardened of GitOperations in `internal/executor/git.go` is the constructor of the co-author strip: it adds config flags, sets cmd.Dir, and sets cmd.Env to os.Environ() plus GIT_NO_REPLACE_OBJECTS=1. withGitCredentials in `internal/executor/git_credentials.go` adds the token and the askpass variables to cmd.Env.

This leg is a refactor plus the route. No workspace exists yet, so with the option empty and with the option set every command of a real run still takes the plain route. The option must stay empty on every host until the hand-over leg and the gates leg merge.

## Scope

1. Config: one string field RunAsWorkspaceDir (yaml run_as_workspace_dir) next to RunAsUser in the ClaudeCodeConfig struct of `internal/executor/backend.go`. With run_as_user set and this field empty the value is /var/lib/pilot/agent-workspaces. The resolved root becomes part of the run-as settings that agentCommand reads, and part of the comparison that decides if PreflightRunAsUser runs again. Document the field in `configs/pilot.example.yaml` with this sentence: run_as_user must stay empty on every host until the hand-over leg and the gates leg of TASK-651 merge.
2. Constructor, new file, plain text: internal/executor/git_command.go. A method gitCmd(ctx, args...) on GitOperations and a free function for the sites that have no GitOperations value. Each takes, or derives from the receiver, the directory that decides the route. A site that passes `-C <dir>` today keeps its arguments and gives that directory for the route.
3. Plain route: with the option empty, or with a route directory that is not inside the workspace root, the command has the same argv, the same cmd.Dir (set or not set) and the same cmd.Env as the inline command of that site today.
4. Agent route: with the option set and the route directory inside the workspace root, the command comes from agentCommand and its environment goes through setAgentEnv. "Inside" compares the two paths after filepath.EvalSymlinks and filepath.Clean, so that a root reached through a symbolic link, a trailing slash or a /private prefix takes the agent route. With the option set, a route directory that cannot be resolved is an error.
5. Fail closed: when agentCommand returns an error, or item 4 gives an error, the returned command has its Err field set. Run, Output, CombinedOutput and Start then return that error and no process starts. The constructor never falls back to the plain command.
6. Environment on the agent route. The git process gets the allowlist environment plus the git variables of a fixed list in the constructor file (GIT_NO_REPLACE_OBJECTS and the other non-credential variables that the sites set today; list each one). No other variable of the daemon reaches it. A site that sets cmd.Env today does it through one helper of the constructor file, so that the rule holds for both routes.
7. Credentials: withGitCredentials on a command of the agent route mints no token, sets no variable, and sets the Err field with a message that names the hand-over leg. A push from a workspace then fails with that error; this is intended until the hand-over leg.
8. Replace every inline git command in the non-test files of the package directory `internal/executor` (not its subpackages) with the constructor. gitHardened keeps its config flags and its GIT_NO_REPLACE_OBJECTS=1 and builds on the constructor.
9. Source test: parse the non-test files of the package directory with go/parser and fail when a file other than git_command.go holds a call of exec.Command or exec.CommandContext whose command argument is the string literal "git". The test names the file and the line. It does not depend on the name of the context variable.
10. Tests, table-driven, no root needed. Add a second mock sudo helper next to mockSudo in `internal/executor/agent_command_test.go`: it drops the sudo flags up to and including `--` and runs the rest as the present user, and it records its argv (mockSudo and mockSudoBackground only record; they do not run the command). Use stubRunAsGOOS, because agentCommand builds a sudo command on Linux only. Cases: for a sample of sites that includes a `-C` site, a site with cmd.Dir and gitHardened, the plain route gives the argv, cmd.Dir and cmd.Env of today; a directory outside the root takes the plain route; a directory inside the root gives a sudo argv and git runs through the mock; a root behind a symbolic link and a path with a trailing slash take the agent route; a failed preflight gives a command whose Run returns the error and the mock records no call; withGitCredentials on the agent route sets Err and the provider is not called; a variable DAEMON_ONLY_SECRET of the test process is absent from the environment of git on the agent route and GIT_NO_REPLACE_OBJECTS=1 is present for gitHardened.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'GitCommand|GitCmd|GitSource|RunAsUser|AgentCommand'` passes; paste the output into the PR body.
- [ ] Add one inline exec.CommandContext(fctx, "git", ...) to a non-test file -> the source test fails and names the line.
- [ ] Return the plain command when agentCommand fails -> the fail-closed test fails.
- [ ] Compare the paths without filepath.EvalSymlinks -> the symbolic-link test fails.
- [ ] With the option empty, the tests of the package that exist today pass without a change to their assertions.
- [ ] The PR body gives the count of replaced sites per file.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The creation of a workspace, its cleanup and the sweeps (next leg). The package `internal/executor/secretpatterns`, which has its own constructor gitCommand in `pushrange.go` and cannot import the executor package; the secret gate moves to the daemon's repository in the hand-over leg. The package `internal/quality`. Any change of an argument, a directory or an environment value on the plain route.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG U2 — repo: qf-studio/pilot — title: feat(executor): run_as_user workspace — an agent-owned clone per run under the workspace root, cleanup and sweeps as the agent user, fail-closed preflight (off by default)

probe: present qf-studio/pilot func[[:space:]]\(g[[:space:]]\*GitOperations\)[[:space:]]gitCmd\(
probe: present qf-studio/pilot RunAsWorkspaceDir[[:space:]]+string
probe: present qf-studio/pilot func[[:space:]]CleanupOrphanedWorktrees\(
probe: present qf-studio/pilot func[[:space:]]PreflightRunAsUser\(
probe: absent  qf-studio/pilot func[[:space:]]createAgentWorkspac[e]\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). The option run_as_user, the constructor agentCommand and the git constructor gitCmd exist (earlier legs): a git command whose directory is inside the workspace root runs as the agent user. The workspace is still a git worktree that the daemon user owns: CreateWorktreeWithBranch in `internal/executor/worktree.go` creates it under the temp directory and it shares the .git directory of the daemon's repository. An agent that runs as a second user cannot write there. A shared .git that the agent can write would let a hook or a config value run as the daemon user.

Facts at origin/main 1e99ccbe0:

- The runner creates the worktree near `internal/executor/runner.go` near line 4178 and gets a cleanup function back (`runner.go` near line 4300); preserveWorktreeOnPushFailure keeps the directory after a failed push. The runner creates no worktree when allowWorktree is false, when the branch is empty, or when DirectCommit is set.
- The base ref can be origin/BRANCH, a commit SHA that only FETCH_HEAD holds (`internal/executor/git.go` near line 1977), or the text FETCH_HEAD (`internal/executor/worktree.go` near line 658).
- cleanupWorktreeAndBranch removes the worktree and deletes the task branch in the daemon's repository. CleanupOrphanedWorktrees and PruneOrphanedWorktreeForTask in `internal/executor/worktree.go` remove orphans; the second matches the prefix pilot-worktree- plus sanitizeBranchName of the task. findOrphanedWorktreeDirs in `internal/executor/finish_tripwires.go` only reports. All three recognise a .git FILE with a gitdir line and learn the project from it; a clone has a .git DIRECTORY and the workspace root is common to all projects.
- PreflightRunAsUser(cfg *ClaudeCodeConfig, backendType string) in `internal/executor/agent_command.go` does not see use_worktree and worktree_pool_size; both are fields of BackendConfig in `internal/executor/backend.go`. Its callers are in `cmd/pilot/main.go`.
- The host script `scripts/box/setup-agent-user.sh` creates the workspace root with the agent user as owner, the daemon's group, and mode 2770. Design change of 2026-11-15: the agent user gets no access to a repository of the daemon (the shared read of the script is removed in another leg). Do not depend on it.

With the option empty nothing changes. After this leg a run with the option set still cannot finish: the daemon writes files into the workspace (next leg) and the push needs the hand-over leg. The option stays empty on every host; keep the sentence in the config example.

## Scope

1. Workspace creation, new file, plain text: internal/executor/agent_workspace.go. A function createAgentWorkspace(ctx, repoPath, taskID, branch, baseRef) returns the workspace path and a cleanup function with the contract of the worktree cleanup function. Every command that creates or changes a file in the workspace runs as the agent user through agentCommand or gitCmd.
2. Name: a direct child of the workspace root, pilot-ws-PROJECT-TASK-NANOSECONDS. PROJECT is the first 8 hex characters of the SHA-256 of the canonical path of the daemon's repository. TASK is sanitizeBranchName of the task ID. The project is read from the name only, never from a file inside the workspace.
3. Base commit. The daemon runs fetchOriginBranchForWorktree in its own repository as today and resolves the base ref to a commit SHA there (all three forms of the Context). For a fix continuation it resolves the head of the task branch in the same way.
4. Transport. The daemon never lets the agent user read its repository, and it writes no file into the workspace root. It runs `git bundle create` in its own repository with the output on standard output (a temporary ref under refs/pilot/ws-base/ names the commit; delete it in every outcome) and sends that stream to the standard input of a process that runs as the agent user and stores it as a file in the workspace root. Then, as the agent user: git init in the new workspace directory, one fetch from the bundle file, the task branch at the commit that CreateWorktreeWithBranch would use, the origin URL of the daemon's repository with any user or password part removed, and the removal of the bundle file. Keep the transport in one function.
5. Properties of the result, each checked by the daemon before createAgentWorkspace returns: the head of the task branch equals the SHA of item 3; the repository has no alternates file and no object file that is a hard link into the daemon's repository; it holds no ref other than the task branch and the fetched base; no bundle file is left in the workspace root; the daemon user can list the workspace directory. A failed check removes the workspace through item 7 and returns an error.
6. Use it: at the place in `internal/executor/runner.go` where CreateWorktreeWithBranch is called for a task run, call createAgentWorkspace when the option is set. Every later step receives the workspace path as it receives the worktree path today, and preserveWorktreeOnPushFailure keeps a workspace as it keeps a worktree. With the option set, a run for which the runner would create no worktree (allowWorktree false, an empty branch, DirectCommit) stops before any process starts, with one Error line that names the task and the reason.
7. Cleanup: with the option set, the removal of a workspace runs `rm -rf` as the agent user through agentCommand, with a 60 second timeout. Before the call, check that the path is a direct child of the resolved workspace root, that its name starts with pilot-ws-, and that it holds no symbolic-link component (filepath.EvalSymlinks equals the path). A path that fails the check is not removed; log one Error line. For a workspace, do not delete a branch in the daemon's repository: the daemon's repository never got the task branch, and a local branch with the same name belongs to another flow.
8. Sweeps: with the option set, CleanupOrphanedWorktrees and PruneOrphanedWorktreeForTask also handle the direct children of the workspace root whose name starts with pilot-ws-PROJECT- for the project they are called for (and, for the second, the TASK part of item 2). They apply the orphan rule that they apply to worktree directories today and remove through item 7. Log one Info line per removed workspace with the task ID. findOrphanedWorktreeDirs reports these directories too and removes nothing. A workspace of another project is never touched.
9. Preflight: PreflightRunAsUser also gets use_worktree and worktree_pool_size (change the signature and the callers in `cmd/pilot/main.go`). With the option set it fails closed when the workspace root does not exist, when its owner is not the agent user, when the daemon user cannot list it, when a directory that the agent user creates in it cannot be listed by the daemon user (create one probe directory through agentCommand and remove it), when use_worktree is false, or when worktree_pool_size is over 0. One Error line names the failed check.
10. Route rule of the git constructor in `internal/executor/git_command.go` (found by the review of the earlier leg; the earlier spec was wrong). (a) A directory takes the agent route when its cleaned path as given OR its resolved path is inside the workspace root; when the two disagree the command has its Err field set. A symbolic link inside a workspace that points at a repository of the daemon must never give a command that runs as the daemon user. (b) A `-C` or `--git-dir` argument that points inside the workspace root while the route directory is outside sets the Err field. A command of the daemon's repository that takes a workspace path as an argument (git worktree add, remove) is not used for a workspace. (c) A workspace root that cannot be resolved fails only the commands whose directory is inside the root as given; commands of other directories take the plain route. (d) The preflight refuses a workspace root that is `/`, the home directory of the daemon user, the data directory of the daemon, or a parent of one of them.
11. Tests, table-driven, no root needed, with the mock sudo helper of the earlier leg that runs the command, and stubRunAsGOOS. Real temporary repositories. For item 10: a link inside a workspace that points at a second repository gives Err and no git process runs in that repository; a `-C` argument into a workspace from an outside route directory gives Err; an unreadable parent of the root leaves a command of another directory on the plain route; each refused root of 10d fails the preflight. Two test corrections from the review after the merge of the earlier leg: the plain-route test calls real site functions (for example resolveGitRemote, checkGitClean and one `-C` site) and compares the command that they build, not a command written by hand in the test; the source test also fails on a git command that is built through the wrapper execCommandContext or through agentCommand outside git_command.go. Cases: createAgentWorkspace gives a repository with the task branch at the base commit for each of the three base-ref forms and for a fix continuation; no alternates file; an origin URL without a credential when the daemon's origin URL holds one; the temporary ref and the bundle file are gone after success and after a failure; a head that differs from the resolved SHA is an error and the workspace is removed; the three no-worktree cases of item 6 stop the run; cleanup refuses a path outside the root, a wrong prefix and a symbolic link, and deletes no branch in the daemon's repository when a branch with the task's name exists there; the sweep removes an orphan workspace of its project, keeps a workspace of another project, and keeps the worktree behaviour; each preflight failure of item 9.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentWorkspace|OrphanedWorktree|RunAsUser|Preflight'` passes; paste the output into the PR body.
- [ ] Remove the symbolic-link check of item 7 -> the cleanup test fails.
- [ ] Decide the route from the resolved path only -> the test with the link inside a workspace fails.
- [ ] Remove the SHA comparison of item 5 -> its test fails.
- [ ] Let the sweep ignore the PROJECT part of the name -> the other-project test fails.
- [ ] With the option empty, the worktree tests that exist today pass without a change to their assertions.
- [ ] Read the daemon's repository from the agent side (a fetch from its path) -> a test fails: the test makes that path unreadable for commands that run through the mock sudo helper.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Files that the daemon writes into the workspace (task document, Navigator files, the settings file) and the Navigator copy in both directions (next leg). The push, the secret gate, the PR creation, the recovery ref and the co-author strip (hand-over leg). Quality gates, workflow hooks, evidence commands and the lint fix. The GitHub token for the agent. The post-merge reviewer checkout. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG N2 — repo: qf-studio/pilot — title: fix(executor): run_as_user — the preflight refuses the option until the gates leg merges; corrections of the agent workspace from its review (off by default)

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]verifyAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]PreflightRunAsUser\(
probe: absent  qf-studio/pilot runAsIncompleteRefusa[l]

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). With run_as_user set, the workspace of a run is a clone that the agent user owns (createAgentWorkspace in `internal/executor/agent_workspace.go`), and a git command inside the workspace root runs as the agent user (`internal/executor/git_command.go`). A run-it review of that leg found the points below. This leg is one of three parts of the earlier workspace-files leg (issue 6385 ran into the one-hour task limit twice). Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. Interlock (from the run-it review of the workspace leg). With run_as_user set, daemon-user processes still run inside the agent-owned workspace until the gates leg merges: workflow hooks and quality gates in `internal/executor/runner.go`, the gate commands of `internal/quality/runner.go`, and an inline git command in `internal/quality/scope.go`. PreflightRunAsUser therefore fails closed for every host with one Error line that names the legs that are not merged (hand-over, gates). One unexported package variable switches the refusal off for tests. The config example says that the option cannot be set yet. The gates leg removes the refusal. Name the switch runAsIncompleteRefusal.
2. Corrections of the workspace leg, from the same review. (a) The workspace gets the ref refs/remotes/origin/BASE at the base commit, so that the commit count and the merge-base of a run work; a test runs both in a new workspace. (b) The check of `-C` and `--git-dir` in `internal/executor/git_command.go` reads every argument before the subcommand, also after an option that takes a value (--namespace, --attr-source, --config-env, -c); a test gives each form from an outside directory and gets Err. (c) The workspace sweep runs only in the daemon process at start and for a task that the daemon owns; the one-shot commands in `cmd/pilot/commands.go` do not remove workspaces. (d) An empty task ID is refused by createAgentWorkspace. (e) The check after the creation also fails when the objects directory of the workspace is a symbolic link; add tests that fail when the alternates check, the hard-link check or the refs check is removed. (f) The test TestGitCommandRouteInsideRoot resolves its temporary directory first, so that it passes on macOS.
3. Tests, table-driven, no root needed, with the mock sudo helper that runs the command (mockSudoExec) and stubRunAsGOOS: one test for each lettered point of item 2 and one for item 1.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'RunAsUser|AgentWorkspace|GitCommand|Preflight|OrphanedWorktree'` passes; paste the output into the PR body.
- [ ] Remove the refusal of item 1 -> its test fails.
- [ ] Remove the alternates check, the hard-link check or the refs check -> a test fails for each.
- [ ] `go test -count=1 ./internal/executor/ -run TestGitCommandRouteInsideRoot` passes on macOS and on Linux.
- [ ] With the option empty, the tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Files that the daemon writes into the workspace and the Navigator copy (next two legs). The push, the secret gate, the PR creation, the recovery ref and the co-author strip (hand-over leg). Quality gates, workflow hooks, evidence commands and the lint fix (gates leg). The GitHub token for the agent. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG N5 — repo: qf-studio/pilot — title: fix(executor): run_as_user — a test reads the production default of the preflight refusal, the workspace sweep waits for a passed preflight, the config example says that the option is refused

probe: present qf-studio/pilot runAsIncompleteRefusal
probe: present qf-studio/pilot func[[:space:]]CleanupOrphanedWorktreesKeepAgentWorkspaces\(
probe: absent  qf-studio/pilot runAsIncompleteRefusalDefaul[t][[:space:]]*(:?=|bool)

## Context

Navigator TASK-651. With run_as_user set, PreflightRunAsUser in `internal/executor/agent_command.go` refuses the option on every host until the hand-over leg and the gates leg merge; the package variable runAsIncompleteRefusal holds the switch and is true in production. A run-it review of that leg found three small points. This is a small leg: only the items below. With the option empty nothing changes.

1. No test reads the production default. TestMain in `internal/executor/gitenv_testmain_test.go` sets the variable to false for every test of the package, and the refusal test sets it to true itself. A change of the declaration to false passes every test.
2. The sweep of agent workspaces at daemon start (CleanupOrphanedWorktrees in `internal/executor/worktree.go`) starts `sudo … rm -rf` for a matching directory in the workspace root also when the preflight refused the option, and it takes the user name from the config without the name check of the preflight.
3. `configs/pilot.example.yaml` says that run_as_user must stay empty. It does not say that the startup check refuses the option on every host.

## Scope

1. A test that fails when the declaration of runAsIncompleteRefusal is not true. TestMain stores the value that the variable has before it changes it, in a test variable with the name runAsIncompleteRefusalDefault, and one test asserts that this value is true.
2. With the option set, the workspace sweep and the removal of a workspace start no process when the preflight did not pass. They log one Warn line that names the directory count. A test with the mock sudo helper shows zero sudo calls after a refused preflight and one call after a passed preflight.
3. The config example: the comment of run_as_user says that the startup check refuses the option on every host until the hand-over leg and the gates leg of TASK-651 merge, and that a run then fails with one Error line.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'RunAsUser|AgentWorkspace|OrphanedWorktree|Preflight'` passes; paste the output into the PR body.
- [ ] Set the declaration of runAsIncompleteRefusal to false -> the test of item 1 fails.
- [ ] Let the sweep run after a refused preflight -> the test of item 2 fails.
- [ ] With the option empty, the tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Files that the daemon writes into the workspace and the Navigator copy (other legs). The single-instance lock of the gateway form of pilot start. The push, the secret gate, quality gates and workflow hooks.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6397

### LEG N3 — repo: qf-studio/pilot — title: feat(executor): run_as_user workspace files — the daemon writes the task document, the Navigator files and the settings file through a helper that runs as the agent user (off by default)

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot runAsIncompleteRefusal
probe: absent  qf-studio/pilot func[[:space:]]agentWriteFil[e]\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). With run_as_user set, the workspace of a run is a clone that the agent user owns (earlier leg). The daemon user can read it and cannot write it. A write of the daemon into a directory that the agent controls is also a risk: the agent can put a symbolic link at the path and the daemon then writes its own file.

Places at origin/main 1e99ccbe0 where the daemon process writes into the workspace or reads through it:

- Before the session, in `internal/executor/runner.go`: CreateTaskDoc (near line 4993), maybeInitNavigator (near 4411), and the `.claude/settings.json` clean and merge with CleanStalePilotHooks and MergeWithExisting (near 5100 to 5165), which also sets includeCoAuthoredBy to false. A failure of the settings write is a Warn line today.
- After the session, in `internal/executor/runner.go` (near 7503 to 7530): ArchiveTaskDoc, UpdateFeatureMatrix, syncNavigatorIndex and the context marker. Also SimplifyModifiedFiles in `internal/executor/simplify.go`.
- Navigator copy into the workspace: EnsureNavigatorInWorktree in `internal/executor/worktree.go`. The source is the `.agent` directory of the daemon's repository, with files that git does not track; the agent user has no read access to that working tree.
- Navigator copy out of the workspace: CopyNavigatorBackFromWorktree in `internal/executor/worktree.go`. It checks the type of the last path element only, so a symbolic link in place of `.agent` or of a directory below it is followed. It runs `git -C WORKSPACE ls-files` (gitListPaths) and ignores a failure of that command.

The rule of this leg: with the option set, the daemon process creates, changes and removes no file inside the workspace root, and a read of the daemon from the workspace never leaves the workspace. With the option empty nothing changes. The option stays empty on every host until the hand-over leg and the gates leg merge.

This leg is one of three parts of the earlier workspace-files leg (issue 6385 ran into the one-hour task limit twice). Keep the change small: only the items below. The Navigator copy in both directions is the next leg.

## Scope

1. Helper, new file, plain text: internal/executor/agent_files.go. A function agentWriteFile(ctx, path, data, mode) and what the sites below need next to it (make a directory, rename, remove). With the option set and a path inside the workspace root (the path rule of the git constructor) the operation runs as the agent user through agentCommand, with the data on standard input and never in the argv. With the option empty, or outside the root, it is the os call of today. An error of agentCommand is returned; there is no fallback to a direct write.
2. Move every write of the Context list to the helper. Go through each function to its last file operation. A site whose result the session reads or the commit holds goes through the helper. A site that only serves the daemon can be skipped with one Info line when the option is set. The PR body has a table: site, file and line, helper or skipped, reason. The Navigator copy functions EnsureNavigatorInWorktree and CopyNavigatorBackFromWorktree are NOT part of this leg.
3. The settings file: with the option set, a failure of its write stops the run with one Error line. It is not a Warn line.
4. Evidence commands: the mutate and revert writes in `internal/executor/acceptance_evidence_run.go` belong to the gates leg. With the option set, this leg makes them return an error that names that leg.
5. Tests, table-driven, no root needed, with a fixture that shows the boundary: the workspace root is not writable for the test process, and the mock sudo helper makes it writable only while its command runs. Cases: each site of item 2 that uses the helper produces its file in the workspace; a direct os.WriteFile into the workspace fails in the fixture (control case); the settings failure stops the run; a path outside the workspace root makes the os call of today; with the option empty every site makes the os call of today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentFiles|AgentWriteFile|WorkspaceWrites|RunAsUser'` passes; paste the output into the PR body.
- [ ] Change one helper site back to os.WriteFile -> its test fails in the fixture.
- [ ] Make the settings failure a Warn line again -> its test fails.
- [ ] With the option empty, the tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The Navigator copy into and out of the workspace (next leg). The push, the secret gate, the PR creation, the recovery ref and the co-author strip (hand-over leg). Quality gates, workflow hooks, evidence commands and the lint fix (gates leg). The GitHub token for the agent. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG N6 — repo: qf-studio/pilot — title: fix(executor): run_as_user workspace files — a size limit for a read as the agent user, the settings clean-up failure stops the run, a new file keeps its mode, the unused rename helper is removed

probe: present qf-studio/pilot func[[:space:]]agentWriteFile\(
probe: present qf-studio/pilot func[[:space:]]agentReadFile\(
probe: present qf-studio/pilot func[[:space:]]runAgentFileScript\(
probe: absent  qf-studio/pilot agentFileReadLimi[t][[:space:]]*=

## Context

Navigator TASK-651. With run_as_user set, the daemon reads and writes files of the agent workspace through helpers in `internal/executor/agent_files.go` that run a small command as the agent user (agentWriteFile, agentReadFile, runAgentFileScript). A run-it review of that leg found the points below. This is a small leg: only the items below. With the option empty nothing changes.

1. A read as the agent user has no size limit: runAgentFileScript collects the standard output and the standard error of the command in memory without a bound. The agent can put a symbolic link to an endless file at a path that the daemon reads (the settings file, go.mod), and the daemon collects data for the whole timeout.
2. A failed write of CleanStalePilotHooks on the settings file of the workspace is a Warn line in `internal/executor/runner.go`. The earlier leg asked for a stop of the run for a failed settings write; only the two merge calls stop it.
3. agentWriteFile with the mode 0755 creates a file with mode 0644, so a restored executable file loses its execute bit.
4. agentRename uses `mv -f -T`, which only the GNU form of mv has. Its test fails on macOS. No production code calls agentRename.

## Scope

1. A limit for the read: a constant with the name agentFileReadLimit (4 MB) for the standard output of a read, and 64 KB for the standard error of every helper command. Over the limit the helper stops the command through the cancel path of agentCommand and returns an error that names the path and the limit. No partial content is returned.
2. With the option set, a failed write of CleanStalePilotHooks on the settings file of the workspace stops the run with one Error line, as the two merge calls do.
3. agentWriteFile gives a new file the requested mode: the permission bits of the result equal the requested bits and no more.
4. Remove agentRename and its test.
5. Tests with the fixture of the earlier leg: a read of a file over the limit returns the error and the mock records the kill; a read of a link to an endless source returns inside 5 seconds; a failed clean-up write stops the run; a file written with 0755 has 0755 and a file written with 0600 has 0600; the package tests pass on macOS.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentFiles|AgentWriteFile|WorkspaceWrites|RunAsUser'` passes on macOS and on Linux; paste the output into the PR body.
- [ ] Remove the limit of item 1 -> its test fails.
- [ ] Make the clean-up failure a Warn line again -> its test fails.
- [ ] With the option empty, the tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The Navigator copy into and out of the workspace (next leg). Other reads of the daemon from the workspace (the workflow file, the README and the environment context for the prompt): they belong to the gates leg. The push, the secret gate, quality gates and workflow hooks.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6410

### LEG N4 — repo: qf-studio/pilot — title: feat(executor): run_as_user Navigator copy — into the workspace as a tar stream to the agent user, out of it without a followed link at any path element (off by default)

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]agentWriteFile\(
probe: present qf-studio/pilot CopyNavigatorBackFromWorktree\(
probe: present qf-studio/pilot EnsureNavigatorInWorktree\(
probe: absent  qf-studio/pilot func[[:space:]]navigatorTarStream\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`, section 4.2). With run_as_user set, the workspace of a run is a clone that the agent user owns, and the daemon writes its own files there through the helper agentWriteFile (earlier legs). Two functions in `internal/executor/worktree.go` still work as the daemon user inside the workspace. EnsureNavigatorInWorktree copies the `.agent` directory of the daemon's repository into the workspace, with files that git does not track; the agent user has no read access to that working tree. CopyNavigatorBackFromWorktree copies Navigator files out of the workspace: it checks the type of the last path element only, so a symbolic link in place of `.agent` or of a directory below it is followed, there is a window between its check and its read, and it runs `git -C WORKSPACE ls-files` (gitListPaths) and ignores a failure of that command.

The rule of this leg: with the option set, the daemon process creates, changes and removes no file inside the workspace root, and a read of the daemon from the workspace never leaves the workspace. This leg is one of three parts of the earlier workspace-files leg (issue 6385 ran into the one-hour task limit twice). Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. Navigator copy into the workspace, option set: the daemon reads the files that EnsureNavigatorInWorktree selects today and sends them as one tar stream to a tar process that runs as the agent user in the workspace. The agent user needs no read access to the daemon's working tree, and the daemon writes no file into the workspace. Name the function that builds the stream navigatorTarStream.
2. Navigator copy out of the workspace, option set: the daemon reads data only. It opens each path element below the workspace without following a link (a check of every element, and O_NOFOLLOW on the file), accepts regular files only, and resolves each destination inside the Navigator directory of the daemon's repository (a path that leaves it is rejected). The list of tracked paths comes through the git constructor. A failure of that list stops the copy out with one Warn line that names the task; nothing is copied.
3. Tests, table-driven, no root needed, with the fixture of the earlier leg (the workspace root is writable only while a mock sudo command runs). Cases: the copy in gives the same files as today, with a file that git does not track; the copy out copies a regular file; the copy out copies nothing through a symbolic link at `.agent`, at a directory below it, and at a file, when each link points at a file outside the workspace; a failed tracked-path list copies nothing and logs the Warn line; with the option empty both functions make the calls of today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'NavigatorCopy|NavigatorTar|RunAsUser'` passes; paste the output into the PR body.
- [ ] Check only the last path element in the copy out -> the test with the link at `.agent` fails.
- [ ] With the option empty, the Navigator copy tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The push, the secret gate, the PR creation, the recovery ref and the co-author strip (hand-over leg). Quality gates, workflow hooks, evidence commands and the lint fix (gates leg). The GitHub token for the agent. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG N7 — repo: qf-studio/pilot — title: fix(executor): run_as_user Navigator copy — one descriptor of the workspace for the whole copy out, no followed link in the copy in, a size limit and a hard link refusal

probe: present qf-studio/pilot func[[:space:]]navigatorTarStream\(
probe: present qf-studio/pilot func[[:space:]]readWorkspaceFileNoFollow\(
probe: present qf-studio/pilot func[[:space:]]agentReadFile\(
probe: absent  qf-studio/pilot func[[:space:]]openWorkspaceRootFD\(

## Context

Navigator TASK-651. With run_as_user set, the Navigator copy works in two directions (files `internal/executor/navigator_agent_copy.go` and `navigator_agent_copy_unix.go`): the copy in sends the `.agent` directory of the daemon's repository as a tar stream to the agent user (navigatorTarStream), and the copy out reads files of the workspace without a followed link below the workspace. A run-it review found the points below. The first one lets the daemon read outside the workspace. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- The copy out opens the workspace directory itself with link following, and each directory walk and each file read starts again from the path string of the workspace. The agent user owns the workspace root, so a process of the agent user can rename its workspace and put a symbolic link in its place between two opens. The daemon then reads `.agent` files of any directory that it can read, and the next copy in gives them to the agent.
- The copy in reads the daemon's `.agent` with os.Stat and os.ReadFile, which follow a link. A symbolic link under `.agent` that came in through a merged PR sends its target (for example the config file of the daemon) to the agent. A FIFO there blocks the stream builder, and the wait for the builder does not end when the tar command times out.
- The copy out reads each file whole into memory with no limit on size or on the number of files.
- A hard link is a regular file and passes every check of the copy out.

## Scope

1. Copy out: a new function openWorkspaceRootFD opens the configured workspace root, then opens the workspace name below it with Openat and O_NOFOLLOW. The copy out holds that one descriptor from its start to its end; every directory walk and every file read starts from it, never from the path string (the function openWorkspaceNoFollow of today takes the path string of the workspace; change it to take the descriptor). A workspace path outside the workspace root, or a link at the workspace name, stops the copy out with the Warn line of today.
2. Copy in: navigatorTarStream uses Lstat and sends directories and regular files only. A symbolic link, a FIFO or another type is skipped with one Warn line that names the path. A regular file is opened with O_NOFOLLOW. The wait for the stream builder ends when the context of the tar command ends.
3. Copy out limits: a file over 4 MB (the limit of agentReadFile) is skipped with a Warn line; the read stops at the limit and does not load more. More than 2000 files stop the copy out with a Warn line; nothing more is copied.
4. Copy out: a regular file with a link count above 1 is skipped with a Warn line.
5. Tests, table-driven, no root needed: the workspace name is a link to an outside directory -> nothing is copied; the workspace is renamed and replaced by a link after the descriptor is open -> the read still comes from the original directory; a link and a FIFO in the daemon's `.agent` -> absent from the stream, and the builder returns; a file of 4 MB + 1 -> skipped; a hard link -> skipped; a directory or a FIFO at a file path of the workspace -> skipped (no test covers the regular-file rule today); with the option empty both functions make the calls of today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'NavigatorCopy|NavigatorTar|RunAsUser'` passes; paste the output into the PR body.
- [ ] Open the workspace by its path string for each file -> the rename test fails.
- [ ] Use os.Stat in navigatorTarStream -> the link test fails.
- [ ] Remove the regular-file check of the copy out -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The file mode of a copied file. A check of the kernel setting protected_hardlinks. The hand-over, the gates, the GitHub token. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6424

### LEG B2 — repo: qf-studio/pilot — title: fix(box): agent user setup script, third run — an undo that the agent user cannot block, no access of the agent user to a repository of the daemon, a sudo proof by behaviour, the mocked test passes on macOS, a CI job with real iptables and systemd

probe: present qf-studio/pilot ^prove_sudo_removed\(\)
probe: present qf-studio/pilot ^step_shared_read\(\)
probe: present qf-studio/pilot ^resolve_inside\(\)
probe: absent  qf-studio/pilot box-scripts-rea[l]:

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` creates the agent user, its directories, a sudo rule, firewall rules and a boot unit, and has an --undo mode. Its tests are `scripts/box/setup-agent-user_test.sh` (mocked commands from `scripts/box/testdata/mock-host-cmds.sh`, run by the job box-scripts of `.github/workflows/ci.yml`) and `scripts/box/setup-agent-user_container_test.sh` (a privileged container; no CI job runs it). No host has run the script. A review of the second run found four defects, and the firewall and the boot unit never ran against a real iptables and a real systemd.

1. The agent user can block --undo. The containment checks near line 258 run at top level, before the undo branch near line 1214, on paths that the agent user owns (the go, go/cache and go/mod directories of its home). A symbolic link at go/cache makes resolve_inside stop the script with exit 2. No undo step runs, and the sudoers file of the daemon user is not restored.
2. Shared read gives too much. share_repo gives the group the traverse bit on each working tree, so a file there with mode 0644 that git does not track (for example .env) can be read by the agent user. share_git_dir makes `.git/config` readable, and a remote URL there can hold a token.
3. The sudo proof reads text. prove_sudo_removed fails only on a NOPASSWD line of `sudo -n -l -U`. With `Defaults:USER !authenticate` and `USER ALL=(ALL) ALL` in another file the proof passes, and the daemon user still gets root without a password.
4. The mocked test fails on macOS (387 passed, 10 failed). `stat -f %Lp` of BSD drops the setgid bit, so a directory with mode 2770 reads as 770 in real_stat of the mock file and in mode_of of the test; the script then sees a change on every run.

Design change (TASK-651, 2026-11-15): the agent workspace gets its base commit as a bundle that the daemon sends, and the Navigator files as a tar stream. The agent user needs no access to a repository of the daemon.

## Scope

1. Undo depends only on what undo changes. With --undo, a path that the agent user or the daemon user can write never stops the script before the undo steps and never stops a step that restores root's files. Each undo step checks the paths that it changes. Test, mocked and real: go/cache is a symbolic link to a directory outside the data directory; --undo exits 0, the sudoers file of the daemon user is restored, and nothing outside the data directory changes.
2. No shared read. Remove the step step_shared_read with share_repo, share_git_dir, set_shared, the --repos-link option, the state file records of that step and undo_shared_read. Remove the state file when no other step uses it. Property, proven in the real test: as the agent user, a read of a file with mode 0644 in a working tree of the daemon fails, a read of `.git/config` of that repository fails, and a list of the repository directory fails.
3. The sudo proof tests behaviour. prove_sudo_removed runs `sudo -n true` as the daemon user and fails the step when the command succeeds, whatever the text of the rules is. Keep the dry-run skip. Test: the case with `Defaults:USER !authenticate` fails the step (mocked, and real in item 6).
4. macOS: real_stat and mode_of give the four-digit mode with the setgid bit on BSD stat (`%Mp%Lp`, without a leading zero difference to the GNU form). `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
5. Smaller defects from the same review. Refuse a --group or an agent user that is, or is a member of, a system group with privileges (root, wheel, sudo, adm, disk, docker, shadow); one line names the reason. Remove a temporary firewall chain that an earlier failed run left, also when the main chain is absent. Undo restores the backup of the sudoers file in which the root line of the daemon user is active, not only the newest backup.
6. A real test in CI. Add the job box-scripts-real to `.github/workflows/ci.yml` on ubuntu-latest (it has a real iptables, a real systemd and sudo for the runner user). It runs a new script (new file, plain text: scripts/box/setup-agent-user_real_test.sh) as root with test names for the daemon user, the agent user and the group, and a new data directory. The script refuses to start when the variable CI is not true. Checks:
   a. After the apply: the chains and the first OUTPUT jump exist for IPv4 and IPv6 (`iptables -C`); the unit is enabled; `systemd-analyze verify` accepts the unit file.
   b. Connections: put 169.254.169.254 on the loopback interface and start a listener on it and one on 127.0.0.1:9091. A curl as the agent user to each fails. The same curl as the daemon user succeeds. A curl as the agent user to another local listener succeeds.
   c. Boot: delete the jump and the chain, start the unit, and both are back; check b again.
   d. The agent user: `sudo -n true` fails; the properties of item 2; the daemon user runs a command as the agent user through the installed rule.
   e. The sudo proof of item 3 with a `Defaults` line in a second file.
   f. --undo with the link of item 1, then a second --undo: exit 0 both times, no chain, no unit, no user, no group, no sudoers file of the script.
7. Remove `scripts/box/setup-agent-user_container_test.sh` when the real test covers each of its checks; say so in the PR body.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] Run the containment checks before the undo branch again -> the test of item 1 fails.
- [ ] Let prove_sudo_removed read the rule text again -> the test of item 3 fails.
- [ ] Remove the rule for port 9091 from the script -> check 6b fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A run of the script on a real host (operator step). Go code. The network rules beyond the metadata address and port 9091. A rename of the state file by the daemon user.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6359

### LEG B3 — repo: qf-studio/pilot — title: fix(box): agent user setup script, fourth run, part 1 — an agent root owned by root, no root step in a directory of the agent user, the daemon's entries closed, a proof as the agent user

probe: present qf-studio/pilot ^step_daemon_private\(\)
probe: present qf-studio/pilot ^ensure_dir\(\)
probe: present qf-studio/pilot box-scripts-real:
probe: absent  qf-studio/pilot --agent-roo[t]

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user: the agent user and a group, a sudo rule from the daemon user to the agent user, the removal of the daemon user's root sudo, firewall rules for the agent user and a boot unit, with --undo. Tests: `scripts/box/setup-agent-user_test.sh` (mocked commands) and `scripts/box/setup-agent-user_real_test.sh` (real, as root, CI job box-scripts-real on ubuntu-24.04 with the classic sudo). No host has run the script. A run-it review of the third run found the points below; each one blocks a run on a real host.

Facts of the target host (Amazon Linux 2023, read on 2026-11-16): the data directory of the daemon is the mount point of a data volume, it belongs to the daemon user and has mode 0755, and its entries (the binary directory, the Go caches, the state directory, the repositories) have mode 0755; the home of the daemon user has mode 0700; the daemon starts by hand in a tmux session, no systemd unit starts it; sudo is 1.9.15; iptables is the nft variant; the daemon user is a member of the groups wheel, adm and docker.

1. Nothing proves that the agent user can enter its home. With a data directory below a 0700 home every root step passes and the agent user stays outside.
2. prove_sudo_removed runs only `sudo -n true` as the daemon user. A rule such as `USER ALL=(root) NOPASSWD: /usr/bin/dnf` passes it.
3. step_daemon_private looks at the top-level entries of the data directory once. The agent home and the workspace root are inside the data directory, between the entries of the daemon.
4. The sudo rule is installed before the firewall steps, so a failed firewall step leaves an agent user that the daemon can use and no firewall. A missing ip6tables is a skip. check_managed_dirs stops an apply before the firewall steps when the agent user put a link, a file or a FIFO at a managed directory.
5. ensure_dir tests the last path element for a symbolic link and then runs `install -d -o AGENT` as root inside a directory that the agent user owns. The agent user can swap the parent for a link between two calls.

This leg is one of two parts of the fourth run, so that each part stays inside the one-hour task limit. Keep the change small: only the items below. This part covers points 1, 3 and 5. Points 2 and 4 are the next leg.

## Scope

1. Nothing proves that the agent user can enter its home. With a data directory below a 0700 home every root step passes and the agent user stays outside.
2. prove_sudo_removed runs only `sudo -n true` as the daemon user. A rule such as `USER ALL=(root) NOPASSWD: /usr/bin/dnf` passes it.
3. step_daemon_private looks at the top-level entries of the data directory once. The agent home and the workspace root are inside the data directory, between the entries of the daemon.
4. The sudo rule is installed before the firewall steps, so a failed firewall step leaves an agent user that the daemon can use and no firewall. A missing ip6tables is a skip. check_managed_dirs stops an apply before the firewall steps when the agent user put a link, a file or a FIFO at a managed directory.
5. Real test additions: the case of context point 1 (a data directory below a 0700 directory fails the apply at the proof of item 4 with the path in the message); a file that the daemon user creates with umask 022 inside one of its entries after the apply cannot be read by the agent user.
6. The apply summary prints the three config values for the operator: run_as_user, run_as_group and run_as_workspace_dir (the workspaces directory of item 1).

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Create a Go cache directory with `install -d` as root again -> the test of item 2 fails or the mocked test shows the root call.
- [ ] Remove the proof of item 4 -> the test with the 0700 directory fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sudo proof, the order of the firewall steps, the warning for the groups of the daemon user and the undo changes (next leg). A run of the script on a real host (operator step). Go code: the copy of the daemon binary into the gh shim directory of a run, a firewall probe in the daemon preflight, the default of run_as_workspace_dir and the Go cache variables of the agent are another leg. The removal of the daemon user from a group.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6378

### LEG B5 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the mode record and the printed chmod lines accept only plain paths inside the data directory

probe: present qf-studio/pilot ^step_daemon_private\(\)
probe: present qf-studio/pilot ^record_modes\(\)
probe: present qf-studio/pilot [-]-agent-root\)
probe: absent  qf-studio/pilot ^plain_data_pat[h]\(\)

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` checks in step_daemon_private that the entries of the daemon's data directory are closed to other users. It changes no mode: it prints one `chmod` line for each entry, and record_modes writes the earlier mode and the resolved path of each entry to a record file that belongs to root. --undo reads the record and prints the `chmod` lines that put the earlier modes back. Tests: `scripts/box/setup-agent-user_test.sh` (mocked) and `scripts/box/setup-agent-user_real_test.sh` (real, CI job box-scripts-real). No host has run the script. This is a small leg: only the items below.

A run-it review found one defect that blocks a run on a host. The daemon user owns the data directory, so it controls the names of the entries and the targets of links there. The script writes the result of realpath for each entry into the record without a check, and the record has one entry on each line. A link whose target path holds a newline gives a second line, for example the mode 4755 and the path of a system binary. Undo then prints a `chmod 4755` line for that binary under the text that tells the operator to put the earlier modes back. The same class: an entry name with a newline forges a line in the apply output, because the descriptive part of that line is printed without quoting; and a link to a directory outside the data directory makes the apply demand a `chmod` on the target.

## Scope

1. One function with the name plain_data_path decides if a path can be recorded or printed: the resolved path is the data directory or is below it, and it holds only letters, digits, dot, underscore, hyphen and slash. Every place that records a path or prints a path in a `chmod` line or in a line about an entry uses it.
2. An entry that fails the function is not recorded and gets no `chmod` line. The apply fails with one line that names the entry with shell quoting and says that the operator must look at it by hand. A link in the data directory whose target is outside the data directory fails the function.
3. Undo prints a line of the record only when its path passes the function for the data directory of this run and its mode is three or four octal digits with no setuid, setgid or sticky bit. For each line that it skips it prints one line that says so, with the line number and no content of the line.
4. Every path that the script prints in a `chmod` line or in the descriptive text of such a line is shell-quoted.
5. Tests in the mocked suite: a link whose target path holds a newline and a second record line (the case of the Context) gives no forged line in the record and none in the undo output; an entry name with a newline gives no second line in the apply output; a link to a directory outside the data directory fails the apply and names the entry; a path with a space is printed quoted; a record line with the mode 4755 is skipped by undo. One real test case: the first of these, as root on the CI host.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Print a `chmod` path without shell quoting -> a test fails.
- [ ] Remove the line filter of undo -> a test fails.
- [ ] Record the result of realpath without the check -> the test of the Context case fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sudo proof, the order of the firewall steps, the warning for the groups of the daemon user and the undo of other steps (next leg). ACLs and groups of the agent user other than the shared group. A run of the script on a real host. Go code.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6401

### LEG B6 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the printed chmod lines run as the daemon user, a link to the data directory or the agent root is refused, the path check does not depend on the locale

probe: present qf-studio/pilot ^plain_data_path\(\)
probe: present qf-studio/pilot ^step_daemon_private\(\)
probe: absent  qf-studio/pilot ^chmod_as_daemon_lin[e]\(\)

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` checks in step_daemon_private that the entries of the daemon's data directory are closed to other users. It changes no mode: it prints one `chmod` line for each entry for the operator, and --undo prints the lines that put the earlier modes back from a record that belongs to root. The function plain_data_path decides which paths can be recorded and printed. Tests: `scripts/box/setup-agent-user_test.sh` (mocked) and `scripts/box/setup-agent-user_real_test.sh` (real, CI job box-scripts-real). No host has run the script. This is a small leg: only the items below.

A run-it review found these points.

1. The printed lines follow a link. The daemon user owns its entries, so it can replace an entry with a symbolic link after the script printed the line. The operator runs the line as root. On apply, root removes permission bits on a path that the daemon user chose. On undo it is worse: the daemon user sets an entry to mode 777 before the apply, the record holds 777 for it, the daemon user replaces the entry with a link to a system file, and the printed `chmod 777` makes that file writable for all users.
2. A link in the data directory that resolves to the data directory itself gives a `chmod o-rwx` line for the data directory, which removes the traverse that the agent user needs. A link that resolves to the agent root passes in the same way.
3. The character class of plain_data_path depends on the locale of the shell.
4. One line of the agent-proof step prints an entry path without the function.
5. Three correct behaviours have no test: a link to a sibling directory whose name starts with the name of the data directory; a record line whose path has a `..` part; an undo with a data directory that differs from the one of the apply.

## Scope

1. One function with the name chmod_as_daemon_line builds every printed `chmod` line, for apply and for undo. The line runs chmod as the daemon user (through runuser), so that the command can change only what the daemon user owns. The text before the lines tells the operator to run them as root as they are.
2. An entry that resolves to the data directory itself or to the agent root gets no `chmod` line. The apply fails with one line that names the entry, quoted, for a look by hand.
3. The script sets LC_ALL=C before any path check.
4. Every line that prints an entry path uses the quoting function of the script, also in the agent-proof step.
5. Tests in the mocked suite: every printed chmod line of apply and of undo starts with the runuser form; the two cases of item 2; the three cases of Context point 5. One real test case: an entry of the daemon is set to 777 before the apply, the apply is run, the entry is replaced with a link to a file of root, the printed undo line is run as root, and the file of root keeps its mode.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Print one chmod line without the runuser form -> a test fails.
- [ ] Accept a link to the data directory -> a test fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sudo proof, the order of the firewall steps, the warning for the groups of the daemon user and the undo of other steps (next leg). ACLs. A run of the script on a real host. Go code.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6409

### LEG B4 — repo: qf-studio/pilot — title: fix(box): agent user setup script — a sudo proof that also reads the rules, a warning for a daemon user in a privileged group

probe: present qf-studio/pilot ^prove_sudo_removed\(\)
probe: present qf-studio/pilot ^is_privileged_group\(\)
probe: present qf-studio/pilot ^chmod_as_daemon_line\(\)
probe: absent  qf-studio/pilot -ll[[:space:]]-U[[:space:]]["$]

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user: the agent user and a group, a sudo rule from the daemon user to the agent user, the removal of the daemon user's root sudo, firewall rules for the agent user and a boot unit, with --undo. Tests: `scripts/box/setup-agent-user_test.sh` (mocked commands) and `scripts/box/setup-agent-user_real_test.sh` (real, as root, CI job box-scripts-real on ubuntu-24.04 with the classic sudo). No host has run the script. A run-it review of the third run found the points below; each one blocks a run on a real host.

Facts of the target host (Amazon Linux 2023, read on 2026-11-16): the data directory of the daemon is the mount point of a data volume, it belongs to the daemon user and has mode 0755, and its entries (the binary directory, the Go caches, the state directory, the repositories) have mode 0755; the home of the daemon user has mode 0700; the daemon starts by hand in a tmux session, no systemd unit starts it; sudo is 1.9.15; iptables is the nft variant; the daemon user is a member of the groups wheel, adm and docker.

1. Nothing proves that the agent user can enter its home. With a data directory below a 0700 home every root step passes and the agent user stays outside.
2. prove_sudo_removed runs only `sudo -n true` as the daemon user. A rule such as `USER ALL=(root) NOPASSWD: /usr/bin/dnf` passes it.
3. step_daemon_private looks at the top-level entries of the data directory once. The agent home and the workspace root are inside the data directory, between the entries of the daemon.
4. The sudo rule is installed before the firewall steps, so a failed firewall step leaves an agent user that the daemon can use and no firewall. A missing ip6tables is a skip. check_managed_dirs stops an apply before the firewall steps when the agent user put a link, a file or a FIFO at a managed directory.
5. ensure_dir tests the last path element for a symbolic link and then runs `install -d -o AGENT` as root inside a directory that the agent user owns. The agent user can swap the parent for a link between two calls.

Earlier legs did points 1, 3 and 5 and added the agent root. This leg covers point 2 only, so that it stays inside the one-hour task limit; the next leg covers point 4. Keep the change small: only the items below.

## Scope

1. Sudo proof, two parts, both needed. Behaviour: as today. Rules: read `sudo -ll -U DAEMON_USER`; every entry must name the agent user as the only run-as user (and no run-as group other than the shared group); an entry without a run-as line, a line that the parser does not know, or a count of entries that differs from the count of run-as lines fails the step. Print the entry that fails. Dry run prints the command.
2. Tests of item 1. Mocked test: a mock sudo prints a rule list for each case (a rule for one command as root, an entry without a run-as line, an unknown line, only the agent rule). Real test, three cases in a second sudoers file: a NOPASSWD rule for one command as root; a rule through a group of the daemon user; only the agent rule (passes).
3. The daemon user. When the daemon user is a member of a group of the privileged list (the list that the script has for the agent user), the apply prints one WARNING block that names each group and says that this membership is a path to root; it does not fail. The summary repeats the line. Mocked test: one case with a group of the list, one case without.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Let prove_sudo_removed skip the rule list -> the case with the rule for one command fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The order of the firewall steps and the sudo rule, the ip6tables rule, undo (next leg). A run of the script on a real host (operator step). Go code. The removal of the daemon user from a group.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6378

### LEG B7 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the firewall before the sudo rule, a missing ip6tables fails the apply, an undo that goes on and stops the agent processes

probe: present qf-studio/pilot ^prove_sudo_removed\(\)
probe: present qf-studio/pilot ^step_firewall_ipv6\(\)
probe: present qf-studio/pilot ^check_rule_list\(\)
probe: absent  qf-studio/pilot conf[./]all[./]disable_ipv[6]

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user: the agent user and a group, a sudo rule from the daemon user to the agent user, the removal of the daemon user's root sudo, firewall rules for the agent user and a boot unit, with --undo. Tests: `scripts/box/setup-agent-user_test.sh` (mocked commands) and `scripts/box/setup-agent-user_real_test.sh` (real, as root, CI job box-scripts-real on ubuntu-24.04 with the classic sudo). No host has run the script. A run-it review found the points below; each one blocks a run on a real host.

- The sudo rule is installed before the firewall steps, so a failed firewall step leaves an agent user that the daemon can use and no firewall. A missing ip6tables is a skip. check_managed_dirs stops an apply before the firewall steps when the agent user put a link, a file or a FIFO at a managed directory.
- Undo stops at the first failed step, and processes of the agent user stay alive when the firewall goes away.

Facts of the target host (Amazon Linux 2023): sudo is 1.9.15; iptables is the nft variant; no systemd unit starts the daemon. Keep the change small: only the items below.

## Scope

1. Order and firewall. The firewall steps and the boot unit run before the sudo rule; a failed firewall step leaves no sudo rule. A missing ip6tables fails the step, unless IPv6 is off on the host (the kernel setting disable_ipv6 of all interfaces is 1). A link, a file or a FIFO at a directory that the agent user owns never stops the firewall steps. Real test: with ip6tables hidden from PATH the apply fails and no sudo rule file exists.
2. Undo. Undo goes on after a failed step, runs every remaining step, prints which steps failed and exits non-zero. Before it removes the firewall it stops every process of the agent user (TERM, then KILL after 5 seconds) and says how many. Mocked test: one step fails, the later steps run, the exit code is not zero.
3. Real test addition: a connection check over IPv6 loopback to port 9091 as the agent user (refused) and as the daemon user (accepted).

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Run the sudo rule step before the firewall steps -> the test of item 1 fails.
- [ ] Stop undo at the first failed step -> its test fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sudo proof (earlier leg). A run of the script on a real host (operator step). Go code. The network rules beyond the metadata address and port 9091.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6378

### LEG B9 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the sudo rule list is read before the first change of a sudoers file, tests for a run-as list and for a failed rule list command

probe: present qf-studio/pilot ^prove_sudo_removed\(\)
probe: present qf-studio/pilot ^step_cloud_init_sudoers\(\)
probe: present qf-studio/pilot conf[./]all[./]disable_ipv6
probe: absent  qf-studio/pilot ^precheck_sudo_rules\(\)

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user. The step step_cloud_init_sudoers removes the root sudo of the daemon user: it comments out the cloud-init line and then calls prove_sudo_removed, which reads `sudo -ll -U DAEMON_USER` and fails when an entry names a run-as user other than the agent user. A run-it review of the last change found the points below. Keep the change small: only the items below.

- The daemon user of the target host is a member of the group wheel. A line such as `%wheel ALL=(ALL) ALL` in the main sudoers file shows as a run-as user ALL, and the proof fails. That is correct. But the script has already changed the cloud-init file at that time and does not restore it. The host stays with a changed file and a failed step. The WARNING block for the groups says "not a failure".
- No test fails when the parser accepts a run-as list that contains the agent user together with another user.
- No test fails when the script ignores a non-zero exit code of the rule list command.

## Scope

1. A function precheck_sudo_rules that runs before the first step that changes a sudoers file. It reads the rule list with the parser of today and ignores only the entries that the script removes by itself (the cloud-init rule of the daemon user). Any other entry that the proof would refuse fails the apply at that point, before a change, and the output names the entry and says that the operator must remove it or the group membership first. Dry run prints the command.
2. The WARNING block for a privileged group of the daemon user says that a sudo rule for that group stops the apply at the check of item 1.
3. Mocked tests: a rule through a group of the daemon user -> the apply fails and the cloud-init file has no change; only the cloud-init rule and the agent rule -> the apply goes on; a run-as list with the agent user and root, in both orders -> the proof fails; the rule list command exits non-zero with output that looks valid -> the proof fails.
4. Real test: with a group rule in a second sudoers file the apply fails and the cloud-init file is the same byte for byte.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Remove the call of precheck_sudo_rules -> the test "the cloud-init file has no change" fails.
- [ ] Let the parser accept a list that contains the agent user -> the run-as list test fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The firewall order and undo (earlier leg). The removal of the daemon user from a group. A run of the script on a real host (operator step). Go code.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6421

### LEG B10 — repo: qf-studio/pilot — title: fix(box): agent user setup script — a failed firewall step on a second run removes the sudo rule, undo removes the sudo rule first, a failed process list fails the undo step

probe: present qf-studio/pilot ^run_plan\(\)
probe: present qf-studio/pilot ^step_sudoers_rule\(\)
probe: present qf-studio/pilot ^precheck_sudo_rules\(\)
probe: absent  qf-studio/pilot ^remove_sudo_rule_on_failure\(\)

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user. An apply runs its steps through run_plan: the firewall steps and the boot unit come before the sudo rule (step_sudoers_rule, file `90-pilot-agent-runas`), and the plan stops at the first failed step. Undo runs every step and stops the processes of the agent user before it removes the firewall. A run-it review of the last change found the points below. The first one blocks a run on a real host. Keep the change small: only the items below.

- Second run. When an earlier apply installed the sudo rule and a later apply fails at a firewall step, run_plan stops and the old rule file stays. The daemon can then start the agent user with no complete firewall. Seen in two ways: the firewall rules are gone and iptables is missing; or the first apply ran with IPv6 off, and IPv6 is on at the second apply with no ip6tables.
- Undo removes the sudo rule as its last step. Between the stop of the agent processes and the removal of the rule, the daemon can start a new agent process, and that process stays when the firewall is gone.
- The count of agent processes is read with `pgrep ... | grep -c . || true`. A pgrep that is missing or fails reads as "no process": the step is skipped, the firewall is removed, and undo exits 0.

## Scope

1. A function remove_sudo_rule_on_failure. When an apply fails at a step before the sudo rule step and the sudo rule file exists, the script removes the file, says so in one line, and lists it in the summary. The exit code stays non-zero. Dry run changes nothing and prints the command.
2. Undo order: the sudo rule step runs first, before the stop of the agent processes. The other steps keep their order.
3. The stop step: a pgrep that is not found, or that exits with a code other than 0 and 1, fails the step with a line that says so. Exit code 1 means no process. The firewall steps still run (undo goes on) and undo exits non-zero.
4. Tests. Mocked: a full apply, then a second apply with a failed IPv4 firewall step -> exit 1, no sudo rule file, the removal line is present; the same for a failed IPv6 step; undo order (the line of the sudo rule removal comes before the first pkill line); pgrep exit 127 and exit 2 -> the step fails and undo exits non-zero; pgrep exit 1 -> skipped as today. Real: a full apply, then an apply with ip6tables hidden from PATH -> no sudo rule file exists.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Remove the call of remove_sudo_rule_on_failure -> the second-run test fails.
- [ ] Put the sudo rule step last in undo -> the order test fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A loop for a process that forks fast. The IPv6 decision for a kernel with IPv6 compiled out. A check of --agent-user in undo. A run of the script on a real host (operator step). Go code.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6426 and PR#6425

### LEG B8 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the quote function escapes every byte outside printable ASCII, a test for the locale line, no chmod line for an entry of root

probe: present qf-studio/pilot ^shq\(\)
probe: present qf-studio/pilot ^chmod_as_daemon_line\(\)
probe: present qf-studio/pilot export[[:space:]]LC_ALL=C
probe: absent  qf-studio/pilot shq_c1_case\([)]

## Context

Navigator TASK-651. The script `scripts/box/setup-agent-user.sh` prepares a host for an agent that runs as a second Unix user. It prints paths for the operator through the quote function shq, and it prints chmod lines that run as the daemon user (chmod_as_daemon_line). A run-it review of the last change found the points below. Keep the change small: only the items below.

- The script sets `LC_ALL=C`. In that locale the class `[[:cntrl:]]` does not match the bytes 0x80 to 0x9f, so shq prints a name with the byte pair of U+009B raw inside single quotes. A terminal can read that as a control sequence. The agent user controls such names (a process name, an open path).
- No test fails when the line `export LC_ALL=C` is removed.
- A top-level entry of the data directory that root owns (for example the parent of a nested --agent-root) gets a chmod line that runs as the daemon user. That line fails, so the apply can never pass.
- When every failed entry is a refused link, the output still ends with "run each chmod above and run again" though no chmod line was printed.

## Scope

1. shq: a string with any byte outside the range space to tilde goes to the escaped form (`printf %q` or an equal form that works on bash 3.2 and bash 5). Mocked test, in a test function with the name shq_c1_case: a name with the bytes 0xC2 0x9B and a name with the byte 0x9B alone give output with printable ASCII only.
2. A test for the locale line: the test starts the script with `LC_ALL` set to a UTF-8 locale and reads the locale that a mocked command sees; it must be C. Remove the export line -> this test fails.
3. An entry of root. When a top-level entry of the data directory does not belong to the daemon user, the script prints no chmod line and no record for it. It prints one line that names the entry and its owner and says that the operator must look at it by hand; the apply fails. Mocked test: one case.
4. The closing sentence names the chmod lines only when the script printed one or more chmod lines. Mocked test: only refused entries -> the sentence is absent.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary lines of both test scripts into the PR body.
- [ ] `./scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Put the `[[:cntrl:]]` test back into shq -> the test shq_c1_case fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sudo proof, the firewall order, undo (other legs). A run of the script on a real host (operator step). Go code.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6415

### LEG B11 — repo: qf-studio/pilot — title: fix(box): agent user setup script — undo keeps the firewall when the sudo rule removal fails, the cloud-init file is restored last, a true removal message, a reason line for an empty process list

probe: present qf-studio/pilot ^undo_sudoers_rule\(\)
probe: present qf-studio/pilot ^remove_sudo_rule_on_failure\(\)
probe: present qf-studio/pilot ^run_every\(\)
probe: present qf-studio/pilot shq_c1_case\([)]
probe: absent  qf-studio/pilot ^undo_firewall_allowed\(\)

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. `scripts/box/setup-agent-user.sh` sets up a host for run_as_user. Its safety rule: the sudo rule file (the daemon user may run commands as the agent user) is never in place while the firewall rules for the agent user are not. `--undo` runs its steps through run_every in this order: sudoers-rule, cloud-init-sudoers, agent-processes, firewall-unit, firewall-ipv4, firewall-ipv6. A failed step does not stop the undo. A run-it review of the last change found the points below (fake host, each mocked command made to fail in turn). Keep the change small: only the items below, only in `scripts/box/setup-agent-user.sh` and `scripts/box/setup-agent-user_test.sh`.

- Undo with a failed `rm` of the sudo rule file: the step sudoers-rule fails, the undo goes on and removes the firewall. End state: rule file present, no firewall. The script itself breaks its rule.
- The step cloud-init-sudoers restores the root line of the daemon user (for example `ec2-user ALL=(ALL) NOPASSWD:ALL`) as the second step, before the agent processes stop. With that line the daemon user can run a command as the agent user again, so the text in the header ("the daemon starts no new agent process after it") is false.
- remove_sudo_rule_on_failure prints "so that the daemon cannot start the agent user without the complete firewall". That is false while an active root line of the daemon user exists in the cloud-init file (after `--keep-daemon-sudo`, or after an apply that failed at the cloud-init step).
- In undo_agent_processes a process list command that exits 0 with no output fails the step with no reason line. For exit 2, 3 and 127 the reason is on standard error only; the summary line of the step has no note.
- No test covers: the branch "sudo rule NOT removed ... remove it by hand" of remove_sudo_rule_on_failure; a failure of the process list command on the second and the third count; a rule file that is a dangling link.

## Scope

1. Undo, guard: a function with the name undo_firewall_allowed. When the step sudoers-rule failed and the rule file still exists (also as a dangling link), the three firewall steps are not run. Each of them gets a summary line "not run: the sudo rule is still in place", the undo prints one line that says to remove the rule file by hand and to run `--undo` again, and the exit status is 1. When the rule file is gone, the firewall steps run as today. A dry run keeps the behaviour of today (it removes nothing, so it prints the whole plan).
2. Undo, order: sudoers-rule, agent-processes, firewall-unit, firewall-ipv4, firewall-ipv6, cloud-init-sudoers. The restore of the cloud-init file is the last step and runs also when a firewall step failed or was not run. Correct the comment above the undo plan and the header text. The undo prints one line before the first step: "stop the daemon before --undo: a daemon that runs can start an agent process until the sudo rule is removed".
3. Message of remove_sudo_rule_on_failure: when the cloud-init file has an active line for the daemon user, the line says that the rule file is removed and that the daemon user still has sudo through the cloud-init file, with the path of that file. In the other case the text of today stays.
4. undo_agent_processes: a process list command that exits 0 with no output fails the step with the reason "pgrep -u AGENT exited 0 with no process id". For each failure of the process list command the reason is also the note of the summary line of the step.
5. Tests in `setup-agent-user_test.sh`: undo with a failed `rm` of the rule file -> exit 1, the rule file exists, the call log has no iptables or ip6tables call that deletes or flushes and no call that removes the unit, the three "not run" lines are in the summary, and the cloud-init file is restored; the order of the undo steps in the call log (the restore of the cloud-init file after the last firewall call); the "stop the daemon" line; the two texts of item 3; an empty process list with exit 0; a process list failure on the second count and on the third count (after TERM, after KILL); a second apply that fails at the firewall with a failed `rm` -> the summary says "sudo rule NOT removed" and "remove it by hand"; the same with a rule file that is a dangling link -> the link is gone.

## Acceptance

- [ ] The jobs box-scripts and box-scripts-real pass on the PR; paste the summary line of each test script into the PR body.
- [ ] `bash scripts/box/setup-agent-user_test.sh` passes on macOS and on Linux with the same count.
- [ ] Remove the guard of item 1 -> the test "undo with a failed rm" fails.
- [ ] Put cloud-init-sudoers back in the second place -> the order test fails.
- [ ] ShellCheck passes for `scripts/box`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A stop of the daemon by the script. A signal trap. An apply that stops before the plan (exit 2). The removal of the rule on a failed step while the firewall is complete. The early check of the sudo rules and the dry run. Any Go file.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6458

### LEG P1 — repo: qf-studio/pilot — title: feat(executor): run_as_user host layout — a data directory option, a workspace default below the agent root, a mode check of the daemon's entries, Go cache paths in the agent home (off by default)

probe: present qf-studio/pilot func[[:space:]]refusedRunAsWorkspaceRoot\(
probe: present qf-studio/pilot func[[:space:]]\(s[[:space:]]\*runAsSettings\)[[:space:]]envBase\(
probe: present qf-studio/pilot func[[:space:]]fileOwnerUID\(
probe: absent  qf-studio/pilot func[[:space:]]checkRunAsDataDirModes\(

## Context

Navigator TASK-651. With run_as_user set, the agent runs as a second Unix user. The host script `scripts/box/setup-agent-user.sh` builds this layout: a data directory of the daemon (script default `/var/lib/pilot`), and inside it an agent root that root owns, with `home` and `workspaces` below it. The script closes every other top-level entry of the data directory to other users. The Go side does not know this layout:

- The default of `run_as_workspace_dir` is the constant defaultRunAsWorkspaceDir in `internal/executor/git_command.go`, `/var/lib/pilot/agent-workspaces`. The script creates `/var/lib/pilot/agent/workspaces`.
- The Go code has no data directory setting, and nothing checks the modes of the daemon's entries. The daemon creates new entries at run time with mode 0755 (logs, state files), and such an entry is open to the agent user.
- The allowlist runAsAllowedEnv in `internal/executor/agent_command.go` passes GOCACHE and GOMODCACHE with the paths of the daemon, which the agent user cannot write. The script prepares `go`, `go/cache` and `go/mod` below the agent home, and nothing points Go at them.

The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. A config field `run_as_data_dir` next to `run_as_workspace_dir` in ClaudeCodeConfig, default `/var/lib/pilot`. Change the default of `run_as_workspace_dir` to `/var/lib/pilot/agent/workspaces`. Update the comments of both fields and the text in `configs/pilot.example.yaml` (no dollar sign followed by a word in that text: the config loader expands it).
2. A function checkRunAsDataDirModes. Rule, the same as the script: the data directory gives the group and other users no read and no write bit; each top-level entry gives other users no bit, and gives its group no bit when the group is `run_as_group`. The entry that holds the workspace root is skipped. For a symbolic link the check reads the target. A data directory that does not exist fails the check. The result names the first entry that fails and its mode. Read the owner group with the pattern of fileOwnerUID (a Unix file and a stub for other systems).
3. PreflightRunAsUser runs the check after the workspace root check and refuses the option on a failure. The creation of an agent workspace runs the check again before each run and fails the run on a failure, because the preflight result is kept for the life of the process.
4. Go paths. With the option set, envBase adds GOCACHE=HOME/go/cache, GOMODCACHE=HOME/go/mod and GOPATH=HOME/go, where HOME is the home of the agent user. A value of the daemon for these three names never reaches the agent.
5. Tests, table-driven, no root needed, with temporary directories: the data directory with mode 0755 fails and with 0711 passes; an entry with mode 0755 fails and with 0700 passes; an entry with group bits and the shared group fails; the entry that holds the workspace root is skipped; a link to an open directory fails; a missing data directory fails; the preflight refuses on a failed check; the agent environment has the three Go paths and not the daemon's values; with the option empty the environment and the defaults of today stay.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsDataDir|RunAsUser'` passes; paste the output into the PR body.
- [ ] Skip the per-run call of the check -> its test fails.
- [ ] Let a daemon value of GOCACHE pass -> the environment test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The firewall probe (next leg). The gh shim and reads from the workspace (other leg). The option `run_as_env` (gates leg). A change of mode by the daemon: the check only reads. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo

### LEG P2 — repo: qf-studio/pilot — title: feat(executor): run_as_user preflight — a firewall probe as the agent user for the metadata address and the gateway port (off by default)

probe: present qf-studio/pilot func[[:space:]]sudoNoTerminal\(
probe: present qf-studio/pilot func[[:space:]]agentCommandAs\(
probe: present qf-studio/pilot func[[:space:]]checkRunAsDataDirModes\(
probe: absent  qf-studio/pilot func[[:space:]]checkRunAsFirewall\(

## Context

Navigator TASK-651. With run_as_user set, the agent runs as a second Unix user. The host script installs firewall rules for that user: no connection to the metadata address 169.254.169.254 and no connection to the gateway port of the daemon on loopback (the script uses 9091 and rejects with a TCP reset). The daemon starts by hand in a tmux session, so no systemd unit can require the firewall. Nothing in Go proves that the rules are active: PreflightRunAsUser in `internal/executor/agent_command.go` checks sudo, the home and the workspace root only.

Two facts shape the probe. The preflight runs before the gateway starts, so nothing listens on the gateway port at that time, and a closed port and a reject rule give the same error. A probe tool that is absent gives an error too, which looks like a blocked connection. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below.

## Scope

1. A function checkRunAsFirewall, held in a variable so that a test can replace it (the pattern of runAsWorkspaceCheck). It runs each connection as the agent user through agentCommandAs with the agent environment: `bash -c` with a redirect to `/dev/tcp/HOST/PORT`, with a limit of 3 seconds for each connection.
2. Control connection. The daemon opens a listener on 127.0.0.1 with a free port. The connection of the agent user to it must succeed. A failure means that the probe tool does not work, and the check fails with that reason.
3. Metadata address. The connection of the agent user to 169.254.169.254 port 80 must fail.
4. Gateway port. PreflightRunAsUser gets the gateway port of the config (a new parameter or a field without a YAML name, set where the caller has the config). The daemon opens a listener on 127.0.0.1 with that port for the time of the probe and closes it after; the connection of the agent user must fail. When the daemon cannot open the listener, the check fails and names the port. With port 0 this part is skipped with one Warn line.
5. PreflightRunAsUser runs the check as the last probe and refuses the option on a failure; the refusal names the address or the port that the agent user reached. Tests with the fixture that runs commands for real (mockSudoAgent): the control connection fails -> refusal; a listener at the gateway port that the agent reaches -> refusal that names the port; a blocked gateway port (a replaced dial function or the replaced check variable) -> pass; port 0 -> the Warn line; with the option empty no probe runs.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsFirewall|RunAsUser'` passes; paste the output into the PR body.
- [ ] Remove the control connection -> the test with the broken probe tool fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The IPv6 metadata address. A change of the firewall or of the host script. A probe at each run. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo

### LEG P3 — repo: qf-studio/pilot — title: feat(executor): run_as_user — the gh shim runs a copy of the daemon binary for each run, the workflow file and the project context are read as the agent user (off by default)

probe: present qf-studio/pilot func[[:space:]]setupGhGuardShim\(
probe: present qf-studio/pilot func[[:space:]]agentReadFile\(
probe: present qf-studio/pilot func[[:space:]]loadProjectContext\(
probe: absent  qf-studio/pilot func[[:space:]]installGhGuardBinary\(

## Context

Navigator TASK-651. With run_as_user set, the agent runs as a second Unix user in a workspace that it owns, and the host script closes the daemon's binary directory to that user. Two things on the Go side do not fit that host:

- setupGhGuardShim in `internal/executor/ghguard_spawn.go` writes a `gh` script that runs the daemon binary (the path from os.Executable) with the subcommand gh-guard. The agent user cannot read that binary on a prepared host, so each `gh` call of the agent fails. After a self-upgrade the path of a running binary on Linux ends in " (deleted)".
- The daemon reads two files of the workspace as the daemon user and follows a link that the agent put there: `.pilot/workflow.yaml` (Load in `internal/executor/workflow/workflow.go`, called from `runner.go`; its content goes into the prompt and the hook list) and `.agent/DEVELOPMENT-README.md` (loadProjectContext in `internal/executor/prompt_builder.go`; its content goes into the prompt). A link to a file of the daemon moves that file into the prompt.

Founder call 11-16: the daemon binary reaches the gh shim as a copy for each run. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. A function installGhGuardBinary. With the option set, setupGhGuardShim copies the running binary into the shim directory of the run (source `/proc/self/exe`, and the path from os.Executable when that does not exist), with the mode and the group that applyGuardPerm gives the `gh` script. The `gh` script runs the copy. A failed copy fails the set-up of the shim. The clean-up of today removes the copy with the directory.
2. Workflow file. Split Load into the read and a function that parses given bytes with the path for error text. With the option set and a workspace as the execution path, the runner reads the file through agentReadFile and parses the bytes. A file that does not exist gives the result of today.
3. Project context. With the option set and a path inside the workspace root, loadProjectContext reads through agentReadFile. A read error gives the empty context of today and one Warn line.
4. Tests, table-driven, no root needed, with the fixture of the earlier legs (mock sudo): with the option set the shim directory holds an executable copy and the script names it, and with the option empty the script names the daemon binary as today; a link at the workflow file and at the README that points at a file outside the workspace is read by the helper command as the agent user, not by the daemon process (the mock sudo log shows the read); a workflow file over the read limit of the helper stops with an error; with the option empty both reads make the calls of today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'GhGuard|Workflow|ProjectContext|RunAsUser'` passes; paste the output into the PR body.
- [ ] Read the workflow file with os.ReadFile when the option is set -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The read of the memory graph in `git.go` (after the hand-over leg). Checks that only test if a path exists. The token file for the gh shim (GitHub access leg). Workflow hooks, gates, evidence commands (gates leg). Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo

### LEG P4 — repo: qf-studio/pilot — title: fix(executor): run_as_user host layout — the workspace root cannot be the data directory or its parent, tests for the mode masks, the group of the gh shim binary copy and a failed copy

probe: present qf-studio/pilot func[[:space:]]checkRunAsDataDirModes\(
probe: present qf-studio/pilot func[[:space:]]installGhGuardBinary\(
probe: present qf-studio/pilot func[[:space:]]checkRunAsFirewall\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsWorkspaceRootNotDataDir\(

## Context

Navigator TASK-651. With run_as_user set, the Go side knows the host layout: `run_as_data_dir` (the data directory of the daemon), `run_as_workspace_dir` (the workspace root inside the agent root), a mode check of the daemon's entries (checkRunAsDataDirModes in `internal/executor/agent_command.go`), and a copy of the daemon binary in the gh shim directory of each run (installGhGuardBinary in `internal/executor/ghguard_spawn.go`). Run-it reviews of those two changes found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- refusedRunAsWorkspaceRoot does not know `run_as_data_dir`. A workspace root that is the data directory itself, or a parent of it, is accepted, and the mode check then skips nothing or everything.
- Four broken forms of the mode check pass every test, although the code is correct today: the group mask without the x bit; the mask of the data directory without the group read bit; a skip of the workspace entry by string prefix; the data directory left out of the compare that keeps the preflight result.
- The test of the binary copy uses a fixture with no group, so the group change never runs. A copy without the agent group passes the test. On a host each `gh` call of the agent then fails.
- No test covers a copy that fails in the middle.
- loadProjectContext reads the README with a background context. A FIFO at that path stalls the prompt for the full helper timeout.

## Scope

1. refusedRunAsWorkspaceRoot refuses a workspace root that is equal to `run_as_data_dir` or a parent of it, in the given and in the resolved form. The refusal names both paths.
2. Tests for item 1, in a function TestRunAsWorkspaceRootNotDataDir: root equal to the data directory; root as the parent; root through a link to the data directory; the default layout passes.
3. Tests for the mode check: an entry with mode 0710 and the shared group fails; a data directory with mode 0750 fails; an entry `agentx` next to the workspace entry `agent` is checked and fails with mode 0755; a change of `run_as_data_dir` alone makes the preflight run again.
4. Tests for the binary copy with a fixture that has a real group id (the group of the test process): the copy has that group and mode 0750; a source that ends early (a reader that returns an error after some bytes, through a replaceable open function) fails the set-up and leaves no file with the name of the copy.
5. The README read of loadProjectContext takes the context of the run when the caller has one.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsWorkspaceRoot|RunAsDataDir|GhGuard|ProjectContext|RunAsUser'` passes; paste the output into the PR body.
- [ ] Change the group mask of an entry from 070 to 060 -> a test fails.
- [ ] Replace applyGuardPerm for the copy by a plain chmod -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A dangling link in the data directory. Group bits for a group other than the shared group. GOFLAGS and GOPROXY of the daemon (gates leg, with `run_as_env`). The read of the env context file. Workflow hooks (gates leg). Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6425 and PR#6427

### LEG P5 — repo: qf-studio/pilot — title: fix(executor): run_as_user firewall probe — a timeout at the gateway port is a refusal, only the daemon start opens the gateway port, a failed result is not kept, a probe for a gateway host that is not loopback

probe: present qf-studio/pilot func[[:space:]]checkRunAsFirewall\(
probe: present qf-studio/pilot func[[:space:]]applyReadGuardConfigFromFile\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsWorkspaceRootNotDataDir\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsFirewallGatewayTimeoutRefuses\(

## Context

Navigator TASK-651. With run_as_user set, PreflightRunAsUser (`internal/executor/agent_command.go`) probes the firewall as the agent user (checkRunAsFirewall): a control connection to a listener of the daemon, a connection to the metadata address that must fail, and a connection to the gateway port that must fail while the daemon holds a listener on 127.0.0.1 with that port. The caller is applyReadGuardConfig in `cmd/pilot/main.go`, reached from `pilot start` and, through applyReadGuardConfigFromFile, from every subcommand. A run-it review found the points below. The option is off, and the general refusal of the preflight still comes before this check. Keep the change small: only the items below. With the option empty nothing changes.

- A timeout of the connection to the gateway port counts as "blocked". The rule of the host script rejects with a TCP reset, so a real block on loopback fails at once; a timeout there proves nothing. A slow sudo gives a passed preflight with no rule.
- Every subcommand with the option set opens the gateway port for the probe. While a daemon runs, another `pilot` subcommand cannot open the port and is refused. `pilot start --replace` is always refused: the old daemon stops after the preflight, and a failed result is kept for the process. A subcommand also holds the port for some seconds, and a daemon that starts in that time cannot bind.
- The probe uses 127.0.0.1 only. The gateway host is a config value, and `0.0.0.0` is allowed. The host script has a rule for the other local addresses, and nothing proves it.
- No test in `cmd/pilot` covers the way of the gateway port from the config to the preflight.

## Scope

1. Gateway probe result: a connection that ends by the time limit or by the context is a refusal with the text "no answer from the gateway port", and the port. A fast failure is "blocked" as today. For the metadata address a timeout stays "blocked".
2. Who opens the port: only the daemon start (`pilot start`) passes the gateway port and runs the gateway part. Each other entry point passes no port and skips the gateway part with one Debug line (not a Warn). `pilot start --replace` runs the preflight after the old daemon has stopped.
3. A failed preflight result is not kept: the next call runs the probes again. A passed result is kept as today.
4. Gateway host: the daemon start passes the gateway host too. When the host is not a loopback address, the daemon also opens a listener on the first non-loopback local IPv4 address with the gateway port, and the connection of the agent user to it must fail. When the host has no such address, this part is skipped with one Warn line.
5. Tests. In the executor package: a function TestRunAsFirewallGatewayTimeoutRefuses (a mock sudo that waits longer than the limit on the gateway connection only); a failed result followed by a call with the same settings runs the probes again; a host of `0.0.0.0` with a reachable non-loopback listener is refused. In `cmd/pilot`: the gateway port and host of the config reach the preflight at the daemon start; another entry point passes no port; a YAML file with a port gives that port at the daemon start.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsFirewall|RunAsUser'` and `go test -count=1 ./cmd/pilot/ -run 'ReadGuard|RunAs|Gateway'` pass; paste the output into the PR body.
- [ ] Count a timeout at the gateway port as "blocked" -> the new test fails.
- [ ] Drop the assignment of the gateway port at the daemon start -> a test of `cmd/pilot` fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

IPv6 addresses. Expansion of environment variables in the config read of the subcommands. The removal of the general refusal. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6437

### LEG P6 — repo: qf-studio/pilot — title: fix(executor): run_as_user firewall probe — a probe that did not run is not "blocked", the probe uses the address of the gateway, a test for the order with --replace, a gateway port of 0 at the daemon start is refused

probe: present qf-studio/pilot func[[:space:]]checkRunAsFirewall\(
probe: present qf-studio/pilot func[[:space:]]agentDialAs\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsFirewallGatewayTimeoutRefuses\(
probe: present qf-studio/pilot func[[:space:]]applyStartReadGuardConfig\(
probe: absent  qf-studio/pilot func[[:space:]]gatewayProbeAddrs\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, PreflightRunAsUser (`internal/executor/agent_command.go`) probes the host firewall as the agent user (checkRunAsFirewall). agentDialAs runs `bash -c ': </dev/tcp/HOST/PORT'` as the agent user through sudo and reports three results: exit 0 is a connection, an end by the time limit is no answer, each other end is a failed connection. For the gateway port a failed connection means "blocked", and the preflight passes. A run-it review found the points below. The option is off, and the general refusal of the preflight still comes before this check. Keep the change small: only the items below. With the option empty nothing changes.

- "Each other end" also holds a probe that never tried to connect: the probe process is killed, sudo exits with an error for the agent user (a process limit, a PAM session error), bash is missing (exit 127). With no firewall rule the preflight passes in each of these cases. The control connection runs once, before the parallel probes, so it does not catch a failure that starts later.
- The gateway host of the config only decides "loopback or not". The probe then uses 127.0.0.1 and the first non-loopback IPv4 address of the interface list. A gateway on a second address, or on 127.0.0.2, is never probed.
- The order for `pilot start --replace` (the old daemon stops, then the preflight runs) is checked only by the presence of an identifier in two function bodies. The preflight can move back before the stop, or its call in runPollingMode can go, with no failed test.
- At the daemon start a gateway port of 0 skips the gateway part with one Debug line.
- The gateway port and the gateway host can be removed from the compare that keeps a passed result, with no failed test.

## Scope

1. Proof that the probe ran: the command of agentDialAs prints one marker line with the exit status of the redirect (for example `bash -c ': </dev/tcp/HOST/PORT; echo pilot-probe=$?'` with the error output of the redirect dropped). The result is "connected" only for the marker with 0, "failed connection" only for the marker with a status that is not 0, "no answer" for an end by the time limit, and a new result "the probe did not run" for each end with no marker. For the gateway port and for the metadata address "the probe did not run" is a refusal with that text and the address. The control connection uses the same rule.
2. A function gatewayProbeAddrs(host): for a host that is a literal IP address of a local interface it returns that address (also 127.0.0.2 and other loopback addresses that are not 127.0.0.1); for an empty host, a wildcard address or a host name it returns 127.0.0.1 and each non-loopback local IPv4 address; for a literal IP that is not local it returns that address and the bind failure is the refusal of today. The daemon opens a listener on each returned address, and the connection of the agent user to each must fail. IPv6 addresses stay out, with the Warn line of today.
3. Daemon start with a gateway port of 0 or below: the preflight refuses with the text "run_as_user needs a gateway port for the firewall probe". The other entry points pass no port and skip the gateway part with the Debug line of today.
4. Tests in the executor package (mock sudo fixture of the firewall tests): a probe that is killed, a probe that exits 127, and a sudo that exits 1 before bash runs, each on the gateway connection only -> refused with "did not run"; the same on the metadata connection -> refused; a host that is the second local address is probed on that address (a fake interface list through a seam); host 127.0.0.2 is probed on 127.0.0.2; a wildcard host is probed on each address of the fake list; a passed result followed by a call with another gateway port, and one with another gateway host, runs the probes again (count the probe commands in the mock sudo log).
5. Tests in `cmd/pilot`: with `--replace` the preflight call comes after the stop of the old daemon (a seam records the order of the two calls; the test fails when the preflight is first and when it is not called); a config with gateway port 0 at the daemon start is refused.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsFirewall|RunAsUser|GatewayProbeAddrs'` and `go test -count=1 ./cmd/pilot/ -run 'ReadGuard|RunAs|Gateway'` pass; paste the output into the PR body.
- [ ] Count a probe with no marker as a failed connection -> the test with the killed probe fails.
- [ ] Return only the first non-loopback address for each host -> the test with the second local address fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

IPv6 probes. A second preflight inside a running daemon. The time limit of the control connection. The `--no-gateway` case. The removal of the general refusal. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6475

### LEG H1 — repo: qf-studio/pilot — title: feat(executor): run_as_user hand-over, part 1 — the daemon takes the branch of the agent workspace as a bundle stream into a ref of its own repository (no caller yet, off by default)

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]gitCommandIn\(
probe: present qf-studio/pilot func[[:space:]]transportWorkspaceBase\(
probe: absent  qf-studio/pilot func[[:space:]]handOverBranch\(

## Context

Navigator TASK-651 (design in `.agent/system/agent-isolation-design.md`). With run_as_user set, the agent works in a clone that the agent user owns, and the daemon must not run git with the workspace as its repository: the agent controls the git config, the hooks and the refs there. Today, with the option set, the push of a run fails by design (withGitCredentials refuses the agent route), so no run can deliver. The hand-over replaces the push from the workspace: the daemon takes the commits of the branch out of the workspace as data and pushes from its own repository. This leg adds only the function that takes the commits; later legs call it. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes, and this leg adds no caller.

Existing pieces to use: gitCommandIn and gitCmd in `internal/executor/git_command.go` route a git command to the agent user for a directory inside the workspace root; transportWorkspaceBase in `internal/executor/agent_workspace.go` moves the base commit the other way as a bundle; `secretpatterns.GitScanConfig` and `HooksOffArgs` hold the hardened git settings of the daemon.

## Scope

1. A new file handover.go in the directory `internal/executor`, with a function handOverBranch(ctx, workspace, daemonRepo, taskID, branch, baseSHA) that returns the head SHA of the branch in the daemon's repository. Step one: a git command as the agent user in the workspace (through the constructor) writes `git bundle create - BRANCH ^BASE` to its standard output, and the daemon copies that stream into a temporary file of its own, outside the workspace. The daemon reads no file of the workspace and writes none.
2. Limits of the stream: a size limit of 2 GB and the context of the run. Over the limit the agent command is stopped through the kill of today, the temporary file is removed and the function returns an error that names the limit.
3. Step two, in the daemon's repository as the daemon user: `git fetch` from the bundle file into the ref `refs/pilot/handover/TASK` only (an explicit refspec with the branch of the bundle; no tags, no other ref), with fetch.fsckObjects on, hooks off and GIT_NO_REPLACE_OBJECTS=1 (the settings of GitScanConfig and HooksOffArgs). The function then reads the SHA of that ref and returns it. The temporary file is removed on every path.
4. A function removeHandOverRef(daemonRepo, taskID) that deletes the ref; a missing ref is no error.
5. Tests, table-driven, no root needed, with the mock sudo fixture of the workspace tests: the head SHA in the daemon's repository equals the head of the workspace branch, and the daemon's repository has the commits; the stream over a small test limit fails and leaves no file and no ref; a pre-push hook and a post-checkout hook in the workspace write a marker file -> no marker after the hand-over; a replace ref in the workspace does not change the objects that arrive; a bundle that holds a second ref (a tag, a forged `refs/remotes/origin/main`) creates only the hand-over ref; a branch name that does not exist in the workspace gives an error; the mock sudo log shows that the bundle command ran as the agent user and the fetch did not.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Fetch with the refspec `refs/*:refs/*` -> the test with the second ref fails.
- [ ] Remove the size limit -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Any caller of the function. The push, the secret gate, the PR creation, the recovery ref (next legs). Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG H2 — repo: qf-studio/pilot — title: feat(executor): run_as_user hand-over, part 2 — the secret gate and the push run in the daemon's repository on the hand-over ref, remote probes too (off by default)

probe: present qf-studio/pilot func[[:space:]]handOverBranch\(
probe: present qf-studio/pilot func[[:space:]]pushArgs\(
probe: present qf-studio/pilot func[[:space:]]CheckSecretGate\(
probe: absent  qf-studio/pilot func[[:space:]]newGitOperationsForWorkspace\(

## Context

Navigator TASK-651. With run_as_user set, the agent works in a clone that the agent user owns. The function handOverBranch (`internal/executor/handover.go`) takes the branch out of the workspace into the ref `refs/pilot/handover/TASK` of the daemon's repository. Today the delivery path still points at the workspace:

- GitOperations (`internal/executor/git.go`) has one path, projectPath, which is the workspace. Push and PushForceWithLease build the push there; withGitCredentials refuses the agent route, so each push fails.
- CheckSecretGate (`internal/executor/secret_gate.go`) scans through `secretpatterns` (`pushrange.go`), whose git commands are plain commands of the daemon user with the daemon environment, inside the repository of the agent. The project settings of the gate (default branch, allow_paths) are keyed by the git directory, so they do not apply to a workspace.
- The remote probes (RemoteBranchExists, ProbeRemoteBranch, RemoteBranchHead) and the fetch calls fail on the agent route, so the retry loop of the push, the lease and the "already published" decision are blind.

The rule of this leg: with the option set, the daemon runs the gate, the push and each remote command in its own repository, on the hand-over ref. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. A constructor newGitOperationsForWorkspace(workspace, daemonRepo) and a second field of GitOperations for the daemon's repository. The run sites that build GitOperations for an agent workspace use it and pass the project path of the task. With the option empty the field is empty and every method works as today.
2. Push and PushForceWithLease, option set: call handOverBranch, then run CheckSecretGate with the daemon's repository as its directory and the hand-over SHA as the tip, then push from the daemon's repository with the refspec HANDOVER_SHA:refs/heads/BRANCH, the push arguments of today (hooks off, the explicit lease) and the credentials of today, without `-u`. A failed hand-over or a refused gate stops before the push with the errors of today.
3. Remote commands, option set: RemoteBranchExists, ProbeRemoteBranch, RemoteBranchHead and the fetch of the count against origin run in the daemon's repository. The count of new commits against origin uses the hand-over SHA when one exists.
4. After a push, option set: the daemon reads the pushed commits in its own repository and checks that no commit message holds a co-author trailer of the model; a hit is logged as one Warn line with the SHA (the strip itself runs before, as the agent user, as today).
5. Tests with the option set (mock sudo fixture, a local bare remote): a run pushes the branch and the remote head equals the workspace head; the mock sudo log shows no git command of the daemon user with the workspace as its directory; a secret in a commit is refused by the gate and nothing is pushed; allow_paths of the project applies; in the workspace a hostile git config (core.hooksPath to a marker hook, a textconv driver, gpg.program, an alias for push, a url rewrite of origin to another remote) has no effect on the gate and the push; the lease refuses when the remote moved; with the option empty the push tests of today pass without a change to their assertions.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'HandOver|Push|SecretGate|RunAsUser'` passes; paste the output into the PR body.
- [ ] Run the gate with the workspace as its directory -> the test with the mock sudo log fails.
- [ ] Push with the workspace as the directory -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The `gh` commands and the PR creation, the recovery ref, the removal of the workspace, the read of the memory graph (next leg). A scan of commit headers and the git identity of the agent user (later leg). PushToMain (a direct-commit run is refused with the option set). The autopilot conflict helpers. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG H3 — repo: qf-studio/pilot — title: feat(executor): run_as_user hand-over, part 3 — gh commands and the recovery ref use the daemon's repository, the workspace stays until its commits are safe (off by default)

probe: present qf-studio/pilot func[[:space:]]newGitOperationsForWorkspace\(
probe: present qf-studio/pilot \)[[:space:]]CreateRecoveryRef\(
probe: present qf-studio/pilot func[[:space:]]withGhCredentials\(
probe: absent  qf-studio/pilot func[[:space:]]ghDirForDaemonRepo\(

## Context

Navigator TASK-651. With run_as_user set, GitOperations has the workspace and the daemon's repository (newGitOperationsForWorkspace), and the push runs from the daemon's repository on the hand-over ref. Four things of the delivery still use the workspace:

- Four `gh` commands in `internal/executor/git.go` (CreatePR, FindMergedPRByBranch, FindOpenPRByBranch, GetPRStateByURL) run as the daemon user with the daemon token and the workspace as their directory. `gh` reads the repository from the git config of that directory, which the agent controls, so the agent chooses the repository that the daemon token acts on. CreatePR takes the head branch from the HEAD of the workspace.
- CreateRecoveryRef writes the ref in the workspace as the agent user. The run then removes the workspace, so the ref is lost. The refusal comment of the secret gate says that the commits are kept on the host under refs/pilot-recovery; with a workspace that is false.
- The run removes the workspace in its deferred clean-up also when the commits never left it.
- loadMemoryGraph and one existence check in `git.go` read `graph.json` of the workspace as the daemon user and follow a link.

The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. A function ghDirForDaemonRepo. With the option set, the four `gh` commands run with the daemon's repository as their directory and with an explicit `--repo OWNER/NAME` from the remote of the daemon's repository. CreatePR passes `--head` with the branch of the task and `--base` as today; it does not read the HEAD of the workspace.
2. CreateRecoveryRef, option set: the ref is created in the daemon's repository from the hand-over SHA. When no hand-over ref exists, it calls handOverBranch first. A failure is logged as one Error line that names the task and the workspace path.
3. Clean-up rule, option set: the run removes the workspace only when the branch is pushed, or a recovery ref exists in the daemon's repository, or the workspace has no commit above the base. In any other case the workspace stays, and one Error line names its path. The sweep at the daemon start of today removes it later. The hand-over ref is deleted (removeHandOverRef) when the workspace is removed.
4. The two reads of `graph.json`, option set and a path inside the workspace root: through agentReadFile; a read error gives the result of today for a missing file and one Warn line.
5. Tests with the option set: a planted remote in the git config of the workspace does not change the `--repo` argument, and the directory of each `gh` command is the daemon's repository (a mock `gh` records its arguments and directory); a branch name in the HEAD of the workspace that differs from the task branch is not used; a refused gate leaves a recovery ref in the daemon's repository with the commits; a failed hand-over leaves the workspace and logs the Error line; a pushed run removes the workspace and the hand-over ref; with the option empty the calls of today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RecoveryRef|CreatePR|RunAsUser'` passes; paste the output into the PR body.
- [ ] Run `gh` with the workspace as its directory -> a test fails.
- [ ] Remove the workspace after a failed hand-over -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The text of the refusal comment for a run with the option empty. A scan of commit headers and the git identity (next leg). Gates, hooks, evidence commands (gates legs). Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · design section 4.2

### LEG H4 — repo: qf-studio/pilot — title: fix(executor): run_as_user — a git identity for the commits of the agent user, the secret gate also scans the header lines of a commit

probe: present qf-studio/pilot func[[:space:]]newGitOperationsForWorkspace\(
probe: present qf-studio/pilot func[[:space:]]gitAgentBaseEnv\(
probe: present qf-studio/pilot func[[:space:]]CheckSecretGate\(
probe: absent  qf-studio/pilot func[[:space:]]agentGitIdentityEnv\(

## Context

Navigator TASK-651. Two notes from earlier reviews are open on the delivery path:

- The agent user has no git identity. The creation of a workspace sets no user.name and no user.email, the host script sets none, and the agent environment for git carries author values only for one internal command. A commit by the agent user (the commit of the model, the commit that keeps unfinished work, the amend that strips a co-author trailer) can fail with "Please tell me who you are".
- The secret gate reads the message of each commit (`%B`) and the diff. It does not read the author, the committer or an extra header line. A secret in such a line passes the gate and is pushed.
- The amend of the trailer strip runs clean filters of the repository; a review of an earlier change asked for a test that a clean filter does not run there.

Keep the change small: only the items below.

## Scope

1. A function agentGitIdentityEnv. With the option set, each git command of the agent user and each model process gets GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL, GIT_COMMITTER_NAME and GIT_COMMITTER_EMAIL. The values come from user.name and user.email of the daemon's repository, read as the daemon user; when they are empty the values are `Pilot` and `pilot@localhost`. The four names are on the list that sudo keeps.
2. The secret gate scans, for each commit of the push range, the raw header of the commit object (author, committer, encoding and each other header line) with the same patterns and registered values as the message. A hit is refused with the SHA and the name of the header line, never with the value. This applies with the option set and with the option empty.
3. Tests: with the option set and no identity in the config of the workspace, a commit and an amend as the agent user succeed and carry the identity (mock sudo fixture); a registered secret value in the author name is refused; a registered value in an extra header line (a commit made with `git hash-object -t commit -w`) is refused; a commit with clean headers passes; a clean filter that writes a marker file does not run in the amend of the trailer strip.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'GitIdentity|SecretGate|PushRange|RunAsUser'` passes; paste the output into the PR body.
- [ ] Scan `%B` only -> the test with the author name fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A git identity setting in the host script. The gates and the GitHub token. A change of the patterns of the gate.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6360

### LEG H5 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — a stream that is not a bundle is refused before the fetch, the tip is a commit above the base, a cancelled context stops the daemon-side git, the daemon-side git ignores GIT_DIR

probe: present qf-studio/pilot func[[:space:]]handOverBranch\(
probe: present qf-studio/pilot func[[:space:]]handOverGitArgs\(
probe: present qf-studio/pilot func[[:space:]]streamHandOverBundle\(
probe: absent  qf-studio/pilot func[[:space:]]checkHandOverBundleHeader\(

## Context

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) takes the branch of the agent workspace into the ref `refs/pilot/handover/TASK` of the daemon's repository: a git command as the agent user writes a bundle to a stream, the daemon copies the stream into a temporary file, and `git fetch` as the daemon user reads that file. The agent user controls the stream. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

- `git fetch FILE` uses the bundle reader only when the file starts with a bundle signature. For any other file git takes the path as a local repository and starts `git-upload-pack FILE` as the daemon user; a regular file is read as a gitfile. A stream of the one line `gitdir: /path/to/other/.git` makes the daemon serve that repository to itself, and the hand-over ref then holds commits the agent user cannot read. A later push publishes them.
- A forged bundle can name the base itself, a commit with no relation to the base, or an annotated tag. The function returns the peeled commit while the ref holds the tag object.
- A cancelled context kills only `git fetch`. Its child `index-pack` holds the output pipe, and the function returns only when that child ends (seen: 6.7 s after the cancel with a 400 MB bundle).
- The daemon-side git commands take the daemon environment as is. With `GIT_DIR` set there, the hand-over works in another repository.

## Scope

1. A function checkHandOverBundleHeader, called on the temporary file before the fetch: the file must start with the line `# v2 git bundle` or `# v3 git bundle`. Any other start is an error "hand-over: the stream is not a git bundle"; no git command reads the file and the file is removed.
2. After the fetch, the tip: the object of the hand-over ref must have the type `commit` (read without a peel), the SHA must differ from the base, and the base must be an ancestor of it (`git merge-base --is-ancestor`). On a failure the ref is removed and the error names the rule. The returned SHA is the SHA the ref holds.
3. Context: each daemon-side git command of this file runs in its own process group; on a cancel or a deadline the group is killed and the function returns within a wait delay of a few seconds. Use the pattern that the agent commands of this package use today for the group kill and the wait delay.
4. Environment of the daemon-side git commands of this file: GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_NAMESPACE and each name that starts with GIT_CONFIG are removed.
5. Tests in `handover_test.go` with the fixture of today (newHandOverEnv): a `gitdir:` stream that names a second repository is refused, the daemon's repository has no new ref and no commit of that repository, and a marker shows that no `git-upload-pack` ran; an empty stream and a garbage stream are refused with the same error; a bundle with the branch at the base is refused; a bundle with an orphan history is refused; a bundle with the branch at an annotated tag is refused; a context cancelled during the fetch returns within the wait delay (a mock that makes the fetch slow) and leaves no ref; with `GIT_DIR` of another repository in the environment, the hand-over writes the ref into the daemon's repository and the other repository has no new ref.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Remove the call of checkHandOverBundleHeader -> the test with the `gitdir:` stream fails.
- [ ] Remove the ancestor check -> the test with the orphan history fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A quarantine object directory for the pack and the removal of packs after a failed hand-over (later leg). Tests for the guards that exist today. A lock per task. Any caller of the function. Any file other than `handover.go` and `handover_test.go`. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6443

### LEG H6 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — only commits whose objects came in the bundle are accepted, a failed hand-over leaves no pack, a branch with nothing above the base gives a typed result

probe: present qf-studio/pilot func[[:space:]]handOverBranch\(
probe: present qf-studio/pilot func[[:space:]]checkHandOverBundleHeader\(
probe: present qf-studio/pilot func[[:space:]]removeHandOverRef\(
probe: absent  qf-studio/pilot func[[:space:]]verifyHandOverObjects\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) takes the branch of the agent workspace into the ref `refs/pilot/handover/TASK` of the daemon's repository: the agent user writes a bundle to a stream, the daemon stores it in a temporary file and runs `git fetch` from that file. The agent user controls the stream and the workspace. The push of a run then publishes the hand-over SHA. Two run-it reviews found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

- The connectivity check of the fetch is satisfied by objects that the daemon's repository already has. A bundle can name, or build on, a commit that only the daemon's repository has (a branch of another task, a private ref). Reproduced through the real push: a workspace commit whose parent is such a commit, with that parent listed in `.git/shallow` of the workspace, was handed over and pushed. The secret gate scans for secrets only, so the commits of another task became public on the branch of this task.
- Each failed hand-over leaves files in the daemon's repository: a complete pack after a late failure, `tmp_pack_*` after a failed index-pack, `.promisor` packs after a filtered bundle. `gc.auto` is 0 there, and one hand-over can be 2 GB.
- A branch with no commit above the base fails with "Refusing to create empty bundle" from git. The callers cannot tell this case from a broken workspace.

## Scope

1. Quarantine: the objects of the bundle go into a temporary object directory of the daemon (outside the workspace, removed on every path), with the object directory of the daemon's repository as an alternate, and not into the daemon's repository. Use the git mechanism for this (GIT_OBJECT_DIRECTORY and GIT_ALTERNATE_OBJECT_DIRECTORIES for the daemon-side commands of the hand-over, or `git index-pack` into the temporary directory).
2. A function verifyHandOverObjects: each commit of `BASE..TIP` must be an object of the quarantine directory itself (read with the quarantine directory as the only object directory, no alternate). A commit of that range that exists only through the alternate is an error "hand-over: commit SHA of the branch did not come from the workspace", and nothing is kept. The rules of today stay (the tip is a commit, differs from the base, and the base is an ancestor of it).
3. Only after item 2 passes, the objects move into the daemon's repository and the ref `refs/pilot/handover/TASK` is set to the tip. On each failure the daemon's repository has no new pack, no `tmp_pack_*` file, no `.promisor` file and no new ref.
4. A typed error errHandOverNothingNew: when the branch of the workspace has no commit above the base, handOverBranch returns it and does not start the bundle fetch. The check runs as the agent user in the workspace through the constructor of today (`git rev-list --count BASE..refs/heads/BRANCH`), and a count that is not a plain number is an error.
5. Tests in `handover_test.go` with the fixture of today: a workspace commit on top of a commit that only the daemon's repository has, with that commit in `.git/shallow` of the workspace, is refused and the daemon's repository has no new ref; a forged bundle with an empty pack that names a commit that only the daemon's repository has is refused; a normal branch of three commits passes and the daemon's repository has the three commits; after a failed hand-over (a truncated bundle; a bundle with a corrupt pack; the over-limit stream) the directory `objects/pack` of the daemon's repository has the same file list as before; a branch equal to the base returns errHandOverNothingNew and the mock sudo log shows no bundle command.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Remove the call of verifyHandOverObjects -> the test with the commit of the daemon's repository fails.
- [ ] Fetch into the daemon's repository as before this change -> the test for the file list of `objects/pack` fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The callers in `git.go` and `runner.go` (next leg). A lock per task. Tests for the guards of today that this change does not touch. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6443 and PR#6461

### LEG H8 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — a pack stream in place of the bundle: index-pack with no fix-thin into a quarantine, each object of the range must be in the quarantine, a failure leaves nothing

probe: present qf-studio/pilot func[[:space:]]handOverBranch\(
probe: present qf-studio/pilot func[[:space:]]handOverDaemonGit\(
probe: present qf-studio/pilot func[[:space:]]checkHandOverTip\(
probe: absent  qf-studio/pilot func[[:space:]]checkHandOverPackHeader\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) takes the branch of the agent workspace into the ref `refs/pilot/handover/TASK` of the daemon's repository. Today the agent user writes `git bundle create - BRANCH ^BASE` to a stream, the daemon stores the stream in a temporary file and runs `git fetch` from that file. The agent user controls the stream, and the push of the run then publishes the hand-over SHA with the credentials of the daemon. Reviews found that `git fetch` from a bundle gives the agent too many ways:

- git unbundles with `index-pack --fix-thin`. A delta in the stream can name, as its base, a commit that only the daemon's repository has; index-pack then copies that base into the pack. So "the object is in the new pack" is no proof that the object came from the agent, and a private commit of the daemon's repository can reach the remote.
- A stream with the bundle signature and a broken header makes `git fetch` fall back to the repository transport and start `git-upload-pack` on the temporary file.
- A v3 bundle with a `@filter` capability is accepted; in a partial-clone repository git then fetches missing objects from the remote by itself.
- Each failed hand-over leaves pack files in the daemon's repository.

The founder chose a new transport on 2026-11-18: a plain pack that is not thin, and no `git fetch` on the daemon side. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. The signature of handOverBranch and its callers do not change. With the option empty nothing changes.

## Scope

1. Agent side, as the agent user in the workspace through the constructor of today. First `git rev-list --count BASE..refs/heads/BRANCH`: a count of 0 returns the typed error errHandOverNothingNew (a new variable), and a text that is not a plain number is an error. Then `git rev-parse --verify refs/heads/BRANCH^{commit}` gives the tip; the daemon accepts it only as a full lower-case hex object name. Then `git pack-objects --stdout --revs` with the two lines `TIP` and `^BASE` on standard input, and with no `--thin` argument, writes the pack to the stream. The size limit, the stream copy into a temporary file outside the workspace, and the kill of today stay. The bundle command, checkHandOverBundleHeader and the bundle signatures are removed.
2. A function checkHandOverPackHeader, called before any git command reads the file: the file must start with the four bytes `PACK` and the version 2. Any other start is the error "hand-over: the stream is not a git pack", and the file is removed.
3. Quarantine: the daemon runs `git index-pack --strict --stdin` with the temporary file as standard input, with NO `--fix-thin`, with GIT_OBJECT_DIRECTORY set to a new directory `pilot-handover-incoming-*` inside the object directory of the daemon's repository and GIT_ALTERNATE_OBJECT_DIRECTORIES set to that object directory (handOverDaemonGit removes these two names today: set them for this command and for the checks of item 4 on purpose). A pack with a delta whose base is not in the pack fails here. No `git fetch`, no `git bundle` command.
4. Checks before anything moves, each read through the quarantine with the alternate: the tip rules of checkHandOverTip (a commit, not the base, the base is an ancestor). Then a function verifyHandOverObjects: each object of `git rev-list --objects TIP --not BASE` must exist in the quarantine directory alone (a `git cat-file --batch-check` with the quarantine as the only object directory and no alternate). An object of that list that exists only through the alternate is the error "hand-over: object SHA of the branch did not come from the workspace".
5. Only after item 4 passes: the pack file and its index move into `objects/pack` of the daemon's repository, and `git update-ref` sets `refs/pilot/handover/TASK` to the tip. When update-ref fails, the moved files are removed again. The quarantine directory is removed on every path. At the start of each hand-over, a directory `pilot-handover-incoming-*` older than one hour is removed (a left-over of a daemon that was killed). After each failure the daemon's repository has the same file list in `objects/pack` as before, and no hand-over ref.
6. Tests in `handover_test.go` with the fixture of today (newHandOverEnv); replace the bundle tests by their pack forms: a normal branch of three commits passes, and the daemon's repository has the commits and the ref; a forged thin pack (made with `git pack-objects --thin --stdout --revs` in a clone that has a commit only the daemon's repository has, so that a delta names that commit or its tree as the base) is refused and the file list of `objects/pack` is the same as before; a workspace commit on top of a commit that only the daemon's repository has, with a pack that does not hold that parent, is refused; a stream that is the line `gitdir: /path/to/other/.git`, an empty stream, and a bundle file are refused by the header check, and a marker shows that no git command read the file; a truncated pack and a pack with a corrupt object are refused with the same file list as before; a failed update-ref (a seam) leaves the same file list as before; a branch equal to the base returns errHandOverNothingNew and the mock sudo log shows no pack-objects command; the tip tests of today (tag, tree, blob, base, orphan history) pass in their pack form.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Add `--fix-thin` to the index-pack command -> the test with the forged thin pack fails.
- [ ] Remove the call of verifyHandOverObjects -> the test with the commit of the daemon's repository fails.
- [ ] `git grep -n 'bundle' internal/executor/handover.go` finds no git bundle command.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The callers in `git.go` and `runner.go` (the leg for the push). The list of environment names that handOverDaemonGit removes (next leg). A lock per task. transportWorkspaceBase, which moves the base commit into the workspace as a bundle (the other direction: the daemon makes that bundle). Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6443, PR#6461 and PR#6469 · hold comment of issue 6470

### LEG H9 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — the daemon-side git gets its environment by allow list, tests for the guards that no test pins

probe: present qf-studio/pilot func[[:space:]]handOverEnvAllowed\(
probe: present qf-studio/pilot func[[:space:]]checkHandOverPackHeader\(
probe: present qf-studio/pilot func[[:space:]]handOverDaemonGit\(
probe: absent  qf-studio/pilot func[[:space:]]TestHandOverDaemonGitEnvAllowList\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. handOverDaemonGit (`internal/executor/handover.go`) builds each git command that the daemon runs in its own repository during a hand-over. It removes six names from the environment (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_NAMESPACE) and each name that starts with GIT_CONFIG. A run-it review set other names in the daemon environment and found:

- GIT_COMMON_DIR of another repository: the hand-over returns success, the pack is written into the other repository, and the daemon's repository gets a ref to an object it does not have.
- GIT_EXEC_PATH of a directory with marker scripts: the scripts run as the daemon user.
- GIT_GRAFT_FILE: the ancestor check accepts an orphan history.
- GIT_TRACE with a file path: git writes that file.
- Tests pin only GIT_DIR: each of the five other names, and the GIT_CONFIG rule, can leave the list with no failed test. Three more lines have no test: the removal of the ref after a failed step, GIT_NO_REPLACE_OBJECTS=1 on the tip commands, and the wait delay.

Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

## Scope

1. handOverEnvAllowed becomes an allow rule for names that start with `GIT_`: each such name is removed, with no exception from the daemon environment. The values that the hand-over needs (GIT_OBJECT_DIRECTORY and GIT_ALTERNATE_OBJECT_DIRECTORIES for the quarantine commands, GIT_NO_REPLACE_OBJECTS=1, GIT_TERMINAL_PROMPT=0) are set by the code after the filter. Names that do not start with `GIT_` pass as today.
2. A test TestHandOverDaemonGitEnvAllowList, table-driven over these names set in the test process: GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_NAMESPACE, GIT_COMMON_DIR, GIT_EXEC_PATH, GIT_GRAFT_FILE, GIT_TRACE, GIT_CONFIG_GLOBAL, GIT_CONFIG_COUNT with KEY_0 and VALUE_0, GIT_CONFIG_PARAMETERS, GIT_SSH_COMMAND. For each row: the environment of the built command does not hold the name with the value of the test, and for GIT_COMMON_DIR, GIT_EXEC_PATH and GIT_GRAFT_FILE a real hand-over writes only into the daemon's repository, runs no marker script, and refuses the orphan history.
3. Tests for the three lines with no test: a failed step after the ref is written removes the ref (a seam); a replace ref in the daemon's repository does not change the result of the tip checks; a command that ignores the kill returns within the wait delay (a mock git that traps the signal).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Let GIT_COMMON_DIR pass the filter -> a row of the table test fails.
- [ ] Remove GIT_NO_REPLACE_OBJECTS from the tip commands -> the replace ref test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The environment of the agent-side commands. The delivery commands in `git.go` (the leg for the push). Any change of the transport. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6469

### LEG H14 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — no lazy fetch in a partial clone, a size limit for each object, the temporary pack is swept, tests for the tree and blob check and for the tip format

probe: present qf-studio/pilot func[[:space:]]verifyHandOverObjects\(
probe: present qf-studio/pilot func[[:space:]]checkHandOverPackHeader\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverDaemonGitEnvAllowList\(
probe: absent  qf-studio/pilot func[[:space:]]TestHandOverBranchPartialCloneNoLazyFetch\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) takes the branch of the agent workspace as a plain pack: the daemon indexes the stream with `git index-pack --strict` and no `--fix-thin` into a quarantine object directory, and verifyHandOverObjects requires each object of `rev-list --objects TIP --not BASE` in the quarantine alone (a `git cat-file --batch-check` with the quarantine as the only object directory). The agent user controls the stream and the tip text. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

- In a daemon repository that is a partial clone (`extensions.partialClone`, a promisor remote), a git command that asks for a missing object fetches it from the remote by itself. The `cat-file` of verifyHandOverObjects asks for each object of the range. For an object that the agent named and did not send (a private parent commit that the remote also has), git runs `git fetch` against the remote with the access of the daemon, the object lands in the quarantine, and the check passes. The objects then move into the daemon's repository and are in the range of the push. With `GIT_NO_LAZY_FETCH=1` in the environment of the command the same case is refused with "did not come from the workspace" and no fetch starts.
- With the argument `--objects` removed from verifyHandOverObjects every test passes: only a missing parent commit is tested, not a missing tree or blob.
- With the format check of the tip removed every test passes; a text such as `--upload-pack=x` then reaches a git command as an argument.
- After a kill of the daemon during index-pack, the temporary file `pilot-handover-*.pack` stays in the temporary directory of the daemon; the sweep covers only the quarantine directories.
- Two hand-overs of the same task at the same time: the second `update-ref` fails ("reference already exists"), and its error path deletes the ref that the first call just set.
- The size limit bounds the stream. A pack of 1 MB can hold a blob of 1 GiB of zeros; a stream near the limit can hold terabytes of blob content.

## Scope

1. Each daemon-side git command of the hand-over gets `GIT_NO_LAZY_FETCH=1` in its environment (set after the environment filter, with the other values that the code sets on purpose). Before the stream is read, handOverBranch also refuses a daemon repository whose config has `extensions.partialClone` or a remote with `promisor=true`: the error "hand-over: the daemon's repository is a partial clone" and no other command.
2. verifyHandOverObjects reads the size of each object of the range in the same `cat-file --batch-check` call. One object over 512 MB, or a sum over 4 GB, is an error that names the limit; both limits are package variables that a test can lower.
3. The temporary pack file: it is created inside the quarantine directory, so that the removal and the sweep of the quarantine cover it. A plain file `pilot-handover-*.pack` older than one hour in the old place is removed by the sweep too.
4. A failed `update-ref` whose error says that the reference already exists does not delete the ref and does not remove files that the other hand-over moved; it returns the error "hand-over: another hand-over of the task is in progress". Each other failure keeps the clean-up of today.
5. Tests in `handover_test.go` with the fixture of today: a function TestHandOverBranchPartialCloneNoLazyFetch — a daemon repository made as a partial clone of a local bare remote is refused before any stream command; and, with the refusal of item 1 switched off through a seam, a commit whose parent is in the daemon's repository and on the remote but not in the pack is refused by verifyHandOverObjects, and a wrapper of the git command shows no `fetch`; a commit (parent is the base) with the tree of a private commit that is not in the pack is refused; a tree that names a private blob by its object name is refused; a table for the tip text from the agent side (upper case, a ref name, `--upload-pack=x`, two lines, 12 characters, empty): each is refused and no daemon git command gets the text as an argument; a blob over the lowered object limit is refused with nothing left; a kill of the stream command leaves no `.pack` file after the clean-up; two hand-overs of one task at once (10 rounds): in each round one returns a SHA and the ref holds that SHA after both returned.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Remove `GIT_NO_LAZY_FETCH=1` -> the partial clone test fails.
- [ ] Remove `--objects` from verifyHandOverObjects -> the test with the private tree fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A lock per task. The mtime rule of the quarantine sweep. The callers in `git.go`. Support for a daemon repository that is a partial clone. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6497

### LEG H15 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — the daemon-side git gets only named variables (no token), no global and no system git config, tests for the update-ref environment and for the wait delay on each host

probe: present qf-studio/pilot func[[:space:]]handOverEnvAllowed\(
probe: present qf-studio/pilot func[[:space:]]handOverDaemonEnv\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverBranchPartialCloneNoLazyFetch\(
probe: absent  qf-studio/pilot func[[:space:]]TestHandOverDaemonGitEnvNoToken\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) runs git commands of the daemon on bytes that the agent user chose (index-pack, cat-file, rev-list, update-ref). handOverDaemonEnv gives these commands the environment of the daemon without each name that starts with `GIT_`. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

- The rule removes names by the prefix `GIT_` and passes each other name. `GITHUB_TOKEN` and each other token of the daemon are in the environment of all nine git processes of a hand-over and of each child they start. These commands are local and need no token.
- `HOME` and `XDG_CONFIG_HOME` pass, so the global git config of the daemon user still acts on these commands: with `[trace2] normalTarget = FILE` in the file .gitconfig in the home directory of the daemon user one hand-over writes a trace file. With `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` set, no file is written.
- The line that sets `GIT_NO_REPLACE_OBJECTS=1` on the last update-ref has no test: with the line removed each test passes.
- TestHandOverDaemonGitWaitDelay skips when the host has no `setsid` program (macOS); there the test proves nothing.
- The row `GIT_EXEC_PATH` of the table test proves its hand-over part only through the lazy fetch of a partial clone, which is off now.

## Scope

1. handOverEnvAllowed becomes a list of names: `PATH`, `HOME`, `TMPDIR`, `LANG`, `USER`, `LOGNAME`, and each name that starts with `LC_`. Each other name of the daemon environment is removed. The values that the code sets on purpose after the filter stay.
2. handOverDaemonEnv appends `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` next to `GIT_TERMINAL_PROMPT=0`.
3. Tests in `handover_test.go`: a function TestHandOverDaemonGitEnvNoToken — with `GITHUB_TOKEN`, `GH_TOKEN`, `ANTHROPIC_API_KEY`, `HTTPS_PROXY`, `LD_PRELOAD`, `SSH_AUTH_SOCK` and `XDG_CONFIG_HOME` set to fake values in the test process and a wrapper `git` first in `PATH` that writes its environment to a file, one hand-over passes and no file of a git process holds one of the names; a hand-over with a the file .gitconfig in the home directory of the daemon user that holds a trace2 target and a `core.hooksPath` passes and writes no trace file; the update-ref command of the hand-over has `GIT_NO_REPLACE_OBJECTS=1` in its environment (through the seam that the failure test of the update-ref uses today).
4. TestHandOverDaemonGitWaitDelay starts its detached child with no external `setsid` program (the test binary as a helper process with `SysProcAttr{Setsid: true}`), so it runs on each host and does not skip.
5. The row `GIT_EXEC_PATH` of the table test: a comment says that the assertion on `cmd.Env` is the proof; remove the part that needs a lazy fetch if it can not fail any more.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver|RunAsUser'` passes; paste the output into the PR body.
- [ ] Put the prefix rule back into handOverEnvAllowed -> TestHandOverDaemonGitEnvNoToken fails.
- [ ] Remove `GIT_CONFIG_GLOBAL=/dev/null` -> the trace file test fails.
- [ ] Remove the `GIT_NO_REPLACE_OBJECTS=1` line of the update-ref -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The git commands of the secret gate and of the push (`secretpatterns`, `git.go`). The environment of the agent side. A `safe.directory` entry for a daemon repository that the daemon user does not own. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6511

### LEG B12 — repo: qf-studio/pilot — title: fix(box): agent user setup script — the dry run reads the sudo rule list, a failed process stop keeps the firewall, a dangling link is flagged, a line when the daemon user has no sudo, a test for the high bytes of the quote function

probe: present qf-studio/pilot ^precheck_sudo_rules\(\)
probe: present qf-studio/pilot ^undo_firewall_allowed\(\)
probe: present qf-studio/pilot ^step_daemon_private\(\)
probe: present qf-studio/pilot shq_c1_case\([)]
probe: absent  qf-studio/pilot dangling_link_case\([)]

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. `scripts/box/setup-agent-user.sh` prepares a host for the option run_as_user: a second Linux user for the agent, a sudo rule, a firewall for that user. Root runs it by hand, with `--dry-run` first; `--undo` removes the set-up. Run-it reviews of the merged script found the points below. No one is a blocker; each one makes the output of the script say less than the state of the host. Keep the change small: only the items below, only in `scripts/box/setup-agent-user.sh` and `scripts/box/setup-agent-user_test.sh` (and its mock file when a test needs it).

- With `--dry-run`, precheck_sudo_rules prints the command `sudo -n -ll -U` and returns. On a host where the daemon user has a rule from a group, the dry run ends with exit 0 and each step "done (dry-run)", and the apply then stops at this check. The command only reads.
- In the undo, a failed step `agent-processes` (the process list command fails, the stop command fails, or a process survives KILL) does not hold the firewall steps. End state seen: exit 1, the firewall of the agent user gone, two processes of the agent user alive. No line says that these processes now reach the gateway port and the metadata address.
- step_daemon_private skips a top-level entry of the data directory that is a dangling link or a link loop (`[ -e "$e" ] || continue`). The Go preflight (`internal/executor/agent_command.go`) resolves each top-level link and refuses both. So the apply passes and the daemon start then refuses.
- The undo can end with the sudo rule gone and the root line of the cloud-init file still commented (no backup, or a failed restore). The daemon user then has no sudo at all, and no line says so.
- The function shq_c1_case in the test file has two names, and both hold the byte 0x9b. A quote function that prints the bytes 0xa0 to 0xff raw passes the whole suite.

## Scope

1. precheck_sudo_rules with `--dry-run`: after the printed command, run the same read-only check (check_rule_list). On a refusal print the entries and a line "dry run: the apply would stop here, nothing would be changed", and let the dry run end with exit 1 after the summary. When the rule list command itself fails, print "dry run: the rule list could not be read" and exit 1 the same way. No other dry-run step changes.
2. Undo: when the step `agent-processes` failed, the three firewall steps do not run; each gets the summary line "not run: processes of the agent user still run", and one line on standard error says that the firewall stays and tells the operator to stop the processes and run `--undo` again. The rule of today for a failed `sudoers-rule` step stays; when both failed, the sudo rule line wins. The exit status is 1.
3. step_daemon_private: an entry that is a link (`-L`) and does not resolve (dangling, or a loop) is not skipped. It gets a line "the entry NAME is a link that does not resolve: remove it or correct it, look at it by hand" with the name through shq, and the step fails as for the other hand-look entries. An unmatched glob is still skipped.
4. Undo: after the last step, when the rule file is gone and the cloud-init file still has a line that this script commented out, print in the summary "the daemon user DAEMON has no sudo rule now: restore CLOUD_INIT_FILE by hand". The check reads the file; it does not use the step result.
5. Tests in `setup-agent-user_test.sh`: a dry run on a mock host with a group rule ends with exit 1 and the new line, and changes no file; a dry run on a clean mock host still ends with exit 0; an undo with a failed process list (the helper pgrep_fails_after) keeps both firewall chains and the unit, and prints the new lines; a function dangling_link_case — a dangling link and a link loop in the data directory each fail the step with the new line, and an empty directory does not; an undo with the backups removed prints the "no sudo rule now" line; a third name in shq_c1_case that has a byte of 0xa0 to 0xff and no byte of 0x80 to 0x9f.

## Acceptance

- [ ] `bash scripts/box/setup-agent-user_test.sh` passes; paste its summary line into the PR body.
- [ ] Change shq so that it prints the bytes 0xa0 to 0xff raw -> the suite fails.
- [ ] Restore `[ -e "$e" ] || continue` as the only guard -> the dangling link test fails.
- [ ] Let the firewall steps run after a failed `agent-processes` step -> the new undo test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The Go preflight. The match of sudo entries with lines of the cloud-init file (hosts, last match). The rule for names with a space. A nested agent root. `setup-agent-user_real_test.sh`. A signal during the undo.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6442, PR#6449, PR#6472, PR#6479

### LEG P7 — repo: qf-studio/pilot — title: fix(executor): run_as_user firewall probe — a metadata probe with no answer must show that it started, the probe shell reads no start file, a strict read of the marker, a port range check

probe: present qf-studio/pilot func[[:space:]]agentDialAs\(
probe: present qf-studio/pilot func[[:space:]]probeMarkerResult\(
probe: present qf-studio/pilot func[[:space:]]gatewayProbeAddrs\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsFirewallMetadataNoStartRefuses\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the preflight checks the host firewall (checkRunAsFirewall in `internal/executor/agent_command.go`): as the agent user it opens a connection to the gateway port and to the metadata address, and each one must be blocked. agentDialAs runs `bash -c` through sudo with a `/dev/tcp` redirect and reads a marker line with the exit status. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `agent_command.go` and its test files. With the option empty nothing changes.

- For the metadata address a probe that ends by the time limit with no output counts as "blocked" (the firewall drops the packets with no reset, so no answer is the normal case). A sudo that hangs for the time limit gives the same result with no connection attempt. A probe that printed nothing proves nothing.
- The probe shell is `bash -c` with no `--norc --noprofile`. Today no file of the agent user is read only because the environment allow list drops `BASH_ENV`. One more name in that list would let a file of the agent user print the marker.
- probeMarkerResult reads the status with strconv.Atoi: `01`, `+1` and `10` are accepted, and each positive number is "failed connection". bash prints `0` or `1` here.
- A gateway port above 65535 is refused late, with "cannot listen", after a wait.
- The output of the probe process has no size limit.

## Scope

1. agentDialAs: the probe script prints a start line (a second marker constant, for example the marker with the suffix `start`) before the redirect. The result "no answer" needs that start line in the output read so far; a time limit with no start line is agentDialNotRun. Read the output through a pipe or a buffer that keeps what the process wrote before the kill.
2. The probe command is `bash --norc --noprofile -c SCRIPT`.
3. probeMarkerResult: the status text must be exactly `0` (connected) or `1` (failed); each other text is agentDialNotRun. The start line does not count as a second status marker.
4. The output of the probe is read with a limit of 4096 bytes; more output is agentDialNotRun.
5. The gateway port is checked before the probe: a port above 65535 fails the preflight at once with a message that names the port. The rule of today for the port 0 stays.
6. Tests: a function TestRunAsFirewallMetadataNoStartRefuses — with the mock sudo of the test fixture that sleeps past the time limit and prints nothing, the metadata probe gives "did not run" and the preflight refuses; with a mock that prints the start line and then sleeps, the result is "blocked" as today; a table for probeMarkerResult (`0`, `1`, `01`, `+1`, `10`, `-1`, empty, two status lines, a start line alone); a test that the probe arguments hold `--norc` and `--noprofile`; an output of 1 MB gives "did not run"; the port 70000 is refused with no listener and no wait. The test TestRunAsFirewallMetadataTimeoutPasses of today keeps its meaning: its mock prints the start line before it sleeps.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsFirewall|ProbeMarker'` passes; paste the output into the PR body.
- [ ] Remove the start line condition -> TestRunAsFirewallMetadataNoStartRefuses fails.
- [ ] Put strconv.Atoi back -> the table test fails for `01`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The host names `localhost` and `[::1]` in gatewayProbeAddrs. The tests that skip where 127.0.0.2 cannot be bound. The script under `scripts/box/`. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6494

### LEG H7 — repo: qf-studio/pilot — title: fix(executor): run_as_user push — a failed hand-over is never forgiven as "already published", the hand-over SHA is recorded after the push, nothing above the base is not a failure, hooks off for the remote reads

probe: present qf-studio/pilot func[[:space:]]newGitOperationsForWorkspace\(
probe: present qf-studio/pilot \)[[:space:]]ghDirForDaemonRepo\(
probe: present qf-studio/pilot func[[:space:]]pushFailureForgiven\(
probe: present qf-studio/pilot errHandOverNothingNew[[:space:]]*=
probe: absent  qf-studio/pilot func[[:space:]]daemonGitEnv\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, Push and PushForceWithLease of GitOperations (`internal/executor/git.go`) call handOverBranch, run the secret gate in the daemon's repository on the hand-over SHA, and push that SHA from the daemon's repository. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- After a failed push, pushBranchCounting (`internal/executor/runner.go`) and pushFailureForgiven (`internal/executor/secret_gate.go`) forgive the failure when the remote branch exists and its head equals LocalHead. LocalHead is `git rev-parse HEAD` as the agent user in the workspace. The agent can make the hand-over fail (it deletes its branch ref) and leave HEAD on the remote head: the loop returns nil after one attempt, and the run goes on to the PR for a head that this attempt never handed over.
- Push records the hand-over SHA before the gate. After a refused gate lastHandOverSHA is the refused commit, and CountNewCommitsAgainstOrigin counts commits that were never published. After a failed hand-over the field keeps the SHA of the attempt before.
- A push with no commit above the base ends in the error errHandOverNothingNew of the hand-over, and the retry loop takes it for an infra failure.
- The fetch of the count and the two `ls-remote` probes run in the daemon's repository with credentials and without the hooks-off arguments: a reference-transaction hook there ran with the token in its environment.
- The gate, the push, the probes, the count and the co-author read are plain commands with the daemon environment; with GIT_DIR set there they work in another repository.

## Scope

1. Forgiveness, option set: a hand-over error is never forgiven. For the other push errors the compare uses the hand-over SHA of this attempt, not LocalHead; when this attempt has no hand-over SHA there is no forgiveness. With the option empty the rule of today stays.
2. Push and PushForceWithLease, option set: the hand-over SHA of GitOperations is set only after the push succeeded, and it is cleared at the start of each attempt.
3. errHandOverNothingNew from the hand-over: Push returns a typed result that the retry loop does not retry and does not count as a failure; the run takes the path that a run with no new commit takes with the option empty.
4. Hooks off: the fetch of the count and the two `ls-remote` probes get the arguments of secretpatterns.HooksOffArgs, with the option set and with it empty.
5. A function daemonGitEnv: the daemon-side git commands of the delivery with the option set (gate, push, probes, fetch of the count, co-author read) get an environment without GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_NAMESPACE and each name that starts with GIT_CONFIG.
6. Tests in `handover_push_test.go`: the agent deletes its branch ref and leaves HEAD on the remote head -> the push loop returns an error and no PR step runs; a refused gate leaves lastHandOverSHA empty and the count of new commits equals the count on the remote; a branch equal to the base gives the typed result after one attempt; a reference-transaction hook and a pre-push hook in the daemon's repository write a marker -> no marker after a push with its probes and its count; with GIT_DIR of another repository in the environment the push lands on the remote of the daemon's repository; the gate tip is the SHA and not the ref name (the ref is moved between the hand-over and the gate by a test seam -> the pushed commit is the gated one).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'HandOver|Push|SecretGate|RunAsUser'` passes; paste the output into the PR body.
- [ ] Compare with LocalHead again -> the test with the deleted branch ref fails.
- [ ] Remove the hooks-off arguments from the push -> the marker test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The hand-over function itself (`handover.go`). The lifetime of the hand-over ref and the recovery ref. PushToMain. The `gh` commands. Any change of behaviour with the option empty, other than item 4.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6461

### LEG H10 — repo: qf-studio/pilot — title: fix(secretpatterns): the refusal of the secret gate never prints a part of a secret from a commit header — redact before the cut, a fixed label for an unknown header name

probe: present qf-studio/pilot func[[:space:]]CommitHeaderFile\(
probe: present qf-studio/pilot func[[:space:]]checkCommitEncodings\(
probe: present qf-studio/pilot func[[:space:]]scanCommitHeader\(
probe: absent  qf-studio/pilot func[[:space:]]TestCheckPushRange_HeaderNameIsNeverPrinted\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The secret gate (`internal/executor/secretpatterns/pushrange.go`) scans each commit of a push range and refuses a push that holds a secret. Its rule, written at PushRangeError: the refusal names the commit and the place, never the value. The gate also scans the raw header of each commit object (scanCommitHeader). The refusal text goes to the returned error, to a log line, to an alert and to a comment on the issue. A run-it review reproduced three cases in which a part of the secret is printed:

- CommitHeaderFile cuts the name of a header line to 40 runes (printableValue) before newPushRangeError redacts it. A registered secret value of 41 or more characters, or a value of a shape pattern longer than 40, as the NAME of an extra header line prints its first 40 characters: the cut value no longer equals the registered value, so the redaction finds nothing.
- checkCommitEncodings has the same order (`Redact(printableValue(enc))`): `encoding VALUE` with a value longer than 40 prints the first 40 characters.
- scanCommitHeader takes the name of a header line as the text up to the first space. A registered value that holds a space, written as one header line, is refused with its first part printed.

Also: checkCommitObjects lists the commits with `rev-list --reverse` and its comment says "Oldest commit first"; across a merge with clock skew the refusal names a child before its parent.

This code runs for each push of the product, with run_as_user empty too. Keep the change small: only the items below, only in `pushrange.go` and its test files in the same package.

## Scope

1. Header names: the refusal prints the name of a header line only when it is one of the known names (tree, parent, author, committer, encoding, gpgsig, gpgsig-sha256, mergetag). For each other name it prints the fixed label `header line N`, where N is the number of the line in the header, starting at 1. A continuation line counts under the name of the line it continues.
2. Encoding: the refusal of checkCommitEncodings prints the value only when it is a known charset name made of letters, digits, `-`, `_`, `.` and `:` with 40 characters or fewer, and when the redaction changes nothing in it. In each other case it prints `an encoding header that is not UTF-8` with no value.
3. Order of operations everywhere in this file: redact first, then cut for print. A cut value is never passed to the redaction.
4. `rev-list --reverse --topo-order` in checkCommitObjects, so that the comment "Oldest commit first" is true across a merge.
5. Tests in the package `secretpatterns` (use the fake values of `internal/testutil` or values built at run time; no realistic token): a function TestCheckPushRange_HeaderNameIsNeverPrinted, table-driven: a registered value of 20, 40, 41 and 80 characters as the name of an extra header line; a shape value longer than 40 as the name; a registered value with a space as one header line; a registered value with a colon. Each row: the push is refused, and no substring of 8 or more characters of the value is in the error text. The same table for `encoding VALUE`. A refusal for a hit in the author line still prints `author`. Across a merge with a parent dated after its child, both with a hit, the refusal names the parent.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/secretpatterns/` and `go test -race -count=1 ./internal/executor/ -run 'SecretGate|PushRange'` pass; paste the output into the PR body.
- [ ] Print the name of an unknown header line again -> the new table test fails.
- [ ] Cut before the redaction in checkCommitEncodings -> the encoding table fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A scan of tag objects. A value split over a header line and its continuation line. A decoded form of a value (base64) in a signature. The git identity of the agent user. Any change of the patterns.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6484 · hold comment of issue 6463

### LEG H11 — repo: qf-studio/pilot — title: fix(executor): run_as_user workspace clean-up — a commit after the last push keeps the workspace, the start-up sweep keeps a workspace whose commits are in no ref of the daemon's repository

probe: present qf-studio/pilot func[[:space:]]agentWorkspaceKeepReason\(
probe: present qf-studio/pilot func[[:space:]]finishAgentWorkspace\(
probe: present qf-studio/pilot func[[:space:]]sweepAgentWorkspaces\(
probe: absent  qf-studio/pilot func[[:space:]]agentWorkspaceTipSafe\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, each run works in a clone that the agent user owns (the workspace). finishAgentWorkspace (`internal/executor/agent_workspace.go`) removes the workspace at the end of the run only when its commits are safe outside it; agentWorkspaceKeepReason decides. A kept workspace is removed later by sweepAgentWorkspaces at the daemon start and by PruneOrphanedWorktreeForTask (`internal/executor/worktree.go`). A run-it review found:

- agentWorkspaceKeepReason returns "remove" as soon as the record says "pushed", and for a recovery ref it compares only the ref with the recorded SHA. Neither is compared with the tip of the workspace. A commit that the agent makes after a good push, or after a failed push and before the recovery ref, is removed with the workspace and no log line.
- The sweep and the prune remove a kept workspace with no check and one Info line. A workspace that was kept because its commits were in no ref loses them at the next daemon start.
- No test pins the rule "the workspace stays after a failed push": a change that records "pushed" before `git push` runs passes the suite.

The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `agent_workspace.go`, `worktree.go` and their test files. With the option empty nothing changes.

## Scope

1. A function agentWorkspaceTipSafe(ctx, wsPath, daemonRepo): it reads, as the agent user in the workspace (hooks off, replace refs off), the commit of HEAD and of each local branch. It returns true only when each of those commits exists in the daemon's repository and is contained in one or more refs of `refs/remotes/`, `refs/pilot-recovery/` or `refs/pilot/handover/` there (a daemon-side `git for-each-ref --contains`), or is the base of the workspace or an ancestor of it. A read error, a value that is not a full hex object name, or a commit the daemon's repository does not have gives false.
2. agentWorkspaceKeepReason: the "pushed" case and the recovery ref case also need agentWorkspaceTipSafe. When it is false the workspace stays, with the reason "a commit of the workspace is in no ref of the daemon's repository". The case "no commit above the base" stays as today.
3. sweepAgentWorkspaces and PruneOrphanedWorktreeForTask: before the removal of a workspace they call agentWorkspaceTipSafe. A workspace that is not safe stays, and one Error line names the task, the path and its age. A workspace that is not safe and older than seven days is removed with one Error line that says that its commits are lost. A workspace that is safe is removed as today.
4. Tests (mock sudo fixture of the workspace tests): a push that succeeds, then one more commit in the workspace -> the workspace stays and the Error line names it; a push that fails with no recovery ref -> the workspace stays (the test must fail when "pushed" is recorded before the push); a recovery ref, then one more commit -> the workspace stays; a pushed workspace with no later commit is removed; the sweep keeps a workspace with a commit in no ref and removes one whose tip is in `refs/remotes/origin/`; the sweep removes a workspace older than seven days with the "lost" line (the age through a seam); a workspace whose HEAD is broken stays.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentWorkspace|RunAsUser|Sweep|PruneOrphaned'` passes; paste the output into the PR body.
- [ ] Remove the call of agentWorkspaceTipSafe in the "pushed" case -> the test with the commit after the push fails.
- [ ] Record "pushed" before the push runs -> the failed-push test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The lifetime of the hand-over ref. The `gh` commands. The reads of `graph.json`. A new hand-over at the sweep. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6482

### LEG H13 — repo: qf-studio/pilot — title: fix(secretpatterns): the secret gate reads a quoted file path in its plain form — a secret in a file name with a quote, a backslash or a control byte is refused and never printed

probe: present qf-studio/pilot func[[:space:]]scanPatch\(
probe: present qf-studio/pilot func[[:space:]]describeEncoding\(
probe: present qf-studio/pilot func[[:space:]]scanCommits\(
probe: absent  qf-studio/pilot func[[:space:]]unquoteGitPath\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The secret gate (`internal/executor/secretpatterns/pushrange.go`) scans each commit of a push range. scanPatch reads the file path of a change from the header lines of the patch that git prints (`+++ b/PATH`, `rename to`, `copy to`, `diff --git`, `diff --cc`), scans the path for a secret, and puts the path into the refusal. The refusal names the commit and the place, never the value; its text goes to the error, a log line, an alert and a comment on the issue. A run-it review reproduced two defects with one cause:

- git prints a path that holds `"`, `\` or a control byte in a quoted form: in double quotes, with `\"`, `\\`, `\t`, `\n`, `\a` and octal forms such as `\001`. The gate scans and prints that quoted text. The redaction knows the plain value, its JSON form and its URL forms. The JSON form differs from the git form when the value also holds `<`, `>` or `&` (the JSON form of Go writes each of them as a six-character Unicode escape that starts with a backslash and the letter u; git writes the character itself) and for a control byte (JSON `\u0001`, git `\001`).
- Printed: a registered value with `"` and `&` as a file name, with the same value in the content, gives `push refused: commit 966923f50d1f adds a secret value in "b/Zq0fakeWZq1f\"eWZq2fakeW&Zq3fak"`.
- Not refused: a file with that name and clean content passes the gate, and the push sends the value in the file name. The same for a value with the byte 0x01 or 0x07.

Also, from the same review: scanCommits lists the commits with `git log --reverse` and checkCommitEncodings reads the default order, so across a merge with a parent dated after its child the refusal names the child, against the comment "oldest first".

This code runs for each push of the product. Keep the change small: only the items below, only in `pushrange.go` and its test files in the same package.

## Scope

1. A function unquoteGitPath: for a path text that starts with `"` and ends with `"` it returns the plain bytes (the escapes `\"`, `\\`, `\a`, `\b`, `\f`, `\n`, `\r`, `\t`, `\v` and octal `\NNN`); each other text is returned as it is; a text with a broken escape is returned as it is. The prefix `a/` or `b/` is removed after the unquote, as today for a plain path.
2. scanPatch takes each path through unquoteGitPath before the scan for a secret and before it is kept for the refusal. The refusal prints the plain path through the redaction and through the print function of today (control bytes are not printed raw).
3. Order: `--topo-order` for the `git log --reverse` of scanCommits, and checkCommitEncodings names the oldest commit with a hit (list in reverse topological order).
4. Tests in the package `secretpatterns` (values built at run time; no realistic token): a table of registered values as a file name with clean content — a value with `"` and `&`, with `\` and `<`, with the byte 0x01, with the byte 0x07, with a tab, with a newline, with non-ASCII text: each row is refused, and no part of 8 or more characters of the value is in the error text; the same table with the value also in the content; a rename to such a name; a normal path with a space and one with non-ASCII text pass with no secret; a table test for unquoteGitPath (plain, each escape, octal, a broken escape, an empty quoted text); across a merge with a parent dated after its child, both with a hit in the commit message, the refusal names the parent.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/secretpatterns/` and `go test -race -count=1 ./internal/executor/ -run 'SecretGate|PushRange'` pass; paste the output into the PR body.
- [ ] Scan the quoted text of the path again -> the row with `"` and `&` and clean content fails.
- [ ] Remove `--topo-order` from scanCommits -> the merge test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The list of charset names for the encoding print. A scan of ref names. The stderr text of git in errors. An empty file whose name holds ` b/`. Any change of the patterns.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6499

### LEG U4 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates — quality gates, workflow hooks, evidence commands and the lint fix run as the agent user with an allowlist environment

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
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

probe: present qf-studio/pilot \)[[:space:]]createAgentWorkspace\(
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

### LEG F3 — repo: qf-studio/pilot — title: test(executor): run_as_user — the opencode spawn-site subtest waits for its server command, so the kill-failure test passes on macOS

probe: present qf-studio/pilot func[[:space:]]TestRunAsUserKillFailureLogsOneErrorWithPgid\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserEverySpawnSiteGoesThroughSudo\(
probe: present qf-studio/pilot func[[:space:]]runAsBackendError\(

## Context

Navigator TASK-651. A review found one test defect in the run_as_user tests; production code is correct. On macOS the test TestRunAsUserKillFailureLogsOneErrorWithPgid in `internal/executor/agent_command_test.go` fails every time with two Error lines where it expects one. On Linux it passes, so CI is green.

Cause: the subtest "opencode server" of TestRunAsUserEverySpawnSiteGoesThroughSudo in `internal/executor/agent_command_sites_test.go` starts the server command and returns without a wait for it. The cancel of its context fires after the subtest ends, when the mock sudo script is no longer on PATH. The cancel then runs the real sudo of the host, which fails. The liveness check counts a process that has ended and is not yet reaped as alive on platforms other than Linux, so the Error line of that failed kill appears two seconds later, in the log buffer of the next test.

Reproduce on macOS: `go test -count=1 ./internal/executor/ -run 'TestRunAsUserEverySpawnSiteGoesThroughSudo/opencode_server|TestRunAsUserKillFailureLogsOneErrorWithPgid'`.

## Scope

1. In the opencode subtest, stop the server and wait for its command before the subtest returns, while the mock sudo is still on PATH (a t.Cleanup registered after the PATH setup runs too late; do it in the subtest body or register the cleanup so that it runs before PATH is restored). No process and no pending cancel may outlive the subtest.
2. Check every other subtest of that table for the same shape (a started command with no wait) and correct each one.
3. Make the log capture of TestRunAsUserKillFailureLogsOneErrorWithPgid safe against a late line of an earlier test: it counts only lines that hold the pgid of its own command.
4. Do not change a non-test file.

## Acceptance

- [ ] `go test -race -count=5 ./internal/executor/ -run 'AgentCommand|RunAsUser|ProcessGroup'` passes on Linux; paste the output into the PR body.
- [ ] State in the PR body which subtests had a command with no wait.
- [ ] The diff holds test files only.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Any change to production code. The liveness check for a process that is not yet reaped on macOS.

## Refs

- Navigator TASK-651 in this repo · review of PR #6361

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
| Step-5 ticket size (11-15) | one ticket; three tickets | three: git constructor, workspace, workspace files | the spec check found 110 git sites (not about 70), about ten daemon writes into the workspace and a base-ref problem; one PR of that size cannot get a run-it review |
| Base commit for the clone (founder approved 11-15) | agent fetches from the daemon's repository over the file transport; daemon sends a bundle | bundle on a stream from the daemon | same shape as the hand-over; no ownership exception and no shared read of the daemon's repositories, so the host-script finding on `.git/config` and world-readable files goes away; a full bundle of this repository takes 2 s and 23 MB |
| Project of an orphan workspace (11-15) | marker file in the workspace; hash in the directory name | hash of the repository path in the name | the daemon reads no file that the agent can change |
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

**Last Updated**: 2026-11-15

## Refs

- U0 → https://github.com/qf-studio/pilot/issues/6320
- U1 → https://github.com/qf-studio/pilot/issues/6321
- F1 → https://github.com/qf-studio/pilot/issues/6344
- B1 → https://github.com/qf-studio/pilot/issues/6345
- F2 → https://github.com/qf-studio/pilot/issues/6354
- F3 → https://github.com/qf-studio/pilot/issues/6366
- G1 → https://github.com/qf-studio/pilot/issues/6372
- B2 → https://github.com/qf-studio/pilot/issues/6373
- U2 → https://github.com/qf-studio/pilot/issues/6380
- N1 → https://github.com/qf-studio/pilot/issues/6385
- N2 → https://github.com/qf-studio/pilot/issues/6387
- B3 → https://github.com/qf-studio/pilot/issues/6389
- N5 → https://github.com/qf-studio/pilot/issues/6403
- N3 → https://github.com/qf-studio/pilot/issues/6406
- B5 → https://github.com/qf-studio/pilot/issues/6407
- N6 → https://github.com/qf-studio/pilot/issues/6411
- B6 → https://github.com/qf-studio/pilot/issues/6412
- B4 → https://github.com/qf-studio/pilot/issues/6416
- N4 → https://github.com/qf-studio/pilot/issues/6417
- P1 → https://github.com/qf-studio/pilot/issues/6419
- P3 → https://github.com/qf-studio/pilot/issues/6420
- B7 → https://github.com/qf-studio/pilot/issues/6422
- N7 → https://github.com/qf-studio/pilot/issues/6428
- B9 → https://github.com/qf-studio/pilot/issues/6430
- P2 → https://github.com/qf-studio/pilot/issues/6431
- H1 → https://github.com/qf-studio/pilot/issues/6434
- P4 → https://github.com/qf-studio/pilot/issues/6438
- H2 → https://github.com/qf-studio/pilot/issues/6445
- H5 → https://github.com/qf-studio/pilot/issues/6447
- B10 → https://github.com/qf-studio/pilot/issues/6450
- P5 → https://github.com/qf-studio/pilot/issues/6455
- B8 → https://github.com/qf-studio/pilot/issues/6459
- H3 → https://github.com/qf-studio/pilot/issues/6462
- H4 → https://github.com/qf-studio/pilot/issues/6463
- H6 → https://github.com/qf-studio/pilot/issues/6470
- B11 → https://github.com/qf-studio/pilot/issues/6473
- P6 → https://github.com/qf-studio/pilot/issues/6480
- H8 → https://github.com/qf-studio/pilot/issues/6488
- H10 → https://github.com/qf-studio/pilot/issues/6490
- H11 → https://github.com/qf-studio/pilot/issues/6492
- H9 → https://github.com/qf-studio/pilot/issues/6500
- H7 → https://github.com/qf-studio/pilot/issues/6501
- H13 → https://github.com/qf-studio/pilot/issues/6503
- H14 → https://github.com/qf-studio/pilot/issues/6514
- B12 → https://github.com/qf-studio/pilot/issues/6515
- P7 → https://github.com/qf-studio/pilot/issues/6516
- H15 → https://github.com/qf-studio/pilot/issues/6519
