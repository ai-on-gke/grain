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

// The capability catalogue (docs/design/config-repo-settings.md §3): one
// declarative account of every capability a run may hold and who may
// hold it, written in grain/config as settings/capabilities.yml
// (pkg/staterepo/capabilities_file.go) and kept at runtime inside
// grain_config.capability_policy (CapabilityPolicy.Catalogue).
//
// The catalogue restricts what it names and nothing else. A grant naming
// a capability the catalogue does not list is left to the checks that
// were always there (the registry, CapabilityPolicy's rules, local
// policy), so adopting the file is never the moment every unlisted
// in-process capability stops dispatching. What it lists, it judges:
// a disabled entry is refused, and so is a grant its restrictions do not
// cover. A server that lists methods lists all of them -- a method it
// does not name is refused -- because a list of what a server offers
// that silently admits what it leaves out is not a list anybody can
// review.

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// The run kinds a catalogue restriction names (CapabilityRestrictions.Kinds).
const (
	CatalogueKindTask     = "task"
	CatalogueKindSession  = "session"
	CatalogueKindDirector = "director"
	CatalogueKindCI       = "ci"
	CatalogueKindProcess  = "process"
	CatalogueKindReview   = "review"
)

// CatalogueKinds is every kind, in the order the design document lists them.
var CatalogueKinds = []string{
	CatalogueKindTask, CatalogueKindSession, CatalogueKindDirector,
	CatalogueKindCI, CatalogueKindProcess, CatalogueKindReview,
}

// CatalogueNoRepo is the repos: pattern a task with no repository matches.
const CatalogueNoRepo = "none"

// CatalogueSecretPrefix is how a catalogue names a secret: by reference
// into the deployment's secret store, never by value.
const CatalogueSecretPrefix = "$secret:"

// Patterns is a restriction list that tells "not written" (nil: no
// restriction) from "written empty" (nobody), in YAML as well as JSON.
type Patterns []string

// IsZero is what yaml.v3's omitempty asks: only an unwritten list is
// left out, so `repos: []` survives an export.
func (p Patterns) IsZero() bool { return p == nil }

// CapabilityRestrictions is who may hold one catalogue entry (§3.3).
type CapabilityRestrictions struct {
	// Repos are owner/name globs ("owner/*", "*", or "none" for a task
	// with no repository). Nil admits every repository; empty admits none.
	Repos Patterns `json:"repos" yaml:"repos,omitempty"`
	// Templates are template id or name globs. Nil (or "*") admits ad-hoc
	// tasks and every template; any other list admits only tasks filed
	// from a template it matches.
	Templates Patterns `json:"templates" yaml:"templates,omitempty"`
	// Kinds are CatalogueKinds. Nil admits every kind; empty admits none.
	Kinds Patterns `json:"kinds" yaml:"kinds,omitempty"`
	// CIOnly keeps the entry off every task, template, schedule and
	// session: only grain CI jobs may hold it.
	CIOnly bool `json:"ciOnly,omitempty" yaml:"ci_only,omitempty"`
	// RequiresPerson says granting the entry is a person's decision.
	RequiresPerson bool `json:"requiresPerson,omitempty" yaml:"requires_person,omitempty"`
	// RequiresPersonWhen is RequiresPerson for particular argument
	// values only: {project: [grain-prod]}.
	RequiresPersonWhen map[string][]string `json:"requiresPersonWhen,omitempty" yaml:"requires_person_when,omitempty"`
}

// CatalogueArg is what a method allows for one argument.
type CatalogueArg struct {
	// Allowed is every value the argument may take; empty allows any.
	Allowed []string `json:"allowed,omitempty" yaml:"allowed,omitempty"`
}

// CatalogueMethod is one method of a capability server.
type CatalogueMethod struct {
	Name         string                  `json:"name" yaml:"name"`
	Description  string                  `json:"description,omitempty" yaml:"description,omitempty"`
	Args         map[string]CatalogueArg `json:"args,omitempty" yaml:"args,omitempty"`
	Restrictions *CapabilityRestrictions `json:"restrictions,omitempty" yaml:"restrictions,omitempty"`
}

// CatalogueServer is a capability server: the restriction half of it.
// Its transport (url, command, secrets) is grain_config.mcp_servers',
// which pkg/staterepo projects the same settings/capabilities.yml entry
// onto.
type CatalogueServer struct {
	// ID is the server's capability_id, the key it has in mcp_servers.
	ID      string            `json:"id" yaml:"id"`
	Name    string            `json:"name,omitempty" yaml:"name,omitempty"`
	Enabled *bool             `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Aliases []string          `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Methods []CatalogueMethod `json:"methods,omitempty" yaml:"methods,omitempty"`
	// Restrictions apply to every method that has none of its own.
	Restrictions *CapabilityRestrictions `json:"restrictions,omitempty" yaml:"restrictions,omitempty"`
}

