# Mirrors the steps .github/workflows/tests.yml's go-test job runs, so
# `make` reproduces CI locally instead of needing paraphrased README
# instructions kept in sync by hand.

BIN := bin
CMDS := grain grit

BUILDVCS ?= auto

# The sandbox container this build of grain goes with -- stamped into the
# binary, so a deployment needs to be told nothing and a rollback to an
# older tag asks for its *own* older sandbox rather than today's. See
# pkg/cli/sandboximage.go for what reads it and why it is a build-time
# value at all.
#
# Empty here on purpose: the source default in that file (the tag CI
# keeps pointed at main) is what an unstamped `make build` should get,
# and stamping an empty string over it would replace a real answer with
# no answer. .github/workflows/build-artifacts.yml passes the exact
# sha- tag of the sandbox image built from this same commit.
SANDBOX_IMAGE ?=
# The equivalent image for Kubernetes sandbox pods; see SANDBOX_IMAGE above.
K8S_SANDBOX_IMAGE ?=

# This build's *own* image reference, stamped in for the same reason and
# with the same rules -- empty here so an unstamped build keeps the
# source default, CI passing the exact sha- tag it is publishing. It is
# what a deployment writes into the CI step of its own state repository,
# so that the check runs the build the deployment runs rather than
# whatever main points at today. See pkg/cli/grainimage.go.
#
# GRAIN_IMAGE_REF rather than GRAIN_IMAGE because scripts/setup.sh
# already means the repository with no tag on it by that second name, and
# what is stamped here is a full reference, tag included.
GRAIN_IMAGE_REF ?=

# The commit an `image` build was made from, labelled onto the image
# rather than stamped into the binary (see the Dockerfile's own
# org.opencontainers.image.revision comment). CI's publish job reads it
# back off whatever `latest` holds to decide whether moving that tag onto
# a new build would move it backwards, so it is the one record of which
# commit is behind a published image.
#
# Empty here for the reason the two above are, and one more: a local
# build is made from a working tree, not from a commit, and a label
# naming HEAD on an image built with uncommitted changes would be a claim
# nothing checks. .github/workflows/build-artifacts.yml passes
# $GITHUB_SHA, which is a commit that exists.
GIT_REVISION ?=
# The version stamped into the binary (pkg/version.Release) and onto the
# image (org.opencontainers.image.version): worked out from this
# checkout's tags and branch by scripts/build-version.sh -- main is the
# next minor's -dev.<n>, candidate its rc plus commits, the scheme in
# docs/release-candidate-management.md's "Build versions". A RELEASE given
# on the command line or in the environment wins, empty included: the
# Dockerfile's `make build RELEASE=${RELEASE}` stamps what `make image`
# worked out here rather than asking a builder's git, and a promotion's
# RELEASE=vX.Y.Z is the release. No git or no tags leaves it empty, and
# the binary reports itself unversioned.
ifeq ($(origin RELEASE),undefined)
RELEASE := $(shell bash scripts/build-version.sh 2>/dev/null)
endif
EDITION ?=

# The main bin/grain is built from: cmd/grain, core grain. A build that
# links registrations of its own into the same command (pkg/cli) has a
# main of its own and names it here. `make image` passes it on to the
# Dockerfile as MAIN_GRAIN.
MAIN_grain ?= ./cmd/grain
LDFLAGS := $(strip \
	$(if $(RELEASE),-X github.com/ai-on-gke/grain/pkg/version.Release=$(RELEASE),) \
	$(if $(EDITION),-X github.com/ai-on-gke/grain/pkg/version.Edition=$(EDITION),) \
	$(if $(SANDBOX_IMAGE),-X github.com/ai-on-gke/grain/pkg/daemon.DefaultSandboxImage=$(SANDBOX_IMAGE),) \
	$(if $(K8S_SANDBOX_IMAGE),-X github.com/ai-on-gke/grain/pkg/cli.defaultK8sSandboxImage=$(K8S_SANDBOX_IMAGE),) \
	$(if $(GRAIN_IMAGE_REF),-X github.com/ai-on-gke/grain/pkg/daemon.DefaultGrainImage=$(GRAIN_IMAGE_REF),))

