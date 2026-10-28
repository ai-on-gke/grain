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

// What a release does when nothing gave it a login.
//
// Every merge into main from 199b85bcd to 37fe68494 built the three
// release images, tested them, and then died at the push with ghcr.io's
// own "unauthenticated: User cannot be authenticated with the token
// provided" -- because bwsalmon/grain's CI capabilities did not include
// `package-publish`, so no credential was placed, and the script said
// "using Docker's existing credentials" and carried on with none. The
// grant is operator-owned repository configuration and nothing in this
// repository can supply it (.grain/README.md); what this repository can
// do is stop at the start of the job, when the reader of the log is
// still being told something they can act on, rather than an hour later
// behind a registry error that names no cause.
//
// These tests run the real script with a stub `docker` on PATH, because
// the claim is about what it does rather than about what it says.

// releaseImagesRun runs scripts/release-images.sh in a hermetic
// environment: a PATH holding nothing but the stubs this returns, no
// credential files, and every variable the script reads set explicitly.
// It returns the combined output and the log of what `docker` was asked
// to do.
func releaseImagesRun(t *testing.T, phase string, env map[string]string) (string, string, error) {
	t.Helper()
	return releaseImagesRunArgs(t, []string{phase}, env)
}

// releaseImagesRunArgs is releaseImagesRun for a phase that takes more
// than one word, such as `promote v1.5.0`.
func releaseImagesRunArgs(t *testing.T, args []string, env map[string]string) (string, string, error) {
	t.Helper()

	// The credential paths the script reads are absolute, so a machine
	// that has one placed is a machine these tests cannot ask this
	// question on.
	for _, path := range []string{
		"/home/debian/.package-publish-token",
		"/home/debian/.registry-push-token",
	} {
		if _, err := os.Stat(path); err == nil {
			t.Skipf("%s exists here, and these tests are about a job that was given no credential", path)
		}
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("creating the stub bin directory: %v", err)
	}
	dockerLog := filepath.Join(dir, "docker.log")
	// The stub drains stdin for `login`, as the real docker does for
	// --password-stdin, and that is not decoration: the script pipes the
	// token in under `set -o pipefail`, so a stub that exits without
	// reading kills printf with SIGPIPE and fails the whole script at the
	// login -- on a loaded machine, and not on an idle one.
	// DOCKER_STUB_MISSING, where a test sets it, names an image the
	// registry does not have: any command reading it fails, as an
	// `imagetools inspect` of a tag that was never pushed does. Writing
	// it -- `imagetools create --tag <it>` -- succeeds, as pushing a tag
	// the registry lacks does.
	// DOCKER_STUB_ANSWERS, where a test sets it, is a shell script the
	// stub sources after logging, for the commands whose output the
	// script reads back -- `docker run <image> version --json`, a label.
	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = login ]; then while IFS= read -r _; do :; done; fi\n" +
		"echo \"$@\" >> \"$DOCKER_STUB_LOG\"\n" +
		"if [ -n \"$DOCKER_STUB_MISSING\" ]; then case \" $* \" in *\" --tag $DOCKER_STUB_MISSING \"*) ;; *\" $DOCKER_STUB_MISSING \"*) exit 1 ;; esac; fi\n" +
		"if [ -n \"$DOCKER_STUB_ANSWERS\" ]; then . \"$DOCKER_STUB_ANSWERS\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatalf("writing the docker stub: %v", err)
	}

	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts", "release-images.sh")}, args...)...)
	cmd.Dir = repoRoot(t)
	// PATH is the stub directory alone: no `gh` to fall back on, and no
	// real docker to reach a registry with.
	cmd.Env = []string{
		"PATH=" + bin,
		"HOME=" + dir,
		"DOCKER_STUB_LOG=" + dockerLog,
		"GRAIN_CI_REPO=bwsalmon/grain",
		"GRAIN_CI_REF=main",
		"GRAIN_CI_COMMIT=0123456789abcdef0123456789abcdef01234567",
		"RELEASE_IMAGES_STATE=" + filepath.Join(dir, "release-images.env"),
	}
	for k, v := range env {
		// A caller asking for the machine's real tools behind the stubs
		// names the stub directory it cannot see with stubBinMarker.
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(v, stubBinMarker, bin))
	}
	// Nothing here should read the test binary's own stdin; a stub that
	// found a terminal there would wait on it.
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	asked, readErr := os.ReadFile(dockerLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("reading what docker was asked to do: %v", readErr)
	}
	return string(out), string(asked), err
}

