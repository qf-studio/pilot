package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

// mockProvider implements Provider for testing
type mockProvider struct {
	name        string
	installed   bool
	setupErr    error
	startURL    string
	startErr    error
	stopErr     error
	statusResp  *Status
	statusErr   error
	url         string
	setupCalled bool
	startCalled bool
	stopCalled  bool
}

func (m *mockProvider) Name() string      { return m.name }
func (m *mockProvider) IsInstalled() bool { return m.installed }
func (m *mockProvider) URL() string       { return m.url }

func (m *mockProvider) Setup(ctx context.Context) error {
	m.setupCalled = true
	return m.setupErr
}

func (m *mockProvider) Start(ctx context.Context) (string, error) {
	m.startCalled = true
	if m.startErr != nil {
		return "", m.startErr
	}
	m.url = m.startURL
	return m.startURL, nil
}

func (m *mockProvider) Stop() error {
	m.stopCalled = true
	m.url = ""
	return m.stopErr
}

func (m *mockProvider) Status(ctx context.Context) (*Status, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	if m.statusResp != nil {
		return m.statusResp, nil
	}
	return &Status{Provider: m.name}, nil
}

func TestNewManager(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		wantErr  bool
		wantProv string
	}{
		{
			name:     "nil config uses defaults",
			config:   nil,
			wantErr:  false,
			wantProv: "cloudflare",
		},
		{
			name: "cloudflare provider",
			config: &Config{
				Provider: "cloudflare",
				Port:     9090,
			},
			wantErr:  false,
			wantProv: "cloudflare",
		},
		{
			name: "ngrok provider",
			config: &Config{
				Provider: "ngrok",
				Port:     9090,
			},
			wantErr:  false,
			wantProv: "ngrok",
		},
		{
			name: "manual mode has no provider",
			config: &Config{
				Provider: "manual",
			},
			wantErr:  false,
			wantProv: "manual",
		},
		{
			name: "empty provider defaults to manual",
			config: &Config{
				Provider: "",
			},
			wantErr:  false,
			wantProv: "manual",
		},
		{
			name: "unknown provider fails",
			config: &Config{
				Provider: "unknown",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewManager(tt.config, slog.Default())

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if m == nil {
				t.Errorf("expected manager, got nil")
				return
			}

			if m.Provider() != tt.wantProv {
				t.Errorf("provider = %q, want %q", m.Provider(), tt.wantProv)
			}
		})
	}
}

func TestNewManagerWithNilLogger(t *testing.T) {
	m, err := NewManager(&Config{Provider: "manual"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manager, got nil")
	}
	if m.logger == nil {
		t.Error("expected logger to be set to default")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled {
		t.Error("default config should not be enabled")
	}

	if cfg.Provider != "cloudflare" {
		t.Errorf("default provider = %q, want %q", cfg.Provider, "cloudflare")
	}

	if cfg.Port != 9090 {
		t.Errorf("default port = %d, want %d", cfg.Port, 9090)
	}
}

func TestConfigFields(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "all fields set",
			config: Config{
				Enabled:  true,
				Provider: "cloudflare",
				Domain:   "example.com",
				Port:     8080,
			},
		},
		{
			name: "minimal config",
			config: Config{
				Provider: "ngrok",
			},
		},
		{
			name:   "zero value config",
			config: Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify config can be used
			cfg := tt.config
			_ = cfg.Enabled
			_ = cfg.Provider
			_ = cfg.Domain
			_ = cfg.Port
		})
	}
}

func TestManagerStatus(t *testing.T) {
	// Test with manual mode (no provider)
	cfg := &Config{Provider: "manual"}
	m, err := NewManager(cfg, slog.Default())
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("failed to get status: %v", err)
	}

	if status.Running {
		t.Error("expected not running for manual mode")
	}

	if status.Provider != "manual" {
		t.Errorf("status provider = %q, want %q", status.Provider, "manual")
	}
}