# --- Containerised build ----------------------------------------------
#
# grain is pure Go (bwsalmon/agents#366 removed the one dependency that
# wasn't -- embedded Dolt's go-icu-regex, which needed cgo and, through
# it, a build host whose ICU archives and glibc agreed with the
# controller's), so `make build` alone is already what most checkouts
# want: no C toolchain, no ICU headers, and the binary it produces is
# statically linked with no runtime library coupling to carry to the
# controller at all.
#
# `make container-build` runs this same `make build`, out of this same
# Makefile, inside Dockerfile.build's pinned Debian 12 toolchain -- the
# release the Granulefile's guest and deploy/terraform/gcp/variables.tf
# both deploy to -- so neither depends on the machine that drove it. It
# is not the default: it wants a container engine, and a first run pays
# for an image pull and a cold module cache, which is a poor trade on a
# host that agrees with itself. `make build` stays what CI and a working
# laptop use.
CONTAINER_ENGINE ?= docker
# Substituted whole on a network that does not reach Docker Hub, e.g.
# GO_IMAGE=mirror.gcr.io/library/golang.
GO_IMAGE ?= golang
BUILDER_IMAGE ?= grain-builder:bookworm
# Read out of go.mod rather than written down a second time here: the Go
# the container carries and the one the module asks for cannot drift if
# only one of them is ever edited. GOTOOLCHAIN=local in Dockerfile.build
# turns a stale image into an error that names both versions.
#
# The `toolchain` line when go.mod has one, the `go` directive when it
# does not. The directive is the oldest Go the module accepts and the
# toolchain line is the one it is built with -- it is how a security
# release (go1.26.9 for GO-2026-6617 and its siblings) is taken without
# raising the floor for every sandbox and host that already has an older
# patch release. Reading only the directive built every image with the
# floor and shipped the vulnerabilities the toolchain line was added to
# fix (GSA1-13).
GO_VERSION := $(shell v=$$(sed -n 's/^toolchain[[:space:]]\{1,\}go//p' go.mod); [ -n "$$v" ] || v=$$(sed -n 's/^go[[:space:]]\{1,\}//p' go.mod); echo $$v)
# Bind-mounted rather than left inside the container: a build cache that
# dies with the container makes every run a cold one. Under the working
# tree (and .gitignore'd) rather than in a named volume so that `rm -rf`
# is enough to clear it and nothing survives the checkout.
CONTAINER_CACHE ?= $(CURDIR)/.container-cache
# Runs as the invoking user so bin/grain and the caches come out owned by
# whoever ran make, not by root. Rootless podman already maps the caller
# to the container's root and needs this left empty -- CONTAINER_USER=
# does that, and the flag disappears with it.
CONTAINER_USER ?= $(shell id -u):$(shell id -g)

# --- the deployment image ---------------------------------------------
#
# `make image` builds Dockerfile (not Dockerfile.build: that one is the
# toolchain `container-build` above runs in) -- the runtime image a
# deployment actually runs, carrying bin/grain and every binary it shells
# out to. .github/workflows/build-artifacts.yml builds the same image
# the same way on every commit and pushes it to GHCR, so this target is
# for a local build against a change to the Dockerfile itself, not
# something a deployment ever needs to run.
#
# The context is the repository root, which is this directory: granulectl
# is built from third_party/granule and `go build`'s commit stamp comes
# from .git. GO_VERSION is passed for the same reason `builder` passes
# it -- one version, read out of go.mod.
IMAGE ?= grain:dev
.PHONY: all build test test-long test-e2e test-e2e-suite test-e2e-long playwright-browsers ui-screenshots vet vuln security fmt fmt-frontend clean builder container-build image frontend loadtest scalability soak merge-stress postgres-stress tunnel-stress long-term-test long-sim cli dist-cli $(CMDS)

all: vet test build

build: frontend $(CMDS)

# ui/ (bwsalmon/agents#356) is React+Vite, built into pkg/ui/static --
# the directory server.go embeds -- rather than checked in itself, so
# this has to run before `go build`/`go vet`/`go test` can see real
# content there. `npm ci` rather than `npm install`: a reproducible
# install from package-lock.json, the same reason `go build` trusts
# go.sum over re-resolving go.mod.
#
# The find clears out a previous build first rather than leaving Vite's
# own emptyOutDir to do it: that would also take pkg/ui/static's own
# .gitignore, .gitkeep and placeholder.html with it, the three files
# that let go:embed compile against this generated directory on a fresh
# checkout that has never run `npm run build` at all.
#
# placeholder.html is exempt because it is *tracked*, unlike everything
# else this deletes. Removing it left the checkout permanently dirty,
# which used to break every deploy after the first successful build on a
# host that built in place from a git clone: the installer refused to
# update a dirty checkout rather than clobber what might be an
# operator's edit. Nothing builds on a deployed host any more
# (bwsalmon/agents#645) and scripts/setup.sh keeps no checkout at all,
# so that particular failure is gone -- but a build that dirties the
# tree it was run in is still worth not doing.
#
# Keeping it cannot ship a placeholder in place of a real UI, which is
# what deleting it was guarding against: server.go serves this directory
# with http.FileServerFS, so "/" resolves to index.html once a build
# exists and placeholder.html is reachable only at its own path.
frontend:
	find pkg/ui/static -mindepth 1 -not -name '.gitignore' -not -name '.gitkeep' \
		-not -name 'placeholder.html' -delete
	cd ui && npm ci --omit=dev && npm run build

