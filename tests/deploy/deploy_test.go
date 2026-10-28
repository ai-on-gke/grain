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

// Package deploy holds the cross-file content checks for the container
// deployment (bwsalmon/agents#645).
//
// grain stopped building a binary on the host it runs on: CI publishes
// one image per commit (.github/workflows/build-artifacts.yml), and
// scripts/setup.sh pulls it and runs it as grain-daemon.service. Almost
// all of that is file content -- a Dockerfile, a workflow, a generated
// systemd unit -- rather than control flow, so this holds to the bar
// v1's own provisioning tests set: `bash -n`, plus assertions pinning
// the handful of values that have to agree across files nothing else
// checks together.
//
// What is *not* here, deliberately: running any of it. The image build
// needs a container engine and a network, the unit needs systemd, and
// pkg/upgrade's own tests already cover the upgrade path's behaviour
// against stubs. These are the cross-file agreements no single package's
// tests can see, which is also why this reads the tree from disk rather
// than importing anything.
package deploy

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// The one name three files have to agree on: setup.sh's default, the
// repository CI pushes to, and the daemon's own -upgrade-image.
const image = "ghcr.io/bwsalmon/grain/grain"

func TestSetupIsSyntacticallyValidBash(t *testing.T) {
	out, err := exec.Command("bash", "-n", filepath.Join(repoRoot(t), "scripts", "setup.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n scripts/setup.sh: %v\n%s", err, out)
	}
}

// Debian Trixie separates the client from docker.io when packages are
// installed without recommendations. The daemon install remains first for
// older Debian releases, but a missing command after that attempt must make
// setup install docker-cli before it reports that Docker is unavailable.
func TestSetupInstallsTheSeparateDockerCLIWhenTheDaemonPackageDoesNot(t *testing.T) {
	install := body(t, setupCode(t), "ensure_docker() {")
	fallback := `DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker.io || true
    if ! command -v docker >/dev/null 2>&1; then
      log "installing docker CLI from its separate package (needed on Debian Trixie)"
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-cli || true
    fi`
	contains(t, install, fallback)
	before(t, install, "docker-cli || true", "required command not found: docker",
		"setup reports a missing docker command before trying Debian's separate CLI package")
}

func TestSetupPullsAnImageAndBuildsNothing(t *testing.T) {
	code := setupCode(t)
	contains(t, code, "docker pull")
	// `make container-build` was the whole deploy once. Nothing on a
	// deployed host runs a toolchain any more, and `make` is no longer
	// even installed (see deploy.sh's install_prerequisites).
	absent(t, code, "container-build")
	absent(t, code, "ensure_make")
}

// Native Docker reaches the host daemon through its socket, but Grain itself
// runs in a container with a different HOME from the installer. Pulling the
// task image during setup both fails early and lets docker use the registry
// login the installer just established (including private-package tokens).
func TestSetupPullsTheNativeDockerSandboxWithInstallerCredentials(t *testing.T) {
	code := setupCode(t)
	pull := body(t, code, "ensure_container_sandbox_image() {")
	// The separator is the assertion: image_run splits its arguments on
	// "--", so without one the subcommand is consumed as a docker
	// argument and `docker run` reads it as the image to run.
	contains(t, pull, `image_run -- container-sandbox-image`)
	contains(t, pull, `docker pull "$image"`)
	contains(t, pull, `docker image inspect "$image"`)
	absent(t, pull, `GRAIN_CONTAINER_SANDBOXES`)

	main := from(t, code, "main() {")
	before(t, main, "pull_image", "ensure_container_sandbox_image",
		"the sandbox image is resolved before Grain's own image is available")
	before(t, main, "ensure_container_sandbox_image", "write_systemd_units",
		"the service is installed before its sandbox image is pulled")
}

// image_run reads the arguments before "--" as docker's own and the ones
// after it as the command line for the image. A call that leaves the
// separator out therefore does not fail in any way that names the
// mistake: every argument lands in the docker list, the command list
// comes out empty, and `docker run` reads the first of them as the image
// to run -- so a forgotten "--" on `image_run container-sandbox-image`
// reported "Unable to find image 'container-sandbox-image:latest'
// locally" and then a Docker Hub pull denial, neither of which mentions
// this script or the image it meant to run.
//
// One assertion over every call site rather than one per caller: the
// mistake is available to each of them, costs a deployment the same way
// every time, and is invisible in review precisely because the correct
// spelling looks like an ordinary argument list.
func TestEveryImageRunCallPassesTheSeparator(t *testing.T) {
	// Continuations first: several calls carry the separator on a later
	// line than the one naming image_run.
	code := strings.ReplaceAll(setupCode(t), "\\\n", " ")
	for _, line := range strings.Split(code, "\n") {
		if !strings.Contains(line, "image_run ") || strings.Contains(line, "image_run() {") {
			continue
		}
		if !strings.Contains(line, " -- ") {
			t.Errorf("image_run call with no \"--\" separator, so docker reads its first argument as the image to run:\n\t%s", strings.TrimSpace(line))
		}
	}
}

func TestSetupMakesContainersAvailableBesideGranule(t *testing.T) {
	code := setupCode(t)
	units := body(t, code, "write_systemd_units() {")

	containerPlumbing := `daemon_args+=(-container-git-proxy-host "$GRAIN_GRANULE_GIT_PROXY_HOST")`
	contains(t, units, containerPlumbing)
	before(t, units, containerPlumbing, `if [ "$GRAIN_GRANULE_ENABLE" = "1" ]; then`,
		"container plumbing is conditional on containers being the selected backend")
}

func TestSetupFallsBackToContainersWhenGranuleIsUnavailable(t *testing.T) {
	code := setupCode(t)
	fallback := body(t, code, "granule_unavailable() {")

	contains(t, fallback, `GRAIN_CONTAINER_SANDBOXES=1`)
	contains(t, fallback, `GRAIN_GRANULE_ENABLE=0`)
	absent(t, fallback, `exit 1`)
}

func TestSetupDefaultsToTheImageCIPublishes(t *testing.T) {
	text := setupText(t)
	contains(t, text, `GRAIN_IMAGE="${GRAIN_IMAGE:-`+image+`}"`)
	// Left unset, the tag is resolve_image_tag's to settle (a new
	// deployment's is latest, image_channel_test.go); a GRAIN_REF given
	// becomes its tag with "/" replaced by ".." -- the same substitution
	// the workflow makes when it pushes and pkg/upgrade.TagForBranch
	// makes when the UI resolves a branch.
	contains(t, text, `GRAIN_IMAGE_TAG="${GRAIN_IMAGE_TAG:-}"`)
	contains(t, text, `GRAIN_IMAGE_TAG="${GRAIN_REF//\//..}"`)
}

// The container is what runs unprivileged; the docker client is not.
//
// `docker run --user` is what keeps the store, the secrets database and
// every sandbox working tree owned by $GRAIN_USER exactly as they were
// before any of this was containerized -- while the unit itself has to
// start as root to reach a root-owned docker socket. A `User=` line back
// in this unit would mean the opposite of what it used to.
func TestTheUnitRunsTheImageAsTheUnprivilegedAccount(t *testing.T) {
	text := setupText(t)
	unit := between(t, text, "cat > /etc/systemd/system/grain-daemon.service", "\nUNIT\n")
	contains(t, unit, "ExecStart=$DOCKER_BIN run --name grain-daemon")
	absent(t, unit, "User=")
	// The image is read from an EnvironmentFile rather than written into
	// the unit, so an upgrade repoints the deployment by writing one line
	// (pkg/upgrade/image.go) with no unit to rewrite -- a root-owned one,
	// filled in from that line by a root pre-step that reads it as data
	// (#2405). The file the daemon writes is never one systemd reads.
	contains(t, unit, "EnvironmentFile=${UNIT_IMAGE_REF_FILE}")
	contains(t, unit, "ExecStartPre=${IMAGE_REF_PROMOTER}")
	absent(t, unit, "EnvironmentFile=${IMAGE_REF_FILE}")
	contains(t, unit, `\${GRAIN_IMAGE}`)

	args := dockerRunArgs(t, text)
	contains(t, args, `--user "${uid}:${gid}"`)
	contains(t, args, "--network host")
	// Sandbox containers are the daemon's CPU-heavy siblings. Give the
	// daemon twice their default relative weight so a saturated host does
	// not also starve the UI; a share leaves its CPU bursts uncapped.
	contains(t, args, "--cpu-shares 2048")
	// Host-wide /proc/stat figures and a container-only process list make
	// top's CPU accounting appear not to add up. Sharing the PID namespace
	// makes the process table describe the same machine as its summary.
	contains(t, args, "--pid host")
	// The store and the sandbox working trees, at the paths they have out
	// here -- granule hands docker host paths, so a mount anywhere else
	// would not resolve. GRAIN_SRC_DIR is deliberately not in this list
	// any more: the only thing in the container that read it was the
	// self-debug capability, and the image carries its own copy of the
	// source now (TestTheImageCarriesTheSourceSelfDebugReads).
	for _, mount := range []string{"GRAIN_DATA_DIR", "GRAIN_SANDBOX_DIR"} {
		if !strings.Contains(args, "${"+mount+"}:${"+mount+"}") {
			t.Errorf("%s is not mounted at its own path", mount)
		}
	}
}

// An operator may add daemon-container flags without asking setup.sh to own
// their local file. The live Trixie installer test proves the mount and failure
// behavior; this smaller assertion keeps the contract and the useful setup
// error present in every ordinary test run.
func TestTheDaemonUnitReadsOperatorDockerArguments(t *testing.T) {
	text := setupText(t)
	unit := between(t, text, "cat > /etc/systemd/system/grain-daemon.service", "\nUNIT\n")
	contains(t, unit, "EnvironmentFile=-/etc/default/grain-daemon")
	contains(t, unit, `\$GRAIN_EXTRA_DOCKER_RUN_ARGS`)

	enable := body(t, setupCode(t), "enable_services() {")
	contains(t, enable, "if systemctl restart grain-daemon.service; then")
	contains(t, enable, `docker inspect --format '{{.State.Running}}' grain-daemon`)
	contains(t, enable, "grain-daemon.service failed to start")
	contains(t, enable, "GRAIN_EXTRA_DOCKER_RUN_ARGS")
	contains(t, enable, "systemctl status grain-daemon.service --no-pager")
}

// The docker socket is the one thing here that grants the container root
// on the host. granule (granule, and the docker-exec sandbox transport)
// and the Upgrade button's own `docker pull` are the two features that
// need it; a deployment running neither should not be handed it.
func TestTheDockerSocketIsOnlyMountedWhenSomethingNeedsIt(t *testing.T) {
	args := dockerRunArgs(t, setupText(t))

	var socketLine string
	for _, line := range strings.Split(args, "\n") {
		if strings.Contains(line, "docker.sock") {
			socketLine = line
			break
		}
	}
	if socketLine == "" {
		t.Fatal("the docker socket is never mounted")
	}

	guard := lastLine(args[:strings.Index(args, socketLine)])
	contains(t, guard, `GRAIN_GRANULE_ENABLE" = "1"`)
	contains(t, guard, `GRAIN_ENABLE_UI_UPGRADE" = "1"`)
}

// --- grain hostd, by default ------------------------------------------
//
// Host mode is gone (task 5041): every machine install is container mode,
// with grain hostd unless GRAIN_HOSTD=0 leaves it out.

// resolveHostd runs resolve_hostd with env and returns what it printed
// and the GRAIN_HOSTD it settled on.
func resolveHostd(t *testing.T, env ...string) (out, hostd string) {
	t.Helper()
	out = runBash(t, logFn+shellFunction(t, "resolve_hostd")+`resolve_hostd; echo "HOSTD=$GRAIN_HOSTD"`,
		append([]string{"GRAIN_USER=grain", "GRAIN_AGY_PATH=", "GOOGLE_AGY_CLI_PATH=/google/bin/agy", "HOSTD_AGY_PATH=",
			"GRAIN_CLAUDE_RUN=", "GRAIN_CODEX_RUN=", "GRAIN_AGY_RUN="}, env...)...)
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "HOSTD="); ok {
			hostd = v
		}
	}
	return out, hostd
}

func TestHostdIsInstalledByDefaultOnEveryTrack(t *testing.T) {
	contains(t, setupText(t), `GRAIN_HOSTD="${GRAIN_HOSTD:-auto}"`)
	for _, track := range []string{"oss", "google"} {
		out, hostd := resolveHostd(t, "GRAIN_HOSTD=auto", "GRAIN_INSTALL_TRACK="+track)
		if hostd != "1" {
			t.Errorf("track %s: auto settled on GRAIN_HOSTD=%q, want 1:\n%s", track, hostd, out)
		}
		contains(t, out, "GRAIN_HOSTD=0 leaves it out")
	}
	// Only the Google track's allowlist names agy; the core one is empty
	// but installed, for an operator to add entries to.
	out := runBash(t, logFn+shellFunction(t, "resolve_hostd")+`resolve_hostd; echo "AGY=[$HOSTD_AGY_PATH]"`,
		"GRAIN_USER=grain", "GRAIN_AGY_PATH=", "HOSTD_AGY_PATH=", "GOOGLE_AGY_CLI_PATH=/google/bin/agy", "GRAIN_HOSTD=auto", "GRAIN_INSTALL_TRACK=oss")
	contains(t, out, "AGY=[]")
	out = runBash(t, logFn+shellFunction(t, "resolve_hostd")+`resolve_hostd; echo "AGY=[$HOSTD_AGY_PATH]"`,
		"GRAIN_USER=grain", "GRAIN_AGY_PATH=", "HOSTD_AGY_PATH=", "GOOGLE_AGY_CLI_PATH=/google/bin/agy", "GRAIN_HOSTD=auto", "GRAIN_INSTALL_TRACK=google")
	contains(t, out, "AGY=[/google/bin/agy]")
	allow := runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content", "HOSTD_AGY_PATH=")
	contains(t, allow, "clis: {}")
}

func TestHostdOptOutLeavesItOutAndRemovesAnInstalledOne(t *testing.T) {
	out, hostd := resolveHostd(t, "GRAIN_HOSTD=0")
	if hostd != "0" {
		t.Fatalf("GRAIN_HOSTD=0 settled on %q:\n%s", hostd, out)
	}
	contains(t, out, `"host" is not offered`)
	// A host run location it has no hostd for would stop the daemon
	// starting (daemon.CheckCLIRunFlags), so it is dropped.
	out, _ = resolveHostd(t, "GRAIN_HOSTD=0", "GRAIN_AGY_RUN=host")
	contains(t, out, "ignoring GRAIN_AGY_RUN=host: there is no grain hostd")
	// hostd refuses to run as root, so auto installs none for root.
	if out, hostd := resolveHostd(t, "GRAIN_HOSTD=auto", "GRAIN_USER=root"); hostd != "0" {
		t.Errorf("auto with GRAIN_USER=root settled on %q:\n%s", hostd, out)
	}

	code := setupCode(t)
	units := body(t, code, "write_hostd_units() {")
	off := between(t, units, `if [ "${GRAIN_HOSTD:-0}" != "1" ]; then`, "return 0")
	contains(t, off, "systemctl disable --now grain-hostd.service")
	contains(t, off, `rm -f "$HOSTD_UNIT"`)
	// The socket mount and -hostd-socket go with it: both are written
	// only when GRAIN_HOSTD=1, and the unit is rewritten on every run.
	contains(t, code, `[ "${GRAIN_HOSTD:-0}" = "1" ] && daemon_args+=(-hostd-socket "$HOSTD_SOCKET")`)
	runArgs := body(t, code, "docker_run_args() {")
	contains(t, runArgs, `if [ "${GRAIN_HOSTD:-0}" = "1" ]; then`)
}

