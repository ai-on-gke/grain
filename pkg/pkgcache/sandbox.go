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

package pkgcache

// What a sandbox is given so that its package managers use this cache.
//
// In transparent proxy mode, sandboxes are configured with:
// 1. The internal Grain Root CA certificate installed in /usr/local/share/ca-certificates/grain-ca.crt
//    and registered via `update-ca-certificates`.
// 2. DNS pointed to the controller DNS service at 172.17.0.1 (/etc/resolv.conf),
//    transparently resolving standard upstream domains for Debian, Ubuntu, PyPI,
//    npm, Go modules, and Docker/OCI registries.
//
// Legacy tool-specific files and commands (.npmrc, pip.conf, etc.) are also retained
// for backward compatibility.
//
// Both files are deliberately additive and narrow: the apt one names a
// proxy per archive host rather than globally, so a repository a run
// adds for itself is fetched directly and cannot be broken by this; the
// pip one sets an index URL, which is the only way to put a cache in
// front of pip at all.
//
// docker's is the third and is not written the same way, because
// /etc/docker/daemon.json is a file the sandbox image already has
// something in (the guest image sets `bip` and `default-address-pools`
// there, and overwriting that would put every guest's docker bridge back
// on a colliding subnet). DockerDaemonJSON merges into what is already
// there instead -- see its own comment, and orchestrator's
// configureDockerCache for the read-merge-write that calls it.
//
// The last two, npm and Go, are pointed at the cache by a *command*
// rather than by a file (SandboxCommands), for docker's reason taken one
// step further. `go env -w` and `npm config set` each own a
// configuration file that already has things in it -- this deployment's
// own guest writes GOTOOLCHAIN into one and three npm settings into the
// other (the repository's Granulefile) -- and where that file lives
// depends on the account and on where the toolchain was installed:
// npm's global config here is under /usr/local/lib/nodejs, not
// /etc/npmrc. Asking the tool to record the setting is how a line is
// added to a file grain does not own, in the place that tool will
// actually read it, without throwing away what was already there.
//
// One file in the set is not for the sandbox at all:
// ChildContainerEnvPath holds the per-ecosystem settings a *container*
// this sandbox builds or runs can be pointed here with. Nothing applies
// it -- a container inherits none of the above, and there is no way to
// reach into a Dockerfile grain has never read -- so it is a file a run
// names on a `docker run --env-file` or turns into `--build-arg`s. See
// ChildContainerEnvFile, and the comment above it on why the one thing
// that *would* apply itself (a default proxy in ~/.docker/config.json)
// is not written here.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	// CACertPath is where the internal Grain Root CA certificate is installed.
	CACertPath = "/usr/local/share/ca-certificates/grain-ca.crt"
	// ResolvConfPath is the system DNS configuration file.
	ResolvConfPath = "/etc/resolv.conf"
	// AptConfigPath is where apt's half lands. The 01- prefix puts it
	// early in the apt.conf.d ordering, so a deployment or a run that
	// wants to override it can, with any file sorting after it.
	AptConfigPath = "/etc/apt/apt.conf.d/01-grain-package-cache"
	// PipConfigPath is pip's system-wide configuration file. Not the
	// per-user ~/.config/pip/pip.conf: a run that installs something as
	// root, which is most of them, would not read that one.
	PipConfigPath = "/etc/pip.conf"
	// DockerConfigPath is dockerd's configuration file. There is no
	// drop-in directory for it -- dockerd reads this one file -- which is
	// why this half is a merge rather than one more file dropped beside
	// the others.
	DockerConfigPath = "/etc/docker/daemon.json"
	// GHCRHostsConfigPath is dockerd's / containerd's registry hosts
	// configuration for GitHub Container Registry (ghcr.io).
	GHCRHostsConfigPath = "/etc/docker/certs.d/ghcr.io/hosts.toml"
	// GCRHostsConfigPath is dockerd's / containerd's registry hosts
	// configuration for Google Container Registry (gcr.io).
	GCRHostsConfigPath = "/etc/docker/certs.d/gcr.io/hosts.toml"
	// ChildContainerEnvPath is where the per-ecosystem settings a
	// *container* can be pointed at this cache with are left, in the
	// KEY=value shape `docker run --env-file` and `docker build
	// --build-arg` both read. Nothing in the sandbox reads it: it is
	// there for a run that builds or runs a container and wants that
	// container's pip, npm and go to come through here too. See
	// ChildContainerEnvFile.
	ChildContainerEnvPath = "/etc/grain/package-cache.env"
)

