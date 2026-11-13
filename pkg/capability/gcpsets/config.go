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

// Package gcpsets is the gcp capability server's engine: admin accounts
// that act on GCP for it, permission sets that say what a grant may do,
// service accounts it creates on demand for each set, and the per-attempt
// keys it mints for them (docs/capabilities.md, "GCP").
//
// Credentials are described by their source:
//
//   - configured: a secret in grain's store, handed to the server through
//     configure -- a service-account key JSON or a workload-identity
//     external-account config;
//   - ambient: the identity the server process already has where it runs
//     -- the user's own on a host through grain hostd (run: host), the
//     container's ADC or metadata identity in a container.
//
// The package never creates, changes or deletes a service account or a
// binding it did not make itself: every account it creates carries its
// deployment's ownership marker in its description (OwnerDescription),
// and the only IAM members it adds or removes are those accounts. Two
// deployments sharing a project never see each other's accounts as
// their own: the marker names the deployment and its installation, and
// listing, minting, reaping and delete_orphans consider only this
// deployment's.
//
// A set's accounts are named <prefix>-<deployment id>-<set>-<n>
// (AccountID), the deployment id being the one grain puts in its branch
// names, which grain sends in configure (protocol.DeploymentIDConfig).
package gcpsets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Credential sources.
const (
	SourceConfigured = "configured"
	SourceAmbient    = "ambient"
)

// DefaultAdmin is the admin account synthesized when a configuration
// omits admins.
const DefaultAdmin = "default"

// DefaultSet is the name migration gives today's fixed pool (Migrate).
const DefaultSet = "sandbox-agent"

// DefaultPoolMax is a set's pool size when it does not say.
const DefaultPoolMax = 3

// DefaultMaxLease is the longest a key lives when a set does not say.
const DefaultMaxLease = 24 * time.Hour

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
var migratedNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var rolePattern = regexp.MustCompile(`^(roles/[A-Za-z0-9_.]+|projects/[a-z0-9-]+/roles/[A-Za-z0-9_.]+|organizations/[0-9]+/roles/[A-Za-z0-9_.]+)$`)
var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
var migratedProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.:-]{0,62}$`)
var serviceAccountEmailPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}@[a-z0-9]([a-z0-9.-]{0,98}[a-z0-9])?\.gserviceaccount\.com$`)
var credentialEnvPattern = regexp.MustCompile(`^(GCP_MINTER_KEY|GRAIN_GCP_MINTER_[A-Z0-9_]+)$`)
var arLocationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var arRepoPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// keyPathPattern is where a migrated gcp-key:<name> set may place its
// key: the path the in-process provider used, and nowhere else in the
// sandbox's home (a set's key must not overwrite another dotfile).
var keyPathPattern = regexp.MustCompile(`^/home/debian/\.gcp-service-account-[a-zA-Z0-9_-]+\.json$`)

// Admin is one admin account: the identity that creates a set's service
// accounts, binds their roles and mints their keys.
type Admin struct {
	Name             string
	CredentialSource string
	// Credential is the configured credential's JSON; empty for ambient.
	// It is a secret: never log it or put it in an error.
	Credential string
	// CredentialEnv names a server environment variable holding the
	// configured credential's JSON when Credential is empty.
	CredentialEnv string
	Projects      []string
	// RoleCeiling is every role this admin may grant; a permission set
	// naming this admin cannot ask for more.
	RoleCeiling []string
}

// PermissionSet is what one grant's with: {permission_set} chooses.
type PermissionSet struct {
	Name    string
	Admin   string
	Project string
	Roles   []string
	// ResourceScope narrows the bindings to one resource rather than the
	// project: "buckets/<name>" or "repositories/<location>/<repo>".
	ResourceScope string
	MaxLease      time.Duration
	PoolMax       int
	// Accounts, when set, are existing service accounts this set mints
	// keys for instead of creating its own: the migrated fixed pool. The
	// server never changes them or their bindings, only their keys.
	Accounts []string
	// KeyPath overrides the primary sandbox key placement path for a
	// migrated gcp-key:<name> set (for example
	// /home/debian/.gcp-service-account-<name>.json).
	KeyPath string
	// AdoptLegacy makes the set's own also the accounts created for it
	// before account names and markers carried the deployment
	// (grain-<set>-<n>, [grain-gcp-key set=<set>]). Off unless the set
	// says so, since nothing tells which deployment those were made by;
	// on for every set Migrate makes.
	AdoptLegacy bool
	// ServiceAccount, when set, is an existing service account every
	// grant of the set acts as (docs/gcp-key-permission-sets.md, "Using
	// an existing service account"). The server never creates, changes or
	// deletes it and never binds a role for it: Roles become what Test
	// checks the account holds, not what anything grants, and the admin's
	// role ceiling does not apply. What the task gets is whatever the
	// account may already do.
	ServiceAccount string
	// Credential is how the set's grants get their credentials:
	// CredentialToken mints short-lived access tokens by impersonation and
	// no key ever exists (creating and binding one grain-managed service
	// account and granting the admin roles/iam.serviceAccountTokenCreator
	// on it when ServiceAccount is omitted, or impersonating ServiceAccount
	// directly when set); CredentialKey mints a per-attempt service-account
	// key (the legacy default when ServiceAccount and Credential are both
	// omitted).
	Credential string
	// PersonGranted makes the set one only a person may grant
	// (protocol.PersonGrantedArgKey): an agent holding it cannot pass it
	// on to a task it files. On by default for a set over an existing
	// service account, whose existing permissions may be broad.
	PersonGranted bool
}

