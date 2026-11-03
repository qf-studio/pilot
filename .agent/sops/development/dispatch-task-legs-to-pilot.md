# SOP: Dispatch a Navigator task to Pilot (leg blocks + nav-dispatch-legs.py)

**Category:** development
**Created:** 2026-11-03
**Trigger:** A task doc in `.agent/tasks/` is ready and the user says "dispatch TASK-NNN", "hand off leg X", or "file the legs".
**Supersedes:** the nav-pilot skill's "file the whole doc with --body-file" flow. That flow is for a single-leg doc with no probes; this repo does not use it since 2026-10-22 (TASK-582/583, 18 issues, 4 repos).

## Why this exists

The task doc is a plan for the human. The issue body is a spec for the executor. They are not the same text: a doc holds design, decisions, pitfalls and several legs; an issue holds one leg in the shape the daemon's spec validator, path gate, epic detector and evidence gate expect (SOP `onboarding/new-project-issue-authoring.md` Rules 2, 3b, 3c, 3d, 5b, 5d). `scripts/nav-dispatch-legs.py` cuts the legs out of the doc, pre-flights them against `origin/main`, files them with `gh`, and writes the issue URLs back.

## 1. Doc shape

Put the legs under a **doc-level** heading that the script recognises as a boundary: `## Legs …`, `## Dispatched …`, `## Held …`. Each leg:

```
### LEG A1 — repo: qf-studio/pilot — title: feat(executor): <conventional-commit title>

## Context
<verified state on main; file paths in backticks only when they exist on origin/main; line numbers in prose ("near line 657"), never `path:line`>

probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnv\(
probe: absent  qf-studio/pilot BillingModeAPIKey[[:space:]]+BillingMode

Depends on: #{{A0}}

## Scope
<what changes, file by file; new files in prose ("new file, plain text: internal/x/y.go")>

## Acceptance
- [ ] `go test -race ./internal/x/ -run TestY` passes; paste the output into the PR body.
- [ ] <change> -> TestY_Case fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope
<one paragraph>

## Refs
- #NNNN (request) · Navigator TASK-NNN in this repo
```

Rules the shape encodes:

- **Leg id** is letters + digits (`A1`, `C10`). **Body H2s** (`## Context`, `## Scope`, `## Acceptance`, `## Out of scope`, `## Refs`) belong to the issue. The leg ends at the next `### LEG` or at a doc-level `## Legs|Held|Refs|Dispatched|Known pitfalls` heading. So a leg's own `## Refs` must be its last section, and the doc's `## Refs` must come after all legs.
- **Backticked paths**: every span with a `/` and a dot after the last slash is checked with `git cat-file -e origin/main:<path>`. A `path:line` span fails. Cross-repo paths go in plain text.
- **Probes** pin the premise and are stripped from the filed body. `present` needs a `git grep -E` hit on that repo's `origin/main`; `absent` needs none. Write `absent` probes so the probe line cannot match itself once the doc is committed: a Go symbol plus `[[:space:]]+` (`Name[[:space:]]+Type`), or anchored `^func`/`^const`. A bare identifier self-matches the task doc after the next docs commit.
- **Dependencies**: `Depends on: #{{ID}}` where ID is an earlier leg in the same run or one already in the doc's `## Refs`. Same repo only; a cross-repo dependency is a **held** leg, filed by hand after the upstream merges.
- **Acceptance dialect** (Rule 5b): paste-output bullets name one allowlisted command in the only backticks of the bullet; mutation pins use `->` and `fails`; no `|`, `;`, `&`, `>` inside a command span; no make build/test/lint re-run (Rule 5d).
- **Epic detector** (Rule 3c): any body with `##` headings and more than 200 words is decomposed unless the issue carries `no-decompose`. Every leg filed with this SOP is one PR, so every leg needs that label (see §3).

## 2. Pre-flight (dry run)

```bash
python3 scripts/nav-dispatch-legs.py .agent/tasks/TASK-NNN-slug.md \
  --checkout qf-studio/pilot=. \
  --checkout qf-studio/pilot-console=../pilot-console \
  --only A1 --dry-run
```

- One `--checkout owner/repo=path` per repo that a leg files to **or probes**. Run `git fetch origin main` in each checkout first; the script reads `origin/main`, not the working tree.
- `--only` takes a comma list of leg ids. Without it every leg in the doc is filed, in document order.
- A clean run prints one `[ID] repo: title` line per leg. `PRE-FLIGHT FAILED` lists every missing path, failed probe, unresolved `#{{ID}}` or already-filed leg; fix them all in one edit and re-run.

## 3. File

The script passes **one** `--label`. The daemon must see `pilot` and `no-decompose` together on first pickup, and the poller may pick the issue up before a second `gh issue edit` lands. File with the second label first, then add `pilot`:

```bash
python3 scripts/nav-dispatch-legs.py .agent/tasks/TASK-NNN-slug.md --checkout qf-studio/pilot=. --only A1 --label no-decompose
```

```bash
gh issue edit <N> --repo qf-studio/pilot --add-label pilot
```

The script writes `- A1 → https://github.com/owner/repo/issues/N` into the doc's `## Refs` (or into the `(filled by the dispatch script)` marker if present). Later `--only` runs resolve `#{{A1}}` from that block.

## 4. Record

By hand, in the same session:

1. Doc `**Status**` line: `🚀 A1 DISPATCHED <date> → [pilot#N](url) (`pilot` + `no-decompose`)`, plus what is held and why.
2. `.agent/DEVELOPMENT-README.md` task-table row: same one-liner.
3. Graph node for the task: `status: dispatched`, `issues: [url]`.
4. If the leg answers a user issue, comment the leg link on that issue.

## 5. Recovery

| Symptom | Cause | Fix |
|---|---|---|
| Issue filed, title only | A body H2 was mistaken for a doc boundary (fixed 10-22) or the leg sat after the doc's `## Refs` | Move the leg above the doc's `## Refs`; `gh issue edit --body-file` |
| "Task held: prerequisite not on main" | A backticked span is not a file on `origin/main` | Reword to prose; `gh issue edit` clears it next poll **only if no execution row escalated yet**; otherwise close and refile (mem-175) |
| Leg decomposed into sub-issues | `no-decompose` missing at first pickup | Kill the child, let the row go terminal, add `no-decompose`, cycle the `pilot` label |
| `absent` probe fails on a re-run | The probe matches the task doc itself on `origin/main` | Rewrite the probe with `[[:space:]]+` or `^func` anchoring |
| Evidence block empty in the PR | Acceptance bullets not in the dialect, or a second backtick in a paste-output bullet | Rule 5b; one command per bullet |

## Refs

- `scripts/nav-dispatch-legs.py` (docstring is the format reference)
- SOP `onboarding/new-project-issue-authoring.md` Rules 2, 3b, 3c, 3d, 5b, 5d
- Memories mem-264 (leg pattern), mem-273 (probes), `dispatch-leg-hygiene-test-names-shipped-symbols-refs-boundary`
- First dispatch under this SOP: TASK-631 A1 → pilot#6016 (2026-11-03)