# BUILDVCS is passed explicitly rather than left to `go build`'s default
# (which is this same `auto`) so that turning it off is a documented word
# on the command line -- `make build BUILDVCS=false`, and likewise
# `make container-build BUILDVCS=false`, which forwards it -- rather than
# an edit to this file.
#
# It reaches `go build` through scripts/buildvcs.sh, which is where the
# three words stop meaning quite what `go build` means by them and start
# meaning what a build of this project wants:
#
#   auto   stamp the commit when this checkout's git can be read, and
#          build without the stamp -- saying so, and saying what git
#          said -- when it cannot. `go build` stops the build instead,
#          with "error obtaining VCS status: exit status 128", which is
#          how a tree an engineer's workstation git wrote but the build
#          image's git cannot read became the reason a whole `make image`
#          failed on a cloudtop with nothing but that line to go on.
#   true   stamp the commit or fail. What the paths that publish an image
#          pass (.github/workflows/build-artifacts.yml,
#          scripts/release-images.sh): a published image nobody can trace
#          back to a commit is worse than a build that stopped.
#   false  never stamp, whatever this tree holds. Still the word for an
#          export or a build context with no usable .git, and still the
#          one to reach for by hand.
#
# What is lost when the stamp is skipped is the stamp and nothing else:
# `go version -m bin/grain` stops reporting vcs.revision, pkg/version
# reads back an empty Info, and the UI's version line says so.
#
# CGO_ENABLED=0 here, and only here: with no cgo anywhere left in the
# dependency graph, the default (CGO_ENABLED=1) build still links
# dynamically against libc -- os/user and net's own cgo-based lookups
# pull it in even though nothing in this module writes any C -- which is
# exactly the "binary needs a newer glibc than the controller has"
# coupling embedded Dolt's ICU dependency used to cause one layer up.
# Forcing it off produces a genuinely static binary instead, with nothing
# left to carry to the controller. `test` and `vet` below leave
# CGO_ENABLED alone: `go test -race` requires it (the race detector is a
# cgo library), and nothing about testing this module ships anywhere.
$(CMDS):
	CGO_ENABLED=0 go build -buildvcs=$$(./scripts/buildvcs.sh $(BUILDVCS)) -ldflags "$(LDFLAGS)" -o $(BIN)/$@ $(or $(MAIN_$@),./cmd/$@)

cli: grain

dist-cli:
	./scripts/build-cli.sh $(BIN)/dist

builder:
	$(CONTAINER_ENGINE) build \
		--build-arg GO_IMAGE=$(GO_IMAGE) \
		--build-arg GO_VERSION=$(GO_VERSION) \
		-t $(BUILDER_IMAGE) -f Dockerfile.build .

