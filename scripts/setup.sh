#!/usr/bin/env bash
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

# Installer and updater for a v2 grain deployment, run directly on the
# target machine -- bwsalmon/agents#355.
#
# v1's shape (../docs/design.md) was a controller VM plus a pool of
# sandbox VMs, all built by a Python host adapter driving libvirt -- code
# this repository no longer carries. This script does the simpler thing
# the issue actually asks for: run the one `grain` binary directly on
# this machine, as a single systemd service, with no controller VM
# anywhere in the picture.
#
# What each dispatched task runs in is a separate question. By default
# this script installs granule sandboxing -- one bwsalmon/granule-managed
# VM per run, created and destroyed with it -- on a host with /dev/kvm, and
# container sandboxing -- one local Docker container per run -- on one
# without. Every run is isolated from this host either way, unless an
# operator asks for the third shape by name: GRAIN_HOST_SANDBOXES=1, every
# run in a plain directory under $GRAIN_SANDBOX_DIR with no isolation at
# all (-host-sandboxes) -- for a single-user workstation, and refused on
# a deployment others can reach (see that variable's own comment).
#
# What this script does, every time it runs (safe to re-run -- this is
# the installer AND the updater):
#   1. pulls the deployment image -- $GRAIN_IMAGE:$GRAIN_IMAGE_TAG,
#      published to GHCR by ../.github/workflows/build-artifacts.yml
#      on every commit -- instead of building a binary here
#      (bwsalmon/agents#645). That image carries grain *and* every binary
#      it shells out to: git, curl, the docker CLI, granule, and every
#      agent CLI -- claude, agy and codex (Dockerfile) -- plus a copy of the
#      source it was built from. So it is also where the handful of
#      steps here that want more than a shell go looking, rather than at
#      this host: see "What this host has to have" below
#   2. installs /usr/local/bin/grain and /usr/local/bin/granule as thin
#      wrappers that run that same image (install_cli_wrappers), so an
#      operator's own shell -- and the rest of this script, which uses
#      `grain schema-version` and `grain secrets` -- reaches the exact
#      build the service runs, with nothing installed on the host to
#      drift out of step with it
#   3. creates an unprivileged system user to run the container as
#   4. installs the two systemd path units that let that account act on
#      the host it cannot reach from inside a container: the UI's
#      reboot-host button (bwsalmon/agents#395) and the restart its
#      Upgrade button needs (bwsalmon/agents#396) each become a touch of
#      a file under $GRAIN_DATA_DIR/control, watched by a unit out here
#      (write_control_units). That replaces the two NOPASSWD sudoers
#      drop-ins this used to install, and grants strictly less: the
#      daemon can restart its own service and reboot this machine, and
#      has no sudo at all
#   5. lays out the rest of $GRAIN_DATA_DIR (secrets, the embedded SQLite
#      store) and seeds secrets from environment variables -- only if
#      they are not already there, so a second run never overwrites a
#      credential placed by the first one or by hand. Also lays out
#      $GRAIN_SANDBOX_DIR, HostSandboxes' own per-slot working
#      directories -- deliberately not under $GRAIN_DATA_DIR
#      (bwsalmon/agents#587): a task's checked-out repo is disposable,
#      unlike the store and secrets, so it belongs on whatever storage a
#      VM wipe or redeploy is free to discard along with the rest of the
#      host, not the one directory meant to survive it
#   6. compares the freshly built binary's schema version (`grain
#      schema-version`) against the version recorded in
#      $GRAIN_DATA_DIR/.schema_version from the last run of this script,
#      and if they differ carries the store's rows across the way the
#      UI's Upgrade button does: `grain state prepare-upgrade` exports
#      them and the daemon's next start rebuilds the store from them,
#      keeping the old one under a timestamped name. Only if that cannot
#      be done, and GRAIN_ALLOW_STORE_RESET=1 says so, is the store moved
#      aside -- never deleted -- for grain to start a fresh, empty one
#      (bwsalmon/agents#394, grain/task-4357; see
#      reformat_store_if_schema_changed below). A schema version that has
#      not changed since the last run leaves the store exactly as it was.
#   7. if $GRAIN_TARGET_REPO is set and has no commits yet, pushes one
#      empty commit so grain has a branch to work from ("format" it --
#      grain always branches off an existing ref, never creates one)
#   8. writes and enables grain-daemon.service, so it comes back on
#      reboot, and restarts it (not just "enable --now") so a second
#      run's new binary actually takes effect -- see
#      v1's own notes on the same failure for why
#      enable-without-restart was already a bug once in v1's own proxy
#      service
#   9. optionally (GRAIN_TAILSCALE_ENABLE=1) installs tailscale on this
#      host, joins it to a tailnet with GRAIN_TAILSCALE_AUTH_KEY, and
#      hands the UI's own loopback port to `tailscale serve`, so the UI
#      is reachable from the tailnet without an SSH tunnel and without a
#      firewall hole -- the "or put it behind Tailscale" this file has
#      always suggested and never done anything about (setup_tailscale).
#      Off by default, and non-fatal at every step: a host that cannot
#      install tailscale, or has no way to log in yet, still finishes
#      this run with a working loopback-only daemon rather than no
#      deployment at all
#
# Alongside those, and before the daemon is started, it takes this host
# out of the business of suspending itself (disable_host_suspend): a
# deployment runs unattended agent work for hours at a stretch, and a
# machine that drops into S3 halfway through does not slow that work
# down, it stops it -- mid-clone, mid-push, with the UI unreachable and
# nothing in any log saying why. That is a real deployment shape here
# (a desktop or laptop under someone's desk), not a hypothetical one, and
# a cloud VM that never suspends anyway loses nothing by being told not
# to. GRAIN_DISABLE_SUSPEND=0 opts out, and undoes it on a re-run.
#
# One shape: grain-daemon.service is a `docker run` of the deployment
# image, which is everything this header describes above. The few
# programs that have to run on the bare machine as $GRAIN_USER -- agy
# under the installing user's corp identity, a `run: host` capability
# server -- are started by grain hostd (grain-hostd.service), which every
# install gets unless GRAIN_HOSTD=0 leaves it out, and which starts only
# what /etc/grain/hostd.yaml allows.
#
# Host mode -- GRAIN_INSTALL_MODE=host, the daemon itself as a plain
# systemd service on this machine -- is gone (task 5041). A re-run on a
# host-mode install moves it to container mode plus hostd, keeping its
# data directory and its unit name; GRAIN_INSTALL_MODE=host is refused.
#
# An upgrade pulls the image and restarts the unit, so "upgrade to branch
# X" means one thing, and a rollback by GRAIN_IMAGE_TAG still does.
#
# What this host has to have: docker and systemd. That is the whole list.
# Everything else this script runs is either a shell builtin or part of a
# base system install -- coreutils, and the `useradd` every distribution
# ships -- so there is no package a minimal cloud image lacks standing
# between an operator and a deployment.
#
# *This script* needed `git` and `jq` until recently, and installed both
# itself on any host without them. Both are gone from the script, and so
# is the checkout git was there to maintain:
#
#   * the source is *in* the image (Dockerfile's own
#     /usr/local/share/grain/src), so the one thing out here that still
#     reads it -- scripts/granule's guest image build -- unpacks it from
#     the image this run is installing (unpack_image_source) instead of
#     from a checkout tracking a branch. That closes the same drift the
#     self-debug capability was moved into the image to close: a
#     checkout follows a branch and an image tag never moves, so the two
#     disagree on every upgrade and every rollback
#   * the two steps that want a real git -- the `git ls-remote` against
#     GRAIN_TARGET_REPO and the empty commit pushed to it -- run git
#     *inside* that image (image_run), which carries one
#   * so does the one that wanted curl and jq: the GCP metadata token
#     that authenticates docker to an Artifact Registry
#
# One consequence worth naming: this script no longer replaces itself
# mid-run. It used to (sync_repo pulled a new copy over the file this
# process was reading, and reexec_if_updated handed over to it), which
# is only a problem worth solving for a script that updates its own
# source. Nothing rewrites this file underneath it now, so the copy an
# operator ran is the copy that finishes. Keeping that copy current is
# the job of whatever put it there: terraform/gcp/files/deploy.sh on the
# GCP path, a `git pull` in your own checkout by hand.
#
# Every setting is an environment variable, not a flag, so the common
# case is `sudo GRAIN_GITHUB_TOKEN=... GRAIN_GEMINI_API_KEY=... ./setup.sh`
# and a re-run to pick up a newly published image is `sudo ./setup.sh`
# with no arguments at all. Run with -h/--help for the full list.
#
# Most daemon settings below (everything except GRAIN_UI_ADDR,
# GRAIN_TARGET_REPO/GRAIN_TARGET_BRANCH and GRAIN_ENABLE_UI_UPGRADE) are
# only *seeded* from these variables, the first time a deployment's store
# has none (pkg/cli/daemon.go's loadConfig, bwsalmon/agents#320) --
# passing this script a new GRAIN_GITHUB_HOST or GRAIN_MAX_WORKERS on
# a later re-run has no effect on a deployment that already has one, and
# loadConfig now logs a line saying so on every start it happens. Change
# an already-seeded value with `grain settings` (or the UI's Settings
# pane) instead -- see each variable's own note below for which ones this
# applies to (bwsalmon/agents#574).
#
# There used to be a second service (grain-ui.service) and, before
# bwsalmon/agents#366 replaced it with embedded SQLite, a `dolt
# sql-server` container behind it: embedded Dolt was single-writer, and a
# daemon plus a UI on the same store both used to need to write.
# bwsalmon/agents#363 removed the second writer -- the daemon now serves
# the UI/API itself, in-process, over the store it already has open (see
# pkg/cli/daemon.go's own doc comment) -- so this script still
# installs exactly one service and needs no separate store container.
# What did come back is `docker` at runtime: that one service is now a
# `docker run` of the deployment image rather than a binary on this
# host's disk.
#
# What the container is given, and what it deliberately is not, is worth
# reading once (docker_run_args, below). It gets host networking (the
# UI's port, and the git proxy every sandbox reaches), the data and
# sandbox directories bind-mounted at the same paths they have out here,
# and -- only when something actually needs it -- the docker socket. It
# runs as $GRAIN_USER's own uid:gid, never as root, so every file it
# writes into those directories comes out owned exactly as it was before
# any of this ran in a container at all.
#
# A fresh install binds the UI to every interface on port 4242
# (0.0.0.0:4242 -- the container shares the host's network, so that is
# the host's own port), at http://<this-host>:4242/, and a re-run keeps
# whatever address the installed unit already uses. Every caller,
# loopback included, presents the access code (grain access-code prints
# it), and the Host check (-ui-allowed-hosts) defaults to this machine's own names.
# Nothing here opens a firewall hole for it -- the closing report only
# says whether one seems to be in the way. To keep it off the network,
# set GRAIN_UI_ADDR=127.0.0.1:4242 and forward that one port over SSH:
# `ssh -L 4242:localhost:4242 <this-host>`, then open
# http://localhost:4242 locally, or put it behind Tailscale/IAP.
# GRAIN_TAILSCALE_ENABLE=1 is the first of those two this script will set
# up for you (setup_tailscale): tailscaled -- a process on this host,
# outside the container -- proxies the tailnet to the UI's port on
# loopback, whatever GRAIN_UI_ADDR binds. See pkg/ui/README's own
# "single-operator tool" framing (README.md, "The UI") for why that is
# the whole access-control story today -- the API and the UI it serves
# carry no auth of their own, so whatever reaches -ui-addr can act as the
# deployment's one configured actor.

set -euo pipefail

# --- configuration (every value overridable via environment) ----------

# Which channel this deployment follows: latest (the last promoted
# release, and a new deployment's default), candidate or main. It names
# no checkout -- nothing here keeps one (see "What this host has to
# have", above) -- only which published image to run, through
# GRAIN_IMAGE_TAG below. Unset on an existing deployment keeps the
# channel it already runs (resolve_image_tag), so a re-run never moves
# it from one channel to another unless asked.
GRAIN_CHANNEL="${GRAIN_CHANNEL:-}"
# The older spelling of the same choice, as any branch rather than one of
# the three channels: GRAIN_REF=release/2.1 runs that branch's image
# (deploy/terraform/gcp passes it). Honoured when GRAIN_CHANNEL is unset.
GRAIN_REF="${GRAIN_REF:-}"
# Whether this run was told a data directory, before the default below
# fills one in: an uninstall that was not reads the one the install
# recorded instead (uninstall_grain).
#
# 2.0's layout keeps everything under one root, split in two: data/ is
# the state to back up (GRAIN_DATA_DIR) and scratch/ the disposable run
# space and caches (GRAIN_SANDBOX_DIR, below). The root itself is
# root-owned and holds nothing but those two (setup_layout_root), so
# either can be a mount of its own. A re-run on a host whose
# /etc/grain/cli.env records another data directory keeps it
# (resolve_recorded_layout), and a fresh install refuses a 1.x flat data
# directory left at the root (refuse_flat_layout).
GRAIN_ROOT_DIR="/var/lib/grain"
GRAIN_DEFAULT_DATA_DIR="$GRAIN_ROOT_DIR/data"
GRAIN_DEFAULT_SANDBOX_DIR="$GRAIN_ROOT_DIR/scratch"
GRAIN_DATA_DIR_GIVEN="${GRAIN_DATA_DIR:+1}"
GRAIN_DATA_DIR="${GRAIN_DATA_DIR:-$GRAIN_DEFAULT_DATA_DIR}"
# A schema change carries the store's rows across (reformat_store_if_schema_changed);
# GRAIN_ALLOW_STORE_RESET=1 is the operator's say-so for the one fallback that
# does not -- moving the store aside and starting empty -- when they cannot be.
GRAIN_ALLOW_STORE_RESET="${GRAIN_ALLOW_STORE_RESET:-0}"
# What that step did, for report_readiness: "", "migrated" or "reset".
GRAIN_SCHEMA_CHANGE_REPORT=""
GRAIN_SCHEMA_CHANGE_FROM=""
GRAIN_SCHEMA_CHANGE_STAMP=""
GRAIN_DB_DRIVER="${GRAIN_DB_DRIVER:-sqlite}"
GRAIN_DB_DSN="${GRAIN_DB_DSN:-}"
# Root for this deployment's disposable sandbox-side data: host-sandbox
# run directories and the package and Actions caches (bwsalmon/agents#587,
# see this file's own header comment, item 5). Unlike GRAIN_DATA_DIR,
# nothing under here needs to survive a redeploy or belongs in a backup,
# so it is scratch/ beside data/ rather than inside it.
#
# Disposable is not the same as small, though, which is why it is worth a
# disk of its own (terraform/gcp gives it one, its sandbox_disk_gb,
# mounted here by files/startup.sh with docker's data root beside it): a
# checkout that fills the disk the store is on takes the store down with
# it. The daemon's flag for it is -sandbox-dir.
GRAIN_SANDBOX_DIR_GIVEN="${GRAIN_SANDBOX_DIR:+1}"
GRAIN_SANDBOX_DIR="${GRAIN_SANDBOX_DIR:-$GRAIN_DEFAULT_SANDBOX_DIR}"
GRAIN_USER="${GRAIN_USER:-grain}"
# The group every file this script lays out is owned by: $GRAIN_USER's own
# primary group, resolved by ensure_user once that account is known to
# exist. Empty until then, and never written by an operator -- it is not a
# choice, it is whatever the account already is.
#
# It used to be spelled "$GRAIN_USER" at each of the twenty places that
# needed it, which is the same string for the account this script creates
# (useradd gives a system user a group of its own name) and not for one an
# operator named instead: `install -g brandon` on a host where that
# account's group is `staff` fails, and failed halfway through laying out
# the data directory.
GRAIN_GROUP=""

# --- where the daemon runs -----------------------------
#
# Only "container" is left: grain-daemon.service is a `docker run` of
# GRAIN_IMAGE below. It is still read so that GRAIN_INSTALL_MODE=host,
# from an operator's environment or an old runbook, is refused rather
# than quietly ignored (resolve_install_mode).
GRAIN_INSTALL_MODE="${GRAIN_INSTALL_MODE:-}"
# --- grain hostd (docs/design/cli-run-location.md) ----------------------
#
# A container-mode daemon starts the few programs that must run on the
# bare machine -- agy under the installing user's corp identity, a stdio
# capability server with `run: host` -- through `grain hostd`: the same
# binary, taken out of the image (install_hostd_binary), run by its own
# unit, grain-hostd.service, as $GRAIN_USER and outside the daemon's
# lifecycle. It starts only what the host's own allowlist,
# /etc/grain/hostd.yaml, names (write_hostd_allowlist).
#
# Every install gets it: "auto" and 1, the default, install it, and 0
# leaves it out -- removing an installed one -- for a deployment where
# nothing runs as $GRAIN_USER on the bare machine and "host" is not
# offered, as on Kubernetes. Its allowlist offers the built-in host
# capabilities (host-git, grain-gchat, grain-gcp, grain-host-script), plus
# agy when GRAIN_AGY_PATH is set. auto with GRAIN_USER=root installs none:
# hostd refuses to run as root.
GRAIN_HOSTD="${GRAIN_HOSTD:-auto}"

# --- the deployment image (bwsalmon/agents#645) ------------------------
#
# GRAIN_IMAGE is the repository CI publishes to on every commit, with no
# tag: ../.github/workflows/build-artifacts.yml's own
# publish-grain-image job. GRAIN_IMAGE_TAG picks which one to run. Left
# unset, resolve_image_tag works it out: GRAIN_CHANNEL's tag, or the tag
# published for GRAIN_REF, or -- with neither -- the tag the deployment
# already runs, or latest for a new one. '/' is not legal in a docker
# tag and grain's branches routinely contain one, so GRAIN_REF's
# becomes '..', the same
# substitution CI makes when it pushes and pkg/upgrade's TagForBranch
# makes when the UI's Upgrade button resolves a branch -- '..' because
# git refuses it in a branch name, so no two branches share a tag
# (#2386; '-' made claude/x and claude-x one image).
#
# Set GRAIN_IMAGE_TAG explicitly to pin a deployment to one immutable
# build -- sha-<short sha>, also published on every commit -- which is
# what a rollback looks like here: re-run this script with the tag of a
# build that worked. An explicit GRAIN_IMAGE_TAG always wins.
#
# Three names move, and they mean different things: `main` is the head
# of main, `candidate` is the current release candidate, and `latest` is
# the last promoted release (v2.X.0, retagged by
# .grain/workflows/promote.yml) -- never main's head. A release is also
# published under its own name, so GRAIN_IMAGE_TAG=v1.5.0 pins one.
GRAIN_LEGACY_IMAGE_REPO="${GRAIN_LEGACY_IMAGE_REPO:-ghcr.io/bwsalmon/grain/grain}"
GRAIN_OSS_IMAGE_REPO="${GRAIN_OSS_IMAGE_REPO:-$GRAIN_LEGACY_IMAGE_REPO}"
GRAIN_ALLOWED_IMAGE_REPOS="${GRAIN_ALLOWED_IMAGE_REPOS:-}"
GRAIN_INSTALL_TRACK="${GRAIN_INSTALL_TRACK:-}"
GRAIN_IMAGE_EXPLICIT="${GRAIN_IMAGE:-}"
GRAIN_IMAGE="${GRAIN_IMAGE:-ghcr.io/bwsalmon/grain/grain}"
GRAIN_IMAGE_TAG="${GRAIN_IMAGE_TAG:-}"
# Credentials for `docker login` against the image's registry, needed
# only if the package is private -- bwsalmon/grain and its packages are
# public, so an ordinary deployment sets neither and pulls anonymously.
# A GitHub PAT with read:packages is what these want when they are
# needed; GRAIN_GITHUB_TOKEN is deliberately *not* reused by default,
# since the credential grain clones repositories with and the credential
# it pulls images with are not the same decision.
GRAIN_IMAGE_PULL_USER="${GRAIN_IMAGE_PULL_USER:-}"
# The cosign public key the images this host pulls must be signed with
# (docs/image-signing.md). Set, the grain image and its sandbox image are
# each verified by digest before anything runs them, and the grain image
# is recorded pinned to the digest that verified; a refusal stops here.
# It is also copied to /etc/grain/cosign.pub (install_image_verify_key)
# for the daemon's own -upgrade-verify-cmd, so the UI's upgrades hold
# what they pull to the same key.
# Empty, they are pulled and run unverified, and this script says so --
# images published before signing began carry no signature to check.
GRAIN_IMAGE_VERIFY_KEY="${GRAIN_IMAGE_VERIFY_KEY:-}"
GRAIN_IMAGE_PULL_TOKEN="${GRAIN_IMAGE_PULL_TOKEN:-}"
# Extra arguments appended to grain-daemon.service's own `docker run`,
# word-split as written -- the escape hatch for whatever docker_run_args
# below does not anticipate. An agy install that needs more of its tree
# than the directory around the binary, for instance:
#   GRAIN_EXTRA_DOCKER_ARGS="--volume /opt/gemini:/opt/gemini:ro"
GRAIN_EXTRA_DOCKER_ARGS="${GRAIN_EXTRA_DOCKER_ARGS:-}"

# Empty here means "not given": resolve_ui_addr (below) settles it before
# anything is written -- the installed unit's own -ui-addr on a re-run, so
# an existing deployment never moves without being asked to, and
# 0.0.0.0:4242 on a fresh install. GRAIN_UI_ALLOWED_HOSTS likewise keeps
# the installed unit's, and on an all-interfaces bind defaults to this
# machine's own names.
GRAIN_UI_ADDR="${GRAIN_UI_ADDR:-}"
GRAIN_UI_ALLOWED_HOSTS="${GRAIN_UI_ALLOWED_HOSTS:-}"
# The daemon's -policy: the local policy file every task must fit before
# it gets a sandbox (docs/local-policy.md), as an absolute path. Unset, a
# re-run keeps the installed unit's own -policy (resolve_policy), so
# re-running this script never turns the owner's repository and
# capability limits off; "none" removes it.
GRAIN_POLICY="${GRAIN_POLICY:-}"
# The account made a system admin (ensure_admin_user, grain/task-3688):
# the person installing this, by default -- $SUDO_USER, the account that
# ran `sudo ./setup.sh`. Set it to name somebody else, or to the empty
# string for nobody but the account the daemon seeds as admin itself
# ($GRAIN_USER). A re-run never removes an admin.
if [ "${GRAIN_ADMIN_USER+set}" != set ]; then
  GRAIN_ADMIN_USER=""
  if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER:-}" != root ]; then
    GRAIN_ADMIN_USER="$SUDO_USER"
  elif [ "$(id -un 2>/dev/null || echo root)" != root ]; then
    GRAIN_ADMIN_USER="$(id -un)"
  fi
fi
# GRAIN_MAX_CONCURRENT is GRAIN_MAX_WORKERS' former name, from before a
# deployment's concurrency was split into workers and mergers
# (model.Limits): still honoured so a re-run of this script on a host
# whose environment still sets it keeps the same worker count.
GRAIN_MAX_WORKERS="${GRAIN_MAX_WORKERS:-${GRAIN_MAX_CONCURRENT:-10}}"
GRAIN_MAX_MERGERS="${GRAIN_MAX_MERGERS:-1}"
# The CI job cache's bounds (grain/task-362). Empty by default and then
# not passed at all, so grain's own constants apply -- deliberately not
# repeated here as numbers, because a copy in this script is a copy that
# drifts from the package the day somebody changes it. The one worth
# setting is GRAIN_CI_CACHE_MAX_BYTES, on a host whose GRAIN_DATA_DIR
# disk is small enough for 256MB of cached job output to matter.
#
# Three of the four are seeds now rather than standing flags
# (grain/task-317): the age, the size and the entry count are stored on
# the config row at a first start and edited in Settings from then on,
# where they take effect without a restart. Setting one here on a host
# that already has a database changes nothing and says so in the journal.
GRAIN_CI_CACHE_MAX_AGE="${GRAIN_CI_CACHE_MAX_AGE:-}"
GRAIN_CI_CACHE_MAX_BYTES="${GRAIN_CI_CACHE_MAX_BYTES:-}"
GRAIN_CI_CACHE_MAX_ENTRIES="${GRAIN_CI_CACHE_MAX_ENTRIES:-}"
GRAIN_CI_CACHE_MAX_ENTRY_BYTES="${GRAIN_CI_CACHE_MAX_ENTRY_BYTES:-}"

# The first port of the 256-port block the daemon binds its
# sandbox-facing services in (the daemon's -sandbox-port-base). Empty by
# default and then not passed, so grain's own default applies. Worth
# setting on a host where something else already holds that block --
# which a grain sandbox itself is: its guest agent forwards the outer
# deployment's git proxy, package cache and model proxies onto those
# same loopback ports, so a deployment installed inside one (this
# script's own e2e suite, tests/installer) has to take another block or
# start with nothing dispatching.
GRAIN_SANDBOX_PORT_BASE="${GRAIN_SANDBOX_PORT_BASE:-}"
GRAIN_POLL_INTERVAL="${GRAIN_POLL_INTERVAL:-10s}"

GRAIN_GITHUB_HOST="${GRAIN_GITHUB_HOST:-github.com}"
GRAIN_GITHUB_INSECURE_HTTP="${GRAIN_GITHUB_INSECURE_HTTP:-0}"
GRAIN_GITHUB_TOKEN="${GRAIN_GITHUB_TOKEN:-}"
GRAIN_GITHUB_CREDENTIAL_NAME="${GRAIN_GITHUB_CREDENTIAL_NAME:-bot}"
GRAIN_GITHUB_APP_ID="${GRAIN_GITHUB_APP_ID:-}"
GRAIN_GITHUB_APP_INSTALLATION_ID="${GRAIN_GITHUB_APP_INSTALLATION_ID:-}"
GRAIN_GITHUB_APP_PRIVATE_KEY="${GRAIN_GITHUB_APP_PRIVATE_KEY:-}"

GRAIN_GEMINI_API_KEY="${GRAIN_GEMINI_API_KEY:-}"
GRAIN_GEMINI_MODEL="${GRAIN_GEMINI_MODEL:-}"
# The other half of that model selection: agy is given a model and a
# reasoning effort (low|medium|high), and refuses a bare family name
# without one. Empty leaves the daemon's own default
# (antigravity.DefaultEffort), and it is ignored for a GRAIN_GEMINI_MODEL
# whose name already carries an effort -- agy's own gemini-3.1-pro-high
# spelling, which it refuses alongside an --effort that disagrees.
GRAIN_GEMINI_EFFORT="${GRAIN_GEMINI_EFFORT:-}"
# Path to an Antigravity CLI (agy) on *this host* to run instead of the
# one baked into the image -- GRAIN_CLAUDE_PATH's exact counterpart for
# the other agent framework, and empty for the same reason: the image
# carries one (Dockerfile), so an ordinary deployment resolves "agy"
# inside the container with nothing to install and nothing per-host to
# keep in step with the build.
#
# Set, the daemon is pointed at it (-agy-path) -- and this host's copy is
# bind-mounted in at that same path, since a path passed as -agy-path has
# to resolve where the daemon is. Beside grain hostd it is instead the
# path hostd's allowlist runs (resolve_hostd).
GRAIN_AGY_PATH="${GRAIN_AGY_PATH:-}"
GRAIN_MAX_AGENT_TURNS="${GRAIN_MAX_AGENT_TURNS:-}"

# The Claude Code OAuth token agent/claude authenticates as, for a
# deployment whose agent-framework setting is (or may be set to)
# "claude" -- the exact counterpart of GRAIN_GEMINI_API_KEY above, seeded
# the same seed-once way into the same secrets directory. Both are
# optional now: whichever is missing can be pasted into the UI instead
# (Settings -> Models), which is the only way to set one on a
# host an operator cannot get a shell on.
GRAIN_CLAUDE_CODE_OAUTH_TOKEN="${GRAIN_CLAUDE_CODE_OAUTH_TOKEN:-}"
# Path to a claude CLI on *this host* to run instead of the one baked
# into the image. Empty -- the default, and what an ordinary deployment
# wants -- resolves "claude" inside the container, where Dockerfile
# already installed it: nothing to download at deploy time and nothing
# per-host to keep in step with the build.
#
# Set, the daemon is pointed at it (-claude-path). That is the escape
# hatch for a deployment that must pin a particular CLI version. In
# container mode it is also a *mount* rather than a $PATH entry, for the
# reason a mount is the only thing that works there: a path that resolves
# out here has to resolve identically in there.
GRAIN_CLAUDE_PATH="${GRAIN_CLAUDE_PATH:-}"
# Override the daemon's default Claude model -- the exact counterpart of
# GRAIN_GEMINI_MODEL above.
GRAIN_CLAUDE_MODEL="${GRAIN_CLAUDE_MODEL:-}"

# The OpenAI API key agent/codex authenticates as, for a deployment whose
# agent-framework setting is (or may be set to) "codex" -- the third of
# the same seed-once credentials, into the same secrets directory, and
# optional for the same reason: it can be pasted into the UI instead
# (Settings -> Models).
GRAIN_OPENAI_API_KEY="${GRAIN_OPENAI_API_KEY:-}"
# A host Codex login to share with Grain without sharing ~/.codex's
# config, MCP registrations, skills or rollout history. This is mounted
# read-write because Codex rotates OAuth refresh tokens in auth.json.
# An API key stored through the UI or seeded above takes precedence.
GRAIN_CODEX_AUTH_FILE="${GRAIN_CODEX_AUTH_FILE:-}"
# Path to a codex CLI on *this host* to run instead of the one resolved by
# default -- GRAIN_CLAUDE_PATH/GRAIN_AGY_PATH's counterpart for the third
# agent framework, passed as -codex-path (and, in container mode, mounted
# in) the same way.
GRAIN_CODEX_PATH="${GRAIN_CODEX_PATH:-}"
# Override the daemon's default Codex model -- the exact counterpart of
# GRAIN_GEMINI_MODEL/GRAIN_CLAUDE_MODEL above.
GRAIN_CODEX_MODEL="${GRAIN_CODEX_MODEL:-}"
# Where each agent CLI runs (docs/design/cli-run-location.md): "container"
# (a subprocess in the daemon's own container, from its image) or "host"
# (on this machine as $GRAIN_USER, through grain hostd, for a CLI its
# allowlist names). Passed as -claude-run, -codex-run and -agy-run; empty
# leaves the daemon's default. Settings -> Models overrides all three.
# With GRAIN_HOSTD=0 a "host" is dropped (resolve_hostd). "auto" is no
# longer a location (v2.3.0): it is accepted with a warning for one
# release and dropped, which is the default it meant in a container.
# GRAIN_CLAUDE_RUN may also be "detached": the image's claude, detached
# through grain hostd's container-equivalent entry so an upgrade does not
# interrupt it -- the default when hostd is on (resolve_hostd).
GRAIN_CLAUDE_RUN="${GRAIN_CLAUDE_RUN:-}"
GRAIN_CODEX_RUN="${GRAIN_CODEX_RUN:-}"
GRAIN_AGY_RUN="${GRAIN_AGY_RUN:-}"
for cli_run_var in GRAIN_CLAUDE_RUN GRAIN_CODEX_RUN GRAIN_AGY_RUN; do
  case "${!cli_run_var}" in
    ''|host|container) ;;
    detached)
      if [ "$cli_run_var" != GRAIN_CLAUDE_RUN ]; then
        echo "setup.sh: $cli_run_var=detached: only claude has a detached run location yet; want host or container" >&2
        exit 2
      fi
      ;;
    auto)
      echo "setup.sh: WARNING: $cli_run_var=auto is no longer a run location; leaving it to the default (container)." >&2
      echo "setup.sh:          Set host or container, or leave $cli_run_var unset. auto stops being accepted in a later release." >&2
      printf -v "$cli_run_var" '%s' ""
      ;;
    *)
      echo "setup.sh: $cli_run_var=${!cli_run_var}: want host or container (or detached, for claude)" >&2
      exit 2
      ;;
  esac
done
unset cli_run_var

# The GCP minter credential and what it mints for (seed_gcp_minter_key,
# mint_gemini_operating_key, and the -gcp-project/-gcp-agent-service-account
# the daemon is given). Defaulted here rather than left unset: this script
# runs under `set -u`, where reading an unset variable is not an empty
# string but an abort -- which is what a deployment with no GCP
# configuration at all got when these three lost their defaults while the
# block above them was removed (#1395), on every install, in the middle of
# seeding its secrets.
GRAIN_GCP_PROJECT="${GRAIN_GCP_PROJECT:-}"
GRAIN_GCP_SERVICE_ACCOUNT_EMAIL="${GRAIN_GCP_SERVICE_ACCOUNT_EMAIL:-}"
GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE="${GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE:-}"
# The IAP web resource whose IAP access the daemon reconciles from its
# teams and access list (-iap-resource), e.g.
# projects/<NUMBER>/iap_web/compute/services/<BACKEND_SERVICE_ID>.
# "none" turns that off.
GRAIN_IAP_RESOURCE="${GRAIN_IAP_RESOURCE:-}"
GRAIN_IAP_AUDIENCE="${GRAIN_IAP_AUDIENCE:-}"

# --- the config repository (pkg/staterepo) ------------------------------
#
# Every deployment owns grain/config under its data directory. It is
# controller-local and has no external mirror; the daemon exports its
# database there and applies native config pull requests from it.

# Where this deployment's *encrypted secrets file* is backed up
# (pkg/secretsrepo). The git proxy refuses this repository to sandboxes.
#
# Left empty, the ciphertext stays on this host's disk alone -- which is
# what grain did before this existed, and is one disk failure away from
# taking every credential with it. Set, a host rebuilt onto a fresh data
# directory gets its secrets back from the repository and its key from
# GRAIN_SECRETS_KEY below, which is the whole of a restore.
GRAIN_SECRETS_REPO_URL="${GRAIN_SECRETS_REPO_URL:-}"
GRAIN_SECRETS_REPO_BRANCH="${GRAIN_SECRETS_REPO_BRANCH:-main}"

# The private half of this deployment's secrets key (pkg/secrets): the
# one file that cannot be rebuilt from anything.
#
# This is neither reissued nor recoverable from another credential, and
# nothing can stand in for it: the encrypted secrets file
# beside it (grain/task-186 moved it out of the state repository, since
# everything a sandbox can clone is everything a sandbox can read) is
# encrypted to this key alone. A host restored onto a fresh data
# directory therefore needs both halves handed back -- the secrets file
# from a backup, and this key from here -- and one that mints itself a
# fresh key instead cannot read a line of what was put beside it, which
# pkg/secrets reports as the unrecoverable state it is rather than
# starting over silently.
#
# Seeded once, on the same never-overwrite contract as every credential
# below (seed_secret): a key already on this host always wins, since it
# is the key the secrets file on this host was encrypted to.
GRAIN_SECRETS_KEY="${GRAIN_SECRETS_KEY:-}"

GRAIN_TARGET_REPO="${GRAIN_TARGET_REPO:-}"
GRAIN_TARGET_BRANCH="${GRAIN_TARGET_BRANCH:-main}"
GRAIN_TARGET_REPOS="${GRAIN_TARGET_REPOS:-}"

# GRAIN_HOST_SANDBOXES=1 installs a deployment that dispatches every run
# into its own plain directory under $GRAIN_SANDBOX_DIR
# (orchestrator.HostSandboxes, the daemon's -host-sandboxes) instead of
# into a VM or a container. Off by default, and never a fallback.
#
# It is not a weaker sandbox, it is no sandbox: a dispatched agent runs as
# the daemon's own user, with its filesystem, its network and whatever
# credentials either can reach, and anything it changes outside its
# directory outlives the run. That is the point of it on a workstation one
# person owns -- the agent reaches the same compilers, CLIs and proxies
# its owner does -- and the wrong trade anywhere somebody else can file a
# task. So, like the daemon itself (pkg/daemon's CheckHostSandboxes), this
# script refuses it on a deployment others can reach -- a UI that listens
# beyond loopback, or one behind IAP -- unless
# GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES=1 says that everyone who can file
# a task there may run anything on this host
# (-allow-unisolated-host-sandboxes). It is checked below, once
# GRAIN_UI_ADDR is settled and before anything on this host is touched.
# The daemon's UI says so too, on a standing banner.
GRAIN_HOST_SANDBOXES="${GRAIN_HOST_SANDBOXES:-0}"
GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES="${GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES:-0}"
# Space-separated entries, each passed as one -host-sandbox-env: NAME or
# NAME=VALUE, an environment variable every host-sandbox run's commands
# get on top of the daemon's default allowlist -- an HTTPS_PROXY, an
# SSH_AUTH_SOCK, a CLOUDSDK_CONFIG a run should use as its owner does.
# NAME alone passes the daemon's own value, which under this script is
# the unit's environment (or, in container mode, the container's), not
# your login shell's: give the value. Only with GRAIN_HOST_SANDBOXES=1,
# and a VALUE may not hold a space or anything systemd reads in an
# ExecStart line ($, %, \, quotes), since it is written there as is.
GRAIN_HOST_SANDBOX_ENV="${GRAIN_HOST_SANDBOX_ENV:-}"
# See "Granule sandboxing" below (ensure_granule_images/
# ensure_granule_kvm_access) and terraform/gcp/README.md's
# own section of the same name. GRAIN_GRANULE_ENABLE=1 -- on by default
# wherever /dev/kvm exists -- needs no
# manual build-and-publish step first (bwsalmon/agents#531), and needs
# nothing configured for its sandbox container either
# (bwsalmon/agents#645). What it does need is nested virtualisation:
# /dev/kvm has to exist on this host (terraform/gcp's own
# enable_nested_virtualization, and a machine_type that supports it).
#
# A granule deployment runs one artifact and builds none. The sandbox
# image is always pulled: GRAIN_GRANULE_OCI_IMAGE overrides which one, and
# left empty (the default) it is whatever `grain sandbox-image` reports --
# the reference stamped into the grain image this host runs at the time it
# was built, so the two are always from one commit. The guest boots from
# inside that image; there is no separate disk to fetch or build.
GRAIN_CONTAINER_SANDBOXES="${GRAIN_CONTAINER_SANDBOXES:-$([ "$GRAIN_HOST_SANDBOXES" != "1" ] && [ ! -e /dev/kvm ] && echo 1 || echo 0)}"
GRAIN_GRANULE_ENABLE="${GRAIN_GRANULE_ENABLE:-$([ "$GRAIN_HOST_SANDBOXES" = "1" ] || [ "$GRAIN_CONTAINER_SANDBOXES" = "1" ] && echo 0 || echo 1)}"
# Whether to drop every packet a container or sandbox guest sends to the
# GCE metadata server (ensure_metadata_block, below). "auto", the default,
# blocks it on a GCE instance and leaves any other host alone; 1 blocks it
# regardless; 0 leaves it reachable -- for a host whose own unrelated
# containers read their credentials from it and that accepts that every
# grain sandbox on it can too.
GRAIN_BLOCK_METADATA="${GRAIN_BLOCK_METADATA:-auto}"
# Remembers what was actually asked for, since ensure_granule_images/
# ensure_granule_kvm_access overwrite GRAIN_GRANULE_ENABLE
# itself back to 0 on a failure partway through -- report_readiness uses
# this to tell "granule was never requested" apart from "granule was
# requested but a prerequisite wasn't ready this run".
GRAIN_GRANULE_REQUESTED="$GRAIN_GRANULE_ENABLE"
# Empty resolves to the sandbox container this build of grain was built
# against (ensure_granule_oci_image, and pkg/cli/sandboximage.go for the
# stamp itself). Set it to run a different one -- a mirror, a private
# copy, or a sandbox image pinned apart from grain's own.
GRAIN_GRANULE_OCI_IMAGE="${GRAIN_GRANULE_OCI_IMAGE:-}"
# Remembers whether this deployment *named* a sandbox image, before
# ensure_granule_oci_image fills the variable in with the stamped-in
# default -- the same "what was actually asked for" GRAIN_GRANULE_REQUESTED
# keeps above, and write_systemd_units is what reads it.
#
# The difference matters because the unit outlives this script. An
# upgrade (pkg/upgrade's image path) pulls the new grain image *and* the
# sandbox image that grain names, then restarts the service -- but the
# service's own -granule-image was resolved here, when setup.sh last ran,
# so a deployment that had upgraded went on booting every sandbox from
# the guest of the commit it was *installed* at while running grain from
# the commit it had upgraded to. Nothing said so: the pull succeeded, the
# restart succeeded, and the two halves quietly came from different
# commits -- which is exactly the drift ensure_granule_oci_image's own
# stamp exists to make impossible. Left unpinned, no image is named in the
# unit at all and the daemon resolves its own stamp at each start
# (pkg/cli/daemon.go's chooseSandboxImage), so a restart is all an
# upgrade needs to move both halves together. A deployment that pinned one
# still gets exactly what it named, upgrade or no upgrade -- which is why
# the pin is written as -sandbox-image: a unit written before that flag
# existed carries -granule-create-arg -granule-image whether its deployment
# pinned anything or not, and the daemon lets go of that one rather than
# letting a host's install date go on choosing its guest.
GRAIN_GRANULE_OCI_IMAGE_PINNED="${GRAIN_GRANULE_OCI_IMAGE:+1}"
# The guest account the daemon execs as, and the account granule's
# -guest-user tells granule to authorize this boot's generated key for.
# There is no key here to configure: granule generates one per VM boot and
# passes the public half on the kernel command line.
GRAIN_GRANULE_SSH_USER="${GRAIN_GRANULE_SSH_USER:-debian}"
GRAIN_GRANULE_WORKSPACE="${GRAIN_GRANULE_WORKSPACE:-/home/debian}"
# flat: the guest is spliced onto its sandbox container's own segment and
# takes over the address docker assigned it, so nothing here has to assign
# one. nat is granule's original mode, where each VM needs its own address
# on a shared private bridge and its own forwarded port -- which is all
# GRAIN_GRANULE_BASE_IP/GRAIN_GRANULE_BASE_PORT below exist to derive, and
# which flat mode ignores. Flat mode needs a guest image carrying granule's
# own guest overlays, which every granule image carries.
GRAIN_GRANULE_NET="${GRAIN_GRANULE_NET:-flat}"
GRAIN_GRANULE_BASE_IP="${GRAIN_GRANULE_BASE_IP:-169.254.100.10}"
GRAIN_GRANULE_BASE_PORT="${GRAIN_GRANULE_BASE_PORT:-12000}"
# The nameserver every sandbox guest resolves through, comma separated
# for a second one. Left empty, granule picks its own default -- a
# public resolver, which is the only answer that is right on an arbitrary
# host: the guest cannot reach this host's own resolver (routinely an
# address that exists only in this host's network namespace) or docker's
# embedded one on 127.0.0.11 (the *namespace's* loopback, not on the
# wire), so a guest pointed at either has open IP egress and hangs on
# every name it looks up.
#
# Set it on a deployment whose network has a resolver of its own, or one
# where reaching a public resolver is not allowed. It reaches the guest
# on its ip= kernel parameter, per boot, so a change here takes effect on
# the next sandbox rather than needing a new guest image.
GRAIN_GRANULE_DNS="${GRAIN_GRANULE_DNS:-}"
GRAIN_GRANULE_GIT_PROXY_HOST="${GRAIN_GRANULE_GIT_PROXY_HOST:-}"
GRAIN_SANDBOX_CPUS="${GRAIN_SANDBOX_CPUS:-}"
GRAIN_SANDBOX_MEMORY_MB="${GRAIN_SANDBOX_MEMORY_MB:-}"
GRAIN_SANDBOX_DISK_GB="${GRAIN_SANDBOX_DISK_GB:-}"

# On by default -- the common case is a single, directly-managed host
# with no rollout mechanism of its own, where the UI's Upgrade button
# (bwsalmon/agents#396) is the only way to ship a new build. Set to 0 by
# terraform/gcp/files/deploy.sh's own invocation of this script: that
# deployment shape already has a separate, Terraform-driven rollout
# (config-sync.sh watching the grain-deploy-generation instance-metadata
# attribute), and leaving both live at once let an operator's UI click
# race, or silently drift out of sync with, a `terraform apply`
# (bwsalmon/agents#405).
GRAIN_ENABLE_UI_UPGRADE="${GRAIN_ENABLE_UI_UPGRADE:-1}"

# Which branches the Upgrade button, a scheduled upgrade or an agent may
# move this deployment to: the daemon's own -upgrade-allowed-branches
# (pkg/cli/daemon.go). Also --upgrade-allowed-branches on the command
# line. Unset keeps whatever the installed grain-daemon.service already
# passes (resolve_upgrade_allowed_branches), so an allowlist an operator
# set once is not reset by the next re-run; with nothing installed
# either, the unit passes no flag and the build's own default stands.
GRAIN_UPGRADE_ALLOWED_BRANCHES="${GRAIN_UPGRADE_ALLOWED_BRANCHES:-}"

# On by default: a deployment exists to run unattended work for hours,
# and a host that suspends underneath it is not a slower deployment but a
# stopped one (disable_host_suspend, below). Set to 0 on a machine whose
# own power policy should win -- a re-run with it unset again puts the
# suspend policy back the way this script found it.
GRAIN_DISABLE_SUSPEND="${GRAIN_DISABLE_SUSPEND:-1}"

# --- Uninstall --------------------------------------------------------
#
# 1 (or --uninstall) removes this deployment from the host instead of
# installing it: its units, wrappers, /etc/grain, docker images and
# suspend policy, keeping $GRAIN_DATA_DIR and $GRAIN_USER
# (uninstall_grain, below, which says exactly what goes and what stays).
GRAIN_UNINSTALL="${GRAIN_UNINSTALL:-0}"
# 1 (or --purge) uninstalls and deletes $GRAIN_DATA_DIR -- the store,
# the secrets and their key -- and the $GRAIN_USER account as well.
GRAIN_UNINSTALL_PURGE="${GRAIN_UNINSTALL_PURGE:-0}"
# 1 (or --keep-images) leaves the grain and sandbox images in docker's
# image store, for a reinstall that should not pull them again.
GRAIN_UNINSTALL_KEEP_IMAGES="${GRAIN_UNINSTALL_KEEP_IMAGES:-0}"

# --- Tailscale (how anyone reaches the UI at all) -----------------------
#
# Off by default, because turning it on publishes this deployment's UI to
# a whole tailnet and the UI carries no auth of its own (see the note at
# the end of this file's header): whatever reaches -ui-addr acts as the
# deployment's one configured actor, so "who is on the tailnet" becomes
# "who can dispatch tasks here". A tailnet ACL restricting this node to
# the accounts that should have it is the answer, and it is a decision
# this script cannot make for an operator -- hence a flag rather than a
# default.
#
# On, this installs tailscale on the host (never in the daemon's
# container: the tailnet address belongs to the machine, and tailscaled
# has to outlive any one `docker run`), brings the node up, and points
# `tailscale serve` at the UI's port on loopback (127.0.0.1:<port>,
# which answers whether GRAIN_UI_ADDR binds loopback or 0.0.0.0).
# Nothing about the daemon changes -- the container keeps host
# networking, and this opens no port of its own.
GRAIN_TAILSCALE_ENABLE="${GRAIN_TAILSCALE_ENABLE:-0}"
# Remembers what was asked for, for the same reason
# GRAIN_GRANULE_REQUESTED does: every step below is allowed to give up and
# set GRAIN_TAILSCALE_ENABLE back to 0 rather than fail the deploy, and
# report_readiness has to tell "never asked for" apart from "asked for and
# not achieved this run".
GRAIN_TAILSCALE_REQUESTED="$GRAIN_TAILSCALE_ENABLE"
# A tailnet auth key (tailscale admin console -> Settings -> Keys), which
# is what lets `tailscale up` complete without a human following a URL in
# a browser -- the only shape that works on a host being deployed
# unattended. An ephemeral, pre-authorized, tagged key is the one to use
# for a machine a redeploy can replace.
#
# Not seeded into $GRAIN_DATA_DIR/secrets and not written to disk by this
# script at all: it authenticates this *host* to a tailnet, which is
# tailscaled's business rather than the daemon's, and tailscaled keeps
# its own state under /var/lib/tailscale once the node is up. A re-run of
# this script with no key at all therefore leaves a node that is already
# logged in exactly as it is.
GRAIN_TAILSCALE_AUTH_KEY="${GRAIN_TAILSCALE_AUTH_KEY:-}"
# The name this node takes on the tailnet, and so the hostname its URL is
# built from. Empty leaves tailscale's own default, this host's hostname.
GRAIN_TAILSCALE_HOSTNAME="${GRAIN_TAILSCALE_HOSTNAME:-}"
# The tailnet-side port the UI is served on. 443 means HTTPS with a
# tailnet certificate, which needs HTTPS enabled for the tailnet (admin
# console -> DNS -> HTTPS Certificates) and is what a deployment should
# prefer; the default 80 is plain HTTP over WireGuard, which needs
# nothing enabled and is already encrypted on the wire by the tailnet
# itself.
GRAIN_TAILSCALE_SERVE_PORT="${GRAIN_TAILSCALE_SERVE_PORT:-80}"
# Extra arguments appended to `tailscale up`, word-split as written --
# GRAIN_EXTRA_DOCKER_ARGS' counterpart for the tailnet side, and the way
# to pass whatever the four variables above do not name:
#   GRAIN_TAILSCALE_UP_ARGS="--advertise-tags=tag:grain --ssh"
GRAIN_TAILSCALE_UP_ARGS="${GRAIN_TAILSCALE_UP_ARGS:-}"

usage() {
  cat <<'EOF'
Usage: sudo ./setup.sh [options]

Options:
  -h, --help                 display this help and exit
  --db-driver DRIVER         database backend driver ("sqlite" or "postgres", default: sqlite, or set GRAIN_DB_DRIVER)
  --db-dsn DSN               database connection string / DSN (or set GRAIN_DB_DSN)
  --channel CHANNEL          which release channel to run: latest (default for a
                             new deployment), candidate or main (or set
                             GRAIN_CHANNEL; see below)
  --upgrade-allowed-branches PATTERNS
                             branches this deployment may upgrade to (or set
                             GRAIN_UPGRADE_ALLOWED_BRANCHES; see below)
  --uninstall                remove this deployment from the host instead of
                             installing it (or set GRAIN_UNINSTALL=1; see
                             "Uninstalling" below)
  --purge                    uninstall, and delete GRAIN_DATA_DIR (the store,
                             the secrets and secrets.key), GRAIN_USER, and
                             /var/lib/grain once nothing else is in it
                             (or set GRAIN_UNINSTALL_PURGE=1)
  --keep-images              with --uninstall/--purge: leave the grain and
                             sandbox images in docker (or set
                             GRAIN_UNINSTALL_KEEP_IMAGES=1)

Installs or updates a v2 grain deployment on this machine: pulls the
deployment image CI publishes on every commit, lays out /var/lib/grain
-- data/ for the state to back up (its embedded SQLite task store and
the secrets) and scratch/ for disposable run space and caches, each of
which may be a mount of its own -- and installs
grain-daemon.service, which runs that image -- the dispatch loop, the
UI/API it serves, and every binary either shells out to. Every setting
is an environment variable; all have defaults, so a bare
`sudo ./setup.sh` re-run is the update path. A re-run that changes the
store's schema (an upgrade or a rollback across a schema change) carries
the deployment's rows across, as the Upgrade button does; if they cannot
be carried it stops without moving anything, unless
GRAIN_ALLOW_STORE_RESET=1 says to start an empty store instead.

Anything marked "Seeded once" below only takes effect the first time this
deployment's store gets a config row (typically this script's very first
run); a later run passing a different value has no effect on an existing
deployment -- grain-daemon.service logs a line saying so on every start it
happens. Change an already-seeded value with `grain settings` (run as
GRAIN_USER) or the UI's Settings pane instead.

Uninstalling: `sudo ./setup.sh --uninstall` stops and removes every
grain unit (grain-daemon.service, the grain-reboot/-shutdown/-restart
watchers, grain-block-metadata.service and its iptables rule), the
grain-daemon container, /usr/local/bin/grain and granule,
/usr/local/lib/grain, /usr/local/share/grain, the files it wrote in
/etc/grain (and the directory, once empty), /etc/profile.d/grain.sh, grain's sudoers and modprobe drop-ins,
GRAIN_SANDBOX_DIR (scratch/) and the grain and sandbox docker images;
it unmasks the sleep targets and removes the logind drop-in. It keeps
GRAIN_DATA_DIR (data/) -- the store, the secrets and secrets.key, whose
path it prints -- and the GRAIN_USER account, so a later install picks
the same deployment up again. `--purge` deletes those two as well, and
then /var/lib/grain if nothing else is left in it. Pass the same
GRAIN_USER the install used (GRAIN_DATA_DIR is read from
/etc/grain/cli.env, GRAIN_SANDBOX_DIR from grain-daemon.service). The
packages an install added (docker.io, git, iptables, nftables,
tailscale) are listed, never removed.

Backups: back up GRAIN_DATA_DIR (/var/lib/grain/data); never back up
GRAIN_SANDBOX_DIR (/var/lib/grain/scratch).

Recognized variables:

  GRAIN_CHANNEL             release channel to run: latest (the last promoted
                             release, v2.X.0), candidate (the current release
                             candidate) or main (the untested head of main).
                             A new deployment defaults to latest; a re-run
                             with it unset keeps whatever the deployment
                             already runs (read from /etc/grain/image.env)
                             and says so. The Upgrade button, scheduled and
                             agent upgrades default to the same channel.
  GRAIN_REF                 any branch to deploy instead of a channel -- names
                             which published image runs, via GRAIN_IMAGE_TAG
                             below (unset by default; GRAIN_CHANNEL wins)
  GRAIN_DATA_DIR            secrets/store root -- state that must survive a redeploy,
                             and the one directory to back up
                             (default: /var/lib/grain/data)
  GRAIN_DB_DRIVER           database backend driver ("sqlite" or "postgres", default: sqlite)
  GRAIN_DB_DSN              database connection string / DSN (e.g. postgres://user:pass@127.0.0.1:5432/graindb or postgres://user:pass@/graindb?host=/cloudsql/INSTANCE)
  GRAIN_SANDBOX_DIR         root for disposable run space and caches -- state
                             that a redeploy is free to discard and a backup
                             leaves out; best on a disk of its own
                             (default: /var/lib/grain/scratch)
  GRAIN_USER                unprivileged account grain runs as (default: grain)
  GRAIN_ALLOW_STORE_RESET   1 to let a run that changes the store's schema (an
                             upgrade or rollback across a schema change) start
                             an EMPTY store when the rows cannot be carried
                             across -- every user, task and run gone; the old
                             store and state repository are moved aside to
                             GRAIN_DATA_DIR/store.schemaN-<time> and
                             state-repo.schemaN-<time>, not deleted. Default 0:
                             such a run migrates the rows the way the UI's
                             Upgrade button does, and stops with nothing moved
                             if it cannot

  GRAIN_INSTALL_MODE        only "container" (the default) is accepted. "host" is
                             refused: host mode is gone, and a re-run moves a
                             host-mode install to container mode plus grain hostd
  GRAIN_HOSTD               1 (default; "auto" means the same) or 0: grain hostd runs
                             allowlisted CLIs (agy) and `run: host` capability servers
                             on this machine, as GRAIN_USER. Writes grain-hostd.service
                             and, once, /etc/grain/hostd.yaml (the allowlist:
                             host-git, grain-gchat, grain-gcp and grain-host-script,
                             plus agy when GRAIN_AGY_PATH is set; later runs keep your
                             edits). 0 leaves it out -- and removes an installed one --
                             so nothing runs as GRAIN_USER on this machine and "host"
                             is not offered as a run location

  GRAIN_UI_ADDR             UI/API bind address (default: 0.0.0.0:4242 on a fresh
                             install -- every interface, at http://<host>:4242/;
                             a re-run keeps the installed unit's own). Set
                             127.0.0.1:4242 to keep it off the network and reach it
                             with `ssh -L 4242:localhost:4242 host`, or put it
                             behind Tailscale/IAP instead
  GRAIN_UI_ALLOWED_HOSTS    host names the UI answers to (-ui-allowed-hosts);
                             default: the installed unit's, else, on an
                             all-interfaces bind, this machine's own names
                             (hostname -f, hostname -s)
  GRAIN_POLICY              absolute path of the local policy file (-policy,
                             docs/local-policy.md) every task must fit before it
                             is dispatched, e.g. $GRAIN_DATA_DIR/grain_policy.yml
                             (a container-mode daemon sees only files under
                             $GRAIN_DATA_DIR). Unset, a re-run keeps the
                             installed unit's own; "none" removes it
  GRAIN_ADMIN_USER          the account to make a system admin (default: $SUDO_USER,
                             whoever ran sudo; empty for only the daemon's own
                             account). A re-run never removes an admin
  GRAIN_MAX_WORKERS         maximum number of ordinary tasks dispatched at once
                             (default: 10; GRAIN_MAX_CONCURRENT is its former name and
                             is still honoured). Seeded once, like every setting below
                             marked the same way -- see this file's own header comment
  GRAIN_MAX_MERGERS         agents on top of GRAIN_MAX_WORKERS that only the merge
                             queue may dispatch, to repair a pull request that will
                             not land (default: 1; 0 makes them wait for a worker slot
                             like anything else). Seeded once
  GRAIN_CI_CACHE_MAX_BYTES  ceiling on the CI job cache under GRAIN_DATA_DIR, in bytes
                             (default: grain's own, 256MB) -- lower it on a small data
                             disk, raise it where there is room and fewer jobs should
                             run twice. GRAIN_CI_CACHE_MAX_AGE, _MAX_ENTRIES and
                             _MAX_ENTRY_BYTES are the same knob's other three
                             dimensions. Nothing here is about correctness: a cache
                             entry is a promise that identical work was already done,
                             so a tighter bound only costs re-runs. Seeded once, bar
                             _MAX_ENTRY_BYTES: the other three are stored settings a
                             running daemon adopts, so move them in Settings after the
                             first start (the System pane shows what they are holding)
  GRAIN_SANDBOX_PORT_BASE   first port of the 256-port block the git proxy, model
                             proxies and caches bind in (default: grain's own, 9728;
                             a multiple of 256). Set it where something else holds
                             that block, such as inside a grain sandbox
  GRAIN_POLL_INTERVAL       daemon reconcile-cycle interval (default: 10s). Seeded once

  GRAIN_GITHUB_HOST         GitHub API host (default: github.com). Seeded once
  GRAIN_GITHUB_INSECURE_HTTP  1 to speak plain HTTP to it (mock servers only). Seeded once
  GRAIN_GITHUB_TOKEN        a token to seed the credential ladder with, once
                             (only written if no credential is configured yet)
  GRAIN_GITHUB_CREDENTIAL_NAME  name to store that token under (default: bot)
  GRAIN_GITHUB_APP_ID       a GitHub App's own ID, together with
  GRAIN_GITHUB_APP_INSTALLATION_ID  its installation ID on test_repos, and
  GRAIN_GITHUB_APP_PRIVATE_KEY  its downloaded PEM private key -- seed all
                             three, once, to store an App credential under
                             GRAIN_GITHUB_CREDENTIAL_NAME instead of a bare
                             token: pkg/gitproxy.CredentialSet mints and
                             refreshes an installation token from it, which
                             (unlike a fine-grained PAT) can read the Checks
                             API -- see terraform/gcp/README.md, "There is
                             no Checks permission to grant". Ignored if
                             GRAIN_GITHUB_CREDENTIAL_NAME already has a
                             credential of either kind on disk

  GRAIN_GEMINI_API_KEY      Gemini API key to seed, once. Not required:
                             grain-daemon.service starts either way, and a
                             deployment with no key set anywhere serves the
                             UI so one can be pasted in there (Settings ->
                             Agent frameworks) -- only a *dispatch* needs
                             one, and a run whose framework has none fails
                             as setup-failed saying so. A deployment with
                             GRAIN_GCP_PROJECT set and a minter credential
                             available (seeded from
                             GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE below) mints
                             its own here instead -- see
                             mint_gemini_operating_key
  GRAIN_CLAUDE_CODE_OAUTH_TOKEN  Claude Code OAuth token to seed, once --
                             GRAIN_GEMINI_API_KEY's counterpart for the
                             "claude" agent framework, and equally optional
                             for the same reasons. Which framework a run is
                             actually driven by is the model a task runs on,
                             so a deployment that might use either wants both
                             credentials. Each key seeded becomes a model
                             connection of its own (gemini, claude, codex),
                             through `grain connections import`
  GRAIN_OPENAI_API_KEY      OpenAI API key to seed, once -- the same
                             counterpart again, for the "codex" agent
                             framework, and optional for the same reasons
  GRAIN_CODEX_AUTH_FILE     path on THIS HOST to auth.json written by
                             codex login. Shared read-write with Codex
                             runs so token refreshes persist; used only
                             when no OpenAI API key is configured
  GRAIN_IMAGE               image repository the deployment runs, with no tag
                             (default ghcr.io/bwsalmon/grain/grain -- what CI
                             publishes on every commit)
  GRAIN_IMAGE_TAG           which tag of it to run; always wins when set.
                             Default: GRAIN_CHANNEL's tag, else the tag
                             published for GRAIN_REF (that branch with "/"
                             replaced by ".."), else the tag the deployment
                             already runs, else latest. Set to sha-<short sha>
                             to pin, or to roll back to, one exact build;
                             v2.X.0 pins a release. Moving tags: latest (the
                             last promoted release), candidate (the current
                             candidate), main (head of main)
  GRAIN_IMAGE_PULL_USER     credentials for `docker login` against that
  GRAIN_IMAGE_PULL_TOKEN     registry -- only needed for a private package;
                             bwsalmon/grain's is public and pulls anonymously
  GRAIN_IMAGE_VERIFY_KEY    a cosign public key (a file on this host) the grain
                             and sandbox images must be signed with; needs
                             cosign on PATH. Copied to /etc/grain/cosign.pub,
                             and the UI's upgrades verify against it too.
                             Unset, they run unverified (docs/image-signing.md)
  GRAIN_EXTRA_DOCKER_ARGS   extra arguments for grain-daemon.service's own
                             `docker run`, word-split as written (e.g. another
                             --volume the deployment needs)

  GRAIN_CLAUDE_PATH         path on THIS HOST to a claude CLI to run instead of
                             the one resolved by default. In container mode that
                             default is the copy the deployment image carries, and
                             what this names is bind-mounted in at the same path,
                             and the daemon is passed -claude-path (default: empty)
  GRAIN_AGY_PATH            path on THIS HOST to an Antigravity CLI (agy), for the
                             second agent framework. Read the same way in both
                             modes (default: empty)
  GRAIN_CODEX_PATH          path on THIS HOST to a codex CLI, for the third. Read
                             the same way in both modes (default: empty)
  GRAIN_CLAUDE_RUN          where claude runs: container (a subprocess in the
  GRAIN_CODEX_RUN            daemon's container) or host (on this machine through
  GRAIN_AGY_RUN              grain hostd, for a CLI its allowlist names; dropped
                             with GRAIN_HOSTD=0). claude also takes detached: the
                             image's claude through hostd's claude-detached entry,
                             which an upgrade does not interrupt. Passed as
                             -claude-run/-codex-run/-agy-run; Settings -> Models
                             overrides them (default: detached for claude with
                             grain hostd, otherwise the daemon's own default)
                             These three are deployment defaults. A connection
                             can name its own CLI (Team settings -> Models &
                             connections -> CLI path), which wins: the order is
                             the connection's path, then these, then PATH. A
                             connection's path is not bind-mounted -- in
                             container mode it must already exist inside the
                             daemon's container
  GRAIN_GEMINI_MODEL        the deployment's default model, seeded once through
  GRAIN_CLAUDE_MODEL        `grain connections import` (the first of these three
  GRAIN_CODEX_MODEL         that is set). Empty leaves the provider's own default
  GRAIN_MAX_AGENT_TURNS     cap on model/tool round trips per run. Empty leaves
                             the framework's own default (20), which a real task
                             can exhaust: reading a few files, writing one, running
                             a test and then add/commit/push are each a turn, and
                             the run fails outright rather than finishing short.
                             Seeded once

  GRAIN_GCP_PROJECT                  enables the gcp-key/gemini-key capabilities.
                                      Seeded once
  GRAIN_GCP_SERVICE_ACCOUNT_EMAIL    the narrow agent service account they mint
                                      for. Seeded once
  GRAIN_IAP_RESOURCE                 the IAP web resource
                                      (projects/N/iap_web/compute/services/S)
                                      whose IAP access grain reconciles; "none"
                                      turns that off. Seeded once
  GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE a minter key to seed under the gcp-key-minter
                                      secret. Written on *every* run, unlike the
                                      seeded-once values above: the deploy that
                                      supplies this key also rotates it in GCP, so a
                                      host that kept its first copy ends up
                                      authenticating with a key GCP has deleted

  GRAIN_IAP_AUDIENCE        the IAP backend audience the UI checks
                             X-Goog-IAP-JWT-Assertion against, identifying people
                             IAP let through by it (-ui-iap-audience)

  GRAIN_SECRETS_REPO_URL    git URL of the repository this deployment's encrypted
                             secrets file is backed up to (pkg/secretsrepo),
                             written to <data-dir>/secrets-repo.json. The git
                             proxy refuses it to sandboxes. Empty (the default)
                             keeps the ciphertext on this host alone. The private
                             key is never pushed to that repository
  GRAIN_SECRETS_REPO_BRANCH branch that file lives on there (default: main)
  GRAIN_SECRETS_KEY         this deployment's secrets private key
                             (`grain state key path` names the file it is
                             written to). Seeded once, like the credentials
                             above -- and the one value here a redeploy really
                             must carry: nothing else can stand in for it, and a
                             host restored onto a fresh data directory that mints
                             itself a key cannot read the secrets file put back
                             beside it. Back the file up when this script reports
                             it (see "Readiness" at the end of a run)

  GRAIN_TARGET_REPO         owner/name: the UI's default target for a task with
                             no repo of its own, and the repo this script pushes
                             one empty commit to if it has no commits yet
  GRAIN_TARGET_BRANCH       branch to create there if formatting it (default: main)
  GRAIN_TARGET_REPOS        comma-separated owner/name list a task's repo may
                             name (default: empty, meaning unrestricted) -- the
                             daemon's own -target-repos, the allowlist a task
                             naming anything else is parked with a comment
                             rather than dispatched against. Seeded once

  GRAIN_ENABLE_UI_UPGRADE   1 (default) to wire up the UI's own Upgrade
                             button (bwsalmon/agents#396); set to 0 on a
                             deployment shape that already has its own
                             rollout mechanism (e.g. terraform/gcp's
                             config-sync.sh/deploy.sh), so the two cannot
                             race or drift out of sync with each other
  GRAIN_UPGRADE_ALLOWED_BRANCHES
                             comma-separated branches an upgrade -- the
                             Upgrade button, a scheduled one, or an agent's
                             -- may install, each a path.Match pattern ('*'
                             never crosses a '/'; e.g. main,release/*), or
                             ** for any branch at all. Written into
                             grain-daemon.service as -upgrade-allowed-branches.
                             Unset keeps the value the installed unit already
                             passes (a re-run never resets it); unset on a
                             fresh install leaves the daemon's own default
                             (main, candidate, latest and vX.Y.Z tags). An
                             upgrade builds and runs whatever the branch
                             holds, so anyone who can push a matching branch
                             can put code into this daemon: ** trusts every
                             branch of the repo, and is an explicit opt-in

  GRAIN_DISABLE_SUSPEND     1 (default) to stop this host suspending,
                             hibernating or idling itself to sleep while a
                             task runs: masks the four sleep targets and
                             tells logind to ignore the lid and the sleep
                             keys. Set to 0 to leave this machine's own
                             power policy alone -- a re-run with 0 also
                             undoes what an earlier run masked

  GRAIN_TAILSCALE_ENABLE     1 to install tailscale on this host, join it to a
                             tailnet, and serve the UI on its tailnet address
                             (default: 0). The UI stays bound to
                             GRAIN_UI_ADDR; tailscaled proxies to its port on
                             loopback, so this opens no port of its own. Left
                             off (with a logged reason) if anything it needs is
                             missing, rather than failing the whole run.
                             NOTE: the UI carries no auth of its own, so
                             everyone who can reach this node on the tailnet
                             can act as this deployment. Restrict it with a
                             tailnet ACL.
  GRAIN_TAILSCALE_AUTH_KEY   tailnet auth key `tailscale up` logs in with, so a
                             deploy needs no browser (admin console -> Settings
                             -> Keys; an ephemeral, pre-authorized, tagged key
                             suits a host a redeploy replaces). Not needed on a
                             host already logged in -- this never re-logs one
                             in, and never writes the key anywhere itself
  GRAIN_TAILSCALE_HOSTNAME   name this node takes on the tailnet, and so the
                             hostname in its URL (default: tailscale's own
                             default, this host's hostname)
  GRAIN_TAILSCALE_SERVE_PORT tailnet-side port the UI is served on (default:
                             80, plain HTTP inside the tailnet's own
                             encryption). 443 serves HTTPS with a tailnet
                             certificate instead, which needs HTTPS enabled for
                             the tailnet (admin console -> DNS)
  GRAIN_TAILSCALE_UP_ARGS    extra arguments for `tailscale up`, word-split as
                             written -- e.g. "--advertise-tags=tag:grain --ssh"

  GRAIN_HOST_SANDBOXES       1 to dispatch each run into its own plain directory
                             under GRAIN_SANDBOX_DIR (-host-sandboxes), with NO
                             isolation: every agent runs as the daemon's user,
                             with this host's (or, in container mode, the
                             daemon container's) filesystem, network and
                             credentials. For a single-user workstation: refused
                             unless GRAIN_UI_ADDR is loopback (127.0.0.1:4242)
                             and GRAIN_IAP_RESOURCE is unset. Default 0; turns
                             GRAIN_GRANULE_ENABLE and GRAIN_CONTAINER_SANDBOXES
                             off by default, and cannot be combined with either
  GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES
                             1 to install GRAIN_HOST_SANDBOXES=1 on a deployment
                             others can reach anyway
                             (-allow-unisolated-host-sandboxes): everyone who can
                             file a task there may run anything on this host as
                             the daemon's user. Default 0
  GRAIN_HOST_SANDBOX_ENV     with GRAIN_HOST_SANDBOXES=1 only: space-separated
                             NAME or NAME=VALUE entries, each one
                             -host-sandbox-env -- a variable every run's commands
                             get on top of the default allowlist (e.g.
                             "HTTPS_PROXY=http://proxy:3128 SSH_AUTH_SOCK=/run/user/1000/ssh-agent").
                             Every run then acts with that identity. NAME alone
                             passes the daemon's own value, which is the unit's,
                             not your shell's
  GRAIN_CONTAINER_SANDBOXES  1 to dispatch each run into its own local Docker
                             container. This is the default when /dev/kvm is absent.
  GRAIN_GRANULE_ENABLE        1 to dispatch onto real bwsalmon/granule-managed
                             VMs over SSH (orchestrator.GranuleSandboxes) --
                             one VM per run, thrown away with it. On by
                             default where /dev/kvm exists. Pulls
                             the sandbox image this build of grain expects and
                             builds nothing (ensure_granule_images, below).
                             Needs /dev/kvm on this host (nested
                             virtualization); a host without it, or without
                             any other prerequisite below, falls back to
                             GRAIN_CONTAINER_SANDBOXES.
  GRAIN_BLOCK_METADATA       auto (the default), 1 or 0: whether containers and
                             sandbox guests are blocked from the GCE metadata
                             server (169.254.169.254). auto blocks it on a GCE
                             instance only; 0 leaves it reachable from every
                             sandbox, and with it this instance's service
                             account token and every secret in its metadata
  GRAIN_GRANULE_OCI_IMAGE     the sandbox image each task's VM runs -- both the
                             container and the guest inside it.
                             Empty (the default) is the one stamped into this
                             grain build at build time -- `grain sandbox-image`
                             -- so grain and its sandbox always come from one
                             commit and nothing has to be configured. Set it to
                             run a different one; it is pulled either way, and
                             never built here. A value set here is written into
                             the unit as the daemon's -sandbox-image, which is
                             how grain knows a pinned sandbox was chosen rather
                             than left behind by an older install of this
                             script (pkg/cli/daemon.go's chooseSandboxImage).
  GRAIN_GRANULE_SSH_USER      username to SSH into each granule VM as, and the
                             account granule's -guest-user has granule
                             authorize this boot's generated key for
                             (default: debian). There is no key to
                             configure: granule generates one per VM boot.
  GRAIN_GRANULE_WORKSPACE     working directory tools operate in on each granule
                             VM (default: /home/debian, GRAIN_GRANULE_SSH_USER's own home)
  GRAIN_GRANULE_NET           granule networking mode: "flat" (default) or "nat".
                             Flat needs a guest carrying granule's own guest
                             overlays, which every granule image has; see
                             scripts/granule/README.md.
  GRAIN_GRANULE_BASE_IP       "-ip" slot 1's granule VM gets; every later slot's
                             (nat mode only -- ignored under flat)
                             is the next address after it (default: 169.254.100.10)
  GRAIN_GRANULE_BASE_PORT     "-port" slot 1's granule VM forwards; every later
                             slot's is this plus its own number minus one
                             (default: 12000)
  GRAIN_GRANULE_DNS           nameserver each sandbox guest resolves through,
                             comma separated for a second one. Empty (the
                             default) leaves granule's own default, a
                             public resolver -- neither this host's own
                             resolver nor docker's embedded one is
                             reachable from inside a guest. Set it on a
                             network with its own resolver, or one where a
                             public resolver is not reachable; it applies
                             per boot, so no new guest image is needed.
  GRAIN_GRANULE_GIT_PROXY_HOST  host (no port) a granule VM reaches this
                             daemon's own git proxy through, in place of the
                             loopback address it otherwise binds to -- a
                             granule VM's guest has its own unrelated
                             127.0.0.1, with no route to this host's
                             (bwsalmon/agents#567). Defaults to docker's own
                             "bridge" network gateway address, detected via
                             `docker network inspect bridge` or, failing
                             that, the default route a container on that
                             network has; set
                             explicitly if this host's granule VM containers
                             join a different docker network.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --db-driver=*)
      GRAIN_DB_DRIVER="${1#*=}"
      shift
      ;;
    --db-driver)
      if [ $# -lt 2 ]; then
        echo "setup.sh: --db-driver requires an argument" >&2
        exit 2
      fi
      GRAIN_DB_DRIVER="$2"
      shift 2
      ;;
    --db-dsn=*)
      GRAIN_DB_DSN="${1#*=}"
      shift
      ;;
    --db-dsn)
      if [ $# -lt 2 ]; then
        echo "setup.sh: --db-dsn requires an argument" >&2
        exit 2
      fi
      GRAIN_DB_DSN="$2"
      shift 2
      ;;
    --channel=*)
      GRAIN_CHANNEL="${1#*=}"
      shift
      ;;
    --channel)
      if [ $# -lt 2 ]; then
        echo "setup.sh: --channel requires an argument" >&2
        exit 2
      fi
      GRAIN_CHANNEL="$2"
      shift 2
      ;;
    --upgrade-allowed-branches=*)
      GRAIN_UPGRADE_ALLOWED_BRANCHES="${1#*=}"
      shift
      ;;
    --upgrade-allowed-branches)
      if [ $# -lt 2 ]; then
        echo "setup.sh: --upgrade-allowed-branches requires an argument" >&2
        exit 2
      fi
      GRAIN_UPGRADE_ALLOWED_BRANCHES="$2"
      shift 2
      ;;
    --uninstall) GRAIN_UNINSTALL=1; shift ;;
    --purge) GRAIN_UNINSTALL=1; GRAIN_UNINSTALL_PURGE=1; shift ;;
    --keep-images) GRAIN_UNINSTALL_KEEP_IMAGES=1; shift ;;
    *)
      echo "setup.sh: unrecognized argument: $1 (see --help)" >&2
      exit 2
      ;;
  esac
done

log() { echo "==> $*"; }

# --- progress reporting ------------------------------------------------------
#
# Shared between setup.sh and edition wrapper scripts (which extract this
# block with sed).
#
# Every step that can take more than ~2s prints a step line before it starts
# (step_start) and its elapsed time when it finishes (step_done). Polling
# loops (wait_with_progress) show live progress: on a TTY, a spinner and
# [Ns] counter on one updating line; without a TTY (logs, CI), a line every
# 10s. Where available, wait_with_progress includes what grain-daemon reports
# it is doing from its last journal line or systemd substate, without dumping
# logs, and on timeout says what timed out and the exact journalctl command
# to inspect.

PROGRESS_STEP_START=""

# progress_is_tty reports whether stdout is a terminal, with PROGRESS_TTY=1|0
# overriding for tests.
progress_is_tty() {
  case "${PROGRESS_TTY:-}" in
    1) return 0 ;;
    0) return 1 ;;
  esac
  [ -t 1 ]
}

step_start() {
  PROGRESS_STEP_START="$SECONDS"
  log "$*"
}

step_done() {
  local label="${1:-done}" elapsed=0
  if [ -n "${PROGRESS_STEP_START:-}" ]; then
    elapsed=$(( SECONDS - PROGRESS_STEP_START ))
  fi
  PROGRESS_STEP_START=""
  log "  ... $label in ${elapsed}s"
}

# daemon_progress_status returns a short one-line summary of what
# grain-daemon.service is doing right now, from its latest journal line
# (slog's msg=... field when present) or its systemd state, never a log dump.
daemon_progress_status() {
  local line="" msg="" state=""
  if command -v journalctl >/dev/null 2>&1; then
    line="$(journalctl -u grain-daemon.service -n 1 --no-pager -o cat 2>/dev/null | tr -d '\r' | tail -n 1 || true)"
    if [[ "$line" =~ msg=\"([^\"]+)\" ]]; then
      msg="${BASH_REMATCH[1]}"
    elif [[ "$line" =~ msg=([^[:space:]]+) ]]; then
      msg="${BASH_REMATCH[1]}"
    else
      msg="$line"
    fi
  fi
  if [ -z "$msg" ] && command -v systemctl >/dev/null 2>&1; then
    state="$(systemctl show -p ActiveState,SubState --value grain-daemon.service 2>/dev/null | tr '\n' '/' || true)"
    state="${state%/}"
    [ -n "$state" ] && [ "$state" != "/" ] && msg="systemd: $state"
  fi
  msg="${msg#"${msg%%[![:space:]]*}"}"
  msg="${msg%"${msg##*[![:space:]]}"}"
  if [ "${#msg}" -gt 72 ]; then
    msg="${msg:0:69}..."
  fi
  printf '%s' "$msg"
}

# wait_with_progress STEP_DESC TIMEOUT_SECS DONE_WORD CHECK_FN [ARGS...]
# runs CHECK_FN until it succeeds or TIMEOUT_SECS iterations elapse.
wait_with_progress() {
  local desc="$1" timeout_secs="$2" done_word="${3:-ready}"
  shift 3
  local start="$SECONDS" i=0 elapsed=0 status_note="" detail=""
  local interval="${PROGRESS_INTERVAL:-1}"
  local report_every="${PROGRESS_REPORT_EVERY:-10}"
  local frames="|/-\\" frame=""
  log "$desc"
  if "$@" >/dev/null 2>&1; then
    elapsed=$(( SECONDS - start ))
    log "  ... $done_word in ${elapsed}s"
    return 0
  fi
  while [ "$i" -lt "$timeout_secs" ]; do
    i=$(( i + 1 ))
    if [ "$interval" != "0" ]; then
      sleep "$interval"
    fi
    if "$@" >/dev/null 2>&1; then
      if progress_is_tty; then
        printf '\r\033[K'
      fi
      elapsed=$(( SECONDS - start ))
      if [ "$interval" = "0" ] || [ "$elapsed" -le 0 ]; then
        elapsed="$i"
      fi
      log "  ... $done_word in ${elapsed}s"
      return 0
    fi
    elapsed=$(( SECONDS - start ))
    if [ "$interval" = "0" ] || [ "$elapsed" -le 0 ]; then
      elapsed="$i"
    fi
    status_note="$(daemon_progress_status)"
    detail=""
    [ -n "$status_note" ] && detail=" ($status_note)"
    if progress_is_tty; then
      frame="${frames:$(( i % 4 )):1}"
      printf '\r==>   [%ds] %s waiting...%s\033[K' "$elapsed" "$frame" "$detail"
    elif [ "$report_every" -gt 0 ] && [ $(( i % report_every )) -eq 0 ]; then
      log "  [${elapsed}s] still waiting...${detail}"
    fi
  done
  if progress_is_tty; then
    printf '\r\033[K'
  fi
  elapsed=$(( SECONDS - start ))
  if [ "$interval" = "0" ] || [ "$elapsed" -le 0 ]; then
    elapsed="$timeout_secs"
  fi
  status_note="$(daemon_progress_status)"
  detail=""
  [ -n "$status_note" ] && detail=" (last status: $status_note)"
  log "  timed out after ${elapsed}s: ${desc}${detail}"
  log "  inspect the daemon logs with: journalctl -u grain-daemon -n 50"
  return 1
}

# --- end of progress reporting -----------------------------------------------

# --- the layout under /var/lib/grain (data/ and scratch/) ------------------

# installed_unit_flag prints the value UNIT's ExecStart passes for -FLAG,
# read as installed_upgrade_allowed_branches reads its own flag: `-flag
# value` or `-flag=value`, one or two dashes, the last one winning,
# continued lines followed and quotes dropped. Nothing when there is no
# such unit or it passes none.
installed_unit_flag() {
  local unit="$1" flag="$2"
  if [ ! -r "$unit" ]; then
    return 0
  fi
  awk -v flag="$flag" '
    /^[[:space:]]*ExecStart=/ { exec = 1 }
    exec {
      for (i = 1; i <= NF; i++) {
        t = $i
        sub(/^ExecStart=/, "", t)
        if (t == "-" flag || t == "--" flag) {
          if (i < NF) { v = $(i + 1); found = 1 }
          i++
        } else if (index(t, "-" flag "=") == 1 || index(t, "--" flag "=") == 1) {
          sub(/^--?[^=]*=/, "", t)
          v = t; found = 1
        }
      }
      if ($0 !~ /\\[[:space:]]*$/) exec = 0
    }
    END {
      if (found) {
        gsub(/^["\047]|["\047]$/, "", v)
        print v
      }
    }' "$unit"
}

# GRAIN_DAEMON_UNIT is the installed daemon unit the layout is read back
# from: its -sandbox-dir is the one record of where an install put
# GRAIN_SANDBOX_DIR.
GRAIN_DAEMON_UNIT="${GRAIN_DAEMON_UNIT:-/etc/systemd/system/grain-daemon.service}"

# resolve_recorded_layout keeps the directories an earlier install of
# this deployment used, for whichever of GRAIN_DATA_DIR and
# GRAIN_SANDBOX_DIR this run was not told: the data directory from
# /etc/grain/cli.env (write_cli_env records it in both install modes), the
# sandbox directory from the installed unit's -sandbox-dir. A 1.x install
# recorded /var/lib/grain itself; that is kept, never moved, and one line
# says what the 2.0 default is instead. Either variable set explicitly
# keeps exactly what it says.
resolve_recorded_layout() {
  local env_file="${CLI_ENV_FILE:-/etc/grain/cli.env}" recorded
  if [ "$GRAIN_DATA_DIR_GIVEN" != "1" ] && [ -r "$env_file" ]; then
    recorded="$(sed -n 's/^GRAIN_DATA_DIR=//p' "$env_file" | tail -n1)"
    if [ -n "$recorded" ]; then
      GRAIN_DATA_DIR="$recorded"
      if [ "$recorded" != "$GRAIN_DEFAULT_DATA_DIR" ]; then
        log "Data directory: keeping $recorded, recorded in $env_file (2.0's default layout is $GRAIN_DEFAULT_DATA_DIR and $GRAIN_DEFAULT_SANDBOX_DIR; nothing is moved)"
      fi
    fi
  fi
  if [ "$GRAIN_SANDBOX_DIR_GIVEN" != "1" ]; then
    recorded="$(installed_unit_flag "$GRAIN_DAEMON_UNIT" sandbox-dir)"
    if [ -n "$recorded" ]; then
      GRAIN_SANDBOX_DIR="$recorded"
    fi
  fi
}

# refuse_flat_layout stops a fresh install from laying data/ out beside a
# 1.x data directory left at the root itself -- its store, or the
# local-api-token the daemon writes there -- rather than mixing the two
# layouts under one directory. Moving that data is the operator's call,
# so this says how and touches nothing. Only the default data directory
# is checked: a run that named another one, or a re-run that kept the
# one recorded above, is not laying data/ out at all.
refuse_flat_layout() {
  [ "$GRAIN_DATA_DIR" = "$GRAIN_DEFAULT_DATA_DIR" ] || return 0
  local entry found=""
  for entry in store store.db local-api-token secrets/secrets.key; do
    if [ -e "$GRAIN_ROOT_DIR/$entry" ]; then
      found="$GRAIN_ROOT_DIR/$entry"
      break
    fi
  done
  [ -n "$found" ] || return 0
  cat >&2 <<FLAT
setup.sh: $GRAIN_ROOT_DIR holds a grain 1.x data directory ($found).

grain 2.0 keeps its data in $GRAIN_DEFAULT_DATA_DIR and its disposable run space
in $GRAIN_DEFAULT_SANDBOX_DIR, with nothing else at $GRAIN_ROOT_DIR, and this script
does not mix the two layouts or move data on its own. Either:

  * move the old data into the new place, then re-run this script --
        sudo systemctl stop grain-daemon
        sudo install -d -m 0750 -o grain -g grain $GRAIN_DEFAULT_DATA_DIR
        (cd $GRAIN_ROOT_DIR && sudo mv <everything except data/ and scratch/> data/)
  * or install somewhere else, deliberately, with GRAIN_DATA_DIR.
FLAT
  return 1
}

# setup_layout_root makes $GRAIN_ROOT_DIR root-owned 0755 when it is the
# parent of the data or the sandbox directory, as it is by default: it
# holds those two and nothing else, and grain writes into neither it nor
# a 1.x data directory recorded at its path -- which is why a parent of
# anything else is left as it is.
setup_layout_root() {
  if [ "${GRAIN_DATA_DIR%/*}" = "$GRAIN_ROOT_DIR" ] || [ "${GRAIN_SANDBOX_DIR%/*}" = "$GRAIN_ROOT_DIR" ]; then
    install -d -m0755 -o root -g root "$GRAIN_ROOT_DIR"
  fi
}

# same_filesystem is whether paths $1 and $2 are on one filesystem (one
# st_dev), the way pkg/sysstat groups disks: a separate mount nested
# under another directory is a filesystem of its own.
same_filesystem() {
  local a b
  a="$(stat -c %d -- "$1" 2>/dev/null)" || return 1
  b="$(stat -c %d -- "$2" 2>/dev/null)" || return 1
  [ "$a" = "$b" ]
}

# uninstall_dir_contents empties directory $1 and then removes it, unless
# it is a mount point: an rm -rf of a mounted disk's root fails half-way,
# so a mount keeps its (now empty) mount point for the host's fstab.
uninstall_dir_contents() {
  local dir="$1"
  if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then
    return 0
  fi
  if [ -d "$dir" ] && [ ! -L "$dir" ] && mountpoint -q -- "$dir" 2>/dev/null; then
    find "$dir" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
    log "  emptied $dir (a mount point; the mount is left in place)"
  else
    uninstall_path "$dir"
  fi
}

# uninstall_layout removes what an uninstall removes of the layout:
# GRAIN_SANDBOX_DIR in both modes, being disposable, and GRAIN_DATA_DIR
# with --purge ($1 = 1), followed by $GRAIN_ROOT_DIR itself once nothing
# is left in it. The root is never removed recursively, and the sandbox
# directory is not removed without --purge when the data directory is
# inside it: that would take the data with it.
uninstall_layout() {
  local purge="$1"
  if uninstall_safe_dir "$GRAIN_SANDBOX_DIR"; then
    case "$GRAIN_DATA_DIR/" in
      "${GRAIN_SANDBOX_DIR%/}"/*)
        if [ "$purge" != "1" ]; then
          log "  kept $GRAIN_SANDBOX_DIR: $GRAIN_DATA_DIR is inside it"
        else
          uninstall_dir_contents "$GRAIN_SANDBOX_DIR"
        fi
        ;;
      *) uninstall_dir_contents "$GRAIN_SANDBOX_DIR" ;;
    esac
  fi
  if [ "$purge" = "1" ]; then
    uninstall_dir_contents "$GRAIN_DATA_DIR"
    if [ -d "$GRAIN_ROOT_DIR" ] && rmdir -- "$GRAIN_ROOT_DIR" 2>/dev/null; then
      log "  removed $GRAIN_ROOT_DIR"
    elif [ -d "$GRAIN_ROOT_DIR" ]; then
      log "  kept $GRAIN_ROOT_DIR: it is not empty ($(cd "$GRAIN_ROOT_DIR" && echo *))"
    fi
  fi
}

if [ "$(id -u)" -ne 0 ]; then
  echo "setup.sh: must run as root (it creates a system user, systemd units, and /usr/local/bin/grain) -- try sudo" >&2
  exit 1
fi

# --- Uninstall (GRAIN_UNINSTALL=1 / --uninstall, --purge) ----------------
#
# Everything an install put on this host, taken back off it -- decided
# here, straight after the root check and before any of the install's
# own preflight below, because none of that preflight is about removing
# anything and some of it installs packages (ensure_docker) or refuses
# a configuration an uninstall has no use for.
#
# The paths below are the ones the install steps further down write, and
# each names the step that writes it; a new file an install step starts
# writing on this host belongs in this list too, or an uninstall leaves
# it behind (tests/installer's teardown runs this, and checks).
#
# What it deliberately keeps, without --purge:
#   * $GRAIN_DATA_DIR -- the task store, the secrets database and
#     secrets/secrets.key, the one file nothing else can recreate. An
#     uninstall followed by a fresh install on the same directory is
#     the same deployment again, so this is the default; the closing
#     report says where the key is, so it can be copied off first
#   * $GRAIN_USER and its group, which own every file in it
# --purge removes both as well. Neither mode removes the packages an
# install may have added (docker.io/docker-cli, git, iptables, nftables,
# sudo, tailscale and its apt source) -- other software on this host may
# well use them, and this script cannot tell -- so the report lists them,
# with the command to remove each, instead.

# The units an install of any version of this script wrote -- including
# grain-shell.* (removed in grain/task-193) and grain-ui.service (before
# bwsalmon/agents#363), which an old host may still carry.
UNINSTALL_UNITS=(
  grain-daemon.service
  grain-hostd.service
  grain-hostd-upgrade.path grain-hostd-upgrade.service
  grain-hostd-allowlist.service
  grain-reboot.path grain-reboot.service
  grain-shutdown.path grain-shutdown.service
  grain-restart.path grain-restart.service
  grain-shell.path grain-shell.service
  grain-ui.service
  grain-block-metadata.service
)

# uninstall_path removes one path, if it is there, and says so.
uninstall_path() {
  local path
  for path in "$@"; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      rm -rf -- "$path"
      log "  removed $path"
    fi
  done
}

# uninstall_safe_dir refuses to rm -rf a directory a typo could point at
# the rest of the host: it has to be absolute, and not / or a top-level
# directory of its own.
uninstall_safe_dir() {
  case "$1" in
    /*/*) return 0 ;;
    *) return 1 ;;
  esac
}

# image_id prints the local image id $1 names, or nothing.
image_id() {
  docker image inspect --format '{{.Id}}' "$1" 2>/dev/null || true
}

# sandbox_image_refs prints the sandbox images grain image $1 (a ref or
# an image id) names, one per line: the questions ensure_granule_oci_image
# and ensure_container_sandbox_image asked when they pulled them.
sandbox_image_refs() {
  local sub
  for sub in sandbox-image container-sandbox-image k8s-sandbox-image; do
    docker run --rm --network none "$1" "$sub" 2>/dev/null | tr -d '\r' | head -n1 || true
  done
}

# uninstall_images prints every image this deployment pulled: each tag of
# its grain image and each untagged one, the sandbox images each of those
# names, and the untagged images of those sandbox repositories
# (`grain sandbox-image`, `container-sandbox-image`, `k8s-sandbox-image`,
# the same questions ensure_granule_oci_image and
# ensure_container_sandbox_image asked when they pulled them) -- asked of
# every tag rather than the current one, since an upgraded host carries
# the previous release's pair as well. A GRAIN_GRANULE_OCI_IMAGE the unit
# was pinned to is named in it as -sandbox-image.
uninstall_images() {
  local repos=("$GRAIN_IMAGE") ref repo
  # An install run with a GRAIN_IMAGE other than this run's says which in
  # the root-owned ref file (write_image_ref).
  if [ -r /etc/grain/image.env ]; then
    ref="$(sed -n 's/^GRAIN_IMAGE=//p' /etc/grain/image.env | tail -n1)"
    repo="${ref%:*}"
    if [ -n "$repo" ] && [ "$repo" != "$GRAIN_IMAGE" ]; then
      repos+=("$repo")
    fi
  fi
  # And the configured OSS/legacy daemon image repositories: a deployment
  # that moved repositories on an upgrade still has the images it ran
  # before the move.
  for repo in "${GRAIN_OSS_IMAGE_REPO:-}" "${GRAIN_LEGACY_IMAGE_REPO:-}"; do
    if [ -n "$repo" ] && [[ " ${repos[*]} " != *" $repo "* ]]; then
      repos+=("$repo")
    fi
  done
  # Untagged ones too, by id: a re-run or an upgrade that moved a tag
  # such as `main` left the image it replaced behind as <none>, and that
  # image is still the only one that can say which sandbox images it
  # pulled (task 1058, N3).
  local grain_refs=() tag id
  for repo in "${repos[@]}"; do
    while read -r tag id; do
      case "$tag" in
        "") continue ;;
        "<none>") grain_refs+=("$id") ;;
        *) grain_refs+=("$repo:$tag") ;;
      esac
    done < <(docker images "$repo" --format '{{.Tag}} {{.ID}}' 2>/dev/null)
  done
  local sandbox_refs=()
  for ref in "${grain_refs[@]}"; do
    echo "$ref"
    mapfile -t -O "${#sandbox_refs[@]}" sandbox_refs < <(sandbox_image_refs "$ref")
  done
  mapfile -t -O "${#sandbox_refs[@]}" sandbox_refs < <(
    sed -n 's/.*-sandbox-image[= ]\([^ ]*\).*/\1/p' /etc/systemd/system/grain-daemon.service 2>/dev/null || true)
  # And every untagged image of the sandbox repositories those named:
  # the previous release's sandbox image, pulled by digest or untagged
  # when its tag moved, which the grain image that named it may no longer
  # be here to say.
  local sandbox_repos=()
  for ref in "${sandbox_refs[@]}"; do
    [ -n "$ref" ] || continue
    echo "$ref"
    repo="${ref%%@*}"
    case "${repo##*/}" in *:*) repo="${repo%:*}" ;; esac
    if [[ " ${sandbox_repos[*]} " != *" $repo "* ]]; then
      sandbox_repos+=("$repo")
    fi
  done
  for repo in "${sandbox_repos[@]}"; do
    docker images "$repo" --format '{{.Tag}} {{.ID}}' 2>/dev/null | awk '$1 == "<none>" {print $2}'
  done
}

uninstall_grain() {
  local purge="$GRAIN_UNINSTALL_PURGE"
  # The directories the install used, when this run was not told them:
  # read before the units and /etc/grain/cli.env that record them go.
  resolve_recorded_layout
  if [ "$purge" = "1" ]; then
    local dir
    for dir in "$GRAIN_DATA_DIR" "$GRAIN_SANDBOX_DIR"; do
      if ! uninstall_safe_dir "$dir"; then
        echo "setup.sh: refusing to purge $dir -- GRAIN_DATA_DIR and GRAIN_SANDBOX_DIR must be absolute paths below a top-level directory" >&2
        exit 2
      fi
    done
    log "Uninstalling grain from this host, data and account included (--purge)"
  else
    log "Uninstalling grain from this host (keeping $GRAIN_DATA_DIR and the $GRAIN_USER account; --purge removes both)"
  fi

  # Images first, while the grain images are still here to say which
  # sandbox images they pulled.
  local images=()
  if [ "$GRAIN_UNINSTALL_KEEP_IMAGES" != "1" ] && command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    mapfile -t images < <(uninstall_images | awk 'NF && !seen[$0]++')
  fi

  log "Stopping and removing grain's systemd units"
  local unit
  for unit in "${UNINSTALL_UNITS[@]}"; do
    if [ -e "/etc/systemd/system/$unit" ] || systemctl cat "$unit" >/dev/null 2>&1; then
      systemctl disable --now "$unit" >/dev/null 2>&1 || true
      log "  stopped and disabled $unit"
    fi
    uninstall_path "/etc/systemd/system/$unit" "/etc/systemd/system/$unit.d"
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl reset-failed "${UNINSTALL_UNITS[@]}" >/dev/null 2>&1 || true

  # ensure_metadata_block's rule. Its unit has no ExecStop, so stopping
  # it leaves the rule in place until the next boot; with nothing of
  # grain's left to fence off, it goes now.
  if command -v iptables >/dev/null 2>&1; then
    while iptables -w -t raw -D PREROUTING -d 169.254.169.254/32 -j DROP 2>/dev/null; do :; done
  fi
  if command -v ip6tables >/dev/null 2>&1; then
    while ip6tables -w -t raw -D PREROUTING -d fd20:ce::254/128 -j DROP 2>/dev/null; do :; done
  fi

  if command -v docker >/dev/null 2>&1; then
    # Asked first: `docker rm -f` of a container that is not there
    # succeeds on a current docker, which would report a removal that
    # never happened.
    if docker container inspect grain-daemon >/dev/null 2>&1 &&
       docker rm -f grain-daemon >/dev/null 2>&1; then
      log "  removed the grain-daemon container"
    fi
  fi

  # disable_host_suspend's two halves, undone the way
  # GRAIN_DISABLE_SUSPEND=0 undoes them.
  log "Restoring this host's suspend policy (unmask sleep/suspend/hibernate targets)"
  systemctl unmask sleep.target suspend.target hibernate.target hybrid-sleep.target >/dev/null 2>&1 || true
  uninstall_path /etc/systemd/logind.conf.d/10-grain-no-suspend.conf
  rmdir /etc/systemd/logind.conf.d 2>/dev/null || true
  systemctl reload systemd-logind.service >/dev/null 2>&1 || true

  log "Removing grain's files outside its data directory"
  # write_host_extractor's kubectl link, only when it is still grain's.
  # (Its variables are set further down than this runs.)
  if [ -L /usr/local/bin/kubectl ] && [ "$(readlink /usr/local/bin/kubectl)" = /usr/local/lib/grain/hostd/current/kubectl ]; then
    uninstall_path /usr/local/bin/kubectl
  fi
  # install_cli_wrappers / write_host_extractor /
  # write_image_ref_promoter, write_cli_profile / write_cli_env /
  # write_image_ref / install_image_verify_key, ensure_metadata_block,
  # write_host_nft_sudoers and write_control_units' predecessors,
  # ensure_granule_nested_virt.
  uninstall_path \
    /usr/local/bin/grain /usr/local/bin/granule \
    /usr/local/lib/grain /usr/local/share/grain \
    /usr/local/sbin/grain-block-metadata \
    /etc/grain/image.env /etc/grain/cli.env /etc/grain/cosign.pub \
    /etc/grain/hostd.yaml /etc/grain/hostd.yaml.default /etc/grain/hostd.env /var/lib/grain-hostd \
    /etc/profile.d/grain.sh \
    /etc/sudoers.d/zzz-grain-nft /etc/sudoers.d/90-grain-nft \
    /etc/sudoers.d/grain-daemon-reboot /etc/sudoers.d/grain-daemon-upgrade \
    /etc/modprobe.d/kvm-nested-grain.conf
  # /etc/grain by name, file by file, and then only if it is empty: a
  # host can keep files of its own there (a grain sandbox image keeps
  # package-cache.env in it), and those are not this script's to delete.
  rm -f /etc/grain/image.env.* /etc/grain/cli.env.*
  if rmdir /etc/grain 2>/dev/null; then
    log "  removed /etc/grain"
  elif [ -d /etc/grain ]; then
    log "  kept /etc/grain: it holds files setup.sh did not write ($(cd /etc/grain && echo *))"
  fi
  # The granule VM directory ensure_granule_kvm_access made: caches, kept
  # by neither mode, like GRAIN_SANDBOX_DIR (uninstall_layout, below).
  # /var/lib/granule itself only if nothing else is in it.
  uninstall_path /var/lib/granule/vms
  rmdir /var/lib/granule 2>/dev/null || true

  if [ "${#images[@]}" -gt 0 ]; then
    log "Removing grain's docker images"
    local image
    for image in "${images[@]}"; do
      if ! docker image inspect "$image" >/dev/null 2>&1; then
        continue
      fi
      # A sandbox container a run left behind holds its image.
      docker ps -aq --filter "ancestor=$image" 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
      if docker rmi "$image" >/dev/null 2>&1; then
        log "  removed $image"
      else
        log "  could not remove $image (still in use?) -- docker rmi $image"
      fi
    done
  elif [ "$GRAIN_UNINSTALL_KEEP_IMAGES" = "1" ]; then
    log "Keeping grain's docker images (--keep-images)"
  fi

  if [ "$purge" = "1" ]; then
    log "Removing $GRAIN_SANDBOX_DIR, $GRAIN_DATA_DIR and the $GRAIN_USER account (--purge)"
  else
    log "Removing $GRAIN_SANDBOX_DIR (disposable; $GRAIN_DATA_DIR is kept)"
  fi
  uninstall_layout "$purge"
  if [ "$purge" = "1" ]; then
    if id -u "$GRAIN_USER" >/dev/null 2>&1; then
      local group
      group="$(id -gn "$GRAIN_USER")"
      if userdel "$GRAIN_USER" >/dev/null 2>&1; then
        log "  removed the $GRAIN_USER account"
      else
        log "  could not remove the $GRAIN_USER account -- userdel $GRAIN_USER"
      fi
      # useradd --system made a private group of the same name; a shared
      # GRAIN_GROUP an operator chose is theirs, and stays.
      if [ "$group" = "$GRAIN_USER" ] && getent group "$group" >/dev/null 2>&1; then
        groupdel "$group" >/dev/null 2>&1 || true
      fi
    fi
  fi

  echo
  echo "grain is uninstalled from this host."
  if [ "$purge" != "1" ]; then
    if [ -d "$GRAIN_DATA_DIR" ]; then
      echo "  Kept:     $GRAIN_DATA_DIR (the task store and secrets) and the $GRAIN_USER account."
      echo "            This is the directory to back up; $GRAIN_SANDBOX_DIR never needed to be."
      if [ -e "$GRAIN_DATA_DIR/secrets/secrets.key" ]; then
        echo "  Secrets key: $GRAIN_DATA_DIR/secrets/secrets.key -- copy it somewhere safe before"
        echo "            deleting anything; without it the secrets database cannot be read."
      fi
      echo "            A fresh install on the same GRAIN_DATA_DIR picks the deployment up again."
      echo "            To delete them too: sudo ./setup.sh --purge"
    else
      echo "  $GRAIN_DATA_DIR does not exist; the $GRAIN_USER account was kept (--purge removes it)."
    fi
  fi
  echo "  Not removed (other software may use them; remove by hand if nothing does):"
  echo "    packages an install may have added: apt-get purge docker.io docker-cli git iptables nftables tailscale"
  echo "    tailscale's apt source:             rm /etc/apt/sources.list.d/tailscale.list /usr/share/keyrings/tailscale-archive-keyring.gpg"
  echo "    a tailnet UI published by GRAIN_TAILSCALE_*: tailscale serve reset; tailscale logout"
  echo "    /etc/default/grain-daemon, if you wrote one"
}

if [ "$GRAIN_UNINSTALL" = "1" ] || [ "$GRAIN_UNINSTALL_PURGE" = "1" ]; then
  uninstall_grain
  exit 0
fi

# Where this install keeps its data and its scratch space: what an
# earlier install recorded, for whatever this run was not told, and never
# a data/ laid out beside a 1.x data directory at the root.
resolve_recorded_layout
if ! refuse_flat_layout; then
  exit 2
fi

# Which sandbox backend this install is for, decided before anything on
# this host is touched -- no package installed, no user created, no image
# pulled (grain/task-15).
#
# GRAIN_GRANULE_ENABLE=1 gives every run a granule-managed VM of its own;
# GRAIN_CONTAINER_SANDBOXES=1 gives it a local Docker container;
# GRAIN_HOST_SANDBOXES=1 gives it a plain directory on this machine and no
# isolation at all. The daemon takes exactly one backend flag, so asking
# for host sandboxes alongside either isolated one is refused rather than
# resolved by picking one; whether this deployment may have host sandboxes
# at all waits for GRAIN_UI_ADDR (check_host_sandboxes, below).
if [ "$GRAIN_HOST_SANDBOXES" = "1" ] && { [ "$GRAIN_GRANULE_ENABLE" = "1" ] || [ "$GRAIN_CONTAINER_SANDBOXES" = "1" ]; }; then
  cat >&2 <<EOF
setup.sh: GRAIN_HOST_SANDBOXES=1 cannot be combined with another sandbox backend.

GRAIN_GRANULE_ENABLE=$GRAIN_GRANULE_ENABLE and GRAIN_CONTAINER_SANDBOXES=$GRAIN_CONTAINER_SANDBOXES:
the daemon dispatches into one backend. Unset whichever you did not mean
-- GRAIN_HOST_SANDBOXES=1 turns both of the others off by default.
EOF
  exit 2
fi
if [ "$GRAIN_GRANULE_ENABLE" != "1" ] && [ "$GRAIN_CONTAINER_SANDBOXES" != "1" ] && [ "$GRAIN_HOST_SANDBOXES" != "1" ]; then
  cat >&2 <<'EOF'
setup.sh: refusing to install without a sandbox backend.

None of GRAIN_GRANULE_ENABLE, GRAIN_CONTAINER_SANDBOXES and
GRAIN_HOST_SANDBOXES is 1, which leaves nothing to dispatch a run into.

  * to sandbox each run in its own VM (the default where /dev/kvm exists),
    leave GRAIN_GRANULE_ENABLE unset
  * to sandbox each run in its own Docker container, re-run with
    GRAIN_CONTAINER_SANDBOXES=1
  * to run every agent unisolated, in a plain directory on this machine --
    a single-user workstation only -- re-run with GRAIN_HOST_SANDBOXES=1
EOF
  exit 2
fi

case "$GRAIN_BLOCK_METADATA" in
  auto|0|1) ;;
  *)
    echo "setup.sh: GRAIN_BLOCK_METADATA must be \"auto\", \"1\" or \"0\", not \"$GRAIN_BLOCK_METADATA\"" >&2
    exit 2
    ;;
esac

# --- the upgrade allowlist (GRAIN_UPGRADE_ALLOWED_BRANCHES) --------------
#
# -upgrade-allowed-branches is the one daemon flag an operator routinely
# edits into grain-daemon.service by hand (to allow `latest`, a release
# branch, or ** on a host that tracks feature branches), and this script
# rewrites that unit on every run. So the value is resolved here, before
# anything on this host is written: given, it is checked and used; not
# given, the value the installed unit already passes is carried over
# rather than dropped (2026-10-05, director task 1913).
#
# Checked here rather than left to the daemon because the daemon does not
# refuse a malformed pattern -- path.Match reports ErrBadPattern and the
# pattern simply never matches, so a typo is an allowlist that silently
# refuses the branch it was written for. tests/deploy's
# upgrade_allowlist_test.go runs these functions against path.Match
# itself so the two cannot drift apart.

# upgrade_branch_pattern_problem prints why one -upgrade-allowed-branches
# pattern is unusable, or nothing for one that is fine. The syntax is
# path.Match's (pkg/upgrade's Upgrader.Allowed), with no '\' escapes --
# no branch name needs one, and systemd would unescape it first -- and
# only the characters a branch name and an ExecStart line both take
# literally.
upgrade_branch_pattern_problem() {
  local p="$1" i=0 n c ranges
  local bad='[^][abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/*?^+@=-]'
  n=${#p}
  if [[ "$p" =~ $bad ]]; then
    echo "it has a character other than letters, digits and . _ / * ? [ ] ^ + @ = -"
    return 0
  fi
  # Only a [...] class can be malformed once the characters are these:
  # an optional '^', then one or more chars or lo-hi ranges, then ']'.
  while [ "$i" -lt "$n" ]; do
    c="${p:$i:1}"
    i=$((i + 1))
    if [ "$c" != "[" ]; then
      continue
    fi
    if [ "${p:$i:1}" = "^" ]; then
      i=$((i + 1))
    fi
    ranges=0
    while :; do
      if [ "$i" -ge "$n" ]; then
        echo "a '[' is never closed"
        return 0
      fi
      c="${p:$i:1}"
      if [ "$c" = "]" ] && [ "$ranges" -gt 0 ]; then
        i=$((i + 1))
        break
      fi
      if [ "$c" = "-" ] || [ "$c" = "]" ]; then
        echo "a '[...]' class has a '$c' where a character belongs (an empty class, or a range with no start)"
        return 0
      fi
      i=$((i + 1))
      if [ "$i" -lt "$n" ] && [ "${p:$i:1}" = "-" ]; then
        i=$((i + 1))
        c="${p:$i:1}"
        if [ "$i" -ge "$n" ] || [ "$c" = "-" ] || [ "$c" = "]" ]; then
          echo "a '[...]' class has a range with no end"
          return 0
        fi
        i=$((i + 1))
      fi
      ranges=$((ranges + 1))
    done
  done
}

# normalize_upgrade_allowed_branches prints VALUE the way the daemon
# reads it (pkg/cli/daemon.go's splitBranchPatterns: comma-separated,
# blanks around each pattern dropped, empty ones skipped), or says on
# stderr why the daemon could not use it and fails. SOURCE says where the
# value came from, for that message.
normalize_upgrade_allowed_branches() {
  local value="$1" origin="$2" pattern problem parts=() out=""
  IFS=, read -r -a parts <<<"$value"
  for pattern in "${parts[@]}"; do
    pattern="${pattern#"${pattern%%[![:space:]]*}"}"
    pattern="${pattern%"${pattern##*[![:space:]]}"}"
    if [ -z "$pattern" ]; then
      continue
    fi
    problem="$(upgrade_branch_pattern_problem "$pattern")"
    if [ -n "$problem" ]; then
      echo "setup.sh: upgrade allowlist pattern \"$pattern\" ($origin) is malformed: $problem." >&2
      echo "          -upgrade-allowed-branches takes comma-separated path.Match patterns (e.g. main,candidate,release/*), or ** for any branch" >&2
      return 1
    fi
    out="${out:+$out,}$pattern"
  done
  if [ -z "$out" ]; then
    echo "setup.sh: upgrade allowlist \"$value\" ($origin) names no branch pattern at all" >&2
    return 1
  fi
  printf '%s\n' "$out"
}

# installed_upgrade_allowed_branches prints the -upgrade-allowed-branches
# value UNIT's ExecStart passes, or nothing when there is no such unit or
# it passes none. Read the way the daemon's flag package would: `-flag
# value`, `-flag=value`, one or two dashes, the last one winning, and an
# ExecStart continued over '\'-ended lines. Quotes a hand edit put round
# the value are dropped, as systemd itself drops them.
installed_upgrade_allowed_branches() {
  local unit="$1"
  if [ ! -r "$unit" ]; then
    return 0
  fi
  awk '
    /^[[:space:]]*ExecStart=/ { exec = 1 }
    exec {
      for (i = 1; i <= NF; i++) {
        t = $i
        sub(/^ExecStart=/, "", t)
        if (t == "-upgrade-allowed-branches" || t == "--upgrade-allowed-branches") {
          if (i < NF) { v = $(i + 1); found = 1 }
          i++
        } else if (t ~ /^--?upgrade-allowed-branches=/) {
          sub(/^--?upgrade-allowed-branches=/, "", t)
          v = t; found = 1
        }
      }
      if ($0 !~ /\\[[:space:]]*$/) exec = 0
    }
    END {
      if (found) {
        gsub(/^["\047]|["\047]$/, "", v)
        print v
      }
    }' "$unit"
}

# resolve_upgrade_allowed_branches settles GRAIN_UPGRADE_ALLOWED_BRANCHES
# for the unit about to be written over UNIT: the value given, checked;
# else the installed unit's own, checked and kept; else empty, which
# write_systemd_units turns into no flag at all and so the build's own
# default. Says which, and fails on a value the daemon could not use.
resolve_upgrade_allowed_branches() {
  local unit="$1" installed
  if [ -n "$GRAIN_UPGRADE_ALLOWED_BRANCHES" ]; then
    GRAIN_UPGRADE_ALLOWED_BRANCHES="$(normalize_upgrade_allowed_branches "$GRAIN_UPGRADE_ALLOWED_BRANCHES" \
      "from GRAIN_UPGRADE_ALLOWED_BRANCHES / --upgrade-allowed-branches")" || return 1
    log "Upgrade allowlist: $GRAIN_UPGRADE_ALLOWED_BRANCHES (as given)"
  else
    installed="$(installed_upgrade_allowed_branches "$unit")"
    if [ -z "$installed" ]; then
      log "Upgrade allowlist: the daemon's own default (none given, and no installed unit sets one)"
      return 0
    fi
    if ! GRAIN_UPGRADE_ALLOWED_BRANCHES="$(normalize_upgrade_allowed_branches "$installed" "kept from $unit")"; then
      echo "          pass --upgrade-allowed-branches (or GRAIN_UPGRADE_ALLOWED_BRANCHES) to replace it" >&2
      return 1
    fi
    log "Upgrade allowlist: keeping $GRAIN_UPGRADE_ALLOWED_BRANCHES from the installed $unit"
    log "  (pass --upgrade-allowed-branches or GRAIN_UPGRADE_ALLOWED_BRANCHES to change it)"
  fi
  case ",$GRAIN_UPGRADE_ALLOWED_BRANCHES," in
    *,\*\*,*)
      log "WARNING: ** lets this deployment upgrade to any branch: anyone who can push a branch"
      log "         to the repo can have it built and run as this daemon"
      ;;
  esac
}

# --- end of the upgrade allowlist ------------------------------------------

if ! resolve_upgrade_allowed_branches /etc/systemd/system/grain-daemon.service; then
  exit 2
fi

# --- the UI address (GRAIN_UI_ADDR, GRAIN_UI_ALLOWED_HOSTS) ----------------

# The address a fresh install binds: every interface, on the port the
# grain CLI defaults to (cmd/grain/main.go's defaultServerURL).
DEFAULT_UI_ADDR="0.0.0.0:4242"

# ui_addr_is_wildcard is whether ADDR listens on every interface.
ui_addr_is_wildcard() {
  case "${1%:*}" in
    ''|0.0.0.0|'[::]'|::) return 0 ;;
  esac
  return 1
}

# ui_addr_is_loopback is whether ADDR listens on loopback alone -- the
# same answer as pkg/daemon's loopbackListenAddr: "localhost", or an
# address in 127.0.0.0/8 or ::1. A wildcard or empty host is not.
ui_addr_is_loopback() {
  local host="${1%:*}"
  host="${host#[}"
  host="${host%]}"
  case "$host" in
    localhost|::1|127.*) return 0 ;;
  esac
  return 1
}

# machine_hostnames prints this machine's own names, comma-separated:
# `hostname -f` and `hostname -s`, the two a browser elsewhere on the
# network puts in Host.
machine_hostnames() {
  local f s
  f="$(hostname -f 2>/dev/null || true)"
  s="$(hostname -s 2>/dev/null || true)"
  [ -n "$f" ] || f="$(hostname 2>/dev/null || true)"
  if [ -n "$s" ] && [ "$s" != "$f" ]; then
    printf '%s\n' "${f:+$f,}$s"
  else
    printf '%s\n' "$f"
  fi
}

# resolve_ui_addr settles GRAIN_UI_ADDR and GRAIN_UI_ALLOWED_HOSTS for the
# unit about to be written over UNIT: the value given; else the installed
# unit's own, kept; else DEFAULT_UI_ADDR. Allowed hosts likewise, and on
# an all-interfaces bind with none, this machine's own names -- without
# them the daemon checks the Host header of loopback requests alone.
# Says which.
resolve_ui_addr() {
  local unit="$1" installed
  if [ -n "$GRAIN_UI_ADDR" ]; then
    log "UI/API address: $GRAIN_UI_ADDR (as given)"
  else
    installed="$(installed_unit_flag "$unit" ui-addr)"
    if [ -n "$installed" ]; then
      GRAIN_UI_ADDR="$installed"
      log "UI/API address: keeping $GRAIN_UI_ADDR from the installed $unit"
      log "  (pass GRAIN_UI_ADDR=$DEFAULT_UI_ADDR to move it to the new default)"
    else
      GRAIN_UI_ADDR="$DEFAULT_UI_ADDR"
      log "UI/API address: $GRAIN_UI_ADDR (the default for a fresh install)"
    fi
  fi
  if [ -n "$GRAIN_UI_ALLOWED_HOSTS" ]; then
    log "UI/API allowed hosts: $GRAIN_UI_ALLOWED_HOSTS (as given)"
    return 0
  fi
  installed="$(installed_unit_flag "$unit" ui-allowed-hosts)"
  if [ -n "$installed" ]; then
    GRAIN_UI_ALLOWED_HOSTS="$installed"
    log "UI/API allowed hosts: keeping $GRAIN_UI_ALLOWED_HOSTS from the installed $unit"
  elif ui_addr_is_wildcard "$GRAIN_UI_ADDR"; then
    GRAIN_UI_ALLOWED_HOSTS="$(machine_hostnames)"
    if [ -n "$GRAIN_UI_ALLOWED_HOSTS" ]; then
      log "UI/API allowed hosts: $GRAIN_UI_ALLOWED_HOSTS -- this machine's own names, since $GRAIN_UI_ADDR"
      log "  listens on every interface (set GRAIN_UI_ALLOWED_HOSTS to name the others it is reached by)"
    fi
  fi
}

# ui_network_url is where a browser elsewhere reaches the UI: this
# machine's own name on an all-interfaces bind, the bind address itself
# otherwise.
ui_network_url() {
  local addr="$1" name
  if ui_addr_is_wildcard "$addr"; then
    name="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo '<this-host>')"
    printf 'http://%s:%s/\n' "$name" "${addr##*:}"
  else
    printf 'http://%s/\n' "$addr"
  fi
}

# ui_firewall_note says whether an active host firewall seems to stand
# between the network and PORT. Reported only: this script never opens
# one.
ui_firewall_note() {
  local port="$1" out
  if command -v ufw >/dev/null 2>&1 && out="$(ufw status 2>/dev/null)" && grep -q '^Status: active' <<<"$out"; then
    if grep -Eq "^${port}(/tcp)?[[:space:]]" <<<"$out"; then
      echo "ufw is active and has a rule for $port"
    else
      echo "ufw is active with no rule for $port, so the network URL is probably blocked (this script opens nothing: ufw allow $port/tcp would)"
    fi
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    if firewall-cmd --list-ports 2>/dev/null | grep -qw "$port/tcp"; then
      echo "firewalld is running and lists $port/tcp"
    else
      echo "firewalld is running without $port/tcp, so the network URL is probably blocked (this script opens nothing)"
    fi
  elif command -v iptables >/dev/null 2>&1 && out="$(iptables -S INPUT 2>/dev/null)" && grep -Eq '^-P INPUT (DROP|REJECT)' <<<"$out"; then
    if grep -Eq -- "--dport ${port}( |$).*-j ACCEPT" <<<"$out"; then
      echo "iptables drops INPUT by default but accepts $port"
    else
      echo "iptables drops INPUT by default with no rule accepting $port, so the network URL is probably blocked (this script opens nothing)"
    fi
  else
    echo "no active host firewall found -- a cloud or network firewall in front of this host may still block $port"
  fi
}

# --- end of the UI address ---------------------------------------------------

resolve_ui_addr /etc/systemd/system/grain-daemon.service

# --- the install mode (GRAIN_INSTALL_MODE) ----------------------------------
#
# Container mode is the only one left. Host mode -- the daemon as a plain
# systemd service on this machine -- was removed once grain hostd could
# run the few programs that need the bare machine (task 5041). What is
# left here is the migration and the refusal: a re-run over a host-mode
# unit writes a container-mode one under the same name, over the same
# data directory, and GRAIN_INSTALL_MODE=host stops before anything is
# written.
#
# Uses installed_unit_flag, from the UI address block above.

# installed_install_mode prints the mode UNIT runs grain in -- "host" or
# "container" -- or nothing when there is no such unit or it is neither.
# Environment=GRAIN_INSTALL_MODE=host says host outright; failing that,
# the ExecStart says it: a `docker run` is container mode, anything else
# runs the binary on this host (a host unit from before that Environment=
# line).
installed_install_mode() {
  local unit="$1"
  if [ ! -r "$unit" ]; then
    return 0
  fi
  awk '
    /^[[:space:]]*Environment=/ {
      line = $0
      sub(/^[[:space:]]*Environment=/, "", line)
      n = split(line, kv, /[[:space:]]+/)
      for (i = 1; i <= n; i++) {
        t = kv[i]
        gsub(/["\047]/, "", t)
        if (t == "GRAIN_INSTALL_MODE=host") env = "host"
        else if (t == "GRAIN_INSTALL_MODE=container") env = "container"
      }
    }
    /^[[:space:]]*ExecStart=/ {
      line = $0
      sub(/^[[:space:]]*ExecStart=[-@:+!]*/, "", line)
      split(line, w, /[[:space:]]+/)
      bin = w[1]
      sub(/.*\//, "", bin)
      exec = (bin == "docker" || bin == "podman") ? "container" : "host"
    }
    END {
      if (env != "") print env
      else if (exec != "") print exec
    }' "$unit"
}

# GRAIN_MIGRATED_FROM_HOST_MODE is 1 when this run is moving a host-mode
# install to container mode; the summary says so.
GRAIN_MIGRATED_FROM_HOST_MODE=0

# resolve_install_mode settles GRAIN_INSTALL_MODE for the unit about to be
# written over UNIT: always container. It refuses host -- here, while
# nothing on this host has been written yet -- and says so when the
# installed unit is a host-mode one this run migrates.
resolve_install_mode() {
  local unit="$1" installed
  installed="$(installed_install_mode "$unit")"
  case "$GRAIN_INSTALL_MODE" in
    ""|container) ;;
    host)
      cat >&2 <<'EOF2'
setup.sh: GRAIN_INSTALL_MODE=host is no longer supported: host mode was removed.
Every machine install runs the daemon in the release container, with grain
hostd on this machine for the CLIs that have to run as you (agy) and
`run: host` capability servers. Unset GRAIN_INSTALL_MODE and re-run:

  sudo ./scripts/setup.sh

A host-mode install is moved to container mode plus grain hostd, keeping its
data directory and grain-daemon.service. GRAIN_HOSTD=0 leaves hostd out.
EOF2
      return 1
      ;;
    *)
      echo "setup.sh: GRAIN_INSTALL_MODE must be \"container\" (or unset), not \"$GRAIN_INSTALL_MODE\"" >&2
      return 1
      ;;
  esac
  GRAIN_INSTALL_MODE=container
  if [ "$installed" = host ]; then
    GRAIN_MIGRATED_FROM_HOST_MODE=1
    log "Install mode: moving this host-mode install to container mode plus grain hostd,"
    log "  keeping its data directory and $unit (host mode was removed)."
    log "  The grain CLI now acts as \"operator\" rather than as ${GRAIN_USER:-grain}, and agent CLIs"
    log "  on this host's PATH are no longer used: the image's are, and agy runs through hostd."
  else
    log "Install mode: container"
  fi
}

# --- end of the install mode ------------------------------------------------

if ! resolve_install_mode /etc/systemd/system/grain-daemon.service; then
  exit 2
fi

# --- the local policy (GRAIN_POLICY) ----------------------------------------

# resolve_policy settles GRAIN_POLICY for the unit about to be written
# over UNIT: the file given; else the installed unit's own -policy, kept
# -- a re-run that dropped it would dispatch everything the owner's
# policy holds back, and say nothing (task 4497); else none. "none"
# removes an installed one, said as the change it is. Afterwards
# GRAIN_POLICY is a path or empty.
resolve_policy() {
  local unit="$1" installed
  installed="$(installed_unit_flag "$unit" policy)"
  case "$GRAIN_POLICY" in
    "")
      if [ -n "$installed" ]; then
        GRAIN_POLICY="$installed"
        log "Local policy: keeping $GRAIN_POLICY from the installed $unit"
        log "  (pass GRAIN_POLICY=none to stop enforcing it)"
      fi
      ;;
    none)
      GRAIN_POLICY=""
      if [ -n "$installed" ]; then
        log "Local policy: removing $installed (the installed $unit), as GRAIN_POLICY=none asks;"
        log "  the daemon will enforce no local policy"
      fi
      return 0
      ;;
    /*)
      if [ -n "$installed" ] && [ "$installed" != "$GRAIN_POLICY" ]; then
        log "Local policy: changing from $installed (the installed $unit) to $GRAIN_POLICY, as given"
      else
        log "Local policy: $GRAIN_POLICY (as given)"
      fi
      ;;
    *)
      echo "setup.sh: GRAIN_POLICY must be an absolute path or none, not \"$GRAIN_POLICY\"" >&2
      return 1
      ;;
  esac
  if [ -z "$GRAIN_POLICY" ]; then
    return 0
  fi
  # Reported, not fatal: the daemon refuses to start on a policy it
  # cannot read, which stops dispatch rather than letting it run unbounded.
  if [ ! -e "$GRAIN_POLICY" ]; then
    log "WARNING: the local policy $GRAIN_POLICY does not exist: the daemon will not start until it does"
  fi
  if [ "${GRAIN_INSTALL_MODE:-container}" = container ]; then
    case "$GRAIN_POLICY" in
      "${GRAIN_DATA_DIR%/}"/*) ;;
      *) log "WARNING: the local policy $GRAIN_POLICY is outside $GRAIN_DATA_DIR, the only host directory"
         log "         a container-mode daemon sees: move it under $GRAIN_DATA_DIR" ;;
    esac
  fi
  return 0
}

# --- end of the local policy ---------------------------------------------------

if ! resolve_policy /etc/systemd/system/grain-daemon.service; then
  exit 2
fi

# check_host_sandboxes refuses GRAIN_HOST_SANDBOXES=1 on a deployment
# others can reach, exactly where the daemon it is about to install would
# refuse to start (pkg/daemon's CheckHostSandboxes) -- so the refusal comes
# now, before anything on this host is touched, rather than as a unit that
# crash-loops. Of the settings that check names, this script can only
# pass two: -ui-addr and IAP (-iap-resource). Tailscale serving a loopback
# UI to a tailnet is not one of them, to the daemon or here; its own
# readiness warning covers it.
check_host_sandboxes() {
  local entry entries
  # read -a rather than an unquoted expansion: word-split, never globbed.
  read -ra entries <<< "$GRAIN_HOST_SANDBOX_ENV"
  for entry in "${entries[@]}"; do
    if [ "$GRAIN_HOST_SANDBOXES" != "1" ]; then
      echo "setup.sh: GRAIN_HOST_SANDBOX_ENV is only for GRAIN_HOST_SANDBOXES=1: every other backend runs its commands in a guest whose environment is its own" >&2
      exit 2
    fi
    # What the daemon refuses (mcp.ValidateSandboxEnv: a name that is
    # not one, or HOME) and what the unit cannot carry (see
    # GRAIN_HOST_SANDBOX_ENV's own comment). A name twice is the
    # daemon's to refuse.
    if ! grep -Eq '^[A-Za-z_][A-Za-z0-9_]*(=[^$%\\"'"'"']*)?$' <<<"$entry" || [ "${entry%%=*}" = HOME ]; then
      echo "setup.sh: GRAIN_HOST_SANDBOX_ENV entry $entry is not NAME or NAME=VALUE (no HOME, and no \$, %, \\ or quotes in VALUE)" >&2
      exit 2
    fi
  done
  [ "$GRAIN_HOST_SANDBOXES" = "1" ] || return 0
  [ "$GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES" = "1" ] && return 0
  local shared=()
  ui_addr_is_loopback "$GRAIN_UI_ADDR" || shared+=("GRAIN_UI_ADDR=$GRAIN_UI_ADDR")
  [ -n "$GRAIN_IAP_RESOURCE" ] && shared+=("GRAIN_IAP_RESOURCE")
  [ "${#shared[@]}" -eq 0 ] && return 0
  cat >&2 <<EOF
setup.sh: refusing GRAIN_HOST_SANDBOXES=1 on a deployment others can reach (${shared[*]}).

Host sandboxes run every task unisolated, as the daemon's own user on this
host. That is for a single-user workstation:

  * re-run with GRAIN_UI_ADDR=127.0.0.1:4242 (and without GRAIN_IAP_RESOURCE),
    and reach the UI with \`ssh -L 4242:localhost:4242 <this-host>\`
  * or unset GRAIN_HOST_SANDBOXES for an isolated backend (granule VMs, or
    GRAIN_CONTAINER_SANDBOXES=1)
  * or, if everyone who can file a task here may run anything on this host,
    re-run with GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES=1
EOF
  exit 2
}
check_host_sandboxes

# --- the image channel (GRAIN_CHANNEL) --------------------------------------
#
# Which tag of GRAIN_IMAGE this run installs (grain/task-3689). A plain
# install used to run the tag published for main -- the untested head of
# main, stamped with the nearest tag's version so it passed for a release
# candidate -- because GRAIN_REF defaulted to main. Now, first match wins:
#
#   1. GRAIN_IMAGE_TAG, as given: a channel, vX.Y.Z or sha-<short sha>;
#   2. GRAIN_CHANNEL (--channel): latest, candidate or main;
#   3. GRAIN_REF, as given: any branch, as the tag CI publishes for it;
#   4. the tag the deployment already runs, read from the unit's own
#      image ref file -- so a re-run never moves a deployment from one
#      channel to another, or off a pin, unless it is asked to;
#   5. latest, the last promoted release, for a new deployment.
#
# The channel it settles on is what the daemon's Upgrade button,
# scheduled and agent upgrades default to as well (pkg/upgrade's
# ChannelForRef reads the same tag back out of the same file), and
# channel_for_tag below is that function's shell copy.
#
# Self-contained functions, sourced on their own by tests/deploy.

# image_ref_in_file prints the last GRAIN_IMAGE= value in $1, read the way
# systemd reads an EnvironmentFile (and pkg/upgrade's readImageRef does):
# last assignment wins, surrounding blanks and quotes stripped. Nothing
# for a file that is missing or names no image.
image_ref_in_file() {
  local line ref=""
  [ -r "$1" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in GRAIN_IMAGE=*) ref="${line#GRAIN_IMAGE=}" ;; esac
  done < "$1"
  ref="${ref%"${ref##*[![:space:]]}"}"
  ref="${ref//\"/}"
  ref="${ref//\'/}"
  printf '%s' "$ref"
}

# image_ref_repo and image_ref_tag split an image reference
# ("ghcr.io/x/grain:latest@sha256:...") into its repository and its tag,
# ignoring any digest pinned beside the tag; the tag is empty for a
# reference that has none.
image_ref_repo() {
  local ref="${1%%@*}"
  case "${ref##*/}" in *:*) ref="${ref%:*}" ;; esac
  printf '%s' "$ref"
}
image_ref_tag() {
  local ref="${1%%@*}"
  case "${ref##*/}" in *:*) printf '%s' "${ref##*:}" ;; esac
}

# image_tag_is_pin is whether tag $1 names one build rather than a
# channel: sha-<short sha>, a release's vX.Y.Z or a candidate's
# vX.Y.Z-rc.N (with an optional -google or -oss edition suffix).
image_tag_is_pin() {
  case "$1" in sha-*) return 0 ;; esac
  [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-(google|oss))?(-rc\.[0-9]+)?(-(google|oss))?$ ]]
}

# channel_for_tag prints the channel tag $1 follows, as pkg/upgrade's
# ChannelForRef does: the channel itself, latest for a release's vX.Y.Z,
# candidate for vX.Y.Z-rc.N, the branch for any other branch's tag, and
# nothing for a sha- pin.
channel_for_tag() {
  case "$1" in
    "" | sha-* | *-discarded) ;;
    rc) printf candidate ;;
    latest | candidate | main) printf '%s' "$1" ;;
    *)
      if [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-(google|oss))?$ ]]; then
        printf latest
      elif [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-(google|oss))?-rc\.[0-9]+(-(google|oss))?$ ]]; then
        printf candidate
      else
        printf '%s' "${1//..//}"
      fi
      ;;
  esac
}

# report_image_revision says which commit the image $1, already pulled,
# was built from -- its org.opencontainers.image.revision label -- and,
# when tag $2 follows a branch (main, or a GRAIN_REF branch's
# tag), warns if that commit is behind the branch's head on GitHub
# (grain/task-4432). A branch's tag moves only when a release of a newer
# commit publishes, twenty minutes or more after the merge, and a failed
# release leaves it where it was; an operator who installs main to pick
# up a change that merged a quarter of an hour ago is owed being told
# they did not get it. It is a report and nothing else: whatever it
# cannot find out it says it could not, and it never fails the install.
report_image_revision() {
  local ref="$1" tag="$2" rev branch repo api head compare behind
  rev="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ref" 2>/dev/null || true)"
  rev="$(printf '%s' "$rev" | tr -dc '0-9a-f')"
  if [ "${#rev}" -ne 40 ]; then
    log "  $ref carries no revision label, so which commit it was built from is unknown"
    return 0
  fi
  log "  $ref was built from commit ${rev:0:9}"
  if [ -z "$tag" ] || [ "$tag" = latest ] || [ "$tag" = candidate ] || [ "$tag" = rc ] || image_tag_is_pin "$tag"; then
    return 0 # a release, release candidate or one build: there is no branch head to be behind
  fi
  branch="${tag//..//}"
  # ghcr.io/<owner>/<repo>/grain is published from github.com/<owner>/<repo>.
  repo="${GRAIN_IMAGE#*/}"
  repo="${repo%/*}"
  case "$repo" in */*) ;; *) return 0 ;; esac
  api="${GRAIN_GITHUB_API:-https://api.github.com}"
  # The image's own curl, as every other tool this script reaches for
  # (image_run): the host is promised docker and systemd and nothing else.
  head="$(image_run --entrypoint curl -- -fsS --max-time 10 -H 'Accept: application/vnd.github.sha' \
    "$api/repos/$repo/commits/$branch" 2>/dev/null | tr -dc '0-9a-f' || true)"
  if [ "${#head}" -ne 40 ]; then
    log "  could not ask GitHub where $repo's $branch is, so whether this is its newest build is unknown"
    return 0
  fi
  if [ "$head" = "$rev" ]; then
    log "  that is $branch's head"
    return 0
  fi
  compare="$(image_run --entrypoint curl -- -fsS --max-time 10 "$api/repos/$repo/compare/$rev...$head" 2>/dev/null || true)"
  behind="$(printf '%s' "$compare" | sed -n 's/^ *"ahead_by": *\([0-9][0-9]*\).*/\1/p' | head -n 1)"
  if [ -n "$behind" ]; then
    log "  WARNING: the $branch image is ${rev:0:9}, $behind commit(s) behind $branch (${head:0:9}); the newest build may still be publishing -- re-run setup.sh once it has, or pin GRAIN_IMAGE_TAG=sha-<short sha> to a build that has"
  else
    log "  WARNING: the $branch image is ${rev:0:9}, which is not $branch's head (${head:0:9}); the newest build may still be publishing"
  fi
}

# detect_install_track classifies the deployment track (defaults to "oss"
# unless GRAIN_INSTALL_TRACK is explicitly set).
detect_install_track() {
  if [ -n "${GRAIN_INSTALL_TRACK:-}" ]; then
    printf '%s\n' "$GRAIN_INSTALL_TRACK"
    return 0
  fi
  printf 'oss\n'
}

# default_image_for_track returns the default image repository for track $1.
default_image_for_track() {
  printf '%s\n' "${GRAIN_OSS_IMAGE_REPO:-${GRAIN_LEGACY_IMAGE_REPO:-ghcr.io/bwsalmon/grain/grain}}"
}

# image_repo_can_transition is whether a deployment whose ref file names
# repository $1 may keep its channel/tag when installing repository $2
# (repository shifts between the legacy, open-source, and allowed track
# repositories).
image_repo_can_transition() {
  local from="${1:-}" to="${2:-}" repo
  [ -n "$from" ] && [ -n "$to" ] || return 1
  [ "$from" = "$to" ] && return 0
  local known=(
    "ghcr.io/bwsalmon/grain/grain"
    "${GRAIN_LEGACY_IMAGE_REPO:-ghcr.io/bwsalmon/grain/grain}"
    "${GRAIN_OSS_IMAGE_REPO:-ghcr.io/bwsalmon/grain/grain}"
  )
  local allowed_list="${GRAIN_ALLOWED_IMAGE_REPOS:-}"
  for repo in ${allowed_list//,/ }; do
    [ -n "$repo" ] && known+=("$repo")
  done
  local from_ok=0 to_ok=0
  for repo in "${known[@]}"; do
    [ "$repo" = "$from" ] && from_ok=1
    [ "$repo" = "$to" ] && to_ok=1
  done
  [ "$from_ok" -eq 1 ] && [ "$to_ok" -eq 1 ]
}

# resolve_image_repository settles GRAIN_INSTALL_TRACK and, when GRAIN_IMAGE
# was not explicitly provided by the caller, selects the default image
# repository for that track.
resolve_image_repository() {
  local unit_service="${1:-/etc/systemd/system/grain-daemon.service}"
  local data_dir="${2:-${GRAIN_DATA_DIR:-/var/lib/grain/data}}"
  local explicit_track="${GRAIN_INSTALL_TRACK:-}"
  GRAIN_INSTALL_TRACK="$(detect_install_track "$unit_service" "$data_dir")"
  if [ -n "$explicit_track" ]; then
    GRAIN_INSTALL_TRACK_REPORT="$GRAIN_INSTALL_TRACK (GRAIN_INSTALL_TRACK=$explicit_track, as given)"
  else
    GRAIN_INSTALL_TRACK_REPORT="$GRAIN_INSTALL_TRACK (default)"
  fi
  if [ -z "${GRAIN_IMAGE_EXPLICIT:-}" ]; then
    GRAIN_IMAGE="$(default_image_for_track "$GRAIN_INSTALL_TRACK")"
  fi
  log "Install track: $GRAIN_INSTALL_TRACK_REPORT"
}

# resolve_image_tag settles GRAIN_IMAGE_TAG by the order above, given the
# unit's own image ref file ($1) and the daemon's upgrade request file
# ($2, read only when $1 names nothing), and says which it picked and
# why. It sets GRAIN_CHANNEL to the channel that tag follows (empty for a
# sha- pin) and GRAIN_CHANNEL_REPORT to the readiness report's line for
# it. A GRAIN_CHANNEL that is not a channel stops the install.
resolve_image_tag() {
  local unit_file="$1" request_file="$2" from="" installed="" installed_repo="" tag="" why=""
  case "$GRAIN_CHANNEL" in
    "" | latest | candidate | rc | main) ;;
    *)
      echo "setup.sh: GRAIN_CHANNEL=$GRAIN_CHANNEL is not a channel: use latest, candidate (or rc), or main" >&2
      echo "          (GRAIN_REF names any other branch, GRAIN_IMAGE_TAG any one build)" >&2
      return 1
      ;;
  esac
  if [ -n "$GRAIN_IMAGE_TAG" ]; then
    why="GRAIN_IMAGE_TAG=$GRAIN_IMAGE_TAG, as given"
    if [ -n "$GRAIN_CHANNEL" ] && [ "$GRAIN_CHANNEL" != "$GRAIN_IMAGE_TAG" ]; then
      log "  GRAIN_IMAGE_TAG=$GRAIN_IMAGE_TAG wins over GRAIN_CHANNEL=$GRAIN_CHANNEL"
    fi
  elif [ -n "$GRAIN_CHANNEL" ]; then
    why="GRAIN_CHANNEL=$GRAIN_CHANNEL, as given"
    if [ "$GRAIN_CHANNEL" = rc ]; then
      GRAIN_IMAGE_TAG=candidate
    else
      GRAIN_IMAGE_TAG="$GRAIN_CHANNEL"
    fi
  elif [ -n "$GRAIN_REF" ]; then
    GRAIN_IMAGE_TAG="${GRAIN_REF//\//..}"
    why="GRAIN_REF=$GRAIN_REF, as given"
  else
    for from in "$unit_file" "$request_file"; do
      installed="$(image_ref_in_file "$from")"
      [ -n "$installed" ] && break
    done
    installed_repo="$(image_ref_repo "$installed")"
    tag="$(image_ref_tag "$installed")"
    if [ -n "$installed" ] && image_repo_can_transition "$installed_repo" "$GRAIN_IMAGE" &&
      [[ "$tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
      GRAIN_IMAGE_TAG="$tag"
      if [ "$installed_repo" = "$GRAIN_IMAGE" ]; then
        why="kept: $from already runs $installed; set GRAIN_CHANNEL=latest|candidate|main to switch"
      else
        why="kept across repository shift ($installed_repo -> $GRAIN_IMAGE): $from already runs $installed; set GRAIN_CHANNEL=latest|candidate|main to switch"
      fi
    else
      if [ -n "$installed" ]; then
        log "  $from names $installed, which is no tag of $GRAIN_IMAGE -- installing a new deployment's default"
      fi
      GRAIN_IMAGE_TAG=latest
      why="the default for a new deployment: the last promoted release"
    fi
  fi
  GRAIN_CHANNEL="$(channel_for_tag "$GRAIN_IMAGE_TAG")"
  if ! image_tag_is_pin "$GRAIN_IMAGE_TAG"; then
    GRAIN_CHANNEL_REPORT="$GRAIN_CHANNEL ($why)"
  elif [ -n "$GRAIN_CHANNEL" ]; then
    GRAIN_CHANNEL_REPORT="pinned to $GRAIN_IMAGE_TAG, upgrades offered from $GRAIN_CHANNEL ($why)"
  else
    GRAIN_CHANNEL_REPORT="pinned to $GRAIN_IMAGE_TAG, which follows no channel ($why)"
  fi
  log "Image: $GRAIN_IMAGE:$GRAIN_IMAGE_TAG"
  log "  channel: $GRAIN_CHANNEL_REPORT"
}

# --- end of the image channel ------------------------------------------------

resolve_image_repository /etc/systemd/system/grain-daemon.service "$GRAIN_DATA_DIR"
if ! resolve_image_tag /etc/grain/image.env "$GRAIN_DATA_DIR/image.env"; then
  exit 2
fi

# --- grain hostd, settled ------------------------------------------------
#
# HOSTD_AGY_PATH is the agy the allowlist's entry names when GRAIN_AGY_PATH
# is given. Behind hostd agy runs on this host, so that path is not one the
# daemon's container has to resolve: it is neither mounted in nor passed
# as -agy-path, and both agent turns and the model-catalog probe run
# through hostd's agy entry.
HOSTD_UNIT=/etc/systemd/system/grain-hostd.service
# grain-hostd-upgrade.path watches $HOSTD_VERSIONS_DIR/current and starts
# grain-hostd-upgrade.service, which drains hostd onto the new binary
# (write_hostd_units).
HOSTD_UPGRADE_PATH_UNIT=/etc/systemd/system/grain-hostd-upgrade.path
HOSTD_UPGRADE_UNIT=/etc/systemd/system/grain-hostd-upgrade.service
HOSTD_UPGRADE_SCRIPT=/usr/local/lib/grain/hostd-upgrade.sh
# grain-hostd-allowlist.service adds grain's own capabilities entries the
# allowlist is missing, as root, with the build current names: before
# every hostd start and on every upgrade (write_hostd_units).
HOSTD_ALLOWLIST_UNIT=/etc/systemd/system/grain-hostd-allowlist.service
HOSTD_ALLOWLIST=/etc/grain/hostd.yaml
# This run's default allowlist, which a re-run rewrites and nothing loads:
# what a new default entry is suggested from (report_hostd_suggestions).
HOSTD_ALLOWLIST_DEFAULT=/etc/grain/hostd.yaml.default
HOSTD_ENV_FILE=/etc/grain/hostd.env
# The unit's RuntimeDirectory, and pkg/hostd's DefaultSocketPath inside it.
HOSTD_SOCKET_DIR=/run/grain-hostd
HOSTD_SOCKET="$HOSTD_SOCKET_DIR/hostd.sock"
# The unit's StateDirectory: where hostd records the process groups it
# started, so a restarted hostd reaps what the last one left.
HOSTD_STATE_DIR=/var/lib/grain-hostd
HOSTD_AGY_PATH=""
# The corp proxy, the one piece of this script's own environment a
# host-run CLI is given: hostd's unit reads it from $HOSTD_ENV_FILE, and
# the allowlist's pass_env hands it to the agy entry and no other.
HOSTD_PROXY_VARS=(HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy)

resolve_hostd() {
  case "$GRAIN_HOSTD" in
    auto)
      if [ "$GRAIN_USER" = root ]; then
        GRAIN_HOSTD=0
        log "grain hostd: not installing it -- GRAIN_USER is root, and hostd refuses to run as root"
      else
        GRAIN_HOSTD=1
        log "grain hostd: installing it (the default; GRAIN_HOSTD=0 leaves it out)"
      fi
      ;;
    1)
      log "grain hostd: installing it (GRAIN_HOSTD=1, as given)"
      ;;
    0)
      log "grain hostd: not installing it (GRAIN_HOSTD=0): every agent CLI runs in the daemon's"
      log "  container, nothing runs as $GRAIN_USER on this machine, and \"host\" is not offered"
      # The daemon refuses a -<cli>-run=host it has no host executor for
      # (daemon.CheckCLIRunFlags), so one is dropped rather than written.
      local var
      for var in GRAIN_CLAUDE_RUN GRAIN_CODEX_RUN GRAIN_AGY_RUN; do
        if [ "${!var}" = host ] || [ "${!var}" = detached ]; then
          log "  ignoring $var=${!var}: there is no grain hostd to run it through"
          printf -v "$var" '%s' ""
        fi
      done
      ;;
    *)
      echo "setup.sh: GRAIN_HOSTD must be auto, 1 or 0, not \"$GRAIN_HOSTD\"" >&2
      return 1
      ;;
  esac
  [ "$GRAIN_HOSTD" = 1 ] || return 0
  # hostd refuses to start as root (pkg/hostd), and everything it starts
  # runs as the account it runs as.
  if [ "$GRAIN_USER" = root ]; then
    echo "setup.sh: GRAIN_HOSTD=1 runs grain hostd as GRAIN_USER, and hostd refuses to run as root; name another account, or set GRAIN_HOSTD=0" >&2
    return 1
  fi
  # claude detached by default: the image's claude, with the deployment's
  # login, as a container run -- but through hostd's claude-detached
  # entry, so a daemon upgrade does not interrupt it
  # (docs/non-disruptive-upgrade.md). Not over an operator's own claude
  # (GRAIN_CLAUDE_PATH), which only a container run mounts in.
  if [ -z "${GRAIN_CLAUDE_RUN:-}" ] && [ -z "${GRAIN_CLAUDE_PATH:-}" ]; then
    GRAIN_CLAUDE_RUN=detached
    log "  claude runs detached through hostd (the default with hostd; GRAIN_CLAUDE_RUN=container keeps it in the daemon's container)"
  fi
  if [ -n "$GRAIN_AGY_PATH" ]; then
    HOSTD_AGY_PATH="$GRAIN_AGY_PATH"
  fi
  if [ -n "$HOSTD_AGY_PATH" ]; then
    case "$HOSTD_AGY_PATH" in
      /*) ;;
      *)
        echo "setup.sh: GRAIN_AGY_PATH must be an absolute path on this host for grain hostd's allowlist: $HOSTD_AGY_PATH" >&2
        return 1
        ;;
    esac
    log "  agy runs on this host through hostd: $HOSTD_AGY_PATH (not mounted into the daemon's container)"
  fi
  GRAIN_AGY_PATH=""
  return 0
}

if ! resolve_hostd; then
  exit 2
fi

# An operator's own Codex login, checked in both modes and here rather
# than in docker_run_args, where it used to be.
#
# That was the right place while a mount was the only thing this value
# did: a path that does not resolve inside the container is a Codex run
# that fails, and the mount is where it would have had to resolve. It is
# the wrong place now. The daemon is passed -codex-auth-file whichever
# mode this is, and host mode (since removed) wrote no `docker run` at
# all -- so the check ran for exactly one of the two deployments that
# depended on it.
#
# Absolute, because it is handed to a daemon whose working directory is
# nothing an operator chose, and in container mode it is also a mount
# source, where docker reads a relative path as a *volume name*. An
# existing regular file, because Codex rewrites this file in place when
# it rotates a token: a path that is not there yet is not a file that
# will be created, it is a login that was never done.
if [ -n "$GRAIN_CODEX_AUTH_FILE" ]; then
  case "$GRAIN_CODEX_AUTH_FILE" in
    /*) ;;
    *)
      echo "setup.sh: GRAIN_CODEX_AUTH_FILE must be an absolute host path: $GRAIN_CODEX_AUTH_FILE" >&2
      exit 1
      ;;
  esac
  if [ ! -f "$GRAIN_CODEX_AUTH_FILE" ]; then
    echo "setup.sh: GRAIN_CODEX_AUTH_FILE does not name an existing regular file: $GRAIN_CODEX_AUTH_FILE" >&2
    echo "  It is auth.json from \`codex login\`, on this host -- not a path grain creates." >&2
    exit 1
  fi
fi

# The base-system commands every step below assumes: systemd's own
# systemctl, and three that any distribution's base install carries
# (setpriv is util-linux's; as_grain uses it so root never writes through
# a path the service account controls, #2405). Checked rather than installed -- a host missing one of these is not a
# host this script can repair -- and deliberately a short list: see this
# file's own header, "What this host has to have".
for cmd in systemctl install useradd setpriv; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "setup.sh: required command not found: $cmd" >&2
    exit 1
  fi
done

# GRAIN_DOCKER_AVAILABLE records what ensure_docker found, for the
# handful of steps that can do without one (see docker_required).
GRAIN_DOCKER_AVAILABLE=1
# The client ensure_docker resolved, empty on the one install that needs
# none: systemd resolves no $PATH of its own, so every unit written below
# names it by path.
DOCKER_BIN=""

# docker_required answers whether this deployment simply cannot be
# installed without a container engine.
#
# Every one of them needs one now: both sandbox backends this script can
# install -- a granule VM's own container, and the local Docker backend --
# start their sandboxes through this host's own docker daemon, and a
# container-mode install's service is itself a `docker run`. Kept as a
# function, rather than inlined, because the steps that ask it are the
# steps that could do without docker while the unisolated host-directory
# backend still existed.
docker_required() {
  return 0
}

# docker is installed rather than only reported missing
# (bwsalmon/agents#617): it is the one package this script needs that a
# vanilla Debian cloud image does not carry, and until #617 it was only
# ever guaranteed by terraform/gcp/files/deploy.sh's own
# install_prerequisites -- which runs *before* this script but is no
# help to anyone reaching it the way this file's header says it should
# be reachable: put setup.sh on a bare VM and run it. git and jq had
# helpers of exactly this shape here too, and are gone -- nothing on
# this host needs either any more (see the header, "What this host has
# to have").
#
# The install first asks for Debian's daemon package, then, if that did
# not provide a `docker` command, asks for its CLI package too. Debian
# Trixie splits the CLI into docker-cli, while older releases provide it
# through docker.io. The `docker info` check a few lines down, not these
# best-effort installs, gates the rest of the script. It also enables the
# daemon, since a fresh install's postinst usually starts it but this does
# not rely on that.
#
# docker is what grain *runs in* now, not merely what it was once built
# in (bwsalmon/agents#645): grain-daemon.service is a `docker run` of the
# deployment image, so this is a hard runtime dependency of the service
# and not just of this script.
ensure_docker() {
  if ! command -v docker >/dev/null 2>&1 && docker_required && command -v apt-get >/dev/null 2>&1; then
    log "installing docker (grain-daemon.service runs the deployment image with it; no Debian cloud image carries it)"
    apt-get update -qq || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker.io || true
    if ! command -v docker >/dev/null 2>&1; then
      log "installing docker CLI from its separate package (needed on Debian Trixie)"
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-cli || true
    fi
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "setup.sh: required command not found: docker, and it could not be installed automatically -- install it (e.g. 'apt-get install docker.io docker-cli') and re-run" >&2
    exit 1
  fi
  # enable, not just start: the daemon has to come back on reboot, or
  # grain-daemon.service comes up to no container engine at all.
  systemctl enable --now docker >/dev/null 2>&1 || true
  # Resolved once, here, rather than written as a literal path: the unit
  # written below is read by systemd, which resolves no $PATH of its own,
  # and `docker` is /usr/bin/docker on Debian's own package but not
  # everywhere.
  DOCKER_BIN="$(command -v docker)"
}
ensure_docker

if [ "$GRAIN_DOCKER_AVAILABLE" = "1" ] && ! docker info >/dev/null 2>&1; then
  # The same question the client's presence could not answer, and the
  # same two answers: an install that needs an engine stops here, and the
  # one that does not carries on without one rather than failing over a
  # dependency it was never going to use.
  if docker_required; then
    echo "setup.sh: 'docker info' failed -- is the Docker daemon running? grain-daemon.service runs the deployment image with it, so this is a runtime dependency, not only a deploy-time one" >&2
    exit 1
  fi
  log "'docker info' failed, and this install needs no engine -- carrying on without one"
  GRAIN_DOCKER_AVAILABLE=0
fi

# --- 1/2. pull the deployment image, and install the CLI wrappers -------
#
# This is where a v2 deploy used to spend its minutes: `make -C v2
# container-build`, a pinned Go toolchain image, a cold module cache, a
# `npm ci` for the frontend, and a binary at the end of it. None of that
# happens on a deployed host any more (bwsalmon/agents#645) -- CI built
# and published this exact image when the commit landed, so a deploy is a
# `docker pull` and a service restart.
#
# GRAIN_IMAGE_REF is the one name everything below agrees on; the ref
# *file* is the indirection the service actually reads, so that the UI's
# Upgrade button can repoint a deployment by writing one line rather than
# by rewriting a systemd unit (see write_image_ref and
# pkg/upgrade/image.go).
GRAIN_IMAGE_REF="${GRAIN_IMAGE}:${GRAIN_IMAGE_TAG}"
IMAGE_REF_FILE="$GRAIN_DATA_DIR/image.env"

# IMAGE_REF_FILE above is $GRAIN_USER's to write -- it is how the daemon
# asks for an upgrade -- and so it is never what root acts on (#2405).
# UNIT_IMAGE_REF_FILE is the root-owned copy grain-daemon.service actually
# reads as its EnvironmentFile, and IMAGE_REF_PROMOTER is the root
# pre-step that copies a request across: parsed as data, never sourced,
# and only ever a reference to an image in $GRAIN_IMAGE. A file that
# account could write being read by systemd as an environment file was a
# way to hand `docker run` any argument at all (GRAIN_EXTRA_DOCKER_RUN_ARGS
# in it was expanded into the ExecStart line), and sourcing it from a
# root script was simply running whatever it said.
UNIT_IMAGE_REF_DIR=/etc/grain
UNIT_IMAGE_REF_FILE="$UNIT_IMAGE_REF_DIR/image.env"
# Where GRAIN_IMAGE_VERIFY_KEY is copied for the daemon's own
# -upgrade-verify-cmd (install_image_verify_key): root's, beside the ref
# file and for the same reason -- what decides which images this host
# will run is never a file $GRAIN_USER can write.
IMAGE_VERIFY_KEY_FILE="$UNIT_IMAGE_REF_DIR/cosign.pub"
IMAGE_REF_PROMOTER=/usr/local/lib/grain/promote-image-ref.sh

# image_ref_from prints the image reference a ref file names: the value
# of its last GRAIN_IMAGE= line, the way systemd and pkg/upgrade's
# readImageRef read one -- and nothing at all unless that value is a
# reference to an image in repository $2, made only of the characters an
# image reference has. Read as data, never sourced, so a file that says
# anything else is a file that names no image rather than a command root
# runs; and never through a symlink, since $GRAIN_USER can plant one
# where the file should be. Copied verbatim (declare -f) into every
# script below that reads a ref file, so there is one parser.
image_ref_from() {
  local file="$1" repo="$2" line ref="" r="" matched=0
  shift 2 || true
  if [ ! -f "$file" ] || [ -L "$file" ]; then
    return 0
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in
      GRAIN_IMAGE=*) ref="${line#GRAIN_IMAGE=}" ;;
    esac
  done < <(head -c 4096 -- "$file")
  ref="${ref%"${ref##*[![:space:]]}"}"
  ref="${ref#[\"\']}"
  ref="${ref%[\"\']}"
  local repos=("$repo" "$@")
  local allowed_list="${GRAIN_ALLOWED_IMAGE_REPOS:-}"
  for r in ${allowed_list//,/ }; do
    [ -n "$r" ] && repos+=("$r")
  done
  case "$repo" in
    "ghcr.io/bwsalmon/grain/grain" | "${GRAIN_LEGACY_IMAGE_REPO:-}" | "${GRAIN_OSS_IMAGE_REPO:-}")
      repos+=(
        "ghcr.io/bwsalmon/grain/grain"
      )
      [ -n "${GRAIN_LEGACY_IMAGE_REPO:-}" ] && repos+=("$GRAIN_LEGACY_IMAGE_REPO")
      [ -n "${GRAIN_OSS_IMAGE_REPO:-}" ] && repos+=("$GRAIN_OSS_IMAGE_REPO")
      ;;
  esac
  for r in "${repos[@]}"; do
    [ -n "$r" ] || continue
    case "$ref" in
      "$r":* | "$r"@*)
        matched=1
        break
        ;;
    esac
  done
  [ "$matched" -eq 1 ] || return 0
  if [ "${#ref}" -gt 512 ] || ! [[ "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]]; then
    return 0
  fi
  printf '%s\n' "$ref"
}

# as_grain runs one command as $GRAIN_USER rather than as the root this
# script is. Every write into a directory that account owns goes through
# it (#2405): root writing, chmod-ing or chown-ing a path there follows
# whatever symlink the account left in its place -- to /etc/shadow as
# easily as to a file of its own -- while the account itself can only
# ever reach what it could have reached anyway.
as_grain() {
  setpriv --reuid="$GRAIN_USER" --regid="$GRAIN_GROUP" --clear-groups -- "$@"
}

# grain_dir creates directory $2 (mode $1) inside $GRAIN_DATA_DIR, as
# $GRAIN_USER. A symlink there is refused rather than followed.
grain_dir() {
  local mode="$1" path="$2"
  if [ -L "$path" ]; then
    echo "setup.sh: $path is a symlink; refusing to follow it as root (#2405)" >&2
    return 1
  fi
  as_grain install -d -m"$mode" "$path"
}

# grain_write writes stdin to file $2 (mode $1), as $GRAIN_USER, through
# a temporary file renamed into place: a reader never sees half a file,
# and a symlink at $2 is replaced rather than written through.
grain_write() {
  # shellcheck disable=SC2016 # expanded by that sh, from its own arguments
  as_grain sh -c 'umask 077; tmp="$(mktemp "$2.XXXXXX")" && cat > "$tmp" && chmod "$1" "$tmp" && mv -fT "$tmp" "$2"' \
    grain_write "$1" "$2"
}

# grain_cli runs the grain CLI as $GRAIN_USER, which is the only way
# this script ever runs it. In container mode /usr/local/bin/grain is
# the `docker run --user uid:gid` wrapper (install_cli_wrappers): it has
# to be started as root to reach the docker socket, and drops to the
# account by itself. Beside grain hostd it is the real binary
# (install_hostd_binary); run as root it would open the secrets database and run git in the state-repo
# working tree -- config, hooks, symlinks -- all of which the account
# controls (#2405). So it is run as the account, with the HOME the
# daemon's unit gives it and from a directory it can read.
grain_cli() {
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    as_grain env -C / HOME="$GRAIN_DATA_DIR/home" /usr/local/bin/grain "$@"
  else
    /usr/local/bin/grain "$@"
  fi
}

# image_run runs one command inside the deployment image: docker
# arguments before "--", the command line for the image after it --
#
#   image_run --network host --entrypoint curl -- -fsS "$url"
#
# This is how every step below that needs more than a shell gets it, and
# it is why this host needs nothing but docker (see this file's header,
# "What this host has to have"). The image already carries git, curl and
# grain's own source, and this host is already pulling and running it --
# so borrowing a tool out of it costs a deployment nothing, while
# requiring the same tool out here would add a package every host must
# have before it can deploy at all.
#
# --entrypoint is not optional in any call: this image's own entrypoint
# is `grain` (Dockerfile), so a caller that forgets it runs a grain
# subcommand instead of the command it named.
#
# Deliberately no --network host by default: only the callers that must
# reach an address on this host itself -- the metadata server, a
# GRAIN_GITHUB_HOST that is a mock on loopback -- ask for it.
image_run() {
  local docker_args=()
  while [ "$#" -gt 0 ]; do
    [ "$1" = "--" ] && { shift; break; }
    docker_args+=("$1")
    shift
  done
  docker run --rm "${docker_args[@]}" "$GRAIN_IMAGE_REF" "$@"
}

# registry_login authenticates to the image's registry, if this
# deployment was given a credential for it. Nothing to do in the ordinary
# case: bwsalmon/grain's GHCR package is public, and an anonymous pull
# works. Never fatal on its own -- a failed login is reported here and
# then simply becomes whatever the pull below reports.
#
# An image on Artifact Registry or GCR takes a Google OAuth2 access token
# instead, under the fixed user name oauth2accesstoken. With no
# GRAIN_IMAGE_PULL_TOKEN given, one is looked up the way
# ensure_granule_oci_image does for the sandbox image: gcloud's own
# credential if this host has gcloud, otherwise the metadata server's
# token for the host's attached service account. granule_gcp_access_token
# asks the metadata server from the host first for exactly this call,
# which runs before $GRAIN_IMAGE_REF has been pulled.
registry_login() {
  local registry="${GRAIN_IMAGE%%/*}" target user token="$GRAIN_IMAGE_PULL_TOKEN"
  target="$registry"
  user="${GRAIN_IMAGE_PULL_USER:-x-access-token}"
  if [[ "$GRAIN_IMAGE" == */* ]] && google_registry_host "$registry"; then
    target="https://${registry}"
    user="${GRAIN_IMAGE_PULL_USER:-oauth2accesstoken}"
    if [ -z "$token" ]; then
      token="$(gcp_access_token || true)"
    fi
  fi
  [ -n "$token" ] || return 0
  log "Logging in to $registry as $user"
  if ! printf '%s' "$token" \
     | docker login "$target" -u "$user" --password-stdin >/dev/null 2>&1; then
    log "  docker login against $registry failed -- the pull below will say whether it mattered"
  fi
}

# google_registry_host is whether $1, an image reference's registry
# host, is Artifact Registry or GCR -- the hosts that are handed this
# machine's Google access token. Matched on the host alone and only on a
# bare DNS name: a glob over the whole reference ("*-docker.pkg.dev/*")
# also matches evil.test/x-docker.pkg.dev/img, and a host carrying a "?"
# or "#" ends in a Google suffix while docker logs in somewhere else.
google_registry_host() {
  [[ "$1" =~ ^[a-z0-9.-]+$ ]] || return 1
  case "$1" in
    *-docker.pkg.dev|gcr.io|*.gcr.io) return 0 ;;
  esac
  return 1
}

# gcp_access_token prints a Google OAuth2 access token for pulling from
# Artifact Registry or GCR: the gcloud credential of $GRAIN_USER when
# this host has gcloud and it answers, otherwise the metadata server's
# (granule_gcp_access_token). Neither is required; no token is a non-zero
# return, and the pull that follows then says whether it mattered.
gcp_access_token() {
  local token=""
  if command -v gcloud >/dev/null 2>&1; then
    token="$(as_grain gcloud auth print-access-token 2>/dev/null | tr -d '\r\n' || true)"
  fi
  if [ -z "$token" ]; then
    token="$(granule_gcp_access_token 2>/dev/null || true)"
  fi
  [ -n "$token" ] || return 1
  printf '%s\n' "$token"
}

# pull_image fetches the build this deploy is meant to run.
#
# A pull that fails is fatal *unless* this host already has that exact
# ref in its local image store: a re-run over a transient network failure
# (or a registry outage) then converges on the image it was already asked
# for rather than refusing to deploy at all -- the same "converge with
# what is ready" trade the granule steps below make. It is not a fallback
# to some *other* image: an unknown tag with nothing local still stops
# here, loudly, instead of leaving grain-daemon.service pointed at
# something that does not exist.
pull_image() {
  if [ "$GRAIN_DOCKER_AVAILABLE" != "1" ]; then
    log "No container engine on this host: nothing to pull"
    return 0
  fi
  registry_login
  step_start "Pulling $GRAIN_IMAGE_REF"
  if docker pull "$GRAIN_IMAGE_REF"; then
    verify_grain_image
    report_image_revision "$GRAIN_IMAGE_REF" "$GRAIN_IMAGE_TAG" || true
    step_done "ready"
    return 0
  fi
  if docker image inspect "$GRAIN_IMAGE_REF" >/dev/null 2>&1; then
    log "  pull failed, but this host already has $GRAIN_IMAGE_REF locally -- deploying that"
    verify_grain_image
    report_image_revision "$GRAIN_IMAGE_REF" "$GRAIN_IMAGE_TAG" || true
    step_done "ready"
    return 0
  fi
  echo "setup.sh: could not pull $GRAIN_IMAGE_REF and this host has no local copy of it." >&2
  echo "  Check that the tag exists (CI publishes one per branch and one per commit --" >&2
  echo "  see .github/workflows/build-artifacts.yml), or set GRAIN_IMAGE_TAG to one that" >&2
  echo "  does. A private package also needs GRAIN_IMAGE_PULL_USER/GRAIN_IMAGE_PULL_TOKEN." >&2
  exit 1
}

# The registry digest this host holds for $1, a pulled `repo:tag` (or
# `repo:tag@sha256:...`, which is what a release stamps its sandbox
# images as -- scripts/release-images.sh): `repo@sha256:...`, or nothing
# if the image did not come from that repository (a local build tagged
# to look like one). A pinned $1 answers with its own digest and no
# other, so what is verified is exactly what it names, never some other
# digest the same repository also left on this host.
image_digest_ref() {
  local repo="${1%%@*}" want=""
  case "${repo##*/}" in *:*) repo="${repo%:*}" ;; esac
  case "$1" in *@sha256:*) want="$repo@${1#*@}" ;; esac
  docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$1" 2>/dev/null \
    | awk -v repo="$repo" -v want="$want" \
      '(want == "" && index($0, repo "@sha256:") == 1) || (want != "" && $0 == want) {print; exit}'
}

# verify_image_signature checks who published $1, already pulled, by
# its digest -- the same question pkg/upgrade's signature step asks
# before an upgrade runs an image (docs/image-signing.md). It prints the
# `repo@sha256:...` it verified. With no GRAIN_IMAGE_VERIFY_KEY it warns
# and prints nothing; a signature that does not verify is fatal.
verify_image_signature() {
  local image="$1" digest_ref
  if [ -z "$GRAIN_IMAGE_VERIFY_KEY" ]; then
    log "WARNING: not verifying who published $image (no GRAIN_IMAGE_VERIFY_KEY) -- running whatever the registry served" >&2
    return 0
  fi
  if ! command -v cosign >/dev/null 2>&1; then
    echo "setup.sh: GRAIN_IMAGE_VERIFY_KEY is set but cosign is not on PATH, so $image cannot be verified." >&2
    echo "  Install cosign (https://github.com/sigstore/cosign/releases), or unset GRAIN_IMAGE_VERIFY_KEY to run it unverified." >&2
    exit 1
  fi
  digest_ref="$(image_digest_ref "$image")"
  if [ -z "$digest_ref" ]; then
    echo "setup.sh: $image has no registry digest on this host, so there is nothing a signature could be checked against." >&2
    exit 1
  fi
  if ! cosign verify --key "$GRAIN_IMAGE_VERIFY_KEY" --insecure-ignore-tlog=true "$digest_ref" >/dev/null; then
    echo "setup.sh: $digest_ref ($image) is not signed with $GRAIN_IMAGE_VERIFY_KEY -- refusing to run it." >&2
    exit 1
  fi
  log "  $digest_ref is signed with $GRAIN_IMAGE_VERIFY_KEY" >&2
  printf '%s\n' "$digest_ref"
}

# verify_grain_image verifies the grain image just pulled and, when it
# did, pins GRAIN_IMAGE_REF to the digest that verified: the ref file and
# every `docker run` below then name those bytes rather than a tag that
# the next pull on this host can move.
verify_grain_image() {
  local verified
  verified="$(verify_image_signature "$GRAIN_IMAGE_REF")" || exit 1
  # A GRAIN_IMAGE_REF that is already pinned keeps the digest it names,
  # which is the one image_digest_ref just verified.
  if [ -n "$verified" ] && [[ "$GRAIN_IMAGE_REF" != *@* ]]; then
    GRAIN_IMAGE_REF="${GRAIN_IMAGE_REF}@${verified#*@}"
  fi
}

# install_image_verify_key copies GRAIN_IMAGE_VERIFY_KEY to
# IMAGE_VERIFY_KEY_FILE, root-owned and world-readable (it is a public
# key, and the daemon reads it as $GRAIN_USER), so that the daemon can
# verify the images its own upgrades pull as this script verified the
# one it pulled: write_systemd_units passes -upgrade-verify-cmd naming
# that path, and docker_run_args mounts it read-only. A copy rather than
# the operator's own path, because that path need not be one $GRAIN_USER
# can read, need not exist after this run, and is not one a unit should
# depend on staying put.
#
# With no key, a copy left by an earlier run is removed: the unit no
# longer names it, and a stale key in /etc/grain is one nobody would
# think to rotate.
install_image_verify_key() {
  if [ -z "$GRAIN_IMAGE_VERIFY_KEY" ]; then
    rm -f "$IMAGE_VERIFY_KEY_FILE"
    return 0
  fi
  if [ ! -f "$GRAIN_IMAGE_VERIFY_KEY" ]; then
    echo "setup.sh: GRAIN_IMAGE_VERIFY_KEY=$GRAIN_IMAGE_VERIFY_KEY is not a file on this host." >&2
    exit 1
  fi
  install -d -m0755 -o root -g root "$UNIT_IMAGE_REF_DIR"
  # install(1) refuses to copy a file onto itself, and naming the
  # installed copy is a reasonable thing for a re-run to do.
  if [ ! "$GRAIN_IMAGE_VERIFY_KEY" -ef "$IMAGE_VERIFY_KEY_FILE" ]; then
    install -m0644 -o root -g root "$GRAIN_IMAGE_VERIFY_KEY" "$IMAGE_VERIFY_KEY_FILE"
  fi
  chown root:root "$IMAGE_VERIFY_KEY_FILE"
  chmod 0644 "$IMAGE_VERIFY_KEY_FILE"
}

# verify_image_carries_cosign warns when the image this unit is about to
# run has no cosign of its own. The upgrade verifier runs inside the
# *running* daemon, so an image built before the Dockerfile shipped cosign
# would refuse every upgrade it was asked for -- including the one that
# would replace it with an image that has it. Warned, not refused, like
# verify_daemon_flags: the deployment itself runs, and the remedy is to
# re-run this script with a newer GRAIN_IMAGE_TAG, which nothing in the
# UI can do for it.
verify_image_carries_cosign() {
  if image_run --entrypoint /usr/local/bin/cosign -- version >/dev/null 2>&1; then
    return 0
  fi
  log "WARNING: $GRAIN_IMAGE_REF has no /usr/local/bin/cosign, so every upgrade from the UI will be refused"
  log "         at its signature step. Re-run this script with a GRAIN_IMAGE_TAG built since cosign"
  log "         was added to the image, or unset GRAIN_IMAGE_VERIFY_KEY to upgrade unverified."
}

# A local Docker sandbox is started by the daemon container through the host's
# Docker socket. Pull its image here, while the host-side client still has the
# registry credentials registry_login installed. The daemon runs with its own
# HOME and cannot see those credentials; leaving this to `docker run` makes a
# private registry fail only when the first task is dispatched.
#
# Ask the Grain image for its immutable, same-commit sandbox reference rather
# than deriving a tag here. That keeps installs, upgrades, and rollbacks paired.
ensure_container_sandbox_image() {
  if [ "$GRAIN_DOCKER_AVAILABLE" != "1" ]; then
    return 0
  fi
  step_start "Checking local Docker sandbox image"
  local image
  image="$(image_run -- container-sandbox-image || true)"
  if [ -z "$image" ]; then
    echo "setup.sh: $GRAIN_IMAGE_REF did not name its local Docker sandbox image." >&2
    exit 1
  fi

  if docker image inspect "$image" >/dev/null 2>&1; then
    log "Local Docker sandbox image $image is already present"
    verify_image_signature "$image" >/dev/null
    step_done "ready"
    return 0
  fi

  log "Pulling local Docker sandbox image $image"
  if docker pull "$image"; then
    verify_image_signature "$image" >/dev/null
    step_done "ready"
    return 0
  fi
  echo "setup.sh: could not pull local Docker sandbox image $image and this host has no local copy of it." >&2
  echo "  A private package needs GRAIN_IMAGE_PULL_USER/GRAIN_IMAGE_PULL_TOKEN." >&2
  exit 1
}

# record_replaced_image notes, before pull_image moves the tag, which
# image this host ran until now: the daemon container's own image, or
# failing that (a stopped service, a host-mode install) the image the
# root-owned ref file names. prune_replaced_images removes it, and the
# sandbox images it named, once this run's daemon is up on the new one.
# A tag such as `main` that the pull moves leaves the old image behind
# untagged, where nothing else would ever remove it (task 1058, N3).
REPLACED_IMAGE_ID=""
record_replaced_image() {
  REPLACED_IMAGE_ID=""
  [ "$GRAIN_DOCKER_AVAILABLE" = "1" ] || return 0
  REPLACED_IMAGE_ID="$(docker container inspect --format '{{.Image}}' grain-daemon 2>/dev/null || true)"
  if [ -z "$REPLACED_IMAGE_ID" ] && [ -r "$UNIT_IMAGE_REF_FILE" ]; then
    local ref
    ref="$(sed -n 's/^GRAIN_IMAGE=//p' "$UNIT_IMAGE_REF_FILE" | tail -n1)"
    if [ -n "$ref" ]; then
      REPLACED_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$ref" 2>/dev/null || true)"
    fi
  fi
}

# prune_replaced_images removes the grain image record_replaced_image
# noted, and the sandbox images it named, when this run replaced it --
# each only if no container (a sandbox a run left behind, say) still
# uses it, and none that this run's image still names. A failed removal
# is reported and is never fatal: the install itself is done.
prune_replaced_images() {
  [ "$GRAIN_DOCKER_AVAILABLE" = "1" ] && [ -n "$REPLACED_IMAGE_ID" ] || return 0
  local current
  current="$(image_id "$GRAIN_IMAGE_REF")"
  if [ -z "$current" ] || [ "$current" = "$REPLACED_IMAGE_ID" ] ||
     ! docker image inspect "$REPLACED_IMAGE_ID" >/dev/null 2>&1; then
    return 0
  fi
  local keep=" $current " ref id
  while IFS= read -r ref; do
    [ -n "$ref" ] && keep+="$(image_id "$ref") "
  done < <(sandbox_image_refs "$GRAIN_IMAGE_REF"; echo "${GRAIN_GRANULE_OCI_IMAGE:-}")
  local old=()
  mapfile -t old < <(sandbox_image_refs "$REPLACED_IMAGE_ID" | awk 'NF && !seen[$0]++')
  old+=("$REPLACED_IMAGE_ID")
  log "Removing the images this run replaced"
  for ref in "${old[@]}"; do
    id="$(image_id "$ref")"
    if [ -z "$id" ] || [[ "$keep" == *" $id "* ]]; then
      continue
    fi
    if [ -n "$(docker ps -aq --filter "ancestor=$id" 2>/dev/null)" ]; then
      log "  kept $ref: a container still uses it"
      continue
    fi
    # By name first, so a tag is all that goes when another still holds
    # the image; then by id, for a ref docker will not take (an untagged
    # image, or a pinned repo:tag@sha256:...).
    if docker rmi "$ref" >/dev/null 2>&1 || docker rmi "$id" >/dev/null 2>&1; then
      log "  removed $ref"
    else
      log "  could not remove $ref -- docker rmi $id"
    fi
  done
}

# install_cli_wrappers puts `grain` and `granule` on this host's PATH
# without installing either binary on it: each is a one-line script over
# a shared runner that runs the deployment image, so an operator's
# `grain list` and
# scripts/granule-diag.sh's `granule vm list` both reach exactly the
# build grain-daemon.service is running, and neither can drift from it.
# This script uses the `grain` one itself, for `grain schema-version`
# (reformat_store_if_schema_changed) and `grain secrets`
# (seed_gcp_minter_key, mint_gemini_operating_key, report_readiness).
#
# The wrapper decides its own mounts at invocation time rather than
# baking in what was true when this ran: it is written early (the rest of
# this script needs it) and the granule decisions below are not final
# until much later, so an existence guard per optional path is what keeps
# one file correct in both cases -- and keeps it correct after a later
# re-run changes them.
#
# It runs the image as $GRAIN_USER, exactly as the service does, so a
# store or secrets database a CLI invocation creates comes out owned by
# the account the daemon reads it with rather than by root.
install_cli_wrappers() {
  install -d -m0755 /usr/local/lib/grain
  # The port the daemon serves on, baked into the wrapper as the CLI's
  # default -server. /etc/profile.d/grain.sh (write_cli_profile, below)
  # exports the same value for an operator's shell, but that export
  # cannot reach a process inside a container -- so without this a
  # `grain list` would fall back to cmd/grain/main.go's own
  # http://127.0.0.1:4242, which is only right while GRAIN_UI_ADDR is. A
  # GRAIN_SERVER already set in the caller's environment still wins.
  local port="${GRAIN_UI_ADDR##*:}"
  # GRAIN_DATA_DIR goes in the same way, for the two subcommands that
  # read this deployment's files rather than its API: `grain state` and
  # `grain secrets` take a -data-dir and default it to this variable, so
  # without it the `grain state status` this script's own closing report
  # tells the operator to run fails with "-data-dir is required"
  # (grain/task-303). Baked rather than let through from the caller like
  # GRAIN_SERVER is: the only data directory that exists inside this
  # container is the one mounted just below, so any other value names a
  # path that is not there.

  # Removed, not overwritten: on a host deployed before
  # bwsalmon/agents#645 both of these are *symlinks* into
  # $GRAIN_DATA_DIR/bin, and `cat >` follows a symlink -- which would
  # write this wrapper over the binary at the far end and leave
  # /usr/local/bin/grain still pointing at it, so `grain` would run a
  # shell script the kernel was asked to exec as a binary.
  rm -f /usr/local/bin/granule
  [ "${GRAIN_HOSTD:-0}" = "1" ] || rm -f /usr/local/bin/grain
  # What a host-mode install (host mode has since been removed) took out
  # of the image, its nft sudoers rule, and the pre-step that re-took the
  # binaries on every start: none of it is container mode's, and a
  # migrating install still has them.
  rm -f "$HOST_NFT_SUDOERS" /usr/local/lib/grain/.extracted-image-id
  if [ "$GRAIN_MIGRATED_FROM_HOST_MODE" = 1 ]; then
    # And the drop-in a host-mode Upgrade wrote (GRAIN_INSTALL_MODE=host,
    # pkg/upgrade's unit step), which would otherwise outlive the unit.
    rm -rf "$HOST_SOURCE_DIR"
    rm -f /etc/systemd/system/grain-daemon.service.d/grain.conf
    rmdir /etc/systemd/system/grain-daemon.service.d 2>/dev/null || true
  fi
  [ "${GRAIN_HOSTD:-0}" = "1" ] || rm -f "$HOST_EXTRACTOR" "$HOSTD_EXTRACTED_ID_FILE"
  local uid gid
  uid="$(id -u "$GRAIN_USER")"
  # id -g of the *user*, not of $GRAIN_GROUP: `id` takes an account name,
  # and $GRAIN_GROUP is a group. The two spellings agree for the account
  # this script creates, whose group is its own name, and the wrong one
  # fails with "id: 'staff': no such user" for exactly the account
  # GRAIN_GROUP exists to support. The number is the same either way --
  # the account's primary gid is its primary group's.
  gid="$(id -g "$GRAIN_USER")"

  cat > /usr/local/lib/grain/run-image.sh <<WRAPPER
#!/usr/bin/env bash
# Written by scripts/setup.sh (install_cli_wrappers). Runs one command
# in the deployment image, with the same identity and the same view of
# this host's directories that grain-daemon.service's own container has.
# Called by /usr/local/bin/grain and /usr/local/bin/granule, which
# differ only in the entrypoint they ask for.
#
# Arguments before "--" are passed to docker run; those after it are the
# command line for the image itself, which this appends the image ref in
# front of.
set -euo pipefail

extra=()
while [ "\$#" -gt 0 ]; do
  [ "\$1" = "--" ] && { shift; break; }
  extra+=("\$1")
  shift
done

# The image the service is running right now, not the one this wrapper
# was written for: an upgrade through the UI ends in a restart that
# rewrites ${UNIT_IMAGE_REF_FILE} (the file grain-daemon.service reads),
# and a CLI that kept answering out of the old image would be reporting
# on a deployment that no longer exists. Parsed as data, never sourced:
# whoever runs this may be root (#2405).
GRAIN_OSS_IMAGE_REPO="\${GRAIN_OSS_IMAGE_REPO:-${GRAIN_OSS_IMAGE_REPO:-}}"
GRAIN_ALLOWED_IMAGE_REPOS="\${GRAIN_ALLOWED_IMAGE_REPOS:-${GRAIN_ALLOWED_IMAGE_REPOS:-}}"
$(declare -f image_ref_from)
image="\$(image_ref_from "${UNIT_IMAGE_REF_FILE}" "${GRAIN_IMAGE}")"
image="\${image:-${GRAIN_IMAGE_REF}}"

args=(
  --rm --interactive
  --network host
  --user ${uid}:${gid}
  --env HOME=${GRAIN_DATA_DIR}/home
  --env "GRAIN_SERVER=\${GRAIN_SERVER:-http://127.0.0.1:${port}}"
  --env GRAIN_DATA_DIR=${GRAIN_DATA_DIR}
  --volume ${GRAIN_DATA_DIR}:${GRAIN_DATA_DIR}
)
[ -d "${GRAIN_SANDBOX_DIR}" ] && args+=(--volume ${GRAIN_SANDBOX_DIR}:${GRAIN_SANDBOX_DIR})
# granule talks to this host's docker daemon and keeps its VM records
# out here; the image paths it hands docker are host paths, so each has
# to be mounted at the very path it already has.
if [ -S /var/run/docker.sock ]; then
  args+=(--volume /var/run/docker.sock:/var/run/docker.sock)
  docker_gid="\$(getent group docker | cut -d: -f3)"
  [ -n "\$docker_gid" ] && args+=(--group-add "\$docker_gid")
fi
[ -d /var/lib/granule ] && args+=(--volume /var/lib/granule:/var/lib/granule)

exec docker run "\${args[@]}" "\${extra[@]}" "\$image" "\$@"
WRAPPER
  chmod 0755 /usr/local/lib/grain/run-image.sh

  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    # Behind grain hostd /usr/local/bin/grain is the image's own binary,
    # not a wrapper: hostd's unit runs it, and a host-run agy forks its
    # mcpserver and hooks by the daemon's own path, which is this one
    # (docs/design/cli-run-location.md, "agy's MCP server and hooks").
    # The operator's `grain` is then that binary too, as in host mode.
    install_hostd_binary
  else
    cat > /usr/local/bin/grain <<'CLI'
#!/usr/bin/env bash
# Written by scripts/setup.sh: the grain CLI, out of the deployment
# image. `grain <args>` reaches the image's own entrypoint.
exec /usr/local/lib/grain/run-image.sh -- "$@"
CLI
    chmod 0755 /usr/local/bin/grain
  fi

  cat > /usr/local/bin/granule <<'CLI'
#!/usr/bin/env bash
# Written by scripts/setup.sh: granule, out of the deployment image
# -- the same copy pkg/granule runs inside grain-daemon.service's own
# container, so a diagnostic run out here (scripts/granule-diag.sh)
# and the daemon's own calls cannot be different builds.
exec /usr/local/lib/grain/run-image.sh --entrypoint granule -- "$@"
CLI
  chmod 0755 /usr/local/bin/granule

  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    log "Installed /usr/local/bin/grain ($GRAIN_IMAGE_REF's own binary, for grain hostd) and /usr/local/bin/granule (runs $GRAIN_IMAGE_REF)"
  else
    log "Installed /usr/local/bin/grain and /usr/local/bin/granule (both run $GRAIN_IMAGE_REF)"
  fi
  write_cli_profile
}

# write_image_ref records which image grain-daemon.service should run.
# Rewritten on every run of this script, so re-running with a different
# GRAIN_IMAGE_TAG (or a different GRAIN_REF) is what pins or rolls back a
# deployment.
#
# Two files, written together. ${IMAGE_REF_FILE} is the one pkg/upgrade's
# image path writes too, so the UI's Upgrade button and this script are
# two ways of doing one thing rather than two mechanisms that can
# disagree -- but it is $GRAIN_USER's, so it is only ever a request:
# written here as that account (grain_write), and read by root only as
# data, by the promoter below. ${UNIT_IMAGE_REF_FILE} is the root-owned
# file the unit itself reads, written here directly from what the
# operator asked for (#2405).
#
# Runs after setup_data_dir, which is what creates the directory the
# first of them lives in.
write_image_ref() {
  printf 'GRAIN_IMAGE=%s\n' "$GRAIN_IMAGE_REF" | grain_write 0644 "$IMAGE_REF_FILE"
  install -d -m0755 -o root -g root "$UNIT_IMAGE_REF_DIR"
  local tmp
  tmp="$(mktemp "$UNIT_IMAGE_REF_FILE.XXXXXX")"
  printf 'GRAIN_IMAGE=%s\n' "$GRAIN_IMAGE_REF" > "$tmp"
  chmod 0644 "$tmp"
  mv -fT "$tmp" "$UNIT_IMAGE_REF_FILE"
}

# write_image_ref_promoter writes the root pre-step grain-daemon.service
# runs before every start in container mode: the one place a request in
# ${IMAGE_REF_FILE} -- the UI's Upgrade button, through pkg/upgrade --
# becomes the image the unit runs. It takes the request as data
# (image_ref_from), never as shell; copies across only a reference to an
# image in ${GRAIN_IMAGE}; and writes nothing but that one GRAIN_IMAGE=
# line into the root-owned file, so nothing else $GRAIN_USER writes
# there reaches the unit's environment (#2405). A request it refuses
# leaves the deployment on the image it was already running, and says
# so, rather than failing a start it could have let succeed.
write_image_ref_promoter() {
  install -d -m0755 "$(dirname "$IMAGE_REF_PROMOTER")"
  cat > "$IMAGE_REF_PROMOTER" <<PROMOTER
#!/usr/bin/env bash
# Written by scripts/setup.sh (write_image_ref_promoter). Run as root by
# grain-daemon.service before every start: copies the image an upgrade
# asked for out of ${IMAGE_REF_FILE} (written by ${GRAIN_USER}) into
# ${UNIT_IMAGE_REF_FILE} (read by the unit), after checking it names an
# image in ${GRAIN_IMAGE}. Never sources either file.
set -euo pipefail

GRAIN_OSS_IMAGE_REPO="\${GRAIN_OSS_IMAGE_REPO:-${GRAIN_OSS_IMAGE_REPO:-}}"
GRAIN_ALLOWED_IMAGE_REPOS="\${GRAIN_ALLOWED_IMAGE_REPOS:-${GRAIN_ALLOWED_IMAGE_REPOS:-}}"
$(declare -f image_ref_from)

ref="\$(image_ref_from "${IMAGE_REF_FILE}" "${GRAIN_IMAGE}")"
if [ -z "\$ref" ]; then
  echo "promote-image-ref: ${IMAGE_REF_FILE} names no image in ${GRAIN_IMAGE}; staying on what ${UNIT_IMAGE_REF_FILE} says" >&2
  exit 0
fi
install -d -m0755 "${UNIT_IMAGE_REF_DIR}"
tmp="\$(mktemp "${UNIT_IMAGE_REF_FILE}.XXXXXX")"
printf 'GRAIN_IMAGE=%s\n' "\$ref" > "\$tmp"
chmod 0644 "\$tmp"
mv -fT "\$tmp" "${UNIT_IMAGE_REF_FILE}"
PROMOTER
  chmod 0755 "$IMAGE_REF_PROMOTER"
}

# write_cli_profile points this host's `grain` CLI at this host's own
# daemon, and at that daemon's own data directory.
#
# The CLI talks to the daemon over REST and defaults to
# http://127.0.0.1:4242 (cmd/grain/main.go's defaultServerURL), which is
# only right while GRAIN_UI_ADDR keeps the default port -- one setting it to
# anything else made every invocation on its own host need an explicit
# -server, forever, with a connection refused as the only hint.
#
# The bind address is not reusable as-is: it is what the daemon listens
# on, so a deployment behind a load balancer or a tunnel binds 0.0.0.0,
# which is not an address to connect *to*. Only the port carries over;
# the host is always loopback, since this file is for shells on the same
# machine.
#
# GRAIN_DATA_DIR is the same favour for `grain state` and `grain secrets`,
# which read this deployment's files rather than its API and default
# their -data-dir to it (grain/task-303). It is exported even when the
# port cannot be worked out: the two are unrelated, and a shell that
# cannot be told where the daemon listens can still be told where its
# state lives.
write_cli_profile() {
  write_cli_env
  local port="${GRAIN_UI_ADDR##*:}"
  local server=""
  if [ -z "$port" ] || [ "$port" = "$GRAIN_UI_ADDR" ]; then
    log "  GRAIN_UI_ADDR ($GRAIN_UI_ADDR) has no port; /etc/profile.d/grain.sh will not set GRAIN_SERVER"
  else
    server="export GRAIN_SERVER=\"http://127.0.0.1:${port}\""
  fi
  cat > /etc/profile.d/grain.sh <<PROFILE
# Written by scripts/setup.sh. Points the grain CLI at the daemon this
# host runs, whose port comes from the -ui-addr it was started with, and
# at the data directory that daemon was started with. An explicit
# -server or -data-dir flag still overrides either.
${server}
export GRAIN_DATA_DIR="${GRAIN_DATA_DIR}"
PROFILE
  chmod 0644 /etc/profile.d/grain.sh
  if [ -n "$server" ]; then
    log "  grain CLI on this host defaults to http://127.0.0.1:${port} (/etc/profile.d/grain.sh)"
  fi
  log "  \`grain state\` and \`grain secrets\` on this host default to ${GRAIN_DATA_DIR} (/etc/profile.d/grain.sh)"
}

# write_cli_env writes ${CLI_ENV_FILE}: the same two facts as the profile
# script, for a CLI started from a shell that never read it -- the one
# that just ran this script, a cron job, `sudo -u`, a systemd unit. The
# grain CLI reads it itself (pkg/cli/cliconn.go): $GRAIN_SERVER and
# $GRAIN_DATA_DIR first, then this file, then its built-in default. It is
# parsed there as data, KEY=VALUE, never sourced (grain/task-3688).
#
# Written in both modes, so the host says where its daemon is the same
# way whichever mode installed it; in container mode the wrapper also
# bakes both into the container it starts, which cannot see /etc/grain.
write_cli_env() {
  local port="${GRAIN_UI_ADDR##*:}" server_line=""
  if [ -n "$port" ] && [ "$port" != "$GRAIN_UI_ADDR" ]; then
    server_line="GRAIN_SERVER=http://127.0.0.1:${port}"
  fi
  install -d -m0755 "$(dirname "$CLI_ENV_FILE")"
  local tmp
  tmp="$(mktemp "$CLI_ENV_FILE.XXXXXX")"
  cat > "$tmp" <<CLIENV
# Written by scripts/setup.sh (write_cli_env): where this host's grain
# daemon listens, and its data directory. Read by the grain CLI as data
# when GRAIN_SERVER / GRAIN_DATA_DIR are not set; -server and -data-dir
# still win over both.
${server_line}
GRAIN_DATA_DIR=${GRAIN_DATA_DIR}
CLIENV
  chmod 0644 "$tmp"
  mv -fT "$tmp" "$CLI_ENV_FILE"
  log "  grain CLI on this host reads ${CLI_ENV_FILE}${server_line:+ (${server_line#GRAIN_SERVER=})}, in any shell"
}

# ensure_admin_user makes $GRAIN_ADMIN_USER -- the person who ran this, by
# default -- a system admin, once the daemon is up (grain/task-3688). grain
# keeps no sign-in accounts or passwords (docs/single-tenant-reset.md,
# stage 3d): a person reaches a multi-tenant daemon through an identity
# proxy that names them, or from this host's loopback as its operator, so
# all there is to set up is the admin list, through the grain CLI as the
# local API token. It adds the account without removing anyone.
# With GRAIN_ADMIN_USER empty there is nothing to add: the daemon seeds
# its own account as admin itself.
#
# Never fatal: a deployment whose admin could not be set up is still a
# deployment, and the readiness report says what to run instead.
ADMIN_USER_STATUS=""
ADMIN_USER_NAME=""
admin_daemon_ready() {
  grain_cli -server "$1" list >/dev/null 2>&1
}

ensure_admin_user() {
  ADMIN_USER_NAME="${GRAIN_ADMIN_USER:-$GRAIN_USER}"
  if [ -z "${DAEMON_PREDATES_TENANCY:-}" ]; then
    # A single-tenant daemon keeps no accounts to sign in to.
    ADMIN_USER_STATUS="single"
    log "  single-tenant: no system admin to set up"
    return 0
  fi
  local port="${GRAIN_UI_ADDR##*:}" out
  local server="http://127.0.0.1:${port}"
  wait_with_progress "Waiting for grain-daemon on $server (first start can take a minute)" 60 ready admin_daemon_ready "$server" || true
  # A CLI from before cli.env (v1.9.0) looks for its daemon on its own
  # compiled-in port whatever this host says, so every command the report
  # prints has to name the server itself.
  CLI_SERVER_ARG=""
  if ! grain_cli list >/dev/null 2>&1; then
    CLI_SERVER_ARG="-server $server "
  fi
  if out="$(add_system_admin "$server" "$ADMIN_USER_NAME" 2>&1)"; then
    ADMIN_USER_STATUS="ok"
    printf '%s\n' "$out" | while IFS= read -r line; do log "  $line"; done
    return 0
  fi
  log "  could not make $ADMIN_USER_NAME a system admin: $out"
  ADMIN_USER_STATUS="failed"
}

# admin_accounts prints the deployment's system admins, one per line,
# out of `grain -json settings` against server $1. bash's own regex rather
# than a JSON tool, as granule_gcp_access_token does: the one field wanted
# is a flat array of account names.
admin_accounts() {
  local json list
  json="$(grain_cli -server "$1" -json settings)" || return 1
  [[ "$json" =~ \"adminAccounts\"[[:space:]]*:[[:space:]]*\[([^]]*)\] ]] || return 0
  list="${BASH_REMATCH[1]}"
  while [[ "$list" =~ \"([^\"]*)\"(.*) ]]; do
    printf '%s\n' "${BASH_REMATCH[1]}"
    list="${BASH_REMATCH[2]}"
  done
}

# add_system_admin adds account $2 to the system admins of the daemon at
# $1 with `grain settings -add-admin`, which takes nobody out, and says
# what it did. Through the CLI, as the local API token.
add_system_admin() {
  local server="$1" name="$2" admins
  case "$name" in
    *[!A-Za-z0-9._@+-]*) echo "refusing to add account name '$name' to the system admins: not a plain name" >&2; return 1 ;;
  esac
  admins="$(admin_accounts "$server")" || return 1
  if grep -qixF -- "$name" <<<"$admins"; then
    echo "$name was already a system admin"
    return 0
  fi
  grain_cli -server "$server" settings -add-admin "$name" >/dev/null || return 1
  echo "$name is now a system admin"
}

# --- 2b. grain hostd's binary ------------------------------------------
#
# The daemon and the CLI wrapper run the image; grain hostd cannot, since
# it is the one thing that runs as $GRAIN_USER on the bare machine. So
# the image's own grain is taken out of it onto this host, by a script
# this also writes (write_host_extractor) rather than inline, because the
# container unit runs it again as its own ExecStartPre: an upgrade pulls
# the next image and restarts the service (pkg/upgrade/image.go), and it
# is that restart which brings hostd's binary to the daemon's version.
#
# Host mode, since removed, took granule and grain's source out of the
# image the same way; a migrating install's copies are removed
# (install_cli_wrappers, write_systemd_units).
HOST_GRAIN_BIN=/usr/local/bin/grain
# pkg/cli/daemon.go's defaultSourceDir: where host mode put the source.
HOST_SOURCE_DIR=/usr/local/share/grain/src
HOST_EXTRACTOR=/usr/local/lib/grain/extract-host-binaries.sh
# The extractor's marker: the image ID it last took grain out of.
HOSTD_EXTRACTED_ID_FILE=/usr/local/lib/grain/.extracted-image-id-hostd
# The image's agent CLIs the extractor puts beside hostd's grain, in the
# same versioned directory: what the container-equivalent allowlist
# entries (cli claude-detached, cli codex-detached) run, and the kubectl
# a detached run's mcpserver reaches a kubernetes-granule sandbox with
# on a host that has none of its own. Each is the image's /usr/local/bin
# copy, dereferenced (claude's is a symlink into /opt/claude).
HOSTD_IMAGE_CLIS=(claude codex kubectl)
# The marker, in a version's directory, that the extractor took the
# image's CLIs out as well as its grain: a version an older extractor
# installed has none, and is not skipped over.
HOSTD_CLIS_MARKER=.clis
# Where the extractor links the image's kubectl on a host with none.
HOST_KUBECTL_BIN=/usr/local/bin/kubectl
# grain hostd's own copies, one directory per image ID it was taken out
# of, and the `current` link grain-hostd.service's ExecStart runs through.
# A new binary is a new directory and a renamed link, never a write over
# the file a running hostd executes; grain-hostd-upgrade.path sees the
# link change and drains hostd onto it (write_hostd_units).
HOSTD_VERSIONS_DIR=/usr/local/lib/grain/hostd
# pkg/cli/cliconn.go's defaultCLIEnvFile. Keep the two in step.
CLI_ENV_FILE="${CLI_ENV_FILE:-/etc/grain/cli.env}"

# install_hostd_binary puts the image's own grain at $HOST_GRAIN_BIN for
# grain hostd, through the extractor (granule stays the image wrapper,
# and the daemon reads its source inside its container).
# The container unit runs it again as an ExecStartPre, so an upgrade's
# restart brings the host's grain to the daemon's version: a host-run
# agy forks `grain mcpserver` from it.
install_hostd_binary() {
  rm -f "${HOST_GRAIN_BIN}.new"
  write_host_extractor
  step_start "Installing grain for grain hostd from $GRAIN_IMAGE_REF"
  "$HOST_EXTRACTOR" "$GRAIN_IMAGE_REF"
  step_done "$HOST_GRAIN_BIN is $GRAIN_IMAGE_REF's own"
}

# write_host_extractor writes the script that takes grain hostd's grain
# out of whatever image this deployment is currently pointed at.
#
# Run twice: once by install_hostd_binary, and once per start of
# grain-daemon.service, as an ExecStartPre -- which is what brings an
# upgrade's new binary to hostd: the UI's Upgrade button pulls the next
# image and rewrites the ref file (pkg/upgrade/image.go), and the restart
# it ends in is where it is installed.
#
# It never fails a start it could have let succeed: an image that cannot
# be opened, on a host whose local copy was pruned or whose registry is
# unreachable, leaves the binary already installed and says so. Only a
# host with no binary at all stops there.
write_host_extractor() {
  local id_file="$HOSTD_EXTRACTED_ID_FILE" extractor_dir
  extractor_dir="$(dirname "$HOST_EXTRACTOR")"
  install -d -m0755 "$extractor_dir"
  cat > "$HOST_EXTRACTOR" <<EXTRACTOR
#!/usr/bin/env bash
# Written by scripts/setup.sh (write_host_extractor). Installs the grain
# binary out of the deployment image this host is pointed at, for grain
# hostd beside the container-mode daemon. Run as root, by that script
# and by grain-daemon.service's own ExecStartPre before every start.
set -euo pipefail
# The image to install, named by the caller or read from this
# deployment's ref file. scripts/setup.sh names one, because the file
# still holds the tag of the *previous* run at the moment it installs;
# the unit's ExecStartPre names none, because that file is exactly what
# an upgrade has just rewritten.
#
# That file is ${GRAIN_USER}'s to write, and this script is root: so it
# is parsed as data and never sourced, and what it names is used only if
# it is an image in ${GRAIN_IMAGE} (image_ref_from, #2405). Anything else
# is said and ignored, and the image this host was set up with is
# installed instead. A valid request is also promoted into
# ${UNIT_IMAGE_REF_FILE}, as write_image_ref_promoter does, so a later setup.sh re-run (resolve_image_tag) keeps the upgraded
# channel or tag rather than reverting to what the previous setup.sh run
# wrote.
GRAIN_OSS_IMAGE_REPO="\${GRAIN_OSS_IMAGE_REPO:-${GRAIN_OSS_IMAGE_REPO:-}}"
GRAIN_ALLOWED_IMAGE_REPOS="\${GRAIN_ALLOWED_IMAGE_REPOS:-${GRAIN_ALLOWED_IMAGE_REPOS:-}}"
$(declare -f image_ref_from)
image="\${1:-}"
if [ -z "\$image" ]; then
  image="\$(image_ref_from "${IMAGE_REF_FILE}" "${GRAIN_IMAGE}")"
  if [ -z "\$image" ]; then
    if [ -e "${IMAGE_REF_FILE}" ]; then
      echo "extract-host-binaries: ${IMAGE_REF_FILE} names no image in ${GRAIN_IMAGE}; ignoring it and installing ${GRAIN_IMAGE_REF}" >&2
    fi
    image="${GRAIN_IMAGE_REF}"
  else
    install -d -m0755 "${UNIT_IMAGE_REF_DIR}"
    tmp="\$(mktemp "${UNIT_IMAGE_REF_FILE}.XXXXXX")"
    printf 'GRAIN_IMAGE=%s\n' "\$image" > "\$tmp"
    chmod 0644 "\$tmp"
    mv -fT "\$tmp" "${UNIT_IMAGE_REF_FILE}"
  fi
fi

# Skip re-extracting when the image ID matches what install_hostd_binary
# (or the previous service start) already extracted and the binary is
# still in place: a redundant docker create + cp otherwise.
extracted_id_file="${EXTRACTED_ID_FILE:-$id_file}"
want_id="\$($DOCKER_BIN image inspect --format '{{.Id}}' "\$image" 2>/dev/null || true)"
if [ -n "\$want_id" ] && [ -r "\$extracted_id_file" ] \
   && [ "\$(cat "\$extracted_id_file" 2>/dev/null || true)" = "\$want_id" ] \
   && [ -x "${HOST_GRAIN_BIN}" ] && [ -x "${HOSTD_VERSIONS_DIR}/current/grain" ] \
   && [ -e "${HOSTD_VERSIONS_DIR}/current/${HOSTD_CLIS_MARKER:-.clis}" ]; then
  exit 0
fi

# install_hostd_version copies ${HOST_GRAIN_BIN} to
# ${HOSTD_VERSIONS_DIR}/<version>/grain and points the current link at
# it by rename, so grain-hostd-upgrade.path sees one change and a running
# hostd's own file is never written over. Versions other than the new
# one and the one it replaces are removed: a hostd still running one
# keeps its inode until it has drained.
install_hostd_version() {
  # (Its closing brace is indented: the tests read setup.sh's own
  # functions up to the first one that is not.)
  local version="\$1" stage="\${2:-}" dir="${HOSTD_VERSIONS_DIR}" previous="" name
  # Held until current has moved: the new version's directory is itself
  # a change grain-hostd-upgrade.path sees, and the upgrade script it
  # starts takes this lock before reading current. Without it that run
  # could read the old link, find hostd already on it and exit, while the
  # rename's own trigger and the start below merge into the same run and
  # are lost: hostd would stay on the old binary. Beside the versions
  # directory rather than in it, so taking it is not a change there.
  install -d -m0755 "\$dir"
  if command -v flock >/dev/null 2>&1; then
    exec 9>"\$dir.lock"
    flock 9
  fi
  # A version already here -- the one a rollback returns to -- is the
  # same image's grain, and may be the file a hostd is running: it is
  # pointed at again, never written over.
  if [ ! -x "\$dir/\$version/grain" ]; then
    install -d -m0755 "\$dir/\$version"
    cp "${HOST_GRAIN_BIN}" "\$dir/\$version/grain.new"
    chmod 0755 "\$dir/\$version/grain.new"
    mv -f "\$dir/\$version/grain.new" "\$dir/\$version/grain"
  fi
  # The image's CLIs, the same way: each one only into a version that
  # lacks it, since a detached run adopted across an upgrade may be
  # executing the copy already here.
  if [ -n "\$stage" ]; then
    for name in ${HOSTD_IMAGE_CLIS[*]:-claude codex kubectl}; do
      if [ -f "\$stage/\$name" ] && [ ! -x "\$dir/\$version/\$name" ]; then
        cp "\$stage/\$name" "\$dir/\$version/\$name.new"
        chmod 0755 "\$dir/\$version/\$name.new"
        mv -f "\$dir/\$version/\$name.new" "\$dir/\$version/\$name"
      fi
    done
    touch "\$dir/\$version/${HOSTD_CLIS_MARKER:-.clis}"
  fi
  previous="\$(readlink "\$dir/current" 2>/dev/null || true)"
  if [ "\$previous" != "\$version" ]; then
    ln -sfn "\$version" "\$dir/current.new"
    mv -fT "\$dir/current.new" "\$dir/current"
    # Drain hostd onto it now. grain-hostd-upgrade.path watches the
    # versions directory too, but this does not depend on it: --no-block,
    # because this runs in grain-daemon.service's own ExecStartPre.
    if command -v systemctl >/dev/null 2>&1 && [ -e "${HOSTD_UPGRADE_UNIT:-}" ]; then
      systemctl start --no-block grain-hostd-upgrade.service >/dev/null 2>&1 || true
    fi
  fi
  local d
  for d in "\$dir"/*/; do
    d="\$(basename "\$d")"
    [ "\$d" = "\$version" ] || [ "\$d" = "\$previous" ] || [ "\$d" = current ] || rm -rf "\${dir:?}/\$d"
  done
  exec 9>&-
  }

if ! cid="\$($DOCKER_BIN create "\$image" 2>/dev/null)"; then
  if [ -x "${HOST_GRAIN_BIN}" ]; then
    echo "extract-host-binaries: cannot open \$image -- starting the binary already installed at ${HOST_GRAIN_BIN}" >&2
    if [ ! -x "${HOSTD_VERSIONS_DIR}/current/grain" ]; then
      install_hostd_version "unversioned"
    fi
    exit 0
  fi
  echo "extract-host-binaries: cannot open \$image, and this host has no ${HOST_GRAIN_BIN} to fall back to" >&2
  exit 1
fi
stage="\$(mktemp -d "${extractor_dir}/.extract-stage.XXXXXX")"
trap '$DOCKER_BIN rm -f "\$cid" >/dev/null 2>&1 || true; rm -rf "\$stage"' EXIT

# Copied to a neighbouring name and renamed into place: a rename is
# atomic and, unlike writing through the path, does not disturb a binary
# something on this host is executing right now.
$DOCKER_BIN cp "\$cid:/usr/local/bin/grain" "${HOST_GRAIN_BIN}.new"
chmod 0755 "${HOST_GRAIN_BIN}.new"
mv "${HOST_GRAIN_BIN}.new" "${HOST_GRAIN_BIN}"

# The image's agent CLIs, dereferenced (-L): /usr/local/bin/claude is a
# symlink into /opt/claude, codex's into /opt/codex. An image built
# without one (INSTALL_CLAUDE_CLI=0) leaves it out, and its detached
# entry then offers a path that does not exist, which a run is refused
# for rather than started on.
for name in ${HOSTD_IMAGE_CLIS[*]:-claude codex kubectl}; do
  $DOCKER_BIN cp -L "\$cid:/usr/local/bin/\$name" "\$stage/\$name" >/dev/null 2>&1 || rm -f "\$stage/\$name"
done

version="\${want_id#sha256:}"
version="\${version:0:12}"
install_hostd_version "\${version:-unknown-\$(date +%s)}" "\$stage"

# kubectl, for a detached run's mcpserver reaching a kubernetes-granule
# sandbox, on a host that has none of its own: a link onto the current
# version's, which this keeps while it is the only one. A kubectl the host
# already has is never replaced.
host_kubectl="${HOST_KUBECTL_BIN:-/usr/local/bin/kubectl}"
if [ -L "\$host_kubectl" ] && [ "\$(readlink "\$host_kubectl")" = "${HOSTD_VERSIONS_DIR}/current/kubectl" ]; then
  :
elif [ ! -e "\$host_kubectl" ] && ! PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin command -v kubectl >/dev/null 2>&1 \
     && [ -x "${HOSTD_VERSIONS_DIR}/current/kubectl" ]; then
  ln -sfn "${HOSTD_VERSIONS_DIR}/current/kubectl" "\$host_kubectl.new"
  mv -fT "\$host_kubectl.new" "\$host_kubectl"
fi

if [ -n "\$want_id" ]; then
  printf '%s\n' "\$want_id" > "\$extracted_id_file"
fi
EXTRACTOR
  chmod 0755 "$HOST_EXTRACTOR"
}

# --- 3. the unprivileged account grain runs as --------------------------

# ensure_user creates $GRAIN_USER.
#
# This user used to also be added to the systemd-journal group here, back
# when the daemon ran directly on the host as $GRAIN_USER and journalctl
# checked the invoking process's own supplementary groups. Now that
# grain-daemon.service's ExecStart is a `docker run --user uid:gid`
# (bwsalmon/agents#645), the process journalctl runs as inside the
# container never sees this host account's group memberships at all --
# `--user` there is two bare numbers, not a login, and Docker does not
# consult this host's /etc/group for it. Granting the container
# permission to read the journal files docker_run_args bind-mounts in is
# instead done the same way docker socket access already is
# (grant_docker_group's own comment): a --group-add of the group's numeric
# GID on the `docker run` itself (docker_run_args, below).
# --- the GCE metadata server ----------------------------------------------
#
# The metadata server answers anyone who can reach it, and what it hands
# out includes a token for this instance's service account -- on a
# terraform/gcp deployment, the identity that reads its secrets. A
# NetworkOpen granule guest and a -container-sandboxes container both
# reach it through docker's bridge unless something stops them; the host
# nft chain only covers NetworkNone guests.
#
# terraform/gcp/files/deploy.sh has installed this block since #2376.
# This is the same rule, script and unit -- byte for byte, which
# tests/deploy holds both files to -- so a GCE instance set up by hand,
# by a wrapper installer or in host mode gets what a terraform deploy
# does, and a terraform deploy that runs both writes the same files
# twice. It is setup.sh's job rather than the daemon's because only
# this script is root: a daemon in a container, or one running as
# $GRAIN_USER, cannot write raw PREROUTING, and must not be able to.
#
# raw PREROUTING sees every packet arriving on an interface -- a bridge
# included -- and nothing the host sends itself, so this script's own
# token read (granule_gcp_access_token, --network host) and the
# --network host daemon keep working.
metadata_block_wanted() {
  case "$GRAIN_BLOCK_METADATA" in
    1) return 0 ;;
    0) return 1 ;;
  esac
  # GCE's own documented test for "am I on GCE": it is in DMI, so it
  # needs no network and no metadata request to answer.
  local dmi="${DMI_ID_DIR:-/sys/class/dmi/id}"
  grep -qx 'Google Compute Engine' "$dmi/product_name" 2>/dev/null ||
    grep -qx 'Google' "$dmi/sys_vendor" 2>/dev/null
}

readonly METADATA_BLOCK="/usr/local/sbin/grain-block-metadata"
readonly METADATA_BLOCK_UNIT="/etc/systemd/system/grain-block-metadata.service"
ensure_metadata_block() {
  if ! metadata_block_wanted; then
    if [ "$GRAIN_BLOCK_METADATA" = "0" ]; then
      log "GRAIN_BLOCK_METADATA=0: containers and sandbox guests can reach the metadata server (169.254.169.254), if this host has one"
    fi
    return 0
  fi
  if ! command -v iptables >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
    log "installing iptables, to block containers and sandbox guests from the GCE metadata server"
    apt-get update -qq || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends iptables || true
  fi

  install -d -m 0755 "$(dirname "$METADATA_BLOCK")"
  cat > "$METADATA_BLOCK.new" <<'SCRIPT'
#!/bin/sh
# Written by grain's deploy.sh or setup.sh (ensure_metadata_block):
# drops every packet that arrives on an interface addressed to the GCE
# metadata server, so no container or sandbox guest can read this
# instance's metadata. The host's own reads go out through OUTPUT.
set -e
PATH=/usr/sbin:/sbin:/usr/bin:/bin
iptables -w -t raw -C PREROUTING -d 169.254.169.254/32 -j DROP 2>/dev/null ||
  iptables -w -t raw -I PREROUTING 1 -d 169.254.169.254/32 -j DROP
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -w -t raw -C PREROUTING -d fd20:ce::254/128 -j DROP 2>/dev/null ||
    ip6tables -w -t raw -I PREROUTING 1 -d fd20:ce::254/128 -j DROP ||
    echo "grain-block-metadata: could not install the IPv6 metadata block" >&2
fi
SCRIPT
  install -m 0755 "$METADATA_BLOCK.new" "$METADATA_BLOCK"
  rm -f "$METADATA_BLOCK.new"

  cat > "$METADATA_BLOCK_UNIT.new" <<UNIT
[Unit]
Description=Keep containers and sandbox guests away from the GCE metadata server
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target docker.service containerd.service
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$METADATA_BLOCK

[Install]
WantedBy=multi-user.target docker.service
UNIT
  if ! cmp -s "$METADATA_BLOCK_UNIT.new" "$METADATA_BLOCK_UNIT"; then
    mv "$METADATA_BLOCK_UNIT.new" "$METADATA_BLOCK_UNIT"
    systemctl daemon-reload
  else
    rm -f "$METADATA_BLOCK_UNIT.new"
  fi
  systemctl enable grain-block-metadata.service >/dev/null 2>&1 || true

  # Run directly as well: a oneshot that is already active
  # (RemainAfterExit) is not re-run by `start`, and the rule may have
  # been removed since -- the script is idempotent. Failing here is
  # deliberate, as it is in deploy.sh: a GCE host that cannot fence the
  # metadata server off should not start sandboxes that can read it.
  if ! "$METADATA_BLOCK"; then
    echo "setup.sh: could not block containers from the GCE metadata server (iptables raw PREROUTING); refusing to start sandboxes that could take this instance's service account token. Install iptables and re-run, or set GRAIN_BLOCK_METADATA=0 to accept that they can." >&2
    exit 1
  fi
  systemctl start grain-block-metadata.service >/dev/null 2>&1 || true
  log "containers and sandbox guests are blocked from the GCE metadata server (169.254.169.254)"
}

ensure_user() {
  if ! id -u "$GRAIN_USER" >/dev/null 2>&1; then
    log "Creating system user $GRAIN_USER"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$GRAIN_USER"
  fi
  # Asked of the account rather than assumed from its name -- see
  # GRAIN_GROUP's own comment. For the account just created above the two
  # are the same string; for one an operator named they need not be.
  GRAIN_GROUP="$(id -gn "$GRAIN_USER")"
  if [ "$GRAIN_GROUP" != "$GRAIN_USER" ]; then
    # What a shared primary group actually reaches, and no more: the data
    # directory is 0750, so its members can read this deployment's store.
    # The secrets directory inside it is 0700 and the control channel that
    # reboots this host is too (setup_data_dir, write_control_units), so
    # neither is theirs.
    log "$GRAIN_USER's primary group is $GRAIN_GROUP: everything under $GRAIN_DATA_DIR is owned by it,"
    log "  and $GRAIN_DATA_DIR is group-readable -- so anyone in $GRAIN_GROUP can read this"
    log "  deployment's task store. Its secrets and its control channel are the account's alone."
  fi
}

# --- 4. the control channel: acting on the host from inside the container -
#
# A handful of things in the UI ask grain to do something to the machine
# it runs on: "reboot host" and "shut down host" (pkg/ui/host.go,
# bwsalmon/agents#395 and grain/task-40), the restart -- of the grain
# service alone, not the machine -- that both the Upgrade button
# (bwsalmon/agents#396) and the System pane's own "restart grain"
# (grain/task-40) end in. The reboot and the restart used to be a
# NOPASSWD sudoers line each, granting $GRAIN_USER exactly one command --
# and neither can work from inside a container, where `systemctl` reaches
# no systemd that matters and sudo grants nothing that crosses the
# boundary.
#
# So the container asks instead of acting. The daemon touches a file
# under $GRAIN_DATA_DIR/control -- a directory it already has mounted --
# and a systemd .path unit out here notices and runs the real command as
# root. write_systemd_units points -reboot-cmd, -shutdown-cmd,
# -restart-cmd and -upgrade-restart-cmd at exactly those files -- three
# files for those four flags, since the two restart flags name the same
# action and so share one.
#
# What those units grant is the same kind of thing the two sudoers files
# granted, by a mechanism with less reach: there is no sudo rule left at
# all, so nothing can be widened by editing one, and the only thing the
# daemon can cause is whichever command those units name.
# Anything able to write into $GRAIN_DATA_DIR/control can trigger them --
# which is $GRAIN_USER and root, exactly who could invoke the old sudo
# rules, and exactly who that directory's 0700 lets in
# (write_control_units).
#
# There is no exception to that sentence any more. A fourth unit
# (grain-shell.path, grain/task-13) ran whatever command it was handed
# rather than one this file names, for the UI's Root shell tab; it is
# gone end to end (grain/task-193), and remove_root_shell_units below
# takes it off a host an earlier run installed it on.
#
# Each service removes the request file before acting, so a request is
# consumed once. PathModified (rather than PathExists) is what watches
# them: a leftover file must not turn into a reboot the next time this
# host boots.
CONTROL_DIR="$GRAIN_DATA_DIR/control"

write_control_units() {
  log "Writing grain-reboot.path/.service, grain-shutdown.path/.service and grain-restart.path/.service"
  # 0700, not 0770: nothing that uses this directory needs the group bit.
  # The daemon is its owner -- it touches the three request files, as
  # $GRAIN_USER here and as that same uid inside the container there --
  # and everything on the other end of the exchange is root, which no
  # directory mode applies to: the .path services below remove each
  # request.
  #
  # The group bit was inherited rather than needed, and what it granted was
  # the whole control channel -- a reboot, a poweroff, a restart --
  # to every account in $GRAIN_GROUP. That is nobody at all for the private
  # group of the account this script creates, and a real widening for a
  # GRAIN_USER whose primary group is shared. The mode is re-applied on
  # every run, so a deployment installed before this tightens on its next
  # one with nothing to migrate.
  grain_dir 0700 "$CONTROL_DIR"
  # A host deployed before bwsalmon/agents#645 still carries the two
  # sudoers drop-ins these units replace. Nothing invokes them any more,
  # but a standing NOPASSWD grant that no longer has a reason to exist is
  # exactly the kind of thing nobody notices again -- so a re-run removes
  # them rather than leaving them behind.
  rm -f /etc/sudoers.d/grain-daemon-reboot /etc/sudoers.d/grain-daemon-upgrade

  cat > /etc/systemd/system/grain-reboot.path <<UNIT
[Unit]
Description=Watch for grain's reboot-host request

[Path]
PathModified=${CONTROL_DIR}/reboot
Unit=grain-reboot.service

[Install]
WantedBy=multi-user.target
UNIT

  cat > /etc/systemd/system/grain-reboot.service <<UNIT
[Unit]
Description=Reboot this host, at the grain daemon's request

[Service]
Type=oneshot
ExecStart=/usr/bin/setpriv --reuid=${GRAIN_USER} --regid=${GRAIN_GROUP} --clear-groups /bin/rm -f ${CONTROL_DIR}/reboot
ExecStart=/usr/bin/systemctl reboot
UNIT

  # The other direction from the reboot above (grain/task-40): the UI's
  # "shut down host" button, which leaves the machine off rather than
  # bringing it back. Its own file and its own unit, because each unit
  # here runs exactly one fixed command -- that is what keeps this
  # channel narrower than the sudoers rules it replaced.
  cat > /etc/systemd/system/grain-shutdown.path <<UNIT
[Unit]
Description=Watch for grain's shut-down-host request

[Path]
PathModified=${CONTROL_DIR}/shutdown
Unit=grain-shutdown.service

[Install]
WantedBy=multi-user.target
UNIT

  cat > /etc/systemd/system/grain-shutdown.service <<UNIT
[Unit]
Description=Power this host off, at the grain daemon's request

[Service]
Type=oneshot
ExecStart=/usr/bin/setpriv --reuid=${GRAIN_USER} --regid=${GRAIN_GROUP} --clear-groups /bin/rm -f ${CONTROL_DIR}/shutdown
ExecStart=/usr/bin/systemctl poweroff
UNIT

  # Watched for two buttons rather than one since grain/task-40: the
  # restart at the end of an Upgrade, and the System pane's own "restart
  # grain" -- the same action (bring this service down and back up), so
  # the same request file and the same unit.
  cat > /etc/systemd/system/grain-restart.path <<UNIT
[Unit]
Description=Watch for grain's restart request (the UI's Upgrade and Restart buttons)

[Path]
PathModified=${CONTROL_DIR}/restart
Unit=grain-restart.service

[Install]
WantedBy=multi-user.target
UNIT

  cat > /etc/systemd/system/grain-restart.service <<UNIT
[Unit]
Description=Restart grain-daemon.service, at the grain daemon's request

[Service]
Type=oneshot
ExecStart=/usr/bin/setpriv --reuid=${GRAIN_USER} --regid=${GRAIN_GROUP} --clear-groups /bin/rm -f ${CONTROL_DIR}/restart
ExecStart=/usr/bin/systemctl restart grain-daemon.service
UNIT

  remove_root_shell_units
}

# ROOT_SHELL_RESPONDER is where an earlier version of this script
# installed the host-side half of the UI's root shell (grain/task-13):
# a script grain-shell.service ran, as root, on whatever command the
# daemon wrote to $CONTROL_DIR/shell. The name is kept only so
# remove_root_shell_units below can take it away again.
ROOT_SHELL_RESPONDER=/usr/local/lib/grain/root-shell.sh

# remove_root_shell_units takes back what those earlier runs granted
# (grain/task-193). The feature is gone from the daemon and from the UI,
# but a host deployed before that still has the .path unit sitting on it
# watching $CONTROL_DIR/shell -- a standing "run anything as root"
# trigger with nothing left that is supposed to pull it, which is
# precisely the kind of grant nobody notices again. So a re-run removes
# it, the same way write_control_units above removes the sudoers
# drop-ins it replaced.
remove_root_shell_units() {
  if [ -e /etc/systemd/system/grain-shell.path ] || [ -e "$ROOT_SHELL_RESPONDER" ]; then
    log "Removing grain-shell.path/.service (the UI's root shell is gone)"
    systemctl disable --now grain-shell.path >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/grain-shell.path /etc/systemd/system/grain-shell.service
    rm -f "$ROOT_SHELL_RESPONDER"
  fi
  # Whatever the last exchange left behind goes too: a command an
  # operator ran through that pane, and what it printed, are as likely as
  # not to hold something worth not leaving on disk.
  as_grain rm -f "$CONTROL_DIR/shell" "$CONTROL_DIR/shell.out" "$CONTROL_DIR/shell.status"
}

# grant_docker_group adds $GRAIN_USER to the docker group if it exists.
#
# Nothing in the *container* needs this: grain-daemon.service's own
# `docker run` is executed by systemd as root, and the socket the
# container itself gets is reached through a --group-add of the docker
# gid (docker_run_args). This is for the account an operator uses on this
# host -- `grain`/`granule` are wrappers around `docker run` now
# (install_cli_wrappers), so a shell as $GRAIN_USER needs to be able to
# talk to the daemon for either of them to work at all.
grant_docker_group() {
  if getent group docker >/dev/null 2>&1; then
    usermod -aG docker "$GRAIN_USER"
  fi
}

# --- granule sandboxing ---------------------------------------------------
#
# Everything below is skipped, with a logged reason, unless
# GRAIN_GRANULE_ENABLE=1 -- turning it off after a prior successful run
# leaves whatever it already did in place (a fetched guest image, a
# pulled OCI image, group memberships); it just stops write_systemd_units
# from wiring up the -granule-* flags that would use them. A failure in
# any step below goes through granule_unavailable and selects the isolated
# local-container backend whose image setup has already prepared.

# granule_unavailable is what every step below calls when a prerequisite
# for granule sandboxing turns out not to be ready on this host: no
# /dev/kvm, a sandbox image that cannot be resolved or pulled, no address
# a guest could reach this host's git proxy through.
#
# If granule cannot run, the local-container backend takes its place.
granule_unavailable() {
  local reason="$1"
  log "$reason -- dispatching into per-run Docker containers instead"
  GRAIN_GRANULE_ENABLE=0
  GRAIN_CONTAINER_SANDBOXES=1
}

# granule_gcp_access_token fetches a short-lived OAuth2 access token for
# this host's own attached service account from the metadata server --
# enough to authenticate docker to an Artifact Registry the sandbox image
# might live in, without installing the whole gcloud SDK just for that.
# iam.tf's own host_reads_granule_registry is what makes the token itself
# actually able to. Not needed at all for the default, GHCR-hosted image
# -- pull_image's own registry_login has already covered that host.
#
# curl and the JSON parse were both host tools until recently -- curl,
# and a jq this script installed for these two lines and nothing else.
# curl runs inside the deployment image now (image_run), and the one
# field wanted out of the response is read with bash's own regex match.
# That match is also what keeps a response *without* the field from
# travelling on as the literal string "null" in an Authorization header:
# no match is a non-zero return, exactly as `jq -er '.access_token //
# empty'` was.
#
# The metadata server is addressed by IP rather than as
# metadata.google.internal: that name resolves on a GCE host through an
# /etc/hosts entry, and a container gets docker's own /etc/hosts even
# under --network host, so the name is not reliably there. The address
# is the documented one that entry points at.
#
# A host that does have curl asks from the host first: registry_login
# needs this token before $GRAIN_IMAGE_REF has been pulled, when there
# is no image yet to borrow curl out of. That curl is optional, not a
# requirement -- with none, or with no answer from it, the image's own
# curl is asked as before.
granule_gcp_access_token() {
  local json="" url="http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
  if command -v curl >/dev/null 2>&1; then
    json="$(as_grain curl -fsS --connect-timeout 2 -H "Metadata-Flavor: Google" "$url" 2>/dev/null || true)"
  fi
  if [ -z "$json" ]; then
    json="$(image_run --network host --entrypoint curl -- \
      -fsS -H "Metadata-Flavor: Google" "$url" \
      2>/dev/null)" || return 1
  fi
  [[ "$json" =~ \"access_token\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1]}"
}

ensure_granule_images() {
  if [ "$GRAIN_GRANULE_ENABLE" != "1" ]; then
    return
  fi

  # One artifact, and nothing built here.
  #
  # This used to be two decisions. The sandbox *container* was pulled,
  # and the guest *disk* was built on every host -- not an oversight but
  # a consequence: the guest build baked this deployment's own SSH public
  # key into the image, so no generic published disk could exist, and
  # GRAIN_GRANULE_IMAGE_BUCKET was there for an operator who built one
  # centrally and shared it across a fleet on one keypair.
  #
  # Both halves of that are gone. granule generates the exec keypair per
  # VM boot, so nothing deployment-specific reaches the disk; and the
  # guest is now built from the repository's Granulefile and packed onto
  # a granule image (scripts/release-images.sh), which produces an image
  # that is itself runnable. The
  # guest and the container it runs in are the same artifact, published
  # per commit by CI, and this host pulls it.
  ensure_granule_oci_image
}

# ensure_granule_oci_image resolves and pulls the sandbox image -- the
# granule image carrying grain's own provisioned guest, which is both the
# container a VM runs in and the guest it boots.
#
# Unset (the default), GRAIN_GRANULE_OCI_IMAGE comes from the grain image
# itself: `grain sandbox-image` prints the reference stamped into this
# build at build time (pkg/cli/sandboximage.go), which is the sandbox
# built from the very same commit. That is what makes the two halves of a
# granule deployment impossible to get out of step -- including on a
# rollback, where an older grain names its own older sandbox rather than
# whatever is newest.
#
# A pull that fails takes the same route every other granule step here
# takes when its prerequisite isn't ready: granule_unavailable, which
# falls back to host directories where the operator has said that is
# acceptable and fails the run where they have not.
ensure_granule_oci_image() {
  if [ -z "$GRAIN_GRANULE_OCI_IMAGE" ]; then
    # `|| true` so the guard below is the thing that reports a CLI that
    # could not answer -- without it `set -e` aborts the whole deploy on
    # the assignment, with nothing said about which step failed or why.
    GRAIN_GRANULE_OCI_IMAGE="$(grain_cli sandbox-image 2>/dev/null | tr -d '\r' | head -n1 || true)"
    if [ -z "$GRAIN_GRANULE_OCI_IMAGE" ]; then
      granule_unavailable "could not ask $GRAIN_IMAGE_REF which sandbox image it expects, and GRAIN_GRANULE_OCI_IMAGE names none"
      return
    fi
    log "sandbox container: $GRAIN_GRANULE_OCI_IMAGE (stamped into $GRAIN_IMAGE_REF at build time)"
  else
    log "sandbox container: $GRAIN_GRANULE_OCI_IMAGE (GRAIN_GRANULE_OCI_IMAGE overrides the built-in default)"
  fi

  # An Artifact Registry in this host's own project needs the metadata
  # server's token; GHCR's public package needs nothing, and a *private*
  # one is already covered -- pull_image's own registry_login ran earlier
  # against the same ghcr.io host with GRAIN_IMAGE_PULL_TOKEN, and docker
  # keeps that session for every later pull from it. So this is attempted
  # only for Artifact Registry, and is never fatal on its own: the pull
  # below is what actually decides.
  local registry_host="${GRAIN_GRANULE_OCI_IMAGE%%/*}" gcp_token
  if [[ "$GRAIN_GRANULE_OCI_IMAGE" == */* ]] && google_registry_host "$registry_host"; then
    gcp_token="$(gcp_access_token || true)"
    if [ -z "$gcp_token" ] \
       || ! printf '%s' "$gcp_token" | docker login -u oauth2accesstoken --password-stdin "https://${registry_host}" >/dev/null 2>&1; then
      log "  could not authenticate docker to ${registry_host}; trying the pull anyway"
    fi
  fi

  log "Pulling the sandbox container $GRAIN_GRANULE_OCI_IMAGE"
  if ! docker pull "$GRAIN_GRANULE_OCI_IMAGE"; then
    if docker image inspect "$GRAIN_GRANULE_OCI_IMAGE" >/dev/null 2>&1; then
      log "  pull failed, but this host already has it locally -- using that"
    else
      granule_unavailable "could not pull $GRAIN_GRANULE_OCI_IMAGE and this host has no local copy"
      return
    fi
  fi
}

# granule itself is no longer built or installed here: it ships inside
# the deployment image (Dockerfile builds it from the same vendored
# third_party/granule this checkout carries), which is where pkg/granule
# execs it from, and install_cli_wrappers puts a wrapper for that same
# copy on this host's PATH for granule-diag.sh and an operator's own use.
#
# That is what bwsalmon/agents#645 replaced ensure_granule with. It had
# to build the binary in the grain-builder image `make container-build`
# left behind, cache it by the vendored tree's git object id, and rebuild
# it whenever that changed -- all of it work to keep a host-installed
# binary in step with the vendored source, which an image built from that
# source in one CI job cannot fall out of step with in the first place.

# ensure_granule_kvm_access grants $GRAIN_USER /dev/kvm (for the nested
# cloud-hypervisor guest itself) and the docker group (for the docker
# granule backend's own `docker run`) -- the same grant grant_docker_group
# gives an operator's own shell, and safe to grant twice: usermod -aG
# only ever adds.
#
# It used to also create and chown the host directory granule put each
# VM's writable disk overlay in. There is no such directory any more: the
# overlay is created inside the VM's own container, against the disk the
# sandbox image carries (bwsalmon/granule#37). What is left below is
# granule's state directory, which it still writes out here.
ensure_granule_kvm_access() {
  if [ "$GRAIN_GRANULE_ENABLE" != "1" ]; then
    return
  fi
  if [ ! -e /dev/kvm ]; then
    granule_unavailable "granule sandboxing needs /dev/kvm and this host has none -- terraform/gcp's enable_nested_virtualization must be on and machine_type must support it (see that variable's own doc)"
    return
  fi
  if getent group kvm >/dev/null 2>&1; then
    usermod -aG kvm "$GRAIN_USER"
  fi
  grant_docker_group
  # granule's own defaultStateDir (third_party/granule/pkg/cli/
  # vm.go) -- where it records each VM it creates. Never
  # overridden by a -granule-create-arg -state-dir below, so this is the
  # exact path granule -- run unprivileged, as $GRAIN_USER -- actually
  # tries to create on its first "vm create": without this, that mkdir
  # fails closed with "permission denied" and grain-daemon.service dies
  # on its very first dispatched task.
  install -d -m0755 -o "$GRAIN_USER" -g "$GRAIN_GROUP" /var/lib/granule/vms
}

# ensure_granule_nested_virt makes this host's KVM pass hardware
# virtualization through to the sandboxes it boots, so a dispatched task
# can run VMs (docker's own runtimes, kind, a nested cloud-hypervisor) of
# its own.
#
# Nothing has to be configured on either side to get that in the ordinary
# case, which is worth stating before the code below suggests otherwise:
# kvm_intel and kvm_amd both ship nested=Y, and cloud-hypervisor leaves
# the CPU's vmx/svm flag in the guest's CPUID unless asked for "--cpus
# nested=off" (v53.0, which third_party/granule pins). The guest-side half
# -- the sandbox account being in the "kvm" group -- is baked into the
# image by the repository's Granulefile.
#
# What this does is the case where the default was overridden: a distro
# or an operator that set nested=0 in a modprobe drop-in, where every
# sandbox gets a CPU with no virtualization flag and nothing in the guest
# can say why. It writes the drop-in that turns it back on -- which takes
# effect on the next module load, i.e. after a reboot -- and reloads the
# module now if nothing is using it. It never unloads a module something
# holds: that would be pulling KVM out from under a running VM, and this
# script is the updater as well as the installer, so it runs against
# hosts with live sandboxes on them.
#
# The reading itself is reported live on the sandbox health pane
# (pkg/sysstat's NestedVirtualization, beside each sandbox's own
# guest-side state), so this logging one line at install time is a
# convenience rather than the only account of it.
ensure_granule_nested_virt() {
  if [ "$GRAIN_GRANULE_ENABLE" != "1" ]; then
    return
  fi
  local module param value
  for module in kvm_intel kvm_amd; do
    param="/sys/module/${module}/parameters/nested"
    [ -r "$param" ] || continue
    value="$(cat "$param")"
    case "$value" in
      Y | y | 1)
        log "nested virtualization is on for ${module} (nested=${value}), so sandboxes can run VMs of their own"
        return
        ;;
    esac
    log "nested virtualization is off for ${module} (nested=${value}); turning it on so sandboxes can run VMs of their own"
    install -d -m0755 /etc/modprobe.d
    echo "options ${module} nested=1" > "/etc/modprobe.d/kvm-nested-grain.conf"
    # `modprobe -r` refuses a module in use and says so; that is the
    # answer here rather than a failure, so the reload is attempted and
    # its outcome reported instead of being allowed to end the script.
    if modprobe -r "$module" 2>/dev/null && modprobe "$module"; then
      log "reloaded ${module} with nesting on"
    else
      log "${module} is in use, so it keeps nesting off until this host is rebooted (the drop-in in /etc/modprobe.d applies then)"
    fi
    return
  done
  log "neither kvm_intel nor kvm_amd is loaded on this host, so there is no nested-virtualization setting to read"
}

# ensure_granule_git_proxy_host resolves GRAIN_GRANULE_GIT_PROXY_HOST -- the
# address startGitProxy (pkg/cli/daemon.go) advertises to a granule VM in
# place of the loopback address it binds to by default, since a granule VM's
# guest runs in its own network namespace with its own unrelated 127.0.0.1
# that this host's daemon is never listening behind (bwsalmon/agents#567:
# "Failed to connect to 127.0.0.1 ... Couldn't connect to server") -- to
# docker's own "bridge" network gateway address when an operator hasn't
# already set one explicitly. That address is what a granule VM's outbound
# NAT (third_party/granule/pkg/netshim's masquerade rule, leaving
# via the VM container's docker-assigned interface) routes through to reach
# this host, the same way any other container on that network would.
#
# Like ensure_granule_kvm_access, hands a gateway it cannot find to
# granule_unavailable rather than installing a daemon whose every
# dispatched task would fail its very first git clone.
#
# `docker network inspect bridge`'s own .IPAM.Config only carries a
# "Gateway" key when something (a custom network, or an operator's own
# `docker network create --gateway`) set one explicitly -- the Debian
# `docker.io` package's bundled daemon (bwsalmon/agents#572: seen with
# docker.io 20.10.24 on the staging host image) never fills it
# in for the default bridge network's own auto-allocated pool, even
# after a container has actually attached to it, so `gw` here came back
# empty on every real install and permanently disabled granule sandboxing.
#
# The fallback asks a container on that network what its own default
# route is, which *is* the address containers reach this host through --
# the thing being looked for, rather than a proxy for it. It used to
# read the bridge device's address with `ip` out here instead; a host
# without iproute2 is not one this script is willing to fail on any more
# (this file's header, "What this host has to have"), and the image
# carries no iproute2 either, hence /proc/net/route rather than `ip
# route` inside it. That file's gateway column is a little-endian hex
# word, which is why it is reassembled backwards below.
bridge_gateway() {
  local route dest gateway hex=""
  route="$(image_run --network bridge --entrypoint sh -- -c 'cat /proc/net/route' 2>/dev/null || true)"
  [ -n "$route" ] || return 0
  while read -r _ dest gateway _; do
    if [ "$dest" = "00000000" ] && [ -n "$gateway" ]; then
      hex="$gateway"
      break
    fi
  done <<< "$route"
  [ -n "$hex" ] || return 0
  printf '%d.%d.%d.%d\n' "0x${hex:6:2}" "0x${hex:4:2}" "0x${hex:2:2}" "0x${hex:0:2}"
}

ensure_granule_git_proxy_host() {
  if [ "$GRAIN_GRANULE_ENABLE" != "1" ] && [ "$GRAIN_CONTAINER_SANDBOXES" != "1" ]; then
    return
  fi
  if [ -n "$GRAIN_GRANULE_GIT_PROXY_HOST" ]; then
    return
  fi
  local gw
  gw="$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null)"
  if [ -z "$gw" ]; then
    gw="$(bridge_gateway)"
  fi
  if [ -z "$gw" ]; then
    echo "setup.sh: GRAIN_GRANULE_GIT_PROXY_HOST is unset and docker's bridge network has no gateway; set it explicitly" >&2
    exit 1
  fi
  GRAIN_GRANULE_GIT_PROXY_HOST="$gw"
  log "granule git proxy host defaulted to docker bridge gateway $GRAIN_GRANULE_GIT_PROXY_HOST"
}

# --- 5. data directory and secrets --------------------------------------

seed_secret() {
  # Writes $2 to file $1 only if it is missing or empty, and only if a
  # value was actually given -- never overwrites a credential a previous
  # run (or an operator by hand) already placed, and never writes an
  # empty file for a value nobody supplied this time.
  #
  # Trailing newline appended deliberately: $2 reaches every caller
  # through at least one layer of "$(...)" command substitution (here,
  # in files/deploy.sh reading metadata -- bash strips every trailing
  # newline doing that), so without adding one back a multi-line
  # PEM/OpenSSH-format value is written one newline short of what it
  # started as. ssh-keygen/ssh both refuse to parse an OpenSSH private
  # key missing its final newline ("error in libcrypto", no clearer
  # message), which is what made the granule SSH key this script used to
  # generate and seed invalid on every later run (bwsalmon/agents#543).
  # That key is gone -- granule generates its own per boot now -- but the
  # GCP minter key is written through here the same way and would hit
  # exactly the same thing. Harmless for every other seed_secret caller
  # (github token, gemini-api-key): both are read with strings.TrimSpace
  # (pkg/gitproxy/credentials.go, pkg/cli/daemon.go's own
  # readTrimmedFile).
  local path="$1" value="$2"
  if [ -s "$path" ]; then
    return
  fi
  if [ -z "$value" ]; then
    return
  fi
  printf '%s\n' "$value" | grain_write 0600 "$path"
}

# json_escape backslash-escapes $1 for embedding in a double-quoted JSON
# string -- just enough for seed_github_app_credential below, whose three
# inputs are a numeric App ID, a numeric installation ID, and a PEM
# private key (printable ASCII with embedded newlines, no literal quotes
# or backslashes of its own), so backslash, double-quote and newline are
# the only characters that ever need it.
json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\r'/}"
  s="${s//$'\n'/\\n}"
  printf '%s' "$s"
}

# seed_git_connections records the seeded default GitHub credential as
# the deployment's default git connection, and each seeded agent key as a
# model connection, once, through `grain connections import` -- the only
# way an install gives grain its connections. The seed file is written, imported and deleted here;
# grain never reads it. The marker keeps a connection an operator later
# removes in Settings removed.
seed_git_connections() {
  local marker="$GRAIN_DATA_DIR/secrets/.connections-seeded"
  [ -e "$marker" ] && return 0
  local body="" models="" default_model
  if [ -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token" ] \
     || [ -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.app.json" ]; then
    body="git:
  - credential: \"${GRAIN_GITHUB_CREDENTIAL_NAME}\"
"
  fi
  # Each agent key seeded above becomes a model connection that runs on
  # its own copy of it.
  local id framework file
  for id in gemini claude codex; do
    case "$id" in
      gemini) framework=antigravity file="$GRAIN_DATA_DIR/secrets/gemini-api-key" ;;
      claude) framework=claude file="$GRAIN_DATA_DIR/secrets/claude-oauth-token" ;;
      codex) framework=codex file="$GRAIN_DATA_DIR/secrets/openai-api-key" ;;
    esac
    [ -s "$file" ] || continue
    models="${models}  - id: ${id}
    framework: ${framework}
    credential_file: \"${file}\"
"
  done
  [ -n "$models" ] && body="${body}models:
${models}"
  default_model="${GRAIN_GEMINI_MODEL:-${GRAIN_CLAUDE_MODEL:-${GRAIN_CODEX_MODEL:-}}}"
  [ -n "$default_model" ] && body="${body}default_model: \"${default_model}\"
"
  [ -n "$body" ] || return 0
  step_start "Seeding git and model connections"
  local seed="$GRAIN_DATA_DIR/secrets/.connections-seed.yaml"
  printf '%s' "$body" | grain_write 0600 "$seed"
  if GRAIN_DB_DRIVER="$GRAIN_DB_DRIVER" GRAIN_DB_DSN="$GRAIN_DB_DSN" \
       grain_cli connections -data-dir "$GRAIN_DATA_DIR" import "$seed"; then
    : | grain_write 0600 "$marker"
    step_done "seeded"
  else
    log "  could not record this deployment's seeded connections; add them in Settings,"
    log "  or run: grain connections import <file>"
  fi
  rm -f "$seed"
}

# seed_github_app_credential writes ${GRAIN_GITHUB_CREDENTIAL_NAME}.app.json
# from GRAIN_GITHUB_APP_ID/INSTALLATION_ID/PRIVATE_KEY, the same
# never-overwrite contract seed_secret gives every plain-token credential
# above: an App credential already on disk, placed by hand or by an
# earlier run, is left alone. pkg/gitproxy.CredentialSet.loadAppCredential
# reads this file's app_id/installation_id/private_key fields and mints a
# refreshing installation token from them -- both the git proxy's own
# push/fetch auth and the daemon's REST client (checks, merges,
# pkg/cli/daemon.go's credentialTokenSource) read through that same
# ladder, so no separate wiring is needed for either to pick it up.
seed_github_app_credential() {
  local path="$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.app.json"
  if [ -s "$path" ]; then
    return
  fi
  if [ -z "$GRAIN_GITHUB_APP_ID" ] && [ -z "$GRAIN_GITHUB_APP_INSTALLATION_ID" ] && [ -z "$GRAIN_GITHUB_APP_PRIVATE_KEY" ]; then
    return
  fi
  if [ -z "$GRAIN_GITHUB_APP_ID" ] || [ -z "$GRAIN_GITHUB_APP_INSTALLATION_ID" ] || [ -z "$GRAIN_GITHUB_APP_PRIVATE_KEY" ]; then
    log "  GRAIN_GITHUB_APP_ID/INSTALLATION_ID/PRIVATE_KEY: only some are set -- need all three to seed an App credential; ignoring"
    return
  fi
  printf '{"app_id":"%s","installation_id":"%s","private_key":"%s"}\n' \
    "$(json_escape "$GRAIN_GITHUB_APP_ID")" \
    "$(json_escape "$GRAIN_GITHUB_APP_INSTALLATION_ID")" \
    "$(json_escape "$GRAIN_GITHUB_APP_PRIVATE_KEY")" | grain_write 0600 "$path"
}

# archive_state_repo moves the config repository's working tree aside
# under a name saying why during schema migration. Moved, never deleted:
# its local history is the only history now that config mirroring is gone.
archive_state_repo() {
  local suffix="$1" repo_dir="$GRAIN_DATA_DIR/state-repo"
  [ -d "$repo_dir" ] || return 0
  local backup="${repo_dir}.${suffix}"
  as_grain mv -T "$repo_dir" "$backup"
  log "  moved the previous working tree to $backup -- nothing here is deleted"
  grain_dir 0700 "$repo_dir"
  if [ -s "$backup/secrets.enc" ]; then
    as_grain cp -p "$backup/secrets.enc" "$repo_dir/secrets.enc"
    log "  carried this deployment's encrypted secrets across to the fresh tree"
  fi
}

# configure_state_repo makes sure the controller-local config repository
# exists before later setup steps write state through grain. `status` opens
# an existing checkout or initializes a fresh one; it also removes an origin
# left by an older mirroring deployment without discarding local history.
configure_state_repo() {
  # As the account (grain_cli), so the state-repo/.git it creates is that
  # account's: git refuses a repository whose owner is not the caller
  # ("detected dubious ownership"), and the daemon that starts a few
  # lines below would otherwise fail its first `git config`. Beside
  # grain hostd, grain_cli runs the host binary, which needs git on
  # this host; a minimal host without git uses run-image.sh, which runs
  # the image's grain and git as the same uid:gid.
  if [ "${GRAIN_HOSTD:-0}" = "1" ] && ! command -v git >/dev/null 2>&1 && [ -x /usr/local/lib/grain/run-image.sh ]; then
    if ! /usr/local/lib/grain/run-image.sh -- state -data-dir "$GRAIN_DATA_DIR" status; then
      log "  could not open the config repository yet -- grain-daemon.service will retry on start"
    fi
    return
  fi
  if ! grain_cli state -data-dir "$GRAIN_DATA_DIR" status; then
    log "  could not open the config repository yet -- grain-daemon.service will retry on start"
  fi
}

# secrets_repo_field prints one field of <data-dir>/secrets-repo.json,
# as a small dependency-free reader for secrets-repo.json.
secrets_repo_field() {
  local file="$GRAIN_DATA_DIR/secrets-repo.json"
  [ -s "$file" ] || return 0
  sed -n "s/^[[:space:]]*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*\$/\1/p" "$file" | head -n1
}

# configure_secrets_repo points this deployment at the repository its
# encrypted secrets file is backed up to (pkg/secretsrepo).
#
# Written rather than clicked so a fleet host knows where its encrypted
# secrets backup goes without an interactive setup step. The deploy value
# is converged on every run.
#
# Nothing is cloned or pushed here. The daemon opens this repository
# before it opens the secret store and reconciles it on every sync, so
# the restore a rebuilt host needs happens on the next start rather than
# in the middle of an installer that has not written the secrets key yet.
configure_secrets_repo() {
  [ -n "$GRAIN_SECRETS_REPO_URL" ] || return 0
  local file="$GRAIN_DATA_DIR/secrets-repo.json"
  local current_remote current_branch
  current_remote="$(secrets_repo_field remote)"
  current_branch="$(secrets_repo_field branch)"
  if [ "$current_remote" != "$GRAIN_SECRETS_REPO_URL" ] || [ "$current_branch" != "$GRAIN_SECRETS_REPO_BRANCH" ]; then
    log "Backing this deployment's encrypted secrets up to $GRAIN_SECRETS_REPO_URL ($GRAIN_SECRETS_REPO_BRANCH)"
    {
      printf '{\n'
      printf '  "remote": "%s",\n' "$(json_escape "$GRAIN_SECRETS_REPO_URL")"
      printf '  "branch": "%s"\n' "$(json_escape "$GRAIN_SECRETS_REPO_BRANCH")"
      printf '}\n'
    } | grain_write 0600 "$file"
  fi
}

# setup_sandbox_dir lays out GRAIN_SANDBOX_DIR, orchestrator.HostSandboxes'
# own baseDir (-sandbox-dir, pkg/cli/daemon.go) -- deliberately its own
# function, separate from setup_data_dir below, since the whole point
# (bwsalmon/agents#587) is that this directory is not part of
# $GRAIN_DATA_DIR: scratch/ beside data/ by default, so a backup of the
# one never carries the other. Run unconditionally, since a deployment
# can flip GRAIN_GRANULE_ENABLE later without a fresh install, and
# grain-daemon.service always passes -sandbox-dir either way
# (write_systemd_units) even though only the non-granule path ever reads
# it.
setup_sandbox_dir() {
  setup_layout_root
  log "Laying out $GRAIN_SANDBOX_DIR"
  install -d -m0755 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_SANDBOX_DIR"
  # The daemon container's TMPDIR behind grain hostd (docker_run_args).
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    as_grain install -d -m0700 "$GRAIN_SANDBOX_DIR/tmp"
  fi
  # An override that puts the sandbox root inside $GRAIN_DATA_DIR on the
  # same filesystem undoes the split this function exists for, and does
  # it quietly: a run's checkout then shares a disk with the SQLite store
  # and the secrets database, so the disk a few large checkouts fill is
  # the disk everything else needs to write to, and a backup of the data
  # directory carries every checkout with it. A separate mount there is
  # its own disk, and siblings under one root (the default) are not
  # inside each other at all, so neither warns. Warned rather than
  # refused -- a deployment with one big disk and nothing else to point
  # at is a legitimate choice, and it is only worth knowing what it costs.
  case "$GRAIN_SANDBOX_DIR/" in
    "${GRAIN_DATA_DIR%/}"/*)
      if same_filesystem "$GRAIN_SANDBOX_DIR" "$GRAIN_DATA_DIR"; then
        log "WARNING: $GRAIN_SANDBOX_DIR is inside \$GRAIN_DATA_DIR ($GRAIN_DATA_DIR) on the same filesystem, so a"
        log "WARNING: task's checkout shares a disk with the store and secrets, and a full sandbox disk takes"
        log "WARNING: those down with it. Point GRAIN_SANDBOX_DIR at a disk of its own (default: $GRAIN_DEFAULT_SANDBOX_DIR)."
      fi
      ;;
  esac
}

setup_data_dir() {
  setup_layout_root
  step_start "Laying out $GRAIN_DATA_DIR"
  install -d -m0750 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_DATA_DIR"
  # bin/ no longer holds a grain binary -- nothing on this host does
  # (install_cli_wrappers) -- but it is kept, owned by $GRAIN_USER, for
  # the same reason $GRAIN_DATA_DIR itself is: a deployment upgraded
  # across bwsalmon/agents#645 already has one, with a previous release's
  # binary in it, and an operator who put something of their own there
  # should find it where they left it. install -d re-applies -o/-g (and
  # its -m mode) even against a directory that already exists, which is
  # exactly what is wanted here and exactly why it is never used on a
  # directory this script does not own.
  grain_dir 0755 "$GRAIN_DATA_DIR/bin"
  # A real, writable HOME for the daemon, exported by grain-daemon.service
  # (write_systemd_units). $GRAIN_USER is created --no-create-home, so the
  # home its passwd entry names (/home/grain) does not exist -- which is
  # fine for the daemon itself, and not fine for the claude CLI it now
  # execs per dispatch: that writes its own state under $HOME, and would
  # fail on every run against a directory that isn't there. Under the data
  # dir, not /home, so it lives on the same disk as everything else this
  # deployment keeps and is backed up with it.
  grain_dir 0700 "$GRAIN_DATA_DIR/home"
  grain_dir 0700 "$GRAIN_DATA_DIR/secrets"
  grain_dir 0700 "$GRAIN_DATA_DIR/secrets/github"
  # The controller-local config repository (pkg/staterepo): the working
  # tree grain exports its configuration database into. Made
  # here, owned by $GRAIN_USER, before the CLI steps below (grain_cli)
  # initialize it as that account -- a git working tree with root-owned
  # files in it is one the daemon's own git refuses to touch at all
  # ("dubious ownership"), not merely one it cannot write.
  grain_dir 0700 "$GRAIN_DATA_DIR/state-repo"
  grain_dir 0700 "$GRAIN_DATA_DIR/postgres"
  # grain-daemon.service's own -data-dir/store, embedded SQLite -- the
  # one process that ever opens it now (this file's own header on
  # bwsalmon/agents#363), so no separate store container or directory
  # layout is needed for it beyond what openStore creates on its own the
  # first time the daemon starts.

  # GitHub credential material (pkg/gitproxy/credentials.go): one
  # <name>.token or <name>.app.json per credential. Which repositories use
  # it is the deployment's git connections, which grain keeps in its store
  # and which this script seeds once, through `grain connections import`
  # (seed_git_connections, once the daemon has started): the default
  # credential becomes the default host's default connection. There is no credentials.json any more -- grain never reads
  # a file to choose a credential (docs/single-tenant-reset.md, stage 2).
  # Settings -> GitHub in the UI adds a token and, for the first one,
  # its connection, so a host that reaches the end of this with no
  # GitHub credential at all is finishable from the UI.
  seed_secret "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token" "$GRAIN_GITHUB_TOKEN"
  seed_github_app_credential

  # Before anything below runs `grain secrets`: opening a store with no
  # key mints one. A rebuilt host must install the key that opens its
  # restored secrets file before any command touches the store.
  seed_secret "$GRAIN_DATA_DIR/secrets/secrets.key" "$GRAIN_SECRETS_KEY"

  # Before anything below runs the image's git over the data directory,
  # which cannot open a repository a host git made with reftable refs.
  convert_reftable_repos

  # Initialize the controller-local config repository and disconnect an
  # origin left by older mirroring deployments without replacing its history.
  configure_state_repo
  # And where the encrypted secrets file itself is backed up, which is a
  # different repository and settles nothing about the one above. Written
  # before the seeds below rather than after: a host restored onto a
  # fresh data directory has its ciphertext fetched back by the daemon's
  # first start, and every seed_secret between here and there writes into
  # the file that comes back.
  configure_secrets_repo

  # Order matters below: the minter key has to be in the secrets
  # database before mint_gemini_operating_key can authenticate with it.
  seed_secret "$GRAIN_DATA_DIR/secrets/gemini-api-key" "$GRAIN_GEMINI_API_KEY"
  seed_secret "$GRAIN_DATA_DIR/secrets/claude-oauth-token" "$GRAIN_CLAUDE_CODE_OAUTH_TOKEN"
  seed_secret "$GRAIN_DATA_DIR/secrets/openai-api-key" "$GRAIN_OPENAI_API_KEY"

  seed_gcp_minter_key

  mint_gemini_operating_key

  if [ ! -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token" ] \
     && [ ! -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.app.json" ]; then
    log "  no GitHub credential configured yet -- open Settings -> GitHub in the UI and paste a"
    log "  token: the first one added becomes this deployment's default, no restart needed."
    log "  Unattended: set GRAIN_GITHUB_TOKEN, or all three of"
    log "  GRAIN_GITHUB_APP_ID/INSTALLATION_ID/PRIVATE_KEY, and re-run"
  fi
  step_done "ready"
}

# seed_gcp_minter_key writes the GCP minter's key into the gcp-key-minter
# secret in the (SQLite-backed, pkg/secrets's own doc comment) secrets
# database -- through the just-installed `grain secrets` CLI rather than a
# raw file write, since that database is no longer a directory tree this
# script can lay files into directly; chown afterward because the CLI, run
# here as root, otherwise leaves the database it creates owned by root,
# unreadable by grain-daemon.service's own $GRAIN_USER.
#
# **It converges rather than seeding once, unlike every plain-file secret
# seed_secret places above, and that difference is the whole point.** This
# used to return early whenever `grain secrets list` already showed a
# gcp-key-minter entry, on the same "never overwrite what is already
# there" rule as the rest -- and for a credential nobody rotates, that
# rule costs nothing. This one is rotated, by the deployment's own
# machinery: terraform/gcp/deploy/push-secrets.sh mints a *fresh* minter
# key on every run, pushes it into instance metadata, and then deletes
# every key on the minter account beyond the newest two. The host, having
# seeded its copy on the first deploy and refused every later one, went on
# authenticating with the key from that first run -- so the third
# push-secrets.sh run deleted the credential the daemon was actually
# using, out from under a deployment that had no idea it had been handed
# a replacement twice.
#
# Nothing failed visibly when that happened. `gcp-key`'s own Settings row
# still read Ready (the secret is set; only GCP knows the key inside it is
# dead), and the failure surfaced a task at a time, as GCP's own
# `invalid_grant` / "Invalid JWT Signature" out of a mint -- see
# pkg/capability/gcpkey's explainRefusedCredential, which now names this
# secret when it happens. The documented remedy was to delete the entry by
# hand over IAP and bump deploy_generation, which is a manual step to
# repair a state the deployment itself created.
#
# So: a key given to this script is the key the daemon ends up holding,
# every run. An operator's own `grain secrets set` still wins for as long
# as no deploy carries a key of its own (the early return below), which is
# the case a hand-installed deployment is actually in -- what it no longer
# does is win against the rotation that is about to invalidate it.
seed_gcp_minter_key() {
  if [ -z "$GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE" ]; then
    return
  fi
  # Staged under $GRAIN_DATA_DIR before being handed to the CLI, and
  # removed straight after: `grain` is a wrapper around `docker run` now
  # (install_cli_wrappers), and -value-file has to name a path that
  # resolves *inside* that container. The file this is given is
  # deliberately somewhere else -- terraform/gcp/files/deploy.sh
  # writes it into a tmpfs under /run -- and mounting an arbitrary
  # operator-supplied path into the daemon's own container for the sake
  # of one read would be a far worse trade than a copy that lives for
  # one command.
  # Staged as $GRAIN_USER, not as root. This script runs as root, so the
  # obvious `umask 077 && cat >` writes a 0600 file owned by root -- and
  # the `grain` that reads it is a `docker run --user` as $GRAIN_USER
  # (install_cli_wrappers), which cannot read that. It failed there,
  # under `set -e`, after the image had been pulled and before
  # write_systemd_units ever ran: a deployment with a minter key got no
  # grain-daemon.service at all, and the only trace was the CLI's own
  # "permission denied" in the deploy log.
  #
  # Root reads the operator's file and the account writes the copy
  # (grain_write), so the copy is never momentarily anyone else's and a
  # symlink left at its name is replaced rather than written through.
  local staged="$GRAIN_DATA_DIR/secrets/.minter-key.staged.json"
  grain_write 0600 "$staged" < "$GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE"
  local key
  key="$(minter_secret_key)"
  grain_cli secrets -data-dir "$GRAIN_DATA_DIR" set \
    -value-file "$staged" gcp-key-minter "$key"
  as_grain rm -f "$staged"
}

# minter_secret_key names the key *inside* the gcp-key-minter secret that
# this deployment's minter credential is written to: whichever single key
# the secret already holds, and key.json for a secret that does not exist
# yet.
#
# Writing in place matters because gcpkey.Config.MinterCredential names
# the bare secret, and pkg/secrets.Store.Resolve only answers a bare name
# when the secret holds exactly one key -- so a secret carrying both a
# `value` written from Settings and a `key.json` written here resolves to
# an error rather than to either of them, and the capability breaks in a
# second way while this function is busy fixing the first. (pkg/ui writes
# to the key already there for exactly the same reason; this is that rule
# from the other side.) A secret that has somehow ended up with several
# keys is already in that state and gets key.json, which is at least the
# name the rest of this deployment's documentation uses.
minter_secret_key() {
  local keys
  keys="$(grain_cli secrets -data-dir "$GRAIN_DATA_DIR" list 2>/dev/null |
    sed -n 's/^gcp-key-minter: //p' || true)"
  case "$keys" in
    "" | *,*) echo "key.json" ;;
    *) echo "$keys" ;;
  esac
}

# mint_gemini_operating_key mints the daemon's own Gemini API key, using
# the minter credential seed_gcp_minter_key just placed, when no key is
# in place yet.
#
# A deployment that grants its minter roles/serviceusage.apiKeysAdmin
# (terraform/gcp's enable_gemini_key, on by default there) already has
# every permission this needs, on this host -- so nobody has to paste a
# Gemini key in by hand before this deployment can dispatch anything.
# Where that grant is absent the mint simply fails, and the deployment is
# left exactly where a deployment with no agent credential now sits: the
# daemon runs, the UI serves, and a key can be pasted into Settings
# instead. It must never fail the whole converge, since the GitHub side
# of a deployment is useful without it and a half-applied setup.sh is
# worse than a deployment that cannot yet dispatch.
#
# Seed-once, like everything else here: `grain secrets mint-gemini-key`
# leaves an existing non-empty key file untouched, so config-sync
# re-running this on every convergence pass does not issue a new key each
# time. To rotate, delete the file (and the old key in GCP -- the
# capability's own reaper deliberately never touches an operating key)
# and let the next pass mint a fresh one.
mint_gemini_operating_key() {
  if [ -s "$GRAIN_DATA_DIR/secrets/gemini-api-key" ]; then
    return
  fi
  # Guarded on the project alone, not also on
  # GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE: seed_gcp_minter_key needs that
  # file to seed a credential, but a re-run where the credential is
  # already in the secrets database has no key file and can still mint.
  # A project set with no minter credential anywhere fails fast and
  # locally, on the resolve, and is reported rather than fatal.
  if [ -z "$GRAIN_GCP_PROJECT" ]; then
    return
  fi
  log "  no Gemini API key in place -- minting the daemon's own from the GCP minter credential"
  # As the account (grain_cli), so the key file it writes is that
  # account's from the start.
  if ! grain_cli secrets -data-dir "$GRAIN_DATA_DIR" \
     mint-gemini-key -project "$GRAIN_GCP_PROJECT"; then
    log "  could not mint a Gemini API key -- the daemon still runs, but a gemini-framework"
    log "  run cannot dispatch until one exists. Paste one into the UI (Settings -> Agent"
    log "  frameworks), check the minter credential holds roles/serviceusage.apiKeysAdmin,"
    log "  or set GRAIN_GEMINI_API_KEY and re-run."
  fi
}

# --- 5b. convert reftable repositories to the files ref format ---------
#
# A newer git can be configured -- init.defaultRefFormat, or a feature
# preset -- to create repositories with the reftable backend
# (extensions.refstorage = reftable), and a Cloudtop's git is. A host-mode
# daemon ran the host's git, so the state repository and every cache,
# mirror and checkout it made under the data and sandbox directories can
# be reftable. The daemon now runs in the release image, whose git is
# bookworm's 2.39: it cannot open a reftable repository at all, and the
# first one it touches stops it at startup with "unknown repository
# extension found: refstorage" (task 5195). The daemon pins the files
# format for everything it creates from now on (pkg/gitrefs); this is the
# other half, for what an older one left behind.
#
# Every run looks, not only a migration: a host-mode install moved by an
# older installer, or a repository put there by hand, is in the same
# state. Each one found is converted with the host's git -- it made them,
# so it reads them -- as the account that owns it, with the daemon
# stopped so nothing writes refs mid-conversion. enable_services starts it
# again. Nothing found, nothing stopped.

# REFTABLE_CONVERTED and REFTABLE_LEFT collect a line per repository for
# print_summary.
REFTABLE_CONVERTED=()
REFTABLE_LEFT=()

# config_ref_format prints the extensions.refstorage a git config file
# sets, lowercased, or nothing. Parsed here rather than by `git config -f`:
# the host need not have a git at all.
config_ref_format() {
  awk '
    /^[ \t]*\[/ { s = tolower($0); sub(/\].*/, "", s); gsub(/[ \t\[]/, "", s); sec = s; next }
    sec == "extensions" {
      l = $0; sub(/[;#].*/, "", l); n = index(l, "=")
      if (n) {
        k = tolower(substr(l, 1, n - 1)); gsub(/[ \t]/, "", k)
        if (k == "refstorage") { v = substr(l, n + 1); gsub(/^[ \t"]+|[ \t"]+$/, "", v); f = tolower(v) }
      }
    }
    END { if (f != "") print f }' "$1" 2>/dev/null
}

# reftable_repos prints "<git dir>\t<format>" for every repository under
# the data and sandbox directories whose config names a ref format other
# than files. find does not follow symlinks, and -xdev keeps it on the
# directories' own filesystems.
reftable_repos() {
  local root config gitdir format
  for root in "$GRAIN_DATA_DIR" "$GRAIN_SANDBOX_DIR"; do
    [ -d "$root" ] || continue
    find "$root" -xdev -type f -name config -size -1024k -print0 2>/dev/null |
      while IFS= read -r -d '' config; do
        grep -qi refstorage "$config" 2>/dev/null || continue
        gitdir="$(dirname "$config")"
        [ -f "$gitdir/HEAD" ] || continue
        format="$(config_ref_format "$config")"
        if [ -n "$format" ] && [ "$format" != files ]; then
          printf '%s\t%s\n' "$gitdir" "$format"
        fi
      done
  done | sort -u
}

# host_git_version prints the host git's version as "<major> <minor>",
# or nothing when there is no git on this host.
host_git_version() {
  command -v git >/dev/null 2>&1 || return 0
  git --version 2>/dev/null | sed -n 's/^git version \([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p'
}

# host_git_at_least MAJOR MINOR
host_git_at_least() {
  local have major minor
  have="$(host_git_version)"
  [ -n "$have" ] || return 1
  read -r major minor <<<"$have"
  [ "$major" -gt "$1" ] || { [ "$major" -eq "$1" ] && [ "$minor" -ge "$2" ]; }
}

# as_repo_owner runs a command as the account that owns git directory $1
# (the rest of the arguments), with neither that account's nor the
# system's git config in the way: nothing a user has set should decide
# how this conversion goes, and the reftable default is exactly the
# setting being undone.
as_repo_owner() {
  local gitdir="$1" uid gid
  shift
  uid="$(stat -c %u "$gitdir")" && gid="$(stat -c %g "$gitdir")" || return 1
  setpriv --reuid="$uid" --regid="$gid" --clear-groups -- \
    env HOME=/nonexistent GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_DEFAULT_REF_FORMAT=files "$@"
}

# rewrite_refs_as_files converts git directory $1 from reftable to files
# without `git refs migrate`, for a host git that reads reftable (2.45)
# but has no migrate command, or one that refuses this repository (2.46
# and 2.47 refuse any with reflogs, which is every work tree): it lists every ref and HEAD through git,
# moves the reftable stack aside into a backup directory inside the git
# directory, and writes the same refs back as packed-refs, with
# symbolic refs as loose files. Reflogs stay in the backup only. It runs
# as the owner (as_repo_owner), refuses a repository with linked
# worktrees -- their per-worktree refs live in stacks of their own -- and
# puts everything back if the refs it wrote do not read back the same.
# Declared for `bash -c` (convert_reftable_repo), so it uses only its
# own arguments.
rewrite_refs_as_files() {
  set -eu
  local g="$1" backup before after head
  export GIT_DIR="$g"
  if [ -d "$g/worktrees" ] && [ -n "$(ls -A "$g/worktrees")" ]; then
    echo "it has linked worktrees, which only git refs migrate can carry" >&2
    return 1
  fi
  before="$(git for-each-ref --format='%(objectname) %(refname) %(symref)')"
  head="$(git symbolic-ref -q HEAD || git rev-parse -q --verify HEAD || true)"
  [ -n "$head" ] || { echo "its HEAD resolves to nothing" >&2; return 1; }
  backup="$g/grain-reftable-backup-$(date +%Y%m%d%H%M%S)"
  mkdir "$backup"
  # Not "config": a backup with a HEAD and a config would read as a
  # reftable repository of its own to the next run's reftable_repos.
  cp -p "$g/config" "$backup/config.reftable"
  for f in reftable refs HEAD packed-refs; do
    if [ -e "$g/$f" ]; then mv "$g/$f" "$backup/$f"; fi
  done
  restore() {
    rm -rf "$g/refs" "$g/packed-refs" "$g/HEAD"
    for f in reftable refs HEAD packed-refs; do
      if [ -e "$backup/$f" ]; then mv "$backup/$f" "$g/$f"; fi
    done
    cp -p "$backup/config.reftable" "$g/config"
  }
  if ! {
    mkdir -p "$g/refs/heads" "$g/refs/tags" &&
    printf '%s\n' "$before" | awk 'NF == 2 { print $1 " " $2 }' | LC_ALL=C sort -k2 > "$g/packed-refs" &&
    printf '%s\n' "$before" | awk 'NF == 3 { print $2 " " $3 }' | while read -r name target; do
      mkdir -p "$(dirname "$g/$name")" && printf 'ref: %s\n' "$target" > "$g/$name"
    done &&
    case "$head" in
      refs/*) printf 'ref: %s\n' "$head" > "$g/HEAD" ;;
      *) printf '%s\n' "$head" > "$g/HEAD" ;;
    esac &&
    git config --file "$g/config" --unset-all extensions.refstorage
  }; then
    restore
    echo "writing the files-format refs failed; put the reftable ones back" >&2
    return 1
  fi
  after="$(git for-each-ref --format='%(objectname) %(refname) %(symref)' 2>/dev/null || true)"
  if [ "$after" != "$before" ]; then
    restore
    echo "the refs did not read back the same after the rewrite; put the reftable ones back" >&2
    return 1
  fi
  echo "$backup"
}

# convert_reftable_repo converts git directory $1 (format $2) to files,
# appending to REFTABLE_CONVERTED or REFTABLE_LEFT.
convert_reftable_repo() {
  local gitdir="$1" format="$2" out backup
  local cmd="git --git-dir=$gitdir refs migrate --ref-format=files"
  if [ "$format" = reftable ] && host_git_at_least 2 46; then
    if out="$(as_repo_owner "$gitdir" git --git-dir="$gitdir" refs migrate --ref-format=files 2>&1)"; then
      log "  converted $gitdir: $format -> files (git refs migrate)"
      REFTABLE_CONVERTED+=("$gitdir ($format -> files, git refs migrate)")
      return 0
    fi
    log "  git refs migrate refused $gitdir: $(printf '%s' "$out" | tr '\n' ' ')"
  fi
  if [ "$format" = reftable ] && host_git_at_least 2 45; then
    if backup="$(as_repo_owner "$gitdir" bash -c "$(declare -f rewrite_refs_as_files); rewrite_refs_as_files \"\$1\"" _ "$gitdir" 2>/tmp/grain-refs-rewrite.$$)"; then
      rm -f /tmp/grain-refs-rewrite.$$
      log "  converted $gitdir: $format -> files (refs rewritten as packed-refs; the reftable stack and its reflogs are kept in $backup)"
      REFTABLE_CONVERTED+=("$gitdir ($format -> files, rewritten by this script, as the host git could not refs migrate it; reftable stack and reflogs kept in $backup)")
      return 0
    fi
    log "  could not rewrite $gitdir's refs: $(tr '\n' ' ' </tmp/grain-refs-rewrite.$$)"
    rm -f /tmp/grain-refs-rewrite.$$
  fi
  local have
  have="$(host_git_version)"
  echo "setup.sh: warning: $gitdir is a $format git repository, which the daemon's git cannot read, and this host's git (${have:+$(printf '%s' "$have" | tr ' ' .)}${have:-none}) could not convert it. With git 2.46 or newer, as its owner ($(stat -c %U "$gitdir" 2>/dev/null)), with the daemon stopped: $cmd" >&2
  REFTABLE_LEFT+=("$gitdir ($format): convert with git 2.46 or newer, as $(stat -c %U "$gitdir" 2>/dev/null), daemon stopped: $cmd")
}

convert_reftable_repos() {
  local found gitdir format
  found="$(reftable_repos)"
  [ -n "$found" ] || return 0
  step_start "Converting reftable git repositories to the files ref format"
  log "These repositories under $GRAIN_DATA_DIR and $GRAIN_SANDBOX_DIR use a ref format the daemon's git cannot read:"
  printf '%s\n' "$found" | while IFS=$'\t' read -r gitdir format; do log "  $gitdir ($format)"; done
  if systemctl is-active --quiet grain-daemon.service 2>/dev/null; then
    log "Stopping grain-daemon.service while they are converted; it is started again below"
    systemctl stop grain-daemon.service
  fi
  while IFS=$'\t' read -r gitdir format; do
    convert_reftable_repo "$gitdir" "$format"
  done <<<"$found"
  step_done "${#REFTABLE_CONVERTED[@]} converted, ${#REFTABLE_LEFT[@]} left"
}

# --- end of reftable repositories --------------------------------------------

# --- 6. reformat the store if a breaking schema change shipped ---------
#
# pkg/model.SchemaVersion's own doc comment: bumped exactly when Tables
# or Views change in a way Store.Init's `CREATE TABLE IF NOT EXISTS`
# cannot safely reconcile an existing database into -- Init only adds a
# table that is missing outright, never a column on one that already
# exists, so a build newer than the database it finds would otherwise
# start silently against stale columns instead of refusing or fixing
# anything. `grain schema-version` (pkg/cli/schemaversion.go) is that
# same constant, printed by the binary this run just installed, so this
# function never has to duplicate pkg/model's own definition of
# "breaking" by parsing source.
#
# $GRAIN_DATA_DIR/.schema_version is the marker this function itself
# maintains, recording the schema version the store on disk was last
# known to agree with. No marker yet -- a brand new $GRAIN_DATA_DIR, or
# an upgrade from before this function existed -- never wipes anything:
# it just records the current version and moves on, since there is
# nothing to safely compare against and "assume compatible, preserve the
# data" is the safer of the two guesses. A marker that disagrees with the
# fresh build has the rows exported for the daemon's next start to migrate
# (see the comment inside). Only when that is impossible and the operator
# has set GRAIN_ALLOW_STORE_RESET=1 is $GRAIN_DATA_DIR/store moved aside
# to a timestamped backup -- never deleted outright, so a mistaken schema
# bump is recoverable by hand, and report_readiness names it and the way
# back -- and grain's own sqlite.Open (pkg/model/sqlite) recreates an
# empty one, with the new schema, the next time grain-daemon.service
# starts.
reformat_store_if_schema_changed() {
  local marker="$GRAIN_DATA_DIR/.schema_version"
  local store_dir="$GRAIN_DATA_DIR/store"
  local new_version
  new_version="$(grain_cli schema-version 2>/dev/null || true)"
  # An unanswerable CLI is reported and stepped over, never guessed at:
  # writing an empty marker would make the *next* run read a schema
  # change that never happened and move a live store aside for nothing,
  # and aborting here would cost the deployment its service over a
  # question that only decides whether to reformat.
  if [ -z "$new_version" ]; then
    local asked="$GRAIN_IMAGE_REF"
    log "WARNING: could not read a schema version out of $asked -- leaving"
    log "         $store_dir and the marker exactly as they are. If the schema did change"
    log "         in this build, the daemon will say so on its first start."
    return
  fi

  if [ ! -s "$marker" ]; then
    log "No schema-version marker yet -- recording schema $new_version, not touching any existing store"
    printf '%s\n' "$new_version" | grain_write 0644 "$marker"
    return
  fi

  local old_version
  old_version="$(cat "$marker")"
  if [ "$old_version" = "$new_version" ]; then
    return
  fi

  # Carry the rows across first, the way the UI's Upgrade button does
  # (pkg/cli/schemaupgrade.go, grain/task-4357): `grain state
  # prepare-upgrade`, run by the build just installed, exports the store
  # into $GRAIN_DATA_DIR/schema-upgrade and rehearses importing it, and
  # the daemon's next start moves the old store aside itself and rebuilds
  # it from that export. Moving the store aside and starting empty -- the
  # only answer this function used to have -- loses every user and task,
  # so it is now what happens only when the migration cannot be made and
  # the operator has said GRAIN_ALLOW_STORE_RESET=1.
  #
  # The daemon is stopped first so that the export is the store's last
  # state: a row written after it would be in the archived store only.
  local daemon_was_active=0
  if systemctl is-active --quiet grain-daemon.service 2>/dev/null; then
    daemon_was_active=1
    log "Schema changed ($old_version -> $new_version) -- stopping grain-daemon.service to carry this deployment's rows across"
    systemctl stop grain-daemon.service
  else
    log "Schema changed ($old_version -> $new_version) -- carrying this deployment's rows across"
  fi
  local prepared="" prepare_failed=0
  prepared="$(grain_cli state -data-dir "$GRAIN_DATA_DIR" prepare-upgrade)" || prepare_failed=1
  if [ "$prepare_failed" = "0" ]; then
    case "$prepared" in
      prepared\ *)
        local from
        from="$(printf '%s\n' "$prepared" | awk '{print $2}')"
        GRAIN_SCHEMA_CHANGE_REPORT="migrated"
        GRAIN_SCHEMA_CHANGE_FROM="$from"
        log "  exported this deployment's rows at schema $from to $GRAIN_DATA_DIR/schema-upgrade; the"
        log "  daemon's next start moves the store aside to $GRAIN_DATA_DIR/store.schema${from}-<time>"
        log "  (kept, never deleted) and rebuilds it at schema $new_version from that export"
        printf '%s\n' "$new_version" | grain_write 0644 "$marker"
        return
        ;;
      current\ *)
        log "  the store is already at schema $new_version (the marker said $old_version) -- nothing to migrate"
        printf '%s\n' "$new_version" | grain_write 0644 "$marker"
        return
        ;;
      none)
        # No embedded store: nothing here for a reset to lose, so the
        # path below needs no confirmation.
        ;;
      *)
        prepare_failed=1
        ;;
    esac
  fi
  if [ "$prepare_failed" = "1" ] && [ "$GRAIN_ALLOW_STORE_RESET" != "1" ]; then
    echo "setup.sh: this build is at schema $new_version and the store in $GRAIN_DATA_DIR/store was" >&2
    echo "  written at schema $old_version, and its rows could not be carried across (the reason is" >&2
    echo "  just above). Nothing has been moved or deleted." >&2
    if [ "$daemon_was_active" = "1" ]; then
      # The unit still names the image it was running: write_image_ref
      # has not run yet, so this starts the build that wrote the store.
      systemctl start grain-daemon.service || true
      echo "  grain-daemon.service has been started again on the build it was running." >&2
    else
      echo "  grain-daemon.service is stopped: the build installed here cannot open that store." >&2
      echo "  Re-run setup.sh with the GRAIN_CHANNEL / GRAIN_REF it was on to go back to it." >&2
    fi
    echo "  To start this build on an EMPTY store instead -- every user, task and run gone; the" >&2
    echo "  old store and state repository are moved aside, not deleted -- re-run with" >&2
    echo "  GRAIN_ALLOW_STORE_RESET=1." >&2
    exit 1
  fi

  local stamp
  stamp="$(date +%Y%m%d%H%M%S)"
  # An upgrade note left by the UI's Upgrade button is not something this
  # path can honour, and leaving it would be worse than removing it. The
  # button's answer to a schema change is to export the rows and have the
  # next start rebuild the store from them (pkg/cli/schemaupgrade.go);
  # this path's answer is to move the store aside, which is the same
  # decision made the other way. Whichever runs first decides, and a note
  # that survived this would sit in the data directory describing rows
  # that are now only in the archived store, waiting for some later start
  # to find a store it cannot open and import them over it.
  if [ -d "$GRAIN_DATA_DIR/schema-upgrade" ]; then
    log "Removing $GRAIN_DATA_DIR/schema-upgrade -- an upgrade had written this deployment's rows out to"
    log "  migrate them; this run moves the store aside instead, so they stay in the archive below"
    rm -rf "$GRAIN_DATA_DIR/schema-upgrade"
  fi
  if [ -d "$store_dir" ]; then
    local backup="${store_dir}.schema${old_version}-${stamp}"
    log "Schema changed ($old_version -> $new_version) -- moving $store_dir aside to $backup so grain starts a fresh store"
    as_grain mv -T "$store_dir" "$backup"
    GRAIN_SCHEMA_CHANGE_REPORT="reset"
    GRAIN_SCHEMA_CHANGE_FROM="$old_version"
    GRAIN_SCHEMA_CHANGE_STAMP="$stamp"
  else
    log "Schema changed ($old_version -> $new_version) -- no existing store to move aside"
  fi

  # The config repository holds an exported view of the store. Archive it
  # alongside an incompatible store, preserving its complete local history,
  # and leave a one-shot note authorizing the daemon to export at the new
  # schema on its next start.
  #
  # The encrypted secrets file used to live in that tree, and had to be
  # carried across by hand here because it is the one thing in there that
  # cannot be regenerated. It lives beside its own private key under
  # $GRAIN_DATA_DIR/secrets now (grain/task-186: everything a sandbox can
  # clone is everything a sandbox can read, and the state repository is
  # somewhere agents are dispatched to work), which archive_state_repo
  # never touches -- so a schema bump no longer goes anywhere near it. A
  # tree written by an older build still has its copy carried across,
  # since grain moves it out on its next start and a tree that was
  # archived first would take it with it.
  if [ -d "$GRAIN_DATA_DIR/state-repo" ]; then
    log "Schema changed -- moving the state repository aside as well; grain re-seeds it on its next start"
    archive_state_repo "schema${old_version}-${stamp}"
    if [ -s "$GRAIN_DATA_DIR/state-repo/secrets.enc" ] && [ ! -s "$GRAIN_DATA_DIR/secrets/secrets.enc" ]; then
      grain_dir 0700 "$GRAIN_DATA_DIR/secrets"
      as_grain mv "$GRAIN_DATA_DIR/state-repo/secrets.enc" "$GRAIN_DATA_DIR/secrets/secrets.enc"
      log "  carried this deployment's encrypted secrets across, beside its key in $GRAIN_DATA_DIR/secrets"
    fi
  fi
  # Owned by $GRAIN_USER because the daemon removes it once it has acted
  # on it, and a root-owned file here would leave it saying the same
  # thing at every start from now on -- which is exactly the standing
  # permission to overwrite a state repository that this is meant to be
  # narrow about.
  local reformat_stamp="$GRAIN_DATA_DIR/state-repo.reformat"
  grain_write 0600 "$reformat_stamp" <<STAMP
Written by scripts/setup.sh at $(date -u +%Y-%m-%dT%H:%M:%SZ).

This deployment's schema changed from $old_version to $new_version, and the
store under $GRAIN_DATA_DIR was reformatted for it -- moved aside, under a
timestamped name, if there was one here to move. The dump in the state
repository was written before that change, so grain replaces it -- with an
export of the database it runs on now -- at its next start, and removes this
file.

Delete it to call that off: grain will then refuse to start on a dump it
cannot read, which is what it does for every repository nobody has said
this about.
STAMP
  log "  recorded the schema change in $reformat_stamp -- the next start replaces the"
  log "  repository's dump with this deployment's database and pushes it"
  printf '%s\n' "$new_version" | grain_write 0644 "$marker"
}

# --- 7. format the target repo, if it is empty --------------------------
#
# Every dispatch grain runs branches off an existing ref -- it never
# creates the first one (tests/e2e's own harness always seeds one commit
# before driving anything against a bare repo). A repo created fresh on
# GitHub has none, so `grain create -repo owner/name ...` would have
# nothing to branch from. Detected with `git ls-remote`, which returns no
# output at all against a repo with zero refs -- no clone needed just to
# find that out.
#
# Both git calls run inside the deployment image (image_run), which
# carries git because the orchestrator clones every task's repo with it
# (Dockerfile). They are the only two steps here that want a real git,
# and running them in there is what lets this host have none at all --
# see this file's header, "What this host has to have". --network host
# so a GRAIN_GITHUB_HOST on this host's own loopback (a mock server,
# under GRAIN_GITHUB_INSECURE_HTTP) is reachable from in there too, and
# the URL travels as an environment variable rather than an argument,
# since it carries a token and a command line is world-readable in `ps`.
#
# The scratch repository is made *inside* the container as well: it
# exists for one empty commit and is thrown away with the container, so
# there is nothing to mount and nothing on this host to clean up.

format_target_repo_if_empty() {
  if [ -z "$GRAIN_TARGET_REPO" ]; then
    return
  fi
  # Both halves of this run git inside the deployment image, which is
  # what lets this host have none of its own (see this file's header).
  # With no engine to open that image there is no git anywhere this
  # script may use, so the check is skipped rather than replaced by a
  # host git this deployment has never required.
  if [ "$GRAIN_DOCKER_AVAILABLE" != "1" ]; then
    log "No container engine on this host: skipping the empty-repo check for $GRAIN_TARGET_REPO"
    log "  (push one commit to $GRAIN_TARGET_BRANCH yourself if it has none -- grain only ever branches off an existing ref)"
    return
  fi
  local token="$GRAIN_GITHUB_TOKEN"
  if [ -z "$token" ] && [ -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token" ]; then
    token="$(cat "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token")"
  fi
  if [ -z "$token" ]; then
    log "GRAIN_TARGET_REPO is set but no GitHub token is available -- skipping the empty-repo check"
    return
  fi

  local proto="https"
  [ "$GRAIN_GITHUB_INSECURE_HTTP" = "1" ] && proto="http"
  local url="${proto}://x-access-token:${token}@${GRAIN_GITHUB_HOST}/${GRAIN_TARGET_REPO}.git"

  local no_proxy_env=()
  local no_proxy_val="${NO_PROXY:-${no_proxy:-}}"
  if [ -n "$no_proxy_val" ]; then
    no_proxy_val="${no_proxy_val},127.0.0.1,localhost,${GRAIN_GITHUB_HOST%%:*}"
  else
    no_proxy_val="127.0.0.1,localhost,${GRAIN_GITHUB_HOST%%:*}"
  fi
  no_proxy_env=(--env "NO_PROXY=${no_proxy_val}" --env "no_proxy=${no_proxy_val}")

  log "Checking whether $GRAIN_TARGET_REPO has any commits yet"
  # The single quotes are the point, not an oversight (SC2016): what is
  # between them is the *container's* sh script, and $GRAIN_TARGET_URL is
  # for that sh to expand out of its own environment. Expanding it here
  # would interpolate the token into an argv this host can read in `ps`,
  # which is exactly what the --env above exists to avoid.
  # shellcheck disable=SC2016
  if [ -n "$(image_run --network host "${no_proxy_env[@]}" --env "GRAIN_TARGET_URL=$url" \
       --entrypoint sh -- -c 'git ls-remote "$GRAIN_TARGET_URL"' 2>/dev/null)" ]; then
    return
  fi

  log "  it's empty -- pushing one empty commit to $GRAIN_TARGET_BRANCH so grain has something to branch from"
  # Same as above: this whole block is the container's script, and its
  # $1, $branch, $tmp and $GRAIN_TARGET_URL all belong to the sh that
  # runs it. Nothing in it may be expanded here (SC2016).
  # shellcheck disable=SC2016
  image_run --network host "${no_proxy_env[@]}" --env "GRAIN_TARGET_URL=$url" --entrypoint sh -- -c '
    set -e
    branch="$1"
    tmp="$(mktemp -d)"
    git init --quiet -b "$branch" "$tmp"
    git -C "$tmp" -c user.name=grain -c user.email=grain@localhost \
      commit --quiet --allow-empty -m "Initial commit (created by grain setup.sh)"
    git -C "$tmp" push --quiet "$GRAIN_TARGET_URL" "HEAD:refs/heads/$branch"
  ' format_target_repo "$GRAIN_TARGET_BRANCH"
}

# --- 8. the systemd unit ---------------------------------------------------

# Every agent framework runs a CLI as a subprocess -- agent/antigravity
# execs `agy`, agent/claude execs `claude`, agent/codex execs `codex` --
# and the deployment image carries all three (Dockerfile). This checks
# that what is about to be deployed actually does, rather than letting
# grain-daemon.service come up and fail at its first dispatch with
# "executable file not found in $PATH".
#
# Asked of the image, not of this host: that is where they live now. The
# exception is an operator-named copy (GRAIN_CLAUDE_PATH/GRAIN_AGY_PATH/
# GRAIN_CODEX_PATH), which is a host path by definition -- docker_run_args
# mounts it in, and a path that names nothing executable out here is
# worth saying out loud here rather than discovering at dispatch.
#
# Never fatal, and reported rather than enforced: which framework a run
# uses is a live UI choice, a deployment may legitimately only ever use
# one of them, and an image missing one still runs everything else. The
# readiness summary says it again at the end.
# `|| true` is load-bearing: `command -v` exits non-zero for a binary
# that is not there, which is exactly the case every caller here exists
# to report -- and a caller assigns this to a variable, where `set -e`
# would turn that report into an aborted deploy.
agent_cli_in_image() {
  image_run --entrypoint sh -- -c "command -v $1" 2>/dev/null | head -n1 || true
}

# cli_run_location is where this install has CLI $1 (claude, codex, agy)
# run: "host" through grain hostd, or "container" -- the daemon's own,
# from its image. The daemon's own resolution (pkg/daemon/clirun.go) for
# a daemon in the release container, from what this script gives it:
# GRAIN_<CLI>_RUN, else the built-in default (container). Without hostd
# there is no host to run on, and resolve_hostd has dropped any host
# already. Settings -> Models can override it after the install.
cli_run_location() {
  local setting=""
  case "$1" in
    claude) setting="$GRAIN_CLAUDE_RUN" ;;
    codex) setting="$GRAIN_CODEX_RUN" ;;
    agy) setting="$GRAIN_AGY_RUN" ;;
  esac
  if [ "${GRAIN_HOSTD:-0}" != "1" ]; then
    echo container
    return
  fi
  case "$setting" in
    host) echo host ;;
    detached) echo detached ;;
    *) echo container ;;
  esac
}

# hostd_cli_path prints the path grain hostd's allowlist runs for its cli
# entry $1, or nothing when it names none. Read from the file rather than
# from $HOSTD_AGY_PATH: a re-run keeps an operator's edits to it. A plain
# reading, as hostd_allowlist_entries is.
hostd_cli_path() {
  [ -r "$HOSTD_ALLOWLIST" ] || return 0
  awk -v want="$1" '
    /^[^[:space:]#][^:]*:/ { sec = $0; sub(/:.*/, "", sec); entry = ""; next }
    sec == "clis" && /^  [A-Za-z0-9_.-]+:/ { entry = $0; sub(/^  /, "", entry); sub(/:.*/, "", entry); next }
    sec == "clis" && entry == want && /^    path:/ {
      p = $0; sub(/^    path:[[:space:]]*/, "", p); sub(/[[:space:]]+#.*$/, "", p)
      gsub(/^["\047]|["\047]$/, "", p)
      print p
      exit
    }' "$HOSTD_ALLOWLIST"
}

# hostd_status is grain hostd's state as it is now, for the readiness
# report: its unit's, and what the hostd on $HOSTD_SOCKET says when asked
# (the first line of `grain hostd -status`: its build and every entry it
# offers) -- or that
# it does not answer, which fails every host run until it does. Asked as
# $GRAIN_USER (grain_cli): hostd answers no other uid.
hostd_status() {
  local unit answer
  unit="$(systemctl is-active grain-hostd.service 2>/dev/null)" || true
  if answer="$(grain_cli hostd -status -socket "$HOSTD_SOCKET" 2>&1)"; then
    # Its first line; the "process" lines after it are hostd_processes'.
    printf 'unit %s; %s\n' "${unit:-unknown}" "$(printf '%s\n' "$answer" | head -n1)"
    return 0
  fi
  case "$answer" in
    *"flag provided but not defined"*)
      printf 'unit %s; not asked -- this build of grain has no "grain hostd -status"\n' "${unit:-unknown}"
      ;;
    *)
      printf 'unit %s; NOT ANSWERING -- %s\n' "${unit:-unknown}" "$(printf '%s\n' "$answer" | head -n1)"
      ;;
  esac
}

# report_agent_cli renders one agent CLI's line for the readiness summary:
# where it runs (cli_run_location) and the binary that runs there, or
# MISSING and why. $1 is the binary, $2 the operator's override for it,
# $3 that override's variable name.
#
# A host run is hostd's allowlist entry: that path, on this host. The
# image's own copy is not what runs then, so it is not what is named.
report_agent_cli() {
  local name="$1" override="$2" var="$3" path
  if [ "$(cli_run_location "$name")" = detached ]; then
    path="$(hostd_cli_path "$name-detached")"
    if [ -z "$path" ]; then
      printf 'container: MISSING its detached entry (%s has no cli entry "%s-detached", so it runs in the daemon'"'"'s container and an upgrade interrupts it)\n' "$HOSTD_ALLOWLIST" "$name"
    elif [ ! -x "$path" ]; then
      printf 'detached, through grain hostd: MISSING (its allowlist entry "%s-detached" names %s, not an executable file on this host)\n' "$name" "$path"
    else
      printf 'detached, through grain hostd: %s (the image'"'"'s, as its allowlist entry "%s-detached")\n' "$path" "$name"
    fi
    return
  fi
  if [ "$(cli_run_location "$name")" = host ]; then
    path="$(hostd_cli_path "$name")"
    if [ -z "$path" ]; then
      printf 'host, through grain hostd: MISSING (%s has no cli entry "%s")\n' "$HOSTD_ALLOWLIST" "$name"
    elif [ ! -x "$path" ]; then
      printf 'host, through grain hostd: MISSING (its allowlist entry "%s" names %s, not an executable file on this host)\n' "$name" "$path"
    else
      printf 'host, through grain hostd: %s (its allowlist entry "%s")\n' "$path" "$name"
    fi
    return
  fi
  if [ -n "$override" ]; then
    if [ -x "$override" ]; then
      printf 'container: %s (mounted from this host)\n' "$override"
    else
      printf 'container: MISSING (%s names nothing executable on this host)\n' "$var"
    fi
    return
  fi
  local cache_set="CACHED_AGENT_CLI_IN_IMAGE_SET_${name}" cache_val="CACHED_AGENT_CLI_IN_IMAGE_${name}"
  if [ "${!cache_set:-0}" = "1" ]; then
    path="${!cache_val:-}"
  else
    path="$(agent_cli_in_image "$name")"
  fi
  if [ -n "$path" ]; then
    printf 'container: %s (in %s)\n' "$path" "$GRAIN_IMAGE_REF"
  else
    printf 'container: MISSING\n'
  fi
}

verify_agent_cli() {
  local name path override
  for name in agy claude codex; do
    case "$name" in
      agy) override="$GRAIN_AGY_PATH" ;;
      claude) override="$GRAIN_CLAUDE_PATH" ;;
      codex) override="$GRAIN_CODEX_PATH" ;;
    esac
    if [ -n "$override" ]; then
      if [ -x "$override" ]; then
        continue
      fi
      log "WARNING: the $name path this deployment was given ($override) is not an executable"
      log "         file on this host, so a dispatch using it will fail until it is one."
      continue
    fi
    path="$(agent_cli_in_image "$name")"
    printf -v "CACHED_AGENT_CLI_IN_IMAGE_${name}" '%s' "$path"
    printf -v "CACHED_AGENT_CLI_IN_IMAGE_SET_${name}" '%s' "1"
    [ -n "$path" ] && continue
    log "WARNING: $GRAIN_IMAGE_REF carries no \"$name\" binary. The agent framework that runs"
    log "         it as a subprocess cannot dispatch until it does -- deploy an image built"
    log "         with it (Dockerfile installs every agent CLI), or set the matching"
    log "         GRAIN_AGY_PATH/GRAIN_CLAUDE_PATH/GRAIN_CODEX_PATH to a copy on this host."
  done
}

# daemon_flag_spec is what the build this deploy will run says about its
# own command line: one line per flag, "<name> bool" or "<name> value"
# (pkg/cli/daemon_unknown_flags.go's printDaemonFlagNames).
#
# Empty when the build cannot be asked -- an image older than the
# subcommand, which answers with a usage error, or a docker run this host
# cannot do. Callers treat that as "not checked" rather than as "accepts
# nothing": every flag in the unit would otherwise be reported as
# unknown, which is a false alarm on the one check nobody can afford to
# learn to ignore.
#
# `|| true` for the same reason agent_cli_in_image has it: a caller
# assigns this, and under `set -e` a non-zero exit from the build being
# too old to answer would end the install.
daemon_flag_spec() {
  image_run -- daemon -flag-names 2>/dev/null || true
}

# daemon_spec_defines says whether $DAEMON_FLAG_SPEC (daemon_flag_spec's
# answer, read once by write_systemd_units) names daemon flag $1.
daemon_spec_defines() {
  printf '%s\n' "${DAEMON_FLAG_SPEC:-}" | awk -v n="$1" '$1 == n { found = 1 } END { exit !found }'
}

# verify_daemon_flags reports any flag this script is about to write into
# grain-daemon.service that the build behind the unit does not define.
#
# This is the installer's half of grain/task-233. The unit and the image
# move separately -- this script writes the ExecStart once and every
# upgrade afterwards only repoints the image ref file it reads -- so they
# can disagree in both directions, and the deployment has met both:
#
#   an image ahead of the unit   an upgrade pulls a build that has
#                                dropped a flag this host's unit still
#                                passes. That is the 52-restart crash
#                                loop of 2026-09-18, and it is now
#                                reported by the upgrade before it cuts
#                                over (pkg/upgrade's "unit flags" step)
#                                and survived by the daemon when it
#                                happens anyway.
#   a unit ahead of the image    *this* case: a newer copy of this script
#                                writing a flag into the unit against an
#                                older image -- a pinned GRAIN_IMAGE, a
#                                rollback, a host being reinstalled onto
#                                the build it was on. Nothing else would
#                                notice: the install reports success and
#                                the daemon comes up with a setting
#                                silently not in force.
#
# Reported rather than enforced, like verify_agent_cli above: an operator
# pinning an older image usually means it, and the flag they lose is
# named here rather than made into a failed deploy. The daemon says the
# same thing at every start and the Sandboxes pane keeps saying it
# (ui.DaemonHealth.UnknownFlags).
verify_daemon_flags() {
  local args=("$@") spec unknown=() i=0 token name kind attached
  # write_systemd_units has usually asked already: one image run, not two.
  if [ -n "${DAEMON_FLAG_SPEC_READ:-}" ]; then
    spec="$DAEMON_FLAG_SPEC"
  else
    spec="$(daemon_flag_spec)"
  fi
  if [ -z "$spec" ]; then
    log "  ($GRAIN_IMAGE_REF cannot list its daemon flags, so this unit's flags are not checked against it)"
    return 0
  fi

  # The tokens are read the way the daemon reads them: a flag that takes
  # a value takes the token after it whatever that token looks like.
  # `-granule-create-arg -disk-mode=overlay` passes granule's own
  # argument as a value, and a reader that skipped that rule would report
  # it as a grain flag that does not exist.
  while [ "$i" -lt "${#args[@]}" ]; do
    token="${args[$i]}"
    i=$((i + 1))
    case "$token" in
      -*) ;;
      *) continue ;;
    esac
    name="${token#-}"
    name="${name#-}"
    attached=0
    case "$name" in
      *=*) attached=1 ;;
    esac
    name="${name%%=*}"
    kind="$(printf '%s\n' "$spec" | awk -v n="$name" '$1 == n { print $2; exit }')"
    if [ -z "$kind" ]; then
      unknown+=("-$name")
      continue
    fi
    if [ "$kind" = value ] && [ "$attached" = 0 ]; then
      i=$((i + 1))
    fi
  done

  # `if` rather than `[ ... ] && return`, for the reason written down
  # beside daemon_args above: under `set -e` a bare test that comes out
  # false is a failed command.
  if [ "${#unknown[@]}" -eq 0 ]; then
    return 0
  fi
  log "WARNING: this unit passes ${unknown[*]}, which $GRAIN_IMAGE_REF does not define."
  log "         The daemon will start and ignore ${#unknown[@]} setting(s) this deployment"
  log "         asked for. The unit and the image have drifted apart -- an older image under"
  log "         a newer installer, usually a pinned GRAIN_IMAGE or a rollback. Deploy the"
  log "         image these flags belong to, or re-run this script once you are on it."
}

write_systemd_units() {
  verify_agent_cli
  log "Writing grain-daemon.service"

  local daemon_args=(
    daemon
    -data-dir "$GRAIN_DATA_DIR"
    -sandbox-dir "$GRAIN_SANDBOX_DIR"
    -max-workers "$GRAIN_MAX_WORKERS"
    -max-mergers "$GRAIN_MAX_MERGERS"
    -poll-interval "$GRAIN_POLL_INTERVAL"
    -gemini-api-key-file "$GRAIN_DATA_DIR/secrets/gemini-api-key"
    -claude-oauth-token-file "$GRAIN_DATA_DIR/secrets/claude-oauth-token"
    -openai-api-key-file "$GRAIN_DATA_DIR/secrets/openai-api-key"
    -github-host "$GRAIN_GITHUB_HOST"
    -ui-addr "$GRAIN_UI_ADDR"
  )

  # Extra Host names the UI answers to (ui.HostGuard): a reverse proxy or
  # DNS name in front of the loopback listener has to be named here, or
  # its requests are refused with 421 as DNS rebinding would be.
  if [ -n "$GRAIN_UI_ALLOWED_HOSTS" ]; then
    daemon_args+=(-ui-allowed-hosts "$GRAIN_UI_ALLOWED_HOSTS")
  fi

  # Tenancy. This build is single-tenant only, and has no -tenancy to
  # pass. Asked of the build first (grain/task-4358): one from before the
  # single-tenant engine -- v1.9.0, the default channel's image when this
  # was written -- is a multi-tenant daemon with no way to be anything
  # else, so it is set up as one. -ui-access-session-lifetime came with
  # the access code, and v1.9.0 does not define it.
  DAEMON_FLAG_SPEC="$(daemon_flag_spec)"
  DAEMON_FLAG_SPEC_READ=1
  DAEMON_PREDATES_TENANCY=""
  if [ -n "$DAEMON_FLAG_SPEC" ] && ! daemon_spec_defines ui-access-session-lifetime; then
    DAEMON_PREDATES_TENANCY=1
    log "WARNING: $GRAIN_IMAGE_REF predates the single-tenant engine and is always multi-tenant:"
    log "         setting up a sign-in account as for one. Deploy a current build, or re-run this"
    log "         script once you are on one."
  fi

  if [ -n "$GRAIN_CODEX_AUTH_FILE" ]; then
    daemon_args+=(-codex-auth-file "$GRAIN_CODEX_AUTH_FILE")
  fi

  # The local policy (-policy, see GRAIN_POLICY above).
  if [ -n "$GRAIN_POLICY" ]; then
    daemon_args+=(-policy "$GRAIN_POLICY")
  fi

  if [ -n "$GRAIN_IAP_AUDIENCE" ]; then
    daemon_args+=(-ui-iap-audience "$GRAIN_IAP_AUDIENCE")
  fi

  if [ -n "$GRAIN_DB_DRIVER" ] && [ "$GRAIN_DB_DRIVER" != "sqlite" ]; then
    daemon_args+=(-db-driver "$GRAIN_DB_DRIVER")
  fi
  if [ -n "$GRAIN_DB_DSN" ]; then
    daemon_args+=(-db-dsn "$GRAIN_DB_DSN")
  fi

  # The CI job cache's bounds, each passed only when this host asked for
  # one: the daemon's own default for each flag is grain's constant, so
  # saying nothing is exactly the sizing every deployment already had,
  # and a flag left out is one fewer number in this unit to go stale
  # (grain/task-362).
  #
  # `if` rather than `[ -n ... ] && ...`, deliberately: this script runs
  # under `set -e`, where a bare test that comes out false is a failed
  # command and ends the install.
  if [ -n "$GRAIN_CI_CACHE_MAX_AGE" ]; then
    daemon_args+=(-ci-cache-max-age "$GRAIN_CI_CACHE_MAX_AGE")
  fi
  if [ -n "$GRAIN_CI_CACHE_MAX_BYTES" ]; then
    daemon_args+=(-ci-cache-max-bytes "$GRAIN_CI_CACHE_MAX_BYTES")
  fi
  if [ -n "$GRAIN_CI_CACHE_MAX_ENTRIES" ]; then
    daemon_args+=(-ci-cache-max-entries "$GRAIN_CI_CACHE_MAX_ENTRIES")
  fi
  if [ -n "$GRAIN_CI_CACHE_MAX_ENTRY_BYTES" ]; then
    daemon_args+=(-ci-cache-max-entry-bytes "$GRAIN_CI_CACHE_MAX_ENTRY_BYTES")
  fi
  if [ -n "$GRAIN_SANDBOX_PORT_BASE" ]; then
    daemon_args+=(-sandbox-port-base "$GRAIN_SANDBOX_PORT_BASE")
  fi
  # bwsalmon/agents#396: the UI's own Upgrade button, which on a
  # container deployment means "pull the image CI published for that
  # branch and restart onto it" rather than "fetch, build, install"
  # (bwsalmon/agents#645, pkg/upgrade/image.go). -upgrade-image names
  # the repository CI publishes a tag per branch to; -upgrade-image-ref-
  # file is the request file this unit's own pre-step reads, so an
  # upgrade repoints the service by writing one line and restarting, with
  # no unit to rewrite. Root's part is only to take that line as data and
  # copy it across if it names an image in that same repository
  # (write_image_ref_promoter, #2405).
  #
  # -upgrade-src-dir is not passed at all. It used to ride along, not to
  # build (with -upgrade-image set the daemon never does) but because
  # grantTools read the checkout it named for the self-debug capability.
  # The deployment image now carries the source it was built from
  # (Dockerfile), and pkg/cli/daemon.go's sourceDir prefers that copy
  # over any flag -- which is the point: this checkout tracks a branch and
  # the image is a fixed tag, so the two drifted apart on every upgrade
  # and the agent read source that was not what was running.
  #
  # Left unset entirely when GRAIN_ENABLE_UI_UPGRADE=0
  # (terraform/gcp's own deploy.sh sets exactly that): the daemon
  # flags themselves default to empty/disabled (pkg/cli/daemon.go), so
  # simply not passing them is enough -- see this script's own header on
  # GRAIN_ENABLE_UI_UPGRADE (bwsalmon/agents#405).
  if [ "$GRAIN_ENABLE_UI_UPGRADE" = "1" ]; then
    daemon_args+=(
      -upgrade-image "$GRAIN_IMAGE"
      -upgrade-image-ref-file "$IMAGE_REF_FILE"
      -upgrade-restart-cmd touch -upgrade-restart-cmd "$CONTROL_DIR/restart"
    )
    # Who published what an upgrade pulls (docs/image-signing.md): the
    # same `cosign verify` this script ran against the image it pulled,
    # against the same key, asked by the daemon before its own health
    # check runs the next one. One argument per flag, attached with `=`
    # so that --key=... is never read as a flag of grain's own. cosign is
    # the image's own (Dockerfile) in a container and the host's in host
    # mode; the key is IMAGE_VERIFY_KEY_FILE, mounted at the same path in
    # the one and simply there in the other.
    if [ -n "$GRAIN_IMAGE_VERIFY_KEY" ]; then
      daemon_args+=(
        -upgrade-verify-cmd=cosign
        -upgrade-verify-cmd=verify
        "-upgrade-verify-cmd=--key=$IMAGE_VERIFY_KEY_FILE"
        -upgrade-verify-cmd=--insecure-ignore-tlog=true
      )
      verify_image_carries_cosign
    fi
  fi
  # Which branches those upgrades may install, as resolve_upgrade_allowed_
  # branches settled it before anything was written: given, or carried
  # over from the unit this one replaces. Passed whether or not the
  # Upgrade button is wired up, so a run with GRAIN_ENABLE_UI_UPGRADE=0
  # does not drop an allowlist the next run that turns it back on would
  # want. Empty passes nothing and leaves the build's own default.
  if [ -n "$GRAIN_UPGRADE_ALLOWED_BRANCHES" ]; then
    daemon_args+=(-upgrade-allowed-branches "$GRAIN_UPGRADE_ALLOWED_BRANCHES")
  fi

  # The UI's reboot-host button, through the same control channel: a
  # touch out of the container, a path unit out here (write_control_units)
  # that turns it into `systemctl reboot`. Always passed -- unlike the
  # Upgrade button this has no opt-out, and its default
  # (`sudo systemctl reboot`, pkg/cli/daemon.go's defaultRebootCmd) is
  # the one thing that cannot work from inside a container.
  daemon_args+=(-reboot-cmd touch -reboot-cmd "$CONTROL_DIR/reboot")

  # And its two neighbours in that pane (grain/task-40), through the same
  # channel and for the same reason their defaults cannot work in here:
  # "shut down host", which grain-shutdown.path turns into `systemctl
  # poweroff`, and "restart grain", which reuses the restart request the
  # Upgrade button already writes -- one file, one unit, one command
  # (`systemctl restart grain-daemon.service`), whether an upgrade asked
  # for it or an operator did.
  daemon_args+=(-shutdown-cmd touch -shutdown-cmd "$CONTROL_DIR/shutdown")
  daemon_args+=(-restart-cmd touch -restart-cmd "$CONTROL_DIR/restart")

  [ -n "$GRAIN_AGY_PATH" ] && daemon_args+=(-agy-path "$GRAIN_AGY_PATH")
  [ -n "$GRAIN_CLAUDE_PATH" ] && daemon_args+=(-claude-path "$GRAIN_CLAUDE_PATH")
  [ -n "$GRAIN_CODEX_PATH" ] && daemon_args+=(-codex-path "$GRAIN_CODEX_PATH")
  [ -n "$GRAIN_CLAUDE_RUN" ] && daemon_args+=(-claude-run "$GRAIN_CLAUDE_RUN")
  [ -n "$GRAIN_CODEX_RUN" ] && daemon_args+=(-codex-run "$GRAIN_CODEX_RUN")
  [ -n "$GRAIN_AGY_RUN" ] && daemon_args+=(-agy-run "$GRAIN_AGY_RUN")
  [ "${GRAIN_HOSTD:-0}" = "1" ] && daemon_args+=(-hostd-socket "$HOSTD_SOCKET")
  [ -n "$GRAIN_MAX_AGENT_TURNS" ] && daemon_args+=(-max-agent-turns "$GRAIN_MAX_AGENT_TURNS")
  [ "$GRAIN_GITHUB_INSECURE_HTTP" = "1" ] && daemon_args+=(-github-insecure-http)
  [ -n "$GRAIN_GCP_PROJECT" ] && daemon_args+=(-gcp-project "$GRAIN_GCP_PROJECT")
  [ -n "$GRAIN_GCP_SERVICE_ACCOUNT_EMAIL" ] && daemon_args+=(-gcp-agent-service-account "$GRAIN_GCP_SERVICE_ACCOUNT_EMAIL")
  [ -n "$GRAIN_IAP_RESOURCE" ] && daemon_args+=(-iap-resource "$GRAIN_IAP_RESOURCE")
  [ -n "$GRAIN_TARGET_REPO" ] && daemon_args+=(-default-target-repo "$GRAIN_TARGET_REPO")
  if [ -n "$GRAIN_SANDBOX_CPUS" ]; then
    daemon_args+=(-sandbox-cpus "$GRAIN_SANDBOX_CPUS")
  fi
  if [ -n "$GRAIN_SANDBOX_MEMORY_MB" ]; then
    daemon_args+=(-sandbox-memory-mb "$GRAIN_SANDBOX_MEMORY_MB")
  fi
  if [ -n "$GRAIN_SANDBOX_DISK_GB" ]; then
    daemon_args+=(-sandbox-disk-gb "$GRAIN_SANDBOX_DISK_GB")
  fi

  # Local containers use the same Docker bridge path back to Grain as
  # granule's VM containers. Pass their companion configuration even when
  # granule is the initial backend so Settings can offer both isolated
  # methods after the installer has pulled the shared sandbox image.
  daemon_args+=(-container-git-proxy-host "$GRAIN_GRANULE_GIT_PROXY_HOST")

  # -granule-sandboxes is what actually selects orchestrator.
  # GranuleSandboxes over ContainerSandboxes (pkg/cli/daemon.go's run()); the
  # VM name itself is not configurable, since a VM name has 11 bytes to
  # live in and a run id needs nine of them (orchestrator.VMNamePrefix) --
  # only ever passed once ensure_granule_images/ensure_granule_kvm_access
  # have both actually succeeded this run (each resets
  # GRAIN_GRANULE_ENABLE to 0 on its own failure), so a host that cannot
  # yet dispatch onto a real VM keeps dispatching into host directories
  # instead of installing a daemon that would fail every task. -backend
  # docker needs no granule setup/containerd/CNI/kubelet on this host
  # (bwsalmon/agents#353). The sandbox container ensure_granule_oci_image
  # just pulled is named to the daemon rather than left to granule's own
  # localhost:5000/granule:latest default, which is what a locally built
  # image used to be retagged to (bwsalmon/agents#645: there is no local
  # build to retag any more, and naming the pulled reference says out loud
  # which sandbox this deployment runs) -- but only by a deployment that
  # *pinned* one, and as -sandbox-image rather than as a create arg. The
  # default is left to the daemon, which stamps in the same reference this
  # script would have resolved and re-resolves it on every start, so that
  # an upgrade moves grain and its sandbox together rather than leaving the
  # unit naming the guest of the commit this host was installed at
  # (GRAIN_GRANULE_OCI_IMAGE_PINNED).
  # No -disk/-kernel/-initramfs, and no -images-hostpath to resolve them
  # against: the guest travels inside -granule-image, so granule boots
  # what that image carries. Those five flags, and the two host
  # directories behind them, were the whole apparatus for handing a VM a
  # disk this script had built or fetched.
  # No -guest-port either, and nothing is lost by that: granule's own flag
  # is "deprecated and ignored: nothing forwards to a fixed in-guest port
  # any more". It used to matter because granule's default was 80, which
  # silently refused every connection to this image's sshd -- and that is
  # also why scripts/granule/README.md still has a "guest-port 22 is not
  # optional" section describing a granule this image no longer carries.
  # -disk-mode=overlay gives each VM a writable root that costs nothing
  # to create: the guest writes into a thin qcow2 inside its own
  # container, backed by the image's disk, which is only ever read
  # (bwsalmon/granule#37). granule's default is readonly, which a
  # dispatched task cannot use.
  if [ "$GRAIN_GRANULE_ENABLE" = "1" ]; then
    daemon_args+=(
      -granule-sandboxes
      -granule-ssh-user "$GRAIN_GRANULE_SSH_USER"
      -granule-workspace "$GRAIN_GRANULE_WORKSPACE"
      -granule-git-proxy-host "$GRAIN_GRANULE_GIT_PROXY_HOST"
      -granule-create-arg -disk-mode=overlay
      # granule authorizes this boot's generated key for root; the daemon
      # execs as GRAIN_GRANULE_SSH_USER, so that account has to be named
      # too. One flag rather than two settings: granule puts it on the
      # VM container as GRANULE_EXEC_USER, where `granule run` reads it to
      # authorize the account and `granule exec` reads it to log in as, so
      # the two cannot disagree.
      -granule-create-arg -guest-user -granule-create-arg "$GRAIN_GRANULE_SSH_USER"
    )
    # Only when this deployment pinned one -- see the block comment above
    # and GRAIN_GRANULE_OCI_IMAGE_PINNED's own. -sandbox-image rather than
    # a -granule-create-arg -granule-image, because the daemon has to be
    # able to tell a pin somebody meant from the one this script used to
    # write for every host at all: it reads a -granule-image naming another
    # tag of its own stamp's repository as that install-time leftover and
    # uses the stamp instead (pkg/cli/daemon.go's chooseSandboxImage).
    if [ -n "$GRAIN_GRANULE_OCI_IMAGE_PINNED" ]; then
      daemon_args+=(-sandbox-image "$GRAIN_GRANULE_OCI_IMAGE")
    fi
    # Only when this deployment named one: granule's own default is a
    # public resolver, and passing nothing is how a deployment says
    # "that one". See GRAIN_GRANULE_DNS above for why a guest cannot
    # simply use this host's resolver.
    if [ -n "$GRAIN_GRANULE_DNS" ]; then
      daemon_args+=(-granule-create-arg -dns -granule-create-arg "$GRAIN_GRANULE_DNS")
    fi
    # Nothing is passed for addressing or a forwarded guest port any more.
    # The granule this image vendors deprecates every flag that block used
    # to set -- "-ip" and "-port" are "deprecated and ignored" because the
    # container runtime assigns the address a guest takes over, and
    # "-guest-port" is "deprecated and ignored: nothing forwards to a fixed
    # in-guest port any more" -- so it configured nothing for some time
    # before being removed from here.
    #
    # GRAIN_GRANULE_NET/_BASE_IP/_BASE_PORT are still accepted so an
    # existing deployment's environment does not fail a re-run, and a value
    # that asked for something is said once rather than dropped quietly.
    # They go away in a later release.
    if [ "$GRAIN_GRANULE_NET" != "flat" ]; then
      log "  warning: GRAIN_GRANULE_NET=$GRAIN_GRANULE_NET is ignored -- the vendored granule has only"
      log "           the former \"flat\" behaviour left, and it is the default. Unset it; it goes away"
      log "           in a later release"
    fi
  elif [ "$GRAIN_HOST_SANDBOXES" = "1" ]; then
    # Only ever reached by asking: GRAIN_HOST_SANDBOXES=1 is never a
    # fallback, and check_host_sandboxes has already refused it wherever
    # the daemon would. -sandbox-dir, which it needs, is passed above.
    daemon_args+=(-host-sandboxes)
    if [ "$GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES" = "1" ]; then
      daemon_args+=(-allow-unisolated-host-sandboxes)
    fi
    local env_entry env_entries
    read -ra env_entries <<< "$GRAIN_HOST_SANDBOX_ENV"
    for env_entry in "${env_entries[@]}"; do
      daemon_args+=(-host-sandbox-env "$env_entry")
    done
  else
    # The only other backend this script installs: the guard near the
    # top refuses a run with none, and granule_unavailable turns
    # this on whenever it turns granule off.
    daemon_args+=(-container-sandboxes)
  fi

  # Before the unit is written: does the build behind it accept what
  # is about to be written into it (verify_daemon_flags)?
  verify_daemon_flags "${daemon_args[@]}"


  docker_run_args

  # The unit runs `docker run` as root, and the container runs as
  # $GRAIN_USER: `docker run --user` is what makes the daemon
  # unprivileged, and the client needs to reach a root-owned socket to
  # ask for that. So the account this service *starts* as is root and the
  # account grain *is* is unchanged from before any of this ran in a
  # container -- no User= here, --user in docker_run_args instead.
  #
  # --rm plus an ExecStartPre `rm -f` is the pair that makes a restart
  # deterministic: the container is named (so ExecStop and an operator's
  # `docker logs grain-daemon` can find it), and a name outliving a hard
  # kill would otherwise make the next start fail with "name already in
  # use" forever. The `-` prefix makes that pre-step's failure -- the
  # ordinary case, where there is nothing to remove -- not a failure of
  # the unit.
  #
  # ExecStop stops the container rather than leaving systemd to kill the
  # client: `docker run` proxies signals, but a stop that goes through
  # the daemon is what actually gives the container its full
  # TimeoutStopSec to shut down instead of racing the client's death.
  #
  # --time 300 and TimeoutStopSec=330 are the daemon's own shutdown drain
  # (pkg/daemon's shutdownDrain, 4m30s) plus 30s for everything around
  # it, and docker's own stop on top of that: a smaller number here cuts
  # the drain off part way, kills the runs still releasing their
  # sandboxes, and leaves their rows and VMs for the next start to
  # recover. pkg/daemon's TestStopTimeoutsCoverTheShutdownDrain holds the
  # three together.
  #
  # Requires=docker.service, not merely After=: this unit is a docker
  # client, and a boot where the engine never came up should leave
  # grain-daemon failed and legible rather than restarting forever
  # against a missing socket.
  #
  # ${UNIT_IMAGE_REF_FILE} carries one GRAIN_IMAGE=<ref> line, which is
  # why the image below is a variable and not a literal: the unit never
  # has to be rewritten to change what runs. It is root's, not
  # $GRAIN_USER's: an upgrade writes its request into ${IMAGE_REF_FILE}
  # (write_image_ref, pkg/upgrade/image.go), and the first ExecStartPre
  # copies a valid one across (write_image_ref_promoter) -- systemd reads
  # an EnvironmentFile afresh for each command it starts, so the
  # ExecStart after it runs what was just promoted. Nothing $GRAIN_USER
  # can write is an EnvironmentFile here, so GRAIN_EXTRA_DOCKER_RUN_ARGS
  # can only ever come from the operator's root-owned
  # /etc/default/grain-daemon (#2405).
  #
  # Behind grain hostd the unit also Wants= hostd and starts after it
  # (write_hostd_units), and its second pre-step brings the host's own
  # grain to the image about to run (install_hostd_binary): a host-run
  # agy's MCP server is that binary. Neither ties hostd to this unit's
  # lifecycle -- restarting the daemon leaves a running hostd alone.
  write_image_ref_promoter
  write_hostd_units
  local hostd_after="" hostd_wants="" hostd_extract=""
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    hostd_after=" grain-hostd.service"
    hostd_wants=" grain-hostd.service"
    hostd_extract="ExecStartPre=${HOST_EXTRACTOR}"
  fi
  cat > /etc/systemd/system/grain-daemon.service <<UNIT
[Unit]
Description=grain daemon (task orchestrator, UI and API), from ${GRAIN_IMAGE}
After=network-online.target docker.service${hostd_after}
Wants=network-online.target${hostd_wants}
Requires=docker.service

[Service]
Type=simple
EnvironmentFile=${UNIT_IMAGE_REF_FILE}
EnvironmentFile=-/etc/default/grain-daemon
ExecStartPre=${IMAGE_REF_PROMOTER}
${hostd_extract}
ExecStartPre=-$DOCKER_BIN rm -f grain-daemon
ExecStart=$DOCKER_BIN run --name grain-daemon ${DOCKER_ARGS[*]} \$GRAIN_EXTRA_DOCKER_RUN_ARGS \${GRAIN_IMAGE} ${daemon_args[*]}
ExecStop=-$DOCKER_BIN stop --time 300 grain-daemon
Restart=on-failure
RestartSec=5
TimeoutStopSec=330

[Install]
WantedBy=multi-user.target
UNIT

  write_control_units
  # Written beside the daemon's own unit, and removed by the same call on
  # a re-run that no longer asks for it -- one daemon-reload covers both.
  systemctl daemon-reload
}

# HOST_NFT_SUDOERS is the one sudoers rule host mode (since removed)
# wrote, letting its daemon run nft for a granule NetworkNone sandbox. A
# container-mode daemon has a file capability on the image's nft instead
# (docker_run_args), so install_cli_wrappers removes the rule from a
# migrating install.
HOST_NFT_SUDOERS=/etc/sudoers.d/zzz-grain-nft

# docker_run_args fills DOCKER_ARGS with everything grain-daemon.service
# hands `docker run` before the image name. It is one list, built here
# rather than written into the unit by hand, because every entry answers
# the same question: what does a process that used to be a plain systemd
# service on this host still need to see once it is in a container?
#
#   --cpu-shares 2048    twice Docker's default relative CPU weight, so
#                        grain keeps enough time to serve the UI when
#                        sibling sandbox containers saturate the host.
#                        This is deliberately a share rather than a cap:
#                        grain can still use otherwise-idle CPUs for its
#                        own bursts.
#   --pid host           the Top pane must list every process contributing
#                        to the host-wide CPU figures procps reads from
#                        /proc/stat, not only grain-daemon's container.
#   --network host       the UI binds -ui-addr and the git proxy binds an
#                        address every sandbox (a granule VM in its own
#                        netns included) has to reach. A bridged network
#                        would put both behind a NAT that neither the
#                        load balancer in front of this host nor a
#                        sandbox VM knows how to cross.
#   --user               $GRAIN_USER's own uid:gid, so the store, the
#                        secrets database and every sandbox working tree
#                        come out owned exactly as they were before.
#   --cap-add NET_BIND_SERVICE
#                        always, not only with a privileged -ui-addr
#                        port: the package cache binds 53, 443 and 80
#                        too, and nothing at install time says whether
#                        it will be switched on. The image gives the
#                        grain binary the matching file capability
#                        (Dockerfile), which is what turns a
#                        bounding-set entry into an actual grant for a
#                        non-root process -- and grants it to that binary
#                        alone, not to a task's own `bash -c`.
#   the data/sandbox/src mounts
#                        the three directories the daemon reads and
#                        writes, each at the very path it has out here so
#                        that a path in a log line, a flag or an error
#                        means the same thing in both places.
#   the journal mounts   pkg/systemlog.Journalctl shells out to
#                        journalctl for the UI's Logs pane; it needs the
#                        journal files and the machine-id that names
#                        them. Read-only, and whichever of the two
#                        journal directories this host actually uses
#                        (/var/log/journal when persistent storage is on,
#                        /run/log/journal when it is not). Paired with a
#                        --group-add of systemd-journal's GID -- the files
#                        just mounted in are group-readable by that group
#                        alone, and $GRAIN_USER's membership in it on the
#                        host (were it granted there, the way it used to
#                        be) would not reach a process the container only
#                        ever gave two bare uid:gid numbers.
#   the docker socket    granule (granule, and pkg/mcp's docker-exec
#                        transport) and the Upgrade button's own `docker
#                        pull` both talk to this host's engine. Mounted
#                        only when one of those is actually turned on --
#                        this is the one entry here that grants the
#                        container root-equivalent authority over the
#                        host, so it is not given for free to a
#                        deployment that has no use for it.
#   the granule mount     granule records its VMs in /var/lib/granule,
#                        and the paths it then hands the host's docker
#                        daemon as bind mounts are those same host paths
#                        -- which is why it is mounted at its own path.
#                        The image and disk-overlay directories that used
#                        to be mounted beside it are gone: the guest
#                        travels inside the sandbox image and its overlay
#                        is created in the VM's own container.
#   the verify key       IMAGE_VERIFY_KEY_FILE, read-only, when
#                        GRAIN_IMAGE_VERIFY_KEY is set and the Upgrade
#                        button is on: the public key the daemon's own
#                        -upgrade-verify-cmd checks signatures against.
#   GRAIN_CLAUDE_PATH/GRAIN_AGY_PATH/GRAIN_CODEX_PATH
#                        an operator's own agent CLI, mounted (with the
#                        directory around it, since a CLI is rarely one
#                        lone file) at the path they named, because a
#                        path passed as -claude-path/-agy-path has to
#                        resolve inside the container.
#   GRAIN_EXTRA_DOCKER_ARGS
#                        the escape hatch for whatever this list does not
#                        anticipate -- another mount, another device,
#                        another environment variable -- so that needing
#                        one does not mean editing this script.
docker_run_args() {
  local uid gid docker_gid journal_gid
  uid="$(id -u "$GRAIN_USER")"
  # id -g of the *user*, not of $GRAIN_GROUP: `id` takes an account name,
  # and $GRAIN_GROUP is a group. The two spellings agree for the account
  # this script creates, whose group is its own name, and the wrong one
  # fails with "id: 'staff': no such user" for exactly the account
  # GRAIN_GROUP exists to support. The number is the same either way --
  # the account's primary gid is its primary group's.
  gid="$(id -g "$GRAIN_USER")"

  DOCKER_ARGS=(
    --rm
    --cpu-shares 2048
    --oom-score-adj -900
    --pid host
    --network host
    --user "${uid}:${gid}"
    --env "HOME=${GRAIN_DATA_DIR}/home"
    --volume "${GRAIN_DATA_DIR}:${GRAIN_DATA_DIR}"
    --volume "${GRAIN_SANDBOX_DIR}:${GRAIN_SANDBOX_DIR}"
  )

  # Whatever -ui-addr says: the UI's port is not the only privileged one
  # this daemon binds, and it is the one that matters least. The package
  # cache's DNS interception binds 53 and the listeners it redirects a
  # fetch to bind 443 and 80 (pkg/cli/packagecache.go), and the cache is
  # a live Settings toggle with no installer flag to condition this on.
  # This puts the capability in the container's bounding set, which is all
  # a non-root process needs for the image's file capability on the grain
  # binary to be raised from -- and that file capability is what keeps the
  # grant to that one binary rather than to a task's own `bash -c`.
  DOCKER_ARGS+=(--cap-add NET_BIND_SERVICE)

  [ -f /etc/machine-id ] && DOCKER_ARGS+=(--volume /etc/machine-id:/etc/machine-id:ro)
  if [ -d /var/log/journal ] || [ -d /run/log/journal ]; then
    [ -d /var/log/journal ] && DOCKER_ARGS+=(--volume /var/log/journal:/var/log/journal:ro)
    [ -d /run/log/journal ] && DOCKER_ARGS+=(--volume /run/log/journal:/run/log/journal:ro)
    # The journal files this just mounted in are root:systemd-journal,
    # mode 0640 -- unreadable to the container's own $GRAIN_USER without
    # its numeric GID, the same --group-add docker_gid already does for
    # the docker socket a few lines down. Permission is a kernel-level
    # GID check, so it does not matter that nothing inside the image's
    # own /etc/group names this GID systemd-journal (or anything at all).
    # `|| true`: under pipefail a host with no such group is an empty
    # answer, not the end of the install.
    journal_gid="$(getent group systemd-journal | cut -d: -f3 || true)"
    [ -n "$journal_gid" ] && DOCKER_ARGS+=(--group-add "$journal_gid")
  fi

  if [ "$GRAIN_GRANULE_ENABLE" = "1" ] || [ "$GRAIN_CONTAINER_SANDBOXES" = "1" ] || [ "$GRAIN_ENABLE_UI_UPGRADE" = "1" ]; then
    DOCKER_ARGS+=(--volume /var/run/docker.sock:/var/run/docker.sock)
    # The socket is root:docker 0660, and the container is not root:
    # without the group, every docker call from in there is a permission
    # denied. --group-add is the container-side equivalent of the docker
    # group membership this used to grant $GRAIN_USER on the host.
    docker_gid="$(getent group docker | cut -d: -f3 || true)"
    [ -n "$docker_gid" ] && DOCKER_ARGS+=(--group-add "$docker_gid")
  fi

  if [ "$GRAIN_GRANULE_ENABLE" = "1" ]; then
    DOCKER_ARGS+=(--volume /var/lib/granule:/var/lib/granule)
    # The bounding-set entry the image's file capability on /usr/sbin/nft
    # is raised from (Dockerfile), for the host nftables chain a granule
    # NetworkNone sandbox is refused without
    # (pkg/orchestrator/granule_firewall.go). Only with granule on: no
    # other backend writes host firewall rules. Any process in the
    # container can run that nft against this host's own firewall, since
    # the container is --network host; the docker socket mounted above
    # already gives the same processes more than that.
    DOCKER_ARGS+=(--cap-add NET_ADMIN)
  fi

  [ -d /cloudsql ] && DOCKER_ARGS+=(--volume /cloudsql:/cloudsql)

  # grain hostd's socket directory, at the path it has out here
  # (write_hostd_units): the directory rather than the socket, so a hostd
  # restart's new socket is the one the running daemon sees. /etc/grain,
  # where the allowlist is, is deliberately not mounted.
  #
  # And TMPDIR, onto the scratch disk: a host run is staged inside this
  # container (agent.HostdHost stages as LocalHost does) and run out
  # there, so every file it is handed has to be on a mount both sides
  # share at one path. The agy HOME and session store already are
  # ($GRAIN_DATA_DIR); a temporary directory in the container's own /tmp
  # would not be, and hostd would refuse it as outside paths_under.
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    DOCKER_ARGS+=(--volume "${HOSTD_SOCKET_DIR}:${HOSTD_SOCKET_DIR}")
    DOCKER_ARGS+=(--env "TMPDIR=${GRAIN_SANDBOX_DIR}/tmp")
  fi

  # The key write_systemd_units' -upgrade-verify-cmd names, at the path it
  # names it by. Read-only: the daemon checks signatures against it and
  # has no business replacing it.
  if [ -n "$GRAIN_IMAGE_VERIFY_KEY" ] && [ "$GRAIN_ENABLE_UI_UPGRADE" = "1" ]; then
    DOCKER_ARGS+=(--volume "$IMAGE_VERIFY_KEY_FILE:$IMAGE_VERIFY_KEY_FILE:ro")
  fi

  [ -n "$GRAIN_CLAUDE_PATH" ] && DOCKER_ARGS+=(--volume "$(dirname "$GRAIN_CLAUDE_PATH"):$(dirname "$GRAIN_CLAUDE_PATH"):ro")
  [ -n "$GRAIN_AGY_PATH" ] && DOCKER_ARGS+=(--volume "$(dirname "$GRAIN_AGY_PATH"):$(dirname "$GRAIN_AGY_PATH"):ro")
  [ -n "$GRAIN_CODEX_PATH" ] && DOCKER_ARGS+=(--volume "$(dirname "$GRAIN_CODEX_PATH"):$(dirname "$GRAIN_CODEX_PATH"):ro")
  # Not :ro: Codex replaces rotated OAuth tokens through this path. That
  # it is absolute and exists is settled near the top of this script, in
  # both modes rather than only in this one.
  if [ -n "$GRAIN_CODEX_AUTH_FILE" ]; then
    DOCKER_ARGS+=(--volume "$GRAIN_CODEX_AUTH_FILE:$GRAIN_CODEX_AUTH_FILE")
  fi

  # Deliberately unquoted: this is a list of arguments an operator wrote,
  # not one argument.
  # shellcheck disable=SC2206
  [ -n "$GRAIN_EXTRA_DOCKER_ARGS" ] && DOCKER_ARGS+=($GRAIN_EXTRA_DOCKER_ARGS)
  return 0
}

# --- grain hostd's unit, allowlist and environment -------------------------
#
# write_hostd_units writes grain-hostd.service: `grain hostd` as
# $GRAIN_USER, the installing user, outside grain-daemon.service's
# lifecycle -- a daemon restart or an upgrade leaves it running. The
# daemon's unit Wants= it and is ordered After= it, so the socket
# directory exists before docker is asked to mount it (docker would
# otherwise create it, as root).
#
# It runs $HOSTD_VERSIONS_DIR/current/grain, which the extractor moves to
# each new image's binary (write_host_extractor) -- on an Upgrade, as the
# daemon's ExecStartPre. grain-hostd-upgrade.path sees the link change
# and starts grain-hostd-upgrade.service, which drains hostd
# (`grain hostd -drain`). A hostd on the new binary's protocol cuts over:
# it keeps taking starts and exits 0 the first moment no run is running;
# only if none comes within the allowlist's drain_cap does it go on to
# refuse new starts (the daemon runs those on a container connection, or
# re-queues them) and wait up to drain_cap again for the runs it holds.
# Any other hostd drains that way from the outset. Restart=always is what
# starts it again, on the binary the link now names. So an Upgrade brings
# hostd to the daemon's build without ending a host run.
#
#   RuntimeDirectory     /run/grain-hostd, 0700 and $GRAIN_USER's, where
#                        hostd makes its 0600 socket. Preserved across a
#                        stop: docker_run_args mounts the directory, and a
#                        directory systemd removed and made again is not
#                        the one a running container has mounted.
#   StateDirectory       /var/lib/grain-hostd, where hostd records the
#                        process groups it started (-state-dir).
#   EnvironmentFile      $HOSTD_ENV_FILE, the corp proxy (write_hostd_env).
#                        hostd hands a child none of its environment but
#                        an entry's pass_env names.
#   ExecStartPre         starts grain-hostd-allowlist.service (as root:
#                        the + prefix), which adds the capabilities entries
#                        grain's own servers need in the build hostd is
#                        about to run -- host-git, grain-gcp, ..., and any
#                        edition-registered server --
#                        that the allowlist has no entry of that name for
#                        (`grain hostd -ensure-builtins`). A drain onto a
#                        new binary ends in a start, so an upgrade brings
#                        the entries its build registers with it. It never
#                        changes an entry that is there, and its failure
#                        (the - prefix) never stops hostd starting.
#   ExecCondition        skips the start, rather than failing it, when
#                        $HOST_GRAIN_BIN has no hostd command: a rollback
#                        with a release's own setup.sh from before hostd
#                        puts that release's grain at the same path and
#                        leaves this unit behind, which would otherwise
#                        crash-loop on `unknown command "hostd"`. That
#                        grain exits 1 and this binary's `hostd -h` 0;
#                        systemd treats 1 as "do not start", not failure.
#                        It asks $HOST_GRAIN_BIN, not the current link
#                        ExecStart runs: that script rewrites only the
#                        former, and the link would still name a hostd.
#
# GRAIN_HOSTD=0 removes the unit again; the allowlist,
# which may hold an operator's edits, is left where it is.
write_hostd_units() {
  if [ "${GRAIN_HOSTD:-0}" != "1" ]; then
    if [ -e "$HOSTD_UNIT" ]; then
      log "  removing grain-hostd.service: GRAIN_HOSTD=0 (kept $HOSTD_ALLOWLIST)"
      systemctl disable --now grain-hostd.service >/dev/null 2>&1 || true
      rm -f "$HOSTD_UNIT"
    fi
    if [ -e "$HOSTD_UPGRADE_PATH_UNIT" ]; then
      systemctl disable --now grain-hostd-upgrade.path >/dev/null 2>&1 || true
      rm -f "$HOSTD_UPGRADE_PATH_UNIT" "$HOSTD_UPGRADE_UNIT" "$HOSTD_UPGRADE_SCRIPT"
    fi
    rm -f "$HOSTD_ALLOWLIST_UNIT"
    return 0
  fi
  write_hostd_env
  write_hostd_allowlist
  cat > "$HOSTD_UNIT" <<UNIT
[Unit]
Description=grain hostd: the host CLIs and capability servers grain-daemon.service may start ($HOSTD_ALLOWLIST)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=${GRAIN_USER}
EnvironmentFile=-${HOSTD_ENV_FILE}
ExecCondition=/bin/sh -c 'exec ${HOST_GRAIN_BIN} hostd -h >/dev/null 2>&1'
ExecStartPre=-+$(command -v systemctl || echo /usr/bin/systemctl) start grain-hostd-allowlist.service
ExecStart=${HOSTD_VERSIONS_DIR}/current/grain hostd -socket ${HOSTD_SOCKET} -allowlist ${HOSTD_ALLOWLIST} -state-dir ${HOSTD_STATE_DIR}
RuntimeDirectory=$(basename "$HOSTD_SOCKET_DIR")
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
StateDirectory=$(basename "$HOSTD_STATE_DIR")
StateDirectoryMode=0700
# Never the OOM killer's pick: under auto overcommit a sandbox is what
# is sacrificed. hostd puts each process it starts back at +500.
OOMScoreAdjust=-900
# A drained hostd exits 0 for the new binary to take over.
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
  write_hostd_allowlist_unit
  write_hostd_upgrade_script
  cat > "$HOSTD_UPGRADE_PATH_UNIT" <<UNIT
[Unit]
Description=grain hostd: drain onto a new binary when ${HOSTD_VERSIONS_DIR}/current moves
StartLimitIntervalSec=0

# The directory, not the link: systemd's watch on a path follows a
# symlink, so a watch on current would see its old target's directory and
# never the rename that moves it. The upgrade script does nothing unless
# hostd runs another binary than current's, so the directory's other
# changes (a new version's directory, a prune) cost nothing.
[Path]
PathChanged=${HOSTD_VERSIONS_DIR}
Unit=grain-hostd-upgrade.service

[Install]
WantedBy=multi-user.target
UNIT
  cat > "$HOSTD_UPGRADE_UNIT" <<UNIT
[Unit]
Description=grain hostd: drain it for an upgrade if it is not running ${HOSTD_VERSIONS_DIR}/current
StartLimitIntervalSec=0

[Service]
Type=oneshot
ExecStart=${HOSTD_UPGRADE_SCRIPT}
UNIT
  log "  grain-hostd.service: ${HOSTD_VERSIONS_DIR}/current/grain hostd as $GRAIN_USER, on $HOSTD_SOCKET;"
  log "    grain-hostd-upgrade.path drains it onto each new binary"
  check_hostd_docker_access
}

# write_hostd_allowlist_unit writes grain-hostd-allowlist.service: a
# oneshot, run as root, that adds to $HOSTD_ALLOWLIST the capabilities
# entries the build ${HOSTD_VERSIONS_DIR}/current names ships for its own
# servers and the file lacks (`grain hostd -ensure-builtins`,
# pkg/hostd's EnsureBuiltins, from the list in pkg/capability/intree's
# HostdBuiltins this script's entry functions are tested against). Root,
# because the allowlist is root's on purpose: the daemon's container can
# never write it, and nothing it runs is given a way to. The binary is the
# one the extractor took out of an image this host accepts (image_ref_from)
# and hostd itself runs; it runs here with nothing but /etc/grain
# writable and no network. Only missing entries are added, by name: an
# entry the operator has is never changed or removed, however it differs
# from the default -- the journal says how -- and a symlinked allowlist is
# refused. hostd re-reads the file on every request, so what it adds is
# offered at once, with nothing restarted and no run touched.
write_hostd_allowlist_unit() {
  cat > "$HOSTD_ALLOWLIST_UNIT" <<UNIT
[Unit]
Description=grain hostd: add grain's own capabilities entries ${HOSTD_ALLOWLIST} is missing
StartLimitIntervalSec=0

[Service]
Type=oneshot
ExecStart=${HOSTD_VERSIONS_DIR}/current/grain hostd -ensure-builtins -allowlist ${HOSTD_ALLOWLIST} -command ${HOST_GRAIN_BIN}
ProtectSystem=strict
ReadWritePaths=$(dirname "$HOSTD_ALLOWLIST")
ProtectHome=yes
PrivateNetwork=yes
PrivateTmp=yes
NoNewPrivileges=yes
UNIT
}

# write_hostd_upgrade_script writes what grain-hostd-upgrade.service
# runs (as root): nothing when hostd is down (its next start runs the new
# binary) or already runs the current one; otherwise a drain, through the
# socket as $GRAIN_USER, the only account hostd answers. `hostd -drain`
# asks a hostd on the new binary's protocol for a cutover rather than a
# drain: it keeps taking starts and exits the first moment no run is
# running, so an upgrade neither holds new host runs back nor cuts a
# standing session's turn short (pkg/hostd's Server.Cutover). A hostd that
# predates draining is restarted instead -- it has no detached runs for a
# restart to end.
write_hostd_upgrade_script() {
  install -d -m0755 "$(dirname "$HOSTD_UPGRADE_SCRIPT")"
  cat > "$HOSTD_UPGRADE_SCRIPT" <<SCRIPT
#!/usr/bin/env bash
# Written by scripts/setup.sh (write_hostd_upgrade_script); run by
# grain-hostd-upgrade.service when ${HOSTD_VERSIONS_DIR}/current moves.
set -uo pipefail
# The extractor holds this lock from making a new version's directory
# (a change that starts this script) until it has moved current
# (install_hostd_version): read current under it, or this run can see
# the link before it moves and miss the upgrade.
if command -v flock >/dev/null 2>&1; then
  exec 9>"${HOSTD_VERSIONS_DIR}.lock" && flock 9
fi
current="\$(readlink -f "${HOSTD_VERSIONS_DIR}/current/grain")"
exec 9>&-
# The new build's own capabilities entries first, whether or not hostd
# needs draining onto it (write_hostd_allowlist_unit).
systemctl start grain-hostd-allowlist.service || true
pid="\$(systemctl show -p MainPID --value grain-hostd.service)"
if [ -z "\$pid" ] || [ "\$pid" = 0 ]; then
  exit 0
fi
running="\$(readlink "/proc/\$pid/exe" 2>/dev/null || true)"
if [ "\$running" = "\$current" ]; then
  exit 0
fi
echo "grain hostd (pid \$pid) runs \${running:-an unknown binary}; draining it onto \$current"
if ! runuser -u "${GRAIN_USER}" -- "\$current" hostd -drain -socket "${HOSTD_SOCKET}"; then
  echo "grain hostd did not take a drain (it predates draining); restarting it onto \$current"
  systemctl restart grain-hostd.service
fi
SCRIPT
  chmod 0755 "$HOSTD_UPGRADE_SCRIPT"
}

# write_hostd_env records the corp proxy this script was run with for
# hostd's unit. Rewritten when this run has one, and left alone when it
# has none, so a re-run from a shell without the proxy set does not take
# it away from a host-run agy.
write_hostd_env() {
  local name lines=""
  for name in "${HOSTD_PROXY_VARS[@]}"; do
    if [ -n "${!name:-}" ]; then
      lines+="${name}=${!name}"$'\n'
    fi
  done
  [ -n "$lines" ] || return 0
  install -d -m0755 "$(dirname "$HOSTD_ENV_FILE")"
  local tmp
  tmp="$(mktemp "$HOSTD_ENV_FILE.XXXXXX")"
  {
    echo "# Written by scripts/setup.sh (write_hostd_env): the proxy grain hostd's"
    echo "# children may be given, by an allowlist entry's pass_env."
    printf '%s' "$lines"
  } > "$tmp"
  chmod 0644 "$tmp"
  mv -fT "$tmp" "$HOSTD_ENV_FILE"
}

# write_hostd_allowlist writes $HOSTD_ALLOWLIST, the list of everything
# grain hostd will start, once: root's, 0644, which is the only kind of
# file hostd loads (pkg/hostd's LoadAllowlist) -- the daemon's container
# runs as $GRAIN_USER, does not have /etc/grain mounted, and could not
# write a root-owned 0644 file if it had.
#
# A re-run keeps the file and whatever an operator has put in it, and
# only puts its ownership and mode back to what hostd will load. With
# GRAIN_AGY_PATH it starts with one cli entry, agy:
#
#   paths_under, cwd_under  $GRAIN_DATA_DIR and $GRAIN_SANDBOX_DIR, the
#                           two directories docker_run_args mounts at the
#                           same path on both sides: the run's working
#                           directory, agy's HOME and session store, its
#                           staged config and transcripts are all there.
#   env                     exactly what pkg/agent/antigravity's agyEnv
#                           sends (the design note, "What a host-run agy
#                           is sent"): HOME, AGY_CLI_DISABLE_AUTO_UPDATE,
#                           and the GEMINI_API_KEY / GOOGLE_API_KEY pair a
#                           key-authenticated connection sends.
#   pass_env                the corp proxy, from $HOSTD_ENV_FILE.
#   set_env                 GRAIN_LOCAL_API_TOKEN_FILE, the daemon's access
#                           code by path: agy forks `grain mcpserver` on
#                           this host, which calls the daemon's API and
#                           inherits none of the daemon's environment.
write_hostd_allowlist() {
  install -d -m0755 -o root -g root "$(dirname "$HOSTD_ALLOWLIST")"
  if [ -L "$HOSTD_ALLOWLIST" ]; then
    echo "setup.sh: $HOSTD_ALLOWLIST is a symlink; grain hostd refuses one, and this script will not write through it" >&2
    return 1
  fi
  if [ -e "$HOSTD_ALLOWLIST" ]; then
    chown root:root "$HOSTD_ALLOWLIST"
    chmod 0644 "$HOSTD_ALLOWLIST"
    log "  grain hostd allowlist: keeping $HOSTD_ALLOWLIST as it is (edit it to change what hostd runs)"
    # The same entries grain-hostd-allowlist.service adds from the
    # binary's own list at hostd's next start (write_hostd_allowlist_unit);
    # tests/deploy holds these functions' entries equal to that list.
    ensure_hostd_host_git
    ensure_hostd_detached_clis
    ensure_hostd_builtin_capabilities
  else
    local tmp
    tmp="$(mktemp "$HOSTD_ALLOWLIST.XXXXXX")"
    hostd_allowlist_content > "$tmp"
    chown root:root "$tmp"
    chmod 0644 "$tmp"
    mv -fT "$tmp" "$HOSTD_ALLOWLIST"
    log "  grain hostd allowlist: wrote $HOSTD_ALLOWLIST${HOSTD_AGY_PATH:+ (agy: $HOSTD_AGY_PATH)}"
  fi
  # This run's default, beside it and never loaded by hostd: what the
  # readiness report's suggestion points to, for an operator to copy from.
  local def
  def="$(mktemp "$HOSTD_ALLOWLIST_DEFAULT.XXXXXX")"
  hostd_allowlist_content > "$def"
  chmod 0644 "$def"
  mv -fT "$def" "$HOSTD_ALLOWLIST_DEFAULT"
  # hostd re-reads the file on every request and refuses one it cannot
  # parse, so a broken edit is a host run that fails: said now, not fatal.
  local checked
  if ! checked="$(as_grain "$HOST_GRAIN_BIN" hostd -check -allowlist "$HOSTD_ALLOWLIST" 2>&1)"; then
    log "WARNING: grain hostd will refuse $HOSTD_ALLOWLIST: $checked"
  fi
}

hostd_allowlist_content() {
  cat <<YAML
# grain hostd's allowlist: every program the containerised grain daemon
# may start on this host, and what each request for it may carry
# (docs/design/cli-run-location.md, "Host CLIs through grain hostd").
#
# Written once by scripts/setup.sh; a re-run keeps your edits. hostd
# re-reads it on every request, and loads it only while it is owned by
# root and writable by nobody else.
YAML
  local proxies
  proxies="$(IFS=,; echo "${HOSTD_PROXY_VARS[*]}")"
  proxies="${proxies//,/, }"
  # The container-equivalent entries (docs/non-disruptive-upgrade.md,
  # "Which substrate carries which CLI"): the image's own claude and
  # codex, which the extractor puts beside hostd's grain, run with the
  # container's HOME -- so ~/.claude, codex's home and the per-task
  # session store are the container's own, shared rather than copied --
  # and given the deployment's login by the daemon, as a container run is.
  # A run set to detached names one; their own names leave an operator's
  # claude and codex entries (a CLI on this host's PATH, with this host's
  # login) meaning what they did. env is exactly what pkg/agent/claude
  # sends (claude.HostdEnv) and what pkg/agent/codex does; tests/deploy
  # holds both in step. Kept in step with hostd_detached_cli_entries,
  # which ensure_hostd_detached_clis adds to an older allowlist.
  if [ -z "${GRAIN_DATA_DIR:-}" ] && [ -z "$HOSTD_AGY_PATH" ]; then
    echo "clis: {}"
  else
    echo "clis:"
  fi
  [ -n "${GRAIN_DATA_DIR:-}" ] && cat <<YAML
  claude-detached:
    path: ${HOSTD_VERSIONS_DIR:-/usr/local/lib/grain/hostd}/current/claude
    paths_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    cwd_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    env: [MCP_TOOL_TIMEOUT, CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT, CLAUDE_CODE_DISABLE_AUTO_MEMORY, CLAUDE_CODE_MAX_RETRIES, CLAUDE_CODE_OAUTH_TOKEN, CLAUDE_CODE_USE_VERTEX, GOOGLE_APPLICATION_CREDENTIALS, ANTHROPIC_VERTEX_PROJECT_ID]
    pass_env: [${proxies}]
    set_env:
      HOME: ${GRAIN_DATA_DIR}/home
      GRAIN_LOCAL_API_TOKEN_FILE: ${GRAIN_DATA_DIR}/local-api-token
  codex-detached:
    path: ${HOSTD_VERSIONS_DIR:-/usr/local/lib/grain/hostd}/current/codex
    paths_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    cwd_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    env: [CODEX_HOME, CODEX_EXEC_SERVER_URL, OPENAI_API_KEY]
    pass_env: [${proxies}]
    set_env:
      HOME: ${GRAIN_DATA_DIR}/home
      GRAIN_LOCAL_API_TOKEN_FILE: ${GRAIN_DATA_DIR}/local-api-token
YAML
  if [ -n "$HOSTD_AGY_PATH" ]; then
    cat <<YAML
  agy:
    path: ${HOSTD_AGY_PATH}
    paths_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    cwd_under: [${GRAIN_DATA_DIR}, ${GRAIN_SANDBOX_DIR}]
    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]
    pass_env: [${proxies}]
    set_env:
      GRAIN_LOCAL_API_TOKEN_FILE: ${GRAIN_DATA_DIR}/local-api-token
YAML
  fi
  cat <<YAML
# A stdio capability server a capability config starts with run: host
# names one of these in its command:
capabilities:
#  corp-search:
#    command: /usr/local/bin/corp-search-mcp
#    args: [--stdio]
YAML
  # The built-ins' entries, written here rather than through
  # hostd_builtin_capability_entry so this function stands alone; the
  # list is HOSTD_BUILTIN_CAPABILITIES' (a pkg/capability/intree test
  # keeps both in step with the table). host-git is written on every
  # track too, so a core install can connect a plain git host or a gh
  # login with `grain git-ambient` without hand-editing hostd.yaml.
  local builtin_caps=(gchat gcp host-script) name proxies
  proxies="$(IFS=,; echo "${HOSTD_PROXY_VARS[*]}")"
  proxies="${proxies//,/, }"
  cat <<YAML
  host-git:
    command: ${HOST_GRAIN_BIN:-/usr/local/bin/grain}
    args: [capability, host-git]
    pass_env: [${proxies}]
YAML
  for name in "${builtin_caps[@]}"; do
    cat <<YAML
  grain-${name}:
    command: ${HOST_GRAIN_BIN:-/usr/local/bin/grain}
    args: [capability, ${name}]
YAML
    case "$name" in
      gchat) printf '    extra_args: true\n    env: [GRAIN_CAPABILITY_PROBE]\n' ;;
    esac
    cat <<YAML
    pass_env: [${proxies}]
YAML
  done
}

# HOSTD_BUILTIN_CAPABILITIES are grain's built-in capability servers
# worth running on the host (pkg/capability/intree, Builder.Host): gcp
# with the host account's ambient credentials, gchat posting as it, and
# host-script. Each gets
# the allowlist entry grain-<name> -- intree.HostEntry, what a built-in
# set to run: host names -- running `grain capability <name>`. A
# pkg/capability/intree test keeps this list in step with the table.
HOSTD_BUILTIN_CAPABILITIES=(gchat gcp host-script)

# hostd_builtin_capability_entry is one built-in's allowlist entry.
# pass_env is the corp proxy, as for agy.
hostd_builtin_capability_entry() {
  local name="$1" proxies
  proxies="$(IFS=,; echo "${HOSTD_PROXY_VARS[*]}")"
  proxies="${proxies//,/, }"
  cat <<YAML
  grain-${name}:
    command: ${HOST_GRAIN_BIN:-/usr/local/bin/grain}
    args: [capability, ${name}]
YAML
  # gchat serves an attempt-scoped MCP toolset on the -listen its entry
  # names (intree Builder.Args), so its entry takes extra args, and a
  # probe asks for an ephemeral port.
  case "$name" in
    gchat) printf '    extra_args: true\n    env: [GRAIN_CAPABILITY_PROBE]\n' ;;
  esac
  cat <<YAML
    pass_env: [${proxies}]
YAML
}

# ensure_hostd_builtin_capabilities adds the built-ins' entries an
# allowlist an earlier run wrote is missing, the way ensure_hostd_host_git
# adds host-git: only to the shape hostd_allowlist_content writes, with
# capabilities: as the last top-level key, and never touching an entry
# that is there -- an operator's edit to one is kept. Any other shape is
# told what to add by hand.
ensure_hostd_builtin_capabilities() {
  [ -f "$HOSTD_ALLOWLIST" ] && [ ! -L "$HOSTD_ALLOWLIST" ] || return 0
  local name absent=()
  for name in "${HOSTD_BUILTIN_CAPABILITIES[@]}"; do
    grep -Eq "^  grain-${name}:" "$HOSTD_ALLOWLIST" || absent+=("$name")
  done
  [ "${#absent[@]}" -gt 0 ] || return 0
  local last tmp
  last="$(grep -E '^[^[:space:]#]' "$HOSTD_ALLOWLIST" | tail -n 1)"
  case "$last" in
    "capabilities:"|"capabilities: {}")
      tmp="$(mktemp "$HOSTD_ALLOWLIST.XXXXXX")"
      sed 's/^capabilities: {}$/capabilities:/' "$HOSTD_ALLOWLIST" > "$tmp"
      for name in "${absent[@]}"; do
        hostd_builtin_capability_entry "$name" >> "$tmp"
      done
      chown root:root "$tmp"
      chmod 0644 "$tmp"
      mv -fT "$tmp" "$HOSTD_ALLOWLIST"
      log "  grain hostd allowlist: added the built-in capability server entries for ${absent[*]} (run: host)"
      ;;
    *)
      log "WARNING: $HOSTD_ALLOWLIST has no entry for the built-in capability servers ${absent[*]}, and its shape is not one"
      log "         this script edits. To let one run on the host (run: host), add under capabilities:"
      for name in "${absent[@]}"; do
        hostd_builtin_capability_entry "$name" | while IFS= read -r line; do log "           $line"; done
      done
      ;;
  esac
}

# hostd_host_git_entry is the allowlist's host-git capability entry:
# grain's own in-tree server (`grain capability host-git`,
# pkg/gitproxy/hostgit.go) that runs an ambient git connection's gh
# or `git credential fill` on this host, as $GRAIN_USER, for the daemon
# in its container (`grain git-ambient`). Its command line is fixed here:
# the daemon names only the entry, and the server runs nothing but a
# registered source's own token, package-token and `<tool> api` commands.
# pass_env is the corp proxy, as for agy.
hostd_host_git_entry() {
  local proxies
  proxies="$(IFS=,; echo "${HOSTD_PROXY_VARS[*]}")"
  proxies="${proxies//,/, }"
  cat <<YAML
  host-git:
    command: ${HOST_GRAIN_BIN:-/usr/local/bin/grain}
    args: [capability, host-git]
    pass_env: [${proxies}]
YAML
}

# ensure_hostd_host_git adds the host-git entry to an allowlist an earlier
# run wrote without one. It edits only the shape
# hostd_allowlist_content writes -- capabilities: as the file's last
# top-level key -- and otherwise says what to add by hand, since the
# file is the operator's to edit and this is no YAML parser.
ensure_hostd_host_git() {
  [ -f "$HOSTD_ALLOWLIST" ] && [ ! -L "$HOSTD_ALLOWLIST" ] || return 0
  if grep -Eq '^  host-git:' "$HOSTD_ALLOWLIST"; then
    return 0
  fi
  local last tmp
  last="$(grep -E '^[^[:space:]#]' "$HOSTD_ALLOWLIST" | tail -n 1)"
  tmp="$(mktemp "$HOSTD_ALLOWLIST.XXXXXX")"
  case "$last" in
    "capabilities:"|"capabilities: {}")
      sed 's/^capabilities: {}$/capabilities:/' "$HOSTD_ALLOWLIST" > "$tmp"
      hostd_host_git_entry >> "$tmp"
      chown root:root "$tmp"
      chmod 0644 "$tmp"
      mv -fT "$tmp" "$HOSTD_ALLOWLIST"
      log "  grain hostd allowlist: added the host-git capability entry (an ambient git connection's gh/git)"
      ;;
    *)
      rm -f "$tmp"
      log "WARNING: $HOSTD_ALLOWLIST has no host-git capability entry, and its shape is not one this script"
      log "         edits. An ambient git connection (grain git-ambient) needs it; add under capabilities:"
      hostd_host_git_entry | while IFS= read -r line; do log "           $line"; done
      ;;
  esac
}

# hostd_detached_cli_entries are the container-equivalent cli entries
# hostd_allowlist_content writes (cli claude-detached, cli
# codex-detached), for ensure_hostd_detached_clis; a tests/deploy test
# holds the two in step.
hostd_detached_cli_entries() {
  hostd_allowlist_content | awk '
    /^[^[:space:]#][^:]*:/ { sec = $0; sub(/:.*/, "", sec); keep = 0; next }
    sec == "clis" && /^  [A-Za-z0-9_.-]+:/ { keep = ($0 ~ /^  [a-z]+-detached:/) }
    sec == "clis" && keep { print }'
}

# ensure_hostd_detached_clis adds the container-equivalent cli entries to
# an allowlist an earlier run wrote without them, the way
# ensure_hostd_host_git adds host-git: right under its clis: key (turning
# "clis: {}" into a map), never touching an entry that is there, and only
# when the file has a single top-level clis: key. Anything else is told
# what to add by hand. A daemon set to detached without them runs its
# CLI in the container, saying why in Settings.
ensure_hostd_detached_clis() {
  [ -f "$HOSTD_ALLOWLIST" ] && [ ! -L "$HOSTD_ALLOWLIST" ] || return 0
  local entries missing="" name tmp
  entries="$(hostd_detached_cli_entries)"
  for name in claude-detached codex-detached; do
    grep -Eq "^  ${name}:" "$HOSTD_ALLOWLIST" || missing+="${missing:+ }$name"
  done
  [ -n "$missing" ] || return 0
  # Only the entries missing, each whole.
  local add=""
  add="$(awk -v want=" $missing " '
    /^  [A-Za-z0-9_.-]+:/ { k = $0; sub(/^  /, "", k); sub(/:.*/, "", k); keep = index(want, " " k " ") > 0 }
    keep { print }' <<<"$entries")"
  if [ "$(grep -cE '^clis:( \{\})?[[:space:]]*$' "$HOSTD_ALLOWLIST")" = 1 ]; then
    tmp="$(mktemp "$HOSTD_ALLOWLIST.XXXXXX")"
    awk -v add="$add" '
      /^clis:( \{\})?[[:space:]]*$/ { print "clis:"; print add; next }
      { print }' "$HOSTD_ALLOWLIST" > "$tmp"
    chown root:root "$tmp"
    chmod 0644 "$tmp"
    mv -fT "$tmp" "$HOSTD_ALLOWLIST"
    log "  grain hostd allowlist: added the container-equivalent cli entries ${missing// /, } (a run set to detached)"
  else
    log "WARNING: $HOSTD_ALLOWLIST has no cli entries ${missing// /, }, and its shape is not one this script edits."
    log "         A CLI set to run detached needs them (it runs in the container, and an upgrade interrupts it,"
    log "         until they are there); add under clis:"
    while IFS= read -r line; do log "           $line"; done <<<"$add"
  fi
}

# report_hostd_suggestions names the cli entries this run's default
# allowlist has that the operator's lacks -- an upgrade's new default
# entry, say -- and where the default is to copy them from. It only ever
# suggests: the allowlist is the operator's, and no run of this script
# writes into one that exists (write_hostd_allowlist).
report_hostd_suggestions() {
  [ -r "$HOSTD_ALLOWLIST" ] || return 0
  local have want missing="" name
  have="$(hostd_allowlist_clis < "$HOSTD_ALLOWLIST")"
  want="$(hostd_allowlist_content | hostd_allowlist_clis)"
  for name in $want; do
    # The detached entries are ensure_hostd_detached_clis' to add, and to
    # say how when it cannot.
    case "$name" in *-detached) continue ;; esac
    grep -qxF "$name" <<<"$have" || missing+="${missing:+, }$name"
  done
  [ -n "$missing" ] || return 0
  echo "                       suggested: add the default clis entry for $missing to $HOSTD_ALLOWLIST -- this"
  echo "                       run's default is in $HOSTD_ALLOWLIST_DEFAULT; your allowlist is left as you wrote it"
}

# hostd_allowlist_clis prints the names under an allowlist's clis:, one
# a line, from its standard input.
hostd_allowlist_clis() {
  awk '
    /^[^[:space:]#][^:]*:/ { sec = $0; sub(/:.*/, "", sec); next }
    /^  [A-Za-z0-9_.-]+:/ && sec == "clis" { k = $0; sub(/^  /, "", k); sub(/:.*/, "", k); print k }'
}

# report_hostd_versions says which version grain hostd runs and which the
# current link names (the two differ while it drains onto an upgrade),
# read as root from its main process's binary rather than asked of hostd.
report_hostd_versions() {
  local pid exe current
  current="$(readlink "$HOSTD_VERSIONS_DIR/current" 2>/dev/null || true)"
  pid="$(systemctl show -p MainPID --value grain-hostd.service 2>/dev/null || true)"
  exe=""
  [ -n "$pid" ] && [ "$pid" != 0 ] && exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
  exe="${exe#"$HOSTD_VERSIONS_DIR"/}"
  exe="${exe%/grain*}"
  if [ -z "$exe" ]; then
    echo "                       version: current is ${current:-none}; hostd is not running"
  elif [ "$exe" = "$current" ]; then
    echo "                       version: $current (the current link's)"
  else
    echo "                       version: running $exe; current is ${current:-none} -- it restarts on that once it has drained"
  fi
}

# hostd_allowlist_entries prints the allowlist's entries as one line --
# "clis: agy; capabilities: none" -- for the readiness report and the
# summary. A plain reading of the two maps' keys, not a YAML parser:
# what hostd itself accepts is `grain hostd -check`'s to say.
hostd_allowlist_entries() {
  [ -r "$HOSTD_ALLOWLIST" ] || { echo "no allowlist at $HOSTD_ALLOWLIST"; return 0; }
  awk '
    /^[^[:space:]#][^:]*:/ { sec = $0; sub(/:.*/, "", sec); next }
    /^  [A-Za-z0-9_.-]+:/ && (sec == "clis" || sec == "capabilities") {
      k = $0; sub(/^  /, "", k); sub(/:.*/, "", k)
      v[sec] = (v[sec] == "" ? k : v[sec] ", " k)
    }
    END {
      printf "clis: %s; capabilities: %s\n", (v["clis"] == "" ? "none" : v["clis"]), (v["capabilities"] == "" ? "none" : v["capabilities"])
    }' "$HOSTD_ALLOWLIST"
}

# hostd_processes is the "process" lines of `grain hostd -status` from
# the running hostd -- one per process it holds -- asked as $GRAIN_USER
# (the only account it answers) with the current binary, or nothing when
# it does not answer.
hostd_processes() {
  [ -S "$HOSTD_SOCKET" ] && [ -x "$HOSTD_VERSIONS_DIR/current/grain" ] || return 0
  { timeout 15 runuser -u "$GRAIN_USER" -- "$HOSTD_VERSIONS_DIR/current/grain" hostd -status -socket "$HOSTD_SOCKET" 2>/dev/null || true; } |
    grep '^process ' || true
}

# hostd_running_runs counts the host runs hostd holds now: processes
# still running that belong to a run (a run: host capability server does
# not, and a drain does not wait for one).
hostd_running_runs() {
  hostd_processes | grep -c '^process .*, run .*, running for ' || true
}

# check_hostd_docker_access says whether $GRAIN_USER can reach this host's
# docker daemon. Behind hostd that account is not only the container's
# --user: a host-run agy forks `grain mcpserver` on this host, as that
# account, and it reaches the sandboxes through the docker socket the
# account itself has. grant_docker_group adds the membership; this says
# so when there is none to add, or the socket is not the docker group's.
check_hostd_docker_access() {
  local sock=/var/run/docker.sock sock_group
  if ! getent group docker >/dev/null 2>&1; then
    log "WARNING: this host has no docker group, so $GRAIN_USER cannot reach $sock outside the"
    log "         daemon's container: a host-run agy's grain MCP server will fail every sandbox call"
    return 0
  fi
  if ! id -nG "$GRAIN_USER" 2>/dev/null | tr ' ' '\n' | grep -qxF docker; then
    log "WARNING: $GRAIN_USER is not in the docker group, so a host-run agy's grain MCP server"
    log "         cannot reach $sock: sudo usermod -aG docker $GRAIN_USER, then re-run this script"
    return 0
  fi
  if [ -S "$sock" ] && sock_group="$(stat -c %G "$sock" 2>/dev/null)" && [ "$sock_group" != docker ]; then
    log "WARNING: $sock belongs to group $sock_group, not docker: $GRAIN_USER's docker membership"
    log "         does not reach it, and a host-run agy's grain MCP server will be refused"
    return 0
  fi
  log "  docker: $GRAIN_USER reaches $sock through the docker group (a host-run agy needs it)"
}

# --- 8b. host power and service enablement -----------------------------
#
# Every route into sleep, as systemd names them. Masked as a set rather
# than one at a time: logind picks among these by hardware and by which
# request arrived (a lid, a key, an idle timer, `systemctl suspend`), so
# closing one and leaving the others is a host that still suspends,
# just for a different reason than the one that was noticed.
SLEEP_TARGETS=(sleep.target suspend.target hibernate.target hybrid-sleep.target)
LOGIND_DROPIN=/etc/systemd/logind.conf.d/10-grain-no-suspend.conf

# disable_host_suspend stops this machine putting itself to sleep under a
# running task -- see this file's header for why that is a stopped
# deployment rather than a slow one.
#
# Two halves, because there are two different things to stop:
#
#   * masking the sleep targets closes the *transition*. Every route into
#     sleep ends in one of those units, and a masked unit cannot be
#     started by anything -- not `systemctl suspend`, not a desktop's
#     power menu, not logind's own idle action -- so this holds even for
#     the routes this script has never heard of
#   * the logind drop-in stops the *events* that would otherwise keep
#     asking: a closed lid, a pressed sleep key, an idle session. Masking
#     alone already makes those fail, but a host failing a suspend every
#     time the lid shuts is a log full of noise and a machine one systemd
#     upgrade away from suspending again
#
# Symmetric, so a deployment is never stuck with a policy it was given
# once: GRAIN_DISABLE_SUSPEND=0 on a later run unmasks the same targets
# and removes the same drop-in, rather than leaving an operator to work
# out what a previous run masked. Neither direction is fatal -- a host
# whose systemd will not mask a target is a host that suspends, which is
# worth a warning and not worth abandoning a deploy over.
disable_host_suspend() {
  if [ "$GRAIN_DISABLE_SUSPEND" != "1" ]; then
    log "GRAIN_DISABLE_SUSPEND=0: leaving this host's suspend policy alone"
    rm -f "$LOGIND_DROPIN"
    systemctl unmask "${SLEEP_TARGETS[@]}" >/dev/null 2>&1 || true
    systemctl reload systemd-logind.service >/dev/null 2>&1 || true
    return
  fi

  log "Disabling suspend on this host (mask ${SLEEP_TARGETS[*]})"
  local masked=1
  if ! systemctl mask "${SLEEP_TARGETS[@]}" >/dev/null 2>&1; then
    masked=0
    log "  warning: could not mask the sleep targets -- this host may suspend under a running task"
  fi

  install -d -m0755 "${LOGIND_DROPIN%/*}"
  cat > "$LOGIND_DROPIN" <<'UNIT'
# Written by grain's setup.sh (GRAIN_DISABLE_SUSPEND). A deployment runs
# unattended work for hours; a lid, a sleep key or an idle timer must not
# end it. Delete this file and unmask the sleep targets to undo, or
# re-run setup.sh with GRAIN_DISABLE_SUSPEND=0, which does both.
[Login]
HandleSuspendKey=ignore
HandleHibernateKey=ignore
HandleLidSwitch=ignore
HandleLidSwitchExternalPower=ignore
HandleLidSwitchDocked=ignore
IdleAction=ignore
UNIT

  # Reloaded rather than restarted: restarting logind on an older systemd
  # takes live sessions down with it, which is a heavy price for settings
  # that only matter the next time a lid closes. A reload this systemd is
  # too old to support leaves the drop-in in place for the next boot, and
  # the masked targets already stop the suspend itself in the meantime.
  if ! systemctl reload systemd-logind.service >/dev/null 2>&1; then
    log "  logind did not reload: the lid and sleep-key settings take effect on the next boot"
    # Only where it is true: with the mask above having failed as well,
    # nothing is stopping a suspend until this host reboots, and saying
    # otherwise is the one way this warning could mislead.
    if [ "$masked" = "1" ]; then
      log "  (the masked targets above already prevent the suspend itself)"
    fi
  fi
}

warn_if_no_agent_credential() {
  if [ ! -s "$GRAIN_DATA_DIR/secrets/gemini-api-key" ] \
     && [ ! -s "$GRAIN_DATA_DIR/secrets/claude-oauth-token" ] \
     && [ ! -s "$GRAIN_DATA_DIR/secrets/openai-api-key" ] \
     && [ ! -s "$GRAIN_CODEX_AUTH_FILE" ]; then
    log "grain-daemon.service is running, but no agent credential is configured -- no task can"
    log "  be dispatched until one is. Set it in the UI (Settings -> Models), or set"
    log "  GRAIN_GEMINI_API_KEY / GRAIN_CLAUDE_CODE_OAUTH_TOKEN / GRAIN_OPENAI_API_KEY,"
    log "  or point GRAIN_CODEX_AUTH_FILE at auth.json from codex login, and re-run this script."
    log "  A deployment whose minter holds roles/serviceusage.apiKeysAdmin can mint the Gemini"
    log "  one instead: set GRAIN_GCP_PROJECT and GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE and re-run."
  fi
}

enable_services() {
  step_start "Starting grain-daemon.service"
  # enable, then restart -- not "enable --now". An already-enabled unit
  # from a previous run of this script needs restarting to pick up a
  # newly pulled image or a config change; --now would leave an already-
  # running one exactly as it was. v1 hit precisely this bug for its own
  # git-proxy service (found live on v1, same failure): restarting
  # an already-running unit is always safe, and starting a stopped one is
  # exactly what --now would have done anyway.
  systemctl enable grain-daemon.service >/dev/null
  # grain hostd first: the daemon's unit mounts its socket directory.
  # One holding a host run is drained, not restarted: the run carries on
  # and the next daemon adopts it, and hostd comes back on this run's
  # binary and unit once its runs have ended (write_hostd_units). An idle
  # one is restarted, so this run's binary and unit serve at once.
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    systemctl enable grain-hostd.service >/dev/null
    systemctl enable --now grain-hostd-upgrade.path >/dev/null
    local held
    held="$(hostd_running_runs)"
    if [ "${held:-0}" -gt 0 ] && runuser -u "$GRAIN_USER" -- "$HOSTD_VERSIONS_DIR/current/grain" hostd -drain -socket "$HOSTD_SOCKET" >/dev/null 2>&1; then
      log "  grain-hostd.service holds $held host run(s): draining it rather than restarting, so they carry on;"
      log "    it restarts on this build once they end (the allowlist's drain_cap bounds the wait)"
    elif ! systemctl restart grain-hostd.service; then
      echo "setup.sh: grain-hostd.service failed to start; see journalctl -u grain-hostd -n 50" >&2
      systemctl status grain-hostd.service --no-pager >&2 || true
      exit 1
    fi
    local hostd_up=0
    for _ in {1..50}; do
      [ -S "$HOSTD_SOCKET" ] && { hostd_up=1; break; }
      sleep 0.2
    done
    if [ "$hostd_up" != 1 ]; then
      echo "setup.sh: grain-hostd.service started but made no socket at $HOSTD_SOCKET; see journalctl -u grain-hostd -n 50" >&2
      exit 1
    fi
    log "  grain-hostd.service is listening on $HOSTD_SOCKET"
  fi
  # The three watchers the daemon reaches the host through
  # (write_control_units). --now on these, unlike the service below: a
  # .path unit holds no state worth restarting, and it has to be actively
  # watching before the first request lands in $CONTROL_DIR.
  systemctl enable --now grain-reboot.path grain-shutdown.path grain-restart.path >/dev/null
  # Started unconditionally, even with no agent credential anywhere. It
  # used to be held back until a Gemini key existed, because the daemon
  # built its one agent framework at startup and could not run without
  # one -- but a credential is set from the UI now, and a UI that is not
  # running is a credential that can never be set. The daemon builds a
  # framework per dispatch instead (cmd/grain's agentFrameworks), so a
  # deployment with no key serves the UI, says which keys are missing,
  # and fails any run that needs one with a message naming the pane to
  # fix it in.
  # Type=simple makes systemctl return as soon as docker run has been
  # executed, not once that client has proved its arguments and created the
  # named container. An invalid GRAIN_EXTRA_DOCKER_RUN_ARGS therefore used to
  # let setup report success while the unit failed a moment later. Observe the
  # thing this service exists to keep running before calling the deploy done.
  local daemon_started=0
  if systemctl restart grain-daemon.service; then
    for _ in {1..50}; do
      if systemctl is-active --quiet grain-daemon.service \
         && [ "$(docker inspect --format '{{.State.Running}}' grain-daemon 2>/dev/null || true)" = "true" ]; then
        daemon_started=1
        break
      fi
      sleep 0.2
    done
  fi
  if [ "$daemon_started" != "1" ]; then
    echo "setup.sh: grain-daemon.service failed to start; check GRAIN_EXTRA_DOCKER_RUN_ARGS in /etc/default/grain-daemon, the service status below, and journalctl -u grain-daemon -n 50" >&2
    systemctl status grain-daemon.service --no-pager >&2 || true
    # The status above can be a restart's first instant, with none of the
    # daemon's own output; the journal still has the last attempt's.
    journalctl -u grain-daemon.service -n 20 --no-pager >&2 || true
    exit 1
  fi
  # A running container is not yet a daemon that stays up: one that
  # refuses its own flags (daemon.CheckCLIRunFlags, an unusable store)
  # exits a moment after docker has started it, and Restart=on-failure
  # brings it back for long enough to look running again. So the same
  # container has to still be running a few seconds on, or this run
  # reports the crash-loop instead of a deploy.
  local daemon_id
  daemon_id="$(docker inspect --format '{{.Id}}' grain-daemon 2>/dev/null || true)"
  for _ in {1..25}; do
    sleep 0.2
    if ! systemctl is-active --quiet grain-daemon.service \
       || [ "$(docker inspect --format '{{.Id}} {{.State.Running}}' grain-daemon 2>/dev/null || true)" != "$daemon_id true" ]; then
      daemon_started=0
      break
    fi
  done
  if [ "$daemon_started" != "1" ]; then
    echo "setup.sh: grain-daemon.service started and then exited: the daemon refused this unit's settings. Its own reason is in the log below (journalctl -u grain-daemon -n 50)" >&2
    journalctl -u grain-daemon.service -n 20 --no-pager >&2 || true
    exit 1
  fi
  step_done "started"
  warn_if_no_agent_credential
}


# --- 9. Tailscale (optional) --------------------------------------------
#
# Everything below is off unless GRAIN_TAILSCALE_ENABLE=1, and every step
# of it converges rather than failing: a host that cannot install
# tailscale, cannot log in, or cannot serve still ends this run with the
# deployment it would have had anyway -- a daemon on loopback, reachable
# over an SSH tunnel -- and a log line naming what was missing. That is
# the same trade the granule steps above make, and for the same reason:
# how an operator reaches the UI is not what makes a deployment work, so
# it must not be what makes one fail.
#
# The UI itself is untouched by any of this. It stays bound to
# GRAIN_UI_ADDR, and tailscaled -- a host process, not something in the
# daemon's container -- proxies the tailnet to its port on 127.0.0.1,
# which answers whether that address is loopback or 0.0.0.0. Nothing
# here opens a port of its own, and the daemon
# needs no configuration of its own to be reached this way.

# os_release_field reads one field out of /etc/os-release, in a subshell:
# that file is a shell fragment setting a dozen variables (ID, NAME,
# VERSION, ...), and sourcing it here would land every one of them in
# this script's own namespace.
os_release_field() {
  (
    # shellcheck disable=SC1091 # a host file, not one in this repository -- and absent on a host without one, hence the guard
    . /etc/os-release 2>/dev/null || exit 0
    printf '%s\n' "${!1:-}"
  )
}

# install_tailscale puts tailscale on this host from Tailscale's own apt
# repository, which is what gets a *current* one: the serve syntax below
# ("tailscale serve --bg --http=...") is only spoken by releases from
# 1.50 onwards, and a distribution's own package is routinely older than
# that or absent entirely.
#
# The two files that repository needs are fetched with the image's curl
# rather than a host one (image_run, and this file's header on why there
# is no curl out here to use), and installed only once they have actually
# arrived -- a redirect straight onto the keyring path would leave a
# truncated key behind on a failed fetch, and apt fails closed on that
# for every package on the host, not just this one.
install_tailscale() {
  if command -v tailscale >/dev/null 2>&1; then
    return 0
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    log "  tailscale is not installed and this host has no apt-get to install it with -- install it by hand (https://tailscale.com/download) and re-run"
    return 1
  fi

  # Which of Tailscale's per-distribution, per-release repositories fits
  # this host. A derivative (Mint, Pop!_OS, an Ubuntu respin) names what
  # it is built from in ID_LIKE and is served by that one's packages.
  local distro codename
  distro="$(os_release_field ID)"
  codename="$(os_release_field VERSION_CODENAME)"
  [ -n "$codename" ] || codename="$(os_release_field UBUNTU_CODENAME)"
  case "$distro" in
    debian | ubuntu | raspbian) ;;
    *)
      case "$(os_release_field ID_LIKE)" in
        *ubuntu*) distro="ubuntu" ;;
        *debian*) distro="debian" ;;
        *) distro="" ;;
      esac
      ;;
  esac
  if [ -z "$distro" ] || [ -z "$codename" ]; then
    log "  cannot tell which tailscale package repository fits this host (/etc/os-release: ID=$(os_release_field ID), VERSION_CODENAME=$(os_release_field VERSION_CODENAME)) -- install tailscale by hand (https://tailscale.com/download) and re-run"
    return 1
  fi

  local base="https://pkgs.tailscale.com/stable/${distro}/${codename}"
  local tmp
  log "  installing tailscale from ${base}"
  install -d -m0755 /usr/share/keyrings /etc/apt/sources.list.d
  tmp="$(mktemp)"
  if ! image_run --entrypoint curl -- -fsSL "${base}.noarmor.gpg" > "$tmp"; then
    rm -f "$tmp"
    log "  could not fetch tailscale's package signing key (${base}.noarmor.gpg)"
    return 1
  fi
  install -m0644 "$tmp" /usr/share/keyrings/tailscale-archive-keyring.gpg
  if ! image_run --entrypoint curl -- -fsSL "${base}.tailscale-keyring.list" > "$tmp"; then
    rm -f "$tmp"
    log "  could not fetch tailscale's apt source list (${base}.tailscale-keyring.list)"
    return 1
  fi
  install -m0644 "$tmp" /etc/apt/sources.list.d/tailscale.list
  rm -f "$tmp"

  apt-get update -qq || true
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tailscale || true
  if ! command -v tailscale >/dev/null 2>&1; then
    log "  apt-get could not install tailscale from ${base} -- install it by hand and re-run"
    return 1
  fi
}

# tailscale_backend_state is what tailscaled says it is doing:
# "NeedsLogin", "Stopped", "Starting", "Running", or nothing at all if it
# is not running yet. Read out of `tailscale status --json` with bash's
# own regex match rather than jq, for the reason granule_gcp_access_token
# above reads its token that way: there is no jq on this host, by design.
tailscale_backend_state() {
  local json
  json="$(tailscale status --json --peers=false 2>/dev/null)" || return 1
  [[ "$json" =~ \"BackendState\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1]}"
}

# tailscale_login brings this node onto its tailnet.
#
# A node that is already up is left exactly as it is: its login is
# tailscaled's state, not this script's, and re-running `tailscale up`
# over it with a key that has since expired would take a working node
# down for no gain. Which is also why an auth key is optional on a re-run
# -- and why it is never written anywhere by this script, unlike every
# credential the daemon itself needs.
tailscale_login() {
  local state
  state="$(tailscale_backend_state || true)"
  case "$state" in
    Running | Starting)
      log "  this host is already on a tailnet (state: $state) -- leaving its login as it is"
      return 0
      ;;
  esac

  local args=()
  if [ -n "$GRAIN_TAILSCALE_AUTH_KEY" ]; then
    args+=("--authkey=$GRAIN_TAILSCALE_AUTH_KEY")
  elif [ "$state" != "Stopped" ]; then
    # "Stopped" is a node that has logged in before and been brought
    # down; `tailscale up` needs no key to bring that one back. Anything
    # else needs one, or a human at a browser this deploy does not have.
    log "  this host is not logged in to a tailnet (state: ${state:-tailscaled not responding}) and no GRAIN_TAILSCALE_AUTH_KEY was given -- run 'tailscale up' here by hand, or re-run this script with a key"
    return 1
  fi
  if [ -n "$GRAIN_TAILSCALE_HOSTNAME" ]; then
    args+=("--hostname=$GRAIN_TAILSCALE_HOSTNAME")
  fi
  if [ -n "$GRAIN_TAILSCALE_UP_ARGS" ]; then
    # Word-split as written, the same contract GRAIN_EXTRA_DOCKER_ARGS
    # has: this is a list of arguments, not one argument.
    # shellcheck disable=SC2206
    args+=($GRAIN_TAILSCALE_UP_ARGS)
  fi
  log "  bringing this node onto its tailnet (tailscale up)"
  if ! tailscale up "${args[@]}"; then
    log "  'tailscale up' failed -- an expired, exhausted or wrong-tailnet auth key is the usual reason; see: journalctl -u tailscaled -n 50"
    return 1
  fi
}

# tailscale_serve_ui is the point of all of the above: the UI's own
# loopback port, published on this node's tailnet address.
#
# The proxy target is always 127.0.0.1:<the UI's port>, never the bind
# address itself: a 0.0.0.0 bind (the default) is not an address to
# connect to, and loopback answers either way. GRAIN_TAILSCALE_SERVE_PORT
# naming the very port the UI listens on is a conflict only when the UI
# binds every interface -- the default 0.0.0.0:4242 against a serve port
# of 4242 -- in which case the two really do want the same socket and
# this reports the failure rather than hiding it. Its default, 80, is
# clear of 4242.
tailscale_serve_ui() {
  # --yes because nothing here has a terminal to answer with: `tailscale
  # serve` asks before replacing a configuration this deployment already
  # has, and a re-run of this script -- which is the update path, so it
  # is the common case rather than the odd one -- is exactly when it
  # would ask.
  local args=(--bg --yes)
  if [ "$GRAIN_TAILSCALE_SERVE_PORT" = "443" ]; then
    # HTTPS on the tailnet needs a certificate, which tailscaled fetches
    # itself -- but only for a tailnet with HTTPS enabled, so this is the
    # one setting here that can fail on the tailnet's configuration
    # rather than on this host's.
    args+=("--https=443")
  else
    args+=("--http=$GRAIN_TAILSCALE_SERVE_PORT")
  fi
  args+=("http://127.0.0.1:${GRAIN_UI_ADDR##*:}")
  log "  serving the UI on the tailnet: tailscale serve ${args[*]}"
  if ! tailscale serve "${args[@]}"; then
    log "  'tailscale serve' failed -- with --https, a tailnet without HTTPS certificates enabled (admin console -> DNS) is the usual reason; see: tailscale serve status"
    return 1
  fi
}

# tailnet_url is where an operator actually points a browser, built from
# this node's MagicDNS name. Empty when the tailnet has MagicDNS off,
# which costs nothing but the URL: the node is still served, on the
# address `tailscale status` reports for it.
tailnet_url() {
  local json name
  json="$(tailscale status --json --peers=false 2>/dev/null)" || return 1
  # Self comes first in that document and --peers=false leaves nothing
  # else in it carrying a DNSName, so the first match is this node's.
  # Trailing dot dropped: it is a valid hostname and an ugly URL.
  [[ "$json" =~ \"DNSName\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]] || return 1
  name="${BASH_REMATCH[1]%.}"
  [ -n "$name" ] || return 1
  case "$GRAIN_TAILSCALE_SERVE_PORT" in
    443) printf 'https://%s/\n' "$name" ;;
    80) printf 'http://%s/\n' "$name" ;;
    *) printf 'http://%s:%s/\n' "$name" "$GRAIN_TAILSCALE_SERVE_PORT" ;;
  esac
}

# Where the UI ended up reachable, for the closing report. Empty means
# "loopback and an SSH tunnel", which is every deployment that did not
# ask for this.
TAILSCALE_URL=""

setup_tailscale() {
  if [ "$GRAIN_TAILSCALE_ENABLE" != "1" ]; then
    return 0
  fi
  step_start "Setting up tailscale (GRAIN_TAILSCALE_ENABLE=1)"
  if ! install_tailscale; then
    GRAIN_TAILSCALE_ENABLE=0
    return 0
  fi
  # enable, not just start, for the reason ensure_docker enables docker:
  # a tailnet address that does not come back after a reboot is a UI
  # nobody can reach until someone SSHes in.
  systemctl enable --now tailscaled > /dev/null 2>&1 || true
  if ! tailscale_login; then
    GRAIN_TAILSCALE_ENABLE=0
    return 0
  fi
  if ! tailscale_serve_ui; then
    GRAIN_TAILSCALE_ENABLE=0
    return 0
  fi
  TAILSCALE_URL="$(tailnet_url || true)"
  if [ -n "$TAILSCALE_URL" ]; then
    log "  the UI is now reachable on the tailnet at $TAILSCALE_URL"
  fi
  step_done "ready"
}

# report_readiness prints what this host can actually do, as opposed to
# what this script did. The two have come apart repeatedly: seeding a
# credential is seed-once, minting the Gemini key is deliberately
# non-fatal, and a missing GitHub credential only warns -- so a deploy
# can converge, report success, and leave a host that cannot clone a
# repo or will not start its daemon.
#
# Every line names the consequence, not just the state, because the
# consequence is what an operator would otherwise have to work backwards
# from: an agent run that ends without pushing or asking anything looks
# like a model problem long before it looks like an empty sandbox.
#
# Presence only, never values -- the same restriction `grain secrets
# list` holds to.
report_readiness() {
  step_start "Checking runtime readiness"
  local github="MISSING" gemini="MISSING" claude="MISSING" openai="MISSING" minter="MISSING" daemon ready=1
  # The binaries, not the tokens: they are independent, and a deployment
  # with a token set and no CLI is exactly the state that fails every run
  # of that framework with "executable file not found in $PATH". All are
  # reported, because which framework a run uses is a live per-task
  # choice -- a deployment is only really ready when every one can run.
  #
  # Asked of whatever actually runs each one (report_agent_cli): the
  # image for a CLI in the daemon's container, where they live
  # (Dockerfile), and grain hostd's allowlist for one run on this host
  # through hostd. Getting that the wrong way round reports a binary that
  # has nothing to do with whether a dispatch can run -- the image's agy,
  # say, where hostd runs another. The exception in the container is an
  # operator-named copy, which is a host path by definition.
  local claude_cli agy_cli codex_cli
  claude_cli="$(report_agent_cli claude "$GRAIN_CLAUDE_PATH" GRAIN_CLAUDE_PATH)"
  agy_cli="$(report_agent_cli agy "$GRAIN_AGY_PATH" GRAIN_AGY_PATH)"
  codex_cli="$(report_agent_cli codex "$GRAIN_CODEX_PATH" GRAIN_CODEX_PATH)"

  # Either file shape counts, the same way CredentialSet.load reads
  # either: seed_github_app_credential above writes the .app.json one, so
  # asking only about .token reported a perfectly working App-backed
  # deployment as having no GitHub credential at all.
  if { [ -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.token" ] \
          || [ -s "$GRAIN_DATA_DIR/secrets/github/${GRAIN_GITHUB_CREDENTIAL_NAME}.app.json" ]; }; then
    github="present as '${GRAIN_GITHUB_CREDENTIAL_NAME}'"
  elif { ls "$GRAIN_DATA_DIR"/secrets/github/*.token >/dev/null 2>&1 \
            || ls "$GRAIN_DATA_DIR"/secrets/github/*.app.json >/dev/null 2>&1; }; then
    # And a credential added from the UI is named by whoever added it,
    # which has nothing to do with this script's own default name -- so
    # a deployment finished in the UI reports what it has rather than
    # reporting nothing (the same point the agent-key checks below make).
    github="present"
  fi
  [ -s "$GRAIN_DATA_DIR/secrets/gemini-api-key" ] && gemini="present"
  [ -s "$GRAIN_DATA_DIR/secrets/claude-oauth-token" ] && claude="present"
  [ -s "$GRAIN_DATA_DIR/secrets/openai-api-key" ] && openai="present"
  if [ "$openai" = "MISSING" ] && [ -s "$GRAIN_CODEX_AUTH_FILE" ]; then
    openai="present (shared ChatGPT login)"
  fi
  # A key set through the UI lands in the secrets database, not in either
  # file above, so presence has to be asked of both places -- otherwise a
  # deployment configured entirely from the UI reports every credential
  # missing while running perfectly well. Asked once so container mode
  # starts one container rather than four.
  local secrets_list
  secrets_list="$(grain_cli secrets -data-dir "$GRAIN_DATA_DIR" list 2>/dev/null || true)"
  if grep -q '^gemini-api-key:' <<<"$secrets_list"; then
    gemini="present"
  fi
  if grep -q '^claude-oauth-token:' <<<"$secrets_list"; then
    claude="present"
  fi
  if grep -q '^openai-api-key:' <<<"$secrets_list"; then
    openai="present"
  fi
  if grep -q '^gcp-key-minter:' <<<"$secrets_list"; then
    minter="present"
  fi
  # is-active prints the state and exits non-zero for every one but
  # active, so a fallback after || would be printed beneath it.
  daemon="$(systemctl is-active grain-daemon.service 2>/dev/null || true)"
  daemon="${daemon:-unknown}"

  # The secrets key, and where this deployment's state lives at all.
  #
  # Presence is asked of the file rather than of `grain state key show`,
  # which would *mint* a key on a host that has none (pkg/secrets.Open) --
  # a readiness report must not create what it is reporting on. The
  # public half is only asked for once the private half is known to be
  # there, which makes that call a read.
  local secrets_key="not yet"
  if [ -s "$GRAIN_DATA_DIR/secrets/secrets.key" ]; then
    secrets_key="present ($(grain_cli state -data-dir "$GRAIN_DATA_DIR" key show 2>/dev/null || echo 'public key unreadable'))"
  fi
  local secrets_repo
  secrets_repo="$(secrets_repo_field remote)"
  step_done "checked"

  echo
  log "Readiness:"
  echo "    daemon:            $daemon"
  echo "    install track:     ${GRAIN_INSTALL_TRACK_REPORT:-${GRAIN_INSTALL_TRACK:-oss}}"
  echo "    deployment:        container -- grain-daemon.service is a docker run of the image below"
  if [ "$GRAIN_MIGRATED_FROM_HOST_MODE" = 1 ]; then
    echo "                       (moved from host mode by this run: same data directory, same unit)"
  fi
  echo "    image:             $GRAIN_IMAGE_REF"
  echo "    channel:           $GRAIN_CHANNEL_REPORT"
  local digest
  digest="$(image_digest_ref "$GRAIN_IMAGE_REF" || true)"
  echo "    image digest:      ${digest:-none on this host (a local build, not a pulled image)}"
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    echo "    hostd:             installed -- grain-hostd.service as $GRAIN_USER; allowlist $HOSTD_ALLOWLIST"
    echo "                       ($(hostd_allowlist_entries))"
    echo "                       status: $(hostd_status)"
    hostd_processes | sed 's/^process /                       holding /'
    report_hostd_versions
    report_hostd_suggestions
  else
    echo "    hostd:             not installed (GRAIN_HOSTD=0) -- every CLI runs in the container; \"host\" is not offered"
  fi
  echo "    GitHub credential: $github"
  echo "    Gemini key:        $gemini"
  echo "    Claude token:      $claude"
  echo "    OpenAI key:        $openai"
  echo "    claude CLI:        $claude_cli"
  echo "    agy CLI:           $agy_cli"
  echo "    codex CLI:         $codex_cli"
  echo "                       (where each runs as this install set it; Settings -> Models can change it)"
  echo "    GCP minter key:    $minter"
  echo "    config repository: grain/config (controller local)"
  echo "    secrets key:       $secrets_key"
  echo "    secrets backup:    ${secrets_repo:-<none: the encrypted file is on this host alone>}"
  echo "    target repos:      ${GRAIN_TARGET_REPOS:-<none: unrestricted -- any repo a task names is allowed>}"
  echo "    default repo:      ${GRAIN_TARGET_REPO:-<none: a task with no repo parks>}"
  # Not GRAIN_MAX_WORKERS / GRAIN_MAX_MERGERS: the daemon ignores both
  # flags now (pkg/cli/daemon.go), and printing them told a fresh
  # install "1" while its default team, with no counts of its own,
  # dispatched as many runs as the sandbox budget had room for.
  echo "    capacity:          each team's own max workers / mergers / CI jobs (Team settings; 0 sets no count),"
  echo "                       within this host's sandbox budget (the daemon logs it at start)"
  echo "    ci cache:          ${GRAIN_CI_CACHE_MAX_BYTES:-<default>} bytes, ${GRAIN_CI_CACHE_MAX_ENTRIES:-<default>} entries, kept ${GRAIN_CI_CACHE_MAX_AGE:-<default>}"
  if [ "$GRAIN_GRANULE_ENABLE" = "1" ]; then
    echo "    sandboxing:        granule VMs (one per run, over SSH as ${GRAIN_GRANULE_SSH_USER})"
  elif [ "$GRAIN_HOST_SANDBOXES" = "1" ]; then
    echo "    sandboxing:        NONE -- host directories under $GRAIN_SANDBOX_DIR/runs (-host-sandboxes)"
  else
    echo "    sandboxing:        Docker containers (one per run)"
  fi
  if ui_addr_is_wildcard "$GRAIN_UI_ADDR"; then
    echo "    network:           $(ui_network_url "$GRAIN_UI_ADDR") -- $GRAIN_UI_ADDR listens on every interface;"
    if [ -n "${GRAIN_UI_ALLOWED_HOSTS:-}" ]; then
      echo "                       Host names allowed: ${GRAIN_UI_ALLOWED_HOSTS}"
      echo "                       (and always this machine's own names, localhost and *.ts.net; IP addresses too)"
    else
      echo "                       Host names allowed: <loopback requests checked only>"
    fi
    echo "                       firewall: $(ui_firewall_note "${GRAIN_UI_ADDR##*:}")"
  else
    echo "    network:           off -- the UI is on ${GRAIN_UI_ADDR} only"
  fi
  if [ "$(deployment_tenancy)" = single ]; then
    echo "    access:            single-tenant -- every caller, localhost included, presents the access code"
    echo "                       (grain access-code prints it); there are no accounts or passwords"
  else
    echo "    loopback:          trusted -- only localhost (an SSH port-forward to it included) is the operator without signing in"
    echo "    accounts:          none -- everyone else reaches the UI through an identity proxy (IAP) that names them"
  fi
  report_sign_in
  if [ "$GRAIN_TAILSCALE_ENABLE" = "1" ]; then
    echo "    tailnet:           serving ${TAILSCALE_URL:-<the tailnet address of this node>} -> http://127.0.0.1:${GRAIN_UI_ADDR##*:}"
  else
    echo "    tailnet:           off"
  fi

  if [ "$github" = "MISSING" ]; then
    ready=0
    echo "    !! With no GitHub credential the git proxy cannot clone. A dispatched run"
    echo "       finds an empty sandbox and ends without pushing or asking anything."
  fi
  if [ "$gemini" = "MISSING" ] && [ "$claude" = "MISSING" ] && [ "$openai" = "MISSING" ]; then
    ready=0
    echo "    !! With no agent credential set at all, the daemon runs and serves ${GRAIN_UI_ADDR},"
    echo "       but every dispatched run fails at setup. Set one in the UI (Settings ->"
    echo "       Agent frameworks) or re-run with GRAIN_GEMINI_API_KEY / "
    echo "       GRAIN_CLAUDE_CODE_OAUTH_TOKEN / GRAIN_OPENAI_API_KEY, or point"
    echo "       GRAIN_CODEX_AUTH_FILE at auth.json from codex login."
  fi
  # Not a readiness failure on its own: a deployment that only ever uses
  # one framework neither needs nor misses the other. Still worth a line
  # each, because nothing in the UI says a framework it offers cannot run
  # here.
  #
  # The last line of each names what would have to change: the image.
  local cli_fix="Deploy an image built with it, or point %s at a copy here.\n"
  # shellcheck disable=SC2059 # the format is this script's own string, chosen just above
  case "$claude_cli" in
    *MISSING*)
      echo "    -- no claude CLI: the \"claude\" agent framework cannot run until there is one"
      echo "       (Settings -> Models, and the per-task override, still offer it)."
      printf "       $cli_fix" GRAIN_CLAUDE_PATH
      ;;
  esac
  # shellcheck disable=SC2059
  case "$agy_cli" in
    *MISSING*)
      echo "    -- no agy CLI: the \"antigravity\" agent framework cannot run until there is"
      echo "       one, and it is the default -- so a deployment in this state dispatches"
      echo "       nothing unless every task overrides the framework."
      printf "       $cli_fix" GRAIN_AGY_PATH
      ;;
  esac
  # shellcheck disable=SC2059
  case "$codex_cli" in
    *MISSING*)
      echo "    -- no codex CLI: the \"codex\" agent framework cannot run until there is one"
      echo "       (Settings -> Models, and the per-task override, still offer it)."
      printf "       $cli_fix" GRAIN_CODEX_PATH
      ;;
  esac
  # Never a readiness failure either way: a host with a key is working,
  # and a host without one yet gets one the moment the daemon starts.
  # Printed regardless, because this is where an operator is already
  # looking and the moment to copy that file somewhere is while it still
  # exists -- the deployment that needs it is the *next* one, and by then
  # a lost key is not recoverable from anything.
  if [ "$secrets_key" = "not yet" ]; then
    echo "    -- no secrets key on this host yet: grain-daemon.service mints one on its"
    echo "       first start. Back up $GRAIN_DATA_DIR/secrets/secrets.key once it exists"
    echo "       (\`grain state status\` prints it), and seed it on a rebuilt host with"
    echo "       GRAIN_SECRETS_KEY -- nothing else can decrypt this deployment's secrets\ file."
  else
    echo "    -- back up $GRAIN_DATA_DIR/secrets/secrets.key: the one file here a redeploy"
    echo "       cannot rebuild, and the only thing that can read $GRAIN_DATA_DIR/secrets/secrets.enc."
    echo "       A host restored with a freshly minted key of its own cannot read a line"
    echo "       of it. Seed it back with GRAIN_SECRETS_KEY (see this script's own -h)."
  fi
  if [ "$minter" = "MISSING" ] && [ -n "$GRAIN_GCP_PROJECT" ]; then
    ready=0
    echo "    !! With no minter credential the gcp-key and gemini-key capabilities cannot"
    echo "       mint, so a task granted either will fail to materialize it."
  fi
  if [ "$daemon" != "active" ]; then
    ready=0
    echo "    !! grain-daemon.service is $daemon -- see: journalctl -u grain-daemon -n 50"
  fi
  # A schema change is never quiet: a migrated store is worth one line, and
  # a reset one -- every user and task gone -- is the first thing an
  # operator needs to know about this run, with the way back.
  if [ "$GRAIN_SCHEMA_CHANGE_REPORT" = "migrated" ]; then
    echo "    -- the store's schema changed (${GRAIN_SCHEMA_CHANGE_FROM} -> this build's): its rows were exported"
    echo "       to ${GRAIN_DATA_DIR}/schema-upgrade and the daemon rebuilt the store from them; the old store"
    echo "       is kept as ${GRAIN_DATA_DIR}/store.schema${GRAIN_SCHEMA_CHANGE_FROM}-<time>"
  fi
  if [ "$GRAIN_SCHEMA_CHANGE_REPORT" = "reset" ]; then
    local archived="${GRAIN_DATA_DIR}/store.schema${GRAIN_SCHEMA_CHANGE_FROM}-${GRAIN_SCHEMA_CHANGE_STAMP}"
    echo "    !! the store's schema changed and GRAIN_ALLOW_STORE_RESET=1 started an EMPTY store: every"
    echo "       task and run is gone from this deployment."
    echo "       The old one is ${archived}"
    echo "       (and state-repo.schema${GRAIN_SCHEMA_CHANGE_FROM}-${GRAIN_SCHEMA_CHANGE_STAMP} beside it). To go back to it:"
    echo "         systemctl stop grain-daemon"
    echo "         mv ${GRAIN_DATA_DIR}/store ${GRAIN_DATA_DIR}/store.empty"
    echo "         mv ${archived} ${GRAIN_DATA_DIR}/store"
    echo "         (likewise state-repo; rm ${GRAIN_DATA_DIR}/state-repo.reformat),"
    echo "         echo ${GRAIN_SCHEMA_CHANGE_FROM} > ${GRAIN_DATA_DIR}/.schema_version,"
    echo "         then re-run setup.sh with the GRAIN_CHANNEL / GRAIN_REF that wrote that store"
  fi
  if [ "$GRAIN_TAILSCALE_REQUESTED" = "1" ] && [ "$GRAIN_TAILSCALE_ENABLE" != "1" ]; then
    ready=0
    echo "    !! GRAIN_TAILSCALE_ENABLE=1 was requested but this run could not finish it (see"
    echo "       the earlier log line naming what was missing) -- the UI is reachable on"
    echo "       ${GRAIN_UI_ADDR} over an SSH tunnel and nowhere else. Re-run once it is;"
    echo "       nothing else about this deployment has to change."
  fi
  # Not a readiness failure -- it is what was asked for -- but the one
  # consequence of asking for it that is easy to miss.
  if [ "$GRAIN_TAILSCALE_ENABLE" = "1" ]; then
    echo "    -- everyone who can reach this node on the tailnet can act as this deployment:"
    echo "       the UI and the API it serves carry no auth of their own, so whatever reaches"
    echo "       them acts as its one configured actor. Restrict this node with a tailnet ACL."
  fi
  if [ "$GRAIN_GRANULE_REQUESTED" = "1" ] && [ "$GRAIN_GRANULE_ENABLE" != "1" ]; then
    ready=0
    echo "    !! granule sandboxing was asked for but a prerequisite wasn't ready this run"
    echo "       (see the earlier log line naming which one) -- dispatching into per-run"
    echo "       Docker containers instead. Re-run once it is; nothing else needs to change."
  fi
  if [ "$GRAIN_CONTAINER_SANDBOXES" = "1" ]; then
    echo "    -- container mode: every dispatched run gets its own disposable Docker container"
  elif [ "$GRAIN_HOST_SANDBOXES" = "1" ]; then
    echo "    -- host sandboxes: every dispatched agent runs unisolated, as the daemon's own"
    echo "       user, with its filesystem, its network and its credentials. Do not let"
    echo "       anyone file work here you would not hand a shell on this host. Re-run"
    echo "       without GRAIN_HOST_SANDBOXES for a VM or container per run."
  fi
  [ "$ready" -eq 1 ] && echo "    all runtime prerequisites are in place"
  return 0
}

# deployment_tenancy is the tenancy the daemon is actually in: single,
# unless its build predates the single-tenant engine
# (DAEMON_PREDATES_TENANCY), which is always multi.
deployment_tenancy() {
  if [ -n "${DAEMON_PREDATES_TENANCY:-}" ]; then
    echo multi
  else
    echo single
  fi
}

# report_sign_in is the readiness report's answer to "how do I get in":
# which account and the URL (grain/task-3688).
report_sign_in() {
  local port="${GRAIN_UI_ADDR##*:}"
  local who="${ADMIN_USER_NAME:-${GRAIN_ADMIN_USER:-$GRAIN_USER}}"
  if ui_addr_is_wildcard "$GRAIN_UI_ADDR"; then
    echo "    sign in:           $(ui_network_url "$GRAIN_UI_ADDR"), or http://localhost:${port}/ after ssh -L ${port}:localhost:${port} <this-host>"
  else
    echo "    sign in:           http://localhost:${port}/ after ssh -L ${port}:localhost:${port} <this-host>"
  fi
  if [ "$ADMIN_USER_STATUS" = single ] || [ "$(deployment_tenancy)" = single ]; then
    echo "                       with the access code (grain access-code prints it, to ${GRAIN_USER} and root):"
    echo "                       every caller presents it, localhost included (this host's grain CLI reads it itself)."
    echo "                       It is full owner access -- grain access-code rotate replaces it if it leaks"
    report_cli_access
    return 0
  fi
  case "$ADMIN_USER_STATUS" in
    ok)
      echo "                       as ${who}, a system admin: grain keeps no passwords, so through an identity"
      echo "                       proxy (IAP) that names ${who}, or from this host's loopback as its operator"
      ;;
    *)
      echo "    !! ${who} was not made a system admin (the log above says why): re-run this script,"
      echo "       or add ${who} under Settings -> System admins from this host's loopback"
      ;;
  esac
  report_cli_access
}

# report_cli_access is report_sign_in's last line: how this host's grain
# CLI reaches the daemon, which is the same in either tenancy.
report_cli_access() {
  # Beside grain hostd /usr/local/bin/grain is the image's own binary
  # (install_hostd_binary) rather than the `docker run` wrapper, and it
  # reads the daemon's local API token as the account that owns it.
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    if [ -n "${CLI_SERVER_ARG:-}" ]; then
      echo "    grain CLI:         sudo -u ${GRAIN_USER} grain ${CLI_SERVER_ARG}list  (this build's CLI does not read ${CLI_ENV_FILE}, so name the daemon)"
    else
      echo "    grain CLI:         sudo -u ${GRAIN_USER} grain list  (it reads ${CLI_ENV_FILE} for the daemon's address)"
    fi
  fi
}

print_summary() {
  echo
  log "Done."
  if ui_addr_is_wildcard "$GRAIN_UI_ADDR" && [ "$(deployment_tenancy)" = single ]; then
    echo "    UI:      $(ui_network_url "$GRAIN_UI_ADDR") -- every visitor, localhost included, enters the"
    echo "             access code (grain access-code). Or tunnel instead:"
    echo "             ssh -L ${GRAIN_UI_ADDR##*:}:localhost:${GRAIN_UI_ADDR##*:} <this-host>, then open http://localhost:${GRAIN_UI_ADDR##*:}"
  elif ui_addr_is_wildcard "$GRAIN_UI_ADDR"; then
    echo "    UI:      $(ui_network_url "$GRAIN_UI_ADDR") -- on the network: sign in with a password account;"
    echo "             only localhost is trusted without one. Or tunnel instead:"
    echo "             ssh -L ${GRAIN_UI_ADDR##*:}:localhost:${GRAIN_UI_ADDR##*:} <this-host>, then open http://localhost:${GRAIN_UI_ADDR##*:}"
  else
    echo "    UI:      http://${GRAIN_UI_ADDR} -- reach it with: ssh -L ${GRAIN_UI_ADDR##*:}:localhost:${GRAIN_UI_ADDR##*:} <this-host>, then open http://localhost:${GRAIN_UI_ADDR##*:}"
  fi
  if [ "$GRAIN_TAILSCALE_ENABLE" = "1" ]; then
    echo "             ${TAILSCALE_URL:-<the tailnet address of this node>} -- the same UI on the tailnet, no tunnel needed (tailscale serve status)"
  fi
  echo "    Store:   embedded SQLite under ${GRAIN_DATA_DIR}/store, owned by grain-daemon.service alone"
  echo "    Secrets: ${GRAIN_DATA_DIR}/secrets"
  echo "    State:   ${GRAIN_DATA_DIR}/state-repo -- the same rows as text, in git (grain state status)"
  echo "    CLI:     grain list  (it finds this daemon at http://127.0.0.1:${GRAIN_UI_ADDR##*:} through ${CLI_ENV_FILE},"
  echo "             in any shell; GRAIN_SERVER or -server override it)"
  echo "    Image:   ${GRAIN_IMAGE_REF} (recorded in ${IMAGE_REF_FILE}; \`docker images\` for what is local)"
  if [ "${GRAIN_HOSTD:-0}" = "1" ]; then
    echo "    hostd:   installed -- grain-hostd.service, as ${GRAIN_USER}, on ${HOSTD_SOCKET}: runs what ${HOSTD_ALLOWLIST}"
    echo "             allows on this host ($(hostd_allowlist_entries)). Edit that file as root; hostd"
    echo "             re-reads it. Logs: journalctl -u grain-hostd.service -f. GRAIN_HOSTD=0 removes it."
  else
    echo "    hostd:   not installed (GRAIN_HOSTD=0): every agent CLI runs in the daemon's container, nothing"
    echo "             runs as ${GRAIN_USER} on this machine, and \"host\" is not offered as a run location"
  fi
  echo "    Logs:    journalctl -u grain-daemon.service -f  (or: docker logs -f grain-daemon)"
  echo "    Update:  re-run this script (sudo ./setup.sh) -- it pulls the newest image on this deployment's"
  echo "             channel and restarts the service. Switch with GRAIN_CHANNEL=latest|candidate|main;"
  echo "             pin or roll back with GRAIN_IMAGE_TAG=sha-<short sha> or vX.Y.Z."
  if [ "${#REFTABLE_CONVERTED[@]}" -gt 0 ]; then
    echo "    Git:     converted to the files ref format (the daemon's git cannot read reftable):"
    printf '               %s\n' "${REFTABLE_CONVERTED[@]}"
  fi
  if [ "${#REFTABLE_LEFT[@]}" -gt 0 ]; then
    echo "    Git:     NOT converted -- the daemon cannot read these until they are:"
    printf '               %s\n' "${REFTABLE_LEFT[@]}"
  fi
  report_readiness
  warn_if_no_agent_credential
}

main() {
  # pull_image comes before everything that runs image_run (the granule
  # steps, the empty-repo check) and before install_cli_wrappers, which
  # writes wrappers around the same image: with no checkout and no host
  # tooling left, this image is where the rest of this run gets both its
  # `grain` CLI and every tool richer than the shell.
  #
  # ensure_metadata_block first of all: it is the one step that can refuse
  # this install for a security reason, and nothing below should have
  # started a container -- least of all the daemon -- before it has.
  ensure_metadata_block
  ensure_user
  grant_docker_group
  # Before pull_image, which can move the tag the previous image wore.
  record_replaced_image
  pull_image
  ensure_container_sandbox_image
  # After ensure_user: the wrappers run the image as that account, so it
  # has to exist to have a uid to run as.
  install_cli_wrappers
  setup_sandbox_dir
  # Ahead of every step below that runs the `grain` CLI: that CLI is a
  # `docker run` with $GRAIN_DATA_DIR bind-mounted (install_cli_wrappers),
  # and docker creates a missing bind-mount source itself -- as root,
  # with a mode nothing here asked for. Laying the directory out first
  # means the first CLI invocation mounts a directory this script already
  # owns rather than one docker invented.
  setup_data_dir
  ensure_granule_images
  ensure_granule_kvm_access
  # After ensure_granule_kvm_access: that step is the one that turns
  # granule sandboxing off entirely on a host with no /dev/kvm, and there
  # is nothing to say about nesting on a host that is not booting VMs in
  # the first place.
  ensure_granule_nested_virt
  ensure_granule_git_proxy_host
  reformat_store_if_schema_changed
  format_target_repo_if_empty
  # After setup_data_dir: this writes into $GRAIN_DATA_DIR, and it has to
  # be in place before write_systemd_units' unit reads it.
  write_image_ref
  # Before write_systemd_units, whose unit mounts and names the copy.
  install_image_verify_key
  write_systemd_units
  # Before the daemon is started, not after: the window between "this
  # host can dispatch work" and "this host will stay awake to finish it"
  # should not exist at all.
  disable_host_suspend
  enable_services
  # After enable_services: the daemon has left the image it replaced, and
  # a run that failed before here keeps it to go back to.
  prune_replaced_images
  # After enable_services: it needs the running daemon's API.
  ensure_admin_user
  # After the daemon's first start: it seeds the deployment's settings
  # row from its flags, and model connections are recorded on that row
  # (`grain connections import` refuses them until it exists).
  seed_git_connections
  # After enable_services: `tailscale serve` publishes the port the
  # daemon listens on, so pointing it at a service that is already up
  # means a failure here is about tailscale rather than about a UI that
  # had not started yet -- and it is print_summary, immediately below,
  # that reports the resulting URL.
  setup_tailscale
  print_summary
}

main "$@"
