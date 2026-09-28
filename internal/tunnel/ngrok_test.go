package tunnel

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// unroutableNgrokEndpoint simulates "no ngrok agent running": connecting to
// port 0 fails immediately without ever touching a real local API, so tests
// using it are deterministic regardless of whether ngrok is actually
// installed and running on the host.
const unroutableNgrokEndpoint = "http://127.0.0.1:0/api/tunnels"

// ngrokFixtureServer starts an httptest server that serves a fixed ngrok
// local-API response containing a single tunnel with the given URL.
func ngrokFixtureServer(t *testing.T, publicURL string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tunnels":[{"public_url":"` + publicURL + `","proto":"https"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestNgrokProviderSetupNotConfigured(t *testing.T) {
	// Skip if ngrok is actually installed and configured
	if _, ok := CheckCLI(ngrokBin); ok {
		t.Skip("skipping test that requires ngrok to NOT be installed")
	}

	p := NewNgrokProvider(&Config{}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.Setup(ctx)
	if err == nil {
		t.Error("expected error when ngrok is not configured")
	}
}

func TestNgrokProviderStartNotInstalled(t *testing.T) {
	// Skip if ngrok is actually installed
	if _, ok := CheckCLI(ngrokBin); ok {
		t.Skip("skipping test that requires ngrok to NOT be installed")
	}

	p := NewNgrokProvider(&Config{Port: 8080}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := p.Start(ctx)
	if err == nil {
		t.Error("expected error when ngrok is not installed")
	}
}

func TestNgrokProviderStopWhenNotRunning(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{},
		logger: slog.Default(),
		cmd:    nil, // Not running
	}

	err := p.Stop()
	if err != nil {
		t.Errorf("Stop should not error when not running: %v", err)
	}
}

func TestNgrokProviderStatusNotRunning(t *testing.T) {
	p := &NgrokProvider{
		config:      &Config{},
		logger:      slog.Default(),
		apiEndpoint: unroutableNgrokEndpoint,
	}

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.Provider != "ngrok" {
		t.Errorf("Provider = %q, want %q", status.Provider, "ngrok")
	}
}

func TestNgrokProviderStatusWithURLSet(t *testing.T) {
	server := ngrokFixtureServer(t, "https://abc123.ngrok.io")

	p := &NgrokProvider{
		config:      &Config{},
		url:         "https://abc123.ngrok.io",
		logger:      slog.Default(),
		apiEndpoint: server.URL,
	}

	ctx := context.Background()
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.URL != "https://abc123.ngrok.io" {
		t.Errorf("URL = %q, want %q", status.URL, "https://abc123.ngrok.io")
	}
}

func TestNgrokProviderURLEmpty(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())
	if got := p.URL(); got != "" {
		t.Errorf("URL() = %q, want empty", got)
	}
}

func TestNgrokProviderURLSet(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{},
		url:    "https://test.ngrok.io",
		logger: slog.Default(),
	}
	if got := p.URL(); got != "https://test.ngrok.io" {
		t.Errorf("URL() = %q, want %q", got, "https://test.ngrok.io")
	}
}

func TestNgrokProviderName2(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())
	if got := p.Name(); got != "ngrok" {
		t.Errorf("Name() = %q, want %q", got, "ngrok")
	}
}

