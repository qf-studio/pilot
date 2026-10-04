---
name: linear-invoices-tickets-founder-owned-hands-off
description: Founder directive 2026-10-02 — Linear Invoices tickets (LIN-QF-* on linearinvoices-service/client via Linear polling) are the founder's own queue; Navigator sessions never label, review, comment on, re-route, cancel or report on them
type: learning
created: 2026-10-02
---

# Linear Invoices tickets are founder-owned — hands off

**Directive (founder, 2026-10-02):** "linear tickets are mine, don't touch em."

## Context

After the TASK-510 GitLab cutover, three LIN-QF issues (282/283/284) appeared queued on
the board for `linearinvoices-service` and a Navigator session asked whether they were
intended. They were. This extends the 10-02 configure-only scope (no tickets labelled by
Navigator, no commits to GitLab) to the whole Linear Invoices queue.

## How to apply

- Treat `LIN-QF-*` rows on the board as read-only context. Do not label, comment, re-route,
  cancel, retry or "clean up" anything in the `linear-invoices` Linear team or its GitLab
  repos.
- Do not ask whether they are intended; they are.
- Mention them only when a row evidences a Pilot product defect (daemon error, stuck poller),
  and then as a generic product gap in this public repo — never ticket titles or content.
- Pilot-repo work (`startups/pilot`, studio-sdk, console) stays in scope as before.

Related: [[founder-priority-pointer-first-saas-parked]] (same class of founder-scope
directive), TASK-510.
