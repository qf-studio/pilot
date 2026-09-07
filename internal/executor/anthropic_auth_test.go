package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAnthropicOAuthToken(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"oauth token", "sk-ant-oat01-abc123", true},
		{"api key", "sk-ant-api03-abc123", false},
		{"bare sk-ant- prefix, no api segment", "sk-ant-somethingelse", true},
		{"non anthropic bearer token", "some-proxy-token", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnthropicOAuthToken(tt.key); got != tt.want {
				t.Errorf("isAnthropicOAuthToken(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestSetAnthropicAuthHeaders(t *testing.T) {
	tests := []struct {
		name           string
		apiKey         string
		wantXAPIKey    string
		wantAuth       string
		wantBetaHeader string
	}{
		{
			name:        "API key uses x-api-key",
			apiKey:      "sk-ant-api03-abc123",
			wantXAPIKey: "sk-ant-api03-abc123",
		},
		{
			name:           "OAuth token uses Authorization Bearer + beta header",
			apiKey:         "sk-ant-oat01-abc123",
			wantAuth:       "Bearer sk-ant-oat01-abc123",
			wantBetaHeader: anthropicOAuthBetaHeader,
		},
		{
			name:     "non sk-ant- token uses Authorization Bearer",
			apiKey:   "some-proxy-token",
			wantAuth: "Bearer some-proxy-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
			setAnthropicAuthHeaders(req, tt.apiKey)

			if got := req.Header.Get("x-api-key"); got != tt.wantXAPIKey {
				t.Errorf("x-api-key = %q, want %q", got, tt.wantXAPIKey)
			}
			if got := req.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			if got := req.Header.Get("anthropic-beta"); got != tt.wantBetaHeader {
				t.Errorf("anthropic-beta = %q, want %q", got, tt.wantBetaHeader)
			}
		})
	}
}
