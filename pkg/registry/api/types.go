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

// Package api defines the wire types and HTTP client shared between
// cmd/grain-registry and grain's daemon and CLI (design/design/registry.design.md).
//
// Packages outside cmd/grain-registry and pkg/registry/... may import only
// pkg/registry/api, and this package imports neither sibling pkg/registry/...
// packages nor grain core packages (enforced by pkg/registry/independence_test.go).
package api

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// DefaultHeartbeatInterval is the nominal cadence at which a connected grain
// sends heartbeats when the registry has not specified a custom interval.
const DefaultHeartbeatInterval = 15 * time.Second

// RegistrationTokenPrefix is the distinguishing prefix on per-entry connected
// registration credentials ("grc_<base64url>").
const RegistrationTokenPrefix = "grc_"

// EntryKind classifies a registry entry (design/design/registry.design.md).
type EntryKind string

const (
	KindInstance  EntryKind = "instance"
	KindConnected EntryKind = "connected"
	KindRedirect  EntryKind = "redirect"
)

// HostKind identifies where an entry's grain process runs.
type HostKind string

const (
	HostKindRegistryVM  HostKind = "registry-vm"
	HostKindWorkstation HostKind = "workstation"
	HostKindKubernetes  HostKind = "kubernetes"
	HostKindOther       HostKind = "other"
)

// ConnectedHostKinds lists the valid host_kind values for connected entries.
func ConnectedHostKinds() []HostKind {
	return []HostKind{HostKindWorkstation, HostKindKubernetes, HostKindOther}
}

// ValidateConnectedHostKind checks that hk is valid for a connected entry.
// Empty defaults to HostKindOther via NormalizeConnectedHostKind.
func ValidateConnectedHostKind(hk HostKind) error {
	switch hk {
	case "", HostKindWorkstation, HostKindKubernetes, HostKindOther:
		return nil
	case HostKindRegistryVM:
		return fmt.Errorf("host_kind %q is reserved for registry-managed VM instances", hk)
	default:
		return fmt.Errorf("unknown host_kind %q (must be workstation, kubernetes, or other)", hk)
	}
}

// NormalizeConnectedHostKind trims hk and defaults empty to HostKindOther.
func NormalizeConnectedHostKind(hk HostKind) (HostKind, error) {
	trimmed := HostKind(strings.ToLower(strings.TrimSpace(string(hk))))
	if err := ValidateConnectedHostKind(trimmed); err != nil {
		return "", err
	}
	if trimmed == "" {
		return HostKindOther, nil
	}
	return trimmed, nil
}

// EntryState is an entry's lifecycle state in the registry store.
type EntryState string

const (
	StateCreating  EntryState = "creating"
	StateRunning   EntryState = "running"
	StateReplacing EntryState = "replacing"
	StateStopping  EntryState = "stopping"
	StateStopped   EntryState = "stopped"
	StateDeleting  EntryState = "deleting"
	StateDeleted   EntryState = "deleted"
	StateFailed    EntryState = "failed"
)

// EntryStatus is an entry's derived runtime liveness status.
type EntryStatus string

const (
	StatusOnline   EntryStatus = "online"
	StatusDegraded EntryStatus = "degraded"
	StatusOffline  EntryStatus = "offline"
	StatusUnknown  EntryStatus = "unknown"
)

// HealthState is the self-reported health of a grain controller.
type HealthState string

