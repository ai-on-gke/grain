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

// Image signing (docs/image-signing.md, grain/task-1773): releases sign
// each image by digest, and setup.sh verifies what it pulls before
// running it when it is given a key.

// signingStubs writes a docker that answers `image inspect` with
// RepoDigests for the grain repository, and a cosign that logs what it
// was asked and refuses when $COSIGN_REFUSE is set, and returns a PATH
// with them first.
func signingStubs(t *testing.T) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	for name, script := range map[string]string{
		"docker": "#!/bin/sh\necho ghcr.io/other/x@sha256:1111\necho ghcr.io/bwsalmon/grain/grain@sha256:abcd\n",
		"cosign": "#!/bin/sh\necho \"cosign $*\" >> " + log + "\n[ -z \"${COSIGN_REFUSE:-}\" ] || exit 1\necho '[{}]'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir + ":" + os.Getenv("PATH"), log
}

func setupVerifyFunctions(t *testing.T) string {
	return "log() { echo \"==> $*\"; }\n" +
		shellFunction(t, "image_digest_ref") +
		shellFunction(t, "verify_image_signature") +
		shellFunction(t, "verify_grain_image")
}

// With a key, setup.sh verifies the grain image by its digest -- never
// the tag it pulled it by -- and records it pinned to the digest that
// verified, so the unit boots those bytes and not whatever the tag names
// at the next restart.
func TestSetupVerifiesThePulledImageByDigestAndPinsIt(t *testing.T) {
	path, log := signingStubs(t)
	out := runBash(t, setupVerifyFunctions(t)+`
GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain/grain:main
GRAIN_IMAGE_VERIFY_KEY=/etc/grain/cosign.pub
verify_grain_image
echo "REF=$GRAIN_IMAGE_REF"
`, "PATH="+path)
	if !strings.Contains(out, "REF=ghcr.io/bwsalmon/grain/grain:main@sha256:abcd\n") {
		t.Errorf("setup.sh did not pin the verified digest:\n%s", out)
	}
	got, _ := os.ReadFile(log)
	if want := "cosign verify --key /etc/grain/cosign.pub --insecure-ignore-tlog=true ghcr.io/bwsalmon/grain/grain@sha256:abcd\n"; string(got) != want {
		t.Errorf("cosign was asked %q, want %q", got, want)
	}
}

// A signature that does not verify stops setup.sh before anything runs
// the image; no key warns and leaves the ref as it was.
func TestSetupRefusesAnUnsignedImageAndWarnsWithoutAKey(t *testing.T) {
	path, _ := signingStubs(t)
	script := setupVerifyFunctions(t) + `
GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain/grain:main
GRAIN_IMAGE_VERIFY_KEY=/etc/grain/cosign.pub
verify_grain_image
echo "REACHED"
`
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+script)
	cmd.Env = append(os.Environ(), "PATH="+path, "COSIGN_REFUSE=1")
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "REACHED") {
		t.Errorf("setup.sh carried on past an image whose signature did not verify (err=%v):\n%s", err, out)
	}

	out2 := runBash(t, setupVerifyFunctions(t)+`
GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain/grain:main
GRAIN_IMAGE_VERIFY_KEY=
verify_grain_image
echo "REF=$GRAIN_IMAGE_REF"
`, "PATH="+path)
	if !strings.Contains(out2, "WARNING: not verifying") || !strings.Contains(out2, "REF=ghcr.io/bwsalmon/grain/grain:main\n") {
		t.Errorf("with no key, want a warning and the ref left alone:\n%s", out2)
	}
}

