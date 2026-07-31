# feat(syncengine): C2 — pure 3-way reconciler (per-field LWW, remote-wins) + transactional commit

## Context

Blocked by: #85

This is **C2 of the S4 mixed-tracker kanban sync engine** — the only genuinely novel logic in the whole engine: a **pure function** that reconciles `(base, remote, local)` card field-sets, plus the transactional commit that applies its result through the `internal/board` store (shipped by the issue this one is blocked by). Ingest workers (C3) and outbound op workers (C4) are separate follow-ups that FEED this engine — do not build them here.

Design rationale you need (the design doc lives in a sibling repo you must NOT read; everything required is embedded here):

- **Rejected: whole-object LWW.** The dominant real workflow is disjoint concurrent edits (customer drags the card to In Progress on our board while a teammate edits the description in Jira). Object-level LWW silently destroys one of those edits every time.
- **Rejected: field-level 3-way text merge / CRDTs.** Trackers give one `updatedAt` per issue, not per field; polling loses intermediate versions, so true edit history cannot be reconstructed. Merging prose bodies without it produces garbage.
- **Chosen: per-field last-writer-wins over a 3-way diff against the shadow** (`link_shadows.snapshot` = last-synced state). Algorithm:

```
base   = shadow                      // nil on first ingest
remote = incoming snapshot           // already mapped to canonical fields by the caller
local  = current card

remoteChanged = fields where remote != base
localChanged  = fields where local  != base  OR  a pending/inflight card_op exists on the field

for f in remoteChanged \ localChanged:  card[f] = remote[f]           // clean remote edit
for f in localChanged  \ remoteChanged: nothing                       // op queue will push
for f in remoteChanged ∩ localChanged:                                // true conflict
    remote wins: card[f] = remote[f]; supersede pending op on f
    journal a sync_conflicts row
shadow = remote (always)
```

- **Why remote (tracker) wins the tiebreak:** the external tracker is the customer's incumbent system of record shared with people who don't use our board yet. A clobbered Jira edit destroys trust silently; a clobbered board drag is visible immediately on the very screen the user is watching (the card snaps back, with a toast). Losing loudly on our side beats losing silently on theirs. Timestamps are NEVER compared across providers and never decide the outcome — the deciding fact is "did a pending local op exist when the remote change arrived."
- **Echo suppression falls out for free:** the outbound op worker (C4) writes its post-write snapshot into the shadow, so when our own write comes back via polling, `remote == base` → empty diff → no-op. Actor-based filtering is impossible (board worker, Pilot instance, and human may share one PAT) — the shadow is the only echo filter there is.
- **Status asymmetry (load-bearing product behavior):** the board's canonical vocabulary (`backlog|todo|queued|in_progress|in_review|done|canceled`) is finer than most providers' (GitHub: `open`/`closed`). Status change detection MUST happen in **provider-state space**: a GitHub issue sitting `open` must never yank a card from `in_review` back to `todo`. Only when the provider-native state actually changed vs the shadow does the card move — to the canonical status the caller pre-mapped (via `status_maps`, `is_primary` reverse row). This asymmetry — the board can be finer than the tracker — is the reason people will use the board.

This task must NOT be decomposed — implement as a single PR. <!-- pilot:no-decompose -->

## Acceptance

1. **Package `internal/syncengine`** — deliberately NOT `internal/sync/engine`: this repo keeps `internal/` flat (one level, verified convention), and a package named `sync` would shadow stdlib `sync`, which `internal/fleet` imports heavily. Record this naming rationale in the package doc comment.

2. **Pure core** (`internal/syncengine/reconcile.go`), mirroring the shape of `internal/fleet`'s `decide` (see its doc comment: single pure decision owner, no I/O, no clock, exhaustively table-tested):

```go
// Field identifies one reconcilable card field.
type Field string // FieldTitle | FieldBody | FieldStatus | FieldPriority | FieldLabels

// Snapshot is the canonical field-set the engine diffs. ProviderState is the
// provider-native status identifier; status change detection happens on it,
// never on Status (the §5 asymmetry). Labels compare as an order- and
// duplicate-insensitive set.
type Snapshot struct {
    Title, Body   string
    Status        string   // canonical vocab (board.Status)
    ProviderState string   // provider-native state id/name
    Priority      string
    Labels        []string
}

type Input struct {
    Base         *Snapshot       // nil = first ingest for this link
    Remote       Snapshot        // caller has already mapped provider→canonical via status_maps
    Local        Snapshot        // current card
    PendingLocal map[Field]bool  // fields with pending|inflight card_ops
}

type Change struct { Field Field; Value any }
type Conflict struct { Field Field; BoardValue, RemoteValue any }

type Result struct {
    CardChanges []Change    // apply to the card
    Superseded  []Field     // pending ops to mark superseded
    Conflicts   []Conflict  // sync_conflicts rows to journal (winner is always "remote")
    NewShadow   Snapshot    // always == Remote
    Echo        bool        // remote == base on every field: nothing to do beyond shadow refresh
}

// Reconcile is pure: no I/O, no clock, no store access.
func Reconcile(in Input) Result
```

