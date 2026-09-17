package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newBoundCoordinator returns a Coordinator whose storage is a fresh temp dir,
// discovered against the given authorization server.
func newBoundCoordinator(t *testing.T, metadata *ServerMetadata) *Coordinator {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	c, err := NewCoordinator("issuer-binding-test", 3400)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	c.serverMetadata = metadata
	return c
}

func TestClientInfoMatchesServer(t *testing.T) {
	const issuer = "https://as.example.com"
	const otherIssuer = "https://other.example.com"

	tests := []struct {
		name       string
		clientInfo *ClientInfo
		metadata   *ServerMetadata
		want       bool
	}{
		{
			name:       "a bound entry matches its own issuer",
			clientInfo: &ClientInfo{ClientID: "abc", RegisteredIssuer: issuer},
			metadata:   &ServerMetadata{Issuer: issuer},
			want:       true,
		},
		{
			name:       "a bound entry is refused to another issuer",
			clientInfo: &ClientInfo{ClientID: "abc", RegisteredIssuer: issuer},
			metadata:   &ServerMetadata{Issuer: otherIssuer},
			want:       false,
		},
		{
			name:       "an unbound entry with a secret is never reused",
			clientInfo: &ClientInfo{ClientID: "abc", ClientSecret: "shhh"},
			metadata:   &ServerMetadata{Issuer: issuer, RegistrationEndpoint: ""},
			want:       false,
		},
		{
			name:       "an unbound entry is replaced when the server can register",
			clientInfo: &ClientInfo{ClientID: "abc"},
			metadata:   &ServerMetadata{Issuer: issuer, RegistrationEndpoint: issuer + "/register"},
			want:       false,
		},
		{
			name:       "an unbound public entry is kept when there is no alternative",
			clientInfo: &ClientInfo{ClientID: "abc"},
			metadata:   &ServerMetadata{Issuer: issuer, RegistrationEndpoint: ""},
			want:       true,
		},
		{
			name:       "nothing discovered yet",
			clientInfo: &ClientInfo{ClientID: "abc"},
			metadata:   nil,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coordinator{serverMetadata: tt.metadata}
			if got := c.clientInfoMatchesServer(tt.clientInfo); got != tt.want {
				t.Errorf("clientInfoMatchesServer() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUnboundClientInfoIsBoundOnUse checks the entry stops being unbound after
// the one time it is accepted, so a later change of authorization server is
// caught rather than waved through again.
func TestUnboundClientInfoIsBoundOnUse(t *testing.T) {
	const issuer = "https://as.example.com"

	c := newBoundCoordinator(t, &ServerMetadata{
		Issuer:               issuer,
		RegistrationEndpoint: "", // no way to re-register, so the entry is kept
	})

	if err := c.saveClientInfo(&ClientInfo{ClientID: "cached-id"}); err != nil {
		t.Fatalf("saveClientInfo failed: %v", err)
	}

	clientInfo, err := c.loadOrRegisterClient()
	if err != nil {
		t.Fatalf("loadOrRegisterClient failed: %v", err)
	}
	if clientInfo.ClientID != "cached-id" {
		t.Errorf("client_id = %q, want the cached one", clientInfo.ClientID)
	}
	if clientInfo.RegisteredIssuer != issuer {
		t.Errorf("RegisteredIssuer = %q, want %q", clientInfo.RegisteredIssuer, issuer)
	}

	// The binding must have reached disk, not just the returned value.
	data, err := os.ReadFile(filepath.Join(getConfigDir(), "issuer-binding-test", "client_info.json"))
	if err != nil {
		t.Fatalf("failed to read the persisted client info: %v", err)
	}
	var persisted ClientInfo
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("failed to parse the persisted client info: %v", err)
	}
	if persisted.RegisteredIssuer != issuer {
		t.Errorf("persisted RegisteredIssuer = %q, want %q", persisted.RegisteredIssuer, issuer)
	}

	// Now that it is bound, a different authorization server must not get it.
	c.serverMetadata = &ServerMetadata{Issuer: "https://other.example.com"}
	if c.clientInfoMatchesServer(&persisted) {
		t.Error("the now-bound entry was accepted for a different issuer")
	}
}
