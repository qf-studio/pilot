# TASK-655: Pre-flight judge — the judge knows the repository it works for, and a link to that repository is never "external"

**Status**: ✅ **J1 MERGED 2026-11-18 04:44Z (PR#6483); no run-it review yet.** Dispatched 2026-11-17 ~20:20Z → [pilot#6456](https://github.com/qf-studio/pilot/issues/6456) (`pilot` + `no-decompose`)
**Created**: 2026-11-17
**Assignee**: Pilot

---

## Context

**Problem**:
On 2026-11-16 the pre-flight judge declined issue 6402 with the decision `reject_out_of_scope`. The daemon had filed that issue itself, in its own repository, from a post-merge review. The reason text of the judge: the issue "requires working with the external `qf-studio/pilot` repository" and "the working directory is not a git repository and no code has been provided". The issue then waited with the label `pilot-needs-clarification` until a person saw it one day later.

Research (navigator-research, 2026-11-17) found the cause:

- The poller of the SDK (studio-sdk v0.38.7) calls the judge with an empty repository context.
- The shim `sdkPreFlightJudge.JudgeIssue` in `cmd/pilot/poller_github.go` passes that empty value on. The shim knows the repository slug (`repoFullName`) but uses it only for alert metadata.
- The judge (`IntentJudge.JudgeIssue` in `internal/executor/intent_judge.go`) adds a "Repository Context" section to its prompt only when the value is not empty. The prompt names no repository. The judge is a `claude` subprocess that runs in the working directory of the daemon, which is not a checkout.
- Each rejection blocks the issue. There is no deterministic guard for the case "the links point to this repository".

One of 49 issues with the label `post-merge-review` got this refusal; issue 6432 with the same body shape passed. So the refusal is model variance, and the missing context makes it possible.

**Goal**:
The judge gets the repository slug and one sentence that says links to it are internal. A `reject_out_of_scope` verdict for a body whose GitHub links all point to the project repository becomes an accept. The other decisions of the judge do not change.

---

## Known Pitfalls & Patterns

- **DECISION** (`cmd/pilot/poller_github.go`, comment above autopilotFixBodyHeading): only CI-fix issues skip the judge; review issues hold untrusted reviewer text and stay judged. → J1 adds no bypass by label and no skip of the judge call.
- **LEARNING** (mem-320): a review-minor issue with comment-only items stalls under the mutation-pin template. → Not part of J1; issue 6402 was cut by hand on 11-17.
- **LEARNING** (declined-run-repicked-and-evidence-fail-files-false-fix-issue): a declined run can come back. → J1 keeps the verdict record of today (metrics, tracker) for the changed case.

---

## Acceptance Criteria

- [ ] The prompt of the pre-flight judge names the repository slug when the caller sends an empty repository context.
- [ ] A `reject_out_of_scope` verdict is an accept when the body has one or more links to the project repository and no link to another GitHub repository.
- [ ] A body with a link to another repository keeps the rejection.
- [ ] Each other rejection (`reject_vague`, `reject_question`, `reject_conflicting`, `reject_stale`) is unchanged.

---

## Route

### Phase 1: the fix
- [x] 1. The judge gets the repository slug, and a same-repository link is never "external" (#6456)

### Waits for you
- [ ] Nothing.

---

## Implementation

### Phase 1: the fix
- [x] **J1** (PR#6483 merged 11-18 04:44Z) ([#6456](https://github.com/qf-studio/pilot/issues/6456), dispatched 11-17 ~20:20Z) the shim builds a repository context from the slug; a guard turns a `reject_out_of_scope` verdict into an accept when all GitHub links of the body name the project repository; tests.

**Files**:
- `cmd/pilot/poller_github.go` - the shim
- `cmd/pilot/poller_github_test.go` - tests

## Legs — pilot (daemon)

### LEG J1 — repo: qf-studio/pilot — title: fix(poller): pre-flight judge — the prompt names the project repository, and a reject_out_of_scope verdict for a body with links to that repository only becomes an accept

probe: present qf-studio/pilot sdkPreFlightJudge\)[[:space:]]JudgeIssue\(
probe: present qf-studio/pilot func[[:space:]]autopilotFixFooterVerdict\(
probe: present qf-studio/pilot func[[:space:]]newPreFlightJudge\(
probe: absent  qf-studio/pilot func[[:space:]]onlyOwnRepoLinks\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-655. The poller of the SDK asks a judge whether an issue is fit for a run. The shim is the method JudgeIssue of sdkPreFlightJudge in `cmd/pilot/poller_github.go`. It first handles CI-fix issues without a judge call, then calls the judge of the executor package (IntentJudge.JudgeIssue in `internal/executor/intent_judge.go`) and maps the verdict. The SDK always sends an empty repoContext, and the judge adds the section "Repository Context" to its prompt only when that value is not empty. So the model sees only the title and the body. On 2026-11-16 it declined an issue that the daemon had filed in its own repository: decision `reject_out_of_scope`, reason "requires working with the external repository" (the body had full links to a PR of the same repository). The struct sdkPreFlightJudge has the field repoFullName (`owner/name`), which newPreFlightJudge sets.

Keep the change small: only the items below, only in the two files named in the scope. Do not change the SDK, the judge prompt constant, or the CI-fix bypass.

## Scope

1. In the shim: when repoContext is empty and repoFullName is not empty, the shim passes this text as repoContext: "This issue is filed in the GitHub repository OWNER/NAME. The code of that repository is available to the developer. Links to github.com/OWNER/NAME, and issue or PR numbers such as #123, point to this repository and are not external resources." (with the real slug). A non-empty repoContext of the caller is passed on with no change.
2. A function onlyOwnRepoLinks(body, repoFullName) that reports true when the body has one or more links of the form `github.com/OWNER/NAME` to the project repository (no case difference) and no link of that form to another repository. A body with no such link gives false.
3. In the shim, after the judge call: when the decision is `reject_out_of_scope` and onlyOwnRepoLinks is true, the shim returns an accept verdict with the reason "links point to the project repository" and writes one Warn line with the title and the reason text of the judge. The success record of the tracker stays as today. Each other decision is mapped as today.
4. Tests in `cmd/pilot/poller_github_test.go`, with a fake judge command (a shell script given to executor.NewIntentJudge, as TestSdkPreFlightJudge_JudgeIssue_TimeoutIncrementsMetricOnce does; the script writes its arguments and its standard input to a file and prints a verdict in the format that the judge parses): a test asserts that the prompt holds the slug when the caller sends an empty value, and that a non-empty value of the caller arrives with no change; a table test for onlyOwnRepoLinks (own links only; own and foreign; foreign only; no link; a slug that is a prefix of another slug, such as `qf-studio/pilot` against `qf-studio/pilot-console`; upper case in the host or the slug); a test that a `reject_out_of_scope` verdict with own links only is accepted and the Warn line is written; the same verdict with a foreign link stays rejected; a `reject_vague` verdict with own links only stays rejected.

## Acceptance

- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'PreFlight|OwnRepoLinks'` passes; paste the output into the PR body.
- [ ] Remove the repoContext assignment -> the test for the slug fails.
- [ ] Make onlyOwnRepoLinks match by prefix -> the row with `qf-studio/pilot-console` fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The SDK. A working directory for the judge subprocess. A confidence threshold for rejections. A bypass of the judge for issues that the daemon files. The text of preflightJudgeSystemPrompt. Any file other than the two named above.

## Refs

- Navigator TASK-655 in this repo · issue 6402 (the declined issue)

---

## Out of Scope

- A change in the SDK so that the poller sends a repository context itself.
- A working directory for the judge subprocess (the path is unknown at the SDK boundary, and a checkout gives the judge tools it does not need).
- A confidence floor for rejections.
- A bypass for issues that the daemon files (the documented decision keeps them judged).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the context is added | SDK poller; shim; judge | Shim | The shim has the slug; no SDK release needed; about 15 lines |
| Guard for the false "external" case | None (prompt only); deterministic guard; bypass by label | Prompt plus guard | The prompt removes the cause; the guard makes the exact case impossible; a bypass by label breaks the rule that review issues stay judged |
| Guard needs one or more own links | Also accept a body with no link | One or more own links, no foreign link | A body with no link gives no evidence that the model was wrong |

---

## Verify

```bash
go test -race -count=1 ./cmd/pilot/ -run 'PreFlight|OwnRepoLinks'
```

---

## Done

- [ ] J1 merged.
- [ ] The next daemon-filed review issue with links to its own repository is not declined as out of scope.

---

## Refs

- Issue 6402 (declined 2026-11-16, cut and started again by hand 2026-11-17)
- Research report of 2026-11-17 (navigator-research, in the session)

---

**Last Updated**: 2026-11-17

## Refs

- J1 → https://github.com/qf-studio/pilot/issues/6456
