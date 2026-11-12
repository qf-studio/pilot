# TASK-650: Secret exposure hardening — value-based redaction, leak detection, adapter canary tests, agent isolation

**Status**: 🚀 **11-12: [#6318](https://github.com/qf-studio/pilot/issues/6318) → PR#6331 merged after a run-it review (GO-WITH-NOTES, 35 tamper cases: replace refs now blocked, no case worse than main). The review found five more gate bypasses on main (textconv driver, `log.showRoot=false`, a file that shadows the branch name, `push.followTags`, a secret in a file path) → L7 [#6334](https://github.com/qf-studio/pilot/issues/6334) dispatched (`pilot` + `no-decompose`).** Earlier: **L1–L6 and the follow-ups released (v2.318.0 on 11-10, v2.319.0 on 11-11 15:03Z). One follow-up in the queue; read guard precision stopped by decision; H1 is the next step.** v2.318.0: L1 [#6249](https://github.com/qf-studio/pilot/issues/6249) → PR#6254 · L2 [#6250](https://github.com/qf-studio/pilot/issues/6250) → PR#6259 · L3 [#6251](https://github.com/qf-studio/pilot/issues/6251) → PR#6256 · L4 [#6252](https://github.com/qf-studio/pilot/issues/6252) → PR#6257 + [#6258](https://github.com/qf-studio/pilot/issues/6258) → PR#6260 · L5 [#6263](https://github.com/qf-studio/pilot/issues/6263) → PR#6269 · L6 [#6264](https://github.com/qf-studio/pilot/issues/6264) → PR#6270 · [#6266](https://github.com/qf-studio/pilot/issues/6266) → PR#6271 · [#6268](https://github.com/qf-studio/pilot/issues/6268) → PR#6275 · [#6273](https://github.com/qf-studio/pilot/issues/6273) → PR#6278. v2.319.0: [#6272](https://github.com/qf-studio/pilot/issues/6272) L5 gaps → PR#6282 (+ [#6286](https://github.com/qf-studio/pilot/issues/6286) → PR#6298) · [#6276](https://github.com/qf-studio/pilot/issues/6276) → PR#6293 · [#6279](https://github.com/qf-studio/pilot/issues/6279) → PR#6294 · [#6281](https://github.com/qf-studio/pilot/issues/6281) → PR#6295. **Read guard precision: not planned (founder decision 11-11, mem-305).** [#6267](https://github.com/qf-studio/pilot/issues/6267) → PR#6285 reverted by PR#6289; [#6287](https://github.com/qf-studio/pilot/issues/6287) → PR#6299 closed after a NO-GO black-box review (runner and full-path wrapper bypasses, superlinear deep paths and cd chains, ancestor rule blocks usual commands). main and the box run the substring matcher; the substring pin tests of PR#6300/#6303 stay. Queue: [#6284](https://github.com/qf-studio/pilot/issues/6284) push gate follow-ups, fresh run from main with a base note (first PR#6297 closed: conflicts with PR#6298 and one red test) · [#6310](https://github.com/qf-studio/pilot/issues/6310) `reflect.Ptr` lint that fails the local pre-push gate. **H1 moved to TASK-651 (design accepted 11-12).** PR#6312 (#6284) reviewed after the merge: safe to ship, residuals in [#6318](https://github.com/qf-studio/pilot/issues/6318): first PR#6323 closed 11-12 after a NO-GO review (its shallow-clone part trusted the reflog of local remote-tracking refs: a forged entry got a secret past the gate); the issue runs again with those two items withdrawn and a new item (gate git calls ignore replace refs). Pipeline defects found on the way: [#6326](https://github.com/qf-studio/pilot/issues/6326) held task re-check between task runs · [#6327](https://github.com/qf-studio/pilot/issues/6327) PR that failed on a CI timeout re-enters on a new head or green checks. Founder: Anthropic key + Slack bot/app tokens rotation pending; paid secret scanning for five private repos undecided; SOP `sops/operations/secret-leak-response.md`. Memories mem-300 to mem-305.
**Created**: 2026-11-09
**Assignee**: Navigator (plan, H1 design, reviews) → Pilot (legs)

---

## Context

**Problem**: two incidents on the founder box.

1. **2026-10-09 → 2026-11-08, Telegram token in the daemon log.** The Telegram client put the bot token in the URL path; every network error wrapped a `*url.Error` that holds the URL, and 134 log lines kept it. Fixed by #6244 (PR#6245) and #6246 in v2.317.0: client-side URL redaction, a redacting log writer, alert redaction, a Telegram pattern, log files 0600. The box log was masked in place and the token rotated 2026-11-09.
2. **2026-08-03, an agent session printed the daemon `config.yaml`.** An agent session (GH-4669, a ticket that cited live box evidence) ran one Bash command — `curl -s http://localhost:9091/metrics`, a `find` for YAML files and `cat ~/.pilot/config.yaml | head -60` ("inspect running config"). The first 60 lines held the Slack bot and app tokens, the Slack classifier Anthropic key (= `bot.api_key`) and the Telegram token; Jira and Discord sit lower and were not exposed. The replay recorder kept the output verbatim in `recordings/TG-1785766274768/stream.jsonl` (`TG-` is the recorder's ID prefix, not a Telegram source). PR#4685 and the box database hold none of the values. Masked in place 2026-11-09; the Slack bot token, Slack app token and Anthropic API key are rotated by the founder.

**What still holds the gap (research 2026-11-09, `origin/main` `deb2c45fa`):**

- Redaction knows only the formats in `internal/executor/secretpatterns/patterns.txt`. A secret with another shape (an Anthropic key with hyphens, a Jira token, a Discord token) passes every redaction step.
- `internal/replay/recorder.go` writes the raw agent stream with no redaction (`RecordEvent`, line 92); directories are 0755 and files 0644. `executions.output`, `executions.error` and `execution_logs.message` (`internal/memory/store.go`) are stored raw.
- Nothing watches for a leak: the token sat in the log for 30 days.
- No adapter except Telegram has a leak test; there is no shared helper, and no CI rule asks for one.
- Agent sessions run as the daemon's uid (`ec2-user`). `config.yaml` (0600, same owner) and `/proc/<daemon>/environ` are readable by every agent. `internal/executor/model_env.go` strips secret-shaped env names, but its own comment says hiding a token from a same-UID process is theater (TASK-491).

**Goal**: a secret that Pilot holds never reaches a log, an alert, a recording or the database in clear text, whatever its format; a leak that gets through is detected within minutes; every adapter proves it does not leak its credential; agent sessions cannot read daemon secrets.

---

## Known Pitfalls & Patterns

- **PITFALL** (95%, `allowedtools-inert-under-dangerously-skip-permissions`): under `--dangerously-skip-permissions`, `--allowedTools` is inert; only `--disallowedTools` and a PreToolUse hook passed with `--settings` hold. → L4 is a PreToolUse hook in the same per-run settings file as the write guard, not an allow list.
- **LEARNING** (95%, mem-035): tests that reach a real exec or network boundary fire live. → L3's helper uses `httptest` and a failing `http.RoundTripper`; no test calls a real API.
- **LEARNING** (95%, mem-179): tests that depend on host state pass on bare CI and fail on production-like hosts. → L4 tests set `HOME` with `t.Setenv`.
- **PITFALL** (new, L5): a secret in a URL path leaks through every `*url.Error`; an agent with the daemon's uid reads every daemon secret.

---

## Legs — pilot (daemon)

### LEG L1 — repo: qf-studio/pilot — title: feat(security): value-based secret redaction — register every configured secret value at startup and mask it in logs, alerts, recordings and stored execution output; recordings 0700/0600
probe: present qf-studio/pilot ^func[[:space:]]RedactBytes\(
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Recorder\)[[:space:]]RecordEvent\(
probe: present qf-studio/pilot func[[:space:]]\(s[[:space:]]\*Store\)[[:space:]]SaveLogEntry\(
probe: absent  qf-studio/pilot ^func[[:space:]]RegisterSecretValues\(

## Context

Redaction today matches only the shapes in `internal/executor/secretpatterns/patterns.txt` (`Redact`, `RedactBytes`). A configured secret with another shape passes the log writer (`newRedactingWriter` in `internal/logging/logging.go`), the alert redaction (`redactEvent` in `internal/alerts/engine.go`) and everything else. The replay recorder (`internal/replay/recorder.go`, `RecordEvent`) writes the raw agent stream with no redaction, with directories 0755 and files 0644; on 2026-08-03 an agent's `Read` of `config.yaml` was stored there verbatim. `executions.output`, `executions.error` and `execution_logs.message` (`internal/memory/store.go`, `SaveExecution`, `UpdateExecutionStatus`, `SaveLogEntry`) are stored raw. `internal/executor/acceptance_evidence_redact.go` (`redactParentSecretEnvValues`, `minRedactableSecretLen`) is the value-based precedent for env values. `secretpatterns` is a stdlib-only leaf package; `internal/logging` already imports it, so a registry there is reachable from logging, alerts, replay and memory without an import cycle.

## Scope

1. In `internal/executor/secretpatterns`: `RegisterSecretValues(values ...string)` adds exact values to a process-wide set (mutex-guarded; values shorter than 8 characters and values that start with `${` are ignored). `Redact` and `RedactBytes` replace registered values first (longest first), then the patterns. `ResetSecretValuesForTest()` for tests.
2. Collect values at daemon startup in `cmd/pilot` right after the config loads, before any adapter starts: walk the loaded config (the same key regex as `secretKeyPattern` in `cmd/pilot/config_cmd.go`, `(?i)(token|key|secret|password)`, minus keys that end in `public_key`) and register every string value; also register the values of process env vars whose names the deny rules in `internal/executor/model_env.go` classify as secret (`isModelEnvDenied`), plus `GITHUB_TOKEN`, `GH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN`. Log one Info line with the count only.
3. Recorder (`internal/replay/recorder.go`): apply `secretpatterns.RedactBytes` to every byte written to `stream.jsonl`, `summary.md` and the metadata file; create directories 0700 and files 0600.
4. Store (`internal/memory/store.go`): apply `secretpatterns.Redact` to `Output` and `Error` in `SaveExecution` and to the error text in `UpdateExecutionStatus`, and to `Message` in `SaveLogEntry`.
5. Tests, table-driven: a registered value with no known shape is masked by `Redact`, by the log writer, by `redactEvent`, by the recorder (the file on disk) and by `SaveLogEntry` (the row read back); a short value and a `${VAR}` value are not registered; recorder files are 0600 and directories 0700.

## Acceptance

- `go test -race ./internal/executor/secretpatterns/ ./internal/logging/ ./internal/alerts/ ./internal/replay/ ./internal/memory/ -run 'Redact|Secret|Recorder'` passes; paste the output into the PR body.
- remove the registered-value pass from RedactBytes -> the recorder test fails
- remove the Redact call in SaveLogEntry -> the store test fails
- create recorder files with 0644 again -> the mode test fails

## Out of scope

- Redaction of PR bodies and Slack or Telegram messages built from agent output (follow-up after a research pass).
- Masking secrets that already sit in old recordings or rows.

## Refs

- TASK-650 · #6244 (PR#6245) · #6246 · GH-5437 (one canonical pattern list) · TASK-491 (env scrub)

### LEG L2 — repo: qf-studio/pilot — title: feat(security): runtime leak detection — scan new daemon log bytes and new recordings for secret patterns and registered values, raise a security_event alert with the location and a masked value
probe: present qf-studio/pilot EventTypeSecurityEvent[[:space:]]+EventType
probe: present qf-studio/pilot func[[:space:]]\(e[[:space:]]\*Engine\)[[:space:]]handleSecurityEvent\(
probe: absent  qf-studio/pilot ^func[[:space:]]NewLeakScanner\(

Depends on: #{{L1}}

## Context

On the box a token stayed in the daemon log for 30 days, and nothing noticed. L1 masks the known values and shapes at every write; this leg is the net for a writer that bypasses it (a new sink, a direct `os.Stderr` write, a secret that was not registered because it was added to the config later). The alerts engine already has `EventTypeSecurityEvent` and `handleSecurityEvent` in `internal/alerts/engine.go`.

## Scope

1. New package `internal/leakscan` (plain text, new): `NewLeakScanner(cfg)` with `Run(ctx)`. Every `interval` (default 5m) it reads the bytes appended to the daemon log file since the last offset (offset kept in memory, starts at the file end at startup; a file shorter than the offset resets it to 0), and every `stream.jsonl` and `summary.md` under the recordings path modified since the last tick.
2. A match is a hit of `secretpatterns.Regexes()` or of a registered value (L1). The scanner must not depend on the writers having redacted: it searches the raw bytes on disk.
3. Per hit it sends one `alerts.Event` of type `EventTypeSecurityEvent`: source `leakscan`, metadata `file`, `offset`, `kind` (pattern name or `configured_value`), and a masked sample (first 4 characters + `…`). It never puts the full value in the event, the log or a metric. Hits for the same file and kind are folded to one event per tick.
4. Config `security.leak_scan` (`enabled`, default true; `interval`) in `internal/config/config.go` and `configs/pilot.example.yaml`; wired in `cmd/pilot` beside the other background loops.
5. Metric `pilot_leak_scan_hits_total{kind}` in `internal/gateway/prometheus.go`.
6. Tests, table-driven, with a temp dir: an appended log line with a token shape → one event with a masked sample; a recording that holds a registered value → one event; a clean append → no event; log truncation → offset reset; the event text never holds the full value.

## Acceptance

- `go test -race ./internal/leakscan/ ./internal/alerts/` passes; paste the output into the PR body.
- remove the masking of the sample -> the "event text never holds the full value" test fails
- keep the offset after truncation -> the truncation test fails

## Out of scope

- Scanning the database, PR bodies or chat messages.
- Automatic token revocation.

## Refs

- TASK-650 · #6244 · `internal/alerts/engine.go` security events

### LEG L3 — repo: qf-studio/pilot — title: test(security): canary leak test for every adapter HTTP client — a shared helper drives each client with a canary credential through a failing transport and a 4xx/5xx server, and a CI check requires the test in every adapter package with an HTTP client
probe: present qf-studio/pilot FakeTelegramBotToken
probe: absent  qf-studio/pilot ^func[[:space:]]AssertNoCanaryLeak\(

## Context

Only Telegram has a leak test (`client_redact_test.go`, added by #6244). The other adapter HTTP clients (asana, azuredevops, discord, github, gitlab, jira, linear, plane, slack) send their credential in a header, which `*url.Error` does not print, but a body echo, a `%v` of a request or a header dump in an error would still leak it. There is no shared helper; each package builds `httptest` servers inline. `internal/testutil` holds `tokens.go` and `gitenv.go`.

## Scope

1. `internal/testutil` (new file, plain text): `const CanaryToken = "pilot-canary-7f3e1c9a-do-not-log"` and `AssertNoCanaryLeak(t, canary string, texts ...string)`, which fails the test with the index of the text that holds the canary. Helpers `FailingTransport()` (an `http.RoundTripper` that returns an error whose text holds the request URL and all request headers, the worst case) and `EchoServer(t, status int)` (an `httptest` server that answers with `status` and echoes the request headers and body in the response body).
2. One test per adapter package with an HTTP client (asana, azuredevops, discord, github, gitlab, jira, linear, plane, slack, telegram): build the client with `CanaryToken` as the credential; call one read method through `FailingTransport` and one through `EchoServer` with 401 and with 500; capture the returned error, a `slog` handler writing to a buffer, and the alert text if the package builds one; `AssertNoCanaryLeak` over all of them. A client whose error echoes the body must be fixed in the same PR (redact the canary-bearing parts before the wrap).
3. scripts/check-adapter-canary-tests.sh (new, plain text): for every directory under `internal/adapters/` whose non-test Go files contain `http.Client` or `http.NewRequest`, require a `_test.go` file that calls `testutil.AssertNoCanaryLeak`; print the missing packages and exit 1. Add it as a step to the existing CI job in `.github/workflows/ci.yml` and as a `make` target.
4. No test reaches a real network endpoint.

## Acceptance

- `go test -race ./internal/adapters/... ./internal/testutil/ -run Canary` passes; paste the output into the PR body.
- ./scripts/check-adapter-canary-tests.sh passes; paste the output into the PR body.
- delete the canary test of one adapter -> check-adapter-canary-tests.sh exits 1 and names that package
- make FailingTransport omit the headers -> the github canary test still passes but the helper's own test fails

## Out of scope

- HTTP clients outside `internal/adapters/` (executor, typesafe, transcription, health): follow-up.

## Refs

- TASK-650 · #6244 (telegram `client_redact_test.go`) · mem-035 (no live calls in tests)

### LEG L4 — repo: qf-studio/pilot — title: feat(executor): read guard for agent sessions — a PreToolUse hook blocks Read/Grep/Glob and Bash commands that touch the Pilot home, the config file or /proc/*/environ, in every Claude Code run
probe: present qf-studio/pilot func[[:space:]]setupWriteBoundary\(
probe: present qf-studio/pilot writeBoundaryGuardName[[:space:]]+=
probe: absent  qf-studio/pilot ^const[[:space:]]+readGuardName[[:space:]]

## Context

On 2026-08-03 an agent session (GH-4669, a ticket that cited live box evidence) ran one Bash command — `curl -s http://localhost:9091/metrics`, a `find` for YAML files and `cat ~/.pilot/config.yaml | head -60` ("inspect running config"). The first 60 lines held the Slack bot and app tokens, the Slack classifier Anthropic key (= `bot.api_key`) and the Telegram token; Jira and Discord sit lower and were not exposed. Agent sessions run as the daemon's uid, so the file is readable; the real fix is a separate uid (H1, held). This leg stops the accidental read now. It is a guard against mistakes, not a security boundary: a determined agent can still read the file through an interpreter. Under `--dangerously-skip-permissions` only a PreToolUse hook holds (`allowedtools-inert-under-dangerously-skip-permissions`). The write guard in `internal/executor/write_boundary.go` (`setupWriteBoundary`, `write-guard.sh`) is the pattern: a per-run settings file passed with `--settings` in `internal/executor/backend_claudecode.go`. Today ticket runs pass no settings file; only chat and bootstrap runs set a write boundary.

## Scope

1. New guard script in `internal/executor/write_boundary.go` or a sibling file: `readGuardName = "read-guard.sh"`, matcher `Read|Grep|Glob|NotebookRead|Bash`. It denies (exit 2, a stderr line that names the rule) when: the path input (`file_path`, `path`, `notebook_path`) resolves under the Pilot home (~/.pilot, resolved through symlinks) or equals the loaded config path; or a Bash `command` contains the Pilot home path, the config path, `.pilot/config`, or `/proc/` followed by `environ`. Unparseable input fails closed.
2. Every Claude Code run gets a per-run settings file: the read guard always, the write guard too when `WriteBoundary` is set (one settings file, two hooks). The Pilot home and config path come from the executor config, not from a hard-coded path.
3. Config `executor.claude_code.read_guard` (default true) in `internal/config/config.go` and `configs/pilot.example.yaml`.
4. Tests (`t.Setenv("HOME", tmp)`, mem-179): the script blocks a `Read` of $HOME/.pilot/config.yaml, a `Read` through a symlink into $HOME/.pilot, `Bash` with `cat ~/.pilot/config.yaml` and with `cat /proc/123/environ`; it allows a `Read` inside the worktree and a `Bash` `go test ./...`; a run with a write boundary gets both hooks in one settings file.

## Acceptance

- `go test -race ./internal/executor/ -run 'ReadGuard|WriteBoundary'` passes; paste the output into the PR body.
- remove the symlink resolution -> the symlink test fails
- drop the Bash matcher -> the `cat ~/.pilot/config.yaml` test fails

## Out of scope

- A separate uid, namespaces or containers (H1).
- Blocking network exfiltration.

## Refs

- TASK-650 · TASK-551 (write guard) · `allowedtools-inert-under-dangerously-skip-permissions`

### LEG L5 — repo: qf-studio/pilot — title: feat(security): secret gate before every push, PR and comment — refuse a push whose new commits hold a registered secret value or a known secret shape, and mask PR titles, PR bodies and issue comments
probe: present qf-studio/pilot func[[:space:]]\(g[[:space:]]\*GitOperations\)[[:space:]]Push\(
probe: present qf-studio/pilot func[[:space:]]\(g[[:space:]]\*GitHubPRCreator\)[[:space:]]CreatePR\(
probe: absent  qf-studio/pilot ^func[[:space:]]CheckPushRange\(

Depends on: #{{L1}}

## Context

On 2026-04-07 a Pilot executor run committed the live Telegram bot token into a doc under `.agent/system/`, and Pilot pushed it to this public repo. GitHub secret scanning raised an alert the same day (alert #2), push protection did not block the push, and nobody saw the alert for seven months; the 2026-09-24 history rewrite took the file off main, but GitHub still serves the commit by SHA and 58 forks exist. A gate based only on shapes misses secrets with unknown formats; the daemon knows the exact values (L1 registry), so it can refuse them whatever their shape. Push sites: `GitOperations.Push` and the lease push in `internal/executor/git.go`, the default-branch push in the same file, and the pushes in `internal/autopilot/conflict_gomod.go` and `internal/autopilot/conflict_stacked.go`. PR creation: `GitOperations.CreatePR` (`internal/executor/git.go`) and `GitHubPRCreator.CreatePR` (`internal/adapters/sdkshim/github_pr_creator.go`). Comments: `Client.AddComment` in `internal/adapters/github/client.go`.

## Scope

1. In `internal/executor/secretpatterns`: `CheckPushRange(ctx, dir, base, head string) error`. It runs `git diff --no-color base..head` and `git log --format=%B base..head` in `dir` and returns an error that names the commit and the file (never the value) when the output holds a registered value (L1) or a pattern hit. `base` is the merge base with the remote default branch; for a new branch use `git merge-base head origin/<default>`.
2. Call it before every push site listed above. A hit fails the push with a clear error (`push refused: commit <sha> adds a secret value in <file>`), sends an `alerts.EventTypeSecurityEvent` with the same text, and leaves the branch local. No retry loop pushes it anyway.
3. Apply `secretpatterns.Redact` to the title and body in both `CreatePR` implementations and to the body in `Client.AddComment`.
4. Tests in a temp git repo with a bare remote: a commit that adds a registered value → push refused, nothing reaches the remote; a commit that adds a Telegram-shaped token → refused; a clean commit → pushed; the error text does not hold the value; a PR body with a registered value is masked.

## Acceptance

- `go test -race ./internal/executor/secretpatterns/ ./internal/executor/ ./internal/autopilot/ -run 'PushRange|SecretGate'` passes; paste the output into the PR body.
- remove the gate call in GitOperations.Push -> the "push refused" test fails
- drop the registered-value check in CheckPushRange -> the registered-value test fails

## Out of scope

- Rewriting existing history or forks.
- Pushes made by humans outside Pilot (the repo pre-commit and pre-push hooks cover those).

## Refs

- TASK-650 · GitHub secret scanning alert #2 (resolved as revoked 2026-11-10)

### LEG L6 — repo: qf-studio/pilot — title: feat(security): watch GitHub secret-scanning alerts — poll open alerts for every configured GitHub repo and send a security_event for each new one
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]ListIssueComments\(
probe: present qf-studio/pilot EventTypeSecurityEvent[[:space:]]+EventType
probe: absent  qf-studio/pilot ^func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]ListSecretScanningAlerts\(

## Context

GitHub found the leaked Telegram token on 2026-04-07 and kept the alert open for seven months, because nothing routes secret-scanning alerts to a person. Pilot already polls GitHub for every configured project and has a Slack alert channel; `internal/adapters/github/client.go` (`ListIssueComments`, `GetPullRequest`) shows the client style, and `internal/alerts/engine.go` has `EventTypeSecurityEvent`.

## Scope

1. `ListSecretScanningAlerts(ctx, owner, repo, state string)` on the GitHub client in `internal/adapters/github/client.go`: `GET /repos/{owner}/{repo}/secret-scanning/alerts?state=open&per_page=100` with pagination. Decode only `number`, `secret_type`, `secret_type_display_name`, `created_at`, `html_url`, `push_protection_bypassed`; never decode or log the `secret` field.
2. A watcher (new package internal/secretalerts, plain text, new) with `Run(ctx)`: every `interval` (default 1h) for each configured project with a GitHub repo, list open alerts; for each alert number not seen before, send one `alerts.Event` of type `EventTypeSecurityEvent` (source `github_secret_scanning`, metadata repo, number, type, created_at, html_url). Seen numbers persist per repo in a 0600 JSON file under the Pilot data directory, so a restart does not resend. On the first run every currently open alert counts as new.
3. A 403 or 404 (missing permission, scanning off) logs one Warn per repo per day with the permission hint (`secret_scanning_alerts: read` for a GitHub App, `security_events` or repo scope for a token) and does not fail the loop.
4. Config `security.github_alert_watch` (`enabled` default true, `interval`) in `internal/config/config.go` and `configs/pilot.example.yaml`; wired in `cmd/pilot` beside the other background loops.
5. Tests with `httptest`: two open alerts → two events, then no event on the next tick; a restart with the seen file → no event; a new alert → one event; a 403 → one Warn and no event; the response `secret` field never appears in an event or log line.

## Acceptance

- `go test -race ./internal/secretalerts/ ./internal/adapters/github/ -run 'SecretScanning|SecretAlert'` passes; paste the output into the PR body.
- remove the seen-file write -> the restart test fails
- log the raw alert JSON -> the "secret field never appears" test fails

## Out of scope

- Resolving alerts automatically.
- GitLab and Azure DevOps equivalents.

## Refs

- TASK-650 · GitHub secret scanning alert #2

### LEG L7 — repo: qf-studio/pilot — title: fix(security): push secret gate — ignore textconv and external diff drivers, show root commits, scan the exact branch ref, no followed tags, scan file paths

probe: present qf-studio/pilot func[[:space:]]CheckPushRange\(
probe: present qf-studio/pilot GIT_NO_REPLACE_OBJECTS
probe: absent  qf-studio/pilot "--no-textcon[v]"

## Context

Navigator TASK-650. The push secret gate is CheckSecretGate in `internal/executor/secret_gate.go`; the scan is CheckPush and CheckPushRange in `internal/executor/secretpatterns/pushrange.go`; the push methods are Push, PushForceWithLease and PushToMain in `internal/executor/git.go`. The gate runs in a repository that the model session can write. The run-it review of PR #6331 found five ways to publish a secret past the gate; each one works on main and none came from that PR:

1. A textconv driver. A config value diff.NAME.textconv plus an attribute line (in the info/attributes file of the .git directory, in a committed .gitattributes file, or through core.attributesFile) makes git log print converted text. The gate scans the converted text, and git runs a command that the session chose.
2. The config value log.showRoot set to false. An orphan root commit that holds the secret, merged and then deleted in a later commit, shows no patch.
3. A shadow of the branch name. A file with the branch name inside the .git directory (for example a path pilot/GH-9 there) that holds a clean SHA makes the gate scan that commit, while git push sends the branch under refs/heads.
4. The config value push.followTags set to true. The message of an annotated tag is published and is not scanned.
5. A secret in a file path. Paths are not scanned. When the content also matches, the refusal error and the log line print the path.

## Scope

1. Every git log, git show and git diff command of the gate passes --no-textconv and --no-ext-diff, and sets log.showRoot to true with a -c argument.
2. The gate resolves the branch to scan as the full ref name under refs/heads, never as a short name. Every push of the three push methods uses a full refspec with refs/heads on both sides, so that the commit that the gate scans is the commit that git sends. A test compares both SHAs.
3. Every push of the three push methods passes --no-follow-tags and sets push.followTags to false with a -c argument.
4. The gate scans the path of each file that the pushed range adds, changes or renames, with the same patterns and registered values as the content scan. A match refuses the push.
5. The refusal error, the log line and the alert text pass the file path through the same mask as other text, so that a path that holds a secret value is not printed.
6. Tests with real temporary repositories through the push method (the fixture newPushRefusedFixture shows the pattern). Use the fake secret shape that the present gate tests use. One test per case of Context, each one must end with a refused push and no new commit on the bare remote: the textconv driver through each of the three attribute places; log.showRoot false with the orphan root; the shadow file; an annotated tag message with push.followTags true (the tag must be absent on the remote; the branch push itself may pass when the commits are clean); a secret in a file name alone; a secret in a file name and in the content (assert that the error text and the captured log do not hold the value). One more test: the textconv command must not run (it would create a marker file).
7. Put a comment `// mutation pin: <case> -> this test fails` above each test.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/... -run 'SecretGate|PushRange|PushRefused'` passes; paste the output into the PR body.
- [ ] Remove --no-textconv -> the three textconv tests and the marker test fail.
- [ ] Remove the log.showRoot argument -> the orphan root test fails.
- [ ] Scan the short branch name again -> the shadow test fails.
- [ ] Remove --no-follow-tags -> the tag test fails.
- [ ] A clean push still passes: the gate tests that exist today pass without a change to their assertions.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The trust in remote-tracking refs that the session can write (TASK-651 moves the gate to a repository that the daemon owns). A scan of tag messages. Any change to the secret patterns.

## Refs

- Navigator TASK-650 in this repo · review of PR #6331

## Reviews 2026-11-10 (Navigator, four review agents; § = follow-up issue)

| Leg | PR | Verdict | Confirmed gaps |
|---|---|---|---|
| L1 value redaction | #6254 | REQUEST-CHANGES → #6266 (PR#6271 merged) | recorder stream is double-escaped, so a value with a quote, backslash or newline passed unmasked; key regex missed `pat`, `webhook_url`, `headers`, `api_key_command` output; only `pilot start` registered values; overlap tail; URL-encoded forms |
| L2 leak scan | #6259 | APPROVE-WITH-NOTES → #6268 (PR#6275 merged) | rotated-log tail never scanned; value split across ticks; recordings over the cap half-scanned; symlinked root skipped; alert dedup can drop a repeat; headless log not scanned |
| L3 canary tests | #6256 | APPROVE-WITH-NOTES → #6268 | CI check satisfied by a comment; GitHub App token exchange and one Slack body path unredacted; Slack/Telegram echo legs vacuous |
| L4 read guard | #6257 + #6260 | APPROVE-WITH-NOTES → #6267 | every spawn site guarded and the incident command blocked; substring match blocks harmless commands in this repo (203 files mention the path, e.g. a commit message); ancestor-recursive reads and some `$HOME` forms pass; no preflight for realpath / jq / python3 |
| L5 push gate | #6269 | REQUEST-CHANGES → #6272 | a refused push is forgiven when the remote branch exists (four sites); most GitHub text paths unmasked (`AddPRComment`, `CreateIssue`, `CreatePullRequest`, releases, `gh` CLI paths); an uncomputable base scans the whole history and refuses every push; refusal classified as retryable infra |
| L6 alert watcher | #6270 | APPROVE-WITH-NOTES → #6273 | an alert is marked seen before delivery is known; reopened alerts never re-fire; one credential for all repos |

## Reviews 2026-11-10 pm (second round, three review agents)

| Change | PR | Verdict | Confirmed gaps |
|---|---|---|---|
| L6 watcher delivery | #6278 | APPROVE-WITH-NOTES → #6279 | the weekly limit of the "unavailable" event is in memory only, so each daemon start sends it again at critical severity (seen live: 8 alerts at the v2.318.0 start); seen-marking happens at queue time, not at delivery; no Warn in gateway-only mode; false page-cap Warn at exactly the cap |
| L5 push gate gaps | #6282 | APPROVE-WITH-NOTES → #6284 | a cancelled context becomes a terminal "secret" refusal; refusal text says "remove the secret" for scan errors; a plain gate error is still forgiven by an existing remote branch; shallow clone with an advanced default branch gives a false alert; no test for the main PR path and the PushToMain path |
| L4 read guard precision | #6285 | REQUEST-CHANGES → reverted by PR#6289, #6287 | reads through inline interpreter code, a heredoc or pipe into a shell, a wrapped shell and a path variable pass (the substring matcher denied them); the token pass takes 15 s on a 10 KB command on bash 5.2; without realpath every call is allowed; one test red on macOS |

**Live checks on v2.318.0 (box, 2026-11-10 15:28Z):** `secret values registered for redaction count=11` ✅ · `leak scan started interval=5m0s log=true` ✅ · `github secret alert watch started repos=10` ✅, 2 repos readable, 8 returned 404 because secret scanning was disabled there (enabled 11-10 on the three public repos; the five private repos need the paid add-on) · recordings root 0700 ✅ (mode of new recording files not confirmed) · 0 `read blocked` lines in the first 90 minutes of ticket runs.

**Daemon defects found on the way:** a re-run after a pre-PR hold starts from the default branch and fails at push (#6281; hit GH-6272, GH-6267 and one console task) · `heldThisCycle` in `processQueue` keeps a held task out until the queue drains, so a hold that clears during a run is not re-checked (no issue yet).

**Not covered by any leg:** a `git push` that the agent runs itself inside a session (bash guard blocks only force pushes) → H1.

## Held

### H1 — agent sessions under a separate Unix uid → moved to TASK-651

**2026-11-12: design accepted; plan and legs are in `.agent/tasks/TASK-651-agent-uid-isolation.md`, design in `.agent/system/agent-isolation-design.md`.** The notes below are the 2026-11-09 research points, kept for history.

Held for a Navigator design pass; filed after the design doc lands in `.agent/system/`. Founder decision 2026-11-09: isolate agent sessions; this reverses TASK-491 "sandboxing the executor is out of scope". Open design points from the 2026-11-09 research:

1. **Privilege path.** The daemon runs unprivileged as `ec2-user`; a uid switch needs `sudo -u <agent>` with a narrow sudoers rule, `CAP_SETUID` on the binary (lost on every self-upgrade write), or a small root helper. `SysProcAttr` has no credential setup today (`internal/procgroup`).
2. **Worktrees and repos.** The agent writes `/tmp/pilot-worktree-*` and the shared `.git` under `/var/lib/pilot/repos`; the daemon reads, pushes, counts commits and sweeps them. Options: a shared group with setgid directories and `core.sharedRepository`, ACLs, or per-run `chown`; git's `safe.directory` trips across uids.
3. **Agent HOME.** Its own `~/.claude` login or `CLAUDE_CODE_OAUTH_TOKEN` injected per run, the Navigator plugin cache, gh and git config, Go and Node caches. `internal/executor/post_merge_confine.go` (per-review `HOME`, `CLAUDE_CONFIG_DIR`, copied login) is the precedent.
4. **Process control.** Killing a different-uid process group, the `/proc`-based liveness checks, and the cgroup memory leaf all need the privilege path or delegation.
5. **`/proc`.** `/proc/<daemon>/environ` is closed to another uid by default; `hidepid=2` is optional. `start-pilot.sh` exports `CLAUDE_CODE_OAUTH_TOKEN`, which the agent still receives by design.
6. **Local gateway.** The agent reached the daemon gateway on `localhost:9091` (metrics). Check which gateway endpoints return config or secrets, and whether an agent uid must be refused there.
7. **Secret values out of files.** The config holds references (for example `ssm:/pilot/box/slack_bot_token`); the daemon resolves them from SSM SecureString at startup and keeps the values in memory. Then `config.yaml` and its backups hold no secret, and an agent `cat` shows references only. This needs the agent uid blocked from the instance metadata service (iptables owner match), or the agent could read SSM with the instance role itself.
8. **Gate.** `executor.claude_code.run_as_user`, default empty (off); macOS and local dev keep today's behaviour.

## Refs

- #6244 → PR#6245 · #6246 (v2.317.0)
- `.agent/tasks/archive/TASK-491-executor-env-scrub-and-hooks-default.md` · TASK-551 (write guard) · TASK-41 (secret patterns)
- L1 → https://github.com/qf-studio/pilot/issues/6249
- L2 → https://github.com/qf-studio/pilot/issues/6250
- L3 → https://github.com/qf-studio/pilot/issues/6251
- L4 → https://github.com/qf-studio/pilot/issues/6252
- L5 → https://github.com/qf-studio/pilot/issues/6263
- L6 → https://github.com/qf-studio/pilot/issues/6264
- L7 → https://github.com/qf-studio/pilot/issues/6334
- Follow-ups: #6258 · #6266 · #6267 · #6268 · #6272 · #6273 · #6276

---

**Last Updated**: 2026-11-09
