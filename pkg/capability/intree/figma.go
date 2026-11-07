// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package intree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ports"
)

// The figma capability server equips UI tasks with Figma design context and
// short-lived OAuth tokens without exposing root Figma credentials to the
// sandbox:
//
//   - figma#mcp: an attempt-scoped MCP server (/runs/<attempt_id>/mcp) with
//     tools to inspect Figma files, extract structured CSS/layout/typography
//     design specs from frames, read styles/variables and comments, and render
//     reference node images (downloaded server-side so airgapped sandboxes
//     receive the PNG/SVG bytes directly).
//   - figma#token: optional short-lived OAuth 2.0 access token generation
//     (minted via POST /v1/oauth/refresh and placed at /home/debian/.figma-token
//     0600), offered only when at least one OAuth account sets
//     allow_sandbox_token: true. Long-lived personal access tokens and OAuth
//     refresh tokens never enter a sandbox.

const (
	// FigmaName is the built-in capability server's name.
	FigmaName = "figma"
	// FigmaMCPMethod is figma#mcp.
	FigmaMCPMethod = "mcp"
	// FigmaTokenMethod is figma#token.
	FigmaTokenMethod = "token"

	// DefaultFigmaPort is the data-plane listener's default loopback port.
	DefaultFigmaPort = ports.Figma

	// FigmaAPIBase is Figma's REST and OAuth token API origin.
	FigmaAPIBase = "https://api.figma.com"
	// FigmaOAuthAuthorizeURL is Figma's browser OAuth 2.0 consent endpoint.
	FigmaOAuthAuthorizeURL = "https://www.figma.com/oauth"

	// FigmaAuthOAuth authenticates by refreshing short-lived OAuth 2.0 tokens.
	FigmaAuthOAuth = "oauth"
	// FigmaAuthPAT authenticates with a Figma personal access token (X-Figma-Token).
	FigmaAuthPAT = "pat"

	// FigmaTokenPath is where figma#token writes a short-lived OAuth access token.
	FigmaTokenPath = "/home/debian/.figma-token"

	// FigmaTestTool is the control-plane tool Settings calls to verify an account.
	FigmaTestTool = "test_account"
	// FigmaAuthorizeURLTool builds the Figma OAuth 2.0 authorization URL.
	FigmaAuthorizeURLTool = "oauth_authorize_url"

	defaultFigmaPerAttempt = 60
	maxFigmaPerAttempt     = 500
	defaultFigmaScopes     = "file_content:read,file_metadata:read,file_comments:read"
	figmaHTTPTimeout       = 45 * time.Second
	figmaTokenSafetyMargin = 2 * time.Minute
	maxFigmaJSONBytes      = 8 * 1024 * 1024
	maxFigmaImageBytes     = 10 * 1024 * 1024
)

var (
	figmaAccountNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	figmaFileKeyPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	figmaScopePattern       = regexp.MustCompile(`^[a-z0-9_:., -]{1,256}$`)
)

// Default allowed host suffixes for Figma's rendered image export URLs.
var defaultFigmaImageHostSuffixes = []string{
	".figma.com",
	".amazonaws.com",
}

// FigmaAccount is one configured Figma credential profile.
type FigmaAccount struct {
	Name              string
	AuthMode          string
	ClientID          string
	Scopes            string
	AllowedFileKeys   []string
	AllowSandboxToken bool

	// Secrets resolved from secret fields or environment; never logged or exported.
	PersonalAccessToken string
	ClientSecret        string
	RefreshToken        string
}

// FigmaConfig is the parsed configuration of a figma capability server.
type FigmaConfig struct {
	Token      string
	PerAttempt int
	Accounts   []FigmaAccount
}

func (c *FigmaConfig) account(name string) *FigmaAccount {
	if c == nil {
		return nil
	}
	if name == "" && len(c.Accounts) == 1 {
		return &c.Accounts[0]
	}
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i]
		}
	}
	return nil
}

// FigmaServerConfig parameterizes NewFigma (with test hooks for HTTP and clock).
type FigmaServerConfig struct {
	Listen              string
	Token               string
	APIBase             string
	AuthorizeURL        string
	Client              *http.Client
	ImageHostSuffixes   []string
	AllowLoopbackImages bool
	Env                 Env
	Now                 func() time.Time
	Log                 *log.Logger
}