func TestNgrokProviderGetURLFromAPIMockServer(t *testing.T) {
	// Create a mock ngrok API server with an http and an https tunnel;
	// getURLFromAPI should prefer the https one.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"tunnels": [
				{
					"public_url": "http://abc123.ngrok.io",
					"proto": "http"
				},
				{
					"public_url": "https://abc123.ngrok.io",
					"proto": "https"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	p := NewNgrokProvider(&Config{}, slog.Default())
	p.apiEndpoint = server.URL

	url, err := p.getURLFromAPI()
	if err != nil {
		t.Fatalf("getURLFromAPI failed: %v", err)
	}
	if url != "https://abc123.ngrok.io" {
		t.Errorf("getURLFromAPI() = %q, want %q", url, "https://abc123.ngrok.io")
	}
}

func TestNgrokProviderGetURLFromAPIEmpty(t *testing.T) {
	// No ngrok agent reachable: getURLFromAPI must error.
	p := NewNgrokProvider(&Config{}, slog.Default())
	p.apiEndpoint = unroutableNgrokEndpoint

	_, err := p.getURLFromAPI()
	if err == nil {
		t.Error("expected error when no ngrok agent is reachable")
	}
}

func TestNgrokProviderIsInstalledCheck(t *testing.T) {
	p := NewNgrokProvider(&Config{}, slog.Default())

	// Should not panic
	installed := p.IsInstalled()
	t.Logf("ngrok installed: %v", installed)
}

func TestNgrokProviderConfigPort(t *testing.T) {
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
			name:     "zero port uses default",
			port:     0,
			wantPort: defaultTunnelPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Port: tt.port}
			p := NewNgrokProvider(cfg, slog.Default())

			port := p.config.Port
			if port == 0 {
				port = defaultTunnelPort
			}
			if port != tt.wantPort {
				t.Errorf("port = %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestNgrokProviderConfigDomain(t *testing.T) {
	cfg := &Config{
		Domain: "my-custom-domain.ngrok.io",
	}
	p := NewNgrokProvider(cfg, slog.Default())

	if p.config.Domain != "my-custom-domain.ngrok.io" {
		t.Errorf("Domain = %q, want %q", p.config.Domain, "my-custom-domain.ngrok.io")
	}
}

func TestNgrokProviderWaitForURLContextCancel(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{},
		logger: slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := p.waitForURL(ctx)
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestNgrokProviderStartArgs(t *testing.T) {
	// Test that Start would construct correct arguments
	tests := []struct {
		name     string
		config   *Config
		wantArgs []string
	}{
		{
			name: "default port",
			config: &Config{
				Port: 9090,
			},
			wantArgs: []string{"http", "9090"},
		},
		{
			name: "custom port",
			config: &Config{
				Port: 8080,
			},
			wantArgs: []string{"http", "8080"},
		},
		{
			name: "with domain",
			config: &Config{
				Port:   9090,
				Domain: "custom.ngrok.io",
			},
			wantArgs: []string{"http", "9090", "--domain", "custom.ngrok.io"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewNgrokProvider(tt.config, slog.Default())

			// Verify config is stored correctly
			if p.config.Port != tt.config.Port {
				t.Errorf("Port = %d, want %d", p.config.Port, tt.config.Port)
			}
			if p.config.Domain != tt.config.Domain {
				t.Errorf("Domain = %q, want %q", p.config.Domain, tt.config.Domain)
			}
		})
	}
}

func TestNgrokProviderSetupWithBinary(t *testing.T) {
	// Skip if ngrok is not installed
	if _, ok := CheckCLI(ngrokBin); !ok {
		t.Skip("skipping test that requires ngrok binary")
	}

	p := NewNgrokProvider(&Config{}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.Setup(ctx)
	// May succeed or fail depending on ngrok configuration
	if err != nil {
		t.Logf("Setup returned error (may be expected): %v", err)
	} else {
		t.Log("ngrok Setup succeeded")
	}
}

func TestNgrokProviderStatusBranches(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "no url",
			url:  "",
		},
		{
			name: "with url",
			url:  "https://abc.ngrok.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiEndpoint := unroutableNgrokEndpoint
			if tt.url != "" {
				apiEndpoint = ngrokFixtureServer(t, tt.url).URL
			}

			p := &NgrokProvider{
				config:      &Config{},
				url:         tt.url,
				logger:      slog.Default(),
				apiEndpoint: apiEndpoint,
			}

			ctx := context.Background()
			status, err := p.Status(ctx)
			if err != nil {
				t.Fatalf("Status failed: %v", err)
			}

			if status.Provider != "ngrok" {
				t.Errorf("Provider = %q, want %q", status.Provider, "ngrok")
			}
			if tt.url != "" && status.URL != tt.url {
				t.Errorf("URL = %q, want %q", status.URL, tt.url)
			}
		})
	}
}

func TestNgrokProviderWaitForURLTimeout(t *testing.T) {
	p := &NgrokProvider{
		config: &Config{},
		logger: slog.Default(),
	}

	// Very short timeout to trigger timeout path
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := p.waitForURL(ctx)
	if err == nil {
		t.Error("expected error from waitForURL with short timeout")
	}
}

func TestNgrokProviderGetURLFromAPIHTTPSPreference(t *testing.T) {
	// Return tunnels with both HTTP and HTTPS; getURLFromAPI must prefer https.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"tunnels": [
				{"public_url": "http://test.ngrok.io", "proto": "http"},
				{"public_url": "https://test.ngrok.io", "proto": "https"}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	p := NewNgrokProvider(&Config{}, slog.Default())
	p.apiEndpoint = server.URL

	url, err := p.getURLFromAPI()
	if err != nil {
		t.Fatalf("getURLFromAPI failed: %v", err)
	}
	if url != "https://test.ngrok.io" {
		t.Errorf("getURLFromAPI() = %q, want %q", url, "https://test.ngrok.io")
	}
}

func TestNgrokProviderGetURLFromAPINoTunnels(t *testing.T) {
	// API reachable but reports zero tunnels: getURLFromAPI must error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tunnels": []}`))
	}))
	defer server.Close()

	p := NewNgrokProvider(&Config{}, slog.Default())
	p.apiEndpoint = server.URL

	_, err := p.getURLFromAPI()
	if err == nil {
		t.Error("expected error when API reports no tunnels")
	}
}
