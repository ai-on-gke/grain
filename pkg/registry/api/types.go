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

// Package api defines the wire data transfer objects, validation rules, typed
// HTTP client, and outbound WebSocket-mux tunnel client for grain-registry.
//
// It intentionally depends only on the Go standard library and leaf internal
// packages (pkg/apierror, pkg/tunnel/wsupgrade, pkg/tunnel/mux) so both the
// grain daemon/CLI and cmd/grain-registry can import it without coupling.
package api

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// EntryKind classifies how grain-registry routes traffic to an entry.
// Note: v1 supports hosted VM instances ("instance") and outbound-tunnel
// connected daemons ("connected"); redirect entries are intentionally omitted.
type EntryKind string

const (
	// KindInstance is a registry-provisioned GCE VM reached over VPC HTTP.
	KindInstance EntryKind = "instance"
	// KindConnected is a self-hosted daemon reached over an outbound grain connect tunnel.
	KindConnected EntryKind = "connected"
)

// Valid reports whether k is a supported v1 EntryKind.
func (k EntryKind) Valid() bool {
	return k == KindInstance || k == KindConnected
}

// EntryState is the persisted lifecycle state of an entry.
type EntryState string

const (
	StateCreating  EntryState = "creating"
	StateRunning   EntryState = "running"
	StateReplacing EntryState = "replacing"
	StateDeleting  EntryState = "deleting"
	StateDeleted   EntryState = "deleted"
	StateFailed    EntryState = "failed"
)

// Valid reports whether s is a known EntryState.
func (s EntryState) Valid() bool {
	switch s {
	case StateCreating, StateRunning, StateReplacing, StateDeleting, StateDeleted, StateFailed:
		return true
	default:
		return false
	}
}

// ReplaceStep tracks sub-progress while an instance entry is in StateReplacing.
type ReplaceStep string

const (
	ReplaceStepDraining   ReplaceStep = "draining"
	ReplaceStepDeletingVM ReplaceStep = "deleting_vm"
	ReplaceStepCreatingVM ReplaceStep = "creating_vm"
	ReplaceStepProbing    ReplaceStep = "probing"
)

// EntryStatus is the derived runtime reachability status of an entry.
type EntryStatus string

const (
	StatusOnline   EntryStatus = "online"
	StatusDegraded EntryStatus = "degraded"
	StatusOffline  EntryStatus = "offline"
)

// Valid reports whether s is a known EntryStatus.
func (s EntryStatus) Valid() bool {
	switch s {
	case StatusOnline, StatusDegraded, StatusOffline:
		return true
	default:
		return false
	}
}

// HealthState is the self-reported daemon health state on heartbeats and status probes.
type HealthState string