// Credential modes of a permission set.
const (
	CredentialToken = "token"
	CredentialKey   = "key"
)

// Existing reports whether the set acts as an existing service account
// it was given (ServiceAccount) rather than accounts it creates.
func (s *PermissionSet) Existing() bool { return s.ServiceAccount != "" }

// TokenMode reports whether the set's grants get impersonated access
// tokens rather than keys.
func (s *PermissionSet) TokenMode() bool {
	if s.Existing() {
		return s.Credential != CredentialKey
	}
	return s.Credential == CredentialToken
}

// ManagedToken reports whether the set is a managed token-mode set: grain
// creates (or adopts) its service account, reconciles its roles and
// grants the admin roles/iam.serviceAccountTokenCreator on that account,
// then mints short-lived access tokens with no keys.
func (s *PermissionSet) ManagedToken() bool {
	return !s.Existing() && len(s.Accounts) == 0 && s.Credential == CredentialToken
}

// TokenModeConfigJSON renders the JSON configuration entry for switching s
// from key mode to token mode without creating keys.
func (s *PermissionSet) TokenModeConfigJSON() string {
	type existingSnippet struct {
		Name           string   `json:"name"`
		Admin          string   `json:"admin,omitempty"`
		ServiceAccount string   `json:"service_account"`
		Credential     string   `json:"credential"`
		Project        string   `json:"project,omitempty"`
		Roles          []string `json:"roles,omitempty"`
	}
	type managedSnippet struct {
		Name          string   `json:"name"`
		Admin         string   `json:"admin,omitempty"`
		Project       string   `json:"project"`
		Credential    string   `json:"credential"`
		Roles         []string `json:"roles"`
		ResourceScope string   `json:"resource_scope,omitempty"`
	}
	admin := s.Admin
	if admin == DefaultAdmin {
		admin = ""
	}
	if s.Existing() {
		proj := s.Project
		if home, ok := AccountProject(s.ServiceAccount); ok && home == proj {
			proj = ""
		}
		b, _ := json.Marshal(existingSnippet{
			Name:           s.Name,
			Admin:          admin,
			ServiceAccount: s.ServiceAccount,
			Credential:     CredentialToken,
			Project:        proj,
			Roles:          s.Roles,
		})
		return string(b)
	}
	roles := s.Roles
	if len(roles) == 0 {
		roles = []string{"roles/..."}
	}
	b, _ := json.Marshal(managedSnippet{
		Name:          s.Name,
		Admin:         admin,
		Project:       s.Project,
		Credential:    CredentialToken,
		Roles:         roles,
		ResourceScope: s.ResourceScope,
	})
	return string(b)
}

// fixed reports whether the set's accounts are given rather than
// created: the migrated pool's Accounts or an existing ServiceAccount.
// Nothing the server does ever creates, binds or deletes one of them.
func (s *PermissionSet) fixed() bool { return len(s.Accounts) > 0 || s.Existing() }

// Config is the server's whole configuration.
type Config struct {
	Admins []Admin
	// AdminCredentialSource is the top-level admin credential source
	// (SourceAmbient by default, or SourceConfigured) used when Admins is
	// omitted.
	AdminCredentialSource string
	// AdminCredential is the configured credential's JSON for the default
	// admin when AdminCredentialSource is SourceConfigured; empty for
	// ambient. It is a secret: never log it or put it in an error.
	AdminCredential string
	Sets            []PermissionSet
	// DeleteOrphans names removed sets whose service accounts the
	// operator has asked to delete, after seeing them listed.
	DeleteOrphans []string
	// Deployment is the deployment id grain puts in its branch names,
	// which configure is sent in protocol.DeploymentIDConfig: the middle
	// of every account name, and part of every ownership marker.
	Deployment string
	// Instance is the installation id (protocol.InstanceIDConfig), which
	// survives a renamed deployment id: an account is this deployment's
	// when its marker carries it (Ours).
	Instance string
	// AccountPrefix starts every account name; DefaultAccountPrefix
	// when the configuration says nothing.
	AccountPrefix string
	// GeminiKey configures the gemini-key method, nil for a server that
	// offers none.
	GeminiKey *GeminiKey
}

