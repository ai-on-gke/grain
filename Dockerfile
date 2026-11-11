# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The image a grain deployment actually runs: `grain` and every binary it
# shells out to, in one artifact CI publishes to GHCR on every commit
# (../.github/workflows/build-artifacts.yml) and scripts/setup.sh pulls
# rather than builds -- bwsalmon/agents#645.
#
# This is a *runtime* image, and the opposite of Dockerfile.build next to
# it. That one is a toolchain: it COPYs nothing, the working tree arrives
# as a bind mount, and what comes out is a binary the host then runs
# under its own systemd unit with whatever else the host happens to have
# installed. This one COPYs the whole checkout, builds inside itself, and
# what comes out is the deployment -- so "whatever else the host happens
# to have installed" stops mattering.
#
# What "all of its dependencies" means concretely, and why each is here:
#
#   git               the orchestrator clones every task's repo through
#                     the git proxy, and pkg/mcp hands each sandbox a
#                     credential helper to use with it
#   bash              pkg/mcp's run_command and pkg/capability/selfrepair
#                     both run `bash -c` for a task's own commands
#   openssh-client    GranuleSandboxes reaches each guest over SSH
#   ca-certificates   every HTTPS call: GitHub's API, the agent CLIs
#   curl              the agent CLIs' own network calls, and a shell in
#                     here that needs to reach something
#   procps            for `top` alone -- pkg/hosttop shells out to it for
#                     the UI's System pane, which is where an operator
#                     asks what is spending the daemon's machine once
#                     sandbox health has said it is loaded
#   systemd           for `journalctl` alone -- pkg/systemlog's Journalctl
#                     and Dmesg both shell out to it for the UI's Logs
#                     pane, against the host journal scripts/setup.sh
#                     mounts in (see that script's own docker_run_args).
#                     The kernel log is read through it too, rather than
#                     through util-linux's dmesg(1): journald has already
#                     collected those messages into that same mounted
#                     journal, and dmesg(1) would want /dev/kmsg and
#                     CAP_SYSLOG, neither of which this container is given
#   postgresql-16,    `grain backup` and the daemon's scheduled backups run
#   postgresql-client `pg_dump -Fc` against a PostgreSQL store (pkg/backup,
#                     pkg/model/postgres), `grain upgrade-postgres` uses
#                     `psql` and `pg_restore`, and embedded clusters run
#                     `initdb`, `postgres` and `pg_ctl`. PostgreSQL 16 from
#                     PGDG (rather than Debian 12's 15) so the client major
#                     version is at least that of the `postgres:16-bookworm`
#                     server scripts/setup.sh provisions by default
#   docker CLI        pkg/granule talks to the *host's* docker daemon over
#                     the socket setup.sh mounts, to inspect the
#                     container each sandbox VM runs in, and pkg/mcp's
#                     docker-exec transport reaches into it the same way
#   granule         pkg/granule runs it to create, exec into and destroy
#                     those VMs. Built here from third_party/granule, the
#                     same vendored copy the sandbox image is built from,
#                     so the two cannot drift apart in a deployment
#   kubectl           the same three jobs for the Kubernetes backend
#                     (-k8s-sandboxes): pkg/k8s creates and deletes a
#                     sandbox pod through it, pkg/mcp's kubectl-exec
#                     transport reaches into one, and an upgrade
#                     repoints this deployment's own Deployment.
#                     Downloaded and checksum-verified at build time,
#                     pinned by KUBECTL_VERSION below
#   cosign            -upgrade-verify-cmd, which checks who signed an
#                     image before an upgrade runs it
#                     (docs/image-signing.md), runs in this container:
#                     setup.sh and deploy/kubernetes/grain.yaml both
#                     point it at `cosign verify` and a mounted public
#                     key. Copied out of sigstore's image, pinned by
#                     COSIGN_IMAGE's digest below
#   claude, agy,      the three agent CLIs a dispatch actually runs:
#   codex             agent/claude execs the Claude Code CLI,
#                     agent/antigravity execs Google's Antigravity CLI,
#                     agent/codex execs OpenAI's Codex CLI. Which one
#                     drives a run is a live, per-task choice
#                     (README.md, "Three agent frameworks, any of them
#                     per task"), so an image carrying some of them is
#                     an image that fails every run choosing another --
#                     they belong here together or not at all.
#
#                     Installed here rather than by setup.sh's own
#                     install_claude_cli, which this replaces: a CLI
#                     baked into the image has its presence settled at
#                     build time, in CI, instead of depending on every
#                     deployed host being able to reach claude.ai,
#                     antigravity.google and github.com at deploy time --
#                     and on depending on it again on every re-deploy,
#                     forever.
#
# Build it from the *repository root*:
#
#     docker build -f Dockerfile -t grain .
#
# third_party/granule is outside the Go tree and granule is built from
# it, and `go build` reads the commit stamp out of the root .git. `make
# image` (the Makefile's own target) already passes both.

