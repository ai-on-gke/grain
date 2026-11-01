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
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/model"
	"golang.org/x/oauth2/google"
)

const (
	// CloudIdentityGroupsReadonlyScope is the OAuth2 scope used to resolve
	// group: entries in -ui-allowed-principals via the Cloud Identity API.
	CloudIdentityGroupsReadonlyScope = "https://www.googleapis.com/auth/cloud-identity.groups.readonly"

	// DefaultCloudIdentityEndpoint is Google's Cloud Identity REST API base URL.
	DefaultCloudIdentityEndpoint = "https://cloudidentity.googleapis.com"

	// DefaultGroupMembershipCacheTTL is how long Cloud Identity group
	// membership results are cached.
	DefaultGroupMembershipCacheTTL = 5 * time.Minute
)

// GroupMemberChecker resolves whether memberEmail belongs to groupEmail.
type GroupMemberChecker interface {
	IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error)
}

// AllowedPrincipalEntry is one parsed entry of -ui-allowed-principals.
type AllowedPrincipalEntry struct {
	Prefix string // "user", "serviceAccount", "domain", "group"
	Value  string // lowercased email or domain
}

// AllowedPrincipalsUsage describes the -ui-allowed-principals flag.
const AllowedPrincipalsUsage = "comma-separated principals admitted when authenticated by IAP or another registered IdentityVerifier " +
	"(user:<email>, serviceAccount:<email>, group:<email>, domain:<domain>); defaults to $GRAIN_ALLOWED_PRINCIPALS, " +
	"and when unset to the deployment owner plus the service accounts grain provisions"

// SplitAllowedPrincipals splits a comma-separated -ui-allowed-principals list,
// trimming whitespace and dropping empty items.
func SplitAllowedPrincipals(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseAllowedPrincipals validates and normalizes -ui-allowed-principals entries.
// Supported forms are user:<email>, serviceAccount:<email>, group:<email>, and domain:<domain>.
func ParseAllowedPrincipals(entries []string) ([]AllowedPrincipalEntry, error) {
	var out []AllowedPrincipalEntry
	for _, raw := range entries {
		for _, item := range SplitAllowedPrincipals(raw) {
			prefix, val, ok := strings.Cut(item, ":")
			if !ok {
				return nil, fmt.Errorf("invalid allowed principal %q: must start with user:, serviceAccount:, group:, or domain:", item)
			}
			prefix = strings.TrimSpace(prefix)
			val = strings.ToLower(strings.TrimSpace(val))
			if val == "" {
				return nil, fmt.Errorf("invalid allowed principal %q: empty %s value", item, prefix)
			}
			switch prefix {
			case "user", "serviceAccount", "group":
				if !strings.Contains(val, "@") {
					return nil, fmt.Errorf("invalid allowed principal %q: %s entry must be an email address", item, prefix)
				}
			case "domain":
				if strings.Contains(val, "@") || strings.Contains(val, "/") {
					return nil, fmt.Errorf("invalid allowed principal %q: domain entry must be a bare domain such as example.com", item)
				}
			default:
				return nil, fmt.Errorf("invalid allowed principal %q: unknown prefix %q (want user:, serviceAccount:, group:, or domain:)", item, prefix+":")
			}
			out = append(out, AllowedPrincipalEntry{Prefix: prefix, Value: val})
		}
	}
	return out, nil
}

// defaultAllowedPrincipalEntries builds the default allowlist when neither
// Config.AllowedPrincipals nor $GRAIN_ALLOWED_PRINCIPALS is set: the
// deployment owner (Config.Actor) plus the service accounts grain provisions.
func (s *Server) defaultAllowedPrincipalEntries(ctx context.Context) []AllowedPrincipalEntry {
	var out []AllowedPrincipalEntry
	seen := map[string]bool{}
	add := func(prefix, val string) {
		val = normalizeIdentityEmail(val)
		val = strings.TrimPrefix(val, "user:")
		val = strings.TrimPrefix(val, "serviceaccount:")
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		key := prefix + ":" + val
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, AllowedPrincipalEntry{Prefix: prefix, Value: val})
	}
	addPrincipal := func(raw string) {
		clean := normalizeIdentityEmail(raw)
		clean = strings.TrimPrefix(clean, "user:")
		clean = strings.TrimPrefix(clean, "serviceaccount:")
		clean = strings.TrimSpace(clean)
		if clean == "" {
			return
		}
		if principalKindForEmail(clean) == model.PrincipalService {
			add("serviceAccount", clean)
		} else {
			add("user", clean)
		}
	}

	if s.tasks != nil {
		if actorID := strings.TrimSpace(s.tasks.Config.Actor.ID); actorID != "" {
			addPrincipal(actorID)
		}
		for _, sa := range s.tasks.Config.ProvisionedServiceAccounts {
			add("serviceAccount", sa)
		}
		if s.tasks.Store != nil {
			if mc, err := s.tasks.Store.GetConfig(ctx); err == nil && mc != nil {
				for _, sa := range gcpkey.PoolEmails(mc.GCPServiceAccountEmail, mc.GCPServiceAccountCount, mc.GCPProject) {
					add("serviceAccount", sa)
				}
				if proj := strings.TrimSpace(mc.GCPProject); proj != "" {
					add("serviceAccount", "grain-daemon@"+proj+".iam.gserviceaccount.com")
				}
			}
		}
	}
	return out
}