// GeminiKey is what the gemini-key method mints Gemini API keys with:
// the project they are created in, and the admin whose credential
// creates them (the formerly in-process gemini-key's minter credential).
type GeminiKey struct {
	Project string
	// Admin names the admin minting; empty is the one admin, or
	// DefaultAdmin among several.
	Admin string
}

// GeminiAdmin is the admin the gemini-key method mints with, or nil when
// it is not configured.
func (c *Config) GeminiAdmin() *Admin {
	if c.GeminiKey == nil {
		return nil
	}
	return c.Admin(c.GeminiKey.Admin)
}

// AccountID is the id of set's n'th account under this configuration's
// prefix and deployment id (the package's AccountID).
func (c *Config) AccountID(set *PermissionSet, n int) string {
	return AccountID(c.prefix(), c.Deployment, set.Project, set.Name, n)
}

// SingleAccountID is the id of a managed token-mode set's one account under
// this configuration's prefix and deployment id (the package's SingleAccountID).
func (c *Config) SingleAccountID(set *PermissionSet) string {
	return SingleAccountID(c.prefix(), c.Deployment, set.Project, set.Name)
}

func (c *Config) prefix() string {
	if c.AccountPrefix == "" {
		return DefaultAccountPrefix
	}
	return c.AccountPrefix
}

// namePattern is how new accounts are named, for Test to say.
func (c *Config) namePattern() string {
	return c.prefix() + "-" + c.Deployment + "-<set>-<n>"
}

// Admin is the admin called name.
func (c *Config) Admin(name string) *Admin {
	for i := range c.Admins {
		if c.Admins[i].Name == name {
			return &c.Admins[i]
		}
	}
	return nil
}

// Set is the permission set called name.
func (c *Config) Set(name string) *PermissionSet {
	for i := range c.Sets {
		if c.Sets[i].Name == name {
			return &c.Sets[i]
		}
	}
	return nil
}

// PersonGrantedSets lists the sets only a person may grant, in
// configured order.
func (c *Config) PersonGrantedSets() []string {
	var out []string
	for _, s := range c.Sets {
		if s.PersonGranted {
			out = append(out, s.Name)
		}
	}
	return out
}

// SetNames lists the permission sets in configured order: the grant
// argument's enum, whose first entry is the default.
func (c *Config) SetNames() []string {
	out := make([]string, 0, len(c.Sets))
	for _, s := range c.Sets {
		out = append(out, s.Name)
	}
	return out
}

