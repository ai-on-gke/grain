package model

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// API scope sets (docs/agent-api.md, "Scope sets"): the presets of the
// agent API as declarations rather than a list in this file. grain ships
// its presets as read-only built-ins under the reserved grain/ prefix
// (grain/task-admin); a deployment declares its own in the
// `api_scope_set` settings table, which the state repository carries as
// tables/api_scope_set.json, so an agent proposes one by pull request
// against grain/config and the state check reports what it resolves to.
//
// A set is a definition, never a grant. What a task may call is still
// decided by its api grants (api:<set>, api:<set>@owner/name), which go
// through admission and capability policy like any other capability;
// declaring or merging a set hands it to nobody.

// APIScopeSetBuiltinPrefix begins the name of every set grain ships. A
// deployment's own set may not use it, so a built-in can be composed but
// never shadowed or edited.
const APIScopeSetBuiltinPrefix = "grain/"

// APIScopeSet is one declared set, as written.
type APIScopeSet struct {
	// Name is how grants, includes and templates refer to the set:
	// lower-case words separated by slashes (ops/triage).
	Name        string `json:"name"`
	Description string `json:"description"`
	// Scopes are scopes of the vocabulary, or "<area>:*" for every scope
	// of one area (tasks:* is tasks:read, tasks:write and tasks:admin).
	Scopes []string `json:"scopes"`
	// Includes are other sets, built-in or declared, whose resolved
	// scopes (and repository limits) this one adds.
	Includes []string `json:"includes"`
	// Repos limits every scope of the set, its included ones too, to
	// these repositories. Empty is no limit of the set's own.
	Repos []string `json:"repos"`
	// Deny are scopes (or "<area>:*") taken out after composition.
	Deny []string `json:"deny"`
	// RequiresPerson says only a person may grant the set. It is forced
	// on for a set resolving to any dangerous scope, whatever is written
	// here: a set can turn it on, never off.
	RequiresPerson bool `json:"requiresPerson"`
	Version        int  `json:"version"`
	Deprecated     bool `json:"deprecated"`
}

// ResolvedAPIScopeSet is a set after composition: what a grant of it
// hands out.
type ResolvedAPIScopeSet struct {
	APIScopeSet
	// Builtin is true for a set grain ships.
	Builtin bool `json:"builtin"`
	// Resolved is every scope the set grants, sorted.
	Resolved []APIScope `json:"resolved"`
	// RepoLimits is, for a scope limited to some repositories, which;
	// a scope not in it reaches every repository.
	RepoLimits map[APIScope][]string `json:"repoLimits,omitempty"`
	// Person is the effective requires-person flag: declared, or forced
	// by a dangerous scope.
	Person bool `json:"person"`
}

var apiScopeSetName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

// apiScopeArea is the area of a scope, the part before its colon.
func apiScopeArea(s APIScope) string {
	area, _, _ := strings.Cut(string(s), ":")
	return area
}