// CatalogueInProcess is one capability built into the controller
// (self-debug, gcp-key, corp-research, ...).
type CatalogueInProcess struct {
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Secret is the $secret:<name> the capability's material comes from,
	// where it has one.
	Secret       string                  `json:"secret,omitempty" yaml:"secret,omitempty"`
	Restrictions *CapabilityRestrictions `json:"restrictions,omitempty" yaml:"restrictions,omitempty"`
}

// CataloguePreset is one api:<name> scope set's entry in api_scopes.presets.
type CataloguePreset struct {
	Enabled      *bool                   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Restrictions *CapabilityRestrictions `json:"restrictions,omitempty" yaml:"restrictions,omitempty"`
}

// CatalogueAPIScopes is the agent API's half of the catalogue.
type CatalogueAPIScopes struct {
	Presets map[string]CataloguePreset `json:"presets,omitempty" yaml:"presets,omitempty"`
	// RawScopes restrict api:<area>:<verb> grants by scope glob
	// ("*:write", "secrets:*"). Every pattern a scope matches applies.
	RawScopes map[string]CapabilityRestrictions `json:"rawScopes,omitempty" yaml:"raw_scopes,omitempty"`
}

// CatalogueRepoDefaults is one repository's own default grants.
type CatalogueRepoDefaults struct {
	Task []string `json:"task,omitempty" yaml:"task,omitempty"`
	CI   []string `json:"ci,omitempty" yaml:"ci,omitempty"`
}

// CatalogueDefaults is what a run holds when nobody asked for anything.
type CatalogueDefaults struct {
	Task  []string                         `json:"task,omitempty" yaml:"task,omitempty"`
	CI    []string                         `json:"ci,omitempty" yaml:"ci,omitempty"`
	Repos map[string]CatalogueRepoDefaults `json:"repos,omitempty" yaml:"repos,omitempty"`
}

// CapabilityCatalogue is the whole of settings/capabilities.yml's
// judgement. A nil catalogue judges nothing.
type CapabilityCatalogue struct {
	Defaults  CatalogueDefaults             `json:"defaults,omitempty"`
	Servers   []CatalogueServer             `json:"servers,omitempty"`
	InProcess map[string]CatalogueInProcess `json:"inProcess,omitempty"`
	APIScopes CatalogueAPIScopes            `json:"apiScopes,omitempty"`
}

// GrantContext is the run a set of grants is judged for.
type GrantContext struct {
	// Repo is the task's repository; nil for a task with none.
	Repo *RepoRef
	// TemplateID and TemplateName are the template the task was filed
	// from; both empty for an ad-hoc task.
	TemplateID   string
	TemplateName string
	// Kind is one of CatalogueKinds.
	Kind string
	// Grants are grant strings as a task records them.
	Grants []string
}

// GrantVerdict is what a catalogue admitted.
type GrantVerdict struct {
	// RequiresPerson is every admitted grant whose holding is a person's
	// decision, sorted.
	RequiresPerson []string
}

// GrantRefusal is a grant the catalogue refuses, and why.
type GrantRefusal struct {
	Grant  string
	Reason string
}

func (r GrantRefusal) Error() string { return r.Reason }

// CatalogueKindOf is the kind a task is judged as: a standing session
// by the session tag it carries (ConsoleSessionTag), the director by
// its own, a review by its origin. A session is only a task grain filed
// as one (interactive, with ReasonConsoleSession): the tag alone is
// something a person or an agent can put on any interactive task, and
// must not buy it the grants the catalogue keeps for sessions or the
// director.
func CatalogueKindOf(t Task) string {
	if id, ok := ConsoleSessionIDOf(t); ok && t.Interactive && t.Origin.Reason == ReasonConsoleSession {
		if id == DirectorSessionID {
			return CatalogueKindDirector
		}
		return CatalogueKindSession
	}
	if t.Origin.Reason == ReasonReview {
		return CatalogueKindReview
	}
	return CatalogueKindTask
}

// TaskGrantContext is the GrantContext of a stored task: its grants
// with arguments, its repository and template, and its kind.
func TaskGrantContext(t Task) GrantContext {
	gc := GrantContext{Repo: t.Target, TemplateID: t.TemplateID, Kind: CatalogueKindOf(t)}
	for _, g := range t.Grants {
		gc.Grants = append(gc.Grants, g.Capability)
	}
	return gc
}

