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

package daemon

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// missingNames is what System -> Health would list as missing, for an
// allowlist with none of grain's own entries -- a host that has not yet
// been given an upgrade's new ones -- and servers.
func missingNames(servers map[string]protocol.ServerConfig) []string {
	st := HostExecutorStatus{Hostd: &HostdStatus{Reachable: true, Missing: ui.MissingHostdBuiltins(nil)}}
	st.OnlyNeeded(servers)
	var out []string
	for _, m := range st.Hostd.Missing {
		out = append(out, m.Name)
	}
	return out
}

func gchatOnHost(config map[string]any, disabled bool) map[string]protocol.ServerConfig {
	return map[string]protocol.ServerConfig{intree.CanonicalID(intree.GChatName): {
		Command: intree.HostEntry(intree.GChatName), Run: protocol.RunHost, Builtin: intree.GChatName,
		Args: []string{"-listen", "127.0.0.1:4251"}, Config: config, Disabled: disabled,
	}}
}

// A built-in nobody has set up is not broken for want of its host entry:
// only entries something would run are reported missing.
func TestHealthReportsOnlyTheHostdEntriesSomethingWouldRun(t *testing.T) {
	gchat := intree.HostEntry(intree.GChatName)
	spaces := map[string]any{"spaces": []any{map[string]any{"name": "team"}}}
	for _, c := range []struct {
		name     string
		servers  map[string]protocol.ServerConfig
		wantChat bool
	}{
		{"no servers configured", nil, false},
		{"gchat in the container", map[string]protocol.ServerConfig{intree.CanonicalID(intree.GChatName): {
			Command: intree.BuiltinCommand, Args: []string{"capability", intree.GChatName}, Builtin: intree.GChatName}}, false},
		{"gchat on the host, not configured", gchatOnHost(nil, false), false},
		{"gchat on the host, configured but disabled", gchatOnHost(spaces, true), false},
		{"gchat on the host, configured", gchatOnHost(spaces, false), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := missingNames(c.servers)
			if !slices.Contains(got, gitproxy.HostGitEntry) {
				t.Errorf("missing = %v, want %s, which no server entry names, always", got, gitproxy.HostGitEntry)
			}
			if slices.Contains(got, gchat) != c.wantChat {
				t.Errorf("missing = %v, want %s listed: %v", got, gchat, c.wantChat)
			}
		})
	}
}
