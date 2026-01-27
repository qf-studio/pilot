# Pilot: Competitive Analysis

## Market Landscape

```
                    Autonomous
                        ↑
                        |
           Pilot        |        Devin
           ●            |        ●
                        |
    ←───────────────────┼───────────────────→
    Self-hosted         |              Cloud
                        |
           Copilot      |       Cursor
           ●            |        ●
                        |
                        ↓
                    Assistant
```

**Pilot's position:** Autonomous + Self-hosted (unique quadrant)

---

## Direct Competitors

### ClawdBot / Melbot

**What it is:** Claude Code wrapper with Telegram/Slack interface

**Pricing:** Token-based (variable)

**Strengths:**
- Quick to set up
- Conversational interface
- General-purpose

**Weaknesses:**
- Token burn (users report $50+/day)
- No codebase context
- Still requires prompting
- No pattern learning

**User complaints (from research):**
- "Burned through my budget in hours"
- "Doesn't know my codebase"
- "Still babysitting it all day"
- "Security concerns - what's it doing?"

**How Pilot wins:**
| ClawdBot | Pilot |
|----------|-------|
| You prompt | Ticket-driven, no prompting |
| Generic context | Navigator knows your patterns |
| Token burn | Predictable ticket pricing |
| Cloud | Self-hosted |

**Sales objection handler:**
> "ClawdBot is great for ad-hoc questions. Pilot is for systematic ticket completion. Different tools for different jobs. Try both - use ClawdBot for exploration, Pilot for your backlog."

---

### Cursor

**What it is:** VS Code fork with AI built-in

**Pricing:** $20/mo pro, $40/mo business (per seat)

**Strengths:**
- Excellent IDE integration
- Fast autocomplete
- Good UX
- Strong community

**Weaknesses:**
- Still requires you in the loop
- File-level context only
- No cross-project memory
- Per-seat pricing penalizes teams

**How Pilot wins:**
| Cursor | Pilot |
|--------|-------|
| You write with AI assist | AI writes, you review |
| In your IDE | In your ticket system |
| Per-seat pricing | Per-ticket pricing |
| File context | Project + cross-project context |

**Positioning:**
> "Cursor makes you faster. Pilot removes you from the loop entirely. Use Cursor for complex work, Pilot for your backlog."

**Coexistence:** Not mutually exclusive. Team can use both.

---

### GitHub Copilot / Copilot Workspace

**What it is:** AI pair programmer (autocomplete + chat)

**Pricing:** $19/mo individual, $39/mo business (per seat)

**Strengths:**
- Microsoft/GitHub backing
- Ubiquitous adoption
- Deep GitHub integration
- Enterprise trust

**Weaknesses:**
- Autocomplete, not autonomous
- No ticket integration
- Generic suggestions
- Per-seat pricing

**How Pilot wins:**
| Copilot | Pilot |
|---------|-------|
| Suggests code as you type | Completes entire tickets |
| You drive | Ticket drives |
| Generic model | Your patterns via Navigator |
| GitHub only | Linear, GitHub, Jira |

**Copilot Workspace (beta):**
- Closer to Pilot's model (ticket → PR)
- But: Cloud-only, GitHub-only, no Navigator equivalent
- Watch this space - potential future threat

---

### Devin (Cognition)

**What it is:** "AI software engineer" - fully autonomous agent

**Pricing:** Enterprise only (rumored $500+/seat/month)

**Strengths:**
- Most autonomous option
- Can handle complex multi-step tasks
- Strong demo videos

**Weaknesses:**
- Expensive
- Cloud-only (security concern)
- Black box
- Overpromised capabilities
- Long wait times for results

**How Pilot wins:**
| Devin | Pilot |
|-------|-------|
| $500+/seat | $149/team |
| Cloud | Self-hosted |
| Black box | Open source |
| "Trust me" | PR review |
| Complex tasks | Right-sized tickets |

**Positioning:**
> "Devin tries to replace engineers. Pilot augments them. We handle the backlog, you handle the architecture."

---

### DIY Claude Code Setup

**What it is:** Teams rolling their own automation

**Strengths:**
- Full control
- No vendor dependency
- Customizable

**Weaknesses:**
- Maintenance burden
- No best practices
- Rebuilding solved problems
- Context management is hard

**How Pilot wins:**
> "We built the infrastructure so you don't have to. Navigator integration, ticket sync, PR creation, progress tracking - all maintained and improving."

---

## Feature Comparison Matrix

| Feature | Pilot | ClawdBot | Cursor | Copilot | Devin |
|---------|-------|----------|--------|---------|-------|
| **Autonomous execution** | ✓ | Partial | ✗ | ✗ | ✓ |
| **Ticket integration** | ✓ | ✗ | ✗ | Partial | ✓ |
| **Codebase context** | Navigator | Generic | File-level | File-level | Proprietary |
| **Cross-project memory** | ✓ | ✗ | ✗ | ✗ | ? |
| **Self-hosted** | ✓ | ✗ | Desktop | ✗ | ✗ |
| **Open source** | ✓ | ✗ | ✗ | ✗ | ✗ |
| **PR output** | ✓ | ✗ | ✗ | Workspace | ✓ |
| **Predictable pricing** | ✓ | ✗ | ✓ | ✓ | ? |
| **Human review required** | ✓ | N/A | N/A | N/A | ✗ |

---

## Positioning Statements

### vs ClawdBot
> "ClawdBot is a chatbot. Pilot is a pipeline. Chat when you need exploration, pipeline when you need execution."

### vs Cursor
> "Cursor is a faster keyboard. Pilot is another pair of hands. Different tools, both valuable."

### vs Copilot
> "Copilot suggests. Pilot ships. Use Copilot for complex work, Pilot for your backlog."

### vs Devin
> "Devin wants to replace you. Pilot wants to unblock you. We handle the tickets you don't want to, you handle the ones that matter."

### vs DIY
> "You could build this. But should you? We maintain the infrastructure, you ship features."

---

## Competitive Moats

**1. Navigator Integration**
- No competitor has equivalent codebase understanding
- Pattern learning across projects
- SOP enforcement
- Growing stronger with each task

**2. Self-Hosted**
- Only autonomous option that's self-hosted
- Enterprise security requirement
- No data leaves your infra

**3. Open Source**
- Inspect the code
- Audit what it does
- Community contributions
- No vendor lock-in

**4. Human-in-the-Loop by Design**
- PR review is mandatory, not optional
- Builds trust gradually
- "Autonomy without oversight is a CVE"

---

## Threats to Watch

| Threat | Timeline | Mitigation |
|--------|----------|------------|
| Copilot Workspace GA | 6-12 mo | Navigator differentiation, self-hosted |
| Cursor adds autonomy | 6-12 mo | Ticket integration, cross-project |
| ClawdBot adds Navigator | 3-6 mo | First-mover, deeper integration |
| Big tech enters | 12-24 mo | Community, open source, self-hosted |

---

## Win/Loss Analysis Template

After each sales conversation, log:

```markdown
## Deal: [Company Name]

**Result:** Won / Lost / Pending

**Competitors evaluated:**
- [ ] ClawdBot
- [ ] Cursor
- [ ] Copilot
- [ ] Devin
- [ ] DIY

**Why they chose us:**
-

**Why they didn't:**
-

**Key objections:**
-

**Feature requests:**
-
```

Build pattern database for messaging refinement.
