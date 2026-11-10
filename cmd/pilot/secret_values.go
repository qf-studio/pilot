package main

import (
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/logging"
)

// publicKeySuffix excludes keys such as ssh public keys, which are not secret
// even though they match secretKeyPattern.
var publicKeySuffix = regexp.MustCompile(`(?i)public_key$`)

// alwaysSecretEnvNames are registered even though the model-env deny rules
// keep them in the subprocess environment.
var alwaysSecretEnvNames = []string{"GITHUB_TOKEN", "GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "AWS_ACCESS_KEY_ID"}

// secretEnvSuffixes mark an env name as a credential on top of the model-env
// deny rules (GH-6266).
var secretEnvSuffixes = []string{"_KEY", "_DSN"}

// notSecretEnvNames are denied to the model session but hold ids, not
// credentials: masking a digit string would corrupt ordinary text (GH-6266).
var notSecretEnvNames = map[string]bool{
	"TELEGRAM_CHAT_ID":          true,
	"TELEGRAM_APPROVER_CHAT_ID": true,
}

// notSecretConfigKeys match secretKeyPattern but hold a name or a path, not a
// credential: api_key_env is an env var name and api_key_command a shell
// command (both under executor.claude_code.billing).
var notSecretConfigKeys = map[string]bool{
	"api_key_env":      true,
	"api_key_command":  true,
	"private_key_path": true,
}

// credentialConfigKeys hold a credential although secretKeyPattern misses
// them: the Azure DevOps PAT, webhook and alert-channel URLs and headers,
// PagerDuty routing keys and DSNs (GH-6266).
var credentialConfigKeys = map[string]bool{
	"pat":             true,
	"webhook_url":     true,
	"webhook_headers": true,
	"headers":         true,
	"routing_key":     true,
	"dsn":             true,
}

// billingKeyEnvConfigKey names the config key whose value is an env var name;
// the variable's value is the secret.
const billingKeyEnvConfigKey = "api_key_env"

// webhookHostFragments mark a url whose path is itself the credential.
var webhookHostFragments = []string{"hooks.slack.com", "discord.com/api/webhooks"}

// collectSecretValues returns every configured secret value: string values of
// config keys matching secretKeyPattern (minus *public_key and the name/path
// keys) or credentialConfigKeys, the values of the env vars that api_key_env
// names, and the values of process env vars classified as secret (GH-6249,
// GH-6266).
func collectSecretValues(cfg *config.Config, environ []string) []string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok {
			env[name] = value
		}
	}

	var out []string
	if cfg != nil {
		log := logging.WithComponent("secret-values")
		data, err := yaml.Marshal(cfg)
		if err != nil {
			log.Warn("secret value collection skipped: config marshal failed", slog.Any("error", err))
		} else {
			var generic interface{}
			if err := yaml.Unmarshal(data, &generic); err != nil {
				log.Warn("secret value collection skipped: config walk failed", slog.Any("error", err))
			} else {
				out = collectKeyedStrings(generic, false, out)
				for _, name := range collectKeyedEnvNames(generic, nil) {
					if v := strings.TrimSpace(env[name]); v != "" {
						out = append(out, v)
					}
				}
			}
		}
	}

	always := make(map[string]bool, len(alwaysSecretEnvNames))
	for _, n := range alwaysSecretEnvNames {
		always[n] = true
	}
	for name, value := range env {
		if value != "" && isSecretEnvName(name, always) {
			out = append(out, value)
		}
	}
	return out
}

// isSecretEnvName reports whether the value of env var name is registered.
func isSecretEnvName(name string, always map[string]bool) bool {
	if notSecretEnvNames[name] {
		return false
	}
	if always[name] || executor.IsSecretEnvName(name) {
		return true
	}
	if publicKeySuffix.MatchString(name) {
		return false
	}
	for _, suffix := range secretEnvSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// isSecretConfigKey reports whether a string under key k is a credential.
func isSecretConfigKey(k string) bool {
	lk := strings.ToLower(k)
	if notSecretConfigKeys[lk] {
		return false
	}
	if credentialConfigKeys[lk] {
		return true
	}
	return secretKeyPattern.MatchString(k) && !publicKeySuffix.MatchString(k)
}

// isCredentialURL reports whether a bare url value is a credential: it holds
// userinfo or a query string, or sits on a webhook host where the path is the
// secret.
func isCredentialURL(raw string) bool {
	u, err := url.Parse(raw)
	if err == nil && (u.User != nil || u.RawQuery != "") {
		return true
	}
	for _, frag := range webhookHostFragments {
		if strings.Contains(raw, frag) {
			return true
		}
	}
	return false
}

// headerSchemes are the Authorization prefixes whose remainder is registered
// on its own, since logs often show the credential without the scheme.
var headerSchemes = []string{"Bearer ", "Basic ", "Token "}

// collectKeyedStrings walks a generically-decoded config tree and appends
// every string found under a secret-shaped key. inSecret is true while
// descending below such a key, so a list of tokens is collected whole and a
// header map yields its values, not its names.
func collectKeyedStrings(v interface{}, inSecret bool, out []string) []string {
	switch val := v.(type) {
	case string:
		if inSecret && val != "" {
			out = append(out, val)
			for _, scheme := range headerSchemes {
				if rest, ok := strings.CutPrefix(val, scheme); ok && rest != "" {
					out = append(out, rest)
				}
			}
		}
	case map[string]interface{}:
		for k, sub := range val {
			secret := inSecret || isSecretConfigKey(k)
			if s, ok := sub.(string); ok && !secret && strings.EqualFold(k, "url") && isCredentialURL(s) {
				secret = true
			}
			out = collectKeyedStrings(sub, secret, out)
		}
	case []interface{}:
		for _, sub := range val {
			out = collectKeyedStrings(sub, inSecret, out)
		}
	}
	return out
}

// collectKeyedEnvNames appends the values of api_key_env keys: names of env
// vars whose value is the secret.
func collectKeyedEnvNames(v interface{}, out []string) []string {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, sub := range val {
			if s, ok := sub.(string); ok && s != "" && strings.EqualFold(k, billingKeyEnvConfigKey) {
				out = append(out, s)
				continue
			}
			out = collectKeyedEnvNames(sub, out)
		}
	case []interface{}:
		for _, sub := range val {
			out = collectKeyedEnvNames(sub, out)
		}
	}
	return out
}

// registerSecretValues registers the configured secrets for value-based
// redaction and returns how many distinct values are now registered. Every
// command that loads config and builds a Runner calls it right after the
// load (GH-6266).
func registerSecretValues(cfg *config.Config) int {
	secretpatterns.RegisterSecretValues(collectSecretValues(cfg, os.Environ())...)
	return secretpatterns.SecretValueCount()
}

// loadConfigRegisteringSecrets loads the config at path and registers its
// secrets for redaction. Commands that build a Runner call it instead of
// config.Load, so no entry point records a session before the registration
// (GH-6266).
func loadConfigRegisteringSecrets(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	registerSecretValues(cfg)
	return cfg, nil
}