// The release signs each image once it is pushed and before any moving
// tag can name it, so no tag a deployment resolves ever points at an
// unsigned image of this release. The two sandbox images are pushed by
// the build, which needs their digests to stamp into the grain image
// (TestTheReleaseStampsSandboxImagesByDigest), but nothing is signed
// until the publish phase -- after the release's suites have passed --
// and the sandboxes are signed before the grain image naming them is
// pushed.
func TestTheReleaseSignsEveryImageBeforeMovingATag(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")
	publish := body(t, script, "publish_images() {")
	signed := strings.Index(publish, `sign_image "$grain"`)
	moved := strings.Index(publish, `docker tag "$source" "$target"`)
	if signed < 0 || moved < 0 || signed > moved {
		t.Errorf("publish_images moves a tag before it signs what the tag names:\n%s", publish)
	}
	build := body(t, script, "build_images() {")
	absent(t, build, "sign_image")
	pushed := strings.Index(publish, `push_image "$grain"`)
	for _, image := range []string{"$guest", "$k8s_sandbox"} {
		at := strings.Index(publish, `sign_image "`+image+`"`)
		if at < 0 || pushed < 0 || at > pushed {
			t.Errorf("publish_images does not sign %s before it pushes the grain image naming it:\n%s", image, publish)
		}
	}
	sign := body(t, script, "sign_image() {")
	contains(t, sign, "--tlog-upload=false")
	contains(t, sign, `"$digest_ref"`)
}

// The upgrade verifier runs in the daemon's own container, so the image
// has to carry the cosign it names -- copied out of a stage built FROM a
// digest-pinned image (TestEveryBaseImageIsPinnedByDigest holds the
// pin) into the runtime stage, not into a build stage nothing ships.
func TestTheGrainImageCarriesCosignForTheUpgradeVerifier(t *testing.T) {
	code := stripComments(read(t, "Dockerfile"))
	contains(t, code, "FROM ${COSIGN_IMAGE} AS cosign")
	runtime := code[strings.LastIndex(code, "\nFROM "):]
	contains(t, runtime, "COPY --from=cosign /ko-app/cosign /usr/local/bin/cosign")
}

