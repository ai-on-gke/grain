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

// IAP audience wiring from deploy/terraform/gcp through files/deploy.sh and
// scripts/setup.sh to grain-daemon.service's -ui-iap-audience flag.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	iapAudienceBegin = "# --- the IAP audience (GRAIN_IAP_AUDIENCE)"
	iapAudienceEnd   = "# --- end of the IAP audience"
)

func resolveIAPAudience(t *testing.T, given *string, unit string) (out, got string, ok bool) {
	t.Helper()
	text := setupText(t)
	givenBlock := between(t, text, "GRAIN_IAP_AUDIENCE_GIVEN=0", `GRAIN_IAP_AUDIENCE="${GRAIN_IAP_AUDIENCE:-}"`) +
		"\n" + `GRAIN_IAP_AUDIENCE="${GRAIN_IAP_AUDIENCE:-}"` + "\n"
	lib := filepath.Join(t.TempDir(), "iap_audience.sh")
	block := "GRAIN_IAP_AUDIENCE_GIVEN=0\n" + givenBlock +
		shellFunction(t, "installed_unit_flag") +
		between(t, text, iapAudienceBegin, iapAudienceEnd)
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\n" +
		"log() { echo \"==> $*\"; }\n"
	if given == nil {
		script += "unset GRAIN_IAP_AUDIENCE\n"
	}
	script += "source \"$1\"\n" +
		"resolve_iap_audience \"$2\" || exit 3\n" +
		"printf 'AUDIENCE=%s\\n' \"$GRAIN_IAP_AUDIENCE\"\n"
	cmd := exec.Command("bash", "-c", script, "bash", lib, unit)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GRAIN_IAP_AUDIENCE=") && !strings.HasPrefix(kv, "GRAIN_IAP_AUDIENCE_GIVEN=") {
			env = append(env, kv)
		}
	}
	if given != nil {
		env = append(env, "GRAIN_IAP_AUDIENCE="+*given)
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
		if v, found := strings.CutPrefix(line, "AUDIENCE="); found {
			got = v
		}
	}
	return out, got, err == nil
}

func TestSetupIAPAudienceFreshInstall(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	out, got, ok := resolveIAPAudience(t, nil, missing)
	if !ok || got != "" {
		t.Fatalf("fresh install with unset GRAIN_IAP_AUDIENCE got (%q, ok=%v), want empty:\n%s", got, ok, out)
	}

	raw := " /projects/123456789/global/backendServices/987654321 , , /projects/123456789/apps/my-proj "
	out, got, ok = resolveIAPAudience(t, strPtr(raw), missing)
	if !ok {
		t.Fatalf("well-formed audience was refused:\n%s", out)
	}
	want := "/projects/123456789/global/backendServices/987654321,/projects/123456789/apps/my-proj"
	if got != want {
		t.Errorf("normalized to %q, want %q (no spaces for ExecStart)", got, want)
	}
	contains(t, out, "IAP audience: "+want+" (as given)")
}

func TestSetupIAPAudienceReRunKeepsInstalledAndExplicitClears(t *testing.T) {
	const aud = "/projects/123456789/global/backendServices/987654321"
	unit := writeUnit(t, installedExecStart+"-ui-iap-audience "+aud+" -default-target-repo acme/app")

	// Bare `sudo ./setup.sh` re-run keeps the installed audience.
	out, got, ok := resolveIAPAudience(t, nil, unit)
	if !ok || got != aud {
		t.Fatalf("bare re-run settled on (%q, ok=%v), want %q:\n%s", got, ok, aud, out)
	}
	contains(t, out, "IAP audience: keeping "+aud+" from the installed")

	// Explicit empty string (such as deploy.sh with expose_ui_publicly = false) clears it.
	out, got, ok = resolveIAPAudience(t, strPtr(""), unit)
	if !ok || got != "" {
		t.Fatalf("explicit empty GRAIN_IAP_AUDIENCE settled on (%q, ok=%v), want empty:\n%s", got, ok, out)
	}
	contains(t, out, "IAP audience: removing "+aud)

	// Explicit "none" also clears it.
	out, got, ok = resolveIAPAudience(t, strPtr("none"), unit)
	if !ok || got != "" {
		t.Fatalf("GRAIN_IAP_AUDIENCE=none settled on (%q, ok=%v), want empty:\n%s", got, ok, out)
	}
	contains(t, out, "IAP audience: removing "+aud)

	// Explicit replacement wins.
	const next = "/projects/123456789/global/backendServices/111222333"
	out, got, ok = resolveIAPAudience(t, strPtr(next), unit)
	if !ok || got != next {
		t.Fatalf("replacement settled on (%q, ok=%v), want %q:\n%s", got, ok, next, out)
	}
	contains(t, out, "IAP audience: changing from "+aud)
}

func TestSetupIAPAudienceRejectsMalformedAndFlagInjectionInputs(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "grain-daemon.service")
	for _, bad := range []string{
		"   ",
		" , , ",
		"-ui-require-iap=false",
		"/projects/1/global/backendServices/2 -allow-unisolated-host-sandboxes",
		"/projects/1/global/backendServices/2\t-ui-require-iap=false",
		"/projects/$PROJECT/global/backendServices/2",
		"/projects/1/global/backendServices/%n",
		`/projects/"1"/global/backendServices/2`,
		`/projects/1\2/global/backendServices/3`,
	} {
		out, _, ok := resolveIAPAudience(t, strPtr(bad), missing)
		if ok {
			t.Errorf("GRAIN_IAP_AUDIENCE=%q was accepted; output:\n%s", bad, out)
		}
	}

	code := setupCode(t)
	before(t, code, "resolve_iap_audience /etc/systemd/system/grain-daemon.service", "write_systemd_units() {",
		"IAP audience must be resolved before grain-daemon.service is written")
	contains(t, body(t, code, "write_systemd_units() {"), `daemon_args+=(-ui-iap-audience "$GRAIN_IAP_AUDIENCE")`)
}

func TestTerraformGCPWiresIAPAudienceWhenBackendExists(t *testing.T) {
	iapTF := read(t, "deploy", "terraform", "gcp", "iap.tf")
	contains(t, iapTF, `data "google_project" "this"`)
	contains(t, iapTF, `count      = var.expose_ui_publicly ? 1 : 0`)
	contains(t, iapTF, `resource "google_compute_instance_group_membership" "host"`)
	contains(t, iapTF, `iap_audience = var.expose_ui_publicly ? "/projects/${data.google_project.this[0].number}/global/backendServices/${google_compute_backend_service.ui[0].generated_id}" : ""`)

	instanceTF := read(t, "deploy", "terraform", "gcp", "instance.tf")
	contains(t, instanceTF, `iap_audience              = local.iap_audience`)
	// A CI serviceAccount deployer_member must never seed allowed_principals.
	contains(t, instanceTF, `startswith(var.deployer_member, "user:") ? [var.deployer_member] : []`)

	deploySH := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	contains(t, deploySH, `IAP_AUDIENCE="$(cfg iap_audience)"`)
	contains(t, deploySH, `GRAIN_IAP_AUDIENCE="$IAP_AUDIENCE"`)

	variablesTF := read(t, "deploy", "terraform", "gcp", "variables.tf")
	absent(t, variablesTF, "does not configure yet")

	readme := read(t, "deploy", "terraform", "gcp", "README.md")
	absent(t, readme, "does not set that yet")
}
