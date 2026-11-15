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
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// The agent API's scope vocabulary (docs/agent-api.md). pkg/ui holds
// every route to one of these scopes (pkg/ui/route_scopes.go) and serves
// the routes as tools; it lives here so that the proposal gate
// (EscalatedCapabilities) can tell a dangerous api grant from any other
// without importing the server.

// APIScope is one permission scope: "<area>:<level>".
type APIScope string

// APIGrantPrefix begins every agent API grant name.
const APIGrantPrefix = "api:"

// APIScopeInternal marks a route that is grain's own plumbing rather than
// something a person does in the UI -- the access-code login, a grain
// hostd dialing in, the endpoints the mcpserver's own built-in tools
// call. It is in the route table so the table stays total, and it is
// never grantable, so no generated tool ever reaches one.
const APIScopeInternal APIScope = "internal"

// APIScopeInfo describes one scope of the vocabulary.
type APIScopeInfo struct {
	Scope       APIScope `json:"scope"`
	Description string   `json:"description"`
	// Dangerous scopes can only be granted by a person (or an explicit
	// capability policy rule), never by an agent to anything, and every
	// call made under one must carry a reason.
	Dangerous bool `json:"dangerous,omitempty"`
}

// apiScopes is the documented vocabulary, in the order docs/agent-api.md
// lists it. A scope that is not here is refused by the route table's
// own test (TestRouteScopesAreTotal), so a route cannot invent one.
var apiScopes = []APIScopeInfo{
	{Scope: "tasks:read", Description: "Read tasks, their conversations, transcripts and trees"},
	{Scope: "tasks:write", Description: "File, edit, comment on, reorder, defer, retry, stop, close and reopen tasks"},
	{Scope: "tasks:admin", Description: "Approve proposed tasks and recreate sandboxes", Dangerous: true},
	{Scope: "tasks:from-template", Description: "File a task from a repository's own declared template"},
	{Scope: "merge:write", Description: "Open pull requests for tasks and retry their merges"},
	{Scope: "merge:force", Description: "Force-merge a task's pull request past its gate", Dangerous: true},
	{Scope: "ci:read", Description: "Read grain CI runs, their job logs and artifacts, merge batches and base-branch gates"},
	{Scope: "ci:write", Description: "Start, re-run and cancel grain CI runs, base-branch and release runs"},
	{Scope: "tests:write", Description: "Quarantine flaky or failing tests and file their follow-up repair tasks"},
	{Scope: "notifications:write", Description: "Raise and clear operator notifications"},
	{Scope: "teams:broadcast", Description: "Post a comment to every active task of the deployment"},
	{Scope: "dispatch:read", Description: "Read whether dispatch is paused, and what a task's next dispatch carries (its brief and handover)"},
	{Scope: "dispatch:write", Description: "Pause and resume dispatch, run a task now, and compact or rebrief what its next dispatch carries"},
	{Scope: "schedules:read", Description: "Read schedules and what they compose"},
	{Scope: "schedules:write", Description: "Create, edit, delete and run schedules"},
	{Scope: "templates:read", Description: "Read task templates"},
	{Scope: "templates:write", Description: "Create, edit and delete task templates"},
	{Scope: "suites:read", Description: "Read suites and suite runs"},
	{Scope: "suites:write", Description: "Create, edit and delete suites, and start suite runs"},
	{Scope: "releases:read", Description: "Read releases, candidates and qualification"},
	{Scope: "releases:write", Description: "Create releases, cut and promote candidates, approve qualification, request release merges"},
	{Scope: "repos:read", Description: "Read repository settings, branches and declarations"},
	{Scope: "repos:write", Description: "Add and remove repositories, change their settings, create branches"},
	{Scope: "boards:read", Description: "Read boards"},
	{Scope: "boards:write", Description: "Create, edit and delete boards"},
	{Scope: "memory:read", Description: "Read memory and memory documents"},
	{Scope: "memory:write", Description: "Write memory and memory documents"},
	{Scope: "release-notes:read", Description: "Read weekly release notes, cluster stats and release versions"},
	{Scope: "release-notes:write", Description: "Write weekly release notes"},
	{Scope: "sessions:read", Description: "Read standing sessions and live consoles"},
	{Scope: "sessions:write", Description: "Create, edit and restart standing sessions, and type into consoles"},
	{Scope: "caches:read", Description: "Read the CI, package and actions caches"},
	{Scope: "caches:write", Description: "Flush the CI, package and actions caches"},
	{Scope: "settings:read", Description: "Read deployment settings and configuration"},
	{Scope: "settings:write", Description: "Change deployment settings, the database and the state repository", Dangerous: true},
	{Scope: "access:read", Description: "Read the allowed users and groups list"},
	{Scope: "access:write", Description: "Add and remove allowed users and groups", Dangerous: true},
	{Scope: "connections:read", Description: "Read git connections and model backends"},
	{Scope: "connections:write", Description: "Change git connections and model backends", Dangerous: true},
	{Scope: "capabilities:read", Description: "Read capability servers and policy, and check grants"},
	{Scope: "capabilities:detach", Description: "Remove grants from a task (never a standing session's) and narrow a repository's default or CI capabilities, with a reason"},
	{Scope: "capabilities:admin", Description: "Change capability servers, policy and any task's or repository's grants", Dangerous: true},
	{Scope: "secrets:read", Description: "List secrets, keys and tokens by name (never their values)"},
	{Scope: "secrets:write", Description: "Set and delete secrets, keys and tokens", Dangerous: true},
	{Scope: "deployment:read", Description: "Read host, sandbox, log, metric and upgrade status"},
	{Scope: "sandboxes:read", Description: "Read the status of persistent sandboxes (implied by deployment:read)"},
	{Scope: "deployment:src", Description: "Read grain's own source under src:// over GET /api/fs/src, which an external agent token is refused without even when it holds deployment:read"},
	{Scope: "deployment:upgrade", Description: "Upgrade grain and its database, or cancel a scheduled upgrade", Dangerous: true},
	{Scope: "deployment:restart", Description: "Restart grain, act on persistent sandboxes, and run sandbox probes", Dangerous: true},
	{Scope: "deployment:reboot", Description: "Reboot or shut down grain's host", Dangerous: true},
}

// impliedAPIScopes are the narrow scopes a wider one carries with it:
// sandboxes:read was split out of deployment:read so a CI job can be
// given the persistent sandboxes' status without the rest of
// deployment:read, and a grant of deployment:read still reaches it.
var impliedAPIScopes = map[APIScope][]APIScope{
	"deployment:read": {"sandboxes:read"},
}

