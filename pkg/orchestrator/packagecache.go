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

package orchestrator

// Pointing one run's sandbox at this deployment's package cache
// (pkg/pkgcache): a handful of configuration files written into the
// sandbox just after its git credentials, plus one `go env -w` and one
// `npm config set` for the two ecosystems whose configuration file is
// their own tool's -- all of it before the checkout that is the first
// thing a run does.
//
// In transparent caching mode, the internal Root CA is installed to
// /usr/local/share/ca-certificates/grain-ca.crt, updated with update-ca-certificates,
// and /etc/resolv.conf points to the controller DNS server (172.17.0.1),
// seamlessly intercepting standard upstream requests.
//
// Every failure here is a warning and nothing more. A run whose sandbox
// could not be pointed at the cache is a run that downloads its packages
// from the internet, which is exactly what every run did before this
// existed -- there is no version of "the cache could not be configured"
// that is worth failing a dispatch over.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// SandboxCommander is implemented by a Sandbox this process can run a
// command inside directly, over the same transport its tool calls use.
type SandboxCommander interface {
	RunInSandbox(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int)
}

// SandboxRootCommander is implemented by a Sandbox whose transport can
// run a command as root directly, without going through sudo inside it.
// The local container backend is one: its stock image
// (Dockerfile.k8s-sandbox) runs as an unprivileged user and carries no
// sudo, so every script below that reaches for `sudo -n` failed there
// with "sudo: not found", and no container sandbox was ever pointed at
// the package cache. `docker exec -u 0` needs nothing from the image.
//
// Only the writes into /etc go this way. Everything that configures a
// tool for the user the run is (`go env -w`, `npm config set`) stays on
// RunInSandbox, since as root it would configure root's home instead.
type SandboxRootCommander interface {
	RunAsRootInSandbox(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int)
}

// SandboxPackageCacheProvisioner is implemented by a Sandbox whose
// package-cache configuration under /etc was put there when the sandbox
// was created, because nothing can write it there afterwards.
//
// A Kubernetes sandbox pod is one (K8sSandboxes, and k8s.Setup on why):
// its image runs unprivileged with no sudo, and `kubectl exec` cannot
// choose a user, so it has neither a SandboxRootCommander nor a sudo for
// the scripts below to find. It reports the cache base and CA it was
// created with; where those are what a dispatch asks for, only the
// per-user half -- `go env -w`, `npm config set` -- is left to do. Where
// they are not, the pod is treated as any other sandbox is, which for an
// unprivileged pod means SandboxRootless below.
type SandboxPackageCacheProvisioner interface {
	ProvisionedPackageCache() (base string, caPEM []byte)
}

// SandboxRootless is implemented by a Sandbox that may have no route to
// root at all: neither a transport that can be root (SandboxRootCommander)
// nor a sudo inside it. RootUnavailable answers "" when root turns out to
// be reachable after all, and otherwise says why it is not, in words a
// log line can carry.
//
// A Kubernetes sandbox pod is the one that needs it. `kubectl exec` runs
// as the container's own user and cannot ask for another, the stock
// image (Dockerfile.k8s-sandbox) is USER debian with no sudo, and the pod
// stays unprivileged (docs/grain-init-platforms.md) -- so every write
// into /etc below failed there with "sudo: not found", on every run.
// Asking first lets ConfigurePackageCacheTransparent skip those steps and
// say so once, and still lets a deployment whose own image does carry
// passwordless sudo get the whole configuration.
type SandboxRootless interface {
	RootUnavailable(ctx context.Context) string
}

// rootProbeScript exits zero where the scripts below can reach root, and
// says why not on stderr where they cannot.
const rootProbeScript = `[ "$(id -u)" = 0 ] && exit 0
if ! command -v sudo >/dev/null 2>&1; then
  echo "it runs as uid $(id -u) and has no sudo" >&2
  exit 1
fi
sudo -n true`

// noRootNoted is the reasons a rootless sandbox has already been logged
// for, so a deployment whose every sandbox lacks root hears it once
// rather than on every dispatch.
var noRootNoted sync.Map

