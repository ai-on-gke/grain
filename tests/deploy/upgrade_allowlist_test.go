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

// scripts/setup.sh's upgrade allowlist (GRAIN_UPGRADE_ALLOWED_BRANCHES /
// --upgrade-allowed-branches): the value an operator gives is written
// into grain-daemon.service as -upgrade-allowed-branches, a re-run that
// gives none keeps the one the installed unit already passes, and a
// malformed one stops the install before anything is written.
//
// Unlike most of this package these run the shell rather than read it:
// the block between the two markers below is self-contained functions,
// so it is sourced into a bash of its own with a scratch unit file and
// asked what it decides. The pattern check is also run against
// path.Match itself, which is what the daemon matches branches with --
// the shell's copy of that syntax is only worth having if it agrees.

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	allowlistBegin = "# --- the upgrade allowlist (GRAIN_UPGRADE_ALLOWED_BRANCHES)"
	allowlistEnd   = "# --- end of the upgrade allowlist"
)

// allowlistShell runs script after sourcing setup.sh's allowlist
// functions, under the same `set -euo pipefail` setup.sh runs under,
// with env added to the environment and args as $2, $3, .... It returns
// combined output and whether bash exited 0.
func allowlistShell(t *testing.T, env []string, script string, args ...string) (string, bool) {
	t.Helper()
	block := between(t, setupText(t), allowlistBegin, allowlistEnd)
	lib := filepath.Join(t.TempDir(), "allowlist.sh")
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{"-c", "set -euo pipefail\n" +
		"log() { echo \"==> $*\"; }\n" +
		"source \"$1\"\n" + script, "bash", lib}, args...)...)
	cmd.Env = append(append(os.Environ(), "GRAIN_UPGRADE_ALLOWED_BRANCHES="), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running bash: %v", err)
		}
	}
	return string(out), err == nil
}

// writeUnit puts a grain-daemon.service with execStart in a scratch
// directory and returns its path.
func writeUnit(t *testing.T, execStart string) string {
	t.Helper()
	unit := filepath.Join(t.TempDir(), "grain-daemon.service")
	text := "[Unit]\nDescription=grain\n\n[Service]\nExecStartPre=/bin/true\n" + execStart + "\nRestart=on-failure\n"
	if err := os.WriteFile(unit, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return unit
}

// resolveAllowlist runs resolve_upgrade_allowed_branches over unit (a
// path that need not exist) and returns what it printed and the
// GRAIN_UPGRADE_ALLOWED_BRANCHES it settled on.
func resolveAllowlist(t *testing.T, given, unit string) (output, value string, ok bool) {
	t.Helper()
	out, ok := allowlistShell(t, []string{"GRAIN_UPGRADE_ALLOWED_BRANCHES=" + given},
		"resolve_upgrade_allowed_branches \"$2\" || exit 3\n"+
			"printf 'RESOLVED=%s\\n' \"$GRAIN_UPGRADE_ALLOWED_BRANCHES\"\n", unit)
	if i := strings.LastIndex(out, "RESOLVED="); i >= 0 {
		return out[:i], strings.TrimSpace(out[i+len("RESOLVED="):]), ok
	}
	return out, "", ok
}

const installedExecStart = "ExecStart=/usr/bin/docker run --name grain-daemon --rm ${GRAIN_IMAGE} daemon -data-dir /var/lib/grain/data "

func TestSetupWritesTheGivenUpgradeAllowlist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, value, ok := resolveAllowlist(t, " main , latest,,release/* ", missing)
	if !ok {
		t.Fatalf("a well-formed allowlist was refused:\n%s", out)
	}
	if value != "main,latest,release/*" {
		t.Errorf("resolved to %q, want the given patterns as the daemon reads them, %q", value, "main,latest,release/*")
	}
	contains(t, out, "Upgrade allowlist: main,latest,release/* (as given)")

	// Nothing given and nothing installed: no flag, the build's default.
	out, value, ok = resolveAllowlist(t, "", missing)
	if !ok || value != "" {
		t.Errorf("with nothing given or installed, got ok=%v value=%q, want the daemon's own default (empty):\n%s", ok, value, out)
	}

	// And the value reaches the unit's own daemon arguments.
	code := setupCode(t)
	contains(t, body(t, code, "write_systemd_units() {"),
		`daemon_args+=(-upgrade-allowed-branches "$GRAIN_UPGRADE_ALLOWED_BRANCHES")`)
	for _, want := range []string{"--upgrade-allowed-branches=*)", "--upgrade-allowed-branches)"} {
		contains(t, code, want)
	}
	// Resolved before anything on this host is written.
	before(t, code, "resolve_upgrade_allowed_branches /etc/systemd/system/grain-daemon.service", "write_systemd_units() {",
		"the allowlist has to be resolved (and refused) before the unit is written")
	before(t, code, "resolve_upgrade_allowed_branches /etc/systemd/system/grain-daemon.service", "ensure_docker() {",
		"a malformed allowlist has to stop the install before anything is installed")
}