func TestTheReadinessReportSaysWhetherHostdIsInstalled(t *testing.T) {
	readiness := body(t, setupCode(t), "report_readiness() {")
	contains(t, readiness, `hostd:             installed -- grain-hostd.service as $GRAIN_USER; allowlist $HOSTD_ALLOWLIST`)
	contains(t, readiness, `$(hostd_allowlist_entries)`)
	contains(t, readiness, `hostd:             not installed (GRAIN_HOSTD=0)`)

	dir := t.TempDir()
	allow := filepath.Join(dir, "hostd.yaml")
	content := runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content",
		"HOSTD_AGY_PATH=/google/bin/agy", "GRAIN_DATA_DIR=/d", "GRAIN_SANDBOX_DIR=/s", "HOSTD_PROXY_VARS=")
	if err := os.WriteFile(allow, []byte(content+"  corp-search:\n    command: /x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runBash(t, shellFunction(t, "hostd_allowlist_entries")+"hostd_allowlist_entries", "HOSTD_ALLOWLIST="+allow)
	// The default offers the built-in capability servers worth running on
	// the host (pkg/capability/intree, Builder.Host), plus host-git.
	contains(t, out, "clis: claude-detached, codex-detached, agy; capabilities: host-git, grain-gchat, grain-gcp, grain-host-script, corp-search")
	if err := os.WriteFile(allow, []byte(runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content", "HOSTD_AGY_PATH=")), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runBash(t, shellFunction(t, "hostd_allowlist_entries")+"hostd_allowlist_entries", "HOSTD_ALLOWLIST="+allow)
	contains(t, out, "clis: none; capabilities: host-git, grain-gchat, grain-gcp, grain-host-script")
}

// A migrating host-mode install loses what host mode alone left on the
// machine: the nft sudoers rule, the old extractor marker, the source
// copy and the drop-in a host-mode Upgrade wrote.
func TestAMigratingHostInstallLosesWhatOnlyHostModeHad(t *testing.T) {
	wrappers := body(t, setupCode(t), "install_cli_wrappers() {")
	contains(t, wrappers, `rm -f "$HOST_NFT_SUDOERS" /usr/local/lib/grain/.extracted-image-id`)
	migrated := between(t, wrappers, `if [ "$GRAIN_MIGRATED_FROM_HOST_MODE" = 1 ]; then`, "\n  fi\n")
	contains(t, migrated, `rm -rf "$HOST_SOURCE_DIR"`)
	contains(t, migrated, "rm -f /etc/systemd/system/grain-daemon.service.d/grain.conf")
}

// Every GRAIN_* variable this script reads is given a default.
//
// setup.sh runs under `set -u`, where reading a variable nobody set is
// not an empty string but an abort -- and an abort in the middle of an
// install, after a user has been created and an image pulled, rather
// than at the top where the other bad-input checks are.
//
// It is written as a sweep rather than as a list because of how the one
// that got through arrived: removing the remote executor (#1395) took
// the three GRAIN_GCP_* defaults out along with the block above them,
// and every install -- both modes, with or without any GCP
// configuration at all -- then died in seed_gcp_minter_key on
// "GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE: unbound variable". Nothing in
// this suite was asking the question, because the failure is not in any
// one of the lines that changed.
func TestEveryVariableTheInstallerReadsHasADefault(t *testing.T) {
	code := setupCode(t)

	assigned := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(GRAIN_[A-Z0-9_]+)=`).FindAllStringSubmatch(code, -1) {
		assigned[m[1]] = true
	}
	// The two this script deliberately never sets, because it is not the
	// shell running it that expands them: one is interpolated by systemd
	// out of the unit's own EnvironmentFile, and the other by the `sh`
	// inside the deployment image that image_run hands a script to.
	elsewhere := map[string]bool{
		"GRAIN_EXTRA_DOCKER_RUN_ARGS": true,
		"GRAIN_TARGET_URL":            true,
	}

	reported := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$\{?(GRAIN_[A-Z0-9_]+)(:-|:\+|##|%%)?`).FindAllStringSubmatch(code, -1) {
		name, guard := m[1], m[2]
		if guard != "" || assigned[name] || elsewhere[name] || reported[name] {
			continue
		}
		reported[name] = true
		t.Errorf("$%s is read but never given a default: under `set -u` every install that does not set it in its own environment aborts where it is read", name)
	}
}

// Everything this script lays out is owned by the account's real group.
//
// "$GRAIN_USER" was the group at each of the two dozen places that
// needed one, which is the same string only for the account setup.sh
// creates itself -- useradd gives a system user a group of its own
// name. Point GRAIN_USER at an account that already exists, as a host
// with an operator account for this does, and every one of those calls
// is `install -g brandon` on a host whose group is `staff`: the deploy
// died with "install: invalid group", halfway through laying out the
// sandbox directory, in both modes.
func TestTheDeploymentIsOwnedByTheAccountsRealGroup(t *testing.T) {
	code := setupCode(t)

	contains(t, body(t, code, "ensure_user() {"), `GRAIN_GROUP="$(id -gn "$GRAIN_USER")"`)
	// ...and `id` still asks about the *account*. It takes a user name,
	// so `id -g "$GRAIN_GROUP"` is "the gid of the user called staff",
	// which fails with "no such user" for exactly the account GRAIN_GROUP
	// exists to support -- while reading correctly for the one whose
	// group is its own name, so nothing but a mismatched account finds
	// it. The numbers it resolves are the pair `docker run --user` is
	// given, in both the daemon's unit and the CLI wrapper.
	if strings.Contains(code, `id -g "$GRAIN_GROUP"`) || strings.Contains(code, `id -u "$GRAIN_GROUP"`) {
		t.Error(`id is being asked about "$GRAIN_GROUP", which is a group name: it takes an account`)
	}
	if n := strings.Count(code, `gid="$(id -g "$GRAIN_USER")"`); n != 2 {
		t.Errorf(`%d places resolve the numeric gid from the account, want 2 (the daemon's own --user, and the CLI wrapper's)`, n)
	}
	// Line by line, and skipping `id -g`, which takes an account and so
	// says "$GRAIN_USER" correctly: what must not come back is an
	// install or a chown naming the *group* after the user.
	for _, line := range strings.Split(code, "\n") {
		if !strings.Contains(line, `-g "$GRAIN_USER"`) || strings.Contains(line, `id -g "$GRAIN_USER"`) {
			continue
		}
		t.Errorf(`ownership call names the group "$GRAIN_USER", which is a group only for the account this script creates: %s`, strings.TrimSpace(line))
	}
	if strings.Contains(code, `"$GRAIN_USER:$GRAIN_USER"`) {
		t.Error(`some chown still names the group "$GRAIN_USER", which is a group only for the account this script creates`)
	}

	// The group is resolved by ensure_user, so every step that lays
	// something out has to come after it.
	main := from(t, code, "main() {")
	for _, step := range []string{"setup_sandbox_dir", "setup_data_dir", "write_systemd_units"} {
		before(t, main, "ensure_user", step,
			"the account's group is resolved before "+step+" owns anything by it")
	}
}

// The control channel is the account's own, not its group's.
//
// A touch of a file in that directory is a reboot, a poweroff or a
// restart of this host's daemon, so who can write there is the whole of
// what this channel grants. Nothing
// needs the group bit to do it: the daemon owns the directory, and
// everything on the other end of the exchange runs as root, which no
// directory mode applies to. It was 0770 anyway, which granted all of
// the above to every account in $GRAIN_GROUP -- nobody at all for the
// private group of the account setup.sh creates, and a real widening
// for a GRAIN_USER whose primary group is shared.
func TestTheControlChannelIsTheAccountsAlone(t *testing.T) {
	code := setupCode(t)
	units := body(t, code, "write_control_units() {")

	contains(t, units, `grain_dir 0700 "$CONTROL_DIR"`)
	if strings.Contains(units, "-m0770") {
		t.Error("the control directory is group-writable again: every account in $GRAIN_GROUP can then ask this host to reboot, power off, or restart the daemon")
	}
}

// An operator's own Codex login is checked in both modes.
//
// That check used to live in docker_run_args, which was right while a
// mount was the only thing the value did -- and wrong the moment a
// second mode existed: host mode never calls that function, the daemon
// is passed -codex-auth-file either way, and so exactly one of the two
// deployments that depend on the path validated it. The other found out
// at its first dispatched Codex task.
func TestTheCodexLoginIsCheckedInBothModes(t *testing.T) {
	code := setupCode(t)
	args := dockerRunArgs(t, code)

	// Out of the container-only argument list...
	absent(t, args, "must be an absolute host path")
	absent(t, args, "does not name an existing regular file")
	// ...and into the checks every install runs, before either unit is
	// written and before anything on the host has been touched.
	const absolute = "GRAIN_CODEX_AUTH_FILE must be an absolute host path"
	contains(t, code, absolute)
	contains(t, code, "GRAIN_CODEX_AUTH_FILE does not name an existing regular file")
	before(t, code, absolute, "docker_run_args() {",
		"the login is checked before the container-only argument list is built")

	// What is left in there is the mount, which really is container
	// mode's alone -- and the flag, which both modes pass.
	contains(t, args, `--volume "$GRAIN_CODEX_AUTH_FILE:$GRAIN_CODEX_AUTH_FILE"`)
	contains(t, body(t, code, "write_systemd_units() {"), `-codex-auth-file "$GRAIN_CODEX_AUTH_FILE"`)
}

// Losing the daemon's container is not losing docker: both sandbox
// backends setup.sh installs start their sandboxes through this host's
// engine, and with host-directory sandboxes gone no install reaches a host
// with no engine at all.
func TestHostModeStillNeedsDockerForItsSandboxes(t *testing.T) {
	code := setupCode(t)
	required := body(t, code, "docker_required() {")
	contains(t, required, "return 0")
	absent(t, required, "return 1")

	ensure := body(t, code, "ensure_docker() {")
	contains(t, ensure, "required command not found: docker")
	absent(t, ensure, "GRAIN_DOCKER_AVAILABLE=0")
}

// The mode is one of two words, and a misspelling installs nothing: this
// is the last point at which nothing on the host has been written.
func TestTheInstallModeIsCheckedBeforeAnythingIsInstalled(t *testing.T) {
	code := setupCode(t)
	contains(t, code, `GRAIN_INSTALL_MODE="${GRAIN_INSTALL_MODE:-}"`)
	contains(t, code, "GRAIN_INSTALL_MODE must be")
	contains(t, code, "GRAIN_INSTALL_MODE=host is no longer supported")
	before(t, code, "if ! resolve_install_mode /etc/systemd/system/grain-daemon.service", "ensure_docker()",
		"the mode is settled and validated before the first thing this script installs")
	// Host mode is gone, and so is everything that existed only for it.
	for _, gone := range []string{"GRAIN_HOST_BINARY", "GRAIN_HOST_SOURCE_DIR", "write_host_daemon_unit",
		"install_host_binaries", "grant_journal_group", "grant_cli_access", "ensure_git()"} {
		absent(t, code, gone)
	}
}

// bwsalmon/agents#645 replaced two NOPASSWD sudoers drop-ins.
//
// `systemctl` inside the container reaches no systemd that matters, so
// the daemon touches a file under $GRAIN_DATA_DIR/control and a .path
// unit out on the host turns it into the real command. PathModified, not
// PathExists: a leftover request file must not become a reboot the next
// time this host boots.
func TestTheContainerReachesTheHostThroughPathUnitsNotSudo(t *testing.T) {
	text := setupText(t)
	code := setupCode(t)

	contains(t, text, "-reboot-cmd touch -reboot-cmd")
	contains(t, text, "-upgrade-restart-cmd touch -upgrade-restart-cmd")
	// The two the System pane's Power tab added (grain/task-40): its own
	// shutdown unit, and a -restart-cmd pointed at the request file the
	// Upgrade button's own restart already writes -- one action, one
	// unit, whoever asked for it.
	contains(t, text, "-shutdown-cmd touch -shutdown-cmd")
	contains(t, text, "-restart-cmd touch -restart-cmd")
	for _, unit := range []string{
		"grain-reboot.path", "grain-reboot.service",
		"grain-shutdown.path", "grain-shutdown.service",
		"grain-restart.path", "grain-restart.service",
	} {
		if !strings.Contains(text, "/etc/systemd/system/"+unit) {
			t.Errorf("%s is not written", unit)
		}
	}
	// One request file per unit, all three named -- a button wired to a
	// file nothing watches is a button that silently does nothing.
	contains(t, text, "PathModified=${CONTROL_DIR}/reboot")
	contains(t, text, "PathModified=${CONTROL_DIR}/shutdown")
	contains(t, text, "PathModified=${CONTROL_DIR}/restart")
	// Each unit still runs exactly one fixed command -- that is what
	// keeps this channel narrower than the sudoers rules it replaced, and
	// adding a button must not widen it.
	contains(t, text, "ExecStart=/usr/bin/systemctl reboot")
	contains(t, text, "ExecStart=/usr/bin/systemctl poweroff")
	contains(t, text, "ExecStart=/usr/bin/systemctl restart grain-daemon.service")
	// Watching, not merely written, and the same argument as the root
	// shell's own --now below: a .path unit that is installed but not
	// running answers a power button with nothing at all.
	// tests/installer asks systemd whether that actually happened.
	contains(t, code, "systemctl enable --now grain-reboot.path grain-shutdown.path grain-restart.path")
	absent(t, code, "PathExists=")
	// No sudoers file is written for any of this any more, and a host
	// upgraded across this change has the two it used to write removed.
	// Host mode's nft grant went with host mode, and a migrating install
	// has it removed too.
	absent(t, code, "visudo")
	contains(t, code, `rm -f "$HOST_NFT_SUDOERS"`)
	contains(t, text, "rm -f /etc/sudoers.d/grain-daemon-reboot /etc/sudoers.d/grain-daemon-upgrade")
}

// The fourth unit on that channel is gone (grain/task-193).
//
// grain-shell.path watched $CONTROL_DIR/shell and ran whatever command
// it found there as root, for the UI's Root shell tab (grain/task-13).
// Unlike the three units beside it, which each run one command this
// script names, it handed unrestricted root on the host to whatever
// could reach a UI that carries no auth of its own -- so it is gone from
// the daemon, from the UI and from here.
//
// Removing it from this script is not enough on its own: a host deployed
// before this still has the unit installed and enabled, watching a file
// nothing writes any more. So a re-run takes it back, the same way this
// script already removes the sudoers drop-ins its control units replaced.
func TestSetupRemovesTheRootShellResponder(t *testing.T) {
	text := setupText(t)
	code := setupCode(t)

	// Nothing installs it any more: no units, no responder script, no
	// flag pointing the daemon at the exchange.
	absent(t, text, "PathModified=${CONTROL_DIR}/shell")
	absent(t, text, "Run one command as root")
	absent(t, text, `bash -lc "$command"`)
	absent(t, code, "-root-shell-control-dir")
	absent(t, code, "systemctl enable --now grain-shell.path")

	// And a re-run takes back what an earlier one installed, as part of
	// writing the control units it belonged beside.
	contains(t, body(t, code, "write_control_units() {"), "remove_root_shell_units")
	removal := body(t, code, "remove_root_shell_units() {")
	contains(t, removal, "systemctl disable --now grain-shell.path")
	contains(t, removal, "rm -f /etc/systemd/system/grain-shell.path /etc/systemd/system/grain-shell.service")
	contains(t, removal, `rm -f "$ROOT_SHELL_RESPONDER"`)
	// Including whatever the last command run through it left lying in
	// the control directory.
	contains(t, removal, `rm -f "$CONTROL_DIR/shell" "$CONTROL_DIR/shell.out" "$CONTROL_DIR/shell.status"`)
}

// A deployed host does not suspend under the work it was given.
//
// The failure is silent and looks like nothing else: the daemon's clock
// stops mid-task, the UI stops answering, and whatever the agent was
// doing -- a clone, a push, a run of CI it was waiting on -- resumes
// minutes or hours later against timed-out connections, with nothing in
// any log saying the machine was asleep. Nothing else in this suite
// reads the deploy's power policy.
//
// Every route in has to be closed, which is why this asserts the whole
// set rather than "suspend is mentioned": masking sleep.target alone
// still leaves a host that hibernates, and masked targets alone leave a
// laptop failing a suspend every time its lid shuts.
func TestTheDeployedHostDoesNotSuspendUnderARunningTask(t *testing.T) {
	code := setupCode(t)

	for _, target := range []string{
		"sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target",
	} {
		if !strings.Contains(code, target) {
			t.Errorf("%s is neither masked nor unmasked, so a host can still reach it", target)
		}
	}
	contains(t, code, `systemctl mask "${SLEEP_TARGETS[@]}"`)
	// logind's own events, so a closed lid or a sleep key is not an
	// endlessly retried, endlessly failing suspend.
	contains(t, code, "HandleLidSwitch=ignore")
	contains(t, code, "HandleSuspendKey=ignore")
	contains(t, code, "IdleAction=ignore")

	// Reversible from the same script that did it. A masked unit and a
	// drop-in outlive the deployment that wrote them, so an operator who
	// turns this off has to be able to turn it off by re-running the
	// installer rather than by reverse-engineering what it masked.
	suspend := body(t, code, "disable_host_suspend() {")
	contains(t, suspend, `systemctl unmask "${SLEEP_TARGETS[@]}"`)
	contains(t, suspend, `rm -f "$LOGIND_DROPIN"`)

	// And the daemon is not started onto a host that might still sleep.
	before(t, code, "disable_host_suspend\n", "enable_services\n",
		"the daemon is started before this host is told not to suspend")
}

// On a host deployed before this change both names are symlinks into
// $GRAIN_DATA_DIR/bin, and `cat >` follows one -- which would write the
// wrapper over the binary at the far end and leave /usr/local/bin/grain
// still pointing at it.
func TestTheCLIWrappersReplaceASymlinkRatherThanWritingThroughIt(t *testing.T) {
	text := setupText(t)
	contains(t, text, "rm -f /usr/local/bin/granule\n")
	contains(t, text, `[ "${GRAIN_HOSTD:-0}" = "1" ] || rm -f /usr/local/bin/grain`)
	before(t, text, "rm -f /usr/local/bin/granule", "cat > /usr/local/bin/granule",
		"the wrapper is written before the symlink is removed")
	before(t, text, "|| rm -f /usr/local/bin/grain", "cat > /usr/local/bin/grain",
		"the wrapper is written before the symlink is removed")
}

// The CLI has to be told the two things about this deployment it cannot
// work out for itself: where the daemon listens, and where its files are.
//
// The second was missing (grain/task-303). `grain state` and `grain
// secrets` edit the data directory directly and take a -data-dir; the
// wrapper already bind-mounts that directory at its own path, but passed
// nothing naming it -- so the `grain state status` this script's own
// closing report prints failed, typed exactly as printed, with
// "-data-dir is required".
//
// Both halves are asserted: the wrapper's container, which is where a
// `grain` invocation on this host actually runs, and the profile script,
// which is what a shell (and anything reading $GRAIN_DATA_DIR out here,
// like pkg/capability/bootstrap's playbooks) gets.
func TestTheCLIIsToldWhereThisDeploymentsStateLives(t *testing.T) {
	text := setupText(t)

	wrapper := between(t, text, "cat > /usr/local/lib/grain/run-image.sh", "\nWRAPPER\n")
	contains(t, wrapper, "--env GRAIN_DATA_DIR=${GRAIN_DATA_DIR}")
	// Baked, not passed through from the caller the way GRAIN_SERVER is:
	// the only data directory inside that container is the one mounted
	// on the next line.
	contains(t, wrapper, "--volume ${GRAIN_DATA_DIR}:${GRAIN_DATA_DIR}")

	profile := body(t, text, "write_cli_profile() {")
	contains(t, profile, `export GRAIN_DATA_DIR="${GRAIN_DATA_DIR}"`)
	contains(t, profile, `export GRAIN_SERVER=`)
	// The data directory is exported even when GRAIN_UI_ADDR carries no
	// port to build a GRAIN_SERVER out of: the two are unrelated, and
	// that branch used to write no profile script at all.
	absent(t, profile, "not writing /etc/profile.d/grain.sh")
}

// The `grain` CLI is a `docker run` with the data dir bind-mounted.
//
// Docker creates a missing bind-mount source itself, as root and with a
// mode nothing asked for -- so anything invoking that CLI before
// setup_data_dir has run would hand the deployment a data directory
// docker invented rather than one this script laid out.
func TestTheDataDirectoryIsLaidOutBeforeTheCLIIsUsed(t *testing.T) {
	main := from(t, setupCode(t), "main() {")
	// ensure_granule_images is the first step that runs the CLI (for
	// `grain sandbox-image`); reformat_store_if_schema_changed and
	// report_readiness follow it.
	before(t, main, "setup_data_dir", "ensure_granule_images",
		"the CLI runs before the data directory is laid out")
	before(t, main, "setup_data_dir", "reformat_store_if_schema_changed",
		"the store is reformatted before the data directory is laid out")
}

// The CLI is a `docker run --user $GRAIN_USER`; this script is root.
//
// A file root writes with a 0600 umask is one that CLI cannot read --
// and `grain secrets set -value-file` failing takes the whole deploy
// down with it under `set -e`, after the image is pulled and before
// grain-daemon.service is ever written. That is not hypothetical: it is
// what a fresh VM with a minter key actually did, leaving a host with the
// sync service, the image, and no daemon.
//
// So anything staged for that CLI is written by $GRAIN_USER
// (grain_write), rather than chowned afterwards and briefly readable by
// nobody but root.
func TestEveryFileHandedToTheContainerisedCLIIsReadableByIt(t *testing.T) {
	staged := body(t, setupCode(t), "seed_gcp_minter_key() {")

	if !strings.Contains(staged, `-value-file "$staged"`) {
		t.Error("the staged copy is no longer what is handed over")
	}
	if !strings.Contains(staged, `grain_write 0600 "$staged" < "$GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE"`) {
		t.Error("the minter key is staged without giving it to $GRAIN_USER")
	}
	// The shape that caused it, so it cannot come back by hand.
	absent(t, staged, "umask 077 && cat")
}

// The minter key is the one secret this deployment rotates on its own,
// and for a while the two halves of that disagreed.
//
// deploy/push-secrets.sh mints a fresh minter key on every run and then
// deletes every key on the account beyond the newest two, while
// seed_gcp_minter_key returned early whenever the host already had a
// gcp-key-minter entry -- so a host kept authenticating with the key
// from its first deploy until the third push-secrets.sh run deleted it,
// and every gcp-key mint after that failed with Google's `invalid_grant`
// on a deployment whose Capabilities tab still read Ready.
//
// So the seed converges: a key handed to this script is the key the
// daemon ends up holding. The early return that is left is the one for
// a deploy carrying no key at all, which must still leave an operator's
// own `grain secrets set` alone.
func TestTheMinterKeyIsReseededOnEveryRunSoRotationReachesTheDaemon(t *testing.T) {
	seed := body(t, setupCode(t), "seed_gcp_minter_key() {")

	if strings.Contains(seed, `grep -q '^gcp-key-minter:'`) {
		t.Error("seed_gcp_minter_key still skips a run whose key it has already seen, " +
			"so a rotated minter key never reaches the daemon")
	}
	if !strings.Contains(seed, `if [ -z "$GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE" ]; then`) {
		t.Error("seed_gcp_minter_key no longer leaves an existing credential alone when " +
			"this deploy carries no key of its own")
	}
	// pkg/secrets.Store.Resolve answers the bare "gcp-key-minter" name
	// only while the secret holds exactly one key, so writing under a
	// fixed name would break a secret first written from Settings (which
	// uses `value`) in a second way -- the resolve would fail as
	// ambiguous rather than hand back either key.
	if !strings.Contains(seed, "minter_secret_key") {
		t.Error("the key is written under a fixed name rather than the one the secret " +
			"already holds, which can leave gcp-key-minter with two keys and no way " +
			"to resolve the bare name")
	}
}

// `set -e` plus a command substitution is a trap worth pinning.
//
// Each of these assigns the output of the containerised CLI to a variable
// and then *checks* it -- but a non-zero exit inside `$( )` in an
// assignment aborts the script before the check ever runs, turning
// "report this and carry on" into "no service on this host". Every one of
// them has to tolerate the failure it is written to describe.
func TestACLIThatCannotAnswerDoesNotAbortTheDeploy(t *testing.T) {
	code := setupCode(t)

	for _, call := range []string{"grain_cli sandbox-image", "grain_cli schema-version"} {
		var lines []string
		for _, line := range strings.Split(code, "\n") {
			if strings.Contains(line, call) && strings.Contains(line, `="$(`) {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			t.Errorf("%s is no longer assigned from a command substitution", call)
			continue
		}
		for _, line := range lines {
			if !strings.Contains(line, "|| true") {
				t.Errorf("%s aborts the deploy instead of reporting: %s", call, strings.TrimSpace(line))
			}
		}
	}

	// agent_cli_in_image is assigned in two places; it absorbs the
	// failure itself so neither caller has to.
	contains(t, body(t, code, "agent_cli_in_image() {"), "|| true")
}

func TestTheUpgradeButtonIsWiredToTheImagePath(t *testing.T) {
	text := setupText(t)
	contains(t, text, `-upgrade-image "$GRAIN_IMAGE"`)
	contains(t, text, `-upgrade-image-ref-file "$IMAGE_REF_FILE"`)
	// The binary path's flag would ask the daemon to build on a host with
	// no toolchain at all.
	absent(t, setupCode(t), "-upgrade-install-path")
}

// The source the self-debug capability reads travels inside the image.
//
// read_grain_source answers "what is the binary I am running made of", so
// the source and the binary have to be the same commit. They were not: the
// daemon read a host checkout bind-mounted in, and that checkout tracks a
// *branch* while the image is a fixed tag -- an upgrade repointed one
// without touching the other, so the agent read the old source while the
// new binary ran, and a rollback left it reading source newer than the
// code running. Both silent.
//
// Three files have to agree for the fix to hold, and nothing else looks at
// all three: the Dockerfile has to carry the tree, cmd/grain has to look
// where it was put, and setup.sh must not reintroduce the mount or the
// flag that used to win over it.
func TestTheImageCarriesTheSourceSelfDebugReads(t *testing.T) {
	// The runtime stage takes the staged copy, not /src -- which still has
	// .git, this build's own bin/, the node_modules `make build` just
	// created in it, and whatever else was sitting in the directory the
	// build context came from.
	dockerfile := read(t, "Dockerfile")
	contains(t, dockerfile, "COPY --from=build /src-export /usr/local/share/grain/src")
	contains(t, dockerfile, "/src/scripts/export-source.sh /src /src-export")

	// pkg/daemon looks exactly there.
	contains(t, read(t, "pkg", "daemon", "capabilities.go"),
		`const defaultSourceDir = "/usr/local/share/grain/src"`)

	// ...and the deployment no longer hands it a host checkout to prefer
	// instead, nor mounts one for it to find.
	code := setupCode(t)
	absent(t, code, "-upgrade-src-dir")
	absent(t, code, "${GRAIN_SRC_DIR}:${GRAIN_SRC_DIR}:ro")
}

func TestTheDockerfileCarriesEveryBinaryGrainShellsOutTo(t *testing.T) {
	text := read(t, "Dockerfile")
	for _, pkg := range []string{"git", "openssh-client", "ca-certificates", "systemd"} {
		if !strings.Contains(text, pkg) {
			t.Errorf("%s is not installed in the runtime image", pkg)
		}
	}
	contains(t, text, "granule")
	// Every agent CLI, not just one: the framework a run uses is a live
	// per-task choice, so an image with only some of them fails every run
	// that chooses another. agy was the one nothing installed anywhere
	// until bwsalmon/agents#645 -- an operator's manual step on every
	// host, for the *default* framework.
	contains(t, text, "downloads.claude.ai/claude-code-releases")
	contains(t, text, "storage.googleapis.com/antigravity-public/antigravity-cli")
	contains(t, text, "openai/codex/releases/download")
	// CAP_NET_BIND_SERVICE reaches a non-root process in a container only
	// through a file capability -- --cap-add alone grants it nothing, so
	// the default -ui-addr (port 80), and the package cache's 53, 443 and
	// 80, would all fail to bind without this.
	contains(t, text, "setcap cap_net_bind_service=+ep /usr/local/bin/grain")
	// The entrypoint is the binary, so `docker run <image> schema-version`
	// runs the CLI -- which is how setup.sh's own wrapper and pkg/upgrade's
	// image health check both invoke it.
	contains(t, text, `"/usr/local/bin/grain"]`)
}

// The privileged ports this daemon binds are not only the UI's.
//
// Both installer modes used to grant CAP_NET_BIND_SERVICE for a
// -ui-addr below 1024 and for nothing else, which is the condition that
// silently switched the package cache's transparent half off on every
// deployment whose UI sits on a high port: the interception resolver
// binds 53 and the listeners its answers redirect a fetch to bind 443
// and 80 (pkg/cli/packagecache.go), so a daemon running as an
// unprivileged account without the capability cannot bind any of the
// three. There is nothing at install time to condition the grant on
// either -- the cache is a live Settings toggle with no flag and no
// environment variable here -- so it is unconditional in both modes,
// and this is the test that keeps it that way.
func TestBothInstallerModesLetTheDaemonBindPrivilegedPorts(t *testing.T) {
	code := setupCode(t)

	// The bounding-set entry the image's file capability
	// on the grain binary is raised from (Dockerfile).
	runArgs := body(t, code, "docker_run_args() {")
	contains(t, runArgs, "DOCKER_ARGS+=(--cap-add NET_BIND_SERVICE)")

	// And not behind a test on -ui-addr's port. Asserted on the guard
	// rather than on the grant, because the way this regresses is
	// somebody putting the condition back rather than deleting the line:
	// a `-lt 1024` anywhere near the grant is the bug.
	if strings.Contains(runArgs, "-lt 1024") {
		t.Errorf("docker run's arguments still condition CAP_NET_BIND_SERVICE on a privileged -ui-addr port, " +
			"which leaves the package cache's DNS interception unable to bind 53 on every " +
			"deployment whose UI is on a high port")
	}
}

// A granule NetworkNone sandbox is refused unless the daemon can install
// its host nftables chain (pkg/orchestrator/granule_firewall.go), so each
// installer mode has to give the daemon a way to run nft with
// CAP_NET_ADMIN -- the choice made in grain/task-1364 was a file
// capability on the image's nft in container mode, and a sudoers rule
// naming nft alone in host mode.
func TestBothInstallerModesLetTheDaemonInstallHostFirewallRules(t *testing.T) {
	dockerfile := read(t, "Dockerfile")
	contains(t, dockerfile, "\t\tnftables \\\n")
	contains(t, dockerfile, "setcap cap_net_admin=+ep /usr/sbin/nft")
	// Not on the grain binary: a process holding it would pass it to
	// nothing it forks, so it would not reach the nft that needs it.
	absent(t, dockerfile, "setcap cap_net_admin=+ep /usr/local/bin/grain")

	code := setupCode(t)

	// Container mode: the bounding-set entry nft's file capability is
	// raised from, with granule only -- nothing else writes host rules.
	runArgs := body(t, code, "docker_run_args() {")
	granule := between(t, runArgs, `if [ "$GRAIN_GRANULE_ENABLE" = "1" ]; then`+"\n    DOCKER_ARGS+=(--volume /var/lib/granule", "\n  fi\n")
	contains(t, granule, "DOCKER_ARGS+=(--cap-add NET_ADMIN)")
	if strings.Count(runArgs, "NET_ADMIN") != 1 {
		t.Errorf("docker_run_args grants NET_ADMIN outside its granule branch")
	}

	// Host mode's sudoers rule for nft went with host mode: no install
	// writes one, a migrating one has it removed, and an uninstall takes
	// either name away.
	absent(t, code, "write_host_nft_sudoers")
	contains(t, code, "HOST_NFT_SUDOERS=/etc/sudoers.d/zzz-grain-nft\n")
	contains(t, body(t, code, "install_cli_wrappers() {"), `rm -f "$HOST_NFT_SUDOERS"`)
	contains(t, body(t, code, "uninstall_grain() {"), "/etc/sudoers.d/zzz-grain-nft /etc/sudoers.d/90-grain-nft")
}

// v1's removal promoted the Go tree from v2/ to the root.
//
// Three files encode that layout independently, and nothing else checks
// them together: the Dockerfile builds the binary, the Makefile mounts and
// builds the same tree in a container, and Dockerfile.build is the
// toolchain image both land in. A stale `v2` segment in any one of them is
// invisible to `go build` and to every Go test -- it only surfaces as a
// failed image build, which is minutes into CI and needs a docker daemon
// to reproduce.
//
// That is not hypothetical: `make -C v2 build` survived the promotion in
// the Dockerfile and broke build-artifacts.yml's image build, while the
// whole Go suite stayed green.
func TestEveryBuildPathAgreesTheModuleRootIsTheRepositoryRoot(t *testing.T) {
	dockerfile := read(t, "Dockerfile")
	// Built at the context root, which is the module root.
	contains(t, dockerfile, "RUN make build")
	absent(t, dockerfile, "make -C v2")
	// ...and the binary comes back from the root's own bin/, not v2/bin/.
	contains(t, dockerfile, "COPY --from=build /src/bin/grain")

	makefile := read(t, "Makefile")
	// The containerised build mounts the checkout and works at its root.
	contains(t, makefile, `-v "$(CURDIR)":/src`)
	contains(t, makefile, "-w /src $(BUILDER_IMAGE)")
	// `make image`'s context is this directory, not its parent.
	contains(t, makefile, "-f Dockerfile .")
	absent(t, makefile, "-f Dockerfile ..")

	contains(t, read(t, "Dockerfile.build"), "WORKDIR /src\n")
}

// Why a package appears in this repo's list rather than only the
// account's.
//
// GHCR attaches a container package to a repository by
// org.opencontainers.image.source and nothing else. An image without it
// still builds, still pushes and still pulls -- it just lands under the
// account's packages, unlinked, with the repo's access settings not
// governing it. That is silent in every log, which is why it went
// unnoticed until someone looked for the packages and found none.
//
// Both images need it and neither had it: the grain Dockerfile carried no
// LABEL at all, and the sandbox inherits granule's, which names granule's
// repository. The sandbox's is overridden at build time rather than by
// editing third_party/, so an operator building into their own registry
// keeps the inherited label.
func TestBothPublishedImagesClaimThisRepository(t *testing.T) {
	if !strings.Contains(read(t, "Dockerfile"),
		`org.opencontainers.image.source="https://github.com/ai-on-gke/grain"`) {
		t.Error("the grain image does not claim this repository")
	}
}

// Every place a branch name becomes an image tag spells it the way
// pkg/upgrade.TagForBranch does: release-images.sh for grain CI and
// setup.sh's default. A shell script cannot call TagForBranch, so each
// one's substitution is run here against branches the old '/'-to-'-'
// spelling merged (#2386) and compared with it.
func TestEveryBranchTagSubstitutionMatchesTagForBranch(t *testing.T) {
	sites := []struct {
		name, text, line, variable string
	}{
		{"scripts/release-images.sh", read(t, "scripts", "release-images.sh"), `branch="${GRAIN_CI_REF//\//..}"`, "GRAIN_CI_REF"},
		{"scripts/setup.sh", setupText(t), `GRAIN_IMAGE_TAG="${GRAIN_REF//\//..}"`, "GRAIN_REF"},
	}
	for _, site := range sites {
		contains(t, site.text, site.line)
		for _, branch := range []string{"main", "claude/task-123", "claude-task-123", "a./b", "rc/v1.6"} {
			script := site.line + "\nprintf %s \"${" + strings.SplitN(strings.TrimSpace(site.line), "=", 2)[0] + "}\"\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), site.variable+"="+branch, "GRAIN_IMAGE_TAG=")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: running the substitution for %q: %v", site.name, branch, err)
			}
			if want := upgrade.TagForBranch(branch); string(out) != want {
				t.Errorf("%s tags branch %q as %q, but pkg/upgrade.TagForBranch resolves it to %q", site.name, branch, out, want)
			}
		}
	}
}

// Every model credential stays out of workflows an unreviewed branch can
// trigger.
//
// live-agent.yml runs tests/e2e's broad live agy suite nightly.
// live-frameworks.yml runs the claude and codex probes nightly. Where their
// credentials are held is the whole safety argument: tests.yml runs code from
// an unreviewed PR branch, and release.yml runs on branch releases. Adding
// `${{ secrets.* }}` to either would hand a model credential to code no human
// has read.
//
// The trigger is asserted for the same reason: no `push` and no
// `pull_request`, since those events run code from branches that have not
// been reviewed.
func TestOnlyDefaultBranchLiveJobsHoldAModelCredential(t *testing.T) {
	for name, text := range map[string]string{
		"tests.yml":   testsWorkflow(t),
		"release.yml": workflow(t),
	} {
		if strings.Contains(stripComments(text), "secrets.") {
			t.Errorf("%s reads a secret: it is triggered by branches nobody has reviewed, and is only safe to trigger that way while it holds nothing worth stealing", name)
		}
	}

	live := stripComments(liveAgentWorkflow(t))
	triggers := upTo(t, live, "jobs:")
	contains(t, triggers, "schedule:")
	contains(t, triggers, "workflow_dispatch:")
	for _, forbidden := range []string{"push:", "pull_request:"} {
		if strings.Contains(triggers, forbidden) {
			t.Errorf("live-agent.yml triggers on %s: the model credential it holds would then be readable by any branch", forbidden)
		}
	}

	nightly := namedJob(t, live, "live-agy")
	contains(t, nightly, "GRAIN_LIVE_AGENT_TEST")
	// The whole of tests/e2e's live set, by prefix rather than by name:
	// a job naming one test is a job that silently stops running the next
	// one somebody adds.
	contains(t, nightly, "-run 'TestLive'")
	contains(t, nightly, "-count=1")
}

// live-frameworks.yml is the same arrangement for the other two
// frameworks a deployment can dispatch, and it is asserted separately
// because one thing about it is new: two credentials, from two vendors,
// in one file.
//
// Each credential gets a job of its own -- and the property worth pinning
// is that neither job can read the other's secret. One job holding both
// would mean a key that has to be revoked costs both frameworks their nightly.
func TestTheLiveFrameworkJobsKeepTheirTwoCredentialsApart(t *testing.T) {
	live := stripComments(liveFrameworksWorkflow(t))
	contains(t, live, "schedule:")
	contains(t, live, "workflow_dispatch:")
	for _, forbidden := range []string{"push:", "pull_request:"} {
		if strings.Contains(upTo(t, live, "jobs:"), forbidden) {
			t.Errorf("live-frameworks.yml triggers on %s: the two credentials it holds would then be readable by any branch, which is what the environments exist to prevent", forbidden)
		}
	}

	for _, job := range []struct {
		name        string
		secret      string
		otherSecret string
		pkg         string
	}{
		{"live-claude", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "./pkg/agent/claude/"},
		{"live-codex", "OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "./pkg/agent/codex/"},
	} {
		text := namedJob(t, live, job.name)
		contains(t, text, job.secret)
		if strings.Contains(text, job.otherSecret) {
			t.Errorf("the %s job reads %s as well: a job holding both credentials turns revoking one key into two frameworks losing their nightly", job.name, job.otherSecret)
		}
		// The same three the agy nightly is held to: the live set by
		// prefix rather than by name, so a test added tomorrow runs
		// without a workflow change; -count=1, so a cached "ok" from a
		// run that skipped can never stand in for a live one; and
		// GRAIN_LIVE_AGENT_TEST, so a missing credential or a missing
		// binary fails instead of skipping -- a skip and a pass are the
		// same `ok` in go test's summary.
		contains(t, text, "-run 'TestLive'")
		contains(t, text, "-count=1")
		contains(t, text, "GRAIN_LIVE_AGENT_TEST")
		// And it runs that framework's own package: the two jobs differ
		// only in which one, so a copy-paste that left both pointed at
		// claude would leave codex with a green nightly nothing ran.
		contains(t, text, job.pkg)
	}

	// The codex job installs the release the Dockerfile pins, read out of
	// the Dockerfile at run time. A second pin written here would drift
	// from that one silently, and a nightly measuring a codex no image
	// carries answers a question nobody asked.
	codex := namedJob(t, live, "live-codex")
	contains(t, codex, "CODEX_CLI_VERSION")
	if strings.Contains(codex, "rust-v") {
		t.Error("live-frameworks.yml pins a codex version of its own: it should read ARG CODEX_CLI_VERSION out of the Dockerfile, so bumping the image's codex is also what points this job at it")
	}

	// And both of codex's credential-free probes run *before* the
	// credential gate. Neither spends a token, and between them they
	// measure the two facts about a live codex that this repository
	// cannot check any other way: that grain's config is one codex loads
	// (TestLiveCodexLoads...), and that `codex exec resume` still does
	// what pkg/agent/codex's retry resumes on
	// (TestLiveCodexResumes...). Behind the gate they would measure
	// nothing at all on a night when no secret has been created -- which,
	// as this file's own OPENAI_API_KEY assertion above is a reminder,
	// has been every night so far.
	free := between(t, codex, "Live codex probes (no credential)", "Require a credential")
	for _, probe := range []string{"TestLiveCodexLoads", "TestLiveCodexResumes"} {
		contains(t, free, probe)
	}

	// Every CLI download in live-frameworks.yml forces IPv4 and retries
	// transient network errors.
	for _, line := range strings.Split(live, "\n") {
		if !strings.Contains(line, "curl ") {
			continue
		}
		if !strings.Contains(line, "curl -4 ") || !strings.Contains(line, "--retry-all-errors") {
			t.Errorf("live-frameworks.yml runs a curl that does not force IPv4 and retry: %s", strings.TrimSpace(line))
		}
	}
}

// Every workflow that installs an agent CLI at runtime must force IPv4 and
// retry on transient network errors, matching the Dockerfile builds.
func TestWorkflowCLIDownloadsForceIPv4AndRetry(t *testing.T) {
	for _, name := range []string{
		"live-frameworks.yml",
		"live-agent.yml",
		"codex-surface.yml",
		"claude-surface.yml",
		"agy-surface.yml",
	} {
		content := stripComments(read(t, ".grain", "workflows", name))
		for _, line := range strings.Split(content, "\n") {
			if !strings.Contains(line, "curl ") {
				continue
			}
			if !strings.Contains(line, "curl -4 ") || !strings.Contains(line, "--retry-all-errors") {
				t.Errorf("%s runs a curl that does not force IPv4 and retry: %s", name, strings.TrimSpace(line))
			}
		}
	}
}

// The GCP credential lives in exactly one workflow too, and it is one no
// branch can trigger.
//
// gcp-smoke.yml stands a real VM and a real GKE cluster up nightly
// (scripts/gce-vm-smoke.sh, scripts/gke-cluster-smoke.sh), consuming the
// short-lived key placed by the gcp-key capability. Every part of
// live-agent.yml's argument applies, and the stakes are higher: this
// credential can create machines and bill for them.
//
// Three things are asserted because they are what make the arrangement
// safe rather than merely intended. The trigger: no `push` and no
// `pull_request`. The credential: consuming the placed service account key.
// And the cleanup: a run that leaks is a run that bills, so leftover
// VMs and clusters are deleted.
func TestOnlyTheScheduledSmokeJobHoldsAGCPCredential(t *testing.T) {
	smoke := stripComments(gcpSmokeWorkflow(t))

	contains(t, smoke, "schedule:")
	contains(t, smoke, "workflow_dispatch:")
	for _, forbidden := range []string{"push:", "pull_request:"} {
		if strings.Contains(upTo(t, smoke, "jobs:"), forbidden) {
			t.Errorf("gcp-smoke.yml triggers on %s: the credential it holds would then be readable by any branch", forbidden)
		}
	}

	contains(t, smoke, "kubectl")
	contains(t, smoke, "google-cloud-cli-gke-gcloud-auth-plugin")
	contains(t, smoke, "/home/debian/.gcp-service-account.json")
	contains(t, smoke, "gcloud auth activate-service-account --key-file=/home/debian/.gcp-service-account.json")
	contains(t, smoke, `gcloud config set project "$project"`)
	contains(t, smoke, "sudo apt-get install -y kubectl google-cloud-cli-gke-gcloud-auth-plugin")

	absent(t, smoke, "GCP_KEY_MINTER_KEY")
	absent(t, smoke, "GCP_AGENT_SERVICE_ACCOUNT")
	absent(t, smoke, "service-accounts keys create")
	absent(t, smoke, "service-accounts keys delete")

	sweep := from(t, smoke, "Delete anything left behind")
	contains(t, sweep, "instances delete")
	contains(t, sweep, "clusters delete")
	contains(t, sweep, "labels.purpose=gce-vm-smoke")
	contains(t, sweep, "resourceLabels.purpose=gke-cluster-smoke")

	for _, obsolete := range []string{
		"MINTER_KEY",
		"AGENT_SA",
		"service-accounts keys create",
		"service-accounts keys delete",
		"Revoke the minted key",
		"Mint a key",
	} {
		absent(t, smoke, obsolete)
	}
}

// The credential that can *write* to a GitHub repository lives in
// exactly one workflow, and it is one no branch can trigger.
//
// live-secrets-repo.yml runs pkg/daemon's TestLiveSecretsRepo* against a
// real private repository nightly, which means CI holds a token with
// Contents: write on it. Every part of live-agent.yml's argument applies
// unchanged -- most branches here are pushed by grain's own agents from
// runs no human has read -- and the thing at stake is different in kind:
// this credential does not spend quota, it writes.
func TestOnlyTheScheduledSecretsRepoJobHoldsAGitHubCredential(t *testing.T) {
	live := stripComments(liveSecretsRepoWorkflow(t))

	contains(t, live, "schedule:")
	contains(t, live, "workflow_dispatch:")
	contains(t, live, "capabilities: [github-sandbox]")
	// The gate's own failure mode: without this the tests skip, and a
	// skip and a pass are the same `ok` in go test's summary.
	contains(t, live, "GRAIN_LIVE_SECRETS_REPO_TEST")
	// The whole live set by prefix rather than by name, for
	// live-agent.yml's reason: a job naming one test is a job that
	// silently stops running the next one somebody adds.
	contains(t, live, "-run 'TestLiveSecretsRepo'")
	// -count=1: a cached "ok" from a run where the tests skipped must
	// never stand in for a live one.
	contains(t, live, "-count=1")
	for _, forbidden := range []string{"push:", "pull_request:"} {
		if strings.Contains(upTo(t, live, "jobs:"), forbidden) {
			t.Errorf("live-secrets-repo.yml triggers on %s: the token it holds would then be readable by any branch", forbidden)
		}
	}

	// And nowhere else. This is the assertion that has to hold as
	// workflows are added: a token that can push to a repository must
	// not appear in a file a branch can trigger, nor be quietly borrowed
	// by a job that has some other reason to run at night.
	for name, text := range map[string]string{
		"tests.yml":       testsWorkflow(t),
		"release.yml":     workflow(t),
		"nightly.yml":     nightlyWorkflow(t),
		"agy-surface.yml": agySurfaceWorkflow(t),
		"live-agent.yml":  liveAgentWorkflow(t),
		"gcp-smoke.yml":   gcpSmokeWorkflow(t),
	} {
		if strings.Contains(stripComments(text), "GRAIN_LIVE_SECRETS_REPO_TOKEN") {
			t.Errorf("%s reads the secrets repository's write token, which belongs to the one scheduled job that holds it", name)
		}
		if strings.Contains(stripComments(text), "github-sandbox") {
			t.Errorf("%s requests the github-sandbox capability, which belongs to the one scheduled job that holds it", name)
		}
	}
}

// The workflow and the test agree on which environment variables and
// capability files the gate reads.
//
// Nothing else would catch a rename: the tests skip when a variable or
// capability file is missing, and the workflow's own
// GRAIN_LIVE_SECRETS_REPO_TEST turns that skip into a failure -- but
// only if it reaches the test under the name the test reads. Renaming
// one of these on either side alone would otherwise show up as a red
// nightly whose message is about a missing credential that is in fact
// right there.
func TestTheSecretsRepoLiveJobPassesTheVariablesTheTestReads(t *testing.T) {
	live := stripComments(liveSecretsRepoWorkflow(t))
	test := read(t, "pkg", "daemon", "live_secrets_repo_test.go")

	for _, name := range []string{
		"GRAIN_LIVE_SECRETS_REPO",
		"GRAIN_LIVE_SECRETS_REPO_TOKEN",
		"GRAIN_LIVE_SECRETS_REPO_TEST",
		"/home/debian/.github-sandbox-repo",
		"/home/debian/.github-sandbox-token",
	} {
		contains(t, live, name)
		if !strings.Contains(test, `"`+name+`"`) {
			t.Errorf("the workflow sets %s and pkg/daemon/live_secrets_repo_test.go reads no such variable", name)
		}
	}
	for _, path := range []string{
		"/home/debian/.github-sandbox-repo",
		"/home/debian/.github-sandbox-token",
	} {
		contains(t, live, path)
		if !strings.Contains(test, `"`+path+`"`) {
			t.Errorf("the workflow checks %s and pkg/daemon/live_secrets_repo_test.go reads no such file", path)
		}
	}
}

// The nightly job runs the scripts that exist, with flags they take.
//
// Nothing else would catch a rename or a dropped flag: this workflow
// fires from a schedule on the default branch, so the first report of a
// broken command line is a red run at night, days after the commit that
// caused it. Same reasoning as the `--- PASS` assertions the real-VM job
// makes about its own suite.
func TestTheSmokeWorkflowRunsTheScriptsAsTheyAre(t *testing.T) {
	smoke := stripComments(gcpSmokeWorkflow(t))
	gce := read(t, "scripts", "gce-vm-smoke.sh")
	gke := read(t, "scripts", "gke-cluster-smoke.sh")

	for _, script := range []string{"gce-vm-smoke.sh", "gke-cluster-smoke.sh"} {
		contains(t, smoke, "./scripts/"+script)
		executable(t, "scripts", script)
		out, err := exec.Command("bash", "-n", filepath.Join(repoRoot(t), "scripts", script)).CombinedOutput()
		if err != nil {
			t.Errorf("bash -n scripts/%s: %v\n%s", script, err, out)
		}
	}

	// The one shape decision the workflow exposes: `default` asks for the
	// production-shaped identity -- the project's own default compute
	// account, which needs roles/iam.serviceAccountUser on it -- rather
	// than the self-acting default both scripts fall back to. Each script
	// spells it differently because each attaches it to a different thing.
	contains(t, smoke, "--service-account=default")
	contains(t, gce, "--service-account)")
	contains(t, smoke, "--node-service-account=default")
	contains(t, gke, "--node-service-account)")
	contains(t, smoke, "--iap")

	// The zone reaches both scripts through the environment rather than
	// through a flag, which is the only reason one `env:` at the top of
	// the workflow governs both legs.
	contains(t, smoke, "ZONE: us-central1-a")
	for _, script := range []string{gce, gke} {
		contains(t, script, `ZONE="${ZONE:-`)
	}
}

// The GCE smoke script can tag the VM it creates, and names the tag this
// module's own networks ask for.
//
// --iap is the leg worth checking, since terraform/gcp gives the host no
// external IP at all -- and network.tf admits IAP's 35.235.240.0/20 to
// port 22 for a *target tag* only: agent_iap_ssh covers
// `<name_prefix>-agent-vm`, the ssh rule covers `<name_prefix>-host`, and
// an instance carrying neither matches no rule and is unreachable however
// good the credential. IAP reports that drop as
// [4003: 'failed to connect to backend'], which is also what it says
// about a VM that has not finished booting, so the error itself sends
// people to look at the key. Two things keep the script out of that hole,
// and neither is visible from the script alone: --tags has to reach the
// create, and the tag its own guidance names has to still be the one
// network.tf creates. A rename there would otherwise leave the script
// advising a tag no rule mentions -- the same dead end, reached by
// following the instructions.
func TestTheGCESmokeScriptCanTagTheVMItCreates(t *testing.T) {
	gce := read(t, "scripts", "gce-vm-smoke.sh")
	code := stripComments(gce)

	contains(t, code, "--tags)")
	contains(t, code, `CREATE_ARGS+=(--tags="$TAGS")`)
	contains(t, code, "[--tags T]")
	contains(t, code, "--no-address)")
	contains(t, code, `CREATE_ARGS+=(--no-address)`)
	contains(t, code, "[--no-address]")

	contains(t, read(t, "deploy", "terraform", "gcp", "network.tf"), `agent_vm_tag = "${var.name_prefix}-agent-vm"`)
	if !strings.Contains(gce, "-agent-vm") {
		t.Error("gce-vm-smoke.sh does not name the `<name_prefix>-agent-vm` tag network.tf's agent_iap_ssh rule is scoped to; without it, --iap tells the operator to pass a tag but not which one")
	}

	// The failure path says which tags the network actually requires,
	// rather than leaving the operator with IAP's own opaque error.
	hint := body(t, gce, "iap_firewall_hint() {")
	contains(t, hint, "firewall-rules list")
	contains(t, hint, "targetTags")
	contains(t, hint, "35.235.240.0/20")
}

// The GKE smoke script creates clusters with private nodes by default to satisfy
// organization policies enforcing private clusters (such as constraints/container.managed.enablePrivateNodes).
func TestTheGKESmokeScriptEnablesPrivateNodes(t *testing.T) {
	gke := read(t, "scripts", "gke-cluster-smoke.sh")
	code := stripComments(gke)

	contains(t, code, "--enable-private-nodes)")
	contains(t, code, "--no-enable-private-nodes)")
	contains(t, code, "--master-ipv4-cidr)")
	contains(t, code, `ENABLE_PRIVATE_NODES=1`)
	contains(t, code, `MASTER_IPV4_CIDR="${MASTER_IPV4_CIDR:-}"`)
	contains(t, code, `ATTEMPT_ARGS+=(--enable-private-nodes --enable-ip-alias --master-ipv4-cidr="$MASTER_IPV4_CIDR"`)
	// gcloud turns authorized networks on, with nobody in them, for a
	// private cluster -- so without this the create succeeds and the first
	// kubectl times out against the public endpoint.
	contains(t, code, "--no-enable-master-authorized-networks)")
}

// The control-plane range is not a fixed default. 172.16.0.0/28 was, and
// the shared project's long-lived bouncer cluster already owns it, so every
// nightly create failed with "Conflicting IP cidr range". The script picks a
// free /28 from what the project lists, and retries a conflict with a new one.
func TestTheGKESmokeScriptChoosesAFreeControlPlaneRange(t *testing.T) {
	code := stripComments(read(t, "scripts", "gke-cluster-smoke.sh"))

	if strings.Contains(code, "172.16.0.0/28") {
		t.Error("gke-cluster-smoke.sh hardcodes 172.16.0.0/28, which other private clusters in the shared project already use")
	}
	pick := body(t, code, "pick_master_cidr() {")
	contains(t, pick, "172.31.")
	used := body(t, code, "used_cidrs() {")
	contains(t, used, "privateClusterConfig.masterIpv4CidrBlock")
	contains(t, used, "subnets list")
	contains(t, used, "secondaryIpRanges")
	contains(t, code, `MASTER_IPV4_CIDR="$(pick_master_cidr)"`)
	contains(t, code, "Conflicting IP cidr range")
}

// The introspection job is dispatch-only, holds nothing, and keeps the
// token that can write a branch out of the job that runs agy.
//
// agy-surface.yml exists because a grain sandbox cannot answer questions
// about agy -- no network beyond the git proxy, no agy in the image -- and
// a runner can (docs/agy-surface.md, scripts/agy-surface.sh). Three things
// about its shape are load-bearing rather than incidental.
//
// The trigger. A schedule would open a pull request a day whether or not
// agy moved, and a `push` would re-answer a question about the *installed*
// agy on every commit; dispatching, meanwhile, takes the same write access
// that pushing this job's branch does, which is what makes `contents:
// write` here no wider than the person who clicked it already has.
//
// The credential, of which there is none. live-agent.yml's header explains
// at length why a repository secret is readable by any workflow on any
// branch push whichever file declares it; nothing here needs one, and the
// way to keep that true is to assert it.
//
// And the split. The job that runs a 200MB binary nobody in this
// repository built has `contents: read` and hands its output on as an
// artifact; the job that holds the write token never runs agy at all --
// the same "hold the credential for the shortest span that gets the work
// published" build-artifacts.yml applies to its GHCR login.
func TestTheAgySurfaceJobIsDispatchOnlyAndHoldsNoCredential(t *testing.T) {
	surface := stripComments(agySurfaceWorkflow(t))

	triggers := upTo(t, surface, "jobs:")
	contains(t, triggers, "workflow_dispatch:")
	for _, forbidden := range []string{"push:", "pull_request:", "schedule:"} {
		if strings.Contains(triggers, forbidden) {
			t.Errorf("agy-surface.yml triggers on %s: it publishes a branch with the repository's own token, and dispatch is what keeps that behind somebody who already has write access", forbidden)
		}
	}
	if strings.Contains(surface, "secrets.") || strings.Contains(surface, "environment:") {
		t.Error("agy-surface.yml reads a credential: it installs and runs a third-party binary, and holding nothing worth stealing is the whole of its safety argument")
	}

	// By their indented job headers, not by bare names: `publish` is also
	// the dispatch input that decides whether the branch is written at
	// all, and it appears above both jobs.
	capture := between(t, surface, "\n  capture:", "\n  publish:")
	contains(t, capture, "./scripts/agy-surface.sh")

	publish := from(t, surface, "\n  publish:")
	contains(t, publish, "needs: [capture]")
	if strings.Contains(publish, "agy") && strings.Contains(publish, "install.sh") {
		t.Error("the publish job installs agy: the point of the split is that the token is never in the job that runs it")
	}

	// The branch, not the ref it was dispatched from: a capture becomes a
	// pull request somebody merges, never a commit that appears on main
	// because a button was clicked.
	contains(t, publish, "HEAD:refs/heads/agy-surface")
	if strings.Contains(publish, "HEAD:refs/heads/main") || strings.Contains(publish, "push origin main") {
		t.Error("agy-surface.yml pushes to main")
	}
}

// The introspection job installs the agy an image would carry, and runs
// the script that is in the tree.
//
// Unpinned in three places on purpose -- the Dockerfile, the nightly live
// run and this one: what each is asking about is the agy a freshly built
// image would have, not one a workflow file froze months ago. That only
// holds while all three fetch the same installer, and a URL that moves in
// one of them is invisible to everything else here.
//
// The script gets the same treatment gcp-smoke.yml's two get, and for the
// same reason: this workflow fires only when somebody dispatches it, so a
// renamed script or a lost +x bit would first be reported by the person
// who dispatched it, at the moment they wanted an answer.
func TestTheAgySurfaceJobInstallsTheAgyAnImageWouldAndRunsTheScriptAsItIs(t *testing.T) {
	const installer = "https://storage.googleapis.com/antigravity-public/antigravity-cli"
	surface := stripComments(agySurfaceWorkflow(t))

	contains(t, read(t, "Dockerfile"), installer)
	contains(t, stripComments(liveAgentWorkflow(t)), installer)
	contains(t, surface, installer)
	// Found, not assumed: agy's install path is documented only by
	// Google, so a layout change on their side should move the symlink
	// rather than fail this job for a binary that is actually there.
	contains(t, surface, `-name antigravity -perm -u+x`)
	contains(t, surface, "agy --version")

	contains(t, surface, "./scripts/agy-surface.sh")
	executable(t, "scripts", "agy-surface.sh")
	out, err := exec.Command("bash", "-n", filepath.Join(repoRoot(t), "scripts", "agy-surface.sh")).CombinedOutput()
	if err != nil {
		t.Errorf("bash -n scripts/agy-surface.sh: %v\n%s", err, out)
	}

	// One path, agreed on by the script's invocation, the commit the
	// publish job makes, and the file in the tree that a sandboxed agent
	// reads instead of asking. The capture is worth committing only
	// because it lands somewhere findable.
	contains(t, surface, "docs/agy-surface.md")
	if _, err := os.Stat(filepath.Join(repoRoot(t), "docs", "agy-surface.md")); err != nil {
		t.Errorf("docs/agy-surface.md is missing: %v -- the workflow publishes over it, and README points at it", err)
	}
}

// A deployment is told nothing about its sandbox image.
//
// One image, not two: the guest a task runs and the container it runs in
// are the same artifact since the guest stopped being built per host, so
// there is a single reference to stamp and a single one to pull.
//
// The grain image carries the reference of the sandbox built from its own
// commit, so `grain sandbox-image` answers it -- which is what
// scripts/setup.sh pulls and what an upgrade pulls alongside the new
// grain. It has to be the immutable sha- tag, not the branch tag: a
// rollback to an older grain must ask for its *own* older sandbox rather
// than whatever that branch points at now.
func TestTheSandboxReferenceIsStampedIntoTheGrainImage(t *testing.T) {
	script := stripComments(read(t, "scripts", "release-images.sh"))
	contains(t, script, `SANDBOX_IMAGE="$guest_pinned"`)
	contains(t, script, `K8S_SANDBOX_IMAGE="$k8s_sandbox_pinned"`)

	// The Makefile turns it into a linker stamp, and the Dockerfile
	// forwards the build arg into that.
	contains(t, read(t, "Makefile"), "-X github.com/ai-on-gke/grain/pkg/daemon.DefaultSandboxImage=$(SANDBOX_IMAGE)")
	contains(t, read(t, "Dockerfile"), "SANDBOX_IMAGE=${SANDBOX_IMAGE}")
}

// The Grain CI release is the credentialed replacement for
// build-artifacts.yml when GitHub Actions is unavailable. It must publish
// both sandbox implementations before publishing a controller that names
// their immutable, same-commit tags -- by digest, which is why they are
// pushed during the build, before the controller is built at all
// (TestTheReleaseStampsSandboxImagesByDigest). Keeping all three references in one
// test makes adding an image to one release path but not the other fail
// locally.
func TestTheGrainReleasePublishesDockerAndKubernetesSandboxImages(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")

	contains(t, script, `guest="$root/guest:sha-$short"`)
	contains(t, script, `k8s_sandbox="$root/k8s-sandbox:sha-$short"`)
	contains(t, script, `docker build -f Dockerfile.k8s-sandbox -t "$k8s_sandbox" .`)
	contains(t, script, `push_pinned "$guest"`)
	contains(t, script, `push_pinned "$k8s_sandbox"`)
	contains(t, script, `SANDBOX_IMAGE="$guest_pinned"`)
	contains(t, script, `K8S_SANDBOX_IMAGE="$k8s_sandbox_pinned"`)
	contains(t, script, `"$k8s_sandbox $root/k8s-sandbox:$branch"`)

	executable(t, "scripts", "release-images.sh")
	out, err := exec.Command("bash", "-n", filepath.Join(repoRoot(t), "scripts", "release-images.sh")).CombinedOutput()
	if err != nil {
		t.Errorf("bash -n scripts/release-images.sh: %v\n%s", err, out)
	}
}

// The other half of the same trick: the grain image is told what it is
// called, not only what sandbox goes with it.
//
// A deployment writes that reference into the CI step it installs in its
// own state repository, whose `grain state check` refuses a dump stamped
// with a schema it does not know -- so the check has to run the build the
// deployment runs, and nothing but the deployment knows which that is.
// The sha- tag again, never the branch tag: a deployment held at an older
// one has to name itself rather than main, which is the whole point.
func TestTheGrainImageIsStampedWithItsOwnReference(t *testing.T) {
	script := stripComments(read(t, "scripts", "release-images.sh"))
	contains(t, script, `GRAIN_IMAGE_REF="$image"`)
	contains(t, script, `GIT_REVISION="$commit"`)

	contains(t, read(t, "Makefile"), "-X github.com/ai-on-gke/grain/pkg/daemon.DefaultGrainImage=$(GRAIN_IMAGE_REF)")
	contains(t, read(t, "Makefile"), "--build-arg GIT_REVISION=$(GIT_REVISION)")
	contains(t, read(t, "Dockerfile"), "GRAIN_IMAGE_REF=${GRAIN_IMAGE_REF}")
	contains(t, read(t, "Dockerfile"), `LABEL org.opencontainers.image.revision="${GIT_REVISION}"`)
}

// bwsalmon/agents#645: a deployment stopped building its sandbox.
//
// It used to run scripts/granule/build-oci-image.sh on every host, which is
// how a deployment could end up running grain from one commit and a
// sandbox from another. What is left building locally is the guest *disk*,
// which bakes in this deployment's own SSH key and so cannot be published
// generically -- see ensure_granule_images' own comment.
func TestTheSandboxContainerIsPulledAndNeverBuilt(t *testing.T) {
	code := setupCode(t)
	contains(t, code, "ensure_granule_oci_image")
	if strings.Contains(code, "build-oci-image.sh") {
		t.Error("the sandbox container is still built here")
	}
	if !strings.Contains(code, "grain sandbox-image") {
		t.Error("nothing resolves the stamped-in default")
	}
	// Named explicitly to the daemon rather than relying on a local retag
	// of granule's default image, which is what the local build used to
	// do -- but only by a deployment that pinned one, and as
	// -sandbox-image rather than as a create arg. Left unpinned, nothing
	// is written at all and the daemon resolves its own stamp on every
	// start (pkg/cli/daemon.go's chooseSandboxImage), so that an upgrade
	// -- which pulls the new grain image and the sandbox image that grain
	// names, then restarts the unit -- actually moves both halves,
	// instead of restarting a unit still naming the guest of the commit
	// this host was installed at.
	//
	// The flag matters as much as the condition around it: the daemon
	// tells a pin somebody chose from the -granule-image every unit
	// written before this carries by *which flag it arrives in*, and a
	// script that went back to writing the create arg would put every
	// pinned deployment back among the fossils (grain/task-373).
	contains(t, code, `-sandbox-image "$GRAIN_GRANULE_OCI_IMAGE"`)
	absent(t, code, "-granule-create-arg -granule-image")
	contains(t, code, `GRAIN_GRANULE_OCI_IMAGE_PINNED="${GRAIN_GRANULE_OCI_IMAGE:+1}"`)
	contains(t, code, `if [ -n "$GRAIN_GRANULE_OCI_IMAGE_PINNED" ]; then`)
	absent(t, code, "localhost:5000/granule:latest")

	// Nothing is built here any more, guest included. The guest disk
	// used to be, because the guest build baked this deployment's own SSH
	// key into it; granule generates that keypair per VM boot now, so the
	// guest is built once from the Granulefile in CI and pulled here like
	// anything else. A host that starts building one again is
	// a host paying a debootstrap on every deploy generation.
	absent(t, code, "build-guest.sh")
	absent(t, code, "ensure_granule_guest_build")
	absent(t, code, "ensure_granule_guest_fetch")

	// The guest travels inside the sandbox image, so granule is given
	// no disk, kernel or initramfs, and no host directories to resolve
	// them against. It does need a writable root: granule's own
	// default is read-only, which a dispatched task cannot use.
	absent(t, code, "-images-hostpath")
	absent(t, code, "-disk-hostpath")
	contains(t, code, "-disk-mode=overlay")
}

// Every sandbox guest gets a nameserver it can actually reach, and a
// deployment can name a different one.
//
// The guest used to resolve through whatever /etc/resolv.conf the machine
// that built the guest image happened to have, which the granule base
// inherited from debootstrap -- routinely an address that exists only in
// that host's own network namespace and is unroutable from a VM on a tap.
// Sandboxes came up with completely open IP egress and no working DNS at
// all, and nothing noticed for a long time because the addresses a run
// depends on (the git proxy, the UI) are literal IPs: the failure showed
// up as `apt`/`npm`/`gcloud` timing out, which reads as a blocked
// network. The resolver is granule's own setting now, defaulting to a
// public one; this asserts grain leaves that default alone unless the
// deployment named something, rather than hard-coding a resolver of its
// own.
func TestASandboxGuestGetsAResolverAndADeploymentCanChooseIt(t *testing.T) {
	code := setupCode(t)
	contains(t, code, "GRAIN_GRANULE_DNS=\"${GRAIN_GRANULE_DNS:-}\"")
	contains(t, code, "-granule-create-arg -dns -granule-create-arg \"$GRAIN_GRANULE_DNS\"")

	// Passed only when set: an empty -dns means "leave the guest's own
	// /etc/resolv.conf alone" to granule, which is the opposite of
	// what an unset deployment setting is asking for.
	if !strings.Contains(code, "if [ -n \"$GRAIN_GRANULE_DNS\" ]; then") {
		t.Error("setup.sh passes -dns unconditionally: an unset GRAIN_GRANULE_DNS would reach granule as the empty string, which means \"no nameserver at all\" rather than \"your default\"")
	}

	// The setting is only usable if it is documented where an operator
	// reads the list of them.
	contains(t, setupText(t), "GRAIN_GRANULE_DNS           nameserver")
}

// The other half of the same setting: the guest *build* can take a
// resolver too -- but this repository's guest build is not one of the
// builds that can take it, and this test is the corrected form of one
// that said it was.
//
// `granule guest build -setup` boots a VM and runs a setup script in it,
// and that script is the one part of such a build that fetches anything
// by name. It resolves through vmspec.Defaults' DNS, netshim's public
// default, reached across two NATs on a CI sandbox -- the flakiest part
// of the build (grain task 130: 268MB of apt archives at 28MB/s, then
// "Could not resolve host: nodejs.org" through curl's own
// --retry-all-errors). `-dns` is what tells it otherwise. It began as a
// local patch on third_party/granule and is on granule's own main now
// (docs/granule-local-patches.md).
//
// grain stopped building its guest that way at the Granulefile
// conversion. Both callers below run `granule granulefile build`, and a
// Granulefile is a Dockerfile with VM PACK in it: every RUN is an
// ordinary docker build step on the build host and the packing is one
// more RUN in a container, so nothing boots, there is no netshim, and
// there is no resolver to point anywhere -- the build resolves as every
// other fetch on that host does. `granulefile build` has no -dns for the
// same reason `guest build` confines its own to the -setup route, which
// is the last assertion here.
//
// So the flag is unreachable from this repository's pipeline. The
// version of this test that landed with the patch (grain task 401)
// asserted the opposite -- that both callers forward a -dns and that a
// granule without the flag "turns every inline guest build into a
// failure on its first line". Neither held: release-images.sh has never
// mentioned -dns, which is why that half of the assertion was red on
// main from the moment it landed, and granule-guest-resolve.sh computed
// a value and passed it to a shell function that dropped it on the
// floor, which is why nobody saw a flag-parsing failure. Both are gone
// now (grain task 517), along with scripts/granule-guest-build-dns.sh,
// which had no other caller.
//
// What this pins is therefore the reason the flag is not needed rather
// than a claim that it is: if a -setup build ever comes back here, this
// test fails and the resolver question is live again.
func TestTheGuestBuildNeedsNoResolverBecauseItBootsNothing(t *testing.T) {
	for _, script := range []string{"granule-guest-resolve.sh", "release-images.sh"} {
		code := read(t, "scripts", script)
		contains(t, code, "granulefile build")
		// Comments stripped first: granule-guest-resolve.sh explains at
		// some length why the -dns block that used to stand here is
		// gone, and that explanation is the point rather than a
		// leftover. What must not come back is an invocation.
		absent(t, stripComments(code), "-dns")
	}

	// The flag still exists on the route that does boot a VM, and still
	// only on that route -- a Dockerfile build has no VM to give a
	// resolver to. If a resync drops it, that is granule's business and
	// not a broken build here; what would make it grain's business again
	// is the loop above changing.
	guest := read(t, "third_party", "granule", "pkg", "cli", "guest.go")
	contains(t, guest, `fs.String("dns"`)
	contains(t, guest, `"dns":              "setup",`)
}

// setup.sh runs on a host with docker and systemd, and nothing else.
//
// What this is about is the tools *this script* reaches for, which is a
// different list from what the deployment it installs needs: a host-mode
// daemon needs a git on the host (ensure_git, and the test above), and
// no step here runs one either way. The one exception is
// convert_reftable_repos, which converts a repository a host git made
// with reftable refs using that same git, if this host still has it, and
// otherwise stops saying what to run: a host with no git has nothing
// for it to do.
//
// It used to want git and jq as well, and installed both itself on any
// host that lacked them -- which made "what a host has to have before it
// can deploy" a list that grew quietly, one apt package at a time, every
// time a step here wanted a tool the shell does not have. The image this
// deployment runs already carries git, curl and grain's own source, so
// every such step borrows one out of it (setup.sh's own image_run)
// instead.
//
// Asserted against the code, per tool and per command position: a `git`
// inside a container command line is the whole point, and prose about
// the git proxy or a "-granule-git-proxy-host" flag is neither. The
// container context is opened by an image_run and runs to the end of
// that call -- the first line that neither continues onto the next nor
// leaves a quote open.
func TestTheInstallerNeedsNothingOnTheHostButDockerAndSystemd(t *testing.T) {
	// Not an exhaustive list of things a base system lacks: these are
	// the ones this script has actually reached for. `install`,
	// `useradd` and `getent` are deliberately absent from it -- those
	// come with any distribution's base install, which docker and
	// systemd do not.
	hostTool := regexp.MustCompile(`(^|[|;&(]|\$\()\s*(git|jq|curl|wget|python3|make|gsutil|gcloud|ip)\b`)

	// The one exception: converting reftable repositories (task 5195)
	// has to use the host's git, because those repositories are what the
	// host's git made and the image's git cannot read them. It is
	// optional rather than required -- host_git_version answers nothing
	// on a host with no git, and the conversion then says what to run
	// instead -- so it adds nothing to what a host must have.
	hostGitFuncs := map[string]bool{"host_git_version": true, "rewrite_refs_as_files": true, "convert_reftable_repo": true}
	inHostGit := false

	inImage := false
	openQuote := false
	for n, line := range strings.Split(setupCode(t), "\n") {
		if name, ok := strings.CutSuffix(line, "() {"); ok && hostGitFuncs[name] {
			inHostGit = true
		}
		if inHostGit {
			if line == "}" {
				inHostGit = false
			}
			continue
		}
		if !inImage && strings.Contains(line, "image_run") {
			inImage, openQuote = true, false
		}
		if inImage {
			if strings.Count(line, "'")%2 == 1 {
				openQuote = !openQuote
			}
			if !openQuote && !strings.HasSuffix(line, `\`) {
				inImage = false
			}
			continue
		}
		if m := hostTool.FindStringSubmatch(line); m != nil {
			t.Errorf("setup.sh:%d runs %s on the host, outside any image_run: %s",
				n+1, m[2], strings.TrimSpace(line))
		}
	}
}

// The tailnet option publishes the UI without moving it.
//
// "Put it behind Tailscale" has been this deployment's own suggested
// answer to reachability since the UI was first bound to loopback
// (setup.sh's header, README.md's "The UI"), and GRAIN_TAILSCALE_ENABLE
// is the script finally doing it. The whole of what it may change is the
// host: tailscaled proxies the tailnet to the port the daemon already
// listens on, so the daemon's own configuration -- what it binds, what
// its container is given -- has to come out of this identical to a
// deployment that never asked for it. A version that widened -ui-addr
// instead would reach the same browser and open the port to everything
// else on the network too.
func TestTheTailnetOptionServesTheUIWithoutMovingIt(t *testing.T) {
	code := setupCode(t)

	// Off unless asked for: turning it on publishes an API with no auth
	// of its own to a whole tailnet, which is an operator's decision.
	contains(t, code, `GRAIN_TAILSCALE_ENABLE="${GRAIN_TAILSCALE_ENABLE:-0}"`)
	// And what the daemon binds is settled without it: resolve_ui_addr
	// knows nothing of the tailnet.
	contains(t, code, `GRAIN_UI_ADDR="${GRAIN_UI_ADDR:-}"`)
	absent(t, body(t, code, "resolve_ui_addr() {"), "TAILSCALE")

	// The served target is whatever -ui-addr's port is, rather than a
	// literal 80: a deployment that moved the UI must still be the thing
	// published, not a port nothing is listening on.
	serve := body(t, code, "tailscale_serve_ui() {")
	contains(t, serve, `http://127.0.0.1:${GRAIN_UI_ADDR##*:}`)
	contains(t, serve, `args+=("--http=$GRAIN_TAILSCALE_SERVE_PORT")`)

	// tailscaled is a host service, not something the daemon's container
	// gets: the tailnet address belongs to the machine and has to
	// outlive any one `docker run`.
	absent(t, dockerRunArgs(t, code), "tailscale")
	contains(t, body(t, code, "setup_tailscale() {"), "systemctl enable --now tailscaled")

	// After the daemon is up, so that a `tailscale serve` failure is
	// about tailscale rather than about a UI that had not started yet.
	before(t, body(t, code, "main() {"), "enable_services", "setup_tailscale",
		"the tailnet is served before the daemon it points at is running")

	// The auth key authenticates this *host* to a tailnet; it is
	// tailscaled's to keep, and nothing here may copy it in among the
	// daemon's own credentials.
	for n, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "GRAIN_TAILSCALE_AUTH_KEY") && strings.Contains(line, "GRAIN_DATA_DIR") {
			t.Errorf("setup.sh:%d writes the tailnet auth key into the deployment's own data directory: %s",
				n+1, strings.TrimSpace(line))
		}
	}
}

// Asking for a tailnet cannot cost you the deployment.
//
// Reachability is not what makes a deployment work, so it must not be
// what makes one fail: an unsupported distribution, a missing auth key,
// a tailnet without HTTPS certificates enabled. Every one of those ends
// the same way the granule prerequisites above do -- the feature off, the
// reason logged, the rest of the run finished -- and report_readiness is
// what keeps that from passing for success.
func TestTheTailnetOptionConvergesRatherThanFailingTheDeploy(t *testing.T) {
	code := setupCode(t)

	setup := body(t, code, "setup_tailscale() {")
	for _, step := range []string{"install_tailscale", "tailscale_login", "tailscale_serve_ui"} {
		if !strings.Contains(setup, "if ! "+step+"; then") {
			t.Errorf("setup_tailscale does not let %s give up: a step that cannot converge fails the whole deploy", step)
		}
	}
	if strings.Contains(setup, "exit ") {
		t.Error("setup_tailscale exits: a host that cannot reach its tailnet still has a working loopback deployment")
	}
	contains(t, setup, "GRAIN_TAILSCALE_ENABLE=0")

	// Which is only honest if the closing report says so -- the same
	// requested-but-not-achieved distinction GRAIN_GRANULE_REQUESTED
	// exists to draw.
	contains(t, code, `GRAIN_TAILSCALE_REQUESTED="$GRAIN_TAILSCALE_ENABLE"`)
	contains(t, body(t, code, "report_readiness() {"),
		`[ "$GRAIN_TAILSCALE_REQUESTED" = "1" ] && [ "$GRAIN_TAILSCALE_ENABLE" != "1" ]`)
}

// Nothing rewrites setup.sh underneath the process running it.
//
// sync_repo pulled a new copy of the checkout over the file this process
// was reading, which is why reexec_if_updated had to exist: every step
// after the pull was otherwise the *old* script's version of that step.
// Neither is needed once the script keeps no checkout -- and neither may
// come back without the other, so both spellings are pinned.
func TestTheInstallerNeitherClonesNorReplacesItself(t *testing.T) {
	code := setupCode(t)
	for _, gone := range []string{"sync_repo", "reexec_if_updated", "GRAIN_SRC_DIR", "GRAIN_REPO_URL"} {
		absent(t, code, gone)
	}
	// Nothing here reads the source at all any more. The one thing that
	// did -- the granule guest build -- unpacked it out of the image so
	// that it matched the binary; there is no guest build here now, so
	// there is nothing to keep in step and no copy to unpack.
	absent(t, code, "unpack_image_source")

	// Which makes keeping the copy of setup.sh on a deployed host
	// current the job of whatever put it there: on the GCP path, the
	// deploy script that runs it.
	deploy := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	contains(t, deploy, `git -C "$SRC_DIR" reset --quiet --hard "origin/$GRAIN_REF"`)
}

func TestTerraformDeployNoLongerInstallsAToolchain(t *testing.T) {
	text := read(t, "deploy", "terraform", "gcp", "files", "deploy.sh")
	prerequisites := body(t, text, "install_prerequisites() {")

	contains(t, prerequisites, "for cmd in git docker jq iptables; do")
	if makeWord.MatchString(prerequisites) {
		t.Error("make is still installed on the host")
	}
	// The image config reaches setup.sh through the same grain-config
	// metadata attribute everything else does.
	for _, v := range []string{"GRAIN_IMAGE", "GRAIN_IMAGE_TAG", "GRAIN_IMAGE_PULL_TOKEN"} {
		if !strings.Contains(text, v+"=") {
			t.Errorf("%s is not passed to setup.sh", v)
		}
	}
}

// The metadata server answers anyone who can reach it, and among what it
// hands out is a token for the host's own service account -- the one
// identity allowed to read this deployment's secrets out of Secret
// Manager -- so a sandbox guest, whose traffic leaves through its VM
// container's docker bridge, must not be able to reach it (#2376). The block is in raw PREROUTING, which every packet
// arriving on an interface passes and the host's own reads (OUTPUT) do
// not; persisted as a unit ordered before docker, so a reboot does not
// reopen it; and applied before the secrets are read and setup.sh starts
// a daemon, failing the deploy if it cannot be.
func TestTerraformDeployBlocksContainersFromTheMetadataServer(t *testing.T) {
	text := read(t, "deploy", "terraform", "gcp", "files", "deploy.sh")
	block := body(t, text, "ensure_metadata_block() {")

	contains(t, block, "iptables -w -t raw -I PREROUTING 1 -d 169.254.169.254/32 -j DROP")
	contains(t, block, "iptables -w -t raw -C PREROUTING -d 169.254.169.254/32 -j DROP")
	contains(t, block, "Before=network-pre.target docker.service containerd.service")
	contains(t, block, "WantedBy=multi-user.target docker.service")
	contains(t, block, "systemctl enable grain-block-metadata.service")
	contains(t, block, "exit 1")

	code := stripComments(text)
	call := strings.Index(code, "\nensure_metadata_block\n")
	if call < 0 {
		t.Fatal("deploy.sh never calls ensure_metadata_block")
	}
	for _, later := range []string{"instance/service-accounts/default/token", "$(secret github-app-private-key)", `"$SRC_DIR/scripts/setup.sh"`} {
		if i := strings.Index(code, later); i < 0 || i < call {
			t.Errorf("%s comes before the metadata server is blocked from containers", later)
		}
	}
}

// A GCE instance installed any other way than deploy.sh -- by hand, by
// a wrapper installer, in host mode -- left the metadata server
// reachable from every NetworkOpen guest and every -container-sandboxes
// container (docs/test-runs/2026-10-01-sandbox-isolation-port-block.md,
// finding 5). setup.sh installs the same block. "The same" is asserted
// as byte-identical script and unit text, because both files write the
// same two paths and a drift between them would have each deploy undo
// the other's.
func TestSetupBlocksContainersFromTheMetadataServerLikeDeploy(t *testing.T) {
	setup := read(t, "scripts", "setup.sh")
	deploy := read(t, "deploy", "terraform", "gcp", "files", "deploy.sh")
	setupBlock := body(t, setup, "ensure_metadata_block() {")
	deployBlock := body(t, deploy, "ensure_metadata_block() {")

	for _, heredoc := range []struct{ open, end string }{
		{"<<'SCRIPT'\n", "\nSCRIPT\n"},
		{"<<UNIT\n", "\nUNIT\n"},
	} {
		got := between(t, setupBlock, heredoc.open, heredoc.end)
		want := between(t, deployBlock, heredoc.open, heredoc.end)
		if got != want {
			t.Errorf("setup.sh's %s differs from deploy.sh's:\n--- setup.sh\n%s\n--- deploy.sh\n%s", strings.TrimSpace(heredoc.end), got, want)
		}
	}
	for _, path := range []string{
		`readonly METADATA_BLOCK="/usr/local/sbin/grain-block-metadata"`,
		`readonly METADATA_BLOCK_UNIT="/etc/systemd/system/grain-block-metadata.service"`,
	} {
		contains(t, setup, path)
		contains(t, deploy, path)
	}
	contains(t, setupBlock, "systemctl enable grain-block-metadata.service")
	contains(t, setupBlock, "exit 1")

	// First thing main does: before the daemon, and before any container
	// this script starts.
	mainFn := stripComments(body(t, setup, "\nmain() {"))
	before(t, mainFn, "\n  ensure_metadata_block\n", "\n  ensure_user\n", "main ensures the user before the metadata block")
	before(t, mainFn, "\n  ensure_metadata_block\n", "\n  enable_services\n", "main starts the daemon before the metadata block")

	contains(t, stripComments(setup), `case "$GRAIN_BLOCK_METADATA" in`)
	contains(t, setup, `GRAIN_BLOCK_METADATA="${GRAIN_BLOCK_METADATA:-auto}"`)
}

// Which hosts setup.sh blocks: "auto" means a GCE instance, recognised
// from DMI the way GCE documents, and nothing else; 1 and 0 override it.
func TestSetupDecidesWhereToBlockTheMetadataServer(t *testing.T) {
	fn := body(t, read(t, "scripts", "setup.sh"), "metadata_block_wanted() {")

	write := func(dir, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gce := t.TempDir()
	write(gce, "product_name", "Google Compute Engine")
	write(gce, "sys_vendor", "Google")
	other := t.TempDir()
	write(other, "product_name", "Cloud Hypervisor")
	write(other, "sys_vendor", "Cloud Hypervisor")
	vendorOnly := t.TempDir()
	write(vendorOnly, "sys_vendor", "Google")
	missing := filepath.Join(t.TempDir(), "absent")

	for _, c := range []struct {
		mode, dmi string
		want      bool
	}{
		{"auto", gce, true},
		{"auto", vendorOnly, true},
		{"auto", other, false},
		{"auto", missing, false},
		{"1", other, true},
		{"1", missing, true},
		{"0", gce, false},
	} {
		cmd := exec.Command("bash", "-c", fn+"\n}\nmetadata_block_wanted")
		cmd.Env = append(os.Environ(), "GRAIN_BLOCK_METADATA="+c.mode, "DMI_ID_DIR="+c.dmi)
		err := cmd.Run()
		if _, isExit := err.(*exec.ExitError); err != nil && !isExit {
			t.Fatalf("running metadata_block_wanted: %v", err)
		}
		if got := err == nil; got != c.want {
			t.Errorf("GRAIN_BLOCK_METADATA=%s with DMI %s: blocks=%v, want %v", c.mode, filepath.Base(c.dmi), got, c.want)
		}
	}
}

// A deployed host runs no interpreter but its own shell.
//
// `cfg`, the GCS object encoder and the metadata token reader were three
// python3 one-liners, which is why every VM apt-installed an interpreter
// -- for three lines of JSON handling and nothing else. They are jq now.
// Asserted rather than left to the prerequisites list above, because the
// failure mode is additive: a fourth one-liner would work on any host that
// happens to have python3, and only fail on the minimal image this
// deployment actually gets.
func TestNoDeployScriptShellsOutToAnInterpreter(t *testing.T) {
	for _, script := range [][]string{
		{"scripts", "setup.sh"},
		{"deploy", "terraform", "gcp", "files", "deploy.sh"},
		{"deploy", "terraform", "gcp", "deploy", "read-outputs.sh"},
		{"deploy", "terraform", "gcp", "deploy", "terraform-apply.sh"},
		{"deploy", "terraform", "gcp", "deploy", "wait-for-host.sh"},
		{"deploy", "terraform", "gcp", "deploy", "write-summary.sh"},
		{"deploy", "terraform", "gcp", "deploy", "push-secrets.sh"},
		{"deploy", "terraform", "gcp", "deploy", "migrate-secrets.sh"},
	} {
		for _, line := range strings.Split(stripComments(read(t, script...)), "\n") {
			if strings.Contains(line, "python") {
				t.Errorf("%s still needs an interpreter: %s",
					filepath.Join(script...), strings.TrimSpace(line))
			}
		}
	}
}

// The one place a JSON boolean's spelling is load-bearing.
//
// `cfg` was a python3 one-liner, and `print` spells True with a capital T,
// so this comparison was written against Python's spelling rather than
// JSON's. jq emits "true", so the comparison had to move with it. Getting
// that wrong is silent in a way worth guarding: no error anywhere, just a
// deployment that came up on HostSandboxes with granule configured, because
// the string never matched and GRAIN_GRANULE_ENABLE was always 0.
// grain/task-15 made the installer refuse an unisolated install nobody
// asked for. Host sandboxes were removed and have since come back behind
// pkg/daemon's CheckHostSandboxes; these pin what the installer does with
// them: it installs them only when asked by name, never as a fallback, and
// refuses them wherever the daemon would refuse to start.

func TestTheInstallerOnlyInstallsHostSandboxesWhenAskedAndRefusesNoBackend(t *testing.T) {
	code := setupCode(t)

	// The default is a sandboxed deployment.
	absent(t, code, `GRAIN_GRANULE_ENABLE:-0}`)
	contains(t, code, `GRAIN_HOST_SANDBOXES="${GRAIN_HOST_SANDBOXES:-0}"`)

	// -host-sandboxes is only ever passed behind GRAIN_HOST_SANDBOXES=1,
	// and -allow-unisolated-host-sandboxes only behind its own variable.
	units := body(t, code, "write_systemd_units() {")
	host := from(t, units, `elif [ "$GRAIN_HOST_SANDBOXES" = "1" ]; then`)
	host = upTo(t, host, "\n  else\n")
	contains(t, host, `daemon_args+=(-host-sandboxes)`)
	contains(t, host, `if [ "$GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES" = "1" ]; then`)
	contains(t, host, `daemon_args+=(-allow-unisolated-host-sandboxes)`)
	contains(t, host, `daemon_args+=(-host-sandbox-env "$env_entry")`)
	if n := strings.Count(code, "daemon_args+=(-host-sandboxes"); n != 1 {
		t.Errorf("-host-sandboxes is passed in %d places, want only the GRAIN_HOST_SANDBOXES branch", n)
	}
	contains(t, units, `-sandbox-dir "$GRAIN_SANDBOX_DIR"`)

	// Host sandboxes beside another backend stop the run: the daemon takes one.
	both := from(t, code, `if [ "$GRAIN_HOST_SANDBOXES" = "1" ] && { [ "$GRAIN_GRANULE_ENABLE" = "1" ] || [ "$GRAIN_CONTAINER_SANDBOXES" = "1" ]; }; then`)
	contains(t, upTo(t, both, "\nfi\n"), "exit 2")

	// So does turning off every backend.
	guard := from(t, code, `if [ "$GRAIN_GRANULE_ENABLE" != "1" ] && [ "$GRAIN_CONTAINER_SANDBOXES" != "1" ] && [ "$GRAIN_HOST_SANDBOXES" != "1" ]; then`)
	contains(t, upTo(t, guard, "\nfi\n"), "exit 2")

	// And no step may turn granule off without a backend to replace it:
	// every granule prerequisite that turns out not to be ready goes
	// through granule_unavailable, which falls back to containers -- never
	// to host sandboxes.
	body := upTo(t, from(t, code, "granule_unavailable() {"), "\n}\n")
	contains(t, body, "GRAIN_CONTAINER_SANDBOXES=1")
	absent(t, body, "GRAIN_HOST_SANDBOXES")
	for _, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "GRAIN_GRANULE_ENABLE=0") && !strings.Contains(body, line) {
			t.Errorf("GRAIN_GRANULE_ENABLE=0 outside granule_unavailable: %s", strings.TrimSpace(line))
		}
		if strings.HasPrefix(strings.TrimSpace(line), "GRAIN_HOST_SANDBOXES=1") {
			t.Errorf("setup.sh turns host sandboxes on by itself: %s", strings.TrimSpace(line))
		}
	}
}

// check_host_sandboxes is run for real, against the same addresses
// pkg/daemon's loopbackListenAddr answers for: the installer has to refuse
// exactly what the daemon would refuse to start with, or it installs a unit
// that crash-loops (too lenient) or refuses a workstation the daemon would
// have run (too strict). Its refusal has to come before anything on this
// host is touched, which is before the first package is installed.
func TestTheInstallerRefusesHostSandboxesWhereTheDaemonWould(t *testing.T) {
	code := setupCode(t)
	fns := body(t, code, "ui_addr_is_loopback() {") + "\n}\n" +
		body(t, code, "check_host_sandboxes() {") + "\n}\n"
	before(t, code, "\ncheck_host_sandboxes\n", "\nensure_docker\n",
		"check_host_sandboxes refuses before docker is installed")

	for _, tc := range []struct {
		hostSandboxes, allow, uiAddr, iap, env string
		refused                                bool
	}{
		{"0", "0", "0.0.0.0:4242", "", "", false},
		{"1", "0", "127.0.0.1:4242", "", "", false},
		{"1", "0", "127.0.0.2:4242", "", "", false},
		{"1", "0", "localhost:4242", "", "", false},
		{"1", "0", "[::1]:4242", "", "", false},
		{"1", "0", "0.0.0.0:4242", "", "", true},
		{"1", "0", ":4242", "", "", true},
		{"1", "0", "[::]:4242", "", "", true},
		{"1", "0", "10.0.0.5:4242", "", "", true},
		{"1", "0", "127.0.0.1:4242", "projects/1/iap_web/x", "", true},
		{"1", "1", "0.0.0.0:4242", "projects/1/iap_web/x", "", false},

		// GRAIN_HOST_SANDBOX_ENV: names and values the unit can carry,
		// only beside host sandboxes, and never HOME.
		{"1", "0", "127.0.0.1:4242", "", "SSH_AUTH_SOCK HTTPS_PROXY=http://proxy:3128", false},
		{"0", "0", "0.0.0.0:4242", "", "SSH_AUTH_SOCK", true},
		{"1", "0", "127.0.0.1:4242", "", "HOME=/root", true},
		{"1", "0", "127.0.0.1:4242", "", "1BAD", true},
		{"1", "0", "127.0.0.1:4242", "", "TOKEN=$SECRET", true},
		{"1", "0", "127.0.0.1:4242", "", "*", true},
	} {
		script := fns + "check_host_sandboxes\n"
		cmd := exec.Command("bash", "-euc", script)
		cmd.Env = append(os.Environ(),
			"GRAIN_HOST_SANDBOXES="+tc.hostSandboxes,
			"GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES="+tc.allow,
			"GRAIN_UI_ADDR="+tc.uiAddr,
			"GRAIN_IAP_RESOURCE="+tc.iap,
			"GRAIN_HOST_SANDBOX_ENV="+tc.env)
		out, err := cmd.CombinedOutput()
		if refused := err != nil; refused != tc.refused {
			t.Errorf("GRAIN_HOST_SANDBOXES=%s GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES=%s GRAIN_UI_ADDR=%s GRAIN_IAP_RESOURCE=%q GRAIN_HOST_SANDBOX_ENV=%q: refused=%v, want %v\n%s",
				tc.hostSandboxes, tc.allow, tc.uiAddr, tc.iap, tc.env, refused, tc.refused, out)
		}
		if tc.refused && tc.env == "" && !strings.Contains(string(out), "GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES=1") {
			t.Errorf("refusal for %s does not name the way past it:\n%s", tc.uiAddr, out)
		}
	}
}

// enable_granule_sandboxes = false on terraform/gcp asks for container
// sandboxes now.
func TestTheGCPDeployAsksForContainersWhenItTurnsGranuleOff(t *testing.T) {
	code := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))

	contains(t, code, `GRAIN_GRANULE_ENABLE="$([ "$ENABLE_GRANULE_SANDBOXES" = "true" ] && echo 1 || echo 0)"`)
	contains(t, code, `GRAIN_CONTAINER_SANDBOXES="$([ "$ENABLE_GRANULE_SANDBOXES" = "true" ] && echo 0 || echo 1)"`)
	absent(t, code, `GRAIN_HOST_SANDBOXES`)
}