// A Grain CI release with no publishing credential stops before it builds
// anything, and says which grant it is missing.
func TestAReleaseWithNoPublishingCredentialStopsBeforeTheBuild(t *testing.T) {
	out, docker, err := releaseImagesRun(t, "build", map[string]string{"GRAIN_CI": "true"})
	if err == nil {
		t.Fatalf("the build succeeded with no credential to publish what it built:\n%s", out)
	}
	containsRun(t, out, "no publishing credential reached this job")
	containsRun(t, out, "grain repo ci-capabilities -set package-publish bwsalmon/grain")
	// The point of failing here rather than at the push: the sandbox has
	// not been asked to build, pull or tag anything yet.
	if docker != "" {
		t.Errorf("docker was asked to do something before the credential was checked:\n%s", docker)
	}
}

// The same is true of the push phase, which is the one that would
// otherwise reach the registry.
func TestAReleasePushWithNoPublishingCredentialStopsBeforeTheRegistry(t *testing.T) {
	out, docker, err := releaseImagesRun(t, "push", map[string]string{"GRAIN_CI": "true"})
	if err == nil {
		t.Fatalf("the push succeeded with no credential:\n%s", out)
	}
	containsRun(t, out, "no publishing credential reached this job")
	if strings.Contains(docker, "push") {
		t.Errorf("an unauthenticated push was attempted:\n%s", docker)
	}
}

// A credential still logs in, and the phases still run. This is the other
// half of the claim above: the check refuses the case it is for and
// nothing else.
func TestAReleaseWithAPublishingCredentialLogsInAndCarriesOn(t *testing.T) {
	out, docker, err := releaseImagesRun(t, "push", map[string]string{
		"GRAIN_CI":                 "true",
		"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
		"PACKAGE_PUBLISH_USERNAME": "x-access-token",
		"PACKAGE_PUBLISH_TOKEN":    "a-token",
	})
	containsRun(t, docker, "login ghcr.io -u x-access-token --password-stdin")
	// It got past the login and stopped where a `push` with no `build` in
	// front of it is supposed to stop, which is the next thing that can
	// go wrong rather than the credential.
	if err == nil {
		t.Fatalf("a push with no state file from a build should have failed:\n%s", out)
	}
	containsRun(t, out, "`push` publishes what `build` built")
}

// A hand-run is left alone: an operator who has already run `docker
// login` -- or who wants `build` for its own sake -- gets the behaviour
// this script has always had.
func TestAHandRunStillUsesDockersOwnCredentials(t *testing.T) {
	out, _, err := releaseImagesRun(t, "push", nil)
	containsRun(t, out, "using Docker's existing credentials for ghcr.io")
	if err == nil {
		t.Fatalf("a push with no state file from a build should have failed:\n%s", out)
	}
	containsRun(t, out, "`push` publishes what `build` built")
}

