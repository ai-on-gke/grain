# grain

Grain is a single-operator agent cluster for turning a backlog of software
tasks into tested pull requests. It runs coding agents in isolated sandboxes,
gives them only the repositories and credentials each task needs, watches CI,
and manages the pull request through merge.

Instead of supervising one agent session at a time, you give Grain work and
let it coordinate the operational loop: choose the next task, create a branch
and sandbox, run an agent, surface questions, open a pull request, respond to
CI, and merge when the result is ready. A web UI and CLI provide the same view
of tasks, runs, repositories, schedules, and deployment health.

grain 2.0 is single-tenant: one owner, who signs in with the deployment's
access code ([docs/access-code.md](docs/access-code.md)). A 1.x deployment
does not upgrade to it. Install 2.0 fresh and bring the old connections over
([docs/releases/v2.0.0.md](docs/releases/v2.0.0.md)). From 2.0.0 on, every
release upgrades a 2.0 deployment in place.

## Why Grain?

- **Run a backlog, not a prompt.** Queue tasks and let Grain dispatch them as
  capacity becomes available.
- **Keep agents contained.** By default, every run gets its own
  [Granule](https://github.com/bwsalmon/granule)-managed VM and scoped access to
  source repositories and credentials.
- **Close the delivery loop.** Agents can push branches, open pull requests,
  wait for CI, fix failures, and hand clean changes to the merge queue.
- **Choose the agent for the job.** Grain supports Claude Code, Codex, and
  Antigravity, with deployment-wide defaults and per-task overrides.
- **Operate one service.** The daemon, task store, REST API, UI, scheduler, and
  orchestration loop ship as one container for a single operator.

## How it works

```text
task backlog → dispatcher → agent + sandbox → branch → CI → pull request → merge
                    ↑                              |
                    └──────── questions/retries ──┘
```

The Grain daemon owns task state and scheduling. For each dispatched task it
creates a sandbox, checks out only the repositories the task may access, and
runs the selected agent with a constrained tool set. Grain records the run,
collects its result, and manages the resulting pull request. Sandboxes do not
receive the deployment's long-lived GitHub or model-provider credentials.

For a 25-minute end-to-end tour of how all the pieces fit together—task states,
the sandbox boundary, the merge queue, where state lives on disk and in Git,
and how to debug a stuck task—read **[docs/architecture.md](docs/architecture.md)**.

Grain is single-tenant (see
[docs/local-policy.md](docs/local-policy.md#single-tenant-mode)): a grain
serves its owner alone. Nobody signs in. Its one credential is the access
code it generates at first start (`grain access-code` prints it), which every
caller presents, loopback included. The code plus a URL that reaches the
daemon is full owner access, so keep the UI on loopback and reach it through
SSH, Tailscale, IAP, or another trusted access layer, or treat the code like a
password ([docs/access-code.md](docs/access-code.md)).

## Get started

Grain is designed to run on a Linux machine with systemd and hardware
virtualization (`/dev/kvm`). The installer uses Docker and can install it on a
vanilla Debian host. You also need GitHub credentials and credentials for at
least one supported agent framework.

Clone this repository on the target machine, then run the installer. This
minimal example configures Codex and one target repository:

```bash
git clone https://github.com/ai-on-gke/grain.git
cd grain

sudo \
  GRAIN_GITHUB_TOKEN="<github-token>" \
  GRAIN_OPENAI_API_KEY="<openai-api-key>" \
  GRAIN_TARGET_REPO="owner/repository" \
  GRAIN_TARGET_REPOS="owner/repository" \
  ./scripts/setup.sh
```

A new deployment runs the last promoted release, the `latest` channel
(`ghcr.io/bwsalmon/grain/grain:latest`, the core build). On a machine an
edition's install track claims (`pkg/installtrack`, where an edition linked
into grain registers its track) the installer pulls that edition's build
instead. The images are published with the same tags, and
`GRAIN_INSTALL_TRACK` overrides the detection.
Pass `GRAIN_CHANNEL=candidate` (the
current release candidate) or `GRAIN_CHANNEL=main` (the untested head of
`main`) to follow another channel, or `GRAIN_IMAGE_TAG=vX.Y.Z` or
`GRAIN_IMAGE_TAG=sha-<short sha>` to pin one build. Re-running the installer
keeps the channel the deployment already runs unless you pass one, and the UI's
Upgrade button offers that same channel by default. The readiness report prints
the channel and the exact image digest installed.

The installer also prints the commit the pulled image was built from. On `main`,
`candidate` or another branch, it warns when that commit is behind the branch's
head. A branch's tag moves only after a release of a newer commit has been
built, tested and published, which takes about twenty minutes after a merge, so
a change merged in the last half hour may not be in the image yet. Grain
releases a branch's head until one publishes, and retries a failed release once.
The tag only moves forward, so a release that finishes late never moves it
back. To get one exact build, re-run setup once it has published, or pin
`GRAIN_IMAGE_TAG=sha-<short sha>`.

The GitHub token is the quickest bootstrap path. For ongoing use, configure a
GitHub App so Grain can use installation tokens and read GitHub Checks. Run
`./scripts/setup.sh --help` for GitHub App settings, other agent providers,
Tailscale setup, storage locations, concurrency, and sandbox networking.

The installer prints a readiness report and is safe to run again to update the
deployment. When an update (or a rollback) changes the store's schema, the re-run
carries the deployment's rows across the way the UI's Upgrade button does, and
keeps the old store as `store.schemaN-<time>` under the data directory. If the
rows cannot be carried, it stops without moving anything; re-running with
`GRAIN_ALLOW_STORE_RESET=1` starts an **empty** store instead (every user, task
and run gone), and the readiness report names the archived store and the steps to
restore it. A fresh install serves the UI/API on every interface, on port 4242
(`GRAIN_UI_ADDR=0.0.0.0:4242`), so open `http://<grain-host>:4242/`. A re-run
keeps whatever address the installed deployment already uses. Only localhost is
trusted without a verified identity: everyone else reaches the UI through an
identity proxy (IAP) that names them. The readiness report prints the
URL, the host names the UI answers to, and whether a host firewall seems to block
the port; the installer never opens one. To keep the UI off the network, install
with `GRAIN_UI_ADDR=127.0.0.1:4242` and tunnel to it from your workstation:

```bash
./scripts/client.sh <grain-host>
```

(or forward the port yourself with `ssh -L 4242:localhost:4242 <grain-host>` and
open <http://localhost:4242>). The `grain` CLI on the host defaults to
`http://127.0.0.1:4242`. Sign in as described in
[First sign-in](#first-sign-in), finish any
setup called out by the readiness report, and create and approve your first
task. You can also do that from a shell on the Grain host:

```bash
grain create \
  -title "Add health checks" \
  -body "Add an HTTP health endpoint and cover it with tests." \
  -repo owner/repository \
  -approve

grain list
```

Grain will create the run, work on a task branch, and expose progress and any
questions in the UI. Add `-auto-merge` when creating a task if its pull request
should merge automatically after all required checks pass.

### First sign-in

grain keeps no sign-in accounts, passwords or administrators. It has one
credential, the **access code**, and whoever holds it is the deployment's
owner: every tab, setting and control is theirs
([docs/access-code.md](docs/access-code.md)). `grain access-code`, run as the
account grain runs as, prints it; the browser asks for it once and keeps a
session.

- **`scripts/setup.sh`, either mode,** and **`grain init`** set up nobody:
  there is no list of administrators to be on. `grain init -admin-account`
  and `-admin-user`, and `grain settings -add-admin`, are accepted and
  ignored -- see [docs/grain-init.md](docs/grain-init.md).

**The CLI on the host.** `grain` finds its daemon through `$GRAIN_SERVER`, then
`/etc/grain/cli.env` (which `setup.sh` writes, with
`GRAIN_SERVER` and `GRAIN_DATA_DIR`), then `http://127.0.0.1:4242`, so it works
in the shell that just ran the installer. It gives up after `-connect-timeout`
(5s) or `-timeout` (2m) with one line saying where it looked and what to set.
It authenticates with `<data-dir>/local-api-token`, the deployment's operator
credential, which only the daemon's account can read: run the CLI with `sudo`
(the `docker run` wrapper drops to that account), or as `sudo -u grain grain ...`
beside grain hostd, where `/usr/local/bin/grain` is the image's own binary.

### Building from source instead

`scripts/setup.sh` pulls the published deployment image. To bring up a host
from a checkout instead -- building the grain binary, the granule CLI, and
the sandbox guest images from source -- run
`./bin/grain init -repo-root . -data-dir /var/lib/grain/data`. It is Linux-only: it
refuses outright on any other platform rather than printing steps that could
never work there.

It installs docker, Go and git as needed, builds the granule CLI and the
three guest images in dependency order, creates the sandbox network the
daemon's git proxy is advertised on, grants the grain binary
`CAP_NET_BIND_SERVICE`, installs the claude CLI, and starts the daemon --
refusing, rather than guessing, if it cannot read a real sandbox network
gateway or a daemon already appears to be running. It separately *demands*,
but will never install, `make`, Node/npm, and `/dev/kvm`; a bare host needs
those (or a `grain` binary built elsewhere) before its first step, since
`make build` needs exactly the same things `grain init` does (`/dev/kvm`
aside -- see below). On a host with no `/dev/kvm`, `-platform=linux-container`
runs every sandbox as a plain docker container instead, skipping the granule
CLI and all three guest images entirely, so it comes up in well under a
minute (see [`docs/grain-init-platforms.md`](docs/grain-init-platforms.md)
for the full table). `grain init -dry-run` prints the plan without changing
anything, and every step -- including the whole command -- is safe to run
again if an earlier run left manual or refused steps behind.

See [`docs/grain-init.md`](docs/grain-init.md) for the full walkthrough:
exit codes, what each step does, and what to do when one is left manual.

> **Sandboxing:** every run gets a sandbox of its own: a Granule VM on a host
> with KVM, and a local Docker container (`GRAIN_CONTAINER_SANDBOXES=1`) on one
> without. Running agents directly in host directories is no longer supported.

> **One install shape: the release container, plus grain hostd.** Every
> machine install runs the daemon as a `docker run` of the deployment image.
> `setup.sh` also installs `grain-hostd.service` by default: `grain hostd`, run
> as `GRAIN_USER` outside the daemon's lifecycle. It starts the few programs
> that must run on the bare machine (agy under the user's corp identity, or a
> stdio capability server with `run: host`), and only those that the root-owned
> allowlist `/etc/grain/hostd.yaml` names: nothing on the core track until you
> add an entry. The installer writes that file once
> and keeps your edits. Its socket directory, `/run/grain-hostd`, is mounted
> into the daemon's container, and `/etc/grain` never is. **`GRAIN_HOSTD=0`
> leaves hostd out** (and removes an installed one): everything then runs in
> the container, nothing runs as you on the machine, and "host" is not offered
> as a run location, as on Kubernetes. The readiness report and Settings →
> Models say which of the two an install is. See
> [docs/design/cli-run-location.md](docs/design/cli-run-location.md), "Trust
> levels", for what each shape trusts grain with.

> **Host mode is gone.** `GRAIN_INSTALL_MODE=host` (the daemon as a plain
> systemd service) was removed, and is refused with the command to run
> instead. Re-running `sudo ./scripts/setup.sh` on a host-mode install moves it
> to container mode plus hostd, keeping its data directory and
> `grain-daemon.service`. A host-mode install upgraded with the Upgrade button,
> or by hand, keeps running on the bare machine but refuses further UI upgrades
> until that re-run, and says so: a `level=warning` line at start and a banner
> in the UI naming the agent CLIs that cannot run until then
> ([docs/releases/v2.4.0.md](docs/releases/v2.4.0.md),
> [docs/design/cli-run-location.md](docs/design/cli-run-location.md#migrating-a-host-mode-install)). Going back to host
> mode means re-running an older release's own `scripts/setup.sh`, which
> replaces `/usr/local/bin/grain` but leaves `grain-hostd.service` in place:
> the unit then skips its start, since that binary has no `hostd`, and
> `sudo systemctl disable --now grain-hostd.service` removes it. Each agent CLI runs
> where `GRAIN_CLAUDE_RUN`, `GRAIN_CODEX_RUN` and `GRAIN_AGY_RUN` say:
> `container` (a subprocess in the daemon's container) or `host` (through
> grain hostd, for a CLI its allowlist names). **Settings → Models** overrides
> them for the next run.

> **Which branches an upgrade may install:** the Upgrade button, a scheduled
> upgrade and an agent's upgrade only install a branch that matches the
> daemon's `-upgrade-allowed-branches`. By default that is `main`, `candidate`,
> `latest` and `vX.Y.Z` release tags. Set it with
> `--upgrade-allowed-branches` or `GRAIN_UPGRADE_ALLOWED_BRANCHES`, for example
> `sudo ./scripts/setup.sh --upgrade-allowed-branches 'main,latest,release/*'`.
> The value is comma-separated `path.Match` patterns (`*` never crosses a `/`),
> or `**` for any branch. Setup refuses a malformed pattern before it writes
> anything. A re-run that does not set it keeps the value already in the
> installed `grain-daemon.service`, and says which value it kept, so an
> allowlist survives the next update. (`grain init` takes the same
> `-upgrade-allowed-branches`, and keeps the installed value the same way.)
> An upgrade builds and runs whatever the branch holds. Anyone who can push a
> branch matching the allowlist can therefore get their code built into the
> daemon, which is why `**` is an explicit opt-in and never a default.

### Where grain keeps its files

Everything lives under one root, split in two:

```
/var/lib/grain/           root:root 0755 -- nothing else lives here
/var/lib/grain/data/      grain:grain 0750 -- GRAIN_DATA_DIR (-data-dir): the store, secrets,
                          secrets.key, local-api-token, bin/ -- everything to back up
/var/lib/grain/scratch/   grain:grain 0755 -- GRAIN_SANDBOX_DIR (-sandbox-dir): host-sandbox
                          run directories and the package, Actions and CI caches -- disposable
```

Back up `/var/lib/grain/data`; never back up `scratch/`. Either directory can
be a mount of its own, a partition or a disk, and `scratch/` is best on a disk
of its own: a runaway checkout that fills it then can't fill the disk the store
is on. Setting `GRAIN_DATA_DIR` or `GRAIN_SANDBOX_DIR` puts that directory
exactly where you say instead.

This is grain 2.0's layout. 1.x used `/var/lib/grain` itself as the data
directory and `/var/lib/grain-sandbox` beside it. A re-run of the installer on
such a host keeps the data directory `/etc/grain/cli.env` records and says so;
nothing is moved. A fresh install refuses to start beside an old flat data
directory left in `/var/lib/grain` and tells you how to move it into `data/`.

### Uninstalling

`scripts/setup.sh` removes what it installed. Run it with the same
`GRAIN_USER` you installed with (if you changed it); it reads
`GRAIN_DATA_DIR` from `/etc/grain/cli.env` and `GRAIN_SANDBOX_DIR` from
`grain-daemon.service`:

```bash
sudo ./scripts/setup.sh --uninstall   # or GRAIN_UNINSTALL=1
sudo ./scripts/setup.sh --purge       # or GRAIN_UNINSTALL_PURGE=1
```

| | `--uninstall` | `--purge` |
|---|---|---|
| `grain-daemon.service`, the `grain-reboot`/`-shutdown`/`-restart` `.path`/`.service` watchers, `grain-block-metadata.service` and its iptables rule, and the `grain-daemon` container | stopped, disabled and removed | stopped, disabled and removed |
| `/usr/local/bin/grain`, `/usr/local/bin/granule`, `/usr/local/lib/grain`, `/usr/local/share/grain` (left by host mode), `/etc/profile.d/grain.sh` | removed | removed |
| `/etc/grain/{image.env,cli.env,cosign.pub}` (the directory too, once it is empty) | removed | removed |
| grain's sudoers drop-in (`/etc/sudoers.d/90-grain-nft`, left by host mode) and `/etc/modprobe.d/kvm-nested-grain.conf` | removed | removed |
| the grain images (`ghcr.io/bwsalmon/grain/grain`, every tag) and every sandbox image those tags name | removed (`--keep-images` keeps them) | removed (`--keep-images` keeps them) |
| `sleep`/`suspend`/`hibernate`/`hybrid-sleep.target` masks and `/etc/systemd/logind.conf.d/10-grain-no-suspend.conf` | unmasked, drop-in removed | unmasked, drop-in removed |
| `GRAIN_SANDBOX_DIR` (`/var/lib/grain/scratch`, caches) and `/var/lib/granule/vms` | removed | removed |
| `GRAIN_DATA_DIR` (`/var/lib/grain/data`: the task store, the secrets and `secrets/secrets.key`) | **kept**; the path of the key is printed | removed |
| `/var/lib/grain` itself | kept (it holds `data/`) | removed if empty; never while it holds anything else |
| the `grain` system user and its group | **kept** | removed |
| packages an install added (`docker.io`/`docker-cli`, `git`, `iptables`, `nftables`, `tailscale` and its apt source) | kept, and listed | kept, and listed |

The default keeps the data directory because `secrets/secrets.key` is the one
file that can't be recreated. Without it the secrets database can't be read.
A fresh install on the same `GRAIN_DATA_DIR` picks the same deployment up
again. Copy the key somewhere safe before you `--purge`. The packages are
never removed, because other software on the host may use them. The closing
report prints the `apt-get purge` that would remove them. A deployment brought
up by `grain init` instead is removed by hand: see
[Uninstalling in docs/grain-init.md](docs/grain-init.md#uninstalling).

## Backups

Back up `/var/lib/grain/data` (`GRAIN_DATA_DIR`), and never
`/var/lib/grain/scratch` (`GRAIN_SANDBOX_DIR`): everything in it is a run's
working space or a cache that grain rebuilds. Stop the daemon, or use
`grain backup` below, for a consistent copy of the store.

Grain can also upload a credential-free backup of its SQLite database and
controller-local `grain/config` repository to either Google Drive or a Google
Cloud Storage (GCS) bucket. For Drive, enable the Drive API for the Google
identity Grain runs as and share the destination folder with that identity.
For GCS, grant that identity permission to create objects in the destination
bucket (for example, `roles/storage.objectCreator`). Application Default
Credentials are used unless a credential JSON file is named explicitly.

Run a backup immediately on the controller:

```bash
# Google Drive
grain backup \
  -data-dir /var/lib/grain/data \
  -google-drive-folder <folder-id> \
  -google-credentials-file /path/to/google-credentials.json

# Google Cloud Storage
grain backup \
  -data-dir /var/lib/grain/data \
  -gcs-bucket <bucket-name> \
  -google-credentials-file /path/to/google-credentials.json
```

To schedule the same operation in the daemon, add
`-backup-interval` (for example, `24h`) and either
`-backup-google-drive-folder` or `-backup-gcs-bucket`. The optional
`-backup-google-credentials-file` applies to either destination. When a GCS
bucket is set it is used instead of Drive. An interval of `0`, the default,
disables scheduled backups.

Each ZIP contains an online-consistent `database/grain.db` snapshot and a
restorable `config/config-repo.bundle` with the config repository's branches,
tags, and history. It does not contain `data-dir/secrets`, Google credential
files, Git remote configuration, or any historical `secrets.enc` object; the
temporary bundle export removes that path from every commit.

## Remote hosts

`grain hostd` is grain's only host agent. On the daemon's own machine it
listens on `/run/grain-hostd/hostd.sock`; on another machine it dials in
to the daemon instead, with the shared token the daemon was started with
(`grain daemon -hostd-token-file`):

```bash
grain hostd -server https://<grain-host> -token-file /etc/grain/hostd.token
```

The remote model executors (`grain executor` / `grain model-server`,
`scripts/run-remote-executor.sh`) were removed; see
[docs/executors-removed.md](docs/executors-removed.md) for what a remote
hostd does and does not do yet.

## Documentation

- **[Architecture](docs/architecture.md)** is the end-to-end introduction for
  new Grain users and operators: the three pieces, the life of a task from
  filing through CI and the merge queue, where state lives, agents/models,
  capabilities, schedules, releases, and a troubleshooting guide.
- [`grain init`](docs/grain-init.md) documents bringing up a deployment from
  a source checkout: prerequisites, what it installs versus only demands,
  exit codes and re-running it.
- [A cloud host for `grain init`](docs/grain-init-host.md) covers the other
  half: bringing up a GCE instance with nested virtualization to run it on
  -- sizing, verifying acceleration actually works, suspend/resume, and
  reaching its loopback-bound UI.
- [Implementation notes](docs/implementation-notes.md) preserve the detailed
  architecture, design decisions, operating notes, and project history that
  previously lived in this README.
- [Data model](docs/data-model.md) describes tasks, runs, capabilities, leases,
  and their lifecycle.
- [Composition](docs/composition.md) explains how Grain's components fit
  together.
- [Agent ergonomics](docs/agent-ergonomics.md) covers the execution environment
  presented to coding agents.
- [CI capabilities](docs/ci-capabilities.md) describes Grain's built-in CI
  model and sandbox requirements.
- [The proxy cache](docs/package-cache.md) explains the apt, PyPI, Docker
  Hub, npm and Go module cache the controller runs in front of every
  sandbox, and how to turn it off or flush it.
- [The Actions cache](docs/actions-cache.md) explains the GitHub Actions
  cache the controller keeps per repository, which is what makes a
  `cache-from: type=gha` build warm across CI runs, who can read it, and
  how to see what it holds, turn it off or flush it.
- [Schedules](docs/schedules.md) documents recurring work.
- [Key user journeys](docs/user-journeys.md) catalogues the main journeys
  through grain, from install to merge, with their step counts and friction
  today, as the basis for a nightly simplification review.
- [Upgrading without stopping the work](docs/non-disruptive-upgrade.md) is a
  proposal for upgrading a deployment without discarding the agent runs and CI
  jobs it has in flight.
- [Declarations](docs/declarations.md) covers the templates, suites and
  schedules a repository declares in its own `.grain` directory.
- [One format, two contexts](docs/declaration-schema.md) is the shared
  definition those files and grain's own settings are both written
  against, and the decision about identity behind it.
- [Repo Controllers & Dynamic Environments](docs/repo-controllers-and-environments.md)
  specifies the Granule-based per-repository controller architecture for dynamic
  infrastructure provisioning and repository state machines.
- [Shibu](docs/shibu.md) is a design for a namespace and allocator layer
  that lets agents delegate permissions and build infrastructure, tested
  against a full software-factory walk-through, with its findings, open
  questions and how it relates to grain.
- [The Shibu API specification](docs/shibu-spec/README.md) derives
  Shibu's APIs from that design: the kernel's control API, the web
  proxy and caller assertion, agents and the harness contract, and the
  contracts for allocators, backend drivers, secret backends and every
  standard-distribution component.

## Development

Build and test Grain from a source checkout with:

```bash
make build
make test
```

The frontend requires Node.js and npm; the backend requires the Go version
declared in [`go.mod`](go.mod). See the [implementation
notes](docs/implementation-notes.md) for package-level detail and design
rationale.

## Contributing

This project is licensed under the [Apache 2.0 License](LICENSE).

We follow [Google's Open Source Community Guidelines](https://opensource.google.com/conduct/).

## Disclaimer

This is not an officially supported Google product.

This project is not eligible for the Google Open Source Software Vulnerability Rewards Program.