// ValidateGrants judges every grant in gc: the first refusal is returned
// as a *GrantRefusal, and otherwise the verdict says which of them a
// person has to approve. A nil catalogue admits everything.
func (c *CapabilityCatalogue) ValidateGrants(gc GrantContext) (GrantVerdict, error) {
	var v GrantVerdict
	if c == nil {
		return v, nil
	}
	for _, g := range gc.Grants {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		person, err := c.judge(g, gc)
		if err != nil {
			return GrantVerdict{}, &GrantRefusal{Grant: g, Reason: err.Error()}
		}
		if person {
			v.RequiresPerson = append(v.RequiresPerson, g)
		}
	}
	sort.Strings(v.RequiresPerson)
	return v, nil
}

// Refusals is ValidateGrants without stopping at the first: every grant
// the catalogue refuses, in gc's order.
func (c *CapabilityCatalogue) Refusals(gc GrantContext) []GrantRefusal {
	if c == nil {
		return nil
	}
	var out []GrantRefusal
	for _, g := range gc.Grants {
		if _, err := c.judge(strings.TrimSpace(g), gc); err != nil {
			out = append(out, GrantRefusal{Grant: g, Reason: err.Error()})
		}
	}
	return out
}

// judge is one grant: whether a person must approve it, or why it is
// refused.
func (c *CapabilityCatalogue) judge(g string, gc GrantContext) (bool, error) {
	switch {
	case IsCapabilityRef(g):
		return c.judgeRef(g, gc)
	case strings.HasPrefix(g, APIGrantPrefix):
		return c.judgeAPI(g, gc)
	}
	return c.judgeInProcess(g, gc)
}

func (c *CapabilityCatalogue) judgeRef(g string, gc GrantContext) (bool, error) {
	var with map[string]any
	ref := CapabilityGrantRef(g)
	if spec, err := CapabilityGrantSpec(g); err == nil {
		ref, with = spec.Capability, spec.With
	}
	id, method, _ := strings.Cut(ref, "#")
	srv, ok := c.server(id)
	if !ok {
		return false, nil
	}
	if srv.Enabled != nil && !*srv.Enabled {
		return false, fmt.Errorf("capability %s: server %s is disabled in settings/capabilities.yml", ref, srv.ID)
	}
	r := srv.Restrictions
	if len(srv.Methods) > 0 {
		var m *CatalogueMethod
		for i := range srv.Methods {
			if srv.Methods[i].Name == method {
				m = &srv.Methods[i]
				break
			}
		}
		if m == nil {
			return false, fmt.Errorf("capability %s: server %s lists no method %q in settings/capabilities.yml", ref, srv.ID, method)
		}
		if m.Restrictions != nil {
			r = m.Restrictions
		}
		for _, name := range sortedArgNames(with) {
			arg, declared := m.Args[name]
			if !declared || len(arg.Allowed) == 0 {
				continue
			}
			val := argString(with[name])
			if !containsString(arg.Allowed, val) {
				return false, fmt.Errorf("capability %s: argument %s=%q is not one settings/capabilities.yml allows (%s)",
					ref, name, val, strings.Join(arg.Allowed, ", "))
			}
		}
	}
	return r.judge(ref, gc, with)
}

func (c *CapabilityCatalogue) judgeAPI(g string, gc GrantContext) (bool, error) {
	person, err := c.judgeRetiredInProcess(g, gc)
	if err != nil {
		return false, err
	}
	p, err := c.judgeAPIScopes(g, gc)
	return person || p, err
}

// judgeRetiredInProcess applies the in_process entries a catalogue still
// keeps under the bare name of a retired capability (memory,
// release-notes, test-quarantine, ...) to the api: preset that replaced
// it. A catalogue written before the retirement -- an upgraded store's
// capability_policy, or a grain/config settings/capabilities.yml nobody
// has edited since -- disabled or restricted the capability under that
// name, and the migrations that rewrote stored grants to the preset do
// not touch the catalogue: without this the restriction would stop
// binding the moment the grant was renamed.
func (c *CapabilityCatalogue) judgeRetiredInProcess(g string, gc GrantContext) (bool, error) {
	person := false
	for _, legacy := range retiredInProcessNames(g) {
		entry, ok := c.InProcess[legacy]
		if !ok {
			continue
		}
		if entry.Enabled != nil && !*entry.Enabled {
			return false, fmt.Errorf("capability %s is disabled in settings/capabilities.yml (in_process.%s, the capability it replaced)", g, legacy)
		}
		p, err := entry.Restrictions.judge(g, gc, nil)
		if err != nil {
			return false, fmt.Errorf("%v (in_process.%s, the capability it replaced)", err, legacy)
		}
		person = person || p
	}
	return person, nil
}

// canonicalPresetKey maps "grain/<preset>" to "<preset>" for grain's built-in
// presets so settings/capabilities.yml's api_scopes.presets.<preset> (and
// retired in_process.<preset> entries) bind api:<preset> and api:grain/<preset>
// alike. Declared scope sets keep their own name.
func canonicalPresetKey(what string) string {
	if trimmed, ok := strings.CutPrefix(what, APIScopeSetBuiltinPrefix); ok {
		if _, isPreset := APIPresetNamed(trimmed); isPreset {
			return trimmed
		}
	}
	return what
}

