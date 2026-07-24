---
name: shared-oauth-session-strands-second-machine
description: Copying ~/.claude/.credentials.json to a second machine makes both share one OAuth session — whichever refreshes first strands the other; the stranded side shows the TUI working while headless `claude --print` hangs forever and every daemon subprocess dies with EMPTY stderr and zero tokens
type: pitfall
---

# One OAuth session, two machines — the second one strands the first

**What happened (2026-07-24, box outage 16:2x–18:17Z):** to unblock the
hosted canary tenant, the box's live `~/.claude/.credentials.json` was copied
to the canary instance via S3 SSE-KMS. Both machines then ran on the same
OAuth session. The canary refreshed the token; the **box** was left holding an
invalidated one. Every Pilot execution on the box died for ~2 hours.

Cost: 5 consecutive failed executions of GH-4531 (45 min of compute), the
repick hard cap tripping, and a `pilot-blocked` label — all attributed to the
task, none to the environment.

## Diagnostic signature (this is the valuable part)

The failure is **silent in every channel that normally reports errors**:

| Probe | Result on a stranded box |
|---|---|
| `claude` (interactive TUI) | **works** — renders "Welcome back", answers prompts |
| `claude --print "say OK"` | **hangs forever**, killed by timeout, exit 124, no output |
| executor subprocess | `unknown: exit status 1`, **stderr EMPTY**, stdout = only the `{"type":"system","subtype":"init"}` line, ~4m30s |
| intent judge / classifiers | `signal: killed (cause=context_deadline)`, hang exactly to their 30s deadline |
| `curl https://api.anthropic.com/v1/models` | **fast 401** — dns 2ms, tls 20ms, total 147ms |
| `stat -c %y ~/.claude/.credentials.json` | mtime **hours stale** (08:54Z after 9h) |

**The TUI working is not evidence the daemon can work** — the banner renders
from the local credentials file without an API round-trip, and launching the
TUI can itself perform the refresh that fixes the problem. Test the headless
path (`--print`), because that is what the daemon spawns.

Perfect network + fast unauthenticated 401 **rules out** the GH-4401
`fetch failed` class. Network-fine + headless-hang = credential.

## How to avoid

1. **Never run one OAuth session on two machines.** Give each consumer its own
   credential: fund a separate `ANTHROPIC_API_KEY` per tenant, or authenticate
   each host independently. A copied credentials file is a time bomb whose
   fuse is the other machine's next refresh.
2. Fix on the stranded host = re-authenticate locally: `claude` → `/login`
   (or `claude setup-token`). Verify with `claude --print` **and** a fresh
   mtime on `.credentials.json` — not with the TUI banner.
3. **No daemon restart is needed for credentials alone** — the daemon spawns a
   fresh `claude` per execution and reads the file from disk each time. Restart
   only to clear in-memory dispatch gates.
4. When every task fails with empty stderr and zero tokens, probe the
   environment **before** re-reading the task spec. Five executions were burned
   here re-attempting a task that was never the problem.

Related: [[oauth-ssm-params-rot-live-credentials-source-of-truth]] (the same
credential rotting in SSM — this is the reverse direction, where the *live*
host is the one left stale), [[ci-infra-failure-misclassified-as-code]] (same
family: environment failure billed to the task),
[[hard-cap-rearm-in-memory-gate]] (cleaning up after the cap trips).
