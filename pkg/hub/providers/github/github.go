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
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
)

const (
	defaultGitHubAPIBaseURL = "https://api.github.com"
	defaultRepoPrefix       = "grain-cfg-"
	DeployKeySecretSuffix   = "config-repo-key"
)

// ErrOutOfScope is returned when a repository name does not match the configured org or repo_prefix.
var ErrOutOfScope = errors.New("github config_repos: repository is outside configured org/prefix scope")

// Option configures a GitHubConfigRepos provider.
type Option func(*GitHubConfigRepos)

// WithHTTPClient overrides the HTTP client used for GitHub API calls.
func WithHTTPClient(hc *http.Client) Option {
	return func(p *GitHubConfigRepos) {
		if hc != nil {
			p.httpClient = hc
		}
	}
}

// WithSecretWriter wires a `registry.SecretWriter` so `EnsureRepo` automatically stores
// the generated `ed25519` private deploy key under `<secret_prefix><name>-config-repo-key`.
func WithSecretWriter(sw hub.SecretWriter) Option {
	return func(p *GitHubConfigRepos) {
		p.secretWriter = sw
	}
}

// GitHubConfigRepos implements `registry.ConfigRepos` against the GitHub REST API,
// provisioning a per-instance private repository `<org>/<repo_prefix><name>` and a
// repository-scoped read-write `ed25519` SSH deploy key.
type GitHubConfigRepos struct {
	org            string
	repoPrefix     string
	appID          int64
	installationID int64
	appPrivKey     *rsa.PrivateKey
	apiBaseURL     string
	httpClient     *http.Client
	secretWriter   hub.SecretWriter
	nowFn          func() time.Time

	tokenMu     sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

// NewGitHubConfigRepos constructs a GitHubConfigRepos provider from `cfg`.
func NewGitHubConfigRepos(cfg hub.ConfigReposConfig, opts ...Option) (*GitHubConfigRepos, error) {
	org := strings.TrimSpace(cfg.GitHubOrg)
	if org == "" {
		return nil, errors.New("github config_repos: config_repos.github_org is required")
	}
	repoPrefix := strings.TrimSpace(cfg.RepoPrefix)
	if repoPrefix == "" {
		repoPrefix = defaultRepoPrefix
	}
	if !strings.HasSuffix(repoPrefix, "-") {
		return nil, fmt.Errorf("github config_repos: repo_prefix %q must end with '-'", repoPrefix)
	}
	apiBase := strings.TrimSpace(cfg.APIBaseURL)
	if apiBase == "" {
		apiBase = defaultGitHubAPIBaseURL
	}

	p := &GitHubConfigRepos{
		org:            org,
		repoPrefix:     repoPrefix,
		appID:          cfg.AppID,
		installationID: cfg.Installation,
		apiBaseURL:     strings.TrimRight(apiBase, "/"),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		nowFn:          time.Now,
	}

	rawPEM := strings.TrimSpace(cfg.PrivateKeyPEM)
	if rawPEM == "" && strings.TrimSpace(cfg.PrivateKeyFile) != "" {
		b, err := os.ReadFile(strings.TrimSpace(cfg.PrivateKeyFile))
		if err != nil {
			return nil, fmt.Errorf("github config_repos: reading private_key_file %s: %w", cfg.PrivateKeyFile, err)
		}
		rawPEM = string(b)
	}
	if rawPEM != "" {
		priv, err := parseRSAPrivateKeyPEM([]byte(rawPEM))
		if err != nil {
			return nil, fmt.Errorf("github config_repos: parsing GitHub App RSA private key: %w", err)
		}
		p.appPrivKey = priv
	}

	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// SetSecretWriter attaches a `registry.SecretWriter` (such as `GCPInstanceResources`) so
// `EnsureRepo` writes the generated deploy key into Secret Manager.
func (p *GitHubConfigRepos) SetSecretWriter(sw hub.SecretWriter) {
	p.secretWriter = sw
}

// RepoNameForEntry returns `<repo_prefix><entryName>` after validating `entryName`.
func (p *GitHubConfigRepos) RepoNameForEntry(entryName string) (string, error) {
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: invalid entry name %q: %v", ErrOutOfScope, entryName, err)
	}
	repoName := p.repoPrefix + entryName
	if !strings.HasPrefix(repoName, p.repoPrefix) {
		return "", fmt.Errorf("%w: repo %q does not start with %q", ErrOutOfScope, repoName, p.repoPrefix)
	}
	return repoName, nil
}

// CheckRepoScope verifies that `repoName` starts with `p.repoPrefix` and corresponds to a valid entry name.
func (p *GitHubConfigRepos) CheckRepoScope(repoName string) error {
	repoName = strings.TrimSpace(repoName)
	if !strings.HasPrefix(repoName, p.repoPrefix) {
		return fmt.Errorf("%w: repository %q does not have required prefix %q", ErrOutOfScope, repoName, p.repoPrefix)
	}
	entryName := strings.TrimPrefix(repoName, p.repoPrefix)
	if err := api.ValidateEntryName(entryName); err != nil {
		return fmt.Errorf("%w: repository %q suffix %q is not a valid entry name: %v", ErrOutOfScope, repoName, entryName, err)
	}
	return nil
}

// EnsureRepo creates (or adopts) the private repository `<org>/<repo_prefix><entryName>`,
// generates a fresh `ed25519` SSH deploy keypair, stores the private key in Secret Manager
// (`<secret_prefix><entryName>-config-repo-key`) if a `SecretWriter` is configured, and
// registers the public key on the repository as a read-write deploy key.
func (p *GitHubConfigRepos) EnsureRepo(ctx context.Context, entryName, ownerEmail string) (*hub.ConfigRepoBundle, error) {
	repoName, err := p.RepoNameForEntry(entryName)
	if err != nil {
		return nil, err
	}
	if err := p.CheckRepoScope(repoName); err != nil {
		return nil, err
	}

	sshURL, err := p.ensurePrivateRepo(ctx, repoName, entryName, ownerEmail)
	if err != nil {
		return nil, err
	}

	comment := "grain-instance:" + entryName
	pubAuthorized, privPEM, err := GenerateED25519DeployKey(comment)
	if err != nil {
		return nil, fmt.Errorf("github config_repos: generating ed25519 deploy key: %w", err)
	}

	if p.secretWriter != nil {
		if _, err := p.secretWriter.PutSecret(ctx, entryName, DeployKeySecretSuffix, []byte(privPEM)); err != nil {
			return nil, fmt.Errorf("github config_repos: storing deploy key in Secret Manager: %w", err)
		}
	}

	if err := p.registerDeployKey(ctx, repoName, comment, pubAuthorized); err != nil {
		return nil, err
	}

	return &hub.ConfigRepoBundle{
		RepoURL:   sshURL,
		DeployKey: privPEM,
	}, nil
}

// ArchiveRepo deletes `<org>/<repo_prefix><entryName>` after verifying prefix scope.
func (p *GitHubConfigRepos) ArchiveRepo(ctx context.Context, entryName string) error {
	repoName, err := p.RepoNameForEntry(entryName)
	if err != nil {
		return err
	}
	return p.DeleteOrArchiveRepoByName(ctx, repoName)
}

// DeleteOrArchiveRepoByName deletes `repoName` after enforcing `CheckRepoScope`.
func (p *GitHubConfigRepos) DeleteOrArchiveRepoByName(ctx context.Context, repoName string) error {
	if err := p.CheckRepoScope(repoName); err != nil {
		return err
	}
	repoURL := fmt.Sprintf("%s/repos/%s/%s",
		p.apiBaseURL, url.PathEscape(p.org), url.PathEscape(repoName))

	return p.doGitHubJSON(ctx, http.MethodDelete, repoURL, nil, nil, http.StatusNotFound)
}

func (p *GitHubConfigRepos) ensurePrivateRepo(ctx context.Context, repoName, entryName, ownerEmail string) (string, error) {
	getURL := fmt.Sprintf("%s/repos/%s/%s",
		p.apiBaseURL, url.PathEscape(p.org), url.PathEscape(repoName))
	var existing struct {
		Name    string `json:"name"`
		SSHURL  string `json:"ssh_url"`
		Private bool   `json:"private"`
	}
	err := p.doGitHubJSON(ctx, http.MethodGet, getURL, nil, &existing)
	if err == nil {
		if existing.SSHURL != "" {
			return existing.SSHURL, nil
		}
		return fmt.Sprintf("git@github.com:%s/%s.git", p.org, repoName), nil
	}
	if !isHTTPStatusError(err, http.StatusNotFound) {
		return "", fmt.Errorf("github config_repos: GET repo %s/%s: %w", p.org, repoName, err)
	}

	createURL := fmt.Sprintf("%s/orgs/%s/repos", p.apiBaseURL, url.PathEscape(p.org))
	createBody := map[string]any{
		"name":        repoName,
		"private":     true,
		"visibility":  "private",
		"description": fmt.Sprintf("grain-registry config repository for instance %s (owner: %s)", entryName, ownerEmail),
		"auto_init":   true,
	}
	var created struct {
		Name   string `json:"name"`
		SSHURL string `json:"ssh_url"`
	}
	if err := p.doGitHubJSON(ctx, http.MethodPost, createURL, createBody, &created); err != nil {
		return "", fmt.Errorf("github config_repos: creating private repo %s/%s: %w", p.org, repoName, err)
	}
	if created.SSHURL != "" {
		return created.SSHURL, nil
	}
	return fmt.Sprintf("git@github.com:%s/%s.git", p.org, repoName), nil
}

func (p *GitHubConfigRepos) registerDeployKey(ctx context.Context, repoName, title, pubKeyAuthorized string) error {
	keysURL := fmt.Sprintf("%s/repos/%s/%s/keys",
		p.apiBaseURL, url.PathEscape(p.org), url.PathEscape(repoName))
	var existingKeys []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	if err := p.doGitHubJSON(ctx, http.MethodGet, keysURL, nil, &existingKeys); err != nil {
		return fmt.Errorf("github config_repos: listing deploy keys on %s/%s: %w", p.org, repoName, err)
	}
	for _, k := range existingKeys {
		if k.Title == title && k.ID != 0 {
			delKeyURL := fmt.Sprintf("%s/repos/%s/%s/keys/%d",
				p.apiBaseURL, url.PathEscape(p.org), url.PathEscape(repoName), k.ID)
			_ = p.doGitHubJSON(ctx, http.MethodDelete, delKeyURL, nil, nil, http.StatusNotFound)
		}
	}

	createKeyBody := map[string]any{
		"title":     title,
		"key":       pubKeyAuthorized,
		"read_only": false,
	}
	if err := p.doGitHubJSON(ctx, http.MethodPost, keysURL, createKeyBody, nil); err != nil {
		return fmt.Errorf("github config_repos: registering deploy key on %s/%s: %w", p.org, repoName, err)
	}
	return nil
}

func (p *GitHubConfigRepos) resolveToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	now := p.nowFn().UTC()
	if p.cachedToken != "" && now.Add(time.Minute).Before(p.tokenExpiry) {
		return p.cachedToken, nil
	}
	if p.appPrivKey == nil || p.appID == 0 || p.installationID == 0 {
		return "", errors.New("github config_repos: GitHub App credentials (app_id, installation_id, private_key) required")
	}

	appJWT, err := signGitHubAppJWT(p.appID, p.appPrivKey, now)
	if err != nil {
		return "", fmt.Errorf("signing GitHub App JWT: %w", err)
	}

	tokURL := fmt.Sprintf("%s/app/installations/%d/access_tokens", p.apiBaseURL, p.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging installation token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("installation token exchange HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokResp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return "", fmt.Errorf("decoding installation token response: %w", err)
	}
	if tokResp.Token == "" {
		return "", errors.New("empty token in installation token response")
	}
	p.cachedToken = tokResp.Token
	if tokResp.ExpiresAt.IsZero() {
		p.tokenExpiry = now.Add(30 * time.Minute)
	} else {
		p.tokenExpiry = tokResp.ExpiresAt
	}
	return p.cachedToken, nil
}

type githubHTTPError struct {
	statusCode int
	body       string
}

func (e *githubHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.statusCode, e.body)
}

