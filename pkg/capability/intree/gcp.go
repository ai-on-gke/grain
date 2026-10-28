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

package intree

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// GCPMinterKeyEnv is the environment variable the gcp server reads the
// old minter key from: the configured credential of the admin a server
// still set up by its flags is migrated to (gcpsets.Migrate), bound in
// grain/config as "GCP_MINTER_KEY": "$secret:gcp-minter-key".
const GCPMinterKeyEnv = "GCP_MINTER_KEY"

// gcpAccountName is an old -account's name.
var gcpAccountName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// GCPKeyPath is where a grant's key is placed: the one path gcloud and
// the Google client libraries are pointed at in the sandbox.
const GCPKeyPath = gcpkey.SandboxKeyPath

// gcpSecondaryKeyPath is where a grant with primary: false puts its key,
// for a task holding more than one permission set.
func gcpSecondaryKeyPath(set string) string { return "/home/debian/.gcp-keys/" + set + ".json" }

// gcpAboutPath is the file beside a key that says whose it is.
func gcpAboutPath(keyPath string) string { return strings.TrimSuffix(keyPath, ".json") + ".about" }

// gcpAbout is that file: the permission set, account and project the key
// at keyPath belongs to, so an agent can check it holds the credentials
// it was meant to get. It holds nothing secret.
func gcpAbout(set, account string, ps *gcpsets.PermissionSet, keyPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "permission_set: %s\nservice_account: %s\nproject: %s\n", set, account, ps.Project)
	if len(ps.Roles) > 0 {
		fmt.Fprintf(&b, "roles: %s\n", strings.Join(ps.Roles, ", "))
	}
	if ps.ResourceScope != "" {
		fmt.Fprintf(&b, "resource_scope: %s\n", ps.ResourceScope)
	}
	fmt.Fprintf(&b, "key: %s\n", keyPath)
	return b.String()
}

// gcpMintFailure is a mint's failure as grain should see it: credentials
// only a person can fix, and IAM denying the admin, are refusals that say
// what to do; a token fetch that timed out even on its retry, and
// anything else, are transient errors.
func gcpMintFailure(m *gcpsets.Manager, set string, err error) (*protocol.MintResponse, error) {
	if ce, ok := gcpsets.AsCredentialError(err); ok {
		if ce.NeedsOperator() {
			return refusedResponse("permission set " + set + ": " + ce.Error()), nil
		}
		return nil, err
	}
	if gcpsets.IsDenied(err) {
		admin, project := "", ""
		if ps := m.Config.Set(set); ps != nil {
			admin, project = ps.Admin, ps.Project
		}
		return refusedResponse(fmt.Sprintf("permission set %s: IAM denied admin %s in project %s (its credentials are fine; it lacks a permission -- Test in Settings → Capabilities lists which): %v", set, admin, project, err)), nil
	}
	return nil, err
}

// ReapInterval is how often the gcp server sweeps its accounts for keys
// nobody released.
const ReapInterval = 10 * time.Minute

// GCPConfig is the gcp server's configuration before configure: what its
// flags and environment say, migrated to permission sets.
type GCPConfig struct {
	// Fallback is the configuration flags and environment give, used
	// until configure supplies one; nil when they say nothing.
	Fallback *gcpsets.Config
	// FallbackErr is why flags and environment that tried to configure
	// the server did not, reported when configure supplies nothing.
	FallbackErr error
	// Connector reaches GCP; nil is gcpsets.GoogleConnector{}.
	Connector gcpsets.Connector
	Now       func() time.Time
	Log       *log.Logger
}

// gcpConfigSchema is the gcp server's configure schema (§4.5): admins,
// permission sets and the admins' configured credentials, plus the old
// fields (project, accounts, maxKeyAge, minterKey) a configuration written
// before permission sets still uses, which are migrated when
// permission_sets is absent.
func gcpConfigSchema() map[string]any {
	s := gcpsets.Schema()
	props := s["properties"].(map[string]any)
	props["project"] = map[string]any{"type": "string", "description": "Deprecated: the old fixed pool's project; use permission_sets.", "deprecated": true}
	props["accounts"] = map[string]any{
		"type":        "array",
		"description": "Deprecated: the old fixed pools, migrated to permission sets over the same accounts.",
		"deprecated":  true,
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"email"},
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "pattern": gcpAccountName.String()},
				"email": map[string]any{"type": "string", "minLength": 1},
				"count": map[string]any{"type": "integer", "minimum": 1},
			},
		},
	}
	props["maxKeyAge"] = map[string]any{"type": "string", "description": "Deprecated: the old pools' key lifetime.", "pattern": `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`, "deprecated": true}
	props["minterKey"] = map[string]any{"type": "string", "description": "Deprecated: the old minter key, migrated to the configured admin \"minter\".", "writeOnly": true, "deprecated": true}
	return s
}

