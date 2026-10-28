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
var rolePattern = regexp.MustCompile(`^(roles/[A-Za-z0-9_.]+|projects/[a-z0-9-]+/roles/[A-Za-z0-9_.]+|organizations/[0-9]+/roles/[A-Za-z0-9_.]+)$`)
var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// Admin is one admin account: the identity that creates a set's service
// accounts, binds their roles and mints their keys.
type Admin struct {
	Name             string
	CredentialSource string
	// Credential is the configured credential's JSON; empty for ambient.
	// It is a secret: never log it or put it in an error.
	Credential string
	Projects   []string
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
	// project; v1 understands buckets: "buckets/<name>".
	ResourceScope string
	MaxLease      time.Duration
	PoolMax       int
	// Accounts, when set, are existing service accounts this set mints
	// keys for instead of creating its own: the migrated fixed pool. The
	// server never changes them or their bindings, only their keys.
	Accounts []string
	// AdoptLegacy makes the set's own also the accounts created for it
	// before account names and markers carried the deployment
	// (grain-<set>-<n>, [grain-gcp-key set=<set>]). Off unless the set
	// says so, since nothing tells which deployment those were made by;
	// on for every set Migrate makes.
	AdoptLegacy bool
}

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
}

// AccountID is the id of set's n'th account under this configuration's
// prefix and deployment id (the package's AccountID).
func (c *Config) AccountID(set *PermissionSet, n int) string {
	return AccountID(c.prefix(), c.Deployment, set.Project, set.Name, n)
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

// SetNames lists the permission sets in configured order: the grant
// argument's enum, whose first entry is the default.
func (c *Config) SetNames() []string {
	out := make([]string, 0, len(c.Sets))
	for _, s := range c.Sets {
		out = append(out, s.Name)
	}
	return out
}

// Schema is the server's configure schema (docs/capabilities-spec.md
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
						"projects":          strs("Projects this admin acts in."),
						"role_ceiling":      strs("Every role this admin may grant; a set cannot exceed it."),
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
				"description": "What a gcp-key grant's with: {permission_set} chooses among; the first is the default.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"name", "project", "roles"},
					"properties": map[string]any{
						"name":           map[string]any{"type": "string", "pattern": namePattern.String()},
						"admin":          map[string]any{"type": "string", "description": "The admin account that manages this set.", "deprecated": true},
						"project":        str("The one project the set's service accounts live and are bound in."),
						"roles":          strs("Roles bound to the set's service accounts within the project (e.g. roles/storage.objectViewer)."),
						"resource_scope": str("Optional: bind on one resource instead of the project, e.g. buckets/<name>."),
						"max_lease":      map[string]any{"type": "string", "pattern": `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`, "description": "Longest a key may live, as a Go duration (default 24h)."},
						"pool_max":       map[string]any{"type": "integer", "minimum": 1, "maximum": 99, "description": "Most service accounts the set may create (default 3)."},
						"accounts":       map[string]any{"type": "array", "description": "Existing service accounts to mint keys for instead of creating any (the migrated fixed pool).", "items": map[string]any{"type": "string", "minLength": 1}, "deprecated": true},
						"adopt_legacy":   map[string]any{"type": "boolean", "description": "Also use this set's accounts from before account names carried the deployment id (grain-<set>-<n>, marked [grain-gcp-key set=<set>]). Turn it on only in the one deployment that created them.", "deprecated": true},
					},
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
			Projects:         strList(item["projects"]),
			RoleCeiling:      strList(item["role_ceiling"]),
		}
		a.Credential = creds[a.Name]
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
			Name:          str(item["name"]),
			Admin:         str(item["admin"]),
			Project:       str(item["project"]),
			Roles:         strList(item["roles"]),
			ResourceScope: str(item["resource_scope"]),
			Accounts:      strList(item["accounts"]),
		}
		s.AdoptLegacy, _ = item["adopt_legacy"].(bool)
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
	if len(c.Sets) == 0 {
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
		switch a.CredentialSource {
		case SourceConfigured:
			if strings.TrimSpace(a.Credential) == "" {
				return fmt.Errorf("admin %q: credential_source is configured but admin_credentials has no credential for it", a.Name)
			}
			if _, err := credentialKind(a.Credential); err != nil {
				return fmt.Errorf("admin %q: %v", a.Name, err)
			}
		case SourceAmbient:
			if a.Credential != "" {
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
		if !namePattern.MatchString(s.Name) {
			return fmt.Errorf("permission set name %q does not match %s", s.Name, namePattern)
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
		if !projectPattern.MatchString(s.Project) {
			return fmt.Errorf("permission set %q: %q is not a project id", s.Name, s.Project)
		}
		if explicitProjects[a.Name] {
			if !contains(a.Projects, s.Project) {
				return fmt.Errorf("permission set %q: project %q is not one of admin %q's projects", s.Name, s.Project, a.Name)
			}
		} else if !contains(a.Projects, s.Project) {
			a.Projects = append(a.Projects, s.Project)
		}
		if len(s.Roles) == 0 && len(s.Accounts) == 0 {
			return fmt.Errorf("permission set %q grants no roles", s.Name)
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
				return fmt.Errorf("permission set %q: %s exceed(s) admin %q's role ceiling", s.Name, strings.Join(over, ", "), a.Name)
			}
		} else {
			for _, r := range s.Roles {
				if !contains(a.RoleCeiling, r) {
					a.RoleCeiling = append(a.RoleCeiling, r)
				}
			}
		}
		if s.ResourceScope != "" {
			if _, ok := bucketOf(s.ResourceScope); !ok {
				return fmt.Errorf("permission set %q: resource_scope %q is not buckets/<name>, the one scope v1 binds on", s.Name, s.ResourceScope)
			}
		}
		for _, e := range s.Accounts {
			if !strings.HasSuffix(e, "@"+s.Project+".iam.gserviceaccount.com") {
				return fmt.Errorf("permission set %q: account %q is not a service account in %s", s.Name, e, s.Project)
			}
		}
		if len(s.Accounts) == 0 && c.Deployment == "" {
			return fmt.Errorf("permission set %q creates service accounts, which are named after this deployment's id, but configure carried none in %s: grain fills it in, so attach this server through grain", s.Name, protocol.DeploymentIDConfig)
		}
		if s.MaxLease <= 0 {
			s.MaxLease = DefaultMaxLease
		}
		if s.PoolMax <= 0 {
			s.PoolMax = DefaultPoolMax
			if len(s.Accounts) > 0 {
				s.PoolMax = len(s.Accounts)
			}
		}
	}
	for _, a := range c.Admins {
		if len(a.Projects) == 0 {
			return fmt.Errorf("admin %q names no projects", a.Name)
		}
	}
	return nil
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
	if !ok || b == "" || strings.Contains(b, "/") {
		return "", false
	}
	return b, true
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