// SandboxFile is one file to write into a sandbox.
type SandboxFile struct {
	Path    string
	Content string
	Mode    string
}

// SandboxCAFiles returns the files required to install the internal Grain Root CA into a sandbox.
func SandboxCAFiles(caPEM []byte) []SandboxFile {
	if len(caPEM) == 0 {
		return nil
	}
	return []SandboxFile{
		{
			Path:    CACertPath,
			Content: string(caPEM),
			Mode:    "0644",
		},
	}
}

// SandboxResolvConf returns a SandboxFile pointing /etc/resolv.conf to dnsIP.
func SandboxResolvConf(dnsIP string) SandboxFile {
	return SandboxFile{
		Path:    ResolvConfPath,
		Content: "nameserver " + dnsIP + "\n",
		Mode:    "0644",
	}
}

// SandboxFiles is the configuration that points one sandbox's apt,
// pip, and OCI registry mirrors (ghcr.io, gcr.io) at the cache reachable
// at baseURL ("http://host:port"), given the archive hosts the apt half
// will serve (Server's own list -- pass nil for the default), plus the
// one file that is not for the sandbox itself: the child-container
// settings at ChildContainerEnvPath, which a run hands to a `docker
// build` or `docker run` of its own.
//
// None of it changes when a deployment points this cache at a mirror of
// its own (Config's upstream fields, and `grain daemon`'s
// -package-cache-* flags). What is written here is the hosts a sandbox
// *asks for* -- the archives it proxies, the index its pip reads -- and
// those are still Debian's and PyPI's own names whoever answers them:
// the substitution happens one hop further out, between this service and
// the mirror, where nothing in the sandbox can see it. So aptHosts is
// the served list and never the upstream one, and a mirror named for a
// host that is not in it would be dead configuration -- which is why
// naming one is refused at startup (ServesAptHost).
//
// It fails rather than guessing when baseURL is not a usable HTTP
// address: everything downstream of this writes files into a sandbox,
// and a sandbox told to fetch its packages from a malformed address is
// one where every install fails with a message about this deployment
// rather than about the package.
func SandboxFiles(baseURL string, aptHosts []string) ([]SandboxFile, error) {
	addr, err := cacheHost(baseURL)
	if err != nil {
		return nil, err
	}
	if len(aptHosts) == 0 {
		aptHosts = DefaultAptHosts
	}
	root := "http://" + addr + "/"

	var apt strings.Builder
	apt.WriteString("// Written by grain: this deployment runs a package cache, and these\n")
	apt.WriteString("// archives are fetched through it so that every sandbox after this one\n")
	apt.WriteString("// gets the same packages without downloading them again. Repositories\n")
	apt.WriteString("// not named here are fetched directly, exactly as they would be with\n")
	apt.WriteString("// no cache at all. An https:// source for an archive named here is sent\n")
	apt.WriteString("// DIRECT rather than through the proxy below, because that is the route\n")
	apt.WriteString("// that gets cached: the proxy is plain HTTP, so apt would reach it with a\n")
	apt.WriteString("// CONNECT, and a tunnel is opaque -- the cache opens one to an archive it\n")
	apt.WriteString("// serves rather than failing the fetch, but it can store nothing out of\n")
	apt.WriteString("// it. DIRECT instead resolves through this sandbox's DNS, which points\n")
	apt.WriteString("// these hosts at the cache's own HTTPS listener. For the same reason, a\n")
	apt.WriteString("// blanket http_proxy or https_proxy is never how to reach this service:\n")
	apt.WriteString("// it caches an allowlist of archives, not the internet. Turn the cache\n")
	apt.WriteString("// off in grain's Settings to stop this file being written.\n")
	for _, host := range aptHosts {
		if strings.HasPrefix(host, ".") {
			continue
		}
		fmt.Fprintf(&apt, "Acquire::http::Proxy::%s \"%s\";\n", host, root)
		fmt.Fprintf(&apt, "Acquire::https::Proxy::%s \"DIRECT\";\n", host)
	}

	pip := "# Written by grain: this deployment runs a PyPI cache, and pip is\n" +
		"# pointed at it so that a wheel downloaded by one run is not downloaded\n" +
		"# again by the next. It proxies pypi.org -- the packages, versions and\n" +
		"# hashes are PyPI's own, and pip still verifies each artifact against\n" +
		"# the hash the index gave it. Turn the cache off in grain's Settings to\n" +
		"# stop this file being written.\n" +
		"[global]\n" +
		"index-url = http://" + addr + pypiSimplePrefix + "\n" +
		"trusted-host = " + addr + "\n"

	ghcrHosts := "# Written by grain: package cache mirror for ghcr.io\n" +
		"server = \"https://ghcr.io\"\n\n" +
		"[host.\"http://" + addr + "/v2/ghcr\"]\n" +
		"  capabilities = [\"pull\", \"resolve\"]\n" +
		"  override_path = true\n"

	gcrHosts := "# Written by grain: package cache mirror for gcr.io\n" +
		"server = \"https://gcr.io\"\n\n" +
		"[host.\"http://" + addr + "/v2/gcr\"]\n" +
		"  capabilities = [\"pull\", \"resolve\"]\n" +
		"  override_path = true\n"

	childEnv, err := ChildContainerEnvFile(baseURL)
	if err != nil {
		return nil, err
	}

	return []SandboxFile{
		{Path: AptConfigPath, Content: apt.String(), Mode: "0644"},
		{Path: PipConfigPath, Content: pip, Mode: "0644"},
		{Path: GHCRHostsConfigPath, Content: ghcrHosts, Mode: "0644"},
		{Path: GCRHostsConfigPath, Content: gcrHosts, Mode: "0644"},
		childEnv,
	}, nil
}