// The installer suite deliberately takes the non-KVM path even on a CI
// runner that exposes /dev/kvm. It must name the isolated Docker backend
// explicitly.
func TestTheInstallerSuiteAsksForTheContainerModeItDeploys(t *testing.T) {
	code := read(t, "tests", "installer", "installer_test.go")

	contains(t, code, `{"GRAIN_GRANULE_ENABLE", "0"}`)
	contains(t, code, `{"GRAIN_CONTAINER_SANDBOXES", "1"}`)
	absent(t, code, `GRAIN_HOST_SANDBOXES`)
}

func TestTheGranuleToggleComparesAgainstTheSpellingCfgEmits(t *testing.T) {
	code := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	contains(t, code, `[ "$ENABLE_GRANULE_SANDBOXES" = "true" ]`)
	absent(t, code, `= "True"`)
}

// The apply's own output is the last place a secret can escape, and the
// only test here that runs a script rather than reading one.
//
// An apply that *replaces* the host renders the instance's prior state in
// full, push-secrets.sh's metadata included, because
// lifecycle.ignore_changes governs in-place diffs and not the state a
// destroy is printed from (instance.tf says the same thing at the block
// itself). That is not hypothetical: raising boot_disk_gb forces
// replacement, and doing so printed the minter account's private key into
// a deploy log (bwsalmon/agents#653). GitHub Actions masks the values a
// workflow handed it; the minter key is minted mid-run, so nothing
// upstream of this filter can mask it.
//
// So terraform-apply.sh's redact runs here for real, over a diff shaped
// like the one that leaked: content checks cannot tell a filter that works
// from a filter with a typo in its regex, and a filter with a typo in its
// regex is indistinguishable from no filter at all until the day it
// matters.
func TestTheApplyRedactsSecretsOutOfItsOwnOutput(t *testing.T) {
	// body stops at the closing brace rather than including it, so the
	// function has to be closed again before bash will take it.
	fn := body(t, read(t, "deploy", "terraform", "gcp", "deploy", "terraform-apply.sh"), "redact() {") + "\n}"

	// Every value here is fake, and every one of them is shaped like the
	// real thing it stands in for -- a jsonencode'd minter key with a
	// heredoc PEM, a one-line token, and a PEM rendered inline with
	// escaped newlines, which is the other way the provider prints one.
	const diff = `      ~ metadata                   = {
          - "grain-gcp-minter-key"     = jsonencode(
                {
                  - client_email                = "grain-main-gcp-key-minter@example.iam.gserviceaccount.com"
                  - private_key                 = <<-EOT
                        -----BEGIN PRIVATE KEY-----
                        MIIEvQIBADANBgkqhkiG9w0BAQEFAASCsentinelHEREDOC
                        -----END PRIVATE KEY-----
                    EOT
                  - private_key_id              = "cbc69cd7d294521700f8744cc2a835c1"
                }
            ) -> null
          - "grain-claude-oauth-token" = "sk-ant-oat-sentinelTOKEN" -> null
          - "grain-github-token"       = "github_pat_sentinelPAT" -> null
          - "grain-github-app-private-key" = "-----BEGIN RSA PRIVATE KEY-----\nsentinelINLINE\n-----END RSA PRIVATE KEY-----"
        }
      ~ initialize_params {
          ~ size                        = 50 -> 100 # forces replacement
        }
`

	cmd := exec.Command("bash", "-c", fn+"\nredact")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running redact: %v\n%s", err, out)
	}
	got := string(out)

	for _, secret := range []string{
		"sentinelHEREDOC", "sentinelTOKEN", "sentinelPAT", "sentinelINLINE",
	} {
		if strings.Contains(got, secret) {
			t.Errorf("redact let %s through:\n%s", secret, got)
		}
	}

	// Redacted, not silenced. The plan has to stay worth reading: this log
	// is the first authenticated plan a deployment ever gets, since a
	// config repo's plan.yml deliberately runs without credentials. And
	// private_key_id is what names the key to revoke when one did leak.
	for _, kept := range []string{
		`"grain-gcp-minter-key"`,
		`"grain-claude-oauth-token"`,
		`"grain-github-app-private-key"`,
		"cbc69cd7d294521700f8744cc2a835c1",
		"size                        = 50 -> 100 # forces replacement",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("redact dropped %s, which is not a secret:\n%s", kept, got)
		}
	}
}

