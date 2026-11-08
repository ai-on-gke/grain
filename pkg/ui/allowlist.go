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
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

const (
	// CloudIdentityGroupsReadonlyScope is the OAuth2 scope used to resolve
	// group: entries in -ui-allowed-principals via the Cloud Identity API.
	CloudIdentityGroupsReadonlyScope = webauth.CloudIdentityGroupsReadonlyScope

	// DefaultCloudIdentityEndpoint is Google's Cloud Identity REST API base URL.
	DefaultCloudIdentityEndpoint = webauth.DefaultCloudIdentityEndpoint

	// DefaultGroupMembershipCacheTTL is how long Cloud Identity group
	// membership results are cached.
	DefaultGroupMembershipCacheTTL = webauth.DefaultGroupMembershipCacheTTL
)

// GroupMemberChecker resolves whether memberEmail belongs to groupEmail.
type GroupMemberChecker = webauth.GroupMemberChecker

// ScopedGroupMemberChecker is an optional extension implemented by a
// GroupMemberChecker that is authoritative for a subset of group identifiers.
type ScopedGroupMemberChecker = webauth.ScopedGroupMemberChecker

// ErrGroupCheckerSkip may be returned by a registered GroupMemberChecker to
// defer groupEmail to remaining checkers.
var ErrGroupCheckerSkip = webauth.ErrGroupCheckerSkip

// RegisterGroupMemberChecker registers c to be consulted alongside Cloud
// Identity when evaluating allowed_principal group: entries, and returns an
// idempotent function that removes it.
func RegisterGroupMemberChecker(c GroupMemberChecker) (unregister func()) {
	return webauth.RegisterGroupMemberChecker(c)
}

// AllowedPrincipalEntry is one parsed entry of -ui-allowed-principals.
type AllowedPrincipalEntry struct {
	Prefix string // "user", "serviceAccount", "delegate", "domain", "group"
	Value  string // lowercased email or domain
	// Role is the entry's role on the database allowlist
	// (model.AllowedPrincipal.Role); "" is model.DefaultAccessRole.
	Role string
	// Label is an optional short label for a delegate: entry.
	Label string
}

// AllowedPrincipalsUsage describes the deprecated -ui-allowed-principals flag.
const AllowedPrincipalsUsage = "deprecated: imported once on first start when the database allowlist is empty " +
	"(user:<email> and group:<email> only; serviceAccount: and domain: entries are logged and dropped), then ignored. " +
	"Manage allowed users and groups in Settings -> Access or with 'grain access'"

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

// GCPKeyPoolEmails returns the normalized service-account emails belonging to
// a gcp-key agent account pool: both the configured base address and every
// numbered pool account gcpkey.PoolEmails derives from it. Keys for these
// accounts are materialized into task sandboxes, so they must never be admitted
// as verified UI/API principals.
func GCPKeyPoolEmails(email string, count int, projectID string) []string {
	email = strings.TrimSpace(email)
	projectID = strings.TrimSpace(projectID)
	if email == "" {
		return nil
	}
	qualify := func(addr string) string {
		addr = normalizeIdentityEmail(addr)
		addr = strings.TrimPrefix(addr, "serviceaccount:")
		addr = strings.TrimSpace(addr)
		if addr == "" {
			return ""
		}
		if !strings.Contains(addr, "@") && projectID != "" {
			addr = addr + "@" + strings.ToLower(projectID) + ".iam.gserviceaccount.com"
		}
		return addr
	}
	var out []string
	seen := map[string]bool{}
	add := func(addr string) {
		q := qualify(addr)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		out = append(out, q)
	}
	add(email)
	for _, sa := range gcpkey.PoolEmails(email, count, projectID) {
		add(sa)
	}
	return out
}

