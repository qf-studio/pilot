---
name: jev-identical-questions-return-identical-answers
description: Jev/TypeSafe questions with byte-identical instructions over a shared state return the same distribution for every item; name the item key and redacted text inside each instruction (found 2026-09-30, #5535)
type: pitfall
---

# Pitfall: Jev questions over a shared state must name their item — identical instructions return identical answers

## Summary
TypeSafe/Jev answers each question independently against the same state. N questions whose instructions are byte-identical (differing only by key `kind_1..kind_N`) get the same distribution for every item, because the question key is not visible to the model and the instruction is the only place it learns which state entry the question is about. Put the item key and the (redacted) item text inside every instruction.

## Context
2026-09-30 audit of the Jev acceptance classifier (#5520 / PR #5524) before the release train. Live call with 4 hand-labelled items: every `kind` answer was `mutation` at 0.65–0.69 probability, confidence 0.48–0.54, so all four counted `low_confidence`; the `target` question, whose options are item-specific, answered correctly at 1.00. Naming the index in the instruction gave 0.97–1.00 with all four correct; inlining the item text gave the same. The stream demo that produced the rubric carried the bullet id in every question; the port dropped it. Filed #5535.

## Details
Go fakes that ignore question content cannot catch this, and the eight mutation pins on the merge logic all passed while the classifier was blind. Shadow-mode counters would have reported 100% `low_confidence` and looked like a threshold problem. `min_confidence` 0.8 is correct once each question names its item.

## Recommended Approach
Build each instruction with `fmt.Sprintf` including the item key and the redacted item text; pin with a test that asserts instructions for two items differ and each contains its own index and text. For every new Jev gate, run one live smoke on 3–5 labelled items and inspect per-question probabilities before trusting shadow counters.

## Related
- TASK-505, #5535, mem-205 (Jev triage decision)
- internal/executor/acceptance_classifier_jev.go
