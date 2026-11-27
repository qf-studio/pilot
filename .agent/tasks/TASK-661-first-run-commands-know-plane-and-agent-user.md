# TASK-661: First-run commands — the Telegram check stops its own conflict, and `pilot onboard`, `pilot doctor` and the config check know Plane and the agent user option

**Status**: 🚀 **State 2026-11-27 19:15Z: the gateway port flag of the host script merged ([pilot#6748](https://github.com/qf-studio/pilot/issues/6748) → PR#6759, 18:55Z; reviewed: 923 script tests pass, a mutation fails four of them) and the two minors of the Plane doctor rows merged ([pilot#6754](https://github.com/qf-studio/pilot/issues/6754) → PR#6757, 18:31Z; reviewed). In the queue: Plane in `pilot onboard` ([pilot#6755](https://github.com/qf-studio/pilot/issues/6755)).** **State 2026-11-27 18:21Z: the daemon's review of PR#6750 found two minors that the session's review did not find (a project list of only blank entries passes the config row; a key of only spaces gives two rows); its ticket [pilot#6754](https://github.com/qf-studio/pilot/issues/6754) has the open PR#6757. In work: the gateway port flag of the host script ([pilot#6748](https://github.com/qf-studio/pilot/issues/6748)); in the queue: Plane in `pilot onboard` ([pilot#6755](https://github.com/qf-studio/pilot/issues/6755)).** **State 2026-11-27 17:56Z: the Plane rows of `pilot doctor` merged ([pilot#6745](https://github.com/qf-studio/pilot/issues/6745) → PR#6750, 17:53Z; reviewed after the merge with two mutations, no finding). Next ticket written: `pilot onboard` offers Plane and tests the key (F6).** **State 2026-11-27 17:25Z: the documentation page of the agent user option merged ([pilot#6736](https://github.com/qf-studio/pilot/issues/6736) → PR#6744, 17:23Z; reviewed against the code, no finding in the page). The page states a product gap: the host script rejects the fixed TCP port 9091, and the default gateway port of the config is 9090, so a host with the default port fails the startup check of the option. A ticket (F5) gives the script a flag for the port.** **State 2026-11-27 17:15Z: the Plane key check merged ([pilot#6735](https://github.com/qf-studio/pilot/issues/6735) → PR#6743, 17:12Z; reviewed after the merge with two mutations, no finding). Next ticket written: `pilot doctor` shows the Plane key check and the errors of the Plane block (F4).** **State 2026-11-27 16:55Z: the Telegram check merged ([pilot#6734](https://github.com/qf-studio/pilot/issues/6734) → PR#6738, 16:43Z; reviewed after the merge with two mutations, no finding; CheckSingleton has no change). It is not released yet; after the release, read the box log for three days.** Earlier state: **F1, F2, F3 DISPATCHED 2026-11-27 16:10Z (`pilot` + `no-decompose`): the Telegram check asks for the bot identity → [pilot#6734](https://github.com/qf-studio/pilot/issues/6734); the daemon checks the Plane key → [pilot#6735](https://github.com/qf-studio/pilot/issues/6735); the documentation page of the agent user option → [pilot#6736](https://github.com/qf-studio/pilot/issues/6736).** Held, written when the step before is merged: the Plane row of `pilot doctor`, the config check of the Plane block, Plane in `pilot onboard` (two steps), the agent user option in `pilot doctor` (two steps).
**Created**: 2026-11-27
**Assignee**: Pilot (one ticket at a time)

---

## Context

**Problem**:
Release v2.328.0 holds two features that a user can set up only by hand in the YAML file, and one false alarm of the daemon. Read on 2026-11-27 (code at v2.328.0):

- **Telegram.** The daemon log of the founder box has 259 lines "Conflict: terminated by other getUpdates request … (code: 409)" since 10-18, about two events each day, each a pair of lines 4 seconds apart. Telegram lets one client at a time ask for updates of a bot. The poller of the daemon holds a 30-second getUpdates request (`internal/adapters/telegram/handler.go`). The method Verify of the Telegram client (`internal/adapters/telegram/client.go`) calls getUpdates too. Verify runs at the start, each 15 minutes in the health loop, on each readiness request and in `pilot doctor` (`cmd/pilot/adapter_preflight.go`, `internal/health/health.go`). The wizard does the same in validateTelegramConn (`cmd/pilot/onboard_notify.go`). So the daemon collides with itself, and the log says "this adapter will silently fail until fixed" for an adapter that works. The client has a method GetMe that no check uses. One place needs the 409 on purpose: CheckSingleton, which decides at the start if a second bot instance runs. The cause is read from the code and the log times; it is not reproduced.
- **Plane.** `pilot onboard` (the main wizard; `pilot setup` is deprecated) offers GitHub, Linear, Jira, GitLab, Azure DevOps and Asana as ticket sources, not Plane (`cmd/pilot/onboard_ticket.go`). The function buildAdapterVerifiers has no Plane entry, so no start check, no health loop and no `pilot doctor` row tests the Plane key. The config check accepts an enabled Plane block with an empty key, and a project entry whose Plane project ID is not in the polled list (`internal/config/config.go` checks only an empty ID and a double use).
- **The agent user option** (`run_as_user`, TASK-651). Its preflight (PreflightRunAsUser in `internal/executor/agent_command.go`) keeps its result in package state and writes a log line; `pilot doctor` shows nothing for it. No page under the public documentation names the option or the host set-up script `scripts/box/setup-agent-user.sh`; the keys are comments in `configs/pilot.example.yaml`.
- **A side finding.** The wizard does not test the credential of Linear, Jira, GitLab, Azure DevOps and Asana: each check accepts any text that is not empty.

**Goal**:
The daemon check of Telegram makes no conflict. A user can set up Plane in the wizard, and the daemon, `pilot doctor` and the config check say when the Plane set-up is wrong. `pilot doctor` shows the state of the agent user option, and the documentation says how to turn it on.

---

## Known Pitfalls & Patterns

- **PITFALL** (merged-feature-dead-callback-not-bridged-onprcreated, mem-352): find the production caller before a change builds on a function. Each ticket of this task names the caller that it serves.
- **LEARNING** (mem-021): a client method that reads pages can hang a test server that gives the full list for each request. The Plane check asks for one project only and its test server answers one page.
- **PITFALL** (this research): the 409 of Telegram is the signal of CheckSingleton. No ticket of this task changes that function.

---

## Acceptance Criteria

- [ ] The daemon log of the box has no new 409 line of the Telegram check for three days after the release with step 1.
- [ ] `pilot doctor` shows a row for the Plane key when Plane is on, and a row for the agent user option when the option is set.
- [ ] `pilot onboard` offers Plane and writes a block that the config check accepts.
- [ ] A public documentation page says how to turn on the agent user option.

---

## Route

### Phase 1: Checks (three tickets with no dependency)
- [x] 1. The Telegram check asks for the bot identity, not for updates; the wizard shows the real bot name (F1) — pilot#6734, merged 11-27 16:43Z
- [x] 2. The daemon checks the Plane key at the start and in the health loop (F2) — pilot#6735, merged 11-27 17:12Z
- [x] 3. A documentation page for the agent user option and its host script (F3) — pilot#6736, merged 11-27 17:23Z

### Phase 2: Plane in the first-run commands
- [x] 4. `pilot doctor` shows a row for the Plane key and a row for the errors of the Plane block: an empty key, slug or project list, and a project entry whose Plane project is not polled (F4, needs 2) — pilot#6745, merged 11-27 17:53Z
- [x] 5. The errors of the Plane block — joined with step 4 on 11-27: they are a row of `pilot doctor`, not a refusal at the start of the daemon. Reason: a refusal stops a daemon whose config works today with a half-filled Plane block.
- [ ] 6. `pilot onboard` offers Plane as a ticket source and tests the key (F6, needs 2, 4)
- [ ] 7. `pilot onboard` asks which Plane project belongs to the repository (needs 6)

### Phase 3: The agent user option in `pilot doctor`
- [x] 7a. The host script takes the gateway port as a flag; the firewall rule, the boot unit and the printed config follow it (F5) — pilot#6748, merged 11-27 18:55Z
- [ ] 8. The preflight of the option becomes a function with a result and no package state (needs a read of its production caller first)
- [ ] 9. `pilot doctor` shows a row for the option (needs 8)

### Side tracks
- [ ] The wizard tests the credential of Linear, Jira, GitLab, Azure DevOps and Asana (one ticket for each; not planned yet)
- [ ] The hint of `pilot doctor` names `pilot onboard`, not the deprecated `pilot setup` — part of F4

### Waits for you
- Nothing for phase 1.

---

## Implementation

### Phase 1 — F1, F2, F3
The three tickets touch different files: the Telegram client and the wizard step of Telegram; a new verifier of the Plane adapter and the verifier list; one new documentation page.

### Phase 2 and 3
Each ticket is written when the step before it is merged, so that it names shipped symbols. Step 8 starts with a read: the research found no call of PreflightRunAsUser outside its own package by a text search, and the box runs the preflight, so the caller is reached in another way.

---

## Out of Scope

- The SDK Telegram client (it has no GetMe; the daemon polls with the internal handler unless the SDK bridge is set).
- CheckSingleton and the start decision for a second bot instance.
- The agent user option in `pilot onboard` (after step 9, as a new decision).
- A live Plane server test.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Telegram check call | getUpdates with a retry on 409; getMe | getMe | It tests the token, makes no conflict, and gives the bot name |
| Plane check call | A new SDK method; ListLabels of one project | ListLabels of the first polled project | It tests the key, the slug and the project ID in one call; the poller needs the labels in any case; no SDK release |
| Order for Plane | Wizard first; checks first | Checks first | The wizard then writes a block that the check accepts, and it can call the same verifier |
| Agent user in `pilot doctor` | Call the preflight as it is; a function with a result | A function with a result | The preflight of today changes package state and logs an error |

---

## Verify

```bash
go test -race -count=1 ./internal/adapters/telegram/ -run 'TestClient_Verify'
go test -race -count=1 ./internal/adapters/plane/ -run 'TestVerifier'
go test -race -count=1 ./cmd/pilot/ -run 'TestBuildAdapterVerifiers|TestValidateTelegramConn'
```

---

## Done

- [ ] Steps 1 to 9 are merged and released.
- [ ] The box log is read three days after the release with step 1.

---

## Legs

### LEG F1 — repo: qf-studio/pilot — title: fix(telegram): the token check asks for the bot identity — no more 409 conflict with the poller of the same daemon; the wizard shows the real bot name

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]GetMe\(ctx[[:space:]]context\.Context\)
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]CheckSingleton\(
probe: present qf-studio/pilot func[[:space:]]validateTelegramConn\(token[[:space:]]string\)
probe: absent  qf-studio/pilot func[[:space:]]TestClient_Verify_UsesGetMe

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. Telegram lets one client at a time ask for the updates of a bot; a second getUpdates request gets the answer 409 "Conflict: terminated by other getUpdates request". The poller of the daemon holds a 30-second getUpdates request (`internal/adapters/telegram/handler.go`). The method Verify of the client in `internal/adapters/telegram/client.go` calls GetUpdates with offset 0 and timeout 0. Verify is the token check of the daemon: `cmd/pilot/adapter_preflight.go` puts the Telegram client into the verifier list, and that list runs at the start, each 15 minutes in the health loop, and on each readiness request; `internal/health/health.go` runs it for `pilot doctor`. So the check and the poller of the same daemon collide. The daemon log of a production box shows about two such events each day, each with the error line "adapter verification failed at startup — this adapter will silently fail until fixed" for an adapter that works.

The wizard has the same call: validateTelegramConn in `cmd/pilot/onboard_notify.go` calls GetUpdates and returns the fixed text pilot_bot as the bot name, with the comment that production would call getMe.

The client already has the method GetMe (it calls the getMe method of the Bot API and returns a User). No check uses it.

One function needs the 409 on purpose and must NOT change: CheckSingleton in the same client file. It makes its own getUpdates request, and `cmd/pilot/main.go` uses its result at the start to decide if another bot instance runs.

## Scope

1. `internal/adapters/telegram/client.go`: Verify calls GetMe in place of GetUpdates. An error keeps the prefix text "telegram token invalid:" and wraps the cause as today. Correct the comment of Verify. Change nothing in CheckSingleton, GetUpdates or GetMe, except what a compile error makes necessary.
2. `cmd/pilot/onboard_notify.go`: validateTelegramConn calls GetMe and returns the Username of the result. When the Username is empty, it returns the FirstName; when both are empty, the text of today. To make the function testable against a test server, give it a way to take the base URL (for example a package variable that holds the constructor, or a second function that takes the client); the call site of the wizard keeps its behaviour.
3. Tests in the test file of the client package (`internal/adapters/telegram/verify_test.go` or the client test file, where Verify tests fit):
   - TestClient_Verify_UsesGetMe: a test server counts the requests by path. After Verify the count for a path that ends with getMe is 1 and the count for a path that ends with getUpdates is 0.
   - TestClient_Verify_InvalidToken: the server answers 401 for getMe; Verify returns an error whose text holds "telegram token invalid".
   - TestClient_Verify_NoConflictWithPoller: the server answers 409 with the text "Conflict: terminated by other getUpdates request" for each getUpdates request and 200 for getMe; Verify returns nil.
   - Use a fake token of the style in `internal/testutil/tokens.go`; no realistic token pattern.
4. A test in the directory of `cmd/pilot/onboard_notify.go`: TestValidateTelegramConn_ReturnsBotUsername (the server answers getMe with a user name; the function returns that name and makes no getUpdates request) and a case for a token with no colon (the format error of today).
5. Existing tests that expect a getUpdates request from Verify change to the new call; name each in the PR body.

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/telegram/ -run 'TestClient_Verify'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestValidateTelegramConn'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/health/...` passes; paste the output into the PR body.
- [ ] Put the GetUpdates call back into Verify -> TestClient_Verify_UsesGetMe fails
- [ ] Put the GetUpdates call back into validateTelegramConn -> TestValidateTelegramConn_ReturnsBotUsername fails
- [ ] The PR body says that CheckSingleton has no change, with the diff stat of the client file.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

CheckSingleton and the start decision for a second bot instance. The poller. The SDK Telegram client and the SDK bridge. Other adapters. The check interval.

## Refs

- Navigator TASK-661 in this repo

### LEG F2 — repo: qf-studio/pilot — title: feat(plane): the daemon checks the Plane key at the start and in the health loop — a wrong key, slug or project ID is an alert, not a silent poller

probe: present qf-studio/pilot func[[:space:]]buildAdapterVerifiers\(cfg[[:space:]]\*config\.Config\)
probe: present qf-studio/pilot func[[:space:]]planeSDKConfig\(cfg[[:space:]]\*plane\.Config\)
probe: present qf-studio/pilot type[[:space:]]Verifiable[[:space:]]interface
probe: absent  qf-studio/pilot func[[:space:]]NewVerifier\(cfg[[:space:]]\*Config

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. The function buildAdapterVerifiers in `cmd/pilot/adapter_preflight.go` builds one verifier for each enabled adapter with a credential: Telegram, Slack, GitHub, Linear, Jira, GitLab, Azure DevOps, Asana. The list runs at the start of the daemon, each 15 minutes in the health loop (with an alert on a failure), and on each readiness request. The interface is Verifiable in `internal/health/verify/verify.go`: a Name method and a Verify method with a context. Each adapter has a small file for it, for example `internal/adapters/asana/verify.go`.

Plane has no entry. A daemon with a wrong Plane key, a wrong workspace slug or a wrong project ID starts with no error; the poller then logs a warning each interval and picks up nothing.

The production poll path uses the Plane client of the SDK (`github.com/qf-studio/studio-sdk`, package sdk/integrations/plane, pinned in `go.mod`). `cmd/pilot/poller_plane.go` builds it; planeSDKConfig maps the config of `internal/adapters/plane/types.go` to the SDK config. The SDK client has ListLabels(ctx, workspaceSlug, projectID); it has no ping method. One ListLabels call tests the key, the slug and the project ID together, and the poller reads the labels in any case.

## Scope

1. A new file in the directory of `internal/adapters/plane/types.go` (name it verify.go) with a type Verifier and a constructor NewVerifier that takes the Plane config of that package (and an option or a parameter for the HTTP client, so that a test can point it at a test server). Name returns the text plane. Verify builds the SDK client with the base URL and the key of the config, and calls ListLabels for the workspace slug and the FIRST entry of the project ID list. It returns nil on success. On an error it returns an error that starts with the text "plane check failed:" and names the workspace slug and the project ID. A caller must NOT be able to get the key from that error: the text, each step of errors.Unwrap, and the output with the verbs %v, %+v and %#v hold no key (the SDK masks its own errors since v0.39.3; do not add the key to the new text).
2. Verify with an empty project list returns an error that says that no project is set; it makes no request.
3. `cmd/pilot/adapter_preflight.go`: buildAdapterVerifiers adds the Plane verifier when the Plane block is enabled and the key is not empty. Follow the form of the Asana entry.
4. Tests, new file beside verify.go (verify_test.go): TestVerifier_OK (a test server answers the labels request with one page; assert the request path holds the slug and the first project ID, and the key header is set); TestVerifier_Unauthorized (401; the error holds "plane check failed" and the project ID); TestVerifier_NoProjects; TestVerifier_ErrorHasNoKey (a marker key of the style in `internal/testutil/tokens.go`; use AssertNoCanaryLeak of `internal/testutil/canary.go` on the error texts for a 401 answer and for a transport failure with FailingTransport).
5. `cmd/pilot/adapter_preflight_test.go`: TestBuildAdapterVerifiers gets a case with Plane on (the list holds a verifier with the name plane) and a case with Plane on and an empty key (no Plane verifier).

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/plane/ -run 'TestVerifier'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestBuildAdapterVerifiers'` passes; paste the output into the PR body.
- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] Remove the Plane entry from buildAdapterVerifiers -> TestBuildAdapterVerifiers fails
- [ ] Put the key into the text of the returned error -> TestVerifier_ErrorHasNoKey fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A row in `pilot doctor` (next ticket). The wizard. The config check. The SDK. A check of each project of the list (one call for each interval is the limit). The webhook secret.

## Refs

- Navigator TASK-661 in this repo

### LEG F3 — repo: qf-studio/pilot — title: docs(deployment): a page for the agent user option — what run_as_user does, the host set-up script, the config keys, the limits

probe: present qf-studio/pilot run_as_user:[[:space:]]""
probe: present qf-studio/pilot func[[:space:]]PreflightRunAsUser\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. The option run_as_user of the executor starts each model process (the coding agent and the quality gates) as a second Unix user that holds no keys. The daemon keeps the credentials and does all work with the remote: it fetches, hands the base commit to the agent as a bundle, takes the commits back as a pack, runs the secret gate, pushes, and opens the pull request. The code is in `internal/executor/agent_command.go` (the spawn through sudo, the environment allow list, the preflight PreflightRunAsUser), `internal/executor/agent_workspace.go` (the workspace of the agent user), `internal/executor/handover.go` (the hand-over of the commits) and `internal/executor/agent_token.go` (an optional read-only GitHub token for the agent, which today comes only from a GitHub App). The host set-up is the script `scripts/box/setup-agent-user.sh`. The config keys are comments in `configs/pilot.example.yaml` (search for run_as_user there).

No page of the public documentation site (`docs/content/`, Nextra) names the option or the script. This repository is public: the page holds no customer name, no host name, no account number and no instance ID.

## Scope

1. A new page under the directory of `docs/content/deployment/` (name it agent-isolation.mdx) and its entry in the meta file of that directory, in the form that the other pages of the directory use. Read two pages of the directory first and keep their style.
2. The page content, each statement taken from the code or from the example config, not from memory:
   - What the option does and what it protects (the agent process has no access to the keys of the daemon).
   - What the daemon does and what the agent does in a run (fetch, bundle, commits, hand-over, secret gate, push, pull request).
   - The host set-up: what `scripts/box/setup-agent-user.sh` makes (read the script; list the user, the directories, the sudo rule, the firewall rule if the script has one), and how to run it.
   - Each config key of the option with its default, from `configs/pilot.example.yaml`.
   - The preflight: which checks run at the start, and what the daemon does when one fails (each model spawn is refused; quote the log text "run_as_user preflight failed").
   - Limits, stated plainly: the agent has no GitHub credential unless a GitHub App gives a read-only token, so inside a session the gh commands and a git fetch fail for a private repository; private dependencies of a quality gate must be reachable without a credential of the agent; the option needs Linux and sudo.
   - How to turn the option off.
3. Add one line with a link to the new page in the comment block of the option in `configs/pilot.example.yaml`.
4. When a statement of this issue does not match the code, the code wins: write what the code does and name the difference in the PR body.

## Acceptance

- [ ] The new page exists and the meta file of the directory lists it; the PR body names both paths.
- [ ] `go test -race -count=1 ./internal/config/...` passes; paste the output into the PR body.
- [ ] The PR body lists, for each section of the page, the source file that the statements come from.
- [ ] The page holds no customer name, host name, account number or instance ID.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Code changes. A `pilot doctor` row for the option. A new token source for the agent. Other documentation pages, except the one meta entry and the one link line.

## Refs

- Navigator TASK-661 in this repo

### LEG F4 — repo: qf-studio/pilot — title: feat(doctor): pilot doctor checks the Plane key live and reports the errors of the Plane block — an empty key, slug or project list, a paired Plane project that is not polled

probe: present qf-studio/pilot func[[:space:]]checkTelegramTokenLive\(cfg[[:space:]]\*config\.Config
probe: present qf-studio/pilot func[[:space:]]NewVerifier\(cfg[[:space:]]\*Config,[[:space:]]opts
probe: present qf-studio/pilot func[[:space:]]checkAdapterVerify\(checkName,[[:space:]]tokenSource[[:space:]]string
probe: absent  qf-studio/pilot func[[:space:]]checkPlaneLive\(
probe: absent  qf-studio/pilot func[[:space:]]checkPlaneConfig\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. The command `pilot doctor` runs RunChecks in `internal/health/health.go`. For Telegram and Slack it adds a live check of the token: checkTelegramTokenLive takes the config and a constructor of a verifier, returns nil when the adapter is off or has no token, and calls checkAdapterVerify, which gives a row with the status OK or Error. The constructors are package variables (newTelegramVerifier), so a test can give a fake verifier.

Plane has a verifier since issue 6735: NewVerifier in `internal/adapters/plane/verify.go` makes one labels request for the first polled project. The daemon uses it at the start and in the health loop. `pilot doctor` does not: for Plane it only lists the adapter as enabled.

No code checks the Plane block of the config. A user can enable Plane with an empty key, an empty workspace slug or an empty project list, and the poller then finds nothing with no clear message. A project entry can name a Plane project (the field plane.project_id of a project, GH-6707) that is not in the polled list project_ids of the adapter; such a project never gets a ticket. The config type is in `internal/adapters/plane/types.go`; the project pairing is in `internal/config/config.go` (validateProjectPlane checks only an empty ID and a double use).

These errors must be rows of `pilot doctor`. They must NOT become errors of the Validate method of the config: a daemon whose config loads today must still start after this change.

## Scope

1. `internal/health/health.go`: a new function checkPlaneLive in the form of checkTelegramTokenLive. It returns nil when Plane is off or the key is empty. In the other case it returns the row of checkAdapterVerify with the check name plane.key.live and a verifier from a new package variable (name it newPlaneVerifier) that calls NewVerifier of the Plane adapter package. RunChecks adds the row beside the Telegram and Slack rows.
2. `internal/health/health.go`: a new function checkPlaneConfig that returns nil when Plane is off. With Plane on it returns one row with the check name plane.config:
   - status Error with a message that names each problem found, in this order: api_key is empty; workspace_slug is empty; project_ids is empty; a project entry names a Plane project ID that is not in project_ids (name the project entry and the ID);
   - status OK with a short message (the workspace slug and the count of polled projects) when there is no problem.
   The row has a Fix text that names the config keys to set. The message must not hold the key. RunChecks adds the row.
3. `internal/health/health.go`: the Fix text of the row "no issue source enabled" says to run pilot onboard (the command pilot setup is deprecated). Change only that word.
4. Tests in the directory of `internal/health/health.go` (a new test file, name it plane_check_test.go):
   - TestCheckPlaneLive: Plane off gives nil; an empty key gives nil; a fake verifier that returns nil gives status OK; a fake verifier that returns an error gives status Error with the error text in the message.
   - TestCheckPlaneConfig as a table: Plane off (nil); a full valid block (OK); an empty key; an empty slug; an empty project list; a project entry with a Plane project ID outside the list (the message names the entry and the ID); two problems at once (the message names both).
   - TestCheckPlaneConfig_NoKeyInMessage: with a marker key of the style in `internal/testutil/tokens.go` and an invalid block, the message and the Fix text do not hold the marker.
5. No change of the Validate method and of validateProjectPlane.

## Acceptance

- [ ] `go test -race -count=1 ./internal/health/ -run 'TestCheckPlane'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/health/... ./internal/config/...` passes; paste the output into the PR body.
- [ ] Remove the check of project_ids from checkPlaneConfig -> TestCheckPlaneConfig fails
- [ ] Remove the checkPlaneLive row from RunChecks -> a test of this issue fails; name the test in the PR body
- [ ] The PR body shows that the Validate method has no change (the diff stat of the config package is empty).
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The wizard (`pilot onboard`). The daemon start. The Validate method of the config. The SDK. A check of each polled project. Other adapters.

## Refs

- Navigator TASK-661 in this repo

### LEG F5 — repo: qf-studio/pilot — title: fix(box): the agent user set-up script takes the gateway port as a flag — a host with the default port 9090 passes the startup check of run_as_user

probe: present qf-studio/pilot ^GATEWAY_PORT="9091"
probe: present qf-studio/pilot PILOT_AGENT_EGRESS
probe: absent  qf-studio/pilot gateway-port\)[[:space:]]

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. The script `scripts/box/setup-agent-user.sh` prepares a Linux host for the option run_as_user: it makes the agent user and a firewall chain (PILOT_AGENT_EGRESS) that rejects the traffic of the agent user to the cloud metadata address and to the gateway port of the daemon. The gateway port is a fixed value of the script: GATEWAY_PORT="9091". The boot unit that the script installs applies the same rules.

The startup check of the option (the firewall probe in `internal/executor/agent_command.go`) connects as the agent user to the gateway port of the config and expects that the connection fails. The default gateway port of the config is 9090 (`configs/pilot.example.yaml`). On a host with the default port the script protects port 9091, the probe reaches port 9090, and the daemon refuses each model process. The documentation page `docs/content/deployment/agent-isolation.mdx` tells the user to set the gateway port to 9091 as a workaround.

Tests of the script: `scripts/box/setup-agent-user_test.sh` (runs with fake commands) and `scripts/box/setup-agent-user_real_test.sh` (real iptables and systemd in CI). The CI jobs for them are named Box Scripts.

## Scope

1. `scripts/box/setup-agent-user.sh`: a new flag with the form --gateway-port and one value. The default stays 9091, so a host that was set up before gets the same rules from a new run. The value must be a whole number from 1 to 65535; another value is a usage error before any change. Each place that uses the port follows the flag: the IPv4 and IPv6 rules, the boot unit and what it applies, the dry-run output, the undo path when it names the port, and the header comment with the usage text.
2. The summary that the script prints for the daemon config gets one more line that names the gateway port which the rules protect, in the form of the config key (gateway, port), so that the operator sets the same value.
3. A second run with another port on a host that already has the chain must leave rules for the new port only (no rule for the old port stays in the chain). When the script cannot do that safely, it must stop with a message that names the undo step; say in the PR body which of the two you built.
4. Tests in `scripts/box/setup-agent-user_test.sh`: the default port gives the rules of today; the flag with 9090 gives rules and a unit for 9090 and none for 9091; a value that is not a number, 0 and 70000 are usage errors with no change; the summary names the port. Extend `scripts/box/setup-agent-user_real_test.sh` with one case for a port that is not the default when the form of that file allows it.
5. `docs/content/deployment/agent-isolation.mdx`: the flag table gets the new flag; the warning about port 9091 says to pass the gateway port of the config with the flag (and that the default of the script is 9091). `configs/pilot.example.yaml`: when the comment block of the option names the port, make it match.

## Acceptance

- [ ] The test file of the script passes when run with bash; paste its output (the last 30 lines) into the PR body.
- [ ] ShellCheck reports nothing new for the script; paste the output into the PR body.
- [ ] Make the rules use the fixed value 9091 again when the flag is set -> a test of this issue fails; name the test in the PR body.
- [ ] The PR body shows the dry-run output of the script with the flag set to 9090 (the lines with the port).
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Go code. The startup check of the option. A change of the default gateway port of the config. A read of the daemon config by the script. Other scripts.

## Refs

- Navigator TASK-661 in this repo

### LEG F6 — repo: qf-studio/pilot — title: feat(onboard): pilot onboard offers Plane as a ticket source — it asks for the server, the key, the workspace and the projects, and tests them with one live request

probe: present qf-studio/pilot func[[:space:]]getTicketSourcesForPersona\(persona[[:space:]]Persona\)
probe: present qf-studio/pilot func[[:space:]]onboardAsanaTickets\(state[[:space:]]\*OnboardState\)
probe: present qf-studio/pilot func[[:space:]]hasTicketSource\(cfg[[:space:]]\*config\.Config\)
probe: present qf-studio/pilot func[[:space:]]NewVerifier\(cfg[[:space:]]\*Config,[[:space:]]opts
probe: absent  qf-studio/pilot func[[:space:]]onboardPlaneTickets\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-661. The command `pilot onboard` is the main set-up wizard. Its ticket source step is in `cmd/pilot/onboard_ticket.go`: getTicketSourcesForPersona holds a list of sources (GitHub Issues, Linear, Jira, GitLab Issues, Azure DevOps, Asana), each with a name, a description, a set-up function and a function that says if the source is on. The persona decides how much of the list is shown, by a slice of the list: the first entry for Solo, the first three for Team, all for Enterprise. A set-up function such as onboardAsanaTickets starts from the default config of the adapter, reads the answers with readOnboardLine, tests the connection, calls handleValidationFailure when the test fails, and sets the polling block and the enabled flag at the end. The function hasTicketSource in `cmd/pilot/onboard.go` says if the config has a ticket source; it knows GitHub, Linear, Jira and Asana.

Plane is not in the list. A user must write the Plane block by hand. The config type and its default are in `internal/adapters/plane/types.go` (base URL, key, webhook secret, workspace slug, project IDs, Pilot label, polling). The adapter package has a verifier (NewVerifier in `internal/adapters/plane/verify.go`) that tests the key, the slug and the first project ID with one labels request; it takes an option for the HTTP client. The command `pilot doctor` reports a wrong Plane block with the row plane.config (`internal/health/health.go`).

The test TestPersonaRouting in `cmd/pilot/onboard_test.go` expects 6 ticket sources for Enterprise.

## Scope

1. `cmd/pilot/onboard_ticket.go`: a new entry for Plane at the END of the list in getTicketSourcesForPersona (so the slices for Solo and Team do not change), with the set-up function onboardPlaneTickets.
2. onboardPlaneTickets asks, in this order: the base URL (the default of the adapter config is shown and kept on an empty answer; a self-hosted server gives its own URL); the API key (an empty answer skips Plane with the same message form as Asana and leaves the block off); the workspace slug (required; an empty answer asks one more time, then skips); the project IDs as one line with commas (at least one is required); the Pilot label (default pilot). Trim each value; drop empty project IDs.
3. The connection test: a new function validatePlaneConn that takes the Plane config and runs the verifier of the adapter package with a time limit of 10 seconds. It is reached through a package variable, so that a test can give a fake. On an error the wizard shows the error text and calls handleValidationFailure with a retry of onboardPlaneTickets. The key must NOT appear in any text that the wizard prints.
4. On success the function sets the polling block (enabled, the default interval of the example config) and the enabled flag, and prints one line that says how many projects are polled.
5. `cmd/pilot/onboard.go`: hasTicketSource returns true for an enabled Plane block.
6. Tests in `cmd/pilot/onboard_test.go` (or a new test file beside it): TestPersonaRouting expects 7 sources for Enterprise and the counts of Solo and Team stay 1 and 3; TestHasTicketSource gets a Plane case; TestOnboardPlaneTickets with scripted input and a fake connection test: a full run (the block is enabled, the values are trimmed, two project IDs from one line, the label default); an empty key (the block is not enabled, the connection test is not called); a failed connection test followed by the answer that skips (the block is not enabled). One more case asserts that the captured output of a failed run does not hold the key (a marker key of the style in `internal/testutil/tokens.go`).
7. `docs/content/integrations/plane.mdx`: one short paragraph says that `pilot onboard` can write the Plane block for the Enterprise persona, and that the project pairing key is still set by hand.

## Acceptance

- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestPersonaRouting|TestHasTicketSource|TestOnboardPlaneTickets'` passes; paste the output into the PR body.
- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] Remove the Plane case from hasTicketSource -> TestHasTicketSource fails
- [ ] Enable the block also when the connection test failed and the user skipped -> TestOnboardPlaneTickets fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The question for the Plane project of each repository (the pairing key of a project entry; next issue). The webhook secret. The other ticket sources and their connection tests. The deprecated command pilot setup. The SDK.

## Refs

- Navigator TASK-661 in this repo

## Refs

- F1 → https://github.com/qf-studio/pilot/issues/6734
- F2 → https://github.com/qf-studio/pilot/issues/6735
- F3 → https://github.com/qf-studio/pilot/issues/6736
- F4 → https://github.com/qf-studio/pilot/issues/6745
- F5 → https://github.com/qf-studio/pilot/issues/6748
- F6 → https://github.com/qf-studio/pilot/issues/6755

- Research of 2026-11-27 (session): the first-run commands (`cmd/pilot/onboard.go`, `cmd/pilot/onboard_ticket.go`, `cmd/pilot/doctor.go`, `internal/health/health.go`), the callers of each Telegram getUpdates request, the box log of the 409 lines by day.
- TASK-658 (Plane integration), TASK-651 (agent isolation), TASK-379 (doctor live probes, archived).

---

**Last Updated**: 2026-11-27
