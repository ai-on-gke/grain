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

package grainclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
	registryapi "github.com/ai-on-gke/grain/pkg/hub/api"
)

// DefaultDirectoryCacheTTL matches the 30 s replica cache decision.
const DefaultDirectoryCacheTTL = 30 * time.Second

// APIError represents a structured HTTP error response from a Grain daemon or registry.
type APIError struct {
	StatusCode int    `json:"status_code"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("grain api status %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("grain api status %d: %s", e.StatusCode, e.Message)
}

// Config configures a Client talking to the Grain Registry and one or more Grain daemons.
type Config struct {
	RegistryURL string
	GrainURL    string
	ClientName  string
	BearerToken string
	// GrainToken is sent as the bearer token only on requests to
	// GrainURL's own origin, never to the registry or to an instance the
	// registry names: it is that one daemon's access code or
	// external-agent token, and another host has no business seeing it.
	GrainToken        string
	RegistryAssertion string
	DirectoryCacheTTL time.Duration
	HTTPClient        *http.Client
	Perf              *perf.Recorder
}

// Client wraps pkg/hub/api.Client and provides InstanceClient handles for
// Grain daemons discovered via the registry or configured directly.
type Client struct {
	cfg        Config
	httpClient *http.Client
	regClient  *registryapi.Client
	perf       *perf.Recorder

	mu          sync.RWMutex
	cachedDir   *DirectoryResponse
	cachedUntil time.Time
}

// New creates a new Client from cfg.
func New(cfg Config) *Client {
	if cfg.ClientName == "" {
		cfg.ClientName = ClientPLCStudio
	}
	if cfg.DirectoryCacheTTL == 0 {
		cfg.DirectoryCacheTTL = DefaultDirectoryCacheTTL
	}
	recorder := cfg.Perf
	if recorder == nil {
		recorder = perf.Default()
	}

	baseHTTP := cfg.HTTPClient
	if baseHTTP == nil {
		baseHTTP = &http.Client{Timeout: 30 * time.Second}
	}
	baseTransport := baseHTTP.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}

	wrappedHTTP := *baseHTTP
	wrappedHTTP.Transport = &delegatingTransport{
		base:              baseTransport,
		clientName:        cfg.ClientName,
		bearerToken:       cfg.BearerToken,
		grainToken:        cfg.GrainToken,
		grainOrigin:       originOf(cfg.GrainURL),
		registryAssertion: cfg.RegistryAssertion,
	}

	var reg *registryapi.Client
	if strings.TrimSpace(cfg.RegistryURL) != "" {
		rc, err := registryapi.NewClient(registryapi.ClientConfig{
			BaseURL:           cfg.RegistryURL,
			BearerToken:       cfg.BearerToken,
			HTTPClient:        &wrappedHTTP,
			AllowInsecureHTTP: true,
		})
		if err == nil {
			reg = rc
		}
	}

	return &Client{
		cfg:        cfg,
		httpClient: &wrappedHTTP,
		regClient:  reg,
		perf:       recorder,
	}
}

type delegatingTransport struct {
	base              http.RoundTripper
	clientName        string
	bearerToken       string
	grainToken        string
	grainOrigin       string
	registryAssertion string
}

// originOf is rawURL's scheme://host, or "" when it has none.
func originOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func (t *delegatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.Header.Get(plcseams.HeaderClient) == "" && t.clientName != "" {
		clone.Header.Set(plcseams.HeaderClient, t.clientName)
	}
	if clone.Header.Get("Authorization") == "" && t.grainToken != "" && t.grainOrigin != "" &&
		originOf(clone.URL.String()) == t.grainOrigin {
		clone.Header.Set("Authorization", "Bearer "+t.grainToken)
	}
	if clone.Header.Get("Authorization") == "" && t.bearerToken != "" {
		clone.Header.Set("Authorization", "Bearer "+t.bearerToken)
	}
	if clone.Header.Get(plcseams.HeaderRegistryAssertion) == "" && t.registryAssertion != "" {
		clone.Header.Set(plcseams.HeaderRegistryAssertion, t.registryAssertion)
	}
	if p, ok := plcseams.PrincipalFromContext(req.Context()); ok && p.Email != "" {
		hdrs, err := plcseams.CurrentDelegationCredentials().HeadersForUser(req.Context(), p.Email)
		if err == nil {
			for k, vals := range hdrs {
				if clone.Header.Get(k) == "" && len(vals) > 0 {
					clone.Header.Set(k, vals[0])
				}
			}
		}
		if clone.Header.Get(plcseams.HeaderOnBehalfOf) == "" {
			clone.Header.Set(plcseams.HeaderOnBehalfOf, p.Email)
		}
		if clone.Header.Get(plcseams.HeaderSubjectToken) == "" && p.SubjectToken != "" {
			clone.Header.Set(plcseams.HeaderSubjectToken, p.SubjectToken)
		}
	}
	return t.base.RoundTrip(clone)
}

// ValidateEntryKind enforces the v1 registry routing decision: only "instance"
// (hosted VMs over VPC) and "connected" (self-hosted daemons over outbound
// tunnel) entries are routed in v1. Redirect entries are rejected while keeping
// Kind as an explicit field so future kinds can be added cleanly.
func ValidateEntryKind(kind EntryKind) error {
	if kind.Valid() {
		return nil
	}
	return fmt.Errorf("%w: %q (only %q and %q are supported in v1)",
		ErrUnsupportedEntryKind, kind, EntryKindInstance, EntryKindConnected)
}

// ResolveEntryURL determines the HTTP(S) base URL for a registry entry after
// verifying its EntryKind. Both plain HTTP (for VPC backends using
// X-Grain-Registry-Assertion) and HTTPS URLs are supported.
func ResolveEntryURL(entry Entry, registryURL string) (string, error) {
	if err := ValidateEntryKind(entry.Kind); err != nil {
		return "", err
	}
	for _, candidate := range []string{entry.BackendURL, entry.URL} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("grainclient: invalid entry URL %q for %q", candidate, entry.Name)
		}
		return strings.TrimRight(candidate, "/"), nil
	}
	if strings.TrimSpace(registryURL) != "" && entry.Name != "" {
		return strings.TrimRight(registryURL, "/"), nil
	}
	return "", fmt.Errorf("grainclient: entry %q has no reachable URL or backend_url", entry.Name)
}

// Directory returns the caller's visible Grain entries from GET /api/directory,
// using a short TTL cache when DirectoryCacheTTL > 0.
func (c *Client) Directory(ctx context.Context) (*DirectoryResponse, error) {
	return c.DirectoryWithQuery(ctx, DirectoryQuery{})
}

// DirectoryWithQuery fetches GET /api/directory with an optional filter query.
func (c *Client) DirectoryWithQuery(ctx context.Context, q DirectoryQuery) (*DirectoryResponse, error) {
	if c.regClient == nil {
		return nil, fmt.Errorf("grainclient: registry URL is not configured")
	}
	cacheable := q == (DirectoryQuery{})
	_, hasPrincipal := plcseams.PrincipalFromContext(ctx)
	if cacheable && !hasPrincipal && c.cfg.DirectoryCacheTTL > 0 {
		c.mu.RLock()
		if c.cachedDir != nil && time.Now().Before(c.cachedUntil) {
			cp := *c.cachedDir
			c.mu.RUnlock()
			c.perf.RecordCacheHit("registry.directory")
			return &cp, nil
		}
		c.mu.RUnlock()
		c.perf.RecordCacheMiss("registry.directory")
	}

	done := c.perf.Start("registry.directory")
	dir, err := c.regClient.Directory(ctx, q)
	done(err)
	if err != nil {
		return nil, err
	}

	// Filter out any non-v1 entry kinds while keeping kind-based routing explicit.
	filtered := make([]DirectoryEntry, 0, len(dir.Entries))
	for _, e := range dir.Entries {
		if ValidateEntryKind(e.Kind) == nil {
			filtered = append(filtered, e)
		}
	}
	dir.Entries = filtered

	if cacheable && !hasPrincipal && c.cfg.DirectoryCacheTTL > 0 {
		c.mu.Lock()
		cp := *dir
		c.cachedDir = &cp
		c.cachedUntil = time.Now().Add(c.cfg.DirectoryCacheTTL)
		c.mu.Unlock()
	}
	return dir, nil
}

// InvalidateDirectoryCache clears the cached DirectoryResponse.
func (c *Client) InvalidateDirectoryCache() {
	c.mu.Lock()
	if c.cachedDir != nil {
		c.perf.RecordCacheEviction("registry.directory")
	}
	c.cachedDir = nil
	c.cachedUntil = time.Time{}
	c.mu.Unlock()
}

// Route queries GET /api/route for visible Grain entries that cover repo.
func (c *Client) Route(ctx context.Context, repo string) (*RouteResponse, error) {
	return c.RouteWithQuery(ctx, RouteQuery{Repo: repo})
}

// RouteWithQuery queries GET /api/route for repo and path.
func (c *Client) RouteWithQuery(ctx context.Context, q RouteQuery) (*RouteResponse, error) {
	if c.regClient == nil {
		return nil, fmt.Errorf("grainclient: registry URL is not configured")
	}
	done := c.perf.Start("registry.route")
	resp, err := c.regClient.Route(ctx, q)
	done(err)
	if err != nil {
		return nil, err
	}
	filtered := make([]RouteCandidate, 0, len(resp.Candidates))
	for _, cand := range resp.Candidates {
		if ValidateEntryKind(cand.Kind) == nil {
			filtered = append(filtered, cand)
		}
	}
	resp.Candidates = filtered
	if resp.Primary != nil && ValidateEntryKind(resp.Primary.Kind) != nil {
		resp.Primary = nil
	}
	if resp.Primary == nil && len(filtered) > 0 {
		first := filtered[0]
		resp.Primary = &first
	}
	return resp, nil
}

// ListEntries lists registry entries via GET /api/entries.
func (c *Client) ListEntries(ctx context.Context, q ListEntriesQuery) ([]Entry, error) {
	if c.regClient == nil {
		return nil, fmt.Errorf("grainclient: registry URL is not configured")
	}
	done := c.perf.Start("registry.list_entries")
	entries, err := c.regClient.ListEntries(ctx, q)
	done(err)
	return entries, err
}

// GetEntry fetches a single registry entry by name via GET /api/entries/<name>.
func (c *Client) GetEntry(ctx context.Context, name string) (*Entry, error) {
	if c.regClient == nil {
		return nil, fmt.Errorf("grainclient: registry URL is not configured")
	}
	done := c.perf.Start("registry.get_entry")
	entry, err := c.regClient.GetEntry(ctx, name)
	done(err)
	if err != nil {
		return nil, err
	}
	if err := ValidateEntryKind(entry.Kind); err != nil {
		return nil, err
	}
	return entry, nil
}

// CreateEntry creates a new registry entry after validating its EntryKind.
func (c *Client) CreateEntry(ctx context.Context, req CreateEntryRequest) (*CreateEntryResponse, error) {
	if c.regClient == nil {
		return nil, fmt.Errorf("grainclient: registry URL is not configured")
	}
	if err := ValidateEntryKind(req.Kind); err != nil {
		return nil, err
	}
	done := c.perf.Start("registry.create_entry")
	resp, err := c.regClient.CreateEntry(ctx, req)
	done(err)
	c.InvalidateDirectoryCache()
	return resp, err
}

// DeleteEntry deletes a registry entry by name.
func (c *Client) DeleteEntry(ctx context.Context, name string, req DeleteEntryRequest) error {
	if c.regClient == nil {
		return fmt.Errorf("grainclient: registry URL is not configured")
	}
	done := c.perf.Start("registry.delete_entry")
	err := c.regClient.DeleteEntry(ctx, name, req)
	done(err)
	c.InvalidateDirectoryCache()
	return err
}

// ForURL returns an InstanceClient targeting baseURL directly.
func (c *Client) ForURL(baseURL string) *InstanceClient {
	return &InstanceClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		clientName: c.cfg.ClientName,
		httpClient: c.httpClient,
		perf:       c.perf,
	}
}

// ForEntry validates entry.Kind and returns an InstanceClient targeting entry.
func (c *Client) ForEntry(entry Entry) (*InstanceClient, error) {
	targetURL, err := ResolveEntryURL(entry, c.cfg.RegistryURL)
	if err != nil {
		return nil, err
	}
	ic := c.ForURL(targetURL)
	ic.entry = &entry
	return ic, nil
}

// DefaultInstance returns an InstanceClient for Config.GrainURL when set, or
// resolves the caller's first visible instance from the registry Directory.
func (c *Client) DefaultInstance(ctx context.Context) (*InstanceClient, error) {
	if strings.TrimSpace(c.cfg.GrainURL) != "" {
		return c.ForURL(c.cfg.GrainURL), nil
	}
	ic, _, err := c.ResolveInstance(ctx, "", "")
	return ic, err
}

// ResolveInstance picks the target Grain instance by explicit instance name,
// repository route, registry directory catalog, or Config.GrainURL fallback.
func (c *Client) ResolveInstance(ctx context.Context, targetInstance, repo string) (*InstanceClient, *Entry, error) {
	targetInstance = strings.TrimSpace(targetInstance)
	repo = strings.TrimSpace(repo)

	if c.regClient != nil {
		if targetInstance != "" {
			entry, err := c.GetEntry(ctx, targetInstance)
			if err != nil {
				return nil, nil, err
			}
			ic, err := c.ForEntry(*entry)
			return ic, entry, err
		}
		if repo != "" {
			routeResp, err := c.Route(ctx, repo)
			if err != nil {
				return nil, nil, err
			}
			if routeResp.Primary != nil {
				entry := Entry{
					Name:   routeResp.Primary.Name,
					URL:    routeResp.Primary.URL,
					Kind:   routeResp.Primary.Kind,
					Status: routeResp.Primary.Status,
					Owner:  routeResp.Primary.Owner,
				}
				ic, err := c.ForEntry(entry)
				return ic, &entry, err
			}
		}
		dir, err := c.Directory(ctx)
		if err != nil {
			return nil, nil, err
		}
		if len(dir.Entries) > 0 {
			first := dir.Entries[0]
			entry := Entry{
				Name:   first.Name,
				URL:    first.URL,
				Kind:   first.Kind,
				Status: first.Status,
				Owner:  first.Owner,
			}
			ic, err := c.ForEntry(entry)
			return ic, &entry, err
		}
	}

	if strings.TrimSpace(c.cfg.GrainURL) != "" {
		return c.ForURL(c.cfg.GrainURL), nil, nil
	}
	return nil, nil, fmt.Errorf("grainclient: no target Grain instance found for instance=%q repo=%q", targetInstance, repo)
}

// InstanceClient talks to a single Grain daemon's HTTP and SSE APIs.
type InstanceClient struct {
	baseURL    string
	entry      *Entry
	clientName string
	httpClient *http.Client
	perf       *perf.Recorder
}

// BaseURL returns the target Grain daemon's base URL.
func (ic *InstanceClient) BaseURL() string {
	return ic.baseURL
}

// Entry returns the registry Entry this client was resolved from, if any.
func (ic *InstanceClient) Entry() *Entry {
	return ic.entry
}

// CreateTask files a new task on the Grain daemon via POST /api/tasks.
func (ic *InstanceClient) CreateTask(ctx context.Context, req CreateTaskRequest) (*Task, error) {
	done := ic.perf.Start("grain.create_task")
	var out Task
	hdrs := make(http.Header)
	if req.FiledVia != "" {
		hdrs.Set(plcseams.HeaderClient, req.FiledVia)
	}
	err := ic.doJSON(ctx, http.MethodPost, "/api/tasks", hdrs, req, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTask fetches a task by ID via GET /api/tasks/{id}.
func (ic *InstanceClient) GetTask(ctx context.Context, id string) (*Task, error) {
	done := ic.perf.Start("grain.get_task")
	var out Task
	err := ic.doJSON(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, nil, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTasks lists tasks matching filter via GET /api/tasks.
func (ic *InstanceClient) ListTasks(ctx context.Context, filter ListTasksFilter) ([]Task, error) {
	done := ic.perf.Start("grain.list_tasks")
	q := url.Values{}
	if filter.ExternalRef != "" {
		q.Set("externalRef", filter.ExternalRef)
	}
	if filter.State != "" {
		q.Set("state", filter.State)
	}
	if filter.Repo != "" {
		q.Set("repo", filter.Repo)
	}
	path := "/api/tasks"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []Task
	err := ic.doJSON(ctx, http.MethodGet, path, nil, nil, &out)
	done(err)
	return out, err
}

// UpdateTask updates a task via PATCH /api/tasks/{id}.
func (ic *InstanceClient) UpdateTask(ctx context.Context, id string, patch TaskPatch) (*Task, error) {
	done := ic.perf.Start("grain.update_task")
	var out Task
	err := ic.doJSON(ctx, http.MethodPatch, "/api/tasks/"+url.PathEscape(id), nil, patch, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PostComment posts a comment on a task via POST /api/tasks/{id}/comments.
func (ic *InstanceClient) PostComment(ctx context.Context, id, body, author string) (*Comment, error) {
	done := ic.perf.Start("grain.post_comment")
	payload := map[string]string{"body": body}
	if author != "" {
		payload["author"] = author
	}
	var out Comment
	err := ic.doJSON(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/comments", nil, payload, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplyTask answers a parked task's question via POST /api/tasks/{id}/reply.
func (ic *InstanceClient) ReplyTask(ctx context.Context, id, message string) error {
	done := ic.perf.Start("grain.reply_task")
	payload := map[string]string{"message": message}
	err := ic.doJSON(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/reply", nil, payload, nil)
	done(err)
	return err
}

// ApproveTask approves a held task via POST /api/tasks/{id}/approve.
func (ic *InstanceClient) ApproveTask(ctx context.Context, id string) (*Task, error) {
	done := ic.perf.Start("grain.approve_task")
	var out Task
	err := ic.doJSON(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/approve", nil, map[string]any{}, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RefSummary fetches the aggregate status rollup and task list for externalRef
// via GET /api/refs/{ref}/summary.
func (ic *InstanceClient) RefSummary(ctx context.Context, externalRef string) (*RefSummary, error) {
	done := ic.perf.Start("grain.ref_summary")
	var out RefSummary
	path := "/api/refs/" + url.PathEscape(externalRef) + "/summary"
	err := ic.doJSON(ctx, http.MethodGet, path, nil, nil, &out)
	done(err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (ic *InstanceClient) doJSON(ctx context.Context, method, path string, extraHeaders http.Header, reqBody, respBody any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, ic.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vals := range extraHeaders {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	resp, err := ic.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var envelope struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error != nil && envelope.Error.Message != "" {
			return &APIError{
				StatusCode: resp.StatusCode,
				Code:       envelope.Error.Code,
				Message:    envelope.Error.Message,
			}
		}
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(raw)),
		}
	}

	if respBody == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response from %s %s: %w", method, path, err)
	}
	return nil
}