const (
	HealthOK        HealthState = "ok"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

// Valid reports whether h is a known HealthState.
func (h HealthState) Valid() bool {
	switch h {
	case HealthOK, HealthDegraded, HealthUnhealthy:
		return true
	default:
		return false
	}
}

// HostKind describes where a connected or hosted grain daemon is running.
type HostKind string

const (
	HostKindRegistryVM  HostKind = "registry-vm"
	HostKindWorkstation HostKind = "workstation"
	HostKindKubernetes  HostKind = "kubernetes"
	HostKindOther       HostKind = "other"
)

// Valid reports whether k is a known HostKind.
func (k HostKind) Valid() bool {
	switch k {
	case HostKindRegistryVM, HostKindWorkstation, HostKindKubernetes, HostKindOther:
		return true
	default:
		return false
	}
}

// ScopeRole distinguishes primary claim ownership from secondary/experimental coverage.
type ScopeRole string

const (
	RolePrimary   ScopeRole = "primary"
	RoleSecondary ScopeRole = "secondary"
)

// Valid reports whether r is a known ScopeRole.
func (r ScopeRole) Valid() bool {
	return r == RolePrimary || r == RoleSecondary
}

// ScopeSource records whether a scope came from live daemon reporting or operator declaration.
type ScopeSource string

const (
	SourceReported ScopeSource = "reported"
	SourceDeclared ScopeSource = "declared"
)

// Valid reports whether s is a known ScopeSource.
func (s ScopeSource) Valid() bool {
	return s == SourceReported || s == SourceDeclared
}

// InactiveReason explains why a declared or reported scope is currently inactive.
type InactiveReason string

const (
	InactiveOverriddenByNarrower  InactiveReason = "overridden_by_narrower"
	InactiveNotReportedByInstance InactiveReason = "not_reported_by_instance"
	InactivePrimaryConflict       InactiveReason = "primary_conflict"
)

// NameStatus reports whether a candidate entry name can be registered.
type NameStatus string

const (
	NameFree     NameStatus = "free"
	NameTaken    NameStatus = "taken"
	NameReserved NameStatus = "reserved"
	NameInvalid  NameStatus = "invalid"
)

const (
	// MaxDisplayNameRunes bounds catalog DisplayName length.
	MaxDisplayNameRunes = 80
	// MaxPurposeRunes bounds catalog Purpose length.
	MaxPurposeRunes = 280
	// MaxTagsPerEntry bounds the number of catalog tags on an entry.
	MaxTagsPerEntry = 10
)

var (
	entryNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)
	tagRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

var reservedEntryNames = map[string]bool{
	"admin":     true,
	"api":       true,
	"assets":    true,
	"auth":      true,
	"connect":   true,
	"docs":      true,
	"ftp":       true,
	"health":    true,
	"healthz":   true,
	"help":      true,
	"imap":      true,
	"internal":  true,
	"localhost": true,
	"login":     true,
	"logout":    true,
	"mail":      true,
	"metrics":   true,
	"null":      true,
	"oauth":     true,
	"proxy":     true,
	"registry":  true,
	"root":      true,
	"smtp":      true,
	"ssh":       true,
	"static":    true,
	"status":    true,
	"support":   true,
	"system":    true,
	"tunnel":    true,
	"undefined": true,
	"vpn":       true,
	"www":       true,
}

// IsReservedEntryName reports whether name is a system-reserved subdomain label.
func IsReservedEntryName(name string) bool {
	return reservedEntryNames[strings.ToLower(strings.TrimSpace(name))]
}

// ValidateEntryName checks that name is a valid, unreserved 3..32 character DNS
// subdomain label matching [a-z][a-z0-9-]{1,30}[a-z0-9].
func ValidateEntryName(name string) error {
	if !entryNameRe.MatchString(name) {
		return fmt.Errorf("invalid entry name %q: must be 3..32 lowercase ASCII letters, digits, or hyphens, starting with a letter and ending with a letter or digit", name)
	}
	if reservedEntryNames[name] {
		return fmt.Errorf("entry name %q is reserved", name)
	}
	return nil
}

// ValidateCatalogMetadata validates optional DisplayName, Purpose, and Tags fields.
func ValidateCatalogMetadata(displayName, purpose string, tags []string) error {
	if utf8.RuneCountInString(displayName) > MaxDisplayNameRunes {
		return fmt.Errorf("display_name exceeds %d characters", MaxDisplayNameRunes)
	}
	if utf8.RuneCountInString(purpose) > MaxPurposeRunes {
		return fmt.Errorf("purpose exceeds %d characters", MaxPurposeRunes)
	}
	if len(tags) > MaxTagsPerEntry {
		return fmt.Errorf("at most %d tags are allowed", MaxTagsPerEntry)
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if !tagRe.MatchString(tag) {
			return fmt.Errorf("invalid tag %q: must match [a-z0-9][a-z0-9-]{0,31}", tag)
		}
		if seen[tag] {
			return fmt.Errorf("duplicate tag %q", tag)
		}
		seen[tag] = true
	}
	return nil
}

// NormalizeRepo canonicalizes a repository identifier (for example
// "https://GitHub.com/Org/Repo.git/" -> "github.com/org/repo").
func NormalizeRepo(repo string) (string, error) {
	r := strings.TrimSpace(repo)
	if r == "" {
		return "", errors.New("repo is required")
	}
	for _, scheme := range []string{"https://", "http://", "ssh://"} {
		if strings.HasPrefix(strings.ToLower(r), scheme) {
			r = r[len(scheme):]
			break
		}
	}
	r = strings.TrimRight(r, "/")
	if strings.HasSuffix(strings.ToLower(r), ".git") {
		r = r[:len(r)-len(".git")]
	}
	r = strings.ToLower(strings.Trim(r, "/"))
	if r == "" {
		return "", fmt.Errorf("invalid repo %q", repo)
	}
	if strings.ContainsAny(r, " \t\r\n?#\\") {
		return "", fmt.Errorf("invalid repo %q: contains forbidden characters", repo)
	}
	parts := strings.Split(r, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid repo %q: expected host/path or owner/repo", repo)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("invalid repo %q: invalid path segment %q", repo, p)
		}
	}
	return r, nil
}