// APIPreset is a named bundle of scopes.
type APIPreset struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Scopes      []APIScope `json:"scopes"`
	// Templates limits the preset's tasks:from-template to these
	// templates of the repository's own .grain/templates (by stem),
	// where a bare tasks:from-template may file from any of them.
	Templates []string `json:"templates,omitempty"`
}

// apiPresets are the bundles a picker offers first. Each is what one of
// the per-ability capabilities that used to be filed one at a time asked
// for (tasks 399, 420, 421, 422, 425), plus the presets the retired
// in-process capabilities of the same names became.
var apiPresets = []APIPreset{
	{Name: "task-admin", Description: "Edit, reorder, retry and close tasks, open and retry their merges, and re-run CI",
		Scopes: []APIScope{"tasks:read", "tasks:write", "merge:write", "ci:read", "ci:write", "notifications:write"}},
	{Name: "ci-read", Description: "Read grain CI runs, logs and artifacts",
		Scopes: []APIScope{"ci:read"}},
	{Name: "dispatch-control", Description: "Reorder, defer and run tasks now, and pause or resume dispatch",
		Scopes: []APIScope{"tasks:read", "tasks:write", "dispatch:read", "dispatch:write"}},
	{Name: "deployment-ops", Description: "Upgrade, restart and reboot this deployment",
		Scopes: []APIScope{"deployment:read", "deployment:upgrade", "deployment:restart", "deployment:reboot"}},
	{Name: "release-manager", Description: "Cut, qualify and promote release candidates, run schedules, and drive their CI and merges",
		Scopes: []APIScope{"tasks:read", "tasks:write", "merge:write", "ci:read", "ci:write",
			"schedules:read", "schedules:write", "releases:read", "releases:write", "repos:read"}},
	{Name: "self-debug", Description: "Read tasks, runs, metrics and deployment status over the REST API (formerly the self-debug capability, whose source checkout is now grain#source)",
		Scopes: []APIScope{"tasks:read", "deployment:read"}},
	{Name: "self-upgrade", Description: "Read and start upgrades (formerly the self-upgrade capability)",
		Scopes: []APIScope{"deployment:read", "deployment:upgrade"}},
	{Name: "pipeline-control", Description: "Pause and resume dispatch (formerly the pipeline-control capability)",
		Scopes: []APIScope{"dispatch:read", "dispatch:write"}},
	{Name: "team-broadcast", Description: "Post a comment to every active task (formerly the team-broadcast capability)",
		Scopes: []APIScope{"teams:broadcast"}},
	{Name: "memory", Description: "Read and write memory and memory documents (formerly the memory capability)",
		Scopes: []APIScope{"memory:read", "memory:write"}},
	{Name: "release-notes", Description: "Read cluster stats and weekly release notes, and publish weekly release notes (formerly the release-notes capability)",
		Scopes: []APIScope{"release-notes:read", "release-notes:write"}},
	{Name: "test-quarantine", Description: "Quarantine flaky or failing tests and file follow-up repair tasks (formerly the test-quarantine capability)",
		Scopes: []APIScope{"tests:write"}},
	{Name: ReleaseGateReadCapability, Description: "What a release workflow reads and files: its repository's CI runs " +
		"and tasks, the persistent sandboxes' status, and its release-notes task",
		Scopes:    []APIScope{"ci:read", "tasks:read", "sandboxes:read", "tasks:from-template"},
		Templates: []string{"release-notes"}},
	// The two sets an external agent credential is issued with
	// (design/spec/external-cli/external-cli.spec.md#XC-16): neither
	// holds a dangerous scope, so neither needs a person to issue.
	{Name: "external-dev", Description: "What a workstation coding agent needs: file and follow tasks, " +
		"drive their CI and merges, and read repositories, templates and deployment status",
		Scopes: []APIScope{"tasks:read", "tasks:write", "ci:read", "ci:write", "merge:write",
			"repos:read", "templates:read", "deployment:read"}},
	{Name: "external-read", Description: "Read-only observer: read tasks, CI, repositories, templates and deployment status",
		Scopes: []APIScope{"tasks:read", "ci:read", "repos:read", "templates:read", "deployment:read"}},
}

// GrainCapabilityServerID is the canonical capability_id of the in-process
// grain server (design/design/unified-grants.design.md §1, §4).
const GrainCapabilityServerID = "https://grain.dev/capabilities/grain"