// FigmaAudit records one Figma MCP call or token mint during an attempt.
type FigmaAudit struct {
	Time      time.Time `json:"time"`
	AttemptID string    `json:"attempt_id"`
	Account   string    `json:"account"`
	Operation string    `json:"operation"`
	FileKey   string    `json:"file_key,omitempty"`
	NodeCount int       `json:"node_count,omitempty"`
	Refused   string    `json:"refused,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// FigmaTestResult is returned by the test_account control-plane tool.
type FigmaTestResult struct {
	Account   string `json:"account"`
	AuthMode  string `json:"auth_mode"`
	UserID    string `json:"user_id,omitempty"`
	Handle    string `json:"handle,omitempty"`
	Email     string `json:"email,omitempty"`
	ExpiresIn int    `json:"expires_in,omitempty"`
	Error     string `json:"error,omitempty"`
}

// FigmaOAuthTokenResponse is Figma's OAuth 2.0 token exchange / refresh payload.
type FigmaOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	UserIDString string `json:"user_id_string,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

// FigmaAuthorizeURLResult is returned by oauth_authorize_url.
type FigmaAuthorizeURLResult struct {
	Account      string `json:"account"`
	AuthorizeURL string `json:"authorize_url"`
	Scopes       string `json:"scopes"`
}

func figmaConfigSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"accounts": map[string]any{
				"type":        "array",
				"title":       "Figma accounts",
				"description": "Named Figma credentials tasks may be granted. Supports OAuth 2.0 refresh tokens (for short-lived token generation) and Personal Access Tokens.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"name", "auth_mode"},
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"title":       "Account name",
							"pattern":     figmaAccountNamePattern.String(),
							"description": "Short lowercase identifier used in grants (e.g. default, design-system).",
						},
						"auth_mode": map[string]any{
							"type":        "string",
							"title":       "Authentication mode",
							"enum":        []any{FigmaAuthOAuth, FigmaAuthPAT},
							"description": "oauth refreshes short-lived access tokens from an OAuth 2.0 app; pat uses a Figma personal access token.",
						},
						"client_id": map[string]any{
							"type":        "string",
							"title":       "OAuth client ID",
							"description": "Required when auth_mode is oauth: the Figma OAuth app's client ID.",
						},
						"scopes": map[string]any{
							"type":        "string",
							"title":       "OAuth scopes",
							"description": "Comma-separated scopes requested during OAuth authorization (defaults to " + defaultFigmaScopes + ").",
						},
						"allowed_file_keys": map[string]any{
							"type":        "array",
							"title":       "Allowed Figma file keys",
							"description": "Optional allowlist of Figma file keys this account may read. Leave empty to allow any file accessible to the credential.",
							"items":       map[string]any{"type": "string", "pattern": figmaFileKeyPattern.String()},
						},
						"allow_sandbox_token": map[string]any{
							"type":        "boolean",
							"title":       "Allow short-lived token placement (figma#token)",
							"description": "For oauth accounts only: permit figma#token grants to mint a short-lived OAuth access token into /home/debian/.figma-token.",
						},
					},
				},
			},
			"per_attempt": map[string]any{
				"type":        "integer",
				"title":       "Max API calls per attempt",
				"minimum":     1,
				"maximum":     maxFigmaPerAttempt,
				"description": fmt.Sprintf("Maximum Figma MCP operations per task attempt (defaults to %d).", defaultFigmaPerAttempt),
			},
			"personal_access_tokens": map[string]any{
				"type":        "string",
				"writeOnly":   true,
				"title":       "Personal access tokens (JSON map)",
				"description": `JSON object mapping pat account names to Figma personal access tokens, e.g. {"default":"figd_..."}. Stored only in Grain's encrypted secrets store.`,
			},
			"client_secrets": map[string]any{
				"type":        "string",
				"writeOnly":   true,
				"title":       "OAuth client secrets (JSON map)",
				"description": `JSON object mapping oauth account names to Figma OAuth client secrets, e.g. {"default":"..."}. Stored only in Grain's encrypted secrets store.`,
			},
			"refresh_tokens": map[string]any{
				"type":        "string",
				"writeOnly":   true,
				"title":       "OAuth refresh tokens (JSON map)",
				"description": `JSON object mapping oauth account names to Figma OAuth refresh tokens, e.g. {"default":"..."}. Can be populated automatically via OAuth code exchange in Settings.`,
			},
			protocol.DataPlaneTokenConfig: map[string]any{
				"type":        "string",
				"writeOnly":   true,
				"title":       "Data-plane token",
				"minLength":   MinTokenBytes,
				"description": DataPlaneTokenDescription,
			},
		},
	}
}

func decodeSecretStringMap(raw any, field string) (map[string]string, error) {
	s, _ := raw.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object mapping account names to strings", field)
	}
	return m, nil
}