// presetLookupKeys are the api_scopes.presets keys that bind an api grant's
// target (both "<preset>" and "grain/<preset>" for a built-in preset).
func presetLookupKeys(what string) []string {
	canon := canonicalPresetKey(what)
	if canon != what {
		return []string{canon, what}
	}
	if _, isPreset := APIPresetNamed(canon); isPreset {
		return []string{canon, APIScopeSetBuiltinPrefix + canon}
	}
	return []string{what}
}

// RetiredInProcessPreset returns the api_scopes.presets key that replaced a
// 1-to-1 retired in-process capability (such as "memory" -> "memory"), or
// false when name is not a retired in-process capability replaced by a preset.
func RetiredInProcessPreset(name string) (string, bool) {
	to, ok := legacyGrantReplacements[strings.TrimSpace(name)]
	if !ok {
		return "", false
	}
	preset, isAPI := strings.CutPrefix(to, APIGrantPrefix)
	if !isAPI {
		return "", false
	}
	if _, ok := APIPresetNamed(preset); !ok {
		return "", false
	}
	return preset, true
}

// retiredInProcessNames are the retired bare capabilities whose
// replacement is the api: grant g (any @repo limit aside), sorted.
func retiredInProcessNames(g string) []string {
	what, _, _ := strings.Cut(strings.TrimPrefix(g, APIGrantPrefix), "@")
	what = canonicalPresetKey(what)
	grant := APIGrantPrefix + what
	var out []string
	for legacy, to := range legacyGrantReplacements {
		if to == grant {
			out = append(out, legacy)
		}
	}
	for legacy, tos := range legacyGrantMultiReplacements {
		if slices.Contains(tos, grant) {
			out = append(out, legacy)
		}
	}
	sort.Strings(out)
	return out
}

func (c *CapabilityCatalogue) judgeAPIScopes(g string, gc GrantContext) (bool, error) {
	rest := strings.TrimPrefix(g, APIGrantPrefix)
	what, _, _ := strings.Cut(rest, "@")
	matchedPreset := false
	person := false
	for _, key := range presetLookupKeys(what) {
		p, ok := c.APIScopes.Presets[key]
		if !ok {
			continue
		}
		matchedPreset = true
		if p.Enabled != nil && !*p.Enabled {
			return false, fmt.Errorf("capability %s is disabled in settings/capabilities.yml", g)
		}
		reqPerson, err := p.Restrictions.judge(g, gc, nil)
		if err != nil {
			return false, err
		}
		person = person || reqPerson
	}
	if matchedPreset {
		return person, nil
	}
	if !strings.Contains(what, ":") {
		return false, nil
	}
	for _, pattern := range sortedRestrictionKeys(c.APIScopes.RawScopes) {
		if ok, _ := path.Match(pattern, what); !ok {
			continue
		}
		r := c.APIScopes.RawScopes[pattern]
		p, err := r.judge(g, gc, nil)
		if err != nil {
			return false, fmt.Errorf("%v (raw scope rule %q)", err, pattern)
		}
		person = person || p
	}
	return person, nil
}

func (c *CapabilityCatalogue) judgeInProcess(g string, gc GrantContext) (bool, error) {
	entry, ok := c.InProcess[g]
	if !ok {
		family, _, cut := strings.Cut(g, ":")
		if !cut {
			return false, nil
		}
		if entry, ok = c.InProcess[family]; !ok {
			return false, nil
		}
	}
	if entry.Enabled != nil && !*entry.Enabled {
		return false, fmt.Errorf("capability %s is disabled in settings/capabilities.yml", g)
	}
	return entry.Restrictions.judge(g, gc, nil)
}

func (c *CapabilityCatalogue) server(id string) (CatalogueServer, bool) {
	for _, s := range c.Servers {
		if s.ID == id || containsString(s.Aliases, id) {
			return s, true
		}
	}
	return CatalogueServer{}, false
}