// What setup.sh verified the pulled image with, the daemon's own
// upgrades verify with too: the key is copied somewhere root owns, the
// container gets it read-only at that same path, and the unit passes
// -upgrade-verify-cmd naming it, one argument per flag.
func TestSetupHandsTheDaemonTheKeyItVerifiedWith(t *testing.T) {
	setup := setupText(t)
	units := body(t, setup, "write_systemd_units() {")
	for _, arg := range []string{
		"-upgrade-verify-cmd=cosign",
		"-upgrade-verify-cmd=verify",
		`"-upgrade-verify-cmd=--key=$IMAGE_VERIFY_KEY_FILE"`,
		"-upgrade-verify-cmd=--insecure-ignore-tlog=true",
	} {
		contains(t, units, arg)
	}
	contains(t, setup, "IMAGE_VERIFY_KEY_FILE=\"$UNIT_IMAGE_REF_DIR/cosign.pub\"")
	main := body(t, setup, "main() {")
	if i, j := strings.Index(main, "\n  install_image_verify_key\n"), strings.Index(main, "\n  write_systemd_units\n"); i < 0 || i > j {
		t.Errorf("main does not install the verify key before writing the unit that mounts it:\n%s", main)
	}

	dir := t.TempDir()
	key := filepath.Join(dir, "release.pub")
	if err := os.WriteFile(key, []byte("-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	// install and chown as root cannot run here; strip the ownership
	// arguments and keep everything else they do.
	out := runBash(t, `
install() { local a=(); while [ "$#" -gt 0 ]; do case "$1" in -o|-g) shift 2 ;; *) a+=("$1"); shift ;; esac; done; command install "${a[@]}"; }
chown() { :; }
log() { echo "==> $*"; }
`+shellFunction(t, "install_image_verify_key")+shellFunction(t, "docker_run_args")+`
UNIT_IMAGE_REF_DIR=`+dir+`/etc
IMAGE_VERIFY_KEY_FILE=$UNIT_IMAGE_REF_DIR/cosign.pub
GRAIN_IMAGE_VERIFY_KEY=`+key+`
GRAIN_USER=`+strings.TrimSpace(string(user))+`
GRAIN_DATA_DIR=/data GRAIN_SANDBOX_DIR=/sandbox
GRAIN_GRANULE_ENABLE=0 GRAIN_CONTAINER_SANDBOXES=0 GRAIN_ENABLE_UI_UPGRADE=1
GRAIN_CLAUDE_PATH= GRAIN_AGY_PATH= GRAIN_CODEX_PATH= GRAIN_CODEX_AUTH_FILE= GRAIN_EXTRA_DOCKER_ARGS=
install_image_verify_key
stat -c 'MODE=%a' "$IMAGE_VERIFY_KEY_FILE"
docker_run_args
printf 'ARG=%s\n' "${DOCKER_ARGS[@]}"
GRAIN_IMAGE_VERIFY_KEY=
install_image_verify_key
[ -e "$IMAGE_VERIFY_KEY_FILE" ] || echo REMOVED
`)
	contains(t, out, "MODE=644\n")
	contains(t, out, "ARG="+dir+"/etc/cosign.pub:"+dir+"/etc/cosign.pub:ro\n")
	contains(t, out, "REMOVED\n")
}

// The Kubernetes manifest wires the same verifier: cosign's default
// JSON output (no --output flag, which is what the tag path reads the
// verified digest out of), against the key a ConfigMap mounts read-only
// at the path the flag names.
func TestTheKubernetesDeploymentVerifiesUpgradesAgainstAConfigMapKey(t *testing.T) {
	objects, nodes := manifestDocs(t)
	var verify []string
	for _, arg := range grainContainer(t, grainDeployment(t)).Args {
		if v, ok := strings.CutPrefix(arg, "-upgrade-verify-cmd="); ok {
			verify = append(verify, v)
		}
	}
	if want := "cosign verify --key=/etc/grain/cosign.pub --insecure-ignore-tlog=true"; strings.Join(verify, " ") != want {
		t.Errorf("grain.yaml's -upgrade-verify-cmd is %q, want %q", strings.Join(verify, " "), want)
	}

	var configMap, mounted string
	for i, obj := range objects {
		switch obj.Kind {
		case "ConfigMap":
			var cm struct {
				Data map[string]string `yaml:"data"`
			}
			if err := nodes[i].Decode(&cm); err != nil {
				t.Fatal(err)
			}
			if _, ok := cm.Data["cosign.pub"]; !ok {
				t.Errorf("ConfigMap %s carries no cosign.pub", obj.Metadata.Name)
			}
			configMap = obj.Metadata.Name
		case "Deployment":
			var dep struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								VolumeMounts []struct {
									Name      string `yaml:"name"`
									MountPath string `yaml:"mountPath"`
									ReadOnly  bool   `yaml:"readOnly"`
								} `yaml:"volumeMounts"`
							} `yaml:"containers"`
							Volumes []struct {
								Name      string `yaml:"name"`
								ConfigMap struct {
									Name string `yaml:"name"`
								} `yaml:"configMap"`
							} `yaml:"volumes"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			if err := nodes[i].Decode(&dep); err != nil {
				t.Fatal(err)
			}
			volume := ""
			for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
				if m.MountPath == "/etc/grain" {
					if !m.ReadOnly {
						t.Errorf("the verify key is mounted writable at /etc/grain")
					}
					volume = m.Name
				}
			}
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if volume != "" && v.Name == volume {
					mounted = v.ConfigMap.Name
				}
			}
		}
	}
	if mounted == "" || mounted != configMap {
		t.Errorf("/etc/grain, where -upgrade-verify-cmd looks for its key, mounts ConfigMap %q; grain.yaml ships %q", mounted, configMap)
	}
}

// A sandbox reference stamped by tag is verified when an upgrade pulls
// it and then not used: every VM and pod is created later from the
// stamped name, which a registry lets anybody with push access repoint.
// So the build pushes both sandbox images before the grain image exists,
// takes the digest each push reports (signed later, by `push`), stamps
// `repo:sha-<short>@sha256:...` into the grain image, and reads the stamp
// back off the image before anything records it.
func TestTheReleaseStampsSandboxImagesByDigest(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")
	build := body(t, script, "build_images() {")
	order := []string{
		`docker build -f Dockerfile.k8s-sandbox -t "$k8s_sandbox" .`,
		`push_pinned "$guest"`,
		`push_pinned "$k8s_sandbox"`,
		// The daemon image, built by build_daemon_image below once the
		// sandboxes are pinned.
		`build_daemon_image "$grain" "$core_main"`,
	}
	at := 0
	for _, step := range order {
		i := strings.Index(build[at:], step)
		if i < 0 {
			t.Fatalf("build_images does not have %q after %q:\n%s", step, build[:at], build)
		}
		at += i + len(step)
	}
	daemon := body(t, script, "build_daemon_image() {")
	at = 0
	for _, step := range []string{
		`make image IMAGE="$image" MAIN_grain="$main" SANDBOX_IMAGE="$guest_pinned" K8S_SANDBOX_IMAGE="$k8s_sandbox_pinned"`,
		`"sandbox-image $guest_pinned"`,
		`"k8s-sandbox-image $k8s_sandbox_pinned"`,
	} {
		i := strings.Index(daemon[at:], step)
		if i < 0 {
			t.Fatalf("build_daemon_image does not have %q after %q:\n%s", step, daemon[:at], daemon)
		}
		at += i + len(step)
	}
	push := body(t, script, "push_pinned() {")
	contains(t, push, `pinned="$image@$digest"`)
	contains(t, push, "digest: sha256:[0-9a-f]{64}")
}

// push_pinned's answer is the digest the push itself reported for the
// tag, appended to that tag -- not some other digest the same repository
// left in the image's RepoDigests -- and a push that reports none is a
// failure rather than a grain image stamped by tag after all.
func TestPushPinnedStampsTheDigestThePushReported(t *testing.T) {
	const digest = "sha256:5a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c55a4db0c5"
	fn := body(t, read(t, "scripts", "release-images.sh"), "push_pinned() {")
	for name, tc := range map[string]struct {
		pushed string
		want   string
	}{
		"reported": {
			pushed: "sha-0123456: digest: " + digest + " size: 1234",
			want:   "pinned=ghcr.io/bwsalmon/grain/guest:sha-0123456@" + digest,
		},
		"not reported": {pushed: "pushed, says nothing", want: "reported no digest"},
	} {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			stub := "#!/bin/sh\necho 'The push refers to repository [ghcr.io/bwsalmon/grain/guest]'\necho '" + tc.pushed + "'\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			script := "set -euo pipefail\n" + fn + "\n}\n" +
				"push_pinned ghcr.io/bwsalmon/grain/guest:sha-0123456\necho \"pinned=$pinned\"\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("push_pinned printed:\n%s\nwant %q", out, tc.want)
			}
		})
	}

	t.Run("permission_denied fails on first attempt without retrying", func(t *testing.T) {
		bin := t.TempDir()
		calls := filepath.Join(bin, "calls")
		stub := "#!/bin/sh\necho x >> " + calls + "\necho 'error from registry: permission_denied: The token provided does not match expected scopes.'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, fnName := range []string{"push_pinned", "push_image"} {
			bodyFn := body(t, read(t, "scripts", "release-images.sh"), fnName+"() {")
			_ = os.Remove(calls)
			script := "set -euo pipefail\n" + bodyFn + "\n}\n" +
				fnName + " ghcr.io/bwsalmon/grain/k8s-sandbox:sha-0123456\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GRAIN_CI_REPO=bwsalmon/grain")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s succeeded on permission_denied:\n%s", fnName, out)
			}
			if !strings.Contains(string(out), "missing package write permission") {
				t.Errorf("%s did not explain the permission error:\n%s", fnName, out)
			}
			gotCalls, _ := os.ReadFile(calls)
			if count := strings.Count(string(gotCalls), "x\n"); count != 1 {
				t.Errorf("%s called docker push %d times on permission_denied, want 1", fnName, count)
			}
		}
	})
}
