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

package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pkg/browser"

	"github.com/ai-on-gke/grain/pkg/ui"
)

var (
	oauthLoginOpenBrowser = browser.OpenURL
	oauthLoginHTTPClient  *http.Client
	// oauthLoginCallbackHook is invoked in tests with the authorization URL
	// once the loopback callback listener is ready.
	oauthLoginCallbackHook func(authURL string)
)

type cliOAuthDiscovery struct {
	Enabled         bool     `json:"enabled"`
	ClientID        string   `json:"clientId"`
	CLIClientID     string   `json:"cliClientId"`
	CLIClientSecret string   `json:"cliClientSecret"`
	AuthURL         string   `json:"authUrl"`
	TokenURL        string   `json:"tokenUrl"`
	HostedDomains   []string `json:"hostedDomains"`
}

// isGitLoginInvocation reports whether `grain login` was called with
// git-login-specific flags (-from-gh, -with-token, -host, -author, -scope)
// so existing `grain login` scripts for GitHub token delegation keep working.
func isGitLoginInvocation(args []string) bool {
	for _, a := range args {
		trimmed := strings.TrimLeft(a, "-")
		name, _, _ := strings.Cut(trimmed, "=")
		switch name {
		case "from-gh", "with-token", "host", "author", "scope":
			return true
		}
	}
	return false
}

func isOAuthLoginInvocation(args []string) bool {
	for _, a := range args {
		trimmed := strings.TrimLeft(a, "-")
		name, _, _ := strings.Cut(trimmed, "=")
		switch name {
		case "client-id", "client-secret", "auth-url", "token-url", "no-browser":
			return true
		}
	}
	return false
}

func isOAuthLogoutInvocation(sel selectedContext, args []string) bool {
	for _, a := range args {
		trimmed := strings.TrimLeft(a, "-")
		name, _, _ := strings.Cut(trimmed, "=")
		if name == "all" {
			return true
		}
	}
	return sel.name == "" && len(args) == 0 && strings.TrimSpace(os.Getenv(ui.OAuthCredentialsFileEnv)) != ""
}

func cmdOAuthLogin(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain login", flag.ContinueOnError)
	serverFlag := fs.String("server", "", "base URL of the grain server to sign in to (overrides global -server)")
	clientIDFlag := fs.String("client-id", "", "OAuth 2.0 Client ID ($GRAIN_OAUTH_CLI_CLIENT_ID or discovered from server)")
	clientSecretFlag := fs.String("client-secret", "", "OAuth 2.0 Client Secret ($GRAIN_OAUTH_CLI_CLIENT_SECRET or discovered from server)")
	authURLFlag := fs.String("auth-url", "", "OAuth 2.0 authorization endpoint URL")
	tokenURLFlag := fs.String("token-url", "", "OAuth 2.0 token endpoint URL")
	noBrowser := fs.Bool("no-browser", false, "print the authorization URL without opening a browser")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	serverURL := ui.NormalizeOAuthServerURL(c.BaseURL)
	if strings.TrimSpace(*serverFlag) != "" {
		serverURL = ui.NormalizeOAuthServerURL(*serverFlag)
	}
	if serverURL == "" {
		return errors.New("a server URL is required (-server or $GRAIN_SERVER)")
	}

	httpClient := oauthLoginHTTPClient
	if httpClient == nil {
		httpClient = c.HTTP
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	disc := fetchCLIOAuthDiscovery(ctx, httpClient, serverURL)

	clientID := firstNonEmptyString(
		*clientIDFlag,
		os.Getenv("GRAIN_OAUTH_CLI_CLIENT_ID"),
		disc.CLIClientID,
		os.Getenv("GRAIN_OAUTH_CLIENT_ID"),
		disc.ClientID,
	)
	if clientID == "" {
		return fmt.Errorf("no OAuth 2.0 Client ID found for %s: pass -client-id, set $GRAIN_OAUTH_CLI_CLIENT_ID, or configure GRAIN_OAUTH_CLI_CLIENT_ID on the server", serverURL)
	}
	clientSecret := firstNonEmptyString(
		*clientSecretFlag,
		os.Getenv("GRAIN_OAUTH_CLI_CLIENT_SECRET"),
		disc.CLIClientSecret,
		os.Getenv("GRAIN_OAUTH_CLIENT_SECRET"),
	)
	authURL := firstNonEmptyString(*authURLFlag, os.Getenv("GRAIN_OAUTH_AUTH_URL"), disc.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth")
	tokenURL := firstNonEmptyString(*tokenURLFlag, os.Getenv("GRAIN_OAUTH_TOKEN_URL"), disc.TokenURL, "https://oauth2.googleapis.com/token")

	state, err := randomURLSafeString(24)
	if err != nil {
		return err
	}
	verifier, err := randomURLSafeString(32)
	if err != nil {
		return err
	}
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("starting local OAuth callback listener: %w", err)
	}
	defer ln.Close()

	redirectURI := fmt.Sprintf("http://%s/callback", ln.Addr().String())
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
	}
	if len(disc.HostedDomains) == 1 {
		q.Set("hd", disc.HostedDomains[0])
	}
	fullAuthURL := authURL + "?" + q.Encode()

	type callbackResult struct {
		code string
		err  error
	}
	resCh := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if errParam := r.URL.Query().Get("error"); errParam != "" {
			desc := r.URL.Query().Get("error_description")
			http.Error(w, "OAuth sign-in failed: "+errParam, http.StatusBadRequest)
			select {
			case resCh <- callbackResult{err: fmt.Errorf("OAuth provider returned error %q (%s)", errParam, desc)}:
			default:
			}
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			select {
			case resCh <- callbackResult{err: errors.New("OAuth callback state mismatch")}:
			default:
			}
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			select {
			case resCh <- callbackResult{err: errors.New("OAuth callback missing authorization code")}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html><body style=\"font-family:system-ui;padding:2rem;\"><h2>Authenticated with Grain</h2><p>You may close this tab and return to your terminal.</p></body></html>")
		select {
		case resCh <- callbackResult{code: code}:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if !out.json {
		fmt.Fprintf(os.Stderr, "Authenticate with Google OAuth 2.0 by visiting:\n  %s\n", fullAuthURL)
		if !*noBrowser && oauthLoginOpenBrowser != nil {
			_ = oauthLoginOpenBrowser(fullAuthURL)
		}
	}
	if oauthLoginCallbackHook != nil {
		oauthLoginCallbackHook(fullAuthURL)
	}

	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return res.err
		}
		code = res.code
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokReq.Header.Set("Accept", "application/json")

	tokResp, err := httpClient.Do(tokReq)
	if err != nil {
		return fmt.Errorf("exchanging authorization code: %w", err)
	}
	defer tokResp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(tokResp.Body, 1<<20))
	if tokResp.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint returned HTTP %d: %s", tokResp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenPayload struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenPayload); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}
	if tokenPayload.IDToken == "" {
		return errors.New("token response did not contain an OIDC id_token")
	}

	email, exp := parseUnverifiedIDTokenMetadata(tokenPayload.IDToken)
	if exp.IsZero() && tokenPayload.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tokenPayload.ExpiresIn) * time.Second)
	}

	cred := ui.OAuthStoredCredential{
		ServerURL:    serverURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		AccessToken:  tokenPayload.AccessToken,
		RefreshToken: tokenPayload.RefreshToken,
		IDToken:      tokenPayload.IDToken,
		Expiry:       exp,
		Email:        email,
		UpdatedAt:    time.Now().UTC(),
	}
	credPath := ui.OAuthCredentialsFilePath()
	if err := ui.SaveOAuthCredential(credPath, cred); err != nil {
		return fmt.Errorf("saving OAuth credentials to %s: %w", credPath, err)
	}

	userResp, verifyErr := verifyServerAuthUser(ctx, httpClient, serverURL, tokenPayload.IDToken)
	if verifyErr != nil {
		return verifyErr
	}
	if userResp.User != "" {
		email = userResp.User
	}
	if out.json {
		out.encode(map[string]any{
			"server":          serverURL,
			"email":           email,
			"roles":           userResp.Roles,
			"scopes":          userResp.Scopes,
			"credentialsFile": credPath,
		})
		return nil
	}
	roleNote := ""
	if len(userResp.Roles) > 0 {
		roleNote = fmt.Sprintf(" (roles: %s)", strings.Join(userResp.Roles, ", "))
	}
	fmt.Printf("Logged in to %s as %s%s.\n", serverURL, email, roleNote)
	return nil
}