// The `guest-cache` phase on a cache hit, which since grain/task-409 is
// what most merges into main make grain dispatch: with the setting
// naming this job, every commit on the base branch has it run, and most
// commits change none of the paths scripts/granule/guest-inputs.sh names.
//
// So what it does when the key already exists is the cost of the whole
// arrangement, and it has to be a pull: a phase that rebuilt a guest for
// a key that was already in the registry would put a ~25-minute nested
// VM build on every merge rather than on the few that change an input,
// which would be a worse bill than the coupling this replaced.
//
// The stub docker answers every command with success, so `docker pull`
// of the cache tag succeeds here exactly as it does on a real hit.
func TestTheGuestCachePhaseOnAHitPullsRatherThanBuilding(t *testing.T) {
	out, docker, err := releaseImagesRun(t, "guest-cache", map[string]string{
		// git, sha256sum and the guest-inputs script are what derive the
		// key, and they are real: the phase is being asked which tag it
		// looks for, and that answer comes out of this checkout.
		"PATH":                     realToolPath(t),
		"GRAIN_CI":                 "true",
		"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
		"PACKAGE_PUBLISH_USERNAME": "x-access-token",
		"PACKAGE_PUBLISH_TOKEN":    "a-token",
	})
	if err != nil {
		output := out
		if output == "" {
			output = "(no output)"
		}
		t.Fatalf("the guest-cache phase failed on a cache hit: %v\n%s", err, output)
	}
	containsRun(t, out, "sandbox guest cache hit")
	containsRun(t, out, "nothing to build")
	if !strings.Contains(docker, "pull ghcr.io/bwsalmon/grain/guest:inputs-") {
		t.Errorf("the phase pulled no guest cache tag:\n%s", docker)
	}
	for _, forbidden := range []string{"build", "push"} {
		for _, line := range strings.Split(docker, "\n") {
			if strings.HasPrefix(line, forbidden+" ") {
				t.Errorf("a hit on the guest cache still ran `docker %s`:\n%s", line, docker)
			}
		}
	}
}

// realToolPath is the stub directory with the machine's own PATH behind
// it, for a test whose subject is what the script computes rather than
// what it asks docker for: the stub still shadows docker, because it is
// first.
func realToolPath(t *testing.T) string {
	t.Helper()
	// The stub bin is prepended by releaseImagesRun's own PATH, which
	// this value replaces, so it has to carry the same directory. It is
	// derived rather than passed because the helper owns the temporary
	// directory; the marker below is what it substitutes.
	return stubBinMarker + ":" + os.Getenv("PATH")
}

// stubBinMarker is replaced by releaseImagesRun with the stub directory
// it made, so a caller can ask for "the stubs, then the real tools"
// without reaching into the helper's temporary directory.
const stubBinMarker = "@stub-bin@"

// containsRun is `contains` with the text it searched in the failure.
// These tests drive a script whose one real caller is a release job, so
// a failure that names only the missing string leaves the next reader
// re-deriving what the script said instead of reading it.
func containsRun(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("expected to find %q in:\n%s", want, text)
	}
}

// scripts/release-images.sh must bootstrap transparent cache and DNS if 172.17.0.1
// is reachable so registry authentication and package resolution succeed.
func TestReleaseImagesBootstrapsTransparentCache(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-images.sh"))
	if err != nil {
		t.Fatalf("reading release-images.sh: %v", err)
	}
	if !strings.Contains(string(content), "ensure_transparent_cache") {
		t.Errorf("release-images.sh should contain ensure_transparent_cache")
	}
}

// scripts/release-images.sh must declare trap cleanup variables in global scope
// so that EXIT/INT/TERM traps do not fail on unbound variables under set -u.
func TestReleaseImagesTrapCleansUpWithoutUnboundVariables(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-images.sh"))
	if err != nil {
		t.Fatalf("reading release-images.sh: %v", err)
	}
	if strings.Contains(string(content), "local ca_copied") {
		t.Errorf("release-images.sh should not declare ca_copied as a local variable inside a function when referenced in EXIT trap")
	}
}

// scripts/release-images.sh must ensure direct upstream routing for container registries
// so image publishing does not get intercepted or refused by intermediate proxies.
func TestReleaseImagesEnsuresDirectRegistryRoute(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-images.sh"))
	if err != nil {
		t.Fatalf("reading release-images.sh: %v", err)
	}
	if !strings.Contains(string(content), "ensure_direct_registry_route") {
		t.Errorf("release-images.sh should contain ensure_direct_registry_route")
	}
}

