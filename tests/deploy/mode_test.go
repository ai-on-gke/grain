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

// scripts/setup.sh's install mode (GRAIN_INSTALL_MODE): container is the
// only one left (task 5041). A re-run over a host-mode unit migrates it
// to container mode, said as the move it is; GRAIN_INSTALL_MODE=host is
// refused, pointing to container mode plus grain hostd. Run, not read, as
// ui_addr_test.go does: the block is sourced with the UI address block
// before it, whose installed_unit_flag it uses.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	modeBegin = "# --- the install mode (GRAIN_INSTALL_MODE)"
	modeEnd   = "# --- end of the install mode"
)

// hostExecStart is the unit host mode's write_host_daemon_unit wrote, as
// far as these functions read it.
const hostExecStart = "Environment=HOME=/var/lib/grain/data/home\nEnvironment=GRAIN_INSTALL_MODE=host\n" +
	"ExecStart=/usr/local/bin/grain daemon -data-dir /var/lib/grain/data -ui-addr 0.0.0.0:4242 "

// resolveMode runs resolve_install_mode over unit with mode as the given
// GRAIN_INSTALL_MODE, and returns its output, what it settled on, and
// whether it succeeded.
func resolveMode(t *testing.T, mode, unit string) (out, gotMode string, ok bool) {
	t.Helper()
	text := setupText(t)
	dir := t.TempDir()
	lib := filepath.Join(dir, "mode.sh")
	block := between(t, text, uiAddrBegin, uiAddrEnd) + "\n" + between(t, text, modeBegin, modeEnd)
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+
		"log() { echo \"==> $*\"; }\n"+
		"source \"$1\"\n"+
		"resolve_install_mode \"$2\" || exit 3\n"+
		"printf 'MODE=%s\\n' \"$GRAIN_INSTALL_MODE\"\n", "bash", lib, unit)
	cmd.Env = append(os.Environ(), "GRAIN_USER=grain", "GRAIN_INSTALL_MODE="+mode,
		"GRAIN_DATA_DIR="+filepath.Join(dir, "data"))
	b, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running bash: %v", err)
		}
	}
	out = string(b)
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(line, "MODE="); found {
			gotMode = v
		}
	}
	return out, gotMode, err == nil
}

func TestSetupModeFreshInstall(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, mode, ok := resolveMode(t, "", missing)
	if !ok || mode != "container" {
		t.Errorf("a fresh install settled on %q (ok=%v), want container:\n%s", mode, ok, out)
	}
	contains(t, out, "Install mode: container")
}

func TestSetupModeReRunMovesAHostInstallToContainerMode(t *testing.T) {
	for name, tc := range map[string]struct {
		execStart, given string
		migrates         bool
	}{
		"host mode":        {hostExecStart, "", true},
		"host mode, given": {hostExecStart, "container", true},
		// A host unit written before Environment=GRAIN_INSTALL_MODE=host
		// existed is still a host unit: its ExecStart is the binary.
		"host mode, older unit": {"ExecStart=/usr/local/bin/grain daemon \\\n  -data-dir /var/lib/grain/data", "", true},
		"container mode":        {installedExecStart + "-ui-addr 0.0.0.0:4242", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			out, mode, ok := resolveMode(t, tc.given, writeUnit(t, tc.execStart))
			if !ok || mode != "container" {
				t.Fatalf("a re-run settled on mode %q (ok=%v), want container:\n%s", mode, ok, out)
			}
			if got := strings.Contains(out, "moving this host-mode install to container mode plus grain hostd"); got != tc.migrates {
				t.Errorf("says it migrates = %v, want %v:\n%s", got, tc.migrates, out)
			}
			if tc.migrates {
				contains(t, out, `"operator"`)
			}
		})
	}
}

func TestSetupModeRefusesHostMode(t *testing.T) {
	for _, unit := range []string{filepath.Join(t.TempDir(), "grain-daemon.service"), writeUnit(t, hostExecStart)} {
		out, _, ok := resolveMode(t, "host", unit)
		if ok {
			t.Fatalf("GRAIN_INSTALL_MODE=host was accepted:\n%s", out)
		}
		contains(t, out, "GRAIN_INSTALL_MODE=host is no longer supported")
		contains(t, out, "container mode plus grain hostd")
		contains(t, out, "sudo ./scripts/setup.sh")
	}
}

func TestSetupModeRefusesWhatIsNeither(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, _, ok := resolveMode(t, "vm", missing)
	if ok {
		t.Errorf("GRAIN_INSTALL_MODE=vm was accepted:\n%s", out)
	}
	contains(t, out, `GRAIN_INSTALL_MODE must be "container" (or unset), not "vm"`)
}

// Settled before anything is installed.
func TestSetupResolvesModeBeforeInstalling(t *testing.T) {
	code := setupCode(t)
	before(t, code, "if ! resolve_install_mode /etc/systemd/system/grain-daemon.service", "ensure_docker() {",
		"the mode has to be settled before anything is installed")
}
