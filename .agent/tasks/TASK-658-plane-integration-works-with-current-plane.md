# TASK-658: Plane integration works with a current Plane server — text priority on the SDK path, no lost tickets, webhook label match

**Status**: ✅ **State 2026-11-27 20:19Z: the gap ticket merged ([pilot#6761](https://github.com/qf-studio/pilot/issues/6761) → PR#6766, 20:17Z; reviewed after the merge with one mutation, no finding): a work item whose pull request no controller takes is completed at the pull request. Each ticket of phase 4 is merged; nothing of this task is with Pilot. Open: the release (11-28), the founder's decision for a pull request that closes with no merge, a run against a live Plane server.** **State 2026-11-27 19:15Z: PHASE 4 IS MERGED. A Plane work item is completed when its pull request merges ([pilot#6751](https://github.com/qf-studio/pilot/issues/6751) → PR#6760, 19:13Z; reviewed after the merge with four mutations, no finding). Not released yet (next train 11-28). One gap of the change has a ticket (M6): with a project pairing, a pull request that no controller registers is never completed. One product decision is open: what a pull request that closes with no merge does to the work item. No run against a live Plane server was made.** **State 2026-11-27 18:21Z: one comment for each event merged ([pilot#6741](https://github.com/qf-studio/pilot/issues/6741) → PR#6756, 18:18Z; reviewed after the merge with two mutations, no finding). The last ticket (the completed state at the merge, [pilot#6751](https://github.com/qf-studio/pilot/issues/6751)) is in the queue.** Two limits for later tickets: a pull request that closes with no merge leaves the work item in the started state; a pull request that no autopilot controller registers (a URL that is not GitHub) is never completed when the option is on. **State 2026-11-27 17:47Z: the stored Plane link merged ([pilot#6739](https://github.com/qf-studio/pilot/issues/6739) → PR#6749, 17:44Z; reviewed after the merge with three mutations, no finding; SDK v0.39.5 pinned, the option off). The last ticket of phase 4 is written: the completed state at the merge, with the option on in the same change (M5, after the comment ticket).** **State 2026-11-27 16:55Z: the founder chose one comment for each event (11-27: "Go proposal"); the ticket for it is written (M4, after the link ticket). The daemon's review of PR#190 found that the completed-state tests of the SDK gave a result by timing; the fix merged ([studio-sdk#191](https://github.com/qf-studio/studio-sdk/issues/191) → PR#192, 16:52Z; tests only; reviewed: 20 runs with the race detector pass, a mutation fails the right tests in each of five runs). The two reviews of the session had not found it.** **State 2026-11-27 16:31Z: the adapter change merged ([studio-sdk#189](https://github.com/qf-studio/studio-sdk/issues/189) → PR#190, 16:28Z; reviewed after the merge with two mutations, no finding) and SDK v0.39.5 holds it (cut by hand 16:29Z on `8d29b62`). Next: Pilot pins v0.39.5 with the option off and stores the Plane link of a pull request (M3); then the completed state at the merge, with the option on in the same change.** **State 2026-11-27 16:19Z: the poller option merged ([studio-sdk#187](https://github.com/qf-studio/studio-sdk/issues/187) → PR#188, 16:17Z; reviewed after the merge with two mutations, no finding) and SDK v0.39.4 holds it (cut by hand 16:19Z on `8ace118`). A gap of the session's ticket text: Pilot builds the poller through the adapter of the SDK (New, then NewPoller with the poller dependencies), and that path builds its own option list, so Pilot cannot set the new option. A second SDK ticket (M2) gives the SDK config a field that the adapter maps to the option; the pin in Pilot waits for the release with it.** **M1 DISPATCHED 2026-11-27 16:10Z → [studio-sdk#187](https://github.com/qf-studio/studio-sdk/issues/187) (`pilot` + `no-decompose`): a poller option leaves the state of the work item alone when the run opened a pull request.** Held: the SDK release and the pin in Pilot, the stored Plane link of a pull request, the completed state at the merge. **State 2026-11-27 16:09Z: the founder decided that a Plane work item is done when the pull request merges (11-27). The lifecycle is traced; the change is planned in four steps (phase 4 of the route), and the first ticket (an SDK option) is written.** The comment question is still open: the trace found four comments for one successful run. **State 2026-11-27 15:08Z: RELEASED. Pilot v2.328.0 (15:04Z) holds each Plane change of this task (PR#6714, PR#6715, PR#6717, PR#6721, PR#6723) and the later SDK pin v0.39.3 (PR#6728); the founder box runs 2.328.0 since 15:07Z.** Still open: the two design decisions of the founder (when a Plane ticket counts as done; one comment for each event or not), and a run against a live Plane server, which was not made. Earlier state: **State 2026-11-26 17:53Z: each ticket of the roadmap is merged; nothing is with Pilot.** The last one, [#6722](https://github.com/qf-studio/pilot/issues/6722) (Pilot pins the SDK release v0.39.2, a poller test against a fake Plane server, the Plane page corrected), merged as PR#6723 at 17:52Z; reviewed after the merge, no blocking finding. **Not released yet:** the last Pilot release is v2.327.2 (11-25); the Plane fixes reach users with the next Pilot release. **Open:** the two design decisions of the founder (done at the merge; one comment for each event). **Not proven:** a run against a live Plane server (each review read the diff; the tests use fake servers). **Found, not planned:** no credential leak test for the adapter clients of the SDK.** **State 2026-11-26 17:18Z: one ticket is in work: [#6722](https://github.com/qf-studio/pilot/issues/6722), Pilot pins the SDK release v0.39.2, with a poller test against a fake Plane server and the Plane page corrected (dispatched 17:18Z). Done since the state below: studio-sdk#171 merged (SDK pull request 172, 17:16Z, reviewed, no finding); the SDK release v0.39.2 cut by hand at 17:17Z on the commit 3f78c7e after green CI (it holds the SDK pull requests 165, 167, 168, 170, 172; founder word "go ... release updates in sdk"); #6719 merged (PR#6721, 17:14Z, reviewed, no finding: 2010 lines deleted, no new code). After #6722 merges, the fixes reach users with the next Pilot release. Open after that: the two design decisions of the founder (done at the merge; one comment for each event). Found, not planned: the SDK has no credential leak test for its adapter clients (the internal Plane canary test left with the deleted client).** **State 2026-11-26 17:08Z: studio-sdk#169 is merged (SDK pull request 170, 17:04Z; reviewed, no blocking finding). The daemon's review found eight minor defects and its follow-up was skipped by the daily cap, so the session dispatched [studio-sdk#171](https://github.com/qf-studio/studio-sdk/issues/171) at 17:08Z (leg K9): the label set by the project of the list request, the orphan recovery for a project found later, the Warn that names the label, two test notes. The SDK release waits for the merge of #171; then the pin ticket in Pilot.** **State 2026-11-26 16:54Z: founder: "go labels and release updates in sdk". Dispatched 16:54Z: [studio-sdk#169](https://github.com/qf-studio/studio-sdk/issues/169), the SDK poller looks up the labels for each Plane project (leg K8, new in the plan). Order: #169 merges → one SDK release by a tag (it holds the SDK pull requests 165, 167, 168 and the one of #169) → the ticket in which Pilot pins that release (poller test, the priority warning of the Plane page removed, `plane_doc_test.go` adjusted).** **State 2026-11-26 16:46Z: the five tickets of the dispatch are MERGED, each reviewed by the session after its merge with no blocking finding (comment on each pull request):** studio-sdk#163 (PR 165), studio-sdk#164 (PR 167), pilot #6705 (PR#6714), #6706 (PR#6715), #6707 (PR#6717). The daemon's own reviews filed and merged two follow-ups: studio-sdk#166 (PR 168: a nil logger keeps the default, a response with no results is an empty list, the host logger reaches the Plane client) and pilot #6716 (PR#6718: three text fixes of the Plane page, with `internal/adapters/plane/plane_doc_test.go`). **In work: [#6719](https://github.com/qf-studio/pilot/issues/6719), the removal of the unused internal Plane client, poller and notifier (dispatched 16:45Z).** **Held: the pin of the new SDK release with a poller test (needs an SDK release after PR 168; the last release v0.39.1 is older). That ticket must also remove the priority warning of the Plane page and adjust `plane_doc_test.go`, which pins the text about the pinned SDK.** **Found by the reviews, not in the plan (founder decision open, word "labels"):** the SDK poller takes the Pilot label ID from the first configured project and filters each project by it (cacheLabelIDs); Plane labels belong to one project, so with two or more Plane projects only one is picked up on the poll path, and the routing of #6707 has one project to route. All these runs went with the option `run_as_user` on (TASK-651), with no failure. **State 2026-11-26 15:16Z: the five tickets are DISPATCHED (label `pilot` added at 15:16Z; founder, 11-26: "let's ship the Plane roadmap"): studio-sdk#163 and #164 (#164 waits for #163 by its Depends-on line), pilot #6705, #6706 and #6707. These are also the first runs with the option `run_as_user` on (TASK-651); the session reads each run and reviews each pull request after its merge.** **State 2026-11-25 16:28Z: five tickets are FILED and NOT dispatched (no label `pilot`): studio-sdk [#163](https://github.com/qf-studio/studio-sdk/issues/163) and [#164](https://github.com/qf-studio/studio-sdk/issues/164), pilot [#6705](https://github.com/qf-studio/pilot/issues/6705), [#6706](https://github.com/qf-studio/pilot/issues/6706) and [#6707](https://github.com/qf-studio/pilot/issues/6707). Founder, 11-25: "Create tickets, do not dispatch them ... I'll tell you to pick up this roadmap." The dispatch (the label `pilot` on each ticket) waits for the founder's word.** Done: the outside SDK pull request 160 (the priority as text on the SDK path) is merged, 16:05Z, on the founder's "run it" of the same day. Held legs, not written: K4 (Pilot pins a new SDK release, with a poller test) and K6 (removal of the unused internal Plane code). Roadmap document: `.agent/system/plane-integration-roadmap.md`. Two design decisions wait for the founder (done at the merge; one comment for each event).
**Created**: 2026-11-25
**Assignee**: Pilot for each leg; the founder for the merge of the outside SDK pull request and for an SDK release.

---

## Context

**Problem**:
An outside user runs Pilot with Plane (plane.so) and sent fixes. Pull request 6510 was merged on 2026-11-25: the type `Priority` of the internal Plane adapter now decodes text values. The code research of the same day showed that this was the wrong place for the main flow:

- Pilot has two copies of the Plane code. The ticket intake (the poller), the state changes and the comments run through the SDK (`cmd/pilot/poller_plane.go`). The internal adapter (`internal/adapters/plane`) is live only for the config type and the webhook handler; its client, poller and notifier have no production caller.
- The SDK version that Pilot pins (v0.38.7) still has `type Priority int` with no decoder. The Plane API documents `priority` as a string with the values none, urgent, high, medium, low. So with a current Plane server each page of work items fails to decode, the poller logs a Warn at each poll, and Pilot takes no ticket.
- The same decoder for the SDK is the outside pull request 160 in `qf-studio/studio-sdk` (open, CI green, same code and tests as pull request 6510).

More gaps from the research (each with its place in the code):

1. One work item that cannot be decoded fails the whole page (SDK `sdk/integrations/plane/client.go`, ListWorkItems; the poller then skips the project for that poll).
2. Only the first page of work items is read; there is no cursor handling. The Plane API returns 100 items for each page at most, with `next_cursor` and `next_page_results`.
3. The internal webhook handler compares the configured `pilot_label` (a label NAME, default "pilot") with the label UUIDs of the event (hasPilotLabel in `internal/adapters/plane/webhook.go`). It never matches unless the user puts a UUID into the config.
4. Every Plane project maps to one repository (the repository resolver has a TODO for Plane).
5. The work item moves to the completed state when the pull request opens, not when it merges.
6. Up to three places write a comment to Plane for one task (not verified).
7. `docs/content/integrations/plane.mdx` names a task ID format and a webhook behaviour that the code does not have.

**Goal**:
A user with a current Plane server gets each ticket with the Pilot label picked up, by polling and by webhook, with no ticket lost to one bad item or to a second page. The documentation says what the code does.

---

## Known Pitfalls & Patterns

- **PITFALL** (mem-352): for an adapter fix, find the production caller of the changed type before a merge; research first, merge second. → Each leg below names the production call site that it changes.
- **PITFALL** (mem-353): the list of the Plane gaps of 2026-11-25. → Legs K1 to K7 and the two design points of the roadmap document.
- **PITFALL** (`merged-feature-dead-callback-not-bridged-onprcreated`): a feature that hangs on the pull request lifecycle must be traced from the tracker event to the call site for that task family. → The "done at merge" item is not a leg yet: it needs that trace first.
- **LEARNING** (`operator-issue-bodies-must-pass-base-presence-check-before-filing`): backticked paths must exist on the main branch of the target repository. → New files are named in plain text.
- **PITFALL** (mem-335): a merged change is not in use until a release holds it. → The held leg K4 pins the SDK release; the Pilot change reaches users with the next Pilot release.

---

## Acceptance Criteria

- [ ] A fake Plane server that returns `"priority": "urgent"` for a work item with the Pilot label leads to one dispatched task (a test in `cmd/pilot`).
- [ ] A page with one broken work item among good ones gives the good ones, and one Warn names the broken item.
- [ ] A project with two pages of labelled work items gives every item.
- [ ] A webhook event with the label of the configured name is accepted with the default config.
- [ ] The Plane documentation page matches the code for the task ID, the webhook events and the label precondition.

---

## Route

### Phase 1: Tickets from a current Plane server are picked up
- [x] 1. The SDK accepts the priority as text (the outside pull request 160 of the SDK repository) (operator)
- [x] 2. One broken work item no longer blocks the page; the poller logs it and goes on (qf-studio/studio-sdk#163)
- [x] 3. The SDK reads each page of work items (qf-studio/studio-sdk#164, needs 2)
- [x] 4. A new SDK release holds 1 to 3 (v0.39.2, 11-26 17:17Z; it also holds the label lookup for each project)
- [x] 5. Pilot uses that SDK release, with a test of the poller against a fake Plane server (#6722, merged 11-26)

### Phase 2: The webhook path and the documentation
- [x] 6. The webhook accepts the Pilot label by its name (#6705)
- [x] 7. The Plane documentation page matches the code (#6706)
- [x] 8. The unused internal Plane client, poller and notifier are removed (#6719, merged 11-26)

### Phase 3: Several Plane projects
- [x] 9. A project entry can name its Plane project, and the poller routes each ticket to that repository (#6707)

### Phase 4: Done at the merge (founder decision 11-27)
- [x] 10. The SDK poller can leave the state of the work item alone when the run opened a pull request (studio-sdk#187, merged 11-27; SDK v0.39.4)
- [x] 10a. The SDK adapter passes that choice from the config to the poller (studio-sdk#189, merged 11-27; SDK v0.39.5)
- [x] 11. An SDK release holds steps 10 and 10a (SDK v0.39.5, cut by hand 11-27 16:29Z)
- [x] 12. Pilot pins SDK v0.39.5 with the option off, and keeps the Plane project and the work item of a pull request in its pull request state, so that the link is there after a restart (M3, needs 11) — pilot#6739, merged 11-27 17:44Z
- [x] 13. Pilot sets the work item to the completed state when the pull request merges, one time, with one comment, and turns the option on in the same change (M5, needs 12 and 14) — pilot#6751, merged 11-27 19:13Z
- [x] 15. A work item whose pull request no controller registers is completed at the pull request, as before the option (M6, needs 13) — pilot#6761, merged 11-27 20:17Z
- [x] 14. One comment for each event: a run with a pull request posts one pull request comment, not three (M4, needs 12) — pilot#6741, merged 11-27 18:18Z

### Side tracks
- [x] The work item is set to done at the merge, not at the pull request — decided by the founder 11-27; traced; now phase 4 of the route
- [x] One comment for each event in Plane — decided by the founder 11-27; now step 14 of the route

### Waits for you
- [x] The merge of the outside SDK pull request 160 (step 1; founder, 11-25: "run it")
- [x] The go for the plan and the roadmap (founder, 11-25)
- [x] The word to pick up the roadmap (founder, 11-26: "let's ship the Plane roadmap"); the label `pilot` is on the five tickets since 15:16Z
- [x] A decision on the comments in Plane: one comment for each event (founder, 11-27: "Go proposal")

---

## Implementation

### Phase 1

**Facts**: SDK `ListWorkItems` (`sdk/integrations/plane/client.go` in the SDK repository) does one GET on the work items path, with `?label=UUID` when a label is given, and decodes into `paginatedResponse{Results []WorkItem, TotalCount int}`. The poller calls it at two places (new items, orphaned in-progress items) and treats an error as "skip this project for this poll". The Plane API: header `X-API-Key`; 60 requests each minute for each key, with `X-RateLimit-Remaining` and `X-RateLimit-Reset`; pagination with `per_page` (default and maximum 100) and `cursor`, response fields `next_cursor`, `prev_cursor`, `next_page_results`, `prev_page_results`, `count`, `total_pages`, `total_results`, `results`.

Order: K1 and K2 touch the same function, so K2 depends on K1. Step 1 (the outside pull request) touches only the types file and its test.

### Phase 2

**Facts**: the webhook handler is built at one place (`internal/pilot/pilot.go`, NewWebhookHandler) and has no API client today; it knows only the configured label text. The SDK client has the label list call that the poller uses to turn the name into a UUID.

### Later (no leg yet)

**Done at the merge (phase 4), trace of 11-27 at release v2.328.0:**

- Today the SDK poller does three things after a successful run, all at the time of the pull request: it removes the in-progress label, adds the done label, and moves the work item to the first state of the completed group. Pilot has no Plane code on the merge path.
- **The labels stay as they are.** The poller skips an item with a status label, and at a start it takes the in-progress label off each item that carries it and runs the item again. An item with no status label and an open pull request is a second run after a restart. So the done label keeps its meaning "the run of Pilot is finished", and only the state moves to the merge.
- Between the pull request and the merge the item stays in the started state. Plane has no fixed review group; a named review state is a later option.
- Linear and Jira have a merge step in the autopilot controller that finds the ticket from the branch name. The Plane branch holds only a sequence number; the state change needs the project ID and the work item ID. The handler in `cmd/pilot/poller_plane.go` has both at the time of the pull request, so Pilot stores them with the pull request state (step 12) and reads them at the merge (step 13).
- A pull request that closes with no merge then leaves the item in the started state with the done label. A comment for that case belongs to the comment decision.
- No config switch: no user runs Plane against a live server yet, and the founder's decision is the one behaviour.

**Comments:** a leg follows the founder's choice. The roadmap document `.agent/system/plane-integration-roadmap.md` holds the proposal.

---

## Out of Scope

- Forgejo or another code host for Plane users (declined as pull request 6513; explored in the research report of 2026-11-25).
- A write of the priority to Plane (Pilot does not do it).
- Rate limit handling beyond the one retry that exists.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the text priority is fixed | the internal adapter only (done by pull request 6510); the SDK | the SDK (outside pull request 160), then an SDK release and a pin in Pilot | the poller decodes the SDK type |
| A broken work item | fail the page (today); skip the item with a Warn | skip the item with a Warn that names its ID | one bad item must not stop the intake of a project |
| An unknown priority text | error (today, in both decoders); map to none | error in the decoder, and the item-level skip of K1 contains it | the decoder stays strict; the damage stays at one item, and the Warn tells the operator |
| Webhook label | require a UUID in the config; resolve the name | resolve the name through the SDK client, cached for each project | the default config must work; the poller does the same |

---

## Verify

```bash
# SDK
go test ./sdk/integrations/plane/...
# Pilot
go test ./cmd/pilot -run Plane
go test ./internal/adapters/plane/...
```

---

## Done

- [ ] Route steps 1 to 8 checked; a Pilot release holds steps 5, 6 and 8.
- [ ] The outside user gets a short note on the pull requests that the intake now works with a text priority.

---

## Legs

### LEG K1 — repo: qf-studio/studio-sdk — title: fix(plane): one work item that does not decode no longer fails the whole page — it is skipped with a Warn that names it

probe: present qf-studio/studio-sdk func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]ListWorkItems\(
probe: present qf-studio/studio-sdk type[[:space:]]paginatedResponse[[:space:]]struct
probe: absent  qf-studio/studio-sdk func[[:space:]]TestListWorkItemsSkipsAnItemThatDoesNotDecode\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

The Plane client lists work items with ListWorkItems (`sdk/integrations/plane/client.go`). It decodes the whole response into paginatedResponse (`sdk/integrations/plane/types.go`), whose Results field is a slice of WorkItem. When one item of the page does not decode (for example a field with a type that the struct does not expect), the whole call returns an error. The poller (`sdk/integrations/plane/poller.go`) then logs a Warn and skips the project for that poll, at each poll. So one bad work item stops the ticket intake of a whole Plane project. This happened in the field with a priority sent as text.

## Scope

1. ListWorkItems decodes the list item by item: the results are read as raw JSON messages, and each one is decoded into a WorkItem by itself. An item that does not decode is left out of the result.
2. For each item left out, the client writes one Warn. When the client has no logger today, add an optional one with a functional option or a setter, in the style of the package; the default is the standard slog logger. The line holds the project ID, the ID of the item when its "id" field can still be read from the raw JSON, and the decode error. It never holds the full JSON of the item.
3. When the response itself is not valid (no results array, broken JSON), the call still returns the error as today.
4. The poller needs no change of its control flow; keep its two call sites as they are.
5. Tests in the client test file (`sdk/integrations/plane/client_test.go`), with the fake server of that file: a new test TestListWorkItemsSkipsAnItemThatDoesNotDecode — a page with three items, the second with a field of a wrong type, gives two items and one Warn that names the ID of the second; a page with no broken item gives every item and no Warn; a broken response still gives an error.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/...` passes; paste the output into the PR body.
- [ ] The PR body names each call site of ListWorkItems in the repository and says that none needed a change of its error handling.

## Out of scope

Pagination. The Priority type. The webhook. A retry policy.

## Refs

- Navigator TASK-658 of qf-studio/pilot (plan)

### LEG K2 — repo: qf-studio/studio-sdk — title: fix(plane): the work item list follows the cursor — every page is read, with a page limit

probe: present qf-studio/studio-sdk func[[:space:]]\(c[[:space:]]\*Client\)[[:space:]]ListWorkItems\(
probe: absent  qf-studio/studio-sdk func[[:space:]]TestListWorkItemsReadsEveryPage\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Depends on: #{{K1}}

ListWorkItems (`sdk/integrations/plane/client.go`) reads one response and returns its results. The Plane API pages its lists: the request takes per_page (default 100, maximum 100) and cursor; the response holds results, next_cursor, prev_cursor, next_page_results, prev_page_results, count, total_pages and total_results. A cursor has the form value:offset:is_prev. So a project with more labelled work items than one page loses the rest today, with no log line.

## Scope

1. paginatedResponse (`sdk/integrations/plane/types.go`) gets the fields next_cursor (string) and next_page_results (bool).
2. ListWorkItems asks for per_page=100 and follows the cursor: while next_page_results is true and next_cursor is not empty, it requests the next page with the cursor parameter (the label filter stays on each request) and adds the results.
3. A hard limit of 50 pages for one call. When the limit is reached, the call returns the items read so far and logs one Warn with the project ID and the limit; it is not an error.
4. An error on a later page returns the error (no partial result), as an error on the first page does today.
5. The query string is built with net/url values, not by string concatenation.
6. Tests in the client test file (`sdk/integrations/plane/client_test.go`): a new test TestListWorkItemsReadsEveryPage — a fake server with three pages gives every item in order and gets the cursor of each page; one page with next_page_results false makes one request; a server that always says there is a next page stops at the limit with the Warn; the label filter is on each request.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/...` passes; paste the output into the PR body.
- [ ] The PR body says how many requests one poll of one project now makes for 1, 100 and 250 labelled items (the API allows 60 requests each minute for each key).

## Out of scope

Rate limit handling. Other list calls (states, labels). The poller interval.

## Refs

- Navigator TASK-658 of qf-studio/pilot (plan) · Plane API reference, section pagination

### LEG K3 — repo: qf-studio/pilot — title: fix(plane): the webhook accepts the Pilot label by its name — the name is resolved to the label ID for each project

probe: present qf-studio/pilot func[[:space:]]\(h[[:space:]]\*WebhookHandler\)[[:space:]]hasPilotLabel\(
probe: absent  qf-studio/pilot func[[:space:]]TestWebhookAcceptsTheLabelByName\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The Plane webhook handler (`internal/adapters/plane/webhook.go`) decides with hasPilotLabel whether a work item event is for Pilot. It compares the configured value pilot_label with the label IDs of the event. The config value is a label NAME (default "pilot"); a Plane event carries label UUIDs. So with the default config the webhook never accepts an event. The polling path does not have this defect: it resolves the name to the UUID through the label list of the project. The handler is built at one place (`internal/pilot/pilot.go`, the call of plane.NewWebhookHandler).

## Scope

1. The handler gets an optional resolver: a function that takes a project ID and a label name and returns the label UUID. NewWebhookHandler keeps its signature for the callers of today; the resolver is set with a new setter or option.
2. hasPilotLabel: when the configured value is equal to one of the label IDs of the event, it matches as today (a UUID in the config keeps its meaning). Else, when a resolver is set, the handler resolves the configured name for the project of the event and compares the result with the label IDs.
3. The result of the resolver is kept for each project for 10 minutes (a small map with a mutex and a time stamp; no new dependency). A failed lookup is not kept.
4. When the resolver fails or finds no label with that name, the event is not accepted, and one Warn names the project ID and the label name (not more than one such line for each project in 10 minutes).
5. Where the handler is built (`internal/pilot/pilot.go`), set the resolver from the Plane client of the SDK that the poller already uses for the label list, built from the same adapter config. When the Plane adapter has no API key, no resolver is set, and the behaviour of today stays.
6. Tests in the webhook test file (`internal/adapters/plane/webhook_test.go`): a new test TestWebhookAcceptsTheLabelByName — with the name "pilot" in the config and a resolver that maps it to a UUID, an event with that UUID is accepted and one without it is not; a UUID in the config still matches with no resolver call; the resolver is called once for two events of one project; a failed resolver refuses the event with one Warn.

## Acceptance

- [ ] `go test -race -count=1 ./internal/adapters/plane/ ./internal/pilot/ -run 'Plane|Webhook'` passes; paste the output into the PR body.
- [ ] The PR body names the production call site that sets the resolver.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The poller. The internal Plane client, poller and notifier files. The task that the webhook starts.

## Refs

- Navigator TASK-658 in this repo

### LEG K5 — repo: qf-studio/pilot — title: docs(plane): the Plane page matches the code — task ID, webhook events, the label precondition, the two paths

probe: present qf-studio/pilot PLANE-

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The documentation page of the Plane integration (`docs/content/integrations/plane.mdx`) differs from the code in three places. It gives the task ID as PLANE- plus eight characters of the UUID; the code uses PLANE- plus the sequence number of the work item (`cmd/pilot/handlers.go`, handlePlaneIssueWithResult). It says that an "issue updated (label added)" webhook event is processed in a special way; the webhook code treats "updated" as "created" (`internal/adapters/plane/webhook.go`). It does not say that the Plane project must have a label with the configured name before the poller starts.

## Scope

1. Correct the task ID format on the page, with one example.
2. Describe the webhook events as the code handles them.
3. Add a short "Before you start" list: a label with the configured name (default pilot) in each Plane project; the optional labels pilot-in-progress, pilot-done and pilot-failed; an API key; the workspace slug; the project IDs.
4. State that polling is the main path and that the priority comes from Plane as text.
5. Compare each config key on the page with the example config (`configs/pilot.example.yaml`, the plane block) and correct the page where they differ. Do not change the example config.

## Acceptance

- [ ] The PR body lists each sentence of the page that changed, with the code place that proves the new text.
- [ ] The diff touches only the Plane page.

## Out of scope

Code. Other documentation pages. The feature matrix.

## Refs

- Navigator TASK-658 in this repo

### LEG K7 — repo: qf-studio/pilot — title: feat(plane): one repository for each Plane project — a project entry can name its Plane project ID, and the poller routes the task there

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Config\)[[:space:]]GetProjectByLinearID\(
probe: present qf-studio/pilot func[[:space:]]planePollerRegistration\(
probe: absent  qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Config\)[[:space:]]GetProjectByPlaneID\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The Plane poller (`cmd/pilot/poller_plane.go`, planePollerRegistration) hands every work item to handlePlaneIssueWithResult (`cmd/pilot/handlers.go`) with one project path for the whole adapter (deps.ProjectPath: the default project). The adapter config can list several Plane project IDs, so work items of two Plane projects land in one repository. Linear already has the pairing that Plane lacks: a project entry of the config can carry a linear block with a project_id, and GetProjectByLinearID (`internal/config/config.go`) finds the project entry for an event. The SDK event of a Plane work item carries the Plane project UUID in its ProjectID field.

## Scope

1. Config (`internal/config/config.go`): a new type ProjectPlaneConfig with one field ProjectID (yaml project_id), a new optional field Plane on ProjectConfig (yaml plane, omitempty), and a new method GetProjectByPlaneID that mirrors GetProjectByLinearID.
2. Validation at load: two project entries with the same plane.project_id are an error that names both entries; an empty project_id in a plane block is an error.
3. The poller wiring (`cmd/pilot/poller_plane.go`): for each event, the project path is the path of the project entry whose plane.project_id is equal to the ProjectID of the event; when no entry matches, the path is deps.ProjectPath as today. One Info line for each task names the work item, the Plane project ID and the chosen project name; when the fallback is used and at least one project entry has a plane block, the line is a Warn.
4. The base branch and each other value that the handler derives from the project path must follow the chosen path (the handler already takes the path as an argument; check that the wiring passes the chosen one to each call).
5. The example config (`configs/pilot.example.yaml`): two comment lines in the projects part that show the plane block. The documentation page (`docs/content/integrations/plane.mdx`): a short section "Several Plane projects".
6. Tests: a config test for GetProjectByPlaneID and the two validation errors; a test in the Plane poller test file (`cmd/pilot/poller_plane_test.go`) that two events with two Plane project IDs get two project paths, and that an unknown ID gets the default path.

## Acceptance

- [ ] `go test -race -count=1 ./internal/config/ ./cmd/pilot/ -run 'Plane'` passes; paste the output into the PR body.
- [ ] The PR body says what a config with no plane block does (the behaviour of today).
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The webhook path. The SDK. The repository resolver stub in the sdkshim package. A change of the adapter-level project ID list.

## Refs

- Navigator TASK-658 in this repo

### LEG K6 — repo: qf-studio/pilot — title: refactor(plane): remove the internal Plane client, poller and notifier that no code path uses — the SDK owns the poll path

probe: present qf-studio/pilot func[[:space:]]sanitizeWorkItemInPlace\(
probe: present qf-studio/pilot func[[:space:]]NewNotifier\(client[[:space:]]\*Client,[[:space:]]workspaceSlug

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The package `internal/adapters/plane` holds two things. One is in use: the adapter config (Config, PollingConfig, DefaultConfig in `internal/adapters/plane/types.go`) and the webhook handler (`internal/adapters/plane/webhook.go`). The other is not: an HTTP client (`client.go`), a poller (`poller.go`), a notifier (`notifier.go`) and a sanitize helper (`sanitize.go`). The daemon polls Plane through the SDK (`cmd/pilot/poller_plane.go` builds the SDK adapter; `internal/pilot/pilot.go` uses the SDK client for the label lookup). No file outside the package calls NewClient, NewPoller or NewNotifier of the internal package. The unused copy misleads: an outside fix (pull request 6510) changed its Priority type, and the poll path did not change, because it uses the SDK type.

## Scope

1. Delete `internal/adapters/plane/client.go`, `poller.go`, `notifier.go` and `sanitize.go`, and the test files that test only them: `client_test.go`, `client_canary_test.go`, `notifier_test.go`.
2. In `internal/adapters/plane/types.go` keep Config, PollingConfig and DefaultConfig. Remove each other type, constant and function of the file that has no use left in the repository after step 1 (WorkItem, State, Label, Comment, Priority and its decoder, PriorityName, the response wrappers, StateGroup). Before each removal, search the whole repository for the name; keep a name that a file outside the deleted set still uses, and say so in the PR body. Adjust `types_test.go` to what stays; delete it when nothing of it stays.
3. `webhook.go`, `webhook_test.go`, `plane_doc_test.go` and the webhook data type stay as they are. Do not change their behaviour.
4. Every importer of the package must still build: `cmd/pilot/main.go`, `cmd/pilot/wiring_test.go`, `cmd/pilot/poller_plane_test.go`, `internal/config/config.go`, `internal/pilot/pilot.go`, `internal/pilot/plane_label_resolver_test.go`, `internal/pilot/webhook_handlers_test.go`, `internal/orchestrator/orchestrator.go`.
5. The canary gate (`scripts/check-adapter-canary-tests.sh`) asks for a credential leak test only in an adapter directory whose non-test files use http.Client or http.NewRequest. After step 1 the package has no HTTP client, so the gate must pass with the canary test deleted. Run the script and paste its output.
6. Comments and documents that name the removed code: correct the line in `internal/adapters/sdkshim/doc.go` when it refers to the internal Plane client; correct the Plane rows of `.agent/system/FEATURE-MATRIX.md` that name `adapters/plane` for the poller, the client or the notifier (the path is the SDK now). Do not change `docs/content/integrations/plane.mdx`.

## Acceptance

- [ ] `go build ./...` and `go vet ./internal/adapters/plane/ ./internal/pilot/ ./internal/config/ ./internal/orchestrator/ ./cmd/pilot/` pass; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/adapters/plane/ ./internal/pilot/ ./internal/config/ -run 'Plane|Webhook'` passes; paste the output into the PR body.
- [ ] `bash scripts/check-adapter-canary-tests.sh` passes; paste the output into the PR body.
- [ ] The PR body lists each deleted file, and each name of `types.go` that was kept with the file that still uses it.
- [ ] No new code: the change only deletes code and corrects comments and documents. If a removal needs new code to keep the build, stop and say so in the PR body.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The webhook handler. The SDK. The config fields of the Plane adapter. The poller wiring in `cmd/pilot/poller_plane.go`. The Plane documentation page.

## Refs

- Navigator TASK-658 in this repo
- Pull request 6510 (the outside fix that changed the unused type)

### LEG K8 — repo: qf-studio/studio-sdk — title: fix(plane): the poller looks up the labels for each Plane project — a second project is picked up, and the status labels get the IDs of the project of the work item

probe: present qf-studio/studio-sdk func[[:space:]]\(p[[:space:]]\*Poller\)[[:space:]]cacheLabelIDs\(
probe: present qf-studio/studio-sdk func[[:space:]]\(p[[:space:]]\*Poller\)[[:space:]]cacheStateIDs\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

The Plane poller (`sdk/integrations/plane/poller.go`) keeps four label IDs in single fields: pilotLabelID, inProgressLabelID, doneLabelID and failedLabelID. cacheLabelIDs reads the labels of the configured projects in order and stops at the first project that has the Pilot label. Each later step uses those four IDs for every project: the list filter in checkForNewIssues, the orphan recovery in recoverOrphanedIssues, the status labels in processIssueAsync, and hasStatusLabel. A Plane label belongs to one project, and the same label name has another UUID in each project. So with two or more configured projects, the list of each other project is filtered by a label ID that does not exist there and is empty, with no log line; and a status label of another project would be set on the work item. The state IDs do not have this defect: cacheStateIDs keeps them in a map by project ID.

## Scope

1. Keep the label IDs for each project: a map from the project ID to a set of the four IDs (Pilot, in progress, done, failed), in the style of the state maps of cacheStateIDs. Remove the four single fields.
2. cacheLabelIDs reads the labels of each configured project (it no longer stops at the first match) and fills the set of that project. A project with no label of the Pilot name gets one Warn with the project ID and the label name. The start still fails, with the error text of today, only when no configured project has the Pilot label.
3. checkForNewIssues and recoverOrphanedIssues use the set of the project of the loop. A project with no Pilot label ID is skipped in the poll (no list request with an empty filter, which would return every work item of the project).
4. A project with no set, because its label list failed at the start or it had no Pilot label, is looked up again at each poll before the list request. The first failure of a project is a Warn; a repeat for the same project is a Debug line. When the lookup works, one Info line names the project.
5. processIssueAsync and hasStatusLabel use the set of the project of the work item (its ProjectID field). When that set has no ID for a status label, the step is skipped, as today for an empty ID.
6. The maps are read and written under the mutex that the poller already has for its state, or under a new one; a poll and a running task can use them at the same time.
7. Tests in a new test file for the poller (name it poller_test.go, beside `sdk/integrations/plane/poller.go`), with a fake Plane server (httptest) in the style of the client test file: a new test TestPollerLooksUpLabelsForEachProject — two projects whose labels have different UUIDs, one labelled work item in each: the handler gets both work items; the list request of each project carries the Pilot label ID of that project; the in-progress and the done label requests of each work item carry the label IDs of its project. A project with no Pilot label is skipped with one Warn, the other project still works, and no list request without a label filter is sent. No project has the label: Start returns the error. A project whose label list fails at the start is picked up after a later poll in which the list works.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/...` passes; paste the output into the PR body.
- [ ] Use the label set of the first project for each project -> the test with two projects fails.
- [ ] Send the list request of a project with no Pilot label ID -> its test fails.
- [ ] The PR body says how many label list requests the start makes for one, two and three projects, and how many a poll makes when each project has its set (none).

## Out of scope

The client (`client.go`). The webhook. The states. The label names. A change of the Config type. Rate limit handling.

## Refs

- Navigator TASK-658 of the Pilot repository
- Review comment on pull request 6717 of the Pilot repository (the routing by Plane project needs this)

### LEG K9 — repo: qf-studio/studio-sdk — title: fix(plane): the poller keys the label set on the project that listed the work item, runs the orphan recovery for a project found later, and names the label in its Warn

probe: present qf-studio/studio-sdk func[[:space:]]\(p[[:space:]]\*Poller\)[[:space:]]lookUpLabels\(
probe: present qf-studio/studio-sdk func[[:space:]]\(p[[:space:]]\*Poller\)[[:space:]]logLabelLookupFailure\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Follow-up of pull request 170 (issue #169), from the review after its merge. The Plane poller (`sdk/integrations/plane/poller.go`) now keeps a label set for each project. Three defects stay:

1. hasStatusLabel, the done check in checkForNewIssues and processIssueAsync take the label set by the ProjectID field of the work item (JSON key project). Before pull request 170 the status label check did not depend on that field. When a list response carries a work item with an empty project key, the item has no set: a work item with the done or the in-progress label is dispatched again, and no status label is set.
2. recoverOrphanedIssues runs one time, in Start. A project whose label set is found later, in checkForNewIssues, never gets the recovery: its work items with the in-progress label of a run before the restart are skipped at each poll.
3. logLabelLookupFailure keeps one flag for each project for two different messages. When the label list of a project fails first and a later list has no Pilot label, the line that names the label is a Debug line, so no Warn names the missing label.

## Scope

1. In checkForNewIssues, each work item of the list of a project gets that project ID when its ProjectID field is empty, before it is added to the items of the poll. In recoverOrphanedIssues the loop already uses its own project ID; keep that.
2. Move the body of the recovery for one project into a helper that takes the project ID. recoverOrphanedIssues calls it for each configured project. checkForNewIssues calls it for a project directly after a late lookup stored the set of that project, before the list request of that project.
3. logLabelLookupFailure keeps the flag for each project and each kind of failure (list failed; label not found), so the first line of each kind is a Warn and a repeat of the same kind is a Debug line.
4. Tests in the poller test file (`sdk/integrations/plane/poller_test.go`), with its fake server: a new test TestPollerUsesTheProjectOfTheList — a list response whose work item has no project key and has the done label of the project is not given to the handler; one with no status label is given to the handler, and its in-progress label request carries the label ID of the project of the list. A new test TestPollerRecoversOrphansOfAProjectFoundLater — the label list of a project fails at the start; at a later poll it works; a work item of that project with the in-progress label loses the label and is then given to the handler. A new test for the two Warn lines of item 3. Give each call of Start in the test file a context with a timeout, so a Start that does not return fails the test and does not hang the package.
5. In the test "project with no pilot label is skipped" of that file, add the check that the project with no Pilot label got no list request at all.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/...` passes; paste the output into the PR body.
- [ ] Remove the fill of the empty ProjectID -> TestPollerUsesTheProjectOfTheList fails.
- [ ] Remove the recovery call after the late lookup -> TestPollerRecoversOrphansOfAProjectFoundLater fails.
- [ ] The PR body says how many label list requests the start makes for one, two and three projects, and how many a poll makes when each project has its set.

## Out of scope

The client (`client.go`). The webhook. The states. The Config type. Rate limit handling.

## Refs

- Navigator TASK-658 of the Pilot repository
- Pull request 170 of this repository and the review comment on it

### LEG K4 — repo: qf-studio/pilot — title: chore(deps): pin studio-sdk v0.39.2 — the Plane poll path decodes the text priority, reads each page and looks up the labels for each project; a poller test against a fake Plane server

probe: present qf-studio/pilot func[[:space:]]planePollerRegistration\(
probe: present qf-studio/pilot func[[:space:]]TestPlaneDocPriorityWarningNamesPinnedSDK\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. `go.mod` pins `github.com/qf-studio/studio-sdk` v0.38.7. The SDK release v0.39.2 holds the Plane fixes of the SDK: the priority as text decodes (SDK pull request 160), one work item that does not decode is skipped with a Warn (165), the work item list follows the cursor (167), the host logger reaches the Plane client (168), the poller looks up the labels for each Plane project (170, 172). The releases v0.39.0 and v0.39.1 lie between the pin of today and v0.39.2 (v0.39.0 added a ClosedAt field to the issue snapshot of the SDK core). The daemon polls Plane through the SDK (`cmd/pilot/poller_plane.go`, planePollerRegistration). The Plane documentation page (`docs/content/integrations/plane.mdx`, section "Priority") holds a warning that the poll path cannot decode the text priority, and `internal/adapters/plane/plane_doc_test.go` pins that text to the SDK version of `go.mod`.

## Scope

1. `go.mod` and `go.sum`: `github.com/qf-studio/studio-sdk` at v0.39.2 (`go get github.com/qf-studio/studio-sdk@v0.39.2`, then `go mod tidy`). Change no other requirement on purpose; when tidy changes another line, name it in the PR body.
2. When the build or a test fails because of an SDK change between v0.38.7 and v0.39.2, make the smallest change that restores the behaviour of today, and name each such change in the PR body. Do not adopt new SDK features.
3. A new test in the Plane poller test file (`cmd/pilot/poller_plane_test.go`): TestPlanePollerAgainstFakePlaneServer. A fake Plane server (httptest) serves the label list and the work item list for two project IDs with different label UUIDs. The list of the first project has two pages (the first page says there is a next page and gives a cursor); one work item has the priority as text ("high"), and one work item of the first page does not decode (a field with a wrong type). Build the SDK Plane adapter and its poller with the config mapping that planePollerRegistration uses, with a handler that records each event; when the mapping is inline in CreateAndStart, move it into a small function that both use. Start the poller with a context that has a timeout. Assert: the handler gets each valid work item of the two projects, of each page; the broken work item is not given to the handler; the event of the work item with the text priority maps to the high priority of Pilot through sdkshim.PriorityFromSDK; the list request of each project carries the Pilot label ID of that project.
4. The documentation page (`docs/content/integrations/plane.mdx`), section "Priority": remove the warning block about the priority as a number; the section says that the poll path maps the text priority to the task priority and that the webhook path does not read the priority. In the section "Label Lookup at Start", replace the two paragraphs with the behaviour of the new SDK: the poller looks up the labels in each configured project; a project with no label of the name of `pilot_label` is skipped with a warning and looked up again at each poll; the start fails only when no configured project has the label.
5. `internal/adapters/plane/plane_doc_test.go`: adjust TestPlaneDocPriorityIsNotMapped to the new text (rename it to say what it now pins: the poll path maps the text priority, the webhook path does not read it). Keep TestPlaneDocPriorityWarningNamesPinnedSDK (it returns early when the warning is gone) and TestPlaneDocTaskIDNamesWebhookTaskID as they are.

## Acceptance

- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ ./internal/adapters/plane/ ./internal/adapters/sdkshim/ -run 'Plane'` passes; paste the output into the PR body.
- [ ] With `go.mod` back at v0.38.7 (and no other change), TestPlanePollerAgainstFakePlaneServer fails; say in the PR body with which message.
- [ ] The PR body lists each code change that the SDK step from v0.38.7 made necessary, or says that there was none.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The webhook path. The SDK. A Pilot release. Other adapters that use the SDK, beyond a fix that the build needs.

## Refs

- Navigator TASK-658 in this repo
- SDK release v0.39.2 of qf-studio/studio-sdk

### LEG M1 — repo: qf-studio/studio-sdk — title: feat(plane): a poller option leaves the state of the work item alone when the run opened a pull request — the caller sets the completed state at the merge

probe: present qf-studio/studio-sdk func[[:space:]]WithOnPRCreated\(fn[[:space:]]func\(ev[[:space:]]core\.PRCreatedEvent\)\)
probe: present qf-studio/studio-sdk type[[:space:]]PollerOption[[:space:]]func\(\*Poller\)
probe: absent  qf-studio/studio-sdk func[[:space:]]WithCompletedStateAtMerge\(

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Navigator TASK-658 of the Pilot repository. The Plane poller in `sdk/integrations/plane/poller.go` runs a handler for each work item with the trigger label. After a successful run (the result has Success true) it does three things: it removes the in-progress label, it adds the done label, and it moves the work item to the first state of the completed group of the project (UpdateIssueState with the ID from completedStateIDs). All three happen when the handler returns, which is the time of the pull request, not of the merge. A work item is then "completed" in Plane while its pull request is open, and it stays completed when the pull request closes with no merge.

The owner of the product decided: a Plane work item is done when the pull request merges. The caller (Pilot) sets the completed state at the merge. The poller must give the caller a way to keep the state change out of the poller.

The labels must NOT change with this option. The poller skips an item that carries a status label, and at its start it takes the in-progress label off each item that carries it and runs the item again. So the done label keeps its meaning "the run is finished" and stops a second run.

## Scope

1. `sdk/integrations/plane/poller.go`: a new poller option WithCompletedStateAtMerge() in the form of the other options of the file (for example WithOnPRCreated). With the option set, the poller does NOT call UpdateIssueState for the completed state when the result has Success true and a pull request number above zero. In each other case the behaviour of today stays: with no option, or with the option and a successful result with no pull request (number zero), the poller moves the item to the completed state.
2. With the option set or not: the in-progress label is removed, the done label is added on success, the pull request comment is posted, and the OnPRCreated callback fires, all as today.
3. The doc comment of the option says who sets the completed state then (the caller, at the merge) and why the labels do not change.
4. Tests in `sdk/integrations/plane/poller_test.go`, against the test server style of the file (count the state update requests and the label requests):
   - TestPollerCompletedStateAtMerge_PRLeavesStateAlone: option set, the handler returns Success true with a pull request number; assert no request moves the item to the completed state, the done label is added, the in-progress label is removed, and the OnPRCreated callback fired one time.
   - TestPollerCompletedStateAtMerge_NoPRStillCompletes: option set, Success true and pull request number zero; assert the completed state request is made.
   - TestPollerCompletedStateDefault_Unchanged: no option, Success true with a pull request number; assert the completed state request is made (the behaviour of today).
5. One line for the option in the Plane part of the package documentation or README of the SDK, where the other poller options are listed, when such a list exists.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/ -run 'TestPollerCompletedState'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./sdk/integrations/plane/` passes; paste the output into the PR body.
- [ ] Remove the option check in front of the state update -> TestPollerCompletedStateAtMerge_PRLeavesStateAlone fails
- [ ] Skip the state update for each successful result when the option is set -> TestPollerCompletedStateAtMerge_NoPRStillCompletes fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The labels. The comments of the poller and of the notifier. The started state. A call from the SDK at the merge (the SDK has no merge event). Other integrations. A release.

## Refs

- Navigator TASK-658 of qf-studio/pilot

### LEG M2 — repo: qf-studio/studio-sdk — title: feat(plane): the adapter passes the completed-state-at-merge choice from the config to the poller — a host that builds the poller through the adapter can set it

probe: present qf-studio/studio-sdk func[[:space:]]WithCompletedStateAtMerge\(\)
probe: present qf-studio/studio-sdk func[[:space:]]\(a[[:space:]]\*Adapter\)[[:space:]]NewPoller\(deps[[:space:]]core\.PollerDeps\)
probe: present qf-studio/studio-sdk func[[:space:]]TestAdapter_NewPoller_BridgesOnPRCreated\(
probe: absent  qf-studio/studio-sdk CompletedStateAtMerge[[:space:]]+bool

## Context

This issue is for this repository (qf-studio/studio-sdk); the code is in the working tree.

Navigator TASK-658 of the Pilot repository. The poller option WithCompletedStateAtMerge in `sdk/integrations/plane/poller.go` (issue 187 of this repository) makes the poller leave the state of a work item alone when a successful run opened a pull request; the caller sets the completed state at the merge.

A host cannot set that option through the adapter. The production host (Pilot) builds the poller with New and the method NewPoller of the adapter in `sdk/integrations/plane/adapter.go`. NewPoller takes the poller dependencies of the core package and builds its own option list (the handler, the processed store, the logger, the limit of parallel runs, the pull request callback). It has no way to add the new option. The poller dependencies type of the core package is shared by each integration and must NOT get a Plane-only field.

## Scope

1. `sdk/integrations/plane/types.go`: the Config type gets a field CompletedStateAtMerge of type bool with the yaml name completed_state_at_merge and a comment that says what it does and that the default (false) keeps the behaviour of today.
2. `sdk/integrations/plane/adapter.go`: NewPoller adds WithCompletedStateAtMerge() to its option list when the config field is true. No other change of the method.
3. Tests in `sdk/integrations/plane/adapter_test.go`, in the style of TestAdapter_NewPoller_BridgesOnPRCreated (a test server; the handler of the poller dependencies returns Success true with a pull request number):
   - TestAdapter_NewPoller_CompletedStateAtMerge: config field true; run the poller until the work item is processed; assert that no request moves the work item to the completed state and that the done label is added.
   - TestAdapter_NewPoller_CompletedStateDefault: config field false; assert that the completed state request is made.
4. When the README or the package documentation lists the config fields of the Plane integration, add the new field there.

## Acceptance

- [ ] `go test -race -count=1 ./sdk/integrations/plane/ -run 'TestAdapter_NewPoller'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./sdk/integrations/plane/` passes; paste the output into the PR body.
- [ ] Remove the new option from the option list of NewPoller -> TestAdapter_NewPoller_CompletedStateAtMerge fails
- [ ] Add the option also when the config field is false -> TestAdapter_NewPoller_CompletedStateDefault fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The core package and its poller dependencies type. The poller logic of issue 187. The labels and the comments. Other integrations. A release.

## Refs

- Navigator TASK-658 of qf-studio/pilot

### LEG M3 — repo: qf-studio/pilot — title: feat(plane): the pull request state keeps the Plane project and work item of its task — the link survives a restart; pin studio-sdk v0.39.5

probe: present qf-studio/pilot LinearDoneNotified[[:space:]]+bool
probe: present qf-studio/pilot func[[:space:]]routedOnPRCreated\(component,[[:space:]]defaultRepo[[:space:]]string
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]persistPRState\(prState[[:space:]]\*PRState\)
probe: absent  qf-studio/pilot PlaneDoneNotified[[:space:]]+bool
probe: absent  qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]SetPlaneLink\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The owner of the product decided: a Plane work item is done when its pull request merges. Today the SDK poller moves the work item to the completed state when the run opens the pull request, and Pilot has no Plane code on the merge path. The change comes in two steps. This issue is the first: Pilot remembers which Plane work item a pull request belongs to. The next issue uses that link at the merge. This issue changes NO behaviour that a Plane user can see.

How the pieces fit:

- `cmd/pilot/poller_plane.go` builds the Plane poller of the SDK. The handler closure gets an issue event with the Plane project ID (the field ProjectID) and the work item ID (the field IssueID, a UUID). After the handler returns with a pull request, the SDK poller fires the OnPRCreated callback with an event that holds the pull request number, its URL, the branch name and IssueID (the same work item UUID). The event has no project ID.
- The callback registers the pull request with an autopilot controller. There are two branches in that file: with a project pairing, the function newRoutedOnPRCreated of `cmd/pilot/pr_controller_resolve.go` finds the controller of the repository from the pull request URL (routedOnPRCreated, shared with Linear); with no pairing, the default controller gets a direct OnPRCreated call.
- The controller keeps a PRState for each pull request (`internal/autopilot/types.go`) and stores it in the table autopilot_pr_state (`internal/autopilot/state_store.go`). The Linear merge step is the model: the field LinearDoneNotified, the column linear_done_notified with its migration line and its place in each insert, update and select of the store.
- The branch name of a Plane task holds only a sequence number, so the merge step cannot find the work item from the branch as Linear and Jira do. The project ID and the work item ID must be stored.

`go.mod` pins `github.com/qf-studio/studio-sdk` v0.39.3. SDK v0.39.5 adds a config field CompletedStateAtMerge to the Plane config of the SDK (default false). This issue pins v0.39.5 and does NOT set that field.

## Scope

1. `go.mod` and `go.sum`: the SDK at v0.39.5 (go get, then go mod tidy). Change no other requirement on purpose; name each other changed line in the PR body. Do not set the new SDK config field anywhere.
2. `internal/autopilot/types.go`: PRState gets three fields with doc comments in the style of LinearDoneNotified: PlaneProjectID (string), PlaneWorkItemID (string), PlaneDoneNotified (bool). The last one is not read by any code of this issue; it is stored for the next step.
3. `internal/autopilot/state_store.go`: three columns (plane_project_id and plane_work_item_id as text with the default empty; plane_done_notified as integer with the default 0), each with a migration line in the form of linear_done_notified, and in each place where linear_done_notified is written or read (the create statement, each insert, the conflict update, each select and its scan). A database that was made before this change loads with empty values.
4. `internal/autopilot/controller.go`: a new method SetPlaneLink(prNumber int, projectID, workItemID string) on the controller. When the controller tracks that pull request, it sets the two fields under the lock that the other state writers use and persists the state with persistPRState. When the pull request is unknown or one of the IDs is empty, it does nothing and returns false; it returns true when it stored the link.
5. `cmd/pilot/poller_plane.go` and, where necessary, `cmd/pilot/pr_controller_resolve.go`: after the Plane callback registered a pull request, the SAME controller gets SetPlaneLink with the project ID and the work item ID. The project ID is not in the callback event: the handler closure records the pair (work item ID to project ID) for each run that returns a pull request, in a small map that is safe for parallel runs, and the callback reads and removes the entry. Both branches (the routed one and the default one) store the link. The Linear callback keeps its behaviour: when you extend the shared routing function or the registrar interface, Linear must not call SetPlaneLink.
6. Tests:
   - In the directory of `internal/autopilot/state_store.go`: TestPlaneLink_StateStoreRoundTrip (save a PRState with the three fields set, load it from a new store on the same database file, assert the three values) and TestPlaneLink_OldDatabaseLoads (a database without the three columns gets them by the migration, and an old row loads with empty values).
   - In the directory of `internal/autopilot/controller.go`: TestPlaneLink_SetPlaneLink with the cases: known pull request (the fields are set and are in the store after a reload), unknown pull request (false, no row), empty ID (false).
   - In the directory of `cmd/pilot/poller_plane.go`: TestPlaneOnPRCreated_StoresLink for the routed branch and for the default branch, with a fake registrar that records the calls: after the handler path recorded the pair and the callback fired, the registrar got OnPRCreated and then SetPlaneLink with the right pull request number, project ID and work item ID. A case with no recorded pair: OnPRCreated is called, SetPlaneLink is not.
   - A test that the Linear callback makes no SetPlaneLink call (extend the existing test of the routed callback when one exists).

## Acceptance

- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/autopilot/ -run 'TestPlaneLink'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestPlaneOnPRCreated|TestPlanePoller'` passes; paste the output into the PR body.
- [ ] Remove plane_work_item_id from the conflict update of the store -> TestPlaneLink_SetPlaneLink fails
- [ ] Remove the SetPlaneLink call from the default branch of the Plane callback -> TestPlaneOnPRCreated_StoresLink fails
- [ ] The PR body says that the SDK config field CompletedStateAtMerge is set nowhere, with the output of a text search for it.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The state change at the merge and the notifier for it (next issue). The SDK option (it stays off). Comments in Plane. The labels. A pull request that closes with no merge. Linear, Jira and the other adapters, except that their tests still pass.

## Refs

- Navigator TASK-658 in this repo

### LEG M4 — repo: qf-studio/pilot — title: fix(plane): one comment for each event — a run with a pull request posts one pull request comment, not three

probe: present qf-studio/pilot func[[:space:]]buildPlaneExecutionComment\(result[[:space:]]\*executor\.ExecutionResult
probe: present qf-studio/pilot planeNotifier\.LinkPR\(issueCtx
probe: present qf-studio/pilot func[[:space:]]handlePlaneIssueWithResult\(ctx[[:space:]]context\.Context
probe: absent  qf-studio/pilot func[[:space:]]planeResultComment\(

Depends on: #{{M3}}

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The owner of the product decided: a Plane work item gets ONE comment for each event (start, pull request, merge, failure). Today a successful run with a pull request posts four comments:

1. Start: the handler closure in `cmd/pilot/poller_plane.go` calls NotifyTaskStarted of the SDK notifier ("Pilot started working on this issue").
2. Result: handlePlaneIssueWithResult in `cmd/pilot/handlers.go` posts the text of buildPlaneExecutionComment ("Pilot execution completed successfully", with the pull request link, the branch and the duration).
3. Link: the handler closure in `cmd/pilot/poller_plane.go` calls LinkPR of the SDK notifier ("Pull Request Created").
4. The SDK poller posts "PR created" with the number and the link after the handler returns. This comment has a tracking ID, so a second run for the same pull request does not post it again. Pilot cannot turn it off, and it is the one comment that stays for the pull request event.

A failed run posts one comment (the failure text of handlePlaneIssueWithResult). A run that succeeds with no change posts one comment ("no changes were made"). These two cases must keep their one comment.

## Scope

1. `cmd/pilot/poller_plane.go`: remove the LinkPR call and its warning. Keep NotifyTaskStarted. Keep each other statement of the closure, also the code that issue #{{M3}} added there.
2. `cmd/pilot/handlers.go`: move the decision "which comment does the handler post after the run" out of handlePlaneIssueWithResult into a new function planeResultComment. It takes what the decision needs (the execution error, the handler result, the branch name) and returns the comment text and a flag that says if a comment is posted. handlePlaneIssueWithResult calls it and posts the text when the flag is true. The rules:
   - an execution error: the failure comment of today;
   - success with no commit and no pull request (and not an epic): the "no changes" comment of today, and the result counts as not successful as today;
   - success WITH a pull request URL: NO comment (the SDK poller posts the pull request comment);
   - success with a commit and no pull request, or an epic: the comment of buildPlaneExecutionComment as today;
   - a result that failed and is not terminal by design: the failure comment of today;
   - a result that is terminal by design (superseded or canceled): no comment, as today.
   Keep each other effect of the function (the returned result, the Success flag change for the no-change case).
3. Tests in the directory of `cmd/pilot/handlers.go` (a new test file, name it handlers_plane_comment_test.go): TestPlaneResultComment as a table with one case for each rule above; assert the flag and one distinctive part of the text.
4. A test that the Plane closure makes no LinkPR call: extend TestPlanePollerAgainstFakePlaneServer in `cmd/pilot/poller_plane_test.go` when the fake server can count comment requests for a run with a pull request (then assert the count of comments whose text holds "Pull Request Created" is zero). When the test cannot drive a run with a pull request without the executor, say so in the PR body and give the output of a text search for LinkPR in the directory of `cmd/pilot/poller_plane.go` in place of the test.
5. `docs/content/integrations/plane.mdx`: when the page lists the comments that Pilot posts, make the list match: start, pull request, failure, no change.

## Acceptance

- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestPlaneResultComment|TestPlanePoller'` passes; paste the output into the PR body.
- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] Return the success comment also when the result has a pull request URL -> TestPlaneResultComment fails
- [ ] Return no comment for an execution error -> TestPlaneResultComment fails
- [ ] The PR body lists, for each event (start, pull request, failure, no change), the one place that posts its comment after this change.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The SDK and its notifier. The comment at the merge (the issue for the merge step posts it). A comment for a pull request that closes with no merge. The labels and the states. Other adapters.

## Refs

- Navigator TASK-658 in this repo

### LEG M5 — repo: qf-studio/pilot — title: feat(plane): a Plane work item is completed when its pull request merges — one time, with one comment; the poller no longer completes it at the pull request

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]SetPlaneLink\(prNumber[[:space:]]int
probe: present qf-studio/pilot PlaneDoneNotified[[:space:]]+bool
probe: present qf-studio/pilot func[[:space:]]registerLinearDoneNotifier\(
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]notifyLinearDone\(ctx
probe: present qf-studio/pilot func[[:space:]]planeSDKConfig\(cfg[[:space:]]\*plane\.Config\)
probe: absent  qf-studio/pilot type[[:space:]]PlaneDoneNotifier[[:space:]]interface
probe: absent  qf-studio/pilot CompletedStateAtMerge:[[:space:]]+true

Depends on: #{{M4}}

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. The owner of the product decided: a Plane work item is done when its pull request merges. Today the Plane poller of the SDK moves the work item to the completed state when the run returns, which is the time of the pull request.

What exists:

- The SDK (pinned v0.39.5, package sdk/integrations/plane) has a config field CompletedStateAtMerge. When it is true, the poller built by the adapter does NOT move the work item to the completed state for a successful run that has a pull request; the caller sets that state at the merge. The labels do not change: the poller still removes the in-progress label and adds the done label, and that done label stops a second run. A successful run with no pull request is still completed by the poller. Pilot maps its Plane config to the SDK config in planeSDKConfig in `cmd/pilot/poller_plane.go`; the field is set nowhere today.
- Each pull request state of the autopilot controller (PRState in `internal/autopilot/types.go`) holds PlaneProjectID and PlaneWorkItemID since issue 6739 (set by SetPlaneLink, stored in the table autopilot_pr_state), and a flag PlaneDoneNotified that no code reads yet.
- The model for the merge step is Linear: the interface LinearDoneNotifier and the method notifyLinearDone in `internal/autopilot/controller.go`, called from handleMerging and from the merged branch of the check for an external merge (two call sites, each beside notifyJiraDone); the flag LinearDoneNotified is set after the first attempt and stored at once, also when the call failed; a failure is a warning and never stops the merge. The wiring is registerLinearDoneNotifier in `cmd/pilot/poller_linear.go` (the default controller and each controller of a repository); the implementation is in `cmd/pilot/linear_state.go`. The tests are in `internal/autopilot/controller_linear_done_test.go`.
- The SDK client has ListStates(ctx, workspaceSlug, projectID), which gives each state with its group (the group of done states has the text completed), UpdateIssueState(ctx, workspaceSlug, projectID, workItemID, stateID), and AddCommentWithTracking, which posts a comment with an external ID so that the same comment is not posted two times.

The two changes of this issue must go together. With the option on and no merge step, a work item with a pull request never reaches the completed state.

## Scope

1. `internal/autopilot/controller.go`: an interface PlaneDoneNotifier with one method NotifyMerged(ctx, projectID, workItemID, prURL string) error; a field for it on the controller; a setter SetPlaneDoneNotifier; a method notifyPlaneDone in the form of notifyLinearDone. notifyPlaneDone does nothing when the notifier is nil, when PlaneProjectID or PlaneWorkItemID of the state is empty, or when PlaneDoneNotified is true. In the other case it calls the notifier one time, logs a warning on an error, sets PlaneDoneNotified and stores the state at once (also after an error). Call it at the two places where notifyLinearDone is called.
2. A new file in the directory of `cmd/pilot/poller_plane.go` (name it plane_done.go) with the implementation: it lists the states of the project, takes the first state whose group is completed, calls UpdateIssueState, and then posts ONE comment with AddCommentWithTracking (the text says that the pull request merged and holds the link; the external ID is built from the work item ID and the word merged, so that a second attempt posts no second comment). When the project has no state of the completed group, it returns an error that names the project. An error of the state change is returned and no comment is posted then.
3. `cmd/pilot/poller_plane.go`: where the Plane poller is started, wire the notifier on the default controller and on each controller of a repository, in the form of registerLinearDoneNotifier. In planeSDKConfig set the SDK config field CompletedStateAtMerge to true when, and only when, an autopilot controller exists that takes the merge step (the same condition under which the Plane callback for a created pull request is not nil). With no controller, the field stays false and the poller completes the work item as today; say in a comment why.
4. Tests:
   - A new file in the directory of `internal/autopilot/controller_linear_done_test.go` (name it controller_plane_done_test.go), in the form of the Linear tests: TestNotifyPlaneDone_HandleMerging, TestNotifyPlaneDone_ExternalMerge, TestNotifyPlaneDone_OncePerPR (a second call makes no second notifier call, also after a reload of the state from the store), TestNotifyPlaneDone_NilNotifier, TestNotifyPlaneDone_NoLink (empty IDs: no call), TestNotifyPlaneDone_ErrorStillSetsFlag.
   - A new test file beside plane_done.go against a test server: TestPlaneDoneNotifier_MovesToCompletedAndComments (the state request carries the ID of the first completed-group state; exactly one comment request, with the pull request link); TestPlaneDoneNotifier_NoCompletedState (an error, no state request, no comment); TestPlaneDoneNotifier_StateErrorNoComment.
   - In `cmd/pilot/poller_plane_test.go`: TestPlaneSDKConfig_CompletedStateAtMerge (true with a controller, false with none).
5. `docs/content/integrations/plane.mdx`: say when a work item reaches the completed state (at the merge of its pull request; at the end of the run when the run has no pull request), and that the done label is set at the end of the run.

## Acceptance

- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/autopilot/ -run 'TestNotifyPlaneDone|TestNotifyLinearDone|TestPlaneLink'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestPlaneDoneNotifier|TestPlaneSDKConfig|TestPlanePoller|TestPlaneOnPRCreated'` passes; paste the output into the PR body.
- [ ] Remove the notifyPlaneDone call from the external merge branch -> TestNotifyPlaneDone_ExternalMerge fails
- [ ] Set the SDK config field to true also with no controller -> TestPlaneSDKConfig_CompletedStateAtMerge fails
- [ ] Post the comment before the state change returns -> TestPlaneDoneNotifier_StateErrorNoComment fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The SDK. The labels. A pull request that closes with no merge (the work item stays in the started state with the done label; a later issue). A named review state. The other comments. Linear and Jira, except that their tests still pass.

## Refs

- Navigator TASK-658 in this repo

### LEG M6 — repo: qf-studio/pilot — title: fix(plane): a work item whose pull request no controller takes is completed at the pull request — it no longer stays open for ever

probe: present qf-studio/pilot func[[:space:]]planeRoutedOnPRCreated\(defaultRepo[[:space:]]string
probe: present qf-studio/pilot func[[:space:]]registerRoutedPR\(component,[[:space:]]defaultRepo[[:space:]]string
probe: present qf-studio/pilot func[[:space:]]newPlaneDoneNotifier\(client[[:space:]]planeDoneAPI
probe: present qf-studio/pilot CompletedStateAtMerge:[[:space:]]+mergeStep
probe: absent  qf-studio/pilot func[[:space:]]completePlaneWorkItemWithoutController\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-658. Since issue 6751 a Plane work item is completed when its pull request merges. The pieces, all in the directory of `cmd/pilot/poller_plane.go`:

- planeSDKConfig sets the SDK config field CompletedStateAtMerge when a callback for created pull requests exists. With that field the SDK poller does NOT move a work item with a pull request to the completed state; it only sets the done label.
- The callback registers the pull request with an autopilot controller and stores the Plane link on it. The merge step of that controller (notifyPlaneDone in `internal/autopilot/controller.go`) later calls the notifier of `cmd/pilot/plane_done.go`, which moves the work item to the first state of the completed group and posts one comment.

The gap: with a project pairing the callback is planeRoutedOnPRCreated. It finds the controller from the pull request URL with registerRoutedPR (`cmd/pilot/pr_controller_resolve.go`). When that function returns no controller (the URL is not a GitHub pull request URL, for example a GitLab merge request; or the repository has no controller), the callback returns. The SDK option is on for the whole poller, so the poller did not complete the work item, and no controller watches the pull request. The work item then stays in the started state for ever. Before issue 6751 the poller completed it at the pull request.

## Scope

1. In the directory of `cmd/pilot/poller_plane.go`: when the routed Plane callback gets no controller for a pull request and the handler recorded a Plane project for the work item, complete the work item at once: move it to the first state of the completed group (reuse the state lookup and the state change of the notifier in `cmd/pilot/plane_done.go`; split that code so that the state change can be called without the merge comment). Post NO merge comment: the pull request did not merge, and the SDK poller already posted the pull request comment. Put this into a function with the name completePlaneWorkItemWithoutController, and log one Info line that names the work item, the pull request URL and the reason. An error is a warning; it does not stop the callback.
2. The default branch of the callback (no project pairing) has a controller in each case and does not change.
3. The path with a controller does not change: the work item is completed at the merge, with the comment.
4. Tests in `cmd/pilot/poller_plane_test.go` (or the test file beside the notifier): TestPlaneOnPRCreated_NoControllerCompletesNow with a fake state API: a pull request URL that is not a GitHub URL gives one state change to the completed-group state and no comment; a pull request of a repository with a controller gives no state change at that time; a pull request with no recorded Plane project gives no call; an error of the state change is logged and does not panic.
5. `docs/content/integrations/plane.mdx`: one sentence in the part about the completed state: a work item whose pull request Pilot does not watch (for example a GitLab merge request) is completed when the pull request opens.

## Acceptance

- [ ] `go test -race -count=1 ./cmd/pilot/ -run 'TestPlaneOnPRCreated|TestPlaneDoneNotifier|TestPlaneSDKConfig'` passes; paste the output into the PR body.
- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] Return from the routed callback with no state change when no controller takes the pull request -> TestPlaneOnPRCreated_NoControllerCompletesNow fails
- [ ] Post the merge comment in the no-controller path -> TestPlaneOnPRCreated_NoControllerCompletesNow fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The SDK. A merge step for GitLab or other hosts. A pull request that closes with no merge. The labels. The controller code.

## Refs

- Navigator TASK-658 in this repo

## Refs

- Research of 2026-11-25: code research of the Plane paths (session report); Plane API reference (introduction: authentication, rate limits, pagination; work item object: `priority` is a string).
- Outside contributions: pull request 6509 and 6510 (merged 2026-11-25), pull request 6513 (closed), SDK pull requests 160 (open, CI green) and 161 (open).
- Held legs, written after their preconditions: K4 (Pilot pins the new SDK release and gets a poller test with a fake Plane server and a text priority; needs the SDK release), K6 (remove the unused internal Plane client, poller and notifier; after K3 merged).
- Dispatch SOP: `.agent/sops/development/dispatch-task-legs-to-pilot.md`; SDK checkout for the dispatch script: a clone outside this repository (`--checkout qf-studio/studio-sdk=PATH`).
- - K1 → https://github.com/qf-studio/studio-sdk/issues/163
- K2 → https://github.com/qf-studio/studio-sdk/issues/164
- K3 → https://github.com/qf-studio/pilot/issues/6705
- K5 → https://github.com/qf-studio/pilot/issues/6706
- K7 → https://github.com/qf-studio/pilot/issues/6707
- K6 → https://github.com/qf-studio/pilot/issues/6719
- K8 → https://github.com/qf-studio/studio-sdk/issues/169
- K9 → https://github.com/qf-studio/studio-sdk/issues/171
- K4 → https://github.com/qf-studio/pilot/issues/6722
- M1 → https://github.com/qf-studio/studio-sdk/issues/187
- M2 → https://github.com/qf-studio/studio-sdk/issues/189
- M3 → https://github.com/qf-studio/pilot/issues/6739
- M4 → https://github.com/qf-studio/pilot/issues/6741
- M5 → https://github.com/qf-studio/pilot/issues/6751
- M6 → https://github.com/qf-studio/pilot/issues/6761

---

**Last Updated**: 2026-11-25