// Schema is the server's configure schema (design/spec/capabilities/capabilities.spec.md
// §4.5). By default, a single admin is configured at the top level via
// admin_credential_source (defaulting to ambient) and admin_credential;
// multi-admin configurations may still use admins and admin_credentials.
func Schema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strs := func(desc string) map[string]any {
		return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string", "minLength": 1}}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"admin_credential_source": map[string]any{
				"type":        "string",
				"title":       "Admin credential source",
				"enum":        []any{SourceAmbient, SourceConfigured},
				"default":     SourceAmbient,
				"description": "ambient: the identity the server already runs as (default); configured: a service-account key JSON or workload-identity config in admin_credential.",
			},
			"admin_credential": map[string]any{
				"type":        "string",
				"title":       "Admin credential",
				"description": "Service-account key JSON or workload-identity external-account config when admin_credential_source is configured.",
				"writeOnly":   true,
			},
			"admins": map[string]any{
				"type":        "array",
				"title":       "Admin accounts",
				"description": "Identities that create the sets' service accounts, bind their roles and mint their keys.",
				"deprecated":  true,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"name", "credential_source"},
					"properties": map[string]any{
						"name":              map[string]any{"type": "string", "pattern": namePattern.String(), "description": "The name sets refer to this admin by."},
						"credential_source": map[string]any{"type": "string", "enum": []any{SourceConfigured, SourceAmbient}, "description": "configured: a credential in admin_credentials; ambient: the identity the server already runs as."},
						"credential_env":    map[string]any{"type": "string", "pattern": credentialEnvPattern.String(), "description": "Environment variable holding this admin's configured credential.", "deprecated": true},
						"projects":          strs("Projects this admin acts in."),
						"role_ceiling":      strs("Every role this admin may grant. Each permission set's roles must be a subset of it: a role added to a set has to be added here too, or the whole configuration is refused. Left empty, the ceiling is whatever the admin's sets ask for."),
					},
				},
			},
			"admin_credentials": map[string]any{
				"type":        "string",
				"title":       "Configured admin credentials",
				"description": `JSON object from admin name to its credential (service-account key JSON or workload-identity external-account config), for each admin whose credential_source is configured: {"minter": {...}}.`,
				"writeOnly":   true,
				"deprecated":  true,
			},
			"permission_sets": map[string]any{
				"type":        "array",
				"title":       "Permission sets",
				"description": "What a gcp#gcp-key grant's with: {permission_set} chooses among; the first is the default.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"name"},
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "pattern": migratedNamePattern.String()},
						"credential": map[string]any{
							"type":    "string",
							"enum":    []any{CredentialToken, CredentialKey},
							"default": CredentialToken,
							"x-grain-enum-labels": map[string]any{
								CredentialToken: "token (recommended, no keys)",
								CredentialKey:   "key (service-account key pool)",
							},
							"description": "How grants get credentials. token (recommended, no keys): with no service_account, grain creates one service account (<prefix>-<deployment>-<set>), binds roles on project or resource_scope, grants the admin roles/iam.serviceAccountTokenCreator on that account only, and mints short-lived access tokens; with service_account, grain impersonates that existing account without creating or binding anything. key: grain mints a per-attempt service-account key (pool of up to pool_max accounts, blocked when constraints/iam.disableServiceAccountKeyCreation is enforced).",
						},
						"service_account": map[string]any{"type": "string", "pattern": serviceAccountEmailPattern.String(),
							"description": "Optional: an existing service account every grant acts as, instead of one grain creates and binds. Leave empty for managed sets so grain creates the service account and binds roles itself. When set, grain never creates, changes or deletes the account and binds no roles; roles only list what Test checks it holds. Exclusive with pool_max, adopt_legacy, accounts and resource_scope."},
						"admin":          map[string]any{"type": "string", "description": "The admin account that manages this set.", "deprecated": true},
						"person_granted": map[string]any{"type": "boolean", "description": "Only a person may grant this set; an agent holding it cannot pass it on to a task it files. Default: on with service_account, off otherwise."},
						"project":        str("The one project the set's service accounts live and are bound in (required unless service_account is set, where it defaults to the account's own project)."),
						"roles":          strs("Roles grain binds to a managed set's service account(s) on project or resource_scope, bounded by the admin's role_ceiling (with service_account: optional list checked read-only by Test)."),
						"resource_scope": str("Optional: bind roles on one resource instead of the project -- buckets/<name> or repositories/<location>/<repo> (Artifact Registry). Managed sets only."),
						"max_lease":      map[string]any{"type": "string", "pattern": `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`, "description": "Longest a grant or key may live, as a Go duration (default 24h)."},
						"pool_max":       map[string]any{"type": "integer", "minimum": 1, "maximum": 99, "description": "Key mode only: most service accounts the set may create (default 3). Omit for token mode, which uses a single account with no key cap."},
						"accounts":       map[string]any{"type": "array", "description": "Existing service accounts to mint keys for instead of creating any (the migrated fixed pool).", "items": map[string]any{"type": "string", "pattern": serviceAccountEmailPattern.String()}, "deprecated": true},
						"key_path":       map[string]any{"type": "string", "pattern": keyPathPattern.String(), "description": "Deprecated: primary sandbox key placement path for a migrated gcp-key:<name> set.", "deprecated": true},
						"adopt_legacy":   map[string]any{"type": "boolean", "description": "Also use this set's accounts from before account names carried the deployment id (grain-<set>-<n>, marked [grain-gcp-key set=<set>]). Turn it on only in the one deployment that created them.", "deprecated": true},
					},
				},
			},
			"gemini_key": map[string]any{
				"type":                 "object",
				"title":                "Gemini API keys",
				"description":          "Offer the gemini-key method: a per-attempt Gemini API key, created in project with admin's credential and deleted at release.",
				"additionalProperties": false,
				"required":             []any{"project"},
				"properties": map[string]any{
					"project": str("The project Gemini API keys are created in."),
					"admin":   map[string]any{"type": "string", "description": "The admin whose credential creates them (default: the one admin, or default)."},
				},
			},
			"account_prefix": map[string]any{
				"type":        "string",
				"title":       "Service account name prefix",
				"pattern":     prefixPattern.String(),
				"description": "Starts every service account the sets create, <prefix>-<deployment id>-<set>-<n> (default grain). Letters and digits, at most 10.",
			},
			protocol.DeploymentIDConfig: map[string]any{"type": "string", "description": "Filled in by grain: the deployment id in its branch names.", "deprecated": true},
			protocol.InstanceIDConfig:   map[string]any{"type": "string", "description": "Filled in by grain: its installation id.", "deprecated": true},
			"delete_orphans": map[string]any{
				"type":        "array",
				"title":       "Delete removed sets' service accounts",
				"description": "Names of removed permission sets whose service accounts (listed by Test) should be deleted. Nothing is deleted unless named here.",
				"items":       map[string]any{"type": "string"},
			},
		},
	}
}

