---
name: pilot_issue_missing_no_decompose_fragments_single_fix
description: A coherent single-PR fix dispatched without the no-decompose marker fragments into sequential children that each re-derive overlapping code and produce conflicting, individually-incoherent PRs
type: pitfall
created: 2026-08-03
---

# Missing `no-decompose` fragments a single-PR fix into conflicting siblings

**What happened (2026-08-03, GH-4655).** A ~100-line executor fix was filed with only
the `pilot` label — no `no-decompose` label and no `<!-- pilot:no-decompose -->` body
marker. The epic classifier decomposed it into **7 sequential children**, all editing
the same ~40-line region of `runner.go`'s epic decision block. Outcome: 4 PRs totalling
**~1,270 lines** for work that should have been ~100-200; two conflicted (later children
branched before earlier ones merged); one modified unrelated regression tests
(`gh3938_test.go`, `gh4405_test.go`) to accommodate its slice. Every PR had to be closed
and the whole fix re-filed as one `no-decompose` issue (#4677).

**Why it bites specifically here.** Children are dispatched sequentially but each branches
from whatever `main` looked like at ITS start, and each carries only `inherited-spec: true`
— no sibling awareness. When slices touch one region, textual conflicts are guaranteed for
anything after the first merge, and semantic coherence is nobody's job: no single session
or reviewer ever sees the whole change.

**How to apply.**

- Any fix that is *one coherent change to one region* MUST carry BOTH the `no-decompose`
  label AND the body marker `<!-- pilot:no-decompose -->` plus the sentence
  "This task must NOT be decomposed — implement as a single PR."
- Decomposition is for genuinely separable work (different packages/files/concerns), not
  for splitting one function's edit into "add helper" / "call helper" / "handle case A" /
  "handle case B" — that ordering is exactly what conflicts.
- Planning is **non-deterministic**: the same issue text planned 1 subtask on one run and
  2 on the next (GH-4648 gen-0 vs gen-1). Never rely on "it's small, it won't decompose."
- When authoring a batch, check the marker on every issue before dispatch — in the
  incident, sibling issues filed minutes earlier for other repos DID carry it; only the
  pilot-repo ones were missed.
- Salvage guidance: when fragmentation has already happened, prefer closing all partial
  PRs and re-filing one `no-decompose` issue over rebasing/merging the fragments. Merging
  a mergeable subset lands half-wired intermediate states no reviewer validated.

Related: [[pilot_stalled_status_is_retry_not_cancel]] ·
`.agent/tasks/TASK-437-duplicate-execution-race-prevention.md` ·
`.agent/sops/onboarding/new-project-issue-authoring.md`