// noteNoRoot logs, once per distinct reason, what a sandbox without root
// does and does not get from the package cache.
func noteNoRoot(name, reason string) {
	if _, seen := noRootNoted.LoadOrStore(reason, true); seen {
		return
	}
	log.Printf("orchestrator: %s cannot write as root (%s), so grain does not install its CA certificate, "+
		"point its resolver at the package cache, or write apt, pip and docker configuration into its /etc. "+
		"Go and npm are still pointed at the cache through the run's own user configuration. A sandbox image "+
		"with passwordless sudo gets the rest. Logged once; every later sandbox like it is treated the same way",
		name, reason)
}

// runAsRoot runs argv as root where the sandbox's transport can, and as
// the sandbox's own user otherwise -- where the scripts it is given find
// root through sudo themselves.
func runAsRoot(ctx context.Context, commander SandboxCommander, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	if root, ok := commander.(SandboxRootCommander); ok {
		return root.RunAsRootInSandbox(ctx, argv, stdin)
	}
	return commander.RunInSandbox(ctx, argv, stdin)
}

// writeAsRootScript installs one file from stdin, as root, with a given mode.
const writeAsRootScript = `set -eu
dir=$(dirname "$1")
if [ "$(id -u)" = 0 ]; then
  mkdir -p "$dir"
  install -m "$2" /dev/stdin "$1"
else
  sudo -n mkdir -p "$dir"
  sudo -n install -m "$2" /dev/stdin "$1"
fi`

// writeInPlaceScript replaces one file's *contents* as root, leaving the
// file itself -- its inode, its mode, its owner -- exactly where it was.
//
// For /etc/resolv.conf, which is the only thing that needs this and the
// reason it exists. In a container sandbox that path is not a file at
// all: docker bind-mounts the host's own resolv.conf over it, and
// `install` writes a temporary file and renames it into place, which the
// kernel refuses across a mount point:
//
//	orchestrator: configuring DNS /etc/resolv.conf in grain-task-327:
//	  install: cannot remove '/etc/resolv.conf': Device or resource busy
//
// That is a warning and the dispatch carries on, so what it produced was
// a container-sandboxes deployment where DNS interception was bound,
// advertised, logged as working -- and configured into nothing, silently,
// every time. Truncating and writing through the existing inode works on
// a bind-mounted file and on the ordinary file a granule guest has alike.
//
// No mode is set for the same reason: on a bind mount a chmod would
// change the host's own file, and the file being written through is
// already the 0644 every resolv.conf is.
const writeInPlaceScript = `set -eu
if [ "$(id -u)" = 0 ]; then
  cat > "$1"
else
  sudo -n tee "$1" >/dev/null
fi`

// configureToolScript runs one of pkgcache.SandboxCommands, and does
// nothing at all when the tool it configures is not installed here.
// When configuring the Go toolchain (`go env -w`), it also compiles the
// standalone GOCACHEPROG client helper into GoCacheProgPath once (unless a
// prebuilt binary is installed at /usr/local/bin/grain-gocacheprog or the
// caller skips the build via GRAIN_SKIP_GOCACHEPROG_BUILD=1).
const configureToolScript = `set -eu
command -v "$1" >/dev/null 2>&1 || exit 0
if [ "$1" = go ] && [ "${2:-}" = env ] && [ "${3:-}" = -w ]; then
  src="` + pkgcache.GoCacheProgPath + `.$$.go"
  cat >"$src" <<'GRAIN_GOCACHEPROG_EOF'
` + pkgcache.GoCacheProgSource + `GRAIN_GOCACHEPROG_EOF
  if [ ! -x "` + pkgcache.GoCacheProgPath + `" ] && [ -x /usr/local/bin/grain-gocacheprog ]; then
    cp -f /usr/local/bin/grain-gocacheprog "` + pkgcache.GoCacheProgPath + `" && \
      cp -f "$src" "` + pkgcache.GoCacheProgPath + `.go" || true
  fi
  if [ "${GRAIN_SKIP_GOCACHEPROG_BUILD:-}" = 1 ]; then
    rm -f "$src"
  elif [ ! -x "` + pkgcache.GoCacheProgPath + `" ] || ! cmp -s "$src" "` + pkgcache.GoCacheProgPath + `.go" 2>/dev/null; then
    GO111MODULE=off GOCACHEPROG="" GOPROXY=off CGO_ENABLED=0 "$1" build -o "` + pkgcache.GoCacheProgPath + `.tmp.$$" "$src" >/dev/null 2>&1 && \
      mv -f "` + pkgcache.GoCacheProgPath + `.tmp.$$" "` + pkgcache.GoCacheProgPath + `" && \
      mv -f "$src" "` + pkgcache.GoCacheProgPath + `.go" || \
      rm -f "` + pkgcache.GoCacheProgPath + `.tmp.$$" "$src"
  else
    rm -f "$src"
  fi
  if [ ! -x "` + pkgcache.GoCacheProgPath + `" ]; then
    shift 4
    set -- go env -w "$@"
  fi
fi
exec "$@"`

