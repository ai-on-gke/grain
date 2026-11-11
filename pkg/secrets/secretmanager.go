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

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Supported backend names for -secrets-backend.
const (
	BackendFile          = "file"
	BackendSecretManager = "secretmanager"
)

const (
	// DefaultSecretManagerEndpoint is Google Cloud Secret Manager's v1 REST root.
	DefaultSecretManagerEndpoint = "https://secretmanager.googleapis.com"
	// DefaultSecretManagerCacheTTL is how long resolved secret values stay in the
	// in-memory read cache before being re-read from Secret Manager.
	DefaultSecretManagerCacheTTL = 30 * time.Second

	// SecretManagerEndpointEnv overrides SecretManagerConfig.Endpoint when the
	// config leaves Endpoint empty (for subprocess e2e tests against a fake).
	SecretManagerEndpointEnv = "GRAIN_SECRET_MANAGER_ENDPOINT"
	// SecretManagerTokenEnv supplies a static Bearer token when
	// SecretManagerConfig.Token is nil (for subprocess e2e tests against a fake).
	SecretManagerTokenEnv = "GRAIN_SECRET_MANAGER_TOKEN"

	defaultMetadataHost   = "169.254.169.254"
	maxSecretManagerIDLen = 255
	minSecretSlugBudget   = 16
	versionSweepLookback  = 32
)

// CatalogueEntry records one stored secret key's metadata in the deployment
// database (secret_entries). It never holds secret material.
type CatalogueEntry struct {
	SecretName string
	Key        string
	SMSecret   string
	Version    string
	UpdatedAt  time.Time
}

// Catalogue persists the names and keys stored in Secret Manager so List, Has,
// and bare-name Resolve work without project-wide secretmanager.secrets.list.
type Catalogue interface {
	ListSecretEntries(ctx context.Context) ([]CatalogueEntry, error)
	GetSecretEntry(ctx context.Context, secretName, key string) (CatalogueEntry, bool, error)
	PutSecretEntry(ctx context.Context, entry CatalogueEntry) error
	DeleteSecretEntry(ctx context.Context, secretName, key string) error
	DeleteSecretEntries(ctx context.Context, secretName string) error
}

// MemoryCatalogue is an in-memory Catalogue for unit tests.
type MemoryCatalogue struct {
	mu      sync.Mutex
	entries map[string]CatalogueEntry
}

// NewMemoryCatalogue returns an empty in-memory Catalogue.
func NewMemoryCatalogue() *MemoryCatalogue {
	return &MemoryCatalogue{entries: make(map[string]CatalogueEntry)}
}

func catalogueMapKey(secretName, key string) string {
	return secretName + "/" + key
}

func (m *MemoryCatalogue) ListSecretEntries(_ context.Context) ([]CatalogueEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CatalogueEntry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SecretName != out[j].SecretName {
			return out[i].SecretName < out[j].SecretName
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (m *MemoryCatalogue) GetSecretEntry(_ context.Context, secretName, key string) (CatalogueEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[catalogueMapKey(secretName, key)]
	return e, ok, nil
}

func (m *MemoryCatalogue) PutSecretEntry(_ context.Context, entry CatalogueEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	} else {
		entry.UpdatedAt = entry.UpdatedAt.UTC()
	}
	m.entries[catalogueMapKey(entry.SecretName, entry.Key)] = entry
	return nil
}

func (m *MemoryCatalogue) DeleteSecretEntry(_ context.Context, secretName, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, catalogueMapKey(secretName, key))
	return nil
}

func (m *MemoryCatalogue) DeleteSecretEntries(_ context.Context, secretName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := secretName + "/"
	for k := range m.entries {
		if strings.HasPrefix(k, prefix) {
			delete(m.entries, k)
		}
	}
	return nil
}