// The filter above is keyed on metadata names. Nothing writes a secret
// into metadata any more, but a deployment that predates secrets.tf
// still carries them until its first apply after it removes them -- an
// in-place diff that prints each old value. migrate-secrets.sh's
// legacy_keys is the list of those names, kept in one place; one it has
// and the redact does not is printed unredacted on exactly that apply.
func TestEveryLegacySecretMetadataKeyIsRedacted(t *testing.T) {
	legacy := between(t,
		read(t, "deploy", "terraform", "gcp", "deploy", "migrate-secrets.sh"), "legacy_keys=(", "\n)")
	redactor := body(t,
		read(t, "deploy", "terraform", "gcp", "deploy", "terraform-apply.sh"), "redact() {")

	keys := regexp.MustCompile(`"(grain-[a-z-]+)=([a-z-]+)"`).FindAllStringSubmatch(legacy, -1)
	if len(keys) == 0 {
		t.Fatal("migrate-secrets.sh's legacy_keys names no metadata keys")
	}
	for _, k := range keys {
		if !strings.Contains(redactor, k[1]) {
			t.Errorf("%s may still be on an instance, but terraform-apply.sh's redact does not cover it", k[1])
		}
	}
}

// Secrets live in Secret Manager (#2376), and four files have to agree on
// it: secrets.tf creates "<name_prefix>-<key>" for each key and grants
// the host account alone read on it, push-secrets.sh writes versions of
// those names, migrate-secrets.sh moves each old metadata attribute onto
// one of them, and deploy.sh reads every one it hands setup.sh. A key
// missing from any of them is a credential that is pushed and never read,
// or read and never pushable.
func TestSecretsLiveInSecretManagerNotInstanceMetadata(t *testing.T) {
	tf := read(t, "deploy", "terraform", "gcp", "secrets.tf")
	keyList := between(t, tf, "host_secret_keys = toset([", "])")
	keys := regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(keyList, -1)
	if len(keys) == 0 {
		t.Fatal("secrets.tf's host_secret_keys names no secrets")
	}
	contains(t, tf, `secret_id = "${var.name_prefix}-${each.key}"`)
	// Per secret, to the host account -- never a project-wide accessor
	// grant, which would let it read every secret in a shared project.
	contains(t, between(t, tf, `resource "google_secret_manager_secret_iam_member" "host_reads" {`, "\n}"),
		`role      = "roles/secretmanager.secretAccessor"`)
	absent(t, read(t, "deploy", "terraform", "gcp", "iam.tf"), "roles/secretmanager.secretAccessor")
	// Terraform makes the secrets, never their values.
	absent(t, stripComments(tf), "google_secret_manager_secret_version")

	push := stripComments(read(t, "deploy", "terraform", "gcp", "deploy", "push-secrets.sh"))
	absent(t, push, "add-metadata")
	contains(t, push, "gcloud secrets versions add")
	migrate := read(t, "deploy", "terraform", "gcp", "deploy", "migrate-secrets.sh")
	deploy := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	for _, k := range keys {
		contains(t, push, fmt.Sprintf(`push_secret "%s" `, k[1]))
		contains(t, migrate, fmt.Sprintf(`"grain-%s=%s"`, k[1], k[1]))
		contains(t, deploy, fmt.Sprintf(`"$(secret %s)"`, k[1]))
	}

	// The host reads nothing secret off its metadata: only the config and
	// the token Secret Manager is read with.
	for _, line := range strings.Split(deploy, "\n") {
		if strings.Contains(line, "instance/attributes/") && !strings.Contains(line, "instance/attributes/grain-config") {
			t.Errorf("deploy.sh still reads instance metadata: %s", strings.TrimSpace(line))
		}
	}
	contains(t, read(t, "deploy", "terraform", "gcp", "instance.tf"),
		"secret_ids = { for k, _ in google_secret_manager_secret_iam_member.host_reads : k => google_secret_manager_secret.host[k].id }")

	// And nothing tells Terraform to keep a metadata attribute it does
	// not declare: that was only ever there for the secrets, and with it
	// gone the first apply removes whatever an old deployment left there.
	absent(t, stripComments(read(t, "deploy", "terraform", "gcp", "instance.tf")), "ignore_changes")

	// Which is why the apply migrates first, while the attributes are
	// still there to copy.
	apply := stripComments(read(t, "deploy", "terraform", "gcp", "deploy", "terraform-apply.sh"))
	before(t, apply, "\nmigrate_metadata_secrets\n", `echo "--- terraform apply"`,
		"terraform-apply.sh runs the full apply, which removes the old secret attributes, before copying them into Secret Manager")
}