// gcpConfigFrom is the configuration a configure call gives: its
// permission sets when it has any, otherwise its old fields migrated over
// the fallback's, otherwise the fallback itself.
func gcpConfigFrom(fallback *gcpsets.Config, fallbackErr error, config map[string]any) (*gcpsets.Config, error) {
	cfg, err := gcpConfigOf(fallback, fallbackErr, config)
	if err != nil || cfg.Deployment != "" {
		return cfg, err
	}
	// A migrated configuration names no accounts after the deployment,
	// but Test still says which deployment it is.
	c := *cfg
	c.Deployment, _ = config[protocol.DeploymentIDConfig].(string)
	c.Instance, _ = config[protocol.InstanceIDConfig].(string)
	return &c, nil
}

func gcpConfigOf(fallback *gcpsets.Config, fallbackErr error, config map[string]any) (*gcpsets.Config, error) {
	if _, ok := config["permission_sets"]; ok {
		return gcpsets.Parse(config)
	}
	_, hasProject := config["project"]
	_, hasAccounts := config["accounts"]
	_, hasKey := config["minterKey"]
	if !hasProject && !hasAccounts && !hasKey {
		if fallback == nil {
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			return nil, fmt.Errorf("gcp: no permission sets are configured: add a permission set in Settings → Capabilities")
		}
		return fallback, nil
	}
	project, key := "", ""
	var accounts []gcpsets.LegacyAccount
	if fallback != nil && len(fallback.Admins) > 0 {
		key = fallback.Admins[0].Credential
		project = fallback.Admins[0].Projects[0]
		for _, s := range fallback.Sets {
			accounts = append(accounts, gcpsets.LegacyAccount{Name: s.Name, Email: s.Accounts[0], Count: len(s.Accounts)})
		}
	}
	if v, ok := config["project"].(string); ok {
		project = v
	}
	if v, ok := config["minterKey"].(string); ok {
		key = v
	}
	if list, ok := config["accounts"].([]any); ok {
		accounts = nil
		for _, item := range list {
			a, _ := item.(map[string]any)
			acct := gcpsets.LegacyAccount{Name: "default"}
			if n, ok := a["name"].(string); ok {
				acct.Name = n
			}
			acct.Email, _ = a["email"].(string)
			if c, ok := a["count"].(float64); ok {
				acct.Count = int(c)
			}
			accounts = append(accounts, acct)
		}
	}
	cfg, err := migrateGCP(project, accounts, key)
	if err != nil {
		return nil, err
	}
	if v, ok := config["maxKeyAge"].(string); ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("gcp: maxKeyAge %q is not a positive duration", v)
		}
		for i := range cfg.Sets {
			cfg.Sets[i].MaxLease = d
		}
	}
	return cfg, nil
}

// migrateGCP is gcpsets.Migrate with the old server's own messages.
func migrateGCP(project string, accounts []gcpsets.LegacyAccount, minterKey string) (*gcpsets.Config, error) {
	if project == "" {
		return nil, fmt.Errorf("gcp: -project is required")
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("gcp: at least one -account is required")
	}
	if minterKey == "" {
		return nil, fmt.Errorf("gcp: %s is not set; bind it to the minter key's secret in grain/config, or configure an admin account", GCPMinterKeyEnv)
	}
	for _, a := range accounts {
		if !gcpAccountName.MatchString(a.Name) {
			return nil, fmt.Errorf("gcp: account name %q does not match %s", a.Name, gcpAccountName)
		}
		if a.Email == "" {
			return nil, fmt.Errorf("gcp: account %q has no service-account email", a.Name)
		}
	}
	cfg, err := gcpsets.Migrate(project, accounts, minterKey)
	if err != nil {
		return nil, fmt.Errorf("gcp: %v", err)
	}
	return cfg, nil
}