func isHTTPStatusError(err error, code int) bool {
	var he *githubHTTPError
	return errors.As(err, &he) && he.statusCode == code
}

func (p *GitHubConfigRepos) doGitHubJSON(ctx context.Context, method, reqURL string, reqPayload any, respOut any, allowedStatuses ...int) error {
	tok, err := p.resolveToken(ctx)
	if err != nil {
		return err
	}
	var bodyReader io.Reader
	if reqPayload != nil {
		b, err := json.Marshal(reqPayload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if reqPayload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	for _, code := range allowedStatuses {
		if resp.StatusCode == code {
			return nil
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &githubHTTPError{
			statusCode: resp.StatusCode,
			body:       strings.TrimSpace(string(respBytes)),
		}
	}
	if respOut != nil && len(bytes.TrimSpace(respBytes)) > 0 {
		if err := json.Unmarshal(respBytes, respOut); err != nil {
			return fmt.Errorf("decoding GitHub response JSON: %w", err)
		}
	}
	return nil
}

// GenerateED25519DeployKey generates a fresh `ed25519` keypair and returns the OpenSSH
// `ssh-ed25519 <base64> <comment>` public key and PKCS#8 PEM-encoded private key.
func GenerateED25519DeployKey(comment string) (pubAuthorized string, privPEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	wire := encodeSSHWireString("ssh-ed25519")
	wire = append(wire, encodeSSHWireBytes(pub)...)
	pubAuthorized = "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire)
	if strings.TrimSpace(comment) != "" {
		pubAuthorized += " " + strings.TrimSpace(comment)
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	privBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	return pubAuthorized, string(privBytes), nil
}

func encodeSSHWireString(s string) []byte {
	return encodeSSHWireBytes([]byte(s))
}

func encodeSSHWireBytes(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out[:4], uint32(len(b)))
	copy(out[4:], b)
	return out
}

func parseRSAPrivateKeyPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("PEM key is not an RSA private key")
	}
	return rsaKey, nil
}

func signGitHubAppJWT(appID int64, priv *rsa.PrivateKey, now time.Time) (string, error) {
	hdrBytes, _ := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	})
	payBytes, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func init() {
	hub.RegisterConfigReposProvider("github", func(_ context.Context, cfg hub.ConfigReposConfig) (hub.ConfigRepos, error) {
		return NewGitHubConfigRepos(cfg)
	})
}
