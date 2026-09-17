package proxy

import (
	"strings"
	"testing"
)

// TestNoBrowserForAClientThatIsGone is the regression guard for the second
// browser window.
//
// A host that starts a proxy and immediately drops it -- which Claude Desktop
// does on every launch, spawning one instance and replacing it a moment later --
// leaves the abandoned instance mid-authorization. It used to finish the flow
// anyway: register, bind a callback port, and open a browser window for a client
// that no longer existed. The user got two windows, and only one of them could be
// completed, because each carried its own `state` and its own port.
func TestNoBrowserForAClientThatIsGone(t *testing.T) {
	server := newOAuthMockServer(t, "", nil)
	seedHome(t)

	// Nothing may reach the browser in this test.
	opened := captureBrowserOpens(t, nil)

	proxy, err := NewProxyWithTransport(server.MCPURL(), 0, map[string]string{}, "abandoned-auth-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	// The client is already gone by the time authorization would begin.
	proxy.cancel()

	err = proxy.handleAuthentication(server.Challenge)

	if err == nil {
		t.Fatal("authorization reported success for a client that was gone")
	}
	if got := opened(); len(got) != 0 {
		t.Errorf("opened a browser window for a client that was gone: %v", got)
	}
	if !strings.Contains(err.Error(), "client is gone") {
		t.Errorf("error does not say why it stopped: %v", err)
	}
}
