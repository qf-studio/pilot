# feat(fleet): move the unscoped store behind an explicitly-named type so org scoping is genuinely compiler-enforced

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-08-27
**Last Updated**: 2026-08-27
**Target repo**: qf-studio/pilot-console
**Follows**: qf-studio/pilot-console#229 → PR#230 (merged; reviewed REQUEST-CHANGES)

## Context

Compiler-enforced org scoping is now the **primary** tenant-isolation control for the console, following the 2026-08-27 decision to demote row-level security to a dormant, deferred second layer. There is no working backstop underneath it: two attempts at row-level security both shipped inert.

PR#230 delivered part of this. It added a scoped handle and narrowed the interfaces two consumer packages depend on, plus a genuine runtime test that the scoped queries carry an org predicate. That is real progress.

But it added the scoped handle **alongside** the unscoped API rather than in place of it, so the compiler does not yet enforce anything at the store boundary. `SetDesiredState` — the write that terminates real AWS instances — is unchanged and still filters on the row id alone, with no org predicate. The raw database handle remains reachable throughout the package. Consequently an unscoped org query is still trivially writable: any package holding the store can call the raw-id methods with no org at all; a new method added inside the fleet package can use the raw handle exactly as before; and scoping by an org id read off the row you are about to check type-checks while enforcing nothing.

The escape hatch is also the shortest, most obvious spelling — the accidental path and the deliberate cross-org path are named identically today.

## Task

**Split the raw handle behind a separately-named type.** The store should expose the org-scoped constructor and an explicit cross-org accessor — something a reader immediately recognises as deliberate — with the raw-id methods and the database handle moved onto that second type. Update the background callers that legitimately work across orgs to go through it.

That single move satisfies two things at once: writing an org-unscoped query from handler-facing code becomes a compile error, and cross-org access acquires a name that stands out in review. Roughly fifteen exported raw-id methods are involved, including the desired-state and observed-state writes, the config-generation and config-spec methods, the wake-hold methods, and the event append and list methods. Audit the file rather than working from that list.

**Stop scoping by an org id read off the row being checked.** Two call sites in the instances handlers construct the scope from the fetched row's own org rather than from the caller's. That is a tautology which type-checks and proves nothing. The deprovision and events handlers already thread the caller-derived scope correctly — follow that shape.

**Make the documentation claim precise.** The package comment currently states that writing an org-unscoped fleet query from handler-facing code is a compile error. That is true only for the two narrowed interfaces, not for anyone holding the store directly. Given the row-level-security history, this claim should be exactly true or not made.

**Add a cheap CI backstop.** Nothing currently prevents re-widening the narrowed interface — adding one line back re-opens it silently — or adding a new raw-id method. A grep-level check in the same style as the repository's existing guards is enough while the type split lands.

**Do not** delete the existing handler-level org comparisons. They remain the working protection until the type split is proven.

## Acceptance

- A query touching org-scoped fleet data cannot be written from handler-facing code without supplying a caller-derived org — demonstrated by the raw handle and raw-id methods being unreachable from the type handlers hold.
- Cross-org access is available only through an explicitly-named accessor, and that accessor is not the default spelling.
- The desired-state write carries an org predicate when reached through the scoped path.
- No call site constructs a scope from the org of the row it is about to check.
- A CI check rejects a new raw-id method or a re-widened consumer interface.
- The package documentation states exactly what is enforced and what is not.
- Existing handler-level checks and their tests remain; existing fleet tests pass.

## Out of scope

The board and orgs packages — those follow once this pattern holds. Do not change the row-level-security migrations.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot-console/issues/232

- Reviewed PR: qf-studio/pilot-console#230
- Companion: qf-studio/pilot-console#218 (row-level security — deferred, see TASK-487)
