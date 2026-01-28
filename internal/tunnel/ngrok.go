package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

const (
	ngrokBin         = "ngrok"
	ngrokAPIEndpoint = "http://127.0.0.1:4040/api/tunnels"
)

// NgrokProvider implements the ngrok tunnel provider
type NgrokProvider struct {
	config *Config
	logger *slog.Logger
	cmd    *exec.Cmd
	url    string
	mu     sync.Mutex
}

// NewNgrokProvider creates a new ngrok provider
func NewNgrokProvider(cfg *Config, logger *slog.Logger) *NgrokProvider {
	return &NgrokProvider{
		config: cfg,
		logger: logger,
	}
}

// Name returns the provider name
func (p *NgrokProvider) Name() string {
	return "ngrok"
}

// IsInstalled checks if ngrok is installed
func (p *NgrokProvider) IsInstalled() bool {
	_, ok := CheckCLI(ngrokBin)
	return ok
}

// Setup validates ngrok configuration
func (p *NgrokProvider) Setup(ctx context.Context) error {
	// ngrok doesn't require explicit setup - just check auth
	output, err := RunCommand(ctx, ngrokBin, "config", "check")
	if err != nil {
		p.logger.Warn("ngrok config check failed", "error", err)
		return fmt.Errorf("ngrok not configured - run 'ngrok authtoken <token>' first")
	}
	p.logger.Info("ngrok configured", "status", output)
	return nil
}

// Start starts the ngrok tunnel
func (p *NgrokProvider) Start(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		return p.url, nil
	}

	port := p.config.Port
	if port == 0 {
		port = defaultTunnelPort
	}

	// Start ngrok
	args := []string{"http", fmt.Sprintf("%d", port)}

	// Add custom domain if specified
	if p.config.Domain != "" {
		args = append(args, "--domain", p.config.Domain)
	}

	p.cmd = exec.CommandContext(ctx, ngrokBin, args...)

	// Capture stderr for logging
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start ngrok: %w", err)
	}

	// Log stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			p.logger.Debug("ngrok", "line", scanner.Text())
		}
	}()

	// Wait for tunnel to be ready
	url, err := p.waitForURL(ctx)
	if err != nil {
		_ = p.Stop()
		return "", err
	}

	p.url = url
	p.logger.Info("ngrok tunnel started", "url", url)

	return url, nil
}

// Stop stops the ngrok tunnel
func (p *NgrokProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("failed to stop ngrok: %w", err)
	}

	_ = p.cmd.Wait()
	p.cmd = nil
	p.url = ""

	return nil
}

// Status returns tunnel status
func (p *NgrokProvider) Status(ctx context.Context) (*Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := &Status{
		Provider: "ngrok",
		URL:      p.url,
	}

	// Check if process is running
	if p.cmd != nil && p.cmd.Process != nil {
		status.Running = true
		status.Connected = true
	} else {
		// Check ngrok API for external instance
		if url, err := p.getURLFromAPI(); err == nil && url != "" {
			status.Running = true
			status.Connected = true
			status.URL = url
		}
	}

	return status, nil
}

// URL returns the public URL
func (p *NgrokProvider) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

// waitForURL polls the ngrok API for the tunnel URL
func (p *NgrokProvider) waitForURL(ctx context.Context) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-ticker.C:
			url, err := p.getURLFromAPI()
			if err == nil && url != "" {
				return url, nil
			}
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for ngrok tunnel URL")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// getURLFromAPI retrieves the tunnel URL from ngrok's local API
func (p *NgrokProvider) getURLFromAPI() (string, error) {
	resp, err := http.Get(ngrokAPIEndpoint)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
			Proto     string `json:"proto"`
		} `json:"tunnels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	// Prefer HTTPS tunnel
	for _, t := range result.Tunnels {
		if t.Proto == "https" {
			return t.PublicURL, nil
		}
	}

	// Fall back to any tunnel
	if len(result.Tunnels) > 0 {
		return result.Tunnels[0].PublicURL, nil
	}

	return "", fmt.Errorf("no tunnels found")
}
