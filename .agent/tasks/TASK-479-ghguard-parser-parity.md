# TASK-479: gh-guard parser parity — the guard must derive what `gh` derives

**Status**: 📋 Planned — NOT dispatched. Research complete 2026-08-17 (empirical probe of `parseArgs`/`Classify` on main via build overlay; repo untouched).
**Created**: 2026-08-17
**Origin**: review of external contributor PR #4905 (lkshrk). The PR's `-X GET` relaxation is sound in principle but rests on a parser that does not match `gh`'s. Research then found **11 pre-existing parity gaps on main today**, independent of that PR.

## Problem

`internal/executor/ghguard/` inspects `gh` command lines our autonomous executor is about to run and classifies each as read (allow) or mutation (deny). Correctness depends entirely on our `parseArgs` deriving the **same effective HTTP method and body-presence** that the real `gh` binary derives from the same argv. Where the two disagree, the guard is simply wrong.

`gh` uses pflag. Our parser models only space-separated flag values, one global flag table, and no bundling. pflag additionally accepts attached shorthand (`-XPOST` ≡ `-X POST`), `=` form (`-X=POST`), boolean bundles (`-piX POST`), last-occurrence-wins for scalars, and `--` termination — and `gh`'s flag grammar is **per-command** (`-f`, `-F`, `-p`, `-t`, `-s` mean different things under `api` vs `pr create` vs `issue comment`).

## Confirmed parity gaps (probed against main)

| # | argv | our parse | gh derives | today | correct |
|---|---|---|---|---|---|
| G1 | `api X -XPOST` | method="" | POST | allow | deny |
| G2 | `api X -XDELETE` | method="" | DELETE | allow | deny |
| G3 | `api X -fstate=closed` | hasData=false | POST+body | allow | deny |
| G4 | `api graphql -Fquery=x` | hasData=false | POST | allow | deny |
| G5 | `api X -pX DELETE` | method="" | DELETE | allow | deny |
| G6 | `api X -pXDELETE` | method="" | DELETE | allow | deny |
| G7 | `api X -p -X POST` | `-p` swallows `-X` | POST | allow | deny |
| G8 | `api X -X GET -XPOST` | GET | POST (last wins) | allow | deny |
| G9 | `issue view 1 -Rother/repo` | repoGiven=false | other repo | allow | deny |
| G10 | `issue comment N -Rother/repo --body x` | repoGiven=false | comments other repo | allow | deny |
| G11 | `pr create -f --head other-branch` | `-f` swallows `--head` | head=other-branch | allow | deny |

G12–G17 are already-correct or benign-false-deny shapes; keep them as regression rows (`-X=POST`, `--method=PATCH`, `--input` variants, `--` terminator, interspersed flags, `--cache`/`--preview`).

Root causes: (a) no attached-shorthand splitting (`policy.go:251-258` + the ignore-unknown-as-boolean default at `295-299`); (b) one global `valueFlags` table (`policy.go:185-213`) where gh's grammar is per-command; (c) no boolean-bundle handling; (d) no `--` handling.

## Current structure (policy.go, 579 lines)

`allowRules` :134-148 · `hardDenyCommands`/`hardDenyIssueSubs` :153-173 · `valueFlags` :185-213 · `parsedArgs` :218-230 · `parseArgs` :234-302 (sub extraction :241-244, `=` split :254-258, method :267-272, data flags :273-277, ignore-unknown :295-299) · `Classify` :348-386 · `checkAPIRead` :394-403 · `checkOwnArtifact` :407-446.

Enforcement path (sole `Classify` call site): shim `gh` → `pilot gh-guard -- <argv>` → `runGhGuard` (`cmd/pilot/ghguard.go:67-118`, Classify at :74). Deny ⇒ journal JSONL + stderr + exit 1, real `gh` never exec'd (fail-closed). Shim wiring `internal/executor/ghguard_spawn.go:39-97`; denial audit `internal/executor/ghguard_audit.go:26-73`.

## Fix

