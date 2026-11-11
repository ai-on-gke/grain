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

package webauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// CloudIdentityGroupsReadonlyScope is the OAuth2 scope used to resolve
	// group: entries via the Google Cloud Identity API.
	CloudIdentityGroupsReadonlyScope = "https://www.googleapis.com/auth/cloud-identity.groups.readonly"

	// DefaultCloudIdentityEndpoint is Google's Cloud Identity REST API base URL.
	DefaultCloudIdentityEndpoint = "https://cloudidentity.googleapis.com"

	// DefaultGroupMembershipCacheTTL is how long Cloud Identity group
	// membership results are cached.
	DefaultGroupMembershipCacheTTL = 5 * time.Minute
)

// defaultCloudIdentityTimeoutNs bounds each call the default Cloud Identity
// group checker makes, including ADC token fetches.
var defaultCloudIdentityTimeoutNs atomic.Int64

func init() {
	defaultCloudIdentityTimeoutNs.Store(int64(10 * time.Second))
}

// SetCloudIdentityTimeout sets the per-call timeout used by CloudIdentityGroupChecker
// instances that do not pin their own timeout, and returns a function that
// restores the previous timeout.
func SetCloudIdentityTimeout(d time.Duration) (restore func()) {
	prev := defaultCloudIdentityTimeoutNs.Swap(int64(d))
	var once sync.Once
	return func() {
		once.Do(func() {
			defaultCloudIdentityTimeoutNs.Store(prev)
		})
	}
}

func currentCloudIdentityTimeout() time.Duration {
	return time.Duration(defaultCloudIdentityTimeoutNs.Load())
}

// GroupMemberChecker resolves whether memberEmail belongs to groupEmail.
type GroupMemberChecker interface {
	IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error)
}

// ScopedGroupMemberChecker is an optional extension implemented by a
// GroupMemberChecker that is authoritative for a subset of group identifiers
// (for example, a corporate group directory namespace). When HandlesGroup
// returns true for groupEmail, NewDefaultGroupMemberChecker consults matching
// scoped checkers without falling back to Cloud Identity.
type ScopedGroupMemberChecker interface {
	GroupMemberChecker
	HandlesGroup(groupEmail string) bool
}

// ErrGroupCheckerSkip may be returned by a registered GroupMemberChecker's
// IsMember method when groupEmail does not belong to that checker's directory,
// signaling NewDefaultGroupMemberChecker to continue to remaining checkers.
var ErrGroupCheckerSkip = errors.New("group not handled by this group checker")

type registeredGroupChecker struct {
	id uint64
	c  GroupMemberChecker
}

var (
	groupCheckerRegistryMu sync.RWMutex
	groupCheckerNextID     atomic.Uint64
	groupCheckerRegistry   []registeredGroupChecker
)

// RegisterGroupMemberChecker registers c to be consulted alongside Cloud
// Identity when resolving group memberships, and returns an idempotent
// function that removes it.
//
// Wrapper editions call this at init time to plug corporate group directories
// into the daemon's allowed_principal group: evaluator and registry group
// checks beside Cloud Identity.
func RegisterGroupMemberChecker(c GroupMemberChecker) (unregister func()) {
	if c == nil {
		panic("webauth: RegisterGroupMemberChecker called with nil GroupMemberChecker")
	}
	id := groupCheckerNextID.Add(1)
	groupCheckerRegistryMu.Lock()
	groupCheckerRegistry = append(groupCheckerRegistry, registeredGroupChecker{id: id, c: c})
	groupCheckerRegistryMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			groupCheckerRegistryMu.Lock()
			defer groupCheckerRegistryMu.Unlock()
			for i, entry := range groupCheckerRegistry {
				if entry.id == id {
					groupCheckerRegistry = append(groupCheckerRegistry[:i], groupCheckerRegistry[i+1:]...)
					break
				}
			}
		})
	}
}

// RegisteredGroupMemberCheckers returns a snapshot of the registered
// GroupMemberCheckers in registration order.
func RegisteredGroupMemberCheckers() []GroupMemberChecker {
	groupCheckerRegistryMu.RLock()
	defer groupCheckerRegistryMu.RUnlock()
	if len(groupCheckerRegistry) == 0 {
		return nil
	}
	out := make([]GroupMemberChecker, len(groupCheckerRegistry))
	for i, entry := range groupCheckerRegistry {
		out[i] = entry.c
	}
	return out
}

type compositeGroupChecker struct {
	fallback GroupMemberChecker
}

