package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
)

// Provider defines the tunnel provider interface
type Provider interface {
	// Name returns the provider name
	Name() string

	// IsInstalled checks if the provider CLI is installed
	IsInstalled() bool

	// Setup performs initial setup (create tunnel, configure DNS)
	Setup(ctx context.Context) error

	// Start starts the tunnel and returns the public URL
	Start(ctx context.Context) (string, error)

	// Stop stops the tunnel
	Stop() error

	// Status returns the tunnel status
	Status(ctx context.Context) (*Status, error)

	// URL returns the public URL (if known)
	URL() string
}

// Status represents tunnel status
type Status struct {
	Running   bool   `json:"running"`
	Provider  string `json:"provider"`
	URL       string `json:"url"`
	TunnelID  string `json:"tunnel_id,omitempty"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// Manager manages tunnel lifecycle
type Manager struct {
	config   *Config
	provider Provider
	logger   *slog.Logger
	mu       sync.Mutex

	running bool
	url     string
}

// NewManager creates a new tunnel manager
func NewManager(cfg *Config, logger *slog.Logger) (*Manager, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if logger == nil {
		logger = slog.Default()
	}

	m := &Manager{
		config: cfg,
		logger: logger,
	}

	// Initialize provider
	switch cfg.Provider {
	case "cloudflare":
		m.provider = NewCloudflareProvider(cfg, logger)
	case "ngrok":
		m.provider = NewNgrokProvider(cfg, logger)
	case "manual", "":
		// No provider needed for manual mode
		return m, nil
	default:
		return nil, fmt.Errorf("unknown tunnel provider: %s", cfg.Provider)
	}

	return m, nil
}

// Setup performs initial tunnel setup
func (m *Manager) Setup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.provider == nil {
		return errors.New("no tunnel provider configured")
	}

	// Check if provider CLI is installed
	if !m.provider.IsInstalled() {
		return fmt.Errorf("%s CLI is not installed", m.provider.Name())
	}

	m.logger.Info("setting up tunnel", "provider", m.provider.Name())

	if err := m.provider.Setup(ctx); err != nil {
		return fmt.Errorf("tunnel setup failed: %w", err)
	}

	m.logger.Info("tunnel setup complete", "provider", m.provider.Name())
	return nil
}

// Start starts the tunnel
func (m *Manager) Start(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.provider == nil {
		return "", errors.New("no tunnel provider configured")
	}

	if m.running {
		return m.url, nil
	}

	m.logger.Info("starting tunnel", "provider", m.provider.Name())

	url, err := m.provider.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to start tunnel: %w", err)
	}

	m.running = true
	m.url = url

	m.logger.Info("tunnel started", "url", url)
	return url, nil
}

// Stop stops the tunnel
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.provider == nil || !m.running {
		return nil
	}

	m.logger.Info("stopping tunnel", "provider", m.provider.Name())

	if err := m.provider.Stop(); err != nil {
		return fmt.Errorf("failed to stop tunnel: %w", err)
	}

	m.running = false
	m.url = ""

	m.logger.Info("tunnel stopped")
	return nil
}

// Status returns the tunnel status
func (m *Manager) Status(ctx context.Context) (*Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.provider == nil {
		return &Status{
			Running:  false,
			Provider: "manual",
		}, nil
	}

	return m.provider.Status(ctx)
}

// URL returns the current tunnel URL
func (m *Manager) URL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.url
}

// IsRunning returns whether the tunnel is running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Provider returns the current provider name
func (m *Manager) Provider() string {
	if m.provider == nil {
		return "manual"
	}
	return m.provider.Name()
}

// CheckCLI checks if a CLI tool is available
func CheckCLI(name string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

// RunCommand runs a command and returns output
func RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