// The formatting gate lives in one place, and CI runs that place rather
// than a second copy of it.
//
// Unformatted Go builds, vets and tests exactly like formatted Go, so
// until the go job ran this nothing in CI could see the drift, and three
// files sat unformatted on main long enough that `gofmt -l` was no use
// as a local signal: it always had something to say about files the
// change in hand had not touched, and an editor that formats on save
// turned that into diff noise. `make fmt` rather than a gofmt line of
// the workflow's own is what keeps the two from drifting apart -- a
// contributor reproducing a red build locally has to be able to run the
// same check.
func TestTheGoJobRunsTheFormattingGate(t *testing.T) {
	goJob := namedJob(t, stripComments(testsWorkflow(t)), "go")
	contains(t, goJob, "run: make fmt")

	// And the target it names has to be a check that fails, not one that
	// lists offenders and exits 0 -- which is how this went unnoticed
	// before there was a job running it at all.
	contains(t, between(t, read(t, "Makefile"), "\nfmt:", "\n\n"), "gofmt -l")
	contains(t, between(t, read(t, "Makefile"), "\nfmt:", "\n\n"), "exit 1")
}

// The frontend's half of it, which has three moving parts in three files
// -- the workflow step, the Makefile target it names and the npm script
// that target names -- and one ordering constraint that is invisible in
// any of them: prettier lives in ui/node_modules, so the step only works
// where `make frontend` has already run `npm ci`. Moved up beside the Go
// `Format` step, where it looks like it belongs, it would fail with
// "prettier: not found" on a tree that is perfectly formatted.
//
// It lives in the `frontend` job rather than in `go-test` since the
// vitest suite and this gate were split out of it: neither needs the Go
// toolchain, and running them after `go test -race` was the difference
// between a nine-minute workflow and a seven-minute one.
func TestTheFrontendJobRunsTheFrontendFormattingGateAfterInstallingIt(t *testing.T) {
	job := namedJob(t, stripComments(testsWorkflow(t)), "frontend")
	contains(t, job, "run: make fmt-frontend")

	before(t, job, "run: make frontend", "run: make fmt-frontend",
		"the frontend job would check frontend formatting before `make frontend` installs prettier, and fail on a formatted tree")

	// The target, and the script it hands off to. `--check` rather than
	// `--write`: a gate that silently rewrote the tree would pass on
	// every commit and gate nothing.
	target := between(t, read(t, "Makefile"), "\nfmt-frontend:", "\n\n")
	contains(t, target, "npm run format:check")
	pkg := read(t, "ui", "package.json")
	contains(t, pkg, `"format:check": "prettier --check .`)

	// And prettier has to be a locked devDependency, so that `npm ci`
	// installs it and a new prettier release cannot redden a commit that
	// touched nothing -- unlike gofmt, prettier's output does change
	// between versions.
	contains(t, between(t, pkg, `"devDependencies"`, "\n  }"), `"prettier"`)
	contains(t, read(t, "ui", "package-lock.json"), `"node_modules/prettier"`)

	// The config file is what pins the gate to prettier's defaults, and
	// what stops prettier walking up out of the repository into whatever
	// ~/.prettierrc the contributor happens to have.
	read(t, "ui", ".prettierrc")
}

// Grain has two Go modules of its own, and every check-in runs both with
// the same commands a developer gets from `make test`.
//
// ./... deliberately stops at a nested module boundary. That made the v3
// suite especially easy to miss: the root command stayed green while no
// check-in compiled or tested anything below v3/. third_party/granule is
// excluded here because it is vendored upstream code with its own module;
// v3 is Grain itself.
//
// Compared as whole commands rather than by asserting -race twice: any
// divergence at all -- a flag, a package pattern, a timeout on one side
// only -- means a red build locally and a green one in CI, or the reverse.
func TestTheGoJobRunsTheSameSuiteCommandsTheMakefileDoes(t *testing.T) {
	recipes := map[string]string{}
	for _, line := range strings.Split(between(t, read(t, "Makefile"), "\ntest: frontend", "\n\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "go test"):
			recipes["."] = trimmed
		}
	}
	for _, module := range []string{"."} {
		if recipes[module] == "" {
			t.Errorf("the Makefile's test target runs no `go test` command for %s", module)
		}
	}
	// Dropping -race would silently lose the repository's race coverage.
	contains(t, recipes["."], "-race")

	// In .grain/workflows/tests.yml, the Go suite is sharded across these
	// five jobs (package coverage is verified by
	// TestTheGoJobsCoverEveryPackageInTheModuleOnce in grain_workflows_test.go).
	wf := stripComments(testsWorkflow(t))
	for _, id := range []string{"go", "go-e2e", "go-orchestrator", "go-model", "go-unit"} {
		goJob := namedJob(t, wf, id)
		contains(t, goJob, "./scripts/run-go-tests.sh go test -race")

		// The detector is a cgo library. Disabling cgo for this job would not
		// quietly drop the coverage -- `go test -race` refuses to build
		// without it -- but it would turn the whole suite red for a reason
		// that reads like a toolchain fault, so keep the two apart: only the
		// binary that ships is built with CGO_ENABLED=0.
		if regexp.MustCompile(`CGO_ENABLED[=:]\s*["']?0`).MatchString(goJob) {
			t.Errorf("the %s job disables cgo somewhere, and `go test -race` needs it", id)
		}
	}
}

