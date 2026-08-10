# SOP: Pre-push gate anatomy + docs-only fast path

**Status:** Active · **Created:** 2026-08-06 · **Related:** GH-4771

## Symptom

Docs-only pushes (`.agent/` closeouts, dispatch records, markers — no `.go`,
`go.mod`, or `go.sum` changes) pay the full 224-287s pre-push gate before
they can land. With main advancing ~12-29×/day (median gap ~21 min), a
~230s gate window collides with an origin/main advance ~16-20% of the time,
forcing repeated gate re-runs (worst during the 16:00 daily train hour).

## Gate anatomy

`.git/hooks/pre-push` is generated from the heredoc in
`scripts/install-hooks.sh` (run `make install-hooks` to regenerate — never
hand-edit `.git/hooks/*`, it's untracked and gets overwritten). The hook
invokes `scripts/pre-push-gate.sh`, which runs 8 steps in the full path:

| Step | Check | Measured cost |
|------|-------|----------------|
| 1/8 | `go build` | 1-5s |
| 2/8 | `golangci-lint` | 4-12s |
| 3/8 | `go test -short -race ./...` | 188-238s |
| 4/8 | `check-secret-patterns.sh` | 3s |
| 5/8 | `check-mocks.sh` | 0s |
| 6/8 | `check-destructive-calls.sh` (TASK-459 Phase 4 / GH-4823) | 0s |
| 7/8 | `check-graph.py` | 0s |
| 8/8 | `check-integration.sh` | 26-29s |

## The docs-only fast path

`scripts/pre-push-classify.sh` reads the `<local_ref> <local_sha>
<remote_ref> <remote_sha>` lines git feeds a pre-push hook on stdin. For
each ref update it computes `git diff --name-only <remote_sha>..<local_sha>`
and takes the **union** of changed paths across every pushed ref. It prints
exactly one word to stdout — `docs-only` or `full` — and always exits 0
(classification never fails; ambiguity biases toward `full`).

**Forced to `full` on any of:**
- Null-OID remote (`remote_sha` is all zeros) — new branch, nothing to diff against.
- Null-OID local (`local_sha` is all zeros) — ref deletion.
- `git diff` errors or returns an empty path list.
- No ref-update lines read from stdin at all.
- Any changed path matches `*.go`, `go.mod`, or `go.sum` (on any pushed ref).

Only if **none** of the above trigger, and the diff is non-empty with zero
code paths, does the push classify as `docs-only`.

`scripts/pre-push-gate.sh` reads `PILOT_GATE_DOCS_ONLY` (set by the
pre-push hook after classification). When `1`, it runs **only**
check-secrets + check-graph and prints an explicit banner naming what was
skipped and why — never a silent skip. Target: <10s.

**Must always run, even docs-only** — do not add these to the skip list:
- `check-secret-patterns.sh` — scans all tracked files, not just code; a
  docs-only push can still introduce a leaked token in a doc or config file.
- `check-graph.py` — exists specifically because of the H3 knowledge-graph
  drift incident (TASK-425), and `.agent`-only pushes are exactly its target
  case.

**Safe to skip on a zero-`.go`/`go.mod`/`go.sum` diff:** build, lint,
`go test -short -race`, check-mocks, check-destructive-calls, check-integration.
Verified (2026-08-06) that no Go test in this repo reads the real `.agent/`
tree — all `.agent`-touching tests build fixtures in `t.TempDir()`.
check-destructive-calls.sh only scans `*.go` files, so it's a no-op (and
correctly skippable) on a docs-only diff by construction.

## `make gate` always runs the full gate

`make gate` invokes `scripts/pre-push-gate.sh` directly with no stdin to
classify, so `PILOT_GATE_DOCS_ONLY` is unset (defaults to `0`) and the full
8-step gate always runs. This is intentional — manual gate runs are for
verifying the complete gate before an unusual push, not for fast iteration.

## Operator step: re-run `make install-hooks` after merge

Hooks are generated, not tracked. **After this change merges, every clone
must re-run `make install-hooks`** to pick up the new stdin-reading,
classifying pre-push hook — an existing installed hook (from before this
change) will keep running the full gate unconditionally until reinstalled.

## Testing

`scripts/test-prepush-fastpath.sh` builds a disposable fixture git repo and
exercises the classifier via fake stdin: docs-only, code-only (`.go` and
`go.sum` diffs), mixed pushes across two refs (one docs, one code), null-OID
remote (new branch), ref deletion, and empty stdin. Run directly:

```bash
./scripts/test-prepush-fastpath.sh
```

It's also wired into `scripts/check-integration.sh` (step 8/8 of the full
gate), so a code push that breaks the classifier fails the gate.

## References

- `scripts/pre-push-classify.sh` — the classifier
- `scripts/pre-push-gate.sh` — fast-path branch + full 7-step gate
- `scripts/install-hooks.sh` — hook generation (pre-commit + pre-push heredocs)
- `scripts/test-prepush-fastpath.sh` — classifier test harness
- GH-4771 — this fast path
- TASK-425 — why check-graph exists and must always run