// RejectGCPKeyPoolAllowedPrincipals refuses any explicit serviceAccount: entry
// in entries that names one of gcpPoolEmails.
func RejectGCPKeyPoolAllowedPrincipals(entries []AllowedPrincipalEntry, gcpPoolEmails []string) error {
	if len(entries) == 0 || len(gcpPoolEmails) == 0 {
		return nil
	}
	pool := make(map[string]bool, len(gcpPoolEmails))
	for _, sa := range gcpPoolEmails {
		norm := normalizeIdentityEmail(sa)
		norm = strings.TrimPrefix(norm, "serviceaccount:")
		norm = strings.TrimSpace(norm)
		if norm != "" {
			pool[norm] = true
		}
	}
	for _, e := range entries {
		if e.Prefix == "serviceAccount" && pool[e.Value] {
			return fmt.Errorf(
				"invalid allowed principal %q: %s is a gcp-key agent service account whose key is materialized into task sandboxes and cannot be admitted as an operator",
				e.Prefix+":"+e.Value, e.Value,
			)
		}
	}
	return nil
}

// ValidateAllowedPrincipals parses entries with ParseAllowedPrincipals and
// refuses any serviceAccount: entry naming an account in gcpPoolEmails.
func ValidateAllowedPrincipals(entries []string, gcpPoolEmails []string) ([]AllowedPrincipalEntry, error) {
	parsed, err := ParseAllowedPrincipals(entries)
	if err != nil {
		return nil, err
	}
	if err := RejectGCPKeyPoolAllowedPrincipals(parsed, gcpPoolEmails); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ConfigGCPKeyServiceAccounts returns every service-account email whose key
// the deployment's stored gcp-key / gcp#gcp-key configuration can materialize
// into a task sandbox: the legacy grain_config pool, stored gcp_key rows, and
// https://grain.dev/capabilities/gcp permission sets (both explicit accounts
// and dynamically created or adopted pool accounts).
func ConfigGCPKeyServiceAccounts(mc *model.Config, keys []model.GCPKey) []string {
	var out []string
	seen := map[string]bool{}
	add := func(addr string) {
		norm := normalizeIdentityEmail(addr)
		norm = strings.TrimPrefix(norm, "serviceaccount:")
		norm = strings.TrimSpace(norm)
		if norm == "" || seen[norm] {
			return
		}
		seen[norm] = true
		out = append(out, norm)
	}
	addPool := func(email string, count int, proj string) {
		for _, sa := range GCPKeyPoolEmails(email, count, proj) {
			add(sa)
		}
	}
	for _, k := range keys {
		addPool(k.ServiceAccountEmail, k.ServiceAccountCount, k.ProjectID)
	}
	if mc == nil {
		return out
	}
	addPool(mc.GCPServiceAccountEmail, mc.GCPServiceAccountCount, mc.GCPProject)
	srv, ok := mc.MCPServers[model.GCPCapabilityServerID]
	if !ok || srv.Config == nil {
		return out
	}
	if rawSets, ok := srv.Config["permission_sets"].([]any); ok {
		prefix, _ := srv.Config["account_prefix"].(string)
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" {
			prefix = gcpsets.DefaultAccountPrefix
		}
		deployment, _ := srv.Config[protocol.DeploymentIDConfig].(string)
		deployment = strings.ToLower(strings.TrimSpace(deployment))
		if deployment == "" {
			deployment = strings.ToLower(strings.TrimSpace(mc.DeploymentID))
		}
		for _, item := range rawSets {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			setName, _ := m["name"].(string)
			setName = strings.TrimSpace(setName)
			proj, _ := m["project"].(string)
			proj = strings.ToLower(strings.TrimSpace(proj))
			var explicit []string
			switch accts := m["accounts"].(type) {
			case []any:
				for _, v := range accts {
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
						explicit = append(explicit, s)
					}
				}
			case []string:
				for _, s := range accts {
					if strings.TrimSpace(s) != "" {
						explicit = append(explicit, s)
					}
				}
			}
			if sa, _ := m["service_account"].(string); strings.TrimSpace(sa) != "" {
				// A set over an existing service account acts as it alone.
				explicit = append(explicit, strings.TrimSpace(sa))
			}
			if len(explicit) > 0 {
				for _, sa := range explicit {
					add(sa)
				}
				continue
			}
			if setName == "" || proj == "" {
				continue
			}
			poolMax := gcpsets.DefaultPoolMax
			switch v := m["pool_max"].(type) {
			case float64:
				if int(v) > 0 {
					poolMax = int(v)
				}
			case int:
				if v > 0 {
					poolMax = v
				}
			}
			if poolMax > 99 {
				poolMax = 99
			}
			adopt, _ := m["adopt_legacy"].(bool)
			// Include up to maxNameTries (20) past pool_max so skipped collision numbers are covered too.
			for n := 1; n <= poolMax+20; n++ {
				if deployment != "" {
					add(gcpsets.Email(gcpsets.AccountID(prefix, deployment, proj, setName, n), proj))
				}
				if adopt {
					add(gcpsets.Email(gcpsets.LegacyAccountID(setName, n), proj))
				}
			}
		}
	} else if rawAccounts, ok := srv.Config["accounts"].([]any); ok {
		proj, _ := srv.Config["project"].(string)
		for _, item := range rawAccounts {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			email, _ := m["email"].(string)
			count := 1
			switch v := m["count"].(type) {
			case float64:
				count = int(v)
			case int:
				count = v
			}
			addPool(email, count, proj)
		}
	}
	return out
}

