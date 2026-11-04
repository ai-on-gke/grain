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
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Some retirements cannot be a schema migration: what a retired name
// became depends on files only the daemon can read, as
// local-scripts:<name>'s host-script grants depend on the scripts its
// declaration holds (pkg/capability/localscripts). The daemon works
// those out when it starts, rewrites what is stored with
// RewriteRetiredGrants, and sets the same renames as runtime expansions,
// so a retired name still arriving from somewhere nothing rewrote -- a
// repository's .grain, an older grain/config -- keeps granting what it
// became (CanonicalLegacyGrants).

var (
	runtimeExpansionsMu sync.RWMutex
	runtimeExpansions   = map[string][]string{}
)

// SetRuntimeGrantExpansions replaces the runtime expansions with
// renames until the returned func restores the ones before.
func SetRuntimeGrantExpansions(renames map[string][]string) (restore func()) {
	next := make(map[string][]string, len(renames))
	for from, tos := range renames {
		if from = strings.TrimSpace(from); from != "" && len(tos) > 0 {
			next[from] = slices.Clone(tos)
		}
	}
	runtimeExpansionsMu.Lock()
	prev := runtimeExpansions
	runtimeExpansions = next
	runtimeExpansionsMu.Unlock()
	return func() {
		runtimeExpansionsMu.Lock()
		runtimeExpansions = prev
		runtimeExpansionsMu.Unlock()
	}
}

// runtimeExpansion is what name became under the runtime expansions.
func runtimeExpansion(name string) ([]string, bool) {
	runtimeExpansionsMu.RLock()
	defer runtimeExpansionsMu.RUnlock()
	tos, ok := runtimeExpansions[name]
	return slices.Clone(tos), ok
}

// runtimeExpansionNames is every name the runtime expansions rewrite.
func runtimeExpansionNames() []string {
	runtimeExpansionsMu.RLock()
	defer runtimeExpansionsMu.RUnlock()
	return slices.Collect(maps.Keys(runtimeExpansions))
}

// RuntimeExpansionSource is the retired name whose runtime expansion
// holds a grant of g's method with g's arguments, and true; "" and false
// when none does or g is not a capability reference. The server is not
// compared, so a grant naming the same script through an alias of its
// server, or another server, is still found: a caller refusing what a
// retired name became errs towards refusing.
func RuntimeExpansionSource(g string) (string, bool) {
	key, ok := runtimeExpansionKey(g)
	if !ok {
		return "", false
	}
	runtimeExpansionsMu.RLock()
	defer runtimeExpansionsMu.RUnlock()
	froms := slices.Sorted(maps.Keys(runtimeExpansions))
	for _, from := range froms {
		for _, to := range runtimeExpansions[from] {
			if k, ok := runtimeExpansionKey(to); ok && k == key {
				return from, true
			}
		}
	}
	return "", false
}

// runtimeExpansionKey is g's method and its arguments as JSON.
func runtimeExpansionKey(g string) (string, bool) {
	spec, err := CapabilityGrantSpec(g)
	if err != nil {
		return "", false
	}
	ref, err := protocol.ParseRef(spec.Capability)
	if err != nil || ref.Method == "" {
		return "", false
	}
	with := spec.With
	if with == nil {
		with = map[string]any{}
	}
	data, err := json.Marshal(with)
	if err != nil {
		return "", false
	}
	return ref.Method + "=" + string(data), true
}

// RewriteRetiredGrants rewrites every stored grant of a name in renames
// to the grants it became, everywhere a schema migration's retirement
// does (rewriteLegacyGrantsMulti): the task, template and schedule
// grants, the deployment's and each repository's default capabilities,
// each repository's CI capabilities, and the console sessions'.
func (s *Store) RewriteRetiredGrants(ctx context.Context, renames map[string][]string) error {
	if len(renames) == 0 {
		return nil
	}
	return s.write(ctx, "rewrite retired grants", func(tx *sql.Tx) error {
		if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
			return err
		}
		return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames)
	})
}