// Parse reads a configure call's (already schema-validated) config and
// validates it (Validate).
func Parse(config map[string]any) (*Config, error) {
	return ParseWithEnv(config, nil)
}

// ParseWithEnv is Parse with environment lookup for admins configured via
// credential_env.
func ParseWithEnv(config map[string]any, env func(string) string) (*Config, error) {
	c := &Config{
		AdminCredentialSource: str(config["admin_credential_source"]),
		AdminCredential:       str(config["admin_credential"]),
	}
	creds := map[string]string{}
	if raw, _ := config["admin_credentials"].(string); strings.TrimSpace(raw) != "" {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			// The value is a secret: say what is wrong, not what it says.
			return nil, fmt.Errorf("admin_credentials is not a JSON object from admin name to credential")
		}
		for k, v := range m {
			s := string(v)
			var asString string
			if json.Unmarshal(v, &asString) == nil {
				s = asString
			}
			creds[k] = s
		}
	}
	for _, item := range list(config["admins"]) {
		a := Admin{
			Name:             str(item["name"]),
			CredentialSource: str(item["credential_source"]),
			CredentialEnv:    str(item["credential_env"]),
			Projects:         strList(item["projects"]),
			RoleCeiling:      strList(item["role_ceiling"]),
		}
		a.Credential = creds[a.Name]
		if a.Credential == "" && a.CredentialEnv != "" && credentialEnvPattern.MatchString(a.CredentialEnv) && env != nil {
			a.Credential = env(a.CredentialEnv)
		}
		if a.Credential == "" && a.Name == DefaultAdmin && a.CredentialSource == SourceConfigured && c.AdminCredential != "" {
			a.Credential = c.AdminCredential
		}
		delete(creds, a.Name)
		c.Admins = append(c.Admins, a)
	}
	if len(c.Admins) == 0 && c.AdminCredential == "" && len(creds) == 1 && creds[DefaultAdmin] != "" {
		c.AdminCredential = creds[DefaultAdmin]
		delete(creds, DefaultAdmin)
	}
	if len(creds) > 0 {
		names := make([]string, 0, len(creds))
		for k := range creds {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("admin_credentials names %s, which no admin is called", strings.Join(names, ", "))
	}
	for _, item := range list(config["permission_sets"]) {
		s := PermissionSet{
			Name:           str(item["name"]),
			Admin:          str(item["admin"]),
			Project:        str(item["project"]),
			Roles:          strList(item["roles"]),
			ResourceScope:  str(item["resource_scope"]),
			Accounts:       strList(item["accounts"]),
			KeyPath:        str(item["key_path"]),
			ServiceAccount: str(item["service_account"]),
			Credential:     str(item["credential"]),
		}
		s.AdoptLegacy, _ = item["adopt_legacy"].(bool)
		if v, ok := item["person_granted"].(bool); ok {
			s.PersonGranted = v
		} else {
			s.PersonGranted = s.ServiceAccount != ""
		}
		if v := str(item["max_lease"]); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("permission set %q: max_lease %q is not a positive duration", s.Name, v)
			}
			s.MaxLease = d
		}
		if n, ok := item["pool_max"].(float64); ok {
			s.PoolMax = int(n)
		}
		c.Sets = append(c.Sets, s)
	}
	if g, ok := config["gemini_key"].(map[string]any); ok {
		c.GeminiKey = &GeminiKey{Project: str(g["project"]), Admin: str(g["admin"])}
	}
	c.DeleteOrphans = strList(config["delete_orphans"])
	c.AccountPrefix = str(config["account_prefix"])
	c.Deployment = str(config[protocol.DeploymentIDConfig])
	c.Instance = str(config[protocol.InstanceIDConfig])
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the configuration as a whole: names, credential
// sources, each set's admin, project and role ceiling.
func (c *Config) Validate() error {
	if len(c.Sets) == 0 && c.GeminiKey == nil {
		return fmt.Errorf("at least one permission set is required")
	}
	if c.AccountPrefix != "" && !prefixPattern.MatchString(c.AccountPrefix) {
		return fmt.Errorf("account_prefix %q does not match %s", c.AccountPrefix, prefixPattern)
	}
	if c.Deployment != "" && (len(c.Deployment) > maxDeploymentLen || !deploymentPattern.MatchString(c.Deployment)) {
		return fmt.Errorf("deployment id %q is not one grain's branch names could hold", c.Deployment)
	}
	if c.Instance != "" && !instancePattern.MatchString(c.Instance) {
		return fmt.Errorf("instance id %q is not lowercase letters and digits", c.Instance)
	}
	if len(c.Admins) == 0 {
		src := c.AdminCredentialSource
		cred := strings.TrimSpace(c.AdminCredential)
		if src == "" || (src == SourceAmbient && cred != "") {
			if cred != "" {
				src = SourceConfigured
			} else {
				src = SourceAmbient
			}
		}
		c.Admins = []Admin{{
			Name:             DefaultAdmin,
			CredentialSource: src,
			Credential:       c.AdminCredential,
		}}
	}
	admins := map[string]*Admin{}
	explicitProjects := map[string]bool{}
	explicitCeiling := map[string]bool{}
	for i := range c.Admins {
		a := &c.Admins[i]
		if !namePattern.MatchString(a.Name) {
			return fmt.Errorf("admin name %q does not match %s", a.Name, namePattern)
		}
		if admins[a.Name] != nil {
			return fmt.Errorf("admin %q is configured twice", a.Name)
		}
		admins[a.Name] = a
		explicitProjects[a.Name] = len(a.Projects) > 0
		explicitCeiling[a.Name] = len(a.RoleCeiling) > 0
		if a.CredentialEnv != "" && !credentialEnvPattern.MatchString(a.CredentialEnv) {
			return fmt.Errorf("admin %q: credential_env %q does not match %s", a.Name, a.CredentialEnv, credentialEnvPattern)
		}
		switch a.CredentialSource {
		case SourceConfigured:
			if strings.TrimSpace(a.Credential) == "" {
				return fmt.Errorf("admin %q: credential_source is configured but admin_credentials has no credential for it", a.Name)
			}
			if _, err := credentialKind(a.Credential); err != nil {
				return fmt.Errorf("admin %q: %v", a.Name, err)
			}
		case SourceAmbient:
			if a.Credential != "" || a.CredentialEnv != "" {
				return fmt.Errorf("admin %q: credential_source is ambient, so admin_credentials must not hold a credential for it", a.Name)
			}
		default:
			return fmt.Errorf("admin %q: credential_source must be %s or %s", a.Name, SourceConfigured, SourceAmbient)
		}
		for _, p := range a.Projects {
			if !projectPattern.MatchString(p) {
				return fmt.Errorf("admin %q: %q is not a project id", a.Name, p)
			}
		}
		for _, r := range a.RoleCeiling {
			if !rolePattern.MatchString(r) {
				return fmt.Errorf("admin %q: %q is not a role", a.Name, r)
			}
		}
	}
	seen := map[string]bool{}
	for i := range c.Sets {
		s := &c.Sets[i]
		if len(s.Accounts) == 0 {
			if !namePattern.MatchString(s.Name) {
				return fmt.Errorf("permission set name %q does not match %s", s.Name, namePattern)
			}
		} else if !migratedNamePattern.MatchString(s.Name) {
			return fmt.Errorf("permission set name %q does not match %s", s.Name, migratedNamePattern)
		}
		if seen[s.Name] {
			return fmt.Errorf("permission set %q is configured twice", s.Name)
		}
		seen[s.Name] = true
		if s.Admin == "" {
			if len(c.Admins) == 1 {
				s.Admin = c.Admins[0].Name
			} else if admins[DefaultAdmin] != nil {
				s.Admin = DefaultAdmin
			}
		}
		a := admins[s.Admin]
		if a == nil {
			return fmt.Errorf("permission set %q: no admin is called %q", s.Name, s.Admin)
		}
		if s.Existing() {
			if err := validateExisting(s, a, explicitProjects[a.Name]); err != nil {
				return err
			}
			continue
		}
		switch s.Credential {
		case "":
			s.Credential = CredentialKey
		case CredentialToken:
			if len(s.Accounts) > 0 {
				return fmt.Errorf("permission set %q: credential: token and accounts are exclusive: accounts names a migrated fixed key pool; omit accounts so grain creates and binds a token-mode service account, or set service_account to impersonate one existing account", s.Name)
			}
			if s.PoolMax > 0 {
				return fmt.Errorf("permission set %q: credential: token and pool_max are exclusive: in token mode grain creates one service account and mints short-lived access tokens with no 10-key cap; omit pool_max, or set credential: key for a multi-account key pool", s.Name)
			}
		case CredentialKey:
		default:
			return fmt.Errorf("permission set %q: credential must be %s or %s", s.Name, CredentialToken, CredentialKey)
		}
		if len(s.Accounts) == 0 {
			if s.Project == "" {
				return fmt.Errorf("permission set %q: %q is not a project id: project is required when service_account is omitted (grain creates the set's service account in project; only a service_account set infers project from the account email)", s.Name, s.Project)
			}
			if !projectPattern.MatchString(s.Project) {
				return fmt.Errorf("permission set %q: %q is not a project id", s.Name, s.Project)
			}
		} else if !migratedProjectPattern.MatchString(s.Project) || strings.Contains(s.Project, "..") {
			return fmt.Errorf("permission set %q: %q is not a project id", s.Name, s.Project)
		}
		if s.KeyPath != "" && !keyPathPattern.MatchString(s.KeyPath) {
			return fmt.Errorf("permission set %q: key_path %q is not a sandbox key path", s.Name, s.KeyPath)
		}
		if explicitProjects[a.Name] {
			if !contains(a.Projects, s.Project) {
				return fmt.Errorf("permission set %q: project %q is not one of admin %q's projects", s.Name, s.Project, a.Name)
			}
		} else if !contains(a.Projects, s.Project) {
			a.Projects = append(a.Projects, s.Project)
		}
		if len(s.Roles) == 0 && len(s.Accounts) == 0 {
			return fmt.Errorf("permission set %q grants no roles: add roles for grain to bind on the set's service account, or set service_account to impersonate an existing account whose roles are already bound", s.Name)
		}
		for _, r := range s.Roles {
			if !rolePattern.MatchString(r) {
				return fmt.Errorf("permission set %q: %q is not a role", s.Name, r)
			}
		}
		if explicitCeiling[a.Name] {
			var over []string
			for _, r := range s.Roles {
				if !contains(a.RoleCeiling, r) {
					over = append(over, r)
				}
			}
			if len(over) > 0 {
				verb, it := "exceeds", "it"
				if len(over) > 1 {
					verb, it = "exceed", "them"
				}
				return fmt.Errorf("permission set %q: %s %s admin %s's role ceiling: add %s to the admin's role_ceiling too", s.Name, strings.Join(over, ", "), verb, a.Name, it)
			}
		} else {
			for _, r := range s.Roles {
				if !contains(a.RoleCeiling, r) {
					a.RoleCeiling = append(a.RoleCeiling, r)
				}
			}
		}
		if s.ResourceScope != "" {
			if _, ok := scopeResource(s.Project, s.ResourceScope); !ok {
				return fmt.Errorf("permission set %q: resource_scope %q must be buckets/<name> or repositories/<location>/<repo>", s.Name, s.ResourceScope)
			}
		}
		for _, e := range s.Accounts {
			if !serviceAccountEmailPattern.MatchString(e) || strings.Contains(e, "..") {
				return fmt.Errorf("permission set %q: account %q is not a service account in %s", s.Name, e, s.Project)
			}
		}
		if len(s.Accounts) == 0 && c.Deployment == "" {
			return fmt.Errorf("permission set %q creates service accounts, which are named after this deployment's id, but configure carried none in %s: grain fills it in, so attach this server through grain", s.Name, protocol.DeploymentIDConfig)
		}
		if s.MaxLease <= 0 {
			s.MaxLease = DefaultMaxLease
		}
		if s.ManagedToken() {
			s.PoolMax = 1
		} else if s.PoolMax <= 0 {
			s.PoolMax = DefaultPoolMax
			if len(s.Accounts) > 0 {
				s.PoolMax = len(s.Accounts)
			}
		}
	}
	if g := c.GeminiKey; g != nil {
		if g.Admin == "" {
			if len(c.Admins) == 1 {
				g.Admin = c.Admins[0].Name
			} else if admins[DefaultAdmin] != nil {
				g.Admin = DefaultAdmin
			}
		}
		a := admins[g.Admin]
		if a == nil {
			return fmt.Errorf("gemini_key: no admin is called %q", g.Admin)
		}
		if !migratedProjectPattern.MatchString(g.Project) || strings.Contains(g.Project, "..") {
			return fmt.Errorf("gemini_key: %q is not a project id", g.Project)
		}
		if explicitProjects[a.Name] {
			if !contains(a.Projects, g.Project) {
				return fmt.Errorf("gemini_key: project %q is not one of admin %q's projects", g.Project, a.Name)
			}
		} else if !contains(a.Projects, g.Project) {
			a.Projects = append(a.Projects, g.Project)
		}
	}
	for _, a := range c.Admins {
		if len(a.Projects) == 0 {
			return fmt.Errorf("admin %q names no projects", a.Name)
		}
	}
	return nil
}

