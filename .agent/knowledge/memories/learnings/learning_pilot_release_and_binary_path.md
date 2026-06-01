---
name: Releasing Pilot — fix/* branches don't auto-release; manual = push a v* tag; the daemon binary is ~/.local/bin/pilot
description: The autopilot auto-cuts releases only on pilot/GH-* merges, so a manually-merged fix/* PR sits on main unreleased. Manual release = push a v* tag (triggers release.yml GoReleaser → Homebrew). The running daemon is ~/.local/bin/pilot (not make install's ~/go/bin, not brew) — update with `pilot upgrade`, not a manual cp.
type: learning
metadata:
  type: learning
---

How a fix actually reaches the running Pilot daemon (learned cutting v2.166.4 for #3363, 2026-06-01):

**Releases.** A release is a pushed `v*` tag → `.github/workflows/release.yml` runs GoReleaser
(`release --clean`) → builds binaries, publishes a GitHub Release, and updates the Homebrew formula at
`qf-studio/homebrew-pilot` (`HOMEBREW_TAP_GITHUB_TOKEN`). The tag also triggers `docs-version-sync.yml`
(bumps `docs/lib/version.ts`) and `release-desktop.yml` + `Docker`.

- The **autopilot auto-cuts releases ONLY on `pilot/GH-*` merges** (`releaser.go` SemVer bump → tag push).
  A **manually-merged `fix/*` PR sits on `main` unreleased** — it rides out with the *next* pilot release,
  or you cut one manually.
- **Manual release:** `git tag vX.Y.Z <sha> && git push origin vX.Y.Z` (tag the exact SHA — no root
  checkout/clean-tree needed; `release.yml` does the build in CI). `make release V=X.Y.Z` is now
  equivalent and safe — **as of #3377 (after v2.166.6) it is tag-only** and no longer does a local
  `gh release create`. (Previously it did, which 422-collided with the CI GoReleaser and made goreleaser
  skip the Homebrew formula publish — the v2.166.6 tap had to be bumped by hand.) Patch bump for a `fix:`.
  See [[decision_release_pipeline_tag_only]].
- `make build` → `./bin/pilot`; `make install` → `go install` → `~/go/bin` (GOBIN unset).

**The daemon binary.** It runs from **`~/.local/bin/pilot`** (a plain 20MB file, not a brew symlink, not
`~/go/bin`). So neither `make install` nor `brew upgrade` updates it. Update with **`pilot upgrade`**
(self-update, `upgrader.go`) then restart: `pilot start --github --env stage --dashboard --replace`
(the `--replace` flag kills the prior instance — verified only 1 daemon after).

**Don't trust the version string** to tell you what's running — it's set from the last tag, so a binary
built from a newer untagged commit still reports the old version. Confirm the actual commit with
`go version -m ~/.local/bin/pilot | grep vcs.revision`. (This is how we caught a "restarted" daemon
still running the pre-fix commit `3277dc80` while reporting `2.166.3`.) Related:
[[learning_selfheal_projectpath_discriminator]].
