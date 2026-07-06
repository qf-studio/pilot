---
name: pilot-debug
description: Pin Pilot executor failures against known v2.149.x patterns before exploring adjacent systems. Invoke when user says "pilot failed", "executor stuck", "epic stuck", "autopilot didn't fire", or similar pilot-failure phrasing.
allowed-tools: Read, Grep, Bash
---

# Pilot Executor Debug Triage

When invoked, state the hypothesis FIRST in this exact order before
running any investigation tool. Each pattern was patched in v2.149.1–.3
and has high prior probability of recurring.

## Triage order

1. **Silent autopilot gate** (v2.149.1, commit f032689b)
   - Diagnostic: grep startup logs for "autopilot disabled" or missing
     startup gate log lines
   - Code: autopilot init path / startup gate evaluation

2. **Worktree path equality** (v2.149.2, commit aac6de5f, TASK-286)
   - Diagnostic: check whether the worktree path is `/tmp/pilot-worktree-*`
     and whether `git rev-parse --git-common-dir` resolves correctly
   - Code: `executor/worktree.go:86` vs `cmd/pilot/repo_allowlist.go:53`

3. **Vacuous-truth empty children** (v2.149.3, commit fd0f7b69)
   - Diagnostic: inspect `allChildrenDone(...)` invocation site for empty
     child-set handling
   - Code: `epic.go:923`

4. **Missing IsEpic guard** (v2.149.3, commit fd0f7b69)
   - Diagnostic: check whether epic-parent results with empty `CommitSHA`
     or `PRUrl` are being flagged as failed
   - Code: `cmd/pilot/handlers.go`, `cmd/pilot/commands.go`

## Output

State one of:

- `match: <pattern N>` — proceed with the per-pattern diagnostic above.
- `novel: <one-line reason none match>` — write a failing regression test
  FIRST, then escalate.

## Anti-patterns

Do NOT touch session dirs, daemon config, queue state, or DB until all 4
known patterns are ruled out. Past sessions burned cycles wandering
through session_dirs/ and daemon config before the actual bug surfaced.

## Related memories

- `.agent/knowledge/memories/pitfalls/mem-pilot-001.md` — silent gate
- `.agent/knowledge/memories/pitfalls/mem-pilot-002.md` — worktree path
- `.agent/knowledge/memories/pitfalls/mem-pilot-003.md` — vacuous truth
- `.agent/knowledge/memories/pitfalls/mem-pilot-004.md` — IsEpic guard
