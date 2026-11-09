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

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// RegistryAuthMode selects how a connected grain authenticates to a grain hub.
type RegistryAuthMode string

const (
	// RegistryAuthToken authenticates heartbeats with the per-entry
	// "grc_..." registration credential kept in pkg/secrets under
	// secrets.RegistryRegistrationTokenSecret ("registry-registration-token").
	RegistryAuthToken RegistryAuthMode = "token"
	// RegistryAuthOIDC authenticates requests with an OIDC ID token minted
	// for OIDCAudience (or URL when OIDCAudience is empty).
	RegistryAuthOIDC RegistryAuthMode = "oidc"
)

// NormalizeRegistryAuthMode validates mode, defaulting empty to RegistryAuthToken.
func NormalizeRegistryAuthMode(mode RegistryAuthMode) (RegistryAuthMode, error) {
	switch m := RegistryAuthMode(strings.ToLower(strings.TrimSpace(string(mode)))); m {
	case "", RegistryAuthToken:
		return RegistryAuthToken, nil
	case RegistryAuthOIDC:
		return RegistryAuthOIDC, nil
	default:
		return "", fmt.Errorf("unknown registry authMode %q (must be %q or %q)", mode, RegistryAuthToken, RegistryAuthOIDC)
	}
}

// RegistryScopeRule customizes or adds an advertised scope in Settings without
// creating a second repository configuration source of truth:
//   - Any configured repository with no matching RegistryScopeRule reports
//     (repo, path_prefix: "", role) derived automatically from RepoConfig.Work.
//   - One or more non-excluded rules for a configured repository override its
//     whole-repo entry (for example narrowing path_prefix to "pkg/autoscaler"
//     or overriding role; leaving Role empty inherits the RepoConfig.Work role).
//   - Exclude: true omits that repository (or path_prefix) from advertisement.
//   - A rule naming a repository outside TargetRepos adds that scope.
type RegistryScopeRule struct {
	Repo       string        `json:"repo"`
	PathPrefix string        `json:"pathPrefix"`
	Role       api.ScopeRole `json:"role,omitempty"`
	Exclude    bool          `json:"exclude,omitempty"`
}

