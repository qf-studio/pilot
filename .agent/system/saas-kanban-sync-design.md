# Pilot SaaS — Mixed-Tracker Kanban Sync Engine (Design)

**Created**: 2026-07-13 · **Status**: DESIGN (target design; staged delivery)
**Parent**: `saas-architecture.md` · **Task doc**: `.agent/tasks/TASK-405-pilot-saas-platform.md`

**Scope staging note (reconciles with the architecture doc):** the architecture's v1 board is
this design's READ PATH plus the three write verbs (dispatch / approve-reject / close), with
`conflict_journal` populated from day one. This doc's full field-level bidirectional write-back
(status/labels/priority/title/body) is the v2 "true sync" item — the data model below is built
in v1 so v2 turns on writes without a migration. §8's "v1 MUST ship" list reflects THIS
component's eventual v1, not SaaS-Phase-1.

## ⚠️ Post-verification corrections

1. **GitHub SDK pagination is NOT exhaustive** (contradicts §2's "Wrap only"): `ListIssues` issues a single request with no paging params (≈30 items), `ListReleases`/`ListTags` fetch one page; only `ListPullRequests` and tag resolution loop (≤50 pages). S3 must implement real cursor/page-following for `ListUpdatedSince`/`ListAll` — it is new code, not a wrapper.
2. **Label hygiene invariant (affects §6)**: pilot's poller Unmarks a processed issue when its status labels (`pilot-in-progress`/`pilot-done`/`pilot-failed`) are removed — re-arming dispatch by design. Board write-back must NEVER remove pilot status labels; the outbound op builder must treat `pilot-*` labels as pilot-owned (additive `pilot` trigger label only).
3. **Plane idempotency (§3.4)**: `external_source/external_id` works for comments (`AddCommentWithTracking`), but NOT for issue creation (`CreateIssue` sends no external fields) — the HTML-comment idem-marker fallback applies to Plane issue creation too (Plane is deferred anyway).
4. **`api.golden` locks `sdk/core` only** — S1's new sync contract lands under the lock, but integrations implementations (S3–S5) are unfrozen; pin the SDK version consumed by console-api per release.

---

# Mixed-Tracker Kanban Sync Engine — Technical Design

**Scope:** the sync layer inside `console-api` (control plane) that materializes one customer board from N tracker connections (Jira + Linear + GitHub in v1), propagates board edits back, and treats the customer's Pilot instance as just another tracker-side writer.

---

## 0. The one load-bearing simplification

**A "mixed board" means cards from different providers side by side — NOT one card mirrored into multiple trackers.**

Every card has **exactly zero or one external link** (`card_links` is 1:0..1 per card in v1). A card born in Jira lives in Jira; a card born on the board picks a single "home tracker" (or stays board-only). This collapses the problem from N-way replication (vector clocks, cross-provider ID fan-out, N² conflict matrix) to N independent **pairwise** syncs: `board card ↔ its one native issue`. Multi-link mirroring is explicitly deferred (§9). Everything below assumes pairwise sync.

---

## 1. Canonical data model (RDS Postgres, owned by console-api)

```sql
-- One board per org in v1 (boards table exists for later multi-board)
CREATE TABLE boards (
  id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL
);

CREATE TABLE cards (
  id            uuid PRIMARY KEY,
  board_id      uuid NOT NULL REFERENCES boards(id),
  title         text NOT NULL,
  body          text NOT NULL DEFAULT '',
  status        text NOT NULL,          -- canonical vocab, §5
  priority      text NOT NULL DEFAULT 'none', -- core.NormalizePriority vocab: urgent|high|medium|low|none
  labels        text[] NOT NULL DEFAULT '{}',
  assignee_display text,                -- READ-ONLY in v1 (no cross-tracker identity map)
  origin        text NOT NULL,          -- 'board' | provider name
  version       bigint NOT NULL DEFAULT 1,  -- bumped on every accepted change
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  archived_at   timestamptz
);

CREATE TABLE card_links (
  card_id           uuid PRIMARY KEY REFERENCES cards(id),
  connection_id     uuid NOT NULL,      -- FK to connections (tracker credential)
  provider          text NOT NULL,      -- github|linear|jira|...
  native_issue_id   text NOT NULL,      -- UUID/GID/nodeID — the stable key
  sequence_id       text NOT NULL,      -- provider-prefixed human key: PROJ-42, GH #42
  native_url        text NOT NULL,
  provider_updated_at timestamptz,      -- last remote updated_at we ingested
  sync_state        text NOT NULL DEFAULT 'ok', -- ok|orphaned|parked
  UNIQUE (connection_id, native_issue_id)
);

-- The "shadow": last-known-synced snapshot of the remote issue, per link.
-- This is the base version for 3-way diff (§4). One row per link.
CREATE TABLE link_shadows (
  card_id     uuid PRIMARY KEY REFERENCES card_links(card_id),
  snapshot    jsonb NOT NULL,           -- canonical-field snapshot {title, body, status, priority, labels}
  updated_at  timestamptz NOT NULL
);

-- Outbound write-back op log (§3)
CREATE TABLE card_ops (
  id            uuid PRIMARY KEY,
  card_id       uuid NOT NULL,
  connection_id uuid NOT NULL,
  field         text NOT NULL,          -- one field per op: status|labels|priority|title|body|comment
  payload       jsonb NOT NULL,         -- {old, new} or {comment_body, idem_key}
  idem_key      text NOT NULL UNIQUE,   -- sha256(card_id, field, new_value_hash, card.version)
  state         text NOT NULL DEFAULT 'pending', -- pending|inflight|applied|superseded|parked
  attempts      int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL,
  last_error    text,
  created_at    timestamptz NOT NULL
);

CREATE TABLE sync_cursors (
  connection_id uuid NOT NULL,
  cursor_kind   text NOT NULL,          -- 'delta_watermark' | 'full_sweep_at'
  value         text NOT NULL,
  PRIMARY KEY (connection_id, cursor_kind)
);

CREATE TABLE status_maps (               -- §5
  connection_id    uuid NOT NULL,
  canonical_status text NOT NULL,
  provider_state   text NOT NULL,        -- state id/name/transition target, provider-specific
  is_primary       boolean NOT NULL,     -- reverse-map winner when N canonical → 1 provider state
  PRIMARY KEY (connection_id, canonical_status)
);

CREATE TABLE sync_conflicts (             -- observability, §4/§8
  id uuid PRIMARY KEY, card_id uuid, field text,
  board_value jsonb, remote_value jsonb, winner text, -- 'board'|'remote'
  resolved_at timestamptz NOT NULL
);
```

Key points:

- **`sequence_id` preserves the SDK's provider-prefix scheme** (`PROJ-42`, `GL-42`, GitHub bare number → rendered `#42`). Pilot's branch naming (`pilot/GH-*`, etc.) depends on it; the card displays it verbatim and the instance-proxy join (§6) keys on it.
- **The shadow is the heart of the engine.** Storing the last synced snapshot per link is what turns "two states, who wins?" into a proper 3-way diff and makes echo suppression free (§4).
- Comments are **not** canonical card state. They're mirrored into a separate `card_comments` read-model (append-only, no conflicts by construction) and board-authored comments go through `card_ops` with `field='comment'`.

## 2. Ingest: polling is the engine, webhooks are a deferred accelerator

The SDK's `Poller`/`IssueEvent` path is **unusable** for sync: trigger-label filtered, `Action created|updated` only, no status/assignee/timestamps. We do **not** bend it. We build on the SDK's **clients** behind a new contract:

```go
// studio-sdk sdk/core/sync.go (new)
type IssueSnapshot struct {
    NativeID, SequenceID, Title, Body string
    State       string      // provider-native state id/name
    StateGroup  string      // provider's own category where it has one (§5)
    Labels      []string
    Priority    string      // already normalized via core.NormalizePriority
    Assignee    string      // display only
    URL         string
    CreatedAt, UpdatedAt time.Time
    Deleted     bool
}

type SyncSource interface {
    ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page Cursor) ([]IssueSnapshot, Cursor, error)
    ListAll(ctx context.Context, projectID string, page Cursor) ([]IssueSnapshot, Cursor, error) // full sweep
    GetIssue(ctx context.Context, nativeID string) (IssueSnapshot, error)
}

type SyncWriter interface {
    UpdateFields(ctx context.Context, nativeID string, fields FieldPatch) (IssueSnapshot, error) // returns post-write snapshot
    TransitionState(ctx context.Context, nativeID, providerState string) error
    AddComment(ctx context.Context, nativeID, body, idemKey string) error
    CreateIssue(ctx context.Context, projectID string, draft IssueDraft) (IssueSnapshot, error)
}
```

Putting this in `sdk/core` (a new `SyncCapable` alongside `Pollable`, per the risk note that bending `IssueEvent` fights the contract) keeps one connector codebase; the `api.golden` lock test manages the v0.x churn.

**Per-provider delta mechanics (v1 = GitHub, Linear, Jira):**

| Provider | `ListUpdatedSince` | Full sweep | Existing SDK support | Gap work |
|---|---|---|---|---|
| GitHub | REST `GET /issues?since=&sort=updated&direction=asc` | REST pagination (exhaustive already) | Retry/RateLimitError/pagination all production-grade | Wrap only |
| Linear | GraphQL `issues(filter:{updatedAt:{gt:$since}}, orderBy: updatedAt)` | GraphQL cursor loop | Client exists; **`first:50` no-cursor bug** | Fix cursor-follow; add updatedAt filter; add retry |
| Jira | JQL `project=X AND updated >= "<ts>"` ORDER BY updated ASC | JQL paged by `startAt/maxResults` | JQL search + transitions exist | Add retry; add `CreateIssue` + `UpdateFields` (plain REST, small) |

**Cursor discipline:** watermark = max `UpdatedAt` seen in a *committed* page, advanced only after the page is reconciled; every poll re-scans with a **2× poll-interval overlap lookback** (tolerates provider clock skew, pagination races, and second-granularity timestamps). Overlap is harmless because ingest is idempotent — re-ingesting an unchanged snapshot diffs empty against the shadow.

**Sweep schedule per connection:** delta poll every 60s (adaptive up to 10min under rate pressure); **full `ListAll` sweep every 6h** to catch deletions and any delta the provider dropped. Vanished natives → `sync_state='orphaned'` on the link, card badged on the board; **never auto-delete a card** (a Jira permission hiccup must not eat the customer's board).

**Webhooks (v1.5, not v1):** console-api (public, unlike instances) later exposes `POST /webhooks/{provider}/{connection_id}`, reusing the SDK's existing per-provider signature verification — **verification mandatory** (the Linear "warn and disable" fallback is removed for SaaS). Handlers do **fetch-on-notify**: never trust webhook payload as state, just enqueue `GetIssue(nativeID)` for immediate reconcile. Consequence: **webhook loss is a latency event, not a correctness event** — polling remains the backstop forever. Customers paste the URL into their tracker manually (no webhook-registration APIs exist in the SDK); programmatic registration is deferred with OAuth apps.

## 3. Outbound write-back with idempotency

Board edits never touch a tracker synchronously. The API handler:
1. applies the change to `cards` (optimistic `version` check),
2. enqueues one `card_ops` row **per field** (single-field ops → partial failure is per-field, and a newer op on the same field marks older pending ones `superseded`),
3. returns — the UI is optimistic; the op worker converges the tracker.

**Op worker** (per-connection FIFO, one in-flight op per card):
1. `GetIssue` → **compare-before-write**: if the remote value already equals the target, mark `applied` (this makes replays and crashed-mid-write retries idempotent for value-writes — status/labels/priority/title/body are naturally idempotent *by value*).
2. Execute via `SyncWriter`; for Jira status this is `GetTransitions` → `TransitionIssueTo` (name-based, per the SDK surface).
3. On success: write the returned post-write snapshot **into the shadow immediately** and bump `card_links.provider_updated_at`. This is the echo-suppression mechanism — see §4.
4. Comments (not idempotent by value): Plane-style `external_source/external_id` where supported; elsewhere embed `<!-- pilot-op:{idem_key} -->` in the comment body and check for it on retry before re-posting. (GitHub/Jira/Linear all preserve HTML comments in markdown/ADF bodies.)

**Failure handling:** exponential backoff via a generalized `WithRetry` (§8), max 5 attempts, then `state='parked'` + a board-visible "sync issue" badge on the card + entry in the activity feed. Parked ops are user-retryable. An op that keeps failing never blocks *other* cards (FIFO is per-card, not per-connection, except that writes share the connection's rate budget).

## 4. Conflict resolution — decision: shadow-based 3-way diff, per-field LWW, remote-wins tiebreak

**Rejected: whole-object LWW.** The dominant real workflow is *disjoint concurrent edits*: customer drags the card to In Progress on our board while a teammate edits the description in Jira. Object-level LWW silently destroys one of those edits every time. Unacceptable for a product whose pitch is "manage everything from our board."

**Rejected: field-level 3-way *merge* (text merging bodies, CRDTs).** Trackers give us one `updatedAt` per issue, not per field; polling loses intermediate versions, so we cannot reconstruct true edit history. Merging prose bodies without it produces garbage. Massive complexity, no trustworthy input.

**Chosen: per-field last-writer-wins over a 3-way diff against the shadow.**

Reconcile algorithm (runs for every ingested snapshot and is the single write path into `cards` for remote changes):

```
base   = link_shadows[card]              // last synced state
remote = incoming IssueSnapshot          // mapped to canonical fields (§5)
local  = current card

remoteChanged = fields where remote != base
localChanged  = fields where local  != base   // == fields with pending/inflight card_ops

for f in remoteChanged \ localChanged:  card[f] = remote[f]          // clean remote edit
for f in localChanged  \ remoteChanged: (nothing — op queue will push) // clean local edit
for f in remoteChanged ∩ localChanged:                                 // true conflict
    remote wins: card[f] = remote[f]; supersede pending op on f
    log sync_conflicts row; surface in activity feed
shadow = remote; provider_updated_at = remote.UpdatedAt
```

Why **remote (tracker) wins** the tiebreak: the external tracker is the customer's incumbent system of record shared with people who don't use our board yet; a clobbered Jira edit destroys trust in the whole product, while a clobbered board drag is visible immediately on the very screen the user is looking at (the card snaps back, with a toast: "Updated in Jira just now — your change was not applied"). Losing loudly on our side beats losing silently on theirs. Timestamps are only used *within* one link (never compared across providers) and are advisory; the ordering that actually decides is "did a pending local op exist when the remote change arrived."

Echo suppression falls out for free: step 3 of the op worker writes the post-write snapshot into the shadow, so when our own write comes back via polling, `remote == base` → empty diff → no-op. This matters because **actor-based filtering is impossible here**: the board sync worker, the Pilot instance, and the human customer may all authenticate with the *same PAT* — we can never distinguish writers by identity, only by value fingerprint. The shadow is therefore mandatory, not an optimization.

## 5. Status mapping across heterogeneous workflows

Canonical status vocabulary (fixed, small):

```
backlog | todo | queued | in_progress | in_review | done | canceled
```

Board columns render canonical statuses. Two columns are special:
- **Queued** = `todo` + side effect: the outbound op applies the **`pilot` trigger label** on the native issue — this is the "tracker as message bus" dispatch from the winning architecture. No provider state transition unless the customer maps one.
- **Needs You** is *not* a status at all — it's an overlay computed from `approval_mirror`; it never writes to any tracker.

Per-connection `status_maps` translate both directions, **auto-seeded from the provider's own state taxonomy** (every v1 provider has one):

| Provider | Native taxonomy used for seeding | Transition mechanism |
|---|---|---|
| Linear | workflow state `type`: backlog/unstarted/started/completed/canceled | `UpdateIssueState` (GraphQL, exists) |
| Jira | status **category**: To Do / In Progress / Done | `GetTransitions` → `TransitionIssueTo` (exists) |
| GitHub | `open`/`closed` (+ optional Projects-V2 status column via existing `ProjectBoardSync`) | close/reopen; `UpdateProjectItemStatus` when a project is linked |
| (later) Plane/AzDO/GitLab | state groups / state categories / scoped-label convention | existing clients |

Seed rules: `backlog→backlog-type`, `todo,queued→unstarted`, `in_progress,in_review→started`, `done→completed`, `canceled→canceled`. Customer can re-map any row in the dashboard (e.g., point `in_review` at Jira's "Code Review" status).

**Lossiness is explicit and directional:**
- N canonical → 1 provider state (GitHub: everything before done maps to `open`): outbound transition fires only when the *provider* state actually changes (compare-before-write already handles this); the extra granularity lives only on the board.
- 1 provider state → N canonical: the reverse map uses the `is_primary` row; **and** the reconciler only moves the card when the *provider* state changed vs the shadow — so a GitHub issue sitting `open` never yanks a card back from `in_review` to `todo`. This asymmetry (board can be finer than tracker) is the whole reason people will use our board.

## 6. Pilot as just-another-writer

The Pilot instance **never talks to console-api's database or sync engine.** Its entire interaction surface is the tracker, exactly as today:

1. Customer drags card → Queued → sync engine writes `pilot` label to the native issue.
2. Instance poller (30s, existing, unmodified) picks it up; `ProcessedStore` dedups.
3. Pilot's existing Notifier transitions the issue to in-progress / comments progress / links the PR / transitions to done — **all of which arrive at the board through the normal ingest path in §2, indistinguishable from human edits.** Card moves to In Progress → In Review → Done with zero Pilot-specific sync code.
4. Execution *enrichment* (live run status, logs, PR checks, cost) is **not sync data**: the dashboard joins card → `sequence_id` → instance-proxy `/api/v1/{status,history,logs}` at render time. Sync owns issue state; the proxy owns execution telemetry. Keeping these planes separate means a sync outage never lies about a running execution and vice versa.

One rule this imposes: **dispatch requires a link.** A board-only card can't reach a Pilot instance (nothing to label). Dragging an unlinked card to Queued prompts "create this issue in ⟨GitHub/Linear/Jira⟩ first?" — one click runs `CreateIssue` on the chosen home tracker, links the card, then applies the label.

Loop safety: Pilot's writes are value-writes ingested via shadow diff; the board's reaction to them is card-state-only (no automatic write-back is triggered by an inbound change — outbound ops are created only by user actions and the Queued side effect). The label side effect is idempotent (label already present → compare-before-write no-op). No feedback loop is constructible.

## 7. Failure modes and handling

| Failure | Handling |
|---|---|
| **Webhook loss / no webhooks at all** | Non-event by design: polling is authoritative, webhooks (when added) only reduce latency. Fetch-on-notify means even a forged/mangled payload can't inject state. |
| **Missed delta (provider timestamp weirdness, pagination race)** | Overlap lookback on every poll + 6h full sweep. Idempotent ingest makes double-delivery free. |
| **Deletion on the tracker** | Detected by full sweep (deltas rarely report deletes). Link → `orphaned`, card badged, never auto-deleted; user resolves (archive / re-link / re-create). |
| **Rate limits** | Generalized typed `RateLimitError` + `WithRetry` honoring Retry-After (lift `github/retry.go`, kill the "status 429" string-matching when generalizing — classify per-provider in each connector, not centrally). Per-connection token-bucket budget split ~70/30 read/write, **writes preempt polling** (a customer's drag is latency-sensitive; a poll is not). Adaptive poll interval backs off to 10min under sustained 429s; `sync_lag_seconds` metric exposes it. |
| **Split-brain (board and tracker both edited)** | §4: per-field 3-way, remote-wins, conflict logged and surfaced. Per-card single-inflight-op FIFO prevents interleaved partial writes; superseded ops collapse. |
| **Crashed mid-write** | Op `inflight` with stale lease → retried; compare-before-write + idem-key comment markers make the retry safe whether or not the first write landed. |
| **console-api down** | Trackers keep working (they're the SoR); Pilot instances keep executing already-labeled work (label bus is durable state in the tracker). On recovery, delta cursors resume; nothing is lost, only delayed. |
| **Poison op (bad Jira transition config, revoked scope)** | Backoff → `parked` + card badge + activity-feed entry; doesn't block the connection. Auth errors (`AuthError`) park immediately (no retry) and flag the *connection* as unhealthy in the dashboard. |
| **Clock skew across providers** | Timestamps never compared across links; within a link, ordering decisions come from the shadow + pending-op set, not raw timestamp comparison. |
| **Two console workers grab one connection** | Per-connection worker leases (Postgres `FOR UPDATE SKIP LOCKED` on a `connection_leases` row, heartbeat + TTL). Horizontal scale = shard connections across workers; a connection is always single-threaded. |

## 8. v1 vs deferred

**v1 MUST ship:**
- Providers: **GitHub, Linear, Jira** (covers the pitch "Jira + Linear + GitHub on one board"; the other four SDK trackers are wiring work once the contract exists).
- Pairwise sync, shadow 3-way, per-field LWW remote-wins, conflict log + activity feed.
- Bidirectional fields: **status, labels, priority**; **title/body** bidirectional on GitHub/Linear/Jira (Jira needs the small `UpdateFields`/`CreateIssue` REST additions); **comments**: inbound mirror + board-authored append (no edit).
- Read-only on card: assignee display, timestamps, native URL, comments thread.
- Status maps with taxonomy auto-seed + dashboard editor.
- Queued→`pilot` label dispatch; create-in-tracker for unlinked cards; execution enrichment via instance proxy.
- Polling delta + overlap + 6h sweep + orphan detection; per-connection budgets; op queue with park/retry UX.
- SDK fixes this hard-depends on: Linear cursor pagination, Linear/Jira retry+rate-limit classification, generalized `WithRetry`.

**Deferred (in rough priority order):**
1. Webhooks fetch-on-notify (latency win only) + eventually programmatic webhook registration.
2. Remaining 4 providers (GitLab, AzDO, Asana, Plane) — plus CreateIssue parity for Asana/GitLab/AzDO.
3. OAuth app flows (Jira 3LO, GitHub App, Linear OAuth) replacing paste-a-PAT.
4. Assignee **write-back** (needs a cross-tracker identity map — a real sub-project).
5. Multi-link mirroring (one card in two trackers) — re-opens N-way sync; only if customers demand it.
6. Custom fields, attachments, re-homing a card to a different tracker, body text merge.
7. Comment edit/delete sync.

## 9. New Go packages / work items (cuttable into issues)

**studio-sdk (module `github.com/qf-studio/studio-sdk`):**

| # | Item |
|---|---|
| S1 | `sdk/core/sync.go`: `IssueSnapshot`, `Cursor`, `FieldPatch`, `SyncSource`, `SyncWriter`, `SyncCapable` — extend `api.golden` |
| S2 | `sdk/util/retry`: generalize `github/retry.go` (`WithRetry`, typed `RateLimitError`/`AuthError`); per-connector error classification, no string matching |
| S3 | `integrations/github`: implement `SyncCapable` (wrap existing list/`since`/write methods; optional Projects-V2 status via existing `ProjectBoardSync`) |
| S4 | `integrations/linear`: cursor-follow pagination fix + `updatedAt` filter + `SyncCapable` + retry adoption |
| S5 | `integrations/jira`: `SyncCapable` (JQL `updated >=`), **new** `CreateIssue` + `UpdateFields`, retry adoption |

**console-api (new repo `pilot-console`):**

| # | Package / item |
|---|---|
| C1 | `internal/board` — canonical types, status vocab, `cards`/`card_links`/`link_shadows`/`card_ops`/`sync_cursors`/`status_maps`/`sync_conflicts` migrations + stores |
| C2 | `internal/sync/engine` — reconciler (3-way diff, LWW policy, shadow commit); pure, table-test heavy |
| C3 | `internal/sync/ingest` — per-connection leased worker: delta poll, overlap, full sweep, orphan detect |
| C4 | `internal/sync/outbound` — op worker: FIFO-per-card, compare-before-write, idem markers, park/retry |
| C5 | `internal/sync/statusmap` — taxonomy auto-seeder + CRUD API |
| C6 | `internal/sync/throttle` — per-connection budgets, write preemption, adaptive intervals |
| C7 | `internal/board/api` — board REST (cards CRUD, drag = status op, comment post, conflict/activity feed, parked-op retry) |
| C8 | `internal/board/dispatch` — Queued side effect (`pilot` label), create-in-tracker flow, sequence_id ↔ instance-proxy execution join |
| C9 | Metrics: `sync_lag_seconds{connection}`, `card_ops_pending`, `sync_conflicts_total`, `orphaned_links` → ops Prometheus |

**Dashboard:** kanban UI itself is a separate epic (drift-ui wireframes are the IA spec); the sync engine's UI contract is exactly C7's API.

Dependency order: S1→S2 → {S3,S4,S5} ∥ C1→C2 → {C3,C4} → C5–C9. C2 (the reconciler) is the only genuinely novel logic and is a pure function over `(base, remote, local)` — build and exhaustively table-test it first; everything else is plumbing around it.