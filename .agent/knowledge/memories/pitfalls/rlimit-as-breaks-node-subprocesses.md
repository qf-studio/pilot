---
name: rlimit-as-breaks-node-subprocesses
description: RLIMIT_AS is NOT an RSS cap — a 4GB address-space limit silently breaks all Node/V8 HTTPS fetches (instant generic "fetch failed"), and darwin no-op code paths detonate on first Linux run
type: pitfall
---

# RLIMIT_AS on Node subprocesses = instant, misleading fetch failures

**What happened (2026-07-17, #4401):** After the AWS cutover, 100% of executor
task executions failed for ~12h with `unknown: exit status 1` / 0 tokens. The
Claude CLI inside ran fine but every API request died in ~25ms with undici
`fetch failed` — AFTER successful TCP connect and `TLS authorized: true`.
Cause: GH-3028's "RSS cap" (`subprocess_limits.max_rss_mb: 4096`) is
implemented as **RLIMIT_AS via prlimit64** (`backend_claudecode.go:462`).
V8 reserves far more *virtual* address space than RSS; mmap failures inside
fetch surface as a generic connection error with no errno anywhere.

**Why it stayed hidden:** the cap is a no-op on darwin — the laptop daemon
never exercised it. Same latent class as [[absolute-state-paths-bypass-cutover-shim]]:
code/config that silently does nothing on macOS and detonates on first Linux run.

**Diagnostic signature (recognize it fast next time):**
- Executor children fail 100%, judge/classifier children fine → per-spawn-path, look at what only that path does to the child (env, prlimit, pipes)
- Every manual replication succeeds incl. same env/flags/cwd/cgroup — but rlimits survive `sudo -iu` re-exec (can't raise a lowered hard limit), so a "still fails through fresh login" result points at rlimits
- `fetch failed` in <50ms after `secureConnect authorized: true` = allocation, not network
- Decisive check: `grep 'address space' /proc/<child>/limits`

**How to avoid:**
1. Never cap Node/V8 with RLIMIT_AS. Use cgroup v2 `memory.high`/`memory.max`
   transient scopes or `NODE_OPTIONS=--max-old-space-size`.
2. Any "no-op on darwin" branch is untested-in-prod code for a macOS-developed
   daemon — inventory them all before a Linux cutover (`grep -rn 'darwin.*no-op\|GOOS'`).
3. Smoke test after enabling any subprocess limit: spawn Node child under the
   limit, assert one HTTPS fetch succeeds.

Box workaround: `subprocess_limits.enabled: false` (config.yaml.bak-4396) —
OOM protection off until #4401 ships the cgroup implementation.

**Fix shipped (#4401):** `applyResourceLimits` (resource_limits_linux.go) now
creates a per-subprocess cgroup v2 leaf (`/sys/fs/cgroup/pilot/pilot-<pid>/`,
`memory.max`) instead of calling `prlimit64`/RLIMIT_AS — this caps actual
resident memory, never virtual address space, so it cannot reproduce this
bug class. Best-effort: degrades to telemetry-only (WARN log) if cgroup v2
isn't mounted/delegated (no root, as on the ec2-user-run box) — `pilot
doctor` surfaces this via the "subprocess_limits cgroup v2 available" check.
Additionally, `NODE_OPTIONS=--max-old-space-size=<max_rss_mb>` is now always
injected when the cap is enabled — a cooperative V8 heap bound that works
regardless of cgroup availability and costs nothing. Regression coverage:
`TestApplyResourceLimits_NodeFetchSucceeds` spawns a real Node child under
the full cap pipeline and asserts an HTTPS fetch succeeds — this is the test
that would have caught the original RLIMIT_AS bug before cutover.