// parseFigmaConfig validates config (without logging or echoing secret values).
// When checkSecrets is false (CheckBuiltinConfig ahead of secret resolution),
// secret presence checks are deferred to configure().
func parseFigmaConfig(config map[string]any, env Env, checkSecrets bool) (*FigmaConfig, error) {
	out := &FigmaConfig{PerAttempt: defaultFigmaPerAttempt}
	if config != nil {
		out.Token, _ = config[protocol.DataPlaneTokenConfig].(string)
		if v, ok := intFromAny(config["per_attempt"]); ok {
			if v < 1 || v > maxFigmaPerAttempt {
				return nil, fmt.Errorf("per_attempt must be between 1 and %d", maxFigmaPerAttempt)
			}
			out.PerAttempt = v
		}
	}
	var pats, clientSecrets, refreshTokens map[string]string
	if checkSecrets && config != nil {
		var err error
		if pats, err = decodeSecretStringMap(config["personal_access_tokens"], "personal_access_tokens"); err != nil {
			return nil, err
		}
		if clientSecrets, err = decodeSecretStringMap(config["client_secrets"], "client_secrets"); err != nil {
			return nil, err
		}
		if refreshTokens, err = decodeSecretStringMap(config["refresh_tokens"], "refresh_tokens"); err != nil {
			return nil, err
		}
	}

	var rawAccounts []any
	if config != nil {
		rawAccounts, _ = config["accounts"].([]any)
	}
	seen := map[string]bool{}
	for i, item := range rawAccounts {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("accounts[%d] must be an object", i)
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if !figmaAccountNamePattern.MatchString(name) {
			return nil, fmt.Errorf("accounts[%d].name %q must match %s", i, name, figmaAccountNamePattern.String())
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate Figma account name %q", name)
		}
		seen[name] = true

		mode, _ := m["auth_mode"].(string)
		mode = strings.TrimSpace(mode)
		if mode != FigmaAuthOAuth && mode != FigmaAuthPAT {
			return nil, fmt.Errorf("account %q: auth_mode must be %q or %q", name, FigmaAuthOAuth, FigmaAuthPAT)
		}
		clientID, _ := m["client_id"].(string)
		clientID = strings.TrimSpace(clientID)
		scopes, _ := m["scopes"].(string)
		scopes = strings.TrimSpace(scopes)
		if scopes == "" {
			scopes = defaultFigmaScopes
		} else if !figmaScopePattern.MatchString(scopes) {
			return nil, fmt.Errorf("account %q: invalid scopes format", name)
		}
		allowToken, _ := m["allow_sandbox_token"].(bool)
		if allowToken && mode != FigmaAuthOAuth {
			return nil, fmt.Errorf("account %q: allow_sandbox_token is supported only for auth_mode %q (personal access tokens do not expire and are never placed in sandboxes)", name, FigmaAuthOAuth)
		}
		if mode == FigmaAuthOAuth && clientID == "" {
			return nil, fmt.Errorf("account %q: client_id is required when auth_mode is %q", name, FigmaAuthOAuth)
		}

		var allowedKeys []string
		if rawKeys, ok := m["allowed_file_keys"].([]any); ok {
			keySeen := map[string]bool{}
			for _, rk := range rawKeys {
				ks, _ := rk.(string)
				ks = strings.TrimSpace(ks)
				if !figmaFileKeyPattern.MatchString(ks) {
					return nil, fmt.Errorf("account %q: invalid file key %q in allowed_file_keys", name, ks)
				}
				if !keySeen[ks] {
					keySeen[ks] = true
					allowedKeys = append(allowedKeys, ks)
				}
			}
		}

		acc := FigmaAccount{
			Name:              name,
			AuthMode:          mode,
			ClientID:          clientID,
			Scopes:            scopes,
			AllowedFileKeys:   allowedKeys,
			AllowSandboxToken: allowToken,
		}
		if checkSecrets {
			switch mode {
			case FigmaAuthPAT:
				acc.PersonalAccessToken = strings.TrimSpace(pats[name])
			case FigmaAuthOAuth:
				acc.ClientSecret = strings.TrimSpace(clientSecrets[name])
				acc.RefreshToken = strings.TrimSpace(refreshTokens[name])
			}
		}
		out.Accounts = append(out.Accounts, acc)
	}

	// Environment fallback when no accounts array is configured.
	if len(out.Accounts) == 0 && env != nil {
		if pat := strings.TrimSpace(env("FIGMA_ACCESS_TOKEN")); pat != "" {
			out.Accounts = append(out.Accounts, FigmaAccount{
				Name:                "default",
				AuthMode:            FigmaAuthPAT,
				Scopes:              defaultFigmaScopes,
				PersonalAccessToken: pat,
			})
		} else if cid := strings.TrimSpace(env("FIGMA_CLIENT_ID")); cid != "" {
			out.Accounts = append(out.Accounts, FigmaAccount{
				Name:              "default",
				AuthMode:          FigmaAuthOAuth,
				ClientID:          cid,
				Scopes:            defaultFigmaScopes,
				ClientSecret:      strings.TrimSpace(env("FIGMA_CLIENT_SECRET")),
				RefreshToken:      strings.TrimSpace(env("FIGMA_REFRESH_TOKEN")),
				AllowSandboxToken: env("FIGMA_ALLOW_SANDBOX_TOKEN") == "1",
			})
		}
	}
	return out, nil
}

func intFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if float64(int(n)) == n {
			return int(n), true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// BuildFigmaOAuthAuthorizeURL returns the Figma OAuth 2.0 browser authorization URL.
func BuildFigmaOAuthAuthorizeURL(authorizeBase, clientID, redirectURI, scopes, state string) (string, error) {
	if strings.TrimSpace(clientID) == "" {
		return "", errors.New("client_id is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return "", errors.New("redirect_uri is required")
	}
	if strings.TrimSpace(state) == "" {
		return "", errors.New("state is required")
	}
	if strings.TrimSpace(scopes) == "" {
		scopes = defaultFigmaScopes
	}
	if authorizeBase == "" {
		authorizeBase = FigmaOAuthAuthorizeURL
	}
	u, err := url.Parse(authorizeBase)
	if err != nil {
		return "", fmt.Errorf("invalid authorize URL: %w", err)
	}
	q := u.Query()
	q.Set("client_id", strings.TrimSpace(clientID))
	q.Set("redirect_uri", strings.TrimSpace(redirectURI))
	q.Set("scope", strings.TrimSpace(scopes))
	q.Set("state", strings.TrimSpace(state))
	q.Set("response_type", "code")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeFigmaOAuthCode exchanges a Figma OAuth 2.0 authorization code for an
// access_token and refresh_token via POST <apiBase>/v1/oauth/token.
func ExchangeFigmaOAuthCode(ctx context.Context, client *http.Client, apiBase, clientID, clientSecret, code, redirectURI string) (*FigmaOAuthTokenResponse, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("client_id and client_secret are required for Figma OAuth token exchange")
	}
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("authorization code is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, errors.New("redirect_uri is required")
	}
	if apiBase == "" {
		apiBase = FigmaAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: figmaHTTPTimeout}
	}
	form := url.Values{}
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("code", strings.TrimSpace(code))
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiBase, "/")+"/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+figmaBasicAuth(clientID, clientSecret))
	return doFigmaOAuthRequest(client, req)
}

// RefreshFigmaOAuthToken mints a fresh short-lived OAuth 2.0 access token from
// a refresh token via POST <apiBase>/v1/oauth/refresh.
func RefreshFigmaOAuthToken(ctx context.Context, client *http.Client, apiBase, clientID, clientSecret, refreshToken string) (*FigmaOAuthTokenResponse, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("client_id and client_secret are required for Figma OAuth refresh")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("refresh_token is not configured yet (generate one via OAuth in Settings or set refresh_tokens)")
	}
	if apiBase == "" {
		apiBase = FigmaAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: figmaHTTPTimeout}
	}
	form := url.Values{}
	form.Set("refresh_token", strings.TrimSpace(refreshToken))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiBase, "/")+"/v1/oauth/refresh", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+figmaBasicAuth(clientID, clientSecret))
	return doFigmaOAuthRequest(client, req)
}

func figmaBasicAuth(clientID, clientSecret string) string {
	raw := strings.TrimSpace(clientID) + ":" + strings.TrimSpace(clientSecret)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func doFigmaOAuthRequest(client *http.Client, req *http.Request) (*FigmaOAuthTokenResponse, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting Figma OAuth endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("reading Figma OAuth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			Message          string `json:"message"`
		}
		_ = json.Unmarshal(body, &errBody)
		detail := strings.TrimSpace(errBody.ErrorDescription)
		if detail == "" {
			detail = strings.TrimSpace(errBody.Message)
		}
		if detail == "" {
			detail = strings.TrimSpace(errBody.Error)
		}
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("Figma OAuth token request failed (%d): %s", resp.StatusCode, detail)
	}
	var tok FigmaOAuthTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("decoding Figma OAuth response: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, errors.New("Figma OAuth response contained no access_token")
	}
	return &tok, nil
}

