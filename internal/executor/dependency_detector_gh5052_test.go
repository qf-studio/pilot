package executor

import (
	"reflect"
	"testing"
)

// GH-5045/GH-5052: table-driven regression tests for the two dispatch-time
// extractors added to dependency_detector.go. The path-extraction cases use
// real issue bodies (fetched verbatim from GH-5021 @ qf-studio/pilot and
// GH-120/124/139 @ qf-studio/pilot-console, the "ui" repo referenced in the
// GH-5045 task text) so the heuristic is validated against genuine prose
// rather than only hand-picked synthetic strings.

// gh5021Body is the real issue body of qf-studio/pilot#5021 at the time this
// test was written — two Go source-file citations, no "Depends on"/"Blocked
// by" marker.
const gh5021Body = "" +
	"<!--autopilot-meta\n" +
	"parent: GH-5019\n" +
	"inherited-spec: true\n" +
	"-->\n\n" +
	"Parent: GH-5019\n\n" +
	"Two related defects in the same package. (a) In `internal/executor/runner.go` (~line 4906, from PR#5016), when the contract-dependency lookup is configured AND the project has contract dependencies, a `GetDiffAgainstOrigin` error must route through the standard contract-evidence failure sequence (alert + webhook + recorder) with `result.Success=false`, not skip the gate with a Warn log; preserve the current skip-and-warn behavior when lookup is unset or the project has no deps. (b) In `internal/executor/contract_evidence.go`, when `required=true` but zero field tokens were extracted, short-circuit before `getContractEvidence` (no LLM/structured-output subprocess call) and record the result as passed with `Required=false` consistently. Add tests: configured-project diff-error -> contract-evidence failure path fires (assert alert + webhook + recorder); unconfigured project + same failure -> unchanged; zero-extracted-fields -> call-count assertion proves no LLM invocation and task proceeds. Both defects live in `internal/executor/` -- bundled to avoid intra-package merge conflicts.\n\n" +
	"## Scope fence\n\n" +
	"Implement ONLY the slice described above."

// ui120Body is the real issue body of qf-studio/pilot-console#120 at the
// time this test was written.
const ui120Body = "## Problem\n\n" +
	"`GET /api/v1/board/activity` is a conflict journal wearing an activity feed's name: it reads only `sync_conflicts`, the DTO's `kind` is hardcoded `\"conflict\"` (`internal/boardapi/dto.go`, `toActivityDTO`), and only two things ever write -- the sync reconciler's LWW conflicts and the C14 decision route (which shoehorns decisions in as pseudo-conflicts: `AppendConflict{Field:\"decision\", ...}`, `internal/boardapi/decision.go:136`). Dispatch, status changes -- the events the bell popover and board activity feed are designed around (`design/approvals-v1.html` activity glance) -- journal nothing.\n\n" +
	"## Context (verified at merge of PR#119)\n\n" +
	"- Activity route + limit semantics: `internal/boardapi/handlers.go` `handleActivity` (default 50, max 200, 400 non-numeric).\n" +
	"- Current storage: `sync_conflicts` (migration `0008_board.up.sql`) -- conflict-specific columns (`field`, `board_value`, `remote_value`, `winner`). Writers: `internal/syncengine/engine.go:129` (`CommitReconcile`) and the decision route.\n" +
	"- Migrations: next free number **0011**.\n" +
	"- Dispatch handler: `internal/boardapi/dispatch.go` `handleDispatch` (:72).\n\n" +
	"## Acceptance\n\n" +
	"1. Migration `0011_activity_journal` (+`.down.sql`).\n" +
	"2. aws s3 cp is not relevant here, this is `make test` territory.\n" +
	"3. `GET /api/v1/board/activity` serves the union.\n\n" +
	"## Refs\n\n" +
	"- Design: `design/approvals-v1.html` (activity glance + annotations) @ pilot-console-ui"

// ui124Body is the real issue body of qf-studio/pilot-console#124.
const ui124Body = "## Problem\n\n" +
	"Settings -> General (design: `settings-v1.html` screen 3) needs org rename. Today the org name is written once at `POST /api/v1/orgs` and never changeable -- no PUT/PATCH route exists on `/api/v1/org`.\n\n" +
	"## Context (verified at origin/main)\n\n" +
	"- Org model + store: `internal/orgs/store.go` (`organizations.name`); create-side validation lives in `handleCreateOrg` (`internal/orgs/handlers.go:72`) -- reuse its name rules exactly.\n" +
	"- Route registration: `internal/orgs/handlers.go:72-78`; house pattern for mutations: `Authenticate` + `bff.CSRFGuard`.\n\n" +
	"## Acceptance\n\n" +
	"1. `PUT /api/v1/org` -- body `{\"name\": \"...\"}` -> 200 with the updated org.\n" +
	"2. Store method (`RenameOrg` or equivalent) updates `name` only.\n\n" +
	"## Refs\n\n" +
	"- Design: `design/settings-v1.html` screen 3 (General) @ pilot-console-ui\n" +
	"- Program: TASK-478 @ qf-studio/pilot `.agent/tasks/` (CON-4)"

