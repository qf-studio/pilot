# TASK-651: Agent isolation — agent sessions under a separate Unix user, read-only GitHub token per run, secrets from SSM

**Status**: 🚀 **State 2026-11-21 14:52Z. With Pilot: nothing. The must-merge list is EMPTY: each leg before the removal of the preflight refusal is merged, and each merge has a review on the PR with no open should-fix finding.** **Next: U47 (the preflight no longer refuses the option) — FOUNDER HOLD, a draft; its probes are met on main (dry run 11-21 14:45Z); never dispatched without the founder's word.** The last four reviews of 11-21: PR#6671 (mutation check, epic path, writes through the workspace root) and PR#6675 (credential helper, token directory) with no finding that needs a fix; PR#6674 (the hook settings file never enters a commit) with a guard regression in the option-empty mode, closed by PR#6678 within one hour. **What no review shows:** nothing ran on Linux with a real second user; each "as the agent user" result is from macOS with a mock sudo. **Open design notes for the founder (not blockers):** a tracked `.claude/settings.json` that the agent itself commits carries the hook entries of the daemon; with the option empty the daemon no longer runs project hooks on its own pull and fetch (PR#6633; a release note is owed); the daemon files minor follow-up tickets on its own minor PRs (a chain of three on 11-20 and 11-21). **Merged 11-20, each with a review on the PR; no finding blocks the plan; the checklist lines hold the findings:** the four merges of the early morning (PR#6608, PR#6609, PR#6610, PR#6611), U57 (token file follow-up, PR#6619), U45 (lint as the agent user, PR#6625), U56 (gate start tests and preflight probe, PR#6628), H26 (token commands follow-up, PR#6633), U59 (workflow hooks follow-up, PR#6634), U61 (the mutation check on the PR creation path, PR#6637), U63 (the token provider on each start path, PR#6640), U53 (git credential helper of the agent side, PR#6644), the daemon-filed lint minors (PR#6645, diff read), U65 (one tool environment with no model names, PR#6650), U62 (the mutation restore check, PR#6651). Merged, review owed: U60 (PR#6649). **The pattern of the day:** each command of the agent side that is not the model session got names of the model (`ANTHROPIC_`, `CLAUDE_`) one at a time; U65 removes both prefixes in one place. **Release note owed:** with the option empty the daemon no longer runs the hooks of the project on its own pull and fetch (PR#6633). **Founder question open (11-20):** two runs at the same time share the agent user, so one run can read the token file of another run (see the checklist line below U52); options: accept and document (recommended), serial runs, a user for each project. **Merged in the night of 11-19 to 11-20, each with a review on the PR:** the quality gates run as the agent user and tests prove it (PR#6580, PR#6591, PR#6601), the evidence commands (PR#6605), the gh commands (PR#6586, PR#6598), the read-only token: mint, config and owner checks, token file (PR#6571, PR#6578, PR#6583, PR#6593), the secret gate regression fix (PR#6574). **U4 and U5 are written again (11-19): U41 to U47 and U51 to U53, in the order of their probes; U47 removes the preflight refusal and is a founder hold (a draft).** **Written, waits:** H21 (no program of the repository config under the token commands; after H25). Each merged PR of 11-19 has its run-it review on the PR. U41 (gates: seam in the quality package) merged as PR#6553; its review gave U48 (the remaining reads of the task directory), which goes before U42. **All host script legs are merged and reviewed (B12 was the last); the operator notes for the O4 run are in the checklist.** Product gap of the box incident: TASK-656 (planned, founder go). **Founder decisions 11-19 (mem-327):** one option for all projects, no `run_as_env`, the Go proxy variables pass and a GOPROXY with credentials refuses the start, the one PR that removes the preflight refusal is held as a draft. **Box, 11-19 (mem-326):** the quality gates failed for 16 hours because 133 orphan process trees of old agent runs loaded the box (load average 17 to 21 on 4 CPUs); the test gate then needed more than its limit of 10 minutes and was killed (`make test-changed exited with code -1`; no test fails). Founder go 10:04Z: the orphan trees are stopped, the gate limit is 20 minutes (backup of the config next to the file), the daemon restarted 10:30Z. The gate now passes in about 4 minutes. Open product gap: a run does not stop its background processes at its end. Merged on 11-17 to 11-19 (the checklist has each PR): H1 to H5, H8 (the hand-over transport is a plain pack since PR#6497; founder call 11-18, way A), H9 (environment filter of the daemon-side git, PR#6511, 20:12Z; run-it review 11-19: no `GIT_` name reaches git, but each other name does, tokens too → H15), H7 (push fixes, PR#6520, 11-19 11:14Z, first run on the quiet box; run-it review 11-19: no way for the agent to a false "pushed", two major gaps → H17), H17 (push: each inherited `GIT_` name dropped, `core.fsmonitor` off, PR#6533, 15:25Z), H22 (secret gate: binary file of a merge commit, PR#6566, 20:42Z), H12 (gh commands, PR#6565, 20:28Z), H20 (hand-over: recovery ref, PR#6557, 19:23Z), U48 (gates: reads through the seam, PR#6558, 19:34Z), H16 (workspace sweep, PR#6552, 18:24Z), H18 (push records, PR#6545, 17:43Z), B12 (host script notes, PR#6539, 17:17Z), H19 (secret gate: submodule path, PR#6540), P8 (firewall probe: status line at the time limit, PR#6542), H13 (secret gate: quoted file paths, PR#6523), H15 (the hand-over git gets only named variables, PR#6527), P7 (firewall probe: start line, strict marker, PR#6526), H10, H14 (no lazy fetch, object size limit, PR#6518, 11-19 10:46Z; run-it review 11-19: no blocker, seven minor notes), H11 (workspace clean-up rule, PR#6506; run-it review 11-19: no blocker, the notes go to H12), P2, P4, P5, P6, B8 to B11 (all host script legs), N7. Earlier: U0, U1, F1, F2, F3, B1 to B7, G1, U2, N2 to N6, P1, P3. **Must merge before the refusal is removed:** nothing is left. Then U47 removes the refusal: founder hold, a draft. `run_as_user` is refused by the preflight on every host until then. **O4 (host script on the box):** all host script legs are merged and reviewed, no blocker. The run does not go through SSM by hand: by the rule of the infrastructure owner (11-17) it goes into the infra repository as a PR (the script under `scripts/founder-box/`, the removal of the daemon user from the group wheel as its first step, a workflow on demand, dry run first, then the run in a window with no active task); answer of the infrastructure owner 11-18: nothing uses sudo as the daemon user, a reboot does not restore the group, a new image does (override planned in the box image). **Merge rule (founder, 11-17, mem-319):** no draft hold. The review of Pilot and green CI are sufficient for the merge; the daemon merges. The run-it review runs after the merge and its findings become follow-up legs. The founder is asked only for work that leaves the plan and for a step on the box. Legs stay small: the task limit of Pilot is one hour. Work rules from 11-18: after a retry of a parked ticket, compare the commit of the PR with the commit of the hold comment; before a dispatch, wait for an open daemon-filed ticket on the same files. **O1 done 2026-11-12.** Waiting for the founder: O2 (GitHub App), O3 (secrets to SSM), the infra repository PR for the host script, the decision round of TASK-656. Design: `.agent/system/agent-isolation-design.md`.
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
- [x] **B9** (PR#6449 merged 11-17 19:09Z; run-it review done, see the notes line above) ([#6430](https://github.com/qf-studio/pilot/issues/6430), dispatched 11-17 ~09:34Z; blocks O4) (the first run of Pilot from 09:52Z ended with no PR, the second from 13:45Z stalled; the third started the agent at 17:58Z and opened the PR at 18:56Z) from the review of PR#6421: the script reads the sudo rule list before the first change of a sudoers file (the daemon user of the box is in wheel; a rule for that group fails the proof after the cloud-init file is changed); tests for a run-as list and for a failed rule list command.
- [x] **B11** (PR#6479 merged 11-18 03:49Z; the run-it review reports after the merge; **O4 waits for that review**) ([#6473](https://github.com/qf-studio/pilot/issues/6473), dispatched 11-18 ~01:10Z; blocks O4) from the run-it review of PR#6458 (comment on the PR): when the removal of the sudo rule fails, undo still removes the firewall (end state seen: rule file present, no firewall); undo restores the cloud-init root line of the daemon user before the agent processes stop, so the daemon can start an agent process again during the undo; the removal message is false while a root line of the daemon user is active; a process list command that exits 0 with no output fails the step with no reason line; three branches with no test.
- **Notes from the review of PR#6458 that B11 does not cover:** the fault matrix (94 runs: each mocked command fails in turn, over six apply scenarios) found no end state with the rule file and an incomplete firewall on the apply side; a second apply that stops before the plan (exit 2: bad arguments, or no `--confirm-root-path` with an active cloud-init line) keeps the rule file whatever the firewall state is, and prints nothing about it; each failed step before the sudoers-rule step removes the rule also with a complete firewall (a new 0644 file in the data directory fails the daemon-private step on a second apply, the rule goes, and the cloud-init line stays commented out: the daemon user has no sudo until an apply passes) — **for O4 and the runbook: a second apply on a working host can stop the agent path**; no signal trap (an apply interrupted in a firewall step of a second run leaves the old rule file); the PR body lacks the two summary lines. **For O4:** stop the daemon before `--undo`. Not run: Linux, real sudo, iptables, systemd (CI log only: 696 and 204 passed).
- [x] **B10** (PR#6458 merged 11-17 22:08Z; run-it review done, see B11) ([#6450](https://github.com/qf-studio/pilot/issues/6450), dispatched 11-17 ~19:12Z; blocked O4) from the review of PR#6426: on a second run a failed firewall step leaves the sudo rule of the earlier run (seen twice in the mocked harness), so the script removes the rule on such a failure; undo removes the sudo rule first; a failed process list fails the undo step. (The `run_as_data_dir` line of the summary moved to issue 6432.)
- **Notes from the run-it review of PR#6472 (comment on the PR; not written as a leg; host script file, so after B11):** the quote function is correct (255 single bytes and 37 hostile strings: no output byte outside 0x20 to 0x7e, exact round trip in bash and sh; 711 passed on macOS and in CI); an open entry of root fails the step before a change. **One test gap: no test fails when the escape of 0xa0 to 0xff is removed** (both names of `shq_c1_case` hold 0x9b; add a name such as `a\xc3\xa9b`). The escaped form `$'...'` is for bash only: in dash a quoted name with a newline and a quote runs a command (no line made for a paste has this form). A nested `--agent-root DATA/outer/agent` is a dead end: no mode of `outer` lets the apply pass, and the message does not say so (the default agent root is not affected). With one chmod line and one hand-look entry the last sentence names only the chmod. The owner and group names of the new line are printed without the quote function. The owner check runs only for an entry that is open by mode: an entry of the agent user at 0700 passes the daemon-private step and fails only at the last step, after each change step ran. Not run: Linux, bash 5.
- [x] **B8** (PR#6472 merged 11-18 01:04Z; run-it review done, see the notes line above) ([#6459](https://github.com/qf-studio/pilot/issues/6459), dispatched 11-17 ~22:15Z) notes from the review of PR#6415: the quote function escapes every byte outside printable ASCII (with LC_ALL=C the class cntrl misses the bytes 0x80 to 0x9f), a test for the locale line, no chmod line for an entry of root, the closing sentence.
- [x] **P1** (PR#6425 merged 11-17 09:35Z; the run-it review reports after the merge) ([#6419](https://github.com/qf-studio/pilot/issues/6419), dispatched 11-16 ~22:05Z) Go side of the host layout, part 1: the option `run_as_data_dir`, the workspace default below the agent root, a mode check of the daemon's entries at the preflight and before each run, Go cache paths in the agent home.
- [x] **P2** (PR#6437 merged 11-17 15:33Z; the run-it review reports after the merge) ([#6431](https://github.com/qf-studio/pilot/issues/6431), dispatched 11-17 ~09:34Z) the preflight probes the firewall as the agent user: a control connection, the metadata address, the gateway port.
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
- [x] **B12** (PR#6539 merged 11-19 17:17Z after a new CI run; run-it review done 11-19, see the notes line below) ([#6515](https://github.com/qf-studio/pilot/issues/6515), dispatched 11-19 ~08:25Z; first run: PR#6525 failed CI on one ShellCheck warning (SC2115 in the test file) and the size guard of the automatic CI fix stopped it (207 added lines, limit 200); PR#6525 closed 11-19 ~14:45Z, a ShellCheck line added to the acceptance of the issue, ticket re-armed; second run: PR#6539 open since 15:30Z, its CI test job failed on TestRunAsFirewallProbeOutputLimit (a test of the firewall probe that depends on the host speed, not on the script), the size guard stopped the automatic fix again (250 added lines); branch updated with main 11-19 ~17:00Z for a new CI run) from the open notes of the run-it reviews of PR#6442, PR#6449, PR#6472 and PR#6479: the dry run runs the read-only check of the sudo rule list; a failed stop of the agent processes in the undo keeps the firewall; a dangling link or a link loop in the data directory is flagged as the Go preflight does; a summary line when the daemon user is left with no sudo rule; a test name with a byte of 0xa0 to 0xff for the quote function.
- **Notes from the run-it review of PR#6539 (comment on the PR, 11-19; no blocker for the run on the real host; all minor; not written as a leg):** the line "has no sudo rule now" reads only the cloud-init file: it prints when the daemon user has sudo through a second sudoers file, and it is absent when the file and its backups are both gone; a dry run on a host that is already set up, with a later group rule, exits 0 and the apply exits 1; `--undo --dry-run` prints "done (dry-run)" and "restore by hand" for the same file; the script comment on the Go preflight is wrong (Go passes a dangling link whose target is inside the data directory); two test gaps; the summary says "still run" where standard error says "may". Held: 869 passed, ShellCheck clean, the three mutations of the acceptance fail the suite, no undo run left agent processes with the firewall gone. Not run: Linux, root, the real test script.
- **Notes for the operator of the O4 run (from the same review):** run `--dry-run` first and read the exit status, not the last line (after a refusal each step still prints "done (dry-run)"); a dry run with exit 0 on a fresh host proves the check of the sudo rules, not the proof after the edit; before the apply, look for links in the top level of the data directory (a link that does not resolve stops the apply before the first change); keep the backup of the cloud-init sudoers file until the undo is done or the set-up is final; when an undo exits 1 with "processes of the agent user may still run", the firewall is still in place and the rule file is gone: stop the daemon, stop the processes of the agent user, run `--undo` again; stop the daemon before `--undo`; check a "has no sudo rule now" line with `sudo -l -U` for the daemon user before any action.
- [x] **P8** (PR#6542 merged 11-19 16:55Z; run-it review done 11-19, see the notes line below) ([#6530](https://github.com/qf-studio/pilot/issues/6530), dispatched 11-19 ~15:10Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6526 (comment on the PR, 11-19), two major findings: at the time limit only the start line is read, so an output with the start line and the status `0` from a sudo that ends late gives "no answer" and the metadata check passes with a connection that was made (reproduced with a mock sudo); the new start line test fails on a loaded host (300 ms limit for a new script; 7 of 8 runs on a loaded macOS host). Also: `bash -p` (the two long options do not stop `BASH_ENV`), a held output pipe, a status with no start line, a check of the host and port text, four test gaps.
- **Notes from the run-it review of PR#6542 (comment on the PR, 11-19; no major; not written as a leg):** no way found for the preflight to pass with a connection that was made or with a probe that did not run; the findings 1, 3, 4, 6 and 7 of the PR#6526 review are closed. The CI failure of TestRunAsFirewallProbeOutputLimit (seen once, on PR#6539) did not occur in 16 idle and 16 loaded runs; possible causes, not proven: the limit of 3 seconds for the control connection, or the control listener takes the port that the metadata or gateway listener freed. Minor: no test pins the wait delay error case, the value 4096 and the over-limit case at the time limit; the port texts `+80` and `080` run as 80; the PR body has no test output.
- [x] **P7** (PR#6526 merged 11-19 13:15Z; run-it review done 11-19, see P8) ([#6516](https://github.com/qf-studio/pilot/issues/6516), dispatched 11-19 ~08:25Z; must merge before the removal of the preflight refusal) from the partial run-it review of PR#6494: a metadata probe with no answer counts as blocked only when the probe printed its start line; `bash --norc --noprofile`; the marker status is exactly `0` or `1`; an output limit; a port above 65535 is refused at once.
- **Notes from the run-it review of PR#6518 (comment on the PR, 11-19; all minor; for H12 or a later leg):** the partial clone refusal reads `promisor` only as `true`, `yes`, `on`, `1` or empty, and git reads each integer other than 0 as true (`promisor=2` is not refused; `GIT_NO_LAZY_FETCH` still holds on git 2.44 and later, the box has 2.50.1; refuse each value that is not false); a loser of two hand-overs that fails on the lock file of the winner still deletes the ref of the winner (forced interleaving only; 0 of 80 natural rounds); the in-progress path leaves the pack of the loser in `objects/pack`; the size limits run after index-pack and count only objects in the range of the tip; `LC_ALL=C` on the update-ref has no test; the concurrency test accepts two SHAs; the quarantine age now includes the stream time. Held: finding 1 of the PR#6497 review is closed (refusal after one `git config` command; with the refusal off no fetch starts); both mutations of the acceptance fail the named tests. Not run: Linux, a real second user, git older than 2.44.
- [x] **H14** (PR#6518 merged 11-19 10:46Z, first run, gate passed in 3m31s after the stop of the orphan processes; run-it review done 11-19, see the notes line above) ([#6514](https://github.com/qf-studio/pilot/issues/6514), dispatched 11-19 ~08:20Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6497 (comment on the PR): in a daemon repository that is a partial clone, the object check itself makes git fetch a missing object from the promisor remote into the quarantine, and the check then passes: an object that the agent only named reaches the hand-over ref (reproduced; with `GIT_NO_LAZY_FETCH=1` the same case is refused and no fetch starts). Also: no test for the check of trees and blobs and for the format check of the tip; the temporary pack file of a killed daemon is never removed; the loser of two hand-overs of one task deletes the ref of the winner; the size limit covers the stream and not what it inflates to.
- **Notes from the review of PR#6497 that H14 does not cover:** in a normal daemon repository about 60 forged streams found no way for an object of the daemon's repository to the hand-over ref (thin packs and deltas against a private object fail at index-pack; a named private parent, tree or blob fails at verifyHandOverObjects, which is the line of defence: `--strict` accepts links into the alternate); each failure leaves no file and no ref; no false refusal for a merge commit, 2000 files, a second hand-over, re-created content; a gitlink that names a private commit is accepted (a push does not send it); the sweep uses the mtime of the quarantine directory, so a hand-over that runs for more than one hour can lose its quarantine (fails closed); with GIT_COMMON_DIR in the daemon environment the quarantine is made in the other repository (H9). Not run: a real second user, Linux, SHA-256 repositories, a shallow daemon repository.
- [x] **H8** (PR#6497 merged 11-18 13:49Z; run-it review done, see H14; the run-it review reports after the merge) ([#6488](https://github.com/qf-studio/pilot/issues/6488), dispatched 11-18 ~10:40Z; replaces H6; must merge before the removal of the preflight refusal) hand-over by a pack stream: the agent user sends `git pack-objects --stdout --revs` (not thin); the daemon checks the pack signature, runs `git index-pack --strict` with no `--fix-thin` into a quarantine object directory, requires each object of the range in the quarantine alone, then moves the pack and sets the ref; a failure leaves nothing; a branch with nothing above the base gives errHandOverNothingNew.
- [x] **H15** (PR#6527 merged 11-19 13:50Z; run-it review done 11-19, see the notes line below) ([#6519](https://github.com/qf-studio/pilot/issues/6519), dispatched 11-19 ~10:55Z, after the merge of H14; must merge before the removal of the preflight refusal) from the run-it review of PR#6511 (comment on the PR, 11-19): the filter of the daemon-side git removes names by the prefix `GIT_` and passes each other name, so `GITHUB_TOKEN` and each other token reach all nine git processes of a hand-over (reproduced; the contract of the issue kept the other names); the global git config of the daemon user still acts (a trace2 target writes a file); no test for the `GIT_NO_REPLACE_OBJECTS=1` line of the update-ref; the wait delay test skips on a host with no `setsid`.
- **Notes from the run-it review of PR#6527 (comment on the PR, 11-19; no blocker, no major; for H16 and H12):** no token and no config outside the daemon's repository reaches one of the ten daemon-side git processes of a hand-over; each name with the prefix `LC_` still passes (use exact names, or drop them and set `LC_ALL=C`); `git remote get-url origin` of ghDirForDaemonRepo no longer applies a global `insteadOf`, so the origin of the daemon's repository must be a literal GitHub URL (for H12: say it, and let the preflight check it); for a daemon repository of another owner the partial clone check reads exit 1 as "no key" (use `config --local`) and the advice of git to set a global `safe.directory` has no effect; the hook marker assertion of the row `no global config` cannot fail; `LC_ALL=C` on the update-ref has no test. Not run: Linux, a real second user.
- **Notes from the run-it review of PR#6511 that H15 does not cover:** the secret gate runs nine git commands with the full daemon environment (`secretpatterns/pushrange.go`); with a foreign `GIT_DIR` it fails closed, no bypass was built; item 5 of H7 (daemonGitEnv) covers these commands with a list of denied names, so after H7 check that tokens are out there too (for H12). Held: 43 hostile `GIT_` names, none reaches one of the nine processes; both mutations of the acceptance fail the named tests. Not run: Linux, a real second user, the system git config.
- [x] **H9** (PR#6511 merged 11-18 20:12Z on the third gate run; run-it review done 11-19, see H15) ([#6500](https://github.com/qf-studio/pilot/issues/6500), dispatched 11-18 ~15:25Z) the daemon-side git of the hand-over gets an environment by allow list (run-it review of PR#6469: `GIT_COMMON_DIR` moves the object store to another repository with a success result, `GIT_EXEC_PATH` runs programs of a named directory, `GIT_GRAFT_FILE` makes the ancestor check accept an orphan history); tests for the guards that no test pins.
- **Notes from the run-it review of PR#6469 that H8 and H9 do not cover:** the four fixes of H5 hold (seven bad tips refused with the ref removed; the return comes 15 ms after a cancel with a 300 MB bundle and no process survives); a merge commit whose second parent is an unrelated history is a legal tip, so the whole of that history is in the push range and only the secret gate stands before the remote; a header line of 100 MB takes 1 m 37 s (gone with the pack transport). Not run: real sudo, Linux, a partial-clone daemon repository through handOverBranch.
- [x] **H6** (REPLACED by H8 on 11-18; [#6470](https://github.com/qf-studio/pilot/issues/6470) closed with no PR), dispatched 11-18 ~00:30Z; **parked by Pilot (`pilot-needs-human`), branch `pilot/GH-6470` at d3a4d94d7, no PR:** the pre-PR review names two blocking defects. (1) A forged thin bundle gets a commit of the daemon's repository past verifyHandOverObjects: the fetch unbundles with `index-pack --fix-thin`, a delta in the stream can name the private commit as its base, and index-pack copies that base out of the alternate into the quarantine pack, where the check then finds it (read from the git source by the reviewer, not run). (2) A failed update-ref after the move of the objects leaves the packs in the daemon's repository. **Design consequence: "the object is in the quarantine" is no proof while a thin pack is accepted. Options: the agent side sends a pack that is not thin (`git pack-objects --stdout --revs` with no `--thin`) and the daemon runs `git index-pack --strict --stdin` with no `--fix-thin`; or the check compares each object of the range with the daemon's object store before the hand-over. Waits for a founder call (it changes the transport of design section 4.2).** Must merge before the removal of the preflight refusal) from the run-it reviews of PR#6443 and PR#6461: the hand-over accepts only commits whose objects came in the bundle. Seen through the push of H2: a workspace commit whose parent is a commit that only the daemon's repository has (listed in `.git/shallow` of the workspace) was handed over, passed the gate (it scans for secrets only) and was **pushed to the remote**. Also: a failed hand-over leaves no pack; a branch with no commit above the base gives a typed "nothing to hand over" result, not a bundle error.
- [x] **H16** (PR#6552 merged 11-19 18:24Z; run-it review done 11-19, see the notes line below) ([#6536](https://github.com/qf-studio/pilot/issues/6536), dispatched 11-19 ~15:45Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6506: the sweep reads each ref of the workspace (a stash, a tag), skips a workspace with a live record, uses the directory time for an unknown age, counts one removal once.
- **Notes from the run-it review of PR#6552 (comment on the PR, 11-19; no blocker, no major; the findings 2 to 6 of the PR#6506 review are closed; no removal outside the workspace root; not written as a leg):** `rev-parse HEAD --all` misses the HEAD and `refs/worktree/` of a linked worktree inside the workspace, so such a commit goes with no Error line; the fallback age is the time of the directory, which the agent user can refresh: the workspace is then kept with no limit; a directory with the suffix `.bundle` is removed with no tip check (older code); 30000 refs overflow the argument list and the workspace is kept as not safe; a live record that is never cleared keeps the workspace until a restart, with a Debug line only; three guards with no test, one vacuous subtest. Candidate for a small leg (the file is free). Not run: a real second user, real sudo.
- [x] **H20** (PR#6557 merged 11-19 19:23Z; run-it review done 11-19, see the notes line below) ([#6537](https://github.com/qf-studio/pilot/issues/6537), dispatched 11-19 ~15:45Z; must merge before the removal of the preflight refusal) from the run-it reviews of PR#6506, PR#6518 and PR#6527: the old hand-over ref becomes a recovery ref before its removal (the major finding of the PR#6506 review); the loser of two hand-overs deletes no ref and leaves no pack; each promisor value is refused, `config --local`; no `LC_` prefix rule, `LC_ALL=C` for each command.
- **Notes from the run-it review of PR#6557 (comment on the PR, 11-19; no blocker, no major; the eight earlier findings of three reviews are closed; not written as a leg):** `config --local` skips files of `include.path` and the worktree config, so a promisor key there is not refused (`GIT_NO_LAZY_FETCH` still holds); with a forced order a loser with the same tip removes the pack while the winner holds the lock and has no ref yet: both fail closed (the daemon filed [#6559](https://github.com/qf-studio/pilot/issues/6559) for it); a lock file that stays after a crash blocks the hand-over of the task until a person removes it; a recovery ref is also made for a commit that is handed over again: 50 retries leave 49 refs and 150 pack files, and no rule removes them; three guards with no test; the PR also changed `git.go` (the environment of the secret gate commands loses `LANG` and the `LC_` names). Candidate for a small leg: a removal rule for recovery refs and a rule for a stale lock. Not run: Linux, a real second user.
- [x] **H17** (PR#6533 merged 11-19 15:25Z, first run; run-it review done 11-19, see H21) ([#6529](https://github.com/qf-studio/pilot/issues/6529), dispatched 11-19 ~14:40Z, after the merge of the daemon-filed ticket 6522 (PR#6528, 14:34Z); must merge before the removal of the preflight refusal) from the run-it review of PR#6520 (comment on the PR, 11-19), two major findings: the list of denied names of the delivery git misses `GIT_COMMON_DIR` (the push goes to the remote of another repository and Push returns nil; reproduced) and `GIT_GRAFT_FILE` / `GIT_SHALLOW_FILE` (the gate reads another history and passes; the remote stopped the push); the hooks-off arguments do not turn off `core.fsmonitor`, so a program of the repository config runs under the fetch of the count with the token, with the option set and with it empty (reproduced). Also: no token for the git commands of the secret gate (from the PR#6511 review); two guards with no test.
- [x] **H18** (PR#6545 merged 11-19 17:43Z; run-it review done 11-19, see the notes line below) ([#6534](https://github.com/qf-studio/pilot/issues/6534), dispatched 11-19 ~15:30Z, after the merge of H17; must merge before the removal of the preflight refusal) from the run-it review of PR#6520, minor findings: a forgiven push records nothing; the count comes from the workspace after a refused gate; the loser of two Push calls is forgiven; the loop stores the HEAD of the workspace as pushedWIPSHA; lastHandOverSHA has no reader.
- **Notes from the run-it review of PR#6545 (comment on the PR, 11-19; no major; the findings 3 to 7 of the PR#6520 review are closed for their cases):** the auto-preserve push (`git_freshness.go`) still stores the HEAD of the workspace as pushedWIPSHA, so with a detached HEAD it names a commit that is not on the remote; before the first push attempt, and on a new GitOperations value, the count of new commits runs in the agent workspace; with branch X published and no push attempt for branch Y the count of X is given for Y; recordPublishedHandOver accepts an empty or unrelated SHA (not reachable today); three guards have no test. The daemon filed [#6548](https://github.com/qf-studio/pilot/issues/6548) for four more items of the same PR (one of them: the nothing-new result is now inside the typed hand-over error, so its branch in the push loop is not reached). Candidate for a small leg after H21 (same file); not written.
- [x] **H21** (PR#6608 merged 11-20 04:10Z; run-it review 11-20, comment on the PR: the four named commands hold; with the option empty ten other token commands still run a hook or `core.fsmonitor`, plus `core.alternateRefsCommand`, a signed push and `fetch.bundleURI` → H26) ([#6599](https://github.com/qf-studio/pilot/issues/6599), dispatched 11-20 ~01:55Z, after the merge of H25) from the run-it review of PR#6533 (comment on the PR, 11-19): with a network remote, `credential.helper`, `core.sshCommand`, `core.gitProxy` and an `ext::` remote of the repository config start a program under the commands that carry the token (minor with the option set: the config is the daemon's own); an inherited `GIT_TERMINAL_PROMPT=0` is dropped when the provider gives no token; the local count and the co-author read still get tokens; one test gap. Held: both major findings of the PR#6520 review are closed.
- [x] **H12** (PR#6565 merged 11-19 20:28Z; run-it review done 11-19, see H23) ([#6546](https://github.com/qf-studio/pilot/issues/6546), dispatched 11-19 ~17:50Z, after the merge of H18; must merge before the removal of the preflight refusal) from the run-it review of PR#6482: the `gh` repository argument keeps the host of the remote; a final `.git/`; no credential in the parse error; six `gh` calls of the epic code leave the workspace; the graph file read has a time limit and refuses a FIFO.
- [x] **H23** (PR#6586 merged 11-19 23:52Z; run-it review done 11-20, see H25) ([#6569](https://github.com/qf-studio/pilot/issues/6569), dispatched 11-19 ~21:30Z, after the merge of the daemon-filed ticket 6548 (PR#6568); must merge before the removal of the preflight refusal) from the run-it review of PR#6565 (comment on the PR, 11-19), one major finding: handleSubIssueCoverageGap gives the execution path to the label and comment helpers, so `gh issue comment`, `gh label create` and `gh issue edit` still run in the workspace with no `--repo` (reproduced with a fake `gh`). Also: the fallback directory of getSubIssuePRState; the graph file check and read in one open, a size limit, a budget for all graph reads of a delivery; a cancelled context is not a missing file. Held: the six calls that the issue named run in the daemon's repository with `--repo`; ghRepoArg keeps the host and prints no credential. Minor, not in the leg: `ssh.github.com` and `www.github.com` keep the host; the owner and the name text are not checked.
- [x] **H25** (PR#6598 merged 11-20 01:42Z; diff read by the session, comment on the PR) ([#6589](https://github.com/qf-studio/pilot/issues/6589), dispatched 11-20 ~00:15Z; before H21, same file; must merge before the removal of the preflight refusal) from the run-it review of PR#6586 (comment on the PR, 11-20), one major finding: the budget of 30 seconds for the graph reads also counts reads that succeed; the graph of this repository needs 879 sudo reads (16 seconds on a fast host), so on a slower host the budget is used up by good reads and the guards that use the graph are skipped. Also: fetchIssueState takes the repository for `gh issue view` from the remote of the workspace; a GitOperations value for a workspace with no daemon repository runs PR commands in the workspace (no caller today). Held: the label, comment and PR state calls run in the daemon's repository with `--repo`. Minor, not in the leg: a FIFO costs 2 seconds for each read; the open time limit includes the start of sudo; three test gaps; the PR body has no list of call sites.
- [x] **H26** (PR#6633 merged 11-20 12:58Z — the first PR#6631 failed CI, CI fix 6632 on the same branch, one fixture line; run-it review 11-20, comment on the PR: no blocking finding; with the option set no program of the repository runs with the token. Open notes, option empty only, NOT in a leg (in that mode the agent is the daemon user and reads the token directly): the merge half of `git pull` runs filter, merge driver and signing programs; `remote.origin.uploadpack` and `receivepack` run a program; a proxy of the repository config gets the token; no test pins `-c fetch.bundleURI=`; the source read test reads two files only. **Release note owed:** with the option empty the daemon no longer runs the hooks of the project (`post-merge`, `reference-transaction`) on its own pull and fetch) ([#6615](https://github.com/qf-studio/pilot/issues/6615), dispatched 11-20 ~09:34Z) from the run-it review of PR#6608: hooks off in the one credential wrapper for each git command with the token, `core.alternateRefsCommand` and `fetch.bundleURI` off, no signed push, a test for `GIT_PROXY_COMMAND` with the option set. Each point needs the option empty; not on the must-merge list.
- **Held in the run-it review of PR#6520:** no path from the agent side to a false "pushed" or to a publish with no gate; both mutations of the acceptance fail the named tests. Not run: Linux, a real second user, network remotes, the PR creation with `gh`.
- [x] **H7** (PR#6520 merged 11-19 11:14Z: five failed runs under the box load, first run after the box fix; run-it review done 11-19, see H17 and H18) ([#6501](https://github.com/qf-studio/pilot/issues/6501), dispatched 11-18 ~15:30Z; its probe for ghDirForDaemonRepo was corrected to the method form; must merge before the removal of the preflight refusal) from the run-it review of PR#6461: the "already published" forgiveness after a failed push compares the remote head with the HEAD of the workspace, which the agent controls, so a failed hand-over reads as a delivered push; the hand-over SHA is recorded before the gate, so the count of new commits uses a refused commit; a push with nothing above the base is retried as an infra failure; the fetch of the count and two `ls-remote` probes run with credentials and without the hooks-off arguments; four guards with no test.
- **Notes from the review of PR#6461 that H6 and H7 do not cover:** no daemon-user git command ran in the workspace in any driven case (20 daemon git calls over the delivery methods, 0 in the workspace; 0 marker lines from the daemon side with a hostile workspace config of 20 keys and 8 hooks); the gate refused each secret case (later removed, merged side branch, merge commit, orphan history, commit message, binary attribute, replace ref, moved remote-tracking refs); branch names with `:`, `..`, `^`, `*`, spaces are refused and names such as `--mirror` or `+plus` go to `refs/heads/NAME` only; the refused commit stays under `refs/pilot/handover/TASK` until the next hand-over (the lifetime of the ref is in H3); with `GIT_DIR` in the daemon environment the gate, the push with credentials and the probes would follow it (H5 covers `handover.go` only: **H7 takes the same environment cleanup for the delivery commands**); a token in the environment of the agent-side bundle command is not caught by a test under the mock sudo (real `env_reset` not exercised). Not run: real sudo, Linux, the `default_branch` setting of the gate.
- [x] **H2** (PR#6461 merged 11-17 22:47Z; run-it review done, see H6 and H7) ([#6445](https://github.com/qf-studio/pilot/issues/6445), dispatched 11-17 ~18:20Z) hand-over, part 2: the secret gate, the push and the remote probes run in the daemon's repository on the hand-over ref.
- **Notes from the run-it review of PR#6506 (comment on the PR, 11-19; no blocker; all for H12):** after a failed push the kept workspace is removed by the next sweep or prune, because the hand-over ref counts as safe, and the next hand-over of the task deletes that ref: the commit is then in no ref (reproduced; the fix is a recovery ref before the removal, or no hand-over ref in the safe list of the sweep); only HEAD and `refs/heads/` are read, so a commit only in a stash, a tag or a custom ref goes with no Error line (`rev-parse HEAD --all`); with no record, a workspace on a base that only a local branch of the daemon holds is kept seven days and then logged as lost; two sweeps at once count one removal twice; an age that cannot be read keeps a workspace for ever; the sweep does not skip a workspace with a live record (older than the PR). Guards with no test: the unknown age, the `.git` check for a link and a file, the hex check of the tips, the sweep with no record. Held: no removal outside the workspace, both mutations of the acceptance fail the named tests.
- [x] **H11** (PR#6506 merged 11-18 17:32Z after a second attempt (the quality gates failed once); run-it review done 11-19, see the notes line above) ([#6492](https://github.com/qf-studio/pilot/issues/6492), dispatched 11-18 ~11:40Z) from the run-it review of PR#6482 (comment on the PR): the clean-up removes a workspace as "pushed" also when the agent made a commit after the last push, with no log line; the sweep at the daemon start and the prune of a task remove a kept workspace with no check, so its commits are lost; no test for the rule "keep the workspace after a failed push" (a mutation that records "pushed" before the push passes the suite).
- **Notes from the review of PR#6482 for a later leg (H12, to write after H7: `git.go` and `epic.go`):** ghDirForDaemonRepo keeps only the last two path segments of the daemon's remote, so a GitHub Enterprise or GitLab remote gives `--repo owner/name` with no host and `gh` resolves it against github.com with the daemon token (derivation reproduced, real gh not run); a remote that ends in `.git/` gives `owner/name.git`; the parse error for a one-segment remote with credentials prints the credential; three more `gh` commands run in the workspace (`epic.go`: sub-issue, label, comment calls take the execution path); a FIFO in place of `graph.json` stalls each read for 30 s or more and the reads use a background context; `refs/pilot/handover/TASK` stays for ever after a sweep and the next hand-over of the task deletes it, also when it is the only copy of the commits. Held in the review: the four `gh` commands ran in the daemon's repository with the daemon's `--repo` and `--head` against a hostile workspace config; the recovery ref is made in the daemon's repository; "pushed" is the exit status of the daemon's own push. Not run: a real second user, real sudo, real gh.
- [x] **H3** (PR#6482 merged 11-18 04:31Z; run-it review done, see H11 and the notes line above) ([#6462](https://github.com/qf-studio/pilot/issues/6462), dispatched 11-17 ~22:55Z) hand-over, part 3: the four `gh` commands and the recovery ref use the daemon's repository; the workspace stays until its commits are safe; the graph reads go through the helper.
- [x] **H24** (PR#6574 merged 11-19 22:17Z; run-it review done 11-19, see the notes line below) ([#6572](https://github.com/qf-studio/pilot/issues/6572), dispatched 11-19 ~22:05Z) from the run-it review of PR#6566 (comment on the PR, 11-19): the bypass is closed (both reproductions and 21 more merge forms are refused, nothing printed), but the PR brought a regression: the first-parent patch of the second scan replaces the combined patch, so a merge of the published base into the work branch, with one binary file that differs from both parents, is refused when the delta of the base holds a registered value or is over the size limit of the scan. An honest push is refused; the code runs for each user. Fix: the second scan reads only the binary paths, and the combined patch stays. Also: the refusal names git 2.31; six test pins.
- **Notes from the run-it review of PR#6574 (comment on the PR, 11-19; the regression is closed and no bypass came back; all minor, all fail closed; not written as a leg):** a binary path that holds the text ` b/` is refused as an honest push in a merge (new); on macOS a path in decomposed Unicode form with `core.precomposeunicode` is refused (new); about 10000 binary paths in one merge are over the argument limit and are refused (5000 pass); four arguments of the second scan have no test; `GIT_LITERAL_PATHSPECS=1` in the environment refuses each merge with a binary line. Candidate for one small leg with the other minor notes of the secret gate reviews. Not run: Linux, a real git older than 2.31.
- [x] **H22** (PR#6566 merged 11-19 20:42Z; run-it review done 11-19, see H24) ([#6561](https://github.com/qf-studio/pilot/issues/6561), dispatched 11-19 ~19:50Z, after the merge of the daemon-filed ticket 6541 (PR#6560)) from the run-it review of PR#6540 (comment on the PR, 11-19), in older code: git does not apply `--text` to the combined diff of a merge commit, so a file with a NUL byte or the attribute `binary` that a merge adds is not read; when a later commit deletes the file, or no base resolves, the gate passes and the push sends the value (reproduced with a real push; no config needed). Fix: a second scan of that commit against the first parent, else a refusal. Also test pins for the two submodule arguments and for the cut after the redaction.
- **Notes from the run-it review of PR#6540 that H22 does not cover:** the base of the scan comes from the remote-tracking refs of the local repository; a session that can write them (`git update-ref refs/remotes/origin/main`, or a forged `refs/remotes/zz/x` on the no-base path) moves the base past a commit with a value, and the gate passes (reproduced with CheckPush alone; the callers and their fetch were not run). With `run_as_user` set the gate runs in the daemon's repository, whose refs the agent cannot write; with the option empty the session runs as the daemon user. A fix takes the base from the remote (`ls-remote`) and needs a design for credentials in the gate: a later leg of TASK-650, not written. Minor: a part of a value next to a full value in one path is printed; UTF-16 content passes. Held: all 12 submodule rows of the PR#6523 review are refused and nothing is printed.
- [x] **H19** (PR#6540 merged 11-19 15:56Z; run-it review done 11-19, see H22) ([#6535](https://github.com/qf-studio/pilot/issues/6535), dispatched 11-19 ~15:30Z, after the merge of H17) from the run-it review of PR#6523 (comment on the PR, 11-19): in older code, not from the PR — a registered value in the path of a submodule entry passes the gate when a diff setting changes the print of submodules (`diff.submodule`, `diff.ignoreSubmodules`, `ignore = all` in `.gitmodules`; 12 cases reproduced; two fixed arguments on the two scan commands close them). This code runs for each user. Also: no length limit for the printed path; three test gaps. Held: the quoted path cases of the issue are refused and no part of a value is printed.
- [x] **H13** (PR#6523 merged 11-19 11:41Z; run-it review done 11-19, see H19) ([#6503](https://github.com/qf-studio/pilot/issues/6503), dispatched 11-18 ~16:10Z) from the run-it review of PR#6499 (comment on the PR): the three header leaks are closed in the error, the log line, the alert and the comment. One leak is left in older code, the file path print: git prints a path with `"`, `\` or a control byte in its quoted form, and the redaction does not know that form when the value also holds `<`, `>` or `&` or a control byte. Such a registered value as a file name is printed whole in the refusal; and a file with that name and clean content is **not refused, and the push goes out** with the value in the file name. This code runs for each user of the product.
- **Notes from the review of PR#6499 that H13 does not cover:** `header line N` is the right line of `git cat-file -p` in each case tried; no false refusal for signed commits; 2000 commits cost 0.10 s; an encoding value that is a part of 40 or fewer characters of a longer registered value is printed (the object holds only that part); an empty new file whose name is a registered value that holds ` b/` is not refused; the stderr of git is printed with no redaction for a ref that does not resolve, only through CheckPushRange and not through the product path. Not run: Linux, invalid UTF-8 in a path, a rename with a quoted path.
- [x] **H10** (PR#6499 merged 11-18 15:18Z; run-it review done, see H13; released in v2.324.0; the run-it review reports after the merge) ([#6490](https://github.com/qf-studio/pilot/issues/6490), dispatched 11-18 ~11:15Z) from the run-it review of PR#6484 (comment on the PR): the refusal text of the secret gate prints up to 40 characters of a secret in three cases — the name of an extra header line (a registered value of 41 or more characters, or a shape value longer than 40), the value of an `encoding` header longer than 40 (older code, GH-6343), and a value with a space written as one header line. The text goes to the error, the log line, the alert and the issue comment. **This code runs for each user of the product, with the option empty too.** PR#6484 merged the commit cc24cf2fe that the hold comment named: the retry of 11-18 did not run the agent again, so neither point of the retry comment landed (the label removal alone made the daemon open the PR for the pushed branch).
- **Notes from the review of PR#6484 that H10 does not cover:** the header scan refuses each place tried (author, committer, a second encoding line, an invented header, continuation lines, a header after gpgsig, mergetag, CRLF, a NUL byte, the root commit, a merge commit, the first and last commit of 2000); no false refusal for 150 signed commits (GPG ed25519, GPG rsa4096, SSH ed25519), tree and parent lines, token-like emails, a long non-ASCII name; 2000 commits cost 0.10 s and 6 git processes; the identity of the agent works with no identity in the workspace, and a newline or `<>` in user.name of the daemon's repository injects no header; **not refused:** a value split over a header line and its continuation, the base64 form of a value in gpgsig, the message of an annotated tag (tag objects are not scanned; read: no push sends a tag); a `user.name` of only `<>` in the daemon's repository makes each commit of the agent user fail. Not run: Linux, real sudo.
- [x] **H4** (PR#6484 merged 11-18 06:03Z; run-it review done, see H10) ([#6463](https://github.com/qf-studio/pilot/issues/6463), dispatched 11-17 ~22:55Z; parked by Pilot 11-18 02:23Z: the pre-PR review found one blocking defect (a part of a secret in the name of an extra header line can show in the refusal) and the fix round had no time left; branch `pilot/GH-6463` at cc24cf2fe; started again 11-18 ~03:55Z with a comment that narrows the retry to that defect and one comment fix) a git identity for the commits of the agent user; the secret gate also scans the header lines of a commit.
- **Spec check 11-17 for U4 and U5 (history; the blocks were written again on 11-19, see the line above the U41 line):** U4 becomes three parts: (a) a command builder with configure and kill hooks in `internal/quality` (the quality runner replaces the process attributes of an agent command, so a constructor alone is not sufficient), wired at the gate command and both scope commands; (b) workflow hooks, evidence commands, both lint commands, the env context file read and the settings restore failure; (c) the removal of the preflight refusal, alone, as the last PR. U4 changes `agent_command.go`, so it follows P2 and P4. `run_as_env` is stale: the Go paths are set since P1. U5 becomes two parts: (a) a scoped token (repositories and permissions in the mint request; the mint of today sends no body), a token file with refresh, `GH_TOKEN` set in the gh-guard subcommand, the credential helper entries also for the gates; (b) the override for each project, which is a refactor (the run-as state is one value for the process: 21 reads, 16 spawn callers, none with a project).
- **Founder decisions 11-19 ~14:55Z ("ok" to the four recommendations of the spec check 11-17):** (1) one `run_as_user` option for the daemon, for all projects; no override for each project. (2) `run_as_env` is dropped. (3) The Go proxy variables of the daemon (GOPROXY, GOFLAGS, GONOSUMDB, GOPRIVATE) pass to the agent; the start is refused when the value of GOPROXY holds credentials. (4) The one PR that removes the preflight refusal is held as a draft for the founder; each other PR keeps the merge rule (no draft hold). U4 and U5 are written from these. Defaults taken without a question: the gate scans commit headers and the agent gets a git identity from the environment (H4); after a failed hand-over the workspace stays (H3).
- **U4 and U5 written again 11-19 from the code of that day and the founder decisions (mem-327); the blocks of 11-12 are gone. Order by probes:** U41 (seam in `internal/quality`) → U42 (gates as the agent user) → U43 (workflow hooks), U44 (evidence commands), U46 (GOPROXY check) → U45 (lint fix; after U43, same file) → U47 (removes the preflight refusal; **founder hold: a draft, dispatch only on the founder's word**; its probes need H12, H21, H16, H20 and U42 to U46). U51 (scoped token mint; no host precondition) → U52 (token file and gh shim) → U53 (git credential helper; after U42). The check of U5 on the box needs O2 (GitHub App).
- [x] **U41** (PR#6553 merged 11-19 18:32Z; run-it review done 11-19, see U48) ([#6549](https://github.com/qf-studio/pilot/issues/6549), dispatched 11-19 ~18:40Z) a command builder seam in `internal/quality`: build, configure, kill and file read through one interface; no change of behaviour.
- [x] **U48** (PR#6558 merged 11-19 19:34Z; run-it review done 11-19, see the notes line below) ([#6555](https://github.com/qf-studio/pilot/issues/6555), dispatched 11-19 ~19:00Z; before U42) from the run-it review of PR#6553 (comment on the PR, 11-19): the behaviour with the default builder is the same as before (12 cases on both trees), and no program starts outside the builder; but the reads of the `Makefile`, the marker file checks, the directory list, the `go.mod` check and the path resolution still read the task directory as the daemon (a FIFO in place of the `Makefile` blocks a gate past its time limit); the scope commands never get Configure; a nil command from Build makes the runner panic.
- **Notes from the run-it review of PR#6558 (comment on the PR, 11-19):** nothing in the quality package reads the task directory outside the builder any more, and the four acceptance lines hold. For the builder of the agent side (taken into U42): the scope reads and the command detection have no time limit in the package, so each read method of that builder needs a limit of its own; a Build that blocks also blocks. Minor, in the package, not written as a leg: each gate with a read that never returns leaves one goroutine and one thread, with no cap; the limit counts twice (a limit of 1 s gives 1.8 s); a panic in a path read shows as a time limit error; with a nil builder the scope says "not a Go module root"; three guards with no test, and the FIFO test hangs when it fails. Not run: Linux, sudo.
- [x] **U42** (PR#6580 merged 11-19 23:04Z; run-it review done 11-19, see U49) ([#6563](https://github.com/qf-studio/pilot/issues/6563), dispatched 11-19 ~20:20Z, after the merge and the review of U48) the quality gates and their scope commands run as the agent user with the allow list environment; the scope variables and `PILOT_TASK_ID` pass.
- [x] **U49** (PR#6591 merged 11-20 00:34Z; run-it review done 11-20, see the notes line below) ([#6584](https://github.com/qf-studio/pilot/issues/6584), dispatched 11-19 ~23:30Z; before U43, U44 and U46; must merge before the removal of the preflight refusal) from the run-it review of PR#6580 (comment on the PR, 11-19): with the option set each gate command, scope command and read of the quality package goes through sudo as the agent user, and no secret name or value reaches the gate environment (shown with the mock sudo on both production routes). Two major findings, both about proof: the test does not pin the allow list (with it removed the test passes and a gate sees 93 names of the daemon); no test fails when the registration of the builder is removed (the gates then run as the daemon user). Also: the scope names and `PILOT_TASK_ID` pass values that the daemon process holds to each agent command; the route error guard of three read methods has no test; Build does not fail closed for a command that is not a sudo command; the time limit of a read depends on the kill.
- **Notes from the run-it review of PR#6591 (comment on the PR, 11-20): both mutations that passed before now fail the suite (the allow list, the registration), and with the wrong builder all three gate starts (the gate run, the re-gate of the pre-PR fix, the timeout salvage) refuse and no gate runs as the daemon user.** One major, a test gap: with the guard removed at one of the three call sites the suite passes. Minor: the command detection reads the workspace as the daemon user before the guard, when the builder is wrong; the rule that tells an inherited value from a value set on purpose drops a caller value that equals the daemon value (matters for the hooks leg: it must pass `PILOT_TASK_ID` in a way that this rule keeps); `ANTHROPIC_BASE_URL` and the `CLAUDE_` names reach a gate; the six gh guard names pass from the daemon process to a gate. **For the first run on a real host:** a gate gets `PATH`, `TMPDIR`, `GOFLAGS`, `NODE_OPTIONS` and `GOTOOLCHAIN` with the values of the daemon; when `go`, `make` or the lint program is found through a `PATH` entry in the home of the daemon user, or `TMPDIR` is a directory of the daemon user, each gate fails though the code is right — check on the host before the run with a probe as the agent user; a read whose kill fails leaves one process for each call. The daemon filed [#6592](https://github.com/qf-studio/pilot/issues/6592) for minors of the same PR. **To write after #6592 merges (same files): a leg U56 with the call site tests, the detection read, the gh guard names, and a preflight probe for the tools and the temporary directory of the agent user.** Not run: Linux, real sudo.
- **Notes from the run-it review of PR#6580 that U49 does not cover:** ReadDir has a limit of 4 MiB and Stat gives made-up file modes; the scope reads have a limit for each read and no total bound; the daemon still does an Lstat and a path resolution of the workspace for the route check, the change of directory and the lookup of programs in its own PATH. Not run: Linux, real sudo, a second user.
- [x] **U43** (PR#6609 merged 11-20 04:41Z; run-it review 11-20, comment on the PR: with the option empty a hook that leaves a background child now fails after 5 s; with the option set `ANTHROPIC_CUSTOM_HEADERS` reaches the hook; no test fails with the full environment → U59) ([#6602](https://github.com/qf-studio/pilot/issues/6602), dispatched 11-20 ~02:40Z, after the merge of the daemon-filed ticket 6592 (PR#6601)) workflow hooks run as the agent user; a kill of the process group for each hook.
- [x] **U44** (PR#6605 merged 11-20 03:06Z; run-it review done 11-20, see U58) ([#6594](https://github.com/qf-studio/pilot/issues/6594), dispatched 11-20 ~01:00Z, after the merge and the review of U49) evidence commands run as the agent user; the evidence file writes and the env context read go through the agent side.
- [x] **U58** (PR#6611 merged 11-20 05:58Z; run-it review 11-20, comment on the PR: the stop exists only before the pre-PR review, at PR creation the error is dropped → U61; the check compares bytes only (link, mode), the git repair loses uncommitted work and runs filter programs → U62) ([#6606](https://github.com/qf-studio/pilot/issues/6606), dispatched 11-20 ~03:35Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6605 (comment on the PR, 11-20): with the option set no evidence command runs as the daemon user and no value that only the daemon has reaches it (production path, mock sudo); a failed, timed-out or cancelled command still restores the file. Two major findings: a failed restore leaves the file changed, the run goes on to the review and the push, and no log line says so; a changed write that fails after the file was cut is not restored. Both are also true with the option empty. Also: a cancelled command is reported as a caught mutation; two test gaps; a FIFO at the env context path blocks 30 seconds.
- **Notes from the run-it review of PR#6605 that U58 does not cover:** an evidence command gets `PATH`, `TMPDIR`, `LANG` and `GOFLAGS` with the values of the daemon (the preflight probe of U56 covers the tools and the temporary directory); `NODE_ENV` and `CI` are dropped for it; with a task directory outside the workspace root the mutation read and write are plain calls as the daemon user; the path resolution and the marker file checks stay with the daemon (metadata only). Not run: real sudo, a second user, Linux.
- [x] **U45** (PR#6625 merged 11-20 11:01Z — the first PR#6620 failed CI, the daemon filed the CI fix 6624 on the same branch; the CI fix changed test setup only; run-it review 11-20, comment on the PR: no blocking finding; names with the prefix `ANTHROPIC_` and `CLAUDE_` reach the lint program and the git hooks of the amend → U65; the restore of the hook settings runs after the push, no test runs that path through the runner, the lint does not refuse a command that is not a sudo command → U66) ([#6613](https://github.com/qf-studio/pilot/issues/6613), dispatched 11-20 ~09:28Z; the leg text holds the test lessons of the PR#6609 review; after U43) the lint run and the lint fix run as the agent user; a failed restore of the hook settings fails the run.
- [x] **U65** (PR#6650 merged 11-20 17:19Z; run-it review 11-20, comment on the PR: no blocking finding, no `ANTHROPIC_` or `CLAUDE_` name reaches a gate, a hook, the lint, an evidence command or a git hook, each of the nine mutations is killed; the file helpers, the two preflight probes, the bundle commands and the `tar` copy (fixed scripts, no program of the workspace) still get the wide environment, and the config reference does not say that a gate gets no model key → two items in U66) ([#6635](https://github.com/qf-studio/pilot/issues/6635), dispatched 11-20 ~13:31Z; after U59; must merge before the removal of the preflight refusal) from the run-it reviews of PR#6609, PR#6611 and PR#6625: one tool environment for each agent-side command that is not the model session — no name with the prefix `ANTHROPIC_` or `CLAUDE_` reaches a gate, a hook, the lint, an evidence command or a git hook of the workspace.
- [x] **U66** (PR#6667 merged 11-21 10:00Z; run-it review 11-21, comment on the PR: no blocking finding; the restore runs before the push, the helper commands get the tool environment, 127 with output is a lint failure; the first of the two lint fix places still runs before the restore, so its amend can put the hook settings file of the daemon into the pushed commit (older than the PR, option empty too); a refused lint command does not reach the result → U70) ([#6661](https://github.com/qf-studio/pilot/issues/6661), dispatched 11-21 ~09:20Z, with two points of the PR#6650 review and a note on PR#6645 and PR#6660 in its text) from the run-it review of PR#6625: the lint refuses a command that is not a sudo command; a failed restore of the hook settings fails the run before the push; a test runs that path through the runner; exit status 127 with output is a lint failure.
- [x] **U70** (PR#6674 merged 11-21 13:43Z; run-it review 11-21, comment on the PR: the settings file is out of the pushed commit in the case of the PR#6667 review; **a guard regression:** with the pre-push lint on, the new early restore runs before the contract evidence session, which then starts without the hook entries of the daemon (option empty too); the pathspec exclude makes `git add` fail where `.claude/` is ignored or a symlink → U71. Open note: a tracked settings file that the agent itself commits holds the hook entries in that commit (older)) ([#6668](https://github.com/qf-studio/pilot/issues/6668), dispatched 11-21 ~10:21Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6667: the restore runs before the first lint fix too; the lint amend never stages the settings file of the daemon; a refused lint command reaches the result; the run-level test of the open PR is back.
- [x] **U71** (PR#6678 merged 11-21 14:40Z; run-it review 11-21, comment on the PR: **the regression of PR#6674 is closed** — each model session before the push has the hook entries, the restore still runs before the push, the lint fix is committed where `.claude/` is ignored, a symlink or a submodule. Open note, out of scope: a tracked settings file that the agent itself commits reaches the pushed commit with the hook entries of the daemon) ([#6676](https://github.com/qf-studio/pilot/issues/6676), dispatched 11-21 ~13:58Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6674: no restore of the hook settings before the first lint fix, so the contract evidence session keeps the guard hooks; the lint amend keeps the settings file out of the index with a reset, not with a pathspec exclude.
- [x] **U46** (PR#6610 merged 11-20 05:14Z; diff read by the session 11-20, comment on the PR: the check passes each element that `url.Parse` rejects → U60) ([#6603](https://github.com/qf-studio/pilot/issues/6603), dispatched 11-20 ~02:40Z, after the merge of the daemon-filed ticket 6592 (PR#6601)) the start is refused when GOPROXY of the daemon holds credentials.
- [x] **U56** (PR#6628 merged 11-20 11:48Z; run-it review 11-20, comment on the PR: no blocking finding, each of the nine mutations is killed; on the timeout salvage path the refusal of the guard still pushes the branch through the hold, the probe file name can be known before, no test pins the probe time limit → items 5 to 7 of U60; a gate gets the `CLAUDE_` and `ANTHROPIC_` names of the daemon → U65) ([#6614](https://github.com/qf-studio/pilot/issues/6614), dispatched 11-20 ~09:28Z; after U43 and U46, same files; must merge before the removal of the preflight refusal) from the run-it review of PR#6591: tests on the three production gate start functions (the one major finding: the guard at each call site had no test); no command detection as the daemon user with the wrong builder; the gh guard names do not pass to a gate; a preflight probe as the agent user for the tools and the temporary directory (the point for the first run on a real host); an Error line when a read is given up. The daemon-filed PR#6601 already closed the rule for values set on purpose.
- [x] **U59** (PR#6634 merged 11-20 13:21Z; run-it review 11-20, comment on the PR: no blocking finding, a hook that leaves a background child passes again; the working directory of the hook with the option empty is still not under test → a test line in U65; the lint does not call the shared check agentSudoCommandError → U66; open notes: the Warn line has no task id and no test, a cancel in the wait delay returns late) ([#6616](https://github.com/qf-studio/pilot/issues/6616), dispatched 11-20 ~09:34Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6609: a hook that leaves a background child does not fail the run (a change of behaviour with the option empty); with the option set the hook gets no model credential and no request header; the hook builder refuses a command that is not a sudo command; tests for the environment and the working directory.
- [x] **U60** (PR#6649 merged 11-20 16:47Z; run-it review 11-21 (second run, small scope), comment on the PR: no blocking finding, the check gives the wanted answer for 13 values. Open notes, not in a leg: the test of the probe file name is a text check only; three values with no credentials are refused with the text "holds credentials"; `mktemp` has no `--`) ([#6629](https://github.com/qf-studio/pilot/issues/6629), dispatched 11-20 ~12:02Z, with three probe points and the salvage push from the review of PR#6628 in its text; after U56, same file; must merge before the removal of the preflight refusal) from the review of PR#6610: the GOPROXY credentials check fails closed — an element that does not parse or that holds an `@` refuses the start.
- [x] **U61** (PR#6637 merged 11-20 14:05Z; run-it review 11-20, comment on the PR: no blocking finding, each of the six call sites handles the error; five of the six have no test that fails when the handling is made dead; on the epic path the file stays changed in a shared checkout and no alert is sent; the texts do not fit each path → U67) ([#6617](https://github.com/qf-studio/pilot/issues/6617), dispatched 11-20 ~09:34Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6611: on the PR creation path a file that the mutation check left changed fails the run before the PR; the log line says what happened.
- [x] **U62** (PR#6651 merged 11-20 18:03Z; run-it review 11-20, comment on the PR: the five scope items hold for the last path component; a link in a PARENT directory still takes the restore outside the workspace (with the option empty a write of the daemon), a link put at the path between the check and the write is followed, the git repair reads the file name as a pattern, the setuid bit is lost → U69) ([#6638](https://github.com/qf-studio/pilot/issues/6638), dispatched 11-20 ~14:23Z; after U61, same files; must merge before the removal of the preflight refusal) from the run-it review of PR#6611: the restore check reads the file type and mode, the restore never follows a link, the git repair keeps uncommitted work and gets the filtered environment, a panic still restores.
- [x] **U69** (PR#6659 merged 11-20 20:54Z; run-it review 11-21, comment on the PR: no blocking finding, the four findings of the PR#6651 review are closed for their cases, a workspace behind a symlink gets no false stop; the path resolution and the write are still two steps (a parent swapped at the write makes the daemon create a file outside, with the option empty), the check without its second resolution passes the tests → items 5 and 6 of U67) ([#6652](https://github.com/qf-studio/pilot/issues/6652), dispatched 11-20 ~18:16Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6651: the restore resolves the path again and never leaves the workspace; the restore write follows no link; literal pathspecs for the git repair; the setuid, setgid and sticky bits are kept.
- [x] **U67** (PR#6671 merged 11-21 11:44Z; run-it review 11-21, comment on the PR: no blocking and no should-fix finding; each of the six call sites has a test, the epic path puts the file back and sends the alert, a parent directory swapped for a link at the write makes nothing outside the workspace. Open notes, not in a leg: the workspace root is opened by path at each write, not once for a run; on the epic path a file with a staged change is not put back) ([#6664](https://github.com/qf-studio/pilot/issues/6664), dispatched 11-21 ~09:39Z, with two points of the PR#6659 review in its text; after U69) from the run-it review of PR#6637: the file is put back on the epic path too, with the alert; a test for each of the six call sites; texts that fit each path; the result names the pushed branch.
- [ ] **U47** (FOUNDER HOLD; below; after each other leg of the must-merge list) the preflight no longer refuses the option; the pull request stays a draft until the founder releases it.
- [x] **U51** (PR#6571 merged 11-19 21:54Z; run-it review done 11-19, see U54) ([#6550](https://github.com/qf-studio/pilot/issues/6550), dispatched 11-19 ~18:40Z) an installation token with read permissions for one repository; a provider for it in the executor.
- [x] **U54** (PR#6578 merged 11-19 22:43Z; run-it review done 11-19, see U55) ([#6575](https://github.com/qf-studio/pilot/issues/6575), dispatched 11-19 ~22:15Z; before U52) from the run-it review of PR#6571 (comment on the PR, 11-19), two major findings that must be closed before the token goes into a file for the agent: the repository of the token is not bound to the config of the daemon (a name of another owner, and each other repository name of the installation, gives a token; a caller that reads the name from the workspace would let the agent choose); no test proves that the provider returns the scoped token and never the full one. Also: permission keys outside the four pass; the answer of GitHub is not checked (a wider token is returned); expiry and empty token; redirects; the feature matrix line. Held: no body with all permissions or all repositories was sent, and no run returned a token of the daemon.
- [x] **U55** (PR#6583 merged 11-19 23:30Z; run-it review done 11-19, see the notes line below) ([#6581](https://github.com/qf-studio/pilot/issues/6581), dispatched 11-19 ~23:15Z; before U52) from the run-it review of PR#6578 (comment on the PR, 11-19), one major finding that must be closed before the token goes into a file for the agent: the owner is compared with config text only, and the answer of GitHub is not checked for the full repository name: a config entry of owner B with an installation of owner A that has the same repository name gives a read token for the repository of owner A. Also: a strict form for the token text (it goes into a file and an environment value), no extra permission keys, an upper limit for the expiry, a safe error text, name rules, no redirects for the full token mint. Held: the ten findings of the PR#6571 review are closed, and no path returns the full token.
- **Notes from the run-it review of PR#6583 (comment on the PR, 11-19): verdict — it is safe to write this token into a file that the agent reads, with conditions for U52 (taken into the U52 leg: a Go file call only, only the path in the environment, an exclusive create with no link follow, the repository from the config, no other credential on an error, a cut error text, a margin on the expiry, a test for the start-up line).** No blocker and no major. Minor, not written as a leg: the compare of the full name folds letters outside ASCII (a look-alike owner in the answer is accepted; the answer comes from GitHub); a JSON parse error carries the expiry text with no cut; three test gaps; a bad config entry fails at each call with a message that names no config field; the margin for a wrong local clock is 30 minutes. Not run: the real GitHub API.
- [x] **U52** (PR#6593 merged 11-20 01:06Z; run-it review done 11-20, see U57 and the founder question below) ([#6587](https://github.com/qf-studio/pilot/issues/6587), dispatched 11-19 ~23:50Z, after the merge and the review of U55) a read-only token file for each run, with a refresh; the gh shim of the agent reads it.
- [x] **U57** (PR#6619 merged 11-20 10:04Z after the retry; run-it review 11-20, comment on the PR: the repository lookup is on each runner build site, but the token provider is installed in the polling mode only, so gateway mode and `pilot task` still get no token (fails closed, no log line) → U63; the start sweep removes the directory of a live run of another daemon process → U64) (parked 11-20 03:30Z by the pre-PR review of Pilot: in gateway mode no run gets a read token; **re-armed 11-20 ~08:55Z** with three more scope items in the issue body (the repository lookup on each runner build site of `cmd/pilot`, the sweep in the chat-only gateway start, a test through the run-level wiring) and acceptance commands with no shell operator; the retry runs; after the PR compare its commit with the hold commit `cee1add61`) ([#6596](https://github.com/qf-studio/pilot/issues/6596), dispatched 11-20 ~01:30Z; before U53) from the run-it review of PR#6593 (comment on the PR, 11-20): no token value was found in an environment, an argument list, a log line or an error value. Minor findings in the leg: a kill of the daemon leaves a directory with a valid token and nothing removes it at start; one token for each backend process, not one for a run; on the webhook and retry paths the repository is matched by name only (two owners with the same repository name can cross); three test gaps; no wait between refresh retries; the shim reads the file with no size limit.
- [x] **U63** (PR#6640 merged 11-20 14:47Z; run-it review 11-20, comment on the PR: no blocking finding; the six start paths that run a task install the provider and the lookup, the provider asks for read permissions only, a path inside a project or a prefix does not match; the source read test counts calls for a file and not for a Runner, the interactive mode runs a task with no provider, three Warn lines have no task id → items 4 to 6 of U64) ([#6621](https://github.com/qf-studio/pilot/issues/6621), dispatched 11-20 ~10:23Z; must merge before the removal of the preflight refusal) from the run-it review of PR#6619: the token provider is installed on each start path; a run with no token says why in one Warn line; the project path is matched in its canonical form; a source read test over each Runner build site and a test of the orchestrator bridge.
- [x] **U64** (PR#6656 merged 11-21 09:16Z — the first PR#6654 failed CI, CI fix 6655 on the same branch; run-it review 11-21, comment on the PR: no blocking finding, the lock is sound against the agent user (lock file 0600 of the daemon user, directory 0750), each asked mutation is killed; the refresh trusts what is at the path of a lost directory (a link there takes the token write, a file there makes the refresh ask the provider with no end), the sweep can remove a directory between its creation and its lock, a FIFO at the lock path stops the start → items 5 to 7 of U68) ([#6641](https://github.com/qf-studio/pilot/issues/6641), dispatched 11-20 ~14:59Z, with three points of the PR#6640 review in its text; after U63) from the run-it review of PR#6619: the start sweep keeps the token directory of a live run (a lock file); the refresh makes a lost directory again or stops; tests that pin the owner check, the 1024 byte limit of the shim and the create with no link follow.
- **Founder question from the same review (open, 11-20): two runs at the same time share the agent user, so the agent of run A can read the token file of run B, also of another project** (the default `max_concurrent` is 2; shown by modes, group and directory; the read as the agent user was not run). The design accepts today that one run can read the workspace of another run (`.agent/system/agent-isolation-design.md`, "Concurrent runs"); the token adds read access to the issues and pull requests of that project. Options: accept and say it in the design and in a start warning (the projects of one daemon belong to one owner); refuse the option with more than one run at a time; a user for each project (the full fix, a later task). Not decided.
- [x] **U53** (PR#6644 merged 11-20 15:30Z; run-it review 11-20 in two passes (the laptop disk was full in the first), comment on the PR: no blocking finding; real git gives the token to the scoped host only, no daemon-side git gets the helper, 14 of 17 mutations are killed; minor: the helper path is not quoted for the shell, no `GIT_TERMINAL_PROMPT=0`, three test gaps → U68) ([#6622](https://github.com/qf-studio/pilot/issues/6622), dispatched 11-20 ~10:23Z; after U57) git of the agent side and the gates read the token file through a credential helper; no token in the environment.
- [x] **U68** (PR#6675 merged 11-21 14:23Z after a CI fix round (seven test lines for a git identity); run-it review 11-21, comment on the PR: no blocking and no should-fix finding; the quoting holds with real git for thirteen hostile directory names, the refresh stops at a link or a file, the sweep does not block on a FIFO and loses no token in the stress run. Open notes, not in a leg: a directory younger than 60 seconds at a quick restart stays until the next start; a relative `TMPDIR` gives a relative helper path; two test pins) ([#6665](https://github.com/qf-studio/pilot/issues/6665), dispatched 11-21 ~09:39Z, with three points of the PR#6656 review in its text; after U64) from the run-it review of PR#6644: the helper path is quoted for the shell; git of the agent side never prompts; the config names of the daemon never pass; tests for the retry context and the quoting.
- **Open points of the U4 and U5 research (11-19), for the founder with the release of U47:** the removal of the refusal does not need U5 (the option stays off in each config until a host sets it), but a host that turns the option on before U5 and O2 gives the agent no `gh` and no fetch of a private repository; the gates as the agent user need `make`, the Go tools and the lint program on the PATH of the agent user (not checked on the box), and the first gate run has a cold build cache; `internal/executor/gate.go` has no caller (left as it is).
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

### LEG H16 — repo: qf-studio/pilot — title: fix(executor): run_as_user workspace sweep — every ref of the workspace is read, a workspace with a live record is skipped, an unknown age uses the directory time, one removal is counted once

probe: present qf-studio/pilot func[[:space:]]agentWorkspaceTipSafe\(
probe: present qf-studio/pilot func[[:space:]]sweepAgentWorkspaces\(
probe: present qf-studio/pilot func[[:space:]]PruneOrphanedWorktreeForTask\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentWorkspaceSweepReadsEveryRef\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent works in a workspace of its own. The daemon removes a workspace after the run, and sweepAgentWorkspaces (`internal/executor/agent_workspace.go`) removes left workspaces at the daemon start and in PruneOrphanedWorktreeForTask. agentWorkspaceTipSafe decides if the commits of a workspace are safe in the daemon's repository. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `agent_workspace.go`, `worktree.go` and their test files. With the option empty nothing changes.

- agentWorkspaceTipSafe reads `rev-parse HEAD --branches`: only HEAD and `refs/heads/`. A commit that is only in `refs/stash`, a tag or another ref of the workspace is removed with the workspace and no Error line. `git stash` is the case an honest agent can hit.
- The sweep does not skip a workspace with a live record (a run that is alive). A prune for such a task removes the workspace when its tip is safe at that moment; a commit made between the check and the removal is lost.
- With no record there is no base. A workspace with no commit of its own whose base is only in a local branch of the daemon's repository is kept seven days with an Error line at each start, and is then logged as lost.
- An unsafe workspace whose age cannot be read is kept at each sweep for ever, with `age=0s` in the line.
- Two sweeps at once count one removal twice and log it twice.
- No test covers: the unknown age, the `.git` check for a link and for a file, the hex check of the tips, the sweep with no record.

## Scope

1. agentWorkspaceTipSafe reads `rev-parse HEAD --all` in place of `HEAD --branches`.
2. The sweep skips a workspace path that has a record of a live run (the map that createAgentWorkspace fills), with a Debug line; PruneOrphanedWorktreeForTask gets the same skip through the sweep.
3. In the sweep path, with no record, the walk of agentWorkspaceTipSafe also excludes `--glob=refs/heads/` of the daemon's repository. The path with a record keeps the walk of today.
4. An age that cannot be read from the name falls back to the modification time of the workspace directory (read with Lstat). The log line has no `age` field when neither is known.
5. The sweep checks with Lstat that the path is still there right before the removal and counts only a removal it made.
6. Tests: a function TestAgentWorkspaceSweepReadsEveryRef — a workspace with a commit only in `refs/stash` (made by `git stash`), one with a commit only in a tag and one with a commit only in `refs/agent/x` are each kept after a good push, with the Error line; a workspace with a live record is not removed by a prune of its task; a workspace with no record and no commit of its own on a base that only a local branch of the daemon holds is removed as safe; a name with no readable time uses the directory time (older than seven days: removed with the "lost" line; newer: kept); two sweeps at once on one safe workspace return a sum of 1; `.git` as a link and `.git` as a file are each not safe; a tip text that is not hex is not safe.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentWorkspace'` passes; paste the output into the PR body.
- [ ] Put `--branches` back -> TestAgentWorkspaceSweepReadsEveryRef fails.
- [ ] Remove the skip of a live record -> the prune test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The hand-over ref and `handover.go` (other leg). A lock between the tip check and the removal for a workspace with no record. The seven day limit. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6506

### LEG H20 — repo: qf-studio/pilot — title: fix(executor): run_as_user hand-over — the old hand-over ref is kept as a recovery ref before its removal, the loser of two hand-overs changes nothing of the winner, each promisor value is refused, exact locale names

probe: present qf-studio/pilot func[[:space:]]checkHandOverPartialClone\(
probe: present qf-studio/pilot func[[:space:]]removeHandOverRef\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverDaemonGitEnvNoToken\(
probe: absent  qf-studio/pilot func[[:space:]]TestHandOverBranchKeepsTheOldRefAsRecovery\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, handOverBranch (`internal/executor/handover.go`) takes the branch of the agent workspace into the daemon's repository and sets `refs/pilot/handover/TASK`. At its start it removes the ref of an earlier hand-over of the task. The workspace sweep counts a commit in `refs/pilot/handover/` as safe and removes the workspace. Run-it reviews found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `handover.go` and `handover_test.go`. With the option empty nothing changes.

- After a failed push the workspace is kept, and the next sweep removes it because the hand-over ref holds its commit. A retry of the task then removes that ref at the start of its hand-over. The commit is in no ref and git can prune it. Reproduced.
- Two hand-overs of one task: a loser whose `update-ref` fails on the lock file of the winner ("Unable to create ... .lock") takes the general error path and deletes the ref of the winner. Seen with a forced order only.
- The path "another hand-over of the task is in progress" leaves the pack files of the loser in `objects/pack` with no ref.
- checkHandOverPartialClone reads the value of `remote.NAME.promisor` only as `true`, `yes`, `on`, `1` or empty. git reads each integer other than 0 as true: `promisor=2` is not refused. It also runs `git config --get-regexp` with no `--local`: for a repository that git does not accept (another owner) the exit status 1 reads as "no key".
- handOverEnvAllowed passes each name that starts with `LC_`; such names are free-form, and a value under `LC_SECRET` reaches each git process of a hand-over.
- No test pins `LC_ALL=C` on the last update-ref; TestHandOverBranchConcurrentSameTask accepts two SHAs in one round.

## Scope

1. Before handOverBranch removes an old `refs/pilot/handover/TASK`, it reads the commit of that ref. When no ref below `refs/remotes/` and no ref below `refs/pilot-recovery/` contains the commit, it sets `refs/pilot-recovery/TASK-SHORT` (SHORT = the first 12 characters of the commit) to it, with one Info line that names the ref. A failure to set that ref stops the hand-over with an error; the old ref stays.
2. A failed last `update-ref` never deletes the ref of the task when its error holds "reference already exists" or "Unable to create" with ".lock": both give the error of "another hand-over of the task is in progress".
3. The in-progress path removes the files that this call moved into `objects/pack` (removeHandOverFiles with its own list) before it returns.
4. checkHandOverPartialClone uses `git config --local --get-regexp`; an exit status other than 0 and 1 is an error of the hand-over; each promisor value other than `false`, `no`, `off` or `0` (any letter case) is a partial clone.
5. handOverEnvAllowed passes no name by prefix: the names are `PATH`, `HOME`, `TMPDIR`, `USER`, `LOGNAME`. handOverDaemonEnv appends `LC_ALL=C` with the other values that the code sets, and the single `LC_ALL=C` line of the update-ref goes away (no name twice in one environment).
6. Tests: a function TestHandOverBranchKeepsTheOldRefAsRecovery — an old hand-over ref with a commit that no remote ref holds, then a new hand-over of the task: the commit is in a ref below `refs/pilot-recovery/` and `git for-each-ref --contains` finds it; an old ref whose commit is in `refs/remotes/origin/` makes no recovery ref; a lock file in place for the ref of the task gives the in-progress error and a ref that the test sets as the winner is still there; the in-progress path leaves `objects/pack` as it was; promisor values `2`, `-1`, `TRUE` are refused and `false`, `0`, `off` are not; with `LC_SECRET` and `LANG` set in the test process no git process of a hand-over has `LC_SECRET`, and each one has `LC_ALL=C` exactly once; TestHandOverBranchConcurrentSameTask requires exactly one SHA in each round.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOver'` passes; paste the output into the PR body.
- [ ] Remove the recovery ref step -> TestHandOverBranchKeepsTheOldRefAsRecovery fails.
- [ ] Put the prefix rule for `LC_` back -> the `LC_SECRET` test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The workspace sweep in `agent_workspace.go` (other leg). The size limits and objects that the tip does not reach. A lock for each task. The age rule of the quarantine sweep. A removal rule for old recovery refs. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6506, PR#6518 and PR#6527

### LEG H23 — repo: qf-studio/pilot — title: fix(executor): run_as_user gh commands, second round — the label and comment helpers and the sub-issue PR state leave the workspace; one command checks and reads the graph file; a bound for all graph reads of a run

probe: present qf-studio/pilot func[[:space:]]ghDirForExecutionPath\(
probe: present qf-studio/pilot func[[:space:]]ghIssueComment\(
probe: present qf-studio/pilot func[[:space:]]ghAddLabels\(
probe: present qf-studio/pilot func[[:space:]]TestGhRepoArgKeepsHost\(
probe: absent  qf-studio/pilot func[[:space:]]TestGhHelpersLeaveTheWorkspace\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, a `gh` command of the daemon (it carries the token of the daemon) must not run in the agent workspace: `gh` reads the repository from the git config of its directory, and that config is the agent's. ghDirForExecutionPath (`internal/executor/git.go`) gives the directory and the `--repo` arguments for an execution path: for an agent workspace on record, the daemon's repository and the repository of its origin. Six gh calls of `internal/executor/epic.go` use it. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- handleSubIssueCoverageGap (`internal/executor/epic.go`) passes the execution path to ghAddLabels and ghIssueComment (`internal/executor/title_rejection.go`). Their commands (`gh issue comment`, `gh label create`, `gh issue edit`) run in the workspace with no `--repo`. Reproduced with a fake `gh`.
- getSubIssuePRState (`internal/executor/epic.go`) falls back to the execution path as its directory; the callers of today give it another path.
- readMemoryGraphFile (`internal/executor/git.go`) checks "regular file" and reads in two steps of one shell command; a swap of the file between them makes the read block for the full 10 seconds, and nothing bounds the sum over all graph nodes of a run.
- A cancelled context of the caller is reported as a missing graph file.

## Scope

1. ghIssueComment and ghAddLabels resolve their directory argument through ghDirForExecutionPath themselves: each gh command they start gets the directory and the repository arguments it returns; an error of the resolve is the error of the helper and no gh command runs. Each caller keeps its argument.
2. getSubIssuePRState resolves its directory the same way.
3. A search for each other gh command of the package that takes a directory from an execution path (`git grep -n 'withGhCredentials' internal/executor`): each one that can get a workspace path uses ghDirForExecutionPath; list in the PR body each call site and the result (changed, or safe and why).
4. readMemoryGraphFile, with the option set: the check and the read use one open of the path, so that a swap of the file between them has no effect; the open has a time limit of 2 seconds (a FIFO does not block the read for longer), what is not a regular file is refused, and at most 8 MB are read. Use only shell tools that the agent file helpers of the package already use; no script for an interpreter of the host. A file over the size limit is a missing file with the Warn line.
5. The graph reads of one delivery share a budget of 30 seconds: when it is used up, each later read of that delivery is a missing file with one Warn line for the delivery.
6. A cancelled context of the caller gives the context error to the caller, not a missing file.
7. Tests with the mock sudo of the package and a fake `gh` first in `PATH` that writes its directory and arguments: a function TestGhHelpersLeaveTheWorkspace — with an agent workspace on record, ghIssueComment, ghAddLabels (with a label that must be created) and getSubIssuePRState run each gh command in the daemon's repository with the `--repo` argument; a workspace path with no record gives an error and no gh call; with the option empty the directory is the argument and no `--repo` is added. Graph read: a file of 9 MB is a missing file; five reads that each block use at most the budget in total; a cancelled context gives the context error.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'GhHelpers|GhRepoArg|MemoryGraph'` passes; paste the output into the PR body.
- [ ] Let ghAddLabels use its directory argument directly -> TestGhHelpersLeaveTheWorkspace fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The host rules of ghRepoArg (`ssh.github.com`, `www.github.com`). A check of the owner and the name text. The gh commands of the agent side (the gh guard). The token commands of git. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6565

### LEG H25 — repo: qf-studio/pilot — title: fix(executor): run_as_user — the budget of the graph reads counts only reads that failed, the issue state call takes its repository from the daemon side, a workspace with no daemon repository is refused for PR commands

probe: present qf-studio/pilot func[[:space:]]readMemoryGraphFile\(
probe: present qf-studio/pilot func[[:space:]]TestGhHelpersLeaveTheWorkspace\(
probe: present qf-studio/pilot func[[:space:]]ghDirForExecutionPath\(
probe: absent  qf-studio/pilot func[[:space:]]TestMemoryGraphBudgetCountsOnlyFailedReads\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the daemon reads the files of the knowledge graph of the workspace as the agent user (readMemoryGraphFile in `internal/executor/git.go`), one sudo call for each file, and the reads of one delivery share a budget of 30 seconds (memoryGraphReadBudget). A `gh` command of the daemon takes its directory and its repository argument from ghDirForExecutionPath, so that the config of the workspace cannot choose the repository. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `git.go`, `epic.go` and their test files. With the option empty nothing changes.

- The budget also counts the time of reads that succeed. The graph of this repository needs 879 reads, about 16 seconds through sudo on a fast host. On a host where one sudo call is 50 ms slower the budget is used up by good reads; each later read is then a missing file and the guards that use the graph are skipped.
- fetchIssueState (`internal/executor/epic.go`), called from handleSubIssueCoverageGap, takes the owner and the name for `gh issue view --repo` from the remote of the workspace: the agent chooses the repository of that read.
- A GitOperations value that is made for a workspace path with no daemon repository runs its PR commands in the workspace with no `--repo`. No caller does this today.

## Scope

1. The budget of the graph reads counts only the time of a read that ended with an error, a refusal or a time limit. A read that returned content costs nothing. The Warn line for a used-up budget stays, one for a delivery.
2. The reads of one delivery are also bounded as a whole by the context of the delivery; no new fixed limit for good reads.
3. fetchIssueState takes its directory and its repository arguments from ghDirForExecutionPath; the remote of the workspace is not read for it. An error of the resolve is the error of the call, and no gh command runs.
4. With the option set, a gh command of GitOperations for a path that is an agent workspace with no daemon repository on the value returns an error and runs no gh command.
5. Tests with the mock sudo of the package and a fake `gh` first in `PATH`: a function TestMemoryGraphBudgetCountsOnlyFailedReads — 1000 good reads with a mock sudo that adds 40 ms to each call all return content and write no budget Warn line; five reads that each end by the time limit use up the budget, and the sixth is a missing file with the one Warn line; a cancelled context of the delivery ends the reads with the context error. fetchIssueState with an agent workspace whose origin names another owner: the gh call runs in the daemon's repository with the repository of the daemon side. A GitOperations value for a workspace path with no daemon repository: its PR list call gives an error and no gh call is logged.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'MemoryGraph|GhHelpers|GhRepoArg'` passes; paste the output into the PR body.
- [ ] Count good reads in the budget again -> TestMemoryGraphBudgetCountsOnlyFailedReads fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

One sudo call for all graph files (a later change). The size limit and the open time limit of one read. The host rules of ghRepoArg. The token commands of git. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6586

### LEG H21 — repo: qf-studio/pilot — title: fix(executor): the git commands that carry the token run no program of the repository config — credential helper, ssh command, proxy command and ext remotes off; no terminal prompt; no token for the local count and the co-author read

probe: present qf-studio/pilot func[[:space:]]withDaemonGitEnv\(
probe: present qf-studio/pilot func[[:space:]]TestDaemonGitEnvDropsInheritedGitNames\(
probe: present qf-studio/pilot func[[:space:]]TestMemoryGraphBudgetCountsOnlyFailedReads\(
probe: absent  qf-studio/pilot func[[:space:]]TestTokenCommandsRunNoConfigProgram\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The push, the fetch of the commit count and the two `ls-remote` probes of GitOperations (`internal/executor/git.go`) carry the token of the daemon in their environment (withGitCredentials sets an askpass helper). The hooks-off arguments already stop hooks and `core.fsmonitor`. A run-it review found more keys of the repository config that start a program under these commands when the remote is a network remote. Keep the change small: only the items below, only in `git.go`, `git_credentials.go` and their test files.

- With an https remote git asks each `credential.helper` before the askpass helper: `credential.helper = !script` in the repository config runs the script with the token in its environment. `core.sshCommand`, `core.gitProxy` and a remote URL of the form `ext::` do the same for their transports. Seen with the option run_as_user set (the config is the daemon's own) and with it empty.
- When the provider gives no token, withGitCredentials sets nothing, and the filter of withDaemonGitEnv then removes an inherited `GIT_TERMINAL_PROMPT=0`. git can then ask on the terminal of the daemon.
- The co-author read and the local `rev-list --count` of an agent workspace are local commands and still get each token of the daemon. The count runs with no `GIT_NO_REPLACE_OBJECTS`.
- No test pins the split of the environment into the inherited part and the set part in withDaemonGitEnv.

## Scope

1. Each git command for which withGitCredentials set the askpass helper also gets the arguments `-c credential.helper=`, `-c core.gitProxy=` and `-c protocol.ext.allow=never`, and the value `GIT_SSH_COMMAND=ssh` in its environment (a value that the code sets on purpose: add the name to the set that withDaemonGitEnv keeps). This holds with the option run_as_user set and with it empty.
2. withDaemonGitEnv appends `GIT_TERMINAL_PROMPT=0` when the set part has no value for that name.
3. The co-author read and the local count of an agent workspace get the environment of the secret gate commands (no token) plus `GIT_NO_REPLACE_OBJECTS=1`.
4. The doc comment of daemonGitEnv says that `HOME` and `XDG_CONFIG_HOME` pass and the global config of the daemon user is read (a `pushInsteadOf` there redirects the push).
5. Tests: a function TestTokenCommandsRunNoConfigProgram — a local HTTP test server that answers 401 with `WWW-Authenticate: Basic` as the remote, `credential.helper = !SCRIPT` in the repository config with a marker file: after the two probes, the fetch of the count and a push attempt the marker file is absent, with the option set and with it empty; the arguments of each of these commands hold `protocol.ext.allow=never` and `core.gitProxy=`, and the environment holds `GIT_SSH_COMMAND=ssh` (a wrapper `git` that writes both); with a provider that gives no token the remote commands of an agent workspace have `GIT_TERMINAL_PROMPT=0`; with `GIT_ASKPASS=/inherited` and `GIT_TERMINAL_PROMPT=1` in the test process the count and the co-author read have no `GIT_ASKPASS` and no fake `GITHUB_TOKEN`, and the push has the helper path of the code.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'TokenCommands|DaemonGitEnv|HandOverPush'` passes; paste the output into the PR body.
- [ ] Remove `-c credential.helper=` -> TestTokenCommandsRunNoConfigProgram fails.
- [ ] Make withDaemonGitEnv treat the whole environment as the set part -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A changed `remote.origin.url` with the option empty (the session runs as the daemon user there). The global config of the daemon user. The gh commands. The commands of the hand-over and of the secret gate.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6533

### LEG H17 — repo: qf-studio/pilot — title: fix(executor): run_as_user push, second round — the delivery git drops each inherited GIT_ name, fsmonitor off for the remote reads, the secret gate git gets no token

probe: present qf-studio/pilot func[[:space:]]daemonGitEnvDenied\(
probe: present qf-studio/pilot func[[:space:]]HooksOffArgs\(
probe: absent  qf-studio/pilot func[[:space:]]TestDaemonGitEnvDropsInheritedGitNames\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, Push and PushForceWithLease of GitOperations (`internal/executor/git.go`) take the branch of the agent workspace over into the daemon's repository, run the secret gate on the hand-over SHA and push that SHA with the credentials of the daemon. A run-it review of the last change found the points below. Item 2 is also true with the option empty. For each other item: the option is off, and the preflight refuses it on every host today. Keep the change small: only the items below.

- daemonGitEnvDenied is a list of denied names. `GIT_COMMON_DIR` is not in it: with that variable in the daemon process the push goes to the remote of another repository and Push returns nil. `GIT_GRAFT_FILE` and `GIT_SHALLOW_FILE` are not in it: the gate then reads another history than the push sends, and passes.
- secretpatterns.HooksOffArgs sets `core.hooksPath`. That does not turn off `core.fsmonitor`. The fetch of CountNewCommitsAgainstOrigin reads the index, so the program that `core.fsmonitor` of the repository config names runs with the token in its environment. Seen with the option set and with it empty.
- The git commands of the secret gate are local and still get each token of the daemon in their environment.
- No test fails when withDaemonGitEnv is removed from the co-author read or the gate environment from gateRepoKey; no test covers the hooks-off arguments with the option empty.

## Scope

1. daemonGitEnv becomes an allow rule for `GIT_` names: it removes each inherited name that starts with `GIT_`, and keeps the `GIT_` values that the code set on the command on purpose before the filter (`GIT_ASKPASS`, `GIT_TERMINAL_PROMPT`, `GIT_NO_REPLACE_OBJECTS`, and each other one that withGitCredentials or appendGitEnv sets — read both and list them in the doc comment). Remove daemonGitEnvDenied or make it the test of this rule.
2. HooksOffArgs also gives `-c core.fsmonitor=false`. Each caller gets it, with the option set and with it empty.
3. The git commands of the secret gate for an agent workspace get the environment of item 1 without each name that is not `PATH`, `HOME`, `TMPDIR`, `LANG`, `USER`, `LOGNAME`, a name that starts with `LC_`, or a `GIT_` value that the code set on purpose. The push, the fetch and the remote probes keep the credentials.
4. Tests: a function TestDaemonGitEnvDropsInheritedGitNames — with `GIT_COMMON_DIR`, `GIT_GRAFT_FILE`, `GIT_SHALLOW_FILE`, `GIT_EXEC_PATH`, `GIT_SSH_COMMAND`, `GIT_TRACE` and `GIT_REPLACE_REF_BASE` set in the test process and a wrapper `git` first in `PATH` that writes its environment, one Push with an agent workspace succeeds on the daemon's remote and no daemon-side git command of the delivery (gate, push, probes, fetch, count, co-author read) has one of the names; the same wrapper shows no `GITHUB_TOKEN` (fake value) in a gate command; a `core.fsmonitor` program in the repository config does not run under CountNewCommitsAgainstOrigin, with the option set and with it empty (a marker file); with the call of withDaemonGitEnv removed from the co-author read the test fails (the wrapper log covers that command).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'DaemonGitEnv|HooksOff|SecretGate'` passes; paste the output into the PR body.
- [ ] Put the deny list back into daemonGitEnv -> TestDaemonGitEnvDropsInheritedGitNames fails.
- [ ] Remove `core.fsmonitor=false` from HooksOffArgs -> the marker file test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The environment of the hand-over commands in `handover.go`. The gh commands. Network remotes (ssh, proxy, credential helper keys). The records of a push (published SHA, count, forgiveness; a later leg). Other push sites than Push and PushForceWithLease. Any other change of behaviour with the option empty than item 2.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6520

### LEG P8 — repo: qf-studio/pilot — title: fix(executor): run_as_user firewall probe — a status line at the time limit is read, the start line test does not depend on the host speed, bash runs with -p, a held output pipe ends as "did not run"

probe: present qf-studio/pilot func[[:space:]]TestRunAsFirewallMetadataNoStartRefuses\(
probe: present qf-studio/pilot func[[:space:]]probeMarkerResult\(
probe: present qf-studio/pilot func[[:space:]]agentDialAs\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsFirewallStatusAtTimeLimit\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the preflight proves the host firewall (checkRunAsFirewall in `internal/executor/agent_command.go`): as the agent user it runs a bash script that prints a start line, opens a connection through `/dev/tcp` and prints a status line. For the metadata address a time limit with a start line counts as "no answer", and that passes. A run-it review of the last change found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `agent_command.go` and `agent_firewall_test.go`. With the option empty nothing changes.

- At the time limit only the start line is read. An output with the start line and the status `0` (connected), from a sudo that ends late, gives "no answer", and the metadata check passes with a connection that was made. Mock: `echo pilot-probe=start; echo pilot-probe=0; sleep 10`.
- The case `start_line` of TestRunAsFirewallMetadataNoStartRefuses gives a time limit of 300 ms to a mock script that was written a moment before. On a loaded host the first run of a new script takes 500 to 900 ms, and the test fails (7 of 8 runs on a loaded macOS host).
- `--norc --noprofile` do not stop `BASH_ENV` and do not stop an exported function with the name `echo`; a non-interactive bash reads both. The option `-p` stops both. The allow list of the environment drops both today.
- When the probe process ends and another process keeps its standard output open, the call lasts the wait delay (10 seconds for a limit of 0.5 seconds) and the result comes from the context, although the probe ended in time.
- A status line with no start line before it is accepted as "failed (blocked)".
- The host and port text go into the script with no check.
- Not pinned by a test: the value of the output limit, the port 65536, the whole-line match of the start line, the over-limit case at the time limit.

## Scope

1. The time limit branch of agentDialAs: with a start line and no status line the result is "no answer"; with a start line and one status line the result is the one of probeMarkerResult for `0` (connected) and "did not run" for each other text; with no start line "did not run" as today.
2. probeMarkerResult gives "did not run" unless exactly one start line comes before exactly one status line.
3. The probe command is `bash --norc --noprofile -p -c SCRIPT`.
4. An error that is exec.ErrWaitDelay gives "did not run". The probe command gets a wait delay of 2 seconds.
5. agentDialAs gives "did not run" with no command when the host does not parse as an IP address or the port is not a number from 1 to 65535.
6. Tests: a function TestRunAsFirewallStatusAtTimeLimit with the mock of the first Context item: the metadata probe gives "connected" and the preflight refuses; the `start_line` case of TestRunAsFirewallMetadataNoStartRefuses runs its mock once before the measured call and uses a limit of 2 seconds; a real bash with `BASH_ENV` set to a file that prints the status marker (set by the mock sudo) does not change the result; a mock that ends at once and leaves a child with the output pipe open returns in under 5 seconds with "did not run"; table rows for a status with no start line, a status before the start line, the start line inside a longer line, 4096 and 4097 bytes of output, the port 65536, a host text `127.0.0.1;echo x`.

## Acceptance

- [ ] `go test -race -count=5 ./internal/executor/ -run 'RunAsFirewall|ProbeMarker'` passes; paste the output into the PR body.
- [ ] Read only the start line at the time limit -> TestRunAsFirewallStatusAtTimeLimit fails.
- [ ] Remove `-p` from the probe command -> the `BASH_ENV` test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The host names `localhost` and `[::1]`. The tests that skip where 127.0.0.2 cannot be bound. The script under `scripts/box/`. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6526

### LEG H19 — repo: qf-studio/pilot — title: fix(secretpatterns): the secret gate sees a submodule path with every diff config — fixed submodule arguments for the two scan commands, a length limit for the printed path, tests for the copy and merge headers

probe: present qf-studio/pilot func[[:space:]]unquoteGitPath\(
probe: present qf-studio/pilot func[[:space:]]scanPatch\(
probe: present qf-studio/pilot func[[:space:]]TestDaemonGitEnvDropsInheritedGitNames\(
probe: absent  qf-studio/pilot func[[:space:]]TestPushRangeSubmodulePathAnyConfig\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

The secret gate (`internal/executor/secretpatterns/pushrange.go`) scans the commits of a push for registered secret values and refuses the push. It reads the paths of a commit from the output of two git commands: the `git diff` of checkRange and the `git log -p` of scanCommits. This code runs for each user. A run-it review found the points below. Keep the change small: only the items below, only in `pushrange.go` and its test files.

- Both commands take the print mode for a submodule entry (a gitlink, mode 160000) from the config. With `diff.submodule=log` or `diff.submodule=diff` git prints a `Submodule PATH ...` line and no `diff --git` block. With `diff.ignoreSubmodules=all`, or with `ignore = all` for the path in a `.gitmodules` file of the working tree, git prints nothing for the entry. scanPatch then sees no path, and a registered value in the path of a submodule entry passes the gate. The tree of the push holds the plain value. With the arguments `--submodule=short --ignore-submodules=none` on both commands each of these cases is refused.
- The refusal prints the path whole. A path of 200 KB gives an error text, a log line, an alert and an issue comment of 200 KB.
- No test pins the unquote of the path on a `copy to` line and on a `diff --cc` line, and no test pins the rule for the byte 0x7f of printableText.

## Scope

1. The `git diff` of checkRange and the `git log -p` of scanCommits get the arguments `--submodule=short` and `--ignore-submodules=none`.
2. The printed path of a refusal is cut after the redaction: at most the last 200 characters, with `...` in front when it was cut. The scan still reads the whole path.
3. Tests: a function TestPushRangeSubmodulePathAnyConfig — a table with one row for each way (`diff.submodule=log`, `diff.submodule=diff`, `diff.ignoreSubmodules=all`, a `.gitmodules` file in the working tree with `ignore = all` and no commit of it, a committed `.gitmodules` with `ignore = all`), each with a gitlink entry made by `git update-index --add --cacheinfo 160000,SHA,PATH` whose path holds a registered fake value: CheckPush refuses and the error holds no part of the value; one row with a value that holds a double quote; a path of 5000 characters with the value at its start gives an error text under 400 characters with no part of the value; an empty file with a quote in its name added by a merge commit (the `diff --cc` header) is refused; a 100 % copy under `diff.renames=copies` to a name with the value is refused; a name with the byte 0x7f is printed in its escaped form.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/secretpatterns/` passes; paste the output into the PR body.
- [ ] Remove `--ignore-submodules=none` -> a row of TestPushRangeSubmodulePathAnyConfig fails.
- [ ] Remove `--submodule=short` -> a row of TestPushRangeSubmodulePathAnyConfig fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The content of a `.gitmodules` file (the escaped form of a config value). The commits inside a submodule. A mode-only change of a file whose name holds a value. The environment of the git commands. The redaction rules for file content.

## Refs

- Navigator TASK-651 and TASK-650 in this repo · review comment of PR#6523

### LEG H22 — repo: qf-studio/pilot — title: fix(secretpatterns): the secret gate reads a binary file that a merge commit adds — a combined diff with "Binary files differ" is scanned again against the first parent, or the push is refused

probe: present qf-studio/pilot func[[:space:]]scanPatch\(
probe: present qf-studio/pilot func[[:space:]]scanCommits\(
probe: present qf-studio/pilot func[[:space:]]TestPushRangeSubmodulePathAnyConfig\(
probe: absent  qf-studio/pilot func[[:space:]]TestPushRangeMergeAddsBinaryFile\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

The secret gate (`internal/executor/secretpatterns/pushrange.go`) scans the commits of a push for registered secret values and refuses the push. scanCommits reads each commit with `git log -p --cc --text`. This code runs for each user. A run-it review found the points below. Keep the change small: only the items below, only in `pushrange.go` and its test files.

- git does not apply `--text` to a combined diff. For a merge commit, a file with a NUL byte, or with the attribute `binary` or `-diff` (a committed `.gitattributes`, or the file `info/attributes` of the git directory), prints as `diff --cc PATH` and "Binary files differ". scanPatch then reads no added line of that file. The net diff of checkRange still shows the content, but it is empty when a later commit of the range deletes the file, and it does not run when no base resolves and the range holds a merge. In both cases CheckPush returns nil and the push sends the blob with the value. Reproduced with a real push; no config is needed.
- With `--diff-merges=first-parent` git honours `--text` for the merge commit and prints the content.
- Each of the two submodule arguments is pinned by a test only for both commands together: with the argument removed from one command alone each test passes.
- No test fails when the cut of the printed path runs before the redaction.

## Scope

1. scanCommits: when the patch of a commit holds a `diff --cc` or `diff --combined` block with a "Binary files" line, the commit is scanned a second time with `git log -1 -p --diff-merges=first-parent --text` and the other arguments of the first scan (no ext diff, no textconv, the submodule arguments, the prefixes); the result of that scan counts as the result for the commit. When the second scan fails or its output still holds a "Binary files" line for that path, the push is refused with a PushScanError "a binary file of a merge commit could not be read" (fail closed).
2. Tests: a function TestPushRangeMergeAddsBinaryFile — a merge commit that adds a file with a NUL byte and a registered fake value, and a later commit that deletes the file: CheckPush refuses and prints no part of the value; the same with the attribute `binary` from a committed `.gitattributes` and no NUL byte; the same with the attribute `-diff` from the file `info/attributes` of the git directory; the same file kept, with no base that resolves (the remote has no default branch ref); a merge commit that adds a clean binary file passes.
3. Test pins: one row that calls scanCommits directly for `diff.submodule=log` and for `diff.ignoreSubmodules=all`; one check that both scan commands carry both submodule arguments (through the seam that records the git commands); a path of 300 characters, then the value, then 185 characters gives an error text with no part of the value.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/secretpatterns/` passes; paste the output into the PR body.
- [ ] Remove the second scan -> TestPushRangeMergeAddsBinaryFile fails.
- [ ] Remove `--ignore-submodules=none` from the `git log -p` alone -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The base of the scan (remote-tracking refs that the session can write). UTF-16 content. A part of a value that stands next to a full value in one path. The environment of the git commands.

## Refs

- Navigator TASK-651 and TASK-650 in this repo · review comment of PR#6540

### LEG H24 — repo: qf-studio/pilot — title: fix(secretpatterns): the second scan of a merge commit reads only its binary paths — a merge of the published base is not refused for content of the base

probe: present qf-studio/pilot func[[:space:]]scanCommits\(
probe: present qf-studio/pilot func[[:space:]]TestPushRangeMergeAddsBinaryFile\(
probe: absent  qf-studio/pilot func[[:space:]]TestPushRangeMergeOfBaseWithBinaryFile\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

The secret gate (`internal/executor/secretpatterns/pushrange.go`) scans the commits of a push for registered secret values. This code runs for each user. For a merge commit whose combined diff holds a "Binary files" line, scanCommits runs a second scan of that commit with `--diff-merges=first-parent --text`, because git does not print binary content in a combined diff. A run-it review found the points below. Keep the change small: only the items below, only in `pushrange.go` and its test files.

- The patch of the second scan replaces the combined patch of the commit. The first-parent diff of a merge holds the whole change that the other parent brings. For a merge of the published base branch into the work branch, with one binary file that differs from both parents, the gate then scans the whole delta of the base: the push is refused when that delta holds a registered value, or when it is over the size limit of the scan, though the work branch adds none of it. With the second scan removed such a push passes. This refusal of an honest push is new.
- The second scan needs git 2.31 (`--diff-merges`). On an older git it fails and the push is refused; the refusal text and the warning for an old git do not name the version.
- No test pins: the trigger for a `diff --combined` block; the per-path check of the "Binary files" line; the refusal when the second scan fails; `--text` in the second scan; the submodule arguments in the second scan; the size limit of the second scan.

## Scope

1. The second scan reads only the paths of the combined diff that have a "Binary files" line: the paths go to the second `git log` call as literal pathspecs after `--` (the pathspec magic `:(literal)` or the option `--literal-pathspecs`), in the plain form of the path (the quoted form of git is decoded first).
2. The result for the commit is the combined patch of the first scan with the blocks of the second scan for those paths added; the blocks of the first scan for other paths count as before.
3. The fail closed rule stays: when the second scan fails, or one of those paths still has a "Binary files" line, the push is refused with the PushScanError of today.
4. The refusal for a failed second scan names the cause when git does not know the option: the text says that git 2.31 or later is needed. The check for an old git warns below 2.31.
5. Tests: a function TestPushRangeMergeOfBaseWithBinaryFile — a base branch on the remote gets a commit that adds a registered fake value in a text file (the remote already has it); the work branch merges that base, and the merge resolves one binary file to content that differs from both parents and holds no value: CheckPush passes; the same merge with the value in the binary file: refused, no part of the value printed; a merge of a base whose delta is over the size limit of the scan, with a clean binary file: passes. Pins: a `diff --combined` block triggers the second scan; a path with a quote and a space reaches the second scan as one literal pathspec; a wrapper git that fails for `--diff-merges` gives the refusal with the version text; the second scan carries `--text` and both submodule arguments.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/secretpatterns/` passes; paste the output into the PR body.
- [ ] Let the second patch replace the combined patch again -> TestPushRangeMergeOfBaseWithBinaryFile fails.
- [ ] Remove the pathspecs from the second scan -> TestPushRangeMergeOfBaseWithBinaryFile fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The base of the scan. A bound for the count of second scans in one push. UTF-16 content. The environment of the git commands.

## Refs

- Navigator TASK-651 and TASK-650 in this repo · review comment of PR#6566

### LEG H18 — repo: qf-studio/pilot — title: fix(executor): run_as_user push, records — a forgiven push is recorded, the count is 0 when nothing is published, a hand-over error is never forgiven, the WIP SHA is the published one

probe: present qf-studio/pilot func[[:space:]]TestDaemonGitEnvDropsInheritedGitNames\(
probe: present qf-studio/pilot \)[[:space:]]publishedHandOverSHA\(
probe: present qf-studio/pilot \)[[:space:]]attemptHandOverSHA\(
probe: absent  qf-studio/pilot func[[:space:]]TestHandOverPushForgivenIsRecorded\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, Push and PushForceWithLease of GitOperations (`internal/executor/git.go`) take the branch of the agent workspace over into the daemon's repository, run the secret gate on the hand-over SHA and push that SHA. GitOperations keeps the SHA of the attempt and the published SHA. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- A push that the forgiveness accepts (the push command fails after the remote took the commit) records nothing: publishedHandOverSHA is empty and the workspace record says "not pushed". The count then comes from the workspace of the agent, and the clean-up logs commits as not pushed.
- After a refused gate on the first attempt the count of new commits comes from the workspace and is 1 for a commit that never left the machine. One GitOperations value with a published branch gives its count for another branch.
- Two Push calls of one value at once: the loser fails in the hand-over ("another hand-over of the task is in progress") and the forgiveness accepts it, because the attempt SHA is a field of the value and holds the SHA of the winner.
- The loop of `runner.go` stores the HEAD of the agent workspace as pushedWIPSHA after a lease push (two places). The agent chooses that value.
- lastHandOverSHA has no caller outside tests.

## Scope

1. One method of GitOperations records a published hand-over (published SHA, the branch name, and the workspace record as pushed). The success path of the push and the forgiveness path both call it.
2. CountNewCommitsAgainstOrigin with an agent workspace: when nothing is published for the current branch it returns 0 and does not count in the workspace. The published SHA is used only for the branch it was recorded for.
3. A hand-over error is a typed error and is in the list of errors that the forgiveness never accepts. The attempt SHA is cleared at the start of each attempt before any step that can fail.
4. For an agent workspace the loop of `runner.go` stores publishedHandOverSHA as pushedWIPSHA, not the HEAD of the workspace (both places). With the option empty the value of today stays.
5. Remove lastHandOverSHA and its field when no production code reads them after items 1 to 3.
6. Tests in `handover_push_test.go`: a function TestHandOverPushForgivenIsRecorded — a push command that succeeds on the remote and then exits 1 (a wrapper `git`) leaves the published SHA set and the workspace record as pushed, and the count equals the count on the remote; the count after a refused gate on the first attempt is 0; a value with branch X published gives 0 for a branch Y with a refused gate; two Push calls at once (10 rounds): the loser returns an error; after a lease push with an agent workspace pushedWIPSHA equals the published SHA when the agent moved its HEAD.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'HandOverPush'` passes; paste the output into the PR body.
- [ ] Remove the record call from the forgiveness path -> TestHandOverPushForgivenIsRecorded fails.
- [ ] Take the hand-over error out of the never-forgiven list -> the test with two Push calls fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The environment of the git commands. The hooks-off arguments. The gh commands. Other push sites than Push and PushForceWithLease. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6520

### LEG H12 — repo: qf-studio/pilot — title: fix(executor): run_as_user gh commands — the repository argument keeps the host of the remote, the epic gh calls leave the workspace, the graph file read has a time limit and refuses a FIFO

probe: present qf-studio/pilot \)[[:space:]]ghDirForDaemonRepo\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverPushForgivenIsRecorded\(
probe: present qf-studio/pilot func[[:space:]]readMemoryGraphFile\(
probe: present qf-studio/pilot func[[:space:]]queryRecentSubIssues\(
probe: absent  qf-studio/pilot func[[:space:]]TestGhRepoArgKeepsHost\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent works in a workspace that the agent user owns, and the daemon delivers from its own repository. A `gh` command of the daemon must not run in the workspace: gh reads the repository from the git config of its directory, and that config is the agent's. ghDirForDaemonRepo (`internal/executor/git.go`) gives the four PR commands the daemon's repository and a `--repo OWNER/NAME` argument. Run-it reviews found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- ghDirForDaemonRepo keeps only the owner and the name of the origin remote. For a remote on another host (GitHub Enterprise) `--repo owner/name` has no host, and gh resolves it against github.com with the token of the daemon.
- A remote that ends in `.git/` gives the name `name.git`.
- The parse error for a remote with one path segment prints the URL, with the credentials when the URL holds them.
- More gh commands of `internal/executor/epic.go` take the execution path as their directory: queryRecentSubIssues, recoverExistingSubIssues, runGHIssueCreateWithRetry, the issue comment call, the issue close call and queryParentDoneViaGitHub. With an agent workspace that path is the workspace.
- readMemoryGraphFile reads the graph file of the workspace as the agent user with a background context. A FIFO in place of the file stalls each read for 30 seconds or more.

## Scope

1. A function ghRepoArg in `git.go`: from the URL of the origin remote it gives `OWNER/NAME` for the host github.com and `HOST/OWNER/NAME` for each other host (https, ssh and the scp-like form; a port is dropped). It removes a final `/` before the suffix `.git`. Its error never holds the user part of the URL: print the URL with the part before `@` replaced by `REDACTED`. ghDirForDaemonRepo uses it.
2. A helper that gives the directory and the repository arguments of a gh command for an execution path: for a path that is an agent workspace on record it gives the daemon's repository and the arguments of ghRepoArg; for each other path it gives the path and no argument, as today. The six gh calls of `epic.go` named above use it. A workspace path with no record is an error; no gh command runs in it.
3. readMemoryGraphFile takes a context from its caller; the read as the agent user has a time limit of 10 seconds and refuses a path that is not a regular file (checked as the agent user in the same command, with no second step). A refusal or a time limit is handled as a missing file with the Warn line of today.
4. Tests: a function TestGhRepoArgKeepsHost — a table of remote URLs (github.com https and scp-like, another host https, another host scp-like, a port, a final `.git/`, one path segment with credentials in the URL): the argument, and for the error row no credential text in the error; with an agent workspace each of the six epic calls runs in the daemon's repository with the `--repo` argument (a fake `gh` first in `PATH` that writes its directory and arguments to a file); a workspace path with no record runs no gh command; a FIFO in place of the graph file returns in under 15 seconds with the result of a missing file.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'GhRepoArg|Epic.*Workspace|MemoryGraph'` passes; paste the output into the PR body.
- [ ] Let ghRepoArg drop the host -> TestGhRepoArgKeepsHost fails.
- [ ] Set `cmd.Dir` of one epic call back to the execution path -> the epic test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The life of the hand-over ref and the workspace sweep (a later leg). The environment of the git commands of the delivery and of the secret gate. The `gh` shim of the agent side. GitLab and other trackers. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6482

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

### LEG H26 — repo: qf-studio/pilot — title: fix(executor): each git command that carries the token runs no hook and no program of the repository config — hooks off in the one credential wrapper, alternate refs command off, no signed push, no bundle URI

probe: present qf-studio/pilot func[[:space:]]withGitCredentials\(
probe: present qf-studio/pilot func[[:space:]]TestTokenCommandsRunNoTransportProgram\(
probe: absent  qf-studio/pilot func[[:space:]]TestEachTokenCommandRunsNoRepositoryProgram\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. withGitCredentials (`internal/executor/git_credentials.go`) gives a git command the token of the daemon through an askpass helper. PR#6608 made four commands (the push, the fetch of the count, the two `ls-remote` probes) run no program of the repository config. A run-it review found the points below; each needs the run_as_user option empty (with the option set these commands run in the repository of the daemon). Keep the change small: only the items below.

- Ten other call sites of withGitCredentials (seven fetch and pull sites in `internal/executor/git.go`, three in `internal/executor/worktree.go`) do not get the hooks-off arguments (secretpatterns.HooksOffArgs). A `reference-transaction` hook or a `core.fsmonitor` program of the repository runs with `PILOT_GIT_TOKEN` in its environment.
- `core.alternateRefsCommand` is not in the list of gitCredentialConfigArgs. With a file `.git/objects/info/alternates` the fetch of the count runs it with the token.
- pushArgs (`internal/executor/git.go`) has no `--no-signed`. With `push.gpgSign=true` and `gpg.program` in the repository config, and a remote that takes a signed push, the push runs the program with the token.
- `fetch.bundleURI` of the repository config makes a fetch send a request with the token to another host.
- `-c core.gitProxy=` protects nothing: `GIT_PROXY_COMMAND=` is the line that stops the proxy program. With `GIT_PROXY_COMMAND` removed from daemonGitEnvSet (`git.go`) each test passes: the transport test runs only with the option empty.

## Scope

1. withGitCredentials puts the hooks-off arguments on each command, next to the arguments of gitCredentialConfigArgs, so no call site can miss them. A call site that adds them itself keeps one copy.
2. gitCredentialConfigArgs also sets `core.alternateRefsCommand` and `fetch.bundleURI` to empty.
3. pushArgs adds `--no-signed`.
4. Tests: a function TestEachTokenCommandRunsNoRepositoryProgram — a temp repository with a bare remote one commit ahead; an executable `.git/hooks/reference-transaction`, a `core.fsmonitor` program and a `core.alternateRefsCommand` program (with an alternates file) each write a marker file when `PILOT_GIT_TOKEN` is in their environment; call GetDiffAgainstOrigin, CommitsBehindMain, Pull and CountNewCommitsAgainstOrigin with a fake token provider: no marker file exists. A source read test: each call of withGitCredentials in `git.go` and `worktree.go` is found, and the count matches a number in the test, so a new call site makes the test fail until its author adds a case. The push: with `push.gpgSign=true` and a `gpg.program` in the config and `receive.certNonceSeed` on the bare remote, the push runs no program. TestTokenCommandsRunNoTransportProgram runs with the option set too (the mock sudo of the package), and it fails when `GIT_PROXY_COMMAND` is removed from daemonGitEnvSet.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run TokenCommand` passes; paste the output into the PR body.
- [ ] Remove the hooks-off arguments from withGitCredentials -> TestEachTokenCommandRunsNoRepositoryProgram fails.
- [ ] Remove `GIT_PROXY_COMMAND` from daemonGitEnvSet -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A remote URL that the repository config changes (`url.*.insteadOf`). A host check in the askpass helper. `remote.*.uploadpack`. The local git commands with no token. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6608

### LEG U41 — repo: qf-studio/pilot — title: refactor(quality): a command builder seam for the quality gates — build, configure, kill and file read go through one interface, with no change of behaviour

probe: present qf-studio/pilot \)[[:space:]]executeCommand\(
probe: present qf-studio/pilot func[[:space:]]listPackages\(
probe: absent  qf-studio/pilot func[[:space:]]SetCommandBuilder\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The package `internal/quality` runs the quality gates of a task (build, test, lint) in the directory of the task: executeCommand in `internal/quality/runner.go` starts `sh -c COMMAND` with the environment of the daemon, and `internal/quality/scope.go` runs `git` and `go list` and reads test files there. With the option run_as_user the code in that directory is written by the agent user, and these commands must run as the agent user. The package `internal/executor` holds the code for that (agentCommand), and `internal/quality` cannot import it. This leg adds the seam; a later leg plugs the agent side in. Keep the change small: only the items below, only in `internal/quality`. No behaviour changes.

## Scope

1. A new file `command.go` in `internal/quality` with an interface CommandBuilder: Build(ctx, dir string, env []string, name string, args ...string) (*exec.Cmd, error); Configure(cmd *exec.Cmd, waitDelay time.Duration); Kill(cmd *exec.Cmd, sig syscall.Signal) error; ReadFile(path string) ([]byte, error). A default builder does what the code does today: exec.CommandContext with the directory and the environment, procgroup.Configure, procgroup.Kill, os.ReadFile.
2. A function SetCommandBuilder(b CommandBuilder) that sets the builder of the package (nil sets the default), safe for use from more than one goroutine, and a function that returns the current one.
3. executeCommand, the gitOutput and listPackages of `scope.go`, and the test file read of `scope.go` use the current builder. The environment that executeCommand passes is the one of today (the process environment plus the scope variables); the builder gets it as an argument.
4. The kill on a time limit and the wait delay of a gate go through Configure and Kill of the builder; executeCommand does not call procgroup or set `cmd.Cancel` itself.
5. Tests: a function TestExecuteCommandUsesTheBuilder with a fake builder that records each call: a gate run calls Build with `sh`, the directory and an environment that holds the scope variables; a gate that sleeps past its time limit is ended through Kill of the builder and returns the time limit error of today; the scope code calls Build for `git` and for `go` and ReadFile for a test file; after SetCommandBuilder(nil) the default is back. Each test of the package that exists today passes with no change.

## Acceptance

- [ ] `go test -race -count=1 ./internal/quality/` passes; paste the output into the PR body.
- [ ] Call procgroup.Kill directly in executeCommand again -> TestExecuteCommandUsesTheBuilder fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Any code in `internal/executor`. The lookup of the `make` program. The scope rules. Any change of behaviour.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U48 — repo: qf-studio/pilot — title: refactor(quality): each read of the task directory goes through the command builder — the Makefile reads, the marker file checks and the path resolution; the scope commands get Configure; a nil command is an error

probe: present qf-studio/pilot func[[:space:]]SetCommandBuilder\(
probe: present qf-studio/pilot func[[:space:]]TestExecuteCommandUsesTheBuilder\(
probe: absent  qf-studio/pilot func[[:space:]]TestTaskDirectoryReadsUseTheBuilder\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The package `internal/quality` has a seam, CommandBuilder (`internal/quality/command.go`): Build, Configure, Kill and ReadFile, with a default that does what the code did before. A later leg plugs in a builder that runs each command as a second Linux user, because the task directory then belongs to that user and its content is not trusted. A run-it review of the seam found that some reads of the task directory do not use it, and three more points. Keep the change small: only the items below, only in `internal/quality`. With the default builder the behaviour stays as it is.

- The read of the `Makefile` (`internal/quality/runner.go` and `internal/quality/types.go`) is a plain os.ReadFile and os.Open as the daemon, outside the time limit of the gate. A FIFO in place of the `Makefile` blocks the gate run for longer than its limit, with no call of the builder.
- The marker and lock file checks (os.Stat in fileExists), the list of the project directory (os.ReadDir), the check for `go.mod` (`internal/quality/scope.go`) and the path resolution with filepath.EvalSymlinks read the task directory as the daemon.
- The gitOutput and listPackages of `scope.go` call Build and never Configure: these commands get no process group and no wait delay from the builder.
- A Build that returns a nil command with a nil error, and a builder value that is a typed nil, make the runner panic.
- With a cancel of the outer context the gate result has the exit code -1 and a nil error.

## Scope

1. CommandBuilder gets three more methods: Stat(path string) (os.FileInfo, error), ReadDir(path string) ([]os.DirEntry, error) and EvalSymlinks(path string) (string, error). The default builder calls os.Stat, os.ReadDir and filepath.EvalSymlinks.
2. Each read of the task directory in the package uses the current builder: both reads of the `Makefile` (through ReadFile), fileExists, the list of the project directory, the `go.mod` check and each EvalSymlinks call of `scope.go`.
3. The reads of the `Makefile` are inside the time limit of the gate: a read that does not return before the limit gives the time limit error of the gate (run the read in a goroutine and select on the context; the default builder cannot stop a blocked read, the gate must still return).
4. gitOutput and listPackages call Configure on the command before the start, with the wait delay that the gates use.
5. A nil command from Build, or a nil builder value, is an error result of the gate or of the scope ("quality: the command builder gave no command"), not a panic. SetCommandBuilder with a typed nil sets the default.
6. A cancel of the outer context gives the result of today plus a non-nil error that wraps the context error.
7. Tests: a function TestTaskDirectoryReadsUseTheBuilder with a fake builder that records each call — a gate run and a scope run call ReadFile for the `Makefile`, Stat for a marker file and for `go.mod`, ReadDir for the project directory and EvalSymlinks, and no file of the task directory is opened by another route (the fake returns content that differs from the disk, and the result shows the content of the fake); a FIFO in place of the `Makefile` with the default builder: the gate returns the time limit error within the limit plus 2 seconds; the scope commands get one Configure call each, before the start; a builder whose Build gives a nil command gives the error and no panic; a typed nil builder sets the default; an outer cancel gives a non-nil error.

## Acceptance

- [ ] `go test -race -count=1 ./internal/quality/` passes; paste the output into the PR body.
- [ ] `git grep -nE 'os\.(ReadFile|Open|Stat|ReadDir)\(|filepath\.EvalSymlinks\(' -- internal/quality ':!internal/quality/*_test.go' ':!internal/quality/command.go'` finds nothing.
- [ ] Read the `Makefile` with os.ReadFile again -> TestTaskDirectoryReadsUseTheBuilder fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Any code in `internal/executor`. The lookup of the `make` program on the PATH of the daemon. The scope rules. A builder for each runner in place of one for the process.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6553

### LEG U42 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates, part 1 — the quality gates run as the agent user with the allow list environment

probe: present qf-studio/pilot func[[:space:]]TestTaskDirectoryReadsUseTheBuilder\(
probe: present qf-studio/pilot func[[:space:]]setAgentEnvIn\(
probe: present qf-studio/pilot func[[:space:]]agentCommand\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsUserGateEnvHasNoDaemonSecret\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent works as a second Linux user in a workspace of its own. The quality gates (`make build`, `make test`, `make lint` and the scope commands) still run as the daemon user with the environment of the daemon, on code that the agent wrote: a test of that code can read each secret of the daemon. The package `internal/quality` has a command builder seam (SetCommandBuilder). The executor has the pieces for the agent side: agentCommand (a sudo command in a session of its own, with a kill through sudo), setAgentEnvIn (the allow list environment), killProcessGroup, agentReadFile. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. A new file `gate_command.go` in `internal/executor` with a type that implements quality.CommandBuilder. With the option empty (runAsSettingsNow gives nil) each method does what the default builder of the quality package does. With the option set: Build uses agentCommand, sets the directory, and sets the environment with setAgentEnvIn from the environment it got; Configure does nothing more than set the wait delay (agentCommand made the session and the cancel); Kill uses killProcessGroup; ReadFile uses agentReadFile; Stat, ReadDir and EvalSymlinks run as the agent user (the helpers of `internal/executor/agent_files.go` when one fits, else a command of the agent side such as `test -e`, `ls -a` or `realpath`, with a time limit of 10 seconds), so that no method of the builder touches the workspace as the daemon user. Build never returns a nil command with a nil error. Each of ReadFile, Stat, ReadDir and EvalSymlinks returns within 10 seconds with the option set (its command of the agent side has a context with that limit and the kill of the agent route): the quality package gives the scope reads and the command detection no time limit of their own, so a read that does not return would block a run before any gate limit.
2. The builder is registered once with quality.SetCommandBuilder where the executor package is set up for the daemon (the place that sets the run_as_user state at start); a test helper registers and restores it.
3. The allow list of the agent environment (`internal/executor/agent_command.go`) also passes each name that starts with `PILOT_CHANGED_` (the scope variables of the gates) and the name `PILOT_TASK_ID`.
4. Tests with the mock sudo of the package (mockSudoAgent, setRunAsStateForTest): a function TestRunAsUserGateEnvHasNoDaemonSecret — with a variable `DAEMON_ONLY_SECRET` and a fake `GITHUB_TOKEN` in the test process, a gate command that prints its environment runs through sudo as the agent user and prints neither name, and prints the `PILOT_CHANGED_` values; the scope commands (`git`, `go list`) go through sudo too; a gate that sleeps past its time limit is ended through the sudo kill route and the result is the time limit error; a FIFO in place of the `Makefile` of the workspace gives the time limit error of the gate and no read as the daemon user; a link in the workspace that points to a file of the daemon is not followed as the daemon user by Stat, ReadDir or EvalSymlinks; with a mock sudo that sleeps, each of the four read methods returns an error within 12 seconds; with the option empty the first argument of the gate command is `sh` and no sudo call is logged.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./internal/quality/ -run 'RunAsUserGate|ExecuteCommand'` passes; paste the output into the PR body.
- [ ] Let Build pass the environment with no allow list -> TestRunAsUserGateEnvHasNoDaemonSecret fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Workflow hooks, evidence commands, the lint fix (other legs). The lookup of the `make` program. The Go proxy check. The preflight refusal. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U49 — repo: qf-studio/pilot — title: fix(executor): run_as_user gates — tests that prove the allow list and the registration; a gate refuses to run with the wrong builder; only values that the code sets pass under the scope names

probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateEnvHasNoDaemonSecret\(
probe: present qf-studio/pilot func[[:space:]]registerGateCommandBuilder\(
probe: present qf-studio/pilot func[[:space:]]setAgentEnvIn\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsUserGateRefusesTheDefaultBuilder\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the quality gates run as the agent user: a command builder in `internal/executor/gate_command.go` is registered with the quality package (registerGateCommandBuilder, called from PreflightRunAsUser in `internal/executor/agent_command.go`), it builds each command with agentCommand and gives it the allow list environment. A run-it review found that the code does this, and that the tests do not prove it, and some smaller points. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below, only in `gate_command.go`, `agent_command.go` and their test files. With the option empty nothing changes.

- The test of the gate environment uses two probe names that end in `_SECRET` and `_TOKEN`. A deny filter of Build removes names with these endings before the allow list runs. With the allow list removed from Build the test passes, and a gate then sees 93 names of the daemon, among them `SSH_AUTH_SOCK`, `AWS_ACCESS_KEY_ID` and the home directory of the daemon.
- No test fails when the call of registerGateCommandBuilder is removed from PreflightRunAsUser: each test registers the builder with its own helper. Without that one line the gates run as the daemon user with the full environment while the option is set.
- The allow list passes each name with the prefix `PILOT_CHANGED_` and the name `PILOT_TASK_ID` to each agent command, with the value that the daemon process holds. The gate scope sets exactly three names (`PILOT_CHANGED_PACKAGES`, `PILOT_CHANGED_FULL`, `PILOT_CHANGED_REASON`). Nothing on the gate path sets `PILOT_TASK_ID`, so a gate sees the value of the daemon process.
- For Stat, ReadDir and EvalSymlinks no test proves that an error of the route check is an error and never a read as the daemon user.
- Build does not check that the command it returns is a sudo command of the agent side.
- The time limit of a read method holds only when the kill through sudo works: with a kill that fails a read takes 22 seconds.
- `ANTHROPIC_CUSTOM_HEADERS` of the daemon reaches a gate.

## Scope

1. The allow list: the prefix rule for `PILOT_CHANGED_` goes away. The three scope names and `PILOT_TASK_ID` are names that only the code sets: setAgentEnvIn drops the value that the daemon process holds for each of them and keeps a value only when the caller put it into the environment argument on purpose (the gate builder for the three scope names; no caller for `PILOT_TASK_ID` in this leg). The agent session itself gets none of the four from the daemon process.
2. `ANTHROPIC_CUSTOM_HEADERS` does not pass to a gate command.
3. Build, with the option set, returns an error when the command is not a sudo command for the agent user (a check of the first argument and the user argument of the built command); it never returns a command that would run as the daemon user.
4. Where the gates of a run start in the executor (both routes that call the quality runner: the gate run after the backend, and the re-gate of the pre-PR fix): with the option set, the run refuses to start the gates when the builder of the quality package is not the gate builder of the executor, with the error "run_as_user: the quality gates have no agent command builder". The registration no longer depends on a single call that can be skipped in silence.
5. Each read method of the builder returns after its time limit plus 2 seconds at most, also when the kill does not end the process (the method stops the wait and returns the time limit error; the process is left to the kill route).
6. Tests with the mock sudo of the package: a function TestRunAsUserGateRefusesTheDefaultBuilder — with the option set and the default builder of the quality package in place, the gate start returns the error and no gate command runs; after PreflightRunAsUser alone (no test helper) the builder of the quality package is the gate builder. The environment test gets probes with no denied ending (`DAEMON_ONLY_VALUE`, `SSH_AUTH_SOCK`, `AWS_ACCESS_KEY_ID`) and asserts that each name of the gate environment is in a fixed list of the test (the allow list names, the names that the agent base sets, the three scope names, the names that the shell sets). With `PILOT_CHANGED_OPERATOR=x` and `PILOT_TASK_ID=GH-DAEMON` in the test process, neither reaches a gate nor the environment of the agent session; the three scope names arrive with the values of the scope. Stat, ReadDir, EvalSymlinks and ReadFile of a path below a link of the workspace that points to a directory of the daemon each give an error. A mock sudo that ignores the kill: each read method returns within its limit plus 3 seconds. A test seam that makes agentCommand return a plain command: Build gives the error.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsUserGate|GateCommand'` passes; paste the output into the PR body.
- [ ] Replace setAgentEnvIn in Build by a plain copy of the filtered environment -> the environment test fails.
- [ ] Remove the call of registerGateCommandBuilder from PreflightRunAsUser -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Workflow hooks, evidence commands, the lint fix. The size limit of ReadDir and the file modes of Stat. A total bound for all scope reads of one run. The preflight refusal. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6580

### LEG U43 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates, part 2 — workflow hooks run as the agent user, with a kill of the process group

probe: present qf-studio/pilot func[[:space:]]runWorkflowHook\(
probe: present qf-studio/pilot func[[:space:]]RunHook\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateRefusesTheDefaultBuilder\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunWorkflowHookRunsAsAgent\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. A repository can name hook scripts in its workflow file; RunHook (`internal/executor/workflow/hooks.go`) runs each one with `bash -lc` in the directory of the task. runWorkflowHook (`internal/executor/runner.go`) calls it with an environment made of the full environment of the daemon plus `PILOT_TASK_ID`. With run_as_user set these scripts are text of the repository, and the agent can change them: they must run as the agent user with the allow list environment. A cancel of the context stops only the bash process today, not its children. The package `workflow` cannot import the executor. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty the commands and their environment stay as today, except the kill of the group.

## Scope

1. `internal/executor/workflow/hooks.go`: a function RunHookWith that takes one more argument, a function that builds the command (ctx, dir, env, script) and returns the `*exec.Cmd`. RunHook stays and calls RunHookWith with a builder that does what the code does today, plus a process group and a kill of that group on cancel (procgroup).
2. runWorkflowHook: with the option set it calls RunHookWith with a builder that uses agentCommand for `bash -lc SCRIPT`, the directory, and setAgentEnvIn with the environment of gitAgentBaseEnv plus `PILOT_TASK_ID` of the task (the hook builder puts the value into the environment argument on purpose; the value of the daemon process never passes); the kill is the one of agentCommand. With the option empty it calls RunHook with the environment of today.
3. Each call site of runWorkflowHook keeps its order and its context; the hook before the removal of the worktree still runs before the workspace of the agent is removed.
4. Tests with the mock sudo of the package: a function TestRunWorkflowHookRunsAsAgent — a hook that prints its environment runs through sudo as the agent user, with `PILOT_TASK_ID` and with no `DAEMON_ONLY_SECRET` of the test process; a hook that starts a background child and sleeps: after a cancel, the child is not alive (option empty) and the sudo kill route is called (option set); with the option empty no sudo call is logged and the environment holds a variable of the test process as today.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'WorkflowHook|RunHook'` passes; paste the output into the PR body.
- [ ] Build the hook command with exec.CommandContext when the option is set -> TestRunWorkflowHookRunsAsAgent fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The Claude Code hooks in the settings file. The load of the workflow file (it already reads as the agent user). The quality gates, the evidence commands, the lint fix. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U44 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates, part 3 — evidence commands run as the agent user; the evidence file writes and the env context read go through the agent side

probe: present qf-studio/pilot func[[:space:]]sanitizedAcceptanceEnv\(
probe: present qf-studio/pilot func[[:space:]]evidenceWriteFile\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateRefusesTheDefaultBuilder\(
probe: absent  qf-studio/pilot func[[:space:]]TestEvidenceCommandRunsAsAgent\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. After a run the daemon checks the acceptance lines of the issue: it runs their commands in the directory of the task (RunCommand of shellAcceptanceCommandRunner in `internal/executor/acceptance_evidence_run.go`) and, for a mutation line, reads a file, writes a changed copy, runs the test and restores the file. With run_as_user set the command is built with no sudo and runs as the daemon user on code of the agent; the read of the file is a plain os.ReadFile as the daemon; the write (evidenceWriteFile in `internal/executor/agent_files.go`) refuses with errEvidenceWriteRunAsUser. readEnvContext (`internal/executor/prompt_builder.go`) reads a file of the workspace as the daemon and follows links. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. RunCommand: with the option set the command is built by agentCommand (`sh -c COMMAND` as today), with the directory and with setAgentEnvIn from the environment of sanitizedAcceptanceEnv; the time limit and the kill stay (killProcessGroup knows the agent route). With the option empty the code of today runs.
2. The read of a file for a mutation line uses agentReadFile when the path is on the agent side (agentFileRoute), else os.ReadFile as today.
3. evidenceWriteFile writes through agentWriteFile when the path is on the agent side; errEvidenceWriteRunAsUser and its refusal go away. The restore of the file after the mutation run uses the same route and still runs after a failed or cancelled command.
4. readEnvContext reads through agentReadFile when the path is on the agent side; a read error is the empty text of today.
5. Tests with the mock sudo of the package: a function TestEvidenceCommandRunsAsAgent — an evidence command that prints its environment runs through sudo with no `DAEMON_ONLY_SECRET`; a mutation line changes the file through the agent write route, runs the command and restores the first content, also when the command fails; a link in place of the env context file that points to a file of the daemon gives the empty text; with the option empty no sudo call is logged.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'Evidence|EnvContext'` passes; paste the output into the PR body.
- [ ] Put the refusal back into evidenceWriteFile -> the mutation case of TestEvidenceCommandRunsAsAgent fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The rules that choose which acceptance lines run. The quality gates, the workflow hooks, the lint fix. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U58 — repo: qf-studio/pilot — title: fix(executor): the mutation check of the acceptance evidence never leaves a changed file — a failed write or restore is repaired or stops the run before the review and the push

probe: present qf-studio/pilot func[[:space:]]evidenceWriteFile\(
probe: present qf-studio/pilot func[[:space:]]TestEvidenceCommandRunsAsAgent\(
probe: present qf-studio/pilot func[[:space:]]readEnvContext\(
probe: absent  qf-studio/pilot func[[:space:]]TestMutationRestoreFailureStopsTheRun\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. After the backend the daemon checks the acceptance lines of the issue (`internal/executor/acceptance_evidence_run.go`). For a mutation line it reads a file of the task directory, writes a changed copy, runs a test command, and writes the first content back. The evidence runs before the pre-PR review, its fix rounds and the push, which all work on the same tree. With the option run_as_user the reads and writes go through the agent side (sudo), which has more ways to fail than a plain file write: a sudo error, a time limit, a kill, a full disk of the agent user. A run-it review found the points below. Item 1 and item 2 are also true with the option empty. Keep the change small: only the items below.

- When the restore write fails, the file stays in its changed form, the next acceptance line still runs, and the run goes on to the review and the push. No log line says so; the only trace is a "Not verified" reason, which also prints the full path of the workspace into the PR body.
- When the write of the changed copy fails after the file was cut to zero length (the helper cuts first, then copies; a cancel of the budget context or a time limit between the two), the code returns with no restore: an empty or partial file stays.
- A command that a cancelled context killed is reported as a mutation that the test caught.
- No test fails when the environment of an evidence command is the full process environment in place of the filtered one, or when the directory of the command is not set.
- A FIFO at the path of the env context file blocks readEnvContext (`internal/executor/prompt_builder.go`) for 30 seconds.

## Scope

1. After a mutation line, the file is checked: the code reads it again and compares it with the first content. This check runs after each outcome (the command passed, failed, was killed; the changed write failed; the restore write failed), with a context that is not the cancelled budget context.
2. When the content differs, the code writes the first content again (once), then, when it still differs, runs `git checkout -- FILE` for it through the git route of the task directory, then checks again.
3. When the file is still changed after that: one Error line with the task id and the path of the file relative to the task directory; no later acceptance line runs; the evidence result carries a typed error, and the run stops with a failed result before the pre-PR review, the fix rounds and the push ("acceptance evidence left FILE changed; the run stops before the push").
4. The reason text of a line never holds the full path of the task directory: paths are relative to it.
5. A command that ended because the context was cancelled or the budget ran out is reported with that reason, not as a caught mutation.
6. readEnvContext with the option set: the read has a time limit of 5 seconds and takes a regular file only; each other case is the empty text.
7. Tests: a function TestMutationRestoreFailureStopsTheRun — with a write seam that fails the restore once, the second write repairs the file and the run goes on; with a seam that fails each write, `git checkout` repairs it (a git repository as the task directory) and the run goes on; with the writes and the checkout failing, the file is reported, the next line does not run, and the run result is the failure with no push step (drive the function of the runner that calls the evidence); a changed write that cuts the file and fails leaves the first content after the repair; a cancel during the command gives the cancel reason and the first content; no reason text holds the directory of the test. With the mock sudo and the option set: an evidence command gets no `DAEMON_ONLY_VALUE` and no `SSH_AUTH_SOCK` of the test process, and its working directory is the task directory; a FIFO at the env context path returns the empty text within 7 seconds.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'Evidence|Mutation|EnvContext'` passes; paste the output into the PR body.
- [ ] Remove the check of the file after a mutation line -> TestMutationRestoreFailureStopsTheRun fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The rules that choose which acceptance lines run. Files over the size limit of the agent file helpers. The names `NODE_ENV` and `CI` in the environment of an evidence command. A task directory outside the workspace root. The quality gates, the workflow hooks, the lint fix.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6605

### LEG U45 — repo: qf-studio/pilot — title: feat(executor): run_as_user gates, part 4 — the lint run and the lint fix run as the agent user; a failed restore of the hook settings fails the run

probe: present qf-studio/pilot \)[[:space:]]runGoLint\(
probe: present qf-studio/pilot \)[[:space:]]autoFixLint\(
probe: present qf-studio/pilot func[[:space:]]TestRunWorkflowHookRunsAsAgent\(
probe: absent  qf-studio/pilot func[[:space:]]TestAutoFixLintRunsAsAgent\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. runGoLint and autoFixLint of GitOperations (`internal/executor/lint.go`) start the lint program in the directory of the task with the environment of the daemon; the lint fix changes files there. With run_as_user set the directory is the workspace of the agent user: the program must run as that user (its config file is text of the repository, and the fix must write files that the agent user owns). The stage and amend steps after the fix already use the git route of the agent side. After a session the daemon restores the hook settings file of the workspace (RestoreHookSettings in `internal/executor/hooks.go`); when the restore fails the run only logs a warning. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. runGoLint and autoFixLint: with the option set (an agent workspace) the lint command is built by agentCommand with the directory and with setAgentEnvIn from gitAgentBaseEnv; with the option empty the command of today.
2. The lookup of the lint program: with the option set, a missing program is reported from the result of the run as the agent user (exit status and output), not from a lookup in the PATH of the daemon.
3. With the option set, a failed restore of the hook settings file after a session is an error of the run: the result of the run is a failure with a text that names the file. With the option empty the warning of today stays.
4. With the option set, the lint commands do not get the name `ANTHROPIC_CUSTOM_HEADERS` of the daemon (the gate builder drops it through gateEnvDenied in `internal/executor/gate_command.go`; use the same list).
5. Tests with the mock sudo of the package: a function TestAutoFixLintRunsAsAgent — the lint command and the fix command go through sudo as the agent user; the test process sets fake values for `DAEMON_ONLY_SECRET`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `GITHUB_TOKEN` and `ANTHROPIC_CUSTOM_HEADERS`, and none of these names is in the environment of the two commands (the review of the workflow hooks PR#6609 found that a test with one name passes when the code hands over the model environment); the working directory of the two commands is the task directory (the fake lint program writes its `pwd` to a file); a missing lint program gives the result of today for "no linter"; with the option empty no sudo call is logged. One test: with the option set a restore function that fails makes the run fail.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'Lint|RestoreHookSettings'` passes; paste the output into the PR body.
- [ ] Build the fix command with exec.CommandContext when the option is set -> TestAutoFixLintRunsAsAgent fails.
- [ ] Give the lint command the environment of os.Environ() when the option is set -> TestAutoFixLintRunsAsAgent fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The gate script code in `internal/executor/gate.go` (it has no caller). The quality gates, the workflow hooks, the evidence commands. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U65 — repo: qf-studio/pilot — title: fix(executor): run_as_user — one tool environment for each agent-side command that is not the model session: no name with the prefix ANTHROPIC_ or CLAUDE_ reaches a gate, a hook, the lint, an evidence command or a git hook

probe: present qf-studio/pilot func[[:space:]]TestAutoFixLintRunsAsAgent\(
probe: present qf-studio/pilot func[[:space:]]TestRunHookBackgroundChildDoesNotFail\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentToolEnvDropsModelNames\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the allow list of the agent user (`internal/executor/agent_command.go`) keeps each name with the prefix `ANTHROPIC_` or `CLAUDE_`, because the model session needs them; a deny list for the model drops the credentials among them. The same input (gitAgentBaseEnv) feeds the commands that are not the model session: the quality gates, the workflow hooks, the lint, the evidence commands and git of the agent side (whose hooks are programs of the workspace). Three run-it reviews found one name after another that reaches such a command: `ANTHROPIC_CUSTOM_HEADERS` (it can hold a gateway credential; the gate builder drops it through gateEnvDenied, the git route does not, so a `pre-commit` hook of the workspace gets it in the amend after the lint fix), `CLAUDE_CODE_CLIENT_KEY_PASSPHRASE`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, `CLAUDE_CODE_USE_BEDROCK`. A list of single names does not hold. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. One function in `agent_command.go` gives the tool environment: the input environment with each name removed that has the prefix `ANTHROPIC_` or `CLAUDE_`, and each name of gateEnvDenied. A comment says that only the model session gets these names.
2. With the option set, these commands use it: the gate builder (`internal/executor/gate_command.go`), the workflow hook builder (agentHookCommand in `internal/executor/runner.go`), the lint program (`internal/executor/lint.go`), the evidence command (`internal/executor/acceptance_evidence_run.go`), and each git command of the agent side (`internal/executor/git_command.go`). The backend command of the model session and the other model subprocesses keep the environment of today.
3. Tests: a function TestAgentToolEnvDropsModelNames — the test process sets fake values for `ANTHROPIC_API_KEY`, `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_CLIENT_KEY_PASSPHRASE`, `CLAUDE_CODE_USE_BEDROCK` and a name `CLAUDE_NEW_NAME_OF_TOMORROW`; with the mock sudo of the package, for each of the five command kinds a program of the workspace writes its environment to a file (a gate command, a hook script, the fake lint program, an evidence command, a `pre-commit` hook that runs in the amend after the lint fix): no name with one of the two prefixes is in the file. One case shows that the backend command of the model session still gets `ANTHROPIC_BASE_URL`. In `internal/executor/workflow`: a hook `pwd -P` with the option empty prints the directory of the call (the review of PR#6634 found that the removal of the working directory in bashCommand of `hooks.go` passes each test).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run AgentToolEnv` passes; paste the output into the PR body.
- [ ] Give the git route the environment of gitAgentBaseEnv with no filter -> TestAgentToolEnvDropsModelNames fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The names `GOFLAGS`, `NODE_OPTIONS` and `GOTOOLCHAIN`. The environment with the option empty. The model deny list. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comments of PR#6609, PR#6611 and PR#6625

### LEG U66 — repo: qf-studio/pilot — title: fix(executor): run_as_user lint — the lint refuses a command that is not a sudo command, a failed restore of the hook settings fails the run before the push, and a test runs that path through the runner

probe: present qf-studio/pilot func[[:space:]]TestAutoFixLintRunsAsAgent\(
probe: present qf-studio/pilot func[[:space:]]TestAgentToolEnvDropsModelNames\(
probe: absent  qf-studio/pilot func[[:space:]]TestSettingsRestoreFailureStopsBeforeThePush\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. PR#6625 made the lint run and the lint fix run as the agent user when run_as_user is set (runLintProgram in `internal/executor/lint.go`), and made a failed restore of the hook settings file a failure of the run (`internal/executor/runner.go`, in executeWithOptions). A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes. Since this text was written, two daemon-filed follow-ups merged (PR#6645 and PR#6660): they added a test through executeWithOptions for the failed restore (TestSettingsRestoreFailureFailsRunThroughExecute) and decided that a run whose PR is already open keeps its result, with the failure in IntentWarning. Keep that decision. Read `internal/executor/hooks.go`, `runner.go` and `lint_agent_test.go` first, and do only what is still missing of the items below.

- The restore runs in a defer at the return of executeWithOptions, after the push and the PR step. A failed restore then gives a failed run with a pushed branch and maybe a PR link.
- No test goes through the runner for the restore failure: the test calls the two helper functions by hand, and four changes of the wiring in `runner.go` pass each test.
- runLintProgram does not refuse a command that is not a sudo command of the agent user. With the option set and a host where agentCommand gives a plain command, the lint runs as the daemon user with the environment of the daemon. The gate builder refuses this case.
- Exit status 127 of a lint program that is present is read as "program missing", and its output is dropped.

## Scope

1. With the option set, the restore of the hook settings file runs once, before the push step, on the paths that reach the push; a failure there fails the run before the push with the text of today. The defer stays for the paths that do not reach the push, and it does not run the restore a second time.
2. runLintProgram returns an error and runs nothing when the option is set and the built command is not a sudo command of the agent user (call agentSudoCommandError, the shared check that the gate builder and the hook builder use since PR#6634).
3. "Program missing" is read from exit status 127 only when the output is empty or is the one line of sudo or the shell that says that the command is not found; else the result is a lint failure with the output.
4. Tests: a function TestSettingsRestoreFailureStopsBeforeThePush — through executeWithOptions with a fake backend that commits and the mock sudo of the package that fails the write of the settings file after the backend ran: the result is a failure, the branch is not on the remote, no PR call is made. The test fails when the restore call before the push is removed. With the option set and a command builder that gives a plain command, the lint does not run and the error names the cause. A fake lint program that prints findings and exits with 127 gives a lint failure with the output.
5. From the run-it review of PR#6650: with the option set, the file helpers of the agent side (`internal/executor/agent_files.go`), the two preflight probes (`agent_command.go`), the workspace bundle commands (`agent_workspace.go`) and the `tar` copy (`navigator_agent_copy.go`) use the tool environment function too; one test dumps the environment of a file helper and of the gate tool probe and finds no name with the prefix `ANTHROPIC_` or `CLAUDE_`.
6. `configs/pilot.example.yaml`, at the run_as_user option: one comment line says that with the option set a gate, a hook, the lint and an evidence command get no `ANTHROPIC_*` and no `CLAUDE_*` name, so a test that calls a model API from a gate gets no key.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Lint` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run SettingsRestore` passes; paste the output into the PR body.
- [ ] Remove the restore call before the push -> TestSettingsRestoreFailureStopsBeforeThePush fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The environment of the lint program and of the git hooks (the tool environment ticket). The route choice of the lint outside the workspace root. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6625

### LEG U70 — repo: qf-studio/pilot — title: fix(executor): the hook settings file of the daemon never enters a commit — the restore runs before the first lint fix too, the lint amend never stages the file, and a refused lint command reaches the result

probe: present qf-studio/pilot func[[:space:]]TestSettingsRestoreFailureStopsBeforeThePush\(
probe: present qf-studio/pilot func[[:space:]]TestAutoFixLintRunsAsAgent\(
probe: absent  qf-studio/pilot func[[:space:]]TestLintAmendNeverCommitsTheHookSettings\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. For a model session the daemon writes its hook entries into `.claude/settings.json` of the workspace and restores the file after the session. The lint fix (autoFixLint in `internal/executor/lint.go`) amends the commit with `git add -A`. PR#6667 moved the restore before the push so that the amend does not stage the file; a run-it review found the points below. The first point is older than that PR and holds with the run_as_user option empty too, which is the mode of each host today. Keep the change small: only the items below.

- executeWithOptions (`internal/executor/runner.go`) calls autoFixLint at two places. The new restore point is before the second one only. When the first one fixes an issue and amends, the settings file of the daemon is in the pushed commit (it holds the hook entries with the path of the script directory of the daemon), in a project where the file is not ignored. In a project that tracks the file, the amend commits the changed file.
- With the option set, a lint command that is not a sudo command is refused, but the refusal does not reach the result or the log: autoFixLint returns the same value as for a missing program.
- A lint program that is present and exits with 127 and one line that names the program and ends with "no such file or directory" is read as missing, and the line is dropped.
- The run-level test of "a run with an open PR keeps its result" (TestSettingsRestoreFailureKeepsOpenPRThroughExecute) was replaced and not kept.
- The push of uncommitted work in `internal/executor/git_freshness.go` has no restore before it.

## Scope

1. The restore of the hook settings runs before the first autoFixLint call too, with the rule of the second place (once for a run; a failure with the option set fails the run before the push; with the option empty the warning of today).
2. The amend of the lint fix never stages the settings file of the daemon: when the restore did not run or the file still differs from its state before the run, the path is left out of the `git add` (a literal pathspec exclude for `.claude/settings.json`), and one Warn line says so.
3. The push of uncommitted work in `git_freshness.go` runs after the same restore, or leaves the file out in the same way.
4. A refused lint command (the error of agentSudoCommandError) is an error of the run step with its text in the result and one Error line; it is not "no linter".
5. "Program missing" needs exit status 127 and an output that is empty or is one line of the shell or of sudo with "command not found"; a line with "no such file or directory" from the program itself is a lint failure with the output.
6. Tests: a function TestLintAmendNeverCommitsTheHookSettings — through executeWithOptions with hooks on, the pre-push lint on, a fake lint program that reports one issue until `--fix` ran, and a backend that commits: the pushed commit does not list `.claude/settings.json`, in a repository that does not track the file and in one that tracks it (there the file in the commit is the tracked content); with the option empty and with it set (the mock sudo of the package). The run-level test of the open PR is back. A refused lint command gives a failed step with the text of the refusal.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Lint` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run SettingsRestore` passes; paste the output into the PR body.
- [ ] Remove the restore before the first lint fix and the exclude of item 2 -> TestLintAmendNeverCommitsTheHookSettings fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Test pins for the tool environment of the preflight probes, the bundle commands and the `tar` copy. The restore before the direct-commit push (that path is refused with the option set). The line length of the config reference. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6667

### LEG U71 — repo: qf-studio/pilot — title: fix(executor): the contract evidence session keeps the guard hooks of the daemon — no restore of the hook settings before the first lint fix; the lint amend keeps the settings file out of the index with a reset

probe: present qf-studio/pilot func[[:space:]]TestLintAmendNeverCommitsTheHookSettings\(
probe: present qf-studio/pilot func[[:space:]]TestSettingsRestoreFailureStopsBeforeThePush\(
probe: absent  qf-studio/pilot func[[:space:]]TestModelSessionsBeforeThePushHaveTheHookSettings\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. For a model session the daemon writes its hook entries (guards) into `.claude/settings.json` of the workspace and restores the file later. PR#6674 added a restore before the first lint fix place of executeWithOptions (`internal/executor/runner.go`), so that the amend of the lint fix (`git add -A` in `internal/executor/lint.go`) does not commit the file, and it added a pathspec exclude for the amend. A run-it review found the points below. The first one is a regression with the run_as_user option empty, the mode of each host today. Keep the change small: only the items below.

- With the pre-push lint on, the new restore runs before the contract evidence session (getContractEvidence in `runner.go`, a `claude --print` in the workspace). That session now starts without the hook entries of the daemon; before PR#6674 it had them.
- The exclude (`git add -A -- :/ ':(exclude,literal).claude/settings.json'`) makes `git add` fail when `.claude/` is ignored (exit 1) and when `.claude` is a symlink or a submodule (exit 128): the lint fix is then not committed.
- With either one of the two guards removed (the early restore, the exclude) the run-level test passes: only the pair is under test.

## Scope

1. Remove the restore before the first lint fix place. The restore points of PR#6667 stay (before the push, after the last model session before the push; the defer for the other paths). No model session between the backend and the push starts without the hook entries.
2. The amend of the lint fix: stage with `git add -A` as before PR#6674, then take the settings file out of the index again when the hook entries of the daemon are in the workspace file (`git reset -q -- .claude/settings.json`, with `--literal-pathspecs`; a path that git does not know is not an error). No pathspec exclude. One Debug line says that the file was kept out. This holds at both lint fix places and at the push of uncommitted work in `internal/executor/git_freshness.go`.
3. Tests: a function TestModelSessionsBeforeThePushHaveTheHookSettings — through executeWithOptions with hooks on and the pre-push lint on, with the contract evidence seam (contractEvidenceFetchFn) reading `.claude/settings.json` of the workspace: the hook entries are there; the test fails when a restore runs before that session. TestLintAmendNeverCommitsTheHookSettings gets three more repositories: `.claude/` in `.gitignore` (the lint fix is committed, `git status` shows no staged leftover), `.claude` as a symlink to a directory outside, and the untracked case with the reset removed in the acceptance mutation below. With the option empty and with it set (the mock sudo of the package).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Lint` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run ModelSessions` passes; paste the output into the PR body.
- [ ] Remove the reset after the stage -> TestLintAmendNeverCommitsTheHookSettings fails.
- [ ] Put a restore before the first lint fix place back -> TestModelSessionsBeforeThePushHaveTheHookSettings fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A tracked settings file that the agent itself commits during the session (the commit of the agent then holds the hook entries; a separate design point). A lint program that writes the settings file. The 127 rule. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6674

### LEG U46 — repo: qf-studio/pilot — title: feat(executor): run_as_user preflight — the start is refused when GOPROXY of the daemon holds credentials

probe: present qf-studio/pilot func[[:space:]]PreflightRunAsUser\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateRefusesTheDefaultBuilder\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsUserPreflightRefusesGoProxyCredentials\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent user gets an allow list environment. Founder decision 2026-11-19: the Go proxy variables of the daemon (GOPROXY, GOFLAGS, GONOSUMDB, GOPRIVATE) pass to the agent user, and the daemon start is refused when the value of GOPROXY holds credentials. The four names are in the allow list today; no check of the value exists. PreflightRunAsUser (`internal/executor/agent_command.go`) holds the start checks of the option. Keep the change small: only the items below, only in `agent_command.go` and its test files. With the option empty nothing changes.

## Scope

1. A function goProxyHasCredentials(value string) bool: the value is a list with the separators comma and pipe; it is true when one element parses as a URL with a user part (a name, or a name and a password). The elements `direct` and `off` and an element that does not parse are not credentials.
2. PreflightRunAsUser: with the option set, a check that reads GOPROXY through a variable that a test can replace, placed with the other config checks before the check of the host; on true the start is refused with a message that names GOPROXY and says that a proxy with credentials cannot pass to the agent user. The message never prints the value.
3. A GOFLAGS value that holds the text `GOPROXY=` is checked with the same function for the text after `GOPROXY=` up to the next space.
4. Tests: a function TestRunAsUserPreflightRefusesGoProxyCredentials — a table: empty, `https://proxy.golang.org,direct`, `off`, a URL with a name and a password (refused), a list whose second element has a name only (refused), the pipe separator with a credential element (refused), an element that is not a URL; the refusal text holds no part of the value; with the option empty the check does not run.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsUserPreflight'` passes; paste the output into the PR body.
- [ ] Remove the check from PreflightRunAsUser -> TestRunAsUserPreflightRefusesGoProxyCredentials fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

GONOSUMDB and GOPRIVATE (they hold no credentials). A netrc file of the daemon user. An option for more environment names (dropped by the founder decision). The preflight refusal of the option as a whole.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U56 — repo: qf-studio/pilot — title: fix(executor): run_as_user gates — tests on the three gate start functions, no command detection as the daemon with the wrong builder, a preflight probe for the tools and the temporary directory of the agent user

probe: present qf-studio/pilot func[[:space:]]guardGateChecker\(
probe: present qf-studio/pilot func[[:space:]]TestRunWorkflowHookRunsAsAgent\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserPreflightRefusesGoProxyCredentials\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsUserGateStartSitesRefuseTheDefaultBuilder\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the quality gates run as the agent user through a command builder of the executor (`internal/executor/gate_command.go`). A guard (guardGateChecker) refuses to start the gates when the builder of the quality package is not that builder; it is called at three places: the gate run after the backend and the timeout salvage (`internal/executor/runner.go`), and the re-gate of the pre-PR fix (`internal/executor/pre_pr_fix.go`). PreflightRunAsUser (`internal/executor/agent_command.go`) holds the start checks of the option. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- With the guard removed at one of the three call sites each test passes: the test calls the guard function itself, not the production functions.
- The detection of the build and test command of a project runs before the guard and through the current builder. With the option set and the wrong builder it reads the workspace (the `Makefile`, marker files, the directory list) as the daemon user.
- The six names of the gh guard (the list runAsGhGuardEnv) pass from the daemon process to a gate command; a gate does not call `gh`.
- A gate gets `PATH` and `TMPDIR` with the values of the daemon. When `go`, `make` or the lint program is found only through a `PATH` entry that the agent user cannot read, or `TMPDIR` is a directory that the agent user cannot write, each gate fails though the code is right. Nothing checks this before the first run.
- A read of the builder whose kill fails leaves its process, with no log line.

## Scope

1. The detection of the build and test command, with the option set: it runs only when the builder of the quality package is the gate builder of the executor; else it returns the error of the guard and reads nothing. This holds at both places that build the quality checker (the executor, and the factory in `cmd/pilot/adapters.go` through an exported form of the check).
2. The gate builder does not pass the names of runAsGhGuardEnv to a gate command.
3. PreflightRunAsUser, after the sudo checks: a probe as the agent user with the environment that a gate gets. It checks that each tool of a list is found (`sh`, `git`, and `make` and `go` when the daemon itself finds them in its PATH) and that a file can be made and removed in the temporary directory of that environment. A failure refuses the start with a message that names the tool or the directory and the agent user. The probe has a time limit of 10 seconds, and a test can replace it.
4. When a read of the gate builder gives up after a failed kill, one Error line names the method, the path and the process id.
5. Tests with the mock sudo of the package: a function TestRunAsUserGateStartSitesRefuseTheDefaultBuilder — for each of the three production functions (the gate run after a fake backend that commits, the timeout salvage, the re-gate of the pre-PR fix; use the fixtures that their tests use today), with the option set and the default builder: the result holds the error text of the guard and a marker file that the gate command would write does not exist. With the wrong builder the detection reads no file of the workspace (no sudo line and no content of a `Makefile` that the test wrote). A gate environment holds none of the gh guard names. The preflight probe: a mock sudo that reports `make` as missing refuses with the name `make`; a temporary directory that cannot be written refuses with its path; a host where the daemon has no `go` does not ask for `go`.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'RunAsUserGate|GateCommand|RunAsUserPreflight'` passes; paste the output into the PR body.
- [ ] Remove guardGateChecker at the re-gate of the pre-PR fix -> TestRunAsUserGateStartSitesRefuseTheDefaultBuilder fails.
- [ ] Remove the probe from PreflightRunAsUser -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Values of `GOFLAGS`, `NODE_OPTIONS` and `GOTOOLCHAIN` of the daemon. A second kill for a read that was given up. The size limit of ReadDir. Workflow hooks, evidence commands, the lint fix. The preflight refusal of the option as a whole.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6591

### LEG U59 — repo: qf-studio/pilot — title: fix(executor): workflow hooks — a hook that leaves a background child does not fail the run; with run_as_user the hook gets no model credential and no request header, and the builder refuses a command that is not a sudo command

probe: present qf-studio/pilot func[[:space:]]agentHookCommand\(
probe: present qf-studio/pilot func[[:space:]]TestRunWorkflowHookRunsAsAgent\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunHookBackgroundChildDoesNotFail\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. PR#6609 moved the workflow hooks to the agent user when run_as_user is set, and added a kill of the process group. RunHookWith (`internal/executor/workflow/hooks.go`) runs the scripts of a hook; agentHookCommand (`internal/executor/runner.go`) builds the command when the option is set. A run-it review found the points below. The first point is a change of behaviour with the option empty, which is the mode of each host today. Keep the change small: only the items below.

- With the option empty: a script that exits with status 0 and leaves a background child on its output pipe (`sleep 8 & echo done`) now fails after 5 seconds with `exec: WaitDelay expired before I/O complete`, and the later scripts of that hook do not run. Before PR#6609 the call returned nil.
- With the option set: the environment of the hook comes from gitAgentBaseEnv, and the allow list keeps `ANTHROPIC_CUSTOM_HEADERS` of the daemon. The gate builder drops this name (gateEnvDenied in `internal/executor/gate_command.go`); the hook builder does not.
- No test fails when the environment of the hook is os.Environ(): the test probes one name that the allow list drops in each case.
- agentHookCommand does not check that the built command is a sudo command of the agent user (the gate builder does), and the working directory of the hook is not under test.

## Scope

1. RunHookWith: when the script exits with status 0 and the wait ends with exec.ErrWaitDelay, the script counts as passed: one Warn line names the hook and says that a child process kept the output open; the later scripts run. A script with a status that is not 0, a time limit and a cancel stay failures as today. This holds with the option empty and with the option set.
2. With the option set, the hook environment drops each name of gateEnvDenied.
3. agentHookCommand returns an error, and no command, when the command that agentCommand built is not a sudo command of the agent user (use the check of the gate builder; move it to a shared function if needed).
4. Tests: a function TestRunHookBackgroundChildDoesNotFail — the hook `sleep 8 & echo done` and a second script `echo second`: no error, the second script runs, the call returns within 7 seconds. In TestRunWorkflowHookRunsAsAgent or next to it, with the mock sudo of the package: the test process sets fake values for `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_CUSTOM_HEADERS`, and none of these names is in the environment of the hook; a hook `pwd -P` prints the task directory; with the option set and a command builder that returns a plain command the hook does not run and the error names the cause.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/workflow/ -run RunHook` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run WorkflowHook` passes; paste the output into the PR body.
- [ ] Give the hook the environment of os.Environ() when the option is set -> a test fails.
- [ ] Remove the working directory from the hook command -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A child that leaves the process group with `setsid` (the process clean-up task TASK-656 covers left processes). A size limit for the hook output. A check that the hook directory is inside the workspace root. The lint run. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6609

### LEG U60 — repo: qf-studio/pilot — title: fix(executor): run_as_user preflight — the GOPROXY credentials check fails closed: an element that does not parse or that holds an `@` refuses the start

probe: present qf-studio/pilot func[[:space:]]goProxyHasCredentials\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserPreflightRefusesGoProxyCredentials\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateStartSitesRefuseTheDefaultBuilder\(
probe: absent  qf-studio/pilot func[[:space:]]TestGoProxyCredentialsCheckFailsClosed\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, GOPROXY and GOFLAGS of the daemon pass to the agent user, and PreflightRunAsUser (`internal/executor/agent_command.go`) refuses the start when goProxyHasCredentials finds a proxy URL with a user part. A review found that the check passes each element that url.Parse rejects: a password with a `%` and no hex digits, with a space, a `/`, a `#` or a `?`, a bad port, and an element with no scheme. Go cannot use such a proxy, but the text with the password still reaches the agent. The test row "element that is not a URL" fixes this behaviour as wanted today. A run-it review of PR#6628 found three small points on the tool probe of the preflight (checkRunAsGateTools in `internal/executor/gate_command.go`) and one on the timeout salvage; items 5 to 7 hold them. Keep the change small: only the items below. With the option empty nothing changes.

## Scope

1. goProxyHasCredentials: an element other than `direct` and `off` is credentials when it does not parse as a URL, or when it holds an `@`, or when it parses with a user part. An empty element stays no credentials.
2. goFlagsProxyHasCredentials: the value after `GOPROXY=` ends at a space or a tab.
3. The refusal texts stay as they are; no part of a value is printed.
4. Tests: a function TestGoProxyCredentialsCheckFailsClosed — a table on goProxyHasCredentials with fake values: `https://test-user:fake%password@proxy.example.com`, a password with a space, with a `/`, with a `#`, with a `?`, `https://test-user:fake-password@proxy.example.com:port`, `test-user:fake-password@proxy.example.com` (no scheme), `https://test-user@%zz` — each is true; `https://proxy.golang.org,direct`, `off`, the empty text and `https://proxy.example.com/path` are false. A GOFLAGS value with a tab after the proxy value is checked. Change the row "element that is not a URL" of TestRunAsUserPreflightRefusesGoProxyCredentials to a refusal.
5. The tool probe: the probe file gets a name that cannot be known before (`mktemp` in the temporary directory, removed by a `trap` at the exit of the script), not `.pilot-gate-probe-<pid>`: today a file or a link of that name, which the agent user can make, refuses the start with the wrong cause, and a failed remove leaves the file.
6. The tool probe: the refusal for a failed probe command holds the first line of the standard error of sudo (at most 200 bytes), and a refusal by the time limit says "no answer in 10 seconds". A test pins the time limit: with a context that has no limit the test fails.
7. The timeout salvage (`internal/executor/runner.go`): when the gate checker returns the error of the guard (errGateNoAgentBuilder), the run fails with the text of the guard and pushes nothing; today this error goes to holdPushedBranch, which pushes a branch with a commit that no gate checked. Each other checker error keeps the hold push of today. In the subtest `timeout salvage` of TestRunAsUserGateStartSitesRefuseTheDefaultBuilder: the remote has no branch of the task.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run GoProxy` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run RunAsUserGate` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run RunAsUserPreflight` passes; paste the output into the PR body.
- [ ] Put the `continue` for an element that does not parse back -> TestGoProxyCredentialsCheckFailsClosed fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A token in the path or in the query of a proxy URL (no rule can tell it from a path; the operator notes of the option name it). GONOSUMDB and GOPRIVATE. The preflight refusal of the option as a whole.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6610

### LEG U61 — repo: qf-studio/pilot — title: fix(executor): acceptance evidence at PR creation — a file that the mutation check left changed is put back or fails the run; the log line says what happened

probe: present qf-studio/pilot func[[:space:]]testMutationLeftChangedStopsExecute\(
probe: present qf-studio/pilot \)[[:space:]]leftChanged\(
probe: absent  qf-studio/pilot func[[:space:]]TestMutationLeftChangedAtPRCreation\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The mutation check of the acceptance evidence changes a file of the workspace, runs a command and restores the file (`internal/executor/acceptance_evidence_run.go`). PR#6611 made a failed restore stop the run: the collection carries the error (leftChanged in `internal/executor/pre_pr_evidence.go`), and the run stops when the evidence is collected before the pre-PR review. A run-it review found that the stop exists only on that path. collectAcceptanceEvidenceForReview returns nil when the review mode is off (the default), when no reviewer is set, for a direct commit, when a gate did not pass, and when too little time is left; the evidence then runs at PR creation through appendAcceptanceEvidence, which returns only the text of the PR body. There the error is dropped: the run reports success, the PR is made, the file stays changed in the tree, and the log has the Error line "Acceptance evidence left a file changed; the run stops before the push", which is false on this path. The pushed commit is clean, because the push is before this point. Keep the change small: only the items below.

## Scope

1. Each caller of appendAcceptanceEvidence gets the error of the collection (leftChanged), through a second return value or a method that the callers use. Do not drop it at a call site.
2. On the PR creation path, a collection with this error makes the run a failure before the PR is created, with a text that names the file. The failure path of today then cleans the tree. The PR is not created.
3. The Error line says what happens on its path: on the path before the review it says that the run stops before the review and the push; on the PR creation path it says that the branch is pushed, the run fails and no PR is created.
4. Tests: a function TestMutationLeftChangedAtPRCreation — the fixture of testMutationLeftChangedStopsExecute with the review mode off: the result is a failure, no PR call is made, the error names the file, and the log line does not say "stops before the push". A source read test: each call of appendAcceptanceEvidence in the package handles the error (the count of the calls matches a number in the test).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Mutation` passes; paste the output into the PR body.
- [ ] Drop the error at the PR creation call site -> TestMutationLeftChangedAtPRCreation fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The byte compare of the restore check, the `git checkout` repair, a panic of the command runner (a second ticket covers them). The evidence environment. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6611

### LEG U62 — repo: qf-studio/pilot — title: fix(executor): the mutation check of the acceptance evidence — the restore check reads the file type and mode, the restore never follows a link, the git repair keeps uncommitted work and gets the filtered environment, a panic still restores

probe: present qf-studio/pilot func[[:space:]]TestMutationRestoreFailureStopsTheRun\(
probe: present qf-studio/pilot func[[:space:]]TestMutationLeftChangedAtPRCreation\(
probe: absent  qf-studio/pilot func[[:space:]]TestMutationRestoreChecksTypeAndMode\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The mutation check of the acceptance evidence (`internal/executor/acceptance_evidence_run.go`) changes a file of the workspace, runs a command, restores the file and checks the result; when a write fails it repairs the file with `git checkout`. A run-it review of PR#6611 found the points below, with the run_as_user option set (the file operations go through the agent side) and with it empty. Keep the change small: only the items below.

- The check after the restore compares bytes only. A command that replaces the file by a symbolic link leaves a link: the check passes, and the restore write lands in the target outside the workspace (with the option empty it is a write of the daemon). A command that writes a copy and renames it over the file changes the mode (0755 becomes 0644): the check passes and the run goes on with a changed tree.
- The `git checkout` repair writes the index version over the file. An uncommitted change of the agent in that file is lost. The checkout also runs a smudge filter program of the repository config; with the option empty the filter gets the full environment of the daemon.
- The restore is not in a `defer`: a panic in the command runner leaves the mutated file.
- No test fails when the evidence command gets the full process environment with the option set: the test probes two names that the allow list of the agent user drops in each case.

## Scope

1. Before the mutation: read the type and the permission bits of the file with Lstat (through the agent side when the option is set). A file that is not a regular file is not mutated; the line is "Not verified" with the reason.
2. The restore, and the check after it: when the path is no longer a regular file, the restore does not write through it. It removes the path and makes a new regular file with the first content and the first permission bits (no follow of a link). The check passes only when the type is regular, the permission bits are the first ones and the content is the first content. A mismatch that the restore cannot repair is the error of today (the run stops).
3. The `git checkout` repair runs only when the first content is equal to the index version of the file (`git show :PATH`, compared in memory); else it is skipped and the run stops with the error of today. The two git calls get the environment of the evidence commands (the filtered one), not the full environment of the daemon, and the hooks-off arguments stay.
4. The restore and the check run in a `defer`, so a panic of the command runner still restores the file; the panic goes on after it.
5. Tests: a function TestMutationRestoreChecksTypeAndMode, with the option set (the mock sudo of the package) and with it empty — the command replaces the file by a link to a file outside the workspace: at the end the path is a regular file with the first content, and the outside file is unchanged; a file with mode 0755 that the command replaces through a rename is 0755 with the first content at the end; a file with an uncommitted change, and a write that fails after the first one: the repair does not run `git checkout`, and the run stops; a `.gitattributes` filter program that writes its environment to a file does not see a `GITHUB_TOKEN` of the test process; a command runner that panics leaves the first content. In TestEvidenceCommandEnvAndDirAsAgent: a fake `ANTHROPIC_API_KEY` of the test process is not in the output of the command.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Mutation` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/executor/ -run EvidenceCommand` passes; paste the output into the PR body.
- [ ] Make the check compare the content only -> TestMutationRestoreChecksTypeAndMode fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The stop on the PR creation path (its own ticket). The wording of the reason after a cancel. A stop after a fix round. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6611

### LEG U69 — repo: qf-studio/pilot — title: fix(executor): the mutation check of the acceptance evidence — the restore never leaves the workspace through a link in a parent directory or a link put at the path, and the git repair reads the file name as a literal path

probe: present qf-studio/pilot func[[:space:]]TestMutationRestoreChecksTypeAndMode\(
probe: present qf-studio/pilot func[[:space:]]TestMutationLeftChangedAtPRCreation\(
probe: absent  qf-studio/pilot func[[:space:]]TestMutationRestoreStaysInTheWorkspace\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The mutation check of the acceptance evidence (`internal/executor/acceptance_evidence_run.go`) changes a file of the workspace, runs a command (a program of the workspace), restores the file and checks the result. PR#6651 made the restore and the check read the file type and the mode of the last path component. A run-it review found the points below. With the run_as_user option empty the daemon does the writes itself, so a write through a link is a write of the daemon outside the workspace, and the check then says "restored" for a workspace file that is still mutated. Keep the change small: only the items below.

- The path is resolved once, before the command (resolveWorktreeConfinedPath). When the command replaces a parent directory by a link to a directory outside the workspace, the restore writes, or makes, the file outside (mutationRestoreWrite; `os.WriteFile` in `internal/executor/agent_files.go`); the check passes; the real file stays mutated.
- The restore is "Lstat, then write through the path". A link put at the path between the two is followed, with the option empty and with it set (the write script is `cat > "$1"`).
- The `git checkout` of the repair (mutationGitCheckout: `checkout -- rel`) reads the file name as a pathspec pattern: for a file `a*.go` it writes the index version over an uncommitted change of `ab.go`.
- The setuid, setgid and sticky bits are not kept and not compared: a file with mode 4755 ends as 0755 and the check passes.

## Scope

1. Before each restore write, and in the check after it, the path is resolved again with the rule of resolveWorktreeConfinedPath. A path that is no longer inside the workspace is "not restored": nothing is written through it, and the run stops with the error of today, with a text that says that the path left the workspace.
2. The restore write never follows a link at the last component: with the option empty the file is opened with `O_NOFOLLOW` and with no `O_CREAT`, or the content goes to a new file beside the target (exclusive create) that is renamed over it; with the option set the script does the same (a new file beside the target, then `mv -f`), not `cat > "$1"`.
3. The git calls of the repair use literal pathspecs (`git --literal-pathspecs ...`), with the `--` of today.
4. The mode that is read, compared and set back includes the setuid, setgid and sticky bits.
5. Tests: a function TestMutationRestoreStaysInTheWorkspace, with the option empty and with it set (the mock sudo of the package) — the command replaces the parent directory by a link to a directory outside the workspace, with the same file name present there and absent there: the outside directory is unchanged, no file is made there, and the run stops; a write seam puts a link at the path before the restore write: the outside file is unchanged; a file `a*.go` and a file `ab.go` with an uncommitted change, and a write that fails after the first one: `ab.go` keeps its change; a file with mode 4755 has mode 4755 at the end (skip this case when the file system of the test drops the bit).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Mutation` passes; paste the output into the PR body.
- [ ] Remove the second path resolution before the restore write -> TestMutationRestoreStaysInTheWorkspace fails.
- [ ] Remove `--literal-pathspecs` -> TestMutationRestoreStaysInTheWorkspace fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A hard link of the file to a file outside the workspace. The epic path and the texts of the call sites (their own ticket). The wording of the "Not verified" reason. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6651

### LEG U67 — repo: qf-studio/pilot — title: fix(executor): acceptance evidence — a file that the mutation check left changed is put back on the epic path too, each call site has a test, and the texts fit their path

probe: present qf-studio/pilot func[[:space:]]TestMutationLeftChangedAtPRCreation\(
probe: present qf-studio/pilot func[[:space:]]TestMutationRestoreChecksTypeAndMode\(
probe: present qf-studio/pilot func[[:space:]]TestMutationRestoreStaysInTheWorkspace\(
probe: absent  qf-studio/pilot func[[:space:]]TestMutationLeftChangedAtEachCallSite\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. The mutation check of the acceptance evidence changes a file of the workspace, runs a command and restores it; a failed restore gives an error of the collection (leftChanged). PR#6637 made each of the six call sites of appendAcceptanceEvidence handle that error (five in `internal/executor/runner.go`, one in `internal/executor/pre_pr_hold.go`). A run-it review found the points below. The behaviour is right at each site today, with one exception (the epic path). Keep the change small: only the items below.

- Five of the six call sites have no test that fails when the handling is made dead (`if false && ...`); only one PR creator site is driven, and the source read test checks only that an `if` follows the call.
- On the epic path (finalizeEpicBranchPR in `runner.go`) the failure returns before the deferred reset of a failed run without a worktree is registered: the changed file stays in the shared checkout for the next task. This site also sends no TaskFailed alert and no `task.failed` webhook, unlike the three sites of Execute.
- The texts do not fit each path: the error text says "the run stops before the push" on paths where the branch is pushed; the hold comment says "the run fails and no PR is created" next to "Pilot parked this task"; the log line "the branch is pushed" is written before the push of the hold.
- At the three sites of Execute the result does not name the branch, and `result.BranchPushed` is not set though the branch is on the remote.

## Scope

1. The epic path: when the collection has the error, put the file back before the return (the restore of the collection with a fresh context, or `git checkout -- FILE` with the hooks-off arguments when the file is not changed in the index), and send the TaskFailed alert and the `task.failed` webhook as the sites of Execute do.
2. Texts: the Error() text of the error is neutral ("acceptance evidence left FILE changed"); each path adds what happens there — before the review: "the run stops before the review and the push"; at PR creation: "the branch BRANCH is pushed, the run fails, no PR is created"; on the hold: "the task is held, no PR is created". The log line of the hold is written after the push of the hold.
3. At the PR creation sites the result has `BranchPushed` set and the error text names the branch.
4. Tests: a function TestMutationLeftChangedAtEachCallSite — a table with one case for each of the six call sites (the three PR creators through Execute, with the table of TestExecute_EvidenceRunsOnceAcrossReviewAndEveryCreatePRSite as the model; the epic site, the timeout salvage site and the hold by a direct call): the result is a failure or a hold as the path wants, no PR call is made, the text is the text of that path, and on the epic site `git status --porcelain` is empty at the end. Each case fails when the handling at its site is made dead.
5. From the run-it review of PR#6659: with the option empty, the mutation write and the restore write open the file through the workspace root as one step, so that no path part can leave the workspace between the check and the write: open the root once with `os.OpenRoot` (Go 1.25 is the version of the module) and do the write, the temp file and the rename through it. The error text of a stop no longer says "nothing was written through it" when a write was tried. A test: a parent directory swapped for a link to an outside directory at the write seam makes no file outside, for the mutation write and for the restore write.
6. A test that fails when the second path resolution of the restore CHECK is removed: the command replaces the parent directory by a link to an outside directory whose file holds the first content; the run stops and does not go on with the real file still mutated.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run Mutation` passes; paste the output into the PR body.
- [ ] Make the handling dead at the gh CLI creator site -> TestMutationLeftChangedAtEachCallSite fails.
- [ ] Remove the put-back on the epic path -> TestMutationLeftChangedAtEachCallSite fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The clean-up of a failed run without a worktree (`git reset --hard` and `git clean` on the shared checkout; behaviour of today). An issue comment or a label change by the executor at the sites of Execute. The restore check itself (file type and mode). The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6637

### LEG U47 — repo: qf-studio/pilot — title: feat(executor): run_as_user — the preflight no longer refuses the option as incomplete (FOUNDER HOLD: this PR stays a draft)

probe: present qf-studio/pilot func[[:space:]]TestRunAsUserPreflightRefusal\(
probe: present qf-studio/pilot func[[:space:]]TestAutoFixLintRunsAsAgent\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateStartSitesRefuseTheDefaultBuilder\(
probe: present qf-studio/pilot func[[:space:]]TestMutationRestoreFailureStopsTheRun\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserPreflightRefusesGoProxyCredentials\(
probe: present qf-studio/pilot func[[:space:]]TestTokenCommandsRunNoConfigProgram\(
probe: present qf-studio/pilot func[[:space:]]TestGhHelpersLeaveTheWorkspace\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverBranchKeepsTheOldRefAsRecovery\(
probe: present qf-studio/pilot func[[:space:]]TestAgentWorkspaceSweepReadsEveryRef\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunAsUserPreflightPassesWhenAllChecksPass\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. PreflightRunAsUser (`internal/executor/agent_command.go`) refuses the option run_as_user on every host with the message of runAsIncompleteCheck, because the delivery and the gates did not yet run on the agent side. Each leg of that work is merged now (the probes of this issue name their tests). This leg removes the refusal and nothing else. Founder decision 2026-11-19: this pull request is held as a draft; the founder releases it. Do not merge it. Keep the change small: only the items below, only in `agent_command.go` and the test files named below.

## Scope

1. Remove runAsIncompleteRefusal, runAsIncompleteCheck and the case of PreflightRunAsUser that uses them. Each other check stays in its order.
2. Tests: the tests that pin the refusal (TestRunAsUserPreflightRefusal and the test of its default) are replaced by a function TestRunAsUserPreflightPassesWhenAllChecksPass — with each check of the option met in the test fixture the preflight passes; with one check not met at a time (a table: no group, a root user, a missing home, the firewall probe refused) the preflight still refuses with the message of that check. The switch in the test main file that turned the refusal off for each test goes away.
3. The example config (`configs/pilot.example.yaml`): the comment of the option no longer says that the daemon refuses it; it names the host set-up script as the precondition.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunAsUser'` passes; paste the output into the PR body.
- [ ] `git grep -n 'runAsIncomplete' internal/` finds nothing.
- [ ] The pull request is a draft, and its body starts with the line "FOUNDER HOLD: do not merge; the founder releases this change."
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Each other check of the preflight. The read-only token. The host set-up. Any change of behaviour with the option empty.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U51 — repo: qf-studio/pilot — title: feat(github): an installation token with read permissions for one repository — a scoped mint next to the full one

probe: present qf-studio/pilot \)[[:space:]]mint\(
probe: present qf-studio/pilot func[[:space:]]mintGitHubAppToken\(
probe: absent  qf-studio/pilot func[[:space:]]TestMintScopedBodyHasReposAndReadPermissions\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent user has no GitHub credential: its `gh` and its `git fetch` of a private repository do not work. The plan gives each run a token that can only read one repository. The daemon can mint installation tokens of a GitHub App: mint of TokenSource (`internal/adapters/github/apptoken.go`) posts an empty body, so the token carries each permission of the installation for each repository. GitHub accepts a body with `repositories` and `permissions` that narrows the token. Keep the change small: only the items below. The full token and each caller of today stay as they are.

## Scope

1. A method of TokenSource, ScopedToken(ctx, repo string, perms map[string]string) (token string, expires time.Time, err error): it posts the body with `repositories` (the one repository name with no owner) and `permissions` to the access token endpoint of the installation and returns the token with its expiry. It keeps no token in the cache of the full token.
2. A function ReadOnlyPermissions() that gives `contents`, `issues`, `pull_requests` and `metadata`, each `read`. ScopedToken refuses each permission value other than `read` with an error and no request.
3. `cmd/pilot/main.go`, next to the wiring of the git and gh credential providers: when the GitHub App is configured, a provider function for a read token of a repository is set in the executor through a new setter SetAgentReadTokenProvider (a new file `agent_token.go` in `internal/executor`, with the provider type, the setter and a getter). With no App configured the provider is nil. The token of the daemon is never a substitute.
4. Tests with a test HTTP server (the constructor with a base URL): a function TestMintScopedBodyHasReposAndReadPermissions — the request body holds the one repository and the four read permissions; a permission `write` gives an error and no request; an HTTP error of the endpoint gives an error with no token text; the full token cache is not changed by a scoped call; with no App the getter gives nil.

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/github/ ./internal/executor/ -run 'Scoped|AgentReadToken'` passes; paste the output into the PR body.
- [ ] Let ScopedToken accept `contents: write` -> the test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A token file. The gh shim. A git credential helper. A personal access token. The GitHub App set-up on a host.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U54 — repo: qf-studio/pilot — title: fix(github): the read-only token is bound to a repository of the daemon config, the answer of GitHub is checked, and tests prove that the full token is never returned

probe: present qf-studio/pilot \)[[:space:]]ScopedToken\(
probe: present qf-studio/pilot func[[:space:]]agentReadTokenProvider\(
probe: present qf-studio/pilot func[[:space:]]ReadOnlyPermissions\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentReadTokenProviderNeverReturnsTheFullToken\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. ScopedToken of TokenSource (`internal/adapters/github/apptoken.go`) asks GitHub for an installation token with read permissions for one repository. agentReadTokenProvider (`cmd/pilot/main.go`) wraps it for the executor. A later leg writes this token into a file that the agent user reads; the agent is not trusted. So the token must never be wider than read access to the one repository of the run, and the token of the daemon must never be returned in its place. A run-it review found the points below. No code calls the provider yet. Keep the change small: only the items below, only in `apptoken.go`, `cmd/pilot/main.go` and their test files.

- The repository is not bound to anything: ScopedToken cuts the owner from `OWNER/NAME` with no compare, and the provider passes each name through. A call with the name of another owner and the same repository name gives a token; a call with each other name gives a read token for that repository of the installation. A later caller that takes the name from the workspace lets the agent choose the repository.
- No test proves that the provider returns the scoped token: with the full token of the source returned in its place, or after an error of the scoped call, each test passes.
- Only the permission values are checked, not the keys: `administration: read` is sent.
- The answer of GitHub is not read: an answer with `contents: write` or with `repository_selection: all` gives that token with no error.
- An expiry that is zero or in the past, and a token of white space, are accepted. Redirects are followed. The error message of GitHub is copied with no length limit.
- The feature matrix (`.agent/system/FEATURE-MATRIX.md`) marks the feature as done; no run uses it.

## Scope

1. agentReadTokenProvider: it accepts a repository only in the form `OWNER/NAME`, and only when it equals (letter case ignored) a repository of the GitHub config of the daemon (the repository of the adapter and each project repository of the config). Each other value is an error and no request. The doc comment of the provider type says that the argument comes from the config of the project, never from the workspace.
2. ScopedToken: the owner of an `OWNER/NAME` argument is no longer cut in silence: the method takes the name with no owner only, and refuses a text with a slash, an empty text, and each character outside letters, digits, dot, dash and underscore. The provider passes the name part after its own check.
3. ScopedToken refuses each permission key that is not in ReadOnlyPermissions, with no request.
4. ScopedToken reads the answer: `repository_selection` must be `selected`, `repositories` must hold exactly the one name asked for, each permission of the answer must be `read`, the token must be non-empty after a trim, and the expiry must be in the future. Each other answer is an error with no token.
5. The HTTP client of the scoped call follows no redirect. The error message of GitHub in an error value is cut to 200 characters.
6. Tests with a test HTTP server and a key made in the test: a function TestAgentReadTokenProviderNeverReturnsTheFullToken — a token source with a base URL is put where the provider reads it; a full token is made first; the provider call for a configured repository posts the body with the one name and the four read permissions, and returns a token that differs from the full token; a 500 answer gives an error and an empty token; a repository that is not in the config, the name of another owner with a configured repository name, and a name with no owner each give an error and no request. Table tests for ScopedToken: a key outside the four; an answer with `contents: write`; with `repository_selection: all`; with two repositories; with another repository name; an expiry in the past; a token of spaces; a redirect answer: each is an error with no token.
7. The feature matrix line for the read-only token says "partial: the token can be made; no run uses it yet".

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/github/ ./cmd/pilot/ -run 'Scoped|AgentReadToken'` passes; paste the output into the PR body.
- [ ] Return the full token of the source from the provider -> TestAgentReadTokenProviderNeverReturnsTheFullToken fails.
- [ ] Remove the check of the answer -> the table test fails for `contents: write`.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A token file. The gh shim. A git credential helper. A cache for scoped tokens. The GitHub App set-up on a host.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6571

### LEG U55 — repo: qf-studio/pilot — title: fix(github): the read-only token is checked against the full repository name of the answer — owner and name; a strict form for the token text, the expiry and the name

probe: present qf-studio/pilot \)[[:space:]]ScopedToken\(
probe: present qf-studio/pilot func[[:space:]]agentReadTokenProvider\(
probe: present qf-studio/pilot func[[:space:]]TestAgentReadTokenProviderNeverReturnsTheFullToken\(
probe: absent  qf-studio/pilot func[[:space:]]TestScopedTokenChecksTheOwnerOfTheAnswer\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. ScopedToken of TokenSource (`internal/adapters/github/apptoken.go`) asks GitHub for an installation token with read permissions for one repository, and agentReadTokenProvider (`cmd/pilot/main.go`) wraps it. A later leg writes this token into a file that the agent user reads; the agent is not trusted. The provider accepts only a repository of the daemon config, and ScopedToken checks the answer of GitHub. A run-it review found the points below. No code calls the provider yet. Keep the change small: only the items below, only in `apptoken.go`, `cmd/pilot/main.go` and their test files.

- The owner is compared with config text only. The request to GitHub holds the repository name with no owner, and the token is for the installation of the App. With a config that names `OWNER-B/NAME` and an installation of owner A that has a repository `NAME`, the provider returns a read token for the repository of owner A. The answer of GitHub holds the full name of each repository (`full_name`); the code does not read it.
- A token text with a newline, a space or a NUL byte is accepted. The next leg writes the text into a file and into an environment value.
- The answer can hold permission keys that were not asked for, each at `read`: accepted.
- The expiry has no upper limit.
- The text of the answer goes into an error value with control characters as they are.
- The names `.`, `..`, a name with a leading dash and a name of 300 characters are sent to GitHub.
- The mint of the full token follows redirects and sends the App token of the request on to the next host. This is older code.

## Scope

1. ScopedToken takes the owner too: ScopedToken(ctx, owner, name string, perms map[string]string). The answer must hold exactly one repository whose `full_name` equals `OWNER/NAME` (letter case ignored); each other answer is an error with no token. The provider passes the owner and the name of the config entry that matched, not the text of its argument.
2. The token text of the answer must be 20 to 255 characters, each one a printable ASCII character with no space; each other text is an error.
3. The permissions of the answer must be a subset of the keys that were asked for, each at `read`; an empty permissions object is an error.
4. The expiry must be in the future and at most 90 minutes from now.
5. The text of an answer in an error value is cut to 200 characters, and each character outside printable ASCII is replaced by `?`.
6. The owner and the name must each be 1 to 100 characters of letters (ASCII only), digits, dot, dash and underscore, must not start with a dash or a dot, and must not be `.` or `..`.
7. The HTTP client of the full token mint follows no redirect.
8. Tests with a test HTTP server: a function TestScopedTokenChecksTheOwnerOfTheAnswer — an answer with the right name and the `full_name` of another owner is an error with no token; the right `full_name` in another letter case passes; the provider for a config entry `OWNER-B/NAME` with an answer for `OWNER-A/NAME` gives an error. Table tests: a token with a newline, with a space, of 10 characters; an extra permission key at `read`; an empty permissions object; an expiry of two hours; the names `.`, `..`, `-x`, a name of 101 characters, an owner with a non-ASCII letter: each is an error, and each name case sends no request; an answer text with an escape character is shown with `?`; a redirect answer to the full token mint is an error and no request reaches the second server.

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/github/ ./cmd/pilot/ -run 'Scoped|AgentReadToken|Token'` passes; paste the output into the PR body.
- [ ] Remove the `full_name` check -> TestScopedTokenChecksTheOwnerOfTheAnswer fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A token file. The gh shim. A git credential helper. A reload of the config while the daemon runs (the list of the provider is the one of the daemon start). A cache for scoped tokens.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6578

### LEG U52 — repo: qf-studio/pilot — title: feat(executor): run_as_user GitHub access, part 1 — a read-only token file for each run, and the gh shim of the agent reads it

probe: present qf-studio/pilot func[[:space:]]TestScopedTokenChecksTheOwnerOfTheAnswer\(
probe: present qf-studio/pilot func[[:space:]]setupGhGuardShim\(
probe: present qf-studio/pilot func[[:space:]]runGhGuard\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentTokenFileModeAndRefresh\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the agent user gets no GitHub name in its environment, so the `gh` of the agent has no credential. The executor has a provider for a read-only token of one repository (SetAgentReadTokenProvider). The agent calls `gh` through a shim that runs `pilot gh-guard` (runGhGuard in `cmd/pilot/ghguard.go`), which checks the command and runs the real `gh`. The shim directory (setupGhGuardShim in `internal/executor/ghguard_spawn.go`) lives only for one backend process. This leg gives each run a token file and lets the shim use it. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty, or with no provider, nothing changes.

## Scope

1. In `internal/executor/agent_token.go`: a type for the token of a run. At the start of a run with the option set and a provider present, it asks the provider for the repository of the project config of the run (`OWNER/NAME` from the config of the daemon; never a value read from the workspace or from a git remote) and makes a directory for the run (owner the daemon, group the shared group of the agent, mode 0750, through the helper that sets these for the shim directory) and writes the token into a file `github-token` in it (mode 0640, written to a temporary file and renamed). The write is a Go file call only: the token never goes through a shell, an argument list or a script text (its form allows quote characters, `$`, a backslash and a leading dash). The temporary file is created with an exclusive create and no follow of a link; the agent user cannot write the directory. A provider error is logged as a warning, with the error text cut to 200 characters, and the run goes on with no file: with a nil provider or an error there is no token at all, and no other credential is ever a substitute.
2. A refresh: 15 minutes before the expiry of the token the file gets a new token, until the run ends (the expiry is the clock of GitHub: keep that margin). Nothing can revoke a token: the last token stays valid up to one hour after the run, so the refresh never runs after the end of the run. At the end of the run, on each path, the refresh stops and the directory is removed.
3. The environment of the agent process holds the name `PILOT_GH_TOKEN_FILE` with the path of the file (add the name to the gh guard names that pass to the agent user). No token value is ever in an environment or in a log line.
4. runGhGuard: when `PILOT_GH_TOKEN_FILE` is set and the file can be read, the real `gh` gets `GH_TOKEN` with the content of the file (cut of white space); a missing or empty file means no `GH_TOKEN`, as today.
5. Tests: a function TestAgentTokenFileModeAndRefresh with a fake provider and a short expiry — the directory and the file have the modes and the group above; the file content changes before the expiry; after the end of the run the directory is gone and the provider is not called again; a provider error leaves no file and does not fail the run, and the warning holds at most 200 characters of the error; a link with the name of the temporary file in the directory makes the write fail and the target of the link is not written; a token with a quote, `$` and a leading dash is in the file byte for byte; no environment value and no argument of a process of the run holds the token; the start-up line that sets the provider in `cmd/pilot/main.go` has a test. A test of runGhGuard with a fake `gh` that prints its environment: the token of the file arrives as `GH_TOKEN`; with no file no `GH_TOKEN`. With the option empty no directory is made.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentTokenFile|GhGuard'` passes; paste the output into the PR body.
- [ ] Write the file with mode 0644 -> TestAgentTokenFileModeAndRefresh fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

git of the agent side (other leg). The rules of the gh guard. The GitHub App set-up on a host. A token for the quality gates.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U57 — repo: qf-studio/pilot — title: fix(executor): run_as_user token file — left token directories are removed at the daemon start, the repository comes from the project config of the task path, one token directory for a run

probe: present qf-studio/pilot func[[:space:]]startAgentTokenFile\(
probe: present qf-studio/pilot func[[:space:]]TestAgentTokenFileModeAndRefresh\(
probe: present qf-studio/pilot func[[:space:]]CleanupOrphanedWorktrees\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentTokenDirsAreSweptAtStart\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set and a provider for a read-only GitHub token, startAgentTokenFile (`internal/executor/agent_token.go`) makes a directory `pilot-ghtoken-*` in the temporary directory and writes the token of the run into a file that the agent user can read; a refresh keeps it valid, and the end of the run removes the directory. The token is valid up to one hour, and nothing can revoke it. A run-it review found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty, or with no provider, nothing changes.

- A kill of the daemon leaves the directory with a valid token. Nothing removes it at the next start, and the next run of any project can read it (each run uses the same agent user).
- The token is made for each backend process of a run (`internal/executor/backend_claudecode.go`), not once for the run: a run with retries leaves several valid tokens behind.
- The repository that goes to the provider: on the paths with no full repository name of the task (webhook and retry paths) the project is matched by the repository name only. With two configured repositories of the same name under two owners, the run of one project can get the token of the other.
- No test fails when the repository at the call site is a fixed text, when the exclusive create is removed, or when the refresh margin is one minute.
- The refresh tries again at once after a provider error, with no wait. The stop of the refresh waits for a provider call that is in flight. The gh shim (`cmd/pilot/ghguard.go`) reads the token file with no size limit.

## Scope

1. A function sweepAgentTokenDirs in `agent_token.go`: it removes each directory `pilot-ghtoken-*` in the temporary directory that the daemon user owns (checked with Lstat; a link is never followed). The daemon start calls it once, where it removes left worktrees, with the option set; a test seam gives the directory.
2. The token directory belongs to the run: it is made once for a run, before the first backend process, and removed at the end of the run on each path; a retry of the backend inside the run uses the same directory and the same refresh.
3. The repository for the provider is the `OWNER/NAME` of the project config entry whose path is the project path of the task. When the task carries a full repository name and it differs (letter case ignored) from that entry, no token is made and one Warn line names both. A project with no GitHub repository in its config gets no token.
4. The refresh waits after a provider error: 30 seconds, then twice that for each next error, up to 5 minutes. The stop of the refresh cancels a provider call in flight through its context and returns within 2 seconds.
5. The gh shim reads at most 1024 bytes of the token file; a larger file gives no `GH_TOKEN`.
6. Tests: a function TestAgentTokenDirsAreSweptAtStart — a left directory of the daemon user is removed; a directory with that name pattern that is a link to another directory is not followed and the target stays; with the option empty nothing is removed. A run with two backend calls asks the provider once and uses one directory. Two project config entries with the same repository name under two owners: the run of each one gets the token of its own entry; a task with a full repository name of the other owner gets no token and the Warn line. The exclusive create: a file at the temporary name makes the write fail. The refresh margin: with an expiry in 20 minutes the refresh runs within 5 minutes plus a margin of the test, not after 19 minutes. Three provider errors: the waits grow. A stop during a provider call that blocks returns within 3 seconds. A token file of 2000 bytes gives no `GH_TOKEN` in the shim.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run 'AgentToken|GhGuard'` passes; paste the output into the PR body.
- [ ] Pass a fixed repository text at the call site -> the test with two owners fails.
- [ ] Remove the sweep call from the daemon start -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A separate agent user or group for each project (two runs at the same time share the agent user by design; a design decision of the founder). The order of the mode and owner calls of the shim directory helper. git of the agent side. The GitHub App set-up on a host.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6593

### LEG U63 — repo: qf-studio/pilot — title: fix(executor): run_as_user token file — the token provider is installed on each start path, a run with no token says why in one log line, the project path is matched in its canonical form

probe: present qf-studio/pilot func[[:space:]]TestAgentTokenRunWiringThroughExecute\(
probe: present qf-studio/pilot func[[:space:]]TestAgentTokenFileProviderSetAtStartup\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentTokenProviderOnEachStartPath\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, startRunAgentTokenFile (`internal/executor/agent_token.go`) writes a read-only GitHub token of the run into a file for the agent user. It needs two things: the token provider (SetAgentReadTokenProvider) and the repository lookup (SetAgentTokenRepoLookup). PR#6619 put the repository lookup on each place in `cmd/pilot` that builds a Runner. A run-it review found the points below. Each one fails closed (no token), but nothing tells the operator. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- The token provider is installed in runPollingMode only (`cmd/pilot/main.go`). In gateway mode and in `pilot task` the lookup is set and the provider is not: no run gets a token, and no log line says so. TestAgentTokenFileProviderSetAtStartup wants exactly one call in `main.go`.
- The lookup newProjectAgentTokenRepoLookup (`cmd/pilot/main.go`) uses FindProjectByPath of `internal/config/config.go`, which compares the text of the path. A trailing slash, a link or `./` in the task path or in the config entry gives no repository and no token, with no log line. FindProjectByCanonicalPath is next to it.
- The source read test of the lookup passes for a Runner build site that has neither the Navigator lookup nor the token lookup; `pilot github run` (`cmd/pilot/commands.go`) is such a site and runs a task. No test runs the bridge call in `internal/orchestrator/orchestrator.go` that hands the lookup to the runner.

## Scope

1. One function in `cmd/pilot` installs the token provider from the config; each start path that builds a Runner for task runs calls it (the polling mode, the two gateway runners, `pilot task`, `pilot github run`, and the pilot instance of the orchestrator), next to the call that sets the repository lookup. `pilot github run` also sets the repository lookup.
2. startRunAgentTokenFile, with the option set: when it makes no token, one Warn line for the run says why — no provider, no lookup, no project config entry for the path (the line names the path), or no GitHub repository in the entry. With the option empty no line.
3. newProjectAgentTokenRepoLookup uses the canonical form of the path (FindProjectByCanonicalPath or the same rule): a trailing slash, `./` and a link to the project directory find the entry.
4. Tests: a function TestAgentTokenProviderOnEachStartPath in `cmd/pilot` — a source read of each `.go` file of the package that is not a test: each file that builds a Runner (`executor.NewRunner`) and is not on a short list in the test, with a reason for each name, calls both the provider install function and SetAgentTokenRepoLookup. Change TestAgentTokenFileProviderSetAtStartup to the new rule. A test in `internal/orchestrator`: the lookup that the orchestrator gets reaches its runner. In `internal/executor`: with the option set and no provider, a run logs the Warn line once and the backend gets no token file. In `cmd/pilot`: the lookup finds the entry for a task path with a trailing slash and for a link to the project directory.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ ./internal/orchestrator/ -run AgentToken` passes; paste the output into the PR body.
- [ ] Remove the provider install call from one gateway runner -> TestAgentTokenProviderOnEachStartPath fails.
- [ ] Remove the call that hands the lookup to the runner in `orchestrator.go` -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sweep of the token directories and a second daemon process (its own ticket). The token of an epic parent and of its sub-issues. The refresh after the run context is done. A config with `adapters.github.repo` and no project entry. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6619

### LEG U64 — repo: qf-studio/pilot — title: fix(executor): run_as_user token file — the start sweep keeps the token directory of a live run of another daemon process; the refresh makes a lost directory again or stops; three tests that pin the limits

probe: present qf-studio/pilot func[[:space:]]TestAgentTokenDirsAreSweptAtStart\(
probe: present qf-studio/pilot func[[:space:]]TestAgentTokenProviderOnEachStartPath\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentTokenSweepKeepsLiveRun\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the daemon start removes each directory `pilot-ghtoken-*` of the daemon user in the temporary directory (SweepAgentTokenDirs in `internal/executor/agent_token.go`), because a killed daemon leaves a directory with a valid token. A run-it review of PR#6619 found the points below. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- The sweep cannot tell a left directory from the directory of a live run in another daemon process of the same user (the gateway start takes no daemon lock). The live run loses its token with no log line. Each later refresh then asks the provider for a new token and fails to write it, with no end.
- Three changes pass each test: the sweep without the owner check, the size limit of the gh shim at 1999 bytes in place of 1024 (`cmd/pilot/ghguard.go`), the create of the token file without `O_NOFOLLOW`.

## Scope

1. The process that makes a token directory holds a lock on a file in it for the life of the directory (an flock on a file that only the daemon user can write; not the token file). The sweep tries the lock with no wait: a directory whose lock is held is kept, and one Info line names it. A directory with no lock file, or with a free lock, is removed as today.
2. The refresh: when the write fails because the directory is gone, it makes the directory again with the same path, mode and group and writes the token; when that fails too, it logs one Error line and stops the refresh (no more provider calls for this run).
3. Tests: a function TestAgentTokenSweepKeepsLiveRun — a token file of a live run, then the sweep: the directory stays and the token file is readable; after the end of the run a second sweep finds nothing; a directory with the name pattern and no lock is removed. A refresh after the directory was removed makes it again with the same mode. A seam for the owner of a directory: a directory of another owner stays (the test fails when the owner check is removed). The gh shim: a token file of 1024 bytes gives `GH_TOKEN`, one of 1025 bytes gives none. The create: a link at the path of the token file is not followed (the test fails when both `O_EXCL` and `O_NOFOLLOW` are removed; say in a comment which flag does the work).
4. From the run-it review of PR#6640: TestAgentTokenProviderOnEachStartPath (`cmd/pilot`) checks each Runner and not each file — for each call of `executor.NewRunner` in a file of the package that is not a test (with each import name of the executor package), the provider install function and SetAgentTokenRepoLookup are called on that Runner in the same function, or the function is on the list of the test with a reason. A second Runner in `commands.go` with neither call makes the test fail. A test runs the bridge in `internal/pilot/pilot.go` that hands the lookup to the orchestrator.
5. The interactive mode (`cmd/pilot/interactive.go`) runs a task: it installs the provider and sets the lookup as the other start paths do, and it leaves the list of the test.
6. Each Warn line of startRunAgentTokenFile and of the token file code that belongs to a run has the fields `task_id` and `project_path`.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./cmd/pilot/ -run AgentToken` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ -run GhGuard` passes; paste the output into the PR body.
- [ ] Remove the lock check from the sweep -> TestAgentTokenSweepKeepsLiveRun fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A daemon lock for the gateway start. The token of an epic parent and of its sub-issues. The refresh after the run context is done. The sweep with the option empty. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6619

### LEG U53 — repo: qf-studio/pilot — title: feat(executor): run_as_user GitHub access, part 2 — git of the agent side reads the token file through a credential helper, with no token in the environment

probe: present qf-studio/pilot func[[:space:]]TestAgentTokenDirsAreSweptAtStart\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsUserGateEnvHasNoDaemonSecret\(
probe: present qf-studio/pilot func[[:space:]]setAgentEnvIn\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentGitConfigEntriesCarryNoToken\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, each run has a read-only GitHub token in a file that the agent user can read (`PILOT_GH_TOKEN_FILE`). The `gh` shim uses it. git of the agent side has no credential: a fetch of a private repository, and a download of a private module by a quality gate, do not work. The origin of the agent workspace is the GitHub URL with no credentials. This leg lets git read the token file through a credential helper. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty, or with no token file, nothing changes.

## Scope

1. The directory of the run token also gets a helper script `git-credential-pilot` (mode 0750, same owner and group): for the operation `get` it prints `username=x-access-token` and `password=` with the content of the token file; for each other operation it prints nothing. The script holds the path of the file, not the token.
2. The environment of each command of the agent side (setAgentEnvIn) gets, when the run has a token file, the config entries `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_0` and `GIT_CONFIG_VALUE_0` that set the credential helper of git for the host github.com over https (the key `credential.URL.helper` with the URL of that host) to the path of the script. When the environment already has config entries from the code, the new entry is added with the next index. The entries are set by the code after the allow list; an inherited value of these names never passes.
3. The quality gates of the agent side (the gate command builder) get the same entries.
4. Tests: a function TestAgentGitConfigEntriesCarryNoToken — with a token file of a fake value, the environment of an agent command holds the helper entry and no part of the token value; the helper script prints the name and the token for `get` with a host of github.com and nothing for `store` and `erase`; a git command with these entries against a local test server that asks for basic authentication sends the token of the file; with no token file no entry is set; with the option empty no entry is set.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentGitConfig|AgentTokenFile'` passes; paste the output into the PR body.
- [ ] Put the token value into `GIT_CONFIG_VALUE_0` -> TestAgentGitConfigEntriesCarryNoToken fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Hosts other than github.com. A push by the agent (the token can only read). The daemon-side git. The GitHub App set-up on a host.

## Refs

- Navigator TASK-651 in this repo · founder decisions of 11-19 (memory mem-327)

### LEG U68 — repo: qf-studio/pilot — title: fix(executor): run_as_user git credential helper — the helper path is quoted for the shell, git of the agent side never prompts, and tests pin the retry context, the inherited config names and the quoting

probe: present qf-studio/pilot func[[:space:]]TestAgentGitConfigEntriesCarryNoToken\(
probe: present qf-studio/pilot func[[:space:]]TestAgentTokenSweepKeepsLiveRun\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentGitCredentialHelperPathIsQuoted\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-651. With run_as_user set, the directory of the read-only token of a run holds a helper script `git-credential-pilot`, and each agent-side command gets `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_n` and `GIT_CONFIG_VALUE_n` that name the script as the credential helper for github.com (`internal/executor/agent_token.go`, PR#6644). A run-it review found no blocking point; git gives the token to the scoped host only. The minor points below stay. The option is off, and the preflight refuses it on every host today. Keep the change small: only the items below. With the option empty nothing changes.

- git runs the value of `credential.*.helper` through the shell when it is an absolute path. The value is the path with no quoting: a temporary directory of the daemon with a space gives no token and no log line; one with `$(...)` or `;` runs as a command of the agent user.
- The token path inside the script: with the shell quoting removed each test passes.
- The agent side has no `GIT_TERMINAL_PROMPT=0`. git did not wait for input in the runs only because the sudo command has no terminal.
- An inherited `GIT_CONFIG_GLOBAL` or `GIT_CONFIG_PARAMETERS` of the daemon process that passes the allow list fails no test.
- The retry gate (`internal/executor/runner.go`) uses a context that keeps the token file; with `context.Background()` in its place each test passes.

## Scope

1. The helper value in `GIT_CONFIG_VALUE_n` is the path quoted for the shell (the quoting function that the script uses for the token path).
2. With the option set, each agent-side command gets `GIT_TERMINAL_PROMPT=0`, set by the code after the allow list.
3. The names `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM`, `GIT_CONFIG_NOSYSTEM` and `GIT_CONFIG_PARAMETERS` of the daemon process never pass to an agent-side command.
4. Tests: a function TestAgentGitCredentialHelperPathIsQuoted — a temporary directory with a space and one with the text `$(touch MARK)` in its name: real git against a local test server that asks for basic authentication gets the token, and no file MARK exists. The script with the token path not quoted fails a test. An agent command has `GIT_TERMINAL_PROMPT=0` and none of the four config names of item 3 when the test process sets them. The retry gate: its command gets the helper entry of the run (the test fails with `context.Background()` at that place).
5. From the run-it review of PR#6656: the refresh makes a lost token directory again only when nothing is at its path (Lstat gives "not exist") and with a create that fails on an existing name (`os.Mkdir`, no MkdirAll); when a link, a file or a directory of another owner is at the path, it logs one Error line and stops the refresh (no more provider calls for this run). Tests: a link to a directory at the path gets no token file; a file at the path stops the refresh after one Error line.
6. The sweep opens the lock file with `O_NONBLOCK` and `O_NOFOLLOW`, and reads an open error other than "not exist" as "kept" with a Warn line, not as "no lock". A test: a FIFO at the lock path does not block the sweep.
7. The sweep keeps a directory that is younger than 60 seconds (its modification time), so that a directory between its creation and its lock is not removed. A test with a new directory and no lock file.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run AgentGit` passes; paste the output into the PR body.
- [ ] Remove the quoting of the helper value -> TestAgentGitCredentialHelperPathIsQuoted fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The "next index" branch of the config entries (no code-set entry exists today). The sweep of the token directories. The gh shim. The preflight refusal.

## Refs

- Navigator TASK-651 in this repo · review comment of PR#6644

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
- H17 → https://github.com/qf-studio/pilot/issues/6529
- P8 → https://github.com/qf-studio/pilot/issues/6530
- H18 → https://github.com/qf-studio/pilot/issues/6534
- H19 → https://github.com/qf-studio/pilot/issues/6535
- H16 → https://github.com/qf-studio/pilot/issues/6536
- H20 → https://github.com/qf-studio/pilot/issues/6537
- H12 → https://github.com/qf-studio/pilot/issues/6546
- U41 → https://github.com/qf-studio/pilot/issues/6549
- U51 → https://github.com/qf-studio/pilot/issues/6550
- U48 → https://github.com/qf-studio/pilot/issues/6555
- H22 → https://github.com/qf-studio/pilot/issues/6561
- U42 → https://github.com/qf-studio/pilot/issues/6563
- H23 → https://github.com/qf-studio/pilot/issues/6569
- H24 → https://github.com/qf-studio/pilot/issues/6572
- U54 → https://github.com/qf-studio/pilot/issues/6575
- U55 → https://github.com/qf-studio/pilot/issues/6581
- U49 → https://github.com/qf-studio/pilot/issues/6584
- U52 → https://github.com/qf-studio/pilot/issues/6587
- H25 → https://github.com/qf-studio/pilot/issues/6589
- U44 → https://github.com/qf-studio/pilot/issues/6594
- U57 → https://github.com/qf-studio/pilot/issues/6596
- H21 → https://github.com/qf-studio/pilot/issues/6599
- U43 → https://github.com/qf-studio/pilot/issues/6602
- U46 → https://github.com/qf-studio/pilot/issues/6603
- U58 → https://github.com/qf-studio/pilot/issues/6606
- U45 → https://github.com/qf-studio/pilot/issues/6613
- U56 → https://github.com/qf-studio/pilot/issues/6614
- H26 → https://github.com/qf-studio/pilot/issues/6615
- U59 → https://github.com/qf-studio/pilot/issues/6616
- U61 → https://github.com/qf-studio/pilot/issues/6617
- U63 → https://github.com/qf-studio/pilot/issues/6621
- U53 → https://github.com/qf-studio/pilot/issues/6622
- U60 → https://github.com/qf-studio/pilot/issues/6629
- U65 → https://github.com/qf-studio/pilot/issues/6635
- U62 → https://github.com/qf-studio/pilot/issues/6638
- U64 → https://github.com/qf-studio/pilot/issues/6641
- U69 → https://github.com/qf-studio/pilot/issues/6652
- U66 → https://github.com/qf-studio/pilot/issues/6661
- U67 → https://github.com/qf-studio/pilot/issues/6664
- U68 → https://github.com/qf-studio/pilot/issues/6665
- U70 → https://github.com/qf-studio/pilot/issues/6668
- U71 → https://github.com/qf-studio/pilot/issues/6676