// newGCPMethod is the gcp-key method over m: with: {permission_set}
// chooses the set, an enum of the configured ones whose first is the
// default -- so a grant with no arguments, as every grant was before
// permission sets, gets the first set. report is what Test shows.
func newGCPMethod(m *gcpsets.Manager, now func() time.Time, report string) (*protocol.Method, error) {
	names := make([]any, 0, len(m.Config.Sets))
	for _, n := range m.Config.SetNames() {
		names = append(names, n)
	}
	desc := "Mint a per-attempt key for a service account of one of this deployment's GCP permission sets, deleted at release or past the attempt's deadline."
	if report != "" {
		desc += "\n\n" + report
	}
	return protocol.NewMethod(protocol.MethodSpec{
		Name:        "gcp-key",
		Description: desc,
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"permission_set": map[string]any{"type": "string", "title": "Permission set", "enum": names, "default": names[0]},
				"primary": map[string]any{"type": "boolean", "default": true,
					"description": "Place the key at ~/.gcp-service-account.json and activate it; false places it at ~/.gcp-keys/<set>.json only, for a task's second set."},
				"account":  map[string]any{"type": "string", "pattern": gcpAccountName.String(), "description": "Deprecated: the old name for permission_set; \"default\" is the first set."},
				"activate": map[string]any{"type": "boolean", "description": "Deprecated: a primary key is always activated."},
			},
		},
		Now: now,
		Mint: func(ctx context.Context, attemptID string, deadline time.Time, args map[string]any) (*protocol.MintResponse, error) {
			if !pathSafe(attemptID) {
				return refusedResponse("attempt_id " + attemptID + " cannot name a directory"), nil
			}
			set, _ := args["permission_set"].(string)
			if old, ok := args["account"].(string); ok && old != "" {
				set = old
				if old == "default" && m.Config.Set(old) == nil {
					set = m.Config.Sets[0].Name
				}
			}
			primary := true
			if v, ok := args["primary"].(bool); ok {
				primary = v
			}
			keyJSON, lease, err := m.Mint(ctx, attemptID, set, deadline)
			if err != nil {
				if r, ok := err.(*gcpsets.Refusal); ok {
					return refusedResponse(r.Reason), nil
				}
				if e, ok := err.(*gcpsets.Exhausted); ok {
					return &protocol.MintResponse{Status: protocol.StatusResourceExhausted, Reason: e.Reason, RetryAfter: e.RetryAfter.UTC().Format(time.RFC3339)}, nil
				}
				return gcpMintFailure(m, set, err)
			}
			ps := m.Config.Set(set)
			keyPath := GCPKeyPath
			if !primary {
				keyPath = gcpSecondaryKeyPath(set)
			}
			aboutPath := gcpAboutPath(keyPath)
			roles := ""
			if len(ps.Roles) > 0 {
				roles = ", roles " + strings.Join(ps.Roles, ", ")
			}
			resp := &protocol.MintResponse{
				Status: protocol.StatusOK,
				Placements: []protocol.Placement{
					{Path: keyPath, Mode: "0600", Content: keyJSON},
					{Path: aboutPath, Mode: "0644", Content: gcpAbout(set, lease.Account, ps, keyPath)},
				},
				PromptSection: fmt.Sprintf("GCP permission set %s: service account %s in project %s%s. Its key is at %s; its client_email is that account, and %s names the set, the account and the project.",
					set, lease.Account, ps.Project, roles, keyPath, aboutPath),
			}
			if primary {
				resp.PromptSection += " gcloud is logged in as that account with the project selected, and GOOGLE_APPLICATION_CREDENTIALS-style clients find the key there."
				script := "/home/debian/.gcp-key/" + attemptID + "/activate.sh"
				resp.Placements = append(resp.Placements, protocol.Placement{
					Path: script, Mode: "0700",
					Content: "#!/bin/sh\nset -eu\n" +
						"command -v gcloud >/dev/null 2>&1 || exit 0\n" +
						"gcloud auth activate-service-account --key-file=" + shellQuote(keyPath) + " >/dev/null 2>&1\n" +
						"gcloud config set project " + shellQuote(ps.Project) + " >/dev/null 2>&1\n",
				})
				resp.Setup = script
			}
			return resp, nil
		},
		Release: m.Release,
	})
}

// NewGCP builds the gcp server configured by cfg.Fallback alone, with no
// configure tool: what tests and a fixed deployment use.
func NewGCP(cfg GCPConfig) (*Server, error) {
	if cfg.Fallback == nil {
		return nil, fmt.Errorf("gcp: no permission sets are configured")
	}
	m := newGCPManager(cfg, cfg.Fallback)
	method, err := newGCPMethod(m, m.Now, "")
	if err != nil {
		return nil, err
	}
	s, err := newServer("gcp", cfg.Log, method)
	if err != nil {
		return nil, err
	}
	s.onTick(reaper(s, func() *gcpsets.Manager { return m }))
	return s, nil
}

func newGCPManager(cfg GCPConfig, c *gcpsets.Config) *gcpsets.Manager {
	conn := cfg.Connector
	if conn == nil {
		conn = gcpsets.GoogleConnector{}
	}
	m := gcpsets.NewManager(c, conn)
	if cfg.Now != nil {
		m.Now = cfg.Now
	}
	return m
}

// reaper is the tick that runs current's Reap every ReapInterval.
func reaper(s *Server, current func() *gcpsets.Manager) func(context.Context) {
	var last time.Time
	return func(ctx context.Context) {
		m := current()
		if m == nil || (!last.IsZero() && m.Now().Sub(last) < ReapInterval) {
			return
		}
		last = m.Now()
		deleted, err := m.Reap(ctx)
		for _, k := range deleted {
			s.Log.Printf("gcp: reaped unreleased key %s", k)
		}
		if err != nil {
			s.Log.Printf("gcp: reaping: %v", err)
		}
	}
}