# Kept in step with go.mod by the Makefile, which reads the version out
# of it and passes it as a --build-arg; the default here is what a bare
# `docker build` with no --build-arg gets, so it is written down twice on
# purpose and GOTOOLCHAIN=local below turns a stale copy into an error
# naming both versions rather than a silently different compiler.
ARG GO_VERSION=1.26.11
# Separate from the tag so a mirror can be substituted whole
# (GO_IMAGE=mirror.gcr.io/library/golang) on a network that does not
# reach Docker Hub -- the same escape hatch Dockerfile.build and the
# Makefile already give the toolchain image.
ARG GO_IMAGE=golang
# Every base image is pinned by digest as well as by tag. A tag is a
# name the upstream registry can repoint at different bytes whenever it
# likes -- a routine rebuild, or an account that is not upstream's any
# more -- and a rebuild of this same commit would pull whatever it
# points at that day with nothing in the tree changing. The digest is
# what is actually fetched; the tag stays beside it to say what the
# digest was resolved from. Each digest is the multi-arch index, so the
# amd64 and arm64 builds are both pinned by one value. Bump the tag and
# its digest together, resolving the new digest with
#
#     docker buildx imagetools inspect <image>:<tag>
#
# GO_IMAGE_DIGEST is golang:${GO_VERSION}-bookworm's, and is separate
# from GO_VERSION because the Makefile passes that one in from go.mod: a
# go.mod bump without a digest bump still builds with the pinned image,
# and GOTOOLCHAIN=local below turns the mismatch into an error naming
# both versions. Dockerfile.build pins the same value, and
# TestEveryBaseImageIsPinnedByDigest keeps the two equal.
ARG GO_IMAGE_DIGEST=sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
ARG RUNTIME_IMAGE=debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
# The docker CLI is copied out of this image rather than apt-installed:
# Debian's own `docker.io` package pulls in the whole daemon --
# containerd, runc, iptables, a systemd unit -- to get one client binary
# that only ever talks to a socket mounted from the host. The 29 CLI
# negotiates down to an older engine's API (it still drives Engine 24),
# and the hosts grain drives run a current one anyway: docker-ce from
# pkg/hostinit, or the CI runner's own engine under tests/container and
# tests/installer.
ARG DOCKER_CLI_IMAGE=docker:29-cli@sha256:2357639ad1ea7d0194134bf3292622fd4e562483966ed674e5d7c945b5f3b28d
# cosign, copied out of sigstore's own release image for the same reason:
# it is one static binary (ko builds it into /ko-app/cosign, on a
# distroless base with nothing else in it), and this pin is the whole of
# what decides which bytes it is. This cosign only ever verifies. It is
# the same v3 release scripts/release-images.sh signs with (its
# COSIGN_VERSION default), which writes the legacy `.sig` signature
# (--new-bundle-format=false) rather than v3's new-format bundle: images
# carrying the old v2.4.1 verifier are still in the field and cannot find
# the new format. v3 verifies the legacy format with the same flags and
# the same JSON on stdout the Kubernetes upgrade path reads its digest
# from. A bump of one is the moment to look at the other, but they need
# not move together.
ARG COSIGN_IMAGE=ghcr.io/sigstore/cosign/cosign:v3.1.4@sha256:0f06154836d2de34f887a5c273506d52ca43edaa1d9e53e6510fa78a48f69c2d

FROM ${DOCKER_CLI_IMAGE} AS docker-cli

FROM ${COSIGN_IMAGE} AS cosign

FROM ${GO_IMAGE}:${GO_VERSION}-bookworm@${GO_IMAGE_DIGEST} AS build

# See Dockerfile.build's own comment: a go.mod toolchain line newer than
# the pinned image would otherwise quietly fetch a different Go at build
# time, undoing the pin.
ENV GOTOOLCHAIN=local

