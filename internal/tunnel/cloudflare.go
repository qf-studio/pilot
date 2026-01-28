package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cloudflaredBin     = "cloudflared"
	tunnelName         = "pilot-webhook"
	defaultTunnelPort  = 9090
	connectionTimeout  = 30 * time.Second
)

// CloudflareProvider implements the Cloudflare Tunnel provider
type CloudflareProvider struct {
	config   *Config
	logger   *slog.Logger
	cmd      *exec.Cmd
	tunnelID string
	url      string
	mu       sync.Mutex
}

// NewCloudflareProvider creates a new Cloudflare provider
func NewCloudflareProvider(cfg *Config, logger *slog.Logger) *CloudflareProvider {
	return &CloudflareProvider{
		config: cfg,
		logger: logger,
	}
}

// Name returns the provider name
func (p *CloudflareProvider) Name() string {
	return "cloudflare"
}

// IsInstalled checks if cloudflared is installed
func (p *CloudflareProvider) IsInstalled() bool {
	_, ok := CheckCLI(cloudflaredBin)
	return ok
}

// Setup creates or retrieves the tunnel
func (p *CloudflareProvider) Setup(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if logged in
	if err := p.checkAuth(ctx); err != nil {
		p.logger.Info("cloudflared not authenticated, starting login flow")
		if err := p.login(ctx); err != nil {
			return fmt.Errorf("cloudflare login failed: %w", err)
		}
	}

	// Check if tunnel exists
	tunnelID, err := p.getTunnelID(ctx)
	if err != nil {
		// Tunnel doesn't exist, create it
		p.logger.Info("creating cloudflare tunnel", "name", tunnelName)
		tunnelID, err = p.createTunnel(ctx)
		if err != nil {
			return fmt.Errorf("failed to create tunnel: %w", err)
		}
	}

	p.tunnelID = tunnelID
	p.logger.Info("tunnel configured", "id", tunnelID)

	// Create config file
	if err := p.writeConfig(); err != nil {
		return fmt.Errorf("failed to write tunnel config: %w", err)
	}

	// Configure DNS if domain specified
	if p.config.Domain != "" {
		if err := p.configureDNS(ctx); err != nil {
			// Don't fail - DNS might already be configured
			p.logger.Warn("DNS configuration failed", "error", err)
		}
	}

	return nil
}

// Start starts the tunnel
func (p *CloudflareProvider) Start(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		// Already running
		return p.url, nil
	}

	// Ensure we have a tunnel ID
	if p.tunnelID == "" {
		tunnelID, err := p.getTunnelID(ctx)
		if err != nil {
			return "", fmt.Errorf("no tunnel configured, run 'pilot setup --tunnel' first")
		}
		p.tunnelID = tunnelID
	}

	// Start the tunnel process
	p.cmd = exec.CommandContext(ctx, cloudflaredBin, "tunnel", "run", p.tunnelID)

	// Capture output for URL detection
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start cloudflared: %w", err)
	}

	// Wait for connection with timeout
	urlChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			p.logger.Debug("cloudflared stdout", "line", line)
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			p.logger.Debug("cloudflared stderr", "line", line)

			// Look for connection established
			if strings.Contains(line, "Connection") && strings.Contains(line, "registered") {
				// Determine URL
				url := p.determineURL()
				urlChan <- url
				return
			}

			// Look for errors
			if strings.Contains(line, "error") || strings.Contains(line, "failed") {
				errChan <- fmt.Errorf("cloudflared error: %s", line)
				return
			}
		}
	}()

	select {
	case url := <-urlChan:
		p.url = url
		p.logger.Info("cloudflare tunnel connected", "url", url)
		return url, nil
	case err := <-errChan:
		_ = p.Stop()
		return "", err
	case <-time.After(connectionTimeout):
		// Timeout but process is running - assume success
		url := p.determineURL()
		p.url = url
		p.logger.Info("cloudflare tunnel started", "url", url)
		return url, nil
	case <-ctx.Done():
		_ = p.Stop()
		return "", ctx.Err()
	}
}

// Stop stops the tunnel
func (p *CloudflareProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("failed to stop cloudflared: %w", err)
	}

	_ = p.cmd.Wait()
	p.cmd = nil
	p.url = ""

	return nil
}

