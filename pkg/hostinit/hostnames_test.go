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
