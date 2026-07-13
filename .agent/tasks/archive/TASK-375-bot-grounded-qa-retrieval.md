# TASK-375: Bot Module Phase 3 — grounded Q&A retrieval

**Status**: ✅ SHIPPED 2026-06-26 — #3671 closed `pilot-done` (PR #3684)
**Created**: 2026-06-26
**Assignee**: Pilot (queued)
**Parent plan**: `/Users/aleks.petrov/.claude/plans/there-is-a-problem-inherited-fiddle.md`
**Depends on**: TASK-374 (Responder).

---

## Context

`handleQuestion` (`internal/comms/handler.go:326`) uses the executor (90s) even for
shallow code questions. Add a bounded retrieval layer so code questions answer in
~2–4s, with executor fallback when the question is too broad. This is the
most-tunable piece — ship it behind `bot.retrieval.enabled`.

---

## Acceptance Criteria

- [ ] `internal/comms/retrieval.go`: bounded walk of the active project (`filepath.WalkDir`), keyword/glob match, pick top `max_files`, read up to `max_bytes`, assemble a context block. Reuse an existing repo-search helper if one exists under `internal/executor`/`internal/memory`; else stdlib walk + `strings.Contains`. Returns `(contextBlock string, tooBroad bool)`.
- [ ] `Responder.Answer(ctx, history, question)`: run retrieval → if `tooBroad`, signal fallback; else `llm.Answer` with `answerModel` (Sonnet) + the context block.
- [ ] `handleQuestion`: if responder present → `Answer`; on `tooBroad`/error → existing executor path. Cite real file paths in the answer.
- [ ] Config `bot.retrieval`: `enabled`, `max_files` (default 8), `max_bytes` (default 24000).

---

## Out of Scope
- Embeddings / vector search (simple lexical retrieval only for v1).

## Verify
```bash
go test ./internal/comms/...
go build ./... && make lint
```
Live: "how does intent classification work?" → grounded answer citing real files in
a few seconds; "explain the whole repo" → falls back to executor.

## Done
- [ ] Simple code questions answered via retrieval+LLM; broad ones fall back.
- [ ] Flag-gated; tests + lint clean.

## Refs
- Parent plan; depends on TASK-374.

**Last Updated**: 2026-06-26
