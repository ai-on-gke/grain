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
	"fmt"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// GCPSetAccounts is the existing service accounts each of a gcp entry's
// permission sets mints keys for, by set name: the accounts of its
// configured permission_sets, or of the old fixed pools its config or its
// -project/-account arguments still describe, which gcpsets.Migrate turns
// into a set over those same accounts. A set that creates accounts of its
// own lists none.
func GCPSetAccounts(cfg protocol.ServerConfig) map[string][]string {
	out := map[string][]string{}
	if sets, ok := cfg.Config["permission_sets"].([]any); ok {
		for _, item := range sets {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			list, _ := m["accounts"].([]any)
			for _, a := range list {
				if s, ok := a.(string); ok && s != "" {
					out[name] = append(out[name], s)
				}
			}
		}
		return out
	}
	project, _ := cfg.Config["project"].(string)
	var legacy []gcpsets.LegacyAccount
	if list, ok := cfg.Config["accounts"].([]any); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			a := gcpsets.LegacyAccount{Name: "default"}
			if n, ok := m["name"].(string); ok {
				a.Name = n
			}
			a.Email, _ = m["email"].(string)
			if c, ok := m["count"].(float64); ok {
				a.Count = int(c)
			}
			legacy = append(legacy, a)
		}
	}
	if len(legacy) == 0 {
		// The arguments' old flags, as `grain capability gcp` reads them.
		args := cfg.Args
		if InTreeName(cfg) == "gcp" && !cfg.OnHost() {
			args = args[2:]
		}
		for i := 0; i < len(args); i++ {
			flag, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
			if !hasValue && i+1 < len(args) {
				value = args[i+1]
			}
			switch flag {
			case "project":
				if project == "" {
					project = value
				}
			case "account":
				if a, err := parseGCPAccount(value); err == nil {
					legacy = append(legacy, a)
				}
			}
		}
	}
	for _, a := range legacy {
		name := a.Name
		if name == "default" || name == "" {
			name = "sandbox-agent"
		}
		out[name] = append(out[name], gcpkey.PoolEmails(a.Email, a.Count, project)...)
	}
	return out
}

// GCPOverlapWarnings are Test's warnings for a gcp entry one of whose
// permission sets mints keys for an account the in-process gcp-key
// provider also mints from (pool, gcpkey.PoolEmails of the deployment's
// gcp settings): GCP caps every service account at ten keys, and two
// leasing systems that cannot see each other's leases would share that
// cap, so either can find the account full.
func GCPOverlapWarnings(cfg protocol.ServerConfig, pool []string) []string {
	if len(pool) == 0 {
		return nil
	}
	inPool := map[string]bool{}
	for _, a := range pool {
		inPool[accountKey(a)] = true
		inPool[localPart(a)] = true
	}
	sets := GCPSetAccounts(cfg)
	names := make([]string, 0, len(sets))
	for n := range sets {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		var shared []string
		for _, a := range sets[n] {
			// By local part as well: a pool written without its project
			// has no domain to compare.
			if inPool[accountKey(a)] || inPool[localPart(a)] {
				shared = append(shared, a)
			}
		}
		if len(shared) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("permission set %q mints keys for %d service account(s) the built-in gcp-key capability also mints from (%s): "+
			"GCP allows ten keys per account and the two cannot see each other's leases, so either can find an account full. "+
			"To trial this server beside gcp-key, give it a permission set with service accounts of its own (docs/gcp-key-permission-sets.md).",
			n, len(shared), strings.Join(shared, ", ")))
	}
	return out
}

// accountKey is a service account as an email, lower-cased, whether it
// was written as one or as projects/<p>/serviceAccounts/<email>.
func accountKey(a string) string {
	if i := strings.LastIndex(a, "/"); i >= 0 {
		a = a[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(a))
}

func localPart(a string) string {
	local, _, _ := strings.Cut(accountKey(a), "@")
	return local
}