Replace the global `valueFlags` with per-command flag specs:

```go
type flagSpec struct {
    takesValue bool
    isBody     bool // -f/-F fields — body unless method is explicitly GET
    isInput    bool // --input — body ALWAYS
    // semantic sinks: setMethod, setRepo, setHead, ...
}
// apiFlags (complete), prCreateFlags, issueCommentFlags, prCommentFlags, listFlags…
```

Parse loop replacing `policy.go:245-300`, mirroring pflag's `parseSingleShortArg`:

1. Bare `--` ⇒ rest is positional, stop flag parsing.
2. Long `--name[=v]`: split at first `=`; if takesValue and no `=`, consume next token (missing ⇒ `parseErr`). Scalars last-wins; body/input flags OR-accumulate.
3. Shorthand `-abc…`: walk chars left to right. Value-taking ⇒ value is `chars[1:]` minus a leading `=` if present (`-X=POST`, `-XPOST`), else the next argv token (missing ⇒ `parseErr`); stop this token. Boolean ⇒ record and continue the bundle. Unknown ⇒ `unknownFlag`, stop.
4. Method: store raw, last-wins; keep `ToUpper` normalization in `checkAPIRead`.
5. **Split `hasDataFlag` into `hasFieldFlag` (`-f`/`-F`) and `hasInputFlag` (`--input`).** Required for a sound #4905.
6. Fail closed on `unknownFlag`/`parseErr` for `api` and all `kindOwnArtifact` commands (gh itself rejects unknown flags, so no legitimate call is lost; a new gh flag stays denied until table-added — documented safety bias). `kindRead` commands may stay lenient.

New `checkAPIRead`:

```go
if p.hasInputFlag { deny("--input always sends a request body") }        // regardless of method
method := strings.ToUpper(strings.TrimSpace(p.method))
if p.hasFieldFlag && method != "GET" { deny("… add -X GET or URL-encode …") }
if method != "" && method != "GET" { deny(...) }
allow
```

## Relationship to PR #4905

The relaxation's premise is correct: gh auto-switches to POST only when `--method` was **not** passed; with explicit GET, `-f`/`-F` values go to the query string. Keep it — it fixes the #4877 false-deny. Two amendments: it keys off the merged `hasDataFlag`, so `gh api x -X GET --input p.json` would flip deny→allow (`--input` is always a body); and its full value only lands once method derivation is trustworthy. On the broken parser the relaxation fails *safe* (an unseen `-XGET` just means the data-flag deny fires), so merge order is flexible.

## Test rows

`policy_test.go` conventions: anonymous struct `{name string; args []string; wantVerdict Verdict}` in `TestClassify` (:21-25, :104-119); every deny must carry non-empty `Reason` and `Allowed` (already asserted). Add sections `// --- parser parity (pflag shapes) ---` and `// --- explicit-GET query fields (#4905) ---`, plus derived-value assertions in `TestParseArgs_FlagAliases` style for `method`/`hasFieldFlag`/`repo`/`head` on attached forms.

