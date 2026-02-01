# Deployment Guide

Deploy Pilot on your preferred cloud platform. Pilot is a Go binary that runs as a long-running daemon, polling for tasks and executing them.

## Requirements

- Outbound HTTPS (Claude API, GitHub/Linear/Jira APIs)
- Persistent disk for SQLite knowledge graph (~100MB)
- `ANTHROPIC_API_KEY` environment variable
- No inbound ports required (Pilot polls, doesn't need webhooks)

## Platform Comparison

### Recommended (Long-running daemon support)

| Platform | Config | Min Cost | Notes |
|----------|--------|----------|-------|
| [Azure Container Apps](#azure-container-apps) | `deploy/azure/` | ~$5/mo | Always-on with reduced idle pricing |
| [Cloudflare Containers](#cloudflare-containers) | `deploy/cloudflare/` | Usage-based | New in 2025, "Region: Earth" deployment |
| [Fly.io](#flyio) | `deploy/fly.toml` | ~$2/mo | Hardware-virtualized, instant start |
| [Railway](#railway) | `deploy/railway.toml` | ~$5/mo | Native Workers support, auto-detect Go |
| [Render](#render) | `deploy/render.yaml` | ~$7/mo | Background workers + cron built-in |
| [AWS Fargate](#aws-fargateecs) | `deploy/aws/` | ~$10/mo | Full control, enterprise-grade |

### Not Recommended

| Platform | Issue |
|----------|-------|
| Vercel | 5min function timeout, serverless-only |
| AWS App Runner | No persistent processes, stateless only |
| Cloudflare Workers (non-container) | 30s CPU limit |
| AWS Lambda | 15min max, cold starts |

## Quick Deploy

### Docker (Any Platform)

```bash
docker run -d \
  --name pilot \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  -e GITHUB_TOKEN=$GITHUB_TOKEN \
  -e TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN \
  -v pilot-data:/data \
  -v /path/to/projects:/projects \
  ghcr.io/alekspetrov/pilot:latest \
  start --telegram --github
```

---

## Azure Container Apps

Azure Container Apps supports always-on background jobs with reduced idle pricing.

### Deploy with Azure CLI

```bash
# Create resource group
az group create --name pilot-rg --location eastus

# Create Container Apps environment
az containerapp env create \
  --name pilot-env \
  --resource-group pilot-rg \
  --location eastus

# Deploy Pilot
az containerapp create \
  --name pilot \
  --resource-group pilot-rg \
  --environment pilot-env \
  --image ghcr.io/alekspetrov/pilot:latest \
  --min-replicas 1 \
  --max-replicas 1 \
  --cpu 0.5 \
  --memory 1Gi \
  --env-vars \
    ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
    GITHUB_TOKEN=$GITHUB_TOKEN \
    TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN \
  --command "pilot" "start" "--telegram" "--github"
```

### Deploy with Bicep

See `deploy/azure/container-app.bicep` for Infrastructure as Code.

```bash
az deployment group create \
  --resource-group pilot-rg \
  --template-file deploy/azure/container-app.bicep \
  --parameters anthropicApiKey=$ANTHROPIC_API_KEY
```

**Estimated cost:** ~$5-15/mo depending on usage (idle replicas billed at reduced rate)

---

## Cloudflare Containers

Cloudflare Containers (launched June 2025) allows running any Docker image globally.

### Deploy

```bash
# Install Wrangler CLI
npm install -g wrangler

# Login to Cloudflare
wrangler login

# Deploy container
wrangler containers deploy \
  --name pilot \
  --image ghcr.io/alekspetrov/pilot:latest
```

### Configuration

See `deploy/cloudflare/wrangler.toml`:

```toml
name = "pilot"
compatibility_date = "2025-06-01"

[containers]
image = "ghcr.io/alekspetrov/pilot:latest"
command = ["pilot", "start", "--telegram", "--github"]

[containers.env]
ANTHROPIC_API_KEY = { type = "secret" }
GITHUB_TOKEN = { type = "secret" }
```

**Note:** Cloudflare Containers is in beta. Check [Cloudflare docs](https://developers.cloudflare.com/containers/) for current limitations.

**Estimated cost:** Usage-based, competitive with other platforms

---

## Fly.io

Fly.io runs hardware-virtualized containers with instant start times. Great for Go binaries.

### Deploy

```bash
# Install flyctl
curl -L https://fly.io/install.sh | sh

# Launch app
fly launch --no-deploy

# Set secrets
fly secrets set \
  ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  GITHUB_TOKEN=$GITHUB_TOKEN \
  TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN

# Deploy
fly deploy
```

### Configuration

See `deploy/fly.toml`:

```toml
app = "pilot"
primary_region = "iad"

[build]
image = "ghcr.io/alekspetrov/pilot:latest"

[env]
PILOT_DATA_DIR = "/data"

[mounts]
source = "pilot_data"
destination = "/data"

[[services]]
internal_port = 9090
protocol = "tcp"
auto_stop_machines = false
auto_start_machines = true
min_machines_running = 1

[processes]
app = "pilot start --telegram --github"
```

**Estimated cost:** ~$2-5/mo (shared-cpu-1x with 256MB)

---

## Railway

Railway auto-detects Go projects and supports background workers natively.

### Deploy

```bash
# Install Railway CLI
npm install -g @railway/cli

# Login
railway login

# Initialize project
railway init

# Deploy
railway up
```

### Configuration

See `deploy/railway.toml`:

```toml
[build]
builder = "nixpacks"

[deploy]
startCommand = "pilot start --telegram --github"
restartPolicyType = "always"
```

Set environment variables in Railway dashboard or CLI:

```bash
railway variables set ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
railway variables set GITHUB_TOKEN=$GITHUB_TOKEN
```

**Estimated cost:** ~$5/mo (usage-based)

---

## Render

Render has native background worker support with persistent disks.

### Deploy

1. Connect your GitHub repository
2. Select "Background Worker" as service type
3. Configure environment variables
4. Deploy

### Configuration

See `deploy/render.yaml`:

```yaml
services:
  - type: worker
    name: pilot
    runtime: docker
    dockerfilePath: ./Dockerfile
    envVars:
      - key: ANTHROPIC_API_KEY
        sync: false
      - key: GITHUB_TOKEN
        sync: false
      - key: TELEGRAM_BOT_TOKEN
        sync: false
    disk:
      name: pilot-data
      mountPath: /data
      sizeGB: 1
```

**Estimated cost:** ~$7/mo (Starter plan)

---

## AWS Fargate/ECS

For enterprise deployments with full control.

### Deploy with AWS CLI

```bash
# Create ECS cluster
aws ecs create-cluster --cluster-name pilot-cluster

# Register task definition
aws ecs register-task-definition \
  --cli-input-json file://deploy/aws/task-definition.json

# Create service
aws ecs create-service \
  --cluster pilot-cluster \
  --service-name pilot \
  --task-definition pilot \
  --desired-count 1 \
  --launch-type FARGATE \
  --network-configuration "awsvpcConfiguration={subnets=[subnet-xxx],securityGroups=[sg-xxx],assignPublicIp=ENABLED}"
```

### Configuration

See `deploy/aws/task-definition.json` for full ECS task definition.

**Estimated cost:** ~$10-20/mo (0.25 vCPU, 0.5GB)

---

## Self-Hosted (VM/VPS)

For any Linux VM (Azure VM, EC2, DigitalOcean, etc.):

```bash
# Install Pilot
curl -fsSL https://raw.githubusercontent.com/alekspetrov/pilot/main/install.sh | bash

# Create systemd service
sudo tee /etc/systemd/system/pilot.service > /dev/null <<EOF
[Unit]
Description=Pilot AI Development Agent
After=network.target

[Service]
Type=simple
User=pilot
Environment=ANTHROPIC_API_KEY=your-key
Environment=GITHUB_TOKEN=your-token
ExecStart=/usr/local/bin/pilot start --telegram --github
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start
sudo systemctl enable pilot
sudo systemctl start pilot
```

**Estimated cost:** $4-10/mo (smallest VM tier)

---

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ANTHROPIC_API_KEY` | Yes | Claude API key |
| `GITHUB_TOKEN` | For GitHub | Personal access token with repo scope |
| `TELEGRAM_BOT_TOKEN` | For Telegram | Bot token from @BotFather |
| `TELEGRAM_CHAT_ID` | For Telegram | Your chat ID |
| `LINEAR_API_KEY` | For Linear | Linear API key |
| `SLACK_BOT_TOKEN` | For Slack | Slack bot OAuth token |

## Persistent Storage

Pilot stores data in SQLite. Mount a persistent volume at:

- Default: `~/.pilot/`
- Override: `PILOT_DATA_DIR=/custom/path`

Minimum disk: 100MB (grows with knowledge graph)

## Health Checks

Pilot exposes health endpoints when gateway is enabled:

```bash
# Health check
curl http://localhost:9090/health

# Ready check
curl http://localhost:9090/ready
```

## Troubleshooting

### Container won't start

Check logs for missing environment variables:
```bash
docker logs pilot
fly logs
railway logs
```

### Can't connect to APIs

Ensure outbound HTTPS (443) is allowed. No inbound ports needed.

### High memory usage

Pilot typically uses 50-200MB. If higher:
- Check for stuck Claude Code processes
- Restart the container

---

## Next Steps

- [Configuration Guide](../README.md#configuration)
- [CLI Reference](../README.md#cli-reference)
- [Telegram Setup](./TELEGRAM.md)