// Status returns tunnel status
func (p *CloudflareProvider) Status(ctx context.Context) (*Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := &Status{
		Provider: "cloudflare",
		TunnelID: p.tunnelID,
		URL:      p.url,
	}

	// Check if process is running
	if p.cmd != nil && p.cmd.Process != nil {
		status.Running = true
		status.Connected = true
	} else {
		// Check for external cloudflared process
		running, err := p.checkExternalProcess()
		if err == nil && running {
			status.Running = true
			status.Connected = true
			// Get URL from config if not set
			if status.URL == "" {
				status.URL = p.determineURL()
			}
		}
	}

	return status, nil
}

// URL returns the public URL
func (p *CloudflareProvider) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

// checkAuth verifies cloudflared authentication
func (p *CloudflareProvider) checkAuth(ctx context.Context) error {
	_, err := RunCommand(ctx, cloudflaredBin, "tunnel", "list")
	return err
}

// login initiates cloudflared login
func (p *CloudflareProvider) login(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, cloudflaredBin, "tunnel", "login")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// getTunnelID gets the tunnel ID by name
func (p *CloudflareProvider) getTunnelID(ctx context.Context) (string, error) {
	output, err := RunCommand(ctx, cloudflaredBin, "tunnel", "list", "--output", "json")
	if err != nil {
		return "", err
	}

	var tunnels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	if err := json.Unmarshal([]byte(output), &tunnels); err != nil {
		return "", fmt.Errorf("failed to parse tunnel list: %w", err)
	}

	for _, t := range tunnels {
		if t.Name == tunnelName {
			return t.ID, nil
		}
	}

	return "", fmt.Errorf("tunnel %q not found", tunnelName)
}

// createTunnel creates a new tunnel
func (p *CloudflareProvider) createTunnel(ctx context.Context) (string, error) {
	output, err := RunCommand(ctx, cloudflaredBin, "tunnel", "create", tunnelName)
	if err != nil {
		return "", err
	}

	// Parse tunnel ID from output
	// Output format: "Created tunnel pilot-webhook with id <uuid>"
	parts := strings.Fields(output)
	for i, part := range parts {
		if part == "id" && i+1 < len(parts) {
			return parts[i+1], nil
		}
	}

	// Try to get it from list
	return p.getTunnelID(ctx)
}

// writeConfig writes the tunnel config file
func (p *CloudflareProvider) writeConfig() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	configDir := filepath.Join(homeDir, ".cloudflared")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	port := p.config.Port
	if port == 0 {
		port = defaultTunnelPort
	}

	// Build config
	config := map[string]any{
		"tunnel": p.tunnelID,
		"ingress": []map[string]any{
			{
				"hostname": p.getHostname(),
				"service":  fmt.Sprintf("http://localhost:%d", port),
			},
			{
				"service": "http_status:404",
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	configPath := filepath.Join(configDir, "config.yml")

	// Write as YAML (JSON is valid YAML)
	return os.WriteFile(configPath, data, 0644)
}

// configureDNS routes DNS to the tunnel
func (p *CloudflareProvider) configureDNS(ctx context.Context) error {
	if p.config.Domain == "" {
		return nil
	}

	_, err := RunCommand(ctx, cloudflaredBin, "tunnel", "route", "dns", p.tunnelID, p.config.Domain)
	return err
}

// determineURL constructs the tunnel URL
func (p *CloudflareProvider) determineURL() string {
	if p.config.Domain != "" {
		return fmt.Sprintf("https://%s", p.config.Domain)
	}
	if p.tunnelID != "" {
		return fmt.Sprintf("https://%s.cfargotunnel.com", p.tunnelID)
	}
	return ""
}

// getHostname returns the hostname for config
func (p *CloudflareProvider) getHostname() string {
	if p.config.Domain != "" {
		return p.config.Domain
	}
	return "*"
}

// checkExternalProcess checks if cloudflared is running externally
func (p *CloudflareProvider) checkExternalProcess() (bool, error) {
	output, err := exec.Command("pgrep", "-f", "cloudflared tunnel").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil // No process found
		}
		return false, err
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// GetTunnelID returns the tunnel ID
func (p *CloudflareProvider) GetTunnelID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tunnelID
}
