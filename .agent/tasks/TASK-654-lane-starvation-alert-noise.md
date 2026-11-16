# TASK-654: Lane starvation alert — count only what the poller would pick, name the issues, one cooldown per lane

**Status**: 🚀 L1 DISPATCHED 2026-11-16 → [pilot#6391](https://github.com/qf-studio/pilot/issues/6391) (`pilot` + `no-decompose`). The cause of the wrong count is unknown; L1 makes the alert name its issues.
**Created**: 2026-11-16
**Assignee**: Pilot

---

## Context

**Problem**: the alert rule lane_starvation sends "Lane REPO has N open pilot-labeled issue(s) but nothing queued/running for M consecutive poll cycles" to Slack every 2 hours. On 2026-11-16 it named a repository with 2 open issues of which neither has the trigger label (checked on GitHub and with the daemon's own token). The count code filters by label, and a scratch test with those two issues gives 0 for every label value, so the cause of the count is unknown. The alert does not say which issues it counted, so nobody can check it. The box log shows the rule firing every 2 hours since 2026-10-01.

**Known defects on the same path** (navigator-research, 2026-11-16):

- The count includes pull requests that carry the label, and issues that the poller never admits (`pilot-needs-human`, `pilot-spec-incomplete`); only `pilot-blocked` is skipped.
- The cooldown of the alert engine is keyed by rule name, so one lane that fires every 2 hours hides every other lane.
- No test covers "open issues exist and none carries the label".

**Goal**: an alert that is true or that shows why it is not: it counts only issues that the poller would admit, it names them, and each lane has its own cooldown.

---

## Known Pitfalls & Patterns

- **PITFALL** (`task-stuck-and-lane-starvation-fire-on-terminal-and-needs-human`, 2026-09-06): the same rule counted an issue with `pilot-needs-human`; the fix was a config cooldown, the code fix was never filed. → L1 item 1 uses the poller's own exclusion rule.
- **LEARNING** (mem-304): a spec states the fallback and the limits. → L1 item 2 says what the alert shows when the list is long.

---

## Acceptance Criteria

- [ ] An open pull request with the trigger label and an issue with `pilot-needs-human` do not count.
- [ ] The alert text and one Info log line name the issue numbers that were counted.
- [ ] Two lanes that starve inside one cooldown window both alert.
- [ ] After the release, the next alert for the repository of 2026-11-16 either stops or names its issues.

---

## Implementation

### LEG L1 — repo: qf-studio/pilot — title: fix(autopilot): lane starvation alert — count only issues the poller would admit, name the counted issues, one cooldown per lane

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]reconcileLaneStarvation\(
probe: present qf-studio/pilot EventTypeLaneStarvation
probe: absent  qf-studio/pilot open_issue_number[s]

## Context

The alert rule lane_starvation tells the operator that a repository has open issues with the trigger label and nothing in the queue. reconcileLaneStarvation in `internal/autopilot/lane_starvation.go` lists the open issues with the label, skips those with `pilot-blocked`, and sends an event with the count when nothing is queued or running. The alert engine in `internal/alerts/engine.go` keeps the time of the last alert per rule name (lastAlertTimes), so the cooldown is common to all lanes.

On 2026-11-16 the alert reported 2 open issues for a repository whose 2 open issues do not carry the label. The code as read gives 0 for that case, so the cause is unknown; the alert does not name the issues it counted. Known defects: a pull request with the label counts (the list call returns pull requests; the PullRequest field of the issue is not checked); issues that the poller never admits count (`pilot-needs-human`, `pilot-spec-incomplete`); one lane hides the others through the common cooldown.

## Scope

1. Count rule. reconcileLaneStarvation counts an item only when it is an issue (not a pull request), it carries the trigger label, and the GitHub poller would admit it. Use the exclusion rule of the poller in `cmd/pilot/poller_github.go` through one exported function or one exported label list, so that the two places cannot differ; do not copy the label names.
2. Evidence. The event metadata gets open_issue_numbers: the numbers of the counted issues in ascending order, at most 10, then the text "+N more". The alert text shows them. reconcileLaneStarvation writes one Info line when the streak reaches the alert threshold and then once for each alert that fires, with the repository, the streak, the numbers and, for each number, its labels.
3. Cooldown per lane. For this rule the alert engine keys the cooldown on the rule name and the project of the event. Other rules keep their key. A restart keeps the behaviour that the stored alert rows give today.
4. Tests, table-driven: two open issues without the trigger label give no event; an open pull request with the label gives no event; an issue with the label and `pilot-needs-human` gives no event; an issue with only the label gives one event whose metadata names its number; 12 counted issues give 10 numbers and "+2 more"; two lanes that starve inside one cooldown window both alert, and one lane alerts once inside the window.

## Acceptance

- [ ] `go test -race -count=1 ./internal/autopilot/ ./internal/alerts/ ./cmd/pilot/ -run 'LaneStarvation|Cooldown|PollerAdmit'` passes; paste the output into the PR body.
- [ ] Count a pull request again -> its test fails.
- [ ] Key the cooldown on the rule name only -> the two-lane test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The task_stuck rule. The thresholds and the cooldown values of the config. A change of what the poller admits.

## Refs

- Navigator TASK-654 in this repo

---

## Out of Scope

- The root cause of the count of 2026-11-16, until the new alert text names the issues.

---

## Verify

```bash
go test -race -count=1 ./internal/autopilot/ ./internal/alerts/ -run 'LaneStarvation|Cooldown'
```

---

## Done

- [ ] L1 merged and released to the box.
- [ ] The next alert for the repository of 2026-11-16 is checked against its issue numbers; a follow-up is filed when the count is still wrong.

---

**Last Updated**: 2026-11-16

## Refs

- L1 → https://github.com/qf-studio/pilot/issues/6391
