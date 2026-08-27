# fix(db): make row-level-security dormancy honest and stop the test suite leaking a login role

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-08-27
**Last Updated**: 2026-08-27
**Target repo**: qf-studio/pilot-console
**Decision context**: founder decision 2026-08-27 — "plan C": compiler-enforced org scoping becomes the primary tenant-isolation control; row-level security stays as a **dormant, honestly-labelled** second layer, with the database role/connection cutover deferred until hosted multi-tenancy goes live.

## Context

Row-level-security policies exist in this repository and are **inert**. They have been inert since they were added, for a structural reason: the application connects to Postgres as the role that owns the tables, and in every posture this repository actually runs, that role either holds an explicit bypass or is a superuser — both of which bypass row-level security regardless of any policy. A second attempt to activate them (PR#218) did not change that, because the bypass grant is never revoked and the separate runtime connection it introduced is unset in every environment.

Under the decision above, activating them is **deliberately deferred**. That is a legitimate choice. What is not legitimate is leaving the repository in a state where a migration named for row-level security, plus passing tests, imply a protection that is not in force. A security control nobody can tell is off is worse than an absent one, because it stops people looking.

There is also a live problem in the test suite that is independent of all of the above and should not wait for it.

## Task

**1. Stop the test suite leaving a login-capable role behind.**

The row-level-security test setup grants the low-privilege application role `LOGIN` with a password that is a literal committed in this repository, in order to obtain a non-bypassing connection to test the policies with. Postgres roles are cluster-wide and outlive the scratch database the test drops, so on any shared development or staging cluster where this suite has run, that role persists with login enabled and a publicly-known password — and it holds data-modifying grants on the org-scoped tables. Because setting the org context is an unprivileged operation, whoever holds that credential can iterate org identifiers and read every tenant's rows.

Revert the role to no-login when the suite finishes, on every exit path including failure. A cleanup that only runs on the success path is not sufficient. Better still, refuse to run at all unless the target database is demonstrably disposable, so the suite cannot be pointed at a shared cluster by accident.

The password literal should not remain in the repository; generate it per run.

**2. Make the dormancy impossible to miss.**

Anyone reading this repository — or reviewing it — should be able to tell within seconds that the policies are not in force. State it where each audience will actually look:

- In the migration itself, at the top, in plain terms: these policies do not apply to the connection the application uses, and why.
- In the repository's documentation, alongside whatever describes the database setup: the current state, the reason, and what would have to change to activate them.
- At service startup, as a warning that fires when the configuration means the layer is dormant — so it appears in logs rather than only in files.

Say plainly that the primary tenant-isolation control is application-level, and that this layer is deferred defence-in-depth. Do not describe it in terms that imply it is protecting anything today.

**3. Leave the policies themselves alone.**

Do not attempt to activate them, do not revoke the bypass, do not introduce a separate runtime connection. That work is deferred by decision, and doing it halfway is what produced the current confusion twice.

## Acceptance

- The test suite leaves the application role with login disabled, verified by an assertion that runs after the suite, and holds even when a test fails.
- No password literal for that role remains in the repository.
- The suite declines to run against a database that is not disposable.
- The migration, the repository documentation, and a startup warning each state that the layer is dormant, why, and what the primary control is instead.
- No change to the policies, the role grants, or the connection configuration.
- Existing tests continue to pass.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot-console/issues/231

- Superseded PR: qf-studio/pilot-console#218 (attempted activation — close in favour of this)
- Superseded issue: qf-studio/pilot-console#214
- Companion work: qf-studio/pilot-console#229 (compiler-enforced org scoping — the primary control)