// RegistryConfig holds the deployment's connected-entry registry settings
// (design/design/hub.design.md, "Connected entries"), stored as JSON in
// grain_config.registry. Secret material ("grc_..." tokens) never lives here;
// it is stored exclusively in pkg/secrets.
type RegistryConfig struct {
	URL          string              `json:"url,omitempty"`
	Entry        string              `json:"entry,omitempty"`
	AuthMode     RegistryAuthMode    `json:"authMode,omitempty"`
	OIDCAudience string              `json:"oidcAudience,omitempty"`
	DisplayName  string              `json:"displayName,omitempty"`
	Purpose      string              `json:"purpose,omitempty"`
	HostKind     api.HostKind        `json:"hostKind,omitempty"`
	Listed       bool                `json:"listed,omitempty"`
	OwnerGroup   string              `json:"ownerGroup,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
	Scopes       []RegistryScopeRule `json:"scopes,omitempty"`
}

// Enabled reports whether both a registry URL and an entry name are configured.
func (c RegistryConfig) Enabled() bool {
	return strings.TrimSpace(c.URL) != "" && strings.TrimSpace(c.Entry) != ""
}

// Empty reports whether all fields of c are zero-valued.
func (c RegistryConfig) Empty() bool {
	return strings.TrimSpace(c.URL) == "" &&
		strings.TrimSpace(c.Entry) == "" &&
		(c.AuthMode == "" || c.AuthMode == RegistryAuthToken) &&
		strings.TrimSpace(c.OIDCAudience) == "" &&
		strings.TrimSpace(c.DisplayName) == "" &&
		strings.TrimSpace(c.Purpose) == "" &&
		c.HostKind == "" &&
		!c.Listed &&
		strings.TrimSpace(c.OwnerGroup) == "" &&
		len(c.Tags) == 0 &&
		len(c.Scopes) == 0
}

// EffectiveAuthMode returns c.AuthMode, defaulting to RegistryAuthToken.
func (c RegistryConfig) EffectiveAuthMode() RegistryAuthMode {
	if c.AuthMode == RegistryAuthOIDC {
		return RegistryAuthOIDC
	}
	return RegistryAuthToken
}

// EffectiveHostKind returns c.HostKind, defaulting to api.HostKindOther.
func (c RegistryConfig) EffectiveHostKind() api.HostKind {
	if c.HostKind == "" {
		return api.HostKindOther
	}
	return c.HostKind
}

// EffectiveAudience returns the OIDC audience (OIDCAudience when set, else URL).
func (c RegistryConfig) EffectiveAudience() string {
	if aud := strings.TrimSpace(c.OIDCAudience); aud != "" {
		return aud
	}
	return strings.TrimSpace(c.URL)
}

// NormalizeRegistryConfig validates and canonicalizes cfg.
func NormalizeRegistryConfig(cfg RegistryConfig, allowInsecureHTTP bool) (RegistryConfig, error) {
	var out RegistryConfig

	if rawURL := strings.TrimSpace(cfg.URL); rawURL != "" {
		u, err := api.ValidateRegistryURL(rawURL, allowInsecureHTTP)
		if err != nil {
			return RegistryConfig{}, fmt.Errorf("registry url: %w", err)
		}
		out.URL = u
	}

	if entry := strings.ToLower(strings.TrimSpace(cfg.Entry)); entry != "" {
		if err := api.ValidateEntryName(entry); err != nil {
			return RegistryConfig{}, fmt.Errorf("registry entry: %w", err)
		}
		out.Entry = entry
	}

	if out.URL != "" && out.Entry == "" {
		return RegistryConfig{}, fmt.Errorf("registry entry name is required when registry url is set")
	}

	authMode, err := NormalizeRegistryAuthMode(cfg.AuthMode)
	if err != nil {
		return RegistryConfig{}, err
	}
	if authMode != RegistryAuthToken {
		out.AuthMode = authMode
	}

	if aud := strings.TrimSpace(cfg.OIDCAudience); aud != "" {
		if containsSpaceOrControl(aud) || len(aud) > 512 {
			return RegistryConfig{}, fmt.Errorf("registry oidcAudience must be at most 512 non-whitespace characters")
		}
		out.OIDCAudience = aud
	}

	if dn := strings.TrimSpace(cfg.DisplayName); dn != "" {
		if utf8.RuneCountInString(dn) > api.MaxDisplayNameRunes {
			return RegistryConfig{}, fmt.Errorf("registry displayName must be at most %d characters", api.MaxDisplayNameRunes)
		}
		if hasControlChars(dn, false) {
			return RegistryConfig{}, fmt.Errorf("registry displayName must not contain control characters")
		}
		out.DisplayName = dn
	}

	if purpose := strings.TrimSpace(cfg.Purpose); purpose != "" {
		if utf8.RuneCountInString(purpose) > api.MaxPurposeRunes {
			return RegistryConfig{}, fmt.Errorf("registry purpose must be at most %d characters", api.MaxPurposeRunes)
		}
		if hasControlChars(purpose, true) {
			return RegistryConfig{}, fmt.Errorf("registry purpose must not contain control characters")
		}
		out.Purpose = purpose
	}

	if hk := strings.ToLower(strings.TrimSpace(string(cfg.HostKind))); hk != "" {
		normHK := api.HostKind(hk)
		switch normHK {
		case api.HostKindWorkstation, api.HostKindKubernetes, api.HostKindOther:
			out.HostKind = normHK
		default:
			return RegistryConfig{}, fmt.Errorf("registry hostKind %q must be workstation, kubernetes, or other", cfg.HostKind)
		}
	}

	out.Listed = cfg.Listed

	if og := strings.TrimSpace(cfg.OwnerGroup); og != "" {
		if containsSpaceOrControl(og) || len(og) > 256 {
			return RegistryConfig{}, fmt.Errorf("registry ownerGroup must be at most 256 non-whitespace characters")
		}
		out.OwnerGroup = og
	}

	var tags []string
	if len(cfg.Tags) > 0 {
		seenTag := make(map[string]bool, len(cfg.Tags))
		for _, rawTag := range cfg.Tags {
			t := strings.ToLower(strings.TrimSpace(rawTag))
			if t == "" || seenTag[t] {
				continue
			}
			seenTag[t] = true
			tags = append(tags, t)
		}
	}
	if err := api.ValidateCatalogMetadata(out.DisplayName, out.Purpose, tags); err != nil {
		return RegistryConfig{}, fmt.Errorf("registry metadata: %w", err)
	}
	out.Tags = tags

	rules, err := NormalizeRegistryScopeRules(cfg.Scopes)
	if err != nil {
		return RegistryConfig{}, err
	}
	out.Scopes = rules

	return out, nil
}

// NormalizeRegistryScopeRules validates, canonicalizes, and deduplicates rules.
func NormalizeRegistryScopeRules(rules []RegistryScopeRule) ([]RegistryScopeRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make([]RegistryScopeRule, 0, len(rules))
	type key struct{ repo, prefix string }
	seen := make(map[key]bool, len(rules))
	for i, r := range rules {
		repo, err := api.NormalizeRepo(r.Repo)
		if err != nil {
			return nil, fmt.Errorf("registry scopes[%d]: %w", i, err)
		}
		if ref, err := ParseRepo(repo); err == nil && IsConfigRepo(ref) {
			return nil, fmt.Errorf("registry scopes[%d]: config repository %s cannot be advertised", i, repo)
		}
		prefix, err := api.NormalizePathPrefix(r.PathPrefix)
		if err != nil {
			return nil, fmt.Errorf("registry scopes[%d]: %w", i, err)
		}
		role := api.ScopeRole(strings.ToLower(strings.TrimSpace(string(r.Role))))
		if role != "" && !role.Valid() {
			return nil, fmt.Errorf("registry scopes[%d]: invalid role %q (must be primary or secondary)", i, r.Role)
		}
		if r.Exclude {
			role = ""
		}
		k := key{repo: repo, prefix: prefix}
		if seen[k] {
			return nil, fmt.Errorf("registry scopes[%d]: duplicate rule for %s (pathPrefix %q)", i, repo, prefix)
		}
		seen[k] = true
		out = append(out, RegistryScopeRule{
			Repo:       repo,
			PathPrefix: prefix,
			Role:       role,
			Exclude:    r.Exclude,
		})
	}
	return out, nil
}

func hasControlChars(s string, allowNewline bool) bool {
	for _, r := range s {
		if allowNewline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// EncodeRegistryConfig serializes cfg as JSON for grain_config.registry, or ""
// when cfg is empty.
func EncodeRegistryConfig(cfg RegistryConfig) (string, error) {
	if cfg.Empty() {
		return "", nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding registry config: %w", err)
	}
	return string(b), nil
}

// DecodeRegistryConfig parses grain_config.registry JSON back into RegistryConfig.
func DecodeRegistryConfig(raw string) (RegistryConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RegistryConfig{}, nil
	}
	var cfg RegistryConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return RegistryConfig{}, fmt.Errorf("decoding registry config: %w", err)
	}
	return cfg, nil
}

// RegistryScopeOrigin explains why an advertised scope is present.
type RegistryScopeOrigin string

const (
	RegistryScopeFromRepoConfig RegistryScopeOrigin = "repo_config"
	RegistryScopeOverridden     RegistryScopeOrigin = "override"
	RegistryScopeAdded          RegistryScopeOrigin = "added"
)

// RegistryAdvertisedScope pairs a normalized api.Scope with its derivation origin.
type RegistryAdvertisedScope struct {
	Repo       string              `json:"repo"`
	PathPrefix string              `json:"pathPrefix"`
	Role       api.ScopeRole       `json:"role"`
	Origin     RegistryScopeOrigin `json:"origin"`
}

// DeriveRegistryScopes computes the scopes this grain advertises to the registry
// without inventing a second source of truth (design/design/hub.design.md,
// "Catalog metadata and scopes"):
//  1. Configured repositories come from targetRepos and localRepos (falling back
//     to repoConfigs when targetRepos is empty), excluding grain/config and stateRepo.
//  2. Each configured repository's default role is "primary" when
//     RepoConfig.Work.RunsByDefault() is true ("", "on") and "secondary" when
//     RepoConfig.Work.Default is "off".
//  3. Optional rules in Settings (RegistryConfig.Scopes) can narrow a configured
//     repository to sub-path prefixes, override its role (or inherit the
//     work-ownership role when rule.Role == ""), exclude a repository, or add
//     extra scopes.
func DeriveRegistryScopes(
	targetRepos []string,
	localRepos []string,
	stateRepo RepoRef,
	repoConfigs []RepoConfig,
	rules []RegistryScopeRule,
) []RegistryAdvertisedScope {
	roleByRepo := make(map[string]api.ScopeRole, len(repoConfigs))
	for i := range repoConfigs {
		rc := &repoConfigs[i]
		norm, err := api.NormalizeRepo(rc.Repo.String())
		if err != nil {
			continue
		}
		if rc.Work.RunsByDefault() {
			roleByRepo[norm] = api.RolePrimary
		} else {
			roleByRepo[norm] = api.RoleSecondary
		}
	}

	configured := make(map[string]bool)
	addConfigured := func(raw string) {
		ref, err := ParseRepo(raw)
		if err != nil {
			return
		}
		if IsConfigRepo(ref) || (stateRepo != (RepoRef{}) && stateRepo.Same(ref)) {
			return
		}
		norm, err := api.NormalizeRepo(ref.String())
		if err != nil {
			return
		}
		configured[norm] = true
	}
	for _, r := range targetRepos {
		addConfigured(r)
	}
	for _, r := range localRepos {
		addConfigured(r)
	}
	if len(targetRepos) == 0 {
		for i := range repoConfigs {
			addConfigured(repoConfigs[i].Repo.String())
		}
	}

	rulesByRepo := make(map[string][]RegistryScopeRule)
	for _, r := range rules {
		normRepo, err := api.NormalizeRepo(r.Repo)
		if err != nil {
			continue
		}
		if ref, err := ParseRepo(normRepo); err == nil {
			if IsConfigRepo(ref) || (stateRepo != (RepoRef{}) && stateRepo.Same(ref)) {
				continue
			}
		}
		prefix, err := api.NormalizePathPrefix(r.PathPrefix)
		if err != nil {
			continue
		}
		r.Repo = normRepo
		r.PathPrefix = prefix
		rulesByRepo[normRepo] = append(rulesByRepo[normRepo], r)
	}

	defaultRoleFor := func(repo string) api.ScopeRole {
		if role, ok := roleByRepo[repo]; ok {
			return role
		}
		return api.RolePrimary
	}

	var out []RegistryAdvertisedScope
	for repo := range configured {
		repoRules, hasRules := rulesByRepo[repo]
		if !hasRules {
			out = append(out, RegistryAdvertisedScope{
				Repo:       repo,
				PathPrefix: "",
				Role:       defaultRoleFor(repo),
				Origin:     RegistryScopeFromRepoConfig,
			})
			continue
		}
		for _, r := range repoRules {
			if r.Exclude {
				continue
			}
			role := r.Role
			if role == "" {
				role = defaultRoleFor(repo)
			}
			out = append(out, RegistryAdvertisedScope{
				Repo:       repo,
				PathPrefix: r.PathPrefix,
				Role:       role,
				Origin:     RegistryScopeOverridden,
			})
		}
	}

	for repo, repoRules := range rulesByRepo {
		if configured[repo] {
			continue
		}
		for _, r := range repoRules {
			if r.Exclude {
				continue
			}
			role := r.Role
			if role == "" {
				role = defaultRoleFor(repo)
			}
			out = append(out, RegistryAdvertisedScope{
				Repo:       repo,
				PathPrefix: r.PathPrefix,
				Role:       role,
				Origin:     RegistryScopeAdded,
			})
		}
	}

	slices.SortFunc(out, func(a, b RegistryAdvertisedScope) int {
		if c := strings.Compare(a.Repo, b.Repo); c != 0 {
			return c
		}
		return strings.Compare(a.PathPrefix, b.PathPrefix)
	})
	return out
}

// ToScopeInputs converts advertised scopes into wire api.ScopeInput items for
// entry creation and scope-declaration requests.
func ToScopeInputs(scopes []RegistryAdvertisedScope) []api.ScopeInput {
	out := make([]api.ScopeInput, len(scopes))
	for i, s := range scopes {
		out[i] = api.ScopeInput{
			Repo:       s.Repo,
			PathPrefix: s.PathPrefix,
			Role:       s.Role,
		}
	}
	return out
}

// ToHeartbeatScopes converts advertised scopes into wire api.HeartbeatScope
// items for connected-entry heartbeats and instance status probes.
func ToHeartbeatScopes(scopes []RegistryAdvertisedScope) []api.HeartbeatScope {
	out := make([]api.HeartbeatScope, len(scopes))
	for i, s := range scopes {
		out[i] = api.HeartbeatScope{
			Repo:       s.Repo,
			PathPrefix: s.PathPrefix,
			Role:       s.Role,
		}
	}
	return out
}

// RegistryConnectionState summarizes a grain daemon's live registry connection state.
type RegistryConnectionState string

const (
	RegistryStateDisabled   RegistryConnectionState = "disabled"
	RegistryStateConnecting RegistryConnectionState = "connecting"
	RegistryStateConnected  RegistryConnectionState = "connected"
	RegistryStateDegraded   RegistryConnectionState = "degraded"
	RegistryStateError      RegistryConnectionState = "error"
)

// RegistrySnapshot reports a deployment's current connected-entry registration
// and heartbeat status for GET /api/registry, Settings, `grain hub status`,
// and `grain health`.
type RegistrySnapshot struct {
	Enabled                  bool                      `json:"enabled"`
	State                    RegistryConnectionState   `json:"state"`
	Config                   RegistryConfig            `json:"config"`
	EffectiveHostKind        api.HostKind              `json:"effectiveHostKind,omitempty"`
	TokenConfigured          bool                      `json:"tokenConfigured"`
	CredentialSource         string                    `json:"credentialSource,omitempty"`
	RegistryStatus           api.EntryStatus           `json:"registryStatus,omitempty"`
	Registered               bool                      `json:"registered"`
	LastHeartbeatAt          *time.Time                `json:"lastHeartbeatAt,omitempty"`
	LastAttemptAt            *time.Time                `json:"lastAttemptAt,omitempty"`
	NextHeartbeatAt          *time.Time                `json:"nextHeartbeatAt,omitempty"`
	HeartbeatIntervalSeconds int                       `json:"heartbeatIntervalSeconds,omitempty"`
	ConsecutiveFailures      int                       `json:"consecutiveFailures,omitempty"`
	LastError                string                    `json:"lastError,omitempty"`
	ScopeConflicts           []api.ScopeConflict       `json:"scopeConflicts,omitempty"`
	GrainVersion             string                    `json:"grainVersion,omitempty"`
	GrainCommit              string                    `json:"grainCommit,omitempty"`
	Health                   api.HealthState           `json:"health,omitempty"`
	HealthDetail             string                    `json:"healthDetail,omitempty"`
	ActiveTasks              int                       `json:"activeTasks"`
	AwaitingReplyTasks       int                       `json:"awaitingReplyTasks"`
	AdvertisedScopes         []RegistryAdvertisedScope `json:"advertisedScopes"`
}

// RegistryTaskCounts returns the current counts of running ("active") and
// awaiting-reply tasks in the store, read directly from the task_state view.
func (s *Store) RegistryTaskCounts(ctx context.Context) (active int, awaitingReply int, err error) {
	err = each(ctx, s.db,
		"SELECT `state`, COUNT(*) FROM `task_state` WHERE `state` IN (?, ?) GROUP BY `state`",
		[]any{string(StateRunning), string(StateAwaitingReply)},
		func(rows *sql.Rows) error {
			var state string
			var count int
			if scanErr := rows.Scan(&state, &count); scanErr != nil {
				return scanErr
			}
			switch State(state) {
			case StateRunning:
				active = count
			case StateAwaitingReply:
				awaitingReply = count
			}
			return nil
		},
	)
	return active, awaitingReply, err
}