// NormalizePathPrefix canonicalizes a repository path prefix. Both "" and "/"
// normalize to "" (meaning the entire repository). Leading and trailing slashes
// are stripped, and ".", "..", backslashes, or empty internal segments are rejected.
func NormalizePathPrefix(prefix string) (string, error) {
	p := strings.TrimSpace(prefix)
	if p == "" || p == "/" {
		return "", nil
	}
	if strings.ContainsAny(p, "\\\r\n") {
		return "", fmt.Errorf("invalid path_prefix %q: contains forbidden characters", prefix)
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path_prefix %q: invalid path segment %q", prefix, seg)
		}
	}
	return p, nil
}

// PathMatchesPrefix reports whether filePath falls under normalized prefix on a
// path-segment boundary, returning the match specificity (0 for whole-repo "",
// len(prefix) for a non-empty prefix).
func PathMatchesPrefix(filePath, prefix string) (matched bool, specificity int) {
	cleanPrefix := strings.Trim(prefix, "/")
	if cleanPrefix == "" {
		return true, 0
	}
	cleanPath := strings.Trim(filePath, "/")
	if cleanPath == cleanPrefix {
		return true, len(cleanPrefix)
	}
	if strings.HasPrefix(cleanPath, cleanPrefix+"/") {
		return true, len(cleanPrefix)
	}
	return false, 0
}

