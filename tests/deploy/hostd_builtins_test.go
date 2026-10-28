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

package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// extraWorkspace is a fixture entry standing in for an edition-registered
// host capability server (intree.Register) across an upgrade.
var extraWorkspace = hostd.BuiltinEntry{Name: "grain-workspace", Args: []string{"capability", "workspace"}, ExtraArgs: true}

// builtinsFor is intree.HostdBuiltins as the binary has it, plus extra
// when withExtra is true.
func builtinsFor(withExtra bool) []hostd.BuiltinEntry {
	out := intree.HostdBuiltins()
	if withExtra {
		out = append(out, extraWorkspace)
	}
	return out
}

// proxyVarsLine is setup.sh's own HOSTD_PROXY_VARS assignment.
func proxyVarsLine(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^HOSTD_PROXY_VARS=\(([^)]*)\)$`).FindStringSubmatch(setupText(t))
	if m == nil {
		t.Fatal("setup.sh has no HOSTD_PROXY_VARS")
	}
	if got := strings.Fields(m[1]); !reflect.DeepEqual(got, hostd.ProxyVars) {
		t.Errorf("setup.sh's HOSTD_PROXY_VARS = %v, pkg/hostd's ProxyVars = %v", got, hostd.ProxyVars)
	}
	return m[0] + "\n"
}

// The installer's entries for grain's own servers are exactly the ones
// the binary's -ensure-builtins adds (intree.HostdBuiltins): setup.sh's
// heredocs and the Go list cannot drift.
func TestSetupWritesGrainsOwnHostdEntriesAsTheBinaryDoes(t *testing.T) {
	const bin = "/usr/local/bin/grain"
	fns := proxyVarsLine(t) + shellFunction(t, "hostd_host_git_entry") +
		shellFunction(t, "hostd_builtin_capability_entry") + shellFunction(t, "hostd_allowlist_content")
	env := []string{"GRAIN_INSTALL_TRACK=oss", "HOSTD_AGY_PATH=", "GRAIN_DATA_DIR=/d", "GRAIN_SANDBOX_DIR=/s", "HOST_GRAIN_BIN=" + bin}
	content := runBash(t, fns+"hostd_allowlist_content", env...)
	a, err := hostd.ParseAllowlist([]byte(content))
	if err != nil {
		t.Fatalf("hostd refuses setup.sh's allowlist: %v\n%s", err, content)
	}
	want := builtinsFor(false)
	if len(a.Capabilities) != len(want) {
		t.Errorf("setup.sh writes %d capabilities entries, the binary knows %d:\n%s", len(a.Capabilities), len(want), content)
	}
	for _, b := range want {
		have := a.Capabilities[b.Name]
		if have == nil || !reflect.DeepEqual(have, b.Entry(bin)) {
			t.Errorf("%s: setup.sh writes %+v, the binary %+v", b.Name, have, b.Entry(bin))
		}
		// The text too, entry by entry: what an operator is told to
		// copy is the same whichever wrote it.
		if !strings.Contains(content, b.YAML(bin, "  ", "  ")) {
			t.Errorf("%s: setup.sh's text differs from the binary's:\n%s\nwant:\n%s", b.Name, content, b.YAML(bin, "  ", "  "))
		}
	}
	// And each ensure_ function's own entry.
	for name, fn := range map[string]string{"host-git": "hostd_host_git_entry", "grain-gcp": "hostd_builtin_capability_entry gcp"} {
		got := runBash(t, fns+fn, "HOST_GRAIN_BIN="+bin)
		for _, b := range want {
			if b.Name == name && got != b.YAML(bin, "  ", "  ") {
				t.Errorf("%s = %q, want %q", fn, got, b.YAML(bin, "  ", "  "))
			}
		}
	}
}

// An allowlist an older release's installer wrote, before a newly
// registered capability server existed, gains it across an upgrade: the
// new build's grain-hostd-allowlist.service (started before hostd's next
// start, and by the upgrade step) runs -ensure-builtins, which adds it and
// leaves everything else as it was.
func TestAnOlderAllowlistGainsNewBuiltinsAcrossAnUpgrade(t *testing.T) {
	const bin = "/usr/local/bin/grain"
	dir := t.TempDir()

	// What the upgrade runs, and when.
	unitFile := filepath.Join(dir, "grain-hostd-allowlist.service")
	allow := filepath.Join(dir, "hostd.yaml")
	unit := runBash(t, shellFunction(t, "write_hostd_allowlist_unit")+`write_hostd_allowlist_unit; cat "$HOSTD_ALLOWLIST_UNIT"`,
		"HOSTD_ALLOWLIST_UNIT="+unitFile, "HOSTD_ALLOWLIST="+allow, "HOSTD_VERSIONS_DIR=/usr/local/lib/grain/hostd", "HOST_GRAIN_BIN="+bin)
	contains(t, unit, "ExecStart=/usr/local/lib/grain/hostd/current/grain hostd -ensure-builtins -allowlist "+allow+" -command "+bin)
	for _, hardening := range []string{"ProtectSystem=strict", "ReadWritePaths=" + dir, "ProtectHome=yes", "PrivateNetwork=yes", "NoNewPrivileges=yes"} {
		contains(t, unit, hardening)
	}
	units := body(t, setupText(t), "write_hostd_units() {")
	contains(t, units, "ExecStartPre=-+")
	contains(t, units, "start grain-hostd-allowlist.service")
	contains(t, body(t, setupText(t), "write_hostd_upgrade_script() {"), "systemctl start grain-hostd-allowlist.service")

	// An older allowlist: an operator's agy and own server, a grain-gcp
	// they changed, and no grain-workspace.
	older := `# grain hostd's allowlist
