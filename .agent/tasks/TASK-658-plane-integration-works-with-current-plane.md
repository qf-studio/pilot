# TASK-658: Plane integration works with a current Plane server — text priority on the SDK path, no lost tickets, webhook label match

**Status**: 🚀 **State 2026-11-26 17:08Z: studio-sdk#169 is merged (SDK pull request 170, 17:04Z; reviewed, no blocking finding). The daemon's review found eight minor defects and its follow-up was skipped by the daily cap, so the session dispatched [studio-sdk#171](https://github.com/qf-studio/studio-sdk/issues/171) at 17:08Z (leg K9): the label set by the project of the list request, the orphan recovery for a project found later, the Warn that names the label, two test notes. The SDK release waits for the merge of #171; then the pin ticket in Pilot.** **State 2026-11-26 16:54Z: founder: "go labels and release updates in sdk". Dispatched 16:54Z: [studio-sdk#169](https://github.com/qf-studio/studio-sdk/issues/169), the SDK poller looks up the labels for each Plane project (leg K8, new in the plan). Order: #169 merges → one SDK release by a tag (it holds the SDK pull requests 165, 167, 168 and the one of #169) → the ticket in which Pilot pins that release (poller test, the priority warning of the Plane page removed, `plane_doc_test.go` adjusted).** **State 2026-11-26 16:46Z: the five tickets of the dispatch are MERGED, each reviewed by the session after its merge with no blocking finding (comment on each pull request):** studio-sdk#163 (PR 165), studio-sdk#164 (PR 167), pilot #6705 (PR#6714), #6706 (PR#6715), #6707 (PR#6717). The daemon's own reviews filed and merged two follow-ups: studio-sdk#166 (PR 168: a nil logger keeps the default, a response with no results is an empty list, the host logger reaches the Plane client) and pilot #6716 (PR#6718: three text fixes of the Plane page, with `internal/adapters/plane/plane_doc_test.go`). **In work: [#6719](https://github.com/qf-studio/pilot/issues/6719), the removal of the unused internal Plane client, poller and notifier (dispatched 16:45Z).** **Held: the pin of the new SDK release with a poller test (needs an SDK release after PR 168; the last release v0.39.1 is older). That ticket must also remove the priority warning of the Plane page and adjust `plane_doc_test.go`, which pins the text about the pinned SDK.** **Found by the reviews, not in the plan (founder decision open, word "labels"):** the SDK poller takes the Pilot label ID from the first configured project and filters each project by it (cacheLabelIDs); Plane labels belong to one project, so with two or more Plane projects only one is picked up on the poll path, and the routing of #6707 has one project to route. All these runs went with the option `run_as_user` on (TASK-651), with no failure. **State 2026-11-26 15:16Z: the five tickets are DISPATCHED (label `pilot` added at 15:16Z; founder, 11-26: "let's ship the Plane roadmap"): studio-sdk#163 and #164 (#164 waits for #163 by its Depends-on line), pilot #6705, #6706 and #6707. These are also the first runs with the option `run_as_user` on (TASK-651); the session reads each run and reviews each pull request after its merge.** **State 2026-11-25 16:28Z: five tickets are FILED and NOT dispatched (no label `pilot`): studio-sdk [#163](https://github.com/qf-studio/studio-sdk/issues/163) and [#164](https://github.com/qf-studio/studio-sdk/issues/164), pilot [#6705](https://github.com/qf-studio/pilot/issues/6705), [#6706](https://github.com/qf-studio/pilot/issues/6706) and [#6707](https://github.com/qf-studio/pilot/issues/6707). Founder, 11-25: "Create tickets, do not dispatch them ... I'll tell you to pick up this roadmap." The dispatch (the label `pilot` on each ticket) waits for the founder's word.** Done: the outside SDK pull request 160 (the priority as text on the SDK path) is merged, 16:05Z, on the founder's "run it" of the same day. Held legs, not written: K4 (Pilot pins a new SDK release, with a poller test) and K6 (removal of the unused internal Plane code). Roadmap document: `.agent/system/plane-integration-roadmap.md`. Two design decisions wait for the founder (done at the merge; one comment for each event).
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
- [ ] 4. A new SDK release holds 1 to 3 (operator, needs 1, 2, 3)
- [ ] 5. Pilot uses that SDK release, with a test of the poller against a fake Plane server (needs 4)

### Phase 2: The webhook path and the documentation
- [x] 6. The webhook accepts the Pilot label by its name (#6705)
- [x] 7. The Plane documentation page matches the code (#6706)
- [ ] 8. The unused internal Plane client, poller and notifier are removed (#6719, dispatched 11-26)

### Phase 3: Several Plane projects
- [x] 9. A project entry can name its Plane project, and the poller routes each ticket to that repository (#6707)

### Side tracks
- [ ] The work item is set to done at the merge, not at the pull request (needs a trace of the pull request lifecycle for Plane tasks)
- [ ] One comment for each event in Plane (verify the three writers first)

### Waits for you
- [x] The merge of the outside SDK pull request 160 (step 1; founder, 11-25: "run it")
- [x] The go for the plan and the roadmap (founder, 11-25)
- [x] The word to pick up the roadmap (founder, 11-26: "let's ship the Plane roadmap"); the label `pilot` is on the five tickets since 15:16Z
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

---

**Last Updated**: 2026-11-25
