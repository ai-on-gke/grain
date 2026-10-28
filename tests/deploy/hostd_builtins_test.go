package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/hostd"
)

// googleCitc is the entry the Google build's citc registers
// (intree.HostdBuiltins in that build; google/capability/citc's own test
// holds the two equal). Core grain may not import google/.
var googleCitc = hostd.BuiltinEntry{Name: "grain-citc", Args: []string{"capability", "citc"}, ExtraArgs: true}

// builtinsFor is intree.HostdBuiltins as the track's binary has it: the
// core build's, which registers no citc, and on the Google track that
// plus citc's.
func builtinsFor(track string) []hostd.BuiltinEntry {
	out := intree.HostdBuiltins()
	for _, b := range out {
		if b.Name == googleCitc.Name {
			panic("the core build registers grain-citc")
		}
	}
	if track == "google" {
		out = append(out, googleCitc)
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
// the binary's -ensure-builtins adds (intree.HostdBuiltins), on both
// tracks: setup.sh's heredocs and the Go list cannot drift.
func TestSetupWritesGrainsOwnHostdEntriesAsTheBinaryDoes(t *testing.T) {
	const bin = "/usr/local/bin/grain"
	fns := proxyVarsLine(t) + shellFunction(t, "hostd_host_git_entry") + shellFunction(t, "hostd_citc_entry") +
		shellFunction(t, "hostd_builtin_capability_entry") + shellFunction(t, "hostd_allowlist_content")
	for _, track := range []string{"google", "oss"} {
		t.Run(track, func(t *testing.T) {
			env := []string{"GRAIN_INSTALL_TRACK=" + track, "HOSTD_AGY_PATH=", "GRAIN_DATA_DIR=/d", "GRAIN_SANDBOX_DIR=/s", "HOST_GRAIN_BIN=" + bin}
			content := runBash(t, fns+"hostd_allowlist_content", env...)
			a, err := hostd.ParseAllowlist([]byte(content))
			if err != nil {
				t.Fatalf("hostd refuses setup.sh's allowlist: %v\n%s", err, content)
			}
			want := builtinsFor(track)
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
		})
	}
	// And each ensure_ function's own entry.
	for name, fn := range map[string]string{"host-git": "hostd_host_git_entry", "grain-citc": "hostd_citc_entry", "grain-gcp": "hostd_builtin_capability_entry gcp"} {
		got := runBash(t, fns+fn, "HOST_GRAIN_BIN="+bin)
		for _, b := range builtinsFor("google") {
			if b.Name == name && got != b.YAML(bin, "  ", "  ") {
				t.Errorf("%s = %q, want %q", fn, got, b.YAML(bin, "  ", "  "))
			}
		}
	}
}

// An allowlist an older release's installer wrote, before grain-citc
// existed, gains it across an upgrade: the new build's
// grain-hostd-allowlist.service (started before hostd's next start, and
// by the upgrade step) runs -ensure-builtins, which adds it and leaves
// everything else as it was.
func TestAnOlderAllowlistGainsCitcAcrossAnUpgrade(t *testing.T) {
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

	// An older Google allowlist: an operator's agy and own server, a
	// grain-gcp they changed, and no grain-citc.
	older := `# grain hostd's allowlist
clis:
  agy:
    path: /google/bin/agy
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
	res, err := hostd.EnsureBuiltins(allow, bin, builtinsFor("google"))
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
	if e := a.Lookup(hostd.KindCapability, "grain-citc"); e == nil || strings.Join(e.Args, " ") != "capability citc" {
		t.Fatalf("no grain-citc after the upgrade (added %v):\n%s", res.Added, data)
	}
	if _, err := a.Check(&hostd.Request{Kind: hostd.KindCapability, Name: "grain-citc", Args: []string{"-listen", "127.0.0.1:4250"}}, nil); err != nil {
		t.Errorf("the added grain-citc refuses the built-in's -listen: %v", err)
	}
	if strings.Join(a.Capabilities["grain-gcp"].Args, " ") != "capability gcp -project mine" {
		t.Error("the operator's grain-gcp was changed")
	}
	if len(res.Differs) != 2 {
		t.Errorf("differs = %q, want host-git's pass_env and grain-gcp's args reported", res.Differs)
	}
	// A core build adds no citc.
	core := filepath.Join(dir, "core.yaml")
	if err := os.WriteFile(core, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := hostd.EnsureBuiltins(core, bin, builtinsFor("oss")); err != nil || strings.Contains(strings.Join(res.Added, " "), "citc") {
		t.Errorf("the core build added %v (%v)", res.Added, err)
	}
	// The next start's run changes nothing.
	if res, err := hostd.EnsureBuiltins(allow, bin, builtinsFor("google")); err != nil || len(res.Added) != 0 {
		t.Errorf("a second run added %v (%v)", res.Added, err)
	}
	if again, _ := os.ReadFile(allow); string(again) != string(data) {
		t.Errorf("a second run changed the allowlist")
	}
}
