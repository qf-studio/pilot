package tunnel

// Config holds tunnel configuration
type Config struct {
	Enabled  bool   `yaml:"enabled"`
	Provider string `yaml:"provider"` // "cloudflare", "ngrok", "manual"
	Domain   string `yaml:"domain"`   // Custom domain (optional)
	Port     int    `yaml:"port"`     // Local port to tunnel (default: gateway port)
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Enabled:  false,
		Provider: "cloudflare",
		Port:     9090,
	}
}
