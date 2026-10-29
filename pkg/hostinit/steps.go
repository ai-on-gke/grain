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

package hostinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/safepath"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

// shQuote wraps s in single quotes for a Step.Command an operator copies
// into a shell, escaping any single quote s already contains.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// apiCurlCommand is the Step.Command for a request to the daemon's API
// that a step could not make, or decided an operator should: a curl that
// carries the access code as a bearer token, the way sys.HTTPRequest's
// own request did. A daemon is single-tenant and answers every caller
// without it -- loopback included -- with 401, so a bare curl printed
// here was a fix that could not work (task 1710). body is nil for none.
func apiCurlCommand(opts Options, method, url string, body []byte) string {
	cmd := "curl -s"
	if method != http.MethodGet {
		cmd += " -X " + method
	}
	cmd += " " + url + ` -H "Authorization: Bearer ` + accessCodeShell(opts) + `"`
	if body != nil {
		cmd += " -H 'Content-Type: application/json' -d " + shQuote(string(body))
	}
	return cmd
}

// accessCodeShell is a shell command substitution that prints the
// daemon's access code, for apiCurlCommand. On the host platforms it reads
// the token file this run's own requests present ($GRAIN_LOCAL_API_TOKEN_FILE,
// which cmd/grain's init points at -data-dir's), under sudo since the
// file is readable only by grain's account and root. On the Kubernetes
// platforms that file is on the pod's volume, so it asks the pod.
func accessCodeShell(opts Options) string {
	switch opts.Platform {
	case PlatformK8sContainer, PlatformK8sGranule:
		ns, deploy := opts.K8s.Namespace, opts.K8s.Deployment
		if opts.Platform == PlatformK8sGranule {
			ns, deploy = opts.K8sGranule.Namespace, opts.K8sGranule.Deployment
		}
		// deploy/kubernetes/grain*.yaml run the daemon with -data-dir=/data.
		return "$(" + kubectlCommand(opts.K8s, "-n", ns, "exec", "deploy/"+deploy, "--", "grain", "access-code", "-data-dir=/data") + ")"
	}
	path := strings.TrimSpace(os.Getenv(localAPITokenFileEnv))
	if path == "" {
		path = filepath.Join(opts.DataDir, localAPITokenFileName)
	}
	return "$(sudo cat " + shellWord(path) + ")"
}

// installApt get-or-creates pkgs: a no-op if they are already installed,
// an apt-get update+install if this process is root, and a StepManual
// naming the exact command otherwise. Returns whether pkgs ended up
// installed (true for StepDone, false for anything else).
func installApt(ctx context.Context, sys System, opts Options, r *Result, stepName string, pkgs []string, why string) bool {
	script := "apt-get update && apt-get install -y " + strings.Join(pkgs, " ")
	cmd := "sudo sh -c " + shQuote(script)
	if opts.DryRun {
		r.add(Step{Name: stepName, Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if !sys.IsRoot() {
		r.add(Step{Name: stepName, Status: StepManual, Detail: why, Command: cmd})
		return false
	}
	if _, err := sys.Run(ctx, "", nil, "sh", "-c", script); err != nil {
		r.add(Step{Name: stepName, Status: StepManual, Detail: fmt.Sprintf("install failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: stepName, Status: StepDone, Detail: "installed"})
	return true
}

// ensureGit get-or-installs git -- the orchestrator clones every task's
// repo through the git proxy with it.
func ensureGit(ctx context.Context, sys System, opts Options, r *Result) {
	if _, err := sys.Run(ctx, "", nil, "git", "--version"); err == nil {
		r.add(Step{Name: "git", Status: StepSkipped, Detail: "already installed"})
		return
	}
	installApt(ctx, sys, opts, r, "git", []string{"git"}, "the orchestrator clones every task's repo, and every repo it dispatches, through git")
}

// dockerRepoInstallScript installs docker-ce from Docker's own apt
// repository, the way docs.docker.com's own Debian instructions do --
// Debian 12's bundled docker.io package is 20.10, which granule does not
// run on.
const dockerRepoInstallScript = `set -eu
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin`

// ensureDocker get-or-installs docker-ce specifically -- checked by
// package name, not merely "docker --version", because docker.io (if
// present instead) answers that too, with a daemon too old for granule.
func ensureDocker(ctx context.Context, sys System, opts Options, r *Result) bool {
	if _, err := sys.Run(ctx, "", nil, "dpkg", "-s", "docker-ce"); err == nil {
		r.add(Step{Name: "docker", Status: StepSkipped, Detail: "docker-ce is already installed from Docker's own apt repository"})
		return true
	}
	why := "Debian 12's own docker.io package is 20.10 and granule drives a current docker; installed from Docker's own apt repository instead of Debian's"
	cmd := "sudo sh -c " + shQuote(dockerRepoInstallScript)
	if opts.DryRun {
		r.add(Step{Name: "docker", Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if !sys.IsRoot() {
		r.add(Step{Name: "docker", Status: StepManual, Detail: why, Command: cmd})
		return false
	}
	if _, err := sys.Run(ctx, "", nil, "sh", "-c", dockerRepoInstallScript); err != nil {
		r.add(Step{Name: "docker", Status: StepManual, Detail: fmt.Sprintf("install failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: "docker", Status: StepDone, Detail: "installed docker-ce from Docker's own apt repository"})
	return true
}

// goInstallScript is the exact command an operator with root can paste
// to install one Go release -- curl it, verify it against the sha256
// ensureGoFor already read off go.dev's own release index, and extract
// it -- rather than a paragraph describing the lookup and the digest
// comparison for them to write themselves. shQuote'd into a single `sudo
// sh -c` the same way dockerRepoInstallScript is.
func goInstallScript(tarballURL, filename, sha256Hex string) string {
	tmpFile := "/tmp/" + filename
	script := fmt.Sprintf(`set -eu
curl -fsSL -o %s %s
echo '%s  %s' | sha256sum -c -
rm -rf /usr/local/go
tar -C /usr/local -xzf %s
rm -f %s
echo 'export PATH="$PATH:/usr/local/go/bin"' > /etc/profile.d/grain-go.sh`,
		tmpFile, tarballURL, sha256Hex, tmpFile, tmpFile, tmpFile)
	return "sudo sh -c " + shQuote(script)
}

// ensureGo get-or-installs the Go toolchain go.mod asks for, verifying
// the tarball it downloads against go.dev's own release index rather
// than a digest frozen into this source tree. It returns whether a
// usable toolchain is now in place, and, when it just installed one
// itself, the PATH override later steps in this same run need to find it
// (this process's own PATH still lacks it; a new shell would pick it up
// from /etc/profile.d, but this run cannot wait for one).
func ensureGo(ctx context.Context, sys System, opts Options, r *Result) (ok bool, extraEnv []string) {
	return ensureGoFor(ctx, sys, opts, r, runtime.GOOS, runtime.GOARCH)
}

// ensureGoFor is ensureGo with goos/goarch taken explicitly rather than
// read from runtime.GOOS/GOARCH, so tests can pin the Linux host grain
// init always targets instead of inheriting whatever platform happens
// to be running `go test`.
func ensureGoFor(ctx context.Context, sys System, opts Options, r *Result, goos, goarch string) (ok bool, extraEnv []string) {
	const name = "go toolchain"
	want, err := goBuildVersion(opts.RepoRoot)
	if err != nil {
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf("cannot tell what version go.mod asks for: %v", err)})
		return false, nil
	}
	if out, err := sys.Run(ctx, "", nil, "go", "version"); err == nil {
		if got, ok := goVersionFromOutput(out); ok && goVersionAtLeast(got, want) {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("go%s already installed, satisfying go.mod's go%s", got, want)})
			return true, nil
		}
	}

	why := fmt.Sprintf("installing go%s (go.mod's own version) for %s/%s, verified against go.dev's own release index (%s) rather than a digest pinned in this source tree",
		want, goos, goarch, GoReleaseIndexURL)
	if opts.DryRun {
		// A dry run makes no HTTP calls at all (TestInitDryRunChangesNothing
		// pins this), so there is no real filename or sha256 to report yet
		// -- just the shape of what a real run would look up and run.
		cmd := fmt.Sprintf("download go%s for %s/%s from https://go.dev/dl/, verify its sha256 against %s, and extract it to /usr/local/go",
			want, goos, goarch, GoReleaseIndexURL)
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return false, nil
	}

	// Read the index -- and so learn the exact filename and sha256 --
	// regardless of whether this process can install anything itself: an
	// operator who has to run this by hand needs that lookup done for
	// them, not a description of how to do it, which is exactly the
	// shell archaeology this package exists to retire.
	index, err := sys.HTTPGet(ctx, GoReleaseIndexURL)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not read go.dev's release index: %v", err)})
		return false, nil
	}
	filename, wantSHA, err := goTarballSHA256(index, want, goos, goarch)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: err.Error()})
		return false, nil
	}
	tarballURL := "https://go.dev/dl/" + filename
	cmd := goInstallScript(tarballURL, filename, wantSHA)
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return false, nil
	}

	data, err := sys.HTTPGet(ctx, tarballURL)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not fetch %s: %v", tarballURL, err), Command: cmd})
		return false, nil
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		r.add(Step{Name: name, Status: StepRefused,
			Detail: fmt.Sprintf("%s's sha256 (%s) does not match go.dev's own release index (%s) -- refusing to install it", filename, got, wantSHA)})
		return false, nil
	}
	tmpFile := filepath.Join(os.TempDir(), filename)
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write %s: %v", tmpFile, err), Command: cmd})
		return false, nil
	}
	defer os.Remove(tmpFile)
	if _, err := sys.Run(ctx, "", nil, "rm", "-rf", "/usr/local/go"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not remove the previous /usr/local/go: %v", err), Command: cmd})
		return false, nil
	}
	if _, err := sys.Run(ctx, "", nil, "tar", "-C", "/usr/local", "-xzf", tmpFile); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not extract %s: %v", tmpFile, err), Command: cmd})
		return false, nil
	}
	// A future login shell picks this up on its own; this process's own
	// PATH does not change underneath it, so the rest of this run passes
	// the override explicitly to every step that execs go or make.
	if _, err := sys.Run(ctx, "", nil, "sh", "-c", `echo 'export PATH="$PATH:/usr/local/go/bin"' > /etc/profile.d/grain-go.sh`); err != nil {
		r.add(Step{Name: name, Status: StepDone,
			Detail: fmt.Sprintf("installed go%s to /usr/local/go (sha256 %s, verified against go.dev); could not write /etc/profile.d/grain-go.sh: %v -- add /usr/local/go/bin to PATH by hand",
				want, wantSHA, err)})
		return true, []string{"PATH=/usr/local/go/bin:" + os.Getenv("PATH")}
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("installed go%s to /usr/local/go, sha256 %s verified against go.dev's release index", want, wantSHA)})
	return true, []string{"PATH=/usr/local/go/bin:" + os.Getenv("PATH")}
}

// granuleCLICurrent reports whether path is newer than every file under
// third_party/granule -- the whole content a local replace directive
// build actually reads from. This is a Stat-based check rather than the
// commit comparison grainBinaryCurrent below does, because `go build`
// stamps no VCS information at all for a package resolved through a
// replace directive into a directory that is not itself a VCS checkout
// (confirmed by building it and reading `go version -m` back: no vcs.*
// key at all) -- there is no stamp here to compare a commit against.
func granuleCLICurrent(repoRoot, path string) bool {
	bin, err := os.Stat(path)
	if err != nil {
		return false
	}
	current := true
	walkErr := filepath.WalkDir(filepath.Join(repoRoot, "third_party", "granule"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(bin.ModTime()) {
			current = false
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return false
	}
	return current
}

// ensureGranuleCLI get-or-builds the granule CLI from third_party/granule
// through go.mod's own replace directive. The obvious alternative --
// cloning gke-labs/granule -- fails for a reason that reads like a
// permissions problem and is not one: this checkout vendors granule
// precisely so nothing here ever needs to reach it, and this never tries
// to.
func ensureGranuleCLI(ctx context.Context, sys System, opts Options, r *Result, goEnv []string) bool {
	const name = "build the granule CLI"
	if granuleCLICurrent(opts.RepoRoot, opts.GranuleBinaryPath) {
		r.add(Step{Name: name, Status: StepSkipped, Detail: opts.GranuleBinaryPath + " is already newer than every file under third_party/granule"})
		return true
	}
	why := "built from third_party/granule via go.mod's own replace directive -- never cloned from gke-labs/granule, which this checkout has no access to and does not need"
	cmd := fmt.Sprintf("go build -o %s github.com/bwsalmon/granule/cmd/granule", opts.GranuleBinaryPath)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if _, err := sys.Run(ctx, opts.RepoRoot, goEnv, "go", "build", "-o", opts.GranuleBinaryPath, "github.com/bwsalmon/granule/cmd/granule"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("build failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: opts.GranuleBinaryPath})
	return true
}

// ensureImage get-or-builds one docker image: a no-op if tag already
// exists, a StepWould on a dry run, and otherwise build() -- StepManual
// on failure, since a Granulefile build failing almost always means a
// missing prerequisite on this host rather than something a re-run
// alone fixes.
func ensureImage(ctx context.Context, sys System, opts Options, r *Result, name, tag string, build func() (string, error), cmd string) bool {
	if _, err := sys.Run(ctx, "", nil, "docker", "image", "inspect", tag); err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: tag + " already built"})
		return true
	}
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: "would build " + tag, Command: cmd})
		return false
	}
	if _, err := build(); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("build failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: "built " + tag})
	return true
}