// SecretManagerConfig configures the Google Cloud Secret Manager backend for
// Store (-secrets-backend=secretmanager).
type SecretManagerConfig struct {
	// Prefix is the Secret Manager resource prefix every secret is stored
	// under: "projects/<project>/secrets/<id-prefix>", e.g.
	// "projects/123456789/secrets/grain-inst-alice-".
	Prefix string
	// Catalogue stores non-secret metadata (secret names, keys, Secret Manager
	// secret IDs, and active version numbers). Required.
	Catalogue Catalogue
	// Endpoint overrides DefaultSecretManagerEndpoint (or
	// $GRAIN_SECRET_MANAGER_ENDPOINT when empty).
	Endpoint string
	// HTTPClient is the HTTP client to use (a 30s-timeout client when nil).
	HTTPClient *http.Client
	// Token returns an OAuth2 Bearer token (when nil, uses
	// $GRAIN_SECRET_MANAGER_TOKEN if set, else the GCE metadata server).
	Token func(ctx context.Context) (string, error)
	// Labels are optional GCP labels attached when creating a new secret.
	Labels map[string]string
	// CacheTTL bounds how long resolved values stay in the in-memory read
	// cache. Zero uses DefaultSecretManagerCacheTTL; a negative duration
	// disables caching.
	CacheTTL time.Duration
	// Now overrides time.Now for cache expiry and timestamps in tests.
	Now func() time.Time
}

// ParseSecretManagerPrefix validates a -secrets-sm-prefix value of the form
// "projects/<project>/secrets/<id-prefix>" and returns <project> and
// <id-prefix>. Both parts must be non-empty, free of extra slashes, and valid
// Secret Manager path components with room left in the 255-character secretId
// limit for per-secret slugs.
func ParseSecretManagerPrefix(prefix string) (project, idPrefix string, err error) {
	if !strings.HasPrefix(prefix, "projects/") {
		return "", "", fmt.Errorf("secrets: Secret Manager prefix %q must have the form projects/<project>/secrets/<prefix>", prefix)
	}
	rest := strings.TrimPrefix(prefix, "projects/")
	proj, idPref, ok := strings.Cut(rest, "/secrets/")
	if !ok || proj == "" || idPref == "" || strings.Contains(proj, "/") || strings.Contains(idPref, "/") {
		return "", "", fmt.Errorf("secrets: Secret Manager prefix %q must have the form projects/<project>/secrets/<prefix>", prefix)
	}
	if !isValidProjectSegment(proj) {
		return "", "", fmt.Errorf("secrets: invalid project %q in Secret Manager prefix", proj)
	}
	if !isValidSecretID(idPref) {
		return "", "", fmt.Errorf("secrets: invalid secret ID prefix %q (only [a-zA-Z0-9_-] are permitted)", idPref)
	}
	if len(idPref) > maxSecretManagerIDLen-minSecretSlugBudget {
		return "", "", fmt.Errorf("secrets: secret ID prefix %q is too long (%d bytes; max %d)", idPref, len(idPref), maxSecretManagerIDLen-minSecretSlugBudget)
	}
	return proj, idPref, nil
}

// ValidateSecretManagerPrefix checks whether prefix is a valid
// -secrets-sm-prefix value.
func ValidateSecretManagerPrefix(prefix string) error {
	_, _, err := ParseSecretManagerPrefix(prefix)
	return err
}

func isValidProjectSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func isValidSecretID(s string) bool {
	if s == "" || len(s) > maxSecretManagerIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// isCleanSingleSlug reports whether secret can serve directly as the suffix
// after idPrefix when key == AgentCredentialKey ("value"): lowercase ASCII
// letters/digits separated by single hyphens (never underscores, so it can
// never collide with hashed slugs, which always contain "_").
func isCleanSingleSlug(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	prevHyphen := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			if prevHyphen {
				return false
			}
			prevHyphen = true
			continue
		}
		prevHyphen = false
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// secretSlug maps (secret, key) to a deterministic, collision-free suffix to
// append to idPrefix within Secret Manager's 255-byte secretId limit.
func secretSlug(secret, key string, idPrefixLen int) string {
	maxSlug := maxSecretManagerIDLen - idPrefixLen
	if key == AgentCredentialKey && isCleanSingleSlug(secret) && len(secret) <= maxSlug {
		return secret
	}
	sum := sha256.Sum256([]byte(secret + "/" + key))
	hash := fmt.Sprintf("%x", sum[:])[:12]

	raw := strings.ToLower(secret + "-" + key)
	var b strings.Builder
	b.Grow(len(raw))
	lastHyphen := true
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	sanitized := strings.Trim(b.String(), "-")
	maxBase := maxSlug - 1 - len(hash)
	if maxBase > 0 && len(sanitized) > maxBase {
		sanitized = strings.TrimRight(sanitized[:maxBase], "-")
	}
	if sanitized == "" || maxBase <= 0 {
		return "s_" + hash
	}
	return sanitized + "_" + hash
}

type cachedSecret struct {
	value     string
	expiresAt time.Time
}

type smBackend struct {
	prefix     string
	project    string
	idPrefix   string
	cat        Catalogue
	endpoint   string
	httpClient *http.Client
	tokenFn    func(ctx context.Context) (string, error)
	labels     map[string]string
	cacheTTL   time.Duration
	now        func() time.Time

	cacheMu sync.Mutex
	cache   map[string]cachedSecret

	tokMu      sync.Mutex
	tokCached  string
	tokExpires time.Time

	// goneMu guards gone: the catalogued keys knownValues last found
	// missing upstream, keyed by catalogueMapKey, so each is logged once when it
	// goes missing (and once when it comes back) rather than on every
	// redactor build. retained holds, for those keys, the value last read
	// before they went (taken from the read cache, so only while caching is
	// on): a run handed it earlier may still print it, so knownValues keeps
	// redacting it rather than dropping it the moment Secret Manager does.
	goneMu   sync.Mutex
	gone     map[string]bool
	retained map[string]string
}

// OpenSecretManager opens a Store backed by Google Cloud Secret Manager and a
// non-secret Catalogue. As with Open, configuration errors are retained on the
// returned Store and reported on Check, List, Resolve, Set, Has, and Delete*.
func OpenSecretManager(cfg SecretManagerConfig) *Store {
	s := &Store{backend: BackendSecretManager}
	if cfg.Catalogue == nil {
		s.openErr = errors.New("secrets: Secret Manager backend requires a non-nil Catalogue")
		return s
	}
	project, idPrefix, err := ParseSecretManagerPrefix(cfg.Prefix)
	if err != nil {
		s.openErr = err
		return s
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv(SecretManagerEndpointEnv))
	}
	if endpoint == "" {
		endpoint = DefaultSecretManagerEndpoint
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = DefaultSecretManagerCacheTTL
	}
	var labels map[string]string
	if len(cfg.Labels) > 0 {
		labels = make(map[string]string, len(cfg.Labels))
		for k, v := range cfg.Labels {
			labels[k] = v
		}
	}
	s.sm = &smBackend{
		prefix:     cfg.Prefix,
		project:    project,
		idPrefix:   idPrefix,
		cat:        cfg.Catalogue,
		endpoint:   strings.TrimRight(endpoint, "/"),
		httpClient: client,
		tokenFn:    cfg.Token,
		labels:     labels,
		cacheTTL:   ttl,
		now:        nowFn,
		cache:      make(map[string]cachedSecret),
	}
	return s
}

func (b *smBackend) secretIDForEntry(entry CatalogueEntry) (string, error) {
	raw := entry.SMSecret
	if raw == "" {
		return b.idPrefix + secretSlug(entry.SecretName, entry.Key, len(b.idPrefix)), nil
	}
	if strings.HasPrefix(raw, "projects/") {
		if !strings.HasPrefix(raw, b.prefix) {
			return "", fmt.Errorf("secrets: catalogue entry %s/%s names resource %q outside prefix %q", entry.SecretName, entry.Key, raw, b.prefix)
		}
		id := strings.TrimPrefix(raw, "projects/"+b.project+"/secrets/")
		if !isValidSecretID(id) {
			return "", fmt.Errorf("secrets: catalogue entry %s/%s has invalid Secret Manager ID %q", entry.SecretName, entry.Key, id)
		}
		return id, nil
	}
	id := raw
	if !strings.HasPrefix(id, b.idPrefix) {
		id = b.idPrefix + raw
	}
	if !isValidSecretID(id) || !strings.HasPrefix("projects/"+b.project+"/secrets/"+id, b.prefix) {
		return "", fmt.Errorf("secrets: catalogue entry %s/%s has invalid Secret Manager ID %q", entry.SecretName, entry.Key, raw)
	}
	return id, nil
}

