package github

import (
	"strings"
	"testing"
)

func TestAppConfig_Validate_Nil(t *testing.T) {
	var cfg *AppConfig
	if err := cfg.Validate(); err != nil {
		t.Errorf("nil AppConfig.Validate() = %v, want nil (App auth is opt-in)", err)
	}
}

func TestAppConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *AppConfig
		wantErr string // substring expected in the error, "" means no error
	}{
		{
			name: "valid",
			cfg: &AppConfig{
				AppID:          123456,
				InstallationID: 78901234,
				PrivateKeyPath: "/etc/pilot/github-app.pem",
			},
			wantErr: "",
		},
		{
			name:    "missing app_id",
			cfg:     &AppConfig{InstallationID: 78901234, PrivateKeyPath: "/etc/pilot/github-app.pem"},
			wantErr: "app_id",
		},
		{
			name:    "zero app_id",
			cfg:     &AppConfig{AppID: 0, InstallationID: 78901234, PrivateKeyPath: "/etc/pilot/github-app.pem"},
			wantErr: "app_id",
		},
		{
			name:    "missing installation_id",
			cfg:     &AppConfig{AppID: 123456, PrivateKeyPath: "/etc/pilot/github-app.pem"},
			wantErr: "installation_id",
		},
		{
			name:    "missing private_key_path",
			cfg:     &AppConfig{AppID: 123456, InstallationID: 78901234},
			wantErr: "private_key_path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error naming %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want error naming %q", err.Error(), tt.wantErr)
			}
		})
	}
}
