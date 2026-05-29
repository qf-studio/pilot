# SOP: GitHub token architecture for Pilot (multi-project)

**Category:** config
**Created:** 2026-05-29
**Applies to:** local Pilot runs against multiple GitHub repos + the pilot-repo CI

## Problem this prevents

Pilot uses **one global GitHub token** for *all* configured projects, and the
`--project` flag does **not** scope the pollers. A token scoped to a single repo
(e.g. a fine-grained PAT) therefore breaks every other project's poller with
401/403. This was learned the hard way wiring up `qf-studio/studio-sdk`.

### Evidence in code
- `internal/config/config.go:200` — `ProjectGitHubConfig` has only `Owner`/`Repo`,
  **no per-project token field**. One global `adapters.github.token` serves all.
- `cmd/pilot/main.go:2235` — poller loop is `for _, proj := range cfg.Projects`
  (all projects). `--project` only sets the executor cwd (`main.go:215`), it does
  **not** filter this loop.
- Token resolution falls back to `os.Getenv("GITHUB_TOKEN")` in many sites
  (`main.go:1403/1660/2046`, `commands.go:991/2473`).

## The rule

The global `adapters.github.token` must be able to access **every** repo across
**every** configured project — which often spans multiple owners (personal +
org). Only a **classic PAT** can span owners; a fine-grained PAT is locked to a
single resource owner.

## Canonical setup (3 tokens, clean separation)

| Token | Type | Home | Scope | Serves |
|---|---|---|---|---|
| `pilot-local` | **classic** | `~/.pilot/config.yaml` → `adapters.github.token` | `repo`, `read:org`, `project` | all local Pilot work, all projects, all owners |
| `pilot-docs-pat` | fine-grained | `PILOT_DOCS_PAT` Actions secret (pilot repo) | Contents RW + Pull requests RW on pilot | docs auto-merge **chaining** (`docs-version-sync.yml`) |
| `qf-studio-homebrew-tap` | classic | `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret | tap write | release pipeline (brew tap) |

- `project` scope on `pilot-local` is included now so **board-as-source**
  (TASK-317 / GH-3228 `FindIssuesFromProject`) works without re-issuing.
- For org repos under SSO: after creating the classic token, **Configure SSO →
  Authorize** for the org. Membership being granted is not enough; the *token*
  must be authorized.

## Why PILOT_DOCS_PAT is a PAT, not GITHUB_TOKEN

`docs-version-sync.yml:73` uses `secrets.PILOT_DOCS_PAT || secrets.GITHUB_TOKEN`.
A PAT is required only for the **chained** workflow: merges made with the default
`GITHUB_TOKEN` cannot trigger a follow-on workflow run. Without the PAT, docs sync
still works; you just lose the chain step.

## Verify a token without printing it

```python
# python3 - reads adapters.github.token, prints only HTTP status + scopes
import yaml, os, urllib.request, subprocess
cfg = yaml.safe_load(open(os.path.expanduser("~/.pilot/config.yaml")))
tok = cfg["adapters"]["github"]["token"]
if tok.startswith("${"): tok = os.environ[tok[2:-1]]
for repo in ["qf-studio/pilot","alekspetrov/navigator","qf-studio/studio-sdk"]:
    code = subprocess.run(["curl","-s","-o","/dev/null","-w","%{http_code}",
        "-H",f"Authorization: Bearer {tok}",
        f"https://api.github.com/repos/{repo}"],capture_output=True,text=True).stdout
    print(repo, code)
req = urllib.request.Request("https://api.github.com/user",
    headers={"Authorization":f"Bearer {tok}"})
print("scopes:", urllib.request.urlopen(req).headers.get("x-oauth-scopes"))
```

Expected: every repo `200`, scopes include `repo, read:org, project`.

## Gotchas

- A PAT whose one-time value you didn't copy is **unrecoverable** — revoke and
  recreate; it can't be read back out of an Actions secret either.
- Fine-grained PAT permissions reset the form when you change the resource owner.
- Set Actions secrets via `gh secret set NAME -R owner/repo` (interactive paste,
  no shell-history exposure) rather than the web UI.
- Revoke order: create + verify replacements **first**, revoke stale tokens
  **last**, to avoid locking out a running Pilot.