// NewDefaultGroupMemberChecker returns a GroupMemberChecker that consults any
// checkers registered via RegisterGroupMemberChecker in registration order
// beside fallback (typically a *CloudIdentityGroupChecker).
//
// Evaluation rules:
//   - If no checkers are registered via RegisterGroupMemberChecker, fallback is
//     called directly.
//   - If one or more registered checkers implement ScopedGroupMemberChecker and
//     report HandlesGroup(groupEmail) == true, only those scoped checkers are
//     consulted (unless all of them return ErrGroupCheckerSkip).
//   - Otherwise, registered checkers are tried in registration order: a
//     positive match (true, nil) returns immediately without calling fallback;
//     if none matches, fallback is consulted so Cloud Identity groups continue
//     to work alongside corporate groups. Any lookup error fails closed unless
//     another checker positively confirms membership.
func NewDefaultGroupMemberChecker(fallback GroupMemberChecker) GroupMemberChecker {
	return &compositeGroupChecker{fallback: fallback}
}

func (cg *compositeGroupChecker) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	groupEmail = strings.ToLower(strings.TrimSpace(groupEmail))
	memberEmail = strings.ToLower(strings.TrimSpace(memberEmail))
	if groupEmail == "" || memberEmail == "" {
		return false, errors.New("group and member email are required")
	}

	registered := RegisteredGroupMemberCheckers()
	if len(registered) == 0 {
		if cg.fallback == nil {
			return false, errors.New("no group member checker configured")
		}
		return cg.fallback.IsMember(ctx, groupEmail, memberEmail)
	}

	var scoped []GroupMemberChecker
	var unscoped []GroupMemberChecker
	for _, c := range registered {
		if sc, ok := c.(ScopedGroupMemberChecker); ok {
			if sc.HandlesGroup(groupEmail) {
				scoped = append(scoped, c)
			}
			continue
		}
		unscoped = append(unscoped, c)
	}

	if len(scoped) > 0 {
		var firstErr error
		evaluated := 0
		for _, c := range scoped {
			ok, err := c.IsMember(ctx, groupEmail, memberEmail)
			if errors.Is(err, ErrGroupCheckerSkip) {
				continue
			}
			evaluated++
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if ok {
				return true, nil
			}
		}
		if evaluated > 0 {
			return false, firstErr
		}
	}

	var firstErr error
	for _, c := range unscoped {
		ok, err := c.IsMember(ctx, groupEmail, memberEmail)
		if errors.Is(err, ErrGroupCheckerSkip) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			return true, nil
		}
	}

	if cg.fallback != nil {
		ok, err := cg.fallback.IsMember(ctx, groupEmail, memberEmail)
		if err == nil && ok {
			return true, nil
		}
		if firstErr != nil {
			return false, firstErr
		}
		return ok, err
	}
	return false, firstErr
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
	timeout  time.Duration
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
	defaultEndpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("GRAIN_CLOUD_IDENTITY_ENDPOINT")), "/")
	if defaultEndpoint == "" {
		defaultEndpoint = DefaultCloudIdentityEndpoint
	}
	c := &CloudIdentityGroupChecker{
		endpoint:   defaultEndpoint,
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
		c.endpoint = defaultEndpoint
	}
	if c.ttl <= 0 {
		c.ttl = DefaultGroupMembershipCacheTTL
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

func (c *CloudIdentityGroupChecker) callTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return currentCloudIdentityTimeout()
}

func (c *CloudIdentityGroupChecker) httpClient() (*http.Client, error) {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	// The token source keeps the context it is made with for its own
	// fetches, so it gets one that outlives this request and carries a
	// client that gives up on a silent server, not the first caller's.
	// The bound is on the transport rather than http.Client.Timeout,
	// which oauth2.NewClient would copy onto the outer client, whose
	// oauth2.Transport logs a deprecation on every timeout; IsMember's
	// context bounds the Cloud Identity calls themselves.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = c.callTimeout()
	base := &http.Client{Transport: tr}
	hc, err := google.DefaultClient(context.WithValue(context.Background(), oauth2.HTTPClient, base), CloudIdentityGroupsReadonlyScope)
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

	if err := checkCloudIdentityEndpoint(c.endpoint); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout())
	defer cancel()
	hc, err := c.httpClient()
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

// checkCloudIdentityEndpoint refuses an endpoint the checker's
// Application Default Credentials bearer token must not be sent to:
// anything but https, except plain http to a loopback host (the test
// stand-in -ui-cloud-identity-endpoint and $GRAIN_CLOUD_IDENTITY_ENDPOINT
// exist for). A membership check against such an endpoint fails closed
// before any credential is fetched or sent.
func checkCloudIdentityEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("cloud identity endpoint %q is not an absolute URL", endpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("cloud identity endpoint %s is not https: refusing to send credentials to it (plain http is accepted only for a loopback test stand-in)", u.Redacted())
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
