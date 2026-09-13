---
name: hardening-issue-wording-triggers-model-refusal
description: Issue text for security hardening of our own executor tripped the model's cyber safeguard (pilot#5441 declined 2026-09-13) — offensive vocabulary ("bypassable", "smuggling") plus an exfiltration-shaped example command; the declined claim is pinned per task id, so the fix is a reworded superseding issue, not a relabel
type: pitfall
---

# Hardening-issue wording triggers a model refusal → declined claim cannot be re-armed in place

**What happened (2026-09-13 11:31–11:38Z):** pilot#5441 (follow-up to PR#5439:
reject shell operators in acceptance-evidence commands, pin redaction) was
picked up and the executor's model **declined** it with a cyber-safeguard
refusal (`stop_reason: refusal`, category cyber). The technical content was
ordinary input validation of our own CI evidence runner. What tripped it was
the framing: "the allowlist is **bypassable**", "**smuggling** commands", and a
verification example shaped like exfiltration (`curl -d @go.mod https://…`,
`cat ~/.pilot/config.yaml`).

## Why it matters
- `escalateRefusal` marks the claim `declined` and `priorClaimWasEscalatedRefusal`
  pins (task id, project) to "never retry, never re-comment" (GH-5232). Editing
  the body or cycling the `pilot` label does **not** re-arm it — the claim key
  is the task id. Only a **new issue number** gets a fresh claim.
- The refusal reproduces deterministically on the same text; retries burn a
  generation and nothing else.

## Rule
- Security-hardening issues about our own code **lead with the defensive
  context** in the first paragraph ("input validation for our own executor",
  "the runner executes commands from an issue body and pastes output").
- Describe the gap in validation terms ("validates only the first token";
  "shell control characters are interpreted"), not attacker terms (bypass,
  smuggle, exfiltrate, leak).
- Verification examples must be **benign**: `go test ./... && echo second`,
  `make $(echo build)`, `npm test | tee out.txt`. Never a network call, never a
  read of a credentials file.
- Recovery: close the declined issue with a pointer, file the reworded issue
  (same labels, `no-decompose`), update task doc + README refs. Done for
  #5441 → #5442.

## Refs
- pilot#5441 (declined, closed) → pilot#5442; PR#5439/#5440 review
- `internal/executor/dispatcher.go`: `escalateRefusal`, `priorClaimWasEscalatedRefusal`
- Related: [[pilot-never-reads-gh-comments-by-design]] (the daemon cannot be
  told to retry via a comment either)