func cmdOAuthLogout(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain logout", flag.ContinueOnError)
	serverFlag := fs.String("server", "", "base URL of the grain server to log out from (overrides global -server)")
	all := fs.Bool("all", false, "remove stored OAuth credentials for all servers")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	serverURL := ui.NormalizeOAuthServerURL(c.BaseURL)
	if strings.TrimSpace(*serverFlag) != "" {
		serverURL = ui.NormalizeOAuthServerURL(*serverFlag)
	}
	credPath := ui.OAuthCredentialsFilePath()
	removed, err := ui.RemoveOAuthCredential(credPath, serverURL, *all)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]any{"removed": removed, "server": serverURL, "all": *all})
		return nil
	}
	if *all {
		fmt.Printf("Removed %d stored OAuth credential(s).\n", removed)
		return nil
	}
	if removed == 0 {
		fmt.Printf("No stored OAuth credential found for %s.\n", serverURL)
		return nil
	}
	fmt.Printf("Logged out from %s.\n", serverURL)
	return nil
}

func fetchCLIOAuthDiscovery(ctx context.Context, httpClient *http.Client, serverURL string) cliOAuthDiscovery {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/auth/oauth/cli-config", nil)
	if err != nil {
		return cliOAuthDiscovery{}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return cliOAuthDiscovery{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cliOAuthDiscovery{}
	}
	var disc cliOAuthDiscovery
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&disc)
	return disc
}

func verifyServerAuthUser(ctx context.Context, httpClient *http.Client, serverURL, idToken string) (ui.AuthUserResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/auth/user", nil)
	if err != nil {
		return ui.AuthUserResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return ui.AuthUserResponse{}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ui.AuthUserResponse{}, fmt.Errorf("server %s refused the OAuth token (HTTP %d): %s", serverURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return ui.AuthUserResponse{}, nil
	}
	var u ui.AuthUserResponse
	_ = json.Unmarshal(body, &u)
	return u, nil
}

func parseUnverifiedIDTokenMetadata(idToken string) (string, time.Time) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", time.Time{}
	}
	var claims struct {
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", time.Time{}
	}
	var exp time.Time
	if claims.Exp > 0 {
		exp = time.Unix(claims.Exp, 0)
	}
	return claims.Email, exp
}

func randomURLSafeString(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