// scripts/release-images.sh --dry-run prints planned actions without executing builds
// or requiring publishing credentials, and cleans up CA certificates on exit.
func TestReleaseImagesDryRun(t *testing.T) {
	for _, flag := range []string{"--dry-run", "-n"} {
		t.Run(flag, func(t *testing.T) {
			out, docker, err := releaseImagesRun(t, flag, map[string]string{
				"GRAIN_CI": "true",
			})
			if err != nil {
				t.Fatalf("release-images.sh %s failed: %v\nOutput:\n%s", flag, err, out)
			}
			containsRun(t, out, "[dry-run]")
			containsRun(t, out, "repository: bwsalmon/grain")
			containsRun(t, out, "phase 'all': would build and publish")
			// Two daemon images, one per install track, beside the
			// shared sandbox images (grain/task-4944).
			containsRun(t, out, "grain=ghcr.io/bwsalmon/grain/grain:sha-0123456, google_grain=ghcr.io/bwsalmon/grain/google-grain:sha-0123456")
			containsRun(t, out, "ghcr.io/bwsalmon/grain/grain:sha-0123456 from ./cmd/grain (install track oss), ghcr.io/bwsalmon/grain/google-grain:sha-0123456 from ./google/cmd/grain (install track google)")
			if docker != "" {
				t.Errorf("docker was asked to do something during dry-run:\n%s", docker)
			}
		})
	}
}

// An operator who names a registry and relies on an existing `docker
// login` publishes to that registry. The older `registry-push` grant's
// registry used to replace it unconditionally whenever no token was
// given, so PACKAGE_PUBLISH_REGISTRY=localhost:5000 published to ghcr.io
// (docs/test-runs/2026-10-05-image-signing-and-upgrade-targets.md, A).
func TestAHandRunPublishesToTheRegistryItWasGiven(t *testing.T) {
	out, _, _ := releaseImagesRun(t, "push", map[string]string{
		"PACKAGE_PUBLISH_REGISTRY": "localhost:5000",
	})
	containsRun(t, out, "using Docker's existing credentials for localhost:5000")
	if strings.Contains(out, "ghcr.io") {
		t.Errorf("a run naming localhost:5000 still talked about ghcr.io:\n%s", out)
	}
}

// REQUIRE_IMAGE_SIGNING=1 is as much "on" as =true: with no key the
// release stops before it builds anything, rather than publishing three
// unsigned images and moving the branch tag with a warning (finding B).
func TestARequiredSignatureWithNoKeyStopsBeforeTheBuild(t *testing.T) {
	for _, value := range []string{"1", "true", "yes"} {
		for _, phase := range []string{"build", "all"} {
			out, docker, err := releaseImagesRun(t, phase, map[string]string{
				"REQUIRE_IMAGE_SIGNING": value,
				"IMAGE_SIGNING_KEY":     filepath.Join(t.TempDir(), "no-such-key"),
			})
			if err == nil {
				t.Errorf("REQUIRE_IMAGE_SIGNING=%s %s with no key succeeded:\n%s", value, phase, out)
			}
			containsRun(t, out, "REQUIRE_IMAGE_SIGNING is set and there is no signing key")
			for _, line := range strings.Split(docker, "\n") {
				if strings.HasPrefix(line, "build ") || strings.HasPrefix(line, "push ") || strings.HasPrefix(line, "pull ") {
					t.Errorf("REQUIRE_IMAGE_SIGNING=%s %s asked docker to %q before refusing", value, phase, line)
				}
			}
		}
	}
	// Unset, the release still goes ahead unsigned: the check is for a
	// release that asked for it.
	script := read(t, "scripts", "release-images.sh")
	if !strings.Contains(body(t, script, "sign_image() {"), `if [ "$require_signing" = true ]; then`) {
		t.Error("sign_image no longer reads the normalised require_signing")
	}
}

