---
name: graphql-mock-tests-cannot-catch-schema-validation
description: httptest-mocked GraphQL tests validate nothing about the query document — PR#4978 (Linear GetLabelByTeamID) shipped green with $teamId declared String! in an ID position; the live API rejects it GRAPHQL_VALIDATION_FAILED before auth, making the merged fix a functional no-op for its target scenario (#4884 symptom persists). Review caught it only by replaying the exact document against api.linear.app. Fix: #4985. Rule — GraphQL changes need either schema-validated tests (gqlparser against a vendored schema) or a test pinning the exact variable declarations.
type: pitfall
created: 2026-08-19
---

# GraphQL mocks ship no-ops green: variable declarations are only validated by the real server

**What happened.** PR#4978 (GH-4965, TASK-481 Leg A) added a UUID branch to
Linear `GetLabelByName`: `team: { id: { eq: $teamId } }` with the variable
declared `$teamId: String!`. Linear's schema types that position `ID`
(`IDComparator.eq`), and GraphQL variable-position rules do NOT coerce
`String!` into an `ID` location. The live API returns HTTP 400
`GRAPHQL_VALIDATION_FAILED` before even checking auth — so for the exact
scenario the PR targeted (UUID-configured `team_id`), `GetLabelByName` errors
on every call, `GetOrCreateLabel` falls through to `CreateLabel`, and the
original #4884 duplicate-label symptom persists unchanged. One token
(`String!` → `ID!`) is the whole fix (#4985).

**Why every gate missed it.** The tests assert only which operation name
appears in the request body against an httptest mock; the mock happily accepts
any document. Quality gates, CI, intent judge, self-review — all green,
because nothing in the pipeline speaks GraphQL schema. The post-merge review
caught it only by replaying the PR's exact document against api.linear.app
(rejected pre-auth, so no credentials needed for the probe).

**Rules.**
1. A GraphQL query/mutation change is NOT verified by mock-transport tests.
   Either validate outgoing documents against a vendored schema (gqlparser)
   or, at minimum, pin the exact variable declaration text in the test.
2. Reviewing a GraphQL change: replay the document against the live endpoint —
   schema validation errors return before auth, so this is free and safe.
3. Same family as the argument-discarding-mock gate (#4900) and
   [[external-fork-pr-sweeps-stale-agent-state]]-era review discipline: a green
   test proves what the test checks, not what the change claims.