clis:
  agy:
    path: /opt/acme/bin/agy
capabilities:
  corp-search:
    command: /usr/local/bin/corp-search-mcp
    args: [--stdio]
  host-git:
    command: /usr/local/bin/grain
    args: [capability, host-git]
    pass_env: [HTTPS_PROXY]
  grain-gcp:
    command: /usr/local/bin/grain
    args: [capability, gcp, -project, mine]
`
	if err := os.WriteFile(allow, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := hostd.EnsureBuiltins(allow, bin, builtinsFor(true))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(allow)
	if !strings.HasPrefix(string(data), older) {
		t.Errorf("the operator's allowlist moved:\n%s", data)
	}
	a, err := hostd.ParseAllowlist(data)
	if err != nil {
		t.Fatalf("hostd refuses the upgraded allowlist: %v\n%s", err, data)
	}
	if e := a.Lookup(hostd.KindCapability, "grain-workspace"); e == nil || strings.Join(e.Args, " ") != "capability workspace" {
		t.Fatalf("no grain-workspace after the upgrade (added %v):\n%s", res.Added, data)
	}
	if _, err := a.Check(&hostd.Request{Kind: hostd.KindCapability, Name: "grain-workspace", Args: []string{"-listen", "127.0.0.1:4250"}}, nil); err != nil {
		t.Errorf("the added grain-workspace refuses the built-in's -listen: %v", err)
	}
	if strings.Join(a.Capabilities["grain-gcp"].Args, " ") != "capability gcp -project mine" {
		t.Error("the operator's grain-gcp was changed")
	}
	if len(res.Differs) != 2 {
		t.Errorf("differs = %q, want host-git's pass_env and grain-gcp's args reported", res.Differs)
	}
	// A build without the extra registration adds no grain-workspace.
	core := filepath.Join(dir, "core.yaml")
	if err := os.WriteFile(core, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := hostd.EnsureBuiltins(core, bin, builtinsFor(false)); err != nil || strings.Contains(strings.Join(res.Added, " "), "workspace") {
		t.Errorf("the core build added %v (%v)", res.Added, err)
	}
	// The next start's run changes nothing.
	if res, err := hostd.EnsureBuiltins(allow, bin, builtinsFor(true)); err != nil || len(res.Added) != 0 {
		t.Errorf("a second run added %v (%v)", res.Added, err)
	}
	if again, _ := os.ReadFile(allow); string(again) != string(data) {
		t.Errorf("a second run changed the allowlist")
	}
}