// The long suite runs the targets that nothing else runs.
//
// Every one of these existed before nightly.yml did, and none of them
// had ever run in CI: `soak` asked for two thousand rounds and got them
// only when somebody typed it, `loadtest` gated itself on a
// GRAIN_LOAD_TEST that no workflow set, and the Go suite's own
// -shuffle/-count flags were not passed anywhere at all. A target with
// no runner is a target that rots -- the very failure tests.yml's
// real-vm job comment describes for the KVM suite -- so the job that
// runs each one is asserted here by the target it names.
func TestTheNightlySuiteRunsTheLongTargets(t *testing.T) {
	nightly := stripComments(nightlyWorkflow(t))
	makefile := read(t, "Makefile")

	for _, target := range []string{"test-long", "soak", "loadtest", "test-e2e-long", "vuln"} {
		contains(t, nightly, "run: make "+target)
		// And the target has to exist, with a recipe: a workflow naming
		// a target the Makefile does not define fails at 02:11 UTC with
		// "No rule to make target", which is the worst place to find a
		// typo.
		contains(t, makefile, "\n"+target+":")
	}

	// A schedule, because the whole point is that it runs when nobody
	// remembered to ask; and workflow_dispatch, because a change to the
	// reconcile loop or to pkg/staterepo deserves this before it merges
	// rather than the night after.
	contains(t, nightly, "schedule:")
	contains(t, nightly, "workflow_dispatch:")

	// Credential-free, like tests.yml and for a weaker version of the
	// same reason: this one is dispatchable against any branch, and
	// keeping it free of secrets is what makes dispatching it a safe
	// thing for anyone to do.
	if strings.Contains(nightly, "secrets.") {
		t.Error("nightly.yml reads a secret; it is dispatchable against any branch, and holds nothing worth stealing today")
	}

	// Nothing in it may be advisory. `continue-on-error` on a nightly
	// job produces a green tick over a failure, which is how a check
	// stops being read at all.
	absent(t, nightly, "continue-on-error")

	// And the prompt that gets it read. A nightly nobody is asked to
	// look at is the failure mode the whole arrangement exists to avoid,
	// so the workflow points at the standing prompt and the prompt has
	// to be there.
	// (the raw file, not the comment-stripped copy: this reference is
	// prose, and prose is what has just been stripped out above)
	contains(t, nightlyWorkflow(t), "docs/nightly-test-review.md")
	read(t, "docs", "nightly-test-review.md")
}

// The long Go suite is the gate's own suite with more asked of it, not a
// different one.
//
// That distinction is the whole design: if `test-long` drifted into
// running a subset, or dropped -race to buy back the time -count=2
// costs, the nightly would stop being a stricter reading of the same
// coverage and become a second, weaker suite whose green tick means
// something nobody could state. The two flags are named individually
// because dropping either is a silent loss -- the suite still passes,
// it just stops looking for the thing it was added to look for.
func TestTheLongGoSuiteIsTheGateSuiteWithMoreAskedOfIt(t *testing.T) {
	recipes := map[string]string{}
	for _, line := range strings.Split(between(t, read(t, "Makefile"), "\ntest-long: frontend", "\n\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "go test"):
			recipes["."] = trimmed
		}
	}

	for _, module := range []string{"."} {
		recipe := recipes[module]
		if recipe == "" {
			t.Errorf("the Makefile's test-long target runs no `go test` command for %s", module)
			continue
		}
		for _, flag := range []string{"-race", "-shuffle=on", "-count=2", "./..."} {
			contains(t, recipe, flag)
		}
	}
}

// The sandbox guest image's toolchain, which three files have to agree
// on and none of them can see the others.
//
// A dispatched task -- most sharply a merge-queue fix task, which exists
// only to make a red build green -- can only check its own work if the
// guest carries the Go the module asks for and the Node CI runs, with
// caches warm enough to build offline. Getting either version from
// somewhere other than the file that already pins it is how a guest ends
// up failing a task's `go build` for a reason that has nothing to do
// with the task.
func TestTheSandboxGuestBuildsWithTheToolchainsTheRepositoryPins(t *testing.T) {
	granulefile := read(t, "Granulefile")

	// Go: one version, read out of go.mod -- its toolchain line when it
	// has one, its `go` directive when it does not, which is the order
	// the Makefile reads them in (GSA1-13: a guest and an image built
	// with the directive's floor shipped what the toolchain line fixed).
	makefile := read(t, "Makefile")
	contains(t, makefile, `sed -n 's/^toolchain[[:space:]]\{1,\}go//p' go.mod`)
	contains(t, makefile, `sed -n 's/^go[[:space:]]\{1,\}//p' go.mod`)
	goModBytes := read(t, "go.mod")
	goModVersion := regexp.MustCompile(`(?m)^toolchain go(\d+\.\d+(\.\d+)?)`).FindStringSubmatch(goModBytes)
	if goModVersion == nil {
		goModVersion = regexp.MustCompile(`(?m)^go (\d+\.\d+(\.\d+)?)`).FindStringSubmatch(goModBytes)
	}
	if goModVersion == nil {
		t.Fatal("go.mod pins no go version")
	}
	contains(t, granulefile, fmt.Sprintf("ARG GO_VERSION=%s", goModVersion[1]))

	// Node: the guest fetches a full version tarball, and Grain CI runs
	// directly inside the guest built from Granulefile.
	guestNode := regexp.MustCompile(`ARG NODE_VERSION=(\d+)\.[^\n]*`).FindStringSubmatch(granulefile)
	if guestNode == nil {
		t.Fatal("Granulefile pins no NODE_VERSION")
	}
}

// The other half of "what the guest carries": the small binaries a
// dispatched run reaches for without ever being told they are there.
//
// These are not toolchain in the sense the versions above are -- nothing
// in this repository builds against them -- which is exactly why they are
// easy to drop from the install line and hard to notice missing. What
// notices is a run, at dispatch time, by way of `command not found`: 37
// of this deployment's first 183 runs hit one, and 42 of those 67 misses
// were procps' own binaries (grain/task-215). `ps`, `pgrep` and `pkill`
// are how an agent asks whether the thing it started is still running,
// `file` is how it asks what it just downloaded, and `shellcheck` is what
// the repository's own CI runs over every script in scripts/. A miss
// costs more than the turn it wastes: exit 127 in front of an `&&` chain
// takes the rest of the chain with it.
func TestTheSandboxGuestCarriesTheToolsDispatchedRunsReachFor(t *testing.T) {
	granulefile := read(t, "Granulefile")
	for _, pkg := range []string{"procps", "file", "shellcheck", "actionlint"} {
		contains(t, granulefile, pkg)
	}
}

// Essential guest kernel boot and networking packages.
//
// Guest image builds run with `--no-install-recommends`, which avoids
// pulling in recommended packages but can inadvertently drop low-level kernel
// userspace utilities (like `klibc-utils`, which provides
// `/usr/lib/klibc/bin/ipconfig` needed by `granule-net-cmdline.service` to
// configure IPv4 on `eth0`).
//
// These assertions verify that Granulefile explicitly declares the kernel image,
// init system, device manager, module utilities, network tooling, and ACPI daemon.
func TestTheSandboxGuestCarriesEssentialKernelBootAndNetworkingPackages(t *testing.T) {
	granulefile := read(t, "Granulefile")
	for _, pkg := range []string{
		"klibc-utils",
		"systemd-sysv",
		"udev",
		"kmod",
		"iproute2",
		"acpid",
		// Spelled per architecture rather than as linux-image-amd64:
		// this image is built for whichever architecture its host runs,
		// and naming amd64's kernel on an arm64 build asks apt for a
		// package that does not apply to it. Asserting the substitution
		// rather than just "linux-image" is the point -- a regression to
		// a fixed architecture is what would break an arm64 guest, and it
		// would break it at boot rather than at build.
		"linux-image-$(dpkg --print-architecture)",
	} {
		contains(t, granulefile, pkg)
	}
}

// A guest can act on the power button, which is how granule asks it to
// stop.
//
// `granule vm delete` calls cloud-hypervisor's vm.power-button and then
// waits -- VM SET shutdown-timeout, 20s by default -- before forcing the
// VMM down. Nothing in these images consumed that press: acpid was
// installed and never enabled, and it is the only candidate here, since
// systemd-logind needs a dbus this guest does not run. So every teardown
// paid the whole timeout and then a kill. Measured on arm64 before this:
// a guest that had reached ready and answered an exec still produced
// "guest did not shut down in time, forcing vmm shutdown", and the delete
// took 21.1s.
//
// evdev because the press does not arrive the same way on both
// architectures. x86's ACPI button also reaches acpid over netlink, so
// enabling acpid is enough there; on aarch64 there is no ACPI button at
// all -- cloud-hypervisor pulses a PL011-era PL061 GPIO, which Linux
// presents only as a gpio-keys input device -- and without evdev there is
// no /dev/input/event* for acpid to read. The guest showed exactly that:
// gpio-keys present with "Handlers=kbd" and nothing but "mice" under
// /dev/input.
func TestTheSandboxGuestCanActOnThePowerButton(t *testing.T) {
	for _, file := range []string{"Granulefile", "Granulefile.guest-base"} {
		granulefile := read(t, file)
		contains(t, granulefile, "acpid")
		contains(t, granulefile, "systemctl enable acpid")
		contains(t, granulefile, "/etc/modules-load.d/granule-evdev.conf")
		// Installed-but-not-enabled is the bug, so the enable has to be in
		// the same image as the package rather than merely somewhere.
		before(t, granulefile, "acpid", "systemctl enable acpid",
			file+" enables acpid before installing it")
	}
}

// Both guest images pin every toolchain they download, for both
// architectures, and actually reach for the pin.
//
// A checksum is of a particular tarball, so one pin per tool only ever
// verified one architecture. That failed loudly at first -- an arm64 build
// fetched the arm64 archive and then compared it against amd64's digest --
// and the quiet way to make that stop is to drop the verification, which is
// the outcome this exists to prevent.
//
// The *version* is deliberately shared between architectures, which is the
// convention Dockerfile already uses for KUBECTL and the agent CLIs: a bump
// of one is a bump of both, so two builds of one grain commit cannot carry
// different toolchains (TestBothImagesPinTheSameAgentCLIs has the incident
// that established it). Only the digests differ.
func TestBothGuestImagesPinTheirToolchainsForBothArchitectures(t *testing.T) {
	for _, file := range []string{"Granulefile", "Granulefile.guest-base"} {
		code := read(t, file)
		// guest-base installs no toolchains; it only needs the kernel
		// package spelled per architecture, which its own assertion above
		// covers. Keyed on the download itself rather than on the pin: a
		// skip conditioned on GO_SHA256_AMD64 would be satisfied by
		// deleting the very thing this test exists to require, which made
		// it pass against a file that had been reverted to a single pin.
		if !strings.Contains(code, "go${GO_VERSION}.linux-") {
			continue
		}
		for _, want := range []string{
			`amd64) sha="${GO_SHA256_AMD64}"`,
			`arm64) sha="${GO_SHA256_ARM64}"`,
			`amd64) node_arch=x64; sha="${NODE_SHA256_AMD64}"`,
			`arm64) node_arch=arm64; sha="${NODE_SHA256_ARM64}"`,
			`amd64) sha="${ACTIONLINT_SHA256_AMD64}"`,
			`arm64) sha="${ACTIONLINT_SHA256_ARM64}"`,
			// An architecture with no pin stops the build rather than
			// installing a toolchain nobody checked. This is the branch no
			// build on either architecture ever takes, so it is the one
			// worth asserting.
			`no pinned go checksum for $arch`,
			`no pinned node checksum for $arch`,
			`no pinned actionlint checksum for $arch`,
			`| sha256sum -c -`,
		} {
			if !strings.Contains(code, want) {
				t.Errorf("%s does not contain %s", file, want)
			}
		}
		// And no single-architecture pin survives, which is what a careless
		// revert would reintroduce.
		for _, gone := range []string{"ARG GO_SHA256=", "ARG NODE_SHA256=", "ARG ACTIONLINT_SHA256="} {
			if strings.Contains(code, gone) {
				t.Errorf("%s still has the single-architecture pin %s", file, gone)
			}
		}
	}
}

// The ~105 seconds a guest spends waiting for hardware it does not have.
//
// Debian's initramfs-tools infers a resume device at initramfs build time
// and its scripts/local-premount/resume waits for the device RESUME=
// names, giving up about 105 seconds later with "Gave up waiting for
// suspend/resume device" printed on the console and nowhere else. A guest
// here is booted fresh from a packed image every time, has no swap and
// carries no hibernation image, so that wait buys nothing and is paid on
// every boot: it is the whole of the gap between "virtio_blk virtio0:
// [vda]" and "EXT4-fs (vda): mounted filesystem" that put the login
// prompt at ~122s against GranuleConfig.readyTimeout's hard-coded two
// minutes, and so turned sandbox creation into a coin flip -- 254 failed
// creations, 87 "never acquired a default route" entries in one
// 30-minute window, an operator merging pull requests by hand
// (grain/task-593).
//
// Two things say it, because they cover different guests. RESUME=none in
// the Granulefiles is what fixes the guests this repository builds, and
// it has to be written before the initramfs is generated or the
// generated one does not carry it. `noresume` on granule's derived
// kernel command line (docs/granule-local-patches.md) is what covers a
// guest built from an image this repository does not control. It began
// as a local patch on the vendored tree and is on granule's own main now,
// but a resync is still the kind of thing that drops it without a word.
func TestTheSandboxGuestNeverWaitsForASuspendResumeDevice(t *testing.T) {
	for _, file := range []string{"Granulefile", "Granulefile.guest-base"} {
		granulefile := read(t, file)
		contains(t, granulefile, "/etc/initramfs-tools/conf.d")
		contains(t, granulefile, "RESUME=none")
		before(t, granulefile, "RESUME=none", "update-initramfs",
			file+" writes RESUME=none after the initramfs is built, so the initramfs it ships does not carry it")
	}

	// granule's own two spellings of the default command line: the one
	// `vm create` derives (vmspec.VMSpec.Validate) and the one the VM
	// container falls back to when CHV_CMDLINE is unset.
	contains(t, read(t, "third_party", "granule", "pkg", "vmspec", "spec.go"), `root=/dev/vda %s noresume`)
	contains(t, read(t, "third_party", "granule", "pkg", "config", "config.go"), `root=/dev/vda rw noresume`)
	contains(t, read(t, "docs", "granule-local-patches.md"), "noresume")
}

// A granule guest is told the console its architecture actually has.
//
// This is a local patch on a vendored tree
// (docs/granule-local-patches.md), which is exactly the kind of thing a
// resync drops without a word -- and dropping this one does not fail
// loudly. x86 has an 8250 at the ISA ports Linux calls ttyS0; aarch64 has
// no ISA ports and cloud-hypervisor gives the guest an ARM PL011, which
// Linux calls ttyAMA0. A guest told the wrong one prints nothing and
// never becomes reachable, and because the console is the broken thing
// there is nothing on it to say so: `granule vm run` reports only "vsock
// port 1024 is not being listened on inside the guest yet". Measured on
// aarch64, two boots with ttyS0 sat unreachable for ten and twelve
// minutes where the same image with ttyAMA0 reached ready.
//
// That is the whole of what stands between a mac and a working sandbox:
// grain on macOS runs its daemon natively and its guests in an arm64
// Linux VM, so every guest it starts there is one granule would otherwise
// misconfigure.
func TestAGranuleGuestIsToldTheConsoleItsArchitectureHas(t *testing.T) {
	config := read(t, "third_party", "granule", "pkg", "config", "config.go")
	spec := read(t, "third_party", "granule", "pkg", "vmspec", "spec.go")

	// The mapping, and that it is read off the architecture rather than
	// written down once.
	contains(t, config, "func guestConsoleFor(goarch string) string")
	contains(t, config, `return "ttyAMA0"`)
	contains(t, config, "runtime.GOARCH")

	// Both of granule's two spellings of the default command line take the
	// console from it: the one the VM container falls back to when
	// CHV_CMDLINE is unset, and the one `vm create` derives.
	contains(t, config, `"console=" + GuestConsole() + " root=/dev/vda rw noresume`)
	contains(t, spec, "config.GuestConsole()")

	// A resync that reverted either half would put the literal back.
	absent(t, spec, `"console=ttyS0 root=/dev/vda %s noresume`)

	contains(t, read(t, "docs", "granule-local-patches.md"), "ttyAMA0")
}

func TestAGranuleVMContainerHasACgroupMemoryLimitAndPositiveOOMScoreAdj(t *testing.T) {
	spec := read(t, "third_party", "granule", "pkg", "vmspec", "spec.go")
	cli := read(t, "third_party", "granule", "pkg", "cli", "vm.go")
	docker := read(t, "third_party", "granule", "pkg", "dockervm", "docker.go")

	contains(t, spec, "VMMMemoryOverheadMB = 512")
	contains(t, spec, "DefaultOOMScoreAdj = 500")
	contains(t, cli, `"memory-limit-mb"`)
	contains(t, cli, `"oom-score-adj"`)
	contains(t, docker, `"--oom-score-adj"`)
	contains(t, docker, `"--memory"`)
	contains(t, docker, `"--memory-swap"`)
	contains(t, read(t, "docs", "granule-local-patches.md"), "memory-limit-mb")
}

// grain-connect, which is the guest half of grain's only way to reach a
// server listening inside a sandbox.
func TestTheSandboxGuestCarriesGrainsOwnForwarder(t *testing.T) {
	granulefile := read(t, "Granulefile")

	contains(t, granulefile, "/usr/local/bin/grain-connect")
	contains(t, granulefile, "cmd/grain-connect/main.go")

	// The directory a socket goes in survives a boot, or nothing that
	// binds one works on a cloned VM.
	contains(t, granulefile, "/usr/lib/tmpfiles.d/grain.conf")
	contains(t, granulefile, "d /run/grain 0755 root root -")
}

// Docker MTU configuration ensures dockerd's docker0 bridge and container
// interfaces inherit the guest VM's interface MTU (e.g. 1460 in GCP VPC)
// rather than defaulting to 1500 and causing packet drops at the VPC boundary.
func TestTheSandboxGuestConfiguresDockerMTU(t *testing.T) {
	for _, file := range []string{"Granulefile"} {
		granulefile := read(t, file)
		contains(t, granulefile, "/etc/docker/daemon.json")
		contains(t, granulefile, `"mtu": 1460`)
		contains(t, granulefile, "/usr/local/libexec/docker-configure-mtu")
		contains(t, granulefile, "/etc/systemd/system/docker.service.d/10-mtu.conf")
		contains(t, granulefile, "ExecStartPre=/usr/local/libexec/docker-configure-mtu")
		contains(t, granulefile, "/sys/class/net/eth0/mtu")
		contains(t, granulefile, "granule")
		contains(t, granulefile, "cmdline")
	}
}

// OCR, which is the same argument as the one above with a sharper edge:
// this is not a tool a run reaches for and finds missing, it is the only
// way a run reads an attachment at all.
func TestTheSandboxGuestCanReadTextOutOfAnImage(t *testing.T) {
	granulefile := read(t, "Granulefile")
	for _, pkg := range []string{"tesseract-ocr", "tesseract-ocr-eng", "imagemagick", "poppler-utils"} {
		contains(t, granulefile, pkg)
	}
	for _, pkg := range []string{"fonts-urw-base35", "fonts-dejavu-core"} {
		contains(t, granulefile, pkg)
	}
}

// And the other sandbox image, which has the same problem and reaches it
// by a different route: Dockerfile.k8s-sandbox is what the Docker and
// Kubernetes backends run (cmd/grain's defaultK8sSandboxImage), so a
// deployment on either of those gets none of the Granulefile's install
// line. Which backend a task is dispatched into is a deployment's own
// choice and nothing the run is ever told, so the two images answering
// `tesseract` differently would be a difference no prompt accounts for --
// and the failure is the silent one either way, an attachment read as
// bytes rather than a command reported missing.
//
// This image's package line is otherwise deliberately short -- no jq, no
// ripgrep, no python -- which is why the four are worth a test of their
// own rather than a line in the one above: the next person trimming it
// back to "ordinary user-space tools" should have to read why these are
// not that.
func TestTheKubernetesSandboxImageCanReadTextOutOfAnImageToo(t *testing.T) {
	dockerfile := read(t, "Dockerfile.k8s-sandbox")
	fullStage := from(t, dockerfile, "FROM k8s-sandbox-base")
	install := between(t, fullStage, "apt-get install -y --no-install-recommends", "rm -rf /var/lib/apt/lists")
	for _, pkg := range []string{"tesseract-ocr", "tesseract-ocr-eng", "imagemagick", "poppler-utils"} {
		contains(t, install, pkg)
	}
	// The fonts, for the reason the guest's own test spells out: without
	// the URW faces ImageMagick's default font resolves to a file that is
	// not there and `convert -annotate` core-dumps rather than reporting
	// anything at all.
	for _, pkg := range []string{"fonts-urw-base35", "fonts-dejavu-core"} {
		contains(t, install, pkg)
	}
	// `docker inspect` is the only description of this image an operator
	// gets without this repository in front of them -- the counterpart of
	// the guest's /etc/granule-guest/README -- so it names them too.
	label := between(t, dockerfile, "org.opencontainers.image.description=", "\n")
	for _, tool := range []string{"tesseract", "imagemagick", "poppler-utils", "fonts-urw-base35", "fonts-dejavu-core"} {
		contains(t, label, tool)
	}
}

func TestSandboxBaseImagesAreMinimalAndCarryTheVersionedContract(t *testing.T) {
	wantLabel := sandboximage.ContractLabel + `="` + sandboximage.ContractValue() + `"`
	wantFile := `printf '` + sandboximage.ContractValue() + `\n' > ` + sandboximage.ContractFile

	guestBase := read(t, "Granulefile.guest-base")
	granulefile := read(t, "Granulefile")
	k8sDockerfile := read(t, "Dockerfile.k8s-sandbox")

	for _, f := range []struct {
		name string
		text string
	}{
		{"Granulefile.guest-base", guestBase},
		{"Granulefile", granulefile},
		{"Dockerfile.k8s-sandbox", k8sDockerfile},
	} {
		if !strings.Contains(f.text, wantLabel) {
			t.Errorf("%s does not stamp %s", f.name, wantLabel)
		}
		if !strings.Contains(f.text, wantFile) {
			t.Errorf("%s does not write %s", f.name, wantFile)
		}
	}

	// Granulefile.guest-base carries the minimal Grain VM boundary and no
	// developer convenience or OCR packages; Granulefile's sandbox_rootfs
	// layers those convenience packages on top.
	for _, required := range []string{
		"python3-minimal",
		"python3 -c 'import os, socket, sys, threading'",
		"/usr/local/bin/grain-connect",
		"cmd/grain-connect/main.go",
		"useradd -m -u 1000 -s /bin/bash debian",
		"ln -s /home/debian /home/agent",
		"procps", "findutils", "grep", "sed", "gawk", "tar", "coreutils",
	} {
		contains(t, guestBase, required)
	}
	guestBaseCode := stripComments(guestBase)
	sandboxStage := from(t, granulefile, "FROM guest_base_rootfs AS sandbox_rootfs")
	for _, movedPkg := range []string{
		"build-essential",
		"python3-venv",
		"pipx",
		"jq",
		"ripgrep",
		"fd-find",
		"shellcheck",
		"tmux",
		"bubblewrap",
		"tesseract-ocr",
		"imagemagick",
		"poppler-utils",
	} {
		if strings.Contains(guestBaseCode, movedPkg) {
			t.Errorf("Granulefile.guest-base still installs developer/OCR package %q; move it to Granulefile's sandbox_rootfs", movedPkg)
		}
		contains(t, sandboxStage, movedPkg)
	}

	// Dockerfile.k8s-sandbox's k8s-sandbox-base stage carries only the
	// minimal container boundary; OCR and agent CLIs live in k8s-sandbox.
	k8sBaseStage := stripComments(upTo(t, k8sDockerfile, "FROM k8s-sandbox-base"))
	for _, required := range []string{
		"FROM ${BASE_IMAGE} AS k8s-sandbox-base",
		"python3-minimal",
		"python3 -c 'import os, socket, sys, threading'",
		"useradd --create-home --uid 1000 --shell /bin/bash debian",
		"ln -s /home/debian /home/agent",
		wantFile,
		wantLabel,
	} {
		contains(t, k8sBaseStage, required)
	}
	for _, nonBase := range []string{
		"tesseract-ocr",
		"imagemagick",
		"poppler-utils",
		"INSTALL_CLAUDE_CLI",
		"INSTALL_AGY_CLI",
		"INSTALL_CODEX_CLI",
	} {
		if strings.Contains(k8sBaseStage, nonBase) {
			t.Errorf("Dockerfile.k8s-sandbox's k8s-sandbox-base stage contains %q; keep it in the full k8s-sandbox stage", nonBase)
		}
	}

	// guest-inputs.sh base includes cmd/grain-connect/main.go because
	// grain-connect is built into guest-base.
	baseInputs, err := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "granule", "guest-inputs.sh"), "base").Output()
	if err != nil {
		t.Fatalf("running guest-inputs.sh base: %v", err)
	}
	contains(t, string(baseInputs), "cmd/grain-connect/main.go")

	// release-images.sh builds and publishes both base images alongside the
	// full sandbox images.
	releaseScript := read(t, "scripts", "release-images.sh")
	for _, want := range []string{
		`guest_base="$root/guest-base:sha-$short"`,
		`k8s_sandbox_base="$root/k8s-sandbox-base:sha-$short"`,
		`docker build --target k8s-sandbox-base -f Dockerfile.k8s-sandbox -t "$k8s_sandbox_base" .`,
		`push_pinned "$guest_base"`,
		`push_pinned "$k8s_sandbox_base"`,
		`"$guest_base $root/guest-base:$branch"`,
		`"$k8s_sandbox_base $root/k8s-sandbox-base:$branch"`,
	} {
		contains(t, releaseScript, want)
	}

	// Contract documentation names both contract labels and both base images.
	doc := read(t, "docs", "sandbox-image-contract.md")
	for _, want := range []string{
		sandboximage.ContractLabel,
		sandboximage.ContractFile,
		sandboximage.GranuleContractLabel,
		"ghcr.io/bwsalmon/grain/guest-base",
		"ghcr.io/bwsalmon/grain/k8s-sandbox-base",
		"grain sandbox-image contract",
		"grain sandbox-image check",
	} {
		contains(t, doc, want)
	}
}

