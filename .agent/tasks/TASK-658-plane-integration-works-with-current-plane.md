# TASK-658: Plane integration works with a current Plane server — text priority on the SDK path, no lost tickets, webhook label match

**Status**: 🎫 **State 2026-11-25 16:28Z: five tickets are FILED and NOT dispatched (no label `pilot`): studio-sdk [#163](https://github.com/qf-studio/studio-sdk/issues/163) and [#164](https://github.com/qf-studio/studio-sdk/issues/164), pilot [#6705](https://github.com/qf-studio/pilot/issues/6705), [#6706](https://github.com/qf-studio/pilot/issues/6706) and [#6707](https://github.com/qf-studio/pilot/issues/6707). Founder, 11-25: "Create tickets, do not dispatch them ... I'll tell you to pick up this roadmap." The dispatch (the label `pilot` on each ticket) waits for the founder's word.** Done: the outside SDK pull request 160 (the priority as text on the SDK path) is merged, 16:05Z, on the founder's "run it" of the same day. Held legs, not written: K4 (Pilot pins a new SDK release, with a poller test) and K6 (removal of the unused internal Plane code). Roadmap document: `.agent/system/plane-integration-roadmap.md`. Two design decisions wait for the founder (done at the merge; one comment for each event).
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
- [ ] 2. One broken work item no longer blocks the page; the poller logs it and goes on (qf-studio/studio-sdk#163)
- [ ] 3. The SDK reads each page of work items (qf-studio/studio-sdk#164, needs 2)
- [ ] 4. A new SDK release holds 1 to 3 (operator, needs 1, 2, 3)
- [ ] 5. Pilot uses that SDK release, with a test of the poller against a fake Plane server (needs 4)

### Phase 2: The webhook path and the documentation
- [ ] 6. The webhook accepts the Pilot label by its name (#6705)
- [ ] 7. The Plane documentation page matches the code (#6706)
- [ ] 8. The unused internal Plane client, poller and notifier are removed (needs 6)

### Phase 3: Several Plane projects
- [ ] 9. A project entry can name its Plane project, and the poller routes each ticket to that repository (#6707)

### Side tracks
- [ ] The work item is set to done at the merge, not at the pull request (needs a trace of the pull request lifecycle for Plane tasks)
- [ ] One comment for each event in Plane (verify the three writers first)

### Waits for you
- [x] The merge of the outside SDK pull request 160 (step 1; founder, 11-25: "run it")
- [x] The go for the plan and the roadmap (founder, 11-25)
- [ ] The word to pick up the roadmap: then the label `pilot` goes on the five tickets (founder, 11-25: tickets only, no dispatch)
- [ ] A decision on "done at the merge" and on the comments in Plane (two design points; see the roadmap document)

---

## Implementation

### Phase 1

**Facts**: SDK `ListWorkItems` (`sdk/integrations/plane/client.go` in the SDK repository) does one GET on the work items path, with `?label=UUID` when a label is given, and decodes into `paginatedResponse{Results []WorkItem, TotalCount int}`. The poller calls it at two places (new items, orphaned in-progress items) and treats an error as "skip this project for this poll". The Plane API: header `X-API-Key`; 60 requests each minute for each key, with `X-RateLimit-Remaining` and `X-RateLimit-Reset`; pagination with `per_page` (default and maximum 100) and `cursor`, response fields `next_cursor`, `prev_cursor`, `next_page_results`, `prev_page_results`, `count`, `total_pages`, `total_results`, `results`.

Order: K1 and K2 touch the same function, so K2 depends on K1. Step 1 (the outside pull request) touches only the types file and its test.

### Phase 2

**Facts**: the webhook handler is built at one place (`internal/pilot/pilot.go`, NewWebhookHandler) and has no API client today; it knows only the configured label text. The SDK client has the label list call that the poller uses to turn the name into a UUID.

### Later (no leg yet)

The two side tracks need a decision first; each gets its own leg after that. The roadmap document `.agent/system/plane-integration-roadmap.md` holds the proposal for each.

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

---

**Last Updated**: 2026-11-25