# The whole checkout is mounted, `.git` included: `go build` stamps the
# binary with the commit it came from, which it can only read from the
# .git at the repository root.
#
# Which is also why safe.directory is set. The uid inside the container
# need not own the mounted tree -- it does not under rootless podman,
# which maps the invoking user to the container's root, nor under Docker
# with userns-remap, nor on a checkout owned by another account -- and
# git refuses to read a repository it sees as someone else's ("detected
# dubious ownership"). This is the fix that *keeps* the stamp, which is
# why it is here rather than left to scripts/buildvcs.sh above: that one
# gets the build moving again by dropping the stamp, and a build that can
# honestly have one should have it. Passed as GIT_CONFIG_* rather than
# `git config --global` so the exception lasts exactly one container and
# covers exactly the mount, instead of being written into a gitconfig
# somewhere that outlives the build.
container-build: builder
	@mkdir -p $(CONTAINER_CACHE)/build $(CONTAINER_CACHE)/mod
	$(CONTAINER_ENGINE) run --rm \
		$(if $(CONTAINER_USER),--user $(CONTAINER_USER),) \
		$(if $(GOPROXY),-e GOPROXY="$(GOPROXY)",) \
		$(if $(NPM_CONFIG_REGISTRY),-e NPM_CONFIG_REGISTRY="$(NPM_CONFIG_REGISTRY)",) \
		$(if $(PIP_INDEX_URL),-e PIP_INDEX_URL="$(PIP_INDEX_URL)",) \
		$(if $(PIP_TRUSTED_HOST),-e PIP_TRUSTED_HOST="$(PIP_TRUSTED_HOST)",) \
		-v "$(CURDIR)":/src \
		-v "$(CONTAINER_CACHE)/build":/gocache \
		-v "$(CONTAINER_CACHE)/mod":/gomodcache \
		-e GOCACHE=/gocache -e GOMODCACHE=/gomodcache -e HOME=/tmp \
		-e GIT_CONFIG_COUNT=1 \
		-e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src \
		-w /src $(BUILDER_IMAGE) make build BUILDVCS=$(BUILDVCS)

# Both need pkg/ui/static populated first, same as build: go:embed
# fails to compile -- not just to run -- against a static/ holding
# nothing but .gitkeep.
#
# -race, and the `go test` line here is character-for-character the one
# .github/workflows/tests.yml's go-test job runs -- tests/deploy's
# TestTheGoJobRunsTheSameSuiteCommandsTheMakefileDoes fails on a change to
# either file alone. It was not always so: the workflow ran a plain `go
# test ./...` while this file's header claimed to mirror it, so for the
# whole life of that claim the race detector had never run in CI, and a
# data race in the reconcile loop, a dispatch goroutine, an addendum
# poller or the stateManager lock only ever surfaced if a developer
# happened to run `make test` on a machine where it showed. It is worth
# roughly double the wall clock of the run.
#
# -timeout 30m, against `go test`'s 10m per package, because this suite's
# slowest package no longer fits in ten minutes on a slow machine and the
# failure it gets there is a panic that reads like a hang. Measured:
# pkg/staterepo takes 837s under -race on a three-core agent sandbox --
# 131 tests, each opening a real SQLite store and a real git repository,
# with no single one of them the pole. A hosted runner is nowhere near
# that (the go-test job's whole `go test -race ./...` step is ~329s
# across every package, so nothing in it is close to the default), which
# is the point: this raises the ceiling for the machines that need it and
# costs the runner nothing, since a suite that passes never spends its
# timeout. It is per package, not a total.
test: frontend
	go test -race -timeout 30m ./...
	cd ui && { [ -x node_modules/.bin/vitest ] || npm ci; } && npm test

# The same suite, turned up: what .github/workflows/nightly.yml runs
# while nobody is waiting for it.
#
# `test` above is what gates a pull request, and it is deliberately one
# pass in one order -- it has to finish inside the time the KVM jobs take
# anyway, or it becomes the thing everybody waits for. That leaves two
# whole classes of bug that a single pass cannot see, and this target is
# the place to spend the minutes on them:
#
#   -shuffle=on    order dependence. Go runs a package's tests in source
#                  order, so a test that only passes because an earlier
#                  one left a file, an env var or a package-level
#                  variable behind passes on every commit forever. The
#                  seed is printed, and re-running with
#                  -shuffle=<seed> reproduces the order exactly.
#   -count=2       state that leaks between runs of the *same* test --
#                  a sync.Once, a cached client, a t.TempDir the test
#                  assumed was empty, a port it assumed was free. Same
#                  process, so this is stricter than running the suite
#                  twice.
#
# Both are additions to `test`'s flags rather than replacements: -race
# stays, because the whole point is that this is the gate's own suite
# with more asked of it, not a different one.
#
# -timeout 90m against `go test`'s 10m-per-package default, because
# -count=2 doubles every package's own budget and the slowest packages
# here (cmd/grain, pkg/ui, pkg/orchestrator, tests/e2e) are minutes
# rather than seconds. It is a per-package limit, not a total.
test-long: frontend
	go test -race -shuffle=on -count=2 -timeout 90m ./...

