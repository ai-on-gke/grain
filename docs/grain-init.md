# `grain init`

`grain init` takes a bare Linux host to a running Grain deployment, built
from source, in one command. It is the alternative to
[`scripts/setup.sh`](../README.md#get-started) (which pulls the published
deployment image) for a host where you want everything -- the grain
binary, the granule CLI, and the three sandbox guest images -- built from
a checkout instead.

```bash
sudo ./bin/grain init -repo-root . -data-dir /var/lib/grain/data \
  -github-token-file /root/github-token -github-credential-name github \
  -github-repo owner/repo
```

That requires a built `grain` binary and a checkout already in place --
see [Before you start](#before-you-start). Run it as root: installing
packages, granting `CAP_NET_BIND_SERVICE` and writing and starting
`grain-daemon.service` all need it. Run as anyone else, those steps are
reported `[manual]` with the exact command, and the run exits 10. Root
also runs `make build` in the checkout, so the build output it leaves
there (`bin/`, `pkg/ui/static`, `ui/node_modules`) is owned by root, and
`ui/node_modules` holds production dependencies only (`npm ci
--omit=dev`). Use a checkout kept for the deployment, not one you also
develop in.

The token and repo flags are optional, but without them the deployment
comes up with nothing it can work on: see [Flags](#flags).

`grain init` has no release channel to choose. `scripts/setup.sh` defaults a
new deployment to the `latest` image (`GRAIN_CHANNEL`), but `grain init`
installs whatever the checkout at `-repo-root` builds. To run a release,
check out its tag (`git checkout vX.Y.Z`) before you run it.

## What it sets up, next to `scripts/setup.sh`

On a fresh install the two installers bring up the same kind of
deployment:

| | `scripts/setup.sh` | `grain init` |
|---|---|---|
| UI address | `0.0.0.0:4242` | `0.0.0.0:4242` (`-ui-addr`) |
| allowed hosts | `hostname -f`, `hostname -s` | the same two names |
| first admin | none: the access code is owner access | none: the access code is owner access |
| docker | Debian's `docker.io` | Docker's own `docker-ce` |

## Tenancy

The daemon is single-tenant, and has no `-tenancy` flag any more: the unit
passes none, and `grain init -tenancy` is accepted and ignored. Nobody signs
in: every caller, this host's loopback included, presents the access code
(`grain access-code` prints it), so there is no administrator to set up
(see [No system admins](#no-system-admins)).

## `-platform`

`grain init` can target four deployment shapes; this page, and the
command's own default, cover one of them in full -- a single Linux host
with `/dev/kvm`, running every sandbox as its own VM
(`-platform=linux-kvm`). A second, `-platform=linux-container`, is also
implemented: a single Linux host with no `/dev/kvm`, running every
sandbox as a plain docker container instead. It shares every step
described below except three: no `/dev/kvm` check, no granule CLI, and
no three guest images to build -- nothing here to build but the grain
binary itself, so it comes up in well under a minute rather than the
twelve below -- and it starts the daemon with `-container-sandboxes`
and `-container-git-proxy-host` rather than granule's own flags. The
remaining two platforms (a Kubernetes cluster running sandbox pods; a
Kubernetes cluster running sandbox VMs in privileged pods) are named,
real `-platform` values that `grain init` refuses today rather than
attempting, because neither is built yet. See
[grain-init-platforms.md](grain-init-platforms.md) for the full table
of what each needs, what isolation it gives, and what it gives up.

## It is Linux-only

`grain init` refuses outright on anything but Linux. Docker, the granule
CLI (which does not even compile for non-Linux), and the linux claude CLI
release it installs would all either fail or do the wrong thing on, say,
darwin, so it reports one `[refused] host platform` step and exits
non-zero rather than printing manual steps that could never work.

## Before you start

`grain init` cannot bootstrap the two things it needs before its first
step: a `grain` binary to run, and a checkout at `-repo-root` to build
from. Something has to get both onto the host first -- git plus a clone
of this repository, or a binary and a source tarball copied in some other
way. In particular, **you cannot use `make build` to produce that first
binary on a bare host**: `make` itself, Go, and Node/npm are exactly the
prerequisites described below that `grain init` demands but will never
install, so a bare host can run neither `make build` nor `grain init`
until they're there by some other means (build the binary elsewhere and
copy it over, or install those three packages by hand first).

If you already have a grain binary for this host -- a release download,
or one built elsewhere -- pass it as `-use-binary <path>` and `grain init`
runs that binary instead of building one. See [Using a prebuilt
binary](#using-a-prebuilt-binary).

Once you have a binary and a checkout, `grain init` takes care of
everything else.

## What it installs vs. what it only demands

`grain init` installs, idempotently, on your behalf:

- **docker** -- specifically `docker-ce` from Docker's own apt
  repository, because Debian 12's bundled `docker.io` is 20.10, which
  granule does not run on.
- **Go** -- matching the version `go.mod` declares, fetched and verified
  against go.dev's own release index rather than a digest frozen into
  this source tree.
- **git**.
- **the claude CLI** -- fetched to a file, digest-checked, installed, and
  the installed copy digest-checked again.
- **the daemon's own account** -- a fixed, unprivileged system account
  (`grain` by default -- see [The daemon account](#the-daemon-account)),
  created with no login shell if it does not already exist, added to
  the `docker` group, and given ownership (`chown -R`) of `-data-dir`
  and `-claude-home` before the daemon ever starts.

It demands, but will never install, three things, because none of them
is safe to add by guesswork:

- **`make`** -- `make build`'s own build and frontend targets need it;
  `grain init` draws the line at installing a C toolchain for you.
- **`node` and `npm`** -- the frontend build `make frontend` runs before
  the grain binary is built (`pkg/ui/server.go` `go:embed`s its output),
  and `grain init` does not pin or verify a node/npm version the way it
  does Go's.
- **`/dev/kvm`** -- granule boots every sandbox as a cloud-hypervisor VM
  over KVM; without it every sandbox start fails, and there is no safe
  way to turn on nested virtualization or load the kvm module on an
  operator's behalf. See [A cloud host for `grain
  init`](grain-init-host.md) for how to bring up a host that already
  has it.

A preflight check runs before anything else and reports every one of
these three that is missing, together, with the exact command to fix
each -- rather than installing docker, building the granule CLI and its
three images (the most expensive part of the whole run), and only then
discovering the frontend can't be built. If any is missing, `grain init`
makes no change, starts no install, and builds nothing.

### Using a prebuilt binary

`make` and `node`/`npm` are demanded only so that `make build` can run.
Pass `-use-binary <path>` to run a grain binary that is already on the
host instead, and nothing is built:

- preflight checks that `<path>` is an executable Go binary, reported as
  `prebuilt grain binary`, in place of the `make` and `node & npm`
  checks;
- the build step reports `[skipped]` and names the binary (and the
  release or commit it was built from);
- on `-platform=linux-container`, which builds no granule CLI either, the
  Go toolchain step is skipped too, so the host needs none of make, node,
  npm or Go. `linux-kvm` still installs Go for the granule CLI and still
  demands `/dev/kvm`;
- `CAP_NET_BIND_SERVICE` is granted to `<path>`, and `grain-daemon.service`
  runs `<path>` itself, so keep it where it is -- ideally at a root-owned
  path such as `/usr/local/bin/grain` (`sudo install -o root -g root -m
  0755 <binary> /usr/local/bin/grain`), since whoever owns `<path>` can
  replace what the daemon runs (and what holds `CAP_NET_BIND_SERVICE`),
  and `/usr/local/bin/grain` also puts `grain` on `PATH`.

`-use-binary` replaces `-grain-binary` (passing both with different paths
is refused) and applies only to the two Linux host platforms.
`-repo-root` is still required. A binary already at `-grain-binary` built
from the checkout's current clean `HEAD` also skips `make build`, but
without `-use-binary` preflight still demands `make` and `node`/`npm`.

## What it does, in order

1. Checks `make`, `node`/`npm` (or, with `-use-binary`, that binary)
   and, on `linux-kvm` only, `/dev/kvm` (preflight, above). On a host without `/dev/kvm` the refusal says to
   re-run with `-platform=linux-container`.
2. Installs git and docker if needed.
3. Get-or-creates the daemon's own account and adds it to the `docker`
   group -- see [The daemon account](#the-daemon-account).
4. Creates the sandbox docker network (`grain-sandboxes` by default) and
   reads its real gateway address -- see [What it refuses to
   guess](#what-it-refuses-to-guess).
5. Installs the Go toolchain `go.mod` declares (skipped on
   `linux-container` with `-use-binary`).
6. Builds the granule CLI from `third_party/granule` (vendored -- it is
   not cloned from `gke-labs/granule`).
7. Builds the three images a granule guest packs onto, in the order they
   depend on each other: the granule runtime image, then
   `Granulefile.guest-base`, then `Granulefile`.
8. Builds the grain binary (frontend first, since the binary
   `go:embed`s it), or, with `-use-binary`, skips that and names the
   binary it will run.
9. Grants the grain binary `CAP_NET_BIND_SERVICE` (needed to bind
   443/80 for the package cache; this is a property of the file, so it
   is reapplied after every rebuild).
10. Installs the claude CLI.
11. Given a token, writes it as a credential under `secrets/github` and
    records a git connection naming it for each repo it's told about, by
    running `grain connections import` -- never a host-default
    connection (see [git-hosts.md](git-hosts.md#seeding-connections-grain-connections-import-stage-2)).
12. Creates `-data-dir` (0700) if nothing above did, then chowns it (and `-claude-home`, if it lives outside
    `-data-dir`) to the daemon's own account -- everything written into
    either tree by the steps above, run as whoever invoked `grain init`,
    belongs to the daemon's account before the daemon itself ever reads
    or writes a byte of it.
13. Starts the daemon, unless `-start-daemon=false`, and writes
    `/etc/grain/cli.env` naming its address and `-data-dir`, so the grain
    CLI in any shell on this host reaches it -- the same file
    `scripts/setup.sh` writes.
14. Waits for the daemon's API to answer, then, over that same loopback
    API:
    - points the default team's target repos at `-github-repo`,
    - points the default team at the GitHub credential from step 11, if
      it has none of its own -- host platforms only: on the two
      Kubernetes platforms the credential is never in the cluster, so the
      token is instead stored as the default team's own credential
      ("give the deployment the GitHub token"),
    - reads the sandbox budget back (`GET /api/settings`) and reports a
      `[manual]` step if it cannot hold even one sandbox of the
      deployment's shape -- a host too small for its memory reserve, say.
      No run could ever be admitted there, so every approved task would
      stay queued. The step prints the `PUT /api/settings` with a reserve
      and shape that fit. It does not apply that change itself, because
      the trade-off is the operator's (Settings → Sandboxes). The settings
      carry no sandbox commitment while the deployment's own `max_workers`
      is 0, for any caller, admin or not, and this step is then
      `[skipped]` and says that is why.

Every step is get-or-create. Running the whole command again is a no-op
wherever the work is already done -- see [Re-running is the normal
path](#re-running-is-the-normal-path). To take all of it back off the
host, see [Uninstalling](#uninstalling).

## What it refuses to guess

Two things are refused outright, with no manual fallback, no matter how
many times you run this:

- **The sandbox network's gateway.** A guessed
  `-granule-git-proxy-host` (or, under `-platform=linux-container`,
  `-container-git-proxy-host`) produces a first clone that fails with
  "Couldn't connect to server" and nothing in that message says why, so
  `grain init` reads the gateway off a real docker network it created
  instead of assuming one -- the exact same network and the exact same
  read, on both platforms; only the flag the daemon is started with
  differs.
- **Starting a daemon beside one already running.** A suspended host
  resumes with its processes still live, so "my ssh session ended" is
  never evidence that the daemon stopped; `grain init` will not start a
  second one next to it.

(Running on a non-Linux host, covered above, is refused the same way --
before any step runs at all.)

## The daemon account

`grain init` runs the daemon as an unprivileged, fixed system account --
`grain` by default (`-daemon-user`) -- rather than root. It
get-or-creates that account (a system UID, no login shell), get-or-adds
it to the `docker` group (the only way a non-root account reaches
`/var/run/docker.sock`), and chowns `-data-dir` (and `-claude-home`, if
it lives outside `-data-dir`) to it before the daemon ever starts, so
the account the unit's `User=`/`Group=` names is one that can actually
read and write everything the daemon needs to. Every one of those three
steps is get-or-create, the same as everything else `grain init` does.

Name a different account with `-daemon-user` (and, if its group is not
the same name, `-daemon-group`) and `grain init` manages that account
instead, on the same terms -- created if missing, added to `docker`,
given ownership of `-data-dir`. The one value that opts all the way back
out, with none of that performed at all, is the literal `-daemon-user
root`: no account created, no `docker` group membership, no `chown`, and
no `User=`/`Group=` line in the unit -- systemd's own default, and the
only behavior this command had before this account existed.

**Upgrading a deployment `grain init` already brought up as root** (any
deployment from before this feature, since root was the only behavior
then): while that daemon is running, a re-run adopts it as-is -- the
account, `docker` group and `chown` steps are all `[skipped]` ("a daemon
is already running ... grain init adopts it as-is"), and the unit is left
alone, since `ensureDaemon` never rewrites a live unit out from under it.
To actually move the daemon onto the new account: stop it (`systemctl
stop grain-daemon.service`), then run `grain init` again with the same
arguments. It creates the account, chowns `-data-dir`, rewrites
`grain-daemon.service` with `User=grain`/`Group=grain`, and starts it.

The daemon acts as `grain` from then on, and that is all it needs to:
there is no stored admin for its actor to be (`admin_accounts` went at
schema 26, task 4880), so nothing has to carry an earlier root daemon's
`root` across the move. A unit an earlier `grain init` rewrote with
`-as <the stored admin>` is still recognized as this command's own.

**Adopting a daemon started by hand.** A `grain daemon` running with no
unit is found by `pgrep -f "grain daemon"`, and the unit `grain init`
writes for it is that process's own `/proc/<pid>/cmdline` and owner,
flags and all. The match it adopts is the daemon itself -- `<...>/grain
daemon ...` -- not a `sudo -u grain grain daemon ...` or `sh -c` wrapper
around it, and not a grain daemon in another PID namespace (a container,
or a `kind` node's pod), which no unit on this host manages.

## No system admins

`grain init` sets none up, because there are none. The daemon is
single-tenant: it signs nobody in, and every caller, loopback included,
presents its access code (`grain access-code` prints it). Whoever holds
it is the owner ([docs/access-code.md](access-code.md)); the deployment
keeps no list of administrators (`admin_accounts` went at schema 26,
task 4880). `-admin-account` and `-admin-user`, which once checked the
daemon's own seeded admin and added yours (docs/single-tenant-reset.md,
3e-iii-c6), are accepted and ignored, so a script that still passes them
does not stop init. Nor does init any longer rewrite the daemon's unit to
keep a stored admin as its actor: no route asks whether its actor is one.
`scripts/setup.sh` still makes `GRAIN_ADMIN_USER` a system admin when it
installs a v1.9.0 image, which predates the single-tenant daemon.

## The three exit codes

- **0** -- the deployment is actually up and provisioned: every step
  either done or deliberately skipped. Leaving out `-github-token-file`
  and `-github-repo` counts as deliberate: the run exits 0, and the
  default team has no repo or credential to work with until you set them
  in the UI.
- **10** -- it ran to completion, but one or more steps are still manual
  or refused. Run the commands it printed, then run `grain init` again
  with the same arguments. A refused `-platform` (the two Kubernetes
  platforms) also exits 10, though no re-run will change that.
- **1** -- a genuine failure: bad flags, or an internal error. Nothing
  about the host's state is implied either way.

## Re-running is the normal path

Every step is get-or-create. A run that reports `[manual]` steps is
meant to be resumed, not treated as a failure: do what each one says (the
exact command is printed), then run the same `grain init` command again.
Steps already done are skipped; only what's left gets done. The daemon
this command installed counts as done when it is still running with the
same unit. If a re-run rebuilds the grain binary (the checkout moved to a
new commit, or has uncommitted or untracked files), the run asks for
`systemctl restart grain-daemon.service` so the daemon picks up the new
build.

`-dry-run` prints the plan -- what each step would do -- without changing
anything.

## Uninstalling

`grain init` has no uninstall of its own, and `scripts/setup.sh
--uninstall` does not cover it: the two install different things. This
is everything a `linux-kvm` or `linux-container` run leaves on the host,
and the command that removes each. Run them as root, in this order.

| What `grain init` installed | How to remove it |
|---|---|
| `grain-daemon.service` (`/etc/systemd/system/grain-daemon.service`) | `systemctl disable --now grain-daemon.service && rm /etc/systemd/system/grain-daemon.service && systemctl daemon-reload` |
| the CLI connection file (`/etc/grain/cli.env`) | `rm -f /etc/grain/cli.env && rmdir --ignore-fail-on-non-empty /etc/grain` |
| the sandbox docker network (`grain-sandboxes`, or `-sandbox-network`) | `docker network rm grain-sandboxes` (once no sandbox container is on it) |
| `linux-kvm` only: the three images it built, `granule:vendored`, `granule-guest-base:vendored` and `grain-guest:dev` (or the `-granule-runtime-image`/`-guest-base-image`/`-guest-image` you named) | `docker rmi grain-guest:dev granule-guest-base:vendored granule:vendored` |
| `linux-container` only: the sandbox image the daemon pulled on first use (`grain container-sandbox-image` names it) | `docker rmi "$(<repo-root>/bin/grain container-sandbox-image)"` (or substitute the `-use-binary` path for `<repo-root>/bin/grain`) |
| `CAP_NET_BIND_SERVICE` on the grain binary (`<repo-root>/bin/grain`, or the `-use-binary` / `-grain-binary` path) | `setcap -r <repo-root>/bin/grain` (or `setcap -r <use-binary-path>`), or delete the binary |
| the build output in the checkout: `bin/grain`, `bin/granule`, `pkg/ui/static`, `ui/node_modules` (owned by root) | `rm -rf <repo-root>/bin <repo-root>/ui/node_modules`, `git -C <repo-root> checkout pkg/ui/static`, or delete the checkout |
| the claude CLI, installed under `-claude-home` (`<data-dir>/claude` by default) and linked as `/usr/local/bin/claude` | `rm /usr/local/bin/claude`; the rest goes with `-claude-home` |
| Go, in `/usr/local/go` | `rm -rf /usr/local/go` |
| docker: `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-buildx-plugin`, `docker-compose-plugin` from Docker's apt repository (`/etc/apt/sources.list.d/docker.list`, `/etc/apt/keyrings/docker.asc`) | `apt-get purge docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin && rm /etc/apt/sources.list.d/docker.list /etc/apt/keyrings/docker.asc` -- only if nothing else on the host uses docker; `/var/lib/docker` is left behind by the purge |
| `git` | `apt-get purge git` -- again only if nothing else needs it |
| the daemon account (`grain`, or `-daemon-user`) and its `docker` group membership | `userdel grain` (and `groupdel grain` if its group is left) |
| `-data-dir`: the task store, the secrets and `secrets/secrets.key` | **Keep it** unless you mean to lose the deployment: a later `grain init` on the same `-data-dir` picks it up again. Copy `secrets/secrets.key` somewhere safe first; without it the secrets database cannot be read. Then `rm -rf <data-dir>` (and `rmdir --ignore-fail-on-non-empty /var/lib/grain` if `-data-dir` was `/var/lib/grain/data`) |

`grain init` does not install a sleep or suspend policy, a metadata-server
block, or a `/usr/local/bin/grain` wrapper. Those are `scripts/setup.sh`'s,
and `setup.sh --uninstall` removes them (see the
[README](../README.md#uninstalling)). On the two Kubernetes platforms,
remove what it applied from the cluster with `kubectl delete -f` of the
manifest it was given (`-k8s-manifest`) instead.

## How long it takes

On an 8 vCPU host, a full run (nothing pre-installed) takes roughly 12
minutes, almost all of it building the granule runtime, guest-base and
guest images -- the three steps the preflight check exists to avoid
discovering a missing prerequisite behind.

## Flags

Run `./bin/grain init -help` for the full list with defaults; the ones
most worth knowing about up front:

- `-repo-root`, `-data-dir` -- required.
- `-platform` -- which deployment shape to bring up; see
  [`-platform`](#-platform) above. Defaults to `linux-kvm`; all four
  platforms are implemented. Pass `-platform=linux-container` on a host
  without `/dev/kvm`.
- `-use-binary` -- run this prebuilt grain binary instead of building
  one; see [Using a prebuilt binary](#using-a-prebuilt-binary).
- `-github-token-file` -- a file holding a GitHub token to write and
  narrow to `-github-repo`. Prefer this over `-github-token`, which puts
  the token on the command line where `ps` and shell history both expose
  it. It needs `-github-credential-name`.
- `-github-credential-name` -- the name the token is stored under
  (`secrets/github/<name>.token`) and the credential the default team is
  pointed at. Required with a token.
- `-github-repo` -- an `owner/repo` or `owner/*` pattern, repeatable: the
  repos the token is narrowed to, and the default team's target repos.
- `-admin-account`, `-admin-user` -- deprecated and ignored: the daemon
  is single-tenant, so there are no system admins to set up -- see [No
  system admins](#no-system-admins).
- `-tenancy` -- deprecated and ignored: the daemon is single-tenant only.
- `-ui-addr` -- where the UI/API listens; `0.0.0.0:4242` on a fresh
  install, and the installed unit's own on a re-run. Listening on every
  interface, `-ui-allowed-hosts` defaults to this machine's fully
  qualified name and its short name (`hostname -f` and `hostname -s`, as
  `scripts/setup.sh` does); pass `-daemon-arg -ui-allowed-hosts=...` to
  name others.
- `-team-max-workers` -- accepted and ignored: a team has no worker count
  of its own any more.
- `-start-daemon=false` -- build and install everything but don't start
  or touch the daemon.
- `-daemon-user`/`-daemon-group` -- run the daemon as this systemd
  `User=`/`Group=` instead of `grain`, the default -- see [The daemon
  account](#the-daemon-account). `-daemon-user=root` opts all the way
  back out to systemd's own root default.
- `-upgrade-allowed-branches` -- the branches an upgrade of this
  deployment may install, written into the unit as the daemon's own
  flag: comma-separated `path.Match` patterns (`main,latest,release/*`),
  or `**` for any branch. A malformed pattern is refused before anything
  runs. Left unset, a re-run keeps whatever the `grain-daemon.service`
  already on the host passes (the step says which value it kept). With
  no unit there, no flag is written, and the daemon's default applies:
  `main`, `candidate`, `latest` and `vX.Y.Z` tags. An upgrade builds
  and runs the branch it names, so anyone who can push a matching
  branch can get code into the daemon; `**` is an explicit opt-in.
