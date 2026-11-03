# Pitfall: Two console legs dispatched together that each add a migration pick the same nex

## Summary
Two console legs dispatched together that each add a migration pick the same next number: C10 (console#460) merged 0039_chat_turn_cap_50 at 16:17Z while C5 (console#459, cut earlier from 3b012c2) also used 0039 → every DB test failed on the merge ref with 'duplicate migration file: 0039_drafts.down.sql' and the size guard refused a fix issue (1149 prod additions). Fix was a hand-filed revision renumbering to 0040 (console#465). Rule: when two legs with migrations go out at once, name the migration number in each leg body or serialize them (dispatch the second after the first merges).

## Context
Discovered during development.

## Details
Two console legs dispatched together that each add a migration pick the same next number: C10 (console#460) merged 0039_chat_turn_cap_50 at 16:17Z while C5 (console#459, cut earlier from 3b012c2) also used 0039 → every DB test failed on the merge ref with 'duplicate migration file: 0039_drafts.down.sql' and the size guard refused a fix issue (1149 prod additions). Fix was a hand-filed revision renumbering to 0040 (console#465). Rule: when two legs with migrations go out at once, name the migration number in each leg body or serialize them (dispatch the second after the first merges).

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 95%
**Concepts**: issue-authoring, database, dispatch, ci