// PrimaryPrefixesOverlap reports whether two normalized path prefixes within
// the same repository overlap along path-segment boundaries (one is equal to or
// a directory ancestor of the other).
func PrimaryPrefixesOverlap(prefixA, prefixB string) bool {
	a := strings.Trim(prefixA, "/")
	b := strings.Trim(prefixB, "/")
	if a == "" || b == "" || a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ScopeInput is an operator-supplied scope declaration on create or set-scopes.
type ScopeInput struct {
	Repo       string    `json:"repo"`
	PathPrefix string    `json:"path_prefix,omitempty"`
	Role       ScopeRole `json:"role,omitempty"`
}

// Scope is an entry's repository/path claim as stored and returned by the registry.
type Scope struct {
	Repo           string         `json:"repo"`
	PathPrefix     string         `json:"path_prefix"`
	Role           ScopeRole      `json:"role"`
	Source         ScopeSource    `json:"source"`
	Branch         string         `json:"branch,omitempty"`
	Active         bool           `json:"active"`
	InactiveReason InactiveReason `json:"inactive_reason,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at,omitempty"`
}

// ScopeConflict describes why a primary scope claim was rejected or marked inactive.
type ScopeConflict struct {
	Repo                  string `json:"repo"`
	PathPrefix            string `json:"path_prefix"`
	ConflictingEntry      string `json:"conflicting_entry,omitempty"`
	ConflictingOwner      string `json:"conflicting_owner,omitempty"`
	ConflictingOwnerGroup string `json:"conflicting_owner_group,omitempty"`
	ConflictingPrefix     string `json:"conflicting_prefix,omitempty"`
	ConflictingListed     bool   `json:"conflicting_listed"`
}

// CredentialMeta describes a grc_... registration credential without exposing its secret.
type CredentialMeta struct {
	CredentialID string     `json:"credential_id"`
	Label        string     `json:"label,omitempty"`
	CreatedBy    string     `json:"created_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// Entry is the full wire representation of a registry entry.
type Entry struct {
	Name            string           `json:"name"`
	URL             string           `json:"url"`
	Kind            EntryKind        `json:"kind"`
	State           EntryState       `json:"state"`
	Status          EntryStatus      `json:"status"`
	StatusDetail    string           `json:"status_detail,omitempty"`
	ReplaceStep     ReplaceStep      `json:"replace_step,omitempty"`
	DisplayName     string           `json:"display_name,omitempty"`
	Purpose         string           `json:"purpose,omitempty"`
	Owner           string           `json:"owner"`
	OwnerGroup      string           `json:"owner_group,omitempty"`
	PendingOwner    string           `json:"pending_owner,omitempty"`
	Listed          bool             `json:"listed"`
	Tags            []string         `json:"tags,omitempty"`
	Shape           string           `json:"shape,omitempty"`
	Zone            string           `json:"zone,omitempty"`
	MachineType     string           `json:"machine_type,omitempty"`
	BootDiskGB      int              `json:"boot_disk_gb,omitempty"`
	DataDiskGB      int              `json:"data_disk_gb,omitempty"`
	AllowSSH        bool             `json:"allow_ssh"`
	ShipLogs        bool             `json:"ship_logs"`
	BackendURL      string           `json:"backend_url,omitempty"`
	GrainVersion    string           `json:"grain_version,omitempty"`
	GrainCommit     string           `json:"grain_commit,omitempty"`
	TargetVersion   string           `json:"target_version,omitempty"`
	HostKind        HostKind         `json:"host_kind,omitempty"`
	ActiveTasks     int              `json:"active_tasks"`
	QueueDepth      int              `json:"queue_depth"`
	ActiveSessions  int              `json:"active_sessions"`
	ConnectedSince  *time.Time       `json:"connected_since,omitempty"`
	TunnelSessionID string           `json:"tunnel_session_id,omitempty"`
	LastHeartbeatAt *time.Time       `json:"last_heartbeat_at,omitempty"`
	Scopes          []Scope          `json:"scopes,omitempty"`
	Credentials     []CredentialMeta `json:"credentials,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	DeletedAt       *time.Time       `json:"deleted_at,omitempty"`
}

// CreateEntryRequest is the request body for POST /api/entries.
type CreateEntryRequest struct {
	Name            string       `json:"name"`
	Kind            EntryKind    `json:"kind"`
	DisplayName     string       `json:"display_name,omitempty"`
	Purpose         string       `json:"purpose,omitempty"`
	OwnerGroup      string       `json:"owner_group,omitempty"`
	Listed          *bool        `json:"listed,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	Shape           string       `json:"shape,omitempty"`
	Zone            string       `json:"zone,omitempty"`
	AllowSSH        *bool        `json:"allow_ssh,omitempty"`
	ShipLogs        *bool        `json:"ship_logs,omitempty"`
	Scopes          []ScopeInput `json:"scopes,omitempty"`
	CredentialLabel string       `json:"credential_label,omitempty"`
}

// CreateEntryResponse is returned by POST /api/entries. RegistrationToken is
// populated once when creating a connected entry and is never returned again.
type CreateEntryResponse struct {
	Entry             Entry           `json:"entry"`
	RegistrationToken string          `json:"registration_token,omitempty"`
	Credential        *CredentialMeta `json:"credential,omitempty"`
}

// UpdateEntryRequest is the PATCH /api/entries/<name> payload for mutable catalog and VM flags.
type UpdateEntryRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Purpose     *string   `json:"purpose,omitempty"`
	OwnerGroup  *string   `json:"owner_group,omitempty"`
	Listed      *bool     `json:"listed,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	AllowSSH    *bool     `json:"allow_ssh,omitempty"`
	ShipLogs    *bool     `json:"ship_logs,omitempty"`
}

// SetScopesRequest replaces an entry's declared scopes via PUT /api/entries/<name>/scopes.
type SetScopesRequest struct {
	Scopes []ScopeInput `json:"scopes"`
}

// SetScopesResponse returns the resulting scopes after PUT /api/entries/<name>/scopes.
type SetScopesResponse struct {
	Scopes    []Scope         `json:"scopes"`
	Conflicts []ScopeConflict `json:"conflicts,omitempty"`
}

// HeartbeatScope is a single repository checkout or workspace reported by a running daemon.
type HeartbeatScope struct {
	Repo       string `json:"repo"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

// HeartbeatRequest is sent to POST /api/entries/<name>/heartbeat by a connected daemon.
type HeartbeatRequest struct {
	GrainVersion   string           `json:"grain_version,omitempty"`
	GrainCommit    string           `json:"grain_commit,omitempty"`
	HostKind       HostKind         `json:"host_kind,omitempty"`
	Health         HealthState      `json:"health,omitempty"`
	ActiveTasks    int              `json:"active_tasks"`
	QueueDepth     int              `json:"queue_depth"`
	ActiveSessions int              `json:"active_sessions"`
	Scopes         []HeartbeatScope `json:"scopes,omitempty"`
}

// HeartbeatResponse is returned by POST /api/entries/<name>/heartbeat.
type HeartbeatResponse struct {
	Status              EntryStatus     `json:"status"`
	ActiveScopes        []Scope         `json:"active_scopes,omitempty"`
	Conflicts           []ScopeConflict `json:"conflicts,omitempty"`
	NextIntervalSeconds int             `json:"next_interval_seconds,omitempty"`
}

// InstanceStatusResponse is the read-only payload served by a hosted daemon's
// GET /api/registry/status endpoint when probed by the registry reconciler.
type InstanceStatusResponse struct {
	GrainVersion   string           `json:"grain_version"`
	GrainCommit    string           `json:"grain_commit,omitempty"`
	HostKind       HostKind         `json:"host_kind,omitempty"`
	Health         HealthState      `json:"health"`
	ActiveTasks    int              `json:"active_tasks"`
	QueueDepth     int              `json:"queue_depth"`
	ActiveSessions int              `json:"active_sessions"`
	Scopes         []HeartbeatScope `json:"scopes,omitempty"`
}

// RotateCredentialRequest is the request body for POST /api/entries/<name>/credentials/rotate.
type RotateCredentialRequest struct {
	Label          string `json:"label,omitempty"`
	RevokePrevious bool   `json:"revoke_previous,omitempty"`
}

// RotateCredentialResponse returns the newly minted grc_... token once.
type RotateCredentialResponse struct {
	RegistrationToken string         `json:"registration_token"`
	Credential        CredentialMeta `json:"credential"`
}

// UpgradeEntryRequest triggers an image/binary upgrade on a hosted instance entry.
type UpgradeEntryRequest struct {
	TargetVersion string `json:"target_version,omitempty"`
}

// ReplaceEntryRequest triggers a shape or image replacement on a hosted instance entry.
type ReplaceEntryRequest struct {
	Shape         string `json:"shape,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
}

// DeleteEntryRequest deletes an entry via DELETE /api/entries/<name>.
type DeleteEntryRequest struct {
	ConfirmName string `json:"confirm_name,omitempty"`
	DeleteDisk  bool   `json:"delete_disk,omitempty"`
}

// TransferRequest offers ownership transfer via POST /api/entries/<name>/transfer.
type TransferRequest struct {
	NewOwner   string `json:"new_owner"`
	OwnerGroup string `json:"owner_group,omitempty"`
}

// ListEntriesQuery filters GET /api/entries.
type ListEntriesQuery struct {
	Owner          string
	Kind           EntryKind
	Status         EntryStatus
	IncludeDeleted bool
}

// ListEntriesResponse wraps the entries returned by GET /api/entries.
type ListEntriesResponse struct {
	Entries []Entry `json:"entries"`
}

// DirectoryQuery filters GET /api/directory.
type DirectoryQuery struct {
	Query  string
	Repo   string
	Tag    string
	Status EntryStatus
	Kind   EntryKind
	Owner  string
	Limit  int
	Cursor string
}

// DirectoryEntry is the public catalog summary returned for listed entries (and
// entries owned by the caller) on GET /api/directory. Internal infrastructure
// fields (VM IPs, disk sizes, credentials, tunnel session IDs) are omitted.
type DirectoryEntry struct {
	Name         string      `json:"name"`
	URL          string      `json:"url"`
	Kind         EntryKind   `json:"kind"`
	Status       EntryStatus `json:"status"`
	DisplayName  string      `json:"display_name,omitempty"`
	Purpose      string      `json:"purpose,omitempty"`
	Owner        string      `json:"owner"`
	OwnerGroup   string      `json:"owner_group,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	GrainVersion string      `json:"grain_version,omitempty"`
	HostKind     HostKind    `json:"host_kind,omitempty"`
	Scopes       []Scope     `json:"scopes,omitempty"`
	ActiveTasks  int         `json:"active_tasks"`
	QueueDepth   int         `json:"queue_depth"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// DirectoryResponse is returned by GET /api/directory.
type DirectoryResponse struct {
	Entries    []DirectoryEntry `json:"entries"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// RouteQuery queries GET /api/route for which entry owns a repository and path.
type RouteQuery struct {
	Repo           string
	Path           string
	IncludeOffline bool
}

// MatchedScope summarizes the scope that matched a RouteQuery.
type MatchedScope struct {
	Repo        string      `json:"repo"`
	PathPrefix  string      `json:"path_prefix"`
	Role        ScopeRole   `json:"role"`
	Source      ScopeSource `json:"source"`
	Specificity int         `json:"specificity"`
}

// RouteCandidate is one matching entry returned by GET /api/route.
type RouteCandidate struct {
	Name         string       `json:"name"`
	URL          string       `json:"url"`
	Kind         EntryKind    `json:"kind"`
	Status       EntryStatus  `json:"status"`
	DisplayName  string       `json:"display_name,omitempty"`
	Purpose      string       `json:"purpose,omitempty"`
	Owner        string       `json:"owner"`
	OwnerGroup   string       `json:"owner_group,omitempty"`
	MatchedScope MatchedScope `json:"matched_scope"`
}

// RouteResponse is returned by GET /api/route.
type RouteResponse struct {
	Repo       string           `json:"repo"`
	Path       string           `json:"path"`
	Primary    *RouteCandidate  `json:"primary,omitempty"`
	Candidates []RouteCandidate `json:"candidates"`
}

// Shape describes an operator-configured VM shape available for hosted instances.
type Shape struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	MachineType string `json:"machine_type"`
	VCPU        int    `json:"vcpu"`
	MemoryGB    int    `json:"memory_gb"`
	BootDiskGB  int    `json:"boot_disk_gb"`
	DataDiskGB  int    `json:"data_disk_gb"`
	QuotaUnits  int    `json:"quota_units"`
	Default     bool   `json:"default,omitempty"`
}

// ShapesResponse is returned by GET /api/shapes.
type ShapesResponse struct {
	Shapes []Shape `json:"shapes"`
}

// QuotaUsage summarizes a caller's current vs permitted VM quota units and connected entries.
type QuotaUsage struct {
	QuotaUnitsUsed  int `json:"quota_units_used"`
	QuotaUnitsLimit int `json:"quota_units_limit"`
	ConnectedUsed   int `json:"connected_used"`
	ConnectedLimit  int `json:"connected_limit"`
}

// MeResponse is returned by GET /api/me.
type MeResponse struct {
	Email           string     `json:"email"`
	Kind            string     `json:"kind"`
	IsRegistryAdmin bool       `json:"is_registry_admin"`
	CanCreate       bool       `json:"can_create"`
	Groups          []string   `json:"groups,omitempty"`
	Quota           QuotaUsage `json:"quota"`
}

// NameAvailabilityResponse is returned by GET /api/entries/<name>/availability.
type NameAvailabilityResponse struct {
	Name          string     `json:"name"`
	Status        NameStatus `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
}

// OfflineErrorResponse is the structured 502/503 payload returned when a routed
// entry's tunnel or hosted VM is offline.
type OfflineErrorResponse struct {
	Error      string      `json:"error"`
	Entry      string      `json:"entry"`
	Kind       EntryKind   `json:"kind"`
	Status     EntryStatus `json:"status"`
	Owner      string      `json:"owner,omitempty"`
	OwnerGroup string      `json:"owner_group,omitempty"`
	LastSeenAt *time.Time  `json:"last_seen_at,omitempty"`
	Hint       string      `json:"hint,omitempty"`
}
