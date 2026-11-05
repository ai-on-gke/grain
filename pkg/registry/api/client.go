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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20 // 4 MiB

// TokenSource returns the bearer credential (either a "grc_..." registration
// token or an OIDC ID token) to attach to a registry request.
type TokenSource func(ctx context.Context) (string, error)

// StaticTokenSource returns a TokenSource that always yields token.
func StaticTokenSource(token string) TokenSource {
	trimmed := strings.TrimSpace(token)
	return func(context.Context) (string, error) {
		if trimmed == "" {
			return "", fmt.Errorf("empty registry credential")
		}
		return trimmed, nil
	}
}

// TunnelConnector is the outbound-tunnel seam reserved for the follow-up
// connected-proxy design (connect_mode = 'tunnel', design/design/registry.design.md).
// A connection manager can share the Client's base URL, HTTP transport, and
// TokenSource when attaching a persistent reverse-proxy session.
type TunnelConnector interface {
	RunTunnel(ctx context.Context, client *Client, entryName string) error
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the HTTP client used by Client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithTokenSource configures bearer authentication for outgoing requests.
func WithTokenSource(ts TokenSource) ClientOption {
	return func(c *Client) {
		c.tokenSource = ts
	}
}

// WithAllowInsecureHTTP allows plain http:// registry URLs on non-loopback
// hosts (intended strictly for isolated unit-test environments).
func WithAllowInsecureHTTP(allow bool) ClientOption {
	return func(c *Client) {
		c.allowInsecureHTTP = allow
	}
}

// WithUserAgent overrides the User-Agent header sent on registry requests.
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) {
		c.userAgent = strings.TrimSpace(ua)
	}
}

// Client is a typed HTTP client for the grain registry API.
type Client struct {
	baseURL           string
	httpClient        *http.Client
	tokenSource       TokenSource
	allowInsecureHTTP bool
	userAgent         string
}

