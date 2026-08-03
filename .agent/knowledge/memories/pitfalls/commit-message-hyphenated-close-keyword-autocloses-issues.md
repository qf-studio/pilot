---
name: commit_message_hyphenated_close_keyword_autocloses_issues
description: A hyphenated word ending in close/fix/resolve immediately before an issue ref in a commit message triggers GitHub auto-close — "compare-before-close #4679" is parsed as "close #4679"
type: pitfall
created: 2026-08-03
---

# Hyphenated `-close`/`-fix` before `#N` in a commit message auto-closes the issue

**What happened (2026-08-03).** A docs commit summarising newly-filed issues used the
phrase `compare-before-close #4679 filed`. GitHub's closing-keyword parser matched the
trailing word `close` followed by ` #4679` and **closed issue #4679 on push** (commit
`69ea300b`), seconds after it was created and with zero work done on it. In the same
message `cancel-gap #4678` was harmless (`gap` is not a keyword) and `#4655`/`#4677`
survived (no adjacent keyword).

The closure is silent from the author's side: no comment, `stateReason: COMPLETED`, and
the only trace is the GitHub *timeline* (`gh api repos/OWNER/REPO/issues/N/timeline`),
which names the commit as the actor. The daemon log shows nothing — it was not Pilot.

**Keywords that trigger it** (any case, and the parser matches the trailing word of a
hyphenated compound): `close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`,
`resolves`, `resolved`.

**How to apply.**

- When a commit message mentions an issue you are NOT closing, never let a keyword be the
  word immediately before the ref. Reword (`compare-before-close guard (#4679)` still
  risks it — prefer `#4679 compare-before-close hardening` or `issue 4679`) or drop the
  `#`.
- Watch for hyphenated technical phrases that end in a keyword: `compare-before-close`,
  `close-not-escalate`, `conflict-fix`, `auto-resolve`. These read as ordinary prose and
  are easy to miss on review.
- After pushing a commit that references several issue numbers, verify their states —
  a wrongly-closed issue is invisible until someone queries it.
- To find the culprit: `gh api repos/<owner>/<repo>/issues/<n>/timeline` and look for a
  `closed` event carrying a `commit_id`.

Related: [[pilot_issue_missing_no_decompose_fragments_single_fix]] ·
`.agent/tasks/TASK-437-duplicate-execution-race-prevention.md`