// Recorded canonical grants on the in-process grain server.
const (
	GrainJournalRef   = GrainCapabilityServerID + "#journal"
	GrainJournalGrant = GrainJournalRef + "={}"

	GrainHealthRef   = GrainCapabilityServerID + "#health"
	GrainHealthGrant = GrainHealthRef + "={}"

	GrainSQLMethod     = "sql"
	GrainSQLRef        = GrainCapabilityServerID + "#" + GrainSQLMethod
	GrainSQLReadGrant  = GrainSQLRef + `={"write":false}`
	GrainSQLWriteGrant = GrainSQLRef + `={"write":true}`

	// GrainHostExecMethod runs shell commands on grain's own host, each
	// one only after a live reply in the task's chat (formerly
	// self-repair). Dangerous: only a person may grant it.
	GrainHostExecMethod = "host-exec"
	GrainHostExecRef    = GrainCapabilityServerID + "#" + GrainHostExecMethod
	GrainHostExecGrant  = GrainHostExecRef + "={}"

	// GrainSourceMethod lets a run read grain's own baked-in source
	// checkout -- grain-src/ in its sandbox and src:// (src://core/...)
	// through the sandbox CLI -- and, held in its deployment-wide form
	// (admincaps.TeamScoped), every repository's CI and the whole of
	// config:// (formerly the source half of self-debug, whose REST half
	// is the api:self-debug preset).
	GrainSourceMethod = "source"
	GrainSourceRef    = GrainCapabilityServerID + "#" + GrainSourceMethod
	GrainSourceGrant  = GrainSourceRef + "={}"

	// GCPCapabilityServerID is the canonical capability_id of the in-tree gcp
	// server (design/design/unified-grants.design.md §1, §4).
	GCPCapabilityServerID      = "https://grain.dev/capabilities/gcp"
	GCPKeyMethod               = "gcp-key"
	GCPKeyRef                  = GCPCapabilityServerID + "#" + GCPKeyMethod
	GCPKeyDefaultPermissionSet = "sandbox-agent"
	GCPKeySandboxAgentGrant    = GCPKeyRef + `={"permission_set":"` + GCPKeyDefaultPermissionSet + `","primary":true}`

	// GCPGeminiKeyMethod mints a per-attempt Gemini API key on the gcp
	// server, placed as the in-process gemini-key places one.
	GCPGeminiKeyMethod = "gemini-key"
	GCPGeminiKeyRef    = GCPCapabilityServerID + "#" + GCPGeminiKeyMethod
	GCPGeminiKeyGrant  = GCPGeminiKeyRef + "={}"

	// GrainCIMethod runs a repository's own .grain/workflows in grain's
	// sandboxes for the branches a task pushes, and holds the merge
	// queue until they pass (formerly grain-ci). grain grants it from a
	// repository's CI Mode (adjustGrainCIGrant); nobody attaches it by
	// hand.
	GrainCIMethod = "ci"
	GrainCIRef    = GrainCapabilityServerID + "#" + GrainCIMethod
	GrainCIGrant  = GrainCIRef + "={}"

	// GrainCICapability is the retired bare name of GrainCIGrant. Schema
	// migration 96 rewrote the stored rows and an import rewrites an
	// older grain/config (RetiredGrantsBecame); nothing else reads it as
	// the grant.
	GrainCICapability = "grain-ci"

	// ModelProxyCapabilityServerID is the canonical capability_id of the
	// in-process model-proxy server: a per-run token on grain's own proxy
	// to one model vendor, forwarding with the run's team connection
	// (formerly claude-proxy, openai-proxy and gemini-proxy).
	ModelProxyCapabilityServerID = "https://grain.dev/capabilities/model-proxy"
	ModelProxyMethod             = "model-proxy"
	ModelProxyRef                = ModelProxyCapabilityServerID + "#" + ModelProxyMethod

	// ConfigMapCapabilityServerID is the canonical capability_id of the
	// built-in config-map server (and SkillCapabilityServerID its legacy
	// skill alias). BootstrapPlaybooksGrant selects its built-in bootstrap
	// map (formerly bootstrap-playbooks).
	ConfigMapCapabilityServerID = "https://grain.dev/capabilities/config-map"
	SkillCapabilityServerID     = "https://grain.dev/capabilities/skill"
	ConfigMapMethod             = "config-map"
	ConfigMapRef                = ConfigMapCapabilityServerID + "#" + ConfigMapMethod
	BootstrapPlaybooksGrant     = ConfigMapRef + `={"config_maps":["bootstrap"]}`

	// GitHubCapabilityServerID is the canonical capability_id of the
	// in-process github server (design/design/unified-grants.design.md §1, §4),
	// answered by grain itself with the run's team git credential.
	GitHubCapabilityServerID = "https://grain.dev/capabilities/github"

	// GitHubReadIssuesMethod lets a task read GitHub issues and comments on
	// the repositories its run may reach (formerly read-issues).
	GitHubReadIssuesMethod = "read-issues"
	GitHubReadIssuesRef    = GitHubCapabilityServerID + "#" + GitHubReadIssuesMethod
	GitHubReadIssuesGrant  = GitHubReadIssuesRef + "={}"

	// GitHubSandboxRepoMethod provisions a private, single-use GitHub sandbox
	// repository for a run via this deployment's GitHub App and places a token
	// scoped to only that repository (formerly github-sandbox).
	LegacyGitHubSandboxCapability = "github-sandbox"
	GitHubSandboxRepoMethod       = "sandbox-repo"
	GitHubSandboxRepoRef          = GitHubCapabilityServerID + "#" + GitHubSandboxRepoMethod
	GitHubSandboxRepoGrant        = GitHubSandboxRepoRef + "={}"
)

// ModelProxyVendors are the vendors the model-proxy server proxies, in
// pkg/modelproxy's Vendors order (which pkg/capability/modelproxy's tests
// hold this to). Each one's retired bare grant is "<vendor>-proxy".
var ModelProxyVendors = []string{"claude", "openai", "gemini"}

// ModelProxyGrant is the recorded model-proxy grant for vendor.
func ModelProxyGrant(vendor string) string {
	return ModelProxyRef + `={"vendor":` + strconv.Quote(vendor) + `}`
}

// LegacyModelProxyCapability is the retired bare capability name of
// vendor's model-proxy grant, such as claude-proxy.
func LegacyModelProxyCapability(vendor string) string { return vendor + "-proxy" }

// GCPKeyServerGrant returns the recorded gcp#gcp-key grant for the named
// permission set (with primary: true, matching protocol.Normalize).
func GCPKeyServerGrant(permissionSet string) string {
	set := strings.TrimSpace(permissionSet)
	if set == "" || set == "default" {
		set = GCPKeyDefaultPermissionSet
	}
	s, err := EncodeCapabilityGrant(protocol.GrantSpec{
		Capability: GCPKeyRef,
		With:       map[string]any{"permission_set": set, "primary": true},
	})
	if err != nil {
		panic(fmt.Sprintf("model: encoding gcp#gcp-key grant for %q: %v", set, err))
	}
	return s
}

func validLegacyGCPKeyParam(name string) bool {
	return name != "" && !strings.ContainsAny(name, " \t\n\r/\\:*?[")
}

// ParseGCPKeyServerGrant reports whether grant names gcp#gcp-key (or a
// retired gcp-key / gcp-key:<name> alias) and returns its permission set.
func ParseGCPKeyServerGrant(grant string) (permissionSet string, ok bool) {
	s := strings.TrimSpace(grant)
	if keyName, isKey := GCPKeyName(s); isKey {
		if keyName == "" || keyName == "default" {
			return GCPKeyDefaultPermissionSet, true
		}
		if validLegacyGCPKeyParam(keyName) {
			return keyName, true
		}
		return "", false
	}
	if !IsCapabilityRef(s) {
		return "", false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || spec.Capability != GCPKeyRef {
		return "", false
	}
	if err := CheckGCPKeyGrantArgs(spec, ""); err != nil {
		return "", false
	}
	set, _ := spec.With["permission_set"].(string)
	if set == "" {
		set = GCPKeyDefaultPermissionSet
	}
	return set, true
}

// GCPKeySetUnnamed reports whether grant is a gcp#gcp-key capability-server
// grant that leaves its permission set to the server's first configured set
// (no permission_set). (Bare "gcp-key" is false: it is a retired name, not a
// grant.)
func GCPKeySetUnnamed(grant string) bool {
	s := strings.TrimSpace(grant)
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || spec.Capability != GCPKeyRef {
		return false
	}
	if err := CheckGCPKeyGrantArgs(spec, ""); err != nil {
		return false
	}
	set, _ := spec.With["permission_set"].(string)
	return set == ""
}

func init() {
	protocol.SetGrantPolicy(GrainCapabilityServerID, GrainSQLMethod, func(args map[string]any) protocol.GrantKind {
		if w, _ := args["write"].(bool); w {
			return protocol.GrantByPerson
		}
		return protocol.GrantCovered
	})
	protocol.RequirePersonGrant(GrainCapabilityServerID, GrainHostExecMethod)
}

// IsGrainJournalGrant reports whether grant names grain#journal (recorded or
// bare).
func IsGrainJournalGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GrainJournalRef
}

