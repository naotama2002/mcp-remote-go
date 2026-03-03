package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const (
	// codeVerifierLength is the length of the PKCE code verifier (RFC 7636 recommends 43-128).
	codeVerifierLength = 64

	// codeVerifierCharset is the set of unreserved characters allowed in the code verifier
	// per RFC 7636 Section 4.1: [A-Z] / [a-z] / [0-9] / "-" / "." / "_" / "~"
	codeVerifierCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
)

// GenerateCodeVerifier generates a cryptographically random code verifier
// per RFC 7636 Section 4.1.
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, codeVerifierLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	charset := []byte(codeVerifierCharset)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}

	return string(b), nil
}

// ComputeCodeChallenge computes the S256 code challenge from a code verifier
// per RFC 7636 Section 4.2: BASE64URL(SHA256(code_verifier))
func ComputeCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