// DockerDaemonJSON merges the cache mirror address into dockerd's daemon.json.
func DockerDaemonJSON(existing []byte, baseURL string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme != "http" || base.Host == "" {
		return "", fmt.Errorf("pkgcache: %q is not an http:// address with a host in it, so no sandbox's docker can be pointed at it", baseURL)
	}
	doc := map[string]any{}
	if trimmed := strings.TrimSpace(string(existing)); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return "", fmt.Errorf("pkgcache: %s is already there and is not JSON this can add a mirror to: %w", DockerConfigPath, err)
		}
	}
	doc["registry-mirrors"] = prependUnique(doc["registry-mirrors"], "http://"+base.Host)
	doc["insecure-registries"] = prependUnique(doc["insecure-registries"], base.Host)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("pkgcache: writing %s: %w", DockerConfigPath, err)
	}
	return string(out) + "\n", nil
}

// Why there is no ~/.docker/config.json half here, though there was one
// between 2026-09-18 and this comment:
//
// The docker client can be given a default proxy (proxies.default.httpProxy)
// which it passes into every container it builds or runs as HTTP_PROXY and
// http_proxy, and pointing that at this cache looks like the way to make a
// `RUN apt-get install` in somebody's Dockerfile use it too. It is not,
// because that setting is *global* where everything else here is per-host:
// a container gets one proxy for the whole internet, and this service is an
// allowlisted archive cache that speaks plain HTTP and nothing else. Both
// halves of that broke ordinary builds within hours of it being written:
//
//   - an `https://deb.debian.org` source inside a container became a
//     CONNECT through the cache, which it refused outright (405), and
//   - any plain-HTTP fetch of a host that is not an archive -- the rest of
//     the internet -- became a 403 from an allowlist the Dockerfile's author
//     never heard of.
//
// The first of those is no longer fatal: a CONNECT naming a host this
// service serves is tunnelled to it uncached (connect.go), so an https
// archive behind a blanket proxy is slow rather than broken. The second
// stands, and is enough on its own -- a container's build does not only
// fetch archives, and a proxy is one setting for every host it fetches
// from.
//
// A container's apt cannot be given the per-host configuration SandboxFiles
// writes, because grain does not own the image it runs in. So a container's
// apt fetches its own packages, as it did before, and the cache serves the
// sandbox around it. ChildContainerEnv below is the shape that does work:
// per-ecosystem settings naming this service's own endpoints, which say
// nothing about any other host.
//
// The CONNECT half of connect.go is not a step towards putting the proxy
// back. It holds a tunnel to the same allowlist an absolute-form GET is held
// to, on port 443 only, so what it adds to a model.NetworkNone sandbox
// is a TLS connection to a distribution archive it could already reach in
// plaintext -- and it exists for the clients grain cannot configure, not as
// an invitation to point one at every host a build touches.
//
// And why npm and pip below are given no fallback, though GOPROXY has one
// (asked and decided 2026-09-18, because it is the same shape as the above
// one level down: a container whose fetch this service cannot answer fails
// outright rather than going direct).
//
// Accepted deliberately, kept as it is:
//
//   - `,direct` is not a privilege Go was granted here. It is the go
//     command's own default value, and `direct` means "clone the module from
//     the host in its path", which is a route the module protocol defines and
//     the other two ecosystems have no equivalent of. Writing GOPROXY without
//     it would be *removing* a fallback Go has by default; there is nothing
//     to remove or add for the others.
//   - npm has no second registry to fall back to. NPM_CONFIG_REGISTRY is one
//     URL, so the only way to give npm a fallback is for this service to
//     become the thing that falls back -- to answer for registry.npmjs.org
//     when it cannot reach it, which is a mirror's job and not a cache's.
//   - pip's nearest equivalent, PIP_EXTRA_INDEX_URL, is not a fallback: it is
//     a second index consulted for *every* package, with the highest version
//     across both winning. Pointing it at pypi.org would trade a failure when
//     the cache is down for a resolution that silently depends on which index
//     answered first, which is the worse of the two.
//   - The sharpness is already the sandbox's own, and a container cannot be
//     made more robust than the run that builds it. SandboxFiles writes
//     /etc/pip.conf and apt.conf with no fallback either, so a cache that is
//     down fails the sandbox's own installs; a container that survived it
//     would be surviving inside a run that had not.
//   - Unlike everything else here, these settings are opt-in. Nothing applies
//     them: a run names the file on its own `docker run --env-file`, so the
//     escape hatch for a build that must not depend on this service is not
//     passing it -- which is also what makes accepting the sharpness cheap.
//
// What would change this decision is the cache becoming something a run
// cannot choose to bypass, or npm and pip growing a real "try this, then the
// default" setting. Neither is true today.