// figmaAccountClient manages authentication headers and OAuth token caching for one FigmaAccount.
type figmaAccountClient struct {
	acc    FigmaAccount
	base   string
	client *http.Client
	now    func() time.Time

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

func (a *figmaAccountClient) setAuthHeader(ctx context.Context, req *http.Request) error {
	switch a.acc.AuthMode {
	case FigmaAuthPAT:
		if a.acc.PersonalAccessToken == "" {
			return fmt.Errorf("account %q has no personal access token configured in personal_access_tokens", a.acc.Name)
		}
		req.Header.Set("X-Figma-Token", a.acc.PersonalAccessToken)
		return nil
	case FigmaAuthOAuth:
		tok, _, err := a.accessToken(ctx, false)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	default:
		return fmt.Errorf("unsupported auth_mode %q", a.acc.AuthMode)
	}
}

// accessToken returns a valid OAuth 2.0 access token, refreshing when expired or forceFresh is true.
func (a *figmaAccountClient) accessToken(ctx context.Context, forceFresh bool) (string, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if !forceFresh && a.cachedToken != "" && now.Add(figmaTokenSafetyMargin).Before(a.expiresAt) {
		return a.cachedToken, int(a.expiresAt.Sub(now).Seconds()), nil
	}
	if a.acc.ClientSecret == "" {
		return "", 0, fmt.Errorf("account %q has no OAuth client secret configured in client_secrets", a.acc.Name)
	}
	if a.acc.RefreshToken == "" {
		return "", 0, fmt.Errorf("account %q has no OAuth refresh token configured yet (generate one via OAuth in Settings or set refresh_tokens)", a.acc.Name)
	}
	res, err := RefreshFigmaOAuthToken(ctx, a.client, a.base, a.acc.ClientID, a.acc.ClientSecret, a.acc.RefreshToken)
	if err != nil {
		return "", 0, err
	}
	ttl := time.Duration(res.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	a.cachedToken = res.AccessToken
	a.expiresAt = now.Add(ttl)
	return res.AccessToken, int(ttl.Seconds()), nil
}

func (a *figmaAccountClient) getJSON(ctx context.Context, apiPath string, query url.Values) ([]byte, int, error) {
	u := strings.TrimRight(a.base, "/") + apiPath
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if err := a.setAuthHeader(ctx, req); err != nil {
		return nil, 0, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling Figma API %s: %w", apiPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFigmaJSONBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading Figma API %s response: %w", apiPath, err)
	}
	return body, resp.StatusCode, nil
}

type figmaMeResponse struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Email  string `json:"email"`
}

func (a *figmaAccountClient) verifyIdentity(ctx context.Context) (*figmaMeResponse, int, error) {
	var expiresIn int
	if a.acc.AuthMode == FigmaAuthOAuth {
		var err error
		_, expiresIn, err = a.accessToken(ctx, false)
		if err != nil {
			return nil, 0, err
		}
	}
	body, status, err := a.getJSON(ctx, "/v1/me", nil)
	if err != nil {
		return nil, 0, err
	}
	if status < 200 || status >= 300 {
		return nil, 0, fmt.Errorf("Figma /v1/me returned HTTP %d", status)
	}
	var me figmaMeResponse
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, 0, fmt.Errorf("decoding Figma /v1/me: %w", err)
	}
	return &me, expiresIn, nil
}

type figmaServer struct {
	cfg       FigmaServerConfig
	srv       *Server
	listening bool

	mu         sync.Mutex
	conf       *FigmaConfig
	clients    map[string]*figmaAccountClient
	mcpGrants  map[string]*figmaMCPGrant
	mcpRetired map[string][]FigmaAudit
	tokRetired map[string][]FigmaAudit
}

