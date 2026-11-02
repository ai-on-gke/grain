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

package hostd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// TokenProvider defines an interface for retrieving authentication tokens.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// IAPTokenProvider fetches, caches, and refreshes GCP OIDC ID tokens for a
// target audience (IAP OAuth client ID).
type IAPTokenProvider struct {
	Audience string

	// MetadataURL overrides the instance metadata server URL (useful in tests).
	MetadataURL string

	// HTTPClient overrides the HTTP client for metadata requests.
	HTTPClient *http.Client

	// FallbackSource overrides the token source used outside GCE / in tests.
	FallbackSource oauth2.TokenSource

	mu        sync.Mutex
	cachedTok string
	expiresAt time.Time
}

// NewIAPTokenProvider constructs a new IAPTokenProvider for the specified audience.
func NewIAPTokenProvider(audience string) *IAPTokenProvider {
	return &IAPTokenProvider{
		Audience: strings.TrimSpace(audience),
	}
}

// Token retrieves an active OIDC ID token, refreshing from GCE metadata or ADC
// if not cached or nearing expiration.
func (p *IAPTokenProvider) Token(ctx context.Context) (string, error) {
	if p.Audience == "" {
		return "", errors.New("hostd: IAP token provider requires a non-empty audience")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Return cached token if valid for at least another minute.
	if p.cachedTok != "" && time.Now().Add(1*time.Minute).Before(p.expiresAt) {
		return p.cachedTok, nil
	}

	// 1. Attempt retrieval from GCE instance metadata server.
	tok, exp, metaErr := p.fetchFromMetadata(ctx)
	if metaErr == nil && tok != "" {
		p.cachedTok = tok
		p.expiresAt = exp
		return tok, nil
	}

	// 2. Fallback to Application Default Credentials (ADC).
	tok, exp, adcErr := p.fetchFromADC(ctx)
	if adcErr == nil && tok != "" {
		p.cachedTok = tok
		p.expiresAt = exp
		return tok, nil
	}

	return "", fmt.Errorf("hostd: failed to resolve GCP IAP ID token for audience %q: metadata server: %v; ADC fallback: %w", p.Audience, metaErr, adcErr)
}

func (p *IAPTokenProvider) metadataEndpoint() string {
	if p.MetadataURL != "" {
		return p.MetadataURL
	}
	host := os.Getenv("GCE_METADATA_HOST")
	if host == "" {
		host = "metadata.google.internal"
	}
	return fmt.Sprintf("http://%s/computeMetadata/v1/instance/service-accounts/default/identity?audience=%s&format=full", host, url.QueryEscape(p.Audience))
}

func (p *IAPTokenProvider) fetchFromMetadata(ctx context.Context) (string, time.Time, error) {
	reqURL := p.metadataEndpoint()
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("metadata server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}

	rawTok := strings.TrimSpace(string(body))
	if rawTok == "" {
		return "", time.Time{}, errors.New("empty token from metadata server")
	}

	exp := parseTokenExpiry(rawTok)
	return rawTok, exp, nil
}

func (p *IAPTokenProvider) fetchFromADC(ctx context.Context) (string, time.Time, error) {
	if p.FallbackSource == nil {
		ts, err := idtoken.NewTokenSource(ctx, p.Audience)
		if err != nil {
			return "", time.Time{}, err
		}
		p.FallbackSource = ts
	}

	tok, err := p.FallbackSource.Token()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := tok.Expiry
	if exp.IsZero() {
		exp = parseTokenExpiry(tok.AccessToken)
	}
	return tok.AccessToken, exp, nil
}

// parseTokenExpiry extracts the `exp` claim from a JWT string without verifying
// signatures, falling back to a default 50-minute lifetime if unparseable.
func parseTokenExpiry(rawJWT string) time.Time {
	parser := jwt.NewParser()
	var claims jwt.RegisteredClaims
	_, _, err := parser.ParseUnverified(rawJWT, &claims)
	if err == nil && claims.ExpiresAt != nil && !claims.ExpiresAt.IsZero() {
		return claims.ExpiresAt.Time
	}
	return time.Now().Add(50 * time.Minute)
}
