package auth

import (
	"net/url"
)

// OAuthProviderOptions は OAuth プロバイダーの設定オプションを定義します
type OAuthProviderOptions struct {
	ServerURL       string
	CallbackPort    int
	CallbackPath    string
	ClientName      string
	ClientURI       string
	SoftwareID      string
	SoftwareVersion string
}

// OAuthClientInformationFull は OAuth クライアント情報の完全な構造体を定義します
type OAuthClientInformationFull struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	SoftwareID              string   `json:"software_id"`
	SoftwareVersion         string   `json:"software_version"`
}

// OAuthTokens は OAuth トークン情報を定義します
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// OAuthClientProvider は OAuth クライアントプロバイダーのインターフェースを定義します
type OAuthClientProvider interface {
	// ClientInformation は保存されたクライアント情報を取得します
	ClientInformation() (*OAuthClientInformationFull, error)
	
	// SaveClientInformation はクライアント情報を保存します
	SaveClientInformation(clientInformation *OAuthClientInformationFull) error
	
	// Tokens は保存されたトークンを取得します
	Tokens() (*OAuthTokens, error)
	
	// SaveTokens はトークンを保存します
	SaveTokens(tokens *OAuthTokens) error
	
	// RedirectToAuthorization は認証URLにリダイレクトします
	RedirectToAuthorization(authorizationURL *url.URL) error
	
	// SaveCodeVerifier はPKCEコード検証器を保存します
	SaveCodeVerifier(codeVerifier string) error
	
	// CodeVerifier はPKCEコード検証器を取得します
	CodeVerifier() (string, error)
	
	// GetRedirectURL はリダイレクトURLを取得します
	GetRedirectURL() string
	
	// GetClientMetadata はクライアントメタデータを取得します
	GetClientMetadata() map[string]interface{}
}