```go
// attached shorthand values
{"api attached -XPOST", []string{"api","repos/o/r/issues","-XPOST"}, VerdictDeny},
{"api attached -XDELETE", []string{"api","repos/o/r/issues/1","-XDELETE"}, VerdictDeny},
{"api attached -XGET is a read", []string{"api","repos/o/r/issues/1","-XGET"}, VerdictAllow},
{"api eq short -X=POST", []string{"api","x","-X=POST"}, VerdictDeny},              // regression
{"api long eq --method=PATCH", []string{"api","x","--method=PATCH"}, VerdictDeny}, // regression
{"api attached raw-field", []string{"api","repos/o/r/issues/1","-fstate=closed"}, VerdictDeny},
{"api attached typed field", []string{"api","graphql","-Fquery=mutation"}, VerdictDeny},
{"api eq short field", []string{"api","graphql","-F=query=x"}, VerdictDeny},

// bundles + context-sensitive shorthands
{"api bundled bool then value", []string{"api","repos/o/r/x","-pX","DELETE"}, VerdictDeny},
{"api bundled attached", []string{"api","repos/o/r/x","-pXDELETE"}, VerdictDeny},
{"api -p is boolean, does not swallow -X", []string{"api","repos/o/r/x","-p","-X","POST"}, VerdictDeny},
{"api paginate GET stays a read", []string{"api","repos/o/r/x","-p"}, VerdictAllow},
{"api benign value flags", []string{"api","x","--cache","3600s","-H","Accept: a","-q",".items","-t","{{.}}"}, VerdictAllow},
{"api attached header stays a read", []string{"api","x","-HAccept: a"}, VerdictAllow},

// repeated flags — last occurrence wins
{"api repeated -X last POST attached", []string{"api","x","-X","GET","-XPOST"}, VerdictDeny},
{"api repeated -X last GET", []string{"api","x","-X","POST","-X","GET"}, VerdictAllow}, // regression

// attached -R
{"issue view attached -R matching", []string{"issue","view","1","-Rqf-studio/pilot"}, VerdictAllow},
{"issue view attached -R wrong repo", []string{"issue","view","1","-Rqf-studio/upstream"}, VerdictDeny},
{"issue comment attached -R cross-repo", []string{"issue","comment","4671","-Rother/repo","--body","hi"}, VerdictDeny},

// pr create -f is --fill (boolean), not a value flag
{"pr create fill does not swallow --head", []string{"pr","create","-f","--head","other-branch"}, VerdictDeny},
{"pr create fill with own head", []string{"pr","create","-f","--head","pilot/GH-4671"}, VerdictAllow},

// fail-closed on unverifiable api flags
{"api unknown flag fails closed", []string{"api","x","--not-a-real-flag","v"}, VerdictDeny},
{"api dangling -X at end of argv", []string{"api","x","-X"}, VerdictDeny},

// explicit-GET query fields (#4905), on top of the faithful parser
{"api search -f with explicit -X GET", []string{"api","search/issues","-X","GET","-f","q=x in:body"}, VerdictAllow},
{"api search -f with --method get", []string{"api","search/issues","--method","get","-f","q=x"}, VerdictAllow},
{"api search -f with attached -XGET", []string{"api","search/issues","-XGET","-f","q=x"}, VerdictAllow},
{"api field without method still denies", []string{"api","repos/o/r/issues/1","-f","state=closed"}, VerdictDeny}, // regression
{"api field with -X GET then -X POST", []string{"api","x","-X","GET","-f","a=b","-X","POST"}, VerdictDeny},

// --input is a body ALWAYS — immune to the relaxation
{"api --input with explicit GET", []string{"api","x","-X","GET","--input","p.json"}, VerdictDeny},
{"api --input=file", []string{"api","x","--input=p.json"}, VerdictDeny},  // regression
{"api --input from stdin", []string{"api","x","--input","-"}, VerdictDeny},
```

## Open decisions

- **D1** Unknown-flag policy for `api`/own-artifact — recommend fail-closed (deny). Cost: new gh flags need a table line.
- **D2** Deny `api graphql` with field flags regardless of method (belt-and-braces; today GitHub doesn't execute GraphQL over GET, but that's their behavior, not our contract). Recommend adding.
- **D3** Flags-before-subcommand (G15) is a false-deny only; fixing needs known-value-flag consumption while scanning for the sub. Low priority.
- **D4** Verify gh's GET comparison is `EqualFold` against vendored source when implementing (our `ToUpper` assumes it).
- **D5** **Separate issue**: the guard never inspects `GH_REPO`/`GH_HOST` env — `GH_REPO=other/repo gh issue comment N --body x` bypasses the repo check exactly like G10, and no argv fix covers it. `runGhGuard` sees the env and could check/scrub it.

## Refs

- External PR: #4905 (amend or follow immediately to exclude `--input`)
- Related issue: #4877 (the false-deny that motivated #4905)
- Prior art: GH-4649 (the mutation class the guard exists to stop)