func init() {
	register(Builder{
		Name:        "gcp",
		NeedsConfig: true,
		Host:        true,
		Summary:     "per-attempt keys for GCP permission sets' service accounts (mint_gcp-key)",
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("gcp", logOutput(logger))
			project := fs.String("project", "", "deprecated: the old fixed pool's project; configure permission sets instead")
			maxAge := fs.Duration("max-key-age", gcpkey.DefaultMaxKeyAge, "deprecated: the old fixed pool's longest key lifetime")
			var accounts listFlag
			fs.Var(&accounts, "account", "deprecated: an old fixed pool, [name=]email[*count] (repeatable)")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			cfg := GCPConfig{Log: logger}
			var legacy []gcpsets.LegacyAccount
			for _, v := range accounts {
				a, err := parseGCPAccount(v)
				if err != nil {
					return nil, err
				}
				legacy = append(legacy, a)
			}
			if *project != "" || len(legacy) > 0 {
				// A flag error is the configuration's to report: configure
				// may still supply what the flags lack.
				fb, err := migrateGCP(*project, legacy, env(GCPMinterKeyEnv))
				if err != nil {
					cfg.FallbackErr = err
				} else {
					for i := range fb.Sets {
						fb.Sets[i].MaxLease = *maxAge
					}
					cfg.Fallback = fb
				}
			}
			return newConfigurableGCP(cfg)
		},
	})
}

// parseGCPAccount reads -account [name=]email[*count].
func parseGCPAccount(v string) (gcpsets.LegacyAccount, error) {
	a := gcpsets.LegacyAccount{Name: "default"}
	if name, rest, ok := strings.Cut(v, "="); ok {
		a.Name, v = name, rest
	}
	if email, count, ok := strings.Cut(v, "*"); ok {
		v = email
		if _, err := fmt.Sscanf(count, "%d", &a.Count); err != nil || a.Count < 1 {
			return a, fmt.Errorf("-account %q: pool size %q is not a positive number", v, count)
		}
	}
	a.Email = v
	return a, nil
}

// NewConfigurableGCP is newConfigurableGCP, for a caller outside
// `grain capability` that serves the gcp server itself (grain demo).
func NewConfigurableGCP(cfg GCPConfig) (*Server, error) { return newConfigurableGCP(cfg) }

// newConfigurableGCP is the gcp server `grain capability gcp` serves:
// configured by configure, over whatever its flags and environment say.
// Every configure reconciles the sets' bindings, deletes the accounts of
// removed sets the operator named in delete_orphans, and puts a report --
// who each admin is, what it lacks, each set's usage, removed sets'
// accounts -- in the gcp-key method's description, which Settings' Test
// shows.
func newConfigurableGCP(cfg GCPConfig) (*Server, error) {
	var mu sync.Mutex
	var current *gcpsets.Manager
	var methods []*protocol.Method
	if cfg.Fallback != nil {
		m := newGCPManager(cfg, cfg.Fallback)
		method, err := newGCPMethod(m, m.Now, "")
		if err == nil {
			methods, current = append(methods, method), m
		}
	}
	s, err := newServer("gcp", cfg.Log, methods...)
	if err != nil {
		return nil, err
	}
	s.onTick(reaper(s, func() *gcpsets.Manager {
		mu.Lock()
		defer mu.Unlock()
		return current
	}))
	err = s.configurable(gcpConfigSchema(), func(ctx context.Context, config map[string]any) ([]*protocol.Method, error) {
		c, err := gcpConfigFrom(cfg.Fallback, cfg.FallbackErr, config)
		if err != nil {
			return nil, err
		}
		m := newGCPManager(cfg, c)
		report := gcpConfigureReport(ctx, m)
		method, err := newGCPMethod(m, m.Now, report)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		current = m
		mu.Unlock()
		return []*protocol.Method{method}, nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// gcpConfigureTimeout bounds the GCP calls one configure makes.
const gcpConfigureTimeout = 30 * time.Second

func gcpConfigureReport(ctx context.Context, m *gcpsets.Manager) string {
	ctx, cancel := context.WithTimeout(ctx, gcpConfigureTimeout)
	defer cancel()
	var notes []string
	if err := m.Reconcile(ctx); err != nil {
		notes = append(notes, "reconciling bindings: "+err.Error())
	}
	census, err := m.Census(ctx)
	if err != nil {
		notes = append(notes, "listing removed sets' accounts: "+err.Error())
	}
	deleted, err := m.DeleteOrphans(ctx, census.Orphans)
	if err != nil {
		notes = append(notes, "deleting removed sets' accounts: "+err.Error())
	}
	report := m.Report(ctx, census, deleted)
	if len(notes) > 0 {
		report += "\n" + strings.Join(notes, "\n")
	}
	return report
}