// A deployment's daemon and its sandboxes have to be running the same
// three agent CLIs, and the only thing that decides which is a default
// spelled out twice: nothing passes --build-arg to either image
// (build-artifacts.yml, scripts/publish-images.sh and tests.yml all run
// a bare `docker build -f <file> -t <tag> .`), so what each file says is
// what ships.
//
// The failure skew produces is not a build error or a dispatch error.
// pkg/agent/{antigravity,claude,codex} build argv for the CLI this
// repository measured -- docs/{agy,claude,codex}-surface.md are captures
// of exactly the pinned versions, and their tests fail stale against the
// main Dockerfile's ARGs -- and then the sandbox's own binary runs that
// argv. A flag one version takes and another rejects surfaces in the
// middle of somebody's run, on a version no file recorded.
//
// Dockerfile.k8s-sandbox reached that state honestly: it installed agy
// and claude through their vendors' install.sh, each of which serves the
// current release, so two builds of the same grain commit could carry
// two different CLIs (agy 1.1.27 in the daemon's image against the 1.2.5
// install.sh was serving on 2026-09-17 -- docs/hand-runs/2026-09-17-agy-self-update.md,
// whose "Still owed" left this open). Disabling the self-updater stopped
// that binary changing under a *running* task; it said nothing about
// which binary the image started with.
//
// So the two files carry the same values, and this is what makes a bump
// of one a bump of both.
func TestBothImagesPinTheSameAgentCLIs(t *testing.T) {
	daemon := read(t, "Dockerfile")
	sandbox := read(t, "Dockerfile.k8s-sandbox")
	for _, arg := range []string{
		"CLAUDE_CLI_VERSION",
		"CLAUDE_CLI_SHA256_AMD64",
		"CLAUDE_CLI_SHA256_ARM64",
		"AGY_CLI_VERSION",
		"AGY_CLI_BUILD",
		"AGY_CLI_SHA256_AMD64",
		"AGY_CLI_SHA256_ARM64",
		"CODEX_CLI_VERSION",
		"CODEX_CLI_SHA256_AMD64",
		"CODEX_CLI_SHA256_ARM64",
		"CODEX_CODE_MODE_HOST_SHA256_AMD64",
		"CODEX_CODE_MODE_HOST_SHA256_ARM64",
	} {
		want, got := buildArgDefault(t, "Dockerfile", daemon, arg), buildArgDefault(t, "Dockerfile.k8s-sandbox", sandbox, arg)
		if want != got {
			t.Errorf("Dockerfile pins %s=%s and Dockerfile.k8s-sandbox pins %s=%s; a deployment's daemon and its sandboxes must not be on two different CLIs, so bump both in the same commit", arg, want, arg, got)
		}
	}

	// And the pin only means anything if the build fetches the artifact
	// it names, and checks it. Every CLI comes from its immutable release
	// artifact with its digest checked before anything in it runs: agy
	// from the release bucket, the way the daemon's image has fetched it
	// since the installer was found to be mutable; claude from its own
	// release bucket rather than through claude.ai/install.sh, which runs
	// the *latest* release before installing the one asked for; codex's
	// two archives from GitHub Releases. `--version` afterwards is what
	// makes each pin more than a request.
	for _, file := range []struct{ name, text string }{{"Dockerfile", daemon}, {"Dockerfile.k8s-sandbox", sandbox}} {
		code := stripComments(file.text)
		// The prose above the blocks names the mutable installers as the
		// thing this stopped doing, so only the commands are searched.
		for _, installer := range []string{"antigravity.google/cli/install.sh", "claude.ai/install.sh"} {
			if strings.Contains(code, installer) {
				t.Errorf("%s runs %s, which is not pinned to the version it is asked for", file.name, installer)
			}
		}
		for _, want := range []string{
			"storage.googleapis.com/antigravity-public/antigravity-cli",
			`test "$(agy --version | head -n1)" = "$AGY_CLI_VERSION"`,
			"downloads.claude.ai/claude-code-releases/${CLAUDE_CLI_VERSION}/${platform}/claude",
			`echo "$sha256  /tmp/claude" | sha256sum -c -`,
			`echo "$sha256  /opt/claude/.local/share/claude/versions/$CLAUDE_CLI_VERSION" | sha256sum -c -`,
			`= "$CLAUDE_CLI_VERSION"`,
			`echo "$sha256  /tmp/codex.tar.gz" | sha256sum -c -`,
			`echo "$host_sha256  /tmp/codex-code-mode-host.tar.gz" | sha256sum -c -`,
		} {
			if !strings.Contains(code, want) {
				t.Errorf("%s does not run %s", file.name, want)
			}
		}

		// Both architectures by digest, not only the one a given build
		// host happens to be: these images are published for amd64 and
		// arm64, and an arm64 build that fell back to an installer would
		// be unpinned again with every other check here still green.
		for _, digest := range []string{
			"$AGY_CLI_SHA256_AMD64", "$AGY_CLI_SHA256_ARM64",
			"$CLAUDE_CLI_SHA256_AMD64", "$CLAUDE_CLI_SHA256_ARM64",
			"$CODEX_CLI_SHA256_AMD64", "$CODEX_CLI_SHA256_ARM64",
			"$CODEX_CODE_MODE_HOST_SHA256_AMD64", "$CODEX_CODE_MODE_HOST_SHA256_ARM64",
		} {
			if !strings.Contains(code, digest) {
				t.Errorf("%s never checks %s", file.name, digest)
			}
		}
	}

	// Every download here forces IPv4 and retries, which is a property of
	// the *line* and so is lost by anyone who rewrites one -- this test's
	// own change nearly took it off two of them, resolving a conflict
	// with the commit that had just added it. CI's build host has no
	// IPv6 route, so an AAAA-first fetch spends a connect on an
	// unreachable address; the Granulefile forces IPv4 for the same
	// reason (TestTheGuestBuildRidesOutASlowArchive).
	for _, line := range strings.Split(stripComments(sandbox), "\n") {
		if !strings.Contains(line, "curl -") {
			continue
		}
		if !strings.Contains(line, "curl -4 ") || !strings.Contains(line, "--retry-all-errors") {
			t.Errorf("Dockerfile.k8s-sandbox runs a curl that does not force IPv4 and retry: %s", strings.TrimSpace(line))
		}
	}
}

var buildArg = regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+)=(\S+)$`)

// buildArgDefault reads one `ARG NAME=value` default out of a Dockerfile.
func buildArgDefault(t *testing.T, name, dockerfile, arg string) string {
	t.Helper()
	for _, m := range buildArg.FindAllStringSubmatch(dockerfile, -1) {
		if m[1] == arg {
			return m[2]
		}
	}
	t.Fatalf("%s has no ARG %s=... with a default; nothing passes --build-arg to these builds, so an ARG without one installs nothing or installs the latest release", name, arg)
	return ""
}

var (
	fromImage    = regexp.MustCompile(`(?m)^FROM\s+(\S+)`)
	buildArgRef  = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)
	pinnedDigest = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	stageName    = regexp.MustCompile(`(?mi)^(?:VM\s+)?(?:FROM|PACK|WRAP|UNPACK)\b.*\s+AS\s+(\S+)\s*$`)
)

// Every base image the published images are built from is pinned by
// digest, not only by tag. A tag is a name its registry can repoint --
// a routine upstream rebuild, or an upstream account that is not
// upstream's any more -- and a rebuild of the same grain commit would
// then pull different bytes with nothing in this tree changing. The CLI
// downloads above are held to their digests for the same reason; this
// is that rule for the layers under them.
//
// Each FROM is read with its ARG defaults substituted, because that is
// what a bare `docker build` gets and the Makefile passes only
// GO_IMAGE and GO_VERSION -- neither of which chooses the digest.
func TestEveryBaseImageIsPinnedByDigest(t *testing.T) {
	texts := map[string]string{}
	for _, name := range []string{"Dockerfile", "Dockerfile.build", "Dockerfile.k8s-sandbox", "Granulefile", "Granulefile.guest-base"} {
		text := read(t, name)
		texts[name] = text
		code := stripComments(text)
		froms := fromImage.FindAllStringSubmatch(code, -1)
		if len(froms) == 0 {
			t.Fatalf("%s has no FROM line", name)
		}
		// A FROM naming an earlier stage of the same file pulls nothing.
		stages := map[string]bool{}
		for _, m := range stageName.FindAllStringSubmatch(code, -1) {
			stages[m[1]] = true
		}
		for _, m := range froms {
			if stages[m[1]] {
				continue
			}
			image := buildArgRef.ReplaceAllStringFunc(m[1], func(ref string) string {
				return buildArgDefault(t, name, text, buildArgRef.FindStringSubmatch(ref)[1])
			})
			if !pinnedDigest.MatchString(image) {
				t.Errorf("%s builds FROM %s (%s), which is not pinned by @sha256 digest; pin it as <image>:<tag>@sha256:<index digest>", name, m[1], image)
			}
		}
	}

	// The same upstream image carries the same pin everywhere it is used,
	// so one bump cannot leave a second copy behind on the old bytes.
	if a, b := buildArgDefault(t, "Dockerfile", texts["Dockerfile"], "GO_IMAGE_DIGEST"), buildArgDefault(t, "Dockerfile.build", texts["Dockerfile.build"], "GO_IMAGE_DIGEST"); a != b {
		t.Errorf("Dockerfile pins GO_IMAGE_DIGEST=%s and Dockerfile.build pins %s; bump both in the same commit", a, b)
	}
	for _, name := range []string{"Dockerfile.k8s-sandbox", "Granulefile", "Granulefile.guest-base"} {
		if a, b := buildArgDefault(t, "Dockerfile", texts["Dockerfile"], "RUNTIME_IMAGE"), buildArgDefault(t, name, texts[name], "BASE_IMAGE"); a != b {
			t.Errorf("Dockerfile's RUNTIME_IMAGE=%s and %s's BASE_IMAGE=%s differ; they are the same Debian base, so bump them in the same commit", a, name, b)
		}
	}
}

// The caches are what make those toolchains usable, and they are warmed
// from the same six manifests CI installs from: a sandbox has no route
// to proxy.golang.org or registry.npmjs.org, so anything the image did
// not resolve at build time cannot be resolved at dispatch time either.
func TestTheSandboxGuestWarmsItsCachesFromTheLockedManifests(t *testing.T) {
	granulefile := read(t, "Granulefile")
	contains(t, granulefile, "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD")
}

// What that build is exposed to on the way: five apt repositories and
// three download hosts, any one of which ends a nine-minute build. Three
// runs on grain/task-72 died exactly there -- a refused connection to
// packages.cloud.google.com, a name lookup for nodejs.org, and a
// download from that same Google archive that managed 68.7MB at 393kB/s
// with Acquire::Retries already set (grain/task-183).
//
// So the softening is pinned here, because none of it is visible in the
// shape of the script and all of it is easy to drop in an edit. Retries
// need a timeout to have teeth: a stalled download is not a failed one
// until something declares it dead, and apt waits 120s per read by
// default.
func TestTheGuestBuildRidesOutASlowArchive(t *testing.T) {
	granulefile := read(t, "Granulefile")

	contains(t, granulefile, `Acquire::Retries "3"`)
	contains(t, granulefile, `Acquire::http::Timeout "60"`)
	if n := strings.Count(granulefile, "-o Acquire::Retries=5 -o Acquire::http::Timeout=30"); n != 2 {
		t.Errorf("the gcloud archive's own retry policy is on %d apt-get commands, want both the update and the install", n)
	}
	contains(t, granulefile, "google-cloud-cli")
	contains(t, granulefile, "curl -4")
	contains(t, granulefile, `Acquire::ForceIPv4 "true"`)
}

// Every Granulefile the guest build reads has to be one granule accepts,
// which `granule granulefile check` says without building anything.
func TestTheGuestBuildScriptsAreSyntacticallyValid(t *testing.T) {
	for _, file := range []string{"Granulefile", "Granulefile.guest-base"} {
		out, err := exec.Command("granule", "granulefile", "check", "-f", filepath.Join(repoRoot(t), file)).CombinedOutput()
		if err != nil {
			out, err = exec.Command("go", "run", "github.com/bwsalmon/granule/cmd/granule", "granulefile", "check", "-f", filepath.Join(repoRoot(t), file)).CombinedOutput()
			if err != nil {
				t.Errorf("checking %s: %v\n%s", file, err, out)
			}
		}
	}
}

// granuleImage is the granule image every guest in this repository is
// packed onto, and the one name for it.
const granuleImage = "granule:vendored"

// Every `granulefile build` here names a granule image this repository
// builds, rather than taking the default.
//
// A `VM PACK` stage does not stand on its own: the Dockerfile granule
// generates from a Granulefile ends `FROM <granule image>` and copies the
// guest-side pieces out of that image as well
// (`COPY --from=<granule image> /usr/local/share/granule/guest-parts/`).
// With no -kontur-image the reference it writes there is granule's own
// `granule:latest` (third_party/granule's pkg/vmspec.DockerImage) -- and
// nothing in this repository builds, pulls or tags that. An unqualified
// name is a Docker Hub one, so what a build got was
//
//	#2 [internal] load metadata for docker.io/library/granule:latest
//	#2 ERROR: pull access denied, repository does not exist or may
//	   require authorization
//
// at the first step, on every commit, for as long as it stood: the guest
// pipeline became Granulefiles in one commit and left the image they pack
// onto behind (grain/task-512). Nothing caught it because nothing on a
// push builds a guest -- the verdict is a release, a post-merge dispatch
// or nobody -- so the cheap half of it is a content check, and this is it.
//
// The direction is "names one this repository produces", not merely
// "names one". A build arg pointing at a tag somebody has to publish, or
// at a moving `latest`, would put the content of a content-addressed
// cache key in a registry's hands; scripts/granule/guest-inputs.sh names
// third_party/granule for the other half of the same thought.
func TestEveryGuestBuildPacksOntoAGranuleImageThisRepositoryBuilds(t *testing.T) {
	// One: something here produces the image, out of the vendored tree.
	script := filepath.Join("scripts", "granule", "build-granule-image.sh")
	build := read(t, "scripts", "granule", "build-granule-image.sh")
	contains(t, build, `IMAGE="${IMAGE:-`+granuleImage+`}"`)
	contains(t, build, `docker build --target runtime -t "$IMAGE" third_party/granule`)
	executable(t, "scripts", "granule", "build-granule-image.sh")

	// Two: every build of a Granulefile names it. The files are read from
	// disk rather than listed, because the failure this is about is a
	// *new* call site written the way the old ones were.
	//
	// tests/deploy is skipped, and it is the one place a call site would
	// go unchecked: this file has to be able to quote the flag it is
	// asserting on.
	root := repoRoot(t)
	var built int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "third_party" || name == "node_modules" ||
				filepath.Join(root, "tests", "deploy") == path {
				return fs.SkipDir
			}
			return nil
		}
		// How a build is spelled in each kind of file. Go is the narrow
		// one deliberately: `exec.Command` passes the subcommand as two
		// arguments, and a Go file also *talks* about these builds in the
		// failure messages it prints when one goes wrong.
		var call string
		switch filepath.Ext(name) {
		case ".sh", ".yml":
			call = "granulefile build"
		case ".go":
			call = `"granulefile", "build"`
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, call) {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			built++
			named := granuleImageArgument(line)
			if named == "" {
				t.Errorf("%s: %s\n\tbuilds a Granulefile without -kontur-image, so it packs onto granulefile's "+
					"own default `granule:latest` -- a Docker Hub reference nothing in this repository "+
					"produces, and the build fails at its first step. Pass -kontur-image %s and build it "+
					"with %s first.", rel, strings.TrimSpace(line), granuleImage, script)
				continue
			}
			if named != granuleImage && !bindsGranuleImage(string(body), named) {
				t.Errorf("%s: %s\n\tpacks onto %q, which nothing in that file binds to %s -- the image %s "+
					"builds and the only one this repository produces", rel, strings.TrimSpace(line), named, granuleImage, script)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the checkout: %v", err)
	}
	if built == 0 {
		t.Fatal("no file in this checkout builds a Granulefile, so nothing above was checked; if the " +
			"builds moved, move this with them")
	}
}

// granuleImageArgument is what a line passes as -kontur-image, with the
// shell's and Go's quoting taken off: `"$granule_image"` and
// `"-kontur-image", granuleImage,` both name something this can look up.
// Empty means the line passed no such flag.
func granuleImageArgument(line string) string {
	_, rest, ok := strings.Cut(line, "-kontur-image")
	if !ok {
		return ""
	}
	rest = strings.TrimLeft(rest, " \t=,\"")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"',${}`)
}

// bindsGranuleImage is whether a file that names the image through a
// variable sets that variable to the image -- in shell, in Go or as a
// workflow's environment -- or builds whatever the variable names with
// build-granule-image.sh itself, as `grain init` does for a tag its
// caller may override (pkg/hostinit's GranuleRuntimeImage).
func bindsGranuleImage(body, name string) bool {
	for _, binding := range []string{
		name + `="` + granuleImage + `"`,
		name + ` = "` + granuleImage + `"`,
		name + `: ` + granuleImage,
		`"IMAGE=" + ` + name + `}, "./scripts/granule/build-granule-image.sh"`,
	} {
		if strings.Contains(body, binding) {
			return true
		}
	}
	return false
}

// The granule image is part of what the guest build produces, so the
// cache key has to name the tree it is built from.
//
// guest-inputs.sh's own header has the argument: a path left off is a
// change that does not invalidate the cache, and the way that fails is a
// deployment running a guest which is missing something its own commit
// added. The guest is `FROM` the granule image and carries its
// guest-parts, and granule's guest-side contract is only kept by the two
// coming from one commit -- so a vendored resync has to miss, and so does
// an edit to the script that decides how that image is built.
func TestTheGuestInputsNameTheGranuleTheGuestIsPackedOnto(t *testing.T) {
	root := repoRoot(t)
	for _, list := range []string{"base", "guest"} {
		out, err := exec.Command("bash", filepath.Join(root, "scripts", "granule", "guest-inputs.sh"), list).Output()
		if err != nil {
			t.Fatalf("running guest-inputs.sh %s: %v", list, err)
		}
		for _, want := range []string{"third_party/granule", "scripts/granule/build-granule-image.sh"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("scripts/granule/guest-inputs.sh %s does not name %s, so a change to it derives the "+
					"guest key of the tree it replaced and every consumer pulls a guest built from "+
					"something else:\n%s", list, want, out)
			}
		}
	}
}

// The KVM suite reuses the guest build-artifacts.yml published for
// this commit's inputs, and builds one only when there is none to reuse.
//
// That is what makes "a sandbox is built when the sandbox changes, not
// when anything at all is committed" true of a pull request rather than
// only of the publish: without it, every commit here -- nearly all of
// them Go or TypeScript that the guest does not contain -- pays 317s of
// VM boot and apt install it has no use for. It used to pay that twice,
// once in real-vm and once in the granule job beside it; that one is
// gone (grain/task-171), and real-vm is what is left to hold.
//
// Three things have to hold for it, and each of them fails quietly.
func TestTheKVMSuiteReusesThePublishedGuestRatherThanRebuildingIt(t *testing.T) {
	kvm := namedJob(t, stripComments(nightlyWorkflow(t)), "kvm-sandboxes")
	resolve := stripComments(read(t, "scripts", "granule-guest-resolve.sh"))
	release := stripComments(read(t, "scripts", "release-images.sh"))

	// One: scripts/granule-guest-resolve.sh looks the image up by the key
	// scripts/release-images.sh publishes it under -- the same
	// guest-inputs.sh list, the same `git rev-parse`, the same 32
	// characters of the same hash.
	contains(t, release, "mapfile -t guest_inputs < <(./scripts/granule/guest-inputs.sh guest)")
	contains(t, release, `guest_key="$(git rev-parse "${guest_inputs[@]/#/HEAD:}" | sha256sum | cut -c1-32)"`)
	contains(t, release, `guest_cache="$root/guest:inputs-$guest_key"`)

	contains(t, resolve, "mapfile -t inputs < <(./scripts/granule/guest-inputs.sh guest)")
	contains(t, resolve, `key="$(git rev-parse "${inputs[@]/#/HEAD:}" | sha256sum | cut -c1-32)"`)
	contains(t, resolve, `cached="ghcr.io/${GRAIN_CI_REPO,,}/guest:inputs-${key}"`)

	// Two: a miss still builds one, and a hit reaches the suite under the
	// local tag pkg/orchestrator/granule_docker_real_test.go expects.
	contains(t, kvm, "IMAGE: grain-guest:e2e-test")
	contains(t, kvm, "./scripts/granule-guest-resolve.sh")
	contains(t, resolve, `if [ -z "${GRAIN_GUEST_BUILD_ALWAYS:-}" ] && docker pull "$cached"; then`)
	contains(t, resolve, `docker tag "$cached" "$IMAGE"`)
	contains(t, read(t, "pkg", "orchestrator", "granule_docker_real_test.go"), `image = "grain-guest:e2e-test"`)

	// Three: reading that image costs no credential. The package is public
	// and the pull is anonymous.
	absent(t, kvm, "docker login")
	absent(t, resolve, "docker login")
	executable(t, "scripts", "granule-guest-resolve.sh")
	executable(t, "scripts", "granule-guest-probe.sh")
}

// What the probe reads, which is what the comparison above can find.
//
// A fingerprint is only as good as the questions in it: one that read a
// label off the image, or hashed a file every build writes differently,
// would produce a diff nobody can act on or a clean run that means
// nothing. So the two expensive halves are pinned by name here -- the
// module cache and the npm cache are the two expensive inputs a CI job
// should normally be able to reuse, and they are also the two things a
// stale guest is most likely to be wrong about, since they are warmed from
// lock files that change often.
func TestTheGuestProbeReadsTheGuestRatherThanItsLabels(t *testing.T) {
	probe := stripComments(read(t, "scripts", "granule-guest-probe.sh"))

	// The image is a granule image whose guest is an ext4 filesystem
	// inside it, so reading it means booting it. Anything short of that
	// -- a label, an image digest, a config blob -- describes how the
	// image was built rather than what came out.
	contains(t, probe, "granule")
	contains(t, probe, "vm run")

	// The toolchains .grain/workflows/tests.yml counts on, and the caches
	// that keep its dependency installs fast and reproducible.
	for _, want := range []string{
		"/usr/local/go/bin/go",
		"/usr/local/bin/node",
		"/home/debian/go/pkg/mod/cache/download",
		"/home/debian/.npm/_cacache",
		"dpkg-query",
	} {
		contains(t, probe, want)
	}

	// And the two halves have to stay two halves. The pinned one is a
	// verdict and the drift one is a measurement; collapsing them would
	// either fail the job every time Debian ships a security update or
	// bury a real finding in a hundred version bumps.
	contains(t, probe, "#### grain-guest-probe pinned ####")
	contains(t, probe, "#### grain-guest-probe drift ####")
	contains(t, probe, "#### grain-guest-probe end ####")

	// And the name it boots that VM under has to fit in a network
	// interface. netshim derives the tap device from it as "tap-<name>",
	// Linux caps an interface name at 15 characters, and vmspec.Validate
	// rejects anything longer before the VM is ever created -- so an
	// over-long name is not a slow failure, it is the probe never running
	// at all. That is exactly how the first real run of guest-cache-drift
	// died, three seconds in, on a `guest-probe-$$` that every stub
	// granule this script was developed against had accepted.
	//
	// Evaluated rather than matched: the name is generated, so the only
	// assertion worth making is about what it actually comes out as.
	assign := regexp.MustCompile(`(?m)^vm=.*$`).FindString(probe)
	if assign == "" {
		t.Fatalf("granule-guest-probe.sh no longer assigns vm=, so the name it boots under is unchecked")
	}
	out, err := exec.Command("bash", "-c", assign+"\nprintf '%s' \"$vm\"").Output()
	if err != nil {
		t.Fatalf("evaluating %q: %v", assign, err)
	}
	// Eleven, because "tap-" is four of the fifteen.
	if name := string(out); len(name) > 11 {
		t.Errorf("%s produced %q (%d characters); granule rejects a VM name over 11 because tap-%s exceeds 15", assign, name, len(name), name)
	} else if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(name) {
		t.Errorf("%s produced %q, which is not the DNS-1123 label vmspec.ValidateName requires", assign, name)
	}
}