// NewClient validates baseURL and constructs a registry API Client.
func NewClient(baseURL string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		userAgent:  "grain-registry-client/1.0",
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	norm, err := ValidateRegistryURL(baseURL, c.allowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	c.baseURL = norm
	return c, nil
}

// BaseURL returns the normalized registry base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// HTTPClient returns the underlying *http.Client (used by future tunnel transports).
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// BearerToken resolves the current bearer token from the configured TokenSource,
// or "" if no TokenSource is configured.
func (c *Client) BearerToken(ctx context.Context) (string, error) {
	if c.tokenSource == nil {
		return "", nil
	}
	tok, err := c.tokenSource(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tok), nil
}

// APIError represents a non-2xx HTTP response from the registry.
type APIError struct {
	Method     string          `json:"-"`
	Path       string          `json:"-"`
	StatusCode int             `json:"status_code"`
	Message    string          `json:"error"`
	RetryAfter time.Duration   `json:"-"`
	Conflicts  []ScopeConflict `json:"conflicts,omitempty"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("registry %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("registry %s %s: HTTP %d (%s)", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
}

// IsNotFound reports whether err is a 404 Not Found from the registry.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is a 409 Conflict from the registry.
func IsConflict(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// IsUnauthorized reports whether err is a 401 Unauthorized or 403 Forbidden.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) &&
		(apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden)
}

// IsRetryable reports whether err is transient (network failure, 429, or 5xx).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return true
}

// CreateEntry reserves or creates an entry via POST /api/entries.
func (c *Client) CreateEntry(ctx context.Context, req CreateEntryRequest) (CreateEntryResponse, error) {
	var resp CreateEntryResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/entries", req, &resp); err != nil {
		return CreateEntryResponse{}, err
	}
	return resp, nil
}

// GetEntry fetches an entry via GET /api/entries/{name}.
func (c *Client) GetEntry(ctx context.Context, name string) (Entry, error) {
	if err := ValidateEntryName(name); err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := c.doJSON(ctx, http.MethodGet, "/api/entries/"+url.PathEscape(name), nil, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// ListEntries lists entries managed by the caller (or all entries for admins when all is true).
func (c *Client) ListEntries(ctx context.Context, all bool) ([]Entry, error) {
	path := "/api/entries"
	if all {
		path += "?all=true"
	}
	var entries []Entry
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// UpdateEntry patches mutable catalog fields via PATCH /api/entries/{name}.
func (c *Client) UpdateEntry(ctx context.Context, name string, req UpdateEntryRequest) (Entry, error) {
	if err := ValidateEntryName(name); err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := c.doJSON(ctx, http.MethodPatch, "/api/entries/"+url.PathEscape(name), req, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// DeleteEntry deletes or unregisters an entry via DELETE /api/entries/{name}.
func (c *Client) DeleteEntry(ctx context.Context, name string) error {
	if err := ValidateEntryName(name); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodDelete, "/api/entries/"+url.PathEscape(name), DeleteEntryRequest{Confirm: name}, nil)
}

// Heartbeat pushes a connected entry's liveness, build info, task counts, and
// self-reported scopes via PUT /api/entries/{name}/heartbeat.
func (c *Client) Heartbeat(ctx context.Context, name string, req HeartbeatRequest) (HeartbeatResponse, error) {
	if err := ValidateEntryName(name); err != nil {
		return HeartbeatResponse{}, err
	}
	if req.Scopes == nil {
		req.Scopes = []Scope{}
	}
	var resp HeartbeatResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/entries/"+url.PathEscape(name)+"/heartbeat", req, &resp); err != nil {
		return HeartbeatResponse{}, err
	}
	return resp, nil
}

// PromoteConnected converts a redirect entry into a connected entry in place
// via POST /api/entries/{name}/promote-connected.
func (c *Client) PromoteConnected(ctx context.Context, name string, req PromoteConnectedRequest) (CreateEntryResponse, error) {
	if err := ValidateEntryName(name); err != nil {
		return CreateEntryResponse{}, err
	}
	var resp CreateEntryResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/entries/"+url.PathEscape(name)+"/promote-connected", req, &resp); err != nil {
		return CreateEntryResponse{}, err
	}
	return resp, nil
}

// RotateCredential mints a replacement registration token via
// POST /api/entries/{name}/credentials/rotate.
func (c *Client) RotateCredential(ctx context.Context, name string, req RotateCredentialRequest) (RotateCredentialResponse, error) {
	if err := ValidateEntryName(name); err != nil {
		return RotateCredentialResponse{}, err
	}
	var resp RotateCredentialResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/entries/"+url.PathEscape(name)+"/credentials/rotate", req, &resp); err != nil {
		return RotateCredentialResponse{}, err
	}
	return resp, nil
}

// Directory queries public listed entries via GET /api/directory.
func (c *Client) Directory(ctx context.Context, q DirectoryQuery) ([]DirectoryEntry, error) {
	vals := url.Values{}
	if q.Query != "" {
		vals.Set("q", q.Query)
	}
	if q.Repo != "" {
		vals.Set("repo", q.Repo)
	}
	for _, t := range q.Tags {
		if t != "" {
			vals.Add("tag", t)
		}
	}
	if len(q.Statuses) > 0 {
		parts := make([]string, 0, len(q.Statuses))
		for _, s := range q.Statuses {
			parts = append(parts, string(s))
		}
		vals.Set("status", strings.Join(parts, ","))
	}
	if q.HostKind != "" {
		vals.Set("host_kind", string(q.HostKind))
	}
	if q.Owner != "" {
		vals.Set("owner", q.Owner)
	}
	path := "/api/directory"
	if enc := vals.Encode(); enc != "" {
		path += "?" + enc
	}
	var out []DirectoryEntry
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Route resolves candidate grains for a repository and path via GET /api/route.
func (c *Client) Route(ctx context.Context, q RouteQuery) ([]RouteCandidate, error) {
	vals := url.Values{}
	vals.Set("repo", q.Repo)
	if q.Path != "" {
		vals.Set("path", q.Path)
	}
	if q.IncludeOffline {
		vals.Set("include_offline", "true")
	}
	var out []RouteCandidate
	if err := c.doJSON(ctx, http.MethodGet, "/api/route?"+vals.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any, respBody any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encoding request body for %s %s: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("building request %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.tokenSource != nil {
		token, err := c.tokenSource(ctx)
		if err != nil {
			return fmt.Errorf("obtaining registry credential: %w", err)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return fmt.Errorf("obtaining registry credential: empty token")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("registry %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("reading registry %s %s response: %w", method, path, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("registry %s %s response exceeded %d bytes", method, path, maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(method, path, resp.StatusCode, resp.Header, data)
	}

	if respBody != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, respBody); err != nil {
			return fmt.Errorf("decoding registry %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func parseAPIError(method, path string, statusCode int, hdr http.Header, body []byte) error {
	apiErr := &APIError{
		Method:     method,
		Path:       path,
		StatusCode: statusCode,
		RetryAfter: parseRetryAfter(hdr.Get("Retry-After")),
	}
	var payload struct {
		Error          string          `json:"error"`
		Message        string          `json:"message"`
		Conflicts      []ScopeConflict `json:"conflicts"`
		ScopeConflicts []ScopeConflict `json:"scope_conflicts"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &payload); err == nil {
			switch {
			case payload.Error != "":
				apiErr.Message = payload.Error
			case payload.Message != "":
				apiErr.Message = payload.Message
			}
			if len(payload.Conflicts) > 0 {
				apiErr.Conflicts = payload.Conflicts
			} else if len(payload.ScopeConflicts) > 0 {
				apiErr.Conflicts = payload.ScopeConflicts
			}
		}
		if apiErr.Message == "" {
			msg := strings.TrimSpace(string(body))
			if len(msg) > 512 {
				msg = msg[:512] + "..."
			}
			apiErr.Message = msg
		}
	}
	return apiErr
}

func parseRetryAfter(val string) time.Duration {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
