# Pilot: Audience & Personas

## Target Market

AI-assisted development tools. Competing against ClawdBot/Melbot, Cursor, Copilot Workspace, and DIY Claude Code setups.

## Market Concerns (Competitive Intel)

| Concern | What users say | Pilot's answer |
|---------|----------------|----------------|
| **Security** | "Who knows what it's doing" | PRs only, human review required, self-hosted |
| **Installation** | "Took 2 hours to configure" | Setup Wizard (TASK-30) |
| **Token burn** | "Blew through $50 in a day" | Navigator = 12k tokens vs 50k+ |
| **Babysitting** | "Still prompting all day" | Ticket → PR. No prompting loop. |

---

## Personas

### 1. Solo Dev (Pays Fastest)

**Profile:**
- Indie hacker or freelancer
- Working on 1-3 projects
- Time-constrained, wears all hats
- Self-serve, credit card purchase

**Pain:**
- Backlog of "boring" tickets they never get to
- Context switching between features and maintenance
- No one to delegate to

**Value prop:** "Your AI junior dev that handles CRUD while you build features"

**Adoption path:** Free trial → self-serve $29/mo

---

### 2. Team Lead (Wedge Persona - Primary Target)

**Profile:**
- Manages 3-8 devs
- Budget discretion under ~$500/mo
- Cares about code quality and team velocity
- Can champion tool to company

**Pain:**
- Drowning in PR reviews
- Junior devs blocked waiting for guidance
- 40+ ticket backlog of "should do" work
- Consistency across team is hard

**Value prop:** "Turn your ticket backlog into PRs while you focus on architecture"

**Objections to pre-handle:**

| Objection | Response |
|-----------|----------|
| "What if it writes bad code?" | You review the PR. Same as any team member. |
| "Is it secure?" | Self-hosted. Secrets never leave your infra. |
| "Will my team use it?" | No new UI. Label a ticket, get a PR. |
| "How do I justify cost?" | Usage dashboard shows tickets closed, hours saved. |

**Adoption path:**
1. Team lead tries on 5 tickets personally
2. Proves value ("47 tickets → 47 PRs, merged 89%")
3. Rolls out to team
4. Becomes internal champion
5. Company-wide adoption

**What convinces them:**
- Quick proof of value (5 tickets, 1 week)
- Safety story for their manager
- No training required for team
- Measurable ROI

---

### 3. Enterprise (Pays Longest)

**Profile:**
- Engineering org 50+ devs
- Procurement process, security reviews
- Needs audit trail, compliance
- 6-12 month sales cycle

**Pain:**
- Inconsistent code quality across teams
- Security concerns with AI tools
- Justifying AI spend to leadership
- Onboarding new devs to codebase patterns

**Value prop:** "Codify your engineering standards. Every PR follows your patterns."

**Requirements:**
- SSO/SAML
- Audit logging
- Role-based access
- Self-hosted (air-gapped option)
- SLA and support

**Adoption path:** Team lead champion → pilot program → security review → procurement → rollout

---

## Go-to-Market Priority

**Phase 1: Team Lead Focus**
- Lowest friction to prove value
- Can expand to company (land and expand)
- Budget authority without procurement
- Best testimonials ("my team shipped 3x more")

**Phase 2: Solo Dev**
- Self-serve funnel
- Lower LTV but higher volume
- Good for community building

**Phase 3: Enterprise**
- Requires team lead champions first
- Build case studies from Phase 1
- Add compliance features as needed

---

## Messaging by Persona

### Solo Dev
> "Ship your boring tickets while you sleep. Pilot turns your backlog into PRs."

### Team Lead
> "Turn your ticket backlog into PRs while you focus on architecture."

### Enterprise
> "Codify your engineering standards. Every PR follows your patterns, across every team."

---

## Landing Page Structure (Team Lead)

**Hero:**
"Turn your ticket backlog into PRs while you focus on architecture"

**Sub-hero:**
Pilot picks up tickets from Linear/GitHub/Jira, implements with Claude Code, creates PRs you review like any team member.

**CTA:**
"Try Pilot on 5 tickets" (not "Sign up")

**Proof points:**
- "47 CRUD tickets → 47 PRs in 2 days"
- "Reviewed like any junior's code, merged 89%"
- "Self-hosted. Your secrets never leave your infra."

**How it works:**
1. Manager creates ticket in Linear
2. Pilot picks it up automatically
3. Implements using your codebase patterns (Navigator)
4. Creates PR with tests
5. You review and merge

**Trust section:**
- Open source (link to GitHub)
- Self-hosted (no cloud dependency)
- PR-only (never commits to main)
- Human review required

**Use cases:**
- CRUD endpoints and React components
- Test coverage for existing code
- Documentation updates
- Bug fixes with clear repro steps

**What it's not for:**
Architecture decisions, novel algorithms, anything requiring taste.

---

## Key Differentiators vs Competition

| Feature | ClawdBot/Melbot | Cursor | Pilot |
|---------|-----------------|--------|-------|
| Prompting required | Yes | Yes | No (ticket-driven) |
| Codebase context | Generic | File-level | Navigator (patterns, SOPs, architecture) |
| Cross-project memory | No | No | Yes |
| Output | Code suggestions | Code in editor | PR ready for review |
| Deployment | Cloud | Desktop app | Self-hosted |
| Token efficiency | High burn | Medium | Low (Navigator lazy loading) |