func (s *Server) effectiveAllowedPrincipals(ctx context.Context) ([]AllowedPrincipalEntry, error) {
	var raw []string
	if s.tasks != nil && len(s.tasks.Config.AllowedPrincipals) > 0 {
		raw = s.tasks.Config.AllowedPrincipals
	} else if env := strings.TrimSpace(os.Getenv("GRAIN_ALLOWED_PRINCIPALS")); env != "" {
		raw = SplitAllowedPrincipals(env)
	}
	if len(raw) > 0 {
		return ParseAllowedPrincipals(raw)
	}
	return s.defaultAllowedPrincipalEntries(ctx), nil
}

// checkAllowedPrincipal verifies that id is admitted by -ui-allowed-principals
// (or the default owner + provisioned SAs allowlist).
//
// Direct entries (user:, serviceAccount:, domain:) are checked first. Cloud
// Identity is called only if at least one group: entry is configured and no
// direct entry matched, so deployments without group: entries never touch
// Cloud Identity and a failed group lookup refuses only callers that depend on
// a group: entry to be admitted.
func (s *Server) checkAllowedPrincipal(ctx context.Context, id VerifiedIdentity) error {
	entries, err := s.effectiveAllowedPrincipals(ctx)
	if err != nil {
		return fmt.Errorf("principal %q cannot be authorized: %w", id.Email, err)
	}
	email := normalizeIdentityEmail(id.Email)
	var groups []string
	for _, e := range entries {
		switch e.Prefix {
		case "user":
			if id.Kind == model.PrincipalHuman && email == e.Value {
				return nil
			}
		case "serviceAccount":
			if id.Kind == model.PrincipalService && email == e.Value {
				return nil
			}
		case "domain":
			if id.Kind == model.PrincipalHuman {
				if _, domain, ok := strings.Cut(email, "@"); ok && domain == e.Value {
					return nil
				}
			}
		case "group":
			groups = append(groups, e.Value)
		}
	}
	if len(groups) > 0 {
		checker := s.groupChecker()
		var firstErr error
		for _, grp := range groups {
			ok, err := checker.IsMember(ctx, grp, email)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if ok {
				return nil
			}
		}
		if firstErr != nil {
			return fmt.Errorf("principal %q could not be authorized against group allowlist: %w", id.Email, firstErr)
		}
	}
	return fmt.Errorf("principal %q is not allowed by -ui-allowed-principals", id.Email)
}

func (s *Server) groupChecker() GroupMemberChecker {
	if s.tasks != nil && s.tasks.Config.GroupChecker != nil {
		return s.tasks.Config.GroupChecker
	}
	s.defaultGroupsOnce.Do(func() {
		s.defaultGroups = NewCloudIdentityGroupChecker()
	})
	return s.defaultGroups
}

// CloudIdentityOption configures a CloudIdentityGroupChecker.
type CloudIdentityOption func(*CloudIdentityGroupChecker)

// WithCloudIdentityEndpoint overrides the base URL of the Cloud Identity API
// (for example, an httptest.Server in tests).
func WithCloudIdentityEndpoint(endpoint string) CloudIdentityOption {
	return func(c *CloudIdentityGroupChecker) {
		c.endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	}
}

// WithCloudIdentityHTTPClient overrides the HTTP client used to call the Cloud
// Identity API. When unset, Application Default Credentials with
// CloudIdentityGroupsReadonlyScope are initialized lazily on the first lookup.
func WithCloudIdentityHTTPClient(client *http.Client) CloudIdentityOption {
	return func(c *CloudIdentityGroupChecker) {
		c.client = client
	}
}

// WithCloudIdentityCacheTTL sets how long group membership answers are cached.
func WithCloudIdentityCacheTTL(ttl time.Duration) CloudIdentityOption {
	return func(c *CloudIdentityGroupChecker) {
		c.ttl = ttl
	}
}

// WithCloudIdentityClock overrides the clock used for cache expiry in tests.
func WithCloudIdentityClock(now func() time.Time) CloudIdentityOption {
	return func(c *CloudIdentityGroupChecker) {
		c.now = now
	}
}

type groupCacheEntry struct {
	member  bool
	expires time.Time
}

type groupNameCacheEntry struct {
	name    string
	expires time.Time
}