// judge is one restriction block against one run. A nil block restricts
// nothing.
func (r *CapabilityRestrictions) judge(name string, gc GrantContext, with map[string]any) (bool, error) {
	if r == nil {
		return false, nil
	}
	if r.CIOnly && gc.Kind != CatalogueKindCI {
		return false, fmt.Errorf("capability %s is CI-only in settings/capabilities.yml: no %s may hold it", name, kindWord(gc.Kind))
	}
	if r.Kinds != nil && !containsString(r.Kinds, gc.Kind) {
		return false, fmt.Errorf("capability %s is not granted to a %s by settings/capabilities.yml (kinds: %s)",
			name, kindWord(gc.Kind), listWord(r.Kinds))
	}
	if r.Repos != nil && !matchesRepo(r.Repos, gc.Repo) {
		repo := "a task with no repository (" + CatalogueNoRepo + ")"
		if gc.Repo != nil {
			repo = gc.Repo.Owner + "/" + gc.Repo.Name
		}
		return false, fmt.Errorf("capability %s is not granted on %s by settings/capabilities.yml (repos: %s)",
			name, repo, listWord(r.Repos))
	}
	if r.Templates != nil && !matchesTemplate(r.Templates, gc.TemplateID, gc.TemplateName) {
		from := "an ad-hoc task"
		if gc.TemplateID != "" {
			from = "template " + gc.TemplateID
		}
		return false, fmt.Errorf("capability %s is not granted to %s by settings/capabilities.yml (templates: %s)",
			name, from, listWord(r.Templates))
	}
	if r.RequiresPerson {
		return true, nil
	}
	for _, arg := range sortedArgNames(with) {
		if containsString(r.RequiresPersonWhen[arg], argString(with[arg])) {
			return true, nil
		}
	}
	return false, nil
}

func matchesRepo(patterns []string, repo *RepoRef) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "*":
			return true
		case repo == nil:
			if p == CatalogueNoRepo {
				return true
			}
		default:
			if ok, _ := path.Match(p, strings.ToLower(repo.Owner+"/"+repo.Name)); ok {
				return true
			}
		}
	}
	return false
}

func matchesTemplate(patterns []string, id, name string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		for _, cand := range []string{id, name} {
			if cand == "" {
				continue
			}
			if ok, _ := path.Match(p, cand); ok {
				return true
			}
		}
	}
	return false
}

func kindWord(kind string) string {
	switch kind {
	case "":
		return "run"
	case CatalogueKindCI:
		return "CI job"
	case CatalogueKindDirector:
		return "director session"
	case CatalogueKindReview:
		return "review"
	case CatalogueKindProcess:
		return "capability server process"
	}
	return kind
}

