# Roadmap: Plane integration — it works with a current Plane server, then it is robust, then it routes

**Source plan**: `.agent/tasks/TASK-658-plane-integration-works-with-current-plane.md`
**Owner**: founder · **Created**: 2026-11-25 · **Status**: 2026-11-26 15:16Z — five tickets are dispatched (label `pilot` since 15:16Z, on the founder's word); the SDK fix is merged
**Why**: an outside user ran Pilot with Plane on 2026-11-18 and Pilot took no ticket. The research of 2026-11-25 found the cause and six more gaps.

## Status Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Done / merged |
| 🚀 | Dispatched to Pilot, in queue or in flight |
| 🎫 | Ticket written, not dispatched |
| ⏳ | Blocked — waits for an earlier row |
| 📋 | Planned, no ticket yet |

## The goal in one paragraph

A user with a current Plane server sets an API key, a workspace, the project IDs and a label named `pilot`. Each ticket with that label is picked up, by polling and by webhook. One broken ticket or a second page of tickets does not stop the intake. A ticket of each Plane project goes to its own repository. The documentation page says what the code does.

## Milestone 1 — Tickets from a current Plane server are picked up

| # | Item | Status | Where | Ticket |
|---|------|--------|-------|--------|
| 1.1 | The SDK accepts the priority as text (`none`, `urgent`, `high`, `medium`, `low`), as the Plane API sends it | ✅ merged 2026-11-25 16:05Z (outside pull request, reviewed, CI green) | studio-sdk | studio-sdk PR 160 |
| 1.2 | One work item that does not decode is skipped with a Warn; the page is not lost | 🚀 dispatched 11-26 15:16Z | studio-sdk | [studio-sdk#163](https://github.com/qf-studio/studio-sdk/issues/163) |
| 1.3 | The work item list follows the cursor; each page is read | 🚀 dispatched 11-26 15:16Z; needs 1.2 | studio-sdk | [studio-sdk#164](https://github.com/qf-studio/studio-sdk/issues/164) |
| 1.4 | An SDK release holds 1.1 to 1.3 | ⏳ needs 1.1, 1.2, 1.3 | studio-sdk | operator step |
| 1.5 | Pilot pins that SDK release; a test runs the poller against a fake Plane server with a text priority and sees one dispatched task | ⏳ needs 1.4 | pilot | leg K4 (held) |

**Exit**: the test of 1.5 passes on main, and a Pilot release holds it.

## Milestone 2 — The webhook path and the documentation

| # | Item | Status | Where | Ticket |
|---|------|--------|-------|--------|
| 2.1 | The webhook accepts the Pilot label by its name (today it compares the name with label IDs and never matches) | 🚀 dispatched 11-26 15:16Z | pilot | [#6705](https://github.com/qf-studio/pilot/issues/6705) |
| 2.2 | The Plane documentation page matches the code (task ID, webhook events, the label precondition) | 🚀 dispatched 11-26 15:16Z | pilot | [#6706](https://github.com/qf-studio/pilot/issues/6706) |
| 2.3 | The unused internal Plane client, poller and notifier are removed | ⏳ needs 2.1 | pilot | leg K6 (held) |

**Exit**: a webhook event with the default config starts a task in a test; the page is correct.

## Milestone 3 — Several Plane projects

| # | Item | Status | Where | Ticket |
|---|------|--------|-------|--------|
| 3.1 | A project entry of the config can name its Plane project ID; the poller routes each ticket to that repository | 🚀 dispatched 11-26 15:16Z | pilot | [#6707](https://github.com/qf-studio/pilot/issues/6707) |
| 3.2 | The same routing for the webhook path | 📋 after 2.1 and 3.1 | pilot | — |

## Milestone 4 — Behaviour that needs a founder decision first

| # | Item | Status | Proposal |
|---|------|--------|----------|
| 4.1 | The ticket is set to done at the merge, not when the pull request opens | 📋 decision | At the pull request: keep the state, add the label `pilot-done` only at the merge, and move the ticket to the completed state at the merge. It needs a hook at the merge for tasks that come from Plane, and a trace of the pull request lifecycle for that task family first (the pitfall of PR#4992). |
| 4.2 | One comment for each event in Plane | 📋 decision | Today three places can write after a success: the task handler (result comment), the SDK poller (pull request comment, with a duplicate guard) and the notifier (pull request link). Proposal: one "started" comment and one result comment that holds the pull request link; the other two writers are removed. Verify the three writers with a fake server before the ticket. |

## Not on this roadmap

- A code host other than GitHub or GitLab for Plane users (the Forgejo request; see the research report of 2026-11-25).
- A write of the priority from Pilot to Plane.

## Facts that the tickets rest on

- Plane API (developers.plane.so, read 2026-11-25): `priority` is a string; the header is `X-API-Key`; 60 requests each minute for each key; lists page at 100 with `next_cursor` and `next_page_results`.
- Polling, state changes and comments use the SDK (`cmd/pilot/poller_plane.go`); the internal adapter is live only for the config type and the webhook handler.
- Pull request 6510 (merged 2026-11-25) changed the internal type; it does not change the polling path.
- There is no Plane server for a live test. The proof is a test against a fake server that answers as the documentation says; the outside user can confirm on a real server.

## Log

- 2026-11-25: research, plan TASK-658, this roadmap; founder: "plan to tickets all that, create a roadmap, run it"; SDK pull request 160 merged 16:05Z; then founder: "create tickets, do not dispatch them" — five tickets filed 16:27Z with no label `pilot`.
- 2026-11-26 15:16Z: founder: "let's ship the Plane roadmap" — the label `pilot` added to studio-sdk#163 and #164 and to pilot #6705, #6706 and #6707 (#164 waits for #163 by its Depends-on line).
