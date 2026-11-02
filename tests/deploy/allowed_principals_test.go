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

// scripts/setup.sh's IAP principal allowlist (GRAIN_ALLOWED_PRINCIPALS):
// comma-separated entries are normalized without spaces before reaching
// grain-daemon.service's unquoted ExecStart, malformed or flag-injecting
// entries stop the install before anything is written, a bare re-run keeps
// the installed unit's -ui-allowed-principals rather than silently reverting
// to the daemon's default, and an explicit empty value or "none" clears it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	allowedPrincipalsBegin = "# --- the allowed principals (GRAIN_ALLOWED_PRINCIPALS)"
	allowedPrincipalsEnd   = "# --- end of the allowed principals"
)

func resolveAllowedPrincipals(t *testing.T, given *string, unit string) (out, got string, ok bool) {
	t.Helper()
	text := setupText(t)
	givenBlock := between(t, text, "GRAIN_ALLOWED_PRINCIPALS_GIVEN=0", `GRAIN_ALLOWED_PRINCIPALS="${GRAIN_ALLOWED_PRINCIPALS:-}"`) +
		"\n" + `GRAIN_ALLOWED_PRINCIPALS="${GRAIN_ALLOWED_PRINCIPALS:-}"` + "\n"
	lib := filepath.Join(t.TempDir(), "allowed_principals.sh")
	block := "GRAIN_ALLOWED_PRINCIPALS_GIVEN=0\n" + givenBlock +
		shellFunction(t, "installed_unit_flag") +
		between(t, text, allowedPrincipalsBegin, allowedPrincipalsEnd)
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\n" +
		"log() { echo \"==> $*\"; }\n"
	if given == nil {
		script += "unset GRAIN_ALLOWED_PRINCIPALS\n"
	}
	script += "source \"$1\"\n" +
		"resolve_allowed_principals \"$2\" || exit 3\n" +
		"printf 'PRINCIPALS=%s\\n' \"$GRAIN_ALLOWED_PRINCIPALS\"\n"
	cmd := exec.Command("bash", "-c", script, "bash", lib, unit)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GRAIN_ALLOWED_PRINCIPALS=") && !strings.HasPrefix(kv, "GRAIN_ALLOWED_PRINCIPALS_GIVEN=") {
			env = append(env, kv)
		}
	}
	if given != nil {
		env = append(env, "GRAIN_ALLOWED_PRINCIPALS="+*given)
	}
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running bash: %v", err)
		}
	}
	out = string(b)
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(line, "PRINCIPALS="); found {
			got = v
		}
	}
	return out, got, err == nil
}

func strPtr(s string) *string { return &s }

func TestSetupAllowedPrincipalsFreshInstall(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, got, ok := resolveAllowedPrincipals(t, nil, missing)
	if !ok || got != "" {
		t.Fatalf("fresh install with unset GRAIN_ALLOWED_PRINCIPALS got (%q, ok=%v), want empty:\n%s", got, ok, out)
	}

	raw := " user:alice@example.com , group:ops@example.com,,domain:example.com, serviceAccount:ci@my-proj.iam.gserviceaccount.com "
	out, got, ok = resolveAllowedPrincipals(t, strPtr(raw), missing)
	if !ok {
		t.Fatalf("well-formed allowlist was refused:\n%s", out)
	}
	want := "user:alice@example.com,group:ops@example.com,domain:example.com,serviceAccount:ci@my-proj.iam.gserviceaccount.com"
	if got != want {
		t.Errorf("normalized to %q, want %q (no spaces for ExecStart)", got, want)
	}
	contains(t, out, "Allowed principals: "+want+" (as given)")
}

func TestSetupAllowedPrincipalsReRunKeepsInstalledAndExplicitClears(t *testing.T) {
	unit := writeUnit(t, installedExecStart+"-ui-allowed-principals user:alice@example.com,domain:example.com -default-target-repo acme/app")

	// Bare `sudo ./setup.sh` re-run keeps the installed allowlist.
	out, got, ok := resolveAllowedPrincipals(t, nil, unit)
	if !ok || got != "user:alice@example.com,domain:example.com" {
		t.Fatalf("bare re-run settled on (%q, ok=%v), want installed allowlist:\n%s", got, ok, out)
	}
	contains(t, out, "Allowed principals: keeping user:alice@example.com,domain:example.com from the installed")

	// Explicit empty string (such as deploy.sh with allowed_principals = []) clears it.
	out, got, ok = resolveAllowedPrincipals(t, strPtr(""), unit)
	if !ok || got != "" {
		t.Fatalf("explicit empty GRAIN_ALLOWED_PRINCIPALS settled on (%q, ok=%v), want empty:\n%s", got, ok, out)
	}
	contains(t, out, "Allowed principals: removing user:alice@example.com,domain:example.com")

	// Explicit "none" also clears it.
	out, got, ok = resolveAllowedPrincipals(t, strPtr("none"), unit)
	if !ok || got != "" {
		t.Fatalf("GRAIN_ALLOWED_PRINCIPALS=none settled on (%q, ok=%v), want empty:\n%s", got, ok, out)
	}
	contains(t, out, "Allowed principals: removing user:alice@example.com,domain:example.com")

	// Explicit replacement wins.
	out, got, ok = resolveAllowedPrincipals(t, strPtr("user:bob@example.com"), unit)
	if !ok || got != "user:bob@example.com" {
		t.Fatalf("replacement settled on (%q, ok=%v), want user:bob@example.com:\n%s", got, ok, out)
	}
	contains(t, out, "Allowed principals: changing from user:alice@example.com,domain:example.com")
}

func TestSetupAllowedPrincipalsRejectsMalformedAndFlagInjectionInputs(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	for _, bad := range []string{
		"   ",
		" , , ",
		"alice@example.com",
		"admin:alice@example.com",
		"user:",
		"user:not-an-email",
		"domain:alice@example.com",
		"domain:example.com/path",
		"user:alice@example.com -allow-unisolated-host-sandboxes",
		"user:alice@example.com\t-ui-require-iap=false",
		"user:alice$foo@example.com",
		"user:alice%n@example.com",
		`user:"alice"@example.com`,
	} {
		out, _, ok := resolveAllowedPrincipals(t, strPtr(bad), missing)
		if ok {
			t.Errorf("GRAIN_ALLOWED_PRINCIPALS=%q was accepted; output:\n%s", bad, out)
		}
	}

	code := setupCode(t)
	before(t, code, "resolve_allowed_principals /etc/systemd/system/grain-daemon.service", "write_systemd_units() {",
		"allowed principals must be resolved before grain-daemon.service is written")
	contains(t, body(t, code, "write_systemd_units() {"),
		`[ -n "$GRAIN_ALLOWED_PRINCIPALS" ] && daemon_args+=(-ui-allowed-principals "$GRAIN_ALLOWED_PRINCIPALS")`)
}
