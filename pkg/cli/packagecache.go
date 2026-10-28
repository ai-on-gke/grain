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
	"fmt"
	"net/url"
	"strings"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/pkgcache"
)

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