# ui/e2e (bwsalmon/agents#415) drives the real built frontend through a
# real browser, over a real pkg/ui.Server -- a `grain demo` per
# Playwright worker, started by ui/e2e/fixtures.js out of the binary
# ui/e2e/global-setup.js builds first (grain/task-292) -- unlike `test`
# above, that needs a real browser on the machine, which `npx playwright
# install --with-deps` fetches and apt-installs itself.
# Left out of `test`/`all` and its own target instead: forcing a ~300MB
# browser download and a handful of apt packages (X11, fonts, codecs --
# see playwright.config.js) on every `make test` would be a poor trade
# for a checkout that just wants the unit suite; CI runs this as its own
# job (.github/workflows/tests.yml) so the coverage still exists.
#
# Two browsers since grain/task-74: Chromium runs the suite, WebKit runs
# e2e/phone.spec.js -- the touch and layout spec, whose gestures were
# written for an iPad and had until then only ever run on Chromium. See
# playwright.config.js's `projects` for what that second engine buys and
# what it still cannot check.
# Three targets rather than one, and the split is CI's rather than a
# person's (grain/task-498). `npx playwright install --with-deps` is a
# ~300MB download from cdn.playwright.dev and an apt-get: work that says
# something about this machine's network and nothing at all about this
# commit, so the job that runs it runs it as an `infrastructure:` step,
# where a CDN that will not answer errors the job instead of reddening
# the branch (.grain/workflows/tests.yml, pkg/ci.Step.Infrastructure).
# It can only be a step of its own if it is a target of its own, hence
# these two halves -- and `make test-e2e` still means what it always
# meant, which is what a person types.
#
# The install has to come after `frontend`, not before it: `npx
# playwright` resolves out of ui/node_modules, so on a machine that has
# not installed them yet npx would fetch whatever the registry calls
# latest and download the browsers for *that* version rather than for
# the one ui/package-lock.json pins.
#
# The prerequisites below are listed in the order they have to run in,
# which is the order a serial `make` takes them in; nothing here is
# written for `make -j`.
playwright-browsers:
	cd ui && { [ -x node_modules/.bin/playwright ] || npm ci; } && NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps $${PLAYWRIGHT_BROWSERS:-chromium webkit}

test-e2e-suite:
	cd ui && { [ -x node_modules/.bin/playwright ] || npm ci; } && npm run test:e2e $${PLAYWRIGHT_PROJECT:+-- --project="$$PLAYWRIGHT_PROJECT"}

test-e2e: frontend playwright-browsers test-e2e-suite

# Screenshots of the real UI over a `grain demo` store, at desktop and
# phone widths in light and dark, for looking at rather than asserting on
# (ui/e2e/screenshot.mjs; the "UI changes" rule in .grain/settings.yml):
#
#   make ui-screenshots PAGES=/,/inbox OUT=screenshots/after
#
# OUT is relative to ui/ and ignored by git there. WIDTHS and THEMES
# narrow the set, LOOK=1 has Gemini describe the shots, and the
# script's header lists the rest. Chromium only.
ui-screenshots: export PLAYWRIGHT_BROWSERS ?= chromium
ui-screenshots: frontend playwright-browsers
	cd ui && { [ -x node_modules/.bin/playwright ] || npm ci; } && node e2e/screenshot.mjs

