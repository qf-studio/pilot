# TASK-11: Cross-Project Memory

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Pilot learns patterns within a single project but cannot apply learnings across projects. When Pilot fixes an auth bug in Project A, it doesn't know to avoid the same pattern in Project B. This limits Pilot's ability to improve over time across an organization.

**Goal**:
Implement cross-project memory that captures patterns, anti-patterns, and learnings, making them available across all projects while respecting project boundaries.

**Success Criteria**:
- [ ] Patterns captured from successful task completions
- [ ] Anti-patterns captured from failures
- [ ] Patterns queryable across projects
- [ ] Privacy boundaries respected (org-level vs global)

---

## Research

### Pattern Types

| Type | Example | Capture Trigger |
|------|---------|-----------------|
| Code Pattern | "Use context.Context in Go handlers" | Repeated implementation |
| Anti-Pattern | "Don't use string concatenation for SQL" | Task failure |
| Library Preference | "Team prefers Zod over Joi" | Multiple selections |
| Architecture | "Services use event-driven pattern" | System doc analysis |
| Test Pattern | "Integration tests mock external APIs" | Test success/failure |

### Storage Options

| Option | Pros | Cons |
|--------|------|------|
| SQLite per org | Simple, local | No global patterns |
| PostgreSQL | Scalable, relational | Infrastructure |
| Vector DB (Pinecone) | Semantic search | Cost, complexity |
| Knowledge Graph | Rich relationships | Complex queries |

### Pattern Schema

```json
{
  "id": "pat_123",
  "type": "code_pattern",
  "pattern": "Use structured logging with slog",
  "context": "Go services",
  "confidence": 0.85,
  "occurrences": 12,
  "projects": ["api", "worker", "gateway"],
  "examples": [
    {"file": "api/handler.go", "snippet": "slog.Info(...)"}
  ],
  "anti_patterns": ["pat_456"],
  "created_at": "2026-01-15",
  "updated_at": "2026-01-26"
}
```

---

## Implementation Plan

### Phase 1: Pattern Extraction
**Goal**: Extract patterns from task completions

**Tasks**:
- [ ] Create pattern extractor service
- [ ] Analyze completed task diffs for patterns
- [ ] Use LLM to identify meaningful patterns
- [ ] Filter noise (trivial changes, formatting)
- [ ] Calculate pattern confidence scores

**Files**:
- `internal/memory/extractor.go` - Pattern extraction
- `internal/memory/patterns.go` - Pattern types and storage
- `orchestrator/pattern_analyzer.py` - LLM analysis

### Phase 2: Pattern Storage
**Goal**: Persist patterns with relationships

**Tasks**:
- [ ] Extend SQLite schema for patterns
- [ ] Add pattern CRUD operations
- [ ] Implement pattern merging (dedup similar)
- [ ] Track pattern occurrences and confidence
- [ ] Add project→pattern relationships

**Files**:
- `internal/memory/store.go` - Extend schema
- `internal/memory/migrations/` - Schema migrations
- `internal/memory/pattern_store.go` - Pattern operations

### Phase 3: Pattern Query
**Goal**: Query relevant patterns during task execution

**Tasks**:
- [ ] Semantic search for relevant patterns
- [ ] Filter by project scope (project, org, global)
- [ ] Rank patterns by confidence and recency
- [ ] Format patterns for prompt injection
- [ ] Cache frequent queries

**Files**:
- `internal/memory/query.go` - Pattern queries
- `internal/executor/context.go` - Inject patterns into prompt

### Phase 4: Cross-Project Sync
**Goal**: Share patterns across projects

**Tasks**:
- [ ] Define pattern visibility scopes
- [ ] Implement org-level pattern aggregation
- [ ] Handle pattern conflicts across projects
- [ ] Add pattern versioning
- [ ] Support pattern export/import

**Files**:
- `internal/memory/sync.go` - Cross-project sync
- `internal/memory/export.go` - Import/export

### Phase 5: Learning Loop
**Goal**: Improve patterns based on outcomes

**Tasks**:
- [ ] Track pattern usage in tasks
- [ ] Measure task success when pattern applied
- [ ] Increase/decrease confidence based on outcome
- [ ] Deprecate low-confidence patterns
- [ ] Surface high-value patterns

**Files**:
- `internal/memory/feedback.go` - Learning loop

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Storage | SQLite, Postgres, Vector DB | SQLite + embeddings | Local first, add vector search later |
| Pattern extraction | Rule-based, LLM | LLM | More nuanced, handles context |
| Scope model | Flat, Hierarchical | Hierarchical | Project→Org→Global |
| Sync mechanism | Real-time, Batch | Batch | Simpler, less overhead |

---

## Configuration

```yaml
memory:
  cross_project:
    enabled: true
    extraction:
      min_occurrences: 3
      confidence_threshold: 0.7
    scopes:
      - project   # Patterns from this project only
      - org       # Patterns from all org projects
      # - global  # (Future) Community patterns

  learning:
    enabled: true
    feedback_weight: 0.1
    decay_rate: 0.01  # Monthly decay for unused patterns
```

---

## Privacy Considerations

| Scope | Visibility | Use Case |
|-------|------------|----------|
| Project | Same project only | Sensitive projects |
| Org | All org projects | Default |
| Global | All Pilot users | Open source |

- Code snippets anonymized before org/global sharing
- Patterns describe what, not show code
- Opt-out per project

---

## Dependencies

**Requires**:
- [ ] Memory store (existing)
- [ ] Orchestrator (for LLM extraction)
- [ ] Task completion tracking

**Related Tasks**:
- Builds on memory system
- Enhances executor prompt building

---

## Verify

```bash
# Run tests
make test

# View extracted patterns
pilot patterns list

# Search patterns
pilot patterns search "authentication"

# Check pattern stats
pilot patterns stats
```

---

## Done

Observable outcomes that prove completion:

- [ ] Patterns extracted from completed tasks
- [ ] Patterns stored with confidence scores
- [ ] Patterns queryable by project/org scope
- [ ] Relevant patterns injected into task prompts
- [ ] Pattern confidence updates based on outcomes
- [ ] Tests pass

---

## References

- [Knowledge Graphs for AI](https://www.ibm.com/topics/knowledge-graph)
- [Semantic Search with Embeddings](https://www.pinecone.io/learn/semantic-search/)
- [Learning from Experience in AI](https://arxiv.org/abs/2308.10144)

---

**Last Updated**: 2026-01-26
