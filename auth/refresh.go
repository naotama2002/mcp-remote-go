package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
)

// OAuth grant types this client uses (RFC 6749 §4.1, §6).
const (
	grantTypeAuthorizationCode = "authorization_code"
	grantTypeRefreshToken      = "refresh_token"
)

// renewalSkew is how long before expiry an access token is treated as due for
// renewal. Servers and clients do not share a clock, and a token that expires
// mid-flight costs a failed request, so the last few seconds of a lifetime are
// not worth using.
const renewalSkew = 60 * time.Second

// ErrNoRefreshToken means there is nothing stored to renew with, so the caller
// has to fall back to an interactive authorization. It is an ordinary outcome,
// not a failure: a server may issue no refresh token at all.
var ErrNoRefreshToken = errors.New("no refresh token stored")

// stampExpiry records when the access token stops being accepted, from the
// lifetime the server reported. A response without expires_in leaves the field
// alone: an unknown lifetime is better represented by nothing than by a guess,
// and the token is then renewed when the server rejects it.
func (t *Tokens) stampExpiry(now time.Time) {
	if t.ExpiresIn > 0 {
		t.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second).Unix()
	}
}

// DueForRenewal reports whether the access token is close enough to expiry that
// it should be renewed before being used again.
//
// A token with no recorded expiry is never due: nothing is known about it, and
// renewing on that basis would trade a working token for a guess. Such a token
// is renewed reactively instead, when a server turns it down.
func (t *Tokens) DueForRenewal() bool {
	if t == nil || t.ExpiresAt == 0 {
		return false
	}
	return time.Now().Add(renewalSkew).After(time.Unix(t.ExpiresAt, 0))
}

// grantTypes is what registration asks to be allowed to use.
//
// The refresh grant is requested only where the authorization server advertises
// it. Registering for a grant a server does not offer is the same mistake as
// asking for a scope it never published, and it is the whole request that gets
// rejected -- while a client that never asks for the grant is refused when it
// later tries to use one.
func (c *Coordinator) grantTypes() []string {
	grants := []string{grantTypeAuthorizationCode}
	if c.serverMetadata != nil && containsTrimmed(c.serverMetadata.GrantTypesSupported, grantTypeRefreshToken) {
		grants = append(grants, grantTypeRefreshToken)
	}
	return grants
}

// Renew trades the stored refresh token for a fresh access token and stores the
// result, returning ErrNoRefreshToken when there is nothing to trade.
//
// Callers hold the authorization lock: refresh tokens are commonly single-use,
// so two processes renewing from the same stored token would leave one of them
// holding a value the server has already retired. RenewExclusively is the entry
// point that takes the lock.
func (c *Coordinator) Renew(ctx context.Context, serverURL string) (*Tokens, error) {
	stored, err := c.LoadTokens()
	if err != nil {
		return nil, ErrNoRefreshToken
	}
	if stored.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	c.authMutex.Lock()
	defer c.authMutex.Unlock()

	metadata, clientInfo, err := c.authContext()
	if err != nil {
		return nil, err
	}

	formData := map[string]string{
		"grant_type":    grantTypeRefreshToken,
		"refresh_token": stored.RefreshToken,
		"client_id":     clientInfo.ClientID,
	}

	// RFC 8707 resource indicator, as on every other token request the MCP
	// authorization spec covers.
	if resource := c.renewalResource(serverURL); resource != "" {
		formData["resource"] = resource
	}

	headers := make(map[string]string)
	c.applyClientAuthentication(clientInfo, formData, headers)

	client := httpclient.New(nil)
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := client.PostForm(reqCtx, metadata.TokenEndpoint, formData, headers)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.SafeClose() }()

	var renewed Tokens
	if err := resp.JSON(&renewed); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}
	if renewed.AccessToken == "" {
		return nil, errors.New("refresh response carried no access token")
	}

	// RFC 6749 §6: a new refresh token may or may not come back, and the old
	// one stays valid when it does not. Losing this would turn a server that
	// does not rotate into one that can only ever be renewed once.
	if renewed.RefreshToken == "" {
		renewed.RefreshToken = stored.RefreshToken
	}
	renewed.stampExpiry(time.Now())

	// Storing the rotated token is not optional. A server that retires the old
	// one on use leaves nothing to renew with if this write is lost, and the
	// user is sent back through the browser.
	if err := c.SaveTokens(&renewed); err != nil {
		return nil, fmt.Errorf("failed to store the renewed tokens: %w", err)
	}

	return &renewed, nil
}

// RenewExclusively renews the access token under the per-server authorization
// lock, so that concurrent proxies neither spend the same single-use refresh
// token twice nor renew while another one is authorizing interactively.
//
// When another process gets there first its result is used, which is why this
// returns the stored tokens rather than only what this call produced.
func (c *Coordinator) RenewExclusively(ctx context.Context, serverURL string) (*Tokens, error) {
	var renewed *Tokens

	err := c.AuthorizeExclusively(ctx, func() error {
		tokens, err := c.Renew(ctx, serverURL)
		if err != nil {
			return err
		}
		renewed = tokens
		return nil
	})
	if err != nil {
		return nil, err
	}
	if renewed != nil {
		return renewed, nil
	}

	// The flow was skipped because another process stored a token while this
	// one waited; that token is the answer.
	return c.LoadTokens()
}

// authContext returns the token endpoint and the registered client this server
// was last authorized with.
//
// Both are read from the cache when no flow has run in this process, which is
// the normal case for a renewal: a proxy that starts with a stored token never
// runs discovery, and renewing is the first thing it may need to do.
func (c *Coordinator) authContext() (*ServerMetadata, *ClientInfo, error) {
	if c.serverMetadata == nil {
		metadata, err := c.loadServerMetadata()
		if err != nil {
			return nil, nil, fmt.Errorf("no stored authorization server metadata: %w", err)
		}
		c.serverMetadata = metadata
	}
	if c.serverMetadata.TokenEndpoint == "" {
		return nil, nil, errors.New("stored metadata names no token endpoint")
	}

	if c.clientInfo == nil {
		clientInfo, err := c.loadClientInfo()
		if err != nil {
			return nil, nil, fmt.Errorf("no stored client registration: %w", err)
		}
		c.clientInfo = clientInfo
	}

	return c.serverMetadata, c.clientInfo, nil
}

// renewalResource returns the canonical resource URI for the token request,
// preferring the one the flow in this process already derived.
func (c *Coordinator) renewalResource(serverURL string) string {
	if c.resource != "" {
		return c.resource
	}
	if serverURL == "" {
		return ""
	}
	resource, err := CanonicalResourceURI(serverURL)
	if err != nil {
		log.Printf("Warning: could not derive the resource indicator for renewal: %v", err)
		return ""
	}
	c.resource = resource
	return resource
}
