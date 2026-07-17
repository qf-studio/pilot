# SOP: never let an LLM judge see its own truncation marker as evidence

**Category:** Quality / LLM-as-judge prompt construction
**Implemented:** 2026-07-17
**Source incident:** GH-4407 (intent judge vetoed GH-15 and GH-12 at
0.85-0.95 confidence, citing the `...[truncated]` marker *we* injected as
proof the implementation was "incomplete" or "missing")

## Problem

`IntentJudge.Judge` (internal/executor/intent_judge.go) truncated large git
diffs with a single global char cutoff (`diff[:maxChars] + "...[truncated]"`)
before sending them to the Haiku judge model. For any diff over the cap
(previously 8000 chars — roughly 150 changed lines), the cutoff usually fell
mid-file, sometimes before any real content of files late in the diff order
appeared at all.

The judge model then read the literal string `[truncated]` in its own
context and reasoned from it: "this content isn't shown, therefore it
doesn't exist" — and issued a high-confidence FAIL, citing the marker
itself as the reason. This scales inversely with task size: the bigger and
more legitimate the PR, the more likely it hits the cap, the more likely it
gets falsely vetoed.

## Root cause

Any prompt that (a) truncates input data and (b) asks an LLM to judge
*completeness* of that data has this failure mode by construction — a
truncation marker is indistinguishable from "absence of the thing" unless
you tell the model otherwise, AND give it a way to independently confirm
scope.

## Fix pattern (applies to any LLM-judge prompt, not just this one)

1. **Never drop a unit to zero visible content.** If you must truncate a
   multi-file diff to a budget, split the budget per-file first
   (`splitDiffByFile` + `buildJudgeDiffPayload` in intent_judge.go) so every
   file keeps a floor of visible content (`minPerFileDiffChars`), instead of
   letting the first file(s) in the diff exhaust the whole budget.
2. **Ship an out-of-band manifest that is never truncated.** A short
   "## Changed Files (N total)" list with every path + stat line costs
   almost nothing and gives the judge a scope-of-record independent of the
   (possibly abbreviated) diff body.
3. **Say so in the system prompt, explicitly and by name.** Tell the judge
   the exact marker string means "omitted for length," not "missing," and
   forbid citing the marker itself as a FAIL reason. Models will use
   whatever text is in front of them as evidence unless told not to.
4. **Prefer raising the cap over aggressive truncation when cheap.** Haiku's
   context window comfortably fits diffs an order of magnitude larger than
   the old 8000-char cap; raise the default before reaching for cleverer
   truncation. `IntentJudgeConfig.MaxDiffChars` (backend.go) is the operator
   knob — default raised to 32000.

## Prevention checklist for new LLM-judge prompts

- [ ] Does truncation ever fully drop a unit (file, section, item) the judge
      is asked to reason about? If yes, floor it instead.
- [ ] Is there an always-complete manifest/summary the judge can fall back
      on when body content is cut?
- [ ] Does the system prompt explicitly state what the truncation marker
      means and forbid citing it as evidence?
- [ ] Is the cap itself justified by the model's actual context window, or
      is it a stale/arbitrary number from an earlier, smaller model?