// ExpandAPIScopePattern reads one entry of a set's scopes or deny: a
// scope of the vocabulary, or "<area>:*" for every scope the vocabulary
// has in that area -- as of this build, so a scope grain adds to an area
// later joins every set naming the area's wildcard. internal is never a
// scope of any set, and a bare "*" is refused rather than read as
// everything.
func ExpandAPIScopePattern(pattern string) ([]APIScope, error) {
	pattern = strings.TrimSpace(pattern)
	if area, ok := strings.CutSuffix(pattern, ":*"); ok {
		var out []APIScope
		for _, s := range apiScopes {
			if apiScopeArea(s.Scope) == area {
				out = append(out, s.Scope)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%q names no area of the scope vocabulary", pattern)
		}
		return out, nil
	}
	if pattern == string(APIScopeInternal) {
		return nil, fmt.Errorf("%q is grain's own plumbing and is never in a scope set", pattern)
	}
	if _, ok := APIScopeInfoOf(APIScope(pattern)); !ok {
		return nil, fmt.Errorf("%q is not a scope of docs/agent-api.md (nor <area>:*)", pattern)
	}
	return []APIScope{APIScope(pattern)}, nil
}

// BuiltinAPIScopeSets are the sets grain ships: one per preset, under
// grain/.
func BuiltinAPIScopeSets() []APIScopeSet {
	out := make([]APIScopeSet, len(apiPresets))
	for i, p := range apiPresets {
		scopes := make([]string, len(p.Scopes))
		for j, s := range p.Scopes {
			scopes[j] = string(s)
		}
		out[i] = APIScopeSet{Name: APIScopeSetBuiltinPrefix + p.Name, Description: p.Description,
			Scopes: scopes, Version: 1}
	}
	return out
}

// ValidateAPIScopeSetName says whether name may be a deployment's set.
func ValidateAPIScopeSetName(name string) error {
	switch {
	case name == "":
		return errors.New("a scope set needs a name")
	case len(name) > 100:
		return fmt.Errorf("scope set name %q is longer than 100 characters", name)
	case strings.HasPrefix(name, APIScopeSetBuiltinPrefix):
		return fmt.Errorf("scope set %q: the %s prefix is reserved for the sets grain ships", name, APIScopeSetBuiltinPrefix)
	case !apiScopeSetName.MatchString(name):
		return fmt.Errorf("scope set name %q must be lower-case letters, digits, '.', '-' and '_', in '/'-separated parts", name)
	}
	if _, isPreset := APIPresetNamed(name); isPreset {
		return fmt.Errorf("scope set %q would shadow grain's preset of that name (grain/%s)", name, name)
	}
	return nil
}

// ResolveAPIScopeSets validates a deployment's sets as one whole -- names,
// scopes, includes (which must exist and must not cycle), repos and deny
// -- and resolves every set, built-ins first. An error names every set
// that is wrong; nothing is resolved unless all of them are right, so a
// caller installing the result never installs half of a change.
func ResolveAPIScopeSets(declared []APIScopeSet) (map[string]ResolvedAPIScopeSet, error) {
	byName := map[string]APIScopeSet{}
	builtin := map[string]bool{}
	for _, s := range BuiltinAPIScopeSets() {
		byName[s.Name], builtin[s.Name] = s, true
	}
	var errs []error
	for _, s := range declared {
		if err := ValidateAPIScopeSetName(s.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := byName[s.Name]; dup {
			errs = append(errs, fmt.Errorf("scope set %q is declared twice", s.Name))
			continue
		}
		byName[s.Name] = s
	}
	out := map[string]ResolvedAPIScopeSet{}
	var resolve func(name string, path []string) (ResolvedAPIScopeSet, error)
	resolve = func(name string, path []string) (ResolvedAPIScopeSet, error) {
		if r, ok := out[name]; ok {
			return r, nil
		}
		if i := slices.Index(path, name); i >= 0 {
			return ResolvedAPIScopeSet{}, fmt.Errorf("includes form a cycle: %s", strings.Join(append(path[i:], name), " -> "))
		}
		s, ok := byName[name]
		if !ok {
			return ResolvedAPIScopeSet{}, fmt.Errorf("includes %q, which is no scope set", name)
		}
		path = append(path, name)
		r, err := resolveOne(s, builtin[name], func(inc string) (ResolvedAPIScopeSet, error) { return resolve(inc, path) })
		if err != nil {
			return ResolvedAPIScopeSet{}, err
		}
		out[name] = r
		return r, nil
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := resolve(name, nil); err != nil {
			errs = append(errs, fmt.Errorf("scope set %q: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// resolveOne composes one set from its own scopes and its includes'
// resolutions. A scope reaches every repository if any source grants it
// without a limit, and otherwise the union of its sources' limits; the
// set's own repos then narrows every scope, and deny removes scopes last.
func resolveOne(s APIScopeSet, builtin bool, include func(string) (ResolvedAPIScopeSet, error)) (ResolvedAPIScopeSet, error) {
	if s.Version < 0 {
		return ResolvedAPIScopeSet{}, fmt.Errorf("version %d is negative", s.Version)
	}
	all := map[APIScope]bool{}
	limits := map[APIScope][]string{}
	for _, p := range s.Scopes {
		scopes, err := ExpandAPIScopePattern(p)
		if err != nil {
			return ResolvedAPIScopeSet{}, err
		}
		for _, sc := range scopes {
			all[sc] = true
		}
	}
	for _, inc := range s.Includes {
		r, err := include(strings.TrimSpace(inc))
		if err != nil {
			return ResolvedAPIScopeSet{}, err
		}
		for _, sc := range r.Resolved {
			if lim, ok := r.RepoLimits[sc]; ok {
				for _, repo := range lim {
					if !containsFold(limits[sc], repo) {
						limits[sc] = append(limits[sc], repo)
					}
				}
				continue
			}
			all[sc] = true
		}
	}
	if len(s.Scopes) == 0 && len(s.Includes) == 0 {
		return ResolvedAPIScopeSet{}, errors.New("grants nothing: it has neither scopes nor includes")
	}
	var own []string
	for _, repo := range s.Repos {
		ref, err := ParseRepo(strings.TrimSpace(repo))
		if err != nil {
			return ResolvedAPIScopeSet{}, fmt.Errorf("repos: %v", err)
		}
		own = append(own, ref.String())
	}
	denied := map[APIScope]bool{}
	for _, p := range s.Deny {
		scopes, err := ExpandAPIScopePattern(p)
		if err != nil {
			return ResolvedAPIScopeSet{}, fmt.Errorf("deny: %w", err)
		}
		for _, sc := range scopes {
			denied[sc] = true
		}
	}
	r := ResolvedAPIScopeSet{APIScopeSet: s, Builtin: builtin, RepoLimits: map[APIScope][]string{}}
	add := func(sc APIScope, lim []string) {
		if denied[sc] {
			return
		}
		if len(own) > 0 {
			if lim == nil {
				lim = own
			} else {
				lim = slices.DeleteFunc(slices.Clone(lim), func(repo string) bool { return !containsFold(own, repo) })
				if len(lim) == 0 {
					return
				}
			}
		}
		r.Resolved = append(r.Resolved, sc)
		if lim != nil {
			r.RepoLimits[sc] = slices.Clone(lim)
		}
	}
	for sc := range all {
		add(sc, nil)
	}
	for sc, lim := range limits {
		if !all[sc] {
			add(sc, lim)
		}
	}
	sort.Slice(r.Resolved, func(i, j int) bool { return r.Resolved[i] < r.Resolved[j] })
	if len(r.RepoLimits) == 0 {
		r.RepoLimits = nil
	}
	r.Person = s.RequiresPerson || slices.ContainsFunc(r.Resolved, IsDangerousAPIScope)
	return r, nil
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

// The sets in effect in this process. Built-ins are always there; a
// deployment's are installed whole by InstallAPIScopeSets, and an
// install that does not validate leaves the previous ones in effect.
var apiScopeSets struct {
	sync.RWMutex
	resolved map[string]ResolvedAPIScopeSet
	err      error
}

func init() {
	r, err := ResolveAPIScopeSets(nil)
	if err != nil {
		panic("model: grain's own scope sets do not resolve: " + err.Error())
	}
	apiScopeSets.resolved = r
}

// InstallAPIScopeSets makes declared the deployment's sets in this
// process, or -- if they do not all resolve -- keeps the ones already in
// effect and records why (APIScopeSetsError), so a set that slipped past
// the state check is never half-applied.
func InstallAPIScopeSets(declared []APIScopeSet) error {
	r, err := ResolveAPIScopeSets(declared)
	apiScopeSets.Lock()
	defer apiScopeSets.Unlock()
	apiScopeSets.err = err
	if err != nil {
		return err
	}
	apiScopeSets.resolved = r
	return nil
}

// APIScopeSetsError is why the last install was refused, or nil.
func APIScopeSetsError() error {
	apiScopeSets.RLock()
	defer apiScopeSets.RUnlock()
	return apiScopeSets.err
}

// APIScopeSetNamed is the set in effect under name, built-in or declared.
func APIScopeSetNamed(name string) (ResolvedAPIScopeSet, bool) {
	apiScopeSets.RLock()
	defer apiScopeSets.RUnlock()
	r, ok := apiScopeSets.resolved[name]
	return r, ok
}

// APIScopeSetsInEffect is every set in effect, built-ins first, then by
// name.
func APIScopeSetsInEffect() []ResolvedAPIScopeSet {
	apiScopeSets.RLock()
	defer apiScopeSets.RUnlock()
	out := make([]ResolvedAPIScopeSet, 0, len(apiScopeSets.resolved))
	for _, r := range apiScopeSets.resolved {
		out = append(out, r)
	}
	SortResolvedAPIScopeSets(out)
	return out
}

// SortResolvedAPIScopeSets orders sets the way every listing does:
// built-ins first, then by name.
func SortResolvedAPIScopeSets(sets []ResolvedAPIScopeSet) {
	sort.Slice(sets, func(i, j int) bool {
		if sets[i].Builtin != sets[j].Builtin {
			return sets[i].Builtin
		}
		return sets[i].Name < sets[j].Name
	})
}

// APIScopeSetChange is how one set's effective scopes move between two
// versions of a deployment's declarations: what the state check prints
// on a pull request that changes them.
type APIScopeSetChange struct {
	Name    string
	Added   []APIScope
	Removed []APIScope
	// Reach is every scope the set grants before and after whose
	// repositories moved -- a repos limit dropped or changed -- which
	// widens or narrows a grant as surely as adding a scope does.
	Reach     []APIScopeReachChange
	New       bool
	Deleted   bool
	Person    bool
	WasPerson bool
}

// APIScopeReachChange is one scope whose repositories moved: Before and
// After are the repositories it reached, nil for every one.
type APIScopeReachChange struct {
	Scope  APIScope
	Before []string
	After  []string
}

// sameReach reports whether two repository limits (nil for every
// repository) reach the same repositories.
func sameReach(a, b []string) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for _, repo := range a {
		if !containsFold(b, repo) {
			return false
		}
	}
	return true
}

// DiffAPIScopeSets compares two resolutions set by set, declared sets
// only, sorted by name; a set whose scopes, their repositories and its
// flag did not move is left out.
func DiffAPIScopeSets(before, after map[string]ResolvedAPIScopeSet) []APIScopeSetChange {
	names := map[string]bool{}
	for n, r := range before {
		if !r.Builtin {
			names[n] = true
		}
	}
	for n, r := range after {
		if !r.Builtin {
			names[n] = true
		}
	}
	var out []APIScopeSetChange
	for n := range names {
		b, hadB := before[n]
		a, hasA := after[n]
		c := APIScopeSetChange{Name: n, New: !hadB, Deleted: !hasA, Person: a.Person, WasPerson: b.Person}
		for _, sc := range a.Resolved {
			if !slices.Contains(b.Resolved, sc) {
				c.Added = append(c.Added, sc)
				continue
			}
			if before, after := b.RepoLimits[sc], a.RepoLimits[sc]; !sameReach(before, after) {
				c.Reach = append(c.Reach, APIScopeReachChange{Scope: sc, Before: before, After: after})
			}
		}
		for _, sc := range b.Resolved {
			if !slices.Contains(a.Resolved, sc) {
				c.Removed = append(c.Removed, sc)
			}
		}
		if !c.New && !c.Deleted && len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Reach) == 0 && c.Person == c.WasPerson {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// APIScopeSetWriteGrantNames is what declaring set -- in place of the
// deployment's set of that name, alongside the rest in effect -- would
// hand every holder of it, and of every set including it: one api grant
// per scope and repository it reaches. Writing a set is granting those to
// whoever holds it, so an agent writing one through the API must hold
// each itself and none may be dangerous (pkg/ui's grant gate), as for any
// other grant. err is set for a set that would not resolve, and while the
// deployment's declared sets are not the ones in effect
// (APIScopeSetsError), since the write would then install sets this did
// not read.
func APIScopeSetWriteGrantNames(set APIScopeSet) ([]string, error) {
	if err := APIScopeSetsError(); err != nil {
		return nil, fmt.Errorf("the deployment's declared scope sets are not in effect: %w", err)
	}
	var declared []APIScopeSet
	replaced := false
	for _, r := range APIScopeSetsInEffect() {
		switch {
		case r.Builtin:
		case r.Name == set.Name:
			declared, replaced = append(declared, set), true
		default:
			declared = append(declared, r.APIScopeSet)
		}
	}
	if !replaced {
		declared = append(declared, set)
	}
	resolved, err := ResolveAPIScopeSets(declared)
	if err != nil {
		return nil, err
	}
	r := resolved[set.Name]
	var out []string
	for _, sc := range r.Resolved {
		lim, limited := r.RepoLimits[sc]
		if !limited {
			out = append(out, APIGrantPrefix+string(sc))
			continue
		}
		for _, repo := range lim {
			out = append(out, APIGrantPrefix+string(sc)+"@"+repo)
		}
	}
	return out, nil
}

// APIScopeSetGrantNames is what an api grant naming a declared set hands
// a process that cannot read the deployment's sets -- `grain mcpserver`,
// started with the run's grants as flags: one api grant per scope, each
// limited the way the set (and the grant's own @repo) limits it. Every
// other name, built-in presets included, comes back as itself.
func APIScopeSetGrantNames(name string) []string {
	g, ok, err := ParseAPIGrant(name)
	if !ok || err != nil || g.Preset == "" {
		return []string{name}
	}
	if r, found := APIScopeSetNamed(g.Preset); !found || r.Builtin {
		return []string{name}
	}
	var out []string
	for _, sc := range g.Scopes {
		for _, repo := range g.reposOf(sc) {
			n := APIGrantPrefix + string(sc)
			if repo != "" {
				n += "@" + repo
			}
			out = append(out, n)
		}
	}
	return out
}
