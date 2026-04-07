# TASK-13: Enrich GitHub Release Notes with LLM Summary

**Status**: 🚧 In Progress
**Created**: 2026-03-31

---

## Context

**Problem**:
GoReleaser generates basic changelogs from commit messages (grouped by type), but they're mechanical — just commit subjects. Users installing Pilot see raw `feat(executor): ...` lines with no context on what actually changed or why it matters.

**Goal**:
After GoReleaser publishes a release, autopilot enriches the release body with an LLM-generated human-friendly summary — what shipped, why it matters, breaking changes, upgrade notes.

**Success Criteria**:
- [ ] GitHub releases contain both GoReleaser changelog AND an LLM summary section
- [ ] Summary is concise (3-5 bullet points max)
- [ ] Breaking changes are called out prominently
- [ ] Works with existing `mode: append` in GoReleaser config
- [ ] Configurable (can be disabled via `release.generate_summary: false`)

---

## Implementation Plan

### Phase 1: GitHub Client — UpdateRelease method

**Goal**: Add ability to update an existing GitHub release body

**Tasks**:
- [ ] Add `UpdateRelease(ctx, owner, repo, releaseID, body)` to GitHub client
- [ ] Add `GetReleaseByTag(ctx, owner, repo, tag)` to fetch release after GoReleaser creates it

**Files**:
- `internal/adapters/github/client.go` — new methods
- `internal/adapters/github/types.go` — `ReleaseUpdateInput` struct if needed

### Phase 2: Release Summary Generator

**Goal**: LLM-based summary generation from commit data

**Tasks**:
- [ ] Create `internal/autopilot/release_summary.go`
- [ ] Use Claude Haiku (fast, cheap) to generate summary from:
  - Commit messages between tags
  - PR titles and bodies (if available)
  - Files changed (for scope detection)
- [ ] Output format: markdown section with `## What's New` header
- [ ] Include: feature highlights, bug fixes, breaking changes, upgrade notes
- [ ] Timeout: 15s max, fail gracefully (release still ships without summary)

**Prompt structure**:
```
Given these commits for release {version}:
{commit list}

Generate a concise release summary:
- 3-5 bullet points of what shipped
- Call out breaking changes if any
- Keep it under 200 words
```

### Phase 3: Wire into Autopilot Controller

**Goal**: Call summary generator after tag creation, update release

**Tasks**:
- [ ] In `handleReleasing()` (controller.go ~line 1327): after `CreateTag()` succeeds
- [ ] Poll for GoReleaser to create the release (tag exists → release appears within ~3min)
- [ ] Fetch release by tag, prepend LLM summary to existing body, update release
- [ ] Add `GenerateSummary bool` to `ReleaseConfig` (types.go)
- [ ] Wire config through to controller

**Flow**:
```
CreateTag() → wait for GoReleaser (poll GetReleaseByTag every 30s, max 5min)
           → GetRelease body
           → Generate LLM summary
           → Prepend summary to body
           → UpdateRelease()
```

### Phase 4: Config and Tests

**Tasks**:
- [ ] Add `generate_summary` to `ReleaseConfig` in types.go
- [ ] Add to example config
- [ ] Table-driven tests for summary generator
- [ ] Integration test for update release flow
- [ ] Test graceful fallback (LLM timeout, release not found)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| When to enrich | During tag creation / After GoReleaser / Webhook | After GoReleaser (poll) | GoReleaser owns release creation; we append. No webhook needed. |
| LLM model | Haiku / Sonnet | Haiku | Fast, cheap, summary is simple task |
| Failure mode | Block release / Skip summary | Skip summary | Release must ship regardless |
| Summary placement | Prepend / Append / Replace | Prepend | GoReleaser changelog stays as reference, summary is the TL;DR |

---

## Key Files

| File | Purpose |
|------|---------|
| `internal/autopilot/controller.go:1266` | `handleReleasing()` — wire point |
| `internal/autopilot/releaser.go` | Tag creation, version detection |
| `internal/autopilot/types.go:323` | `ReleaseConfig` — add `GenerateSummary` |
| `internal/adapters/github/client.go:504` | `CreateRelease()` — reference for new methods |
| `.goreleaser.yaml` | `mode: append` — compatible with our updates |

---

## Verify

```bash
# Unit tests
go test ./internal/autopilot/... -run TestReleaseSummary -v

# Integration (manual): merge a PR, verify release notes contain summary
gh release view v2.87.0 --repo qf-studio/pilot --json body
```

---

## Done

- [ ] `UpdateRelease()` and `GetReleaseByTag()` in GitHub client
- [ ] `release_summary.go` generates LLM summary from commits
- [ ] `handleReleasing()` enriches release after GoReleaser publishes
- [ ] `generate_summary` config option works (enabled by default)
- [ ] Graceful fallback — release ships even if summary fails
- [ ] Tests pass

---

**Last Updated**: 2026-03-31
