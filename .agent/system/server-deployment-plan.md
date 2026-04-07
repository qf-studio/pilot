# Pilot Server Deployment Plan for Nelya

**Goal**: Move Pilot execution from Aleks's laptop (16GB, OOM issues) to a persistent server.

**Date**: 2026-03-31

---

## What We Need

A single always-on instance running `pilot start` with all projects connected. Not bench infrastructure (that's separate ASG) — this is the production Pilot daemon.

## Architecture

```
┌─────────────────────────────────────────────┐
│  EC2 Instance (t3.xlarge, 16GB)             │
│                                             │
│  pilot start --telegram --github --linear   │
│                                             │
│  Projects:                                  │
│    /opt/pilot/repos/pilot                   │
│    /opt/pilot/repos/aso-generator           │
│    /opt/pilot/repos/navigator               │
│    /opt/pilot/repos/bostonteamgroup         │
│                                             │
│  Config: /opt/pilot/.pilot/config.yaml      │
│  Data:   /opt/pilot/.pilot/data/ (SQLite)   │
│                                             │
│  Adapters:                                  │
│    ├─ Telegram (polling)                    │
│    ├─ GitHub (polling, all repos)           │
│    ├─ Linear (polling, APP workspace)       │
│    ├─ Discord (gateway WebSocket)           │
│    └─ Slack (socket mode)                   │
│                                             │
│  Claude Code: installed via npm             │
│  Auth: Claude Code OAuth token (SSM)        │
└─────────────────────────────────────────────┘
```

## Instance Requirements

| Resource | Spec | Why |
|----------|------|-----|
| Type | t3.xlarge (4 vCPU, 16GB) or t3.2xlarge (8 vCPU, 32GB) | Claude Code subprocess needs RAM, no competing desktop apps |
| EBS | 50GB gp3 | Repos + worktrees + node_modules for JS projects |
| Region | eu-central-1 | Same as existing bench infra |
| AMI | Golden AMI from `aws-infrastructure-pilot` | Already has Node 22, Claude Code, Python, Git |

**Recommendation**: Start with t3.xlarge (16GB). No desktop apps competing = plenty of headroom. Upgrade to 2xlarge if parallel execution needs it.

## SSM Parameters (existing + new)

Existing (from bench infra):
- `/pilot/ANTHROPIC_API_KEY`
- `/pilot/GITHUB_TOKEN`
- `/pilot/CLAUDE_CODE_OAUTH_TOKEN`

New:
- `/pilot/TELEGRAM_BOT_TOKEN` — `8597533436:AAFavqSF1ruY4TeP6IWEU0eRjBfuLMr6hew`
- `/pilot/SLACK_BOT_TOKEN` — from config
- `/pilot/SLACK_APP_TOKEN` — from config
- `/pilot/LINEAR_API_KEY` — from config
- `/pilot/DISCORD_BOT_TOKEN` — from config

**IMPORTANT**: All tokens must be moved from laptop config to SSM. Config.yaml on server references env vars, not raw tokens.

## Setup Steps

### 1. Instance (Nelya)
- Dedicated EC2 (not ASG/warm pool — this is always-on)
- Elastic IP for stable address
- Security group: outbound only (no inbound needed — all adapters are polling/outbound)
- IAM role: SSM read access to `/pilot/*` params

### 2. Deploy Pilot Binary (CI/CD)
- GoReleaser already builds `pilot-linux-amd64.tar.gz`
- Download from GitHub Release on instance boot
- Or: add a `deploy-server` step to release workflow

### 3. Clone All Repos
```bash
mkdir -p /opt/pilot/repos
cd /opt/pilot/repos
git clone https://github.com/qf-studio/pilot.git
git clone https://github.com/alekspetrov/aso-generator.git
git clone https://github.com/alekspetrov/navigator.git
git clone https://github.com/alekspetrov/boston-team-group.git
```

GitHub token for cloning: use `/pilot/GITHUB_TOKEN` from SSM.

### 4. Config
Copy config.yaml template, replace paths and tokens with SSM references:
```yaml
projects:
  - name: pilot
    path: /opt/pilot/repos/pilot
    github:
      owner: qf-studio
      repo: pilot
  - name: aso-generator
    path: /opt/pilot/repos/aso-generator
    github:
      owner: alekspetrov
      repo: aso-generator
  # ... etc
```

### 5. Systemd Service
```ini
[Unit]
Description=Pilot AI Development Pipeline
After=network.target

[Service]
Type=simple
User=pilot
WorkingDirectory=/opt/pilot
ExecStart=/usr/local/bin/pilot start --telegram --github --linear --discord --slack
Restart=always
RestartSec=10
Environment=ANTHROPIC_API_KEY=<from SSM at boot>
Environment=GITHUB_TOKEN=<from SSM at boot>
# ... all tokens

[Install]
WantedBy=multi-user.target
```

### 6. Bootstrap Script (runs on boot)
```bash
#!/bin/bash
# Fetch secrets from SSM
export ANTHROPIC_API_KEY=$(aws ssm get-parameter --name /pilot/ANTHROPIC_API_KEY --with-decryption --query Parameter.Value --output text)
export GITHUB_TOKEN=$(aws ssm get-parameter --name /pilot/GITHUB_TOKEN --with-decryption --query Parameter.Value --output text)
# ... all tokens

# Pull latest repos
for repo in /opt/pilot/repos/*; do
  cd "$repo" && git pull origin main
done

# Download latest Pilot release
LATEST=$(curl -s https://api.github.com/repos/qf-studio/pilot/releases/latest | jq -r .tag_name)
curl -sL "https://github.com/qf-studio/pilot/releases/download/${LATEST}/pilot-linux-amd64.tar.gz" | tar xz -C /usr/local/bin/

# Start Pilot
systemctl start pilot
```

## Monitoring

- Pilot sends alerts to Slack `#engineering` (already configured)
- Telegram bot stays responsive (Aleks controls via phone)
- CloudWatch for instance health
- Optional: expose web dashboard on localhost, access via SSM port forward

## Migration Plan

1. **Phase 1**: Set up instance, deploy Pilot, test with one project (pilot repo only)
2. **Phase 2**: Add aso-generator, navigator, bostonteamgroup
3. **Phase 3**: Stop running `pilot start` on laptop
4. **Phase 4**: Laptop becomes Navigator-only (planning), server does all execution

## Cost Estimate

| Resource | Monthly |
|----------|---------|
| t3.xlarge on-demand | ~$120 |
| t3.xlarge reserved 1yr | ~$75 |
| 50GB gp3 EBS | ~$5 |
| Data transfer | ~$5 |
| **Total** | **~$85-130/mo** |

vs. OOM crashes, laptop fan noise, and 16GB RAM fights.

## Reuses from Bench Infra

- Golden AMI (Node 22, Claude Code, Python, Git)
- SSM parameter store (same prefix `/pilot/`)
- IAM roles (adjust for always-on vs ASG)
- VPC networking (workload VPC)

Nelya already has all the CloudFormation patterns in `qf-studio/aws-infrastructure-pilot`.

---

**Next step**: Nelya reviews, asks questions, provisions instance. Aleks provides token values for SSM.