// ensureImages builds the three images `granule granulefile build` packs
// a guest onto, strictly in the order they depend on each other: the
// granule runtime image first (every later -kontur-image names it),
// then Granulefile.guest-base, then Granulefile. A failure at any stage
// skips the ones after it rather than building atop an image that is not
// there.
func ensureImages(ctx context.Context, sys System, opts Options, r *Result) bool {
	runtimeCmd := fmt.Sprintf("IMAGE=%s ./scripts/granule/build-granule-image.sh", opts.GranuleRuntimeImage)
	runtimeOK := ensureImage(ctx, sys, opts, r, "granule runtime image", opts.GranuleRuntimeImage, func() (string, error) {
		return sys.Run(ctx, opts.RepoRoot, []string{"IMAGE=" + opts.GranuleRuntimeImage}, "./scripts/granule/build-granule-image.sh")
	}, runtimeCmd)

	if !runtimeOK {
		r.add(Step{Name: "guest-base image", Status: StepSkipped, Detail: "skipped: the granule runtime image did not build"})
		r.add(Step{Name: "guest image", Status: StepSkipped, Detail: "skipped: the granule runtime image did not build"})
		return false
	}

	baseCmd := fmt.Sprintf("%s granulefile build -kontur-image %s -f Granulefile.guest-base -t %s",
		opts.GranuleBinaryPath, opts.GranuleRuntimeImage, opts.GuestBaseImage)
	baseOK := ensureImage(ctx, sys, opts, r, "guest-base image", opts.GuestBaseImage, func() (string, error) {
		return sys.Run(ctx, opts.RepoRoot, nil, opts.GranuleBinaryPath, "granulefile", "build", "-kontur-image", opts.GranuleRuntimeImage,
			"-f", "Granulefile.guest-base", "-t", opts.GuestBaseImage)
	}, baseCmd)

	if !baseOK {
		r.add(Step{Name: "guest image", Status: StepSkipped, Detail: "skipped: the guest-base image did not build"})
		return false
	}

	guestCmd := fmt.Sprintf("%s granulefile build -kontur-image %s -f Granulefile -t %s",
		opts.GranuleBinaryPath, opts.GranuleRuntimeImage, opts.GuestImage)
	guestOK := ensureImage(ctx, sys, opts, r, "guest image", opts.GuestImage, func() (string, error) {
		return sys.Run(ctx, opts.RepoRoot, nil, opts.GranuleBinaryPath, "granulefile", "build", "-kontur-image", opts.GranuleRuntimeImage,
			"-f", "Granulefile", "-t", opts.GuestImage)
	}, guestCmd)

	return guestOK
}

// dockerNetworkGateway reports whether name exists, and if so its
// gateway out of `docker network inspect`. Existence is answered by
// `docker network ls --filter`, not by matching prose in an inspect
// error -- docker's own wording for "no such network" has changed
// across versions ("Error: No such network: NAME" on older docker,
// "Error response from daemon: network NAME not found" on newer), and
// anything matched against that text is one more docker release away
// from going stale the way it already has once. Once ls says the
// network exists, any error inspect gives is a real error a caller
// must not treat the same way.
func dockerNetworkGateway(ctx context.Context, sys System, name string) (gateway string, exists bool, err error) {
	out, err := sys.Run(ctx, "", nil, "docker", "network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(out) == "" {
		return "", false, nil
	}
	out, err = sys.Run(ctx, "", nil, "docker", "network", "inspect", name, "-f", "{{json .IPAM.Config}}")
	if err != nil {
		return "", true, err
	}
	var configs []struct {
		Gateway string `json:"Gateway"`
	}
	if err := json.Unmarshal([]byte(out), &configs); err != nil {
		return "", true, fmt.Errorf("parsing docker network inspect's IPAM config: %w", err)
	}
	if len(configs) == 0 {
		return "", true, nil
	}
	return configs[0].Gateway, true, nil
}

// gitProxyFlagFor names the flag ensureSandboxNetwork's gateway becomes
// once the daemon starts, which depends on platform: granule's own VM
// containers read -granule-git-proxy-host, while PlatformLinuxContainer's
// sandbox containers read -container-git-proxy-host. Named here so a
// refusal to guess that gateway (below) tells an operator which flag it
// was protecting them from guessing, not merely that some gateway could
// not be read.
func gitProxyFlagFor(platform Platform) string {
	if platform == PlatformLinuxContainer {
		return "-container-git-proxy-host"
	}
	return "-granule-git-proxy-host"
}

// ensureSandboxNetwork get-or-creates the docker network sandboxes are
// meant to join -- granule's own VM containers on PlatformLinuxKVM, or
// the sandbox containers themselves on PlatformLinuxContainer -- and
// returns its gateway: the address startGitProxy advertises to a
// sandbox in place of the loopback it binds to by default (a sandbox
// with a network namespace of its own has its own unrelated
// 127.0.0.1). It refuses -- StepRefused, no manual fallback -- rather
// than ever return a guessed address: a guessed git-proxy host produces
// a first clone that fails with "Couldn't connect to server" and
// nothing pointing at why. This step itself does not care which
// platform called it; only the flag name a caller reports the gateway
// under (gitProxyFlagFor) differs.
//
// A network is created here rather than this reading docker's own
// default "bridge" network (the way scripts/setup.sh's own
// ensure_granule_git_proxy_host does, with a container-probing fallback
// for the Debian docker.io daemons that leave bridge's own gateway
// unreported): a network this step creates is one docker always
// populates an IPAM gateway for, so there is no fallback to need.
func ensureSandboxNetwork(ctx context.Context, sys System, opts Options, r *Result) string {
	const name = "sandbox docker network"
	gitProxyFlag := gitProxyFlagFor(opts.Platform)
	gateway, exists, err := dockerNetworkGateway(ctx, sys, opts.SandboxNetworkName)
	if err != nil {
		r.add(Step{Name: name, Status: StepRefused,
			Detail: fmt.Sprintf("could not inspect docker network %q: %v -- refusing to guess %s", opts.SandboxNetworkName, err, gitProxyFlag)})
		return ""
	}
	created := false
	if !exists {
		why := fmt.Sprintf(
			"creating %q so this host's sandboxes join a network docker always reports a gateway for",
			opts.SandboxNetworkName)
		cmd := "docker network create " + opts.SandboxNetworkName
		if opts.DryRun {
			r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
			return ""
		}
		if _, err := sys.Run(ctx, "", nil, "docker", "network", "create", opts.SandboxNetworkName); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("create failed: %v", err), Command: cmd})
			return ""
		}
		created = true
		gateway, exists, err = dockerNetworkGateway(ctx, sys, opts.SandboxNetworkName)
		if err != nil || !exists {
			r.add(Step{Name: name, Status: StepRefused,
				Detail: fmt.Sprintf("created %q but could not read its gateway afterward: %v -- refusing to guess %s", opts.SandboxNetworkName, err, gitProxyFlag)})
			return ""
		}
	}
	if gateway == "" {
		r.add(Step{Name: name, Status: StepRefused,
			Detail: fmt.Sprintf("%q exists but docker reports no gateway for it -- refusing to guess %s; recreate it or pass one explicitly", opts.SandboxNetworkName, gitProxyFlag)})
		return ""
	}
	// StepDone only if this run actually created the network just
	// above; when it already existed, this step changed nothing -- it
	// only read the gateway docker had already assigned -- so it is
	// StepSkipped, the same status git/docker/etc. report for finding
	// their own work already done.
	status := StepSkipped
	if created {
		status = StepDone
	}
	r.add(Step{Name: name, Status: status, Detail: fmt.Sprintf("%s, gateway %s", opts.SandboxNetworkName, gateway)})
	return gateway
}

// grainBinaryCurrent reports whether path already carries the exact
// build opts.RepoRoot's checkout would produce right now: pkg/version's
// own build stamp (the commit `go build` embeds, and whether the tree
// it was built from was dirty), compared against the checkout's current
// HEAD -- rather than merely Stat-ing path, which could not tell a
// binary built from last month's HEAD apart from today's. A checkout
// with uncommitted changes right now, or a stamp recorded from one that
// had them at build time, both count as "no": neither is provably the
// build this checkout would produce.
func grainBinaryCurrent(ctx context.Context, sys System, repoRoot, path string) bool {
	info, err := version.ReadFile(path)
	if err != nil || info.Revision == "" || info.Modified {
		return false
	}
	head, err := sys.Run(ctx, repoRoot, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	status, err := sys.Run(ctx, repoRoot, nil, "git", "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return false
	}
	return info.Revision == strings.TrimSpace(head)
}

// describeGrainBinary names path along with whatever build stamp it
// carries, so a step reporting a prebuilt binary says which build the
// daemon is about to run.
func describeGrainBinary(path string) string {
	info, err := version.ReadFile(path)
	switch {
	case err != nil:
		return path
	case info.Release != "" && info.Revision != "":
		return fmt.Sprintf("%s (%s, %s)", path, info.Release, info.Revision)
	case info.Release != "":
		return fmt.Sprintf("%s (%s)", path, info.Release)
	case info.Revision != "":
		return fmt.Sprintf("%s (built from %s)", path, info.Revision)
	default:
		return path
	}
}

// ensureGrainBinary get-or-runs `make build`, which runs `make frontend`
// before building the grain binary -- pkg/ui/server.go go:embeds
// pkg/ui/static, and a binary built before the frontend exists serves
// placeholder.html forever. Run through the Makefile's own dependency
// rule rather than open-coding "frontend, then go build" again here, so
// the ordering cannot drift between the two.
const grainBinaryStepName = "build grain (frontend + binary)"

func ensureGrainBinary(ctx context.Context, sys System, opts Options, r *Result, goEnv []string) bool {
	const name = grainBinaryStepName
	builtPath := filepath.Join(opts.RepoRoot, "bin", "grain")
	if opts.UseBinary != "" {
		// preflight's checkPrebuiltGrain has already confirmed it is an
		// executable Go binary; this run never builds over it.
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: -use-binary " + describeGrainBinary(opts.UseBinary)})
		return true
	}
	if grainBinaryCurrent(ctx, sys, opts.RepoRoot, opts.GrainBinaryPath) {
		r.add(Step{Name: name, Status: StepSkipped, Detail: opts.GrainBinaryPath + " already built from this checkout's HEAD"})
		return true
	}
	why := "`make build` runs `make frontend` before building the binary that go:embeds it -- built first, the binary would only ever serve placeholder.html"
	cmd := "make build"
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if _, err := sys.Run(ctx, opts.RepoRoot, goEnv, "make", "build"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("build failed: %v", err), Command: cmd})
		return false
	}
	if opts.GrainBinaryPath != builtPath {
		data, err := os.ReadFile(builtPath)
		if err != nil {
			r.add(Step{Name: name, Status: StepManual,
				Detail:  fmt.Sprintf("built %s but could not read it to copy to %s: %v", builtPath, opts.GrainBinaryPath, err),
				Command: fmt.Sprintf("cp %s %s", builtPath, opts.GrainBinaryPath)})
			return false
		}
		if err := os.WriteFile(opts.GrainBinaryPath, data, 0o755); err != nil {
			r.add(Step{Name: name, Status: StepManual,
				Detail:  fmt.Sprintf("could not write %s: %v", opts.GrainBinaryPath, err),
				Command: fmt.Sprintf("cp %s %s", builtPath, opts.GrainBinaryPath)})
			return false
		}
	}
	r.add(Step{Name: name, Status: StepDone, Detail: opts.GrainBinaryPath})
	return true
}