// NewFigma creates an in-tree figma capability server. Like gchat, it offers
// no methods and binds no data-plane port until at least one Figma account is
// configured.
func NewFigma(cfg FigmaServerConfig) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.APIBase == "" {
		cfg.APIBase = FigmaAPIBase
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = FigmaOAuthAuthorizeURL
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: figmaHTTPTimeout}
	}
	if len(cfg.ImageHostSuffixes) == 0 {
		cfg.ImageHostSuffixes = defaultFigmaImageHostSuffixes
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:" + strconv.Itoa(DefaultFigmaPort)
	}
	f := &figmaServer{
		cfg:        cfg,
		clients:    map[string]*figmaAccountClient{},
		mcpGrants:  map[string]*figmaMCPGrant{},
		mcpRetired: map[string][]FigmaAudit{},
		tokRetired: map[string][]FigmaAudit{},
	}
	s, err := newServer(FigmaName, cfg.Log)
	if err != nil {
		return nil, err
	}
	f.cfg.Log = s.Log
	f.srv = s
	s.onClose(f.closeAll)

	if err := s.HandleTool(
		FigmaTestTool,
		"Verify a configured Figma account's token or OAuth refresh against Figma /v1/me without exposing credentials. Called by Grain Settings.",
		map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"account": map[string]any{"type": "string"}},
		},
		f.testAccountTool,
	); err != nil {
		s.Close(context.Background())
		return nil, err
	}

	if err := s.HandleTool(
		FigmaAuthorizeURLTool,
		"Build the Figma OAuth 2.0 browser authorization URL for a configured oauth account.",
		map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"account", "redirect_uri", "state"},
			"properties": map[string]any{
				"account":      map[string]any{"type": "string"},
				"redirect_uri": map[string]any{"type": "string"},
				"state":        map[string]any{"type": "string"},
				"scopes":       map[string]any{"type": "string"},
			},
		},
		f.authorizeURLTool,
	); err != nil {
		s.Close(context.Background())
		return nil, err
	}

	if err := s.configurable(figmaConfigSchema(), f.configure); err != nil {
		s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

func (f *figmaServer) configure(ctx context.Context, config map[string]any) ([]*protocol.Method, error) {
	c, err := parseFigmaConfig(config, f.cfg.Env, true)
	if err != nil {
		return nil, fmt.Errorf("figma: %w", err)
	}
	if c.Token == "" {
		c.Token = f.cfg.Token
	}
	if len(c.Accounts) == 0 {
		f.mu.Lock()
		f.conf, f.clients = c, nil
		f.mu.Unlock()
		return nil, nil
	}
	if len(c.Token) < MinTokenBytes {
		return nil, fmt.Errorf("figma offers no method until it has a data-plane token: %s", MissingDataPlaneToken)
	}
	if !f.listening {
		if _, err := f.srv.listen(f.cfg.Listen, f); err != nil {
			return nil, fmt.Errorf("figma: data plane: %w", err)
		}
		f.listening = true
	}

	clients := make(map[string]*figmaAccountClient, len(c.Accounts))
	var report []string
	var tokenAccounts []any
	allNames := make([]any, len(c.Accounts))
	for i, acc := range c.Accounts {
		allNames[i] = acc.Name
		ac := &figmaAccountClient{acc: acc, base: f.cfg.APIBase, client: f.cfg.Client, now: f.cfg.Now}
		clients[acc.Name] = ac
		if acc.AllowSandboxToken && acc.AuthMode == FigmaAuthOAuth {
			tokenAccounts = append(tokenAccounts, acc.Name)
		}
		switch {
		case acc.AuthMode == FigmaAuthPAT && acc.PersonalAccessToken == "":
			report = append(report, fmt.Sprintf("account %s (pat): waiting for personal_access_tokens[%q]", acc.Name, acc.Name))
		case acc.AuthMode == FigmaAuthOAuth && acc.ClientSecret == "":
			report = append(report, fmt.Sprintf("account %s (oauth): waiting for client_secrets[%q]", acc.Name, acc.Name))
		case acc.AuthMode == FigmaAuthOAuth && acc.RefreshToken == "":
			report = append(report, fmt.Sprintf("account %s (oauth): waiting for OAuth token generation (refresh_tokens[%q])", acc.Name, acc.Name))
		default:
			if me, _, err := ac.verifyIdentity(ctx); err != nil {
				report = append(report, fmt.Sprintf("account %s (%s): verification warning: %v", acc.Name, acc.AuthMode, err))
			} else {
				who := strings.TrimSpace(me.Handle)
				if me.Email != "" {
					who = fmt.Sprintf("%s <%s>", who, me.Email)
				}
				report = append(report, fmt.Sprintf("account %s (%s): connected as %s", acc.Name, acc.AuthMode, who))
			}
		}
	}

	f.mu.Lock()
	f.conf, f.clients = c, clients
	f.mu.Unlock()

	mcpDesc := "Equip the run with an attempt-scoped Figma MCP toolset (parse_figma_url, get_file, get_design_context, export_images, get_styles_and_variables, get_comments) without exposing Figma credentials to the sandbox."
	if len(report) > 0 {
		mcpDesc += "\n\n" + strings.Join(report, "\n")
	}
	mcpProps := map[string]any{
		"account": map[string]any{
			"type":        "string",
			"title":       "Figma account",
			"enum":        allNames,
			"description": "Configured Figma account to authenticate as (optional when only one account is configured).",
		},
		"file_keys": map[string]any{
			"type":        "array",
			"title":       "Restrict to Figma file keys",
			"description": "Optional list of Figma file keys this grant may read (narrows any account-level allowlist).",
			"items":       map[string]any{"type": "string", "pattern": figmaFileKeyPattern.String()},
		},
	}
	var mcpRequired []any
	if len(allNames) > 1 {
		mcpRequired = []any{"account"}
	}
	mcpArgs := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           mcpProps,
	}
	if len(mcpRequired) > 0 {
		mcpArgs["required"] = mcpRequired
	}
	mcpMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        FigmaMCPMethod,
		Description: mcpDesc,
		Args:        mcpArgs,
		Mint:        f.mintMCP,
		Release:     f.releaseMCP,
		Audit:       f.takeMCPAudit,
		Now:         f.cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	methods := []*protocol.Method{mcpMethod}

	if len(tokenAccounts) > 0 {
		tokArgs := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"title":       "Figma OAuth account",
					"enum":        tokenAccounts,
					"description": "OAuth account with allow_sandbox_token enabled.",
				},
			},
		}
		if len(tokenAccounts) > 1 {
			tokArgs["required"] = []any{"account"}
		}
		tokMethod, err := protocol.NewMethod(protocol.MethodSpec{
			Name:        FigmaTokenMethod,
			Description: "Generate a fresh short-lived Figma OAuth 2.0 access token via OAuth refresh and place it at " + FigmaTokenPath + " (0600) for in-sandbox design tooling.",
			Args:        tokArgs,
			Mint:        f.mintToken,
			Release:     f.releaseToken,
			Audit:       f.takeTokenAudit,
			Now:         f.cfg.Now,
		})
		if err != nil {
			return nil, err
		}
		methods = append(methods, tokMethod)
	}

	return methods, nil
}

