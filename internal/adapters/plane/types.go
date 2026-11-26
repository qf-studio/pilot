package plane

import "time"

// Config holds Plane.so adapter configuration.
type Config struct {
	Enabled       bool           `yaml:"enabled"`
	BaseURL       string         `yaml:"base_url"`       // https://api.plane.so (or self-hosted)
	APIKey        string         `yaml:"api_key"`        // X-API-Key header value
	WebhookSecret string         `yaml:"webhook_secret"` // For HMAC signature verification
	WorkspaceSlug string         `yaml:"workspace_slug"`
	ProjectIDs    []string       `yaml:"project_ids"`
	PilotLabel    string         `yaml:"pilot_label"` // default: "pilot"
	Polling       *PollingConfig `yaml:"polling,omitempty"`
}

// PollingConfig holds polling configuration for the Plane adapter.
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// DefaultConfig returns default Plane configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		BaseURL:    "https://api.plane.so",
		PilotLabel: "pilot",
	}
}