func TestSetupKeepsTheInstalledUpgradeAllowlist(t *testing.T) {
	for name, execStart := range map[string]string{
		"as setup writes it": installedExecStart + "-upgrade-allowed-branches main,candidate,latest -reboot-cmd touch",
		"attached with =":    installedExecStart + "--upgrade-allowed-branches=main,candidate,latest",
		"quoted":             installedExecStart + `-upgrade-allowed-branches "main,candidate,latest"`,
		"last one wins":      installedExecStart + "-upgrade-allowed-branches main -upgrade-allowed-branches main,candidate,latest",
		"continued line":     "ExecStart=/usr/local/bin/grain daemon \\\n  -data-dir /var/lib/grain/data \\\n  -upgrade-allowed-branches main,candidate,latest",
	} {
		t.Run(name, func(t *testing.T) {
			unit := writeUnit(t, execStart)
			out, value, ok := resolveAllowlist(t, "", unit)
			if !ok {
				t.Fatalf("re-run refused the installed allowlist:\n%s", out)
			}
			if value != "main,candidate,latest" {
				t.Errorf("re-run settled on %q, want the installed unit's main,candidate,latest:\n%s", value, out)
			}
			contains(t, out, "keeping main,candidate,latest from the installed "+unit)
		})
	}

	// A unit that passes no allowlist leaves the default alone.
	out, value, ok := resolveAllowlist(t, "", writeUnit(t, installedExecStart+"-reboot-cmd touch"))
	if !ok || value != "" {
		t.Errorf("a unit with no -upgrade-allowed-branches gave ok=%v value=%q, want the default (empty):\n%s", ok, value, out)
	}
}

func TestSetupOverridesTheInstalledUpgradeAllowlist(t *testing.T) {
	unit := writeUnit(t, installedExecStart+"-upgrade-allowed-branches main,latest")
	out, value, ok := resolveAllowlist(t, "main,**", unit)
	if !ok {
		t.Fatalf("a well-formed override was refused:\n%s", out)
	}
	if value != "main,**" {
		t.Errorf("settled on %q, want the given main,** over the installed main,latest", value)
	}
	absent(t, out, "keeping")
	contains(t, out, "WARNING: ** lets this deployment upgrade to any branch")
}

func TestSetupRefusesAMalformedUpgradeAllowlist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	for _, bad := range []string{"main,release/[", "feature/[]x", "main latest", "main,$(id)", ",, ,", `main\x`} {
		out, value, ok := resolveAllowlist(t, bad, missing)
		if ok {
			t.Errorf("%q was accepted (as %q); setup.sh must refuse it rather than write a unit with it", bad, value)
			continue
		}
		contains(t, out, "setup.sh: upgrade allowlist")
	}

	// A malformed value already in the unit is refused too, with how to
	// replace it -- not silently dropped, and not silently kept.
	out, _, ok := resolveAllowlist(t, "", writeUnit(t, installedExecStart+"-upgrade-allowed-branches main,rel[ease"))
	if ok {
		t.Fatalf("a malformed installed allowlist was carried over:\n%s", out)
	}
	contains(t, out, "pass --upgrade-allowed-branches")
}

// The shell's pattern check agrees with path.Match, which is what the
// daemon matches a branch against (pkg/upgrade's Upgrader.Allowed): every
// pattern here is either refused by both or accepted by both.
func TestSetupsUpgradePatternCheckAgreesWithPathMatch(t *testing.T) {
	patterns := []string{
		"main", "candidate", "latest", "**", "*", "v[0-9]*.[0-9]*.[0-9]*", "release/*", "feature/?x",
		"[abc]", "[^abc]", "[a-z]", "[a-z0-9]x", "[]", "[]a]", "[^]", "[-a]", "[a-]", "[a--]", "[a-z",
		"[", "x[", "a]", "]", "[[]", "[a][b]", "[a][", "a*[", "*[a-", "[^a-z]*", "[a-b-c]", "[ab-]",
		"^main", "x^y", "[a^]", "release/*/[0-9]",
	}
	var script strings.Builder
	for i := range patterns {
		script.WriteString("p=${" + strconv.Itoa(i+2) + "}\n")
		script.WriteString(`if [ -n "$(upgrade_branch_pattern_problem "$p")" ]; then echo bad; else echo ok; fi` + "\n")
	}
	out, ok := allowlistShell(t, nil, script.String(), patterns...)
	if !ok {
		t.Fatalf("checking patterns failed:\n%s", out)
	}
	got := strings.Fields(out)
	if len(got) != len(patterns) {
		t.Fatalf("got %d answers for %d patterns:\n%s", len(got), len(patterns), out)
	}
	for i, p := range patterns {
		_, err := path.Match(p, "main")
		want := "ok"
		if err != nil {
			want = "bad"
		}
		if got[i] != want {
			t.Errorf("pattern %q: setup.sh says %s, path.Match says %s (err %v)", p, got[i], want, err)
		}
	}
}
