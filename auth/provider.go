package auth

import (
	"net/url"
)

// OAuthClientInformationFull は、OAuth クライアント登録情報を表します
type OAuthClientInformationFull struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
}

// OAuthTokens は、OAuth アクセストークンとリフレッシュトークンを表します
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// OAuthClientProvider は、OAuth クライアント機能を提供するインターフェースです
type OAuthClientProvider interface {
	// ClientInformation は、保存されているクライアント情報を取得します
	ClientInformation() (*OAuthClientInformationFull, error)

	// SaveClientInformation は、クライアント情報を保存します
	SaveClientInformation(clientInformation *OAuthClientInformationFull) error

	// Tokens は、保存されているトークンを取得します
	Tokens() (*OAuthTokens, error)

	// SaveTokens は、トークンを保存します
	SaveTokens(tokens *OAuthTokens) error

	// RedirectToAuthorization は、認証URLにユーザーをリダイレクトします
	RedirectToAuthorization(authorizationURL *url.URL) error

	// SaveCodeVerifier は、PKCE コード検証子を保存します
	SaveCodeVerifier(codeVerifier string) error

	// CodeVerifier は、保存されている PKCE コード検証子を取得します
	CodeVerifier() (string, error)

	// GetRedirectURL は、リダイレクトURLを取得します
	GetRedirectURL() string

	// GetClientMetadata は、クライアントメタデータを取得します
	GetClientMetadata() map[string]interface{}
}
