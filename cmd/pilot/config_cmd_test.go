package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "****"},
		{"short (<=8 chars)", "abcd1234", "****"},
		{"long token", "ghp_1234567890abcdef", "ghp_...cdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskSecret(tt.in); got != tt.want {
				t.Errorf("maskSecret(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactSecretsYAML_MasksMatchingKeys(t *testing.T) {
	input := `
adapters:
  github:
    token: ghp_1234567890abcdef
  telegram:
    bot_token: 1234567890:AAFakeTelegramBotTokenForTests
alerts:
  enabled: true
projects:
  - path: /some/project
`
	out, err := redactSecretsYAML([]byte(input))
	if err != nil {
		t.Fatalf("redactSecretsYAML: %v", err)
	}

	var decoded map[string]interface{}
	if err := yaml.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal redacted output: %v", err)
	}

	adapters := decoded["adapters"].(map[string]interface{})
	ghToken := adapters["github"].(map[string]interface{})["token"].(string)
	if ghToken == "ghp_1234567890abcdef" {
		t.Errorf("github.token not redacted: %q", ghToken)
	}
	if !strings.HasPrefix(ghToken, "ghp_") || !strings.HasSuffix(ghToken, "cdef") {
		t.Errorf("github.token redaction = %q, want first4...last4 shape", ghToken)
	}

	tgToken := adapters["telegram"].(map[string]interface{})["bot_token"].(string)
	if tgToken == "1234567890:AAFakeTelegramBotTokenForTests" {
		t.Errorf("telegram.bot_token not redacted: %q", tgToken)
	}

	// Non-secret keys must pass through untouched.
	if decoded["alerts"].(map[string]interface{})["enabled"] != true {
		t.Error("non-secret key alerts.enabled was altered")
	}
	projects := decoded["projects"].([]interface{})
	if projects[0].(map[string]interface{})["path"] != "/some/project" {
		t.Error("non-secret nested slice value was altered")
	}
}

func TestRedactSecretsJSON_MasksMatchingKeys(t *testing.T) {
	type cfg struct {
		APIKey   string `json:"api_key"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	data, err := json.Marshal(cfg{APIKey: "sk-abcdefghijklmnop", Password: "hunter2hunter2", Name: "pilot"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := redactSecretsJSON(data)
	if err != nil {
		t.Fatalf("redactSecretsJSON: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal redacted output: %v", err)
	}

	if decoded["api_key"] == "sk-abcdefghijklmnop" {
		t.Error("api_key not redacted")
	}
	if decoded["password"] == "hunter2hunter2" {
		t.Error("password not redacted")
	}
	if decoded["name"] != "pilot" {
		t.Errorf("non-secret key altered: name=%v", decoded["name"])
	}
}

func TestRedactSecretValue_EmptyStringLeftAlone(t *testing.T) {
	in := map[string]interface{}{"token": ""}
	out := redactSecretValue(in).(map[string]interface{})
	if out["token"] != "" {
		t.Errorf("empty secret value should stay empty, got %q", out["token"])
	}
}
