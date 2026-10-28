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

// scripts/setup.sh's local policy (GRAIN_POLICY): a re-run that names
// none keeps the installed unit's -policy and says so, as it does for
// -ui-addr -- dropping it silently dispatched every task the owner's
// policy was holding (task 4497). Run, not read, as mode_test.go does: the block is sourced with the UI address
// block and installed_unit_flag, which both blocks read.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	policyBegin = "# --- the local policy (GRAIN_POLICY)"
	policyEnd   = "# --- end of the local policy"
)

// resolvePolicy runs resolve_policy over unit with policy as the given
// GRAIN_POLICY, and returns its output, what it settled on, and whether
// it succeeded.
func resolvePolicy(t *testing.T, policy, unit string) (out, got string, ok bool) {
	t.Helper()
	text := setupText(t)
	lib := filepath.Join(t.TempDir(), "policy.sh")
	block := shellFunction(t, "installed_unit_flag") + between(t, text, uiAddrBegin, uiAddrEnd) + "\n" + between(t, text, policyBegin, policyEnd)
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+
		"log() { echo \"==> $*\"; }\n"+
		"source \"$1\"\n"+
		"resolve_policy \"$2\" || exit 3\n"+
		"printf 'POLICY=%s\\n' \"$GRAIN_POLICY\"\n", "bash", lib, unit)
	cmd.Env = append(os.Environ(), "GRAIN_DATA_DIR=/var/lib/grain/data", "GRAIN_INSTALL_MODE=container", "GRAIN_POLICY="+policy)
	b, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running bash: %v", err)
		}
	}
	out = string(b)
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(line, "POLICY="); found {
			got = v
		}
	}
	return out, got, err == nil
}

const installedPolicy = "/var/lib/grain/data/grain_policy.yml"

func TestSetupPolicyFreshInstallHasNone(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, got, ok := resolvePolicy(t, "", missing)
	if !ok || got != "" {
		t.Errorf("a fresh install settled on -policy %q (ok=%v), want none:\n%s", got, ok, out)
	}
}

func TestSetupPolicyReRunKeepsTheInstalledOne(t *testing.T) {
	for name, execStart := range map[string]string{
		"container":       installedExecStart + "-tenancy single -policy " + installedPolicy + " -ui-addr 0.0.0.0:4242",
		"host, continued": hostExecStart + "\\\n  -policy=" + installedPolicy + " \\\n  -tenancy single",
	} {
		t.Run(name, func(t *testing.T) {
			out, got, ok := resolvePolicy(t, "", writeUnit(t, execStart))
			if !ok || got != installedPolicy {
				t.Fatalf("a bare re-run settled on -policy %q (ok=%v), want the installed %q:\n%s", got, ok, installedPolicy, out)
			}
			contains(t, out, "Local policy: keeping "+installedPolicy+" from the installed")
		})
	}
}

func TestSetupPolicyGivenAndRemoved(t *testing.T) {
	unit := writeUnit(t, installedExecStart+"-policy "+installedPolicy)

	out, got, ok := resolvePolicy(t, "/var/lib/grain/data/other.yml", unit)
	if !ok || got != "/var/lib/grain/data/other.yml" {
		t.Fatalf("settled on %q (ok=%v), want the given file:\n%s", got, ok, out)
	}
	contains(t, out, "Local policy: changing from "+installedPolicy+" (the installed")

	out, got, ok = resolvePolicy(t, "none", unit)
	if !ok || got != "" {
		t.Fatalf("GRAIN_POLICY=none settled on %q (ok=%v), want none:\n%s", got, ok, out)
	}
	contains(t, out, "Local policy: removing "+installedPolicy)

	out, _, ok = resolvePolicy(t, "grain_policy.yml", unit)
	if ok {
		t.Errorf("a relative GRAIN_POLICY was accepted:\n%s", out)
	}
	contains(t, out, `GRAIN_POLICY must be an absolute path or none, not "grain_policy.yml"`)

	// A container-mode daemon sees the data directory alone.
	out, _, _ = resolvePolicy(t, "/etc/grain_policy.yml", unit)
	contains(t, out, "WARNING: the local policy /etc/grain_policy.yml is outside /var/lib/grain/data")
}

// Settled before the unit is written, and written into it.
func TestSetupWritesTheResolvedPolicy(t *testing.T) {
	code := setupCode(t)
	before(t, code, "if ! resolve_policy /etc/systemd/system/grain-daemon.service", "write_systemd_units() {",
		"the policy has to be settled before the unit is written")
	contains(t, code, `daemon_args+=(-policy "$GRAIN_POLICY")`)
}
