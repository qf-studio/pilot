# TASK-37: Cloudflare Tunnel Integration

**Status**: 📋 Planned
**Priority**: Medium (P3)
**Created**: 2026-01-27
**Depends On**: TASK-36 (polling is fallback)

---

## Context

**Problem**:
For users who want real-time webhook triggers (vs polling), they need a public URL. ngrok is temporary and requires manual restart. Cloudflare Tunnel is free and permanent.

**Goal**:
Integrated Cloudflare Tunnel setup for permanent webhook URLs without manual configuration.

---

## Design

### Configuration

```yaml
adapters:
  github:
    enabled: true
    webhook:
      enabled: true
      tunnel: cloudflare  # or "ngrok", "manual"
      domain: pilot.example.com  # Optional custom domain
```

### Setup Flow

```
pilot setup --tunnel

1. Check cloudflared installed
   → If not: brew install cloudflared

2. Check tunnel exists
   → If not: cloudflared tunnel create pilot-webhook

3. Configure DNS (if domain provided)
   → cloudflared tunnel route dns pilot-webhook <domain>

4. Generate webhook secret
   → Store in config

5. Create GitHub webhook
   → gh api repos/.../hooks POST

6. Start tunnel as background service
   → launchd plist for macOS
```

### Tunnel Manager

```go
type TunnelManager struct {
    provider string  // cloudflare, ngrok
    domain   string
    port     int
    cmd      *exec.Cmd
}

func (m *TunnelManager) Start(ctx context.Context) (string, error) {
    switch m.provider {
    case "cloudflare":
        return m.startCloudflare(ctx)
    case "ngrok":
        return m.startNgrok(ctx)
    default:
        return "", nil  // Manual mode
    }
}

func (m *TunnelManager) startCloudflare(ctx context.Context) (string, error) {
    // Check for existing tunnel
    tunnelID := m.getOrCreateTunnel()

    // Start tunnel
    m.cmd = exec.CommandContext(ctx, "cloudflared", "tunnel", "run", tunnelID)
    m.cmd.Start()

    return fmt.Sprintf("https://%s.cfargotunnel.com", tunnelID), nil
}
```

### macOS Service (launchd)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.pilot.tunnel</string>
    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/bin/cloudflared</string>
        <string>tunnel</string>
        <string>run</string>
        <string>pilot-webhook</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
```

---

## Implementation

### Files to Create/Modify

| File | Change |
|------|--------|
| `internal/tunnel/manager.go` | New - tunnel lifecycle |
| `internal/tunnel/cloudflare.go` | Cloudflare-specific logic |
| `internal/tunnel/ngrok.go` | ngrok fallback |
| `internal/tunnel/service_darwin.go` | macOS launchd integration |
| `cmd/pilot/setup.go` | Add `--tunnel` flag |
| `cmd/pilot/main.go` | Integrate tunnel with gateway |

### CLI Commands

```bash
# Interactive setup
pilot setup --tunnel

# Check tunnel status
pilot tunnel status

# Restart tunnel
pilot tunnel restart

# Show webhook URL
pilot tunnel url
```

---

## Acceptance Criteria

- [ ] `pilot setup --tunnel` configures Cloudflare Tunnel
- [ ] Tunnel auto-starts on boot (launchd)
- [ ] Webhook URL persisted and displayed
- [ ] GitHub webhook auto-created
- [ ] Falls back to polling if tunnel unavailable
- [ ] `pilot tunnel status` shows connection state

---

## Prerequisites

- Cloudflare account (free tier sufficient)
- `cloudflared` CLI installed
- Domain (optional - uses *.cfargotunnel.com otherwise)

---

## Notes

- Cloudflare Tunnel is free for unlimited bandwidth
- No port forwarding or firewall changes needed
- Works behind CGNAT (common for home internet)
- Consider ngrok as simpler alternative for quick testing
