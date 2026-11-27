# TASK-659: SDK credential leak hardening — a leak test for each adapter HTTP client of the SDK, a redact helper, the Telegram token out of error text, a CI gate

**Status**: 🚀 **State 2026-11-27 09:48Z. With Pilot: [studio-sdk#175](https://github.com/qf-studio/studio-sdk/issues/175) (a masked error no longer hands out its unmasked cause; the leak test helper reads the whole error chain; leg C6, dispatched 09:48Z).** Done: [studio-sdk#173](https://github.com/qf-studio/studio-sdk/issues/173) merged as SDK pull request 174 at 09:39Z (the helpers in `sdk/testutil`, the redact package, the Telegram client masks the token in the text of each error). The review by the session after the merge found one defect, which came from the ticket text ("still unwraps to the cause"): the masked error kept Unwrap, and the cause chain holds a `*url.Error` whose URL holds the token; the helper ErrText read only the top text, so the leak test did not see it. Leg C6 closes that. The daemon's own review did not run for pull request 174 (not in its sample). **Next, in this order, on the founder's "dispatch" of 11-27 (no new word needed):** (1) after the merge of #175: review, then dispatch the two test legs C2 and C3 together; (2) after both merge: the gate leg C4; (3) an SDK release by hand (the next free patch number after v0.39.2); (4) write and dispatch the pin leg in Pilot (C5, held). Before each dispatch: fetch the SDK clone, read the legs C2 to C4 against the merged code (the helper names: CanaryToken, AssertNoCanaryLeak, FailingTransport, EchoServer, ErrText, CaptureSlog in the file leak.go of `sdk/testutil`; the redact package below `sdk/util/text`), and correct a leg that names something else.
**Created**: 2026-11-27
**Assignee**: Pilot (one leg at a time) + one operator step (SDK release)
**Parent**: TASK-650 (secret exposure hardening), leg L3 (the canary tests of Pilot's own adapter clients)

---

## Context

**Problem**:
TASK-650 gave each adapter HTTP client inside Pilot a credential leak test, a redact helper and a CI gate. Since then the production paths moved to the clients of the SDK (`github.com/qf-studio/studio-sdk`): the daemon polls GitHub, Linear, Jira, GitLab, Asana, Azure DevOps and Plane through SDK clients, and builds the SDK adapters for Telegram, Slack and Discord. The SDK never got the hardening. Read on 2026-11-27 (SDK commit 3f78c7e, release v0.39.2):

- Ten integration packages, each with an HTTP client. No package has a credential leak test. The SDK has no redact helper and no gate; its CI has build and test, lint, and the secret pattern check.
- **A real leak:** the SDK Telegram client puts the bot token into the URL path and wraps the transport error with no masking. A failed request gives an error text that holds the full URL with the token. This is the class of incident 6244 (the token in 134 lines of a daemon log). Pilot fixed it only in its own client.
- Three SDK clients put a response body into an error with no masking (Discord, Linear, the Slack socket mode client). A server that echoes a request header into its error body puts the credential into the error.
- On 2026-11-26 the removal of Pilot's unused internal Plane client (PR#6721) also removed its leak test. That was correct, and it shows the gap: the Plane credential has no leak test in any repository now.

Pilot's value-based redaction of log lines is a second line of defence for the daemon. It does not protect an error text that leaves through another path (a ticket comment, an alert, a chat message), and it does not protect another host of the SDK.

**Goal**:
Each HTTP client of the SDK has a test that drives it with a marker credential through a transport failure and through error responses, and proves that the marker is in no error and no log line. The Telegram token is masked in each error. A CI gate of the SDK refuses a new client without such a test. Pilot then pins the SDK release that holds this.

---

## Known Pitfalls & Patterns

- **PITFALL** (mem-300): a secret in a URL path leaks through each `*url.Error`. → C1 masks the token in the Telegram client at each of its request sites, and its test uses a failing transport, which is the case that makes a `*url.Error`.
- **PITFALL** (`jira-two-parallel-clients-poller-is-sdk`): Pilot had two clients for one service, and a fix in the internal one did nothing for the poll path. → This task hardens the SDK clients, which are the production path; the legs do not touch Pilot's internal clients.
- **PITFALL** (mem-329): a test that calls the guard function and not the production call site proves nothing. → Each leak test drives the public methods of the client (a read call and a write call), not the redact helper; each leg has a mutation check that removes the masking at one call site.
- **PITFALL** (`spec-rerouting-a-platform-must-rewrite-composed-tests-of-the-old-flow`): a spec must name the tests that encode the old behaviour. → C1 and C3 say that an existing test which asserts the full error text of a changed error must be adjusted in the same change.

---

## Acceptance Criteria

- [ ] Each of the ten integration packages of the SDK has a test that calls the leak assert helper with the marker credential, through a failing transport and through an echo server with status 401 and 500.
- [ ] With the masking removed at one request site of the Telegram client, the Telegram leak test fails.
- [ ] The gate script of the SDK exits 1 and names the package when the leak test of one package is deleted; CI runs the gate.
- [ ] An SDK release holds the four legs, and `go.mod` of Pilot pins it.

---

## Route

### Phase 1: The tools and the real leak
- [x] 1. The SDK gets the leak test helpers and a redact helper; the Telegram token is masked in each error of the Telegram client, with its leak test (qf-studio/studio-sdk#173)
- [ ] 2. A masked error no longer gives out its unmasked cause, and the test helper reads the full chain of an error (qf-studio/studio-sdk#175, needs 1)

### Phase 2: A leak test for each client
- [ ] 3. Leak tests for the Asana, Azure DevOps, GitLab, Jira and Plane clients; a leak that a test finds is fixed in the same change (needs 2)
- [ ] 4. Leak tests for the Discord, Linear, Slack and GitHub clients; the three response bodies in errors are masked (needs 2)

### Phase 3: The gate and the release
- [ ] 5. A CI gate of the SDK refuses an integration package with an HTTP client and no leak test (needs 3, 4)
- [ ] 6. An SDK release holds 1 to 5 (operator, needs 5)
- [ ] 7. Pilot pins that SDK release (needs 6)

### Waits for you
- [x] The go for the plan, and the order: step 1 first and alone (it holds the real leak), then 2 and 3 together (founder, 11-27: "dispatch")

---

## Implementation

Each leg below is one ticket. The SDK repository is public; no client name and no real token value enters a ticket.

### Phase 1 — C1
**Goal**: the helpers exist, and the one known leak is closed.
**Files** (SDK): `sdk/testutil/tokens.go` (the marker constant lives beside it), a new file for the leak helpers in the same package, a new small redact package under `sdk/util`, `sdk/integrations/telegram/client.go`, `sdk/integrations/telegram/client_test.go`.

### Phase 2 — C2, C3
**Goal**: each client is driven through the two failure classes with the marker credential.
**Files** (SDK): the client file and the client test file of each package; for Slack also `sdk/integrations/slack/socketmode.go`.

### Phase 3 — C4, release, C5
**Goal**: the state cannot rot; Pilot uses it.
**Files** (SDK): a new script beside `scripts/check-secret-patterns.sh`, `.github/workflows/ci.yml`, `Makefile`. Pilot: `go.mod`.

---

## Out of Scope

- Pilot's internal adapter clients (they have their tests since TASK-650).
- A move of the Telegram token out of the URL (the Telegram Bot API takes it only there).
- Webhook secrets and signature checks.
- Log redaction in the host (Pilot has it; the SDK takes a logger from its host).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the helpers live | copy into each package; `sdk/testutil` | `sdk/testutil` | The SDK rules name that package for test tokens; one helper, ten users |
| How a leak is closed | change each error format; one redact helper | one redact helper (text and error), used at the request helper of each client | The model of TASK-650; one place for each client |
| Order | all at once; the Telegram fix first | C1 first and alone | It holds the one leak that is proven; C2 and C3 need its helpers |
| The gate | grep script; a Go analyzer | grep script with a self-test | The model of TASK-650 leg L3; no new tool |

---

## Verify

```bash
# in the SDK repository
go test -race -count=1 ./sdk/...
bash scripts/check-adapter-canary-tests.sh
```

---

## Done

- [ ] The four SDK tickets are merged, each reviewed after its merge.
- [ ] The gate runs in the CI of the SDK.
- [ ] An SDK release holds them and Pilot pins it.

---

## Legs

### LEG C1 — repo: qf-studio/studio-sdk — title: fix(telegram): the bot token is masked in each error of the Telegram client — with leak test helpers in sdk/testutil and a redact helper

probe: present qf-studio/studio-sdk func[[:space:]]NewClientWithBaseURL\(botToken,[[:space:]]baseURL[[:space:]]string
probe: present qf-studio/studio-sdk FakePlaneAPIKe[y]

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

The Telegram client (`sdk/integrations/telegram/client.go`) builds each request URL as the base URL, the bot token and the endpoint: the token is part of the URL path. When the transport fails, the HTTP client of Go returns a `*url.Error` whose text holds the full URL. The client wraps that error with `%w` and returns it, so the error text holds the bot token. A host that logs the error, or puts it into a chat message or a ticket comment, publishes the token. The client has four request sites (get updates, edit message, answer callback, and the shared send path). The SDK has no helper to mask a credential in text and no helper to test for a leak; `sdk/testutil/tokens.go` holds only fake token constants.

## Scope

1. A new file in the package of `sdk/testutil/tokens.go` with the leak test helpers: a constant CanaryToken (a plain marker text that no secret pattern check matches, for example "sdk-canary-7f3e1c9a-do-not-log"); AssertNoCanaryLeak(t, canary, texts...) that fails the test for each text that holds the marker and names the index of the text; FailingTransport() that returns an http.RoundTripper which fails each request with an error whose text holds the request line, each header and the body (the worst case); EchoServer(t, status) that returns a test server which answers each request with the given status and a body that holds the request line, each header and the body of the request; ErrText(t, err) that returns the text of the full error chain (and fails the test for a nil error); CaptureSlog(t) that sets the default slog logger to a buffer for the test and returns the buffer.
2. A test file for these helpers: each helper has a test; one test proves that the error of FailingTransport and the body of EchoServer do hold an Authorization header value and a URL path value that the request carried (so a later change that drops them makes the leak tests blind, and this test fails).
3. A new small package for redaction below the directory of `sdk/util/text/sanitize.go` (its own directory, name redact): Text(s, secrets...) returns s with each non-empty secret replaced by a fixed mask; Error(err, secrets...) returns an error with the masked text that still unwraps to err (errors.Is and errors.As keep to work); a nil error stays nil. With tests.
4. The Telegram client masks the bot token in each error it returns: one helper of the client for the transport error and for each text that comes from the response, used at each of the four request sites. The mask replaces the token value wherever it stands in the text. The error still unwraps to the cause.
5. A leak test in `sdk/integrations/telegram/client_test.go`: TestCanary_ClientDoesNotLeakCredential. Build the client with CanaryToken as the bot token; call the get-updates method and one send method through FailingTransport, and through EchoServer with the status 401 and 500 (the constructor with a base URL exists: NewClientWithBaseURL; when the client has no option for a transport, add one in the style of the other packages); collect the error texts and the captured log; AssertNoCanaryLeak on all of them.
6. An existing test that asserts the full text of an error that changes here is adjusted in the same change.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/testutil/... ./sdk/util/... ./sdk/integrations/telegram/...` passes; paste the output into the PR body.
- [ ] Remove the masking at the get-updates request site -> TestCanary_ClientDoesNotLeakCredential fails and its message holds the marker.
- [ ] Make FailingTransport leave out the URL of the request -> the test of the helpers fails.
- [ ] The PR body lists the four request sites and says how each is masked.

## Out of scope

The other integration packages. A move of the token out of the URL. The webhook code. A CI gate.

## Refs

- Navigator TASK-659 of the Pilot repository

### LEG C6 — repo: qf-studio/studio-sdk — title: fix(redact): a masked error no longer hands out its unmasked cause — the leak test helper reads the whole error chain

probe: present qf-studio/studio-sdk func[[:space:]]ErrText\(t[[:space:]]testing\.TB,[[:space:]]err[[:space:]]error\)
probe: present qf-studio/studio-sdk func[[:space:]]\(e[[:space:]]\*redactedError\)[[:space:]]Unwrap\(\)

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Follow-up of pull request 174 (issue #173), from the review after its merge. The redact package (`sdk/util/text/redact/redact.go`) returns from Error a wrapper whose text is masked and whose Unwrap method returns the cause. The cause still holds the secret in its own text: for the Telegram client (`sdk/integrations/telegram/client.go`) the cause chain holds a `*url.Error` whose URL field holds the bot token. A host that walks the chain (errors.Unwrap in a loop, a logger that prints each cause, errors.As to `*url.Error` and a print of it) gets the token. The leak test helper ErrText (`sdk/testutil/leak.go`) returns only the text of the top error, so the leak tests do not see this.

## Scope

1. ErrText returns the text of the whole chain: the text of the error, then the text of each error that errors.Unwrap gives, to the end of the chain; for an error with the method Unwrap() []error, each branch. The texts are joined with a newline. A nil error still fails the test.
2. The wrapper of redact.Error has no Unwrap method. It gets an Is(target error) bool method that answers errors.Is(cause, target), and an As(target any) bool method that answers errors.As(cause, target), so errors.Is and errors.As on the masked error keep their results. After a successful As, when the target is a pointer to a `*url.Error`, the value it gets is a copy whose URL field is masked with the same secrets and whose Err field is the masked form of the inner error (redact.Error with the same secrets), not the original.
3. Tests in the test file of the redact package: errors.Is with a sentinel in the cause is true; errors.As to a custom error type gives the value; errors.As to `*url.Error` gives a copy with a masked URL, and its text and the text of its Err hold no secret; errors.Unwrap of the masked error is nil; the original error is not changed.
4. The test of the helpers (`sdk/testutil/leak_test.go`): ErrText of a chain of three errors holds the three texts; ErrText of a joined error holds each branch.
5. The Telegram leak test (TestCanary_ClientDoesNotLeakCredential in `sdk/integrations/telegram/client_test.go`) must pass with the new ErrText. Add to it: errors.As of the transport error to `*url.Error` succeeds, and the text of that value holds no marker.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/testutil/... ./sdk/util/... ./sdk/integrations/telegram/...` passes; paste the output into the PR body.
- [ ] Give the wrapper its Unwrap method back -> the Telegram leak test fails and its message holds the marker.
- [ ] Return the original `*url.Error` from As -> the test of the redact package fails.
- [ ] The PR body lists each caller of errors.Unwrap, errors.Is and errors.As in the repository that can get an error of the Telegram client, and says that its result does not change.

## Out of scope

The other integration packages. The other helpers of the test package. A CI gate.

## Refs

- Navigator TASK-659 of the Pilot repository
- Pull request 174 of this repository and the review comment on it

### LEG C2 — repo: qf-studio/studio-sdk — title: test(security): credential leak tests for the Asana, Azure DevOps, GitLab, Jira and Plane clients — a leak that a test finds is masked in the same change

probe: present qf-studio/studio-sdk func[[:space:]]AssertNoCanaryLeak\(
probe: present qf-studio/studio-sdk func[[:space:]]FailingTransport\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

The package of `sdk/testutil/tokens.go` has leak test helpers (CanaryToken, AssertNoCanaryLeak, FailingTransport, EchoServer, ErrText, CaptureSlog), and the SDK has a redact package (Text, Error). The Telegram client has a leak test (TestCanary_ClientDoesNotLeakCredential in `sdk/integrations/telegram/client_test.go`); use it as the model. Five clients send their credential in a request header and have no leak test: `sdk/integrations/asana/client.go` (Authorization, bearer), `sdk/integrations/azuredevops/client.go` (Authorization, basic; two request helpers), `sdk/integrations/gitlab/client.go` (PRIVATE-TOKEN), `sdk/integrations/jira/client.go` (Authorization, basic), `sdk/integrations/plane/client.go` (X-API-Key). A header does not enter a `*url.Error`, but a response body that echoes the request, or an error that prints a request, would leak the credential, and no test would see it.

## Scope

1. In the client test file of each of the five packages, a test TestCanary_ClientDoesNotLeakCredential: build the client with CanaryToken as the credential (for a basic credential: the marker is the secret part); call one read method and one write method through FailingTransport, and through EchoServer with the status 401 and 500; collect each error text with ErrText and the captured log; AssertNoCanaryLeak with the marker. For a basic credential also assert that the base64 form of the user and the marker is in no text.
2. When a client has no way to set the base URL or the transport for a test, add an option in the style of the package (the GitLab and Plane clients have WithHTTPClient; four packages have NewClientWithBaseURL; the Jira client has neither).
3. When a leak test fails, mask the credential with the redact package at the request helper of that client (the error of the transport and each text from the response), and keep the error chain. Name each such fix in the PR body.
4. An existing test that asserts the full text of an error that changes here is adjusted in the same change.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/asana/... ./sdk/integrations/azuredevops/... ./sdk/integrations/gitlab/... ./sdk/integrations/jira/... ./sdk/integrations/plane/...` passes; paste the output into the PR body.
- [ ] In the Plane client, add the response body of a 500 answer to the returned error with no masking -> the Plane leak test fails.
- [ ] The PR body has one line for each of the five clients: which methods the test calls, and "no leak found" or the fix.

## Out of scope

The Discord, Linear, Slack, GitHub and Telegram packages. The helpers of the test package. A CI gate. Webhook secrets.

## Refs

- Navigator TASK-659 of the Pilot repository

### LEG C3 — repo: qf-studio/studio-sdk — title: fix(security): the Discord, Linear and Slack clients mask the credential in a response body they put into an error — with credential leak tests for these clients and the GitHub client

probe: present qf-studio/studio-sdk func[[:space:]]AssertNoCanaryLeak\(
probe: present qf-studio/studio-sdk func[[:space:]]EchoServer\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

The package of `sdk/testutil/tokens.go` has leak test helpers (CanaryToken, AssertNoCanaryLeak, FailingTransport, EchoServer, ErrText, CaptureSlog), and the SDK has a redact package (Text, Error). The Telegram client has a leak test (TestCanary_ClientDoesNotLeakCredential in `sdk/integrations/telegram/client_test.go`); use it as the model. Three clients put a response body into an error with no masking: `sdk/integrations/discord/client.go` (the error "discord API error: HTTP ..."), `sdk/integrations/linear/client.go` (the error "API error: ..."), and `sdk/integrations/slack/socketmode.go` (the error of the connection open call). A server or a proxy that echoes the request into its error body puts the credential into the error text. The Slack package has two clients (`sdk/integrations/slack/client.go` with the bot token, the socket mode client with the app token). The GitHub client (`sdk/integrations/github/client.go`) sends a bearer token at three request sites and has no leak test.

## Scope

1. The Discord, Linear and Slack socket mode clients mask their credential with the redact package in each response text that enters an error, and in the transport error; the error chain stays.
2. In the client test file of the Discord, Linear and GitHub packages, and in the test files of the two Slack clients, a test TestCanary_ClientDoesNotLeakCredential (for the socket mode client: TestCanary_SocketModeDoesNotLeakCredential): build the client with CanaryToken as the credential; call one read method and one write method (for the socket mode client: the connection open call) through FailingTransport, and through EchoServer with the status 401 and 500; collect each error text with ErrText and the captured log; AssertNoCanaryLeak with the marker.
3. When a client has no way to set the base URL or the transport for a test, add an option in the style of the package.
4. When a leak test finds a leak that item 1 does not name, fix it in the same way and name it in the PR body.
5. An existing test that asserts the full text of an error that changes here is adjusted in the same change.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/discord/... ./sdk/integrations/linear/... ./sdk/integrations/slack/... ./sdk/integrations/github/...` passes; paste the output into the PR body.
- [ ] Remove the masking of the response body in the Discord client -> the Discord leak test fails.
- [ ] Remove the masking in the Slack socket mode client -> its leak test fails.
- [ ] The PR body has one line for each of the five clients: which methods the test calls, and "no leak found" or the fix.

## Out of scope

The Asana, Azure DevOps, GitLab, Jira, Plane and Telegram packages. The helpers of the test package. A CI gate. Webhook secrets.

## Refs

- Navigator TASK-659 of the Pilot repository

### LEG C4 — repo: qf-studio/studio-sdk — title: ci(security): a gate refuses an integration package with an HTTP client and no credential leak test

probe: present qf-studio/studio-sdk func[[:space:]]TestCanary_SocketModeDoesNotLeakCredential\(
probe: present qf-studio/studio-sdk func[[:space:]]AssertNoCanaryLeak\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Each integration package of the SDK with an HTTP client has a credential leak test that calls testutil.AssertNoCanaryLeak. Nothing keeps this state: a new client, or a deleted test, passes CI. The CI workflow (`.github/workflows/ci.yml`) has the jobs for the secret pattern check, build and test, and lint; the secret pattern script is `scripts/check-secret-patterns.sh`; the `Makefile` has the target check-secrets.

## Scope

1. A new script beside `scripts/check-secret-patterns.sh` (name it check-adapter-canary-tests.sh, bash, `set -euo pipefail`): for each directory below the integrations directory of the SDK whose non-test Go files contain http.Client or http.NewRequest, a test file in the same directory must call testutil.AssertNoCanaryLeak(t on a line that is not a `//` comment line. A mention in a comment or in a string does not count. The script prints each directory without such a call and exits 1; with none it prints one OK line with the count of checked directories. An environment variable overrides the directory to check, for the self-test.
2. A self-test script beside it (name it test-check-adapter-canary.sh): it builds temporary directories and proves four cases: a package with a client and a real call passes; a package with a client and no test fails and is named; a package with a client and the call only in a comment fails; a package with no client is not checked.
3. The CI workflow gets a job that runs the gate script and its self-test. The Makefile gets a target check-adapter-canary.
4. The gate passes on the repository as it is. When it names a package, do not weaken the gate: stop and say so in the PR body.

## Acceptance

- [ ] `bash scripts/check-adapter-canary-tests.sh` passes; paste the output into the PR body.
- [ ] The self-test script passes; paste the output into the PR body.
- [ ] Delete the leak test of one package in a scratch copy -> the gate exits 1 and names that package; say in the PR body which package was used.

## Out of scope

A change of a client or of a leak test. The secret pattern check. Go analyzers.

## Refs

- Navigator TASK-659 of the Pilot repository

## Refs

- Research of 2026-11-27 (session): inventory of the ten integration packages of the SDK at commit 3f78c7e (release v0.39.2) and of Pilot's adapters at origin/main.
- Parent: TASK-650, leg L3 (the model: the helpers in `internal/testutil/canary.go`, the package `internal/adapters/redact`, the gate `scripts/check-adapter-canary-tests.sh`).
- Incident 6244 (the Telegram token in error text through a `*url.Error`).
- Held leg, written after the SDK release: C5 (Pilot pins the SDK release that holds C1 to C4).
- Dispatch SOP: `.agent/sops/development/dispatch-task-legs-to-pilot.md`; SDK checkout for the dispatch script: a clone outside this repository (`--checkout qf-studio/studio-sdk=PATH`).

---

**Last Updated**: 2026-11-27

## Refs

- C1 → https://github.com/qf-studio/studio-sdk/issues/173
- C6 → https://github.com/qf-studio/studio-sdk/issues/175