// The MTU clamp restarts dockerd only when it changed daemon.json. A
// restart stops every container on the host, and the walkthrough's local
// registry died to one that changed nothing (finding C).
func TestTheMTUClampRestartsDockerOnlyWhenDaemonJSONChanged(t *testing.T) {
	fn := body(t, read(t, "scripts", "grain-ci-host.sh"), "ensure_network_mtu() {")
	restarts := strings.Count(fn, "systemctl restart docker")
	guarded := strings.Count(fn, `if [ "$(cksum < /etc/docker/daemon.json 2>/dev/null || true)" != "$daemon_before" ]; then`)
	if restarts == 0 || restarts != guarded {
		t.Errorf("ensure_network_mtu has %d dockerd restarts and %d daemon.json change checks:\n%s", restarts, guarded, fn)
	}
}

// ensure_transparent_cache must probe 172.17.0.1:53 with a bounded timeout
// before falling back to bash's /dev/tcp redirection, because inside a KVM
// sandbox VM where 172.17.0.1 is routed out the default gateway and dropped
// without RST, an unguarded /dev/tcp connect blocks for the full kernel
// tcp_syn_retries timeout (~130s) on every invocation.
func TestEnsureTransparentCacheBoundsTCPProbeTimeout(t *testing.T) {
	fn := body(t, read(t, "scripts", "grain-ci-host.sh"), "ensure_transparent_cache() {")
	ncIdx := strings.Index(fn, "nc -z -w 1")
	timeoutIdx := strings.Index(fn, "/usr/bin/timeout 1")
	rawDevTCPIdx := strings.Index(fn, `(echo >/dev/tcp/"$host"/53)`)
	if ncIdx < 0 || timeoutIdx < 0 {
		t.Fatalf("ensure_transparent_cache must check nc -z -w 1 and /usr/bin/timeout 1:\n%s", fn)
	}
	if rawDevTCPIdx >= 0 && (rawDevTCPIdx < ncIdx || rawDevTCPIdx < timeoutIdx) {
		t.Errorf("ensure_transparent_cache runs unbounded /dev/tcp before bounded probes:\n%s", fn)
	}
}

// hostToolStubs writes stubs for every tool the host-tuning functions
// reach for -- sudo, ip, iptables, and curl and openssl for the DNS and
// CA lookups -- each logging what it was asked into the returned file
// and doing nothing else, and returns a PATH that puts them in front of
// the docker stub and the machine's own tools.
func hostToolStubs(t *testing.T) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "host-tools.log")
	for _, tool := range []string{"sudo", "ip", "iptables", "curl", "openssl", "update-ca-certificates", "systemctl"} {
		stub := "#!/bin/sh\necho \"" + tool + " $*\" >> \"" + log + "\"\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(stub), 0o755); err != nil {
			t.Fatalf("writing the %s stub: %v", tool, err)
		}
	}
	return dir + ":" + realToolPath(t), log
}

func readHostToolLog(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the host tool log: %v", err)
	}
	return string(data)
}

// A hand run leaves the operator's machine alone: no resolv.conf, CA,
// MTU, daemon.json, iptables or /etc/hosts change, which between them
// cost the 2026-10-05 walkthrough its local registry and left ghcr.io in
// its /etc/hosts. Every phase is asked, dry run included, and none of
// them so much as looks up the default route.
func TestAHandRunTouchesNoHostNetworking(t *testing.T) {
	for _, args := range [][]string{{"push"}, {"build"}, {"all"}, {"--dry-run"}, {"-n", "push"}} {
		for _, reg := range []string{"localhost:5000", "ghcr.io"} {
			t.Run(strings.Join(args, " ")+" "+reg, func(t *testing.T) {
				path, log := hostToolStubs(t)
				out, _, _ := releaseImagesRunArgs(t, args, map[string]string{
					"PATH":                     path,
					"PACKAGE_PUBLISH_REGISTRY": reg,
					// A missing key stops build and all before they build
					// anything, which is past every host change there was.
					"REQUIRE_IMAGE_SIGNING": "1",
					"IMAGE_SIGNING_KEY":     filepath.Join(t.TempDir(), "no-such-key"),
				})
				if asked := readHostToolLog(t, log); asked != "" {
					t.Errorf("a hand run reached for the host's networking:\n%s\noutput:\n%s", asked, out)
				}
			})
		}
	}
}

