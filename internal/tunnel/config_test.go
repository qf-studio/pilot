package tunnel

import (
	"testing"
)

func TestDefaultConfigAllFields(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Enabled", cfg.Enabled, false},
		{"Provider", cfg.Provider, "cloudflare"},
		{"Port", cfg.Port, 9090},
		{"Domain", cfg.Domain, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestConfigStructValues(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "enabled cloudflare with domain",
			config: Config{
				Enabled:  true,
				Provider: "cloudflare",
				Domain:   "webhook.example.com",
				Port:     8080,
			},
		},
		{
			name: "enabled ngrok no domain",
			config: Config{
				Enabled:  true,
				Provider: "ngrok",
				Domain:   "",
				Port:     9090,
			},
		},
		{
			name: "manual mode",
			config: Config{
				Enabled:  false,
				Provider: "manual",
				Domain:   "",
				Port:     0,
			},
		},
		{
			name: "zero value config",
			config: Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify fields can be read
			_ = tt.config.Enabled
			_ = tt.config.Provider
			_ = tt.config.Domain
			_ = tt.config.Port
		})
	}
}

func TestConfigEnabledValues(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{"enabled true", true},
		{"enabled false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Enabled: tt.enabled}
			if cfg.Enabled != tt.enabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.enabled)
			}
		})
	}
}

func TestConfigProviderValues(t *testing.T) {
	providers := []string{
		"cloudflare",
		"ngrok",
		"manual",
		"",
		"custom",
	}

	for _, provider := range providers {
		t.Run("provider_"+provider, func(t *testing.T) {
			cfg := &Config{Provider: provider}
			if cfg.Provider != provider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, provider)
			}
		})
	}
}

func TestConfigPortValues(t *testing.T) {
	ports := []int{0, 80, 443, 8080, 9090, 65535}

	for _, port := range ports {
		t.Run("port", func(t *testing.T) {
			cfg := &Config{Port: port}
			if cfg.Port != port {
				t.Errorf("Port = %d, want %d", cfg.Port, port)
			}
		})
	}
}

func TestConfigDomainValues(t *testing.T) {
	domains := []string{
		"",
		"example.com",
		"webhook.example.com",
		"a.b.c.example.com",
		"test-domain.ngrok.io",
	}

	for _, domain := range domains {
		t.Run("domain_"+domain, func(t *testing.T) {
			cfg := &Config{Domain: domain}
			if cfg.Domain != domain {
				t.Errorf("Domain = %q, want %q", cfg.Domain, domain)
			}
		})
	}
}

func TestConfigEquality(t *testing.T) {
	cfg1 := &Config{
		Enabled:  true,
		Provider: "cloudflare",
		Domain:   "test.com",
		Port:     8080,
	}

	cfg2 := &Config{
		Enabled:  true,
		Provider: "cloudflare",
		Domain:   "test.com",
		Port:     8080,
	}

	if cfg1.Enabled != cfg2.Enabled {
		t.Error("Enabled should match")
	}
	if cfg1.Provider != cfg2.Provider {
		t.Error("Provider should match")
	}
	if cfg1.Domain != cfg2.Domain {
		t.Error("Domain should match")
	}
	if cfg1.Port != cfg2.Port {
		t.Error("Port should match")
	}
}

func TestDefaultConfigNotNil(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig should not return nil")
	}
}

func TestDefaultConfigIsCopy(t *testing.T) {
	cfg1 := DefaultConfig()
	cfg2 := DefaultConfig()

	// Modify cfg1
	cfg1.Enabled = true
	cfg1.Port = 1234

	// cfg2 should not be affected
	if cfg2.Enabled {
		t.Error("Modifying cfg1 should not affect cfg2")
	}
	if cfg2.Port != 9090 {
		t.Errorf("cfg2.Port = %d, want 9090", cfg2.Port)
	}
}
