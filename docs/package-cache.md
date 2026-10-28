# The proxy cache

**One feature, two names.** Everything a reader meets calls this the
*proxy cache*: the page in the sidebar, the switch and the size field on
Settings → Sandbox, and this document. The code, the stored settings and
the HTTP API still call it the package cache — `pkg/pkgcache`,
`packageCacheEnabled` and `packageCacheSizeGb`, `GET /api/package-cache`,
`/etc/grain/package-cache.env`, and this file's own name. Those are an
API and a state repository rather than prose, and renaming them would
break every deployment's stored configuration for one word; so they
stay, and the words in front of an operator are the ones that were
unified (grain/task-328). Both names are the same thing seen from its
two ends: what it *holds* is packages, and what it *is*, to a sandbox,
is the apt/pip/npm/Go/Docker proxy every one of them is pointed at.

Every sandbox Grain builds starts from the same image and then downloads
the same packages again. A deployment dispatching a few dozen runs a day
pays for `apt-get install ripgrep` a few dozen times, and for
`docker pull python:3.12` a few dozen times, from the internet — and a
sandbox with no network of its own (`networkAccess: none`) cannot install
or pull anything at all.

Grain's controller runs a transparent caching proxy in front of all of it:
an internal DNS service (`172.17.0.1:53`) and an HTTP/HTTPS caching service
backed by an internal Root CA. Sandboxes have their `/etc/resolv.conf` pointed
to the controller's DNS server and trust the Grain Root CA certificate, so
standard package managers (`apt`, `pip`, `docker`, `npm`, `go`) and child
Docker containers transparently fetch and cache packages through the
controller without requiring custom per-tool proxy configurations.

## Architecture: Transparent DNS & TLS Interception

1. **Controller DNS Service (`172.17.0.1:53`)**:
   - Intercepts package repository hostnames (`deb.debian.org`, `dl-cdn.alpinelinux.org`,
     `registry.npmjs.org`, `registry.yarnpkg.com`, `pypi.org`, `files.pythonhosted.org`,
     `pypi.python.org`, `proxy.golang.org`, `sum.golang.org`, `registry-1.docker.io`,
     `ghcr.io`, `gcr.io`, `quay.io`, `registry.k8s.io`, `k8s.gcr.io`, `pkg.dev`,
     `download.docker.com`, `pkgs.tailscale.com`, `pkgs.k8s.io`, `deb.nodesource.com`,
     `packages.microsoft.com`, `ppa.launchpadcontent.net`, `github.com`, `codeload.github.com`,
     `raw.githubusercontent.com`, `objects.githubusercontent.com`, etc.) and resolves them to the
     controller IP (`172.17.0.1`).
   - Forwards all other DNS queries to upstream resolvers.
   - In zero-egress sandboxes, returns `NXDOMAIN` for unapproved external domains.

2. **Internal Root CA & Dynamic TLS Certificates**:
   - The controller generates an internal Grain Root CA certificate.
   - Pre-installed in the sandbox at `/usr/local/share/ca-certificates/grain-ca.crt`
     and trusted system-wide via `update-ca-certificates`.
   - The proxy cache dynamically issues TLS certificates for intercepted SNI
     domains, allowing transparent HTTPS caching without opaque CONNECT tunneling.

3. **Seamless Guest & Container Experience**:
   - Standard upstream URLs are used everywhere.
   - Child containers, nested VMs, and custom scripts automatically benefit from
     package caching with zero per-tool config files needed.

## What interception needs from the host, and what it does without it

Interception is three listeners on the address sandboxes reach this host
at (`172.17.0.1` by default — the git proxy's host, `-kontur-git-proxy-host`
and friends), and it runs only when all three are up:

| Port | What it is for |
| --- | --- |
| 53 (UDP and TCP) | the resolver that answers the archive and registry hostnames with this address |
| 443 | where the fetch that follows arrives, under a certificate the internal CA issues for the name the client asked for |
| 80 | the same, for a plain-`http://` source |

All three are privileged, and the daemon runs as an unprivileged account,
so all three rest on `CAP_NET_BIND_SERVICE` — granted unconditionally by
both of `scripts/setup.sh`'s installer modes (`AmbientCapabilities=` in
host mode, `--cap-add NET_BIND_SERVICE` plus the image's file capability
on the `grain` binary in container mode). It used to be granted only for a
`-ui-addr` below 1024, which is why a deployment whose UI sits on a high
port intercepted nothing at all, silently, and fetched every package the
explicit way instead.