// Grain CI still tunes its sandbox, and so does a hand run that asks
// for it -- but neither pins a loopback or port-qualified registry in
// /etc/hosts, which public DNS has nothing to say about.
func TestGrainCIAndAnExplicitOptInStillTuneTheHost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the script writes /etc/resolv.conf itself rather than through sudo, and this test will not let it")
	}
	for _, env := range []map[string]string{
		{"GRAIN_CI": "true"},
		{"RELEASE_IMAGES_TUNE_HOST": "1"},
	} {
		for _, reg := range []string{"localhost:5000", "127.0.0.1", "ghcr.io"} {
			t.Run(strings.Join(mapKeys(env), ",")+" "+reg, func(t *testing.T) {
				path, log := hostToolStubs(t)
				vars := map[string]string{"PATH": path, "PACKAGE_PUBLISH_REGISTRY": reg}
				for k, v := range env {
					vars[k] = v
				}
				out, _, _ := releaseImagesRun(t, "push", vars)
				asked := readHostToolLog(t, log)
				if !strings.Contains(asked, "ip route show default") {
					t.Errorf("the MTU clamp never looked for the default route:\n%s\noutput:\n%s", asked, out)
				}
				lookedUp := strings.Contains(asked, "dns.google") || strings.Contains(asked, "dns-query")
				if reg == "ghcr.io" {
					if !lookedUp && !hostsNames(reg) {
						t.Errorf("no direct route was looked up for %s:\n%s", reg, asked)
					}
				} else if lookedUp {
					t.Errorf("a direct route was looked up for %s:\n%s", reg, asked)
				}
			})
		}
	}
}

// hostsNames reports whether this machine's /etc/hosts already names
// host, in which case the script rightly looks nothing up.
func hostsNames(host string) bool {
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, field := range strings.Fields(line)[min(1, len(strings.Fields(line))):] {
			if field == host {
				return true
			}
		}
	}
	return false
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Every phase that publishes names both daemon images: the push publishes
// and moves the branch tag of each, which is what makes
// google-grain:main exist after a release on main.
func TestReleaseImagesDryRunPushNamesBothDaemonImages(t *testing.T) {
	for phase, want := range map[string]string{
		"build": "would build ghcr.io/bwsalmon/grain/guest:sha-0123456, ghcr.io/bwsalmon/grain/k8s-sandbox:sha-0123456, ghcr.io/bwsalmon/grain/grain:sha-0123456 and ghcr.io/bwsalmon/grain/google-grain:sha-0123456",
		"push":  "would publish and sign ghcr.io/bwsalmon/grain/grain:sha-0123456 and ghcr.io/bwsalmon/grain/google-grain:sha-0123456 to ghcr.io, and move the main tags of all four",
	} {
		t.Run(phase, func(t *testing.T) {
			out, docker, err := releaseImagesRunArgs(t, []string{"--dry-run", phase}, map[string]string{"GRAIN_CI": "true"})
			if err != nil {
				t.Fatalf("release-images.sh --dry-run %s failed: %v\n%s", phase, err, out)
			}
			containsRun(t, out, want)
			if docker != "" {
				t.Errorf("docker was asked to do something during dry-run:\n%s", docker)
			}
		})
	}
}

// The push moves google-grain's branch tag with the other three, and
// signs google-grain before it does.
func TestThePushPublishesAndSignsBothDaemonImages(t *testing.T) {
	published := publishImagesBody(t, read(t, "scripts", "release-images.sh"))
	order := []string{
		`push_image "$grain"`,
		`sign_image "$grain"`,
		`push_image "$google_grain"`,
		`sign_image "$google_grain"`,
		`"$grain $root/grain:$branch"`,
		`"$google_grain $root/google-grain:$branch"`,
	}
	at := 0
	for _, step := range order {
		i := strings.Index(published[at:], step)
		if i < 0 {
			t.Fatalf("publish_images does not have %q after %q:\n%s", step, published[:at], published)
		}
		at += i + len(step)
	}
}

