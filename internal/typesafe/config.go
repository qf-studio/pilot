// Package typesafe is a small stdlib HTTP client for the TypeSafe System One
// API (Jev models) plus the merge contract gates use to combine a model verdict
// with a deterministic (regex) floor. It lives outside internal/executor so
// packages that executor cannot be imported by (internal/autopilot) can share it.
package typesafe

import (
	"os"
	"strings"
	"time"
)

const (
	// DefaultEndpoint is the System One endpoint (docs.typesafe.ai/api, 2026-09-30).
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	// DefaultModel is the default Jev model alias.
	DefaultModel = "jev-latest"
	// DefaultTimeout bounds a single Ask call. Gates fail open to their regex
	// floor, so the model must never make a gate noticeably slower.
	DefaultTimeout = 5 * time.Second

	// APIKeyEnv is the only place the API key is read from. No file fallback:
	// host-state-dependent resolution makes tests differ between CI and the box.
	APIKeyEnv = "TYPESAFE_API_KEY"
)

// Config is the connection block shared by every TypeSafe-backed gate
// (top-level `typesafe:` YAML). All fields are optional.
type Config struct {
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	// Timeout is a Go duration string such as "5s".
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// EffectiveEndpoint returns the configured endpoint or DefaultEndpoint.
// Safe on a nil receiver.
func (c *Config) EffectiveEndpoint() string {
	if c == nil || strings.TrimSpace(c.Endpoint) == "" {
		return DefaultEndpoint
	}
	return strings.TrimSpace(c.Endpoint)
}

// EffectiveModel returns the configured model or DefaultModel.
// Safe on a nil receiver.
func (c *Config) EffectiveModel() string {
	if c == nil || strings.TrimSpace(c.Model) == "" {
		return DefaultModel
	}
	return strings.TrimSpace(c.Model)
}

// EffectiveTimeout returns the configured timeout, or DefaultTimeout when it is
// unset, unparsable or not positive. Safe on a nil receiver.
func (c *Config) EffectiveTimeout() time.Duration {
	if c == nil {
		return DefaultTimeout
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.Timeout))
	if err != nil || d <= 0 {
		return DefaultTimeout
	}
	return d
}

// APIKeyFromEnv reads TYPESAFE_API_KEY and nothing else. It returns the key and
// a source label (`env:TYPESAFE_API_KEY`) suitable for logging; both are empty
// when the variable is unset or blank. Log the label, never the key.
func APIKeyFromEnv() (key, source string) {
	key = strings.TrimSpace(os.Getenv(APIKeyEnv))
	if key == "" {
		return "", ""
	}
	return key, "env:" + APIKeyEnv
}
