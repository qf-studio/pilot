# TASK-650: Secret exposure hardening — value-based redaction, leak detection, adapter canary tests, agent isolation

**Status**: 🚀 **L1–L4 DISPATCHED 2026-11-09** (`pilot` + `no-decompose`) — L1 [#6249](https://github.com/qf-studio/pilot/issues/6249) value-based redaction · L2 [#6250](https://github.com/qf-studio/pilot/issues/6250) leak scan (depends on L1) · L3 [#6251](https://github.com/qf-studio/pilot/issues/6251) adapter canary tests · L4 [#6252](https://github.com/qf-studio/pilot/issues/6252) read guard. **H1 (separate agent uid) held for a design pass** (founder decision 2026-11-09: isolate agent sessions; reverses TASK-491 "sandboxing out of scope").
**Created**: 2026-11-09
**Assignee**: Navigator (plan, H1 design, reviews) → Pilot (legs)

---

## Context

**Problem**: two incidents on the founder box.

1. **2026-10-09 → 2026-11-08, Telegram token in the daemon log.** The Telegram client put the bot token in the URL path; every network error wrapped a `*url.Error` that holds the URL, and 134 log lines kept it. Fixed by #6244 (PR#6245) and #6246 in v2.317.0: client-side URL redaction, a redacting log writer, alert redaction, a Telegram pattern, log files 0600. The box log was masked in place and the token rotated 2026-11-09.
2. **2026-08-03, an agent session read the whole `config.yaml`.** A Telegram task's tool result held every adapter secret, and the replay recorder kept it verbatim in `recordings/TG-1785766274768/stream.jsonl`. Masked in place 2026-11-09; the Slack bot token, Slack app token and Anthropic API key are rotated by the founder.

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

On 2026-08-03 an agent session in a Telegram task read the whole daemon `config.yaml` with a plain tool call. Agent sessions run as the daemon's uid, so the file is readable; the real fix is a separate uid (H1, held). This leg stops the accidental read now. It is a guard against mistakes, not a security boundary: a determined agent can still read the file through an interpreter. Under `--dangerously-skip-permissions` only a PreToolUse hook holds (`allowedtools-inert-under-dangerously-skip-permissions`). The write guard in `internal/executor/write_boundary.go` (`setupWriteBoundary`, `write-guard.sh`) is the pattern: a per-run settings file passed with `--settings` in `internal/executor/backend_claudecode.go`. Today ticket runs pass no settings file; only chat and bootstrap runs set a write boundary.

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

## Held

### LEG H1 — agent sessions under a separate Unix uid (design first)

Held for a Navigator design pass; filed after the design doc lands in `.agent/system/`. Founder decision 2026-11-09: isolate agent sessions; this reverses TASK-491 "sandboxing the executor is out of scope". Open design points from the 2026-11-09 research:

1. **Privilege path.** The daemon runs unprivileged as `ec2-user`; a uid switch needs `sudo -u <agent>` with a narrow sudoers rule, `CAP_SETUID` on the binary (lost on every self-upgrade write), or a small root helper. `SysProcAttr` has no credential setup today (`internal/procgroup`).
2. **Worktrees and repos.** The agent writes `/tmp/pilot-worktree-*` and the shared `.git` under `/var/lib/pilot/repos`; the daemon reads, pushes, counts commits and sweeps them. Options: a shared group with setgid directories and `core.sharedRepository`, ACLs, or per-run `chown`; git's `safe.directory` trips across uids.
3. **Agent HOME.** Its own `~/.claude` login or `CLAUDE_CODE_OAUTH_TOKEN` injected per run, the Navigator plugin cache, gh and git config, Go and Node caches. `internal/executor/post_merge_confine.go` (per-review `HOME`, `CLAUDE_CONFIG_DIR`, copied login) is the precedent.
4. **Process control.** Killing a different-uid process group, the `/proc`-based liveness checks, and the cgroup memory leaf all need the privilege path or delegation.
5. **`/proc`.** `/proc/<daemon>/environ` is closed to another uid by default; `hidepid=2` is optional. `start-pilot.sh` exports `CLAUDE_CODE_OAUTH_TOKEN`, which the agent still receives by design.
6. **Gate.** `executor.claude_code.run_as_user`, default empty (off); macOS and local dev keep today's behaviour.

## Refs

- #6244 → PR#6245 · #6246 (v2.317.0)
- `.agent/tasks/archive/TASK-491-executor-env-scrub-and-hooks-default.md` · TASK-551 (write guard) · TASK-41 (secret patterns)
- L1 → https://github.com/qf-studio/pilot/issues/6249
- L2 → https://github.com/qf-studio/pilot/issues/6250
- L3 → https://github.com/qf-studio/pilot/issues/6251
- L4 → https://github.com/qf-studio/pilot/issues/6252

---

**Last Updated**: 2026-11-09