func (c *Client) gcpKeyPoolEmailList(ctx context.Context) []string {
	if c == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	addAll := func(emails []string) {
		for _, sa := range emails {
			norm := normalizeIdentityEmail(sa)
			norm = strings.TrimPrefix(norm, "serviceaccount:")
			norm = strings.TrimSpace(norm)
			if norm == "" || seen[norm] {
				continue
			}
			seen[norm] = true
			out = append(out, norm)
		}
	}
	addAll(c.Config.GCPKeyServiceAccounts)
	if c.Store != nil {
		mc, _ := c.Store.GetConfig(ctx)
		keys, _ := c.Store.ListGCPKeys(ctx)
		addAll(ConfigGCPKeyServiceAccounts(mc, keys))
	}
	return out
}

func (c *Client) gcpKeyPoolSet(ctx context.Context) map[string]bool {
	list := c.gcpKeyPoolEmailList(ctx)
	if len(list) == 0 {
		return nil
	}
	set := make(map[string]bool, len(list))
	for _, sa := range list {
		set[sa] = true
	}
	return set
}

// provisionedServiceAccountEntries returns daemon-owned GCP service accounts
// grain itself provisioned (excluding the gcp-key agent pool whose keys enter
// task sandboxes).
func (s *Server) provisionedServiceAccountEntries(ctx context.Context) []AllowedPrincipalEntry {
	if s.tasks == nil {
		return nil
	}
	var out []AllowedPrincipalEntry
	seen := map[string]bool{}
	pool := s.tasks.gcpKeyPoolSet(ctx)
	add := func(val string) {
		val = normalizeIdentityEmail(val)
		val = strings.TrimPrefix(val, "serviceaccount:")
		val = strings.TrimSpace(val)
		if val == "" || pool[val] {
			return
		}
		key := "serviceAccount:" + val
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, AllowedPrincipalEntry{Prefix: "serviceAccount", Value: val})
	}
	for _, sa := range s.tasks.Config.ProvisionedServiceAccounts {
		add(sa)
	}
	if s.tasks.Store != nil {
		if mc, err := s.tasks.Store.GetConfig(ctx); err == nil && mc != nil {
			if proj := strings.TrimSpace(mc.GCPProject); proj != "" {
				add("grain-daemon@" + proj + ".iam.gserviceaccount.com")
			}
		}
	}
	return out
}

func (s *Server) effectiveAllowedPrincipals(ctx context.Context) ([]AllowedPrincipalEntry, error) {
	if s.tasks == nil || s.tasks.Store == nil {
		return nil, nil
	}
	if err := s.tasks.MigrateAllowedPrincipals(ctx); err != nil {
		return nil, err
	}
	rows, err := s.tasks.Store.ListAllowedPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AllowedPrincipalEntry, 0, len(rows)+3)
	owner := s.tasks.explicitDeploymentOwnerEmail()
	if owner != "" {
		out = append(out, AllowedPrincipalEntry{Prefix: "user", Value: owner, Role: model.DefaultAccessRole})
	}
	pool := s.tasks.gcpKeyPoolSet(ctx)
	for _, r := range rows {
		prefix, val, _, nErr := model.NormalizeAllowedPrincipalEntry(r.Entry)
		if nErr != nil || ((prefix == "serviceAccount" || prefix == "delegate") && pool[val]) {
			continue
		}
		if owner != "" && prefix == "user" && val == owner && (r.Role == "" || r.Role == model.DefaultAccessRole) {
			continue
		}
		if prefix == "delegate" {
			model.RegisterDelegateLabel(val, r.Label)
		}
		out = append(out, AllowedPrincipalEntry{Prefix: prefix, Value: val, Role: r.Role, Label: r.Label})
	}
	out = append(out, s.provisionedServiceAccountEntries(ctx)...)
	return out, nil
}

