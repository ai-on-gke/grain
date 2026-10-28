package hostinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The machine's names are the fully qualified one and the short one, as
// setup.sh's `hostname -f`, `hostname -s` -- a short hostname is
// qualified from the hosts file, the way `hostname -f` does it.
func TestMachineHostnamesQualifyAShortName(t *testing.T) {
	for _, c := range []struct {
		name, canonical, want string
	}{
		{"grain-vm", "grain-vm.example.internal", "grain-vm.example.internal,grain-vm"},
		{"grain-vm.example.internal", "", "grain-vm.example.internal,grain-vm"},
		{"grain-vm", "", "grain-vm"},
		{"grain-vm", "grain-vm", "grain-vm"},
		{"grain-vm", "other.example.internal", "grain-vm"},
	} {
		if got := strings.Join(hostnamesFor(c.name, c.canonical), ","); got != c.want {
			t.Errorf("hostnamesFor(%q, %q) = %q, want %q", c.name, c.canonical, got, c.want)
		}
	}

	hosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n# a comment grain-vm\n127.0.1.1\tgrain-vm.example.internal grain-vm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := hostsFilePath
	hostsFilePath = hosts
	t.Cleanup(func() { hostsFilePath = prev })
	if got := canonicalHostname("grain-vm"); got != "grain-vm.example.internal" {
		t.Errorf("canonicalHostname = %q, want the hosts file's grain-vm.example.internal", got)
	}
}