// ensureSetcap grants the grain binary CAP_NET_BIND_SERVICE, without
// which the package cache cannot bind 443/80 and every sandbox silently
// falls back to its own resolver with no transparent caching. This is an
// attribute of the file, not the binary's identity, so it is re-checked
// (never assumed from a previous run) every time: a rebuild produces a
// new file with none of its predecessor's capabilities.
func ensureSetcap(ctx context.Context, sys System, opts Options, r *Result) {
	const name = "grant the grain binary CAP_NET_BIND_SERVICE"
	why := "without it the package cache cannot bind 443/80 and every sandbox falls back to its own resolver with no transparent caching -- this is an attribute of the file, so any rebuild drops it and this has to run again"
	cmd := fmt.Sprintf("sudo setcap cap_net_bind_service=+ep %s", opts.GrainBinaryPath)
	if out, err := sys.Run(ctx, "", nil, "getcap", opts.GrainBinaryPath); err == nil && strings.Contains(out, "cap_net_bind_service") {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "already granted"})
		return
	}
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return
	}
	if _, err := sys.Run(ctx, "", nil, "setcap", "cap_net_bind_service=+ep", opts.GrainBinaryPath); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("setcap failed: %v", err), Command: cmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: "granted"})
}

// ensureClaudeCLI installs the claude CLI the way the deployment
// Dockerfile does: fetched to a file, digest-checked against the pinned
// release, installed, and the installed copy digest-checked again --
// never `curl | bash`, whose exit status is its last command's, so a 403
// from the download would "succeed" and leave a deployment that fails at
// every dispatch asking for claude.
func ensureClaudeCLI(ctx context.Context, sys System, opts Options, r *Result) {
	const name = "install the claude CLI"
	if out, err := sys.Run(ctx, "", nil, "claude", "--version"); err == nil && strings.Contains(out, opts.ClaudeCLIVersion) {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "already installed: " + out})
		return
	}

	var platform, wantSHA string
	switch runtime.GOARCH {
	case "amd64":
		platform, wantSHA = "linux-x64", opts.ClaudeCLISHA256Amd64
	case "arm64":
		platform, wantSHA = "linux-arm64", opts.ClaudeCLISHA256Arm64
	default:
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf("no claude CLI release is pinned for %s", runtime.GOARCH)})
		return
	}
	url := fmt.Sprintf("https://downloads.claude.ai/claude-code-releases/%s/%s/claude", opts.ClaudeCLIVersion, platform)
	why := "fetched to a file and digest-checked before it is ever run -- never curl | bash, whose exit status is its last command's and would turn a 403 into a silent success"
	cmd := fmt.Sprintf("curl -fsSL %s -o /tmp/claude && printf '%%s  /tmp/claude\\n' %s | sha256sum -c - && chmod +x /tmp/claude && HOME=%s /tmp/claude install %s",
		url, wantSHA, opts.ClaudeHome, opts.ClaudeCLIVersion)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return
	}

	data, err := sys.HTTPGet(ctx, url)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not fetch %s: %v", url, err), Command: cmd})
		return
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		r.add(Step{Name: name, Status: StepRefused,
			Detail: fmt.Sprintf("%s's sha256 (%s) does not match the pinned digest (%s) -- refusing to install it", url, got, wantSHA)})
		return
	}
	tmpFile := filepath.Join(os.TempDir(), "grain-init-claude")
	if err := os.WriteFile(tmpFile, data, 0o755); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write %s: %v", tmpFile, err), Command: cmd})
		return
	}
	defer os.Remove(tmpFile)
	if err := os.MkdirAll(opts.ClaudeHome, 0o755); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not create %s: %v", opts.ClaudeHome, err), Command: cmd})
		return
	}
	if _, err := sys.Run(ctx, "", []string{"HOME=" + opts.ClaudeHome}, tmpFile, "install", opts.ClaudeCLIVersion); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("claude install failed: %v", err), Command: cmd})
		return
	}

	installedPath := filepath.Join(opts.ClaudeHome, ".local", "share", "claude", "versions", opts.ClaudeCLIVersion)
	installedData, err := os.ReadFile(installedPath)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual,
			Detail: fmt.Sprintf("installed, but could not re-read %s to verify its digest: %v", installedPath, err), Command: cmd})
		return
	}
	sum2 := sha256.Sum256(installedData)
	if got2 := hex.EncodeToString(sum2[:]); got2 != wantSHA {
		r.add(Step{Name: name, Status: StepRefused,
			Detail: fmt.Sprintf("the installed copy at %s has sha256 %s, not the pinned %s -- claude install fetched something of its own", installedPath, got2, wantSHA)})
		return
	}

	linkTarget := filepath.Join(opts.ClaudeHome, ".local", "bin", "claude")
	linkPath := "/usr/local/bin/claude"
	linkCmd := fmt.Sprintf("ln -sf %s %s", linkTarget, linkPath)
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual,
			Detail: fmt.Sprintf("installed and digest-verified at %s; linking it onto PATH needs root", opts.ClaudeHome), Command: linkCmd})
		return
	}
	if _, err := sys.Run(ctx, "", nil, "ln", "-sf", linkTarget, linkPath); err != nil {
		r.add(Step{Name: name, Status: StepManual,
			Detail: fmt.Sprintf("installed and digest-verified, but could not link it onto PATH: %v", err), Command: linkCmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("installed claude %s, digest-verified before and after install", opts.ClaudeCLIVersion)})
}

// wantsRootDaemon reports whether opts.DaemonUser names the literal
// "root" -- the explicit opt-out back to every run's only behavior
// before DaemonUser existed: no account created, no "docker" group
// membership, no chown, and no User=/Group= line in the unit at all.
// opts is assumed already defaulted (withDefaults), so DaemonUser is
// never empty by the time this is asked.
func wantsRootDaemon(opts Options) bool {
	return opts.DaemonUser == "root"
}

// ensureDaemonAccount get-or-creates the system account the daemon runs
// as (opts.DaemonUser/opts.DaemonGroup) -- a fixed account, created once
// with a system UID and no login shell, so re-running this on the same
// host always targets the same owner rather than reusing whoever
// happens to invoke grain init. Returns whether the account now exists
// (true for StepSkipped/StepDone, false for anything else) -- what
// ensureDaemonDockerGroup and ensureDataDirOwnership both gate on before
// naming an account that is not actually there yet.
//
// adopting (with adoptDesc, daemonAlreadyRunning's own description)
// reports this as unnecessary rather than as pending work: a daemon
// already running has whatever account it needs already, and
// opts.DaemonUser names the account a fresh daemon would run as, which
// is not necessarily -- see the bug this guards against -- the account
// the running one actually does.
func ensureDaemonAccount(ctx context.Context, sys System, opts Options, r *Result, adopting bool, adoptDesc string) bool {
	const name = "create the daemon account"
	if adopting {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"skipped: a daemon is already running (%s) -- grain init adopts it as-is instead of creating an account for a fresh one", adoptDesc)})
		return true
	}
	if wantsRootDaemon(opts) {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "-daemon-user=root opts out -- the daemon runs as root, which needs no account of its own"})
		return true
	}
	if _, err := sys.Run(ctx, "", nil, "id", "-u", opts.DaemonUser); err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("%s already exists", opts.DaemonUser)})
		return true
	}
	why := fmt.Sprintf("the daemon runs as %s, not root -- a fixed system account created once and reused across re-runs, rather than reusing whoever invoked grain init", opts.DaemonUser)
	sameGroup := opts.DaemonGroup == opts.DaemonUser
	var cmd string
	if sameGroup {
		cmd = fmt.Sprintf("sudo useradd --system --no-create-home --shell /usr/sbin/nologin --user-group %s", opts.DaemonUser)
	} else {
		cmd = fmt.Sprintf("sudo groupadd --force --system %s && sudo useradd --system --no-create-home --shell /usr/sbin/nologin --gid %s %s",
			opts.DaemonGroup, opts.DaemonGroup, opts.DaemonUser)
	}
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return false
	}
	if sameGroup {
		if _, err := sys.Run(ctx, "", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", opts.DaemonUser); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("useradd failed: %v", err), Command: cmd})
			return false
		}
	} else {
		if _, err := sys.Run(ctx, "", nil, "groupadd", "--force", "--system", opts.DaemonGroup); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("groupadd failed: %v", err), Command: cmd})
			return false
		}
		if _, err := sys.Run(ctx, "", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--gid", opts.DaemonGroup, opts.DaemonUser); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("useradd failed: %v", err), Command: cmd})
			return false
		}
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("created %s:%s", opts.DaemonUser, opts.DaemonGroup)})
	return true
}

// ensureDaemonDockerGroup get-or-adds opts.DaemonUser to the "docker"
// group -- the only way a non-root account reaches
// /var/run/docker.sock, which the daemon needs for every sandbox it
// starts. Skipped for root (needs no group membership at all), for an
// account that does not exist yet (accountOK false), while docker
// itself is not installed yet (dockerOK false -- there is no "docker"
// group to join until the package that creates it is in place), and
// while adopting an already-running daemon (adopting true -- see
// ensureDaemonAccount's own doc comment for why opts.DaemonUser names
// the wrong account to be adding to anything in that case).
func ensureDaemonDockerGroup(ctx context.Context, sys System, opts Options, r *Result, accountOK, dockerOK, adopting bool, adoptDesc string) {
	const name = "add the daemon account to the docker group"
	if adopting {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"skipped: a daemon is already running (%s) -- nothing to add to the docker group for a fresh account", adoptDesc)})
		return
	}
	if wantsRootDaemon(opts) {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "-daemon-user=root opts out -- root already reaches the docker socket"})
		return
	}
	if !accountOK {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: the daemon account does not exist yet -- see the step above"})
		return
	}
	if !dockerOK {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: docker is not installed yet -- there is no \"docker\" group to join"})
		return
	}
	if out, err := sys.Run(ctx, "", nil, "id", "-nG", opts.DaemonUser); err == nil && hasField(out, "docker") {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("%s already belongs to docker", opts.DaemonUser)})
		return
	}
	why := fmt.Sprintf("%s needs to belong to the \"docker\" group to reach /var/run/docker.sock", opts.DaemonUser)
	cmd := fmt.Sprintf("sudo usermod -aG docker %s", opts.DaemonUser)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return
	}
	if _, err := sys.Run(ctx, "", nil, "usermod", "-aG", "docker", opts.DaemonUser); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("usermod failed: %v", err), Command: cmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("added %s to docker", opts.DaemonUser)})
}

// hasField reports whether s, split on whitespace, contains field
// exactly -- used to check "id -nG <user>"'s space-separated group list
// for "docker" without a false match against, say, a group named
// "dockerish".
func hasField(s, field string) bool {
	for _, f := range strings.Fields(s) {
		if f == field {
			return true
		}
	}
	return false
}

// isWithin reports whether target is base itself or lives under it --
// used to skip chowning ClaudeHome on its own when it is the default
// <DataDir>/claude, already covered by chowning DataDir recursively.
// Resolved through safepath.Within rather than a lexical prefix check,
// so a symlink cannot make this say "already covered" about a path that
// is not: the daemon's own chown of -data-dir reaching outside it would
// chown whatever the link points at, recursively, as root.
func isWithin(base, target string) bool {
	_, err := safepath.Within(base, target)
	return err == nil
}

