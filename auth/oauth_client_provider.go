package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net/url"

	"github.com/google/uuid"
	"github.com/pkg/browser"
)

// NodeOAuthClientProvider は OAuth クライアントプロバイダーの実装です
type NodeOAuthClientProvider struct {
	options      OAuthProviderOptions
	serverURLHash string
	callbackPath string
	clientName   string
	clientURI    string
	softwareID   string
	softwareVersion string
}

// NewNodeOAuthClientProvider は新しい NodeOAuthClientProvider を作成します
func NewNodeOAuthClientProvider(options OAuthProviderOptions) *NodeOAuthClientProvider {
	provider := &NodeOAuthClientProvider{
		options:      options,
		serverURLHash: GetServerURLHash(options.ServerURL),
	}

	// デフォルト値の設定
	if options.CallbackPath == "" {
		provider.callbackPath = "/oauth/callback"
	} else {
		provider.callbackPath = options.CallbackPath
	}

	if options.ClientName == "" {
		provider.clientName = "MCP CLI Client"
	} else {
		provider.clientName = options.ClientName
	}

	if options.ClientURI == "" {
		provider.clientURI = "https://github.com/modelcontextprotocol/mcp-cli"
	} else {
		provider.clientURI = options.ClientURI
	}

	if options.SoftwareID == "" {
		provider.softwareID = "2e6dc280-f3c3-4e01-99a7-8181dbd1d23d"
	} else {
		provider.softwareID = options.SoftwareID
	}

	if options.SoftwareVersion == "" {
		provider.softwareVersion = Version
	} else {
		provider.softwareVersion = options.SoftwareVersion
	}

	return provider
}

// GetRedirectURL はリダイレクトURLを取得します
func (p *NodeOAuthClientProvider) GetRedirectURL() string {
	return fmt.Sprintf("http://localhost:%d%s", p.options.CallbackPort, p.callbackPath)
}

// GetClientMetadata はクライアントメタデータを取得します
func (p *NodeOAuthClientProvider) GetClientMetadata() map[string]interface{} {
	return map[string]interface{}{
		"redirect_uris":            []string{p.GetRedirectURL()},
		"token_endpoint_auth_method": "none",
		"grant_types":              []string{"authorization_code", "refresh_token"},
		"response_types":           []string{"code"},
		"client_name":              p.clientName,
		"client_uri":               p.clientURI,
		"software_id":              p.softwareID,
		"software_version":         p.softwareVersion,
	}
}

// ClientInformation は保存されたクライアント情報を取得します
func (p *NodeOAuthClientProvider) ClientInformation() (*OAuthClientInformationFull, error) {
	var clientInfo OAuthClientInformationFull
	err := ReadJSONFile(p.serverURLHash, "client_info.json", &clientInfo)
	if err != nil {
		return nil, err
	}
	
	// 値が空の場合はnilを返す
	if clientInfo.ClientID == "" {
		return nil, nil
	}
	
	return &clientInfo, nil
}

// SaveClientInformation はクライアント情報を保存します
func (p *NodeOAuthClientProvider) SaveClientInformation(clientInformation *OAuthClientInformationFull) error {
	return WriteJSONFile(p.serverURLHash, "client_info.json", clientInformation)
}

// Tokens は保存されたトークンを取得します
func (p *NodeOAuthClientProvider) Tokens() (*OAuthTokens, error) {
	var tokens OAuthTokens
	err := ReadJSONFile(p.serverURLHash, "tokens.json", &tokens)
	if err != nil {
		return nil, err
	}
	
	// 値が空の場合はnilを返す
	if tokens.AccessToken == "" {
		return nil, nil
	}
	
	return &tokens, nil
}

// SaveTokens はトークンを保存します
func (p *NodeOAuthClientProvider) SaveTokens(tokens *OAuthTokens) error {
	return WriteJSONFile(p.serverURLHash, "tokens.json", tokens)
}

// RedirectToAuthorization は認証URLにリダイレクトします
func (p *NodeOAuthClientProvider) RedirectToAuthorization(authorizationURL *url.URL) error {
	log.Printf("\nPlease authorize this client by visiting:\n%s\n", authorizationURL.String())
	
	err := browser.OpenURL(authorizationURL.String())
	if err != nil {
		log.Println("Could not open browser automatically. Please copy and paste the URL above into your browser.")
	} else {
		log.Println("Browser opened automatically.")
	}
	
	return nil
}

// SaveCodeVerifier はPKCEコード検証器を保存します
func (p *NodeOAuthClientProvider) SaveCodeVerifier(codeVerifier string) error {
	return WriteTextFile(p.serverURLHash, "code_verifier.txt", codeVerifier)
}

// CodeVerifier はPKCEコード検証器を取得します
func (p *NodeOAuthClientProvider) CodeVerifier() (string, error) {
	return ReadTextFile(p.serverURLHash, "code_verifier.txt", "No code verifier saved for session")
}

// GetServerURLHash はサーバーURLのハッシュを生成します
func GetServerURLHash(serverURL string) string {
	hash := sha256.Sum256([]byte(serverURL))
	return base64.URLEncoding.EncodeToString(hash[:])[:16]
}

// GenerateCodeVerifier はPKCEコード検証器を生成します
func GenerateCodeVerifier() string {
	return uuid.New().String() + uuid.New().String()
}

// GenerateCodeChallenge はPKCEコードチャレンジを生成します
func GenerateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
}
