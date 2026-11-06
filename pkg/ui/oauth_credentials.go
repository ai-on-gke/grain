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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OAuthCredentialsFileEnv overrides the path where `grain login` stores
// OAuth 2.0 / OIDC credentials and where HTTPClient reads them from.
const OAuthCredentialsFileEnv = "GRAIN_OAUTH_CREDENTIALS_FILE"

// OAuthStoredCredential holds the OAuth 2.0 tokens and endpoint metadata
// saved by `grain login` for a specific grain server.
type OAuthStoredCredential struct {
	ServerURL    string    `json:"serverUrl"`
	ClientID     string    `json:"clientId"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	TokenURL     string    `json:"tokenUrl,omitempty"`
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	IDToken      string    `json:"idToken"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Email        string    `json:"email,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty"`
}

// OAuthCredentialsStore is the on-disk JSON structure at
// ~/.config/grain/oauth-credentials.json.
type OAuthCredentialsStore struct {
	Servers map[string]OAuthStoredCredential `json:"servers"`
}

// OAuthCredentialsFilePath returns the path to the CLI OAuth credentials file.
func OAuthCredentialsFilePath() string {
	if p := strings.TrimSpace(os.Getenv(OAuthCredentialsFileEnv)); p != "" {
		return p
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "grain", "oauth-credentials.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "grain", "oauth-credentials.json")
	}
	return ""
}

// NormalizeOAuthServerURL canonicalizes a server URL for credential lookup.
func NormalizeOAuthServerURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// LoadOAuthCredentials reads the OAuth credentials store from path. If the
// file does not exist, an empty store is returned without error.
func LoadOAuthCredentials(path string) (*OAuthCredentialsStore, error) {
	store := &OAuthCredentialsStore{Servers: make(map[string]OAuthStoredCredential)}
	if path == "" {
		return store, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if store.Servers == nil {
		store.Servers = make(map[string]OAuthStoredCredential)
	}
	return store, nil
}

// SaveOAuthCredential writes or updates cred in the credentials store at path
// with mode 0600.
func SaveOAuthCredential(path string, cred OAuthStoredCredential) error {
	if path == "" {
		return errors.New("empty OAuth credentials file path")
	}
	key := NormalizeOAuthServerURL(cred.ServerURL)
	if key == "" {
		return errors.New("empty server URL in OAuth credential")
	}
	cred.ServerURL = key
	if cred.UpdatedAt.IsZero() {
		cred.UpdatedAt = time.Now().UTC()
	}

	store, err := LoadOAuthCredentials(path)
	if err != nil {
		store = &OAuthCredentialsStore{Servers: make(map[string]OAuthStoredCredential)}
	}
	store.Servers[key] = cred
	return writeOAuthCredentialsStore(path, store)
}

// RemoveOAuthCredential removes the stored credential for serverURL (or all
// credentials when all is true) and returns the number of removed entries.
func RemoveOAuthCredential(path, serverURL string, all bool) (int, error) {
	store, err := LoadOAuthCredentials(path)
	if err != nil {
		return 0, err
	}
	if len(store.Servers) == 0 {
		return 0, nil
	}
	if all {
		n := len(store.Servers)
		store.Servers = make(map[string]OAuthStoredCredential)
		if err := writeOAuthCredentialsStore(path, store); err != nil {
			return 0, err
		}
		return n, nil
	}
	key := NormalizeOAuthServerURL(serverURL)
	if _, ok := store.Servers[key]; !ok {
		return 0, nil
	}
	delete(store.Servers, key)
	if err := writeOAuthCredentialsStore(path, store); err != nil {
		return 0, err
	}
	return 1, nil
}

func writeOAuthCredentialsStore(path string, store *OAuthCredentialsStore) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (c *HTTPClient) resolveStoredOAuthIDToken(ctx context.Context) (string, error) {
	key := NormalizeOAuthServerURL(c.BaseURL)
	if key == "" {
		return "", nil
	}
	path := OAuthCredentialsFilePath()
	store, err := LoadOAuthCredentials(path)
	if err != nil || store == nil {
		return "", nil
	}
	cred, ok := store.Servers[key]
	if !ok {
		return "", nil
	}

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.cachedToken != "" && time.Now().Before(c.cachedExpiry) {
		return c.cachedToken, nil
	}

	expiry := cred.Expiry
	if expiry.IsZero() && cred.IDToken != "" {
		expiry = jwtExpiry(cred.IDToken)
	}
	if cred.IDToken != "" && !expiry.IsZero() && time.Now().Add(time.Minute).Before(expiry) {
		return c.cacheIDToken(cred.IDToken, expiry), nil
	}

	if cred.RefreshToken != "" && cred.ClientID != "" {
		refreshed, err := c.refreshOAuthCredential(ctx, cred)
		if err == nil && refreshed.IDToken != "" {
			_ = SaveOAuthCredential(path, refreshed)
			return c.cacheIDToken(refreshed.IDToken, refreshed.Expiry), nil
		}
		if err != nil && cred.IDToken == "" {
			return "", err
		}
	}

	if cred.IDToken != "" && (expiry.IsZero() || time.Now().Before(expiry)) {
		return c.cacheIDToken(cred.IDToken, expiry), nil
	}
	return "", nil
}

func (c *HTTPClient) refreshOAuthCredential(ctx context.Context, cred OAuthStoredCredential) (OAuthStoredCredential, error) {
	tokenURL := strings.TrimSpace(cred.TokenURL)
	if tokenURL == "" {
		tokenURL = GoogleOAuthTokenURL
	}
	form := url.Values{
		"client_id":     {cred.ClientID},
		"refresh_token": {cred.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	if cred.ClientSecret != "" {
		form.Set("client_secret", cred.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return cred, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return cred, fmt.Errorf("refreshing OAuth token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return cred, fmt.Errorf("OAuth token refresh returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokResp struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return cred, fmt.Errorf("decoding OAuth refresh response: %w", err)
	}
	if tokResp.IDToken == "" {
		return cred, errors.New("OAuth token refresh response did not contain an id_token")
	}
	cred.IDToken = tokResp.IDToken
	if tokResp.AccessToken != "" {
		cred.AccessToken = tokResp.AccessToken
	}
	if tokResp.RefreshToken != "" {
		cred.RefreshToken = tokResp.RefreshToken
	}
	if exp := jwtExpiry(tokResp.IDToken); !exp.IsZero() {
		cred.Expiry = exp
	} else if tokResp.ExpiresIn > 0 {
		cred.Expiry = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	}
	cred.UpdatedAt = time.Now().UTC()
	return cred, nil
}