// ensureOwnership get-or-chowns path (recursively) to
// opts.DaemonUser:opts.DaemonGroup, reporting stepName. A path that does
// not exist yet (ClaudeHome, when -install-claude-cli=false never
// created it) is reported StepSkipped rather than attempted: there is
// nothing there for the daemon to need access to yet, and chown would
// only fail on it.
func ensureOwnership(ctx context.Context, sys System, opts Options, r *Result, stepName, path string) bool {
	owner := opts.DaemonUser + ":" + opts.DaemonGroup
	if _, err := os.Stat(path); err != nil {
		r.add(Step{Name: stepName, Status: StepSkipped, Detail: fmt.Sprintf("%s does not exist yet", path)})
		return true
	}
	if out, err := sys.Run(ctx, "", nil, "stat", "-c", "%U:%G", path); err == nil && strings.TrimSpace(out) == owner {
		r.add(Step{Name: stepName, Status: StepSkipped, Detail: fmt.Sprintf("%s is already owned by %s", path, owner)})
		return true
	}
	why := fmt.Sprintf("the daemon runs as %s and needs to read and write everything under %s", owner, path)
	cmd := fmt.Sprintf("sudo chown -R %s %s", owner, path)
	if opts.DryRun {
		r.add(Step{Name: stepName, Status: StepWould, Detail: why, Command: cmd})
		return false
	}
	if !sys.IsRoot() {
		r.add(Step{Name: stepName, Status: StepManual, Detail: why, Command: cmd})
		return false
	}
	if _, err := sys.Run(ctx, "", nil, "chown", "-R", owner, path); err != nil {
		r.add(Step{Name: stepName, Status: StepManual, Detail: fmt.Sprintf("chown failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: stepName, Status: StepDone, Detail: fmt.Sprintf("chown -R %s %s", owner, path)})
	return true
}

// ensureDataDirOwnership chowns DataDir (and ClaudeHome, if it lives
// outside DataDir -- the default puts it inside, already covered by the
// same recursive chown) to the daemon's own account, before the daemon
// -- running as that account from here on -- ever has to read or write
// either tree. This is also this package's whole answer to upgrading an
// existing deployment some earlier version of it brought up as root:
// re-running grain init against one chowns its entire data directory to
// the new account on the same terms as a fresh host, idempotently,
// whether or not the daemon this run finds already running is the one
// that gets restarted under the new account (see ensureDaemon's own
// refusal to start a second daemon beside one already running --
// finishing that migration on a host whose daemon is still active
// needs `systemctl stop grain-daemon.service` first, same as any other
// unit-rewriting change).
func ensureDataDirOwnership(ctx context.Context, sys System, opts Options, r *Result, accountOK, adopting bool, adoptDesc string) bool {
	const dataDirStep = "give the daemon account ownership of -data-dir"
	const claudeHomeStep = "give the daemon account ownership of -claude-home"
	if adopting {
		r.add(Step{Name: dataDirStep, Status: StepSkipped, Detail: fmt.Sprintf(
			"skipped: a daemon is already running (%s) -- nothing to chown for a fresh account", adoptDesc)})
		return true
	}
	if wantsRootDaemon(opts) {
		r.add(Step{Name: dataDirStep, Status: StepSkipped, Detail: "-daemon-user=root opts out -- root already owns everything"})
		return true
	}
	if !accountOK {
		r.add(Step{Name: dataDirStep, Status: StepSkipped, Detail: "skipped: the daemon account does not exist yet -- see the step above"})
		return false
	}
	if !ensureDataDirExists(sys, opts, r) {
		return false
	}
	ok := ensureOwnership(ctx, sys, opts, r, dataDirStep, opts.DataDir)
	if !isWithin(opts.DataDir, opts.ClaudeHome) {
		ok = ensureOwnership(ctx, sys, opts, r, claudeHomeStep, opts.ClaudeHome) && ok
	}
	return ok
}

// ensureDataDirExists creates DataDir (0700, as the daemon itself would)
// when nothing earlier did, so ensureOwnership has a directory to hand
// the daemon account. Left missing, the daemon -- running as that
// account, under a parent only root may write to (/var/lib) -- cannot
// create it and exits on every restart. With the claude CLI installed,
// ensureClaudeCLI creates it as a side effect of filling ClaudeHome;
// -install-claude-cli=false was a fresh host's crash loop.
func ensureDataDirExists(sys System, opts Options, r *Result) bool {
	const name = "create -data-dir"
	if _, err := os.Stat(opts.DataDir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return true
	}
	cmd := fmt.Sprintf("sudo install -d -m 0700 %s", shQuote(opts.DataDir))
	why := "the daemon account cannot create it under a parent only root may write to"
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return true
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return false
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("mkdir failed: %v", err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: opts.DataDir})
	return true
}

// cliEnvPath is the file the grain CLI reads for where this host's
// daemon listens and keeps its data (pkg/cli/cliconn.go's
// defaultCLIEnvFile); scripts/setup.sh writes the same file
// (write_cli_env). A var so a test can redirect it, like systemdUnitPath.
var cliEnvPath = "/etc/grain/cli.env"

// cliEnvContent is cliEnvPath's text for a daemon at uiAddr (as the
// steps after the daemon starts dial it) with dataDir: the same two
// keys, in the same KEY=VALUE form, that setup.sh writes.
func cliEnvContent(uiAddr, dataDir string) string {
	return "# Written by grain init: where this host's grain daemon listens, and its data\n" +
		"# directory. Read by the grain CLI as data when GRAIN_SERVER / GRAIN_DATA_DIR\n" +
		"# are not set; -server and -data-dir still win over both.\n" +
		"GRAIN_SERVER=http://" + loopbackDialAddr(uiAddr) + "\n" +
		"GRAIN_DATA_DIR=" + dataDir + "\n"
}

// ensureCLIEnv get-or-writes cliEnvPath, so the grain CLI in any shell
// on this host reaches the daemon grain init settled on. Without it the
// CLI falls back to its built-in http://127.0.0.1:4242, which misses a
// daemon a re-run kept on LegacyUIAddr (or any other -ui-addr) and one
// whose -data-dir holds the local API token somewhere else.
func ensureCLIEnv(sys System, opts Options, r *Result) {
	const name = "write the grain CLI's connection file"
	want := cliEnvContent(opts.UIAddr, opts.DataDir)
	if have, err := os.ReadFile(cliEnvPath); err == nil && string(have) == want {
		r.add(Step{Name: name, Status: StepSkipped, Detail: cliEnvPath + " already names http://" + loopbackDialAddr(opts.UIAddr)})
		return
	}
	why := "the grain CLI on this host reads it, in any shell, for http://" + loopbackDialAddr(opts.UIAddr) + " and " + opts.DataDir
	cmd := fmt.Sprintf("sudo install -d -m 0755 %s && printf '%%s' %s | sudo tee %s >/dev/null",
		shQuote(filepath.Dir(cliEnvPath)), shQuote(want), shQuote(cliEnvPath))
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return
	}
	if err := os.MkdirAll(filepath.Dir(cliEnvPath), 0o755); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not create %s: %v", filepath.Dir(cliEnvPath), err), Command: cmd})
		return
	}
	if err := os.WriteFile(cliEnvPath, []byte(want), 0o644); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write %s: %v", cliEnvPath, err), Command: cmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: cliEnvPath + ": " + why})
}

// ensureCredentialLadder writes GitHubToken as GitHubCredentialName's
// material (secrets/github/<name>.token) and records a git connection
// naming it for every repository pattern in GitHubRepos, through
// `grain connections import` -- the one way an install seeds connections
// (docs/single-tenant-reset.md, stage 2). Never a host default, which
// Options.validate already refused outright: the point is narrowing a
// wide token to the repos it actually covers.
func ensureCredentialLadder(ctx context.Context, sys System, opts Options, r *Result) {
	const name = "record the GitHub git connections"
	if opts.GitHubToken == "" {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: "no token given -- configure one later via the UI's Settings pane or `grain connections import`"})
		return
	}
	secretsDir := filepath.Join(opts.DataDir, "secrets", "github")
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would write %s.token under %s and import git connections for %s",
				opts.GitHubCredentialName, secretsDir, strings.Join(opts.GitHubRepos, ", "))})
		return
	}
	cs, err := gitproxy.LoadCredentialSet(secretsDir)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not load the GitHub credentials: %v", err)})
		return
	}
	if err := cs.SetToken(opts.GitHubCredentialName, opts.GitHubToken); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write the token: %v", err)})
		return
	}
	seed, err := os.OpenFile(filepath.Join(secretsDir, ".connections-seed.yaml"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write the connections seed file: %v", err)})
		return
	}
	defer os.Remove(seed.Name())
	var b strings.Builder
	b.WriteString("git:\n")
	for _, pattern := range opts.GitHubRepos {
		fmt.Fprintf(&b, "  - credential: %q\n    scope: %q\n", opts.GitHubCredentialName, pattern)
	}
	_, werr := seed.WriteString(b.String())
	if cerr := seed.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write the connections seed file: %v", werr)})
		return
	}
	args := []string{"connections", "-data-dir", opts.DataDir, "import", seed.Name()}
	if out, err := sys.Run(ctx, "", nil, opts.GrainBinaryPath, args...); err != nil {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("wrote the token but could not import its git connections: %v: %s", err, strings.TrimSpace(out)),
			Command: opts.GrainBinaryPath + " " + strings.Join(args[:len(args)-1], " ") + " <seed file>"})
		return
	}
	r.add(Step{Name: name, Status: StepDone,
		Detail: fmt.Sprintf("%s.token, connected to %s -- no host default", opts.GitHubCredentialName, strings.Join(opts.GitHubRepos, ", "))})
}

// daemonAlreadyRunning reports whether a grain daemon already appears to
// be running, by every means this package knows to check. It only ever
// says "running" on positive evidence; it never reports a check failure
// as proof of anything, so a host with no systemd (or no pgrep) does not
// make ensureDaemon falsely refuse a legitimate first start. pid is the
// daemon's own process id when either check can name one -- "" when the
// unit is active but its MainPID could not be read, never a reason to
// fail this check -- what adoptRunningDaemon reads /proc/<pid>/cmdline
// and /proc/<pid>/status from when no grain-daemon.service unit file is
// already on disk to read instead.
func daemonAlreadyRunning(ctx context.Context, sys System) (running bool, desc string, pid string) {
	if out, err := sys.Run(ctx, "", nil, "systemctl", "is-active", "grain-daemon.service"); err == nil && strings.TrimSpace(out) == "active" {
		mainPID := ""
		if out2, err2 := sys.Run(ctx, "", nil, "systemctl", "show", "-p", "MainPID", "--value", "grain-daemon.service"); err2 == nil {
			mainPID = strings.TrimSpace(out2)
		}
		return true, "systemd unit grain-daemon.service is active", mainPID
	}
	if out, err := sys.Run(ctx, "", nil, "pgrep", "-f", "grain daemon"); err == nil && strings.TrimSpace(out) != "" {
		if p := pickDaemonPID(strings.Fields(out)); p != "" {
			return true, fmt.Sprintf("a process matching \"grain daemon\" is already running (pid %s)", p), p
		}
	}
	return false, "", ""
}

// pickDaemonPID chooses, among the pids `pgrep -f "grain daemon"`
// matched, the one that is the daemon itself rather than something whose
// command line merely mentions it: `sudo -u grain grain daemon …` is two
// processes, sudo first, and adopting sudo wrote a unit whose ExecStart
// was "sudo -u grain …" and whose User= was whoever ran sudo. A pid whose
// cmdline cannot be read is kept as the fallback, so that case still
// reaches adoptRunningDaemon's own refusal instead of being taken as
// "nothing running"; a match that is readable and plainly not a daemon
// (a sudo or sh -c wrapper, an editor) is dropped.
//
// So is a daemon in another PID namespace: a container's (a kind node's
// grain pod, a docker-run deployment), which pgrep sees from the host
// but no unit here manages. On the 10-05 walkthrough one kept a host
// re-run, after `systemctl stop grain-daemon`, from chowning -data-dir
// or starting the unit at all. A namespace this process cannot read
// (another user's pid without root) is no evidence, and is not dropped.
func pickDaemonPID(pids []string) string {
	own, ownErr := readPIDNamespace("self")
	unreadable := ""
	for _, p := range pids {
		if ns, err := readPIDNamespace(p); ownErr == nil && err == nil && ns != own {
			continue
		}
		raw, err := readProcCmdline(p)
		if err != nil {
			if unreadable == "" {
				unreadable = p
			}
			continue
		}
		if argv := splitProcCmdline(raw); len(argv) >= 2 && argv[1] == "daemon" && strings.HasPrefix(filepath.Base(argv[0]), "grain") {
			return p
		}
	}
	return unreadable
}

// ownUnitRunning reports whether the daemon already running is
// grain-daemon.service as this run would have installed it: the unit is
// active and the file at systemdUnitPath is byte-for-byte daemonUnit's
// rendering for these same options and gateway. Anything else -- a
// stray `grain daemon` process, or a unit someone edited or an earlier
// run wrote with different flags -- is not this command's to adopt.
func ownUnitRunning(ctx context.Context, sys System, opts Options, gateway string) bool {
	out, err := sys.Run(ctx, "", nil, "systemctl", "is-active", "grain-daemon.service")
	if err != nil || strings.TrimSpace(out) != "active" {
		return false
	}
	installed, err := os.ReadFile(systemdUnitPath)
	if err != nil {
		return false
	}
	if string(installed) == daemonUnit(opts, gateway) {
		return true
	}
	// An earlier init's rewrite of that same unit, with -as <the stored
	// admin> appended (withActorArg), is still this command's own.
	if _, chosen := daemonActorArg(opts.ExtraDaemonArgs); chosen || wantsRootDaemon(opts) {
		return false
	}
	execStart, _, _, err := parseUnitFile(string(installed))
	fields := strings.Fields(execStart)
	if err != nil || len(fields) < 2 || fields[len(fields)-2] != "-as" {
		return false
	}
	return string(installed) == daemonUnit(withActorArg(opts, fields[len(fields)-1]), gateway)
}