3. **Semantics — every rule below is a named row (or row family) in ONE table test, written FIRST** (the `TestDecide` pattern from `internal/fleet/reconciler_test.go`: anonymous struct slice, `name` first, `want` last, stdlib assertions only — no testify):
   - **R1 echo**: all fields `remote == base` → `Echo: true`, no changes, no conflicts, `NewShadow = Remote`.
   - **R2 clean remote edit**: field changed remotely, not locally dirty → card takes the remote value.
   - **R3 clean local edit**: field locally dirty, unchanged remotely → NO card change, NO supersede (the op queue pushes it; superseding here would silently drop the user's edit).
   - **R4 true conflict**: changed on both sides to different values → remote wins: card change + conflict row + supersede (supersede only if `PendingLocal[f]`).
   - **R5 convergent edit**: both sides changed to the SAME value → not a conflict, no card change, but supersede the pending op (its target is already true; pushing it would be a pointless write).
   - **R6 status via provider state**: `remoteChanged(status) ⇔ Remote.ProviderState != Base.ProviderState`. Test row: GitHub `open`→`open` with card at `in_review` and `Remote.Status == "todo"` → card stays `in_review`, no conflict. Test row: provider state changed → card takes `Remote.Status`.
   - **R7 labels as a set**: `["a","b"]` vs `["b","a","a"]` is NOT a change; a real difference replaces the whole set (field granularity is the set).
   - **R8 first ingest** (`Base == nil`): adopt remote wholesale — every differing field becomes a card change, zero conflicts, no supersede. (By construction the create-in-tracker flow seeds the shadow from the `CreateIssue` response before any reconcile can run, so nil-base with a locally-dirty card cannot occur; the behavior must still be deterministic and tested.)
   - **R9 locally-dirty definition**: a field is dirty iff `local[f] != base[f]` OR `PendingLocal[f]` — the "did a pending op exist when the remote change arrived" clause. Test row: `local == base` but `PendingLocal[f]` true + remote changed → conflict path (remote wins, supersede), not clean-remote.
   - Every `Result` from a non-nil-base input carries `NewShadow == Remote` — assert in all rows.

4. **Transactional commit** (`internal/board/commit.go`, added to the C1 store in THIS PR): `Store.CommitReconcile(ctx, cardID uuid.UUID, p CommitReconcileParams) error` where params carry the field changes, superseded fields, conflict rows, marshaled shadow snapshot, and `providerUpdatedAt time.Time`. ONE transaction: update card fields + `version = version + 1` (exactly once per commit regardless of change count) + `updated_at = now()` → mark this card's pending/inflight ops on superseded fields `superseded` → append `sync_conflicts` rows (`winner='remote'`) → upsert `link_shadows` + `card_links.provider_updated_at`. No optimistic version check — remote is authoritative here; a racing user PUT serializes on the row lock and its op reconciles next cycle (document this in the method comment). Echo results skip the card update but still refresh the shadow timestamp.

5. **Engine wrapper** (`internal/syncengine/engine.go`): `Reconciler` struct holding a narrow consumer-side interface (the repo idiom — declare it here, doc comment `// Satisfied by *board.Store.`) with method `ReconcileAndCommit(ctx, cardID, in Input) (Result, error)` = pure `Reconcile` + `CommitReconcile`. No polling loop, no provider clients, no goroutines — C3 feeds it.

6. **Tests**: the exhaustive pure table (rule rows above + a multi-field matrix row exercising R2+R3+R4 simultaneously in one input) in `reconcile_test.go` — no DB gate, runs everywhere. Plus DB-gated `commit_test.go` (C1's `newTestStore` fixture pattern: raw-SQL org + connection + board + card + link + pending op): commit is atomic, version bumps exactly once, only listed fields superseded (another field's pending op untouched), conflict rows land with correct values, shadow upserted, `provider_updated_at` set; echo commit refreshes shadow only.

7. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

File plan: `internal/syncengine/reconcile.go` · `internal/syncengine/reconcile_test.go` · `internal/syncengine/engine.go` · `internal/board/commit.go` · `internal/board/commit_test.go`. Import direction: `syncengine` → `board` (never the reverse — the pure core imports nothing but stdlib).

Sequencing inside the PR: pure `Reconcile` + its exhaustive table FIRST (self-contained, no DB), then `CommitReconcile` + DB-gated tests, wrapper last.

**Scope fence (do NOT build here):** ingest/polling/cursor logic and provider→canonical status mapping (C3 — the caller maps before invoking) · outbound op execution, compare-before-write, leases (C4) · status-map seeding (C5) · HTTP routes (C7) · any studio-sdk import (this repo deliberately does not depend on the SDK; the engine is provider-agnostic) · config/env vars/main.go changes (nothing runs until C3 wires a loop).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-31 · https://github.com/qf-studio/pilot-console/issues/86 (labels: pilot, no-decompose; gated on #85)
- Blocked by: #85 (board data model — tables + store this commit leg writes through).
- Pure-core precedent in this repo: `internal/fleet/reconciler.go` `decide` + `reconciler_test.go` `TestDecide` (issue #24/PR#25).
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §4 (conflict resolution), §5 (status asymmetry) — embedded above; do not read the sibling repo.