// ChildContainerEnv is what points a *container's* package managers at the
// cache at baseURL: PIP_INDEX_URL and PIP_TRUSTED_HOST for Python,
// NPM_CONFIG_REGISTRY for npm, GOPROXY for Go modules.
//
// A container inherits none of the sandbox's own configuration -- the files
// SandboxFiles writes are in an /etc that is not the container's, and the
// image is one grain does not own -- so these have to be handed in by
// whoever runs it. Environment variables rather than files for exactly that
// reason: `docker run -e`/`--env-file` and `docker build --build-arg` are
// the two ways into a container that need no cooperation from the image.
//
// Each name here is the one ecosystem's own setting, pointed at this
// service's endpoint for it, and says nothing about any other host -- which
// is what makes it safe to hand to a build whose Dockerfile grain has never
// read. There is deliberately no HTTP_PROXY among them and no apt setting at
// all, and PIP_INDEX_URL and NPM_CONFIG_REGISTRY deliberately have no
// fallback where GOPROXY has `,direct`: see the comment above, and
// docs/package-cache.md.
func ChildContainerEnv(baseURL string) (map[string]string, error) {
	addr, err := cacheHost(baseURL)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"PIP_INDEX_URL":       "http://" + addr + pypiSimplePrefix,
		"PIP_TRUSTED_HOST":    addr,
		"NPM_CONFIG_REGISTRY": "http://" + addr + npmPrefix,
		"GOPROXY":             goProxyValue(addr),
	}, nil
}