// grainRebuiltThisRun reports whether ensureGrainBinary replaced the
// grain binary earlier in this same run, which a daemon already running
// it would not pick up until restarted.
func grainRebuiltThisRun(r *Result) bool {
	for _, s := range r.Steps {
		if s.Name == grainBinaryStepName && s.Status == StepDone {
			return true
		}
	}
	return false
}

// daemonUnit renders the systemd unit ensureDaemon installs: a plain
// `ExecStart` of the binary this run just built, with the flags naming
// its sandbox backend -- PlatformLinuxKVM's granule VM flags, pointed
// at the guest image this run just built and joined to the sandbox
// network via granule's own -docker-run-opt passthrough, or
// PlatformLinuxContainer's -container-sandboxes, pointed at that same
// kind of network directly since a plain container needs no per-VM
// create-arg to join one. Both name the sandbox network's own gateway
// -- read once, by ensureSandboxNetwork, before either platform's
// daemon ever starts -- under the flag their own backend reads it from.
// Carries User=/Group= unless opts.DaemonUser is the literal "root",
// the explicit opt-out back to every unit this package wrote before
// DaemonUser defaulted to DefaultDaemonUser -- no line at all, so
// systemd's own root default stands exactly as it did then.
func daemonUnit(opts Options, gateway string) string {
	var args []string
	switch opts.Platform {
	case PlatformLinuxContainer:
		args = []string{
			"daemon",
			"-data-dir", opts.DataDir,
			"-container-sandboxes",
			"-container-sandbox-network", opts.SandboxNetworkName,
			"-container-git-proxy-host", gateway,
		}
	default: // PlatformLinuxKVM
		args = []string{
			"daemon",
			"-data-dir", opts.DataDir,
			"-granule-sandboxes",
			"-granule-ssh-user", opts.GranuleSSHUser,
			"-granule-workspace", opts.GranuleWorkspace,
			"-granule-git-proxy-host", gateway,
			"-granule-create-arg", "-disk-mode=overlay",
			"-granule-create-arg", "-guest-user", "-granule-create-arg", opts.GranuleSSHUser,
			"-granule-create-arg", "-docker-run-opt", "-granule-create-arg", "--network=" + opts.SandboxNetworkName,
			"-sandbox-image", opts.GuestImage,
		}
	}
	if len(opts.UpgradeAllowedBranches) > 0 {
		args = append(args, "-upgrade-allowed-branches", strings.Join(opts.UpgradeAllowedBranches, ","))
	}
	args = append(args, opts.ExtraDaemonArgs...)
	execStart := opts.GrainBinaryPath + " " + strings.Join(args, " ")
	var runAs string
	if !wantsRootDaemon(opts) {
		runAs += "User=" + opts.DaemonUser + "\n"
		runAs += "Group=" + opts.DaemonGroup + "\n"
	}
	return renderUnit(runAs, execStart)
}

// renderUnit is grain-daemon.service's own template, shared by daemonUnit
// (a unit built from this run's own flags, for a fresh start) and
// renderAdoptedUnit (a unit built from what a daemon this run did not
// start is actually running, for adoptRunningDaemon) -- the two differ
// only in where runAs and execStart come from, never in the unit's own
// shape.
func renderUnit(runAs, execStart string) string {
	return fmt.Sprintf(`[Unit]
Description=grain daemon (task orchestrator, UI and API)
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
Environment=GRAIN_INSTALL_MODE=host
%sExecStart=%s
Restart=on-failure
RestartSec=5
OOMScoreAdjust=-900

[Install]
WantedBy=multi-user.target
`, runAs, execStart)
}

// systemdUnitPath is where ensureDaemon writes grain-daemon.service --
// a var rather than a constant so a test can redirect it into a
// temporary directory instead of actually writing to /etc on whatever
// machine runs the test suite.
var systemdUnitPath = "/etc/systemd/system/grain-daemon.service"

// readProcCmdline and readProcStatus read /proc/<pid>/cmdline and
// /proc/<pid>/status -- vars, not direct os.ReadFile calls, so a test
// can fake a pid's own cmdline/status without this package needing a
// real /proc entry (or a real other-user process) to test against.
var (
	readProcCmdline = func(pid string) ([]byte, error) { return os.ReadFile(filepath.Join("/proc", pid, "cmdline")) }
	readProcStatus  = func(pid string) ([]byte, error) { return os.ReadFile(filepath.Join("/proc", pid, "status")) }
	// readPIDNamespace is /proc/<pid>/ns/pid's link target, "self" for
	// this process's own.
	readPIDNamespace = func(pid string) (string, error) { return os.Readlink(filepath.Join("/proc", pid, "ns", "pid")) }
)

// lookupUserByUID and lookupGroupByGID resolve a numeric uid/gid (read
// off /proc/<pid>/status) to the account/group name adoptRunningDaemon
// carries into the unit it writes -- vars, not direct os/user calls, so
// a test can adopt a pid "owned" by some account other than whichever
// uids this sandbox actually has.
var (
	lookupUserByUID = func(uid string) (string, error) {
		u, err := user.LookupId(uid)
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	lookupGroupByGID = func(gid string) (string, error) {
		g, err := user.LookupGroupId(gid)
		if err != nil {
			return "", err
		}
		return g.Name, nil
	}
)

// adoptedDaemon is what adoptRunningDaemon read back about a daemon this
// run did not itself start: Args is ExecStart's own tokens after the
// binary itself (argv[1:]), split on whitespace the same ungarbled way
// daemonUnit's own ExecStart is ever written -- good enough to find
// -data-dir and the sandbox-shape flag by name, not a real shell
// tokenizer, because nothing this package has ever written to
// ExecStart needed one.
type adoptedDaemon struct {
	ExecStart         string
	Args              []string
	User              string
	Group             string
	Source            string
	UnitAlreadyExists bool
}

// parseUnitFile reads ExecStart=/User=/Group= back out of an existing
// grain-daemon.service -- the first place inspectRunningDaemon looks,
// since a unit already on disk is a more direct answer than re-deriving
// the same thing from /proc.
func parseUnitFile(data string) (execStart, user, group string, err error) {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			execStart = strings.TrimPrefix(line, "ExecStart=")
		case strings.HasPrefix(line, "User="):
			user = strings.TrimPrefix(line, "User=")
		case strings.HasPrefix(line, "Group="):
			group = strings.TrimPrefix(line, "Group=")
		}
	}
	if execStart == "" {
		return "", "", "", fmt.Errorf("no ExecStart= line found")
	}
	return execStart, user, group, nil
}

// splitProcCmdline splits /proc/<pid>/cmdline's own NUL-separated,
// NUL-terminated argv back into strings.
func splitProcCmdline(b []byte) []string {
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// processOwner reads uid/gid off /proc/<pid>/status and resolves them
// to names. uid 0 (root) is reported as "", "" -- the same "no
// User=/Group= line at all" convention wantsRootDaemon/daemonUnit
// already use for -daemon-user=root, so a root-owned daemon this run
// adopts gets exactly the unit daemonUnit itself would have written for
// one.
func processOwner(pid string) (username, group string, err error) {
	data, err := readProcStatus(pid)
	if err != nil {
		return "", "", err
	}
	var uid, gid string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Uid:":
			uid = fields[1]
		case "Gid:":
			gid = fields[1]
		}
	}
	if uid == "" {
		return "", "", fmt.Errorf("no \"Uid:\" line in /proc/%s/status", pid)
	}
	if uid == "0" {
		return "", "", nil
	}
	username, err = lookupUserByUID(uid)
	if err != nil {
		return "", "", fmt.Errorf("looking up uid %s: %w", uid, err)
	}
	if gid != "" {
		if g, gerr := lookupGroupByGID(gid); gerr == nil {
			group = g
		}
	}
	return username, group, nil
}

// inspectRunningDaemon is grain init's one way of reading what a daemon
// it did not itself start is actually running, per this task's own
// design: an existing grain-daemon.service unit file if one is already
// there (a more direct answer than re-deriving the same thing from
// /proc, and proof there is nothing left to adopt), or
// /proc/<pid>/cmdline and that process's own owner otherwise. Only
// reads; never guesses -- an error here is adoptRunningDaemon's own
// StepRefused, never a reason to fall back to grain init's own flag
// defaults instead.
func inspectRunningDaemon(pid string) (adoptedDaemon, error) {
	if data, err := os.ReadFile(systemdUnitPath); err == nil {
		execStart, unitUser, unitGroup, perr := parseUnitFile(string(data))
		if perr != nil {
			return adoptedDaemon{}, fmt.Errorf("parsing the existing %s: %w", systemdUnitPath, perr)
		}
		return adoptedDaemon{
			ExecStart:         execStart,
			Args:              strings.Fields(execStart)[1:],
			User:              unitUser,
			Group:             unitGroup,
			Source:            fmt.Sprintf("read from the existing %s", systemdUnitPath),
			UnitAlreadyExists: true,
		}, nil
	}
	if pid == "" {
		return adoptedDaemon{}, fmt.Errorf("no %s exists yet, and no pid was found to read /proc/<pid>/cmdline from", systemdUnitPath)
	}
	raw, err := readProcCmdline(pid)
	if err != nil {
		return adoptedDaemon{}, fmt.Errorf("reading /proc/%s/cmdline: %w", pid, err)
	}
	argv := splitProcCmdline(raw)
	if len(argv) == 0 {
		return adoptedDaemon{}, fmt.Errorf("/proc/%s/cmdline was empty", pid)
	}
	username, group, err := processOwner(pid)
	if err != nil {
		return adoptedDaemon{}, fmt.Errorf("reading the owner of pid %s: %w", pid, err)
	}
	return adoptedDaemon{
		ExecStart: strings.Join(argv, " "),
		Args:      argv[1:],
		User:      username,
		Group:     group,
		Source:    fmt.Sprintf("read from /proc/%s/cmdline", pid),
	}, nil
}

