package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthorizationServerMetadata は、認証サーバーのメタデータを表します
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	JwksURI                           string   `json:"jwks_uri,omitempty"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
}

// ProtectedResourceMetadata は、保護されたリソースのメタデータを表します
type ProtectedResourceMetadata struct {
	ResourceURI        string                     `json:"resource_uri"`
	AuthorizationServers []AuthorizationServerRef `json:"authorization_servers"`
}

// AuthorizationServerRef は、認証サーバーへの参照を表します
type AuthorizationServerRef struct {
	AuthorizationServer string `json:"authorization_server"`
}

// OAuthFlow は、OAuth2.1認証フローを処理します
type OAuthFlow struct {
	httpClient      *http.Client
	authProvider    OAuthClientProvider
	serverURL       *url.URL
	authCoordinator AuthCoordinator
	headers         map[string]string
}

// GetServerURL は、サーバーURLを返します
func (f *OAuthFlow) GetServerURL() string {
	return f.serverURL.String()
}

// NewOAuthFlow は、新しい OAuthFlow インスタンスを作成します
func NewOAuthFlow(serverURL string, authProvider OAuthClientProvider, authCoordinator AuthCoordinator, headers map[string]string) (*OAuthFlow, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}

	return &OAuthFlow{
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		authProvider:    authProvider,
		serverURL:       parsedURL,
		authCoordinator: authCoordinator,
		headers:         headers,
	}, nil
}

// Authenticate は、認証プロセスを実行します
func (f *OAuthFlow) Authenticate(ctx context.Context) error {
	// 既存のトークンを確認
	tokens, err := f.authProvider.Tokens()
	if err == nil && tokens != nil && !f.isTokenExpired(tokens) {
		log.Println("Using existing valid tokens")
		return nil
	}

	// リフレッシュトークンがある場合は、それを使用してトークンを更新
	if tokens != nil && tokens.RefreshToken != "" {
		log.Println("Attempting to refresh token")
		_, err := f.refreshToken(ctx, tokens.RefreshToken)
		if err == nil {
			log.Println("Token refreshed successfully")
			return nil
		}
		log.Printf("Token refresh failed: %v, proceeding with full authentication", err)
	}

	// 認証サーバーのメタデータを取得
	asMetadata, err := f.discoverAuthorizationServer(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover authorization server: %w", err)
	}

	// クライアント情報を取得または登録
	clientInfo, err := f.getOrRegisterClient(ctx, asMetadata)
	if err != nil {
		return fmt.Errorf("failed to get or register client: %w", err)
	}

	// 認証を初期化
	authState, err := f.authCoordinator.InitializeAuth()
	if err != nil {
		return fmt.Errorf("failed to initialize auth: %w", err)
	}

	// PKCE パラメータを生成
	codeVerifier, codeChallenge, err := f.generatePKCEParams()
	if err != nil {
		return fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	// コード検証子を保存
	if err := f.authProvider.SaveCodeVerifier(codeVerifier); err != nil {
		return fmt.Errorf("failed to save code verifier: %w", err)
	}

	// 認証URLを構築
	authURL, err := f.buildAuthorizationURL(asMetadata, clientInfo, codeChallenge)
	if err != nil {
		return fmt.Errorf("failed to build authorization URL: %w", err)
	}

	// 別のインスタンスが認証を処理した場合はスキップ
	if !authState.SkipBrowserAuth {
		// ユーザーを認証URLにリダイレクト
		if err := f.authProvider.RedirectToAuthorization(authURL); err != nil {
			return fmt.Errorf("failed to redirect to authorization: %w", err)
		}

		// 認証コードを待機
		authCode, err := authState.WaitForAuthCode()
		if err != nil {
			return fmt.Errorf("failed to get authorization code: %w", err)
		}

		// 認証コードをトークンに交換
		if err := f.exchangeCodeForTokens(ctx, asMetadata, clientInfo, authCode, codeVerifier); err != nil {
			return fmt.Errorf("failed to exchange code for tokens: %w", err)
		}
	} else {
		// 別のインスタンスが認証を完了したので、少し待ってからトークンを使用
		log.Println("Authentication was completed by another instance - will use tokens from disk...")
		time.Sleep(1 * time.Second)
	}

	return nil
}

// isTokenExpired は、トークンが期限切れかどうかを確認します
func (f *OAuthFlow) isTokenExpired(tokens *OAuthTokens) bool {
	// ExpiresAtがない場合は、期限切れとみなす
	if tokens.ExpiresAt == 0 {
		return true
	}

	// 現在時刻と比較（30秒のバッファを追加）
	return time.Now().Unix() >= tokens.ExpiresAt-30
}

// discoverAuthorizationServer は、認証サーバーのメタデータを取得します
func (f *OAuthFlow) discoverAuthorizationServer(ctx context.Context) (*AuthorizationServerMetadata, error) {
	// まず、認証なしでリソースサーバーにアクセスを試みる
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.serverURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// ヘッダーを設定
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 401 Unauthorized レスポンスを期待
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// WWW-Authenticate ヘッダーからリソースメタデータURLを抽出
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if wwwAuth == "" {
		return nil, fmt.Errorf("WWW-Authenticate header not found")
	}

	resourceMetadataURL := f.extractResourceMetadataURL(wwwAuth)
	if resourceMetadataURL == "" {
		return nil, fmt.Errorf("resource metadata URL not found in WWW-Authenticate header")
	}

	// メタデータの取得を試みる
	// まず、直接認証サーバーメタデータとして取得を試みる
	asMetadata, err := f.tryFetchDirectAuthorizationServerMetadata(ctx, resourceMetadataURL)
	if err == nil {
		// 直接認証サーバーメタデータを取得できた場合
		log.Printf("Successfully fetched authorization server metadata directly")
		return asMetadata, nil
	}
	
	// 直接取得できなかった場合は、リソースメタデータとして取得を試みる
	log.Printf("Failed to fetch authorization server metadata directly: %v. Trying resource metadata instead.", err)
	resourceMetadata, err := f.fetchResourceMetadata(ctx, resourceMetadataURL)
	if err != nil {
		return nil, err
	}

	// 認証サーバーが指定されていない場合はエラー
	if len(resourceMetadata.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("no authorization servers specified in resource metadata")
	}

	// 最初の認証サーバーを使用
	asURL := resourceMetadata.AuthorizationServers[0].AuthorizationServer

	// 認証サーバーのメタデータを取得
	asMetadata, err = f.fetchAuthorizationServerMetadata(ctx, asURL)
	if err != nil {
		return nil, err
	}

	return asMetadata, nil
}

// extractResourceMetadataURL は、WWW-Authenticate ヘッダーからリソースメタデータを抽出します
func (f *OAuthFlow) extractResourceMetadataURL(wwwAuth string) string {
	// デバッグ情報を追加
	log.Printf("WWW-Authenticate header: %s", wwwAuth)

	// Bearer プレフィックスのチェック
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		log.Printf("Bearer prefix not found, trying alternative formats")
		// Bearerプレフィックスがない場合も解析を試みる
		parts := strings.Split(wwwAuth, " ")
		for _, part := range parts {
			if strings.HasPrefix(part, "http") {
				log.Printf("Found direct URL without Bearer prefix: %s", part)
				return part
			}
		}
		// どのパターンにも当てはまらない場合はサーバーのドメインを使用
		domain := f.serverURL.Hostname()
		metadataURL := fmt.Sprintf("https://%s/.well-known/oauth-authorization-server", domain)
		log.Printf("Using server domain for metadata URL: %s", metadataURL)
		return metadataURL
	}



	// 標準形式の解析
	parts := strings.Split(wwwAuth[7:], " ")
	for _, part := range parts {
		// resource_metadata="<url>" 形式
		if strings.HasPrefix(part, "resource_metadata=") {
			url := strings.Trim(part[18:], "\"")
			log.Printf("Found resource_metadata URL: %s", url)
			return url
		}
		
		// resource=<url> 形式
		if strings.HasPrefix(part, "resource=") {
			url := strings.Trim(part[9:], "\"")
			log.Printf("Found resource URL: %s", url)
			return url + "/.well-known/oauth-authorization-server"
		}
	}

	// 他の形式を試す
	// URLを直接含む場合
	for _, part := range strings.Split(wwwAuth, " ") {
		if strings.HasPrefix(part, "http") {
			log.Printf("Found direct URL: %s", part)
			return part
		}
	}

	// どのパターンにも当てはまらない場合はサーバーのドメインを使用
	domain := f.serverURL.Hostname()
	metadataURL := fmt.Sprintf("https://%s/.well-known/oauth-authorization-server", domain)
	log.Printf("Using server domain for metadata URL: %s", metadataURL)
	return metadataURL
}

// fetchResourceMetadata は、リソースメタデータを取得します
func (f *OAuthFlow) fetchResourceMetadata(ctx context.Context, metadataURL string) (*ProtectedResourceMetadata, error) {
	log.Printf("Fetching resource metadata from URL: %s", metadataURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch resource metadata: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("Resource metadata response status: %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch resource metadata: status code %d", resp.StatusCode)
	}

	// レスポンスボディを読み込む
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	
	// デバッグ用にレスポンスを表示
	log.Printf("Resource metadata response body: %s", string(bodyBytes))

	var metadata ProtectedResourceMetadata
	if err := json.Unmarshal(bodyBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse resource metadata: %w", err)
	}

	// デコード後のデータを確認
	log.Printf("Decoded resource metadata: ResourceURI=%s, AuthorizationServers=%+v", 
		metadata.ResourceURI, metadata.AuthorizationServers)

	return &metadata, nil
}

// tryFetchDirectAuthorizationServerMetadata は、URLから直接認証サーバーメタデータを取得します
func (f *OAuthFlow) tryFetchDirectAuthorizationServerMetadata(ctx context.Context, metadataURL string) (*AuthorizationServerMetadata, error) {
	log.Printf("Trying to fetch authorization server metadata directly from URL: %s", metadataURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("Authorization server metadata response status: %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: status code %d", resp.StatusCode)
	}

	// レスポンスボディを読み込む
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	
	// デバッグ用にレスポンスを表示
	log.Printf("Authorization server metadata response body: %s", string(bodyBytes))

	var metadata AuthorizationServerMetadata
	if err := json.Unmarshal(bodyBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse authorization server metadata: %w", err)
	}

	// 必要なフィールドがあるか確認
	if metadata.Issuer == "" || metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" {
		return nil, fmt.Errorf("incomplete authorization server metadata")
	}

	// デコード後のデータを確認
	log.Printf("Decoded authorization server metadata: Issuer=%s, AuthEndpoint=%s, TokenEndpoint=%s", 
		metadata.Issuer, metadata.AuthorizationEndpoint, metadata.TokenEndpoint)

	return &metadata, nil
}

// fetchAuthorizationServerMetadata は、認証サーバーのメタデータを取得します
func (f *OAuthFlow) fetchAuthorizationServerMetadata(ctx context.Context, asURL string) (*AuthorizationServerMetadata, error) {
	// .well-known/oauth-authorization-server エンドポイントを構築
	wellKnownURL := asURL
	if !strings.HasSuffix(wellKnownURL, "/") {
		wellKnownURL += "/"
	}
	wellKnownURL += ".well-known/oauth-authorization-server"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: status code %d", resp.StatusCode)
	}

	var metadata AuthorizationServerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse authorization server metadata: %w", err)
	}

	return &metadata, nil
}

// getOrRegisterClient は、クライアント情報を取得または登録します
func (f *OAuthFlow) getOrRegisterClient(ctx context.Context, asMetadata *AuthorizationServerMetadata) (*OAuthClientInformationFull, error) {
	// 既存のクライアント情報を確認
	clientInfo, err := f.authProvider.ClientInformation()
	if err == nil && clientInfo != nil {
		log.Println("Using existing client information")
		return clientInfo, nil
	}

	// 登録エンドポイントがない場合はエラー
	if asMetadata.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("authorization server does not support dynamic client registration")
	}

	// クライアントを登録
	clientMetadata := f.authProvider.GetClientMetadata()
	jsonData, err := json.Marshal(clientMetadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal client metadata: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, asMetadata.RegistrationEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to register client: status code %d, body: %s", resp.StatusCode, body)
	}

	var newClientInfo OAuthClientInformationFull
	if err := json.NewDecoder(resp.Body).Decode(&newClientInfo); err != nil {
		return nil, fmt.Errorf("failed to parse client registration response: %w", err)
	}

	// クライアント情報を保存
	if err := f.authProvider.SaveClientInformation(&newClientInfo); err != nil {
		return nil, fmt.Errorf("failed to save client information: %w", err)
	}

	return &newClientInfo, nil
}

// generatePKCEParams は、PKCE パラメータを生成します
func (f *OAuthFlow) generatePKCEParams() (string, string, error) {
	// コード検証子を生成（ランダムな43-128文字のASCII文字列）
	b := make([]byte, 64) // 64バイト = 512ビット
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(b)

	// コードチャレンジを生成（SHA-256ハッシュのbase64url表現）
	h := sha256.New()
	h.Write([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	return codeVerifier, codeChallenge, nil
}

// buildAuthorizationURL は、認証URLを構築します
func (f *OAuthFlow) buildAuthorizationURL(asMetadata *AuthorizationServerMetadata, clientInfo *OAuthClientInformationFull, codeChallenge string) (*url.URL, error) {
	authURL, err := url.Parse(asMetadata.AuthorizationEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid authorization endpoint: %w", err)
	}

	q := authURL.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientInfo.ClientID)
	q.Set("redirect_uri", f.authProvider.GetRedirectURL())
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", fmt.Sprintf("%d", time.Now().UnixNano()))
	authURL.RawQuery = q.Encode()

	return authURL, nil
}

// exchangeCodeForTokens は、認証コードをトークンに交換します
func (f *OAuthFlow) exchangeCodeForTokens(ctx context.Context, asMetadata *AuthorizationServerMetadata, clientInfo *OAuthClientInformationFull, authCode, codeVerifier string) error {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", authCode)
	data.Set("redirect_uri", f.authProvider.GetRedirectURL())
	data.Set("client_id", clientInfo.ClientID)
	data.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, asMetadata.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to exchange code for tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token exchange failed: status code %d, body: %s", resp.StatusCode, body)
	}

	var tokens OAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return fmt.Errorf("failed to parse token response: %w", err)
	}

	// ExpiresAtを計算
	if tokens.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().Unix() + int64(tokens.ExpiresIn)
	}

	// トークンを保存
	if err := f.authProvider.SaveTokens(&tokens); err != nil {
		return fmt.Errorf("failed to save tokens: %w", err)
	}

	return nil
}

// refreshToken は、リフレッシュトークンを使用してトークンを更新します
func (f *OAuthFlow) refreshToken(ctx context.Context, refreshToken string) (*OAuthTokens, error) {
	// 認証サーバーのメタデータを取得
	asMetadata, err := f.discoverAuthorizationServer(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover authorization server: %w", err)
	}

	// クライアント情報を取得
	clientInfo, err := f.authProvider.ClientInformation()
	if err != nil || clientInfo == nil {
		return nil, fmt.Errorf("client information not found: %w", err)
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientInfo.ClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, asMetadata.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed: status code %d, body: %s", resp.StatusCode, body)
	}

	var tokens OAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// ExpiresAtを計算
	if tokens.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().Unix() + int64(tokens.ExpiresIn)
	}

	// リフレッシュトークンが返されなかった場合は、古いものを保持
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}

	// トークンを保存
	if err := f.authProvider.SaveTokens(&tokens); err != nil {
		return nil, fmt.Errorf("failed to save tokens: %w", err)
	}

	return &tokens, nil
}
