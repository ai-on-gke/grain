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

package cli

// This deployment's package cache: listeners serving pkg/pkgcache -- apt,
// PyPI, npm, Go modules, and Docker/OCI registries -- to whatever is running
// inside a sandbox.
//
// In transparent caching mode:
// 1. An internal Root CA dynamically issues certificates on HTTPS (port 443).
// 2. An internal DNS service intercepts package & dependency domains and points them to the controller.
// 3. Sandboxes are configured with the Root CA and DNS nameserver.

import (
	"flag"
	"fmt"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

type packageCacheFlags struct {
	gb             *int
	dir            *string
	aptUpstream    stringSliceFlag
	pypiIndex      *string
	npmRegistry    *string
	goProxy        *string
	dockerRegistry *string
	ghcrRegistry   *string
	gcrRegistry    *string
}

// registerPackageCacheFlags declares the -package-cache-* flags on fs; read
// them through apply once fs has parsed.
func registerPackageCacheFlags(fs *flag.FlagSet) *packageCacheFlags {
	f := &packageCacheFlags{}
	// A flag rather than a setting, unlike the switch that decides
	// whether sandboxes use the cache at all (Settings' own "Package
	// cache"): this is how much of *this host's* disk the daemon may
	// spend, which is the same kind of fact as -data-dir and belongs
	// with the deployment rather than in a row a UI edits.
	f.gb = fs.Int("package-cache-size-gb", int(pkgcache.DefaultMaxBytes>>30),
		"how much disk, in GiB, this deployment's package cache may use before it starts evicting the "+
			"packages it fetched longest ago. 0 leaves it at the default")
	f.dir = fs.String("package-cache-dir", "",
		"root directory for the package cache -- defaults to <sandbox-dir>/cache/packages when -sandbox-dir is set, and <data-dir>/cache/packages otherwise")
	// Where the cache fills itself from, for the installation that
	// already has a mirror. Flags for the same reason -package-cache-dir
	// and -package-cache-size-gb are, and not for the reason the switch
	// beside them is a stored setting: which mirror this host can reach
	// is a fact about the network this deployment was installed on --
	// the same kind of fact as -data-dir or -container-git-proxy-host --
	// rather than a decision anybody makes per task. They are also read
	// once, when the cache's listeners come up: pkgcache.Server takes
	// its upstreams at construction and has no live path, so a row in
	// Settings would be a control that appears to work and takes effect
	// on the next restart, which is worse than a flag that says what it
	// is.
	//
	// Nothing about a *sandbox* changes when one of these is set. A
	// sandbox goes on resolving and asking for deb.debian.org,
	// pypi.org and registry-1.docker.io, and the files that point it
	// here (pkgcache.SandboxFiles) name those same hosts; this is only
	// where the cache itself goes on a miss.
	fs.Var(&f.aptUpstream, "package-cache-apt-upstream",
		"fetch one intercepted archive from a mirror instead of from itself, as \"archive-host=base-url\" "+
			"(deb.debian.org=http://mirror.internal/debian) -- repeat for each. The host is one this cache "+
			"already serves, and is what sandboxes go on asking for; the URL is what this service fetches "+
			"from, with the request's own path appended. An archive not named here is fetched from itself")
	f.pypiIndex = fs.String("package-cache-pypi-index", "",
		"the PyPI simple index this cache proxies, for a deployment running a mirror of its own "+
			"(http://mirror.internal/pypi/simple). Defaults to https://pypi.org/simple")
	f.npmRegistry = fs.String("package-cache-npm-registry", "",
		"the npm registry this cache proxies, for a deployment running a mirror of its own "+
			"(http://mirror.internal/npm). Defaults to https://registry.npmjs.org")
	f.goProxy = fs.String("package-cache-go-proxy", "",
		"the Go module proxy this cache forwards to, for a deployment running an Athens of its own "+
			"(http://athens.internal). Defaults to https://proxy.golang.org")
	f.dockerRegistry = fs.String("package-cache-docker-registry", "",
		"the registry this cache mirrors for Docker Hub, for a deployment running a pull-through cache of "+
			"its own (https://registry.internal). Defaults to https://registry-1.docker.io")
	f.ghcrRegistry = fs.String("package-cache-ghcr-registry", "",
		"the registry this cache mirrors for GitHub Container Registry, for a deployment running a "+
			"pull-through cache of its own (https://registry.internal/ghcr). Defaults to https://ghcr.io")
	f.gcrRegistry = fs.String("package-cache-gcr-registry", "",
		"the registry this cache mirrors for Google Container Registry, for a deployment running a "+
			"pull-through cache of its own (https://registry.internal/gcr). Defaults to https://gcr.io")
	return f
}

func (f *packageCacheFlags) apply(cfg *daemon.Config) {
	cfg.PackageCacheDir = *f.dir
	cfg.PackageCacheGB = *f.gb
	cfg.PackageCacheAptUpstream = f.aptUpstream
	cfg.PackageCachePyPIIndex = *f.pypiIndex
	cfg.PackageCacheNPMRegistry = *f.npmRegistry
	cfg.PackageCacheGoProxy = *f.goProxy
	cfg.PackageCacheDockerRegistry = *f.dockerRegistry
	cfg.PackageCacheGHCRRegistry = *f.ghcrRegistry
	cfg.PackageCacheGCRRegistry = *f.gcrRegistry
}

// checkPackageCacheUpstreams reports what is wrong with the mirrors a
// command line named, for checkConfig.
//
// A start is refused over each of these rather than logged, because
// every one of them is a deployment that would run with a cache
// quietly fetching from somewhere other than where it was told to: a
// malformed URL cannot be fetched from at all, and an archive host this
// service does not serve is a mirror nothing will ever ask for. Both
// are settings whose only symptom is traffic that does not arrive.
func checkPackageCacheUpstreams(cfg daemon.Config) error {
	for _, entry := range cfg.PackageCacheAptUpstream {
		host, base, ok := strings.Cut(entry, "=")
		host = strings.TrimSpace(host)
		if !ok || host == "" {
			return fmt.Errorf("-package-cache-apt-upstream %q is not an \"archive-host=base-url\" pair "+
				"(for example deb.debian.org=http://mirror.internal/debian)", entry)
		}
		if err := checkUpstreamURL("-package-cache-apt-upstream "+host, base); err != nil {
			return err
		}
		if !pkgcache.ServesAptHost(host, nil) {
			return fmt.Errorf("-package-cache-apt-upstream names %s, which this cache does not serve, so "+
				"nothing would ever be fetched through that mirror -- the hosts it serves are: %s",
				host, strings.Join(pkgcache.DefaultAptHosts, ", "))
		}
	}
	for _, named := range []struct{ flag, value string }{
		{"-package-cache-pypi-index", cfg.PackageCachePyPIIndex},
		{"-package-cache-npm-registry", cfg.PackageCacheNPMRegistry},
		{"-package-cache-go-proxy", cfg.PackageCacheGoProxy},
		{"-package-cache-docker-registry", cfg.PackageCacheDockerRegistry},
		{"-package-cache-ghcr-registry", cfg.PackageCacheGHCRRegistry},
		{"-package-cache-gcr-registry", cfg.PackageCacheGCRRegistry},
	} {
		if err := checkUpstreamURL(named.flag, named.value); err != nil {
			return err
		}
	}
	return nil
}

// checkUpstreamURL accepts an empty value -- which is every deployment
// that names no mirror -- and otherwise insists on an absolute http or
// https URL with a host in it, since that is the whole of what the
// fetching half does with one: it appends a path and asks for it.
func checkUpstreamURL(what, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s %q is not a URL: %w", what, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s %q is not an http:// or https:// URL with a host in it, and this cache "+
			"fetches by appending a path to it", what, raw)
	}
	return nil
}

// checkConfig validates the flags parsed into cfg (daemon.CheckDaemonFlags plus
// the CLI's own package-cache upstream syntax check).
func checkConfig(cfg daemon.Config) error {
	if err := daemon.CheckDaemonFlags(cfg, daemon.LocalSandboxes != nil, checkPackageCacheUpstreams); err != nil {
		return err
	}
	return checkHubConfig(cfg)
}
