---
name: sandbox-gofmt-corrupts-double-single-quotes
description: The gofmt binary in the Pilot-executor sandbox silently rewrites two adjacent straight single quotes ('') inside a Go comment into one smart quote (”) — almost certainly the root cause of the GH-5257/PR#5258 SQL-guard comment corruption, and a landmine for any comment quoting an empty-string SQL literal
type: pitfall
---

# Sandbox `gofmt` corrupts `''` → `”` inside comments — never run `go fmt`/`make fmt` near a doc comment quoting `''`

**What happened (GH-5261, 2026-08-30):** PR#5258's review flagged that
`SetApprovalDecision`'s doc comment in `internal/memory/store.go` had its
`` `AND approval_decision = ''` `` guard-clause quote mangled into
`` `AND approval_decision = ”` `` (U+201D RIGHT DOUBLE QUOTATION MARK). This
looked like a manual typo or an editor autocorrect mistake. It isn't.

**Root cause, reproduced directly:** the `gofmt` binary on this sandbox
(`/usr/bin/gofmt` → `/etc/alternatives/gofmt`, go1.25.8 toolchain) rewrites
two adjacent ASCII single quotes (`0x27 0x27`) inside a `//` comment into a
single `U+201D` character when formatting. Minimal repro:

```go
package main

// `AND approval_decision = ''` test comment
func main() {}
```

Running `gofmt` on this file changes the comment to
`` // `AND approval_decision = ”` test comment `` — verified via `xxd` on
both the original and `gofmt`-piped output (bytes `27 27` → `e2 80 9d`).
`go build`/`go test`/`go vet` do not invoke formatting and are unaffected;
only `gofmt -w`, `go fmt ./...`, `goimports -w`, or `make fmt` touch this.

## Fix (this issue)
Manually restored the doc comment to `` `AND approval_decision = ''` `` in
`internal/memory/store.go` (`SetApprovalDecision`) via a plain string edit,
not via any formatting tool.

## Recommended Approach
- **Never run `go fmt`, `gofmt -w`, `goimports -w`, or `make fmt`** on a file
  containing a comment that quotes an empty-string SQL literal (`''`) or any
  other doubled-straight-quote sequence, in this sandbox — it will silently
  re-corrupt the comment on the next pass. Verify with `gofmt -d <file> |
  xxd` before trusting a "no diff" or applying a formatting pass near such
  comments.
- If a PR review ever again reports a smart-quote / curly-quote corruption in
  a comment (`'` → `’`, `''`/`"` → `”`/`"`), suspect this tooling bug first,
  not a manual edit — grep the file history for who/what ran `make fmt`
  around the corruption's introduction.
- Longer term this is an environment defect worth reporting/patching
  upstream in this sandbox's toolchain image (gofmt should never alter
  comment/string literal content). Until fixed, treat `''`-in-comments as a
  known trap in this repo's Go files.

## Related
- GH-5257 / PR#5258 (receipts digest — where the corruption first appeared)
- GH-5261 (this revision issue — root cause identified while restoring the
  comment)
- `internal/memory/store.go` (`SetApprovalDecision`)

---
**Captured**: 2026-08-30
**Confidence**: 0.95
**Concepts**: tooling, gofmt, sandbox, go-toolchain, code-review, data-corruption