type skipGoCacheProgBuildKey struct{}

// withSkipGoCacheProgBuild returns a context instructing configureTools not
// to compile grain-gocacheprog.go on the cold path when no prebuilt binary is
// present (e.g. for no-repo interactive tasks that do not run Go builds).
func withSkipGoCacheProgBuild(ctx context.Context, skip bool) context.Context {
	if !skip {
		return ctx
	}
	return context.WithValue(ctx, skipGoCacheProgBuildKey{}, true)
}

func skipGoCacheProgBuild(ctx context.Context) bool {
	v, _ := ctx.Value(skipGoCacheProgBuildKey{}).(bool)
	return v
}

// ConfigurePackageCache writes the package-cache configuration into
// sandbox, and reports what it did for a note a caller may want to make.
func ConfigurePackageCache(ctx context.Context, sandbox Sandbox, base string) (configured bool, err error) {
	return configurePackageCache(ctx, sandbox, base, rootUnavailable(ctx, sandbox))
}

// rootUnavailable is SandboxRootless's answer for sandbox, and "" for
// every sandbox that does not implement it.
func rootUnavailable(ctx context.Context, sandbox Sandbox) string {
	if rootless, ok := sandbox.(SandboxRootless); ok {
		return rootless.RootUnavailable(ctx)
	}
	return ""
}

// configurePackageCache is ConfigurePackageCache with the root question
// already asked. A sandbox without root (noRoot non-empty) gets only the
// per-tool commands, which run as its own user anyway, and is reported
// as not configured: what it was given is not the configuration a caller
// announces as "fetches apt, pip, npm and Go packages through the cache".
func configurePackageCache(ctx context.Context, sandbox Sandbox, base, noRoot string) (configured bool, err error) {
	if strings.TrimSpace(base) == "" {
		return false, nil
	}
	commander, ok := sandbox.(SandboxCommander)
	if !ok {
		return false, nil
	}
	if noRoot != "" {
		noteNoRoot(sandbox.Name(), noRoot)
		return false, configureTools(ctx, commander, sandbox.Name(), base)
	}
	files, err := pkgcache.SandboxFiles(base, nil)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		// writeAsRootScript installs the whole file over whatever is
		// there, so a write the transport cut off is run again rather
		// than failing the sandbox's setup.
		_, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
			return runAsRoot(ctx, commander,
				[]string{"sh", "-c", writeAsRootScript, "sh", file.Path, file.Mode}, file.Content)
		})
		if exitCode != 0 {
			return false, fmt.Errorf("writing %s in %s: %s", file.Path, sandbox.Name(), guestCommandDetail(stderr, exitCode))
		}
	}
	if err := configureDockerCache(ctx, commander, sandbox.Name(), base); err != nil {
		return false, err
	}
	if err := configureTools(ctx, commander, sandbox.Name(), base); err != nil {
		return false, err
	}
	return true, nil
}

// configureTools runs pkgcache.SandboxCommands as the sandbox's own user:
// the ecosystems whose configuration belongs to their own tool, and so
// the part of the cache a sandbox without root still gets.
func configureTools(ctx context.Context, commander SandboxCommander, name, base string) error {
	commands, err := pkgcache.SandboxCommands(base)
	if err != nil {
		return err
	}
	script := configureToolScript
	if skipGoCacheProgBuild(ctx) {
		script = "GRAIN_SKIP_GOCACHEPROG_BUILD=1\n" + configureToolScript
	}
	for _, command := range commands {
		_, stderr, exitCode := commander.RunInSandbox(ctx,
			append([]string{"sh", "-c", script, "sh"}, command.Argv...), "")
		if exitCode != 0 {
			log.Printf("orchestrator: pointing %s in %s at this deployment's package cache: %s "+
				"-- that ecosystem will be fetched from the internet instead",
				command.What, name, guestCommandDetail(stderr, exitCode))
		}
	}
	return nil
}

