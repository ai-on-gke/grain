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
	"strconv"
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
	// Env reads server environment variables (used for migrated per-admin
	// credential_env bindings).
	Env Env
	// Connector reaches GCP; nil is gcpsets.GoogleConnector{}.
	Connector gcpsets.Connector
	// GeminiMinter builds the gemini-key method's API Keys minter; nil is
	// geminikey.NewKeyMinter.
	GeminiMinter GeminiMinterFunc
	Now          func() time.Time
	Log          *log.Logger
	// Listen is the loopback address of the data-plane listener
	// token-mode permission sets' grants exchange their per-attempt token
	// at (gcpTokens); empty is 127.0.0.1:DefaultGCPTokenPort. Nothing
	// listens until a token-mode set is configured.
	Listen string
}

// gcpMethods is the server's methods over m: gcp-key when a permission
// set is configured, and gemini-key when gemini_key is.
func gcpMethods(cfg GCPConfig, m *gcpsets.Manager, report string, tokens *gcpTokens) ([]*protocol.Method, error) {
	var methods []*protocol.Method
	if len(m.Config.Sets) > 0 {
		method, err := newGCPMethod(m, m.Now, report, tokens)
		if err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	if m.Config.GeminiKey != nil {
		method, err := newGeminiKeyMethod(m.Config, cfg.GeminiMinter, m.Now)
		if err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, nil
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
func gcpConfigFrom(fallback *gcpsets.Config, fallbackErr error, env Env, config map[string]any) (*gcpsets.Config, error) {
	cfg, err := gcpConfigOf(fallback, fallbackErr, env, config)
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

func gcpConfigOf(fallback *gcpsets.Config, fallbackErr error, env Env, config map[string]any) (*gcpsets.Config, error) {
	_, hasSets := config["permission_sets"]
	_, hasGemini := config["gemini_key"]
	if hasSets || hasGemini {
		return gcpsets.ParseWithEnv(config, env)
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
func newGCPMethod(m *gcpsets.Manager, now func() time.Time, report string, tokens *gcpTokens) (*protocol.Method, error) {
	names := make([]any, 0, len(m.Config.Sets))
	for _, n := range m.Config.SetNames() {
		names = append(names, n)
	}
	setArg := map[string]any{"type": "string", "title": "Permission set", "enum": names, "default": names[0]}
	if reserved := m.Config.PersonGrantedSets(); len(reserved) > 0 {
		vals := make([]any, 0, len(reserved))
		for _, n := range reserved {
			vals = append(vals, n)
		}
		setArg[protocol.PersonGrantedArgKey] = vals
	}
	desc := "Mint per-attempt GCP credentials for one of this deployment's permission sets: short-lived access tokens by impersonation with no key at all (credential: token -- either on a grain-managed service account whose roles grain binds, or on an existing service_account), or a per-attempt service-account key deleted at release or past the attempt's deadline (credential: key)."
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
				"permission_set": setArg,
				"primary": map[string]any{"type": "boolean", "default": true,
					"description": "Place the key at ~/.gcp-service-account.json and activate it; false places it at ~/.gcp-keys/<set>.json only, for a task's second set."},
			},
		},
		Now: now,
		Mint: func(ctx context.Context, attemptID string, deadline time.Time, args map[string]any) (*protocol.MintResponse, error) {
			if !pathSafe(attemptID) {
				return refusedResponse("attempt_id " + attemptID + " cannot name a directory"), nil
			}
			set, _ := args["permission_set"].(string)
			primary := true
			if v, ok := args["primary"].(bool); ok {
				primary = v
			}
			if ps := m.Config.Set(set); ps != nil && ps.TokenMode() && !tokens.ready() {
				return nil, fmt.Errorf("permission set %s mints access tokens, but the gcp server has no data-plane listener", set)
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
			what := "access tokens"
			if !ps.TokenMode() {
				what = "key " + lease.KeyID
			}
			tokens.log.Printf("gcp: audit: attempt %s: permission set %s: granted %s for %s until %s", attemptID, set, what, lease.Account, deadline.UTC().Format(time.RFC3339))
			keyPath := GCPKeyPath
			if !primary {
				keyPath = gcpSecondaryKeyPath(set)
			} else if ps.KeyPath != "" {
				keyPath = ps.KeyPath
			}
			aboutPath := gcpAboutPath(keyPath)
			roles := ""
			if len(ps.Roles) > 0 {
				roles = ", roles " + strings.Join(ps.Roles, ", ")
			}
			if ps.TokenMode() {
				return gcpTokenResponse(tokens, attemptID, set, lease, ps, deadline, keyPath, primary)
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
			if primary && keyPath == GCPKeyPath {
				resp.PromptSection += " gcloud is logged in as that account with the project selected, and GOOGLE_APPLICATION_CREDENTIALS-style clients find the key there."
				script := "/home/debian/.gcp-key/" + attemptID + "/activate.sh"
				resp.Placements = append(resp.Placements, protocol.Placement{
					Path: script, Mode: "0700",
					Content: gcpKeyActivateScript(keyPath, ps.Project),
				})
				resp.Setup = script
			}
			return resp, nil
		},
		Release: func(ctx context.Context, attemptID string) error {
			tokens.release(attemptID)
			return m.Release(ctx, attemptID)
		},
	})
}

// gcpTokenResponse is a token-mode grant's placements: the
// external_account config where a key would go, the per-attempt token it
// presents, the .about file, an ID-token helper, the forward to this
// server's listener, and -- for a primary grant -- gcloud pointed at the
// config (gcpTokenActivateScript).
func gcpTokenResponse(tokens *gcpTokens, attemptID, set string, lease gcpsets.Lease, ps *gcpsets.PermissionSet, deadline time.Time, keyPath string, primary bool) (*protocol.MintResponse, error) {
	secret, err := tokens.grant(attemptID, set, lease.Account, deadline)
	if err != nil {
		return nil, err
	}
	port := tokens.listenPort()
	dir := gcpTokenDir(attemptID)
	subject := dir + "/" + set + ".token"
	idScript := dir + "/" + set + "-id-token"
	aboutPath := gcpAboutPath(keyPath)
	about := gcpAbout(set, lease.Account, ps, keyPath) +
		"credential: short-lived access tokens by impersonation (no key exists); " + keyPath + " is an external_account config\n" +
		"id_token: " + idScript + " AUDIENCE\n"
	roles := ""
	promptAccount := "service account " + lease.Account + " in project " + ps.Project
	if ps.Existing() {
		promptAccount = "existing service account " + lease.Account + ", project " + ps.Project
		if len(ps.Roles) > 0 {
			roles = " (listed roles " + strings.Join(ps.Roles, ", ") + "; the account's own permissions are what apply)"
		}
	} else {
		if len(ps.Roles) > 0 {
			roles = ", roles " + strings.Join(ps.Roles, ", ")
			if ps.ResourceScope != "" {
				roles += " on " + ps.ResourceScope
			}
		}
	}
	resp := &protocol.MintResponse{
		Status: protocol.StatusOK,
		Ports:  []protocol.Port{{HostPort: port}},
		Placements: []protocol.Placement{
			{Path: keyPath, Mode: "0600", Content: gcpExternalAccount(port, attemptID, set, subject)},
			{Path: subject, Mode: "0600", Content: secret},
			{Path: idScript, Mode: "0700", Content: gcpIDTokenScript(port, attemptID, subject)},
			{Path: aboutPath, Mode: "0644", Content: about},
		},
		PromptSection: fmt.Sprintf("GCP permission set %s: %s%s. %s is an external_account credential config, not a key: clients that read it get short-lived access tokens for that account, refreshed as they expire, until this attempt ends. `%s AUDIENCE` prints an ID token for it. %s names the set, the account and the project.",
			set, promptAccount, roles, keyPath, idScript, aboutPath),
	}
	if primary && keyPath == GCPKeyPath {
		resp.PromptSection += " gcloud uses it (auth/credential_file_override) with the project selected, and GOOGLE_APPLICATION_CREDENTIALS-style clients find it there."
		script := dir + "/activate.sh"
		resp.Placements = append(resp.Placements, protocol.Placement{
			Path: script, Mode: "0700",
			Content: gcpTokenActivateScript(keyPath, ps.Project, subject),
		})
		resp.Setup = script
	}
	return resp, nil
}

// gcpKeyActivateScript is a key-mode primary grant's setup script: gcloud
// activates the service-account key (step "login") and selects the
// project (step "project"); gcpActivateScript says what it prints.
func gcpKeyActivateScript(keyPath, project string) string {
	return gcpActivateScript("",
		"step login gcloud auth activate-service-account --key-file="+shellQuote(keyPath)+" --quiet",
		"step project gcloud config set project "+shellQuote(project)+" --quiet")
}

// gcpTokenActivateScript is a token-mode primary grant's setup script. It
// points gcloud at the external_account config with
// auth/credential_file_override rather than `gcloud auth login
// --cred-file`: login introspects the access token it is handed against
// sts.googleapis.com, and an iamcredentials-minted service-account token
// (which is what the token endpoint exchanges the subject token for) is
// not an STS-issued one, so STS answers {"active": false} and login exits
// 1 -- failing every token-mode primary grant's setup. The override has
// gcloud read the config for each command and refresh through the token
// endpoint, with no introspection. Setting the override checks nothing,
// so a last step ("token exchange") has gcloud exchange the subject token
// once -- its token goes to /dev/null -- and a gcloud that cannot read an
// external_account config, or a forward that does not reach the token
// endpoint, fails setup with gcloud's reason instead of failing the
// agent's first gcloud command. subjectPath, the per-attempt secret, is
// redacted from everything the script prints.
func gcpTokenActivateScript(keyPath, project, subjectPath string) string {
	return gcpActivateScript(subjectPath,
		"step login gcloud config set auth/credential_file_override "+shellQuote(keyPath)+" --quiet",
		"step project gcloud config set project "+shellQuote(project)+" --quiet",
		"step_quiet 'token exchange' gcloud auth print-access-token --quiet")
}

// gcpActivateScript is the setup script running steps, each a `step NAME
// gcloud ...` line (or `step_quiet`, whose stdout is discarded rather than
// logged: it is a token). It exits 0 when gcloud is absent. Everything
// gcloud prints goes to activate.log beside the script, and nothing to
// the script's own output while every step succeeds -- what setup prints
// would be all the task is told of. A failing step prints, on stderr,
// which step failed, `gcloud --version`, a hint when gcloud looks too old
// for external_account credentials, and the last 40 lines of the log:
// CapabilitySetupError's output tail, so the setup-failure comment says
// why. Every line logged or printed passes through redact first: the
// contents of secretFile (the token-mode subject token, empty for none;
// missing or short secretFile fails closed), access tokens, JWTs, OAuth
// token fields, and private keys (to their END line) become [redacted].
func gcpActivateScript(secretFile string, steps ...string) string {
	return `#!/bin/sh
set -u
command -v gcloud >/dev/null 2>&1 || exit 0
secret_file=` + shellQuote(secretFile) + `
if [ -n "$secret_file" ] && { [ ! -r "$secret_file" ] || [ ! -s "$secret_file" ]; }; then
  printf 'gcp-key setup failed: subject token file %s is missing or empty\n' "$secret_file" >&2
  exit 1
fi
dir=$(dirname "$0")
log="$dir/activate.log"
out=""
trap '[ -n "$out" ] && rm -f "$out"' EXIT HUP INT TERM
umask 077
: >"$log" || exit 1
redact() {
  SECRET_FILE="$secret_file" awk 'BEGIN {
    s = ""
    f = ENVIRON["SECRET_FILE"]
    if (f != "") {
      if ((getline line < f) <= 0) exit 2
      close(f)
      sub(/\r$/, "", line)
      if (length(line) < 8) exit 2
      s = line
    }
  }
  {
    if (k) { if ($0 ~ /END [A-Z0-9 ]*PRIVATE KEY|["\047]/) k = 0; next }
    if ($0 ~ /BEGIN [A-Z0-9 ]*PRIVATE KEY/) { print "[redacted]"; k = ($0 !~ /END [A-Z0-9 ]*PRIVATE KEY/ && $0 !~ /PRIVATE KEY[^"\047]*["\047]/); next }
    if ($0 ~ /["\047]?private_key["\047]?[[:space:]]*[:=]/) { print "[redacted]"; next }
    if (s != "") {
      clean = ""
      rest = $0
      while ((i = index(rest, s)) > 0) {
        clean = clean substr(rest, 1, i - 1) "[redacted]"
        rest = substr(rest, i + length(s))
      }
      $0 = clean rest
    }
    gsub(/ya29\.[A-Za-z0-9._~+\/=-]+/, "[redacted]")
    gsub(/eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9._-]*/, "[redacted]")
    gsub(/[Bb]earer +[A-Za-z0-9._~+\/=-]+/, "Bearer [redacted]")
    gsub(/"(access_token|refresh_token|id_token|client_secret|private_key_data)"[[:space:]]*:[[:space:]]*"[^"]*"/, "\"[redacted]\"")
    print
  }'
}
if ! version=$(gcloud --version 2>&1 | head -n 3 | redact); then
  version="[output omitted: redaction failed]"
fi
printf '%s\n' "$version" >>"$log"
run() {
  name=$1 quiet=$2
  shift 2
  printf '+ %s\n' "$*" >>"$log"
  out=$(mktemp "$dir/.activate.XXXXXX") || exit 1
  if [ "$quiet" = 1 ]; then
    "$@" </dev/null >/dev/null 2>"$out"
  else
    "$@" </dev/null >"$out" 2>&1
  fi
  rc=$?
  if ! said=$(redact <"$out"); then
    said="[output omitted: redaction failed]"
  fi
  rm -f "$out"
  out=""
  [ -n "$said" ] && printf '%s\n' "$said" >>"$log"
  [ "$rc" -eq 0 ] && return 0
  {
    printf 'gcp-key setup failed at step "%s" (exit %s): %s\n' "$name" "$rc" "$*"
    echo "gcloud --version:"
    printf '%s\n' "$version"
    if printf '%s\n' "$said" | grep -qiE 'external_account|credential type|credential_file_override|has no property|unrecognized arguments'; then
      echo "This gcloud may be too old to use an external_account credential config (auth/credential_file_override); install a current Google Cloud CLI in the sandbox image."
    fi
    echo "Last 40 lines of $log:"
    tail -n 40 "$log"
  } >&2
  exit 1
}
step() { n=$1; shift; run "$n" 0 "$@"; }
step_quiet() { n=$1; shift; run "$n" 1 "$@"; }
` + strings.Join(steps, "\n") + "\n"
}

// NewGCP builds the gcp server configured by cfg.Fallback alone, with no
// configure tool: what tests and a fixed deployment use.
func NewGCP(cfg GCPConfig) (*Server, error) {
	if cfg.Fallback == nil {
		return nil, fmt.Errorf("gcp: no permission sets are configured")
	}
	m := newGCPManager(cfg, cfg.Fallback)
	tokens := newGCPTokens(func() *gcpsets.Manager { return m }, cfg.Log)
	methods, err := gcpMethods(cfg, m, "", tokens)
	if err != nil {
		return nil, err
	}
	s, err := newServer("gcp", cfg.Log, methods...)
	if err != nil {
		return nil, err
	}
	if err := tokens.ensureListen(s, cfg, m.Config); err != nil {
		s.Close(context.Background())
		return nil, err
	}
	s.onTick(reaper(s, func() *gcpsets.Manager { return m }))
	s.onTick(geminiReaper(s, func() *gcpsets.Config { return m.Config }, cfg.GeminiMinter, m.Now))
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
			listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(DefaultGCPTokenPort), "the loopback address of the data-plane listener token-mode permission sets' grants fetch access tokens from")
			fake := fs.String("fake-cloud", "", "test only: connect to an in-memory GCP (gcpfake) built from this JSON fixture instead of Google")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			cfg := GCPConfig{Log: logger, Env: env, Listen: listenAddr(env, *listen)}
			if *fake != "" {
				cloud, now, err := loadGCPFake(*fake)
				if err != nil {
					return nil, err
				}
				cfg.Connector, cfg.Now = cloud, now
				if logger != nil {
					logger.Printf("gcp: -fake-cloud %s: every admin connects to an in-memory fake GCP; nothing reaches Google", *fake)
				}
			}
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
	tokens := newGCPTokens(func() *gcpsets.Manager {
		mu.Lock()
		defer mu.Unlock()
		return current
	}, cfg.Log)
	if cfg.Fallback != nil {
		m := newGCPManager(cfg, cfg.Fallback)
		if built, err := gcpMethods(cfg, m, "", tokens); err == nil {
			methods, current = built, m
		}
	}
	s, err := newServer("gcp", cfg.Log, methods...)
	if err != nil {
		return nil, err
	}
	if current != nil {
		if err := tokens.ensureListen(s, cfg, current.Config); err != nil {
			s.Close(context.Background())
			return nil, err
		}
	}
	s.onTick(reaper(s, func() *gcpsets.Manager {
		mu.Lock()
		defer mu.Unlock()
		return current
	}))
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	s.onTick(geminiReaper(s, func() *gcpsets.Config {
		mu.Lock()
		defer mu.Unlock()
		if current == nil {
			return nil
		}
		return current.Config
	}, cfg.GeminiMinter, now))
	err = s.configurable(gcpConfigSchema(), func(ctx context.Context, config map[string]any) ([]*protocol.Method, error) {
		c, err := gcpConfigFrom(cfg.Fallback, cfg.FallbackErr, cfg.Env, config)
		if err != nil {
			return nil, err
		}
		m := newGCPManager(cfg, c)
		if err := tokens.ensureListen(s, cfg, c); err != nil {
			return nil, err
		}
		report := ""
		if len(c.Sets) > 0 {
			report = gcpConfigureReport(ctx, m)
		}
		methods, err := gcpMethods(cfg, m, report, tokens)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		current = m
		mu.Unlock()
		return methods, nil
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
