package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/filelock"
	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
)

// OAuth grant types this client uses (RFC 6749 §4.1, §6).
const (
	grantTypeAuthorizationCode = "authorization_code"
	grantTypeRefreshToken      = "refresh_token"
)

// Bounds on the steps an authorization runs while it holds the per-server lock.
// authFlowMaxAge is composed from these, so a change here cannot quietly make
// the staleness threshold too small to cover a flow that is still running.
const (
	// discoveryTimeout bounds the whole discovery chain: protected resource
	// metadata, RFC 8414, OpenID Connect and the fallback, in turn.
	discoveryTimeout = 30 * time.Second

	// registrationTimeout bounds dynamic client registration.
	registrationTimeout = 30 * time.Second

	// tokenRequestTimeout bounds a single call to the token endpoint.
	tokenRequestTimeout = 30 * time.Second
)

// renewalSkew is the most time before expiry that an access token is treated as
// due for renewal. Servers and clients do not share a clock, and a token that
// expires mid-flight costs a failed request, so the last few seconds of a
// lifetime are not worth using.
const renewalSkew = 60 * time.Second

// ErrNoRefreshToken means there is nothing stored to renew with, so the caller
// has to fall back to an interactive authorization. It is an ordinary outcome,
// not a failure: a server may issue no refresh token at all.
var ErrNoRefreshToken = errors.New("no refresh token stored")

// ErrRenewalBusy means another process holds the authorization lock, so this
// one did not renew and the caller keeps the token it already has.
var ErrRenewalBusy = errors.New("another process is authorizing or renewing")

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
	return time.Now().Add(t.renewalLead()).After(time.Unix(t.ExpiresAt, 0))
}

// renewalLead is how far ahead of expiry this particular token is renewed.
//
// A fixed minute of margin is meaningless against a token that only lives for
// one: it would be due from the moment it was issued, and every request would
// renew it. Where the lifetime is known and short, the margin is half of it.
func (t *Tokens) renewalLead() time.Duration {
	if t.ExpiresIn > 0 {
		if half := time.Duration(t.ExpiresIn) * time.Second / 2; half < renewalSkew {
			return half
		}
	}
	return renewalSkew
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
// holding a value the server has already retired. RenewIfUncontested is the
// entry point that takes the lock for the request path; the interactive flow
// already holds it.
func (c *Coordinator) Renew(ctx context.Context, serverURL string) (*Tokens, error) {
	stored, err := c.LoadTokens()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoRefreshToken
		}
		// An unreadable or corrupt token store is not the ordinary "nothing
		// stored to renew with". Reporting it as such would send the user to a
		// browser with no hint of why.
		return nil, fmt.Errorf("could not read the stored tokens: %w", err)
	}
	return c.renewWith(ctx, serverURL, stored)
}

// RenewIfUncontested renews the access token while holding the per-server
// authorization lock, and gives up rather than waiting for it.
//
// The lock is what stops two proxies from spending the same single-use refresh
// token, so renewing without it is not an option. Waiting for it is: this runs
// on the request path, where the caller already holds a token to present, and
// the process holding the lock may be sitting at a browser prompt for minutes.
// Waiting that out belongs to the interactive path, which has nothing else to
// do meanwhile.
func (c *Coordinator) RenewIfUncontested(ctx context.Context, serverURL string, stored *Tokens) (*Tokens, error) {
	lock := filelock.New(c.getAuthLockPath())
	if err := lock.TryLock(); err != nil {
		return nil, ErrRenewalBusy
	}
	defer func() { _ = lock.Unlock() }()

	// Between the caller's read and this lock, another process may have renewed
	// already, which makes this renewal unnecessary and its token the answer.
	if current, err := c.LoadTokens(); err == nil &&
		current.AccessToken != "" && current.AccessToken != stored.AccessToken {
		return current, nil
	}

	return c.renewWith(ctx, serverURL, stored)
}

// renewWith renews from tokens the caller already holds, so that the request
// path does not read and lock the token file again for what is in its hand.
func (c *Coordinator) renewWith(ctx context.Context, serverURL string, stored *Tokens) (*Tokens, error) {
	if stored == nil || stored.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	c.authMutex.Lock()
	defer c.authMutex.Unlock()

	if err := c.loadAuthContext(); err != nil {
		return nil, err
	}

	formData := map[string]string{
		"grant_type":    grantTypeRefreshToken,
		"refresh_token": stored.RefreshToken,
	}

	// RFC 8707 resource indicator, as on every other token request the MCP
	// authorization spec covers.
	if resource := c.renewalResource(serverURL); resource != "" {
		formData["resource"] = resource
	}

	renewed, err := c.requestTokens(ctx, formData)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
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

	// Storing the rotated token is not optional. A server that retires the old
	// one on use leaves nothing to renew with if this write is lost.
	if err := c.SaveTokens(renewed); err != nil {
		return nil, fmt.Errorf("failed to store the renewed tokens: %w", err)
	}

	return renewed, nil
}

// requestTokens posts a grant to the token endpoint and reads the tokens back,
// authenticated the way this client is registered to authenticate.
//
// Both grants this proxy uses come through here: an authorization code exchange
// and a renewal differ only in the form fields their callers fill in.
func (c *Coordinator) requestTokens(ctx context.Context, formData map[string]string) (*Tokens, error) {
	formData["client_id"] = c.clientInfo.ClientID

	headers := make(map[string]string)
	c.applyClientAuthentication(formData, headers)

	reqCtx, cancel := context.WithTimeout(ctx, tokenRequestTimeout)
	defer cancel()

	resp, err := httpclient.New(nil).PostForm(reqCtx, c.serverMetadata.TokenEndpoint, formData, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.SafeClose() }()

	var tokens Tokens
	if err := resp.JSON(&tokens); err != nil {
		return nil, fmt.Errorf("failed to parse the token response: %w", err)
	}

	tokens.stampExpiry(time.Now())
	return &tokens, nil
}

// loadAuthContext makes sure the token endpoint and the registered client this
// server was last authorized with are in hand.
//
// Both come from the cache when no flow has run in this process, which is the
// normal case for a renewal: a proxy that starts with a stored token never runs
// discovery, and renewing is the first thing it may need to do.
func (c *Coordinator) loadAuthContext() error {
	if c.serverMetadata == nil {
		metadata, err := c.loadServerMetadata()
		if err != nil {
			return fmt.Errorf("no stored authorization server metadata: %w", err)
		}
		c.serverMetadata = metadata
	}
	if c.serverMetadata.TokenEndpoint == "" {
		return errors.New("stored metadata names no token endpoint")
	}

	if c.clientInfo == nil {
		clientInfo, err := c.loadClientInfo()
		if err != nil {
			return fmt.Errorf("no stored client registration: %w", err)
		}
		c.clientInfo = clientInfo
	}

	return nil
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
