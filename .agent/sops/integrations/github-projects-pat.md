# SOP: GitHub Projects V2 board — auth token & board provisioning

**Category:** integrations
**Applies to:** driving Pilot work from a GitHub Projects V2 board (`qf-studio/projects/1`, Studio SDK)
**Related:** TASK-319 (board-driven lifecycle loop), TASK-317 / GH-3228 (`FindIssuesFromProject` read path)
**Last updated:** 2026-05-29

---

## 1. The board token (already provisioned — document, do not recreate)

Pilot drives the board with the **existing** classic PAT already configured.

| Property | Value |
|---|---|
| Location | `~/.pilot/config.yaml` — GitHub adapter token field (the one Pilot already reads) |
| Type | classic `ghp_` PAT |
| Scopes | `project`, `read:org`, `repo` |
| Decision | **one token for everything** — no separate `GITHUB_PROJECT_TOKEN` |

What each scope buys:

- **`project`** — board sourcing (`FindIssuesFromProject`) + status-column writes (`UpdateProjectItemStatus`).
- **`repo`** — issues, PRs, contents the executor already needs.
- **`read:org`** — org-level project resolution (`qf-studio/projects/1` resolves via the org, not a user).

**Rotation:** when the PAT expires, regenerate at github.com/settings/tokens (same scopes), update the token field in `~/.pilot/config.yaml`, and restart the daemon. The daemon reads the token from config — there is no env var to update.

**Dev-CLI caveat:** the interactive `gh` CLI uses a keyring `gho_` token that **lacks** project scope (`gh project list --owner qf-studio` → 402 / missing-scope). This does **not** affect the Pilot daemon (it reads the `ghp_` PAT from config). For direct `gh project ...` work in a terminal, either:

```bash
gh auth refresh -s read:project,project     # add scopes to the CLI token
# or
export GH_TOKEN=<the ghp_ PAT>              # use the PAT for this shell only
```

---

## 2. Board provisioning (do once, before go-live)

`qf-studio/projects/1` currently has **3 Status columns**: `Todo | In Progress | Done`.
The full lifecycle loop (Option B) needs **5** — add `In Review` and `Blocked`.

### Recommended — web UI (zero risk)

1. Open <https://github.com/orgs/qf-studio/projects/1>.
2. Status field → settings → **+ Add option**:
   - **In Review** — color **orange** — place **between** `In Progress` and `Done`.
   - **Blocked** — color **red** — place **last**.
3. This preserves all existing items (the UI is additive).

### Alternative — GraphQL (⚠ use only when scripting board setup)

`updateProjectV2Field` **rewrites the entire option set** and can detach items from their
columns. If you must script it, pass **all** options (existing + new) in the desired order.

```
Project ID : PVT_kwDOD34yzs4BZIGz
Status field ID : PVTSSF_lADOD34yzs4BZIGzzhUJB8o
Existing options:
  Todo        (GREEN,  "This item hasn't been started")
  In Progress (YELLOW, "This is actively being worked on")
  Done        (PURPLE, "This has been completed")
```

Prefer the UI unless you are intentionally automating board provisioning.

### Board hygiene

- Move any stale **CLOSED** item out of `In Progress` → `Done`.
- `Todo` starts empty — drop work items there once sourcing is live.

---

## 3. Config block

Full board-driven `project_board` block for `configs/pilot.example.yaml` /
`~/.pilot/config.yaml` (status names must match the board column labels exactly):

```yaml
adapters:
  github:
    repo: qf-studio/studio-sdk
    project_board:
      enabled: true
      project_number: 1
      status_field: Status
      source_enabled: true        # pull work FROM the board (GH-3228 read path)
      source_status: "Todo"
      statuses:
        in_progress: "In Progress"
        review: "In Review"
        done: "Done"
        failed: "Blocked"
```

Column → event mapping (Option B):

| Column | Pilot event | Write path |
|---|---|---|
| Todo | queued work | `source_status` (read, GH-3228) |
| In Progress | issue picked up / executing | write on dispatch (TASK-319 PR-1) |
| In Review | PR opened | write on `OnPRCreated` (TASK-319 PR-2) |
| Done | PR merged | write on merge (already wired) |
| Blocked | exec-fail / CI-fail | write on failure (TASK-319 PR-2) |

---

## 4. Go-live checklist

- [x] Board token present in `~/.pilot/config.yaml` (scopes `project, read:org, repo`).
- [ ] `In Review` (orange) + `Blocked` (red) columns added via web UI.
- [ ] Board hygiene done (stale items moved out of In Progress).
- [ ] Config block applied (`source_enabled: true`, full `statuses` mapping).
- [ ] TASK-319 write-back PRs merged (PR-1 #3252/#3253, PR-2 #3243, PR-3 idempotency).
- [ ] Smoke test: drop a test issue in `Todo` → confirm it flows
      `Todo → In Progress → In Review → Done`, and `→ Blocked` on a forced failure.