// argvFlagValue finds flag's value in args, in either "-flag value" or
// "-flag=value" form -- the only two forms anything in this package has
// ever written to a daemon's own ExecStart.
func argvFlagValue(args []string, flag string) (string, bool) {
	eq := "-" + flag + "="
	for i, a := range args {
		if strings.HasPrefix(a, eq) {
			return strings.TrimPrefix(a, eq), true
		}
		if a == "-"+flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func argvContains(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// adoptConflict reports why opts disagrees with info, or "" if it does
// not -- per this task's own item 4, checked against exactly the two
// things this run cannot adopt a different answer for without acting on
// a guess rather than what is actually running: -data-dir (always
// explicit; the flag has no default and cmdInit refuses to run without
// it) and which sandbox backend -platform selects (-granule-sandboxes
// vs. -container-sandboxes) -- never any of the other flags daemonUnit
// would otherwise fill in from opts' own defaults (-ui-addr, sandbox
// sizing, and so on), since those are exactly the silent-override
// grain-sbx-probe's own incident was, and adopting the daemon's real
// values for them is the entire point of this mode rather than
// something to refuse over.
func adoptConflict(opts Options, info adoptedDaemon) string {
	if v, ok := argvFlagValue(info.Args, "data-dir"); ok && v != opts.DataDir {
		return fmt.Sprintf("its -data-dir is %q, not this run's %q", v, opts.DataDir)
	}
	switch opts.Platform {
	case PlatformLinuxContainer:
		if argvContains(info.Args, "-granule-sandboxes") {
			return "it was started with -granule-sandboxes, but this run is -platform=" + string(PlatformLinuxContainer)
		}
	default: // PlatformLinuxKVM
		if argvContains(info.Args, "-container-sandboxes") {
			return "it was started with -container-sandboxes, but this run is -platform=" + string(PlatformLinuxKVM)
		}
	}
	return ""
}

// renderAdoptedUnit is daemonUnit's own counterpart for adoptRunningDaemon:
// the same template (renderUnit), with ExecStart and User=/Group= taken
// verbatim from what inspectRunningDaemon actually read, never from
// opts' own flags.
func renderAdoptedUnit(info adoptedDaemon) string {
	var runAs string
	if info.User != "" {
		runAs += "User=" + info.User + "\n"
	}
	if info.Group != "" {
		runAs += "Group=" + info.Group + "\n"
	}
	return renderUnit(runAs, info.ExecStart)
}

// adoptRunningDaemon is what ensureDaemon calls once daemonAlreadyRunning
// has found a daemon this run did not start -- grain init's adopt mode.
// Rather than the flat refusal this package made before adopt mode could
// tell the two cases apart, it reads back what is actually running
// (inspectRunningDaemon) and either:
//
//   - refuses (StepRefused) if it cannot read that back at all, or if
//     opts disagrees with it on the two things adoptConflict checks --
//     never a silent override of what is actually running;
//   - reports there is nothing to do (StepSkipped) if a
//     grain-daemon.service unit already exists and already matches, so
//     a repeated `grain init` against an already-adopted daemon is the
//     same no-op get-or-create discipline every other step in this
//     package holds itself to;
//   - otherwise writes a unit matching the running process exactly and
//     enables it (StepDone, or StepManual/StepWould on the same terms
//     ensureDaemon's own fresh-start path already uses) -- `enable`,
//     deliberately never `enable --now`, since the process this adopts
//     is already running and starting a second copy of it against the
//     same -data-dir and -ui-addr is exactly the hazard ensureDaemon has
//     always refused outright; the unit only takes effect the next time
//     this host actually restarts.
//
// Always returns true for its bool: whichever of the above happens, the
// daemon daemonAlreadyRunning found was presumably already answering
// before this run ever started. The *adoptedDaemon return is what
// inspectRunningDaemon actually read back -- nil only when it could not
// be read at all (the StepRefused case) -- so initFor can point
// -ui-addr at this daemon's own real configuration instead of this run's flag defaults, which were
// never asked about a process it did not start.
func adoptRunningDaemon(ctx context.Context, sys System, opts Options, r *Result, name, desc, pid string) (bool, *adoptedDaemon) {
	info, err := inspectRunningDaemon(pid)
	if err != nil {
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
			"a daemon is already running (%s) but its actual configuration could not be read to adopt it: %v -- "+
				"stop it first if you mean to replace it under grain init's own management, or install a systemd unit for it by hand",
			desc, err)})
		return true, nil
	}
	if conflict := adoptConflict(opts, info); conflict != "" {
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
			"a daemon is already running (%s, %s), and %s -- refusing to guess which is right; stop the running daemon first, "+
				"or re-run with flags that match what is actually running",
			desc, info.Source, conflict)})
		return true, nil
	}
	if info.UnitAlreadyExists {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"a daemon is already running (%s), already managed by %s -- nothing to adopt", desc, systemdUnitPath)})
		return true, &info
	}

	unit := renderAdoptedUnit(info)
	unitPath := systemdUnitPath
	why := fmt.Sprintf("adopts the already-running daemon (%s, %s): writes %s matching its actual command line, "+
		"enabled for next boot without restarting the process", desc, info.Source, unitPath)
	cmd := fmt.Sprintf("write the unit below to %s, then: systemctl daemon-reload && systemctl enable grain-daemon.service\n\n%s", unitPath, unit)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return true, &info
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return true, &info
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write %s: %v", unitPath, err), Command: cmd})
		return true, &info
	}
	if _, err := sys.Run(ctx, "", nil, "systemctl", "daemon-reload"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("daemon-reload failed: %v", err), Command: cmd})
		return true, &info
	}
	// enable, never enable --now: the process this adopts is already
	// running, so starting it again would mean two daemons fighting
	// over the same -data-dir and -ui-addr -- exactly what ensureDaemon
	// has always refused to do outright.
	if _, err := sys.Run(ctx, "", nil, "systemctl", "enable", "grain-daemon.service"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("enable failed: %v", err), Command: cmd})
		return true, &info
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf(
		"adopted the already-running daemon (%s): wrote %s matching its actual command line (%s) and enabled it -- "+
			"the running process itself was left alone", desc, unitPath, info.Source)})
	return true, &info
}

// ensureDaemon hands off to adoptRunningDaemon (grain init's adopt mode)
// the instant daemonAlreadyRunning finds a daemon this run did not
// start, rather than the flat refusal this package made before adopt
// mode existed -- see adoptRunningDaemon's own doc comment for what
// happens from there. Otherwise it writes and starts grain-daemon.service
// if it can (root), or hands an operator the unit to install and the
// commands to start it if it cannot.
//
// Its bool return is whether a daemon should now be reachable at
// opts.UIAddr -- true for StepDone, for this command's own unit found
// already running (skipped, or flagged for a restart), and for every
// path through adoptRunningDaemon (that daemon was presumably already
// answering before this run ever started), false for everything else
// (StepManual, StepWould), which is what ensureDaemonReachable uses to
// decide whether there is anything worth polling for. The *adoptedDaemon
// return is adoptRunningDaemon's own return, passed straight through --
// nil whenever this call took the fresh-start or own-unit path, since
// there is nothing to adopt an opts.UIAddr from that this run's own options do not already say.
func ensureDaemon(ctx context.Context, sys System, opts Options, r *Result, gateway string) (bool, *adoptedDaemon) {
	const name = "start the daemon"
	// An upgrade allowlist this run was not given is the one the unit on
	// disk already passes, so rewriting the unit -- or deciding it needs
	// no rewriting -- never drops one an operator set (director task 1913).
	kept, keptErr := keptUpgradeAllowlist(opts)
	if keptErr == nil && len(kept) > 0 {
		opts.UpgradeAllowedBranches = kept
	}
	opts.ExtraDaemonArgs = append(opts.ExtraDaemonArgs, opts.uiDaemonArgs...)
	if running, desc, pid := daemonAlreadyRunning(ctx, sys); running {
		// The one daemon already running that needs no adopting is the
		// one an earlier run of this same command installed: its unit is
		// active and says exactly what this run would have written.
		// Treating it as anything else made every re-run -- the normal
		// path, per docs/grain-init.md -- report steps remaining.
		if ownUnitRunning(ctx, sys, opts, gateway) {
			if grainRebuiltThisRun(r) {
				r.add(Step{Name: name, Status: StepManual,
					Detail:  "grain-daemon.service is running the binary this run just replaced -- restart it to pick the new build up",
					Command: "systemctl restart grain-daemon.service"})
				return true, nil
			}
			r.add(Step{Name: name, Status: StepSkipped,
				Detail: "grain-daemon.service is already running, with the unit this run would have written"})
			return true, nil
		}
		return adoptRunningDaemon(ctx, sys, opts, r, name, desc, pid)
	}

	if keptErr != nil {
		r.add(Step{Name: name, Status: StepRefused, Detail: keptErr.Error() +
			" -- pass -upgrade-allowed-branches to replace it rather than have this run carry it into the unit it writes"})
		return false, nil
	}
	unit := daemonUnit(opts, gateway)
	unitPath := systemdUnitPath
	why := "writes " + unitPath + " and starts it (checked above that none is running yet)"
	if len(kept) > 0 {
		why += fmt.Sprintf("; keeps -upgrade-allowed-branches %s from the unit already there", strings.Join(kept, ","))
	}
	cmd := fmt.Sprintf("write the unit below to %s, then: systemctl daemon-reload && systemctl enable --now grain-daemon.service\n\n%s", unitPath, unit)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: why, Command: cmd})
		return false, nil
	}
	if !sys.IsRoot() {
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: cmd})
		return false, nil
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write %s: %v", unitPath, err), Command: cmd})
		return false, nil
	}
	if _, err := sys.Run(ctx, "", nil, "systemctl", "daemon-reload"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("daemon-reload failed: %v", err), Command: cmd})
		return false, nil
	}
	if _, err := sys.Run(ctx, "", nil, "systemctl", "enable", "--now", "grain-daemon.service"); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("enable --now failed: %v", err), Command: cmd})
		return false, nil
	}
	detail := "grain-daemon.service is running"
	if len(kept) > 0 {
		detail += fmt.Sprintf(", with the -upgrade-allowed-branches %s the unit it replaced passed", strings.Join(kept, ","))
	}
	r.add(Step{Name: name, Status: StepDone, Detail: detail})
	return true, nil
}

// keptUpgradeAllowlist is the -upgrade-allowed-branches ensureDaemon
// carries over from the grain-daemon.service already at systemdUnitPath:
// nil when opts names its own (Options.UpgradeAllowedBranches, or a
// -upgrade-allowed-branches in ExtraDaemonArgs), when there is no unit
// there, or when it passes none. An error is a value there that
// upgrade.CheckAllowedBranches -- or the unit line -- would refuse, which
// is not carried into a new unit silently.
func keptUpgradeAllowlist(opts Options) ([]string, error) {
	if len(opts.UpgradeAllowedBranches) > 0 {
		return nil, nil
	}
	if _, ok := lastFlagValue(opts.ExtraDaemonArgs, "upgrade-allowed-branches"); ok {
		return nil, nil
	}
	data, err := os.ReadFile(systemdUnitPath)
	if err != nil {
		return nil, nil
	}
	execStart, _, _, err := parseUnitFile(string(data))
	if err != nil {
		return nil, nil
	}
	value, ok := lastFlagValue(strings.Fields(execStart), "upgrade-allowed-branches")
	if !ok {
		return nil, nil
	}
	var patterns []string
	for _, p := range strings.Split(strings.Trim(value, `"'`), ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	if err := checkUpgradeAllowedBranches(patterns); err != nil {
		return nil, fmt.Errorf("the existing %s passes %w", systemdUnitPath, err)
	}
	return patterns, nil
}

// lastFlagValue is argvFlagValue read the way the daemon's own flag
// package reads a command line: one dash or two, and the last occurrence
// winning.
func lastFlagValue(args []string, flag string) (value string, found bool) {
	for i := 0; i < len(args); i++ {
		name := strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-")
		if name == args[i] {
			continue
		}
		if v, ok := strings.CutPrefix(name, flag+"="); ok {
			value, found = v, true
		} else if name == flag && i+1 < len(args) {
			value, found = args[i+1], true
			i++
		}
	}
	return value, found
}

// checkUpgradeAllowedBranches is upgrade.CheckAllowedBranches plus what
// the unit's own ExecStart needs of a pattern: daemonUnit joins its
// arguments with spaces and systemd unescapes '\', '%' and '$' and
// strips quotes, so a pattern holding any of those would not reach the
// daemon as written. No branch name needs one.
func checkUpgradeAllowedBranches(patterns []string) error {
	for _, p := range patterns {
		if strings.ContainsAny(p, " \t\n\\%$\"'") {
			return fmt.Errorf("upgrade allowlist pattern %q holds a blank, a quote, or one of \\ %% $, which a systemd unit's command line does not pass through as written", p)
		}
	}
	return upgrade.CheckAllowedBranches(patterns)
}

// waitForDaemonAPI polls http://opts.UIAddr/api/config -- the one route
// pkg/ui/server.go serves to literally anyone (requiresAccess), so it
// carries no risk of a readiness probe being refused for a reason that
// has nothing to do with readiness -- until it answers 200 or
// opts.DaemonReadyTimeout runs out.
func waitForDaemonAPI(ctx context.Context, sys System, opts Options) bool {
	url := "http://" + opts.UIAddr + "/api/config"
	deadline := time.Now().Add(opts.DaemonReadyTimeout)
	for {
		if status, _, err := sys.HTTPRequest(ctx, http.MethodGet, url, nil); err == nil && status == http.StatusOK {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(opts.DaemonReadyPollInterval):
		}
	}
}

// ensureDaemonReachable waits for the daemon's own API to answer once
// ensureDaemon reports it should be running, so the three provisioning
// steps after it (ensureTeamTargetRepos, ensureTeamGitCredential,
// checkSandboxBudget) never guess at whether a loopback call will land
// anywhere. attempted is ensureDaemon's own return value -- when it is
// false there was never a daemon to wait for (StartDaemon=false, dry
// run, or an earlier prerequisite failing), and this skips outright
// rather than polling a port nothing was ever asked to bind.
func ensureDaemonReachable(ctx context.Context, sys System, opts Options, r *Result, attempted bool) bool {
	const name = "confirm the daemon's API is reachable"
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would poll http://%s/api/config until it answers", opts.UIAddr)})
		return false
	}
	if !attempted {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon was not started by this run"})
		return false
	}
	if waitForDaemonAPI(ctx, sys, opts) {
		r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("http://%s/api/config answered", opts.UIAddr)})
		return true
	}
	r.add(Step{Name: name, Status: StepManual,
		Detail:  fmt.Sprintf("http://%s/api/config did not answer within %s", opts.UIAddr, opts.DaemonReadyTimeout),
		Command: "systemctl status grain-daemon.service; journalctl -u grain-daemon.service -n 100 --no-pager"})
	return false
}

