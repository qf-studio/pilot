# TASK-17: Team Management & Permissions

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Monetization / Enterprise

---

## Context

**Problem**:
Single-user only. Teams can't share Pilot or manage access.

**Goal**:
Multi-user support with roles and permissions.

---

## User Roles

| Role | Permissions |
|------|-------------|
| Owner | Full access, billing, delete team |
| Admin | Manage members, projects, settings |
| Developer | Execute tasks, view all projects |
| Viewer | View tasks, read-only |

---

## Features

### Team Management
- Create/delete team
- Invite members (email)
- Assign roles
- Remove members

### Project Permissions
- Per-project access control
- Default role for new projects
- Inherit team permissions

### Audit Log
- Who did what, when
- Required for enterprise compliance

---

## Implementation

### Phase 1: Local Teams
- Team config in YAML
- Role-based CLI access
- No cloud required

### Phase 2: Cloud Teams (Pilot Cloud)
- Web dashboard
- SSO integration
- Centralized team management

---

## Configuration

```yaml
team:
  name: "Acme Corp"
  members:
    - email: "alice@acme.com"
      role: owner
    - email: "bob@acme.com"
      role: developer
      projects: ["frontend", "api"]
```

---

**Monetization**:
- Free: 1 user
- Pro: 5 users
- Team: 20 users
- Enterprise: Unlimited
