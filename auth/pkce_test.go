package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateCodeVerifier(t *testing.T) {
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() error: %v", err)
	}

	// Check length
	if len(verifier) != codeVerifierLength {
		t.Errorf("Expected length %d, got %d", codeVerifierLength, len(verifier))
	}

	// Check character set
	for i, c := range verifier {
		if !strings.ContainsRune(codeVerifierCharset, c) {
			t.Errorf("Invalid character at position %d: %c", i, c)
		}
	}
}

func TestGenerateCodeVerifierUniqueness(t *testing.T) {
	v1, err1 := GenerateCodeVerifier()
	v2, err2 := GenerateCodeVerifier()

	if err1 != nil || err2 != nil {
		t.Fatalf("GenerateCodeVerifier() errors: %v, %v", err1, err2)
	}

	if v1 == v2 {
		t.Error("Two generated verifiers should be different")
	}
}

func TestComputeCodeChallenge(t *testing.T) {
	// Test with a known value
	// RFC 7636 Appendix B test vector:
	// code_verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// Expected code_challenge (S256) = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	expected := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	challenge := ComputeCodeChallenge(verifier)
	if challenge != expected {
		t.Errorf("Expected challenge %s, got %s", expected, challenge)
	}
}

func TestComputeCodeChallengeConsistency(t *testing.T) {
	verifier := "test-verifier-12345"

	c1 := ComputeCodeChallenge(verifier)
	c2 := ComputeCodeChallenge(verifier)

	if c1 != c2 {
		t.Error("Same verifier should produce same challenge")
	}

	// Manually verify: SHA-256 then Base64URL
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])

	if c1 != expected {
		t.Errorf("Challenge doesn't match manual computation: got %s, expected %s", c1, expected)
	}
}

func TestComputeCodeChallengeIsBase64URLNoPadding(t *testing.T) {
	verifier, _ := GenerateCodeVerifier()
	challenge := ComputeCodeChallenge(verifier)

	// Should not contain padding
	if strings.Contains(challenge, "=") {
		t.Error("Challenge should not contain padding characters")
	}

	// Should not contain standard base64 characters
	if strings.Contains(challenge, "+") || strings.Contains(challenge, "/") {
		t.Error("Challenge should use URL-safe base64 encoding")
	}
}