func (b *smBackend) resourceName(secretID string) (string, error) {
	if !isValidSecretID(secretID) {
		return "", fmt.Errorf("secrets: invalid Secret Manager secret ID %q", secretID)
	}
	res := "projects/" + b.project + "/secrets/" + secretID
	if !strings.HasPrefix(res, b.prefix) {
		return "", fmt.Errorf("secrets: Secret Manager resource %q is outside configured prefix %q", res, b.prefix)
	}
	return res, nil
}

func (b *smBackend) getCached(secret, key string) (string, bool) {
	if b.cacheTTL <= 0 {
		return "", false
	}
	k := catalogueMapKey(secret, key)
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	entry, ok := b.cache[k]
	if !ok {
		return "", false
	}
	if !b.now().Before(entry.expiresAt) {
		// Left in place, not deleted: readEntry overwrites it, and if the
		// key turns out gone upstream it is the value redaction retains.
		return "", false
	}
	return entry.value, true
}

func (b *smBackend) putCached(secret, key, value string) {
	if b.cacheTTL <= 0 {
		return
	}
	b.cacheMu.Lock()
	b.cache[catalogueMapKey(secret, key)] = cachedSecret{
		value:     value,
		expiresAt: b.now().Add(b.cacheTTL),
	}
	b.cacheMu.Unlock()
}

func (b *smBackend) deleteCached(secret, key string) {
	k := catalogueMapKey(secret, key)
	b.cacheMu.Lock()
	delete(b.cache, k)
	b.cacheMu.Unlock()
	b.goneMu.Lock()
	delete(b.retained, k)
	b.goneMu.Unlock()
}

// retainGone moves the last value cached for a key just found gone upstream,
// expired or not, out of the read cache and into retained, so it stops
// resolving but is still redacted.
func (b *smBackend) retainGone(secret, key string) {
	k := catalogueMapKey(secret, key)
	b.cacheMu.Lock()
	entry, ok := b.cache[k]
	delete(b.cache, k)
	b.cacheMu.Unlock()
	if !ok {
		return
	}
	b.goneMu.Lock()
	if b.retained == nil {
		b.retained = make(map[string]string)
	}
	b.retained[k] = entry.value
	b.goneMu.Unlock()
}

func (b *smBackend) retainedValue(secret, key string) (string, bool) {
	b.goneMu.Lock()
	defer b.goneMu.Unlock()
	v, ok := b.retained[catalogueMapKey(secret, key)]
	return v, ok
}

func (b *smBackend) token(ctx context.Context) (string, error) {
	if b.tokenFn != nil {
		return b.tokenFn(ctx)
	}
	if envTok := strings.TrimSpace(os.Getenv(SecretManagerTokenEnv)); envTok != "" {
		return envTok, nil
	}
	b.tokMu.Lock()
	defer b.tokMu.Unlock()
	now := b.now()
	if b.tokCached != "" && now.Before(b.tokExpires) {
		return b.tokCached, nil
	}
	tok, ttl, err := fetchMetadataToken(ctx, b.httpClient)
	if err != nil {
		return "", err
	}
	b.tokCached = tok
	b.tokExpires = now.Add(ttl)
	return tok, nil
}