// HasGrainJournal reports whether any grant in grants opens grain#journal.
func HasGrainJournal(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainJournalGrant)
}

// IsGrainSourceGrant reports whether grant names grain#source (recorded or
// bare).
func IsGrainSourceGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GrainSourceRef
}

// HasGrainSource reports whether any grant in grants opens grain#source.
func HasGrainSource(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainSourceGrant)
}

// SelfDebugGrants is what the retired self-debug capability became, in
// the order a grant list records them: the api:self-debug preset for the
// REST half (every task's record, metrics, pipeline and deployment
// status) and grain#source for grain's own source checkout.
func SelfDebugGrants() []string {
	return []string{APIRef("self-debug"), GrainSourceGrant}
}

// IsGrainHealthGrant reports whether grant names grain#health (recorded or
// bare).
func IsGrainHealthGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GrainHealthRef
}

// HasGrainHealth reports whether any grant in grants opens grain#health.
func HasGrainHealth(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainHealthGrant)
}

// IsGrainHostExecGrant reports whether grant names grain#host-exec
// (recorded or bare).
func IsGrainHostExecGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GrainHostExecRef
}

// HasGrainHostExec reports whether any grant in grants opens grain#host-exec.
func HasGrainHostExec(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainHostExecGrant)
}

// IsGrainCIGrant reports whether grant names grain#ci (recorded or bare).
func IsGrainCIGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GrainCIRef
}

// HasGrainCI reports whether any grant in grants opens grain#ci.
func HasGrainCI(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainCIGrant)
}

// IsGitHubReadIssuesGrant reports whether grant names github#read-issues
// (recorded or bare).
func IsGitHubReadIssuesGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GitHubReadIssuesRef
}

// HasGitHubReadIssues reports whether any grant in grants opens
// github#read-issues.
func HasGitHubReadIssues(grants []string) bool {
	return slices.ContainsFunc(grants, IsGitHubReadIssuesGrant)
}

// IsGitHubSandboxRepoGrant reports whether grant names github#sandbox-repo
// (recorded or bare).
func IsGitHubSandboxRepoGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	return CapabilityGrantRef(s) == GitHubSandboxRepoRef
}

// HasGitHubSandboxRepo reports whether any grant in grants opens
// github#sandbox-repo.
func HasGitHubSandboxRepo(grants []string) bool {
	return slices.ContainsFunc(grants, IsGitHubSandboxRepoGrant)
}

// ParseGrainSQLGrant reports whether grant names grain#sql (recorded or bare),
// and whether it allows writes.
func ParseGrainSQLGrant(grant string) (ok bool, write bool) {
	s := strings.TrimSpace(grant)
	if !IsCapabilityRef(s) {
		return false, false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || spec.Capability != GrainSQLRef {
		return false, false
	}
	w, _ := spec.With["write"].(bool)
	return true, w
}

// IsGrainSQLGrant reports whether grant names grain#sql (read or write) or a
// retired db-read / db-write alias.
func IsGrainSQLGrant(grant string) bool {
	ok, _ := ParseGrainSQLGrant(grant)
	return ok
}

// IsGrainSQLWriteGrant reports whether grant names grain#sql with write: true
// or the retired db-write alias.
func IsGrainSQLWriteGrant(grant string) bool {
	ok, write := ParseGrainSQLGrant(grant)
	return ok && write
}

// HasGrainSQL reports whether any grant in grants opens grain#sql (read or write).
func HasGrainSQL(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainSQLGrant)
}

// HasGrainSQLWrite reports whether any grant in grants opens grain#sql with write: true.
func HasGrainSQLWrite(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainSQLWriteGrant)
}

func stringSliceField(with map[string]any, key string) ([]string, bool) {
	raw, ok := with[key]
	if !ok {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		s, ok := v.(string)
		if !ok {
			return nil, true
		}
		out = append(out, s)
	}
	return out, true
}

func configMapSelectsBootstrap(with map[string]any) bool {
	cms, hasCM := stringSliceField(with, "config_maps")
	_, hasSec := with["secrets"]
	keys, hasKeys := stringSliceField(with, "keys")
	hasBootstrapKey := false
	for _, k := range keys {
		if strings.HasPrefix(k, "bootstrap/") {
			hasBootstrapKey = true
			break
		}
	}
	if hasCM || hasSec {
		return slices.Contains(cms, "bootstrap") && (!hasKeys || hasBootstrapKey)
	}
	return hasKeys && hasBootstrapKey
}

// IsBootstrapPlaybooksGrant reports whether grant names the bootstrap playbooks
// via config-map#config-map (or its legacy skill alias, including methodless
// shorthand before server normalization).
func IsBootstrapPlaybooksGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	if len(s) >= len("https://") && strings.EqualFold(s[:len("https://")], "https://") {
		s = "https://" + s[len("https://"):]
	}
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(spec.Capability) {
	case ConfigMapRef, ConfigMapCapabilityServerID, SkillCapabilityServerID + "#" + ConfigMapMethod:
		return configMapSelectsBootstrap(spec.With)
	case ConfigMapCapabilityServerID + "#skill", SkillCapabilityServerID + "#skill":
		skills, ok := stringSliceField(spec.With, "skills")
		return ok && slices.Contains(skills, "bootstrap")
	case SkillCapabilityServerID:
		if _, hasSkills := spec.With["skills"]; hasSkills {
			skills, ok := stringSliceField(spec.With, "skills")
			return ok && slices.Contains(skills, "bootstrap")
		}
		return configMapSelectsBootstrap(spec.With)
	}
	return false
}

