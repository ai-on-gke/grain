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
	"slices"
	"sort"
	"strings"

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
// something a person does in the UI -- the access-code login, the
// executor bootstrap, the endpoints the mcpserver's own built-in tools
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
	{Scope: "dispatch:read", Description: "Read whether dispatch is paused"},
	{Scope: "dispatch:write", Description: "Pause and resume dispatch, and run a task now"},
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
	{Scope: "connections:read", Description: "Read git connections and model backends"},
	{Scope: "connections:write", Description: "Change git connections and model backends", Dangerous: true},
	{Scope: "capabilities:read", Description: "Read capability servers and policy, and check grants"},
	{Scope: "capabilities:admin", Description: "Change capability servers, policy and any task's or repository's grants", Dangerous: true},
	{Scope: "secrets:read", Description: "List secrets, keys and tokens by name (never their values)"},
	{Scope: "secrets:write", Description: "Set and delete secrets, keys and tokens", Dangerous: true},
	{Scope: "deployment:read", Description: "Read host, sandbox, log, metric and upgrade status"},
	{Scope: "sandboxes:read", Description: "Read the status of persistent sandboxes (implied by deployment:read)"},
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
// for (tasks 399, 420, 421, 422, 425), plus the older capabilities
// that now also grant their preset's routes (see apiAliasCapabilities).
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
	{Name: "self-debug", Description: "What the self-debug capability grants: read tasks, runs and deployment status",
		Scopes: []APIScope{"tasks:read", "deployment:read"}},
	{Name: "self-upgrade", Description: "What the self-upgrade capability granted: read and start upgrades",
		Scopes: []APIScope{"deployment:read", "deployment:upgrade"}},
	{Name: "pipeline-control", Description: "What the pipeline-control capability granted: pause and resume dispatch",
		Scopes: []APIScope{"dispatch:read", "dispatch:write"}},
	{Name: "team-broadcast", Description: "What the team-broadcast capability granted: post a comment to every active task",
		Scopes: []APIScope{"teams:broadcast"}},
	{Name: "memory", Description: "What the memory capability granted: read and write memory and memory documents",
		Scopes: []APIScope{"memory:read", "memory:write"}},
	{Name: "release-notes", Description: "What the release-notes capability granted: read cluster stats and weekly release notes, and publish weekly release notes",
		Scopes: []APIScope{"release-notes:read", "release-notes:write"}},
	{Name: "test-quarantine", Description: "What the test-quarantine capability granted: quarantine flaky or failing tests and file follow-up repair tasks",
		Scopes: []APIScope{"tests:write"}},
	{Name: ReleaseGateReadCapability, Description: "What a release workflow reads and files: its repository's CI runs " +
		"and tasks, the persistent sandboxes' status, and its release-notes task",
		Scopes:    []APIScope{"ci:read", "tasks:read", "sandboxes:read", "tasks:from-template"},
		Templates: []string{"release-notes"}},
}

// apiAliasCapabilities are the older capabilities that are now aliases
// for a preset: a task holding one is granted that preset's routes as
// well as whatever tools it still has under their old names (none, for
// one legacyGrantReplacements retires).
var apiAliasCapabilities = map[string]string{
	"self-debug":        "self-debug",
	"self-upgrade":      "self-upgrade",
	"deployment-health": "deployment:read",
	"pipeline-control":  "pipeline-control",
	"team-broadcast":    "team-broadcast",
	"dispatch-control":  "dispatch-control",
	"memory":            "memory",
	"release-notes":     "release-notes",
	"test-quarantine":   "test-quarantine",
}

// GrainCapabilityServerID is the canonical capability_id of the in-process
// grain server (docs/design/unified-grants.md §1, §4).
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
)

func init() {
	protocol.SetGrantPolicy(GrainCapabilityServerID, GrainSQLMethod, func(args map[string]any) protocol.GrantKind {
		if w, _ := args["write"].(bool); w {
			return protocol.GrantByPerson
		}
		return protocol.GrantCovered
	})
}

// IsGrainJournalGrant reports whether grant names grain#journal (recorded or
// bare) or the retired bare grain-log alias.
func IsGrainJournalGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	if s == "grain-log" {
		return true
	}
	return CapabilityGrantRef(s) == GrainJournalRef
}

// HasGrainJournal reports whether any grant in grants opens grain#journal.
func HasGrainJournal(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainJournalGrant)
}

