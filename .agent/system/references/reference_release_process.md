---
name: Release Process
description: Pilot release commands and gotchas (auto-release race, asset naming, GoReleaser CI)
type: reference
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
**ALWAYS run `gh release list --limit 5` before releasing** — Pilot's auto-release may have created newer versions while you weren't looking. Pick `version = max(existing) + 1`, not based on local tags or assumptions.

**Asset naming is case-insensitive:** GitHub release asset names collide on case (`pilot-linux` and `Pilot-Linux` are the same asset). Don't mix.

**GoReleaser CI:** Pilot creates the tag, GoReleaser creates the release. Conflict was fixed in v0.24.1. GoReleaser also runs on tag push — will fail if assets exist (non-blocking).

**Manual release (when autopilot can't):**
```
make package VERSION=vX.Y.Z         # builds + packages with COPYFILE_DISABLE
gh release create vX.Y.Z bin/*.tar.gz bin/checksums.txt   # uploads
```

**Desktop app:** Wails v2 + React, cross-platform (macOS/Windows/Linux). CI workflow `.github/workflows/release-desktop.yml` matrix strategy, artifacts prefixed `Pilot-Desktop-*`.

**Discord (community):** discord.gg/K6mM8TzJ

**Why kept:** Operational checklist — used during every release. Discord URL doesn't belong in docs.

**How to apply:** When user asks for a release, run the version-check command first. When troubleshooting GoReleaser, cross-ref `bug_goreleaser_cancellation.md`.