// daemonActorArg is the -as (or --as) a set of daemon arguments passes,
// the daemon's own flag for the actor it acts as.
func daemonActorArg(args []string) (string, bool) {
	if v, ok := argvFlagValue(args, "as"); ok {
		return v, true
	}
	return argvFlagValue(args, "-as")
}

// withActorArg is opts with -as actor appended to the daemon's
// arguments: the unit an earlier init wrote to keep a -data-dir's stored
// admin as the daemon's actor, before the access code made every caller
// the owner. ownUnitRunning still reads such a unit as its own.
func withActorArg(opts Options, actor string) Options {
	opts.ExtraDaemonArgs = append(append([]string(nil), opts.ExtraDaemonArgs...), "-as", actor)
	return opts
}

// teamSettings is the handful of fields ensureTeamTargetRepos and
// ensureTeamGitCredential read off GET /api/deployment -- ui.Team's own
// JSON shape, trimmed to just what each needs to decide whether it has
// anything to do.
type teamSettings struct {
	TargetRepos   []string `json:"targetRepos"`
	GitCredential string   `json:"gitCredential"`
	// GitConnections and HasGitCredential are the team's git
	// connections and the daemon's own answer to whether any of them
	// (or GitCredential) covers its default host. Adding or deleting a
	// connection blanks GitCredential, so it alone does not say the team
	// has no credential (grain/task-3079).
	GitConnections []struct {
		Credential string `json:"credential"`
		Host       string `json:"host"`
	} `json:"gitConnections"`
	HasGitCredential *bool `json:"hasGitCredential"`
}

// gitCredential is whether the team already has a git credential for the
// default host, and what to call it in a step's detail. A daemon that
// answers hasGitCredential is believed; one too old to has every
// connection counted, since it cannot say which host is its default.
//
// A team set to "anonymous" (None, public repositories only) reads
// hasGitCredential=false -- it can push nothing -- but someone chose that
// by hand, so it counts here as already set rather than being replaced
// with the deployment's token.
func (t teamSettings) gitCredential() (bool, string) {
	if strings.TrimSpace(t.GitCredential) == "anonymous" {
		return true, "anonymous"
	}
	for _, c := range t.GitConnections {
		if strings.TrimSpace(c.Credential) == "anonymous" && strings.TrimSpace(c.Host) == "" {
			return true, "anonymous"
		}
	}
	if t.HasGitCredential != nil && !*t.HasGitCredential {
		return false, ""
	}
	if c := strings.TrimSpace(t.GitCredential); c != "" {
		return true, c
	}
	if len(t.GitConnections) > 0 {
		return true, fmt.Sprintf("%d git connection(s), first %s", len(t.GitConnections), t.GitConnections[0].Credential)
	}
	if t.HasGitCredential != nil {
		return true, "a git connection"
	}
	return false, ""
}

// getTeamSettings reads the deployment's current settings off GET
// /api/deployment -- the one read every write step below does first, so
// a deployment an operator already configured by hand is reported
// StepSkipped (already set) rather than clobbered on a second run.
func getTeamSettings(ctx context.Context, sys System, opts Options) (teamSettings, int, []byte, error) {
	url := deploymentURL(opts)
	status, body, err := sys.HTTPRequest(ctx, http.MethodGet, url, nil)
	if err != nil || status != http.StatusOK {
		return teamSettings{}, status, body, err
	}
	var t teamSettings
	if err := json.Unmarshal(body, &t); err != nil {
		return teamSettings{}, status, body, fmt.Errorf("parsing %s's response: %w", url, err)
	}
	return t, status, body, nil
}

// deploymentURL is the daemon's /api/deployment, where the deployment's
// target repos and git connections are kept (docs/single-tenant-reset.md,
// stage 3e-iv).
func deploymentURL(opts Options) string {
	return fmt.Sprintf("http://%s/api/deployment", opts.UIAddr)
}

// ensureTeamTargetRepos points the default team's TargetRepos at
// opts.GitHubRepos -- the same repositories ensureCredentialLadder connects a
// token to -- over PUT /api/deployment: leaving one set
// without the other is a trap, a deployment that can authenticate to a
// repo it is not allowed to dispatch work into. Never overwrites a team
// that already has target repos of its own, by hand or from an earlier
// run with a different list.
func ensureTeamTargetRepos(ctx context.Context, sys System, opts Options, r *Result, daemonUp bool) {
	const name = "set the default team's target repos"
	if len(opts.GitHubRepos) == 0 {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "no -github-repo given"})
		return
	}
	url := deploymentURL(opts)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would PUT %s with targetRepos=%s if the default team has none yet", url, strings.Join(opts.GitHubRepos, ", "))})
		return
	}
	if !daemonUp {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon's API is not reachable -- see the step above"})
		return
	}

	current, status, body, err := getTeamSettings(ctx, sys, opts)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("GET %s: status %d: %v: %s", url, status, err, strings.TrimSpace(string(body))),
			Command: apiCurlCommand(opts, http.MethodGet, url, nil)})
		return
	}
	if len(current.TargetRepos) > 0 {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("already set to %s", strings.Join(current.TargetRepos, ", "))})
		return
	}

	reqBody, _ := json.Marshal(struct {
		TargetRepos []string `json:"targetRepos"`
	}{opts.GitHubRepos})
	manualCmd := apiCurlCommand(opts, http.MethodPut, url, reqBody)
	status, respBody, err := sys.HTTPRequest(ctx, http.MethodPut, url, reqBody)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("PUT %s: status %d: %v: %s", url, status, err, strings.TrimSpace(string(respBody))),
			Command: manualCmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("targetRepos=%s", strings.Join(opts.GitHubRepos, ", "))})
}

// ensureTeamGitCredential points the default team at the credential
// ensureCredentialLadder wrote, if the team has none of its own yet.
// Since grain/task-1592 a team clones and pushes with its own credential
// or with none: the deployment ladder is no longer a fallback, and the
// one-time migration that copied it into each team ran at the daemon's
// first start -- before ensureTeamTargetRepos gave the default team any
// repos, and before any later run that adds -github-token-file. Without
// this step a deployment that init reports up refuses every task into
// -github-repo with "team "default" has no GitHub credential".
//
// The credential is recorded as the team's git connection to github.com
// (POST /api/deployment/git-connections naming it), not the retired
// single team credential (grain/task-3929). A loopback request is the
// deployment's own actor, a system admin, which is what naming a
// deployment credential on a team requires.
func ensureTeamGitCredential(ctx context.Context, sys System, opts Options, r *Result, daemonUp bool) {
	const name = "point the deployment at the GitHub credential"
	if opts.GitHubToken == "" {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: "no token given -- set one in Settings -> Git (/settings/git)"})
		return
	}
	// On the Kubernetes platforms the ladder this names was never written
	// where the daemon can read it (-data-dir is on this machine), and
	// pointing the team at it first made ensureK8sTeamGitHubCredential see
	// "already has one" and skip storing the token at all: the 10-05
	// walkthrough found the default team at gitCredential=walkthrough, a
	// credential the pod did not have.
	if opts.Platform == PlatformK8sContainer || opts.Platform == PlatformK8sGranule {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: "skipped: -platform=" + string(opts.Platform) + " has no credential ladder in the cluster -- \"give the deployment the GitHub token\" stores the token as the deployment's own instead"})
		return
	}
	url := deploymentURL(opts)
	connURL := url + "/git-connections"
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would POST %s naming %s if the default team has no credential yet", connURL, opts.GitHubCredentialName)})
		return
	}
	if !daemonUp {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon's API is not reachable -- see the step above"})
		return
	}

	current, status, body, err := getTeamSettings(ctx, sys, opts)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("GET %s: status %d: %v: %s", url, status, err, strings.TrimSpace(string(body))),
			Command: apiCurlCommand(opts, http.MethodGet, url, nil)})
		return
	}
	if has, what := current.gitCredential(); has {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("already %s", what)})
		return
	}

	reqBody, _ := json.Marshal(struct {
		Credential string `json:"credential"`
		Host       string `json:"host"`
	}{opts.GitHubCredentialName, "github.com"})
	manualCmd := apiCurlCommand(opts, http.MethodPost, connURL, reqBody)
	status, respBody, err := sys.HTTPRequest(ctx, http.MethodPost, connURL, reqBody)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("POST %s: status %d: %v: %s", connURL, status, err, strings.TrimSpace(string(respBody))),
			Command: manualCmd})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("git connection to github.com with %s", opts.GitHubCredentialName)})
}

// sandboxBudgetSettings is the slice of GET /api/settings that
// checkSandboxBudget reads: ui.SandboxCommitment and its Budget, trimmed
// to the fields that say whether one sandbox of the deployment's own
// shape fits inside the budget at all.
type sandboxBudgetSettings struct {
	SandboxCommitment *struct {
		Uncapped           bool `json:"uncapped"`
		CPUsPerSandbox     int  `json:"cpusPerSandbox"`
		MemoryMBPerSandbox int  `json:"memoryMbPerSandbox"`
		HostCPUs           int  `json:"hostCpus"`
		HostMemoryMB       int  `json:"hostMemoryMb"`
		Budget             *struct {
			CPUOvercommit    float64 `json:"cpuOvercommit"`
			MemoryOvercommit float64 `json:"memoryOvercommit"`
			ReserveMemoryMB  int     `json:"reserveMemoryMb"`
			CPUs             float64 `json:"cpus"`
			CPUsBounded      bool    `json:"cpusBounded"`
			MemoryMB         int     `json:"memoryMb"`
			MemoryBounded    bool    `json:"memoryBounded"`
		} `json:"budget"`
	} `json:"sandboxCommitment"`
}

// minSuggestedReserveMB is the smallest memory reserve
// checkSandboxBudget will suggest: what the 10-04 walkthrough's 7.8 GiB
// host was brought to dispatching with (docs/test-runs/2026-10-04-grain-init-platforms.md).
const minSuggestedReserveMB = 512

// checkSandboxBudget reads the deployment's sandbox budget back off the
// daemon it just brought up and reports a manual step when that budget
// cannot hold even one sandbox of the deployment's own shape. Such a
// deployment is not "up": every approved task stays queued forever,
// because model.Budget.Admits refuses the first sandbox. That was a
// 7.8 GiB host whose default reserve was 8 GiB (task 1940).
//
// It never writes the settings itself. A reserve and a shape are an
// operator's trade-off on their own machine. So this prints the PUT
// that would fit, and the operator decides.
func checkSandboxBudget(ctx context.Context, sys System, opts Options, r *Result, daemonUp bool) {
	const name = "check the sandbox budget can admit a run"
	url := fmt.Sprintf("http://%s/api/settings", opts.UIAddr)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would GET %s and check that one sandbox of the deployment's shape fits inside its budget", url)})
		return
	}
	if !daemonUp {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon's API is not reachable -- see the step above"})
		return
	}

	status, body, err := sys.HTTPRequest(ctx, http.MethodGet, url, nil)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("GET %s: status %d: %v: %s", url, status, err, strings.TrimSpace(string(body))),
			Command: apiCurlCommand(opts, http.MethodGet, url, nil)})
		return
	}
	var s sandboxBudgetSettings
	if err := json.Unmarshal(body, &s); err != nil {
		r.add(Step{Name: name, Status: StepManual,
			Detail:  fmt.Sprintf("parsing %s's response: %v", url, err),
			Command: apiCurlCommand(opts, http.MethodGet, url, nil)})
		return
	}
	c := s.SandboxCommitment
	switch {
	case c == nil:
		// GET /api/settings answers the same for every caller; it omits
		// the commitment while the deployment's own max_workers is 0.
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon reports no sandbox commitment -- none while the " +
			"deployment has no worker slots, and none at all where it cannot count its host's cores and memory (a Kubernetes " +
			"cluster, or a daemon not on Linux), so there is no budget to check"})
		return
	case c.Budget == nil:
		r.add(Step{Name: name, Status: StepSkipped, Detail: "the daemon reports no sandbox budget on this host -- nothing bounds a run"})
		return
	case c.Uncapped:
		r.add(Step{Name: name, Status: StepSkipped, Detail: "host sandboxing -- runs are not shaped, so there is no budget to fit"})
		return
	}
	b := c.Budget

	var problems []string
	fix := map[string]any{}
	tooSmall := false
	if b.MemoryBounded && c.MemoryMBPerSandbox > 0 && b.MemoryMB < c.MemoryMBPerSandbox {
		problems = append(problems, fmt.Sprintf("%d MiB of sandbox memory (%d MiB host less %d MiB reserved, at %.2fx) cannot hold one %d MiB sandbox",
			b.MemoryMB, c.HostMemoryMB, b.ReserveMemoryMB, b.MemoryOvercommit, c.MemoryMBPerSandbox))
		reserve, shape := fittingMemory(c.HostMemoryMB, b.ReserveMemoryMB, b.MemoryOvercommit, c.MemoryMBPerSandbox)
		if reserve != b.ReserveMemoryMB {
			fix["sandboxMemoryReserveMb"] = reserve
		}
		switch {
		case shape == 0:
			tooSmall = true
		case shape != c.MemoryMBPerSandbox:
			fix["sandboxMemoryMb"] = shape
		}
	}
	if b.CPUsBounded && c.CPUsPerSandbox > 0 && b.CPUs < float64(c.CPUsPerSandbox) {
		problems = append(problems, fmt.Sprintf("%.1f vCPU of sandbox cpu (%d host vCPU less the controller's reserve, at %.2fx) cannot hold one %d vCPU sandbox",
			b.CPUs, c.HostCPUs, b.CPUOvercommit, c.CPUsPerSandbox))
		if cpus := int(b.CPUs); cpus >= 1 {
			fix["sandboxCpus"] = cpus
		} else {
			// The cpu reserve is not a setting, so a host it eats whole
			// can only be unbounded in cpu (model.Budget.CPUBudget).
			fix["sandboxCpuOvercommit"] = 0
		}
	}
	if len(problems) == 0 {
		detail := fmt.Sprintf("one %d MiB sandbox fits", c.MemoryMBPerSandbox)
		if b.MemoryBounded {
			detail += fmt.Sprintf(" in %d MiB of sandbox memory", b.MemoryMB)
		}
		r.add(Step{Name: name, Status: StepDone, Detail: detail})
		return
	}

	detail := "no run can ever be admitted, so every approved task would stay queued: " + strings.Join(problems, "; ") +
		". Lower the reserve or the sandbox shape in Settings → Sandbox"
	step := Step{Name: name, Status: StepManual}
	if tooSmall {
		detail += " -- though this host has too little memory left for even a 1 GiB sandbox"
	}
	if len(fix) > 0 && !tooSmall {
		reqBody, _ := json.Marshal(fix)
		detail += ", or run the command below"
		step.Command = apiCurlCommand(opts, http.MethodPut, url, reqBody)
	}
	step.Detail = detail
	r.add(step)
}

