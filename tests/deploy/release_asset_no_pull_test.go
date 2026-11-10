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
	"strings"
	"testing"
)

// A release-asset image (grain/grain:sha-..., grain/guest:sha-...,
// grain/k8s-sandbox:sha-...: docs/release-assets.md) names no registry
// host, so docker reads it as docker.io/grain/..., a Docker Hub namespace
// this project does not control. setup.sh loads it from the release and
// checks it against images.json; nothing it runs or leaves behind may
// pull that name, explicitly or by the implicit pull of `docker run` /
// `docker create` of a missing image.

func TestReleaseAssetRefIsTheContractsLocalRefsOnly(t *testing.T) {
	fn := shellFunction(t, "release_asset_ref")
	for ref, want := range map[string]bool{
		"grain/grain:sha-0123456789ab":                  true,
		"grain/guest:sha-0123456789ab":                  true,
		"grain/k8s-sandbox:sha-0123456789ab":            true,
		"docker.io/grainai/grain:main":                  false,
		"docker.io/grain/guest:sha-0123456789ab":        false,
		"registry.example/grain/guest:sha-0123456789ab": false,
		"grainai/grain:main":                            false,
		"":                                              false,
	} {
		got := strings.TrimSpace(runBash(t, fn+`if release_asset_ref "$REF"; then echo yes; else echo no; fi`, "REF="+ref)) == "yes"
		if got != want {
			t.Errorf("release_asset_ref %q = %v, want %v", ref, got, want)
		}
	}
}

// ensure_granule_oci_image used to `docker pull` the sandbox image
// unconditionally: for a release-asset install that asks Docker Hub for
// grain/guest:sha-..., and a pull that succeeded would replace the image
// an earlier release's release-asset install had checked.
func TestGranuleSandboxImageFromReleaseAssetsIsNeverPulled(t *testing.T) {
	fn := shellFunction(t, "release_asset_ref") + shellFunction(t, "ensure_granule_oci_image")
	stubs := `
log() { echo "log: $*"; }
granule_unavailable() { echo "unavailable: $1"; GRAIN_GRANULE_ENABLE=0; }
docker() {
  echo "docker $*" >> "$DOCKER_LOG"
  if [ "$1" = image ] && [ "$2" = inspect ]; then [ "$LOADED" = 1 ]; return; fi
  return 0
}
`
	for _, loaded := range []string{"1", "0"} {
		log := t.TempDir() + "/docker.log"
		out := runBash(t, stubs+fn+`
: > "$DOCKER_LOG"
GRAIN_GRANULE_ENABLE=1
GRAIN_GRANULE_OCI_IMAGE=grain/guest:sha-0123456789ab
ensure_granule_oci_image
echo "enabled=$GRAIN_GRANULE_ENABLE"
cat "$DOCKER_LOG"
`, "DOCKER_LOG="+log, "LOADED="+loaded)
		if strings.Contains(out, "docker pull") || strings.Contains(out, "docker login") {
			t.Errorf("loaded=%s: ensure_granule_oci_image reached the registry for a release-asset image:\n%s", loaded, out)
		}
		wantEnabled := map[string]string{"1": "enabled=1", "0": "enabled=0"}[loaded]
		if !strings.Contains(out, wantEnabled) {
			t.Errorf("loaded=%s: want %s:\n%s", loaded, wantEnabled, out)
		}
	}
}

// What setup.sh leaves behind for later -- the daemon unit, the CLI
// wrapper, the host-binary extractor -- and its own image_run and
// sandbox_image_refs never let docker pull a release-asset image either.
func TestNothingSetupLeavesBehindPullsAReleaseAssetImage(t *testing.T) {
	code := setupText(t)
	contains(t, body(t, code, "ensure_container_sandbox_image() {"), `if release_asset_ref "$image"; then`)
	contains(t, body(t, code, "image_run() {"), `if release_asset_ref "$GRAIN_IMAGE_REF"; then docker_args+=(--pull=never); fi`)
	contains(t, body(t, code, "sandbox_image_refs() {"), `if release_asset_ref "$1"; then pull=(--pull=never); fi`)
	contains(t, body(t, code, "install_cli_wrappers() {"), `case "\$image" in grain/*) args+=(--pull=never) ;; esac`)
	contains(t, body(t, code, "write_host_extractor() {"), `case "\$image" in grain/*) create_args+=(--pull=never) ;; esac`)
	contains(t, code, `create "\${create_args[@]}" "\$image"`)
	units := body(t, code, "write_systemd_units() {")
	guard := `ExecStartPre=/bin/sh -c 'case "\$\$GRAIN_IMAGE" in grain/*) $DOCKER_BIN image inspect "\$\$GRAIN_IMAGE"`
	contains(t, units, guard)
	before(t, units, guard, "ExecStart=$DOCKER_BIN run --name grain-daemon",
		"the daemon unit refuses a missing release-asset image before docker run could pull it")
}

// pull_image used to reach release-asset refs only through
// install_release_assets, which loaded them and never pulled. With that
// path gone, a GRAIN_IMAGE naming a grain/ repository (or a host whose
// earlier install left one) must still never become a `docker pull` of
// Docker Hub's grain/ namespace: a copy already on the host is deployed,
// and a missing one stops the install. A registry ref is pulled as before.
func TestPullImageNeverPullsAReleaseAssetRef(t *testing.T) {
	fn := shellFunction(t, "release_asset_ref") + shellFunction(t, "pull_image")
	stubs := `
log() { echo "log: $*"; }
step_start() { :; }
step_done() { :; }
registry_login() { echo "docker login" >> "$DOCKER_LOG"; }
verify_grain_image() { echo "verified $GRAIN_IMAGE_REF"; }
report_image_revision() { :; }
docker() {
  echo "docker $*" >> "$DOCKER_LOG"
  if [ "$1" = image ] && [ "$2" = inspect ]; then [ "$LOADED" = 1 ]; return; fi
  return 0
}
`
	for _, tc := range []struct {
		ref, loaded string
		wantPull    bool
		wantOK      bool
	}{
		{"grain/grain:main", "1", false, true},
		{"grain/grain:main", "0", false, false},
		{"docker.io/grainai/grain:main", "0", true, true},
	} {
		log := t.TempDir() + "/docker.log"
		out := runBash(t, stubs+fn+`
: > "$DOCKER_LOG"
GRAIN_DOCKER_AVAILABLE=1
GRAIN_IMAGE_TAG=main
GRAIN_IMAGE="${REF%:*}"
GRAIN_IMAGE_REF="$REF"
if (pull_image); then echo "status=ok"; else echo "status=failed"; fi
cat "$DOCKER_LOG"
`, "DOCKER_LOG="+log, "LOADED="+tc.loaded, "REF="+tc.ref)
		pulled := strings.Contains(out, "docker pull") || strings.Contains(out, "docker login")
		if pulled != tc.wantPull {
			t.Errorf("%s (loaded=%s): reached the registry = %v, want %v:\n%s", tc.ref, tc.loaded, pulled, tc.wantPull, out)
		}
		if ok := strings.Contains(out, "status=ok"); ok != tc.wantOK {
			t.Errorf("%s (loaded=%s): pull_image succeeded = %v, want %v:\n%s", tc.ref, tc.loaded, ok, tc.wantOK, out)
		}
	}
}