// ui139Body is the real issue body of qf-studio/pilot-console#139.
const ui139Body = "## Summary\n\n" +
	"Automated PR created by Pilot for task GH-138.\n\n" +
	"Closes #138\n\n" +
	"## Context\n\n" +
	"Live factory-path testing (2026-08-16 evening, un-patched ship-test re-run) proved PR#133's exists-path convergence is unreachable for existing tenants: `ensureInstanceProfile` (`internal/fleet/tenantres.go:195-205` @ `f2e8c81`) returns the profile ARN as soon as `GetInstanceProfile` succeeds -- `ensureRole` is only called on the profile-not-found branch. Observed live: a role created before `BinaryS3URI` was configured never gained the `s3:GetObject` statement across multiple provisions -> box `aws s3 cp` AccessDenied -> no binary -> ready check exit 7 -> `provision.failed`, repeatably.\n\n" +
	"## Fix\n\n" +
	"`ensureInstanceProfile`'s exists-path must still call `f.ensureRole(ctx, orgID, name)` before returning the existing profile ARN. `BinaryS3Bucket/BinaryS3ReleasesPrefix` and `tenantres.created` are unaffected.\n\n" +
	"## Refs\n\n" +
	"GH-126 -> GH-132/PR#133 lineage -- found live in TASK-405 un-patched re-run 2026-08-16"

func TestExtractReferencedPaths(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "GH-5021 real body: two Go source citations, directory-only ref excluded",
			body: gh5021Body,
			want: []string{
				"internal/executor/runner.go",
				"internal/executor/contract_evidence.go",
			},
		},
		{
			name: "ui#120 real body: line-ref suffix stripped, dedup across repeats, shell/URL/bare-name noise excluded",
			body: ui120Body,
			want: []string{
				"internal/boardapi/dto.go",
				"internal/boardapi/decision.go",
				"design/approvals-v1.html",
				"internal/boardapi/handlers.go",
				"internal/syncengine/engine.go",
				"internal/boardapi/dispatch.go",
			},
		},
		{
			name: "ui#124 real body: extensionless API routes and bare filenames excluded, range-ref suffix stripped",
			body: ui124Body,
			want: []string{
				"internal/orgs/store.go",
				"internal/orgs/handlers.go",
				"design/settings-v1.html",
			},
		},
		{
			name: "ui#139 real body: bare SHA, slash-but-no-extension, and shell-command noise excluded",
			body: ui139Body,
			want: []string{
				"internal/fleet/tenantres.go",
			},
		},
		{
			name: "excludes URLs even with a path-like shape",
			body: "See `https://example.com/owner/repo/blob/main/internal/foo.go` for context.",
			want: nil,
		},
		{
			name: "excludes whitespace-containing backtick spans (shell commands)",
			body: "Run `make test` and `aws s3 cp foo/bar.txt s3://bucket/` before merging.",
			want: nil,
		},
		{
			name: "excludes extensionless bare filenames with no path separator",
			body: "See `0008_board.up.sql` for the migration.",
			want: nil,
		},
		{
			name: "excludes bare API routes with no extension",
			body: "Route is `/api/v1/orgs`, not `/api/v1/org/rename`.",
			want: nil,
		},
		{
			name: "dedups repeated citations of the same path",
			body: "See `internal/foo.go` and again `internal/foo.go`.",
			want: []string{"internal/foo.go"},
		},
		{
			// GH-5133: the incident body citing `cmd/pilot/*.go` — a plain
			// single-star glob — as a natural way to reference "the Go
			// files under cmd/pilot". Must never be treated as a
			// checkable prerequisite path.
			name: "excludes a plain single-star glob (GH-5133 incident shape)",
			body: "See the Go files under `cmd/pilot/*.go` for context.",
			want: nil,
		},
		{
			name: "excludes a recursive double-star glob",
			body: "Touches everything under `internal/**/*_test.go`.",
			want: nil,
		},
		{
			name: "excludes a brace-set glob",
			body: "Update both `pkg/{a,b}/main.go` files.",
			want: nil,
		},
		{
			name: "excludes a character-class glob",
			body: "Applies to `file[0-9].go` variants.",
			want: nil,
		},
		{
			name: "glob and real path together: only the real path is extracted",
			body: "Fix `cmd/pilot/*.go` callers of `internal/executor/runner.go`.",
			want: []string{"internal/executor/runner.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractReferencedPaths(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractReferencedPaths() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExtractDependencyRefs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []int
	}{
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "no explicit ref marker in prose alone (GH-5021 real body)",
			body: gh5021Body,
			want: nil,
		},
		{
			name: "no explicit ref marker in prose alone (ui#139 real body, has '->' lineage but no marker)",
			body: ui139Body,
			want: nil,
		},
		{
			name: "single explicit Depends on ref",
			body: "This sub-issue depends on prior work.\n\nDepends on: #123",
			want: []int{123},
		},
		{
			name: "Blocked by phrasing, case-insensitive",
			body: "blocked BY: #456",
			want: []int{456},
		},
		{
			name: "multiple distinct refs, first-seen order",
			body: "Depends on: #10\nSome text.\nBlocked by: #20\nDepends on:#10",
			want: []int{10, 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDependencyRefs(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractDependencyRefs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