// CloudIdentityGroupChecker resolves group memberships through the Google
// Cloud Identity v1 REST API and caches results for a few minutes. Its ADC
// HTTP client is created lazily on the first IsMember call, so deployments
// without group: entries never look for Cloud Identity credentials.
type CloudIdentityGroupChecker struct {
	endpoint string
	ttl      time.Duration
	now      func() time.Time

	clientMu sync.Mutex
	client   *http.Client

	cacheMu    sync.RWMutex
	groupNames map[string]groupNameCacheEntry
	members    map[string]groupCacheEntry
}

// NewCloudIdentityGroupChecker returns a GroupMemberChecker backed by the
// Cloud Identity groups API with in-memory caching.
func NewCloudIdentityGroupChecker(opts ...CloudIdentityOption) *CloudIdentityGroupChecker {
	c := &CloudIdentityGroupChecker{
		endpoint:   DefaultCloudIdentityEndpoint,
		ttl:        DefaultGroupMembershipCacheTTL,
		now:        time.Now,
		groupNames: map[string]groupNameCacheEntry{},
		members:    map[string]groupCacheEntry{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.endpoint == "" {
		c.endpoint = DefaultCloudIdentityEndpoint
	}
	if c.ttl <= 0 {
		c.ttl = DefaultGroupMembershipCacheTTL
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

func (c *CloudIdentityGroupChecker) httpClient(ctx context.Context) (*http.Client, error) {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	hc, err := google.DefaultClient(ctx, CloudIdentityGroupsReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("cloud identity credentials: %w", err)
	}
	c.client = hc
	return hc, nil
}

// IsMember reports whether memberEmail has transitive membership in groupEmail,
// caching positive and negative answers for c.ttl. Failed API calls are never
// cached and return an error so the caller fails closed.
func (c *CloudIdentityGroupChecker) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	groupEmail = strings.ToLower(strings.TrimSpace(groupEmail))
	memberEmail = strings.ToLower(strings.TrimSpace(memberEmail))
	if groupEmail == "" || memberEmail == "" {
		return false, errors.New("group and member email are required")
	}

	now := c.now()
	memberKey := groupEmail + "\x00" + memberEmail

	c.cacheMu.RLock()
	if entry, ok := c.members[memberKey]; ok && now.Before(entry.expires) {
		c.cacheMu.RUnlock()
		return entry.member, nil
	}
	c.cacheMu.RUnlock()

	hc, err := c.httpClient(ctx)
	if err != nil {
		return false, err
	}

	groupName, err := c.lookupGroupName(ctx, hc, groupEmail, now)
	if err != nil {
		return false, err
	}

	isMember, err := c.checkTransitiveMembership(ctx, hc, groupName, memberEmail)
	if err != nil {
		return false, err
	}

	c.cacheMu.Lock()
	c.members[memberKey] = groupCacheEntry{
		member:  isMember,
		expires: c.now().Add(c.ttl),
	}
	c.cacheMu.Unlock()

	return isMember, nil
}

func (c *CloudIdentityGroupChecker) lookupGroupName(ctx context.Context, hc *http.Client, groupEmail string, now time.Time) (string, error) {
	c.cacheMu.RLock()
	if entry, ok := c.groupNames[groupEmail]; ok && now.Before(entry.expires) {
		c.cacheMu.RUnlock()
		return entry.name, nil
	}
	c.cacheMu.RUnlock()

	u := c.endpoint + "/v1/groups:lookup?groupKey.id=" + url.QueryEscape(groupEmail)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("looking up Cloud Identity group %q: %w", groupEmail, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("reading Cloud Identity group lookup for %q: %w", groupEmail, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Cloud Identity group lookup for %q returned HTTP %d: %s", groupEmail, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding Cloud Identity group lookup for %q: %w", groupEmail, err)
	}
	name := strings.TrimPrefix(strings.TrimSpace(parsed.Name), "/")
	if !strings.HasPrefix(name, "groups/") || len(name) <= len("groups/") {
		return "", fmt.Errorf("Cloud Identity group lookup for %q returned unexpected resource %q", groupEmail, parsed.Name)
	}

	c.cacheMu.Lock()
	c.groupNames[groupEmail] = groupNameCacheEntry{
		name:    name,
		expires: c.now().Add(c.ttl),
	}
	c.cacheMu.Unlock()
	return name, nil
}

func escapeCELSingleQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

func (c *CloudIdentityGroupChecker) checkTransitiveMembership(ctx context.Context, hc *http.Client, groupName, memberEmail string) (bool, error) {
	query := "member_key_id == '" + escapeCELSingleQuoted(memberEmail) + "'"
	u := c.endpoint + "/v1/" + groupName + "/memberships:checkTransitiveMembership?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("checking Cloud Identity membership in %s: %w", groupName, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, fmt.Errorf("reading Cloud Identity membership in %s: %w", groupName, err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Cloud Identity membership check in %s returned HTTP %d: %s", groupName, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		HasMembership bool `json:"hasMembership"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("decoding Cloud Identity membership in %s: %w", groupName, err)
	}
	return parsed.HasMembership, nil
}