// fittingMemory is the reserve and sandbox memory checkSandboxBudget
// suggests for a host of hostMB: the current reserve, or a quarter of
// the host if that is smaller (never under minSuggestedReserveMB). Then
// the current shape if that now fits, else the largest whole GiB that
// does. A shape of 0 means not even 1 GiB fits.
func fittingMemory(hostMB, reserveMB int, ratio float64, shapeMB int) (int, int) {
	reserve := min(reserveMB, max(hostMB/4/256*256, minSuggestedReserveMB))
	budget := int(float64(hostMB-reserve) * ratio)
	if budget >= shapeMB {
		return reserve, shapeMB
	}
	return reserve, budget / 1024 * 1024
}

// ProvisionOverAPI runs the four steps of Init that talk to nothing but
// the daemon's own loopback-trusted HTTP API -- ensureDaemonReachable,
// ensureTeamTargetRepos, ensureTeamGitCredential and
// checkSandboxBudget, in that order -- and nothing else: no Step above this point in the file
// makes a System.Run call, only HTTPGet/HTTPRequest. initFor calls this
// for the host target it already builds the daemon for; it is exported
// so a caller that brought a daemon up some other way (most concretely,
// `kubectl apply` of deploy/kubernetes/grain.yaml -- see
// docs/k8s-init-design.md) can provision it identically without
// reimplementing any of the loopback calls. attempted reports
// whether this run itself expects the daemon to be reachable yet (a
// daemon this run just started or applied, as opposed to -dry-run or an
// earlier prerequisite that never got that far) -- see
// ensureDaemonReachable's own doc comment.
func ProvisionOverAPI(ctx context.Context, sys System, opts Options, r *Result, attempted bool) {
	daemonUp := ensureDaemonReachable(ctx, sys, opts, r, attempted)
	provisionReachableDaemon(ctx, sys, opts, r, daemonUp)
}

// provisionReachableDaemon is ProvisionOverAPI after its reachability
// check, for initFor's host path.
func provisionReachableDaemon(ctx context.Context, sys System, opts Options, r *Result, daemonUp bool) {
	ensureTeamTargetRepos(ctx, sys, opts, r, daemonUp)
	ensureTeamGitCredential(ctx, sys, opts, r, daemonUp)
	checkSandboxBudget(ctx, sys, opts, r, daemonUp)
}

// loopbackDialAddr is addr as a client on this host dials it: a listener
// bound to every interface (":4242", "0.0.0.0:4242", "[::]:4242") is
// reached on 127.0.0.1.
func loopbackDialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if wildcardHost(host) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

func wildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// wildcardBind is whether the listen address bind is on every interface.
func wildcardBind(bind string) bool {
	h, _, err := net.SplitHostPort(bind)
	return err == nil && wildcardHost(h)
}

// machineHostnames is this machine's fully qualified name and its short
// one -- `hostname -f` and `hostname -s`, the pair scripts/setup.sh's
// machine_hostnames defaults GRAIN_UI_ALLOWED_HOSTS to -- what a browser
// elsewhere on the network puts in Host, and so what -ui-allowed-hosts
// defaults to on a listener bound to every interface. A host whose own
// name is short (Debian's default) is qualified the way `hostname -f`
// does it (canonicalHostname); before that it got only the short name,
// and a browser using the FQDN was refused (task 4359). A variable so
// tests can be another machine.
var machineHostnames = func() []string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return nil
	}
	canonical := ""
	if !strings.Contains(name, ".") {
		canonical = canonicalHostname(name)
	}
	return hostnamesFor(name, canonical)
}

// hostnamesFor is machineHostnames for a machine named name, whose
// canonical name -- for a short name -- is canonical: the fully
// qualified name first, then the short one, without repeats.
func hostnamesFor(name, canonical string) []string {
	var names []string
	add := func(n string) {
		if n = strings.TrimSuffix(n, "."); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	fqdn := name
	if !strings.Contains(name, ".") {
		fqdn = ""
		canonical = strings.TrimSuffix(canonical, ".")
		if short, _, ok := strings.Cut(canonical, "."); ok && strings.EqualFold(short, name) {
			fqdn = canonical
		}
	}
	add(fqdn)
	if short, _, _ := strings.Cut(name, "."); short != "" {
		add(short)
	}
	return names
}

// hostsFilePath is the hosts file canonicalHostname reads first; a
// variable so tests can supply their own.
var hostsFilePath = "/etc/hosts"

// canonicalHostname is name's canonical name, as `hostname -f` finds it:
// the first name on the hosts-file line that lists name (Debian writes
// "127.0.1.1 host.example.com host" there), else what the resolver says
// name is a CNAME for. "" when neither knows a better one.
func canonicalHostname(name string) string {
	if data, err := os.ReadFile(hostsFilePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			for _, f := range fields[1:] {
				if strings.EqualFold(f, name) {
					return fields[1]
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if cname, err := net.DefaultResolver.LookupCNAME(ctx, name); err == nil {
		return strings.TrimSuffix(cname, ".")
	}
	return ""
}

// DefaultAllowedHosts is -ui-allowed-hosts on a listener bound to every
// interface when nothing names it: this machine's own names, plus what
// each registered AllowedHostsHook adds for them (a web proxy's names,
// on a machine some environment fronts with one), and why, for the step
// line.
func DefaultAllowedHosts(names []string) (hosts []string, why string) {
	if len(names) == 0 {
		return nil, ""
	}
	why = "this machine's own names"
	hosts = append([]string(nil), names...)
	for _, h := range hookedAllowedHosts(names) {
		hosts = append(hosts, h.host)
		why += ", plus " + h.host + " " + h.purpose
	}
	return hosts, why
}

// onlyMachineHostnames is whether the comma-separated list names
// nothing but entries of names: what an earlier grain init defaulted
// -ui-allowed-hosts to.
func onlyMachineHostnames(list string, names []string) bool {
	if len(names) == 0 {
		return false
	}
	named := false
	for _, e := range strings.Split(list, ",") {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if !slices.Contains(names, e) {
			return false
		}
		named = true
	}
	return named
}

// settleUIAddr decides the -ui-addr and -ui-allowed-hosts a host
// platform's unit passes, and points opts.UIAddr -- where every step
// after the daemon starts reaches it -- at that address:
//
//   - -ui-addr in ExtraDaemonArgs, or -ui-addr given to grain init, wins;
//   - otherwise a unit already at systemdUnitPath keeps its own -ui-addr,
//     or, naming none, LegacyUIAddr -- the default it was serving on --
//     so a re-run never moves an existing deployment;
//   - otherwise, a fresh install, DefaultUIAddr.
//
// -ui-allowed-hosts is likewise ExtraDaemonArgs', else the installed
// unit's, else -- when the address listens on every interface -- this
// machine's own names (DefaultAllowedHosts: with what a registered
// AllowedHostsHook adds, such as a web proxy's names), so the Host check
// (ui.HostGuard) covers every request rather than only loopback ones. An
// installed list naming this machine's own names alone, as an earlier
// run defaulted it, gains what the hooks add. Says which as a step.
func settleUIAddr(opts Options, r *Result) Options {
	const name = "choose the UI/API address"
	var installed []string
	unitExists := false
	if data, err := os.ReadFile(systemdUnitPath); err == nil {
		unitExists = true
		if execStart, _, _, err := parseUnitFile(string(data)); err == nil {
			installed = strings.Fields(execStart)
		}
	}

	bind, why := "", ""
	if v, ok := lastFlagValue(opts.ExtraDaemonArgs, "ui-addr"); ok {
		bind, why = v, "from -daemon-arg -ui-addr"
	} else if opts.uiBindAddr != "" {
		bind, why = opts.uiBindAddr, "as given (-ui-addr)"
		opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-addr", bind)
	} else if v, ok := lastFlagValue(installed, "ui-addr"); ok {
		bind, why = v, "kept from the installed "+systemdUnitPath
		if v != DefaultUIAddr {
			why += " (pass -ui-addr " + DefaultUIAddr + " to move to the new default)"
		}
		opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-addr", bind)
	} else if unitExists {
		bind, why = LegacyUIAddr, "kept: the installed "+systemdUnitPath+" names no -ui-addr, and "+LegacyUIAddr+
			" is the default it was written to serve on (pass -ui-addr "+DefaultUIAddr+" to move to the new default)"
		opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-addr", bind)
	} else {
		bind, why = DefaultUIAddr, "the default for a fresh install"
		opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-addr", bind)
	}
	opts.UIAddr = loopbackDialAddr(bind)

	hosts := ""
	if _, ok := lastFlagValue(opts.ExtraDaemonArgs, "ui-allowed-hosts"); ok {
		hosts = "from -daemon-arg -ui-allowed-hosts"
	} else if v, ok := lastFlagValue(installed, "ui-allowed-hosts"); ok && v != "" {
		names := machineHostnames()
		var hooked []hookedHost
		if wildcardBind(bind) && onlyMachineHostnames(v, names) {
			hooked = hookedAllowedHosts(names)
		}
		if len(hooked) > 0 {
			var adding []string
			for _, h := range hooked {
				v += "," + h.host
				adding = append(adding, h.host+" "+h.purpose)
			}
			hosts = v + ", adding " + strings.Join(adding, " and ") +
				" to the installed unit's own names (pass -daemon-arg -ui-allowed-hosts=... to keep it as it was)"
		} else {
			hosts = v + ", kept from the installed unit"
		}
		opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-allowed-hosts", v)
	} else if wildcardBind(bind) {
		if names, why := DefaultAllowedHosts(machineHostnames()); len(names) > 0 {
			opts.uiDaemonArgs = append(opts.uiDaemonArgs, "-ui-allowed-hosts", strings.Join(names, ","))
			hosts = strings.Join(names, ",") + ", " + why + " (pass -daemon-arg -ui-allowed-hosts=... to name others)"
		}
	}
	detail := "-ui-addr " + bind + ", " + why
	if hosts != "" {
		detail += "; -ui-allowed-hosts " + hosts
	}
	if h, port, err := net.SplitHostPort(bind); err == nil && wildcardHost(h) {
		detail += fmt.Sprintf(". Listening on every interface: every caller presents the access code (grain access-code prints it), "+
			"so http://<this host>:%s/ asks a browser for it; or keep it private with ssh -L %s:localhost:%s <this host>",
			port, port, port)
	}
	r.add(Step{Name: name, Status: StepDone, Detail: detail})
	return opts
}
