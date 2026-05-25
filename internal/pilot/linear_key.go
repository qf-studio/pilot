package pilot

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// parseLinearWebhookKey decodes a PEM-encoded Ed25519 public key for use in
// Linear webhook signature verification. Returns (nil, nil) when pemStr is
// empty — callers treat that as "verification disabled".
//
// On malformed input the error describes the specific failure so the operator
// can diagnose from logs without restarting with debug output.
func parseLinearWebhookKey(pemStr string) (ed25519.PublicKey, error) {
	if pemStr == "" {
		return nil, nil
	}

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("linear webhook_public_key: not valid PEM")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("linear webhook_public_key: expected PEM type \"PUBLIC KEY\", got %q", block.Type)
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("linear webhook_public_key: failed to parse PKIX public key: %w", err)
	}

	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("linear webhook_public_key: expected Ed25519 key, got %T", pub)
	}

	return ed, nil
}