A deployment that cannot bind them says so once, at startup, naming the
address it could not have:

```
grain daemon: package cache: intercepting DNS on 172.17.0.1:53, serving what it
  redirects on 172.17.0.1:443 and 172.17.0.1:80 -- sandboxes resolve package and
  registry domains to 172.17.0.1 and are fetched through this cache under its own CA
```

and, when it could not:

```
grain daemon: package cache: binding the DNS interception server on 172.17.0.1:53:
  listen udp 172.17.0.1:53: bind: permission denied -- sandboxes keep their own
  resolver and fetch from the internet, and no transparent caching happens
  (port 53 is privileged: the daemon needs CAP_NET_BIND_SERVICE)
```

Either way no sandbox is harmed. A sandbox is pointed at the resolver only
when the listeners that resolver's answers lead to are actually there:
`/etc/resolv.conf` is replaced rather than added to, so a sandbox sent to a
nameserver nobody answers on would have no name resolution at all, and one
told `deb.debian.org` is at an address with nothing on 443 could not fetch
a package at all. Without interception every sandbox keeps its own
resolver and still reaches the cache the explicit way — the apt proxy, the
pip index, the npm and Go registries, and the containerd registry mirrors
— which is how apt has been cached here all along.

`/etc/resolv.conf` is replaced over exec, which needs the guest up and
answering — so there is a window, between a granule VM's own boot and
that exec reaching it, where the guest resolves through whatever
`/etc/resolv.conf` its image shipped with rather than through this cache.
The granule backend closes most of that window instead of only
documenting it: when the resolver is actually serving (the same
condition above), `GranuleConfig.DNS` names it on the VM's own
`-dns`, which lands on the guest's kernel command line and is applied by
its `granule-configure-dns` in early boot, before granule ever gets a
shell inside it. A deployment with the cache off, or whose resolver is
not serving, creates VMs exactly as it did before this existed.

Nothing else on the host may hold 443 or 80 *on that address*; a `-ui-addr`
of `0.0.0.0:80` does, and turns interception off with the message above.
The loopback default (`127.0.0.1:80`) does not.

## What it caches, and what it deliberately does not

| Fetched through the cache | Stored |
| --- | --- |
| `.deb`/`.udeb`/`.ddeb` files under Debian/Ubuntu `pool/` and third-party mirrors | yes |
| `.apk` package files for Alpine Linux | yes |
| `InRelease`, `Release`, `Packages.gz`, `APKINDEX.tar.gz` and their signatures | no — forwarded every time |
| wheels and sdists on `files.pythonhosted.org` | yes |
| PyPI simple-index pages | no — forwarded every time |
| Docker Hub, GHCR (`ghcr.io`), GCR (`gcr.io`), Quay (`quay.io`), Kubernetes (`registry.k8s.io`, `k8s.gcr.io`), and Google Artifact Registry (`*.pkg.dev`) image layers (blobs, by digest) | yes |
| Image manifests and tags | no — forwarded every time |
| npm / Yarn package tarballs (`.tgz`) | yes |
| npm packuments (the per-package JSON) | no — forwarded every time |
| Go module `@v/<version>.zip`, `.mod` and `.info` | yes |
| Go `@v/list`, `@latest`, and a `.info` for a branch | no — forwarded every time |
| checksum-database tiles (`sumdb/.../tile/...`) | yes |
| checksum-database lookups | no — forwarded every time |
| Git Smart HTTP packfiles (`git-upload-pack` result streams for object SHAs) | yes |
| Git reference advertisements (`/info/refs?service=git-upload-pack`) | no — forwarded every time |
| GitHub release assets (`/releases/download/...`) and tag/commit archives (`/archive/refs/tags/...`, `/archive/<sha>...`) | yes |
| GitHub branch archives (`/archive/refs/heads/...`, `/archive/main.tar.gz`) and mutable raw files | no — forwarded every time |
| Git push operations (`git-receive-pack`) | no — forwarded every time |

