# [Project Name] - Development Navigator

**Project**: [Brief project description]
**Tech Stack**: [Your tech stack]
**Updated**: [Date]

---

## Quick Start

### Starting a New Feature
1. Check `tasks/` for similar previous work
2. Review `system/` for architecture context
3. Create implementation plan

### Documentation Structure

```
.agent/
├── DEVELOPMENT-README.md     ← You are here
├── tasks/                    ← Implementation plans (archive/ when done)
├── system/                   ← Architecture docs, incl. FEATURE-MATRIX.md
├── knowledge/                ← Knowledge graph consumed every session
│   ├── graph.json            ← Indexed concept/memory nodes
│   └── memories/             ← One markdown file per memory
│       ├── patterns/
│       ├── pitfalls/
│       ├── decisions/
│       └── learnings/
└── sops/                     ← Standard Operating Procedures
    ├── integrations/
    ├── debugging/
    ├── development/
    └── deployment/
```

---

## Project Structure

```
[Project Name]/
└── .agent/              ← Navigator docs (this directory)
```

_Fill in the real source tree above during the first task session._

## Key Files

_List the files a new session should read first — fill in as the project grows._

## Architecture

### Key Components

_Describe the major components/services and their responsibilities — fill in
during the first task session._

---

## Token Optimization

Load only what you need:
1. This file (~2,000 tokens)
2. Current task doc (~3,000 tokens)
3. Relevant system doc (~5,000 tokens)

Total: ~10,000 tokens vs loading everything

---

**Powered By**: Pilot + Navigator