// BootstrapSkillSetUnnamed reports whether grant is an unnormalized #skill (or
// methodless skill) capability-server grant that omits "skills", leaving its
// selection to the server's default ("all", which includes "bootstrap" whenever
// the server serves the bootstrap playbooks). Once normalized, "skills" is
// explicit so unrelated skill directories are no longer flagged.
func BootstrapSkillSetUnnamed(grant string) bool {
	s := strings.TrimSpace(grant)
	if len(s) >= len("https://") && strings.EqualFold(s[:len("https://")], "https://") {
		s = "https://" + s[len("https://"):]
	}
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(spec.Capability) {
	case ConfigMapCapabilityServerID + "#skill", SkillCapabilityServerID + "#skill":
		_, hasSkills := spec.With["skills"]
		return !hasSkills
	case SkillCapabilityServerID:
		for _, k := range []string{"skills", "config_maps", "secrets", "keys"} {
			if _, ok := spec.With[k]; ok {
				return false
			}
		}
		return true
	}
	return false
}

// IsEmbeddedBootstrapOnlyGrant reports whether grant is a config-map or skill
// capability reference whose arguments select exclusively the stateless
// embedded bootstrap playbooks (no operator config maps and no secrets).
func IsEmbeddedBootstrapOnlyGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return false
	}
	switch spec.Capability {
	case ConfigMapRef, SkillCapabilityServerID + "#" + ConfigMapMethod:
		// Anything but a well-formed config_maps/keys/secrets string list
		// -- a field the classifier does not know, or a list holding a
		// non-string the mint would skip past to the strings beside it --
		// is not provably bootstrap-only.
		for field := range spec.With {
			switch field {
			case "config_maps", "keys", "secrets":
			default:
				return false
			}
		}
		secs, hasSecs := stringSliceField(spec.With, "secrets")
		if hasSecs && (secs == nil || len(secs) > 0) {
			return false
		}
		cms, hasCM := stringSliceField(spec.With, "config_maps")
		keys, hasKeys := stringSliceField(spec.With, "keys")
		if !hasCM && !hasKeys {
			return false
		}
		if (hasCM && cms == nil) || (hasKeys && keys == nil) {
			return false
		}
		for _, cm := range cms {
			if cm != "bootstrap" {
				return false
			}
		}
		for _, k := range keys {
			if !strings.HasPrefix(k, "bootstrap/") {
				return false
			}
		}
		return (hasCM && len(cms) > 0) || (hasKeys && len(keys) > 0)
	case ConfigMapCapabilityServerID + "#skill", SkillCapabilityServerID + "#skill":
		for field := range spec.With {
			if field != "skills" {
				return false
			}
		}
		skills, hasSkills := stringSliceField(spec.With, "skills")
		if !hasSkills || len(skills) == 0 {
			return false
		}
		for _, sk := range skills {
			if sk != "bootstrap" {
				return false
			}
		}
		return true
	}
	return false
}

// HasBootstrapPlaybooks reports whether any grant in grants opens the bootstrap
// playbooks.
func HasBootstrapPlaybooks(grants []string) bool {
	return slices.ContainsFunc(grants, IsBootstrapPlaybooksGrant)
}

// retiredGrantReplacements are the bare capabilities converted to api or
// capability-server grants (design/design/unified-grants.design.md §4) whose
// in-process provider is gone. The store migration that retired each
// rewrote the rows already stored; this is the migration-only copy the
// paths that bring in grants written before then read -- a grain/config
// export being imported (RetiredGrantsBecame), a settings/capabilities.yml
// still naming the old capability (retiredInProcessNames). A bare name
// arriving anywhere else is no longer an alias for anything: it was one
// for a release, and every promoted release since v3.3.0 has carried
// each conversion.
var retiredGrantReplacements = map[string]string{
	"team-broadcast":   APIRef("team-broadcast"),
	"dispatch-control": APIRef("dispatch-control"),
	"pipeline-control": APIRef("pipeline-control"),
	"memory":           APIRef("memory"),
	"release-notes":    APIRef("release-notes"),
	"grain-log":        GrainJournalGrant,
	"db-read":          GrainSQLReadGrant,
	"db-write":         GrainSQLWriteGrant,
	"self-upgrade":     APIRef("self-upgrade"),
	"test-quarantine":  APIRef("test-quarantine"),
	"self-repair":      GrainHostExecGrant,
	"git-push-any-ref": GitPushAnyRefGrant,
	"gcp-key":          GCPKeySandboxAgentGrant,
	GrainCICapability:  GrainCIGrant,
	// Retired by schema migration 100 onto the in-process model-proxy
	// server, which forwards with the same team connection.
	"claude-proxy": ModelProxyGrant("claude"),
	"openai-proxy": ModelProxyGrant("openai"),
	"gemini-proxy": ModelProxyGrant("gemini"),
	// Retired by schema migration 101 onto the in-process release
	// server, which places what the providers placed on the same events.
	"image-signing":          ImageSigningRef + "={}",
	"registry-push":          RegistryPushRef + "={}",
	CapabilityPackagePublish: PackagePublishRef + "={}",
	// Retired by schema migration 103 onto the gcp server's gemini-key,
	// which mints with the same gcp-key-minter secret.
	"gemini-key": GCPGeminiKeyGrant,
	// Retired by schema migration 118 onto the built-in config-map server's
	// bootstrap map.
	"bootstrap-playbooks": BootstrapPlaybooksGrant,
	// Retired by schema migration 121 onto the in-process github server's
	// read-issues method, which reads with the same team git credential.
	"read-issues": GitHubReadIssuesGrant,
	// Retired by schema migration 124 onto the in-process github server's
	// sandbox-repo method, which provisions the same private sandbox repo
	// and scoped installation token via this deployment's GitHub App.
	LegacyGitHubSandboxCapability: GitHubSandboxRepoGrant,
}

// retiredGrantMultiReplacements are the retired bare capabilities that
// split into several canonical grants (for example an api scope plus an
// in-process grain server method): retiredGrantReplacements' migration-only
// copy for them.
var retiredGrantMultiReplacements = map[string][]string{
	"deployment-health": {APIRef("deployment:read"), GrainHealthGrant},
	// Retired by schema migration 115: the REST half of self-debug is its
	// api preset, the source checkout the grain server's #source method.
	SelfDebugCapability: SelfDebugGrants(),
}

