package pilot

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

// generateTestPEM returns a PEM-encoded Ed25519 public key for use in tests.
func generateTestPEM(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal PKIX: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return pub, string(pemBytes)
}

func TestParseLinearWebhookKey_Empty(t *testing.T) {
	key, err := parseLinearWebhookKey("")
	if err != nil {
		t.Fatalf("expected nil error for empty string, got: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for empty string, got non-nil")
	}
}

func TestParseLinearWebhookKey_Valid(t *testing.T) {
	wantPub, pemStr := generateTestPEM(t)

	key, err := parseLinearWebhookKey(pemStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
	if !key.Equal(wantPub) {
		t.Errorf("parsed key does not match original")
	}
}

func TestParseLinearWebhookKey_NotPEM(t *testing.T) {
	_, err := parseLinearWebhookKey("not-pem-data")
	if err == nil {
		t.Fatal("expected error for non-PEM input")
	}
	if !strings.Contains(err.Error(), "not valid PEM") {
		t.Errorf("error should mention 'not valid PEM', got: %v", err)
	}
}

func TestParseLinearWebhookKey_WrongPEMType(t *testing.T) {
	_, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// Encode with wrong PEM block type
	wrongPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("fake")})

	_, parseErr := parseLinearWebhookKey(string(wrongPEM))
	if parseErr == nil {
		t.Fatal("expected error for wrong PEM type")
	}
	if !strings.Contains(parseErr.Error(), "expected PEM type") {
		t.Errorf("error should mention 'expected PEM type', got: %v", parseErr)
	}
}

func TestParseLinearWebhookKey_CorruptedDER(t *testing.T) {
	// Valid PEM header/footer but garbage DER content
	badPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("garbage-der-bytes")})

	_, err := parseLinearWebhookKey(string(badPEM))
	if err == nil {
		t.Fatal("expected error for corrupted DER")
	}
	if !strings.Contains(err.Error(), "failed to parse PKIX") {
		t.Errorf("error should mention 'failed to parse PKIX', got: %v", err)
	}
}

func TestParseLinearWebhookKey_WrongKeyType(t *testing.T) {
	// Generate an RSA-style fake by using a non-ed25519 key type.
	// The easiest approach: generate a real ed25519 key but wrap the DER in
	// a PEM block whose content claims it's a different key — actually just
	// use a valid RSA key DER if available. For simplicity, marshal a valid
	// Ed25519 key but encode the *private* key DER (which isn't a valid PKIX
	// public key) to trigger the ParsePKIXPublicKey error path.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private: %v", err)
	}
	// Force-wrap private key DER as "PUBLIC KEY" block
	wrongPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: privDER})

	_, parseErr := parseLinearWebhookKey(string(wrongPEM))
	if parseErr == nil {
		t.Fatal("expected error for private-key-DER-in-public-key-block")
	}
}