// ConfigurePackageCacheTransparent installs the internal Grain Root CA certificate,
// points /etc/resolv.conf to the controller DNS service, and configures ecosystem files.
func ConfigurePackageCacheTransparent(ctx context.Context, sandbox Sandbox, base string, caPEM []byte, dnsIP string) (configured bool, err error) {
	if strings.TrimSpace(base) == "" && len(caPEM) == 0 && strings.TrimSpace(dnsIP) == "" {
		return false, nil
	}
	if _, ok := sandbox.(SandboxCommander); !ok {
		return false, nil
	}
	commander := sandbox.(SandboxCommander)
	if provisioner, ok := sandbox.(SandboxPackageCacheProvisioner); ok {
		if provisioned, err := configureProvisioned(ctx, provisioner, commander, sandbox.Name(), base, caPEM, dnsIP); provisioned || err != nil {
			return provisioned, err
		}
	}
	// The CA and the resolver go together or not at all, which is why a
	// Kubernetes pod is not given the resolver through its own spec
	// (dnsConfig) when it cannot have the CA: the resolver sends every
	// intercepted registry to the cache, which answers TLS with a
	// certificate only grain's CA vouches for, so a pod trusting the one
	// without the other would fail every HTTPS fetch it now makes.
	if noRoot := rootUnavailable(ctx, sandbox); noRoot != "" {
		if strings.TrimSpace(base) == "" {
			noteNoRoot(sandbox.Name(), noRoot)
		}
		return configurePackageCache(ctx, sandbox, base, noRoot)
	}
	if len(caPEM) > 0 {
		for _, file := range pkgcache.SandboxCAFiles(caPEM) {
			_, stderr, exitCode := runAsRoot(ctx, commander,
				[]string{"sh", "-c", writeAsRootScript, "sh", file.Path, file.Mode}, file.Content)
			if exitCode != 0 {
				log.Printf("orchestrator: writing CA cert %s in %s: %s", file.Path, sandbox.Name(), guestCommandDetail(stderr, exitCode))
			}
		}
		_, stderr, exitCode := runAsRoot(ctx, commander,
			[]string{"sh", "-c", "if [ \"$(id -u)\" = 0 ]; then update-ca-certificates; else sudo -n update-ca-certificates; fi"}, "")
		if exitCode != 0 {
			log.Printf("orchestrator: updating CA certificates in %s: %s", sandbox.Name(), guestCommandDetail(stderr, exitCode))
		}
	}
	if strings.TrimSpace(dnsIP) != "" {
		resolv := pkgcache.SandboxResolvConf(dnsIP)
		_, stderr, exitCode := runAsRoot(ctx, commander,
			[]string{"sh", "-c", writeInPlaceScript, "sh", resolv.Path}, resolv.Content)
		if exitCode != 0 {
			log.Printf("orchestrator: configuring DNS %s in %s: %s -- this sandbox keeps its own "+
				"resolver, so nothing it fetches goes through the package cache transparently",
				resolv.Path, sandbox.Name(), guestCommandDetail(stderr, exitCode))
		}
	}
	return configurePackageCache(ctx, sandbox, base, "")
}

