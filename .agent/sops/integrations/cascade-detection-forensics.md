# Cascade Detection & Recovery Forensics (SOP)

> Born from OAuth cascade #2 (2026-05-04) — codified so future on-call can
> short-circuit the 3+ hours of triage we burned.

## 1. Detection signals — when to suspect a cascade

| Signal | What to look for |
|--------|-----------------|
| Title flood | Many issues with identical or templated titles created in < 30 min |
| Empty sub-issues | Sub-issue body is literal `Parent: GH-N` with no other content |
| Uniform PR prefix | PR titles all share the same `feat(scope):` prefix despite unrelated issue bodies |
| Ghost merge | `gh pr view --json mergedAt` returns `null` but `git log origin/main` has the commit SHA (squash-merge artifact — see `pattern_squash_merge_mergedat_null.md`) |
| Autopilot chain | `fix(ci)` issues spawning for a PR that was never real work |

If **two or more** signals are present simultaneously, treat it as a cascade.

## 2. First action: kill the daemon

Stop the bleeding before doing any forensics.

```bash
pkill pilot
ps aux | grep -E "[p]ilot start"   # confirm dead — output must be empty
```

Do NOT try to be clever (pause, skip, re-label). Kill first. Investigate second.

## 3. Queue inspection

Check what Pilot has already touched:

```bash
# Issues currently held by Pilot
gh issue list --repo qf-studio/pilot --label pilot-in-progress --state open

# Issues created in the last 24h that share a suspicious scope
gh issue list --repo qf-studio/pilot \
  --search "feat(auth) in:title is:open created:>$(date -u -v-1d +%Y-%m-%d)" \
  --limit 50

# PRs that reference GH- numbers in their body (likely autopilot-spawned)
gh pr list --repo qf-studio/pilot --state all --search "GH- in:body" --limit 20
```

Look for clusters: identical title prefixes, sub-issues pointing at the same parent, PRs with overlapping file diffs.

## 4. Executions table inspection

```bash
sqlite3 ~/.pilot/data/pilot.db \
  "SELECT id, task_id, status, datetime(created_at,'localtime') FROM executions
   WHERE created_at > datetime('now','-2 hours') ORDER BY created_at DESC;"
```

A cascade typically shows 5–20+ rows with `status='running'` or `status='completed'` against phantom task IDs clustered within a few minutes.

## 5. Ghost row cleanup — for issues being unstuck

If a legitimate issue is stuck because a phantom execution row marked it `completed`:

```bash
# Identify the stuck task_id first (e.g. GH-2345)
sqlite3 ~/.pilot/data/pilot.db \
  "SELECT id, task_id, status FROM executions WHERE task_id='GH-2345';"

# Delete only the ghost row
sqlite3 ~/.pilot/data/pilot.db \
  "DELETE FROM executions WHERE task_id IN ('GH-N','GH-M') AND status='completed';"
```

Cross-reference `bug_ghost_close_db_lockout.md` memory before deleting — confirm no real PR exists for that task.

## 6. Cleanup of cascade artefacts

Work through the issue list from §3 and for each phantom issue:

```bash
# Close as not-planned (replace NNN with issue number)
gh issue close NNN --repo qf-studio/pilot --reason "not planned" \
  --comment "Cascade artefact — closing as not planned."

# Strip cascade labels
gh issue edit NNN --repo qf-studio/pilot --remove-label pilot
gh issue edit NNN --repo qf-studio/pilot --remove-label pilot-in-progress
```

After cleaning up:

```bash
# Verify the pilot queue is empty
gh issue list --repo qf-studio/pilot --label pilot --state open
```

Output must be empty (or contain only pre-cascade legitimate issues).

## 7. Smoke test before resuming

Do NOT restart Pilot until this passes.

1. File one tiny, clearly-scoped issue with no relation to the cascade topic:
   ```
   Title: chore(memory): trivial comment cleanup
   Body:  Add a one-line comment to internal/memory/store.go. Tiny test.
   ```
2. Restart daemon (see §8).
3. Label the smoke issue with `pilot`.
4. Watch the first sub-issue or PR that Pilot creates:
   - Scope must match `chore(memory)`, NOT `feat(auth)` or the cascade scope
   - No phantom glyphs or inherited prefixes in the title
   - Quality gate must fire (self-review comment visible on PR)
5. If the smoke issue produces cascade-scoped output, DO NOT proceed — the
   prompt leak is still present. Follow `prompt-leak-fix-checklist.md`.

## 8. Restart sequence + monitoring

```bash
# Restart Pilot
pilot start --dashboard --github --env stage

# Tail logs (alternative to watching the TUI dashboard)
tail -f ~/.pilot/data/logs/pilot.log
```

Watch for:
- Normal poller cadence (one `polling` log line per interval)
- No burst of `dispatching` lines within seconds of start
- Smoke issue transitions: `open → pilot-in-progress → pilot-done`

## 9. Cross-references

| Resource | Role |
|----------|------|
| `incident_oauth_cascade_series.md` (memory) | Full incident write-up for cascade #1 and #2 |
| `prompt-leak-fix-checklist.md` (sister SOP) | The *fix* side — how to patch leaky prompts; this SOP is the *detect/recover* side |
| `feedback_check_all_prompts_for_leaks.md` (memory) | Rule: scan every embedded prompt, not just the one you found |
| `pattern_squash_merge_mergedat_null.md` (memory) | Why `mergedAt: null` does not mean "not merged" after a squash |
| `bug_ghost_close_db_lockout.md` (memory) | Ghost `completed` rows that silently block re-dispatch |