// checkAllowedPrincipal verifies that id is admitted by the database-backed
// allowed users and groups list (Settings -> Access / `grain access`), or is a
// daemon-provisioned non-pool service account.
//
// Direct entries (user:, serviceAccount:) are checked first. Cloud Identity is
// called only for human principals (PrincipalHuman) when at least one group:
// entry is configured and no direct entry matched, so service accounts can
// never gain operator access through group membership, deployments without
// group: entries never touch Cloud Identity, and a failed group lookup refuses
// only callers that depend on a group: entry to be admitted.
//
// A refusal is logged (logRefusedPrincipal), so an operator can see who was
// turned away -- above all a gcp-key pool account, whose key reaching the
// UI through the proxy means one handed to a sandbox was used outside it.
func (s *Server) checkAllowedPrincipal(ctx context.Context, id VerifiedIdentity) error {
	err := s.allowedPrincipalErr(ctx, id)
	if err != nil {
		logRefusedPrincipal(id, err)
	}
	return err
}

// errGCPKeyPoolPrincipal marks allowedPrincipalErr's refusal of a gcp-key
// pool account, for logRefusedPrincipal.
var errGCPKeyPoolPrincipal = errors.New("gcp-key agent service account")

func (s *Server) allowedPrincipalErr(ctx context.Context, id VerifiedIdentity) error {
	_, err := s.allowedPrincipalRoles(ctx, id, false)
	return err
}