// configureProvisioned finishes configuring a sandbox whose /etc half was
// written when it was created, and reports false -- leaving the caller
// to try the ordinary way -- when what it was created with is not what
// is being asked for now: a cache switched on, or its CA rotated, between
// the pod's creation and this dispatch.
func configureProvisioned(ctx context.Context, provisioner SandboxPackageCacheProvisioner, commander SandboxCommander,
	name, base string, caPEM []byte, dnsIP string) (bool, error) {
	provisionedBase, provisionedCA := provisioner.ProvisionedPackageCache()
	if provisionedBase == "" && len(provisionedCA) == 0 {
		return false, nil
	}
	if provisionedBase != base || !bytes.Equal(provisionedCA, caPEM) {
		log.Printf("orchestrator: %s was created for a different package-cache configuration than this "+
			"dispatch's (the cache was switched, or its CA replaced, in between) -- configuring it as a sandbox built "+
			"without one", name)
		return false, nil
	}
	// /etc/resolv.conf is the kubelet's, not the image's, and has no
	// writer here: the transparent half of the cache that hangs off DNS
	// does not reach a sandbox like this one, and the explicit mirrors
	// above it are what it gets instead.
	if strings.TrimSpace(dnsIP) != "" {
		log.Printf("orchestrator: %s keeps its own resolver -- DNS interception at %s is not configured "+
			"into a sandbox whose /etc is fixed when it is created", name, dnsIP)
	}
	if base != "" {
		if err := configureTools(ctx, commander, name, base); err != nil {
			return false, err
		}
	}
	return true, nil
}

// RunSandboxCommand runs one command inside a sandbox over the same
// transport everything else in this file uses, and reports what it
// printed and what it exited with. A non-zero exit is an answer rather
// than an error, exactly as it is for a CI step; the error is the
// sandbox having no such transport at all.
func RunSandboxCommand(ctx context.Context, sandbox Sandbox, argv []string, stdin string) (stdout string, exitCode int, err error) {
	commander, ok := sandbox.(SandboxCommander)
	if !ok {
		return "", 0, fmt.Errorf("orchestrator: %s cannot be run in over its own transport", sandbox.Name())
	}
	out, stderr, code := commander.RunInSandbox(ctx, argv, stdin)
	if code != 0 && strings.TrimSpace(out) == "" {
		out = stderr
	}
	return out, code, nil
}

// WriteSandboxFile installs one small file inside a sandbox, as root,
// over the same transport everything above uses.
//
// It is here rather than in its caller because writeAsRootScript is:
// "write this file in a machine that may or may not let you be root
// without sudo" is one problem with one answer, and the second caller
// (a CI job being told where this deployment's Actions cache is, in
// cmd/grain's ciSandboxes) wants exactly the answer this file already
// had. Failing to write is an error the caller decides about; both of
// them so far treat it as a warning and carry on.
func WriteSandboxFile(ctx context.Context, sandbox Sandbox, path, mode, content string) error {
	commander, ok := sandbox.(SandboxCommander)
	if !ok {
		return fmt.Errorf("orchestrator: %s cannot be written to over its own transport", sandbox.Name())
	}
	_, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
		return runAsRoot(ctx, commander,
			[]string{"sh", "-c", writeAsRootScript, "sh", path, mode}, content)
	})
	if exitCode != 0 {
		return fmt.Errorf("writing %s in %s: %s", path, sandbox.Name(), guestCommandDetail(stderr, exitCode))
	}
	return nil
}

// readAsRootScript prints one file as root, or nothing when it does not
// exist -- the read half of configureDockerCache's merge.
const readAsRootScript = `set -eu
if [ "$(id -u)" = 0 ]; then
  [ -f "$1" ] && cat "$1" || true
else
  sudo -n sh -c '[ -f "$0" ] && cat "$0" || true' "$1"
fi`

// reloadDockerScript asks a running dockerd to re-read daemon.json, and
// does nothing where there is no systemd or no active docker service.
const reloadDockerScript = `set -eu
run() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo -n "$@"; fi; }
command -v systemctl >/dev/null 2>&1 || exit 0
run systemctl is-active --quiet docker || exit 0
run systemctl reload docker`

