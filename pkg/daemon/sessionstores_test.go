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