What is stored is only what cannot change underneath it: an archive pool
file, a published PyPI artifact, a container layer, a published npm
tarball and a module at a version are each named after the exact bytes
they contain and are never rewritten in place, so a cache hit is either
the right bytes or absent. Package *indexes* are the half that changes, so
they are never stored — which costs a few kilobytes per run and removes
the entire class of bug where a deployment installs last month's package
list, or runs last month's `:latest`.

Layers are stored under their own digest rather than under the URL they
came from, so a base layer two images share is fetched once and held once.
Because that key names no registry, the cache hashes every layer (and
every manifest fetched by digest) as it streams past and stores it only if
the bytes match the digest they were asked for by. A registry or mirror
that answers with the wrong bytes still has them passed to the client,
which rejects them itself, but they are never stored and never served to
the next pull of that layer from any registry.

Nothing about integrity changes. `apt` verifies the archive's signed
indexes against its own keyring, and the index it verifies them from is
fetched fresh every time; `pip` and `npm` check every artifact against the
hash their index gave (npm's `integrity`, and it fails with `EINTEGRITY`
if it does not match), and that hash travels through the rewrite
untouched; `dockerd` resolves every tag upstream and checks every layer
against the digest its manifest names; the `go` command checks every
module against `go.sum` and, for a module it has never seen, against the
checksum database the cache serves for it. A file served from the cache
that is not the file upstream published fails in the package manager
rather than being installed.

## https, and what happens when something points a proxy at the cache

There are two ways to reach the cache, and one of them is not a way at all.

The way it is meant to be reached is by name: a sandbox's
`/etc/resolv.conf` points at the controller's DNS service, the archive and
registry hostnames resolve to the controller, and the cache serves them
over HTTP and over HTTPS with a certificate from the internal Grain Root
CA that the sandbox trusts. `https://deb.debian.org/...` is cached exactly
as the plain-HTTP form is. `apt` additionally gets
`/etc/apt/apt.conf.d/01-grain-package-cache`, which names the cache as an
HTTP proxy *per archive host* — `Acquire::http::Proxy::deb.debian.org` —
and sends the same host `DIRECT` over https, because DIRECT is the route
that ends at the cache's own HTTPS listener and gets cached.

The way it is not meant to be reached is a blanket `HTTP_PROXY` /
`http_proxy` / `https_proxy` covering every host a process fetches from. An
HTTP proxy asked for an `https://` URL is asked with `CONNECT`, and until
2026-09-18 this service answered every `CONNECT` with `405 Method Not
Allowed`. That is not a fallback to a direct fetch — it is a hard failure:

```
E: Failed to fetch https://deb.debian.org/debian/pool/main/c/cpio/cpio_2.13-7.1_amd64.deb
   Invalid response from proxy: HTTP/1.1 405 Method Not Allowed
```

It took out every image build in this deployment, on every branch, for
three days. So a `CONNECT` naming a host the cache serves, on port 443, is
now **tunnelled straight through to that host, uncached**: nothing in a
tunnel can be stored, and a fetch that is merely as slow as it would be
with no cache is not a broken build. A `CONNECT` to any other host, or to
any other port, is refused with `403` and a sentence naming the allowlist —
the same allowlist a plain-HTTP fetch through the cache is held to, which
is what keeps a `networkAccess: none` sandbox from getting a
general-purpose way out.

None of that makes a blanket proxy the way to use this service. It is still
one setting for every host a build touches, and most of those are not
archives: they get the allowlist's `403`, from a list whose existence the
Dockerfile's author has never heard of. Grain therefore points nothing at
the cache that way — there is no `proxies.default.httpProxy` in a sandbox's
`~/.docker/config.json`, and no `HTTP_PROXY` in
`/etc/grain/package-cache.env` — and a `docker build` is given the
per-ecosystem settings in that file instead. The tunnel is for the clients
grain cannot configure, not an invitation.

**If you add an archive of your own over https**, either leave it
unproxied, or add `Acquire::https::Proxy::<host> "DIRECT";` in a file
sorting after `01-grain-package-cache` — anything else reaches the cache as
a tunnel, which works but caches nothing.

## Turning it on and off

**Settings → Sandbox → Proxy cache.** The switch takes effect on the
next run dispatched, not on runs already under way.