// configureDockerCache merges this deployment's registry mirror into the
// sandbox's /etc/docker/daemon.json, keeping whatever else is there, and
// reloads dockerd so it applies without a restart. A failed reload is
// only logged: the mirror is in the file for the next time dockerd
// starts.
func configureDockerCache(ctx context.Context, commander SandboxCommander, name, base string) error {
	existing, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
		return runAsRoot(ctx, commander,
			[]string{"sh", "-c", readAsRootScript, "sh", pkgcache.DockerConfigPath}, "")
	})
	if exitCode != 0 {
		return fmt.Errorf("reading %s in %s: %s", pkgcache.DockerConfigPath, name, guestCommandDetail(stderr, exitCode))
	}
	merged, err := pkgcache.DockerDaemonJSON([]byte(existing), base)
	if err != nil {
		return err
	}
	if _, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
		return runAsRoot(ctx, commander,
			[]string{"sh", "-c", writeAsRootScript, "sh", pkgcache.DockerConfigPath, "0644"}, merged)
	}); exitCode != 0 {
		return fmt.Errorf("writing %s in %s: %s", pkgcache.DockerConfigPath, name, guestCommandDetail(stderr, exitCode))
	}
	if _, stderr, exitCode := runAsRoot(ctx, commander,
		[]string{"sh", "-c", reloadDockerScript, "sh"}, ""); exitCode != 0 {
		log.Printf("orchestrator: %s: its docker daemon did not reload its configuration (%s) -- it will use "+
			"grain's registry mirror the next time it starts", name, guestCommandDetail(stderr, exitCode))
	}
	return nil
}

// PackageCacheSource is where a sandbox learns about this deployment's
// package cache: the three answers Config carries as PackageCache,
// PackageCacheCA and PackageCacheDNS. A nil field answers nothing, and a
// source with all three nil is a deployment running no cache.
type PackageCacheSource struct {
	Base func(context.Context) string
	CA   func(context.Context) []byte
	DNS  func(context.Context) string
}

// PackageCacheSource is this Config's cache, as PrepareSandbox takes it.
func (c Config) PackageCacheSource() PackageCacheSource {
	return PackageCacheSource{Base: c.PackageCache, CA: c.PackageCacheCA, DNS: c.PackageCacheDNS}
}

// SandboxPreparation is what PrepareSandbox did to one sandbox: whether
// its package managers, CA bundle and resolver now go through the cache,
// and why not when they do not.
type SandboxPreparation struct {
	// Base is the cache's address, empty when the deployment gave none.
	Base string
	// Configured is true once the sandbox was pointed at the cache.
	Configured bool
	// Err is why it could not be; the sandbox then fetches from the
	// internet, as every sandbox did before the cache existed.
	Err error
}

// Summary is the one line a log or a status panel says about it.
func (p SandboxPreparation) Summary() string {
	switch {
	case p.Err != nil:
		return fmt.Sprintf("could not be pointed at this deployment's package cache at %s: %v "+
			"-- it fetches its packages from the internet instead", p.Base, p.Err)
	case p.Configured:
		return fmt.Sprintf("fetches apt, pip, npm and Go packages, and Docker Hub images, through this "+
			"deployment's package cache at %s", p.Base)
	default:
		return "fetches its packages from the internet: this deployment runs no package cache, " +
			"or this sandbox cannot be configured for one"
	}
}

// PrepareSandbox is the one step every sandbox grain acquires goes
// through between Acquire and the first command it runs -- a task's, a
// recreated one, a persistent sandbox's and a CI job's -- so a new kind of
// sandbox cannot be built that the cache, its CA and its resolver never
// reach. It never fails the caller: a sandbox that could not be prepared
// still works, against the internet.
func PrepareSandbox(ctx context.Context, src PackageCacheSource, sandbox Sandbox) SandboxPreparation {
	if src.Base == nil && src.CA == nil && src.DNS == nil {
		return SandboxPreparation{}
	}
	var out SandboxPreparation
	if src.Base != nil {
		out.Base = src.Base(ctx)
	}
	var caPEM []byte
	if src.CA != nil {
		caPEM = src.CA(ctx)
	}
	var dnsIP string
	if src.DNS != nil {
		dnsIP = src.DNS(ctx)
	}
	out.Configured, out.Err = ConfigurePackageCacheTransparent(ctx, sandbox, out.Base, caPEM, dnsIP)
	if out.Err != nil {
		out.Configured = false
	}
	return out
}

// applyPackageCache is the whole of what a dispatch does about the
// cache: PrepareSandbox, logged rather than failed.
func applyPackageCache(ctx context.Context, cfg Config, sandbox Sandbox, taskID string) {
	prep := PrepareSandbox(ctx, cfg.PackageCacheSource(), sandbox)
	if prep.Err != nil || prep.Configured {
		log.Printf("orchestrator: task %s: its sandbox %s", taskID, prep.Summary())
	}
}