var (
	registeredLegacyAliasesMu sync.RWMutex
	registeredLegacyAliases   = map[string]string{}
)

// RegisterLegacyGrantAlias makes the bare capability name from an alias
// for the grant to until the returned func is called: CanonicalLegacyGrant
// rewrites it wherever a grant is recorded. It is the seam a build outside
// core retires one of its own in-process capabilities through
// (capability.Register's Aliases), so core need not name it. It panics on
// an empty name or one already an alias, or one core retired.
func RegisterLegacyGrantAlias(from, to string) (unregister func()) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		panic(fmt.Sprintf("model: RegisterLegacyGrantAlias(%q, %q): an alias needs both names", from, to))
	}
	registeredLegacyAliasesMu.Lock()
	defer registeredLegacyAliasesMu.Unlock()
	_, core := retiredGrantReplacements[from]
	_, coreMulti := retiredGrantMultiReplacements[from]
	if _, ok := registeredLegacyAliases[from]; ok || core || coreMulti {
		panic(fmt.Sprintf("model: RegisterLegacyGrantAlias(%q): already an alias", from))
	}
	registeredLegacyAliases[from] = to
	return func() {
		registeredLegacyAliasesMu.Lock()
		defer registeredLegacyAliasesMu.Unlock()
		delete(registeredLegacyAliases, from)
	}
}

// registeredLegacyAlias is what a registered alias rewrites name to.
func registeredLegacyAlias(name string) (string, bool) {
	registeredLegacyAliasesMu.RLock()
	defer registeredLegacyAliasesMu.RUnlock()
	to, ok := registeredLegacyAliases[name]
	return to, ok
}

// retiredGrantReplacement is what the retired bare name became, from
// core's migration-only table or a registered alias.
func retiredGrantReplacement(name string) (string, bool) {
	if to, ok := retiredGrantReplacements[name]; ok {
		return to, true
	}
	if keyName, ok := GCPKeyName(name); ok {
		if keyName == "" || keyName == "default" {
			return GCPKeySandboxAgentGrant, true
		}
		if validLegacyGCPKeyParam(keyName) {
			return GCPKeyServerGrant(keyName), true
		}
	}
	if credName, ok := LegacyGitCredentialName(name); ok && ValidGitCredentialName(credName) {
		return RepoCredentialGrant(credName), true
	}
	return registeredLegacyAlias(name)
}

// allRetiredGrantReplacements is retiredGrantReplacements with every
// registered alias added.
func allRetiredGrantReplacements() map[string]string {
	out := maps.Clone(retiredGrantReplacements)
	registeredLegacyAliasesMu.RLock()
	defer registeredLegacyAliasesMu.RUnlock()
	maps.Copy(out, registeredLegacyAliases)
	return out
}

// CanonicalLegacyGrant is name, or the grant a registered alias
// (RegisterLegacyGrantAlias) rewrites it to. Core's own retired names are
// not aliases any longer: they are what they say, a capability nothing
// provides.
func CanonicalLegacyGrant(name string) string {
	if to, ok := registeredLegacyAlias(strings.TrimSpace(name)); ok {
		return to
	}
	return name
}

// CanonicalLegacyGrants is CanonicalLegacyGrant as a list, for callers
// recording a capability list.
func CanonicalLegacyGrants(name string) []string {
	return []string{CanonicalLegacyGrant(name)}
}

// RetiredGrantsBecame returns the canonical grant(s) a retired in-process
// capability became -- core's migration-only table, a registered alias,
// or a runtime expansion (SetRuntimeGrantExpansions) -- or
// []string{name} when name is not retired. It is for the paths that
// upgrade grants written before a retirement, such as a grain/config
// export being imported; a grant being recorded uses
// CanonicalLegacyGrants.
func RetiredGrantsBecame(name string) []string {
	trimmed := strings.TrimSpace(name)
	if many, ok := retiredGrantMultiReplacements[trimmed]; ok {
		return slices.Clone(many)
	}
	if many, ok := runtimeExpansion(trimmed); ok {
		return many
	}
	if to, ok := retiredGrantReplacement(trimmed); ok {
		return []string{to}
	}
	return []string{name}
}

// RetiredGrantNames is every retired bare capability name
// RetiredGrantsBecame rewrites, in no particular order.
func RetiredGrantNames() []string {
	all := allRetiredGrantReplacements()
	names := make([]string, 0, len(all)+len(retiredGrantMultiReplacements))
	for name := range all {
		names = append(names, name)
	}
	for name := range retiredGrantMultiReplacements {
		names = append(names, name)
	}
	return append(names, runtimeExpansionNames()...)
}

// APIScopes is the vocabulary, in its documented order.
func APIScopes() []APIScopeInfo { return slices.Clone(apiScopes) }

// APIPresets is every preset, in the order a picker offers them.
func APIPresets() []APIPreset {
	out := make([]APIPreset, len(apiPresets))
	for i, p := range apiPresets {
		out[i] = p
		out[i].Scopes = slices.Clone(p.Scopes)
		out[i].Templates = slices.Clone(p.Templates)
	}
	return out
}

func APIScopeInfoOf(scope APIScope) (APIScopeInfo, bool) {
	for _, s := range apiScopes {
		if s.Scope == scope {
			return s, true
		}
	}
	return APIScopeInfo{}, false
}

func APIPresetNamed(name string) (APIPreset, bool) {
	for _, p := range apiPresets {
		if p.Name == name {
			return p, true
		}
	}
	return APIPreset{}, false
}

// IsDangerousAPIScope reports whether scope is one only a person may
// grant.
func IsDangerousAPIScope(scope APIScope) bool {
	info, ok := APIScopeInfoOf(scope)
	return ok && info.Dangerous
}

// sandboxModelOnlyScopes are non-dangerous scopes that remain model-only
// (never reachable via the sandbox proxy credential / X-Grain-Channel: sandbox-cli).
//
// capabilities:detach is model-only so that removing a grant is always a
// reasoned MCP tool call the transcript records, never something a script
// in the sandbox does (director-capabilities.spec.md §3.1.2).
var sandboxModelOnlyScopes = map[APIScope]bool{
	"capabilities:detach": true,
	"dispatch:write":      true,
	"repos:write":         true,
	"sessions:write":      true,
	"teams:broadcast":     true,
}

