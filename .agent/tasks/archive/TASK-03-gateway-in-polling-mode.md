# TASK-03: Start Gateway HTTP Server in Polling Mode

**Status**: 🚧 In Progress
**Created**: 2026-02-20
**Assignee**: Pilot

---

## Context

**Problem**:
Desktop app shows "daemon offline" even when `pilot start` is running with polling adapters (`--telegram --github --slack`). The desktop app's `GetServerStatus()` checks the gateway's `/health` endpoint, but the gateway never starts in polling mode.

**Root Cause**:
`cmd/pilot/main.go` line 266:
```go
if noGateway || hasPollingAdapter {
    return runPollingMode(cfg, projectPath, replace, dashboardMode)
}
```
When any polling adapter is active, `runPollingMode()` is called which does NOT start the HTTP gateway. The gateway only runs in "full daemon mode" (webhook-only adapters like Linear/Jira).

**Goal**:
Start a lightweight HTTP gateway in the background within `runPollingMode()` so that:
1. Desktop app can detect running daemon via `/health`
2. Dashboard API endpoints (`/api/v1/*`) are available
3. Web UI can connect when running in polling mode

**Success Criteria**:
- [ ] `/health` returns 200 when `pilot start --telegram --github` is running
- [ ] Desktop app shows "daemon running" instead of "daemon offline"
- [ ] `--no-gateway` flag still suppresses the gateway
- [ ] No disruption to existing polling mode behavior

---

## Implementation Plan

### Phase 1: Start gateway in background within runPollingMode

**Goal**: Add gateway HTTP server startup to `runPollingMode()` function

**Tasks**:
- [ ] In `runPollingMode()` (~line 1179), after store initialization (~line 1330), create and start a gateway server in a background goroutine
- [ ] Only start gateway if `!noGateway` (respect the explicit opt-out flag)
- [ ] Use `cfg.Gateway` for host/port configuration
- [ ] Wire existing dashboard store, autopilot controller, and metrics sources to the gateway
- [ ] Serve embedded React dashboard if available (`dashboardFS`)
- [ ] Handle graceful shutdown — cancel gateway context when polling mode exits

**Files**:
- `cmd/pilot/main.go` — `runPollingMode()` function

**Implementation details**:

After the memory store and autopilot controller setup (around line 1384), add:

```go
// Start lightweight gateway in background for desktop app / web UI connectivity
if !noGateway && cfg.Gateway != nil {
    gwCfg := &gateway.Config{
        Host: cfg.Gateway.Host,
        Port: cfg.Gateway.Port,
    }
    gwServer := gateway.NewServer(gwCfg)

    // Wire dashboard data sources
    if store != nil {
        gwServer.SetMetricsSource(store)
        gwServer.SetDashboardStore(store)
    }
    if autopilotController != nil {
        gwServer.SetAutopilotProvider(autopilotController)
    }

    // Serve embedded React dashboard if available
    if dashboardEmbedded {
        gwServer.SetDashboardFS(dashboardFS)
    }

    go func() {
        if err := gwServer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
            logging.WithComponent("gateway").Warn("gateway server stopped", slog.Any("error", err))
        }
    }()
    logging.WithComponent("start").Info("gateway started in background",
        slog.String("address", fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)))
}
```

### Phase 2: Fix hardcoded port in desktop app

**Goal**: `fetchAutopilotFromDaemon()` uses hardcoded port 9090, should use config

**Tasks**:
- [ ] In `desktop/app.go` line 263, replace `daemonGatewayURL` constant with `a.gatewayURL`
- [ ] Remove the unused `daemonGatewayURL` constant (line 257)

**Files**:
- `desktop/app.go`

### Phase 3: Pass noGateway flag to runPollingMode

**Goal**: `runPollingMode()` needs access to `noGateway` to respect opt-out

**Tasks**:
- [ ] Add `noGateway bool` parameter to `runPollingMode()` signature
- [ ] Update the call site at line 267

**Files**:
- `cmd/pilot/main.go`

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Gateway scope | Full gateway vs health-only | Full gateway | Desktop app also needs `/api/v1/autopilot`, web UI needs all endpoints |
| Gateway lifecycle | Separate process vs goroutine | Background goroutine | Simpler, shares context and store, auto-cleanup on shutdown |
| `--no-gateway` | Remove flag vs keep | Keep | Users may want polling-only mode without any HTTP server |

---

## Verify

```bash
# Build and test
make test
make build

# Verify gateway starts in polling mode
./bin/pilot start --telegram --github --env stage &
sleep 5
curl -s http://127.0.0.1:9091/health  # Should return 200
curl -s http://127.0.0.1:9091/api/v1/status  # Should return version

# Verify --no-gateway still works
./bin/pilot start --telegram --github --no-gateway &
sleep 5
curl -s http://127.0.0.1:9091/health  # Should fail (connection refused)
```

---

## Done

- [ ] Gateway HTTP server starts in `runPollingMode()` background
- [ ] Desktop app shows "daemon running" when Pilot is active
- [ ] `--no-gateway` suppresses gateway as before
- [ ] `fetchAutopilotFromDaemon()` uses config port, not hardcoded 9090
- [ ] All existing tests pass

---

**Last Updated**: 2026-02-20