// ChildContainerEnvFile is ChildContainerEnv written out as the file a
// sandbox gets at ChildContainerEnvPath -- the one thing that carries those
// settings from the controller to the container a run builds.
//
// The format is `docker run --env-file`'s, which is also `docker compose`'s
// `env_file` and is a `sed` away from a list of `--build-arg` flags: one
// KEY=value per line, blank lines and `#` comments ignored. So the file is
// both the values and the instructions for using them, which matters because
// this is the half of the package cache nothing applies on a run's behalf --
// a `docker build` grain never sees is the only thing that can pass it in.
//
// Why a file at all, rather than exporting the four into the sandbox's own
// environment: the sandbox's tools are already pointed here by
// SandboxFiles and SandboxCommands, so an exported GOPROXY would be a second
// copy of a setting that is already right, and the shells a run opens hours
// apart inherit nothing from each other anyway (the whole reason the rest of
// this is files). What a run needs is a stable path it can name in a command
// line, and this is it.
//
// The keys are written in sorted order so that a sandbox configured twice --
// which the recreate path does -- gets the same bytes both times.
func ChildContainerEnvFile(baseURL string) (SandboxFile, error) {
	env, err := ChildContainerEnv(baseURL)
	if err != nil {
		return SandboxFile{}, err
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# Written by grain: this deployment runs a package cache, and these are the\n")
	b.WriteString("# settings that point a container's own package managers at it. A container\n")
	b.WriteString("# inherits nothing from the sandbox around it -- grain does not own the image\n")
	b.WriteString("# it runs, so it cannot write the per-host configuration this sandbox has in\n")
	b.WriteString("# /etc/apt and /etc/pip.conf -- so they have to be passed in:\n")
	b.WriteString("#\n")
	b.WriteString("#   docker run --env-file " + ChildContainerEnvPath + " ...\n")
	b.WriteString("#   docker build $(grep -v '^#' " + ChildContainerEnvPath + " | grep . | sed 's/^/--build-arg /') .\n")
	b.WriteString("#\n")
	b.WriteString("# The build form needs the Dockerfile to declare a matching `ARG` for each name\n")
	b.WriteString("# it wants to use; a --build-arg it does not declare is a warning and nothing\n")
	b.WriteString("# more, so passing all four at a Dockerfile that reads one is fine.\n")
	b.WriteString("#\n")
	b.WriteString("# There is deliberately no HTTP_PROXY here, and no apt setting. Every name\n")
	b.WriteString("# below is one ecosystem's own endpoint on this service and says nothing about\n")
	b.WriteString("# any other host; a proxy would be one setting for the whole internet, and this\n")
	b.WriteString("# service is an allowlisted, plain-HTTP archive cache that would refuse most of\n")
	b.WriteString("# it. A container's `apt-get install` downloads its packages itself, as it does\n")
	b.WriteString("# with no cache at all. See docs/package-cache.md.\n")
	b.WriteString("#\n")
	b.WriteString("# GOPROXY ends in `,direct` because that is the go command's own default: a\n")
	b.WriteString("# module this cache cannot serve is fetched from its own host. PIP_INDEX_URL\n")
	b.WriteString("# and NPM_CONFIG_REGISTRY have no equivalent -- neither pip nor npm has a\n")
	b.WriteString("# second index to fall back to -- so a build given them while this service is\n")
	b.WriteString("# down fails rather than going direct, exactly as the sandbox's own pip would.\n")
	b.WriteString("# A build that must not depend on this deployment's cache should not be given\n")
	b.WriteString("# this file: nothing applies it on your behalf.\n")
	for _, name := range names {
		b.WriteString(name + "=" + env[name] + "\n")
	}
	return SandboxFile{Path: ChildContainerEnvPath, Content: b.String(), Mode: "0644"}, nil
}

// prependUnique puts value at the head of a JSON list already in the
// file, dropping any copy of it further down. Anything in that field
// that is not a list of strings is replaced: dockerd would reject it, and
// a sandbox whose docker will not start is worse than one that lost a
// setting nothing could have been using.
func prependUnique(field any, value string) []string {
	out := []string{value}
	items, ok := field.([]any)
	if !ok {
		return out
	}
	for _, item := range items {
		if s, ok := item.(string); ok && s != value {
			out = append(out, s)
		}
	}
	return out
}

// cacheHost parses baseURL and returns its host:port for use in proxy URLs.
func cacheHost(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("pkgcache: %q is not a usable URL: %w", baseURL, err)
	}
	if u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("pkgcache: %q is not an http:// address with a host in it, so no sandbox can be pointed at it", baseURL)
	}
	return u.Host, nil
}

// SandboxCommand is one command to run inside a sandbox.
type SandboxCommand struct {
	What string
	Argv []string
}

// SandboxCommands returns the commands that point one sandbox's npm
// and go toolchains at baseURL ("http://host:port").
func SandboxCommands(baseURL string) ([]SandboxCommand, error) {
	addr, err := cacheHost(baseURL)
	if err != nil {
		return nil, err
	}
	return []SandboxCommand{
		{
			What: "Go package cache proxy",
			Argv: []string{
				"go", "env", "-w",
				"GOCACHEPROG=" + goCacheProgValue(addr),
				"GOPROXY=" + goProxyValue(addr),
			},
		},
		{
			What: "npm package cache registry",
			Argv: []string{
				"npm", "config", "set",
				"registry=" + "http://" + addr + npmPrefix,
				"audit=false",
			},
		},
	}, nil
}
