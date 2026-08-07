---
name: ci-infra-failure-misclassified-as-code
description: GitHub reports conclusion=failure identically for "linter found violations" and "the runner never got the action" (429/504) — handleCIFailed trusts it blindly, so an infra flake closes a correct PR, spawns a garbage fix issue, and burns the repick budget toward pilot-blocked
type: pitfall
---

# CI infra failures are misclassified as code failures

**What happened (2026-07-24):** GH-4526 attempt 2 (PR #4529) had every real
check green — `test`, Knowledge Graph Drift Gate, Check Secret Patterns, Box
Scripts — and the code was correct: attempt 1's genuine lint error
(`preflight_test.go:165: Error return value of os.Remove is not checked`,
errcheck) had been fixed and no `os.Remove` remained in the diff. The `lint`
job still reported `conclusion: failure`, because GitHub's own infrastructure
died inside it:

```
Failed to download action 'actions/checkout' ... 429 (Too Many Requests)
##[error]Failed to run: Error: Unexpected HTTP response: 504
```

golangci-lint never executed. Autopilot read `failure`, closed the PR,
deleted the branch, spawned garbage fix issue #4530, and incremented the
repick counter — which hit the hard cap (5) and put GH-4526 into
`pilot-blocked`. Correct work discarded by a GitHub outage.

## Mechanism

- `handleCIFailed` (`internal/autopilot/controller.go:1977`) calls
  `GetFailedChecks` and branches on `ConclusionFailure` alone
  (`internal/autopilot/ci_monitor.go:540`). The conclusion string is
  identical for a real lint finding and a runner that never started.
- The log-fetch machinery to tell them apart already exists
  (`GetFailedCheckLogs` → `ghClient.GetJobLogs`,
  `internal/adapters/github/client.go:770`) but is only used to *decorate*
  fix-issue bodies (GH-1567), never to classify.
- No re-run capability existed at all — no `rerun-failed-jobs` call anywhere
  in the client.
- Compounding trap: the repick hard cap counts these as issue toxicity. On
  GH-4526 the cap tripped after only 2 real attempts because 3 earlier
  re-picks died on the hosted daemon's `git_clean` preflight deadlock — the
  very bug that issue was written to fix. An environment bug blocked the
  issue that fixes it.

## How to avoid

1. Before believing a CI verdict, check whether the job produced findings.
   Infra signatures: `Failed to download action` + `429`,
   `##[error]Failed to run:` + `Unexpected HTTP response: 5xx`, runner
   shutdown-signal, "lost communication with the server". A failure with
   zero annotations on changed files is suspect.
2. When triaging a Pilot CI-fail close by hand, read the failed job log
   before trusting the fix issue — `gh run view --job <id> --log`. If the
   real checks are green and the failed job died in `Set up job`, re-run
   instead of re-implementing.
3. Autopilot-spawned `autopilot-fix` issues from an infra false negative are
   *dispatchable garbage*: they carry the `pilot` label and describe a
   nonexistent defect. With approvals off, a bogus fix PR can auto-merge.
   Close them with a supersession comment.
4. Durable fix: [TASK-418](../../../tasks/TASK-418-ci-infra-failure-classification.md)
   → [#4531](https://github.com/qf-studio/pilot/issues/4531) — classify from
   job logs, bounded `rerun-failed-jobs` (2 per head SHA), `failure_class`
   dimension on CI/PR-failure metrics so outages stop polluting baselines.
5. **2026-08-06 recurrence:** TASK-418's classifier was still four hardcoded
   log-prose substrings — a GitHub Actions outage with new wording ("Failed
   to resolve action download info. Error: Service Unavailable") matched
   none of them and closed a correct PR (#4770) again. GH-4779 replaced
   prose-first classification with structural signals evaluated *before* any
   log text is read (synthetic-step name, zero repo-defined steps executed,
   `startup_failure`/`stale` conclusion — see `isStructuralInfra` in
   `internal/autopilot/failure_class.go`), and closed the companion gap where
   `classifyPRFailure(nil)` used to fall back to `FailureClassCode`: zero
   gathered evidence now classifies `FailureClassUnknown` and routes to
   `escalateAndHold`, never `ClosePullRequest`. New outage prose can no
   longer cause a blind close — only a genuinely unrecognized *and*
   structurally-ambiguous shape falls through to the (still prose-based)
   fail-safe default of code.

Related: [[claim-lost-drops-count-toward-hard-cap]] and
[[hard-cap-rearm-in-memory-gate]] (same class: non-code failures consuming an
issue's retry budget), [[bug_false_supersession_label_trust]] (trusting a
GitHub-reported status without reading the evidence).
