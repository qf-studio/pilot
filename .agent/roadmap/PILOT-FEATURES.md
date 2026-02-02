# Pilot Feature Roadmap

> Brainstormed 2026-02-01 — Ideas for Pilot evolution beyond v0.10

## Execution Enhancements

### Fleet — Multi-repo Orchestration
- One ticket triggers coordinated changes across multiple repos
- Dependency-aware PR sequencing
- Example: "Update auth library" → PRs in api, frontend, mobile, docs, infra
- Handles cross-repo dependencies and merge ordering

### Autopilot Pro — Self-healing Systems
- Monitor → Detect anomaly → Create fix ticket → Execute → Deploy
- Closes the loop: human gets "Fixed memory leak, here's the PR"
- Integrates with alerting (PagerDuty, Datadog, Sentry)
- Auto-rollback on failed deploys

### Architect — Continuous Codebase Evolution
- Watches patterns, suggests refactors proactively
- "Your auth code is scattered across 12 files, consolidate?"
- Tech debt reduction on autopilot
- Weekly "codebase health" reports with actionable PRs

### Bridge — Product → Engineering Translation
- Figma designs + PRD → Pilot-ready tickets with specs
- Product manager describes feature → implementation tickets appear
- Requires: Figma MCP, doc parsing, spec generation
- Reduces product → engineering handoff friction

## Platform Evolution

### Pilot SDK / API
- Others build on top of Pilot
- Custom workflows, vertical solutions
- "Pilot for DevOps", "Pilot for Data", "Pilot for Security"
- Plugin system for custom adapters

### Cross-org Learning (Future)
- Anonymized patterns from all Pilot users
- "Teams with similar stack solved this with X"
- Opt-in collective intelligence
- Privacy-preserving pattern sharing

## Notes

All features 1-4 are additive to Pilot core, not separate products.

Team intelligence (original "Hive" idea) may not be needed — team members research with Claude, create smart issues, Pilot executes. Intelligence is distributed.

---

*Source: Strategy session, Navigator + Pilot pipeline discussion*
