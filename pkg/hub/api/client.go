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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	maxResponseBytes   = 4 << 20 // 4 MiB response body cap
	maxErrorBytes      = 64 << 10
)

// APIError represents a non-2xx HTTP response returned by grain-registry.
type APIError struct {
	StatusCode int            `json:"-"`
	Message    string         `json:"error"`
	Code       string         `json:"code,omitempty"`
	Ref        string         `json:"ref,omitempty"`
	Conflict   *ScopeConflict `json:"conflict,omitempty"`
	RetryAfter time.Duration  `json:"-"`
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Ref != "" {
		return fmt.Sprintf("registry API %d (%s, ref %s): %s", e.StatusCode, e.Code, e.Ref, msg)
	}
	if e.Code != "" {
		return fmt.Sprintf("registry API %d (%s): %s", e.StatusCode, e.Code, msg)
	}
	return fmt.Sprintf("registry API %d: %s", e.StatusCode, msg)
}

// IsNotFound reports whether err is an HTTP 404 APIError.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is an HTTP 409 APIError.
func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusConflict
}

// IsUnauthorized reports whether err is an HTTP 401 APIError.
func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusUnauthorized
}

// IsForbidden reports whether err is an HTTP 403 APIError.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusForbidden
}

// IsRateLimited reports whether err is an HTTP 429 APIError.
func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusTooManyRequests
}

// IsServiceUnavailable reports whether err is an HTTP 503 APIError.
func IsServiceUnavailable(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusServiceUnavailable
}

func parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	ae := &APIError{
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	var wire struct {
		apierror.Response
		Conflict *ScopeConflict `json:"conflict,omitempty"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &wire) == nil && (wire.Error != "" || wire.Code != "" || wire.Conflict != nil) {
		ae.Message = wire.Error
		ae.Code = wire.Code
		ae.Ref = wire.Ref
		ae.Conflict = wire.Conflict
		return ae
	}
	ae.Message = strings.TrimSpace(string(body))
	return ae
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().IsLoopback()
	}
	return false
}

func isForbiddenMetadataHost(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "metadata" || (strings.HasSuffix(lower, ".internal") && strings.HasPrefix(lower, "metadata.")) {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		unmapped := addr.Unmap()
		if unmapped.IsLinkLocalUnicast() || unmapped.IsLinkLocalMulticast() || unmapped.IsUnspecified() {
			return true
		}
	}
	return false
}

func validateBaseURL(raw string, allowInsecureHTTP bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 2048 {
		return nil, fmt.Errorf("registry URL exceeds 2048 bytes")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid registry URL %q: %w", raw, err)
	}
	if u.User != nil {
		return nil, fmt.Errorf("registry URL must not include userinfo credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("registry URL must not include query parameters or fragments")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP && !isLoopbackHost(u.Host) {
			return nil, fmt.Errorf("refusing insecure http:// registry URL %q for non-loopback host", u.Redacted())
		}
	default:
		return nil, fmt.Errorf("registry URL %q must use https:// (or http:// on loopback)", raw)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("registry URL %q is missing host", raw)
	}
	if isForbiddenMetadataHost(host) {
		return nil, fmt.Errorf("registry URL host %q is not permitted", host)
	}
	u.Scheme = scheme
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// ValidateHubURL validates and normalizes a grain-registry base URL,
// requiring https:// except on loopback hosts (or when allowInsecureHTTP is true).
func ValidateHubURL(raw string, allowInsecureHTTP bool) (string, error) {
	u, err := validateBaseURL(raw, allowInsecureHTTP)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// ClientConfig configures a typed grain-registry HTTP Client.
type ClientConfig struct {
	// BaseURL is the registry root URL (for example "https://grain.example.com").
	BaseURL string
	// RegistrationToken is a grc_... entry credential for self-service calls
	// (heartbeats, tunnel setup, status updates). When set, requests carry
	// X-Grain-Auth: token and X-Grain-Registry-Token, plus Authorization: Bearer
	// when no BearerToken or IDToken callback occupies Authorization.
	RegistrationToken string
	// BearerToken is an optional static OIDC/IAP bearer token sent in
	// Authorization: Bearer.
	BearerToken string
	// IDToken optionally mints a fresh OIDC/IAP bearer token per request for
	// proxy traversal or CLI authentication.
	IDToken func(ctx context.Context) (string, error)
	// ExtraHeaders are optional additional headers attached to every request.
	ExtraHeaders http.Header
	// HTTPClient is the underlying HTTP client; defaults to a 30s-timeout client.
	HTTPClient *http.Client
	// UserAgent sets the User-Agent header when non-empty.
	UserAgent string
	// AllowInsecureHTTP permits plain http:// to non-loopback hosts (for tests).
	AllowInsecureHTTP bool
}

// Client is a typed HTTP client for the grain-registry REST API.
type Client struct {
	baseURL           *url.URL
	registrationToken string
	bearerToken       string
	idToken           func(ctx context.Context) (string, error)
	extraHeaders      http.Header
	httpClient        *http.Client
	userAgent         string
}

// NewClient validates cfg and returns a configured registry HTTP Client.
func NewClient(cfg ClientConfig) (*Client, error) {
	u, err := validateBaseURL(cfg.BaseURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	clonedHTTP := *hc
	clonedHTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		baseURL:           u,
		registrationToken: strings.TrimSpace(cfg.RegistrationToken),
		bearerToken:       strings.TrimSpace(cfg.BearerToken),
		idToken:           cfg.IDToken,
		extraHeaders:      cfg.ExtraHeaders.Clone(),
		httpClient:        &clonedHTTP,
		userAgent:         strings.TrimSpace(cfg.UserAgent),
	}, nil
}

func applyAuthHeaders(ctx context.Context, h http.Header, regToken, bearerToken string, idTokenFn func(context.Context) (string, error)) error {
	var proxyBearer string
	if idTokenFn != nil {
		tok, err := idTokenFn(ctx)
		if err != nil {
			return fmt.Errorf("obtaining registry ID token: %w", err)
		}
		proxyBearer = strings.TrimSpace(tok)
	} else if bearerToken != "" {
		proxyBearer = bearerToken
	}

	if regToken != "" {
		h.Set(HeaderAuth, AuthModeToken)
		h.Set(HeaderHubToken, regToken)
		h.Set(HeaderRegistryToken, regToken)
	}
	if proxyBearer != "" {
		h.Set("Authorization", "Bearer "+proxyBearer)
	} else if regToken != "" {
		h.Set("Authorization", "Bearer "+regToken)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, relPath string, query url.Values, reqBody any, respOut any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	u := *c.baseURL
	u.Path = c.baseURL.Path + relPath
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return err
	}
	for k, vals := range c.extraHeaders {
		for _, val := range vals {
			req.Header.Add(k, val)
		}
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if err := applyAuthHeaders(ctx, req.Header, c.registrationToken, c.bearerToken, c.idToken); err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}
	if respOut == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := dec.Decode(respOut); err != nil {
		return fmt.Errorf("decoding %s %s response: %w", method, relPath, err)
	}
	return nil
}

// Me fetches GET /api/me (authenticated principal, groups, and quota usage).
func (c *Client) Me(ctx context.Context) (*MeResponse, error) {
	var out MeResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/me", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Shapes fetches GET /api/shapes (available hosted VM shapes).
func (c *Client) Shapes(ctx context.Context) ([]Shape, error) {
	var out ShapesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/shapes", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Shapes, nil
}

// CheckName fetches GET /api/entries/<name>/availability.
func (c *Client) CheckName(ctx context.Context, name string) (*NameAvailabilityResponse, error) {
	var out NameAvailabilityResponse
	path := "/api/entries/" + url.PathEscape(name) + "/availability"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Directory fetches GET /api/directory with optional catalog search/filter parameters.
func (c *Client) Directory(ctx context.Context, q DirectoryQuery) (*DirectoryResponse, error) {
	vals := url.Values{}
	if q.Query != "" {
		vals.Set("q", q.Query)
	}
	if q.Repo != "" {
		vals.Set("repo", q.Repo)
	}
	if q.Tag != "" {
		vals.Set("tag", q.Tag)
	}
	if q.Status != "" {
		vals.Set("status", string(q.Status))
	}
	if q.Kind != "" {
		vals.Set("kind", string(q.Kind))
	}
	if q.HostKind != "" {
		vals.Set("host_kind", string(q.HostKind))
	}
	if q.Owner != "" {
		vals.Set("owner", q.Owner)
	}
	if q.Limit > 0 {
		vals.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		vals.Set("cursor", q.Cursor)
	}
	var out DirectoryResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/directory", vals, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Route fetches GET /api/route to resolve which entry covers a repository and path.
func (c *Client) Route(ctx context.Context, q RouteQuery) (*RouteResponse, error) {
	vals := url.Values{}
	vals.Set("repo", q.Repo)
	if q.Path != "" {
		vals.Set("path", q.Path)
	}
	if q.IncludeOffline {
		vals.Set("include_offline", "true")
	}
	var out RouteResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/route", vals, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListEntries fetches GET /api/entries for the caller (or admin filter).
func (c *Client) ListEntries(ctx context.Context, q ListEntriesQuery) ([]Entry, error) {
	vals := url.Values{}
	if q.Owner != "" {
		vals.Set("owner", q.Owner)
	}
	if q.OwnerGroup != "" {
		vals.Set("owner_group", q.OwnerGroup)
	}
	if q.Kind != "" {
		vals.Set("kind", string(q.Kind))
	}
	if q.Status != "" {
		vals.Set("status", string(q.Status))
	}
	if q.Listed != nil {
		vals.Set("listed", strconv.FormatBool(*q.Listed))
	}
	if q.Conflict != nil {
		vals.Set("conflict", strconv.FormatBool(*q.Conflict))
	}
	if q.IncludeDeleted {
		vals.Set("include_deleted", "true")
	}
	if q.All {
		vals.Set("all", "true")
	}
	if q.Mine {
		vals.Set("mine", "true")
	}
	var out ListEntriesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/entries", vals, nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// CreateEntry sends POST /api/entries to create an instance or connected entry.
func (c *Client) CreateEntry(ctx context.Context, req CreateEntryRequest) (*CreateEntryResponse, error) {
	var out CreateEntryResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/entries", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetEntry fetches GET /api/entries/<name>.
func (c *Client) GetEntry(ctx context.Context, name string) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateEntry sends PATCH /api/entries/<name>.
func (c *Client) UpdateEntry(ctx context.Context, name string, req UpdateEntryRequest) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodPatch, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetScopes sends PUT /api/entries/<name>/scopes to replace declared scopes.
func (c *Client) SetScopes(ctx context.Context, name string, scopes []ScopeInput) (*SetScopesResponse, error) {
	var out SetScopesResponse
	path := "/api/entries/" + url.PathEscape(name) + "/scopes"
	if err := c.doJSON(ctx, http.MethodPut, path, nil, SetScopesRequest{Scopes: scopes}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Heartbeat sends PUT /api/entries/<name>/heartbeat.
func (c *Client) Heartbeat(ctx context.Context, name string, req HeartbeatRequest) (*HeartbeatResponse, error) {
	var out HeartbeatResponse
	path := "/api/entries/" + url.PathEscape(name) + "/heartbeat"
	if err := c.doJSON(ctx, http.MethodPut, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InstanceStatus fetches GET /api/registry/status from a grain instance.
func (c *Client) InstanceStatus(ctx context.Context) (*InstanceStatusResponse, error) {
	var out InstanceStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/registry/status", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateCredential sends POST /api/entries/<name>/credentials/rotate.
func (c *Client) RotateCredential(ctx context.Context, name string, req RotateCredentialRequest) (*RotateCredentialResponse, error) {
	var out RotateCredentialResponse
	path := "/api/entries/" + url.PathEscape(name) + "/credentials/rotate"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeCredential sends DELETE /api/entries/<name>/credentials/<credentialID>.
func (c *Client) RevokeCredential(ctx context.Context, name, credentialID string) error {
	path := "/api/entries/" + url.PathEscape(name) + "/credentials/" + url.PathEscape(credentialID)
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, nil)
}

// UpgradeEntry sends POST /api/entries/<name>/upgrade.
func (c *Client) UpgradeEntry(ctx context.Context, name string, req UpgradeEntryRequest) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/upgrade"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplaceEntry sends POST /api/entries/<name>/replace.
func (c *Client) ReplaceEntry(ctx context.Context, name string, req ReplaceEntryRequest) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/replace"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreEntry sends POST /api/entries/<name>/restore.
func (c *Client) RestoreEntry(ctx context.Context, name string) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/restore"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopEntry sends POST /api/entries/<name>/stop to halt a hosted instance while keeping externalized state.
func (c *Client) StopEntry(ctx context.Context, name string) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/stop"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StartEntry sends POST /api/entries/<name>/start to wake a stopped hosted instance.
func (c *Client) StartEntry(ctx context.Context, name string) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/start"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteEntry sends DELETE /api/entries/<name>.
func (c *Client) DeleteEntry(ctx context.Context, name string, req DeleteEntryRequest) error {
	path := "/api/entries/" + url.PathEscape(name)
	return c.doJSON(ctx, http.MethodDelete, path, nil, req, nil)
}

// OfferTransfer sends POST /api/entries/<name>/transfer.
func (c *Client) OfferTransfer(ctx context.Context, name string, req TransferRequest) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/transfer"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AcceptTransfer sends POST /api/entries/<name>/transfer/accept.
func (c *Client) AcceptTransfer(ctx context.Context, name string) (*Entry, error) {
	var out Entry
	path := "/api/entries/" + url.PathEscape(name) + "/transfer/accept"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status fetches GET /api/status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/status", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AdminSummary fetches GET /api/admin/summary.
func (c *Client) AdminSummary(ctx context.Context) (*AdminSummaryResponse, error) {
	var out AdminSummaryResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/admin/summary", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetOwnerDisabled sends POST /api/admin/owners/<email>.
func (c *Client) SetOwnerDisabled(ctx context.Context, email string, disabled bool) (*OwnerQuotaSummary, error) {
	var out OwnerQuotaSummary
	path := "/api/admin/owners/" + url.PathEscape(email)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, SetOwnerDisabledRequest{Disabled: disabled}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetQuota sends PUT /api/admin/quotas/<email>.
func (c *Client) SetQuota(ctx context.Context, email string, req SetQuotaRequest) (*OwnerQuotaSummary, error) {
	var out OwnerQuotaSummary
	path := "/api/admin/quotas/" + url.PathEscape(email)
	if err := c.doJSON(ctx, http.MethodPut, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResetQuota sends DELETE /api/admin/quotas/<email> to remove a custom quota override.
func (c *Client) ResetQuota(ctx context.Context, email string) (*OwnerQuotaSummary, error) {
	var out OwnerQuotaSummary
	path := "/api/admin/quotas/" + url.PathEscape(email)
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveScopeConflict sends POST /api/admin/conflicts/resolve.
func (c *Client) ResolveScopeConflict(ctx context.Context, req ResolveConflictRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/admin/conflicts/resolve", nil, req, nil)
}

// SetSQLInstanceDraining sends POST /api/admin/sql-instances/<name>.
func (c *Client) SetSQLInstanceDraining(ctx context.Context, name string, draining bool) (*SQLInstanceStatus, error) {
	var out SQLInstanceStatus
	path := "/api/admin/sql-instances/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, SetSQLInstanceDrainingRequest{Draining: draining}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteOrphanVM sends DELETE /api/admin/orphans/<zone>/<instanceID>.
func (c *Client) DeleteOrphanVM(ctx context.Context, zone, instanceID string) error {
	path := "/api/admin/orphans/" + url.PathEscape(zone) + "/" + url.PathEscape(instanceID)
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, nil)
}

// DisconnectTunnel sends DELETE /api/admin/tunnels/<name>.
func (c *Client) DisconnectTunnel(ctx context.Context, name string) error {
	path := "/api/admin/tunnels/" + url.PathEscape(name)
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, nil)
}

// PurgeEntry sends POST /api/admin/entries/<name>/purge.
func (c *Client) PurgeEntry(ctx context.Context, name string, req HardPurgeRequest) (*Entry, error) {
	var out Entry
	path := "/api/admin/entries/" + url.PathEscape(name) + "/purge"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ForceTransfer sends POST /api/admin/entries/<name>/transfer.
func (c *Client) ForceTransfer(ctx context.Context, name string, req TransferRequest) (*Entry, error) {
	req.Force = true
	var out Entry
	path := "/api/admin/entries/" + url.PathEscape(name) + "/transfer"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDelegatingApps sends GET /api/admin/delegating-apps (admin only).
func (c *Client) ListDelegatingApps(ctx context.Context) ([]DelegatingApp, error) {
	var out ListDelegatingAppsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/admin/delegating-apps", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Apps, nil
}

// CreateDelegatingApp sends POST /api/admin/delegating-apps (admin only).
func (c *Client) CreateDelegatingApp(ctx context.Context, req CreateDelegatingAppRequest) (*DelegatingApp, error) {
	var out DelegatingApp
	if err := c.doJSON(ctx, http.MethodPost, "/api/admin/delegating-apps", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetDelegatingAppDisabled sends PATCH /api/admin/delegating-apps/<name> (admin only).
func (c *Client) SetDelegatingAppDisabled(ctx context.Context, name string, disabled bool) (*DelegatingApp, error) {
	var out DelegatingApp
	path := "/api/admin/delegating-apps/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodPatch, path, nil, SetDelegatingAppDisabledRequest{Disabled: disabled}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDelegationGrants sends GET /api/delegation-grants: the signed-in
// person's own grants, including revoked ones when includeRevoked is set.
func (c *Client) ListDelegationGrants(ctx context.Context, includeRevoked bool) ([]DelegationGrant, error) {
	var q url.Values
	if includeRevoked {
		q = url.Values{"include_revoked": {"true"}}
	}
	var out ListDelegationGrantsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/delegation-grants", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Grants, nil
}

// CreateDelegationGrant sends POST /api/delegation-grants, granting (or
// updating the signed-in person's live grant to) a delegating app.
func (c *Client) CreateDelegationGrant(ctx context.Context, req CreateDelegationGrantRequest) (*DelegationGrant, error) {
	var out DelegationGrant
	if err := c.doJSON(ctx, http.MethodPost, "/api/delegation-grants", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeDelegationGrant sends DELETE /api/delegation-grants/<ref>, where ref is
// a grant ID (`dlg_...`) or the name of an app the signed-in person has a
// live grant to.
func (c *Client) RevokeDelegationGrant(ctx context.Context, ref string) (*DelegationGrant, error) {
	var out DelegationGrant
	path := "/api/delegation-grants/" + url.PathEscape(ref)
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