// A fine-grained PAT (github_pat_...) cannot grant GHCR package permissions,
// and a classic PAT (ghp_...) missing write:packages still passes `docker
// login ghcr.io` only to fail with `permission_denied: The token provided
// does not match expected scopes.` at `docker push` after building the
// sandbox images. Both stop before docker is asked to log in, pull or build.
func TestAReleaseWithAGHCRTokenLackingWritePackagesStopsBeforeTheBuild(t *testing.T) {
	t.Run("fine-grained PAT", func(t *testing.T) {
		out, docker, err := releaseImagesRun(t, "build", map[string]string{
			"GRAIN_CI":                 "true",
			"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
			"PACKAGE_PUBLISH_USERNAME": "x-access-token",
			"PACKAGE_PUBLISH_TOKEN":    "github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
		})
		if err == nil {
			t.Fatalf("release-images.sh build succeeded with a fine-grained PAT:\n%s", out)
		}
		containsRun(t, out, "fine-grained personal access token")
		containsRun(t, out, "write:packages")
		if docker != "" {
			t.Errorf("docker was asked to do something before the fine-grained PAT was refused:\n%s", docker)
		}
	})

	t.Run("classic PAT missing write:packages", func(t *testing.T) {
		curlDir := t.TempDir()
		curlStub := "#!/bin/sh\nprintf 'HTTP/2 200 \\r\\nx-oauth-scopes: repo, workflow\\r\\n\\r\\n'\n"
		if err := os.WriteFile(filepath.Join(curlDir, "curl"), []byte(curlStub), 0o755); err != nil {
			t.Fatal(err)
		}
		out, docker, err := releaseImagesRun(t, "build", map[string]string{
			"PATH":                     stubBinMarker + ":" + curlDir,
			"GRAIN_CI":                 "true",
			"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
			"PACKAGE_PUBLISH_USERNAME": "x-access-token",
			"PACKAGE_PUBLISH_TOKEN":    "ghp_0123456789abcdef0123456789abcdef0123",
		})
		if err == nil {
			t.Fatalf("release-images.sh build succeeded with a classic PAT missing write:packages:\n%s", out)
		}
		containsRun(t, out, "repo, workflow")
		containsRun(t, out, "missing 'write:packages'")
		if docker != "" {
			t.Errorf("docker was asked to do something before the under-scoped classic PAT was refused:\n%s", docker)
		}
	})

	t.Run("classic PAT with write:packages logs in", func(t *testing.T) {
		curlDir := t.TempDir()
		curlStub := "#!/bin/sh\nprintf 'HTTP/2 200 \\r\\nx-oauth-scopes: repo, write:packages, workflow\\r\\n\\r\\n'\n"
		if err := os.WriteFile(filepath.Join(curlDir, "curl"), []byte(curlStub), 0o755); err != nil {
			t.Fatal(err)
		}
		out, docker, err := releaseImagesRun(t, "push", map[string]string{
			"PATH":                     stubBinMarker + ":" + curlDir,
			"GRAIN_CI":                 "true",
			"PACKAGE_PUBLISH_REGISTRY": "ghcr.io",
			"PACKAGE_PUBLISH_USERNAME": "x-access-token",
			"PACKAGE_PUBLISH_TOKEN":    "ghp_0123456789abcdef0123456789abcdef0123",
		})
		containsRun(t, docker, "login ghcr.io -u x-access-token --password-stdin")
		if err == nil {
			t.Fatalf("a push with no state file from a build should have failed:\n%s", out)
		}
		containsRun(t, out, "`push` publishes what `build` built")
	})
}