// IsGrainHealthGrant reports whether grant names grain#health (recorded or
// bare) or the retired bare deployment-health alias.
func IsGrainHealthGrant(grant string) bool {
	s := strings.TrimSpace(grant)
	if s == "deployment-health" {
		return true
	}
	return CapabilityGrantRef(s) == GrainHealthRef
}

// HasGrainHealth reports whether any grant in grants opens grain#health.
func HasGrainHealth(grants []string) bool {
	return slices.ContainsFunc(grants, IsGrainHealthGrant)
}

// ParseGrainSQLGrant reports whether grant names grain#sql (recorded or bare)
// or the retired bare db-read / db-write aliases, and whether it allows writes.
func ParseGrainSQLGrant(grant string) (ok bool, write bool) {
	s := strings.TrimSpace(grant)
	switch s {
	case "db-read":
		return true, false
	case "db-write":
		return true, true
	}
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

// legacyGrantReplacements are the bare capabilities converted to api or
// capability-server grants (docs/design/unified-grants.md §4) whose
// in-process provider is gone: each is written as its replacement wherever a
// grant is recorded (CanonicalLegacyGrant / CanonicalLegacyGrants), and the
// store migration that retired it rewrote the rows already stored. The bare
// name keeps granting what it did so a grant nothing rewrote -- a
// repository's own .grain declaration, an older grain/config -- keeps working.
var legacyGrantReplacements = map[string]string{
	"team-broadcast":   APIGrantPrefix + "team-broadcast",
	"dispatch-control": APIGrantPrefix + "dispatch-control",
	"pipeline-control": APIGrantPrefix + "pipeline-control",
	"memory":           APIGrantPrefix + "memory",
	"release-notes":    APIGrantPrefix + "release-notes",
	"grain-log":        GrainJournalGrant,
	"db-read":          GrainSQLReadGrant,
	"db-write":         GrainSQLWriteGrant,
	"self-upgrade":     APIGrantPrefix + "self-upgrade",
	"test-quarantine":  APIGrantPrefix + "test-quarantine",
	"git-push-any-ref": GitPushAnyRefGrant,
}

// legacyGrantMultiReplacements maps retired bare capabilities that split into
// multiple canonical grants (for example an api scope plus an in-process
// grain server method).
var legacyGrantMultiReplacements = map[string][]string{
	"deployment-health": {APIGrantPrefix + "deployment:read", GrainHealthGrant},
}

// CanonicalLegacyGrant is name, or the single grant a retired 1-to-1
// in-process capability of that name became. Callers recording a capability
// list should use CanonicalLegacyGrants so 1-to-many retirements (such as
// deployment-health) expand to every grant they became.
func CanonicalLegacyGrant(name string) string {
	trimmed := strings.TrimSpace(name)
	if to, ok := legacyGrantReplacements[trimmed]; ok {
		return to
	}
	return name
}

// CanonicalLegacyGrants returns the canonical grant(s) a retired in-process
// capability became, or []string{name} when name is not retired.
func CanonicalLegacyGrants(name string) []string {
	trimmed := strings.TrimSpace(name)
	if many, ok := legacyGrantMultiReplacements[trimmed]; ok {
		return slices.Clone(many)
	}
	return []string{CanonicalLegacyGrant(name)}
}

// APIAliasPreset returns the preset name that a bare capability name aliases,
// or "" if name is not a preset alias capability.
func APIAliasPreset(name string) string {
	return apiAliasCapabilities[name]
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
var sandboxModelOnlyScopes = map[APIScope]bool{
	"dispatch:write":  true,
	"repos:write":     true,
	"sessions:write":  true,
	"teams:broadcast": true,
}

// IsSandboxDeniedAPIScope reports whether scope is refused on the sandbox
// proxy channel (X-Grain-Channel: sandbox-cli): every Dangerous scope, plus
// model-only scopes (dispatch:write, repos:write, sessions:write,
// teams:broadcast, and any deployment:* scope other than deployment:read).
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
func ParseAPIGrant(name string) (g APIGrant, ok bool, err error) {
	rest, isAPI := strings.CutPrefix(strings.TrimSpace(name), APIGrantPrefix)
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

// APIGrantsOf reads every api grant (and preset alias) among names.
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
		if preset, ok := apiAliasCapabilities[name]; ok {
			name = APIGrantPrefix + preset
		}
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