// validateExisting checks a set over an existing service account: it
// creates nothing, so nothing that sizes, adopts or binds a pool applies,
// and its roles are checked by Test, never granted, so the admin's role
// ceiling does not bound them. The account must be in one of the admin's
// projects: the admin's right to act as it is what the set relies on.
func validateExisting(s *PermissionSet, a *Admin, explicitProjects bool) error {
	switch {
	case s.AdoptLegacy:
		return fmt.Errorf("permission set %q: service_account and adopt_legacy are exclusive: a set over an existing account adopts nothing", s.Name)
	case len(s.Accounts) > 0:
		return fmt.Errorf("permission set %q: service_account and accounts are exclusive", s.Name)
	case s.PoolMax > 0:
		return fmt.Errorf("permission set %q: service_account and pool_max are exclusive: a set over an existing account creates no pool", s.Name)
	case s.ResourceScope != "":
		return fmt.Errorf("permission set %q: resource_scope binds roles, which a set over an existing service account never does (omit service_account so grain creates a managed account and binds roles on %s, or bind the role yourself and omit resource_scope)", s.Name, s.ResourceScope)
	}
	if !serviceAccountEmailPattern.MatchString(s.ServiceAccount) || strings.Contains(s.ServiceAccount, "..") {
		return fmt.Errorf("permission set %q: service_account %q is not a service account email", s.Name, s.ServiceAccount)
	}
	switch s.Credential {
	case "":
		s.Credential = CredentialToken
	case CredentialToken, CredentialKey:
	default:
		return fmt.Errorf("permission set %q: credential must be %s or %s", s.Name, CredentialToken, CredentialKey)
	}
	home, ok := AccountProject(s.ServiceAccount)
	if !ok {
		return fmt.Errorf("permission set %q: cannot tell which project service account %s is in; use one named <id>@<project>.iam.gserviceaccount.com", s.Name, s.ServiceAccount)
	}
	if explicitProjects {
		if !contains(a.Projects, home) {
			return fmt.Errorf("permission set %q: service account %s is in project %q, which is not one of admin %q's projects", s.Name, s.ServiceAccount, home, a.Name)
		}
	} else if !contains(a.Projects, home) {
		a.Projects = append(a.Projects, home)
	}
	if s.Project == "" {
		s.Project = home
	}
	if !projectPattern.MatchString(s.Project) {
		return fmt.Errorf("permission set %q: %q is not a project id", s.Name, s.Project)
	}
	for _, r := range s.Roles {
		if !rolePattern.MatchString(r) {
			return fmt.Errorf("permission set %q: %q is not a role", s.Name, r)
		}
	}
	if s.MaxLease <= 0 {
		s.MaxLease = DefaultMaxLease
	}
	s.PoolMax = 1
	return nil
}