// The excerpt itself, run rather than matched.
//
// Two things decide whether that annotation is worth reading, and neither
// is visible in the workflow's text. It has to pair each removed line with
// the added one: `diff -u` emits a hunk's removals and then its additions,
// so the first eight lines of a hunk that moved thirty package versions
// are eight *cached* values with nothing to compare them against, and a
// reader is no better off than with the count. And it has to stay one
// escaped line, because a workflow command that contains a real newline is
// a command the runner stops reading at that newline.
func TestTheDiffExcerptPairsWhatMovedAndCountsTheRest(t *testing.T) {
	executable(t, "scripts", "diff-excerpt.sh")
	if out, err := exec.Command("bash", "-n", filepath.Join(repoRoot(t), "scripts", "diff-excerpt.sh")).CombinedOutput(); err != nil {
		t.Errorf("bash -n scripts/diff-excerpt.sh: %v\n%s", err, out)
	}
	dir := t.TempDir()
	var cached, fresh []string
	for i := 1; i <= 10; i++ {
		cached = append(cached, fmt.Sprintf("package.version: pkg%02d 1.0.%d", i, i))
		fresh = append(fresh, fmt.Sprintf("package.version: pkg%02d 1.1.%d", i, i))
	}
	// One hunk, every line of it changed: the shape that made file order
	// useless, and the shape a Debian point release actually produces.
	diff := unifiedDiff(t, dir, strings.Join(cached, "\n")+"\n", strings.Join(fresh, "\n")+"\n")

	out := excerpt(t, diff, "8")
	if strings.Contains(out, "\n") {
		t.Errorf("the excerpt spans more than one line, which truncates the workflow command that carries it:\n%s", out)
	}
	lines := strings.Split(out, "%0A")
	if len(lines) != 9 {
		t.Fatalf("expected 8 named lines and a tail, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for i, line := range lines[:8] {
		want := "-"
		if i%2 == 1 {
			want = "+"
		}
		if !strings.HasPrefix(line, want) {
			t.Errorf("line %d of the excerpt is %q, so the removals and additions are not paired: half of an excerpt is values with nothing to compare them against", i+1, line)
		}
		if !strings.Contains(line, fmt.Sprintf("pkg%02d", i/2+1)) {
			t.Errorf("line %d of the excerpt is %q, which is not package %d: the pairing is off", i+1, line, i/2+1)
		}
	}
	// Twenty changed lines in, eight named, so the tail accounts for the
	// twelve the summary and the artifact still hold.
	if lines[8] != "... and 12 more" {
		t.Errorf("the excerpt ends %q rather than saying how many lines it left out", lines[8])
	}

	// And a comparison with nothing in it says nothing: the step still has
	// its own sentence to annotate, and "()" or a bare newline in the
	// middle of it would be worse than the count it replaced.
	same := unifiedDiff(t, dir, "a: 1\n", "a: 1\n")
	if got := excerpt(t, same, "8"); got != "" {
		t.Errorf("an empty diff excerpted to %q", got)
	}
}

// unifiedDiff writes two fingerprints into dir and returns the path of
// `diff -u` over them -- the same command the nightly runs, rather than a
// diff hand-written to suit the assertion.
func unifiedDiff(t *testing.T, dir, before, after string) string {
	t.Helper()
	a := filepath.Join(dir, "cached")
	b := filepath.Join(dir, "fresh")
	for path, text := range map[string]string{a: before, b: after} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// diff exits 1 when the files differ, which is the interesting case,
	// so only its output is read.
	out, err := exec.Command("diff", "-u", a, b).Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("diff -u: %v", err)
		}
	}
	path := filepath.Join(dir, "d.diff")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func excerpt(t *testing.T, diff, max string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(repoRoot(t), "scripts", "diff-excerpt.sh"), diff, max).Output()
	if err != nil {
		t.Fatalf("diff-excerpt.sh %s %s: %v", diff, max, err)
	}
	return strings.TrimRight(string(out), "\n")
}

func TestGranuleGuestMountsTmpOnTmpfsAndCloudtopScalesLargeHostSandboxShape(t *testing.T) {
	// 1. Both guest image definitions mount /tmp on tmpfs in /etc/fstab before VM PACK.
	const fstabTmpfs = `echo 'tmpfs /tmp tmpfs mode=1777,strictatime,nosuid,nodev,size=50% 0 0' >> /etc/fstab`
	for _, file := range []string{"Granulefile", "Granulefile.guest-base"} {
		content := read(t, file)
		before(t, content, fstabTmpfs, "VM PACK --variant=debian AS guest_base",
			file+" must configure /tmp on tmpfs in /etc/fstab before packing the base guest rootfs")
	}

	// 2. GranuleSandboxes.runnerFor live-mounts tmpfs on /tmp for pre-built guest images.
	sandboxes := read(t, "pkg", "orchestrator", "granule_sandboxes.go")
	runnerBody := body(t, sandboxes, "func (k *GranuleSandboxes) runnerFor(")
	contains(t, runnerBody, "k.ensureGuestTmpfs(ctx, name, runner)")
	contains(t, sandboxes, `findmnt -n -o FSTYPE /tmp`)
	contains(t, sandboxes, `sudo -n mount -t tmpfs -o mode=1777,nosuid,nodev,size=50% tmpfs /tmp`)

	// 3. Large hosts (>= 32 vCPUs and >= 64 GiB RAM) seed 8 vCPUs / 16 GiB RAM.
	cloudtop := read(t, "google", "scripts", "cloudtop-install.sh")
	for _, want := range []string{
		"CLOUDTOP_LARGE_HOST_MIN_CPUS=32",
		"CLOUDTOP_LARGE_HOST_MIN_MEMORY_MB=65536",
		"CLOUDTOP_LARGE_SANDBOX_CPUS=8",
		"CLOUDTOP_LARGE_SANDBOX_MEMORY_MB=16384",
		`GRAIN_SANDBOX_CPUS:=$CLOUDTOP_LARGE_SANDBOX_CPUS`,
		`GRAIN_SANDBOX_MEMORY_MB:=$CLOUDTOP_LARGE_SANDBOX_MEMORY_MB`,
	} {
		contains(t, cloudtop, want)
	}
	setup := setupCode(t)
	contains(t, setup, `daemon_args+=(-sandbox-cpus "$GRAIN_SANDBOX_CPUS")`)
	contains(t, setup, `daemon_args+=(-sandbox-memory-mb "$GRAIN_SANDBOX_MEMORY_MB")`)
}

// --- the sandbox volume -------------------------------------------------
//
// terraform/gcp gives sandboxes a disk of their own: docker's data root
// (the sandbox image, and every granule VM's writable root, which is a
// qcow2 overlay inside that VM's own container) and HostSandboxes'
// per-run checkouts, so a task that fills a disk costs the runs in flight
// rather than the OS, the store, config-sync and the UI. Three files have
// to agree for that to be true, and none of them can see the others:
// instance.tf attaches the disk, files/startup.sh mounts it and points
// docker at it, and scripts/setup.sh decides where the daemon looks for
// its checkouts.

func startupScript(t *testing.T) string {
	return read(t, "deploy", "terraform", "gcp", "files", "startup.sh")
}

// startupPaths reads every `readonly NAME="..."` out of startup.sh with
// references to earlier ones expanded, so an assertion can be about the
// paths the script actually uses rather than the text it spells them
// with.
func startupPaths(t *testing.T, text string) map[string]string {
	t.Helper()
	vals := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^readonly ([A-Z_]+)="([^"]*)"`).FindAllStringSubmatch(text, -1) {
		value := m[2]
		// Longest name first: "$DOCKER_DROPIN_DIR" also starts with
		// "$DOCKER_DROPIN", and substituting the shorter one into it
		// would produce a path that appears nowhere on the host.
		names := make([]string, 0, len(vals))
		for name := range vals {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
		for _, name := range names {
			value = strings.ReplaceAll(value, "$"+name, vals[name])
		}
		vals[m[1]] = value
	}
	// A second pass, for a value that names one declared below it -- the
	// expansion above only knows about the ones it has already read.
	for name, value := range vals {
		for other, known := range vals {
			if other != name {
				value = strings.ReplaceAll(value, "$"+other, known)
			}
		}
		vals[name] = value
	}
	return vals
}

// under reports whether path is dir itself or anything inside it.
func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// Every package CI publishes is a package something deletes from.
func TestThePruneJobCoversEveryPackageCIPublishes(t *testing.T) {
	published := map[string]bool{}
	for _, m := range regexp.MustCompile(`"\$root/([a-z0-9-]+):`).FindAllStringSubmatch(stripComments(read(t, "scripts", "release-images.sh")), -1) {
		published[m[1]] = true
	}
	if len(published) == 0 {
		t.Fatal("release-images.sh pushes no ghcr.io package at all, which cannot be right")
	}

	prune := read(t, "cmd", "ghcr-prune", "main.go")
	m := regexp.MustCompile(`fs\.String\("packages", "([^"]+)"`).FindStringSubmatch(prune)
	if m == nil {
		t.Fatal("cmd/ghcr-prune has no default -packages list")
	}
	pruned := map[string]bool{}
	for _, name := range strings.Split(m[1], ",") {
		pruned[strings.TrimSpace(name)] = true
	}

	for name := range published {
		if !pruned[name] {
			t.Errorf("CI publishes %q and the prune job never looks at it: that package grows for ever", name)
		}
	}
	for name := range pruned {
		if !published[name] {
			t.Errorf("the prune job names %q, which nothing publishes any more", name)
		}
	}
}

// The prune rule and the push agree on how the tags it judges are
// spelled.
//
// cmd/ghcr-prune decides what a tag is *for* by matching its spelling --
// sha-<short> is a rollback target, inputs-<hash> is the guest cache,
// guest-base's bare hash is the granule base cache -- and anything it
// cannot place it treats as a name somebody may be using, which is the
// safe direction but also a silent one: a workflow that started pushing
// a 12-character short sha against a rule expecting seven would keep
// every build for ever and nothing would fail. So the widths CI actually
// pushes are checked against the widths the rule accepts.
func TestThePruneRuleKnowsTheTagsCIActuallyPushes(t *testing.T) {
	build := stripComments(read(t, "scripts", "release-images.sh"))
	plan := read(t, "cmd", "ghcr-prune", "plan.go")

	width := func(text, pattern, what string) int {
		t.Helper()
		m := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("cannot find %s (%s)", what, pattern)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s is not a number: %v", what, err)
		}
		return n
	}
	bounds := func(pattern, what string) (int, int) {
		t.Helper()
		m := regexp.MustCompile(pattern).FindStringSubmatch(plan)
		if m == nil {
			t.Fatalf("cmd/ghcr-prune/plan.go no longer matches %s the way this test reads it (%s)", what, pattern)
		}
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		return lo, hi
	}
	fits := func(n, lo, hi int, what string) {
		t.Helper()
		if n < lo || n > hi {
			t.Errorf("CI pushes a %d-character %s and the prune rule matches %d to %d, so it would not recognise one", n, what, lo, hi)
		}
	}

	// sha-${commit:0:7}, across all published images.
	lo, hi := bounds(`sha-\[0-9a-f\]\{(\d+),(\d+)\}`, "the commit tag")
	fits(width(build, `commit:0:(\d+)`, "the short sha CI publishes"), lo, hi, "commit tag")

	// inputs-<key> and guest-base's <key>, both `sha256sum | cut -c1-N`.
	key := width(build, `cut -c1-(\d+)`, "the cache key CI publishes")
	lo, hi = bounds(`inputs-\[0-9a-f\]\{(\d+),(\d+)\}`, "the guest cache tag")
	fits(key, lo, hi, "guest cache tag")
	lo, hi = bounds(`hashTag   = regexp\.MustCompile\(`+"`"+`\^\[0-9a-f\]\{(\d+),(\d+)\}`, "the base cache tag")
	fits(key, lo, hi, "granule base cache tag")
}

// A branch tag and the commit tag of the build it names are one
// manifest, which is what makes "the most recent build of every live
// branch is kept" keep a rollback target rather than just a moving name.
func TestABranchTagAndItsCommitTagAreOneManifest(t *testing.T) {
	script := stripComments(read(t, "scripts", "release-images.sh"))
	for _, pair := range []string{
		`"$guest_base $root/guest-base:$branch"`,
		`"$guest $root/guest:$branch"`,
		`"$k8s_sandbox_base $root/k8s-sandbox-base:$branch"`,
		`"$k8s_sandbox $root/k8s-sandbox:$branch"`,
		`"$grain $root/grain:$branch"`,
		`"$google_grain $root/google-grain:$branch"`,
	} {
		contains(t, script, pair)
	}
	contains(t, script, `docker tag "$source" "$target"`)
	contains(t, script, `push_image "$target"`)
}

// The job that can delete images is a job no branch push can reach, it
// deletes only when it is told to, and it is not part of anything a host
// runs.
//
// It holds package-publish, which is the capability that deletes a
// package version -- strictly worse in the wrong hands than the one
// build-artifacts.yml holds to publish, and the same argument that file
// makes about push-vs-pull_request applies with more force. What keeps
// it out of reach is the trigger: workflow_dispatch needs write access
// to the repository. Scheduled runs cannot receive package-publish credentials,
// so automated pruning runs on release in release.yml instead.
//
// The dry-run default is the other half. Deleting a package version
// cannot be undone, so the command deletes only when passed -apply, and
// a human dispatching this to see what it would do must not have to
// remember that.
func TestThePruneJobIsDeliberateAndOutOfBranchReach(t *testing.T) {
	prune := stripComments(pruneTemplate(t))

	contains(t, prune, "name: Prune GHCR packages")
	contains(t, prune, "cmd/ghcr-prune")

	// And it is a maintenance tool, not the deployment's: nothing on a host runs
	// it, so it stays out of the binaries `make build` produces and out
	// of the image they go into.
	for _, line := range strings.Split(read(t, "Makefile"), "\n") {
		if strings.HasPrefix(line, "CMDS :=") && strings.Contains(line, "ghcr-prune") {
			t.Errorf("ghcr-prune is in %q: it is a maintenance tool and belongs in no deployment image", line)
		}
	}
	absent(t, read(t, "Dockerfile"), "ghcr-prune")
}

// In Grain CI sandboxes, GITHUB_REPOSITORY is unset and GRAIN_CI_REPO is
// injected instead. cmd/ghcr-prune falls back to GRAIN_CI_REPO so scheduled
// and dispatch prune workflows run without requiring explicit -owner and -repo flags.
func TestThePruneCommandRecognizesGrainCIRepo(t *testing.T) {
	pruneMain := read(t, "cmd", "ghcr-prune", "main.go")
	contains(t, pruneMain, "GRAIN_CI_REPO")
	contains(t, pruneMain, `repoEnv = os.Getenv("GRAIN_CI_REPO")`)
}

func TestTheSandboxDiskTerraformAttachesIsTheOneTheHostMounts(t *testing.T) {
	instance := read(t, "deploy", "terraform", "gcp", "instance.tf")
	startup := startupScript(t)
	paths := startupPaths(t, startup)

	// The by-id link is derived from attached_disk's device_name, and is
	// the only stable way to tell the two disks apart inside the VM.
	for device, mount := range map[string]string{"grain-data": "DATA_MNT", "grain-sandbox": "SANDBOX_MNT"} {
		contains(t, instance, `device_name = "`+device+`"`)
		if paths[mount] == "" {
			t.Errorf("startup.sh declares no %s to mount %s at", mount, device)
		}
		contains(t, startup, "/dev/disk/by-id/google-"+device)
	}

	// Whether there is a second disk at all is metadata, not a guess: a
	// declared disk that failed to attach has to be a warning rather than
	// a host quietly filling its boot disk again.
	contains(t, instance, "grain-sandbox-disk = var.sandbox_disk_gb > 0")
	contains(t, startup, "instance/attributes/grain-sandbox-disk")

	// dockerd must not start before the volume it stores everything on is
	// mounted: without this it wins the race often enough to create its
	// data root on the boot disk and have the mount hide it. Written
	// either as the variable or as the path it holds.
	if !strings.Contains(startup, "RequiresMountsFor=$SANDBOX_MNT") &&
		!strings.Contains(startup, "RequiresMountsFor="+paths["SANDBOX_MNT"]) {
		t.Errorf("nothing makes docker.service wait for %s, so dockerd can start before the volume is mounted and put its data root on the boot disk", paths["SANDBOX_MNT"])
	}
}

func TestTheSandboxVolumeHoldsDockersStoreAndTheCheckoutsButNeverOneInsideTheOther(t *testing.T) {
	startup := startupScript(t)
	paths := startupPaths(t, startup)

	docker, work, mnt := paths["SANDBOX_DOCKER_DIR"], paths["SANDBOX_WORK_DIR"], paths["SANDBOX_MNT"]
	containerd := paths["SANDBOX_CONTAINERD_DIR"]
	if !under(docker, mnt) {
		t.Errorf("docker's data root (%s) is not on the sandbox volume (%s), so the sandbox image and every VM's overlay stay on the boot disk", docker, mnt)
	}
	if !under(containerd, mnt) {
		t.Errorf("containerd's root (%s) is not on the sandbox volume (%s)", containerd, mnt)
	}
	if !under(work, mnt) {
		t.Errorf("the per-run checkouts (%s) are not on the sandbox volume (%s)", work, mnt)
	}
	// The one arrangement that looks tidy and destroys the deployment:
	// orchestrator.HostSandboxes.ReapOrphans removes *everything* under
	// its base directory at startup, docker's whole store included.
	if under(docker, work) {
		t.Errorf("docker's data root (%s) is inside the directory HostSandboxes.ReapOrphans empties (%s)", docker, work)
	}
	if under(containerd, work) {
		t.Errorf("containerd's root (%s) is inside the directory HostSandboxes.ReapOrphans empties (%s)", containerd, work)
	}

	// The bind mount is what makes the checkouts land there with no
	// -sandbox-dir override anywhere: it has to be the path setup.sh
	// already defaults to.
	if sandboxDir := setupDefault(t, "GRAIN_SANDBOX_DIR"); paths["HOST_SANDBOX_MNT"] != sandboxDir {
		t.Errorf("startup.sh bind-mounts the sandbox volume onto %s but setup.sh points the daemon at %s", paths["HOST_SANDBOX_MNT"], sandboxDir)
	}
	if dataDir := setupDefault(t, "GRAIN_DATA_DIR"); paths["DATA_MNT"] != dataDir {
		t.Errorf("startup.sh mounts the data disk at %s but setup.sh points the daemon at %s", paths["DATA_MNT"], dataDir)
	}
	contains(t, stripComments(startup), `"data-root": "%s"`)
}

// startup.sh grew real branching with the sandbox volume (a mount that is
// allowed to fail, and a configuration to undo when the volume is turned
// off), and it is the one script here that only ever runs on a real GCE
// boot -- a syntax error in it is a host that never installs config-sync
// and so never deploys at all.
func TestTheHostBootScriptsAreSyntacticallyValidBash(t *testing.T) {
	for _, script := range []string{"startup.sh", "config-sync.sh", "deploy.sh"} {
		path := filepath.Join(repoRoot(t), "deploy", "terraform", "gcp", "files", script)
		if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("bash -n deploy/terraform/gcp/files/%s: %v\n%s", script, err, out)
		}
	}
}

// TestPrivateInfrastructureOrgPoliciesAreConfigured verifies that terraform/gcp
// declares organization policies restricting public GKE cluster endpoints,
// denying external IP allocation, and restricting public Cloud SQL IP access,
// controlled by enforce_private_infrastructure_policies.
func TestPrivateInfrastructureOrgPoliciesAreConfigured(t *testing.T) {
	variables := read(t, "deploy", "terraform", "gcp", "variables.tf")
	contains(t, variables, `variable "enforce_private_infrastructure_policies"`)
	contains(t, variables, "default     = true")

	iam := read(t, "deploy", "terraform", "gcp", "iam.tf")
	contains(t, iam, `resource "google_project_organization_policy" "restrict_public_endpoints"`)
	contains(t, iam, `constraint = "constraints/gke.restrictPublicEndpoints"`)
	contains(t, iam, `resource "google_project_organization_policy" "no_external_ip"`)
	contains(t, iam, `constraint = "constraints/compute.vmExternalIpAccess"`)
	contains(t, iam, `resource "google_project_organization_policy" "restrict_public_ip"`)
	contains(t, iam, `constraint = "constraints/sql.restrictPublicIp"`)
	contains(t, iam, "var.enforce_private_infrastructure_policies ? 1 : 0")

	playbook := read(t, "pkg", "capability", "bootstrap", "playbooks", "gcp-capabilities.md")
	contains(t, playbook, "-target=google_project_organization_policy.restrict_public_endpoints")
	contains(t, playbook, "-target=google_project_organization_policy.no_external_ip")
	contains(t, playbook, "-target=google_project_organization_policy.restrict_public_ip")
	contains(t, playbook, "enforce_private_infrastructure_policies")
}

// TestCloudSQLCapabilityIsConfigured verifies that terraform/gcp declares
// the capability to manage Cloud SQL instances for agent service accounts.
func TestCloudSQLCapabilityIsConfigured(t *testing.T) {
	variables := read(t, "deploy", "terraform", "gcp", "variables.tf")
	contains(t, variables, `variable "agent_can_manage_cloud_sql"`)
	contains(t, variables, "default     = true")

	iam := read(t, "deploy", "terraform", "gcp", "iam.tf")
	contains(t, iam, "var.agent_can_manage_cloud_sql")
	contains(t, iam, "roles/cloudsql.admin")
	contains(t, iam, `resource "google_project_service" "sqladmin"`)
	contains(t, iam, `service            = "sqladmin.googleapis.com"`)
	contains(t, iam, `resource "google_project_iam_member" "agent_cloud_sql"`)

	instance := read(t, "deploy", "terraform", "gcp", "instance.tf")
	contains(t, instance, "var.agent_can_manage_cloud_sql")

	playbook := read(t, "pkg", "capability", "bootstrap", "playbooks", "gcp-capabilities.md")
	contains(t, playbook, "-target=google_project_iam_member.agent_cloud_sql")
	contains(t, playbook, "-target=google_project_service.sqladmin")
}

// TestEuropeWest4TerraformConfiguration verifies that the europe-west4
// terraform setup configures a c4-standard-288-metal host with nested virtualization
// and a 2TB sandbox partition formatted with XFS.
func TestEuropeWest4TerraformConfiguration(t *testing.T) {
	tfvars := read(t, "deploy", "terraform", "gcp", "europe-west4.tfvars")
	contains(t, tfvars, `region = "europe-west4"`)
	contains(t, tfvars, `machine_type                 = "c4-standard-288-metal"`)
	contains(t, tfvars, `enable_nested_virtualization = true`)
	contains(t, tfvars, `sandbox_disk_gb = 2048`)

	startup := startupScript(t)
	contains(t, startup, "mkfs.xfs")
}

// The walkthrough's install (task 5047, bug B): a Google-track container
// install with hostd, GRAIN_AGY_PATH=/opt/fakeagy/agy GRAIN_AGY_RUN=host.
// The readiness block names the binary hostd's allowlist runs on this
// host, and where each CLI runs -- not the image's own agy.
func TestTheReadinessReportNamesWhereEachCLIRunsAndHostdsAgy(t *testing.T) {
	dir := t.TempDir()
	agy := filepath.Join(dir, "agy")
	if err := os.WriteFile(agy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	allow := filepath.Join(dir, "hostd.yaml")
	content := runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content",
		"HOSTD_AGY_PATH="+agy, "GRAIN_DATA_DIR=/d", "GRAIN_SANDBOX_DIR=/s", "HOSTD_PROXY_VARS=")
	if err := os.WriteFile(allow, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script := shellFunction(t, "cli_run_location") + shellFunction(t, "hostd_cli_path") +
		shellFunction(t, "report_agent_cli") +
		"agent_cli_in_image() { echo /usr/local/bin/$1; }\n" +
		`for c in claude agy codex; do echo "$c: $(report_agent_cli "$c" "" X)"; done`
	env := func(extra ...string) []string {
		return append([]string{"HOSTD_ALLOWLIST=" + allow, "GRAIN_IMAGE_REF=img", "GRAIN_HOSTD=1",
			"GRAIN_INSTALL_TRACK=google", "GRAIN_CLAUDE_RUN=", "GRAIN_CODEX_RUN=", "GRAIN_AGY_RUN="}, extra...)
	}

	out := runBash(t, script, env("GRAIN_AGY_RUN=host")...)
	contains(t, out, `agy: host, through grain hostd: `+agy+` (its allowlist entry "agy")`)
	absent(t, out, "/usr/local/bin/agy")
	contains(t, out, "claude: container: /usr/local/bin/claude (in img)")
	contains(t, out, "codex: container: /usr/local/bin/codex (in img)")

	// The Google build's own default puts agy on the host behind hostd.
	contains(t, runBash(t, script, env()...), "agy: host, through grain hostd: "+agy)
	// The core build's does not, and without hostd nothing is on the host.
	contains(t, runBash(t, script, env("GRAIN_INSTALL_TRACK=oss")...), "agy: container: /usr/local/bin/agy (in img)")
	contains(t, runBash(t, script, env("GRAIN_HOSTD=0", "GRAIN_AGY_RUN=host")...), "agy: container: /usr/local/bin/agy (in img)")
	// A host CLI hostd has no entry for, or one naming nothing runnable,
	// is MISSING -- which the report's per-CLI warnings key on.
	contains(t, runBash(t, script, env("GRAIN_CLAUDE_RUN=host")...),
		`claude: host, through grain hostd: MISSING (`+allow+` has no cli entry "claude")`)
	if err := os.Remove(agy); err != nil {
		t.Fatal(err)
	}
	contains(t, runBash(t, script, env()...), "agy: host, through grain hostd: MISSING (its allowlist entry \"agy\" names "+agy)

	readiness := body(t, setupCode(t), "report_readiness() {")
	contains(t, readiness, `status: $(hostd_status)`)
	contains(t, readiness, `case "$agy_cli" in`+"\n    *MISSING*)")
	status := body(t, setupCode(t), "hostd_status() {")
	contains(t, status, `grain_cli hostd -status -socket "$HOSTD_SOCKET"`)
	contains(t, status, "systemctl is-active grain-hostd.service")
}
