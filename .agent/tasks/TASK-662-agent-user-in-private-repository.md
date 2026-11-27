# TASK-662: The agent user in a private repository — prove the run, then decide if the agent needs a read token

**Status**: 🚧 **Planned 2026-11-27 17:00Z. No ticket yet: the first step is a test run on the box, and it waits for the founder's choice of a private repository.** The design options for a read token are written below; the choice comes after the test.
**Created**: 2026-11-27
**Assignee**: the session (test run on the box) + the founder (two decisions) + Pilot (tickets after the decisions)

---

## Context

**Problem**:
The option `run_as_user` (TASK-651) is on for each of the 13 projects of the founder box since 11-26. It has 19 runs with no failure, and each of the 19 was in a public repository (mem-355). A run in a private repository is not proven. The founder asked for a design and a plan (11-27).

Read on 2026-11-27 (code at release v2.328.0; the research ran nothing):

- **The core path needs no credential of the agent.** The daemon fetches with its own credential and hands the base commit to the agent user as a git bundle (`internal/executor/agent_workspace.go`). The agent commits in a workspace that it owns. The daemon takes the commits back as a pack, runs the secret gate, pushes and opens the pull request (`internal/executor/handover.go`, `internal/executor/git.go`). The remote of the agent workspace has no credential and is not used.
- **The agent environment holds no GitHub token.** The allow list in `internal/executor/agent_command.go` drops each name except PATH, the locale, TMPDIR, the Go cache and proxy names and the names of the model provider. Git prompts are off.
- **Three operations fail in a private repository:**
  1. A gh command in the session (for example a read of the issue or of a pull request).
  2. A git fetch or pull in the session.
  3. The download of a private dependency in a quality gate (a private Go module, a private package). This one is inferred from the code and not tested; private submodules are not read.
- **The read token of the agent has one source today: a GitHub App.** The provider in `cmd/pilot/main.go` needs the App block of the GitHub adapter and gives a token with read-only rights for one repository; `internal/executor/agent_token.go` writes it to a file, refreshes it, and serves it to git and to the gh guard. The code says plainly that there is no fallback to the token of the daemon, because that token can write.
- **Decision of 11-24, confirmed 11-27 (mem-354):** no GitHub App key goes onto the founder box. The App of Pilot Cloud can make a token for each customer installation, and the box runs agent code.

**Goal**:
A run as the agent user in a private repository is proven on the box, with a written list of what works and what does not. Then the founder decides if the agent gets a read token without a GitHub App, and from which source.

---

## Known Pitfalls & Patterns

- **PITFALL** (mem-354): before a step that moves a key or a credential, read the recorded decision. No option below puts an App key on the founder box.
- **LEARNING** (mem-355): say "N runs, all public" for the option, not only "N runs, no failure".
- **LEARNING** (mem-337): each temporary state of the box gets a timer with a hard limit and a look at the first gate result. The test run of step 1 follows it.
- **LEARNING** (mem-336): before a plan says that Pilot runs on a repository, check the live daemon config.

---

## Acceptance Criteria

- [ ] One ticket in a private repository ran as the agent user from pickup to an open pull request, and the record names each step with its result.
- [ ] One ticket whose session needs a gh read ran, and the record says what the agent did when the command failed.
- [ ] The founder's decision on a read token is recorded here, with the option and the reason.

---

## Route

### Phase 1: Prove the run
- [ ] 1. The founder names a private repository of the box for one small test ticket (waits for the founder)
- [ ] 2. The session files one small ticket there (a text change), watches the run, and records each step: workspace, commits, gates, hand-over, push, pull request (operator, needs 1)
- [ ] 3. The session reads the run log for each failed gh or git command of the agent and records what the agent did then (operator, needs 2)

### Phase 2: Decide the read token
- [ ] 4. The founder chooses: no token (option A), a fine-grained read-only token (option B), or dependencies fetched by the daemon (option C) (waits for the founder, needs 3)
- [ ] 5. Tickets for the chosen option (written after 4)

### Side tracks
- [ ] The limits of the option are in the public documentation (TASK-661, the documentation page ticket)

### Waits for you
- [ ] The name of a private repository for the test ticket (step 1). The cheapest safe case: a repository with no private dependency, and a ticket that changes one text file.
- [ ] The choice of step 4, after the test.

---

## Implementation

### Phase 1 — the test run
The repository must be a project of the live daemon config (read it through SSM first). The ticket is small, carries the labels of a normal run, and has no dependency on a private module. The off-switch stays ready (the box helper for the option, then a restart). Stop rule: the first failed gate with a retry ends the test; the session reads the log before a second try.

### Phase 2 — the options

| Option | What changes | Size | Risk |
|---|---|---|---|
| **A. No token** | Nothing. The limits go into the documentation. The ticket text and the injected context must give the agent what it would read with gh. | 0 tickets | A session that needs a gh read works with less context; a gate with a private dependency fails |
| **B. Fine-grained read-only token** | A second source for the read token provider in `cmd/pilot/main.go`: a token file named by a new config key. The file, the refresh and the git helper of `internal/executor/agent_token.go` are reused. | 1 to 2 tickets | A long-lived token that agent code can read. Limit it to read-only contents of the repositories of the box. It does not rotate by itself. |
| **C. The daemon fetches dependencies** | Before the session, the daemon downloads the dependencies with its credential into the cache of the agent. | 2 to 3 tickets for each ecosystem | The daemon runs build code that the repository controls |
| **D. A credential proxy** | The daemon serves read-only git and gh over a socket. | 4 tickets or more | New attack surface; not recommended now |

**Recommendation of the session:** A first, with the test of phase 1 as the proof. B only when the test or a real project shows that sessions fail without a gh read or a private dependency. The daemon already puts the issue text into the prompt, so the common case needs no gh read.

---

## Out of Scope

- A GitHub App on the founder box (decided 11-24).
- The customer boxes of Pilot Cloud; they get the option with the customer image, and their token comes from the App of that product.
- Changes of the gh guard policy.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Order | Build a token source first; test first | Test first | The code read says that the core path works; a test costs one ticket and shows the real gaps |
| Test repository | A new empty private repository; an existing private project of the box | The founder's choice | A new repository is a new project in the daemon config; an existing one is a real case |

---

## Verify

The record of steps 2 and 3 in this document: the ticket number, the pull request number, the result of each step, and each failed command of the agent.

---

## Done

- [ ] Steps 1 to 4 are recorded.
- [ ] TASK-657 step 12 and the open decision of TASK-651 point to the result.

---

## Refs

- Research of 2026-11-27 (session): `internal/executor/agent_command.go`, `internal/executor/agent_workspace.go`, `internal/executor/agent_token.go`, `internal/executor/handover.go`, `cmd/pilot/main.go`, `cmd/pilot/ghguard.go`.
- TASK-651 (agent isolation), TASK-657 (the box set-up; its step 12 is this question).
- Memories mem-354, mem-355, mem-336, mem-337.

---

**Last Updated**: 2026-11-27
