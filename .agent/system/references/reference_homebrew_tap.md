---
name: Homebrew Tap Setup
description: Brew tap at qf-studio/homebrew-pilot, GoReleaser auto-publishes formula, classic PAT in HOMEBREW_TAP_GITHUB_TOKEN secret
type: reference
---

Homebrew tap lives at `qf-studio/homebrew-pilot`.

- `brew tap qf-studio/pilot && brew install pilot` — user-facing install command
- GoReleaser `brews:` section in `.goreleaser.yaml` auto-publishes formula on every release tag
- `HOMEBREW_TAP_GITHUB_TOKEN` secret in `qf-studio/pilot` repo — classic PAT with `repo` scope
- Old tap `alekspetrov/homebrew-pilot` still exists but is stale — no longer used
- `brews` is deprecated in GoReleaser v2 — migrate to `homebrew_casks` eventually
- Curl install (`install.sh`) supports `pilot upgrade` hot-upgrade; Homebrew does not