func listWord(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func argString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func sortedArgNames(with map[string]any) []string {
	out := make([]string, 0, len(with))
	for k := range with {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRestrictionKeys(m map[string]CapabilityRestrictions) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate is the catalogue's internal consistency (§3.3): every
// restriction is well formed, every secret is named by reference, and
// every default grant is one the catalogue itself admits where it is a
// default.
func (c *CapabilityCatalogue) Validate() error {
	if c == nil {
		return nil
	}
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	check := func(where string, r *CapabilityRestrictions) {
		if r == nil {
			return
		}
		if err := r.Validate(); err != nil {
			add("%s: %v", where, err)
		}
	}
	seen := map[string]bool{}
	for i, s := range c.Servers {
		where := fmt.Sprintf("servers[%d]", i)
		if strings.TrimSpace(s.ID) == "" {
			add("%s has no id", where)
		} else if seen[s.ID] {
			add("%s: server %s is listed twice", where, s.ID)
		}
		seen[s.ID] = true
		check(where, s.Restrictions)
		methods := map[string]bool{}
		for j, m := range s.Methods {
			mw := fmt.Sprintf("%s.methods[%d]", where, j)
			if strings.TrimSpace(m.Name) == "" {
				add("%s has no name", mw)
			} else if methods[m.Name] {
				add("%s: method %s is listed twice", mw, m.Name)
			}
			methods[m.Name] = true
			check(mw, m.Restrictions)
		}
	}
	for _, name := range sortedInProcess(c.InProcess) {
		e := c.InProcess[name]
		if e.Secret != "" && !strings.HasPrefix(e.Secret, CatalogueSecretPrefix) {
			add("in_process.%s: secret must be a %s<name> reference, never a value", name, CatalogueSecretPrefix)
		}
		check("in_process."+name, e.Restrictions)
	}
	for name, p := range c.APIScopes.Presets {
		check("api_scopes.presets."+name, p.Restrictions)
	}
	for pattern, r := range c.APIScopes.RawScopes {
		if _, err := path.Match(pattern, ""); err != nil {
			add("api_scopes.raw_scopes: %q: %v", pattern, err)
		}
		r := r
		check("api_scopes.raw_scopes."+pattern, &r)
	}
	// Defaults are judged where they are defaults: a task default as an
	// ad-hoc task on each repository that names it (or on none), a CI
	// default as a CI job.
	for _, g := range c.Defaults.Task {
		if err := c.defaultRefusal(g, nil, CatalogueKindTask); err != nil {
			add("defaults.task: %v", err)
		}
	}
	for _, g := range c.Defaults.CI {
		if err := c.defaultRefusal(g, nil, CatalogueKindCI); err != nil {
			add("defaults.ci: %v", err)
		}
	}
	for _, name := range sortedRepoDefaults(c.Defaults.Repos) {
		ref, err := ParseRepo(name)
		if err != nil {
			add("defaults.repos: %q is not owner/name: %v", name, err)
			continue
		}
		d := c.Defaults.Repos[name]
		for _, g := range d.Task {
			if err := c.defaultRefusal(g, &ref, CatalogueKindTask); err != nil {
				add("defaults.repos[%s].task: %v", name, err)
			}
		}
		for _, g := range d.CI {
			if err := c.defaultRefusal(g, &ref, CatalogueKindCI); err != nil {
				add("defaults.repos[%s].ci: %v", name, err)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// defaultRefusal judges a deployment-wide task default only where it has
// a repository restriction to break: a default every repository holds
// cannot be checked against "every repository", so it is checked against
// its kind and template rules alone.
func (c *CapabilityCatalogue) defaultRefusal(g string, repo *RepoRef, kind string) error {
	gc := GrantContext{Repo: repo, Kind: kind, Grants: []string{g}}
	if repo == nil {
		probe := *c
		probe.stripRepos()
		_, err := probe.ValidateGrants(gc)
		return err
	}
	_, err := c.ValidateGrants(gc)
	return err
}

// stripRepos clears every repos: rule of a copy, for defaultRefusal.
func (c *CapabilityCatalogue) stripRepos() {
	strip := func(r *CapabilityRestrictions) *CapabilityRestrictions {
		if r == nil {
			return nil
		}
		cp := *r
		cp.Repos = nil
		return &cp
	}
	servers := make([]CatalogueServer, len(c.Servers))
	for i, s := range c.Servers {
		s.Restrictions = strip(s.Restrictions)
		methods := make([]CatalogueMethod, len(s.Methods))
		for j, m := range s.Methods {
			m.Restrictions = strip(m.Restrictions)
			methods[j] = m
		}
		s.Methods = methods
		servers[i] = s
	}
	c.Servers = servers
	inProcess := map[string]CatalogueInProcess{}
	for k, e := range c.InProcess {
		e.Restrictions = strip(e.Restrictions)
		inProcess[k] = e
	}
	c.InProcess = inProcess
	presets := map[string]CataloguePreset{}
	for k, p := range c.APIScopes.Presets {
		p.Restrictions = strip(p.Restrictions)
		presets[k] = p
	}
	raw := map[string]CapabilityRestrictions{}
	for k, r := range c.APIScopes.RawScopes {
		raw[k] = *strip(&r)
	}
	c.APIScopes = CatalogueAPIScopes{Presets: presets, RawScopes: raw}
}

// Validate is one restriction block's own consistency.
func (r CapabilityRestrictions) Validate() error {
	for _, k := range r.Kinds {
		if !containsString(CatalogueKinds, k) {
			return fmt.Errorf("kind %q is not one of %s", k, strings.Join(CatalogueKinds, ", "))
		}
	}
	if r.CIOnly && r.Kinds != nil && !(len(r.Kinds) == 1 && r.Kinds[0] == CatalogueKindCI) {
		return fmt.Errorf("ci_only is set, so kinds must be [ci], not [%s]", strings.Join(r.Kinds, ", "))
	}
	for _, p := range append(append([]string{}, r.Repos...), r.Templates...) {
		if _, err := path.Match(strings.ToLower(p), ""); err != nil {
			return fmt.Errorf("pattern %q: %v", p, err)
		}
	}
	return nil
}

func sortedInProcess(m map[string]CatalogueInProcess) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRepoDefaults(m map[string]CatalogueRepoDefaults) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CatalogueRequiresPersonNote is the line a proposal filed for a person
// because of the catalogue carries.
func CatalogueRequiresPersonNote(grants []string) string {
	return fmt.Sprintf("settings/capabilities.yml marks %s as a person's decision (requires_person)", strings.Join(grants, ", "))
}

// JoinCapabilities and SplitCapabilities are a stored capability list's
// text (joinCapabilities, splitCapabilities), for the state repository's
// projection of settings/capabilities.yml onto its columns.
func JoinCapabilities(items []string) string { return joinCapabilities(items) }

// SplitCapabilities is JoinCapabilities' inverse.
func SplitCapabilities(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return splitCapabilities(s)
}

// EffectivePreset returns the catalogue entry binding a scope set or preset
// (such as "grain/memory" -> presetKey "memory", or "ops/triage" ->
// "ops/triage"), combining any api_scopes.presets entry with any retired
// in_process entry that still binds it. legacyInProcess lists any retired
// in_process keys contributing to the result.
func (c *CapabilityCatalogue) EffectivePreset(name string) (presetKey string, entry CataloguePreset, legacyInProcess []string, ok bool) {
	presetKey = canonicalPresetKey(strings.TrimSpace(name))
	if c == nil {
		return presetKey, CataloguePreset{}, nil, false
	}
	for _, key := range presetLookupKeys(name) {
		p, exists := c.APIScopes.Presets[key]
		if !exists {
			continue
		}
		entry = mergeCataloguePresets(entry, ok, p)
		ok = true
	}
	for _, legacy := range sortedInProcess(c.InProcess) {
		target, isRetired := RetiredInProcessPreset(legacy)
		if !isRetired || target != presetKey {
			continue
		}
		ip := c.InProcess[legacy]
		entry = mergeCataloguePresets(entry, ok, CataloguePreset{
			Enabled:      ip.Enabled,
			Restrictions: ip.Restrictions,
		})
		ok = true
		legacyInProcess = append(legacyInProcess, legacy)
	}
	return presetKey, entry, legacyInProcess, ok
}

// FoldRetiredInProcess moves retired 1-to-1 preset capability entries out of
// c.InProcess into c.APIScopes.Presets, merging restrictions conservatively
// when both exist. It returns true when c was modified.
func (c *CapabilityCatalogue) FoldRetiredInProcess() bool {
	return c.foldRetiredInProcessMap(nil)
}

func (c *CapabilityCatalogue) foldRetiredInProcessMap(retiredPresets map[string]string) bool {
	if c == nil || len(c.InProcess) == 0 {
		return false
	}
	changed := false
	for _, legacy := range sortedInProcess(c.InProcess) {
		var preset string
		var ok bool
		if retiredPresets != nil {
			preset, ok = retiredPresets[legacy]
		} else {
			preset, ok = RetiredInProcessPreset(legacy)
		}
		if !ok {
			continue
		}
		ip := c.InProcess[legacy]
		if ip.Secret != "" {
			continue
		}
		if c.APIScopes.Presets == nil {
			c.APIScopes.Presets = map[string]CataloguePreset{}
		}
		existing, hasExisting := c.APIScopes.Presets[preset]
		c.APIScopes.Presets[preset] = mergeCataloguePresets(existing, hasExisting, CataloguePreset{
			Enabled:      ip.Enabled,
			Restrictions: ip.Restrictions,
		})
		delete(c.InProcess, legacy)
		changed = true
	}
	if len(c.InProcess) == 0 {
		c.InProcess = nil
	}
	return changed
}

func mergeCataloguePresets(a CataloguePreset, hasA bool, b CataloguePreset) CataloguePreset {
	if !hasA {
		return CataloguePreset{
			Enabled:      cloneBoolPtr(b.Enabled),
			Restrictions: cloneRestrictions(b.Restrictions),
		}
	}
	return CataloguePreset{
		Enabled:      mergeEnabledPtr(a.Enabled, b.Enabled),
		Restrictions: MergeCapabilityRestrictions(a.Restrictions, b.Restrictions),
	}
}

func cloneBoolPtr(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func mergeEnabledPtr(a, b *bool) *bool {
	if (a != nil && !*a) || (b != nil && !*b) {
		v := false
		return &v
	}
	if a != nil || b != nil {
		v := true
		return &v
	}
	return nil
}

func clonePatterns(p Patterns) Patterns {
	if p == nil {
		return nil
	}
	out := make(Patterns, len(p))
	copy(out, p)
	return out
}

func cloneRestrictions(r *CapabilityRestrictions) *CapabilityRestrictions {
	if r == nil {
		return nil
	}
	cp := CapabilityRestrictions{
		Repos:          clonePatterns(r.Repos),
		Templates:      clonePatterns(r.Templates),
		Kinds:          clonePatterns(r.Kinds),
		CIOnly:         r.CIOnly,
		RequiresPerson: r.RequiresPerson,
	}
	if len(r.RequiresPersonWhen) > 0 {
		cp.RequiresPersonWhen = make(map[string][]string, len(r.RequiresPersonWhen))
		for k, vals := range r.RequiresPersonWhen {
			cp.RequiresPersonWhen[k] = slices.Clone(vals)
		}
	}
	return &cp
}

// MergeCapabilityRestrictions returns a conservative restriction block that
// admits only runs admitted by both a and b, and requires a person whenever
// either requires one.
func MergeCapabilityRestrictions(a, b *CapabilityRestrictions) *CapabilityRestrictions {
	if a == nil {
		return cloneRestrictions(b)
	}
	if b == nil {
		return cloneRestrictions(a)
	}
	ciOnly := a.CIOnly || b.CIOnly
	kinds := intersectKindPatterns(a.Kinds, b.Kinds)
	if ciOnly && kinds != nil {
		if !containsString(kinds, CatalogueKindCI) {
			ciOnly = false
			kinds = Patterns{}
		} else {
			kinds = Patterns{CatalogueKindCI}
		}
	}
	return &CapabilityRestrictions{
		Repos:              intersectRepoPatterns(a.Repos, b.Repos),
		Templates:          intersectTemplatePatterns(a.Templates, b.Templates),
		Kinds:              kinds,
		CIOnly:             ciOnly,
		RequiresPerson:     a.RequiresPerson || b.RequiresPerson,
		RequiresPersonWhen: unionRequiresPersonWhen(a.RequiresPersonWhen, b.RequiresPersonWhen),
	}
}

func intersectKindPatterns(a, b Patterns) Patterns {
	if a == nil {
		return clonePatterns(b)
	}
	if b == nil {
		return clonePatterns(a)
	}
	out := Patterns{}
	for _, k := range a {
		if containsString(b, k) && !containsString(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func intersectRepoPatterns(a, b Patterns) Patterns {
	if a == nil {
		return clonePatterns(b)
	}
	if b == nil {
		return clonePatterns(a)
	}
	if len(a) == 0 || len(b) == 0 {
		return Patterns{}
	}
	hasAll := func(list Patterns) bool {
		return slices.ContainsFunc(list, func(p string) bool { return strings.TrimSpace(p) == "*" })
	}
	if hasAll(a) {
		return clonePatterns(b)
	}
	if hasAll(b) {
		return clonePatterns(a)
	}
	out := Patterns{}
	add := func(p string) {
		pl := strings.ToLower(strings.TrimSpace(p))
		if !slices.ContainsFunc(out, func(x string) bool { return strings.ToLower(strings.TrimSpace(x)) == pl }) {
			out = append(out, strings.TrimSpace(p))
		}
	}
	for _, p := range a {
		if repoPatternSubsumedByList(p, b) {
			add(p)
		}
	}
	for _, q := range b {
		if repoPatternSubsumedByList(q, a) {
			add(q)
		}
	}
	return out
}

func repoPatternSubsumedByList(pattern string, list Patterns) bool {
	pl := strings.ToLower(strings.TrimSpace(pattern))
	if pl == "" {
		return false
	}
	if pl == CatalogueNoRepo {
		return matchesRepo(list, nil)
	}
	if !strings.ContainsAny(pl, "*?[") {
		if ref, err := ParseRepo(pl); err == nil {
			return matchesRepo(list, &ref)
		}
	}
	for _, raw := range list {
		ql := strings.ToLower(strings.TrimSpace(raw))
		if ql == "*" || ql == pl {
			return true
		}
		if globSubsumes(ql, pl, true) {
			return true
		}
	}
	return false
}

func intersectTemplatePatterns(a, b Patterns) Patterns {
	if a == nil {
		return clonePatterns(b)
	}
	if b == nil {
		return clonePatterns(a)
	}
	if len(a) == 0 || len(b) == 0 {
		return Patterns{}
	}
	if slices.Contains(a, "*") {
		return clonePatterns(b)
	}
	if slices.Contains(b, "*") {
		return clonePatterns(a)
	}
	out := Patterns{}
	add := func(p string) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, p := range a {
		if templatePatternSubsumedByList(p, b) {
			add(p)
		}
	}
	for _, q := range b {
		if templatePatternSubsumedByList(q, a) {
			add(q)
		}
	}
	return out
}

func templatePatternSubsumedByList(pattern string, list Patterns) bool {
	if pattern == "" {
		return false
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return matchesTemplate(list, pattern, "")
	}
	for _, q := range list {
		if q == "*" || q == pattern {
			return true
		}
		if globSubsumes(q, pattern, false) {
			return true
		}
	}
	return false
}

// globSubsumes reports whether single-star glob outer ("prefix*suffix", no ?
// or [) matches every string that inner could match. When singleSegment is
// true, path.Match's '/' boundary is respected.
func globSubsumes(outer, inner string, singleSegment bool) bool {
	if strings.ContainsAny(outer, "?[") || strings.Count(outer, "*") != 1 {
		return false
	}
	prefix, suffix, _ := strings.Cut(outer, "*")
	if len(inner) < len(prefix)+len(suffix) || !strings.HasPrefix(inner, prefix) || !strings.HasSuffix(inner, suffix) {
		return false
	}
	middle := inner[len(prefix) : len(inner)-len(suffix)]
	if singleSegment && strings.Contains(middle, "/") {
		return false
	}
	return true
}

func unionRequiresPersonWhen(a, b map[string][]string) map[string][]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, src := range []map[string][]string{a, b} {
		for k, vals := range src {
			for _, v := range vals {
				if !slices.Contains(out[k], v) {
					out[k] = append(out[k], v)
				}
			}
			sort.Strings(out[k])
		}
	}
	return out
}