Turning the cache off leaves what it has collected on disk. The service
keeps listening — nothing is pointed at it, so nothing reaches it — which
is what makes the switch a live setting rather than a restart.

The same setting is `packageCacheEnabled` through `PUT /api/settings`,
`grain sync`, and `grain_config.package_cache_enabled` in the state
repository.

## Flushing it

System → Caches lists what each half of the proxy cache holds and offers a
flush per ecosystem plus one for all of it. The rail entry itself carries
how many packages are cached in total, abbreviated (`2.5k`, `24k`), with
the exact figure on its tooltip; it comes from `GET /api/config`'s
`packageCacheEntries`, which is the cache's running count rather than the
directory walk `GET /api/package-cache` does, and is absent on a
deployment running no cache. The figures were under System → Sandbox
health until grain/task-321, and the disk bars on System → Sandboxes
still point at this tab. The route the buttons call keeps the API's own name:

```bash
curl -X POST localhost:8080/api/package-cache/flush          # everything
curl -X POST 'localhost:8080/api/package-cache/flush?kind=apt'
curl -X POST 'localhost:8080/api/package-cache/flush?kind=pypi'
curl -X POST 'localhost:8080/api/package-cache/flush?kind=docker'
curl -X POST 'localhost:8080/api/package-cache/flush?kind=npm'
curl -X POST 'localhost:8080/api/package-cache/flush?kind=go'
```

A flush is safe at any moment, runs in flight included: an entry being
read when it is removed goes on being readable to whoever has it open, and
everything else is one fetch from upstream away. Nothing is lost that a
re-download does not put back, which is why the button asks for no
confirmation.

The cache also evicts on its own, oldest-first, to stay under **Settings
→ Sandbox → Proxy cache max size (GiB)** — `packageCacheSizeGb` through
the API, blank for the daemon's own `-package-cache-size-gb` (20 GiB by
default). The size it evicts against is measured from the directory, not
accumulated from a running total, so the number the Proxy cache page
shows is what the disk holds and eviction cannot be provoked by an
accounting mistake.

A single object larger than 2 GiB is forwarded but never stored: one file
that size would evict most of the useful contents of the cache to hold
something nothing asks for twice. That is logged, once per fetch, naming
the URL and the limit — a cache that is enabled, healthy and never serving
one particular object is otherwise indistinguishable from a broken one.

Several runs asking for the same object at the same moment share one
fetch rather than making one each, which is the case this deployment is
usually in: several CI lanes starting the same job within seconds of each
other. What is shared is the download and not its outcome — a run whose
shared fetch came back with nothing storable goes upstream itself, so an
upstream failure is never handed to a run that did not provoke it.

## Which hosts it will fetch from

The cache fetches from distribution archives (`deb.debian.org`,
`security.debian.org`, the Ubuntu archives and their regional mirrors,
`download.docker.com`, `packages.cloud.google.com`, and `apt.releases.hashicorp.com`),
from the hosts PyPI's own index links to, from Docker Hub
(`registry-1.docker.io`, and wherever it redirects a layer download to),
from GitHub Container Registry (`ghcr.io`, and wherever it redirects a layer download to),
from Google Container Registry (`gcr.io`, and wherever it redirects a layer download to),
from `registry.npmjs.org`, and from `proxy.golang.org` and
`sum.golang.org`. Anything else is refused with a sentence saying so.

That allowlist is the whole security story, and it is enforced over a
`CONNECT` tunnel as well as over a fetch. A sandbox running with
`networkAccess: none` has had its default route taken away precisely so
that this deployment's own services are all it can reach; an HTTP proxy in
that set that forwarded wherever it was told would quietly hand it a
general-purpose egress path back. The flip side is the nicest thing about
this feature: a sandbox with no network can now install packages at all,
which it could not before.

A PyPI index link naming a host outside the list is left exactly as it
arrived, so a package hosted elsewhere is downloaded by `pip` directly
rather than refused — an index may name any host in the world, and for
PyPI the cache is not the only route out.

## Fetching through a mirror of your own

An installation that already fronts one of those with a mirror — a
caching proxy, an internal apt-mirror, a regional mirror on a private
network, an Athens, a pull-through registry — can have this cache fill
itself from there instead of from the internet. Seven daemon flags, one
per ecosystem:

```
grain daemon \
  -package-cache-apt-upstream deb.debian.org=http://mirror.internal/debian \
  -package-cache-apt-upstream security.debian.org=http://mirror.internal/security \
  -package-cache-pypi-index http://mirror.internal/pypi/simple \
  -package-cache-npm-registry http://mirror.internal/npm \
  -package-cache-go-proxy http://athens.internal \
  -package-cache-docker-registry https://registry.internal \
  -package-cache-ghcr-registry https://registry.internal/ghcr \
  -package-cache-gcr-registry https://registry.internal/gcr
```

`-package-cache-apt-upstream` is per archive and repeatable: the key is
the host a sandbox asks for, the value the base URL its requests are
fetched from with the request's own path appended, and an archive not
named is fetched from itself as before. The other six take one URL each
and default to `https://pypi.org/simple`, `https://registry.npmjs.org`,
`https://proxy.golang.org`, `https://registry-1.docker.io`,
`https://ghcr.io` and `https://gcr.io`. The three registry flags are
told apart by the prefix a pull names: an unprefixed repository under
`/v2/` is Docker Hub's, `/v2/ghcr/` and `/v2/gcr/` are the other two,
and an interception of `ghcr.io` or `gcr.io` itself lands on the same
upstream as its prefix does.

**Nothing about a sandbox changes.** It still resolves `deb.debian.org`,
still asks this cache for `deb.debian.org`, and the files it is given
(`/etc/apt/apt.conf.d/01-grain-package-cache`, `/etc/pip.conf`) still name
the public hosts — the substitution happens one hop further out, between
this service and the mirror, where nothing in the sandbox can see it. So
these change where a *miss* is filled from and nothing else. The
allowlist above is unaffected too: a mirror named for a host the cache
does not serve is configuration nothing would ever reach, and the daemon
refuses to start rather than run with it, naming the hosts it does serve.
A value that is not an `http://` or `https://` URL with a host in it is
refused the same way.

They are flags rather than rows in Settings because which mirror this
host can reach is a fact about the network grain was installed on, like
`-data-dir`, rather than a decision anybody makes per task — and because
the cache reads them once, when its listeners come up, so a control in the
UI would appear to work and take effect on the next restart. Set them in
`grain-daemon.service`'s `ExecStart` and restart the unit; a build that
predates them ignores them with a line in the journal rather than
refusing to start (`cmd/grain/daemon_unknown_flags.go`). A deployment
that is fetching through a mirror says so once at startup:

```
grain daemon: package cache: fetching deb.debian.org from
http://mirror.internal/debian, PyPI from http://mirror.internal/pypi/simple
rather than from the public archives.
```

The other registries `pkg/pkgcache` serves — `quay.io`,
`registry.k8s.io`, `k8s.gcr.io`, Artifact Registry — have no field of
their own and no flag in front of one: they are fetched from themselves.

## Containers a run builds

A container inherits nothing from the sandbox around it — the `/etc` the
cache writes into is not the container's, and grain does not own the image
— so the settings that point a container's own package managers here are
left at `/etc/grain/package-cache.env` for a run to pass in:

```bash
docker run --env-file /etc/grain/package-cache.env ...
docker build $(grep -v '^#' /etc/grain/package-cache.env | grep . | sed 's/^/--build-arg /') .
```

`GOPROXY` there ends in `,direct`, which is the go command's own default:
a module the cache cannot serve is fetched from its own host. `PIP_INDEX_URL`
and `NPM_CONFIG_REGISTRY` deliberately have no equivalent. Neither pip nor
npm has a second index to fall back to — pip's `PIP_EXTRA_INDEX_URL` is a
second index searched for *every* package rather than a fallback, and
resolving across two of them is a worse failure than the one it would
avoid — so a container given this file while the cache is down fails
rather than going direct, exactly as the sandbox's own `pip` does. Nothing
applies the file on a run's behalf, so a build that must not depend on
this deployment's cache is one that does not pass it.

There is deliberately no `HTTP_PROXY` and no `apt` setting in it. A proxy
is one setting for the whole internet, where everything else here is
per-host, and this service is an allowlisted plain-HTTP archive cache that
would refuse most of it: a container's `apt-get install` downloads its own
packages, as it does with no cache at all.