// IsSandboxDeniedAPIScope reports whether scope is refused on the sandbox
// proxy channel (X-Grain-Channel: sandbox-cli): every Dangerous scope, plus
// model-only scopes (capabilities:detach, dispatch:write, repos:write,
// sessions:write, teams:broadcast, and any deployment:* scope other than
// deployment:read).
func IsSandboxDeniedAPIScope(scope APIScope) bool {
	if scope == "" || scope == APIScopeInternal {
		return true
	}
	if IsDangerousAPIScope(scope) || sandboxModelOnlyScopes[scope] {
		return true
	}
	if strings.HasPrefix(string(scope), "deployment:") && scope != "deployment:read" {
		return true
	}
	return false
}

// ExpandAPIPreset is a preset's scopes, or a scope on its own, or
// nothing for a name that is neither.
func ExpandAPIPreset(name string) []APIScope {
	if p, ok := APIPresetNamed(name); ok {
		return slices.Clone(p.Scopes)
	}
	if set, ok := APIScopeSetNamed(name); ok {
		return slices.Clone(set.Resolved)
	}
	if _, ok := APIScopeInfoOf(APIScope(name)); ok {
		return []APIScope{APIScope(name)}
	}
	return nil
}

// APIGrant is one api grant name, read: the scopes it grants and the
// repository it is limited to ("" for all of them).
type APIGrant struct {
	Name   string
	Preset string // the preset or scope set it names, or "" for a single scope
	Scopes []APIScope
	Repo   string
	// ScopeRepos is, for a scope set limiting some of its scopes to
	// some repositories (APIScopeSet.Repos), which.
	ScopeRepos map[APIScope][]string
	// RequiresPerson is a scope set's own requires-person flag
	// (ResolvedAPIScopeSet.Person).
	RequiresPerson bool
	// Templates is a preset's limit on tasks:from-template
	// (APIPreset.Templates); empty is every template of the repository.
	Templates []string
}

// ReposOf is the repositories g grants scope on: [""] for every one, nil
// for none -- which is a set limited to repositories the grant's own
// @repo is not among.
func (g APIGrant) ReposOf(scope APIScope) []string {
	lim, limited := g.ScopeRepos[scope]
	switch {
	case !limited && g.Repo == "":
		return []string{""}
	case !limited:
		return []string{g.Repo}
	case g.Repo == "":
		return lim
	case containsFold(lim, g.Repo):
		return []string{g.Repo}
	}
	return nil
}

// OutsideRepoLimit is why g grants nothing at all: it names a scope set
// limited to some repositories, on an @repo none of the set's scopes
// reach. Nil for every other grant, including one some of whose scopes
// reach g.Repo and some do not. Refused when a grant is recorded rather
// than left to grant nothing silently; a grant already recorded keeps
// parsing, so a set narrowed later only stops granting.
func (g APIGrant) OutsideRepoLimit() error {
	if g.Repo == "" || len(g.ScopeRepos) == 0 {
		return nil
	}
	var reach []string
	for _, scope := range g.Scopes {
		if len(g.ReposOf(scope)) > 0 {
			return nil
		}
		for _, r := range g.ScopeRepos[scope] {
			if !containsFold(reach, r) {
				reach = append(reach, r)
			}
		}
	}
	sort.Strings(reach)
	return fmt.Errorf("api grant %q grants nothing: scope set %s reaches only %s, not %s",
		g.Name, g.Preset, strings.Join(reach, ", "), g.Repo)
}

// ParseAPIGrant reads an api grant name. ok is false for a name that is
// not an api grant at all; err is set for one that is, but names no
// scope or preset of the vocabulary, or a repository that does not
// parse.
//
// An api reference (https://grain.dev/capabilities/api#<what>, with an
// optional {repo}) reads as the api:<what>[@repo] it stands for
// (CanonicalGrant); g.Name is name as given.
func ParseAPIGrant(name string) (g APIGrant, ok bool, err error) {
	spelled := strings.TrimSpace(name)
	if _, isRef := apiMethodOf(CapabilityGrantRef(spelled)); isRef {
		legacy, legal := LegacyGrantString(CanonicalGrant(spelled))
		if !legal {
			return APIGrant{}, true, fmt.Errorf("api grant %q: takes no argument but repo", name)
		}
		spelled = legacy
	}
	rest, isAPI := strings.CutPrefix(spelled, APIGrantPrefix)
	if !isAPI {
		return APIGrant{}, false, nil
	}
	g.Name = name
	what, repo, limited := strings.Cut(rest, "@")
	if limited {
		ref, perr := ParseRepo(repo)
		if perr != nil {
			return APIGrant{}, true, fmt.Errorf("api grant %q: %v", name, perr)
		}
		g.Repo = ref.String()
	}
	if p, isPreset := APIPresetNamed(what); isPreset {
		g.Preset = p.Name
		g.Scopes = slices.Clone(p.Scopes)
		g.Templates = slices.Clone(p.Templates)
		return g, true, nil
	}
	if set, isSet := APIScopeSetNamed(what); isSet {
		g.Preset = set.Name
		g.Scopes = slices.Clone(set.Resolved)
		g.RequiresPerson = set.Person
		if len(set.RepoLimits) > 0 {
			g.ScopeRepos = map[APIScope][]string{}
			for sc, lim := range set.RepoLimits {
				g.ScopeRepos[sc] = slices.Clone(lim)
			}
		}
		return g, true, nil
	}
	if what == string(APIScopeInternal) {
		return APIGrant{}, true, fmt.Errorf("api grant %q: %q is grain's own plumbing and is never granted", name, what)
	}
	if _, isScope := APIScopeInfoOf(APIScope(what)); !isScope {
		return APIGrant{}, true, fmt.Errorf("api grant %q: %q is neither a scope, a preset nor a scope set of docs/agent-api.md", name, what)
	}
	g.Scopes = []APIScope{APIScope(what)}
	return g, true, nil
}

// Dangerous reports whether g grants any dangerous scope, or names a
// scope set only a person may grant.
func (g APIGrant) Dangerous() bool {
	return g.RequiresPerson || slices.ContainsFunc(g.Scopes, IsDangerousAPIScope)
}

// APIGrantSet is everything a task's grants let it call: for each scope,
// the repositories it reaches, nil meaning all of them.
type APIGrantSet struct {
	scopes map[APIScope][]string
	all    map[APIScope]bool
}

// APIGrantsOf reads every api grant among names.
// Names that are not api grants are ignored; an api grant that does not
// parse grants nothing.
func APIGrantsOf(names []string) APIGrantSet {
	return apiGrantsOf(names, false)
}

