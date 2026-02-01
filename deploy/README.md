# Pilot Deployment Configs

Pre-built configurations for deploying Pilot on various cloud platforms.

## Quick Reference

| Platform | Config | Deploy Command |
|----------|--------|----------------|
| [Fly.io](https://fly.io) | `fly.toml` | `fly launch && fly deploy` |
| [Railway](https://railway.app) | `railway.toml` | `railway up` |
| [Render](https://render.com) | `render.yaml` | Connect repo in dashboard |
| [Cloudflare](https://developers.cloudflare.com/containers/) | `cloudflare/wrangler.toml` | `wrangler containers deploy` |
| [Azure](https://azure.microsoft.com/en-us/products/container-apps) | `azure/container-app.bicep` | `az deployment group create ...` |
| [AWS](https://aws.amazon.com/fargate/) | `aws/cloudformation.yaml` | `aws cloudformation deploy ...` |

## Full Documentation

See [docs/DEPLOYMENT.md](../docs/DEPLOYMENT.md) for:
- Detailed setup instructions per platform
- Environment variable reference
- Persistent storage configuration
- Cost estimates
- Troubleshooting

## Required Secrets

All platforms need these environment variables:

```bash
ANTHROPIC_API_KEY=sk-ant-...    # Required
GITHUB_TOKEN=ghp_...            # For GitHub integration
TELEGRAM_BOT_TOKEN=...          # For Telegram integration
```

## Docker

For any platform supporting containers:

```bash
docker run -d \
  --name pilot \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  -e GITHUB_TOKEN=$GITHUB_TOKEN \
  -v pilot-data:/data \
  ghcr.io/alekspetrov/pilot:latest \
  start --telegram --github
```