# ui/ is React+Vite and `make build` runs `npm ci && npm run build`
# there before `go build`, so this stage carries a JS toolchain for the
# same reason Dockerfile.build does -- and, as there, Debian 12's own
# nodejs/npm are recent enough for the pinned Vite version, with no
# NodeSource repo to add.
RUN apt-get update && apt-get install -y --no-install-recommends nodejs npm ca-certificates libcap2-bin \
	&& rm -rf /var/lib/apt/lists/*

COPY Dockerfile .grain-ca.cr[t] /tmp/
RUN if [ -f /tmp/.grain-ca.crt ]; then \
		cp /tmp/.grain-ca.crt /usr/local/share/ca-certificates/grain-ca.crt \
		&& update-ca-certificates; \
	fi && rm -f /tmp/.grain-ca.crt /tmp/Dockerfile
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt

WORKDIR /src
COPY . .

# Plain `make build`, out of the same Makefile a laptop runs, rather than
# an open-coded `go build` here: one copy of the build rules, so the
# binary CI publishes and the binary a developer builds cannot drift.
#
# BUILDVCS is an ARG so a build that must carry a commit stamp can insist
# on one with --build-arg BUILDVCS=true (the publish paths do), and one
# that must not can say --build-arg BUILDVCS=false. The default, `auto`,
# goes through scripts/buildvcs.sh inside `make build`: a context whose
# .git this stage's git cannot read builds without the stamp rather than
# failing on `go build`'s "error obtaining VCS status" -- see the
# Makefile's own BUILDVCS comment.
ARG BUILDVCS=auto
# safe.directory over the copy, for the reason the Makefile's
# container-build sets it over its mount: a build engine that does not
# leave the context owned by the uid running the build (buildah and
# podman carry the context's ownership across where docker does not)
# hands git a repository it reads as someone else's, and git refuses.
# Keeping the stamp is worth a line here, because the alternative --
# buildvcs.sh's fallback -- gets the build through at the cost of it.
#
# Written to a gitconfig rather than passed as GIT_CONFIG_*, which is
# what container-build does: there the exception has to end with the
# container, while this one is a build stage that ships nothing (the
# runtime image below is a different FROM) and two RUN lines need it.
RUN git config --global --add safe.directory /src
# SANDBOX_IMAGE stamps in the sandbox container this build goes with
# (pkg/cli/sandboximage.go). Empty -- a bare `docker build` -- leaves
# the source default, the tag CI keeps pointed at main; a release passes
# the sandbox image built from this same commit by its sha- tag and the
# digest that push was given (repo:sha-<short>@sha256:..., see
# scripts/release-images.sh), so the two halves of a granule deployment
# can never be two different commits, and a sandbox is created from the
# very image an upgrade verified.
ARG SANDBOX_IMAGE=
ARG K8S_SANDBOX_IMAGE=
# GRAIN_IMAGE_REF stamps in what this image is *called*
# (pkg/cli/grainimage.go), which is how a deployment can write the tag
# it runs into the CI step of its own state repository -- a check run by
# some other build fails on the schema stamp rather than on the change.
# Empty leaves the source default, the tag CI keeps pointed at main; CI
# passes the exact sha- tag it is about to publish this image under.
ARG GRAIN_IMAGE_REF=
# RELEASE stamps in the release name (pkg/version.Release), which is what
# the UI's sidebar footer and `GET /api/config`'s version.release show.
# Empty leaves the version Go reads off the commit -- whatever tag was
# newest on it when it was built, which for a commit built as the
# candidate is its rc. `release-images.sh promote` rebuilds a promoted
# release with RELEASE=vX.Y.Z, so `latest` reports the release it is.
ARG RELEASE=
# MAIN_GRAIN is the main bin/grain is built from: ./cmd/grain, core
# grain, unless a build with a main of its own says otherwise (the
# Makefile's MAIN_grain).
ARG MAIN_GRAIN=./cmd/grain
ARG MAIN_GRAIN_AGENTD=./cmd/grain-agentd
RUN make build BUILDVCS=${BUILDVCS} SANDBOX_IMAGE=${SANDBOX_IMAGE} K8S_SANDBOX_IMAGE=${K8S_SANDBOX_IMAGE} GRAIN_IMAGE_REF=${GRAIN_IMAGE_REF} RELEASE=${RELEASE} MAIN_grain=${MAIN_GRAIN} \
	&& setcap cap_net_bind_service=+ep /src/bin/grain

# granule only -- not `granule` or `granule-mem-agent`, which run inside
# each sandbox VM's own container (the image
# ../.github/workflows/build-artifacts.yml publishes as `guest`, which is
# both that container and the guest disk it boots) rather than next to
# the daemon. pkg/granule shells out to exactly this
# one binary. CGO_ENABLED=0 for the same reason Makefile forces it:
# a genuinely static binary, with no libc coupling to the stage it was
# built in.
#
# -buildvcs through scripts/buildvcs.sh like every other build here, and
# not because anything reads granule's stamp: third_party/granule is a
# module of its own but it sits *inside* this repository, so `go build`
# consults the same .git the line above does and refuses the same way
# over it. Without this, a checkout the stage's git cannot read simply
# moves the failed build four lines down.
RUN vcs="$(./scripts/buildvcs.sh ${BUILDVCS})" && mkdir -p /out \
	&& CGO_ENABLED=0 go build -buildvcs="$vcs" -trimpath -ldflags='-s -w' -o /out/granule github.com/gke-labs/granule/cmd/granule \
	&& CGO_ENABLED=0 go build -buildvcs="$vcs" -trimpath -ldflags='-s -w' -o /out/github-webhook ./cmd/github-webhook \
	&& CGO_ENABLED=0 go build -buildvcs="$vcs" -trimpath -ldflags='-s -w' -o /out/grain-sandbox ./cmd/grain-sandbox \
	&& BUILDVCS="${BUILDVCS}" RELEASE="${RELEASE}" GRAIN_AGENTD_MAIN="${MAIN_GRAIN_AGENTD}" ./scripts/build-agentd.sh /out/agentd

# kubectl, for a deployment running -k8s-sandboxes: pkg/k8s creates,
# inspects and deletes sandbox pods through it, pkg/mcp's kubectl-exec
# transport reaches into one the same way, and an upgrade repoints this
# deployment's own Deployment with it. It is the Kubernetes backend's
# equivalent of granule above, and it is here for the same reason: a
# binary a deployment cannot run is a deployment where every dispatch
# fails, so it is settled at build time rather than on each host.
#
# Downloaded and checksum-verified rather than copied out of
# registry.k8s.io/kubectl, so that what lands here is pinned to a
# version and a hash this repo wrote down. KUBECTL_VERSION is a
# build-arg so a cluster far from this default can build an image whose
# kubectl is within a minor of its own control plane, which is the skew
# Kubernetes supports.
#
# Retried the way the agent CLI downloads below are, and for the same
# reason: a main release build in October 2026 failed here on
# `curl: (6) Could not resolve host: dl.k8s.io`, a DNS blip on a step
# that had passed in about a second on the commit before. --retry alone
# does not count a failed lookup as transient, hence --retry-all-errors.
ARG KUBECTL_VERSION=v1.37.2
ARG KUBECTL_SHA256_AMD64=c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4
ARG KUBECTL_SHA256_ARM64=fcc16f63186659d9722014b7e02d5fdd2d4e669cb8c0ab66365d7d3b8ba03509
RUN set -eu; \
	arch="$(dpkg --print-architecture)"; \
	case "$arch" in \
		amd64) sha="${KUBECTL_SHA256_AMD64}" ;; \
		arm64) sha="${KUBECTL_SHA256_ARM64}" ;; \
		*) echo "no kubectl checksum pinned for $arch -- add one above" >&2; exit 1 ;; \
	esac; \
	curl -4 -fsSL --max-time 180 --retry 8 --retry-max-time 600 --retry-connrefused --retry-all-errors \
		-o /out/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl"; \
	echo "${sha}  /out/kubectl" | sha256sum -c -; \
	chmod 0755 /out/kubectl

# The source the runtime stage carries, for the self-debug capability
# (pkg/capability/selfdebug) -- read_grain_source and list_grain_source
# are the agent's view of the code the binary next to them was built
# from, so the two have to be the same commit. Baked in rather than
# bind-mounted from a host checkout, which is what it used to be: that
# checkout tracks a *branch*, this image is an immutable tag, and an
# upgrade repoints one without touching the other -- so the agent read
# the old source while the new binary ran, and a rollback inverted it.
# Same reasoning as SANDBOX_IMAGE above: what a build goes with travels
# inside it.
#
# Staged here rather than copied straight from /src, because what is in
# /src is not only this repository: it is `make build`'s own output
# (bin/, and the node_modules its `npm ci` just created right here), the
# 6MB of history in .git that nothing reads (selfdebug is os.ReadFile and
# os.ReadDir, no git metadata), and -- the part that made this a script --
# whatever else was sitting in the directory the build context was taken
# from.
#
# export-source.sh stages what git says is *tracked* and refuses a tree
# too large to be source. It is an allowlist because the denylist this
# used to be (a `cp -a` and three `rm -rf`s) shipped 3.1GB of stale build
# cache in every published image; that script's own header is the account
# of it. setup.sh's host-mode install stages the same tree through the
# same script, so the two copies of "grain's source" cannot mean two
# different things.
#
# SOURCE_HISTORY_DEPTH is how many commits of shallow git history travel
# with that tree, so a self-debug run can `git log` and `git blame` in its
# grain-src/ (export-source.sh's own comment). 0 bakes none, and is the
# default until the secret scan's findings over the default window of
# 500 are signed off (task 4337): everything in that history ships in
# every published image, deleted files included. Whenever it is above 0,
# gitleaks scans exactly the history being baked and fails this build on
# a finding (GRAIN_SOURCE_SECRET_SCAN=require). gitleaks is go-installed
# at a pinned version, which the Go checksum database verifies.
ARG SOURCE_HISTORY_DEPTH=0
ARG GITLEAKS_VERSION=v8.28.0
RUN set -eu; \
	if [ "${SOURCE_HISTORY_DEPTH}" != 0 ]; then \
		GOBIN=/usr/local/bin go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}"; \
	fi; \
	GRAIN_SOURCE_HISTORY_DEPTH="${SOURCE_HISTORY_DEPTH}" GRAIN_SOURCE_SECRET_SCAN=require \
		/src/scripts/export-source.sh /src /src-export

FROM ${RUNTIME_IMAGE}

# What attaches the published package to this repository. GHCR links a
# container image to a repo by this label and nothing else: without it
# the image is still pushed and still pullable, but it appears only under
# the *account's* packages rather than in this repository's own list, and
# the repo's access settings do not govern it. This image had no label at
# all until now, which is why its packages have never shown up here.
LABEL org.opencontainers.image.source="https://github.com/bwsalmon/grain" \
      org.opencontainers.image.title="grain" \
      org.opencontainers.image.description="The grain daemon and every binary it shells out to, in one image."

# How a daemon started from this image knows it is in grain's container,
# which is the one place an ambient git credential (the host's own gh
# login) has nothing to borrow (pkg/hostmode). The other signals it reads
# -- /.dockerenv, /proc/1/cgroup -- are about containers in general and
# depend on the runtime; this one is grain's own. A host-mode install
# takes only the binary out of this image, so none of it reaches the host.
ENV GRAIN_IN_CONTAINER=1

# libcap2-bin is installed, used on /usr/sbin/nft, and purged in this one
# layer: leaving it in the image would hand a task's own `bash -c` a tool
# for handing out capabilities. /usr/local/bin/grain gets its
# CAP_NET_BIND_SERVICE file capability in the build stage before COPY so
# BuildKit carries the extended attribute in that single COPY layer rather
# than copying up the whole ~75MB binary a second time here.
#
# systemd is here for /bin/journalctl and nothing else -- see this file's
# header. --no-install-recommends keeps it to the binaries and their
# libraries rather than dragging in a whole init the container never
# runs.
#
# PostgreSQL 16 comes from PGDG (`bookworm-pgdg`), authenticated by the
# PGDG keyring Debian 12's own `postgresql-common` package installs at
# /usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg, rather than
# Debian 12's default PostgreSQL 15: scripts/setup.sh provisions a
# `postgres:16-bookworm` local server by default, and `pg_dump` refuses
# to dump a server whose major version is newer than its own.
# `create_main_cluster = false` prevents postgresql-common's postinst
# from initializing an unused 38MB cluster at /var/lib/postgresql/16/main
# (embedded clusters run `initdb` against `<data-dir>/postgres`).
RUN set -eu; \
	printf '%s\n' \
		'path-exclude /usr/share/i18n/locales/*' \
		'path-exclude /usr/share/i18n/charmaps/*' \
		> /etc/dpkg/dpkg.cfg.d/grain-slim; \
	apt-get update -o Acquire::Retries=3 \
	&& apt-get install -y --no-install-recommends -o Acquire::Retries=3 \
		bash \
		ca-certificates \
		curl \
		git \
		libcap2-bin \
		nftables \
		openssh-client \
		postgresql-common \
		procps \
		systemd \
		tini; \
	setcap cap_net_admin=+ep /usr/sbin/nft; \
	apt-get purge -y --auto-remove libcap2-bin; \
	test -f /usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg; \
	mkdir -p /etc/postgresql-common/createcluster.d; \
	echo "create_main_cluster = false" > /etc/postgresql-common/createcluster.d/no-main-cluster.conf; \
	echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" \
		> /etc/apt/sources.list.d/pgdg.list; \
	apt-get update -o Acquire::Retries=3 \
	&& apt-get install -y --no-install-recommends -o Acquire::Retries=3 \
		postgresql-16 \
		postgresql-client-16; \
	rm -f /etc/apt/sources.list.d/pgdg.list; \
	rm -rf /var/lib/apt/lists/* /var/cache/debconf/*-old /var/log/apt/* /var/log/dpkg.log /var/log/alternatives.log; \
	(useradd --system --uid 1000 --user-group --create-home --shell /bin/bash grain || true); \
	chmod 0666 /etc/passwd /etc/group

ENV PATH="/usr/lib/postgresql/16/bin:${PATH}"

# Three of this daemon's listeners want a privileged port -- which an
# unprivileged process cannot bind. The UI/API binds -ui-addr, and
# scripts/setup.sh's own default for that is port 80; the package cache's
# DNS interception binds 53, and the listeners its answers redirect a
# sandbox's fetch to bind 443 and 80 of the address sandboxes reach this
# host at (pkg/cli/packagecache.go).
#
# The unit setup.sh writes runs this container as the host's own
# unprivileged `grain` account (never root), so systemd's
# AmbientCapabilities, which is how the pre-container unit granted this,
# has no equivalent here: a non-root process in a container gets no
# capability from --cap-add alone. A file capability (set on /src/bin/grain
# in the build stage before COPY below) does what is needed instead -- with
# CAP_NET_BIND_SERVICE in the container's bounding set (the unit passes
# --cap-add=NET_BIND_SERVICE, unconditionally now that it is not only the
# UI's port at stake) this binary, and only this binary, gains it; every
# other process in the container, a task's `bash -c` included, gets nothing.
#
# /usr/sbin/nft got CAP_NET_ADMIN in the apt layer above the same way, for
# the host nftables chain that keeps a granule NetworkNone sandbox off the
# network from outside its VM (pkg/orchestrator/granule_firewall.go). The
# daemon fails such a sandbox's Acquire when it cannot install that chain,
# and this image has no sudo for runNFT's fallback, so without this
# NetworkNone cannot run on a container deployment at all. setup.sh adds
# NET_ADMIN to the bounding set only when granule is on (docker_run_args);
# without it the capability is never raised and nft is an ordinary binary.
#
# Unlike the grain binary's grant, this one is not only the daemon's:
# the container runs --network host, so any process in it that runs nft
# can change the host's firewall, a host-directory sandbox's own commands
# included. That was weighed and accepted (grain/task-1364): every
# deployment that gets NET_ADMIN here also has the host's docker socket
# mounted in, which is already root-equivalent on the host for those same
# processes. The guest VMs get nothing from it -- they run in their own
# containers and network namespaces.
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /src/bin/grain /usr/local/bin/grain
COPY --from=build /out/granule /usr/local/bin/granule
COPY --from=build /out/github-webhook /usr/local/bin/github-webhook
# The sandbox's own grain (cmd/grain-sandbox), beside the daemon's: sandbox
# setup places it at /usr/local/bin/grain in every sandbox
# (pkg/orchestrator/sandbox_grain.go), so a guest image built before the
# sandbox CLI it carries learned a command still answers with this
# daemon's.
COPY --from=build /out/grain-sandbox /usr/local/bin/grain-sandbox
COPY --from=build /out/agentd /usr/local/share/grain/agentd
COPY --from=build /out/kubectl /usr/local/bin/kubectl
COPY --from=cosign /ko-app/cosign /usr/local/bin/cosign
# pkg/cli/daemon.go's defaultSourceDir -- keep the two in step.
COPY --from=build /src-export /usr/local/share/grain/src

# The Claude Code CLI, from the pinned release's own binary with its
# digest checked, rather than through https://claude.ai/install.sh. That
# script is mutable, and not only as a script: it always downloads
# whatever release is *latest* that day and runs it, and only then asks
# that binary to install the version it was given -- so a checksum on
# the script would pin neither the script's next revision nor the binary
# it executes first. What is fetched here is the release artifact the
# installer itself would end up with, from the same bucket, checked
# against the digests Anthropic's manifest publishes for
# CLAUDE_CLI_VERSION (downloads.claude.ai/claude-code-releases/<version>/manifest.json,
# "linux-x64" and "linux-arm64"), and only then run -- as
# `claude install`, which lays out $HOME/.local the way install.sh would.
# The installed copy is checked against the same digest afterwards, so
# `claude install` fetching anything of its own could not go unnoticed
# either. Bump the version and both digests together.
#
# Fetched to a file and then run, never `curl | bash` -- a pipeline's
# exit status is its last command's, so a 403 would otherwise "succeed"
# and leave an image whose claude framework fails at every dispatch.
#
# A failure here is fatal, and deliberately so: an image is built once,
# in CI, with network access -- a build that cannot fetch it should fail
# loudly and be re-run rather than ship an image that is quietly missing
# a framework the UI still offers.
#
# It is retried first, though, because "be re-run" costs a ten-minute
# rebuild of everything above it and the thing being retried is one
# request: claude.ai reset the connection on one pull request's build in
# September 2026 (`curl: (35) Recv failure`, the same commit's other
# image build succeeding beside it), which is a flake rather than a
# network that cannot reach it. --retry-all-errors because a reset
# mid-handshake is not one of the transient failures --retry alone
# covers; no --retry-delay because setting a fixed delay disables curl's
# exponential backoff (1s..128s) and exhausts three retries in 6 seconds
# during a multi-minute GitHub or CDN 504 brownout. A network that
# genuinely cannot reach downloads.claude.ai still fails after backoff,
# exactly as loudly.
#
# INSTALL_CLAUDE_CLI=0 opts out for a build on a network that cannot
# reach downloads.claude.ai at all; the resulting image runs everything
# except the claude framework, which fails at dispatch naming the
# missing binary.
#
# $HOME is where the installer puts things, so it goes somewhere fixed
# and world-readable rather than /root, which the unprivileged uid this
# container runs as cannot read. /usr/local/bin/claude is a symlink onto
# the default PATH so the daemon's own bare exec.LookPath("claude")
# finds it with nothing to configure.
ARG INSTALL_CLAUDE_CLI=1
ARG CLAUDE_CLI_VERSION=2.1.319
ARG CLAUDE_CLI_SHA256_AMD64=dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed
ARG CLAUDE_CLI_SHA256_ARM64=a7e687fd1cb054d651c7588223de3786c99d4c682365e3c3ae1e59ce7f3a591f
RUN if [ "$INSTALL_CLAUDE_CLI" = "1" ]; then \
		set -eu; \
		mkdir -p /opt/claude; \
		arch="$(uname -m)"; \
		case "$arch" in \
			x86_64|amd64) platform=linux-x64; sha256="$CLAUDE_CLI_SHA256_AMD64" ;; \
			aarch64|arm64) platform=linux-arm64; sha256="$CLAUDE_CLI_SHA256_ARM64" ;; \
			*) echo "unsupported architecture for the Claude CLI: $arch" >&2; exit 1 ;; \
		esac; \
		curl -4 -fsSL --max-time 300 --retry 8 --retry-max-time 600 --retry-connrefused --retry-all-errors \
			"https://downloads.claude.ai/claude-code-releases/${CLAUDE_CLI_VERSION}/${platform}/claude" \
			-o /tmp/claude; \
		echo "$sha256  /tmp/claude" | sha256sum -c -; \
		chmod +x /tmp/claude; \
		HOME=/opt/claude /tmp/claude install "$CLAUDE_CLI_VERSION"; \
		rm -f /tmp/claude; \
		echo "$sha256  /opt/claude/.local/share/claude/versions/$CLAUDE_CLI_VERSION" | sha256sum -c -; \
		test -x /opt/claude/.local/bin/claude; \
		ln -sf /opt/claude/.local/bin/claude /usr/local/bin/claude; \
		chmod -R a+rX /opt/claude; \
		test "$(claude --version | awk '{print $1}')" = "$CLAUDE_CLI_VERSION"; \
	fi

# The Antigravity CLI is downloaded from the immutable release artifact
# rather than through install.sh. That installer follows a mutable
# per-platform manifest, so two builds of the same Grain commit could
# otherwise contain different CLIs. Google publishes release hashes;
# keeping them beside the version makes a changed artifact fail
# the build instead of silently changing the agent runtime.
#
# This is what setup.sh's verify_agent_cli used to warn about on every
# deploy: the repo had no installer URL for agy, so the default agent
# framework's binary was the operator's to install by hand on every host,
# and a deployment could offer "antigravity" in Settings and fail every
# run that chose it. GRAIN_AGY_PATH still overrides this with a copy on
# the host, for a deployment pinning a particular version.
#
# And the pin only holds while the binary stays the one that was
# verified. agy updates itself in place during an ordinary run -- its own
# installer says so ("The Antigravity CLI automatically self-updates in
# the background during regular runs"), and one dispatch-shaped exec of
# the 1.1.27 unpacked below replaced it with 1.2.5 seven seconds later
# when the file was writable (the 2026-09-17 agy-self-update hand run).
# `chmod -R a+rX` further down stops that for a non-root process and
# stops nothing at all for a root one, and this image declares no USER of
# its own: the systemd unit scripts/setup.sh writes passes --user, while
# deploy/kubernetes/grain.yaml sets no runAsUser and gets root.
#
# So the switch, as an ENV rather than only in the environment
# pkg/agent/antigravity builds (selfupdate.go, which is where the
# reasoning and the measurement live): this covers the `agy --version`
# check at the end of the block below, an operator's shell in this
# container, and anything else in here that execs agy without going
# through that package. It has to be exactly "true" -- a bare 1 was
# measured not to work.
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
ARG INSTALL_AGY_CLI=1
ARG AGY_CLI_VERSION=1.5.3
ARG AGY_CLI_BUILD=6604829099950080
ARG AGY_CLI_SHA256_AMD64=1b4850599cce73a17f94ffb422df3bf9531531b77dbddc40c1a435e0c2c16807
ARG AGY_CLI_SHA256_ARM64=2d50060c7b1327995e5c81ec58829c91c2564ebb9fec165d3f9f1325ec188675
RUN if [ "$INSTALL_AGY_CLI" = "1" ]; then \
		set -eu; \
		mkdir -p /opt/antigravity; \
		arch="$(uname -m)"; \
		case "$arch" in \
			x86_64|amd64) platform=linux-x64; archive=cli_linux_x64.tar.gz; sha256="$AGY_CLI_SHA256_AMD64" ;; \
			aarch64|arm64) platform=linux-arm; archive=cli_linux_arm64.tar.gz; sha256="$AGY_CLI_SHA256_ARM64" ;; \
			*) echo "unsupported architecture for the Antigravity CLI: $arch" >&2; exit 1 ;; \
		esac; \
		curl -4 -fsSL --max-time 180 --retry 8 --retry-max-time 600 --retry-connrefused --retry-all-errors \
			"https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_CLI_VERSION}-${AGY_CLI_BUILD}/${platform}/${archive}" \
			-o /tmp/agy.tar.gz; \
		echo "$sha256  /tmp/agy.tar.gz" | sha256sum -c -; \
		tar -xzf /tmp/agy.tar.gz -C /opt/antigravity; \
		rm -f /tmp/agy.tar.gz; \
		agy="$(find /opt/antigravity -type f -name agy -perm -u+x | head -n1)"; \
		if [ -z "$agy" ]; then agy="$(find /opt/antigravity -type f -name antigravity -perm -u+x | head -n1)"; fi; \
		test -n "$agy"; \
		ln -sf "$agy" /usr/local/bin/agy; \
		chmod -R a+rX /opt/antigravity; \
		test "$(agy --version | head -n1)" = "$AGY_CLI_VERSION"; \
	fi

# The Codex CLI, from its own published release archive rather than from
# an installer script: OpenAI ships codex as a static binary per
# platform on GitHub Releases (and as an npm package, which would put a
# whole node runtime in this image to run a binary that needs none).
#
# The archive is fetched to a file and unpacked, never piped, for the
# reason the two blocks above give; the binary inside it is *found*
# rather than assumed, the way agy's is, since its name carries the
# target triple and a release that renames it should move the symlink
# instead of failing a build for a binary that is actually there. `codex
# --version` at the end is what makes "found something" mean "installed
# something that runs".
#
# CODEX_CLI_VERSION pins which release is fetched. "latest" is
# deliberately not the default: an image built twice a week apart would
# otherwise carry two different agents with no record of which, and the
# whole point of baking the CLIs in is that what an image runs is
# settled at build time. Bump it here, in one place, and the deployment
# that pulls the next image gets it.
#
# INSTALL_CODEX_CLI=0 opts out for a build on a network that cannot
# reach GitHub's release CDN; the resulting image runs everything except
# the codex framework, which fails at dispatch naming the missing
# binary. GRAIN_CODEX_PATH overrides this with a copy on the host, the
# same escape hatch GRAIN_AGY_PATH/GRAIN_CLAUDE_PATH are.
#
# A release tag pins a name, not bytes -- GitHub lets a release's assets
# be deleted and re-uploaded under it -- so both archives are checked
# against their digests before anything in them is unpacked, for both
# architectures, the way agy's is above. The values are the sha256
# digests GitHub's release API reports for each asset of
# CODEX_CLI_VERSION; bump them with the version. When GitHub's web
# /releases/download/... redirect returns HTTP 504 on large multi-asset
# releases, each archive falls back to the GitHub Releases REST API
# (api.github.com/repos/openai/codex/releases/tags/${CODEX_CLI_VERSION}
# -> /releases/assets/<id> with Accept: application/octet-stream, which
# redirects straight to release-assets.githubusercontent.com) and checks
# the same release digest; if GitHub Releases and its API are both
# unreachable (or in a sandbox network that reaches registry.npmjs.org
# through the package cache), the build falls back to the immutable
# @openai/codex@<CODEX_CLI_NPM_VERSION>-linux-{x64,arm64} tarball on
# registry.npmjs.org, verified against CODEX_CLI_NPM_SHA256_* before
# unpacking the static musl binaries (no node runtime needed).
ARG INSTALL_CODEX_CLI=1
ARG CODEX_CLI_VERSION=rust-v0.167.0
ARG CODEX_CLI_SHA256_AMD64=9d64836106d5b374e90ee4454c7ebcde7ca7106558bd7e41a34b9f26ebda251a
ARG CODEX_CLI_SHA256_ARM64=843489acb4d4b1079745c36b03c5963b887b83155af208fd53584e33ee8eb281
ARG CODEX_CODE_MODE_HOST_SHA256_AMD64=b5074d5dd64643b2c073927e494a588eff31c1f5a27f12c1e2c45f9a6cf07dd4
ARG CODEX_CODE_MODE_HOST_SHA256_ARM64=6e99948fa20ca816a01f222a56afa261b321e00cbed611d76bb92c765fd11ec6
ARG CODEX_CLI_NPM_VERSION=0.167.0
ARG CODEX_CLI_NPM_SHA256_AMD64=2dc9aa871abb513c3539d20d025f701faf1974c46ee906bda3a1e1f5026d4434
ARG CODEX_CLI_NPM_SHA256_ARM64=e24d7b6f0af5695a36c04692c3a79672bdadebcac7c44005259cb3cad36ac40e
RUN if [ "$INSTALL_CODEX_CLI" = "1" ]; then \
		set -eu; \
		mkdir -p /opt/codex; \
		arch="$(uname -m)"; \
		case "$arch" in \
			x86_64|amd64) target=x86_64-unknown-linux-musl; npm_arch=x64; sha256="$CODEX_CLI_SHA256_AMD64"; host_sha256="$CODEX_CODE_MODE_HOST_SHA256_AMD64"; npm_sha256="$CODEX_CLI_NPM_SHA256_AMD64" ;; \
			aarch64|arm64) target=aarch64-unknown-linux-musl; npm_arch=arm64; sha256="$CODEX_CLI_SHA256_ARM64"; host_sha256="$CODEX_CODE_MODE_HOST_SHA256_ARM64"; npm_sha256="$CODEX_CLI_NPM_SHA256_ARM64" ;; \
			*) echo "unsupported architecture for the codex CLI: $arch" >&2; exit 1 ;; \
		esac; \
		fetch_codex_asset() { \
			asset="$1"; out="$2"; \
			if curl -4 -fsSL --max-time 60 --retry 2 --retry-max-time 45 --retry-connrefused --retry-all-errors \
				"https://github.com/openai/codex/releases/download/${CODEX_CLI_VERSION}/${asset}" \
				-o "$out"; then \
				return 0; \
			fi; \
			rm -f "$out"; \
			echo "GitHub Releases download failed for ${asset}; falling back to GitHub Releases API" >&2; \
			if [ ! -s /tmp/codex-release.json ]; then \
				curl -4 -fsSL --max-time 60 --retry 3 --retry-max-time 90 --retry-connrefused --retry-all-errors \
					-H "Accept: application/vnd.github+json" \
					"https://api.github.com/repos/openai/codex/releases/tags/${CODEX_CLI_VERSION}" \
					-o /tmp/codex-release.json || { rm -f /tmp/codex-release.json; return 1; }; \
			fi; \
			asset_url=$(grep -oE '"(\\.|[^"\\])*"|[{}]|[:,]' /tmp/codex-release.json | awk -v want="$asset" ' \
				$0 == "{" { d++; if (d == 2) { name = ""; url = "" }; key = ""; prev = ""; next } \
				$0 == "}" { \
					if (d == 2 && name == "\042" want "\042" && url ~ /^\042https:\/\/api\.github\.com\/repos\/openai\/codex\/releases\/assets\/[0-9]+\042$/) { \
						print substr(url, 2, length(url) - 2); exit \
					} \
					d--; key = ""; prev = ""; next \
				} \
				$0 == "," { key = ""; prev = ""; next } \
				d == 2 && $0 == ":" { key = prev; prev = ""; next } \
				d == 2 { \
					if (key == "\042name\042") name = $0; \
					else if (key == "\042url\042") url = $0; \
					key = ""; prev = $0 \
				} \
			' || true); \
			if [ -z "$asset_url" ]; then \
				echo "GitHub Releases API metadata for ${CODEX_CLI_VERSION} has no asset URL for ${asset}" >&2; \
				return 1; \
			fi; \
			curl -4 -fsSL --max-time 180 --retry 5 --retry-max-time 300 --retry-connrefused --retry-all-errors \
				-H "Accept: application/octet-stream" \
				"$asset_url" \
				-o "$out" || { rm -f "$out"; return 1; }; \
		}; \
		if fetch_codex_asset "codex-${target}.tar.gz" /tmp/codex.tar.gz && \
			fetch_codex_asset "codex-code-mode-host-${target}.tar.gz" /tmp/codex-code-mode-host.tar.gz; then \
			rm -f /tmp/codex-release.json; \
			echo "$sha256  /tmp/codex.tar.gz" | sha256sum -c -; \
			tar -xzf /tmp/codex.tar.gz -C /opt/codex; \
			rm -f /tmp/codex.tar.gz; \
			codex="$(find /opt/codex -type f -name 'codex*' ! -name 'codex-code-mode-host*' -perm -u+x | head -n1)"; \
			test -n "$codex"; \
			ln -sf "$codex" /usr/local/bin/codex; \
			echo "$host_sha256  /tmp/codex-code-mode-host.tar.gz" | sha256sum -c -; \
			tar -xzf /tmp/codex-code-mode-host.tar.gz -C /opt/codex; \
			rm -f /tmp/codex-code-mode-host.tar.gz; \
		else \
			rm -f /tmp/codex.tar.gz /tmp/codex-code-mode-host.tar.gz /tmp/codex-release.json; \
			echo "GitHub Releases download and API fallback failed for ${CODEX_CLI_VERSION}; falling back to digest-pinned @openai/codex npm archive" >&2; \
			curl -4 -fsSL --max-time 180 --retry 8 --retry-max-time 600 --retry-connrefused --retry-all-errors \
				"https://registry.npmjs.org/@openai/codex/-/codex-${CODEX_CLI_NPM_VERSION}-linux-${npm_arch}.tgz" \
				-o /tmp/codex-npm.tgz; \
			echo "$npm_sha256  /tmp/codex-npm.tgz" | sha256sum -c -; \
			tar -xzf /tmp/codex-npm.tgz -C /opt/codex --strip-components=4 \
				"package/vendor/${target}/bin/codex" \
				"package/vendor/${target}/bin/codex-code-mode-host"; \
			rm -f /tmp/codex-npm.tgz; \
			test -x /opt/codex/codex; \
			ln -sf /opt/codex/codex /usr/local/bin/codex; \
		fi; \
		code_mode_host="$(find /opt/codex -type f -name 'codex-code-mode-host*' -perm -u+x | head -n1)"; \
		test -n "$code_mode_host"; \
		if [ "$code_mode_host" != "/opt/codex/codex-code-mode-host" ]; then \
			ln -sf "$code_mode_host" /opt/codex/codex-code-mode-host; \
		fi; \
		ln -sf /opt/codex/codex-code-mode-host /usr/local/bin/codex-code-mode-host; \
		chmod -R a+rX /opt/codex; \
		codex --version >/dev/null; \
		/opt/codex/codex-code-mode-host --help >/dev/null; \
	fi

# What commit this image was built from, and the only thing a *published*
# image says about it.
#
# This is what CI's move-forward guard reads
# (../.github/workflows/build-artifacts.yml's publish job): before moving
# `latest` or a branch tag onto a new build it inspects the image that tag
# holds now, takes this label, and refuses the move if the commit named
# here is a descendant of the one being published. Without it the registry
# knows only "some image is called latest" and two runs finishing out of
# order leave that name on the older commit.
#
# Last in the file, after everything that takes time, because a
# --build-arg invalidates every layer that follows it and this one changes
# on every commit: at the top it would rebuild the apt layer and all three
# CLI installs each time. Here it is a label and nothing else.
#
# Empty by default, so a local `make image` publishes no claim about a
# tree that may not even be committed. The guard reads an absent or empty
# label as "unknown" and moves the tag, which is the fail-open behaviour
# it wants everywhere else too.
ARG GIT_REVISION=
LABEL org.opencontainers.image.revision="${GIT_REVISION}"
# The release the binary above was stamped with, as a label too, so that
# `release-images.sh promote` can ask the registry whether a published
# vX.Y.Z already says it -- without pulling the image -- and leave it
# (and `latest`'s digest) alone when it does.
ARG RELEASE=
LABEL org.opencontainers.image.version="${RELEASE}"

# No USER of its own. The unit scripts/setup.sh writes always passes
# --user with the host's own grain uid:gid, because everything this
# process writes -- the store, the secrets database, each sandbox's
# working tree -- lands in a host directory bind-mounted in, and has to
# come out owned by the account that owns those directories on the host
# rather than by whatever uid this image happened to name.
#
# That is an argument for naming no uid *here*, not for running as root:
# what a caller that names none gets is uid 0, which is why
# deploy/kubernetes/grain.yaml names one in its pod securityContext (uid
# 1000, with the fsGroup its volume needs) rather than leaving it to this
# file. Both paths run this image unprivileged; neither does it from a
# USER line, because the right uid is the deployment's to know.
#
# tini as PID 1: the daemon forks agent CLIs which fork MCP servers of
# their own (pkg/procgroup), and a container's PID 1 is the only reaper
# there is -- without one, every one of those that outlives its parent
# stays a zombie in this namespace for as long as the daemon runs.
#
# ENTRYPOINT is the binary and CMD its subcommand, so `docker run <image>
# schema-version` reads as running the CLI (which is exactly how
# setup.sh's own /usr/local/bin/grain wrapper and pkg/upgrade's image
# health check use it) while the unit passes `daemon` and its flags.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/grain"]
CMD ["daemon"]
