---
name: Duplicate type-block merge pattern
description: Epic sub-issues both add new types to the same Go file, each merges cleanly at different line ranges, merged file has redeclarations
type: project
---

When Pilot decomposes an epic into sub-issues that each expand the same existing Go file with new types, each sub-PR can merge cleanly (different line ranges) but the merged file has redeclarations. `go vet` reports "redeclared in this block". Git merge sees no conflict because the additions are at different line ranges.

**Example (2026-04-07, auth-service#28 multi-tenancy):**
- 14 sub-issues decomposed from the multi-tenancy epic
- Two sub-issues (GH-361 and an earlier pass) both added `AdminTenant`, `AdminTenantList`, `CreateTenantRequest`, `UpdateTenantRequest`, `AdminTenantService` to `internal/api/admin_services.go`
- First copy at lines 245-330, second at 531-573 — both merged clean
- `AdminTenantService.ListTenants` ended up with two different signatures (3-arg vs 4-arg)
- Handler written against 4-arg version, concrete service implements 3-arg
- Result: `go build ./...` fails with 6 errors, main branch broken, last green tag was v0.66.0
- Cost: P0 fix issue qf-studio/auth-service#367 + downstream fixes #369, #370, #371 blocked until compile-broken state resolved

**Why:**
- `isSinglePackageScope` detection (v1.0.11, GH-1265) catches directory overlap, not single-file overlap
- Planning prompt tells sub-issue B "add types to admin_services.go" without knowing sub-issue A already did
- LLM planner has no awareness of in-flight sibling sub-issue edits
- Per-PR `go vet`/`go build` passes because each sub-issue's branch only has its own additions; breakage only materializes after merge

**How to apply:**

1. **When debugging "redeclared in this block" after a Pilot epic run** — first check if multiple sub-issues of the same epic targeted the same *file*, not just the same package. Grep for the redeclared type name in `git log --oneline` for the epic's sub-issues.

2. **When planning epic decomposition manually** — structure sub-issues so types land first in a single "foundation types" sub-issue, then consumer sub-issues in parallel reference those types. Avoid having multiple parallel sub-issues each add new top-level declarations to the same existing file.

3. **Quick triage check** — after an epic completes but before tagging a release:
   ```bash
   go vet ./... 2>&1 | grep -c "redeclared in this block"
   ```
   Non-zero = duplicate type block merge. Look at recent commits touching the affected file.

4. **Not yet filed as a Pilot issue** — the proper fix would be file-level overlap detection in the planner + post-merge `go vet` gate at the epic level (not just per-PR). Consider filing if the pattern recurs.

**Related memory:**
- `resolved-bugs.md` (older) — serial conflict cascade (TASK-01, v1.0.11) fixed directory-level overlap
- This is the file-level successor pattern to that bug