# The Playwright suite's nightly form, for the same reason `test-long`
# exists: a browser suite driving a real server over a real network stack
# is where flake lives, and one green pass per commit cannot tell a test
# that always passes from one that passes four times in five. Three runs
# of each test is enough to make the second kind fail reliably, and cheap
# -- the whole suite is about ninety seconds.
#
# Three whole invocations rather than one `--repeat-each=3`, which is
# what this was until grain/task-236. Every spec here is written for one
# reader of a freshly seeded `grain demo` store making one pass over it,
# and half of what `--repeat-each` did to that is fixed now while the
# other half cannot be:
#
#   - The second pass inherits the first one's store, and a server per
#     worker (ui/e2e/fixtures.js, grain/task-292) does not change that:
#     a worker starts one `grain demo` and keeps it for every test it is
#     handed, so repeat 1 of a file reads whatever repeat 0 left behind
#     whenever the two land in the same worker. e2e/inbox.spec.js
#     answers the demo store's one parked question, and answering it
#     requeues that task for good, so the two tests that need a task
#     waiting on an answer fail on every repeat after the first -- at one
#     worker, every time, with no race in it at all. Nothing else in the
#     suite spends a fixture that way; those two were what `--repeat-each`
#     had been reporting nightly.
#   - Playwright schedules each (file, repeat index) as its own job, so
#     `fullyParallel: false` keeps a *file* serial but happily runs
#     repeat 0 and repeat 1 of it in two workers at once. That used to
#     mean two copies of e2e/phone.spec.js's "moves a task to the top of
#     the backlog" reordering one backlog and failing each other --
#     reproduced under grain/task-236 with `--workers=4
#     --repeat-each=2`, with inbox.spec.js left out so the deterministic
#     failure above was not doing the work. Two workers no longer share
#     a store, so this one is gone; it is the reason the flag looked
#     like a scheduling bug rather than the fixture reuse above.
#
# Each invocation starts its servers cold, so each pass gets the freshly
# seeded store the specs are written against, and the seeding and cold
# start are themselves exercised three times rather than once. The Go
# build is not: ui/e2e/global-setup.js builds `grain` once per
# invocation, and the second and third find the build cache warm.
#
# `|| exit 1` stops at the first red pass instead of running the other
# two: nightly.yml uploads
# ui/test-results on failure, and Playwright clears that directory when
# it starts, so a later green pass would take the failing pass's traces
# with it. The log still says which of the three it was.
#
# Both browsers, like `test-e2e`, and not because this target wants a
# second engine of its own: since grain/task-74 the suite *is* two
# projects, and `npm run test:e2e` runs both whatever this line
# installs. Fetching Chromium alone here does not narrow the run to
# Chromium -- it fails it, on a WebKit binary that was never downloaded.
# Narrowing would take `--project=chromium`, which would be a decision
# to stop repeating the touch spec on the engine its gestures were
# written for: the newest tests in the suite, on the engine with the
# least history here, which is the last pair this target should skip.
# The cost is one more ~300MB download and eleven more tests per pass,
# on a job that has all night.
test-e2e-long: frontend playwright-browsers
	for pass in 1 2 3; do echo "== e2e pass $$pass of 3 =="; (cd ui && { [ -x node_modules/.bin/playwright ] || npm ci; } && npm run test:e2e) || exit 1; done

vet: frontend
	go vet ./...

# The security gate on its own: go vet, then tests/security -- Layer 1's
# static rules over cmd/ and pkg/ and Layer 2's trust-boundary tests
# (identity, session tokens, artifact paths, sandbox containment). The
# `go` CI job already runs both as part of `test`; this is the verdict
# without the other few thousand tests, for a change that touches auth.
# `vuln` below stays separate and nightly, for the reason it says.
security:
	go vet ./...
	go test -race -count=1 ./tests/security/...

# Known vulnerabilities in what this module actually reaches.
#
# Nightly rather than per-commit, and it is the one check here whose
# verdict can change with no commit at all: the vulnerability database
# moves, so a tree that was clean this morning is not necessarily clean
# tonight. Gating a pull request on it would mean a disclosure against a
# transitive dependency reddens every open branch at once, which is a
# fire drill rather than a signal.
#
# govulncheck rather than a dependency scanner: it reports only the
# vulnerabilities on a call path the binary actually has, so what it
# prints is work to do rather than a list to triage. Unpinned, for the
# same reason live-agent.yml installs an unpinned agy -- the point is to
# ask today's database today.
#
# The toolchain is pinned to GO_VERSION, the one every image above is
# built with, because govulncheck's standard-library findings are about
# the Go it runs under. GOTOOLCHAIN=auto picked go.mod's toolchain line
# while the images were built with the `go` directive, so this scanned a
# compiler nothing shipped and reported green over 13 reachable stdlib
# vulnerabilities (GSA1-13). Naming the version rather than saying
# `local` keeps that true on a host whose own Go is older or newer than
# the images': the scan is of what ships, not of what is installed.
vuln:
	GOTOOLCHAIN=go$(GO_VERSION) go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# The sustained concurrent-load harness (tests/e2e/loadtest_test.go,
# bwsalmon/agents#416) -- skipped by `test` above (GRAIN_LOAD_TEST unset)
# since it takes minutes rather than seconds and is meant to be run by
# hand against a host actually sized for it, not on every commit. See
# that file's own doc comment for how to size it up further; every
# GRAIN_LOAD_TEST_* env var below is optional.
#
# `test` above is not blind to that harness, though: TestLoadHarnessSmoke
# drives the same setup and the same dispatch path at a handful of ticks
# and is not gated on GRAIN_LOAD_TEST, so a commit that breaks this
# harness fails there rather than in the nightly that runs this target.
loadtest: frontend
	GRAIN_LOAD_TEST=1 ./scripts/go-test-must-pass.sh TestLoadSustainedConcurrency ./tests/e2e/ -timeout 30m