// SandboxAPIGrantsOf is APIGrantsOf for requests arriving on the sandbox
// proxy channel (X-Grain-Channel: sandbox-cli): scope sets with
// RequiresPerson and scopes refused on the sandbox channel are excluded.
func SandboxAPIGrantsOf(names []string) APIGrantSet {
	return apiGrantsOf(names, true)
}

func apiGrantsOf(names []string, sandboxChannel bool) APIGrantSet {
	set := APIGrantSet{scopes: map[APIScope][]string{}, all: map[APIScope]bool{}}
	for _, name := range names {
		g, ok, err := ParseAPIGrant(name)
		if !ok || err != nil {
			continue
		}
		if sandboxChannel && g.RequiresPerson {
			continue
		}
		for _, scope := range g.Scopes {
			for _, repo := range g.ReposOf(scope) {
				for _, s := range append([]APIScope{scope}, impliedAPIScopes[scope]...) {
					if sandboxChannel && IsSandboxDeniedAPIScope(s) {
						continue
					}
					if repo == "" {
						set.all[s] = true
						continue
					}
					if !slices.Contains(set.scopes[s], repo) {
						set.scopes[s] = append(set.scopes[s], repo)
					}
				}
			}
		}
	}
	return set
}

// Empty reports whether the set grants nothing.
func (s APIGrantSet) Empty() bool { return len(s.all) == 0 && len(s.scopes) == 0 }

// Intersect returns the grant set holding only scopes and repositories that
// both s and other allow (AU-25.1).
func (s APIGrantSet) Intersect(other APIGrantSet) APIGrantSet {
	out := APIGrantSet{scopes: map[APIScope][]string{}, all: map[APIScope]bool{}}
	for _, info := range APIScopes() {
		sc := info.Scope
		if !s.Has(sc) || !other.Has(sc) {
			continue
		}
		if s.Allows(sc, "") && other.Allows(sc, "") {
			out.all[sc] = true
			continue
		}
		if s.Allows(sc, "") {
			out.scopes[sc] = other.Repos(sc)
			continue
		}
		if other.Allows(sc, "") {
			out.scopes[sc] = s.Repos(sc)
			continue
		}
		var common []string
		for _, r := range s.Repos(sc) {
			if other.Allows(sc, r) {
				common = append(common, r)
			}
		}
		if len(common) > 0 {
			out.scopes[sc] = common
		}
	}
	return out
}

// Has reports whether the set grants scope for at least one repository.
func (s APIGrantSet) Has(scope APIScope) bool {
	return s.all[scope] || len(s.scopes[scope]) > 0
}

// Allows reports whether the set grants scope on repo. repo "" is a call
// whose repository is not known (or that has none), which only a grant
// for every repository allows.
func (s APIGrantSet) Allows(scope APIScope, repo string) bool {
	if scope == APIScopeInternal || scope == "" {
		return false
	}
	if s.all[scope] {
		return true
	}
	if repo == "" {
		return false
	}
	return slices.ContainsFunc(s.scopes[scope], func(r string) bool { return SameRepoText(r, repo) })
}

// Repos is the repositories the set grants scope on, sorted, when it
// grants it on named repositories only (nil when it grants it on every
// repository, or on none).
func (s APIGrantSet) Repos(scope APIScope) []string {
	if s.all[scope] || len(s.scopes[scope]) == 0 {
		return nil
	}
	out := slices.Clone(s.scopes[scope])
	slices.Sort(out)
	return out
}

// Scopes is every scope the set grants anywhere, sorted.
func (s APIGrantSet) Scopes() []APIScope {
	var out []APIScope
	for scope := range s.all {
		out = append(out, scope)
	}
	for scope := range s.scopes {
		if !s.all[scope] {
			out = append(out, scope)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Without returns s with every scope in deny removed.
func (s APIGrantSet) Without(deny map[APIScope]bool) APIGrantSet {
	if len(deny) == 0 {
		return s
	}
	out := APIGrantSet{scopes: map[APIScope][]string{}, all: map[APIScope]bool{}}
	for scope := range s.all {
		if !deny[scope] {
			out.all[scope] = true
		}
	}
	for scope, repos := range s.scopes {
		if !deny[scope] {
			out.scopes[scope] = slices.Clone(repos)
		}
	}
	return out
}

// IsDangerousAPIGrant reports whether a grant name is an api grant of a
// dangerous scope (or a preset holding one): what only a person may
// grant, so that even a proposer holding it cannot pass it on.
func IsDangerousAPIGrant(name string) bool {
	g, ok, err := ParseAPIGrant(name)
	return ok && err == nil && g.Dangerous()
}

// ciAPIScopes are the scopes a CI job's token may hold
// (pkg/capability/ciapi). A job's workflow file is a branch's to
// rewrite, and what its token reads is the deployment's, so the list is
// the reads whose answers can be held to the job's own repository, plus
// tasks:from-template, the one write whose repository is the job's by
// construction. Every dangerous scope is refused, and so is every other
// write and every deployment-wide read (settings, secrets, connections,
// capabilities, sessions, memory, deployment status): those cannot be
// narrowed to one repository at all.
var ciAPIScopes = map[APIScope]bool{
	"ci:read":             true,
	"tasks:read":          true,
	"tasks:from-template": true,
	"sandboxes:read":      true,
	"templates:read":      true,
	"suites:read":         true,
	"releases:read":       true,
	"repos:read":          true,
	"schedules:read":      true,
	"dispatch:read":       true,
}

// CIAPIScopeRefusal says why a CI job may not hold scope, or "" if it
// may.
func CIAPIScopeRefusal(scope APIScope) string {
	switch {
	case ciAPIScopes[scope]:
		return ""
	case IsDangerousAPIScope(scope):
		return fmt.Sprintf("%s is a dangerous scope, which no CI job is ever given", scope)
	}
	return fmt.Sprintf("%s is not offered to CI jobs: their tokens hold only scopes whose answers can be "+
		"narrowed to the job's own repository (%s)", scope, strings.Join(CIAPIScopes(), ", "))
}

// CIAPIScopes is every scope a CI job's token may hold, sorted.
func CIAPIScopes() []string {
	out := make([]string, 0, len(ciAPIScopes))
	for s := range ciAPIScopes {
		out = append(out, string(s))
	}
	sort.Strings(out)
	return out
}