const (
	HealthOK        HealthState = "ok"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

// ValidateHealthState verifies h is one of ok, degraded, or unhealthy.
func ValidateHealthState(h HealthState) error {
	switch h {
	case HealthOK, HealthDegraded, HealthUnhealthy:
		return nil
	default:
		return fmt.Errorf("unknown health state %q (must be ok, degraded, or unhealthy)", h)
	}
}

// ConnectMode describes how browser visits reach a connected entry.
// ConnectModeTunnel is reserved as the seam for the outbound proxy tunnel design.
type ConnectMode string

const (
	ConnectModeNone     ConnectMode = "none"
	ConnectModeRedirect ConnectMode = "redirect"
	ConnectModeTunnel   ConnectMode = "tunnel"
)

// ScopeRole declares whether a grain holds primary standing-work ownership or
// secondary (manual/delegated) capacity for a repository and path prefix.
type ScopeRole string

const (
	ScopeRolePrimary   ScopeRole = "primary"
	ScopeRoleSecondary ScopeRole = "secondary"
)

// ValidateScopeRole checks that r is primary or secondary.
func ValidateScopeRole(r ScopeRole) error {
	switch r {
	case ScopeRolePrimary, ScopeRoleSecondary:
		return nil
	default:
		return fmt.Errorf("unknown scope role %q (must be primary or secondary)", r)
	}
}

// ScopeSource distinguishes instance-reported scopes from registry-declared overrides.
type ScopeSource string

const (
	ScopeSourceReported ScopeSource = "reported"
	ScopeSourceDeclared ScopeSource = "declared"
)

// Scope represents a (repo, path_prefix, role) claim on a registry entry.
type Scope struct {
	Repo           string      `json:"repo"`
	PathPrefix     string      `json:"path_prefix"`
	Role           ScopeRole   `json:"role"`
	EffectiveRole  ScopeRole   `json:"effective_role,omitempty"`
	Source         ScopeSource `json:"source,omitempty"`
	Active         *bool       `json:"active,omitempty"`
	ConflictWith   string      `json:"conflict_with,omitempty"`
	InactiveReason string      `json:"inactive_reason,omitempty"`
}

// ScopeConflict describes a primary-scope collision with an existing listed entry.
type ScopeConflict struct {
	Repo         string `json:"repo"`
	PathPrefix   string `json:"path_prefix"`
	ConflictWith string `json:"conflict_with"`
	Owner        string `json:"owner,omitempty"`
	OwnerGroup   string `json:"owner_group,omitempty"`
}

// CredentialInfo summarizes a connected entry's hashed registration token
// without exposing secret material.
type CredentialInfo struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Entry is the registry's representation of an instance, connected, or redirect entry.
type Entry struct {
	Name                string           `json:"name"`
	Kind                EntryKind        `json:"kind"`
	State               EntryState       `json:"state,omitempty"`
	Status              EntryStatus      `json:"status,omitempty"`
	Owner               string           `json:"owner,omitempty"`
	OwnerGroup          string           `json:"owner_group,omitempty"`
	DisplayName         string           `json:"display_name,omitempty"`
	Purpose             string           `json:"purpose,omitempty"`
	HostKind            HostKind         `json:"host_kind,omitempty"`
	Listed              bool             `json:"listed"`
	Tags                []string         `json:"tags,omitempty"`
	URL                 string           `json:"url,omitempty"`
	ExternalURL         string           `json:"external_url,omitempty"`
	TargetURL           string           `json:"target_url,omitempty"`
	AppendPath          *bool            `json:"append_path,omitempty"`
	ConnectedPrincipal  string           `json:"connected_principal,omitempty"`
	ConnectMode         ConnectMode      `json:"connect_mode,omitempty"`
	Shape               string           `json:"shape,omitempty"`
	GrainVersion        string           `json:"grain_version,omitempty"`
	GrainCommit         string           `json:"grain_commit,omitempty"`
	Health              HealthState      `json:"health,omitempty"`
	HealthDetail        string           `json:"health_detail,omitempty"`
	ActiveTasks         *int             `json:"active_tasks,omitempty"`
	AwaitingReplyTasks  *int             `json:"awaiting_reply_tasks,omitempty"`
	LastHeartbeatAt     *time.Time       `json:"last_heartbeat_at,omitempty"`
	HeartbeatAgeSeconds *int64           `json:"heartbeat_age_seconds,omitempty"`
	HasReportedScopes   bool             `json:"has_reported_scopes,omitempty"`
	Scopes              []Scope          `json:"scopes,omitempty"`
	ScopeConflicts      []ScopeConflict  `json:"scope_conflicts,omitempty"`
	Credentials         []CredentialInfo `json:"credentials,omitempty"`
	CreatedAt           *time.Time       `json:"created_at,omitempty"`
	UpdatedAt           *time.Time       `json:"updated_at,omitempty"`
}

// CreateEntryRequest is the body of POST /api/entries.
type CreateEntryRequest struct {
	Name               string      `json:"name,omitempty"`
	Kind               EntryKind   `json:"kind"`
	Shape              string      `json:"shape,omitempty"`
	HostKind           HostKind    `json:"host_kind,omitempty"`
	ExternalURL        string      `json:"external_url,omitempty"`
	TargetURL          string      `json:"target_url,omitempty"`
	AppendPath         *bool       `json:"append_path,omitempty"`
	ConnectedPrincipal string      `json:"connected_principal,omitempty"`
	ConnectMode        ConnectMode `json:"connect_mode,omitempty"`
	DisplayName        string      `json:"display_name,omitempty"`
	Purpose            string      `json:"purpose,omitempty"`
	OwnerGroup         string      `json:"owner_group,omitempty"`
	Listed             *bool       `json:"listed,omitempty"`
	Tags               []string    `json:"tags,omitempty"`
	Scopes             []Scope     `json:"scopes,omitempty"`
	MintToken          bool        `json:"mint_token,omitempty"`
}

// CreateEntryResponse is returned by POST /api/entries and
// POST /api/entries/{name}/promote-connected. Token is populated at most once,
// when a connected registration credential ("grc_...") was minted.
type CreateEntryResponse struct {
	Entry
	Token string `json:"token,omitempty"`
}

// UpdateEntryRequest is the body of PATCH /api/entries/{name}.
type UpdateEntryRequest struct {
	DisplayName        *string     `json:"display_name,omitempty"`
	Purpose            *string     `json:"purpose,omitempty"`
	HostKind           *HostKind   `json:"host_kind,omitempty"`
	ExternalURL        *string     `json:"external_url,omitempty"`
	TargetURL          *string     `json:"target_url,omitempty"`
	AppendPath         *bool       `json:"append_path,omitempty"`
	ConnectedPrincipal *string     `json:"connected_principal,omitempty"`
	ConnectMode        ConnectMode `json:"connect_mode,omitempty"`
	OwnerGroup         *string     `json:"owner_group,omitempty"`
	Listed             *bool       `json:"listed,omitempty"`
	Tags               *[]string   `json:"tags,omitempty"`
}

// DeleteEntryRequest is the body of DELETE /api/entries/{name}.
type DeleteEntryRequest struct {
	Confirm string `json:"confirm"`
}

// HeartbeatRequest is the body of PUT /api/entries/{name}/heartbeat.
type HeartbeatRequest struct {
	GrainVersion       string      `json:"grain_version"`
	GrainCommit        string      `json:"grain_commit"`
	Health             HealthState `json:"health"`
	HealthDetail       string      `json:"health_detail,omitempty"`
	ActiveTasks        int         `json:"active_tasks"`
	AwaitingReplyTasks int         `json:"awaiting_reply_tasks"`
	HostKind           HostKind    `json:"host_kind,omitempty"`
	ExternalURL        string      `json:"external_url,omitempty"`
	DisplayName        string      `json:"display_name,omitempty"`
	Purpose            string      `json:"purpose,omitempty"`
	Scopes             []Scope     `json:"scopes"`
}

// HeartbeatResponse is returned by PUT /api/entries/{name}/heartbeat.
type HeartbeatResponse struct {
	Status                   EntryStatus     `json:"status,omitempty"`
	NextHeartbeatSeconds     int             `json:"next_heartbeat_seconds,omitempty"`
	HeartbeatIntervalSeconds int             `json:"heartbeat_interval_seconds,omitempty"`
	ScopeConflicts           []ScopeConflict `json:"scope_conflicts,omitempty"`
}

// Interval returns the server-advised heartbeat cadence, or 0 if unspecified.
func (r HeartbeatResponse) Interval() time.Duration {
	if r.NextHeartbeatSeconds > 0 {
		return time.Duration(r.NextHeartbeatSeconds) * time.Second
	}
	if r.HeartbeatIntervalSeconds > 0 {
		return time.Duration(r.HeartbeatIntervalSeconds) * time.Second
	}
	return 0
}

// PromoteConnectedRequest is the body of POST /api/entries/{name}/promote-connected.
type PromoteConnectedRequest struct {
	HostKind           HostKind `json:"host_kind,omitempty"`
	ConnectedPrincipal string   `json:"connected_principal,omitempty"`
	MintToken          bool     `json:"mint_token,omitempty"`
}

// RotateCredentialRequest is the body of POST /api/entries/{name}/credentials/rotate.
type RotateCredentialRequest struct {
	Label          string     `json:"label,omitempty"`
	RevokePrevious bool       `json:"revoke_previous,omitempty"`
	RevokeCredID   string     `json:"revoke_cred_id,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// RotateCredentialResponse returns the newly minted registration credential.
type RotateCredentialResponse struct {
	Credential CredentialInfo `json:"credential"`
	Token      string         `json:"token"`
}

// InstanceStatus is the read-only payload served by a grain controller on
// GET /api/registry/status so hosted instances and local diagnostics share
// the exact status vocabulary connected heartbeats push.
type InstanceStatus struct {
	GrainVersion       string      `json:"grain_version"`
	GrainCommit        string      `json:"grain_commit"`
	Health             HealthState `json:"health"`
	HealthDetail       string      `json:"health_detail,omitempty"`
	ActiveTasks        int         `json:"active_tasks"`
	AwaitingReplyTasks int         `json:"awaiting_reply_tasks"`
	Scopes             []Scope     `json:"scopes"`
}

// DirectoryQuery filters GET /api/directory.
type DirectoryQuery struct {
	Query    string
	Repo     string
	Tags     []string
	Statuses []EntryStatus
	HostKind HostKind
	Owner    string
}

// DirectoryEntry is a public catalog projection returned by GET /api/directory.
type DirectoryEntry struct {
	Name                string      `json:"name"`
	DisplayName         string      `json:"display_name,omitempty"`
	URL                 string      `json:"url,omitempty"`
	ExternalURL         string      `json:"external_url,omitempty"`
	Kind                EntryKind   `json:"kind"`
	HostKind            HostKind    `json:"host_kind"`
	Owner               string      `json:"owner,omitempty"`
	OwnerGroup          string      `json:"owner_group,omitempty"`
	Purpose             string      `json:"purpose,omitempty"`
	Tags                []string    `json:"tags,omitempty"`
	Scopes              []Scope     `json:"scopes,omitempty"`
	Status              EntryStatus `json:"status"`
	State               EntryState  `json:"state,omitempty"`
	HeartbeatAgeSeconds *int64      `json:"heartbeat_age_seconds,omitempty"`
	GrainVersion        string      `json:"grain_version,omitempty"`
	GrainCommit         string      `json:"grain_commit,omitempty"`
	ActiveTasks         *int        `json:"active_tasks,omitempty"`
	AwaitingReplyTasks  *int        `json:"awaiting_reply_tasks,omitempty"`
}

// RouteQuery parameterizes GET /api/route.
type RouteQuery struct {
	Repo           string
	Path           string
	IncludeOffline bool
}

// RouteCandidate is one ranked grain returned by GET /api/route.
type RouteCandidate struct {
	DirectoryEntry
	MatchedPrefix string    `json:"matched_prefix"`
	EffectiveRole ScopeRole `json:"effective_role"`
}

var (
	entryNameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)
	tagRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	reservedNames = []string{
		"api", "admin", "www", "static", "status", "healthz",
		"login", "logout", "auth", "mail", "well-known", "directory", "route",
	}
)

// ValidateEntryName checks that name is a valid lowercase DNS label of length
// 3..32 and is not one of the registry's reserved names.
func ValidateEntryName(name string) error {
	if !entryNameRe.MatchString(name) {
		return fmt.Errorf("entry name %q must be 3..32 lowercase letters, digits, or hyphens, starting with a letter and ending with a letter or digit", name)
	}
	if slices.Contains(reservedNames, name) {
		return fmt.Errorf("entry name %q is reserved", name)
	}
	return nil
}

// NormalizeRepo validates and lowercases an "<owner>/<name>" repository slug.
func NormalizeRepo(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q must have the form <owner>/<name>", raw)
	}
	if !validRepoSegment(owner) || !validRepoSegment(name) {
		return "", fmt.Errorf("repository %q contains invalid path characters", raw)
	}
	return owner + "/" + name, nil
}

func validRepoSegment(seg string) bool {
	if seg == "." || seg == ".." {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// NormalizePathPrefix trims surrounding whitespace and slashes from prefix and
// validates that it is either "" (entire repository) or clean slash-separated
// relative directory segments with no "." or ".." elements.
func NormalizePathPrefix(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "/" {
		return "", nil
	}
	if strings.Contains(s, `\`) {
		return "", fmt.Errorf("path_prefix %q must use forward slashes", raw)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("path_prefix %q contains control characters", raw)
		}
	}
	s = strings.Trim(s, "/")
	if s == "" {
		return "", nil
	}
	parts := strings.Split(s, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("path_prefix %q contains empty or dot path segments", raw)
		}
	}
	return s, nil
}

// NormalizeScope validates and normalizes repo, path_prefix, and role.
// If s.Role is empty, it defaults to ScopeRolePrimary.
func NormalizeScope(s Scope) (Scope, error) {
	repo, err := NormalizeRepo(s.Repo)
	if err != nil {
		return Scope{}, err
	}
	prefix, err := NormalizePathPrefix(s.PathPrefix)
	if err != nil {
		return Scope{}, err
	}
	role := ScopeRole(strings.ToLower(strings.TrimSpace(string(s.Role))))
	if role == "" {
		role = ScopeRolePrimary
	}
	if err := ValidateScopeRole(role); err != nil {
		return Scope{}, err
	}
	out := s
	out.Repo = repo
	out.PathPrefix = prefix
	out.Role = role
	return out, nil
}

// NormalizeTags validates, lowercases, and deduplicates entry tags (max 16).
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	var out []string
	seen := make(map[string]bool, len(tags))
	for _, raw := range tags {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" {
			continue
		}
		if !tagRe.MatchString(t) {
			return nil, fmt.Errorf("tag %q must match [a-z0-9][a-z0-9-]{0,31}", raw)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > 16 {
		return nil, fmt.Errorf("at most 16 tags are permitted (got %d)", len(out))
	}
	return out, nil
}

// ValidateRegistryURL validates and normalizes a registry base URL.
// HTTPS is required for non-loopback hosts unless allowInsecureHTTP is true.
// Link-local/cloud-metadata addresses, userinfo credentials, queries, and
// fragments are always rejected.
func ValidateRegistryURL(raw string, allowInsecureHTTP bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("registry URL is empty")
	}
	if len(trimmed) > 2048 {
		return "", fmt.Errorf("registry URL exceeds 2048 bytes")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid registry URL %q: %w", raw, err)
	}
	if u.User != nil {
		return "", fmt.Errorf("registry URL must not include userinfo credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("registry URL must not include query parameters or fragments")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("registry URL %q has no host", raw)
	}
	if isForbiddenMetadataHost(host) {
		return "", fmt.Errorf("registry URL host %q is not permitted", host)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP && !isLoopbackHost(host) {
			return "", fmt.Errorf("registry URL %q must use https:// (http:// is allowed only for loopback addresses)", raw)
		}
	default:
		return "", fmt.Errorf("registry URL %q must use https://", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// ValidateExternalURL validates an optional browser external_url for a
// connected entry (https:// only, no userinfo, <= 2048 bytes).
func ValidateExternalURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 2048 {
		return "", fmt.Errorf("external_url exceeds 2048 bytes")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid external_url %q: %w", raw, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("external_url %q must use https://", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("external_url must not include userinfo credentials")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("external_url must not include a URL fragment")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("external_url %q has no host", raw)
	}
	if isForbiddenMetadataHost(host) {
		return "", fmt.Errorf("external_url host %q is not permitted", host)
	}
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
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
	if lower == "metadata" || strings.HasSuffix(lower, ".internal") && strings.HasPrefix(lower, "metadata.") {
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
