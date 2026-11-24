# TASK-657: Founder box set-up through the infra repository — host script run for the agent user, GitHub App credentials

**Status**: 🚀 **State 2026-11-24 10:30Z: the first dry run on the box is done (workflow run 45375078096 of the infra repository, 10:28Z, on the founder's "keep going"; it changed nothing: no modes record, no new user). The path works end to end: runner → SSM → box → hash check → wrapper → host script.** Result, exit 1 for two causes: (1) as expected, the check of the sudo rules names the wheel rule (one entry of `/etc/sudoers`, run-as users ALL); (2) **new: the step daemon-private fails — eight entries of the daemon are open to other users** (the data directory at mode 755, and `bin`, `go`, `pilot-home`, `repos`, `.pnpm-store` and two left-over `tmp-gh` directories below it). The host script changes no mode itself: it prints one chmod line for each entry and expects an operator to run them (design of TASK-651, leg B3). The plan of 11-23 had no step for that. **Leg A4 [infra#33](https://github.com/qf-studio/aws-infrastructure-pilot/issues/33) added the wrapper mode close-daemon-dirs: PR 34 of the infra repository merged 10:42Z; read by the session at 10:44Z, no blocker, comment on the PR (two notes: with no open directory the mode stops with exit 1 and runs nothing, which fails closed; one test case fails on macOS only, a defect of its mock).** **Each piece for the set-up is now on the main branch of the infra repository; nothing is left for Pilot in phase 1.** New order on the box: dry-run (done) → remove-wheel → apply (stops at the open directories, writes the modes record, changes nothing else) → close-daemon-dirs → dry-run (expect exit 0) → apply. Read-only check of the box at 10:29Z: no running container has a bind mount below the data directory (worktrees are below /tmp), the backup unit runs as the daemon user, so the eight chmods take access only from users that do not use those directories today. Phase 1 legs A1 to A3 are merged (PR 28, 29, 30, 32 there; read by the session, comments on PR 28 and PR 30). **The changes on the box wait for the OK of the infrastructure owner and a window** (Slack 11-23 and 11-24). The infra repository is a project of the daemon again since 11-23 16:53Z (founder word); the old ticket 23 there (AMI refresh) has no label `pilot` until the founder decides. **GitHub App (phase 2): not on the path to the first run** (the first run is in the public Pilot repository). The founder asked on 11-24 whether this App or the App of Pilot Cloud is the right one: see "Which GitHub App" in phase 2; no App is created until the founder decides.
**Created**: 2026-11-23
**Assignee**: Pilot for each leg; the founder for each step that needs the live box, AWS credentials or org owner rights; the infrastructure owner reviews each pull request in the infra repository.

---

## Context

**Problem**:
TASK-651 put each code change for `run_as_user` on main (last: PR#6684, 11-23). The host is not prepared: no agent user, no group, no firewall rules, no sudo rule, and the agent user has no GitHub credential. The steps O2, O3 and O4 of TASK-651 are changes of the box. The rule of the infrastructure owner (11-17, accepted by the founder): no change of the box by hand while Pilot runs on it; a box change goes into the infra repository `qf-studio/aws-infrastructure-pilot`, with a review, and runs in a window with no active task.

The infrastructure owner answered on 11-18 how the host script comes: a pull request against the infra repository, the script in a directory for the founder box, the removal of the daemon user from the group wheel as the first step, run through SSM by a workflow on demand, a dry run first, then the real run.

**Goal**:
The founder box is ready for agent sessions under a second Unix user. The host set-up script runs on the box through a workflow of the infra repository. The daemon gets its GitHub credential from a GitHub App, and the agent gets a read-only token for each run. Then the option is turned on and the first run proves the work on Linux.

---

## Known Pitfalls & Patterns

- **PITFALL** (mem-300, TASK-651): never ask an executor ticket to read or change the live box. → Each leg here writes files in a repository; each run on the box is an operator step.
- **PITFALL** (`box-start-script-exec-kills-tmux-on-stop`): the start script ends in an exec of the daemon; a stop ends the tmux server. → The restart after the set-up is stop, check, a new tmux session (the pilot-aws skill).
- **PITFALL** (`ssm-runs-as-root-cli-reads-empty-ledger`): an SSM command runs as root. → Good for the host script (it needs root); each daemon step uses the login of the daemon user.
- **PITFALL** (pilot-aws skill): a multi-line command with escapes does not survive the SSM JSON. → The workflow sends short commands; the wrapper travels as base64, the large script is fetched on the box and checked by its hash.
- **PITFALL** (`config-env-expansion-eats-dollar-vars-in-commands`): the config loader expands `$` names. → Phase 2 uses `${VAR}` references only where the loader expects them.
- **LEARNING** (`operator-issue-bodies-must-pass-base-presence-check-before-filing`): a backticked path that is not on the main branch of the target repository parks the ticket. → New files are named in plain text in each leg.
- **PITFALL** (mem-334): a record that says "no answer" must come after a search of the message channel.
- **DECISION** (mem-333, founder 11-23): no draft holds; the daemon merges, a review comes after the merge.

---

## Acceptance Criteria

- [ ] The infra repository holds a copy of the host script that is byte-identical to the Pilot commit 7697fcb2 (SHA-256 `f7d052de9cb809156408ea19303314137ce6f10b386312fceb42f2b2f7a1b91e`), a wrapper, and a workflow on demand with the modes dry run, wheel removal and apply.
- [ ] A dry run on the box exits 0 after the wheel removal, and the infrastructure owner has seen its output.
- [ ] The apply run exits 0; the script prints the four `run_as_` config values.
- [ ] The daemon log shows the GitHub App as the token source, and a poll and an autopilot action pass after the one-hour token boundary.
- [ ] One ticket runs with `run_as_user` set: the agent processes belong to the agent user, the quality gates pass, the pull request opens.
- [ ] Each step has a tested way back: the undo mode of the script, the option cleared, the personal token restored.

---

## Route

### Phase 1: The host script runs through the infra repository
- [x] 1. The infra repository gets a copy of the host set-up script, pinned by a hash, and the lint covers the new directory (qf-studio/aws-infrastructure-pilot#25)
- [x] 2. A wrapper removes the daemon user from the group wheel and then starts the script (qf-studio/aws-infrastructure-pilot#26, needs 1)
- [x] 3. A workflow on demand sends the wrapper to the box through SSM, with a dry run mode (qf-studio/aws-infrastructure-pilot#27, needs 2)
- [x] 13. A wrapper mode runs the chmod lines that the host script prints for the open directories of the daemon (qf-studio/aws-infrastructure-pilot#33, needs 3)
- [ ] 4. The infrastructure owner reviews the merged pull requests and gives a window (operator, needs 3)
- [ ] 5. On the box, in a window with no active task: wheel removal, first apply (it only writes the modes record), close the daemon directories, dry run, second apply (operator, needs 4, 13)

### Phase 2: GitHub App and secrets
- [ ] 6. The founder creates the GitHub App and installs it on the repositories that the daemon polls (operator)
- [ ] 7. The infrastructure owner names the Parameter Store path; the App key goes there (operator, needs 6)
- [ ] 8. The start script of the box reads the App key and the seven secrets from the Parameter Store (needs 7; legs not written yet)
- [ ] 9. Full restart of the daemon with the App config; check of the token source, a poll, an action after one hour (operator, needs 8)

### Phase 3: First run with the option on
- [ ] 10. The four `run_as_` values go into the daemon config; full restart with a new tmux server (operator, needs 5)
- [ ] 11. One small ticket in the public Pilot repository runs as the agent user; the session reads the daemon log and the process list (operator, needs 10)
- [ ] 12. The option stays on for the private repositories only after the GitHub App works (operator, needs 9, 11)

### Side tracks
- [ ] The box image gets the override for the group wheel and the sudo line (infrastructure owner, their step 4)
- [ ] Chromium for the UI tests in the box image (answer to the infrastructure owner: system libraries from the package tool, the browser from Playwright)

### Waits for you
- [x] How the infrastructure owner reviews: after the merge and before the first run (founder "go", 11-23)
- [x] The infra repository as a project of the daemon again (done 11-23 16:53Z on the founder's word)
- [x] A message to the infrastructure owner (sent 11-23 on the founder's word; the links of the merged pull requests follow)
- [ ] The old ticket infra#23 (AMI refresh): label `pilot` back, or close it
- [ ] The OK of the infrastructure owner before the first workflow run
- [ ] The word for a read of the start script of the box with each value masked (needed for step 8)
- [ ] Which GitHub App serves the founder box: a private one now, or the Pilot Cloud path later (see phase 2); nothing is created before this decision

---

## Implementation

### Phase 1: host script through the infra repository

**Assignees**: legs A1, A2, A3 = Pilot (the infra repository was a Pilot project from 2026-08-03; **on 11-23 the active daemon config has no project for it**, see the status line). Review = the infrastructure owner. Runs on the box = the founder starts the workflow (or gives the word and the session starts it with `gh workflow run` and reads the output).

**Facts from the research (2026-11-23, infra repository at its main branch):**
- Each workflow there runs on the self-hosted runner with the labels `self-hosted` and `aws-infra-admin` (the management instance, administrator role). No OIDC. So a workflow can send an SSM command to the box with no IAM change.
- `scripts/validate-golden-ami.sh` holds the pattern: send-command with the document AWS-RunShellScript, wait, read the status, print stdout and stderr, fail when the status is not Success.
- The instance ID of the box is already in `stacks/workload/iam-founder-box-operators.yml`.
- CI on a pull request: only the validate workflow, and only for paths below `stacks/`. The lint target of the `Makefile` runs `bash -n` over the top level of the scripts directory only. So a pull request with only new scripts and a workflow gets no check today. Leg A1 widens the lint.
- The host script (`scripts/box/setup-agent-user.sh` in this repository, 1,821 lines, one file, no other file needed): runs as root; flags `--dry-run`, `--undo`, `--confirm-root-path`, `--daemon-user`, `--agent-user`, `--group`, `--data-dir`; exit 0 done, 1 a failed step or a refused sudo check, 2 bad arguments. It never removes a group membership; its check of the sudo rules refuses, before any change, while the daemon user has a rule for all users (the wheel rule). After a refusal in a dry run each step still prints "done (dry-run)" and the exit status is 1: read the exit status.
- The Pilot repository is public, so the box can fetch the script by commit with no credential.

**Order on the box (step 5), each in a window with no active task for the two changes:** dry run (expected: exit 1, the sudo check names the wheel rule) → wheel removal → dry run (expected: exit 0) → apply → record the output in TASK-651 (O4).

### Phase 2: GitHub App and secrets

**Assignees**: App creation and install = the founder (org owner rights). Parameter Store path and the role of the box = the infrastructure owner (their rule of 11-17). Start script change = Pilot leg in the infra repository, after the facts below. Restart and checks = the founder's word, the session runs them.

**Facts:**
- Config block `adapters.github.app`: `app_id`, `installation_id`, `private_key_path`; a partial block fails at load. The key is read from a file only. So the start script writes the key from the Parameter Store to a file with mode 0400 of the daemon user before the daemon starts.
- The boot check of the token warns "could not verify" under App auth (GitHub answers 403 for the user endpoint). The proof is the first poll.
- An ambient `GH_TOKEN` or the personal token export shadows the App path. The start script loses that export in the same change.
- A hot restart keeps the old environment: a full restart is needed.
- With App auth, pull requests and commits of Pilot come from the bot user of the App, not from the founder's account.

**Permissions of the App (repository level), from the API calls of the daemon in `internal/adapters/github/client.go` and the scoped mint in `internal/adapters/github/apptoken.go`:**

| Permission | Level | Why |
|---|---|---|
| Contents | Read and write | branches, pushes, releases, tags |
| Pull requests | Read and write | create, merge, update branch, reviews |
| Issues | Read and write | labels, comments, close |
| Checks | Read and write | check runs |
| Commit statuses | Read and write | statuses |
| Actions | Read and write | job logs, re-run of failed jobs |
| Workflows | Read and write | a push that changes a workflow file (the infra legs of this task do) |
| Metadata | Read | automatic |

The read-only token for each run asks for Contents, Issues, Pull requests and Metadata at read level for one repository; the list above contains them. Organization permission "Projects" only when a project board is in use (not verified). No webhook: the daemon polls.

**Which GitHub App (question of the founder, 11-24):** two different Apps exist in the plans, and neither exists on GitHub yet. (1) The App of Pilot Cloud (console plan TASK-652 in the console repository, planned only): public, each customer installs it, its private key lives only in the control plane, and a box gets hourly tokens with no key on it. (2) An App for the founder box: private to the org, the daemon on the box mints its own tokens, so the private key is a file on the box. The daemon code of today supports only form (2): the key is read from a file on the host. The key of the Pilot Cloud App must never go onto the founder box: it can mint a token for each customer installation, and the box runs agent code. So the choices for the private repositories are: a small private App for the founder box now (recommended when the option must stay on for private repositories; about 15 minutes, no code change), or wait until the token path of the console plan exists and treat the founder box as one more box of it (no key on the box; needs that plan built). With the option off, or for the public Pilot repository, no App is needed.

**Discovery of 11-23:** the org has no GitHub App for Pilot yet (the installation list shows one other App), so step 6 starts from zero. The infra repository has no branch protection and no CI for script paths, so a pull request there merges about one minute after it opens. The first test of phase 3 needs no App: the Pilot repository is public, so the agent user can fetch it without a credential (its `gh` has no token until the App works).

**Not known yet (needs the masked read of the start script):** the current shape of the start script, the instance role of the box, the names of the seven literal keys in the config. The legs B1 (role statement for the Parameter Store path) and B2 (start script in the infra repository with an install workflow) are written after that read.

### Phase 3: first run

The four values that the apply run prints go into the daemon config; the gateway port is 9091 (the script has it fixed). Full restart with a new tmux server: the daemon user gets the new group only in a new login. First ticket: a small docs or test change in the Pilot repository. Checks: the preflight line in the daemon log, the owner of the agent processes, the gates as the agent user (tools on the PATH of the agent user, a cold build cache), the pull request. Way back: clear the option, restart.

---

## Out of Scope

- Rootless Docker and the removal of the docker group (a step of the infrastructure owner's plan).
- A GitHub App installation for each project (new daemon work, named in the founder's message of 11-17).
- The box image changes (infrastructure owner).
- TASK-656 (process clean-up) and the open TASK-651 design decisions.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| How the script reaches the review | a copy in the infra repository; only a hash of a Pilot commit; the Pilot checkout on the box | a copy, pinned by commit and SHA-256 | the infrastructure owner asked for the script in the repository and reviews the bytes; the hash stops a silent edit and ties the copy to the tested commit |
| How the script reaches the box | inline in the SSM command; S3; a fetch on the box from the public Pilot repository by commit with a hash check | fetch by commit with the hash check; the small wrapper inline as base64 | the script is too large for an SSM command; the box role for S3 is unknown; the hash is the same one that the reviewed copy has |
| Wheel removal | inside the apply run; a separate mode | a separate mode | the dry run refuses while the rule exists, so a dry run with exit 0 is possible only after the removal; each change of the box is one visible run |
| Who runs the box steps | Pilot ticket; the founder through the workflow | the founder through the workflow | mem-300; the run needs a window and the admin runner |
| Review of the infra pull requests | a draft; a required review; a review after the merge and before the first run | a review after the merge and before the first run (founder, 11-23) | the daemon merges on its own and the founder ended draft holds; nothing runs at a merge, the workflow starts only on demand |

---

## Verify

```bash
# infra repository, after the three legs
make lint
shasum -a 256 scripts/founder-box/setup-agent-user.sh
# box, through the workflow: mode dry-run, then remove-wheel, then dry-run, then apply
gh workflow run founder-box-setup.yml -R qf-studio/aws-infrastructure-pilot -f mode=dry-run -f confirm=founder-box
```

---

## Done

- [ ] Steps 1 to 5 of the route: the apply run exits 0 and its output is in TASK-651 (O4).
- [ ] Steps 6 to 9: the token source in the daemon log is the GitHub App; O2 and O3 of TASK-651 are checked.
- [ ] Steps 10 and 11: one ticket delivered with the option on; TASK-651 records the first Linux run.

---

## Legs

### LEG A1 — repo: qf-studio/aws-infrastructure-pilot — title: feat(founder-box): the host set-up script of the agent user as a pinned copy; the lint covers script subdirectories

probe: present qf-studio/aws-infrastructure-pilot bash[[:space:]]-n
probe: absent  qf-studio/aws-infrastructure-pilot PILOT_AGENT_EGRESS

## Context

This issue is for this repository (qf-studio/aws-infrastructure-pilot); the code is in the working tree.

The Pilot daemon on the founder box will run its agent sessions as a second Unix user. A host set-up script prepares the box for that: the user, a group, firewall rules, one sudo rule. The script lives in the public repository qf-studio/pilot at the path scripts/box/setup-agent-user.sh, with its own test suite there. The owner of this repository asked for the script in this repository, in a directory for the founder box, so that the bytes that run on the box are reviewed here. This issue adds the copy and nothing that runs it. Two later issues add a wrapper and a workflow.

## Scope

1. A new directory scripts/founder-box with the file setup-agent-user.sh: a byte-identical copy of scripts/box/setup-agent-user.sh of qf-studio/pilot at the commit 7697fcb203cc646d35a36478e49cc523544cb558. Fetch it from the raw file address of that commit. Do not edit the file, not even a header line: its SHA-256 must be f7d052de9cb809156408ea19303314137ce6f10b386312fceb42f2b2f7a1b91e.
2. A new file SHA256SUMS in the same directory with that hash and the file name, in the format that `sha256sum -c` reads.
3. A new file README.md in the same directory: the source repository, path and commit; that the file is never edited here (a new version is a new copy with a new hash); that the tests of the script live in the source repository; the flags of the script in one short list (--dry-run, --undo, --confirm-root-path, --daemon-user, --agent-user, --group, --data-dir) and its exit statuses (0 done, 1 a failed step or a refused check of the sudo rules, 2 bad arguments).
4. The lint target of the `Makefile`: the `bash -n` loop also covers each .sh file one level below the scripts directory, and a new step checks SHA256SUMS in scripts/founder-box with `sha256sum -c` (on macOS `shasum -a 256 -c`; pick the tool that exists).
5. The layout section of `README.md` at the top level gets one line for the new directory.

## Acceptance

- [ ] The SHA-256 of the copied script is the value of item 1; paste the output of the hash command into the PR body.
- [ ] `make lint` passes; paste the output into the PR body.
- [ ] The PR body names the source commit and says that nothing in this PR runs on a host.

## Out of scope

A wrapper. A workflow. Any edit of the copied script. The tests of the script. Any stack template.

## Refs

- Navigator TASK-657 of qf-studio/pilot (plan) · the request of the repository owner of 2026-11-18

### LEG A2 — repo: qf-studio/aws-infrastructure-pilot — title: feat(founder-box): a wrapper for the host set-up script — modes dry-run, remove-wheel, apply and undo; a mock test

probe: absent  qf-studio/aws-infrastructure-pilot gpasswd[[:space:]]-d

## Context

This issue is for this repository (qf-studio/aws-infrastructure-pilot); the code is in the working tree.

Depends on: #{{A1}}

The directory scripts/founder-box holds a pinned copy of the host set-up script of the Pilot agent user (see its README). The script refuses to change anything while the daemon user (ec2-user) has a sudo rule for all users. On the founder box that rule comes from the group wheel. The owner of this repository decided on 2026-11-18: nothing needs ec2-user in wheel, and the removal is the first step of the set-up. The script itself never removes a group membership. This issue adds a wrapper that does the removal as its own mode and starts the script. It runs as root on the box (an SSM command runs as root). A later issue adds the workflow that sends it.

## Scope

1. A new file run.sh in scripts/founder-box, bash, `set -euo pipefail`. Arguments: --mode with one of dry-run, remove-wheel, apply, undo; --script with the path of the host script (default: setup-agent-user.sh next to the wrapper); --daemon-user (default ec2-user). Any other argument: exit 2 with a usage text. Not root: exit 2.
2. Mode remove-wheel: when the daemon user is in the group wheel, run `gpasswd -d` for that user and that group, then print the group list of the user. When the user is not in the group, print that and exit 0. No other change.
3. Mode dry-run: print whether the daemon user is in wheel (when yes, also print that the check of the sudo rules will refuse until the mode remove-wheel ran), then run the script with --dry-run and --confirm-root-path and --daemon-user. Exit with the exit status of the script.
4. Mode apply: when the daemon user is still in wheel, exit 1 with one line that names the mode remove-wheel; start nothing. Else run the script with --confirm-root-path and --daemon-user, and exit with its exit status.
5. Mode undo: run the script with --undo and --daemon-user, and exit with its exit status. The wrapper never puts the user back into wheel.
6. The last line of each mode is "founder-box run: mode=MODE exit=N", so that a reader sees the exit status in the output.
7. A new test file run_test.sh in the same directory, plain bash, no root: it puts mock commands (id, gpasswd, and a mock host script that records its arguments and exits with a set status) first on the PATH and checks each mode, the pass-through of the exit statuses 0 and 1, the refusal of apply while the user is in wheel, and the refusal of an unknown mode. The wrapper needs a way to skip the root check under the test (an environment variable that the README names as test-only).
8. The lint target of the `Makefile` runs the new test. The README of the directory gets the modes and the order of use: dry-run, remove-wheel, dry-run, apply.

## Acceptance

- [ ] The new test passes; paste its output into the PR body.
- [ ] `make lint` passes; paste the output into the PR body.
- [ ] The PR body lists each command that the wrapper can run as root on a host.

## Out of scope

Any edit of the copied host script. A workflow. A change of a stack template. A step that puts a user into a group.

## Refs

- Navigator TASK-657 of qf-studio/pilot (plan) · the decision of the repository owner of 2026-11-18

### LEG A3 — repo: qf-studio/aws-infrastructure-pilot — title: feat(founder-box): a workflow on demand that runs the set-up wrapper on the founder box through SSM

probe: present qf-studio/aws-infrastructure-pilot aws[[:space:]]ssm[[:space:]]send-command
probe: absent  qf-studio/aws-infrastructure-pilot founder-box-setup

## Context

This issue is for this repository (qf-studio/aws-infrastructure-pilot); the code is in the working tree.

Depends on: #{{A2}}

The directory scripts/founder-box holds a pinned copy of the host set-up script of the Pilot agent user and a wrapper run.sh with the modes dry-run, remove-wheel, apply and undo (see the README of the directory). The owner of this repository asked for a run through SSM by a workflow on demand: a dry run first, then the real run in a window with no active task. `scripts/validate-golden-ami.sh` already shows the SSM pattern of this repository: send-command with the document AWS-RunShellScript, wait, read the status, print stdout and stderr, fail when the status is not Success. Each workflow here runs on the self-hosted runner with the labels self-hosted and aws-infra-admin. The instance ID of the founder box is in `stacks/workload/iam-founder-box-operators.yml`.

## Scope

1. A new workflow file founder-box-setup.yml in the workflows directory. Trigger: workflow_dispatch only. Inputs: mode (a choice: dry-run, remove-wheel, apply, undo; default dry-run) and confirm (a string; the job fails at once unless it is "founder-box"). Runner labels as in the other workflows. No schedule, no push trigger.
2. Step one checks SHA256SUMS of scripts/founder-box (the copy in the checkout is the reviewed one).
3. A new script in scripts/founder-box, send.sh, that the workflow calls with the mode and the instance ID. It builds a short list of shell commands for the box and sends it with send-command:
   - make a new temporary directory with mktemp -d, mode 0700;
   - write the wrapper there from a base64 text (the wrapper is small; base64 keeps quotes and line ends safe in the SSM JSON);
   - fetch the host script with curl from the raw file address of qf-studio/pilot at the commit that the README of the directory names, into the same directory;
   - check the fetched file with sha256sum against the hash of SHA256SUMS; a mismatch ends the run with exit 1 before the wrapper starts;
   - run the wrapper with bash, the mode and the path of the fetched script;
   - remove the temporary directory in each case, and exit with the exit status of the wrapper.
4. send.sh then waits for the command, prints stdout and stderr of the invocation, and exits non-zero when the SSM status is not Success. The output limit of get-command-invocation is 24,000 characters: when the output is cut, print one line that says so and names the last line "founder-box run: mode=... exit=..." as the result to trust.
5. The instance ID is one variable at the top of the workflow file, with a comment that names the stack template where the same ID lives.
6. A test file send_test.sh in the same directory, plain bash: a mock aws command on the PATH records the parameters; the test checks that the command list has the hash check before the wrapper start, that the mode reaches the wrapper, that a status other than Success gives a non-zero exit, and that the JSON of the parameters parses (python3 -c with json.load). The lint target of the `Makefile` runs it.
7. The README of the directory gets a section "How to run": the four modes, the order (dry-run, remove-wheel, dry-run, apply), the rule "apply and remove-wheel only in a window with no active Pilot task", and where to read the result.

## Acceptance

- [ ] The new test passes; paste its output into the PR body.
- [ ] `make lint` passes (it lints the workflow YAML too); paste the output into the PR body.
- [ ] The PR body says plainly that the workflow was not started and that no command reached a host.

## Out of scope

A start of the workflow. Any call of the real aws command in a test. A change of an IAM role or a stack template. A schedule. The daemon config of Pilot and its restart.

## Refs

- Navigator TASK-657 of qf-studio/pilot (plan) · the request of the repository owner of 2026-11-18

### LEG A4 — repo: qf-studio/aws-infrastructure-pilot — title: feat(founder-box): wrapper mode close-daemon-dirs — runs the chmod lines that the host script prints, after a strict check of each line

probe: present qf-studio/aws-infrastructure-pilot gpasswd[[:space:]]-d
probe: absent  qf-studio/aws-infrastructure-pilot close-daemon-dirs

## Context

This issue is for this repository (qf-studio/aws-infrastructure-pilot); the code is in the working tree.

The directory scripts/founder-box holds the wrapper `scripts/founder-box/run.sh` (modes dry-run, remove-wheel, apply, undo), the sender `scripts/founder-box/send.sh` and the workflow `.github/workflows/founder-box-setup.yml`. The first dry run on the founder box (2026-11-24) showed a step that the wrapper cannot do yet. The first step of the host script (daemon-private) changes no mode itself. When a directory of the daemon is open to other users it prints, on stderr, one line for each entry and fails. Each such line ends with a chmod command in exactly this form:

runuser -u ec2-user -- chmod o-rwx '/var/lib/pilot/repos'

The mode is go-rw, o-rwx or go-rwx. The path is the data directory (/var/lib/pilot) or an entry in it, in single quotes. In an apply run the host script also writes the modes it found to a record file of root (/root/.setup-agent-user.modes, lines of the form MODE, a tab, PATH), so that its undo mode can print the commands that put them back; a dry run writes no record. After the operator ran each line, the next apply passes the step. On the founder box eight lines are printed. This issue adds a wrapper mode that runs those lines, so that the step goes through the same workflow as each other change.

## Scope

1. `scripts/founder-box/run.sh` gets a fifth mode, close-daemon-dirs, and two new optional arguments for it: --data-dir (default /var/lib/pilot) and --modes-record (default /root/.setup-agent-user.modes).
2. The mode refuses (exit 1, one line, nothing started) while the daemon user is in the group wheel, as the mode apply does.
3. The mode runs the host script with --dry-run, --confirm-root-path and --daemon-user, and reads its stdout and stderr together. It prints that output. From it, it takes each line that contains the text "runuser -u".
4. Each taken line must end with: runuser -u DAEMON_USER -- chmod MODE 'PATH' — with DAEMON_USER equal to the --daemon-user value, MODE one of go-rw, o-rwx, go-rwx, and PATH equal to the data directory or strictly below it, made only of letters, digits, dot, underscore, hyphen and slash, with no empty part and no part "." or "..". When one taken line does not have this form, the mode prints the line, runs no chmod at all, and ends with exit 1.
5. The record check: when the record file does not exist, or one PATH of a taken line is not the second field of a line of the record, the mode runs no chmod and ends with exit 1 and a message that names the mode apply ("run the mode apply once first: it writes the record of the modes and changes nothing while a directory is open"). This keeps the undo of the host script able to put each mode back.
6. Then the mode runs each command as an argument list — runuser, -u, the user, --, chmod, the mode, the path — never through eval or a shell string. It prints each command before it runs it. A failed command ends the mode with exit 1 after it printed which commands ran.
7. With no taken line the mode prints "no directory of the daemon is open: nothing to close" and ends with exit 0.
8. The last line is "founder-box run: mode=close-daemon-dirs exit=N", as for each other mode.
9. `scripts/founder-box/send.sh` and the workflow file accept the new mode (the list of modes in the usage text, in the case statements and in the choice input).
10. `scripts/founder-box/run_test.sh` gets cases for the new mode, with a mock runuser that records its arguments and a mock host script that prints lines: the eight-line case runs eight commands with the right arguments; a line with a path outside the data directory, a path with "..", a path with a space or a quote, a wrong user and a wrong mode each give exit 1 with zero commands run; a missing record and a record without one of the paths give exit 1 with zero commands run; the wheel refusal; the empty case. `scripts/founder-box/send_test.sh` gets one case for the new mode.
11. `scripts/founder-box/README.md`: the new mode, and the new order of use: dry-run, remove-wheel, apply (it stops at the open directories, writes the record and changes nothing else), close-daemon-dirs, dry-run, apply.

## Acceptance

- [ ] Both test files pass; paste their output into the PR body.
- [ ] `make lint` passes; paste the output into the PR body.
- [ ] The PR body lists each command that the new mode can run as root on a host, and says that no workflow was started.

## Out of scope

Any edit of the copied host script. A chmod that the host script did not print. A change of the modes record. The start of the workflow. Any stack template.

## Refs

- Navigator TASK-657 of qf-studio/pilot (plan) · the first dry run on the founder box of 2026-11-24

## Refs

- TASK-651 (`.agent/tasks/TASK-651-agent-uid-isolation.md`): operator steps O2, O3, O4; the box facts of 11-18; the answer of the infrastructure owner of 11-18.
- TASK-461 (`.agent/tasks/TASK-461-app-cutover-sdk-token-wiring.md`): operator steps of the GitHub App cutover.
- Design: `.agent/system/agent-isolation-design.md`.
- Dispatch SOP: `.agent/sops/development/dispatch-task-legs-to-pilot.md`. Infra repository checkout for the dispatch script: a clone outside this repository (`--checkout qf-studio/aws-infrastructure-pilot=PATH`).
- A1 → https://github.com/qf-studio/aws-infrastructure-pilot/issues/25
- A2 → https://github.com/qf-studio/aws-infrastructure-pilot/issues/26
- A3 → https://github.com/qf-studio/aws-infrastructure-pilot/issues/27
- A4 → https://github.com/qf-studio/aws-infrastructure-pilot/issues/33

---

**Last Updated**: 2026-11-23
