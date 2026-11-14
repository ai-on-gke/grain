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

package ui

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is the vocabulary of the agent API (docs/agent-api.md): the
// permission scopes every route of this server is held to
// (route_scopes.go, beside the route table itself), the presets that
// name a useful bundle of them, and the grant names that hand them to a
// task.
//
// The principle it serves is the operator's: anything a person can do in
// grain, an agent can do, if it is given the permission. The permission
// is a scope; the doing is the same route a person's browser calls, with
// the same handler, so validation, side effects and audit are one code
// path rather than a second copy wrapped as a tool.
//
// A grant is one in-process capability name per scope or preset:
//
//	api:<scope>                 e.g. api:tasks:write
//	api:<preset>                e.g. api:task-admin
//	api:<scope|preset>@<repo>   e.g. api:ci:write@bwsalmon/grain
//
// A grant with no @repo reaches every repository; a task holding only
// repo-limited grants for a scope reaches just those repositories, and
// a route whose repository cannot be told is refused to it. One name per
// scope rather than one grant carrying a list is what lets every place
// grain already compares grants by name -- the proposal trust gate
// (model.GrantsSubsetOf), the capability policy's rules, the picker --
// treat a scope exactly as it treats any other capability.

// The vocabulary itself lives in pkg/model (model.APIScope and its
// neighbours), where the proposal gate can read it too; these are its
// names on this side.
type (
	APIScope     = model.APIScope
	APIScopeInfo = model.APIScopeInfo
	APIPreset    = model.APIPreset
	APIGrant     = model.APIGrant
	APIGrantSet  = model.APIGrantSet
)

const (
	APIGrantPrefix = model.APIGrantPrefix
	scopeInternal  = model.APIScopeInternal
)

var (
	ParseAPIGrant       = model.ParseAPIGrant
	APIGrantsOf         = model.APIGrantsOf
	IsDangerousAPIScope = model.IsDangerousAPIScope
	ExpandAPIPreset     = model.ExpandAPIPreset
	APIScopes           = model.APIScopes
	APIPresets          = model.APIPresets
	apiScopeInfo        = model.APIScopeInfoOf
	apiPreset           = model.APIPresetNamed
)

// apiCapability is the picker row for an api grant name -- presets and
// scopes alike, and a repo-limited name described as one -- or false
// for a name that is not a valid api grant, or that grants nothing
// (APIGrant.OutsideRepoLimit) -- which capabilityRefusal then explains.
func apiCapability(id string) (Capability, bool) {
	g, ok, err := ParseAPIGrant(id)
	if !ok || err != nil || g.OutsideRepoLimit() != nil {
		return Capability{}, false
	}
	var name, description, summary string
	scopes := make([]string, len(g.Scopes))
	for i, s := range g.Scopes {
		scopes[i] = string(s)
	}
	if p, isPreset := apiPreset(g.Preset); isPreset {
		name = "API preset: " + p.Name
		summary = p.Description
		description = p.Description + " (" + strings.Join(scopes, ", ") + ")"
	} else if set, isSet := model.APIScopeSetNamed(g.Preset); isSet {
		name = "API scope set: " + set.Name
		summary = set.Description
		if summary == "" {
			summary = "Declared in the config repository (" + APIScopeSetsFile + ")"
		}
		description = summary + " (" + strings.Join(scopes, ", ") + ")"
		if set.Deprecated {
			name += " (deprecated)"
		}
	} else {
		info, _ := apiScopeInfo(g.Scopes[0])
		name = "API: " + string(info.Scope)
		summary = info.Description
		description = info.Description
	}
	if g.Dangerous() {
		name += " (dangerous)"
		description = "Dangerous -- only a person may grant this, and every call needs a reason. " + description
	}
	if g.Repo != "" {
		name += " on " + g.Repo
		description += ", on " + g.Repo + " only"
	}
	return Capability{ID: id, Name: name,
		Description: description + ". Exposes the matching routes of grain's own API as tools (docs/agent-api.md)",
		Preset:      g.Preset != "", Scopes: scopes, Dangerous: g.Dangerous(), Summary: summary}, true
}

// APICapabilities is every api grant a picker offers: the presets first,
// then built-in role scope sets with no unprefixed preset (grain/operator,
// grain/viewer, grain/contributor, grain/delegated) and the deployment's
// own scope sets, then each scope, the dangerous ones flagged in their names.
func APICapabilities() []Capability {
	var out []Capability
	for _, p := range model.APIPresets() {
		row, _ := apiCapability(APIGrantPrefix + p.Name)
		out = append(out, row)
	}
	// Built-in scope sets that mirror an unprefixed preset stay offered
	// once under that preset (GET /api/capability-methods marks the
	// grain/<preset> method as an alias of it); built-in role sets with no
	// unprefixed preset and the deployment's own scope sets are offered here.
	for _, set := range model.APIScopeSetsInEffect() {
		if set.Builtin {
			if _, dup := apiPreset(strings.TrimPrefix(set.Name, model.APIScopeSetBuiltinPrefix)); dup {
				continue
			}
		}
		if row, ok := apiCapability(APIGrantPrefix + set.Name); ok {
			out = append(out, row)
		}
	}
	for _, s := range model.APIScopes() {
		row, _ := apiCapability(APIGrantPrefix + string(s.Scope))
		out = append(out, row)
	}
	return out
}

// pickerCapabilities is Config.Capabilities with its api rows read again
// now rather than when the Config was built. The daemon and grain demo
// append APICapabilities() once at startup, but a deployment's own scope
// sets change underneath a running process -- PUT and DELETE
// /api/api-scope-sets and a state import all call
// model.LoadAPIScopeSets -- and a set declared after that snapshot was
// never offered by any picker, while one since removed or edited went on
// being offered (and accepted) as it was at startup.
//
// A Config with no api rows at all offers none here either: whether the
// picker lists the agent API is the Config's to say, and only which sets
// it lists is read live. The rows keep their place after every other
// one, which is where both callers put them.
func (c *Client) pickerCapabilities() []Capability {
	out := make([]Capability, 0, len(c.Config.Capabilities))
	offersAPI := false
	for _, row := range c.Config.Capabilities {
		if strings.HasPrefix(row.ID, APIGrantPrefix) {
			offersAPI = true
			continue
		}
		out = append(out, row)
	}
	if offersAPI {
		out = append(out, APICapabilities()...)
	}
	return out
}

// apiProvider is the capability provider for one api grant name. It is a
// pure grant: nothing is minted or placed, and what it lets a run do is
// decided call by call, by agentAPIGate, from the task's grants -- so
// resolving it at dispatch only has to say that the name is one grain
// knows.
type apiProvider struct {
	model.BaseCapability
	grant APIGrant
}

func (p apiProvider) Spec() model.CapabilitySpec {
	row, _ := apiCapability(p.grant.Name)
	return model.CapabilitySpec{
		Name:        p.grant.Name,
		Label:       row.Name,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: row.Description,
	}
}

// AgentAPIDynamicProvider resolves every valid api grant name to its
// provider (model.CapabilityRegistry.RegisterDynamic), so a task
// holding one dispatches rather than being refused for a capability no
// provider answers to.
func AgentAPIDynamicProvider(name string) (model.CapabilityProvider, bool) {
	g, ok, err := ParseAPIGrant(name)
	if !ok || err != nil {
		return nil, false
	}
	return apiProvider{grant: g}, true
}
