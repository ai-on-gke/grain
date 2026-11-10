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
	"os"
	"os/exec"
	"path/filepath"
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

// An earlier release's release-asset install recorded the local grain/grain
// ref it loaded in /etc/grain/image.env. A setup.sh re-run keeps that
// channel when shifting onto the default registry repository, while refusing
// the reverse transition onto GRAIN_IMAGE=grain/grain (which would pull from
// Docker Hub).
func TestSetupReRunKeepsAReleaseAssetHostsChannel(t *testing.T) {
	for _, tc := range []struct{ ref, tag, channel string }{
		{"grain/grain:main", "main", "main"},
		{"grain/grain:candidate", "candidate", "candidate"},
		{"grain/grain:sha-0123456789ab", "sha-0123456789ab", ""},
	} {
		f := refFile(t, tc.ref)
		out, tag, channel, ok := resolveTag(t, nil, f, f)
		if !ok || tag != tc.tag || channel != tc.channel {
			t.Errorf("re-run over %s settled on tag=%q channel=%q (ok=%v), want %q %q:\n%s",
				tc.ref, tag, channel, ok, tc.tag, tc.channel, out)
		}
	}
	// grain/grain is only ever a repository a re-run keeps a tag from,
	// never one it moves onto: it would be pulled from Docker Hub.
	f := refFile(t, "docker.io/grainai/grain:main")
	out, tag, _, _ := resolveTag(t, []string{"GRAIN_IMAGE=grain/grain"}, f, f)
	if tag != "latest" {
		t.Errorf("a re-run onto GRAIN_IMAGE=grain/grain kept tag %q; want latest (no shift onto grain/grain):\n%s", tag, out)
	}
}

func TestInstallGrainRefusesImageWithoutRegistryHost(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "install-grain.sh")
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerLog := filepath.Join(dir, "docker.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + dockerLog + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"grain/grain", "grainai/grain", "grain"} {
		cmd := exec.Command("bash", script, "-version", "v1.9.0", "-image", bad, "-dest", filepath.Join(dir, "out"))
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("install-grain.sh -image %q succeeded, want refusal:\n%s", bad, out)
		}
		if !strings.Contains(string(out), "names no registry host") {
			t.Errorf("install-grain.sh -image %q output = %q, want 'names no registry host'", bad, out)
		}
		if raw, _ := os.ReadFile(dockerLog); len(raw) > 0 {
			t.Errorf("install-grain.sh -image %q invoked docker: %s", bad, raw)
		}
	}
}
