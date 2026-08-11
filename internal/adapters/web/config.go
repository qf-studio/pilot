// Package web implements the web (console Operator chat panel) transport for
// the shared internal/comms adapter brain (GH-4835 / C17). It is the fourth
// comms.BuildHandler assembly alongside Telegram, Slack, and Discord — see
// internal/comms/factory.go for the shared inbound/outbound seam.
package web

import "github.com/qf-studio/pilot/internal/comms"

// Config configures the web chat transport — YAML key adapters.chat. Disabled
// by default: when Enabled is false, the gateway does not register the chat
// routes at all (POST /api/v1/chat/messages and GET
// /api/v1/chat/conversations/{id}/events 404 like any other unknown path)
// rather than returning a 503, mirroring how disabled adapters are absent
// elsewhere in this codebase rather than present-but-erroring.
type Config struct {
	Enabled bool `yaml:"enabled"`

	// RateLimit is per-ContextID (per-conversation, "web:"+conversationID) —
	// not shared across conversations. Defaults to comms.DefaultRateLimitConfig
	// when nil, matching the Telegram/Slack call sites.
	RateLimit *comms.RateLimitConfig `yaml:"rate_limit,omitempty"`
}

// DefaultConfig returns the disabled-by-default web chat config, matching
// every other adapter's DefaultConfig (see internal/adapters/slack,
// internal/adapters/telegram).
func DefaultConfig() *Config {
	return &Config{
		Enabled: false,
	}
}
