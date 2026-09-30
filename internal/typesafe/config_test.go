package typesafe

import (
	"testing"
	"time"
)

func TestConfig_Defaults(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *Config
		wantEndpoint string
		wantModel    string
		wantTimeout  time.Duration
	}{
		{"nil", nil, DefaultEndpoint, "jev-latest", 5 * time.Second},
		{"empty", &Config{}, DefaultEndpoint, "jev-latest", 5 * time.Second},
		{"blank strings", &Config{Endpoint: "  ", Model: " ", Timeout: " "}, DefaultEndpoint, "jev-latest", 5 * time.Second},
		{"set", &Config{Endpoint: "http://x/y", Model: "jev-2", Timeout: "2s"}, "http://x/y", "jev-2", 2 * time.Second},
		{"unparsable timeout", &Config{Timeout: "soon"}, DefaultEndpoint, "jev-latest", 5 * time.Second},
		{"zero timeout", &Config{Timeout: "0s"}, DefaultEndpoint, "jev-latest", 5 * time.Second},
		{"negative timeout", &Config{Timeout: "-3s"}, DefaultEndpoint, "jev-latest", 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveEndpoint(); got != tt.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", got, tt.wantEndpoint)
			}
			if got := tt.cfg.EffectiveModel(); got != tt.wantModel {
				t.Errorf("model = %q, want %q", got, tt.wantModel)
			}
			if got := tt.cfg.EffectiveTimeout(); got != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", got, tt.wantTimeout)
			}
		})
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "fake-typesafe-key")
		key, src := APIKeyFromEnv()
		if key != "fake-typesafe-key" || src != "env:TYPESAFE_API_KEY" {
			t.Errorf("got (%q, %q)", key, src)
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "")
		key, src := APIKeyFromEnv()
		if key != "" || src != "" {
			t.Errorf("got (%q, %q), want empty", key, src)
		}
	})
	t.Run("ignores other env vars", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "")
		t.Setenv("TYPESAFE_KEY", "fake-other")
		t.Setenv("ANTHROPIC_API_KEY", "fake-other")
		if key, _ := APIKeyFromEnv(); key != "" {
			t.Errorf("read a key from another variable: %q", key)
		}
	})
}