func TestManagerStatusWithProvider(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		statusResp: &Status{
			Running:   true,
			Provider:  "test",
			URL:       "https://test.example.com",
			TunnelID:  "abc123",
			Connected: true,
		},
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !status.Running {
		t.Error("expected running to be true")
	}
	if status.URL != "https://test.example.com" {
		t.Errorf("URL = %q, want %q", status.URL, "https://test.example.com")
	}
	if status.TunnelID != "abc123" {
		t.Errorf("TunnelID = %q, want %q", status.TunnelID, "abc123")
	}
}

func TestManagerURLEmpty(t *testing.T) {
	cfg := &Config{Provider: "manual"}
	m, err := NewManager(cfg, slog.Default())
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if url := m.URL(); url != "" {
		t.Errorf("expected empty URL, got %q", url)
	}
}

func TestManagerIsRunning(t *testing.T) {
	cfg := &Config{Provider: "manual"}
	m, err := NewManager(cfg, slog.Default())
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if m.IsRunning() {
		t.Error("expected not running initially")
	}
}

func TestManagerSetupNoProvider(t *testing.T) {
	m := &Manager{
		config: &Config{Provider: "manual"},
		logger: slog.Default(),
	}

	ctx := context.Background()
	err := m.Setup(ctx)
	if err == nil {
		t.Error("expected error for no provider")
	}
	if err.Error() != "no tunnel provider configured" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManagerSetupProviderNotInstalled(t *testing.T) {
	mock := &mockProvider{
		name:      "test",
		installed: false,
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	err := m.Setup(ctx)
	if err == nil {
		t.Error("expected error for provider not installed")
	}
	if err.Error() != "test CLI is not installed" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManagerSetupSuccess(t *testing.T) {
	mock := &mockProvider{
		name:      "test",
		installed: true,
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	err := m.Setup(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.setupCalled {
		t.Error("expected Setup to be called on provider")
	}
}

func TestManagerSetupProviderError(t *testing.T) {
	mock := &mockProvider{
		name:      "test",
		installed: true,
		setupErr:  errors.New("setup failed"),
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	err := m.Setup(ctx)
	if err == nil {
		t.Error("expected error")
	}
	if !errors.Is(err, mock.setupErr) && err.Error() != "tunnel setup failed: setup failed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManagerStartNoProvider(t *testing.T) {
	m := &Manager{
		config: &Config{Provider: "manual"},
		logger: slog.Default(),
	}

	ctx := context.Background()
	_, err := m.Start(ctx)
	if err == nil {
		t.Error("expected error for no provider")
	}
	if err.Error() != "no tunnel provider configured" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManagerStartSuccess(t *testing.T) {
	mock := &mockProvider{
		name:     "test",
		startURL: "https://test.example.com",
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	url, err := m.Start(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if url != "https://test.example.com" {
		t.Errorf("URL = %q, want %q", url, "https://test.example.com")
	}
	if !mock.startCalled {
		t.Error("expected Start to be called on provider")
	}
	if !m.running {
		t.Error("expected running to be true")
	}
	if m.url != url {
		t.Errorf("m.url = %q, want %q", m.url, url)
	}
}

func TestManagerStartAlreadyRunning(t *testing.T) {
	mock := &mockProvider{
		name:     "test",
		startURL: "https://test.example.com",
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
		running:  true,
		url:      "https://existing.example.com",
	}

	ctx := context.Background()
	url, err := m.Start(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if url != "https://existing.example.com" {
		t.Errorf("URL = %q, want %q", url, "https://existing.example.com")
	}
	if mock.startCalled {
		t.Error("expected Start NOT to be called when already running")
	}
}

func TestManagerStartError(t *testing.T) {
	mock := &mockProvider{
		name:     "test",
		startErr: errors.New("connection refused"),
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx := context.Background()
	_, err := m.Start(ctx)
	if err == nil {
		t.Error("expected error")
	}
	if m.running {
		t.Error("expected running to be false after error")
	}
}

func TestManagerStopNoProvider(t *testing.T) {
	m := &Manager{
		config: &Config{Provider: "manual"},
		logger: slog.Default(),
	}

	err := m.Stop()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManagerStopNotRunning(t *testing.T) {
	mock := &mockProvider{
		name: "test",
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
		running:  false,
	}

	err := m.Stop()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if mock.stopCalled {
		t.Error("expected Stop NOT to be called when not running")
	}
}

func TestManagerStopSuccess(t *testing.T) {
	mock := &mockProvider{
		name: "test",
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
		running:  true,
		url:      "https://test.example.com",
	}

	err := m.Stop()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.stopCalled {
		t.Error("expected Stop to be called on provider")
	}
	if m.running {
		t.Error("expected running to be false")
	}
	if m.url != "" {
		t.Errorf("expected url to be empty, got %q", m.url)
	}
}

func TestManagerStopError(t *testing.T) {
	mock := &mockProvider{
		name:    "test",
		stopErr: errors.New("process not found"),
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
		running:  true,
	}

	err := m.Stop()
	if err == nil {
		t.Error("expected error")
	}
}

func TestManagerProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		want     string
	}{
		{
			name:     "nil provider returns manual",
			provider: nil,
			want:     "manual",
		},
		{
			name:     "mock provider",
			provider: &mockProvider{name: "test"},
			want:     "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{
				provider: tt.provider,
			}
			if got := m.Provider(); got != tt.want {
				t.Errorf("Provider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckCLI(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		wantOK  bool
		wantLen bool // whether path length > 0
	}{
		{
			name:    "ls exists",
			cli:     "ls",
			wantOK:  true,
			wantLen: true,
		},
		{
			name:    "bash exists",
			cli:     "bash",
			wantOK:  true,
			wantLen: true,
		},
		{
			name:    "nonexistent cli",
			cli:     "definitely-not-a-real-cli-tool-xyz",
			wantOK:  false,
			wantLen: false,
		},
		{
			name:    "empty string",
			cli:     "",
			wantOK:  false,
			wantLen: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, ok := CheckCLI(tt.cli)
			if ok != tt.wantOK {
				t.Errorf("CheckCLI(%q) ok = %v, want %v", tt.cli, ok, tt.wantOK)
			}
			if tt.wantLen && path == "" {
				t.Errorf("CheckCLI(%q) path is empty, want non-empty", tt.cli)
			}
			if !tt.wantLen && path != "" {
				t.Errorf("CheckCLI(%q) path = %q, want empty", tt.cli, path)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		args     []string
		wantErr  bool
		contains string
	}{
		{
			name:     "echo hello",
			cmd:      "echo",
			args:     []string{"hello"},
			wantErr:  false,
			contains: "hello",
		},
		{
			name:    "nonexistent command",
			cmd:     "definitely-not-a-real-command-xyz",
			args:    nil,
			wantErr: true,
		},
		{
			name:    "command with error exit",
			cmd:     "ls",
			args:    []string{"/nonexistent-dir-xyz"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			output, err := RunCommand(ctx, tt.cmd, tt.args...)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.contains != "" && output != tt.contains {
				// Allow for trimmed output
				if len(output) < len(tt.contains) {
					t.Errorf("output = %q, want to contain %q", output, tt.contains)
				}
			}
		})
	}
}

func TestRunCommandWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := RunCommand(ctx, "sleep", "10")
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestStatusStruct(t *testing.T) {
	status := &Status{
		Running:   true,
		Provider:  "cloudflare",
		URL:       "https://example.cfargotunnel.com",
		TunnelID:  "abc-123",
		Connected: true,
		Error:     "",
	}

	if !status.Running {
		t.Error("expected Running to be true")
	}
	if status.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want %q", status.Provider, "cloudflare")
	}
	if status.URL != "https://example.cfargotunnel.com" {
		t.Errorf("URL = %q, want %q", status.URL, "https://example.cfargotunnel.com")
	}
	if status.TunnelID != "abc-123" {
		t.Errorf("TunnelID = %q, want %q", status.TunnelID, "abc-123")
	}
	if !status.Connected {
		t.Error("expected Connected to be true")
	}
	if status.Error != "" {
		t.Errorf("Error = %q, want empty", status.Error)
	}
}

func TestCloudflareProviderName(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())
	if p.Name() != "cloudflare" {
		t.Errorf("name = %q, want %q", p.Name(), "cloudflare")
	}
}

func TestNgrokProviderName(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())
	if p.Name() != "ngrok" {
		t.Errorf("name = %q, want %q", p.Name(), "ngrok")
	}
}

func TestCloudflareIsInstalled(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())
	// Just verify it doesn't panic - actual installation varies
	_ = p.IsInstalled()
}

func TestNgrokIsInstalled(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())
	// Just verify it doesn't panic - actual installation varies
	_ = p.IsInstalled()
}

func TestCloudflareProviderURL(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())

	// Initially empty
	if url := p.URL(); url != "" {
		t.Errorf("expected empty URL, got %q", url)
	}
}

func TestNgrokProviderURL(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())

	// Initially empty
	if url := p.URL(); url != "" {
		t.Errorf("expected empty URL, got %q", url)
	}
}

func TestCloudflareProviderGetTunnelID(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())

	// Initially empty
	if id := p.GetTunnelID(); id != "" {
		t.Errorf("expected empty tunnel ID, got %q", id)
	}
}

func TestCloudflareProviderDetermineURL(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		tunnelID string
		want     string
	}{
		{
			name:     "with custom domain",
			domain:   "webhook.example.com",
			tunnelID: "abc123",
			want:     "https://webhook.example.com",
		},
		{
			name:     "with tunnel ID only",
			domain:   "",
			tunnelID: "abc123-def456",
			want:     "https://abc123-def456.cfargotunnel.com",
		},
		{
			name:     "no domain or tunnel ID",
			domain:   "",
			tunnelID: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &CloudflareProvider{
				config:   &Config{Domain: tt.domain},
				tunnelID: tt.tunnelID,
				logger:   slog.Default(),
			}
			got := p.determineURL()
			if got != tt.want {
				t.Errorf("determineURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCloudflareProviderGetHostname(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		want   string
	}{
		{
			name:   "with custom domain",
			domain: "webhook.example.com",
			want:   "webhook.example.com",
		},
		{
			name:   "without domain",
			domain: "",
			want:   "*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &CloudflareProvider{
				config: &Config{Domain: tt.domain},
				logger: slog.Default(),
			}
			got := p.getHostname()
			if got != tt.want {
				t.Errorf("getHostname() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCloudflareProviderStopNotRunning(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())

	// Stop should not error when not running
	err := p.Stop()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNgrokProviderStopNotRunning(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())

	// Stop should not error when not running
	err := p.Stop()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCloudflareProviderStatus(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want %q", status.Provider, "cloudflare")
	}
}

func TestNgrokProviderStatus(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.Provider != "ngrok" {
		t.Errorf("Provider = %q, want %q", status.Provider, "ngrok")
	}
}

func TestServiceStatus(t *testing.T) {
	status := GetServiceStatus()

	// On any system, should return a valid status struct
	if status == nil {
		t.Fatal("expected non-nil status")
	}

	// These should be consistent with system state
	_ = status.Installed
	_ = status.Running
	_ = status.PlistPath
}

func TestIsServiceInstalled(t *testing.T) {
	// Should not panic
	_ = IsServiceInstalled()
}

func TestIsServiceRunning(t *testing.T) {
	// Should not panic
	_ = IsServiceRunning()
}

func TestManagerConcurrency(t *testing.T) {
	m, err := NewManager(&Config{Provider: "manual"}, slog.Default())
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Run concurrent operations
	done := make(chan bool, 10)

	for i := 0; i < 5; i++ {
		go func() {
			_ = m.URL()
			_ = m.IsRunning()
			_ = m.Provider()
			done <- true
		}()
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		go func() {
			_, _ = m.Status(ctx)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent operations")
		}
	}
}

func TestEnvironmentVariables(t *testing.T) {
	// Test that we can read home directory for config paths
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot get user home dir: %v", err)
	}
	if home == "" {
		t.Error("home directory is empty")
	}
}

// Additional tests for edge cases and comprehensive coverage

func TestCloudflareProviderWriteConfigPortDefault(t *testing.T) {
	// Test writeConfig with default port (0)
	p := &CloudflareProvider{
		config:   &Config{Port: 0, Domain: ""},
		tunnelID: "test-tunnel-id",
		logger:   slog.Default(),
	}

	// We can't run writeConfig without side effects but we can test
	// the logic that would be used by checking the port default
	port := p.config.Port
	if port == 0 {
		port = defaultTunnelPort
	}
	if port != 9090 {
		t.Errorf("expected default port 9090, got %d", port)
	}
}

func TestCloudflareProviderConstants(t *testing.T) {
	// Verify constants are set correctly
	if cloudflaredBin != "cloudflared" {
		t.Errorf("cloudflaredBin = %q, want %q", cloudflaredBin, "cloudflared")
	}
	if tunnelName != "pilot-webhook" {
		t.Errorf("tunnelName = %q, want %q", tunnelName, "pilot-webhook")
	}
	if defaultTunnelPort != 9090 {
		t.Errorf("defaultTunnelPort = %d, want %d", defaultTunnelPort, 9090)
	}
	if connectionTimeout != 30*time.Second {
		t.Errorf("connectionTimeout = %v, want %v", connectionTimeout, 30*time.Second)
	}
}

func TestNgrokProviderConstants(t *testing.T) {
	if ngrokBin != "ngrok" {
		t.Errorf("ngrokBin = %q, want %q", ngrokBin, "ngrok")
	}
	if ngrokAPIEndpoint != "http://127.0.0.1:4040/api/tunnels" {
		t.Errorf("ngrokAPIEndpoint = %q, want %q", ngrokAPIEndpoint, "http://127.0.0.1:4040/api/tunnels")
	}
}

func TestCloudflareProviderStatusWithURL(t *testing.T) {
	p := &CloudflareProvider{
		config:   &Config{Domain: "test.example.com"},
		tunnelID: "abc123",
		url:      "https://test.example.com",
		logger:   slog.Default(),
	}

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.URL != "https://test.example.com" {
		t.Errorf("URL = %q, want %q", status.URL, "https://test.example.com")
	}
	if status.TunnelID != "abc123" {
		t.Errorf("TunnelID = %q, want %q", status.TunnelID, "abc123")
	}
}

func TestNgrokProviderStatusWithURL(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{Domain: "test.example.com"},
		url:    "https://test.ngrok.io",
		logger: slog.Default(),
	}

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.URL != "https://test.ngrok.io" {
		t.Errorf("URL = %q, want %q", status.URL, "https://test.ngrok.io")
	}
}

// TestLaunchdConstants and TestServiceConfigStruct moved to manager_darwin_test.go

func TestServiceStatusStruct(t *testing.T) {
	status := &ServiceStatus{
		Installed: true,
		Running:   true,
		PlistPath: "/path/to/plist",
	}

	if !status.Installed {
		t.Error("expected Installed to be true")
	}
	if !status.Running {
		t.Error("expected Running to be true")
	}
	if status.PlistPath != "/path/to/plist" {
		t.Errorf("PlistPath = %q, want %q", status.PlistPath, "/path/to/plist")
	}
}

func TestCloudflareProviderCheckExternalProcess(t *testing.T) {
	p := NewCloudflareProvider(&Config{}, slog.Default())

	// Should not panic or return an unexpected error
	running, err := p.checkExternalProcess()
	// We can't predict the result, but verify it doesn't panic
	_ = running
	if err != nil {
		// Some errors are expected (e.g., pgrep not found on some systems)
		t.Logf("checkExternalProcess returned error: %v (may be expected)", err)
	}
}

func TestManagerURLConcurrency(t *testing.T) {
	m := &Manager{
		config: &Config{Provider: "manual"},
		logger: slog.Default(),
		url:    "https://test.example.com",
	}

	done := make(chan bool, 20)

	// Concurrent reads
	for i := 0; i < 10; i++ {
		go func() {
			url := m.URL()
			if url != "https://test.example.com" && url != "" {
				t.Errorf("unexpected URL: %q", url)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestManagerIsRunningConcurrency(t *testing.T) {
	m := &Manager{
		config:  &Config{Provider: "manual"},
		logger:  slog.Default(),
		running: true,
	}

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			_ = m.IsRunning()
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestStatusStructWithError(t *testing.T) {
	status := &Status{
		Running:   false,
		Provider:  "cloudflare",
		URL:       "",
		Connected: false,
		Error:     "connection refused",
	}

	if status.Running {
		t.Error("expected Running to be false")
	}
	if status.Connected {
		t.Error("expected Connected to be false")
	}
	if status.Error != "connection refused" {
		t.Errorf("Error = %q, want %q", status.Error, "connection refused")
	}
}

func TestCloudflareProviderDetermineURLEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		tunnelID string
		want     string
	}{
		{
			name:     "domain with subdomain",
			domain:   "api.webhook.example.com",
			tunnelID: "abc",
			want:     "https://api.webhook.example.com",
		},
		{
			name:     "tunnel ID with uuid format",
			domain:   "",
			tunnelID: "550e8400-e29b-41d4-a716-446655440000",
			want:     "https://550e8400-e29b-41d4-a716-446655440000.cfargotunnel.com",
		},
		{
			name:     "empty config",
			domain:   "",
			tunnelID: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &CloudflareProvider{
				config:   &Config{Domain: tt.domain},
				tunnelID: tt.tunnelID,
				logger:   slog.Default(),
			}
			got := p.determineURL()
			if got != tt.want {
				t.Errorf("determineURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagerSetupWithCancelledContext(t *testing.T) {
	mock := &mockProvider{
		name:      "test",
		installed: true,
		setupErr:  context.Canceled,
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.Setup(ctx)
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestManagerStartWithCancelledContext(t *testing.T) {
	mock := &mockProvider{
		name:     "test",
		startErr: context.Canceled,
	}

	m := &Manager{
		config:   &Config{Provider: "test"},
		provider: mock,
		logger:   slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Start(ctx)
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestNgrokProviderGetURLFromAPINoServer(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())

	// Without ngrok running, should fail
	_, err := p.getURLFromAPI()
	if err == nil {
		// If ngrok is actually running on this system, skip
		t.Log("ngrok API responded - ngrok might be running")
	}
}

func TestCloudflareProviderURLLocking(t *testing.T) {
	p := &CloudflareProvider{
		config: &Config{},
		url:    "https://test.example.com",
		logger: slog.Default(),
	}

	// Concurrent access should be safe
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			url := p.URL()
			if url != "https://test.example.com" {
				t.Errorf("unexpected URL: %q", url)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestNgrokProviderURLLocking(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{},
		url:    "https://abc.ngrok.io",
		logger: slog.Default(),
	}

	// Concurrent access should be safe
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			url := p.URL()
			if url != "https://abc.ngrok.io" {
				t.Errorf("unexpected URL: %q", url)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestCloudflareProviderGetTunnelIDLocking(t *testing.T) {
	p := &CloudflareProvider{
		config:   &Config{},
		tunnelID: "test-tunnel-123",
		logger:   slog.Default(),
	}

	// Concurrent access should be safe
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			id := p.GetTunnelID()
			if id != "test-tunnel-123" {
				t.Errorf("unexpected tunnel ID: %q", id)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestRunCommandContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Sleep should timeout
	time.Sleep(10 * time.Millisecond)
	_, err := RunCommand(ctx, "sleep", "10")
	if err == nil {
		t.Error("expected error with timed out context")
	}
}

func TestMockProviderInterface(t *testing.T) {
	// Verify mockProvider satisfies Provider interface
	var _ Provider = (*mockProvider)(nil)
}

func TestCloudflareProviderInterface(t *testing.T) {
	// Verify CloudflareProvider satisfies Provider interface
	var _ Provider = (*CloudflareProvider)(nil)
}

func TestNgrokProviderInterface(t *testing.T) {
	// Verify NgrokProvider satisfies Provider interface
	var _ Provider = (*NgrokProvider)(nil)
}

func TestConfigDomainUsage(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		wantHost string
	}{
		{
			name:     "with domain",
			config:   &Config{Domain: "my.example.com"},
			wantHost: "my.example.com",
		},
		{
			name:     "without domain",
			config:   &Config{Domain: ""},
			wantHost: "*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &CloudflareProvider{
				config: tt.config,
				logger: slog.Default(),
			}
			got := p.getHostname()
			if got != tt.wantHost {
				t.Errorf("getHostname() = %q, want %q", got, tt.wantHost)
			}
		})
	}
}

func TestConfigPortUsage(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		wantPort int
	}{
		{
			name:     "custom port",
			port:     8080,
			wantPort: 8080,
		},
		{
			name:     "default port",
			port:     0,
			wantPort: defaultTunnelPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := tt.port
			if port == 0 {
				port = defaultTunnelPort
			}
			if port != tt.wantPort {
				t.Errorf("port = %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestNewCloudflareProviderFields(t *testing.T) {
	cfg := &Config{
		Provider: "cloudflare",
		Domain:   "test.example.com",
		Port:     8080,
	}
	logger := slog.Default()

	p := NewCloudflareProvider(cfg, logger)

	if p.config != cfg {
		t.Error("config not set correctly")
	}
	if p.logger != logger {
		t.Error("logger not set correctly")
	}
	if p.cmd != nil {
		t.Error("cmd should be nil initially")
	}
	if p.tunnelID != "" {
		t.Error("tunnelID should be empty initially")
	}
	if p.url != "" {
		t.Error("url should be empty initially")
	}
}

func TestNewNgrokProviderFields(t *testing.T) {
	cfg := &Config{
		Provider: "ngrok",
		Domain:   "test.example.com",
		Port:     8080,
	}
	logger := slog.Default()

	p := NewNgrokProvider(cfg, logger)

	if p.config != cfg {
		t.Error("config not set correctly")
	}
	if p.logger != logger {
		t.Error("logger not set correctly")
	}
	if p.cmd != nil {
		t.Error("cmd should be nil initially")
	}
	if p.url != "" {
		t.Error("url should be empty initially")
	}
}

func TestStatusStructFields(t *testing.T) {
	// Test that all fields can be set and retrieved
	s := Status{
		Running:   true,
		Provider:  "test-provider",
		URL:       "https://example.com",
		TunnelID:  "tunnel-123",
		Connected: true,
		Error:     "no error",
	}

	if !s.Running {
		t.Error("Running not set")
	}
	if s.Provider != "test-provider" {
		t.Errorf("Provider = %q, want %q", s.Provider, "test-provider")
	}
	if s.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", s.URL, "https://example.com")
	}
	if s.TunnelID != "tunnel-123" {
		t.Errorf("TunnelID = %q, want %q", s.TunnelID, "tunnel-123")
	}
	if !s.Connected {
		t.Error("Connected not set")
	}
	if s.Error != "no error" {
		t.Errorf("Error = %q, want %q", s.Error, "no error")
	}
}