// AccountProject is the project a service account email names:
// <id>@<project>.iam.gserviceaccount.com, or App Engine's
// <project>@appspot.gserviceaccount.com.
func AccountProject(email string) (string, bool) {
	id, domain, ok := strings.Cut(email, "@")
	if !ok {
		return "", false
	}
	if p, ok := strings.CutSuffix(domain, ".iam.gserviceaccount.com"); ok && projectPattern.MatchString(p) {
		return p, true
	}
	if domain == "appspot.gserviceaccount.com" && projectPattern.MatchString(id) {
		return id, true
	}
	return "", false
}

// credentialKind says which kind of configured credential raw is,
// without ever quoting it.
func credentialKind(raw string) (string, error) {
	var head struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal([]byte(raw), &head); err != nil {
		return "", fmt.Errorf("its configured credential is not JSON")
	}
	switch head.Type {
	case "service_account":
		if head.ClientEmail == "" {
			return "", fmt.Errorf("its service-account key has no client_email")
		}
		return head.Type, nil
	case "external_account":
		return head.Type, nil
	}
	return "", fmt.Errorf("its configured credential must be a service-account key (type service_account) or a workload-identity config (type external_account), not %q", head.Type)
}

func bucketOf(scope string) (string, bool) {
	b, ok := strings.CutPrefix(scope, "buckets/")
	if !ok || b == "" || strings.Contains(b, "/") || strings.Contains(b, "..") {
		return "", false
	}
	return b, true
}

func artifactRepoOf(scope string) (location, repo string, ok bool) {
	rest, ok := strings.CutPrefix(scope, "repositories/")
	if !ok {
		return "", "", false
	}
	loc, name, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(name, "/") || strings.Contains(loc, "..") || strings.Contains(name, "..") {
		return "", "", false
	}
	if !arLocationPattern.MatchString(loc) || !arRepoPattern.MatchString(name) {
		return "", "", false
	}
	return loc, name, true
}

// scopeResource resolves a validated resource_scope in project to the
// Cloud.GetPolicy / SetPolicy resource identifier.
func scopeResource(project, scope string) (string, bool) {
	if b, ok := bucketOf(scope); ok {
		return "buckets/" + b, true
	}
	if loc, repo, ok := artifactRepoOf(scope); ok {
		return "projects/" + project + "/locations/" + loc + "/repositories/" + repo, true
	}
	return "", false
}

func list(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }

func strList(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, it := range items {
		if s := str(it); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
