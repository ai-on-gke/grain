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

package sandboxaccess

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultSecretManagerEndpoint is Secret Manager's REST endpoint.
const DefaultSecretManagerEndpoint = "https://secretmanager.googleapis.com"

// MetadataTokenURL is where a pod with Workload Identity gets an access
// token for its bound service account.
const MetadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

// SecretManagerStore keeps the CA bundle as the latest version of one
// Secret Manager secret. It speaks the REST API with the standard
// library so this package stays neutral.
//
// The daemon's service account needs roles/secretmanager.secretAccessor
// on the secret to Load it, and roles/secretmanager.secretVersionAdder
// on it as well for the first start to Save a generated CA; a deployment
// that seeds the secret itself grants only the first.
type SecretManagerStore struct {
	// Secret is the secret's resource name: projects/<p>/secrets/<s>.
	Secret string
	// Endpoint overrides DefaultSecretManagerEndpoint (tests).
	Endpoint string
	// HTTPClient is the client to use (http.DefaultClient when nil).
	HTTPClient *http.Client
	// Token returns an OAuth access token (MetadataToken when nil).
	Token func(ctx context.Context) (string, error)
}

type secretPayload struct {
	Payload struct {
		Data string `json:"data"`
	} `json:"payload"`
}

// Load implements CAStore.
func (s SecretManagerStore) Load(ctx context.Context) ([]byte, error) {
	body, status, err := s.do(ctx, http.MethodGet, "/v1/"+s.Secret+"/versions/latest:access", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrNoCA
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("sandboxaccess: reading %s: HTTP %d: %s", s.Secret, status, strings.TrimSpace(string(body)))
	}
	var p secretPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("sandboxaccess: reading %s: %w", s.Secret, err)
	}
	data, err := base64.StdEncoding.DecodeString(p.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("sandboxaccess: reading %s: %w", s.Secret, err)
	}
	if len(data) == 0 {
		return nil, ErrNoCA
	}
	return data, nil
}

// Save implements CAStore, adding bundle as the secret's new version.
func (s SecretManagerStore) Save(ctx context.Context, bundle []byte) error {
	var p secretPayload
	p.Payload.Data = base64.StdEncoding.EncodeToString(bundle)
	req, _ := json.Marshal(p)
	body, status, err := s.do(ctx, http.MethodPost, "/v1/"+s.Secret+":addVersion", req)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("sandboxaccess: adding a version to %s: HTTP %d: %s", s.Secret, status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s SecretManagerStore) do(ctx context.Context, method, path string, payload []byte) ([]byte, int, error) {
	if !strings.HasPrefix(s.Secret, "projects/") || !strings.Contains(s.Secret, "/secrets/") {
		return nil, 0, fmt.Errorf("sandboxaccess: secret %q is not projects/<p>/secrets/<s>", s.Secret)
	}
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = DefaultSecretManagerEndpoint
	}
	tokenFn := s.Token
	if tokenFn == nil {
		tokenFn = func(ctx context.Context) (string, error) { return MetadataToken(ctx, s.HTTPClient) }
	}
	token, err := tokenFn(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(s.HTTPClient).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("sandboxaccess: secret manager: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

// MetadataToken fetches an access token from the GKE metadata server.
func MetadataToken(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MetadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := httpClient(client).Do(req)
	if err != nil {
		return "", fmt.Errorf("sandboxaccess: metadata token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sandboxaccess: metadata token: HTTP %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("sandboxaccess: metadata token: %w", err)
	}
	return tok.AccessToken, nil
}

func httpClient(c *http.Client) *http.Client {
	if c == nil {
		return http.DefaultClient
	}
	return c
}
