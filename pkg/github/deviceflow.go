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

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultOAuthClientID is the default GitHub OAuth App client ID used by
// `grain git-login -web` when no custom client ID is supplied via flag or
// $GRAIN_GITHUB_CLIENT_ID. It matches GitHub CLI's public OAuth application
// so interactive Device Flow login works out of the box on github.com.
const DefaultOAuthClientID = "178c6fc778ccc68e1d6a"

// DefaultOAuthScopes are the OAuth scopes requested during interactive
// GitHub Device Flow login (`grain git-login -web`), matching `gh auth login`:
// repo (clone, push, open PRs, comment), read:org, and workflow (push branches
// that touch .github/workflows).
var DefaultOAuthScopes = []string{"repo", "read:org", "workflow"}

// DeviceCodeResponse is GitHub's response from POST /login/device/code
// (RFC 8628 OAuth 2.0 Device Authorization Grant).
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceWebBaseURL returns the scheme://host[:port] base URL where GitHub's
// OAuth endpoints (/login/device/code and /login/oauth/access_token) live for
// host and optional baseURL override.
func DeviceWebBaseURL(host, baseURL string) string {
	if base := strings.TrimSpace(baseURL); base != "" {
		base = strings.TrimRight(base, "/")
		base = strings.TrimSuffix(base, "/api/v3")
		return base
	}
	h := strings.TrimSpace(host)
	if h == "" || strings.EqualFold(h, "github.com") {
		return "https://github.com"
	}
	return "https://" + h
}

func defaultDeviceFlowHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// RequestDeviceCode initiates a GitHub OAuth Device Flow session against
// webBaseURL (e.g. "https://github.com") for clientID and scopes.
func RequestDeviceCode(ctx context.Context, client *http.Client, webBaseURL, clientID string, scopes []string) (DeviceCodeResponse, error) {
	if strings.TrimSpace(clientID) == "" {
		return DeviceCodeResponse{}, errors.New("OAuth client ID is required for device flow login")
	}
	if client == nil {
		client = defaultDeviceFlowHTTPClient()
	}
	endpoint := strings.TrimRight(strings.TrimSpace(webBaseURL), "/") + "/login/device/code"
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(clientID))
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("building device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-cli")

	resp, err := client.Do(req)
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("requesting device code from %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("reading device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DeviceCodeResponse{}, fmt.Errorf("device code request to %s failed (HTTP %d): %s",
			endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		DeviceCodeResponse
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("decoding device code response: %w", err)
	}
	if parsed.Error != "" {
		if parsed.ErrorDescription != "" {
			return DeviceCodeResponse{}, fmt.Errorf("GitHub device flow refused: %s (%s)", parsed.ErrorDescription, parsed.Error)
		}
		return DeviceCodeResponse{}, fmt.Errorf("GitHub device flow refused: %s", parsed.Error)
	}
	if parsed.DeviceCode == "" || parsed.UserCode == "" || parsed.VerificationURI == "" {
		return DeviceCodeResponse{}, errors.New("GitHub device code response was missing required fields")
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}
	return parsed.DeviceCodeResponse, nil
}

// PollDeviceToken polls POST /login/oauth/access_token until the user
// completes browser authorization, the device code expires, or ctx is
// cancelled.
func PollDeviceToken(ctx context.Context, client *http.Client, webBaseURL, clientID, deviceCode string, interval time.Duration) (string, error) {
	if client == nil {
		client = defaultDeviceFlowHTTPClient()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	endpoint := strings.TrimRight(strings.TrimSpace(webBaseURL), "/") + "/login/oauth/access_token"
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(clientID))
	form.Set("device_code", strings.TrimSpace(deviceCode))
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	encodedForm := form.Encode()

	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}

		token, nextInterval, err := pollDeviceTokenOnce(ctx, client, endpoint, encodedForm, interval)
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
		interval = nextInterval
	}
}

func pollDeviceTokenOnce(ctx context.Context, client *http.Client, endpoint, encodedForm string, interval time.Duration) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodedForm))
	if err != nil {
		return "", interval, fmt.Errorf("building device token poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-cli")

	resp, err := client.Do(req)
	if err != nil {
		return "", interval, fmt.Errorf("polling %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", interval, fmt.Errorf("reading device token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", interval, fmt.Errorf("polling %s failed (HTTP %d): %s",
			endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Interval         int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", interval, fmt.Errorf("decoding device token response: %w", err)
	}
	if tok := strings.TrimSpace(parsed.AccessToken); tok != "" {
		return tok, interval, nil
	}

	switch parsed.Error {
	case "authorization_pending":
		return "", interval, nil
	case "slow_down":
		if parsed.Interval > 0 {
			return "", time.Duration(parsed.Interval) * time.Second, nil
		}
		return "", interval + 5*time.Second, nil
	case "expired_token":
		return "", interval, errors.New("the GitHub device code expired before authorization completed; run `grain git-login` again")
	case "access_denied":
		return "", interval, errors.New("GitHub authorization was cancelled in the browser")
	default:
		if parsed.ErrorDescription != "" {
			return "", interval, fmt.Errorf("GitHub OAuth error: %s (%s)", parsed.ErrorDescription, parsed.Error)
		}
		if parsed.Error != "" {
			return "", interval, fmt.Errorf("GitHub OAuth error: %s", parsed.Error)
		}
		return "", interval, errors.New("GitHub OAuth token response contained neither access_token nor error")
	}
}
