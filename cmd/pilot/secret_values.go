package main

import (
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// publicKeySuffix excludes keys such as ssh public keys, which are not secret
// even though they match secretKeyPattern.
var publicKeySuffix = regexp.MustCompile(`(?i)public_key$`)

// alwaysSecretEnvNames are registered even though the model-env deny rules
// keep them in the subprocess environment.
var alwaysSecretEnvNames = []string{"GITHUB_TOKEN", "GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// collectSecretValues returns every configured secret value: string values of
// config keys matching secretKeyPattern (minus *public_key), and the values of
// process env vars classified as secret by the model-env deny rules plus the
// GitHub and Claude Code tokens (GH-6249).
func collectSecretValues(cfg *config.Config, environ []string) []string {
	var out []string
	if cfg != nil {
		if data, err := yaml.Marshal(cfg); err == nil {
			var generic interface{}
			if yaml.Unmarshal(data, &generic) == nil {
				out = collectKeyedStrings(generic, false, out)
			}
		}
	}

	always := make(map[string]bool, len(alwaysSecretEnvNames))
	for _, n := range alwaysSecretEnvNames {
		always[n] = true
	}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if always[name] || executor.IsSecretEnvName(name) {
			out = append(out, value)
		}
	}
	return out
}

// collectKeyedStrings walks a generically-decoded config tree and appends
// every string found under a secret-shaped key. inSecret is true while
// descending below such a key, so a list of tokens is collected whole.
func collectKeyedStrings(v interface{}, inSecret bool, out []string) []string {
	switch val := v.(type) {
	case string:
		if inSecret && val != "" {
			out = append(out, val)
		}
	case map[string]interface{}:
		for k, sub := range val {
			secret := inSecret || (secretKeyPattern.MatchString(k) && !publicKeySuffix.MatchString(k))
			out = collectKeyedStrings(sub, secret, out)
		}
	case []interface{}:
		for _, sub := range val {
			out = collectKeyedStrings(sub, inSecret, out)
		}
	}
	return out
}

// registerSecretValues registers the configured secrets for value-based
// redaction and returns how many distinct values are now registered.
func registerSecretValues(cfg *config.Config) int {
	secretpatterns.RegisterSecretValues(collectSecretValues(cfg, os.Environ())...)
	return secretpatterns.SecretValueCount()
}