func (f *figmaServer) mintToken(ctx context.Context, attemptID string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
	if !pathSafe(attemptID) {
		return refusedResponse("attempt_id " + attemptID + " is not valid"), nil
	}
	accName, _ := args["account"].(string)
	f.mu.Lock()
	c := f.conf
	var ac *figmaAccountClient
	if c != nil {
		if acc := c.account(accName); acc != nil {
			accName = acc.Name
			ac = f.clients[acc.Name]
		}
	}
	f.mu.Unlock()
	if ac == nil {
		return refusedResponse(fmt.Sprintf("Figma account %q is not configured", accName)), nil
	}
	if ac.acc.AuthMode != FigmaAuthOAuth || !ac.acc.AllowSandboxToken {
		return refusedResponse(fmt.Sprintf("Figma account %q does not allow sandbox token placement", accName)), nil
	}
	tok, expiresIn, err := ac.accessToken(ctx, true)
	if err != nil {
		return refusedResponse(fmt.Sprintf("minting Figma OAuth token for %q failed: %v", accName, err)), nil
	}
	f.mu.Lock()
	f.tokRetired[attemptID] = append(f.tokRetired[attemptID], FigmaAudit{
		Time:      f.cfg.Now().UTC(),
		AttemptID: attemptID,
		Account:   accName,
		Operation: "mint_oauth_token",
	})
	f.mu.Unlock()
	return &protocol.MintResponse{
		Status: protocol.StatusOK,
		Placements: []protocol.Placement{
			{Path: FigmaTokenPath, Content: tok + "\n", Mode: "0600"},
		},
		PromptSection: fmt.Sprintf(
			"A short-lived Figma OAuth 2.0 access token for account %q (~%ds TTL) is placed at %s. Pass it as `Authorization: Bearer $(cat %s)` to Figma REST or MCP tools; never commit or log its contents.",
			accName, expiresIn, FigmaTokenPath, FigmaTokenPath,
		),
	}, nil
}

func (f *figmaServer) releaseToken(_ context.Context, _ string) error {
	return nil
}

