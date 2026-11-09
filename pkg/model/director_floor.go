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

// The director's built-in Tier 3 floor
// (design/spec/director/director-capabilities.spec.md §3.3, gap G7): the
// api scopes no run of CatalogueKindDirector may hold, whatever it is
// granted and whatever settings/capabilities.yml says.
//
// The capability catalogue restricts only what an operator wrote, and a
// deployment without settings/capabilities.yml has none at all -- a nil
// catalogue admits everything. This layer sits underneath it:
// CapabilityCatalogue.ValidateGrants and Refusals apply it first, on a
// nil catalogue as on any other, so an operator's file may narrow the
// director further but no rule in it (a raw_scopes entry naming
// kinds: [director], a preset with no restrictions, or no entry at all)
// widens the director past it.

import (
	"fmt"
	"slices"
	"strings"
)

// directorForbiddenAPIScopes are the scopes the director kind is always
// refused (§3.3 items 1, 2, 4 and 6): force-merging past a gate, the
// deployment's settings and state repository, secret material, git
// connections and model backends, and repository settings (which carry
// merge-on-green).
var directorForbiddenAPIScopes = []APIScope{
	"merge:force",
	"settings:write",
	"secrets:write",
	"connections:write",
	"repos:write",
}

// DirectorForbiddenAPIScopes is every api scope grain's built-in floor
// refuses a director session, in the order the specification lists them.
func DirectorForbiddenAPIScopes() []APIScope { return slices.Clone(directorForbiddenAPIScopes) }

// IsDirectorForbiddenAPIScope reports whether scope is one the built-in
// floor refuses a director session.
func IsDirectorForbiddenAPIScope(scope APIScope) bool {
	return slices.Contains(directorForbiddenAPIScopes, scope)
}

// apiCapabilityServerID is the canonical spelling of an api grant
// (design/design/unified-grants.design.md §3): …/api#<scope-or-preset>.
// Grants are not recorded this way yet; the floor reads it all the same,
// so the day they are is not the day it stops binding.
const apiCapabilityServerID = "https://grain.dev/capabilities/api"

// GrantAPIScopes is every api scope grant g reaches anywhere, sorted, in
// whatever spelling it is written: api:<scope> or api:<preset>, either
// narrowed with @owner/name, a deployment's declared scope set, an older
// capability that is now an alias for a preset (self-upgrade), a retired
// name rewritten to an api grant, or the canonical …/api#<scope-or-preset>
// reference. Nil for a grant that reaches no api scope, including an api
// grant that does not parse.
func GrantAPIScopes(g string) []APIScope {
	g = strings.TrimSpace(g)
	if rest, ok := strings.CutPrefix(g, apiCapabilityServerID+"#"); ok {
		rest, _, _ = strings.Cut(rest, "=")
		g = APIGrantPrefix + strings.TrimSpace(rest)
	}
	names := []string{g}
	if !strings.HasPrefix(g, APIGrantPrefix) {
		for _, repl := range CanonicalLegacyGrants(g) {
			if repl != g {
				names = append(names, repl)
			}
		}
	}
	return APIGrantsOf(names).Scopes()
}

// DirectorFloorRefusal is why the built-in floor refuses grant g to a run
// of kind, or nil when it does not: only CatalogueKindDirector is ever
// refused, and only a grant reaching one of DirectorForbiddenAPIScopes.
// The refusal is marked Builtin, so a caller can say it came from grain
// rather than from settings/capabilities.yml.
func DirectorFloorRefusal(g, kind string) *GrantRefusal {
	if kind != CatalogueKindDirector {
		return nil
	}
	var hit []string
	for _, scope := range GrantAPIScopes(g) {
		if IsDirectorForbiddenAPIScope(scope) {
			hit = append(hit, string(scope))
		}
	}
	if len(hit) == 0 {
		return nil
	}
	what := "scope " + hit[0]
	if len(hit) > 1 {
		what = "scopes " + strings.Join(hit, ", ")
	}
	return &GrantRefusal{
		Grant: strings.TrimSpace(g),
		Reason: fmt.Sprintf("capability %s grants %s, which grain never grants a director session "+
			"(built-in Tier 3 floor, design/spec/director/director-capabilities.spec.md §3.3): "+
			"settings/capabilities.yml may narrow the director's grants but cannot widen them past it",
			strings.TrimSpace(g), what),
		Builtin: true,
	}
}

// WithinDirectorFloor is set with every scope the built-in floor refuses
// removed when t is the director (CatalogueKindOf), and set unchanged for
// every other task. The agent API gate applies it to the grants it reads
// off the acting task, so a grant that reached the director some way
// round admission -- a deployment scope set redefined to include
// merge:force after the run started, say -- still reaches none of those
// routes.
func WithinDirectorFloor(t Task, set APIGrantSet) APIGrantSet {
	if CatalogueKindOf(t) != CatalogueKindDirector {
		return set
	}
	deny := make(map[APIScope]bool, len(directorForbiddenAPIScopes))
	for _, scope := range directorForbiddenAPIScopes {
		deny[scope] = true
	}
	return set.Without(deny)
}