# Scalability testing for Grain metadata, teams, concurrent agents, concurrent CI,
# and high-throughput API operations (tests/e2e/scalability_test.go). Skipped by `test`
# above (GRAIN_SCALABILITY_TEST unset); this target runs the comprehensive benchmark.
# TestScalabilitySmoke runs on standard test suites to prevent test rot.
scalability: frontend
	GRAIN_SCALABILITY_TEST=1 ./scripts/go-test-must-pass.sh TestScalabilityLimits ./tests/e2e/ -timeout 30m

# The state repository soak (pkg/staterepo/soak_test.go) turned all the
# way up. Unlike `loadtest` above this one is not skipped by `make test`
# -- sixty rounds of it run on every commit, which is half a minute --
# so this target is for the long form: a couple of thousand rounds of
# merges, failed pushes, divergences, restarts and restores, which is
# what a change to that package deserves before it goes anywhere.
# GRAIN_STATEREPO_SOAK_SEED reproduces a run that failed; every failure
# prints the seed it came from.
#
# No `frontend` dependency, unlike `loadtest` above. That one starts a
# real server and serves the built UI out of it; pkg/staterepo does not
# import pkg/ui at all, in its tests or otherwise, so an npm ci and a
# vite build here bought nothing but two minutes on the front of every
# nightly run of it.
#
# Two thousand rounds is about twelve minutes on a four-CPU host. The
# -timeout is five times that and is deliberately under the nightly
# job's own timeout-minutes, so a soak that hangs is killed by `go test`
# -- which prints every goroutine's stack -- rather than by the job,
# which prints nothing.
soak:
	GRAIN_STATEREPO_SOAK_ROUNDS=$${GRAIN_STATEREPO_SOAK_ROUNDS:-2000} \
		go test ./pkg/staterepo/ -run TestSoak -v -timeout 60m

# The sustained merge liveness stress test (tests/e2e/merge_stress_test.go) --
# creates repositories, dispatches concurrent and overlapping conflicting edits
# through the merge queue, and ensures all tasks reach terminal states without
# getting stuck.
#
# `make test` runs the deterministic, fast form (TestMergeLivenessStressEndToEnd).
# This target runs the long sustained form (TestMergeStressSustained) for extended
# verification. Every GRAIN_MERGE_STRESS_* env var is optional.
merge-stress:
	GRAIN_MERGE_STRESS=1 ./scripts/go-test-must-pass.sh TestMergeStressSustained ./tests/e2e/ -timeout 60m

# The sustained postgres concurrency and stress test suite (pkg/model/postgres/stress_test.go) --
# runs high concurrency CRUD, pool saturation, transaction rollbacks, and schema lifecycles.
postgres-stress:
	GRAIN_POSTGRES_STRESS_ITERATIONS=$${GRAIN_POSTGRES_STRESS_ITERATIONS:-50} \
	GRAIN_POSTGRES_STRESS_DURATION=$${GRAIN_POSTGRES_STRESS_DURATION:-15s} \
	go test -v -race ./pkg/model/postgres/... -run TestPostgres -timeout 15m

# The sustained end-to-end long term test suite (tests/e2e/longterm_test.go) --
# runs full Grain instance with end-to-end sandboxes, agents, multi-repo smart HTTP upstream,
# continuous coding tasks, merge conflicts, broken main events, multi-team isolation, and invariant checks.
#
# `make test` runs the fast smoke form (TestLongTermEndToEndSmoke).
# This target runs the long sustained form (TestLongTermEndToEndSustained) for extended verification.
long-term-test:
	GRAIN_LONG_TERM_TEST=1 ./scripts/go-test-must-pass.sh TestLongTermEndToEndSustained ./tests/e2e/ -timeout 60m

# The long randomized cluster simulation (TestRandomizedClusterLong in
# tests/e2e/random_test.go, bwsalmon/agents#338): the same driver as
# TestRandomizedClusterEndToEnd, which `make test` runs at a fixed seed
# and 25 rounds, run against the clock with a fresh seed each time.
# GRAIN_LONG_SIM_DURATION (default 20m) sets how long; the -timeout
# leaves room over it. A failure logs the seed, and
# GRAIN_LONG_SIM_SEED=<a>,<b> replays it.
long-sim:
	GRAIN_LONG_SIM=1 GRAIN_LONG_SIM_DURATION=$${GRAIN_LONG_SIM_DURATION:-20m} \
		./scripts/go-test-must-pass.sh TestRandomizedClusterLong ./tests/e2e/ -timeout 40m

