---
name: release pipeline is tag-only — goreleaser is the sole publisher
description: Never run a local `gh release create` / asset upload for a release. Just push the version tag and let .github/workflows/release.yml (goreleaser) publish binaries + GitHub release + Homebrew tap. A local publish 422-collides with goreleaser and silently skips the brew formula.
type: decision
---
Decided 2026-06-01 (#3377) after the v2.166.6 cut half-broke the release. The same race had already bitten v2.149.4 (tracked as a P2 backlog item for ~1 week).

**The race:** the old `make release` target did `make package` + `gh release create` (uploading the 4 darwin/linux tarballs + `checksums.txt`) *locally*. Pushing the version tag **also** triggers `.github/workflows/release.yml` (goreleaser), which rebuilds everything and uploads to the **same** GitHub release → `422 Validation Failed [ReleaseAsset already_exists]` on every asset goreleaser re-uploads → goreleaser exits 1. The failure lands in goreleaser's `scm releases` publish phase, so the steps *after* it — notably the **Homebrew formula publish** to `qf-studio/homebrew-pilot` — never run. Net: the GitHub release looks complete (assets are present, from the local upload), but `brew upgrade pilot` stays on the old version. For v2.166.6 the tap had to be bumped to 2.166.6 by hand (verified the published asset sha256s, then PUT the formula).

**Decision:** goreleaser (CI) is the **single source of truth** for publishing. `make release V=X.Y.Z` is now **tag-only** — it validates (clean tree, on `main`), creates the tag, pushes it, and stops. CI does the rest: `release.yml` (goreleaser → binaries incl. windows + GitHub release + Homebrew tap + Docker) and `release-desktop.yml` (desktop bundles). This is how v2.166.0–166.5 shipped cleanly (cut tag-only by hand).

**How to release now:**
- `make release V=2.166.7` — equivalently `git tag v2.166.7 && git push origin v2.166.7`.
- Watch `gh run list --workflow=Release`; the goreleaser run going green == the brew tap was updated. If it ever fails again, check the tap formula version before assuming the release is fine.

**Do NOT:** run a local `gh release create` for a tag, re-add `make package` to the `release` target, or publish any asset before goreleaser. If goreleaser ever legitimately needs to overwrite, set its release mode (`replace`) instead of pre-creating the release.

Rejected alternatives: (a) delete `release.yml`, keep local `make release` — loses the windows build + homebrew + docker that goreleaser uniquely does; (b) make both idempotent — more moving parts. Tag-only won: least surface, matches the documented Release Workflow in `.agent/DEVELOPMENT-README.md`.

Related: [[bug_smoke_test_wrong_cli_contract]] (also a release/upgrade-path gotcha).
