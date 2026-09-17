package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
)

// stateLength is the number of random bytes behind the `state` parameter.
// 32 bytes is well above the entropy needed to make the value unguessable.
const stateLength = 32

// GenerateState returns a cryptographically random, URL-safe `state` value.
//
// `state` binds the authorization request to the callback that answers it, so
// an attacker cannot feed the client an authorization code obtained in a
// different flow (OAuth 2.1 §7.5.1 / RFC 6749 §10.12).
func GenerateState() (string, error) {
	b := make([]byte, stateLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validateAuthorizationResponse checks the query parameters of an
// authorization callback before its code may be exchanged.
//
// Two properties are enforced:
//
//   - `state` matches the value sent on the authorization request. An empty
//     expectedState means no request is in flight, so nothing may be accepted.
//   - `iss` identifies the authorization server we actually sent the user to
//     (RFC 9207). Without this check a client talking to several authorization
//     servers can be made to send a code to the wrong one — the mix-up attack.
//     The parameter is required when the server's metadata advertises support
//     for it, and validated whenever it is present.
func validateAuthorizationResponse(query url.Values, expectedState string, metadata *ServerMetadata) error {
	// Authenticity is established before anything in the response is believed,
	// including a reported failure. The callback listens on loopback, so any
	// page the user has open can reach it; checking `error` first would let
	// such a page abort a live authorization with ?error=access_denied, and
	// have its own error_description surfaced to the user as though the
	// authorization server had said it.
	if expectedState == "" {
		return fmt.Errorf("no authorization request in flight")
	}
	if state := query.Get("state"); state != expectedState {
		return fmt.Errorf("state mismatch: authorization response does not belong to this request")
	}

	if metadata == nil {
		// Nothing to compare an issuer against; the flow cannot have started.
		return fmt.Errorf("no authorization server metadata available")
	}
	iss := query.Get("iss")
	switch {
	case iss != "":
		if iss != metadata.Issuer {
			return fmt.Errorf("issuer mismatch: response came from %q, expected %q", iss, metadata.Issuer)
		}
	case metadata.AuthorizationResponseIssParameterSupported:
		return fmt.Errorf("authorization server advertises iss support but omitted it from the response")
	}

	// The response is established as belonging to this request and to the
	// expected issuer, so a failure it reports can now be taken at face value.
	if errCode := query.Get("error"); errCode != "" {
		if desc := query.Get("error_description"); desc != "" {
			return fmt.Errorf("authorization server returned error %q: %s", errCode, desc)
		}
		return fmt.Errorf("authorization server returned error %q", errCode)
	}

	if query.Get("code") == "" {
		return fmt.Errorf("authorization code not found")
	}

	return nil
}