// fetchMetadataToken asks the metadata server for the default service
// account's access token, and how long to keep it. On GCE that is the
// VM's account; on GKE with Workload Identity it is the Google service
// account the pod's Kubernetes service account is bound to, which the
// GKE metadata server answers for on the same path. Both the Secret
// Manager backend and the KMS key source authenticate this way.
func fetchMetadataToken(ctx context.Context, client *http.Client) (string, time.Duration, error) {
	host := strings.TrimSpace(os.Getenv("GCE_METADATA_HOST"))
	if host == "" {
		host = defaultMetadataHost
	}
	metaCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(metaCtx, http.MethodGet,
		"http://"+host+"/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", 0, fmt.Errorf("secrets: building metadata token request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("secrets: metadata token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", 0, fmt.Errorf("secrets: metadata token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok); err != nil {
		return "", 0, fmt.Errorf("secrets: decoding metadata token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", 0, errors.New("secrets: metadata server returned an empty access_token")
	}
	ttl := 30 * time.Second
	if tok.ExpiresIn > 120 {
		ttl = time.Duration(tok.ExpiresIn-60) * time.Second
	}
	return tok.AccessToken, ttl, nil
}

func (b *smBackend) do(ctx context.Context, method, path string, payload []byte) ([]byte, int, error) {
	tok, err := b.token(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.endpoint+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("secrets: secret manager %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("secrets: reading secret manager response: %w", err)
	}
	return body, resp.StatusCode, nil
}

type smCreateSecretRequest struct {
	Replication smReplication     `json:"replication"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type smReplication struct {
	Automatic *struct{} `json:"automatic"`
}

type smAddVersionRequest struct {
	Payload smPayloadData `json:"payload"`
}

type smPayloadData struct {
	Data string `json:"data"`
}

type smVersionResponse struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type smAccessResponse struct {
	Name    string        `json:"name"`
	Payload smPayloadData `json:"payload"`
}

func (b *smBackend) createSecret(ctx context.Context, secretID string) error {
	if _, err := b.resourceName(secretID); err != nil {
		return err
	}
	reqBody := smCreateSecretRequest{
		Replication: smReplication{Automatic: &struct{}{}},
		Labels:      b.labels,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	path := "/v1/projects/" + url.PathEscape(b.project) + "/secrets?secretId=" + url.QueryEscape(secretID)
	body, status, err := b.do(ctx, http.MethodPost, path, raw)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("secrets: creating Secret Manager secret %s: HTTP %d: %s", secretID, status, strings.TrimSpace(string(body)))
}

func (b *smBackend) addVersion(ctx context.Context, resource string, value []byte) (string, int, []byte, error) {
	reqBody := smAddVersionRequest{
		Payload: smPayloadData{Data: base64.StdEncoding.EncodeToString(value)},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, nil, err
	}
	body, status, err := b.do(ctx, http.MethodPost, "/v1/"+resource+":addVersion", raw)
	if err != nil {
		return "", 0, nil, err
	}
	if status != http.StatusOK {
		return "", status, body, nil
	}
	var ver smVersionResponse
	if err := json.Unmarshal(body, &ver); err != nil {
		return "", status, body, fmt.Errorf("secrets: decoding SecretVersion from %s: %w", resource, err)
	}
	verID := ver.Name
	if idx := strings.LastIndex(ver.Name, "/versions/"); idx >= 0 {
		verID = ver.Name[idx+len("/versions/"):]
	}
	if verID == "" {
		return "", status, body, fmt.Errorf("secrets: Secret Manager returned empty version name for %s", resource)
	}
	return verID, status, body, nil
}

func (b *smBackend) destroyVersion(ctx context.Context, resource, version string) error {
	body, status, err := b.do(ctx, http.MethodPost, "/v1/"+resource+"/versions/"+url.PathEscape(version)+":destroy", []byte("{}"))
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusNotFound:
		return nil
	case http.StatusBadRequest:
		// Destroying an already-DESTROYED version in GCP Secret Manager returns
		// HTTP 400 FAILED_PRECONDITION mentioning DESTROYED.
		upper := strings.ToUpper(string(body))
		if strings.Contains(upper, "DESTROYED") || strings.Contains(upper, "FAILED_PRECONDITION") {
			return nil
		}
	}
	return fmt.Errorf("secrets: destroying %s/versions/%s: HTTP %d: %s", resource, version, status, strings.TrimSpace(string(body)))
}

func (b *smBackend) accessLatest(ctx context.Context, resource string) ([]byte, error) {
	body, status, err := b.do(ctx, http.MethodGet, "/v1/"+resource+"/versions/latest:access", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		if accessGoneUpstream(status, body) {
			return nil, fmt.Errorf("%w: reading %s: HTTP %d: %s", ErrGoneUpstream, resource, status, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("secrets: reading %s: HTTP %d: %s", resource, status, strings.TrimSpace(string(body)))
	}
	var resp smAccessResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("secrets: decoding payload of %s: %w", resource, err)
	}
	if resp.Payload.Data == "" {
		return []byte{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(resp.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("secrets: decoding base64 payload of %s: %w", resource, err)
	}
	return data, nil
}

// accessGoneUpstream reports whether a failed versions/latest:access means the
// value is gone rather than unreachable: the secret itself is deleted (404
// NOT_FOUND, which is also what Secret Manager answers when no version is
// left enabled), or the latest version is destroyed or disabled (400
// FAILED_PRECONDITION naming the state). Anything else -- 401, 403, 5xx -- is
// a failure to read, not evidence of absence, and stays fatal. Only a Google
// API error body counts: a bare 404 from a misconfigured endpoint or a proxy
// in front of it says nothing about the secret, and treating it as gone would
// silently drop every value from redaction.
func accessGoneUpstream(status int, body []byte) bool {
	var apiErr struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err != nil || apiErr.Error.Code != status {
		return false
	}
	switch status {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		msg := strings.ToUpper(apiErr.Error.Message)
		return strings.EqualFold(apiErr.Error.Status, "FAILED_PRECONDITION") &&
			(strings.Contains(msg, "DESTROYED") || strings.Contains(msg, "DISABLED"))
	}
	return false
}

func (b *smBackend) deleteSecretResource(ctx context.Context, resource string) error {
	body, status, err := b.do(ctx, http.MethodDelete, "/v1/"+resource, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNoContent || status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("secrets: deleting %s: HTTP %d: %s", resource, status, strings.TrimSpace(string(body)))
}

func (b *smBackend) check(ctx context.Context) error {
	_, err := b.cat.ListSecretEntries(ctx)
	return err
}

func (b *smBackend) list(ctx context.Context) ([]SecretInfo, error) {
	entries, err := b.cat.ListSecretEntries(ctx)
	if err != nil {
		return nil, err
	}
	bySecret := make(map[string][]string)
	for _, e := range entries {
		bySecret[e.SecretName] = append(bySecret[e.SecretName], e.Key)
	}
	out := make([]SecretInfo, 0, len(bySecret))
	for name, keys := range bySecret {
		sort.Strings(keys)
		out = append(out, SecretInfo{Name: name, Keys: keys})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *smBackend) has(ctx context.Context, secret, key string) (bool, error) {
	_, ok, err := b.cat.GetSecretEntry(ctx, secret, key)
	return ok, err
}

func (b *smBackend) set(ctx context.Context, secret, key string, value []byte) error {
	existing, exists, err := b.cat.GetSecretEntry(ctx, secret, key)
	if err != nil {
		return err
	}
	var secretID string
	if exists && existing.SMSecret != "" {
		secretID, err = b.secretIDForEntry(existing)
		if err != nil {
			return err
		}
	} else {
		secretID = b.idPrefix + secretSlug(secret, key, len(b.idPrefix))
	}
	resource, err := b.resourceName(secretID)
	if err != nil {
		return err
	}

	verID, status, body, err := b.addVersion(ctx, resource, value)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		if err := b.createSecret(ctx, secretID); err != nil {
			return err
		}
		verID, status, body, err = b.addVersion(ctx, resource, value)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("secrets: adding version to %s: HTTP %d: %s", resource, status, strings.TrimSpace(string(body)))
	}

	now := b.now().UTC()
	// Ensure the secret is tracked in the catalogue before destroying older
	// versions so List/Delete never lose track of a created Secret Manager
	// secret if version cleanup fails partway through.
	if !exists {
		initialVer := ""
		if verNum, convErr := strconv.Atoi(verID); convErr != nil || verNum <= 1 {
			initialVer = verID
		}
		if err := b.cat.PutSecretEntry(ctx, CatalogueEntry{
			SecretName: secret,
			Key:        key,
			SMSecret:   secretID,
			Version:    initialVer,
			UpdatedAt:  now,
		}); err != nil {
			return err
		}
	}

	// Destroy superseded versions without requiring secretmanager.versions.list:
	// Secret Manager version numbers are sequential integers starting at 1.
	if newVerNum, convErr := strconv.Atoi(verID); convErr == nil && newVerNum > 1 {
		startVer := 1
		if newVerNum > versionSweepLookback {
			startVer = newVerNum - versionSweepLookback
		}
		if exists {
			if prevNum, prevErr := strconv.Atoi(existing.Version); prevErr == nil && prevNum >= 1 && prevNum < newVerNum {
				startVer = prevNum
			}
		}
		for v := startVer; v < newVerNum; v++ {
			if err := b.destroyVersion(ctx, resource, strconv.Itoa(v)); err != nil {
				// Invalidate any stale cached value even if cleanup failed, and
				// keep the previous Version in the catalogue so the next Set
				// retries destroying from startVer.
				b.deleteCached(secret, key)
				return err
			}
		}
	}

	if exists || verID != "1" {
		if err := b.cat.PutSecretEntry(ctx, CatalogueEntry{
			SecretName: secret,
			Key:        key,
			SMSecret:   secretID,
			Version:    verID,
			UpdatedAt:  now,
		}); err != nil {
			b.deleteCached(secret, key)
			return err
		}
	}

	b.putCached(secret, key, string(value))
	return nil
}

func (b *smBackend) resolveEntry(ctx context.Context, entry CatalogueEntry) (string, error) {
	if cached, ok := b.getCached(entry.SecretName, entry.Key); ok {
		return cached, nil
	}
	return b.readEntry(ctx, entry)
}

// readEntry reads entry's latest value from Secret Manager, bypassing the
// cache, and caches what it reads. A value found gone upstream is dropped
// from the cache, so a deleted secret stops resolving as soon as anything
// notices rather than when its cache entry would have expired; it is
// retained for redaction only (see retainGone).
func (b *smBackend) readEntry(ctx context.Context, entry CatalogueEntry) (string, error) {
	secretID, err := b.secretIDForEntry(entry)
	if err != nil {
		return "", err
	}
	resource, err := b.resourceName(secretID)
	if err != nil {
		return "", err
	}
	data, err := b.accessLatest(ctx, resource)
	if err != nil {
		if errors.Is(err, ErrGoneUpstream) {
			b.retainGone(entry.SecretName, entry.Key)
			return "", fmt.Errorf("%w (run `grain secrets delete %s %s` to drop it from the catalogue, or set it again)",
				err, entry.SecretName, entry.Key)
		}
		return "", err
	}
	val := string(data)
	b.putCached(entry.SecretName, entry.Key, val)
	b.goneMu.Lock()
	delete(b.retained, catalogueMapKey(entry.SecretName, entry.Key))
	b.goneMu.Unlock()
	return val, nil
}

func (b *smBackend) resolve(ctx context.Context, secret, key string, explicit bool) (resolvedKey, value string, err error) {
	entries, err := b.cat.ListSecretEntries(ctx)
	if err != nil {
		return "", "", err
	}
	var matches []CatalogueEntry
	for _, e := range entries {
		if e.SecretName == secret {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("%w named %q", ErrNoSecret, secret)
	}
	var target CatalogueEntry
	var found bool
	if !explicit {
		if len(matches) != 1 {
			names := make([]string, 0, len(matches))
			for _, m := range matches {
				names = append(names, m.Key)
			}
			sort.Strings(names)
			return "", "", fmt.Errorf(
				"secrets: secret %q has %d keys (%s), name one explicitly as %q",
				secret, len(matches), strings.Join(names, ", "), secret+"/<key>")
		}
		target = matches[0]
		found = true
	} else {
		for _, m := range matches {
			if m.Key == key {
				target = m
				found = true
				break
			}
		}
	}
	if !found {
		return "", "", fmt.Errorf("secrets: no key %q in secret %q", key, secret)
	}
	val, err := b.resolveEntry(ctx, target)
	if err != nil {
		return "", "", err
	}
	return target.Key, val, nil
}

func (b *smBackend) knownValues(ctx context.Context) ([]SecretValue, error) {
	entries, err := b.cat.ListSecretEntries(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SecretName != entries[j].SecretName {
			return entries[i].SecretName < entries[j].SecretName
		}
		return entries[i].Key < entries[j].Key
	})
	out := make([]SecretValue, 0, len(entries))
	for _, entry := range entries {
		val, err := b.resolveEntry(ctx, entry)
		if errors.Is(err, ErrGoneUpstream) {
			// Not fatal: see Store.KnownValues. A value this process read
			// before it went is still redacted.
			b.noteGone(entry, err)
			if last, ok := b.retainedValue(entry.SecretName, entry.Key); ok {
				out = append(out, SecretValue{SecretName: entry.SecretName, Key: entry.Key, Value: last})
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", entry.SecretName, entry.Key, err)
		}
		b.noteGone(entry, nil)
		out = append(out, SecretValue{
			SecretName: entry.SecretName,
			Key:        entry.Key,
			Value:      val,
		})
	}
	return out, nil
}

// noteGone records whether entry was just found gone upstream (err non-nil)
// or readable (err nil), logging each change of state once.
func (b *smBackend) noteGone(entry CatalogueEntry, err error) {
	k := catalogueMapKey(entry.SecretName, entry.Key)
	b.goneMu.Lock()
	was := b.gone[k]
	if err != nil {
		if b.gone == nil {
			b.gone = make(map[string]bool)
		}
		b.gone[k] = true
	} else {
		delete(b.gone, k)
	}
	b.goneMu.Unlock()
	switch {
	case err != nil && !was:
		log.Printf("grain secrets: %s/%s is in the catalogue but %v; leaving it out of redaction until it is reconciled",
			entry.SecretName, entry.Key, err)
	case err == nil && was:
		log.Printf("grain secrets: %s/%s is readable in Secret Manager again", entry.SecretName, entry.Key)
	}
}

// missingUpstream probes every catalogued entry in Secret Manager, bypassing
// the cache, and reports those that are gone upstream.
func (b *smBackend) missingUpstream(ctx context.Context) ([]SecretKeyRef, error) {
	entries, err := b.cat.ListSecretEntries(ctx)
	if err != nil {
		return nil, err
	}
	var out []SecretKeyRef
	for _, entry := range entries {
		_, err := b.readEntry(ctx, entry)
		if errors.Is(err, ErrGoneUpstream) {
			out = append(out, SecretKeyRef{SecretName: entry.SecretName, Key: entry.Key})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", entry.SecretName, entry.Key, err)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SecretName != out[j].SecretName {
			return out[i].SecretName < out[j].SecretName
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (b *smBackend) deleteKey(ctx context.Context, secret, key string) error {
	entry, ok, err := b.cat.GetSecretEntry(ctx, secret, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("secrets: no key %q in secret %q", key, secret)
	}
	secretID, err := b.secretIDForEntry(entry)
	if err != nil {
		return err
	}
	resource, err := b.resourceName(secretID)
	if err != nil {
		return err
	}
	if err := b.deleteSecretResource(ctx, resource); err != nil {
		return err
	}
	if err := b.cat.DeleteSecretEntry(ctx, secret, key); err != nil {
		b.deleteCached(secret, key)
		return err
	}
	b.deleteCached(secret, key)
	return nil
}

func (b *smBackend) deleteSecret(ctx context.Context, secret string) error {
	entries, err := b.cat.ListSecretEntries(ctx)
	if err != nil {
		return err
	}
	var matches []CatalogueEntry
	for _, e := range entries {
		if e.SecretName == secret {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("%w named %q", ErrNoSecret, secret)
	}
	for _, entry := range matches {
		secretID, err := b.secretIDForEntry(entry)
		if err != nil {
			return err
		}
		resource, err := b.resourceName(secretID)
		if err != nil {
			return err
		}
		if err := b.deleteSecretResource(ctx, resource); err != nil {
			return err
		}
		if err := b.cat.DeleteSecretEntry(ctx, secret, entry.Key); err != nil {
			b.deleteCached(secret, entry.Key)
			return err
		}
		b.deleteCached(secret, entry.Key)
	}
	return b.cat.DeleteSecretEntries(ctx, secret)
}
