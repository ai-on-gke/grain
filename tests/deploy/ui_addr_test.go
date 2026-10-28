package deploy

// scripts/setup.sh's UI address (GRAIN_UI_ADDR, GRAIN_UI_ALLOWED_HOSTS):
// a fresh install binds 0.0.0.0:4242, a re-run keeps whatever the
// installed unit already passes, an explicit value wins, and on an
// all-interfaces bind the Host check defaults to this machine's own
// names. Run, not read: the block between the markers is sourced into a
// bash of its own with a scratch unit, as upgrade_allowlist_test.go does.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	uiAddrBegin = "# --- the UI address (GRAIN_UI_ADDR, GRAIN_UI_ALLOWED_HOSTS)"
	uiAddrEnd   = "# --- end of the UI address"
)

// resolveUIAddr runs resolve_ui_addr over unit with addr and hosts as
// the given GRAIN_UI_ADDR and GRAIN_UI_ALLOWED_HOSTS, and returns its
// output and what it settled on.
func resolveUIAddr(t *testing.T, addr, hosts, unit string) (out, gotAddr, gotHosts string) {
	t.Helper()
	lib := filepath.Join(t.TempDir(), "uiaddr.sh")
	if err := os.WriteFile(lib, []byte(shellFunction(t, "installed_unit_flag")+between(t, setupText(t), uiAddrBegin, uiAddrEnd)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+
		"log() { echo \"==> $*\"; }\n"+
		"source \"$1\"\n"+
		"resolve_ui_addr \"$2\"\n"+
		"printf 'ADDR=%s\\nHOSTS=%s\\n' \"$GRAIN_UI_ADDR\" \"$GRAIN_UI_ALLOWED_HOSTS\"\n", "bash", lib, unit)
	cmd.Env = append(os.Environ(), "GRAIN_UI_ADDR="+addr, "GRAIN_UI_ALLOWED_HOSTS="+hosts)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve_ui_addr: %v\n%s", err, b)
	}
	out = string(b)
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "ADDR="); ok {
			gotAddr = v
		}
		if v, ok := strings.CutPrefix(line, "HOSTS="); ok {
			gotHosts = v
		}
	}
	return out, gotAddr, gotHosts
}

func TestSetupUIAddrFreshInstall(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, addr, hosts := resolveUIAddr(t, "", "", missing)
	if addr != "0.0.0.0:4242" {
		t.Errorf("a fresh install binds %q, want 0.0.0.0:4242", addr)
	}
	if want := machineNames(t); hosts != want {
		t.Errorf("allowed hosts defaulted to %q, want this machine's own names %q", hosts, want)
	}
	contains(t, out, "the default for a fresh install")
	contains(t, out, "this machine's own names")
}

func TestSetupUIAddrReRunKeepsTheInstalledOne(t *testing.T) {
	for name, execStart := range map[string]string{
		"container mode": installedExecStart + "-ui-addr 127.0.0.1:80 -ui-allowed-hosts grain.corp.example -reboot-cmd touch",
		"host mode":      "ExecStart=/usr/local/bin/grain daemon \\\n  -data-dir /var/lib/grain/data \\\n  -ui-addr=127.0.0.1:80 \\\n  -ui-allowed-hosts \"grain.corp.example\"",
	} {
		t.Run(name, func(t *testing.T) {
			out, addr, hosts := resolveUIAddr(t, "", "", writeUnit(t, execStart))
			if addr != "127.0.0.1:80" || hosts != "grain.corp.example" {
				t.Errorf("a re-run settled on %q / %q, want the installed 127.0.0.1:80 / grain.corp.example:\n%s", addr, hosts, out)
			}
			contains(t, out, "keeping 127.0.0.1:80 from the installed")
		})
	}

	// A loopback bind with no hosts of its own gets none added.
	_, addr, hosts := resolveUIAddr(t, "", "", writeUnit(t, installedExecStart+"-ui-addr 127.0.0.1:80"))
	if addr != "127.0.0.1:80" || hosts != "" {
		t.Errorf("settled on %q / %q, want 127.0.0.1:80 and no allowed hosts", addr, hosts)
	}
}

func TestSetupUIAddrExplicitWins(t *testing.T) {
	unit := writeUnit(t, installedExecStart+"-ui-addr 127.0.0.1:80 -ui-allowed-hosts grain.corp.example")
	out, addr, hosts := resolveUIAddr(t, "0.0.0.0:9000", "a.example,b.example", unit)
	if addr != "0.0.0.0:9000" || hosts != "a.example,b.example" {
		t.Errorf("settled on %q / %q, want the given 0.0.0.0:9000 / a.example,b.example", addr, hosts)
	}
	contains(t, out, "(as given)")
}

// Resolved before the unit is written, and handed to it.
func TestSetupWritesTheResolvedUIAddr(t *testing.T) {
	code := setupCode(t)
	before(t, code, "resolve_ui_addr /etc/systemd/system/grain-daemon.service", "write_systemd_units() {",
		"the UI address has to be settled before the unit is written")
	units := body(t, code, "write_systemd_units() {")
	contains(t, units, `-ui-addr "$GRAIN_UI_ADDR"`)
	contains(t, units, `daemon_args+=(-ui-allowed-hosts "$GRAIN_UI_ALLOWED_HOSTS")`)
	// The readiness report says the exposure out loud.
	report := body(t, code, "report_readiness() {")
	for _, want := range []string{"ui_network_url", "ui_firewall_note", "identity proxy (IAP)", "only localhost"} {
		contains(t, report, want)
	}
}