func (f *figmaServer) takeTokenAudit(attemptID string) ([]any, int) {
	f.mu.Lock()
	recs := f.tokRetired[attemptID]
	delete(f.tokRetired, attemptID)
	f.mu.Unlock()
	out := make([]any, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out, 0
}

func (f *figmaServer) closeAll() error {
	f.mu.Lock()
	f.mcpGrants = map[string]*figmaMCPGrant{}
	f.mu.Unlock()
	return nil
}

func (f *figmaServer) testAccountTool(ctx context.Context, args map[string]any) ([]byte, error) {
	name, _ := args["account"].(string)
	name = strings.TrimSpace(name)
	f.mu.Lock()
	c := f.conf
	var ac *figmaAccountClient
	if c != nil {
		if acc := c.account(name); acc != nil {
			name = acc.Name
			ac = f.clients[acc.Name]
		}
	}
	f.mu.Unlock()
	if ac == nil {
		return json.Marshal(FigmaTestResult{Account: name, Error: fmt.Sprintf("Figma account %q is not configured", name)})
	}
	out := FigmaTestResult{Account: ac.acc.Name, AuthMode: ac.acc.AuthMode}
	me, expiresIn, err := ac.verifyIdentity(ctx)
	if err != nil {
		out.Error = err.Error()
	} else {
		out.UserID = me.ID
		out.Handle = me.Handle
		out.Email = me.Email
		out.ExpiresIn = expiresIn
	}
	return json.Marshal(out)
}

func (f *figmaServer) authorizeURLTool(_ context.Context, args map[string]any) ([]byte, error) {
	name, _ := args["account"].(string)
	redirectURI, _ := args["redirect_uri"].(string)
	state, _ := args["state"].(string)
	scopes, _ := args["scopes"].(string)

	f.mu.Lock()
	c := f.conf
	var acc *FigmaAccount
	if c != nil {
		acc = c.account(strings.TrimSpace(name))
	}
	f.mu.Unlock()
	if acc == nil {
		return nil, fmt.Errorf("Figma account %q is not configured", name)
	}
	if acc.AuthMode != FigmaAuthOAuth {
		return nil, fmt.Errorf("Figma account %q uses auth_mode %q, not oauth", acc.Name, acc.AuthMode)
	}
	if strings.TrimSpace(scopes) == "" {
		scopes = acc.Scopes
	}
	authURL, err := BuildFigmaOAuthAuthorizeURL(f.cfg.AuthorizeURL, acc.ClientID, redirectURI, scopes, state)
	if err != nil {
		return nil, err
	}
	return json.Marshal(FigmaAuthorizeURLResult{
		Account:      acc.Name,
		AuthorizeURL: authURL,
		Scopes:       scopes,
	})
}

func (f *figmaServer) isAllowedImageURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid image URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, errors.New("image URL has no host")
	}
	if f.cfg.AllowLoopbackImages {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && (u.Scheme == "http" || u.Scheme == "https") {
			return u, nil
		}
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("refusing non-HTTPS Figma export URL scheme %q", u.Scheme)
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil, errors.New("refusing IP-literal Figma export URL")
	}
	allowed := slices.ContainsFunc(f.cfg.ImageHostSuffixes, func(suffix string) bool {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		return host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix)
	})
	if !allowed {
		return nil, fmt.Errorf("refusing Figma export URL host %q outside allowed CDN domains", host)
	}
	return u, nil
}

func (f *figmaServer) downloadExportedImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := f.isAllowedImageURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	dlClient := &http.Client{
		Timeout:   figmaHTTPTimeout,
		Transport: f.cfg.Client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			_, err := f.isAllowedImageURL(req.URL.String())
			return err
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("downloading rendered Figma image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("downloading rendered Figma image returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFigmaImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading rendered Figma image: %w", err)
	}
	if len(data) > maxFigmaImageBytes {
		return nil, "", fmt.Errorf("rendered Figma image exceeds %d bytes", maxFigmaImageBytes)
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return data, ct, nil
}

func joinSortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func init() {
	register(Builder{
		Name:        FigmaName,
		NeedsConfig: true,
		Summary:     "Figma design MCP toolset and short-lived OAuth 2.0 token generation (mint_mcp, mint_token)",
		Args:        []string{"-listen", "127.0.0.1:" + strconv.Itoa(DefaultFigmaPort)},
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags(FigmaName, logOutput(logger))
			listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(DefaultFigmaPort), "the data-plane listener's loopback address")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			if fs.NArg() > 0 {
				return nil, fmt.Errorf("grain capability figma takes no arguments, only -listen")
			}
			return NewFigma(FigmaServerConfig{
				Listen: listenAddr(env, *listen),
				Token:  env(protocol.DataPlaneTokenEnv),
				Env:    env,
				Log:    logger,
			})
		},
	})
}