// allowedPrincipalRoles admits id as allowedPrincipalErr describes and, with
// all, returns the role of every entry it matches (docs/design/access-rbac.md
// §2). Without all it stops at the first match and returns that one's role.
// With all, a group whose role could change nothing -- no deny, when what
// already matched holds every scope -- is not looked up, and a failed group
// lookup leaves that group's role out rather than refusing a caller admitted
// some other way, unless that role denies scopes: leaving a veto out would
// widen the caller, so that refuses them instead.
func (s *Server) allowedPrincipalRoles(ctx context.Context, id VerifiedIdentity, all bool) ([]string, error) {
	email := normalizeIdentityEmail(id.Email)
	if id.Kind == model.PrincipalService && s.tasks != nil && s.tasks.gcpKeyPoolSet(ctx)[email] {
		return nil, fmt.Errorf(
			"principal %q is a %w whose key is materialized into task sandboxes and cannot be admitted",
			id.Email, errGCPKeyPoolPrincipal,
		)
	}
	entries, err := s.effectiveAllowedPrincipals(ctx)
	if err != nil {
		return nil, fmt.Errorf("principal %q cannot be authorized: %w", id.Email, err)
	}
	var roles []string
	var groups []AllowedPrincipalEntry
	for _, e := range entries {
		role := e.Role
		if role == "" {
			role = model.DefaultAccessRole
		}
		switch e.Prefix {
		case "user":
			if id.Kind == model.PrincipalHuman && email == e.Value {
				roles = append(roles, role)
			}
		case "serviceAccount":
			if id.Kind == model.PrincipalService && email == e.Value {
				roles = append(roles, role)
			}
		case "group":
			groups = append(groups, e)
		}
		if len(roles) > 0 && !all {
			return roles, nil
		}
	}
	if len(groups) > 0 && id.Kind == model.PrincipalHuman {
		checker := s.groupChecker()
		var firstErr error
		for _, grp := range groups {
			role := grp.Role
			if role == "" {
				role = model.DefaultAccessRole
			}
			if slices.Contains(roles, role) ||
				(len(roles) > 0 && len(roleDenies(role)) == 0 && resolveAccessRoles(email, roles).Full) {
				continue
			}
			ok, err := checker.IsMember(ctx, grp.Value, email)
			if err != nil {
				if all && len(roleDenies(role)) > 0 {
					// The group's role may veto scopes another entry
					// grants (access-rbac.md §2): leaving it out would
					// widen the caller, so refuse rather than guess.
					return nil, &groupAllowlistRefusalError{
						public: fmt.Sprintf("principal %q could not be checked against group %s, whose role %s denies scopes: group membership lookup unavailable",
							id.Email, grp.Value, role),
						cause: err,
					}
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if ok {
				roles = append(roles, role)
				if !all {
					return roles, nil
				}
			}
		}
		if len(roles) == 0 && firstErr != nil {
			return nil, &groupAllowlistRefusalError{
				public: fmt.Sprintf("principal %q could not be authorized against group allowlist: group membership lookup unavailable", id.Email),
				cause:  firstErr,
			}
		}
	}
	if len(roles) > 0 {
		return roles, nil
	}
	return nil, fmt.Errorf("principal %q is not on the allowed users and groups list (Settings -> Access)", id.Email)
}

// refusedPrincipalsLogged is which principals have been logged as refused
// already, and why, so a person retrying (or a browser polling) is logged
// once rather than on every request. Bounded like refusedAssertionsLogged.
var (
	refusedPrincipalsMu     sync.Mutex
	refusedPrincipalsLogged = map[string]bool{}
)

// groupAllowlistRefusalError separates the generic message returned to remote
// HTTP/UI callers from the underlying Cloud Identity / ADC credential error
// recorded in the daemon log.
type groupAllowlistRefusalError struct {
	public string
	cause  error
}

func (e *groupAllowlistRefusalError) Error() string {
	return e.public
}

// logRefusedPrincipal logs, once per principal and kind of refusal, a
// verified identity checkAllowedPrincipal turned away.
func logRefusedPrincipal(id VerifiedIdentity, err error) {
	pool := errors.Is(err, errGCPKeyPoolPrincipal)
	var groupErr *groupAllowlistRefusalError
	hasGroupErr := errors.As(err, &groupErr)
	key := normalizeIdentityEmail(id.Email) + "\x00" + fmt.Sprint(pool) + "\x00" + fmt.Sprint(hasGroupErr)
	refusedPrincipalsMu.Lock()
	seen := refusedPrincipalsLogged[key]
	if !seen && len(refusedPrincipalsLogged) < 64 {
		refusedPrincipalsLogged[key] = true
	}
	refusedPrincipalsMu.Unlock()
	if seen {
		return
	}
	if pool {
		log.Printf("ui: refused verified principal %q: it is a gcp-key agent service account, whose key is handed to task sandboxes -- "+
			"a sandbox's key was presented through the identity proxy; take it off the proxy's access list (iap_members) and rotate the pool's keys", id.Email)
		return
	}
	if hasGroupErr && groupErr.cause != nil {
		log.Printf("ui: refused verified principal %q (%s): %s: %v", id.Email, id.Kind, groupErr.public, groupErr.cause)
		return
	}
	log.Printf("ui: refused verified principal %q (%s): %v", id.Email, id.Kind, err)
}

type delegateNotAllowedError struct {
	personEmail string
	appEmail    string
	label       string
	verifier    string
}

func (e *delegateNotAllowedError) Error() string {
	return fmt.Sprintf("delegating app %q is not allowed to act for people on this grain (Settings -> Access)", e.appEmail)
}

func delegateDisplayLabel(via *DelegatingApp) string {
	if via == nil {
		return ""
	}
	if name := strings.TrimSpace(via.Name); name != "" {
		return name
	}
	return model.DelegateLabelForEmail(via.Email)
}

// logRefusedDelegate logs, at most once per (person, delegating app) under
// the AU-19 bound, a delegated request whose delegating app is not on the
// delegate: allowlist (AU-24.4).
func logRefusedDelegate(personEmail, appEmail string) {
	key := "delegate\x00" + normalizeIdentityEmail(personEmail) + "\x00" + normalizeIdentityEmail(appEmail)
	refusedPrincipalsMu.Lock()
	seen := refusedPrincipalsLogged[key]
	if !seen && len(refusedPrincipalsLogged) < 64 {
		refusedPrincipalsLogged[key] = true
	}
	refusedPrincipalsMu.Unlock()
	if seen {
		return
	}
	log.Printf("ui: refused delegated request for %q via %q: delegating app is not on the delegate: allowlist (Settings -> Access)", personEmail, appEmail)
}

// delegateEntryFor looks up a delegate:<email> entry on the allowlist (AU-24.1).
func (s *Server) delegateEntryFor(ctx context.Context, appEmail string) (AllowedPrincipalEntry, bool, error) {
	email := normalizeIdentityEmail(appEmail)
	if email == "" {
		return AllowedPrincipalEntry{}, false, nil
	}
	if s.tasks != nil && s.tasks.gcpKeyPoolSet(ctx)[email] {
		return AllowedPrincipalEntry{}, false, nil
	}
	entries, err := s.effectiveAllowedPrincipals(ctx)
	if err != nil {
		return AllowedPrincipalEntry{}, false, err
	}
	for _, e := range entries {
		if e.Prefix == "delegate" && e.Value == email {
			if e.Role == "" {
				e.Role = model.DefaultDelegateAccessRole
			}
			return e, true, nil
		}
	}
	return AllowedPrincipalEntry{}, false, nil
}

var (
	delegateLastUsedMu sync.Mutex
	delegateLastUsedAt = map[string]time.Time{}
)

func (s *Server) touchDelegateLastUsed(ctx context.Context, canonicalEntry string) {
	if s.tasks == nil || s.tasks.Store == nil {
		return
	}
	now := s.tasks.now()
	delegateLastUsedMu.Lock()
	last := delegateLastUsedAt[canonicalEntry]
	if !last.IsZero() && now.Sub(last) < time.Minute && !now.Before(last) {
		delegateLastUsedMu.Unlock()
		return
	}
	delegateLastUsedAt[canonicalEntry] = now
	delegateLastUsedMu.Unlock()
	_ = s.tasks.Store.TouchAllowedPrincipalLastUsed(ctx, canonicalEntry, now)
}

func (s *Server) groupChecker() GroupMemberChecker {
	if s.tasks != nil && s.tasks.Config.GroupChecker != nil {
		return s.tasks.Config.GroupChecker
	}
	s.defaultGroupsOnce.Do(func() {
		s.defaultGroups = webauth.NewDefaultGroupMemberChecker(NewCloudIdentityGroupChecker())
	})
	return s.defaultGroups
}

// CloudIdentityOption configures a CloudIdentityGroupChecker.
type CloudIdentityOption = webauth.CloudIdentityOption

// CloudIdentityGroupChecker resolves group memberships through the Google
// Cloud Identity v1 REST API with in-memory caching.
type CloudIdentityGroupChecker = webauth.CloudIdentityGroupChecker

// NewCloudIdentityGroupChecker returns a GroupMemberChecker backed by the
// Cloud Identity groups API with in-memory caching.
func NewCloudIdentityGroupChecker(opts ...CloudIdentityOption) *CloudIdentityGroupChecker {
	return webauth.NewCloudIdentityGroupChecker(opts...)
}

// WithCloudIdentityEndpoint overrides the base URL of the Cloud Identity API
// (for example, an httptest.Server in tests).
func WithCloudIdentityEndpoint(endpoint string) CloudIdentityOption {
	return webauth.WithCloudIdentityEndpoint(endpoint)
}

// WithCloudIdentityHTTPClient overrides the HTTP client used to call the Cloud
// Identity API.
func WithCloudIdentityHTTPClient(client *http.Client) CloudIdentityOption {
	return webauth.WithCloudIdentityHTTPClient(client)
}

// WithCloudIdentityCacheTTL sets how long group membership answers are cached.
func WithCloudIdentityCacheTTL(ttl time.Duration) CloudIdentityOption {
	return webauth.WithCloudIdentityCacheTTL(ttl)
}

// WithCloudIdentityClock overrides the clock used for cache expiry in tests.
func WithCloudIdentityClock(now func() time.Time) CloudIdentityOption {
	return webauth.WithCloudIdentityClock(now)
}
