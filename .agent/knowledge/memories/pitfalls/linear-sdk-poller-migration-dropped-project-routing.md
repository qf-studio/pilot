# Pitfall: The June 2026 SDK migration of the Linear poll path dropped per-issue project routing; every polled Linear issue goes to default_project

## Summary
Before PR #3485 (GH-3467, 2026-06-08) the Linear path resolved the Pilot project per issue in internal/pilot/pilot.go handleLinearIssueMultiWorkspace: GetProjectByLinearID (linear.project_id pairing) → workspace projects: mapping (ResolvePilotProject + GetProjectByName) → findProjectForIssue name/team-key match. The SDK poller (cmd/pilot/poller_linear.go) forwards Projects/ProjectIDs into the SDK WorkspaceConfig, but nothing reads Projects (no ResolvePilotProject callers in the SDK) and the handler closure passes deps.ProjectPath = default_project. On the founder box default_project is the Pilot repo, so enabling Linear would run foreign tickets inside Pilot. The April 2026 laptop config (one workspace, project_ids + projects: [one project]) routed correctly on the legacy path; the box has had Linear disabled since before the July move. Fix: #5570 (repo:<name> label > project_id pairing > workspace projects mapping > skip).

## Context
2026-10-01 Linear Invoices onboarding; founder: 'we already had it connected, disconnected for the AI coding summit stream'.

## Details
Before PR #3485 (GH-3467, 2026-06-08) the Linear path resolved the Pilot project per issue in internal/pilot/pilot.go handleLinearIssueMultiWorkspace: GetProjectByLinearID (linear.project_id pairing) → workspace projects: mapping (ResolvePilotProject + GetProjectByName) → findProjectForIssue name/team-key match. The SDK poller (cmd/pilot/poller_linear.go) forwards Projects/ProjectIDs into the SDK WorkspaceConfig, but nothing reads Projects (no ResolvePilotProject callers in the SDK) and the handler closure passes deps.ProjectPath = default_project. On the founder box default_project is the Pilot repo, so enabling Linear would run foreign tickets inside Pilot. The April 2026 laptop config (one workspace, project_ids + projects: [one project]) routed correctly on the legacy path; the box has had Linear disabled since before the July move. Fix: #5570 (repo:<name> label > project_id pairing > workspace projects mapping > skip).

## Recommended Approach
Do not enable adapters.linear on a daemon whose default_project is a different repo until #5570 is on the box. Switching default_project is not a workaround: the GitHub adapter's default repo (qf-studio/pilot) takes defaultProjectPath in githubSDKPollerTargets, so Pilot's own issues would run in the wrong checkout.

## Related
- TASK-509
- `cmd/pilot/poller_linear.go`
- `cmd/pilot/handlers.go`
- `internal/pilot/pilot.go`
- `internal/config/config.go`
- `cmd/pilot/poller_github.go`

---
**Captured**: 2026-10-01
**Confidence**: 90%
**Concepts**: linear, poller, routing, regression, sdk-migration