# The sustained reverse tunnel multiplexer stress suite (pkg/tunnel/...) --
# runs high concurrency bidirectional streams, payload integrity verification (up to 64KB),
# SSE streaming, WebSockets, client cancellations, keepalive pings, and connection drops/reconnects.
tunnel-stress:
	GRAIN_TUNNEL_STRESS_DURATION=$${GRAIN_TUNNEL_STRESS_DURATION:-30m} \
		go test -v -race ./pkg/tunnel/... -timeout 60m

# Upgrade test suite -- verifies upgrading from the previous version of the software
# to the current version, ensuring data, tasks, schemas, and daemon health transition smoothly.
test-upgrade:
	GRAIN_TEST_IMAGE=$${GRAIN_TEST_IMAGE:-grain-e2e:test} \
		go test -v -timeout 30m ./tests/container/ -run 'TestUpgradeFromPreviousVersion|TestAnUpgrade'


# The formatting gate, run here and by the go job in
# .github/workflows/tests.yml -- one definition, so a local `make fmt`
# and a red build mean the same thing. It fails the way `go vet` does
# when a file needs gofmt, instead of only listing it, and prints the
# diff for each offender rather than leaving the reader to run gofmt
# themselves (the terraform job's `fmt -check -diff` is the same idea).
#
# Plain gofmt, not `gofmt -s`. The point of this gate is that
# `gofmt -l` stays a usable local signal: an editor that formats on save
# should never produce a diff in a file the change did not otherwise
# touch, and what those editors run is plain gofmt. (The tree is -s
# clean today as well; that is not what is being promised here.)
#
# third_party/granule is excluded. It is a separate module of vendored
# upstream code -- `go vet ./...` and `go test ./...` do not reach into
# it either -- and a red build over formatting we would not be the ones
# to fix is a gate nobody can act on.
fmt:
	@files="$$(find . -name '*.go' -not -path './third_party/*')"; \
	unformatted="$$(gofmt -l $$files)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; echo; \
		gofmt -d $$unformatted; \
		exit 1; \
	fi

# The same gate for the frontend, which until now had none of any kind:
# no prettier, no eslint, no .editorconfig, so whatever each editor did
# on save is what landed. That is the drift `fmt` above exists to stop,
# and it is worse in JS than in Go, because format-on-save is the
# default in a lot of JS setups and prettier's opinions are much larger
# than gofmt's -- one contributor with that switch on silently rewrites
# every file they open.
#
# Prettier at its defaults, configured by an empty ui/.prettierrc, for
# the reason `fmt` runs plain gofmt: the promise is "whatever the
# standard tool does", so there is no house style to remember and no
# number of ours for a reviewer to argue with. `--check` prints the
# files that differ; `npm run format` writes them.
#
# Kept out of `fmt`, and out of `all`, on purpose. `fmt` needs nothing
# but the Go toolchain and runs in seconds, and hanging an npm install
# off it would spoil exactly the property that makes it worth running
# locally. This one needs ui/node_modules, so it installs them when they
# are missing -- but only then: CI runs this after `make frontend` has
# already done the install, and paying for a second `npm ci` there would
# be pure waste.
fmt-frontend:
	cd ui && { [ -x node_modules/.bin/prettier ] || npm ci; } && npm run format:check

clean:
	rm -rf $(BIN) $(CONTAINER_CACHE)

image:
	$(CONTAINER_ENGINE) build \
		--build-arg GO_IMAGE=$(GO_IMAGE) \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg BUILDVCS=$(BUILDVCS) \
		--build-arg MAIN_GRAIN=$(MAIN_grain) \
		$(if $(SANDBOX_IMAGE),--build-arg SANDBOX_IMAGE=$(SANDBOX_IMAGE),) \
		$(if $(K8S_SANDBOX_IMAGE),--build-arg K8S_SANDBOX_IMAGE=$(K8S_SANDBOX_IMAGE),) \
		$(if $(GRAIN_IMAGE_REF),--build-arg GRAIN_IMAGE_REF=$(GRAIN_IMAGE_REF),) \
		$(if $(GIT_REVISION),--build-arg GIT_REVISION=$(GIT_REVISION),) \
		$(if $(RELEASE),--build-arg RELEASE=$(RELEASE),) \
		-t $(IMAGE) -f Dockerfile .
