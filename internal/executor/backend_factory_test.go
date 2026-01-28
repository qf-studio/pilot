package executor

import (
	"testing"
)

func TestNewBackend(t *testing.T) {
	tests := []struct {
		name        string
		config      *BackendConfig
		expectType  string
		expectError bool
	}{
		{
			name:       "nil config defaults to claude-code",
			config:     nil,
			expectType: BackendTypeClaudeCode,
		},
		{
			name:       "empty type defaults to claude-code",
			config:     &BackendConfig{Type: ""},
			expectType: BackendTypeClaudeCode,
		},
		{
			name:       "claude-code type",
			config:     &BackendConfig{Type: BackendTypeClaudeCode},
			expectType: BackendTypeClaudeCode,
		},
		{
			name:       "opencode type",
			config:     &BackendConfig{Type: BackendTypeOpenCode},
			expectType: BackendTypeOpenCode,
		},
		{
			name:        "unknown type",
			config:      &BackendConfig{Type: "unknown-backend"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewBackend(tt.config)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if backend == nil {
				t.Fatal("backend is nil")
			}
			if backend.Name() != tt.expectType {
				t.Errorf("Name() = %q, want %q", backend.Name(), tt.expectType)
			}
		})
	}
}

func TestNewBackendFromType(t *testing.T) {
	tests := []struct {
		name        string
		backendType string
		expectType  string
		expectError bool
	}{
		{
			name:       "claude-code",
			backendType: BackendTypeClaudeCode,
			expectType:  BackendTypeClaudeCode,
		},
		{
			name:       "opencode",
			backendType: BackendTypeOpenCode,
			expectType:  BackendTypeOpenCode,
		},
		{
			name:        "unknown",
			backendType: "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewBackendFromType(tt.backendType)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if backend.Name() != tt.expectType {
				t.Errorf("Name() = %q, want %q", backend.Name(), tt.expectType)
			}
		})
	}
}

func TestNewBackendWithClaudeCodeConfig(t *testing.T) {
	config := &BackendConfig{
		Type: BackendTypeClaudeCode,
		ClaudeCode: &ClaudeCodeConfig{
			Command:   "/custom/claude",
			ExtraArgs: []string{"--verbose"},
		},
	}

	backend, err := NewBackend(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if backend.Name() != BackendTypeClaudeCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeClaudeCode)
	}

	// Verify it's a ClaudeCodeBackend
	ccBackend, ok := backend.(*ClaudeCodeBackend)
	if !ok {
		t.Fatal("backend is not *ClaudeCodeBackend")
	}
	if ccBackend.config.Command != "/custom/claude" {
		t.Errorf("Command = %q, want /custom/claude", ccBackend.config.Command)
	}
}

func TestNewBackendWithOpenCodeConfig(t *testing.T) {
	config := &BackendConfig{
		Type: BackendTypeOpenCode,
		OpenCode: &OpenCodeConfig{
			ServerURL: "http://localhost:5000",
			Model:     "anthropic/claude-opus-4",
		},
	}

	backend, err := NewBackend(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if backend.Name() != BackendTypeOpenCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeOpenCode)
	}

	// Verify it's an OpenCodeBackend
	ocBackend, ok := backend.(*OpenCodeBackend)
	if !ok {
		t.Fatal("backend is not *OpenCodeBackend")
	}
	if ocBackend.config.ServerURL != "http://localhost:5000" {
		t.Errorf("ServerURL = %q, want http://localhost:5000", ocBackend.config.ServerURL)
	}
}
