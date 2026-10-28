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
	"strings"
	"testing"
)

func TestSessionStoresOffClaim(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	for _, tc := range []struct {
		name string
		cfg  Config
		env  map[string]string
		want []string
	}{
		{name: "not kubernetes", cfg: Config{DataDir: "/data", GranuleSandboxes: true},
			env: map[string]string{"HOME": "/home/grain"}},
		{name: "the shipped granule manifest", cfg: Config{DataDir: "/data", K8sGranuleSandboxes: true},
			env: map[string]string{"HOME": "/data/home"}},
		{name: "CLAUDE_CONFIG_DIR on the claim", cfg: Config{DataDir: "/data", K8sSandboxes: true},
			env: map[string]string{"HOME": "/home/grain", "CLAUDE_CONFIG_DIR": "/data/claude"}},
		{name: "HOME on the pod's filesystem", cfg: Config{DataDir: "/data", K8sGranuleSandboxes: true},
			env: map[string]string{"HOME": "/home/grain"}, want: []string{"claude's session store /home/grain/.claude"}},
		{name: "a sibling of the data dir is not under it", cfg: Config{DataDir: "/data", K8sSandboxes: true},
			env: map[string]string{"HOME": "/data-home"}, want: []string{"claude's session store /data-home/.claude"}},
		{name: "run pod HOME on its claim", cfg: Config{DataDir: "/data", K8sSandboxes: true, RunHostImage: "self",
			RunHostCredentialDir: "/data/agent-home", K8sRunHostVolumes: []string{"grain-data:/data"}},
			env: map[string]string{"HOME": "/home/grain"}},
		{name: "run pod HOME off its claims", cfg: Config{DataDir: "/data", K8sSandboxes: true, RunHostImage: "self",
			RunHostCredentialDir: "/var/agent-home", K8sRunHostVolumes: []string{"grain-data:/data"}},
			want: []string{"a run pod's claude session store (its HOME, /var/agent-home)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionStoresOffClaim(tc.cfg, env(tc.env))
			if len(got) != len(tc.want) {
				t.Fatalf("sessionStoresOffClaim = %q, want %d finding(s) %q", got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("finding %d = %q, want it naming %q", i, got[i], w)
				}
			}
		})
	}
}
