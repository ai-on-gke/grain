# Where an agent CLI runs: container or host

Status: implemented (grain hostd). Owner decisions 2026-10-22 and 10-23:
host mode is removed, and every machine install is the release container
plus grain hostd, which `GRAIN_HOSTD=0` leaves out ("Installed by
setup.sh", "Trust levels"). Design A, the per-run docker launcher, and
the `auto` run location were removed after that (task 5118, v2.3.0);
its section below is history.

## Problem

This section describes the problem as it stood before host mode was
removed. A host-mode deployment (`GRAIN_INSTALL_MODE=host`, the Cloudtop
install) ran `grain-daemon.service` as the user on the bare machine. The daemon
starts each run's agent CLI as its own child (`agent.LocalHost`). The
CLI's tools act inside the run's sandbox, a Granule microVM or a Docker
container, through grain's MCP server.

A Cloudtop's exec policy blocks `claude` on the bare machine, but it runs
`docker run --entrypoint claude ghcr.io/bwsalmon/grain/grain:main`. The
release image already ships a pinned, checksum-verified `claude` and
`codex`.

## Run locations

Each agent CLI has a **run location**, `container` or `host`. What they
mean depends on where the daemon runs (`pkg/daemon/hostexec.go`,
"Deployments with no host executor" below):

| daemon                                         | `container`                               | `host`                        | default                           |
|------------------------------------------------|-------------------------------------------|-------------------------------|-----------------------------------|
| in the release container, with grain hostd     | a subprocess in the daemon's container     | through grain hostd           | `container` (agy `host` on the Google build) |
| in a container with no hostd, or on Kubernetes | a subprocess in the daemon's container     | not offered                   | `container`                       |
| on the bare machine (development only)         | not offered                               | the daemon's own child        | `host`                            |

The last row is what `grain demo`, a local `grain daemon`, `grain init`
and the e2e harness are; no installer makes one any more (task 5041).
Such a daemon has no container of its own, so Settings offers only Host
and `PUT /api/settings/cli-run` refuses `container`. A `container` that
an earlier release stored, or that a `-<cli>-run` flag still passes,
runs on the host instead. It is logged once per CLI per process, and the
resolution carries a `warning` that Settings shows.

### Precedence

The pattern is the same one `-claude-path` follows against a connection's
own CLI path (`pkg/daemon/clirun.go`):

1. **Settings → Models**, in the "Where each agent CLI runs" card. It is
   stored in `grain_config.cli_run` (`model.Config.CLIRun`, schema 31), so
   it is exported to the state repo with the rest of the row.
2. **Daemon flags** `-claude-run`, `-codex-run` and `-agy-run`. The
   installer writes them from `GRAIN_CLAUDE_RUN`, `GRAIN_CODEX_RUN` and
   `GRAIN_AGY_RUN`.
3. **Built-in default**, from the table above.

The setting is resolved from the live config for every run, so a change
applies to the next run with no restart. The daemon logs every CLI's
resolution at start, and the Settings card shows it with the reason.

### What became of `auto` and the image

Releases before v2.3.0 also took `auto` (host if `<cli> --version` ran on
the bare machine, container otherwise) and a per-CLI image for Design A's
containers. Neither means anything without the per-run launcher:

- A stored `auto` or image is dropped as `grain_config.cli_run` is read
  (`model.DecodeCLIRun`), so that CLI falls back to its default, and the
  next save writes the cleaned value. There is no schema change.
- `-<cli>-run=auto` is accepted, logged at start and read as unset.
  `GRAIN_*_RUN=auto` is accepted by `setup.sh` with a warning and
  dropped. Both stop being accepted in a later release.
- `-cli-image`, `-cli-image-file` and `-cli-container-engine` are
  defined and ignored for one release, so a unit an older installer
  wrote still starts and `verify_daemon_flags` reports no drift
  (`tests/deploy/daemon_flags_test.go`).

## History: Design A, a per-run container of the release image (removed)

This section describes the per-run docker launcher as it was built for
host-mode Cloudtops (#5012) and removed in task 5118, once task 5041 had
removed host mode. `agent.ContainerHost`, `ReapCLIContainers` and the
`ViaDocker` resolution no longer exist; the history is kept for the
reasoning behind the MCP choice, which still holds for grain hostd.

Each agent CLI had a **run location**:

| setting     | meaning                                                                 |
|-------------|-------------------------------------------------------------------------|
| `host`      | a child of the daemon on this machine, which is the old behaviour       |
| `container` | a per-run `docker run --rm --init` of an image                          |
| `auto`      | `host` if `<cli> --version` runs here, `container` otherwise            |

Only claude and codex had a container launcher. agy (Jetski,
`/opt/agy/bin/agy`) needs the host's corp
environment, and a custom command is whatever its operator wrote, so both
accepted only `host`.

#### Precedence, as it was

This is the same pattern as `-claude-path` versus a connection's own CLI
path (`pkg/daemon/clirun.go`):

1. **Settings → Models**, in the "Where each agent CLI runs" card. It is
   stored in `grain_config.cli_run` (`model.Config.CLIRun`, schema 31), so
   it is exported to the state repo with the rest of the row.
2. **Daemon flags** `-claude-run`, `-codex-run` and `-agy-run`. The
   installer writes them from `GRAIN_CLAUDE_RUN`, `GRAIN_CODEX_RUN` and
   `GRAIN_AGY_RUN`.
3. **Built-in default:** `auto` for claude and codex, `host` for agy and
   custom commands. `google/scripts/cloudtop-install.sh` writes
   `container` for claude and codex and `host` for agy.

A per-CLI **image** override sits beside the location. The default image
is `-cli-image`, and failing that the image this deployment was installed
or last upgraded from, read from `/etc/grain/image.env` (`-cli-image-file`)
on every run. An upgrade's new image is therefore the next run's image,
matching the daemon's version.

The setting is resolved from the live config for every run, so a change
applies to the next run with no restart. An `auto` probe is cached per
binary and dropped when the setting is saved. The daemon logs every
CLI's resolution at start, and the Settings card shows it with the reason,
for example:

```
claude: container (image ghcr.io/bwsalmon/grain/google-grain:v2.1.0, version v2.1.0)
agy: host (/opt/agy/bin/agy)
```

Behind grain hostd a host run's path is the one hostd's allowlist entry
runs, as hostd reports it (`agy: host (/opt/agy/bin/agy, through grain
hostd)`), not a binary the daemon's container resolves.

The readiness summary at the end of `setup.sh` says the same, from what
the install gave the daemon (Settings can change it afterwards):

```
claude CLI:        container: /usr/local/bin/claude (in ghcr.io/bwsalmon/grain/google-grain:v2.1.0)
agy CLI:           host, through grain hostd: /opt/agy/bin/agy (its allowlist entry "agy")
```

#### What crossed into the container

`agent.ContainerHost` (`pkg/agent/container.go`) builds one container per
CLI invocation:

| the CLI needs                         | how it gets it                                                                                         |
|---------------------------------------|--------------------------------------------------------------------------------------------------------|
| its binary                            | `--entrypoint claude` (or `codex`) from the image; argv unchanged                                      |
| staged files (`--mcp-config`, `CODEX_HOME`, Vertex credentials) | `MkdirTemp` / `CreateTemp` go under `<data>/cli-containers/staging`, bind-mounted at the same path |
| HOME (claude's session store, read back by `--resume`) | `<data>/cli-containers/home/<cli>`, bind-mounted read-write; `HOME` set to it |
| codex's per-task `CODEX_HOME`         | the session store (`<data>/agent-sessions`), bind-mounted                                              |
| codex's shared `auth.json`            | the target of every `Host.Symlink` is bind-mounted at its own path, so a refreshed login stays shared |
| the working directory                 | `Command.Dir`, bind-mounted; `--workdir` is it, or HOME                                                |
| credentials (OAuth token, API key)    | `--env NAME` with the value in the docker client's own environment, so it is never in an argv, never in `docker inspect`'s command, and never in the log (`CommandLine` redacts it). The value *is* in the container's `Config.Env`, which `docker inspect` shows to anyone who can reach the docker daemon -- an account that is root-equivalent on the host already |
| the corp proxy                        | `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `AUTO_PROXY`, `ALL_PROXY` (and their lowercase forms) passed by name |
| the network                           | `--network host`: the provider, grain's MCP endpoint and every capability server's HTTP `mcp_endpoint` are reachable exactly as they are from the daemon |
| user                                  | `--user <daemon uid>:<daemon gid>`, so files on the mounts stay the daemon's                           |
| transcript and session watching       | stdout is streamed back through the attached client into the same tee (`SessionWatcher`, the live transcript) |

Nothing else is mounted. That includes the data directory itself, the
secrets, the docker socket and the granule exec key.

#### MCP: grain's HTTP endpoint on the host

There were two ways to give a containerised CLI the MCP server:

1. Keep it stdio. The CLI forks `grain mcpserver` inside the container
   (the image has `grain`), with the daemon's local API socket and token
   mounted read-only.
2. Run the mcpserver on the host behind grain's authenticated loopback
   HTTP endpoint (`mcp.StartCommandHTTP`), and write the CLI an HTTP
   `--mcp-config`.

**Option 2 was chosen.** codex has always been wired this way, so it is
the smaller change: one option for claude (`claude.WithHostMCP`) and
nothing for codex. It is also the safer one. A container-side mcpserver
would need everything the sandbox tools need: the docker socket for
container sandboxes (root on the machine), the granule exec key, and the
GitHub credential ladder under the data directory. Each of those would
have been mounted into a container whose CLI runs model-chosen tool
calls. On the host, the mcpserver keeps exactly the access it has today,
and the container holds only a per-run bearer token for one loopback
endpoint.

#### Lifecycle

- **Cancel and deadline:** the run's context cancels `docker run`
  through `exec.Cmd.Cancel`. That runs `docker kill <name>` (a killed
  client leaves its container running), then kills the client.
  `WaitDelay` is the same 5 s bound as `LocalHost`. `--init` reaps
  whatever the CLI forked.
- **Cleanup:** every run ends with `docker rm --force <name>`, so a run
  leaks nothing even if `--rm` never fires.
- **Daemon restart:** every CLI container carries the label
  `dev.grain.cli.deployment=<data dir>`, and the daemon removes its own
  at start (`ReapCLIContainers`). A leftover cannot still be doing a
  run's work, because the MCP endpoint it talked to belonged to the
  previous process.
- **Image freshness:** an image that is not present is pulled once per
  process. A failed pull fails the run, naming the image and what to do.
  A private registry's image is found locally and never re-pulled. The
  disk reclaimer keeps every image a CLI is set to run in
  (`DockerPruner.KeepImages`).

#### Known gaps, when it was removed

- agy and custom commands cannot be containerised.
- A run moved between host and container does not resume a suspended
  claude session from the other location. The two HOMEs differ, so it
  starts fresh.
- The container must share the daemon's filesystem and network, so the
  docker engine must be the local one. That holds for host mode, which
  is the only mode this is for.

## Host CLIs through grain hostd (one install shape)

The owner's decision of 10-22 is one install shape: the daemon runs in the
release container everywhere, Cloudtops included, and only the few things
that must run on the bare machine do. agy is the first: it needs the
user's corp identity. A stdio capability server that needs host-only
tooling is the second. The first of those is grain's own `host-git`
(`pkg/gitproxy/hostgit.go`). It runs an ambient git connection's gh (or a
registered source's tool) as the user, because hostd's argv checks cannot admit `<tool> api`'s
endpoints directly (docs/git-hosts.md, "Ambient credentials"). `grain hostd` (`pkg/hostd`) is how a containerised
daemon starts them. It is local to one machine on purpose. There is no
TCP listener, no remote transport, and no general "run this" operation.

It is being landed in three stacked changes:

1. **hostd and its allowlist (this change).** `grain hostd`, the
   allowlist, the socket protocol, `hostd.Client`, and `agent.HostdHost`,
   which is an `agent.Host` whose `Run` goes through hostd.
2. **Wiring (landed; "Behind grain hostd" below, with "Deployments with
   no host executor").** In a container-mode daemon, `container` means a
   plain subprocess in the daemon's own container, `host` means through
   hostd (allowed only for an entry hostd offers), and `auto` meant
   `container` until it was removed. Capability servers get a `run:
   host` field.
3. **Installer (landed; "Installed by setup.sh" below).** The installer
   writes the systemd unit and the allowlist on the Google track and
   migrates host-mode Cloudtops.
4. **Host mode removed (task 5041).** Every machine install is the
   release container plus hostd, by default on both tracks;
   `GRAIN_HOSTD=0` leaves hostd out. `GRAIN_INSTALL_MODE=host` is
   refused, and a re-run migrates a host-mode install.

### What hostd is

hostd is the same `grain` binary, run as `grain hostd` by a systemd unit
**as the installing user**. It sits outside the daemon's lifecycle, so a
daemon restart or upgrade does not restart it. It listens on one unix
socket, `/run/grain-hostd/hostd.sock` by default, which is bind mounted
into the daemon's container at the same path.

Each connection carries one process. The daemon's first frame is a
request. It names an allowlist entry (`cli agy`, `capability
corp-search`) and gives an argv, an environment and a working directory.
hostd checks every part against the entry. If the request passes, hostd
starts the entry's binary and streams frames over the connection:
stdout, stderr, stdin and stdin-close, signals (TERM, INT, HUP or KILL,
always to the whole process group), and finally an exit frame with the
code, the signal, and whether the deadline or the wait delay fired. A
`list` request returns the entries on offer, which is what Settings will
show.

Process semantics match `agent.LocalHost`, so `pkg/agent` cannot tell the
difference:

- the child gets its own process group;
- cancelling the client's context kills that group;
- `WaitDelay` is the same 5 s, so a CLI whose descendants still hold its
  stdout still returns (`TestHostdHostRunReturnsOnceTheCLIIsGone…`, the
  hostd twin of `host_waitdelay_test.go`).

hostd enforces deadlines itself. It uses the earlier of the request's
deadline and the entry's `max_duration` (24 h by default), so a daemon
that has gone away cannot leave a process running forever.

Reaping:

- **Daemon disconnects.** The process's stdin is closed. A protocol-1
  start still running `-disconnect-grace` (10 s) later has its group
  killed. A detached start (protocol 2) keeps running until its own
  deadline; see "Upgrades and host runs" below.
- **A process stops reading stdin.** hostd never waits on a process's
  stdin while reading the daemon's frames. Once the process is 1024
  frames behind, its stdin is closed and the rest is dropped, so a KILL or
  a disconnect is always seen (`TestCancelKillsAProcessThatNeverReadsItsStdin`,
  `TestADisconnectReapsAProcessThatNeverReadsItsStdin`).
- **hostd starts.** hostd records every group it starts under
  `-state-dir`, together with the process's start time. On start it
  kills each recorded group that is still the same process, and leaves a
  reused pid alone.

### Same paths

hostd does no staging. `agent.HostdHost` stages exactly the way
`LocalHost` does, inside the daemon's container. The run's working
directory, the staging dir, the CLI's HOME and session store, and the
transcript files all sit under `GRAIN_DATA_DIR` or `GRAIN_SANDBOX_DIR`.
`docker_run_args` mounts both at the same path they have on the host, so
a host-run CLI finds every file at the path in its argv. An entry's
`paths_under` and `cwd_under` should name those two directories and
nothing else.

### Security

**Grain is trusted as the installing user.** In the release container it
runs as the user's uid, with `--pid host` and `--network host`, and with
the docker socket whenever container sandboxes need it, which is already
root-equivalent on the host. The container is packaging, not a security
wall between grain and the user who installed it. hostd is therefore not
the boundary that keeps a compromised daemon off the host, and nothing
here claims it is.

**What hostd's checks are for.** The allowlist, and the argv, environment
and working-directory checks below, guard against misconfiguration and
accidents: a daemon bug that builds the wrong argv, a setting that points
a CLI at the wrong file, an entry that would hand a credential to the
wrong program. They do not stop a daemon that has been compromised.

**Accepted non-goal: code through a CLI's own config.** A CLI entry's
HOME, config and hooks are written by the daemon: agy's per-run HOME
(`writeAgyHome`, its `settings.json` permissions and the `hooks.json`
PreToolUse hook that runs `grain`, in `pkg/agent/antigravity`) is staged
in the data dir and handed to the host-run agy as `HOME`. A compromised
daemon can therefore make hostd run arbitrary code on the host through a
CLI entry, by writing that CLI's config or hooks. This is accepted
(#5030, finding 2): the daemon could already do as much as the same user,
and HOME stays the daemon's to write.

**Host scripts.** The `host-script` capability server
([capabilities.md](../../docs/capabilities.md#host-scripts-grain-capability-host-script))
runs operator-defined bash scripts. Under `run: host` they run as the
user, with the user's ambient identity. Three points follow:

- **Script bodies live in `grain_config`**, the server's `configure`
  config, which the daemon controls. A compromised daemon can therefore
  run code on the host through them. That is the same accepted non-goal
  as code through a CLI's own config (below): the daemon is already
  trusted as the user.
- **Script text is never agent-controlled.** A grant names a configured
  script and nothing else, and nothing in a sandbox can supply or alter
  script text.
- **Any process in a granted sandbox can call the script, with any
  arguments**, for as long as the grant lasts: the command and its token
  are ordinary files there. Scripts must treat their arguments as
  untrusted, and must validate them the way any program exposed to
  untrusted input does. grain passes them as an argv array and never
  through a shell.

**What is enforced.**

- **The socket is user-only.** It is created 0600 under a 0177 umask, in
  a 0700 directory, and is never exposed over TCP. hostd also checks the
  peer's uid with `SO_PEERCRED`, which the kernel reports across the
  container boundary, and refuses a connection from any other uid.
- **The allowlist belongs to the host.** It lives at
  `/etc/grain/hostd.yaml`. The installer writes it with sudo, and the
  operator may edit it; hostd re-reads it on every request. hostd refuses
  to load one that is not a regular file, is owned by anyone but its allowed
  owners (`Server.AllowlistOwners`: root alone for `grain hostd`; `grain
  demo` and tests allow their own user), or is writable by group or
  others. `/etc/grain` is not mounted into the daemon's container. Even if
  it were, the container runs as the installing user and so cannot write
  a root-owned 0644 file.
- **Nothing runs as root.** hostd refuses to start as root, and its
  children run as the installing user.
- **hostd enforces the deadline.** It uses the earlier of the request's
  deadline and the entry's `max_duration`, whatever the daemon does, and
  reaps on disconnect and on its own restart (above).

**The checks.** Within that profile, each request is still checked:

- **The daemon names an entry, never a binary.** An unknown entry is
  refused and logged. A CLI entry takes the daemon's argv (agent CLIs are
  driven by flags; both claude and agy read their prompt from stdin), but
  every argument is checked:
  - an argument that contains a NUL byte is refused;
  - an argument with a `..` path component anywhere is refused
    (`sub/../../etc/passwd`, `--dir=..`);
  - an argument is checked whole, or, for `--flag=value`, `name=value`
    and a short option with its value attached (`-o/etc/passwd`), as its
    name and value apart;
  - a part that starts with `~` (`~`, `~/x`, `~root/.ssh`) is refused;
  - a part that contains a `/` anywhere, or is `.` or `..`, is a path: it
    must be a clean absolute path that exists, is not itself a symlink
    (unless it is one of the roots), and resolves, through symlinks,
    under the entry's `paths_under`;
  - the child is given each path as it resolved, not as the daemon sent
    it: `--mcp-config=/var/lib/grain/stage/x.json` reaches the CLI as
    `--mcp-config=` and the file's real path.

  A capability entry takes no arguments from the daemon unless it sets
  `extra_args: true`. A working directory must sit under `cwd_under`, or
  the process starts in `/`. `pkg/agent/claude` and
  `pkg/agent/antigravity` each check the argv and environment their real
  `Run` builds against these rules (`hostd_argv_test.go`).
- **The environment is scrubbed.** A child does not inherit hostd's
  environment or the daemon's. It gets:
  - `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG` and `TZ` from hostd;
  - the entry's `pass_env` names (the corp proxy, for example) and its
    `set_env` values;
  - any variable the daemon sends, but only if the entry's `env` names it,
    exactly or as `NAME_*`.

  The daemon's variables are checked too:
  - any name the entry does not accept refuses the whole request;
  - a name the entry's own `set_env` or `pass_env` supplies is refused,
    even where a pattern in `env` would admit it;
  - hostd's identity variables (`PATH`, `HOME`, `USER`, `LOGNAME`,
    `LANG`, `TZ`) and the loader's `LD_*` are admitted only when `env`
    names them exactly; a pattern never matches them;
  - a value that looks like a path (`/x`, `./x`, `../x`, `~x`, or a
    `:`-separated list of them) must pass the same checks as a path in
    argv, and is set to what it resolved to; a `..` component in a value
    with a `/` is refused.

  **Do not list `HOME`, `PATH` or `LD_*` in an entry's `env` unless the
  CLI needs it.** Each hands the daemon what the child reads its config
  from, which binaries it finds, or what the loader injects into it.
  agy's entry lists `HOME` because grain gives every agy run its own
  (the non-goal above). No entry should need `PATH` or `LD_*`.

  Credentials therefore go to one entry only, and preferably as a file
  path in `set_env` rather than a value. A refusal logs a variable's
  name, never its value.
- **What `paths_under` does and does not hold.** It constrains what the
  child is *told*, not what it later opens. The daemon writes under those
  directories, so a name it sends could be made a link to anywhere the
  installing user can read between hostd's check and the child's open.
  Three things close that for the paths in argv and the environment:
  hostd passes the resolved path, so no link the daemon controls sits in
  it at the moment of the check; it refuses a path that does not exist
  yet, since `safepath.Within` lets a missing tail through as it stands
  and the daemon could create it as a link afterwards; and it refuses a
  final component that is a link. A staged file therefore has to exist
  before the CLI is started (agy's diagnostic log is created with
  `CreateTemp` for this reason). What remains is the daemon rewriting a
  directory *on* the resolved path -- replacing a real directory under
  the root with a link -- before the child opens it, or a path the child
  re-opens later, or one it finds inside a file it was pointed at (a
  config naming another path). hostd cannot close that from outside the
  child: only an `openat`/`os.Root`-style open in the child, relative to
  a directory handle it was given, does. Those directories are the
  daemon's own data, which the daemon could already write, and what a
  link there reaches is limited to what the installing user can read.

The tests that hold this in place are in `pkg/hostd`:

- `TestCheckRefuses…`: unknown entries, argv injection and path
  traversal (including `sub/../../../../etc/passwd`, `-o/etc/passwd`,
  `out=/etc/passwd` and `~root/.ssh/id_rsa`), a path that does not exist
  yet or is a link, extra, entry-owned,
  identity, `LD_*` and out-of-root environment variables, and a working
  directory outside the entry's roots;
- `TestCheckPassesTheResolvedPathSoALinkPlantedLaterIsNotFollowed`: the
  spec names the resolved path in argv (`--flag=`, `-o`, bare) and in
  environment values, so retargeting a link after the check changes
  nothing the child is given;
- `TestLoadAllowlistRefusesOneSomebodyElseCouldHaveWritten`;
- `TestRunRefusesAPeerRunningAsAnotherUser`;
- `TestServeSocketIsOwnerOnly`;
- the reaping, deadline, cancel, disconnect and stdin back-pressure tests.

## Trust levels

Host mode is gone, so a machine install is one of three trust levels,
each a superset of the one before it. The installer's readiness report
and Settings -> Models say which hostd answer the install has.

1. **The container, without hostd and without the docker socket.** This
   is `GRAIN_HOSTD=0` on a host with `/dev/kvm`, where Granule sandboxes
   need no docker socket in the daemon's container. grain runs as your
   uid, but its filesystem is the image plus its bind mounts: the data and
   sandbox directories, the journal (read-only) and the few files
   `docker_run_args` names. It has no home directory of yours, no
   host-only tool trees, and no host credentials. `--pid host` lets it see your
   processes (for the Top pane). What limits acting on them is Docker's
   default confinement, not the uid, which is the same as theirs:
   - the default capability set (no `CAP_SYS_PTRACE`, no `CAP_SYS_ADMIN`)
     and seccomp profile;
   - the `docker-default` AppArmor profile (or SELinux's container type)
     where the host has one, which refuses ptrace-mode access to
     processes outside the container. That access is what reading
     `/proc/<pid>/environ` or walking `/proc/<pid>/root` of one of your
     own processes needs.

   **Without an LSM, that second limit is absent.** A same-uid process
   with `--pid host` may then read your processes' environments and
   reach their filesystems through `/proc/<pid>/root`, home directory
   included. Check `docker info --format '{{.SecurityOptions}}'` names
   `apparmor` or `selinux` before relying on level 1 to keep your files
   from grain. `--network host` puts it on the machine's network as any
   of your processes would be.
2. **The container with the docker socket.** Container sandboxes need the
   docker socket when there is no `/dev/kvm`, and the Upgrade button's
   pull uses it. Access to the docker socket is root-equivalent on the
   machine: a process that can start containers can start one that
   mounts `/` and runs as root. At this level grain is as trusted as root,
   whatever its uid.
3. **hostd installed** (the default). This adds back "runs as you, with
   your identity" for allowlisted entries only: agy with your corp
   login, and any `run: host` capability server an operator names in
   `/etc/grain/hostd.yaml`. The checks in "Security" above apply to every
   request. Running arbitrary code through a CLI entry's config or hooks
   remains an accepted non-goal (above). `GRAIN_HOSTD=0` removes this
   level: nothing runs as you on the bare machine, and "host" is not
   offered as a run location, exactly as on Kubernetes.

**Recommendation: Granule sandboxes.** On a host with `/dev/kvm` the
installer chooses Granule microVMs, so the daemon's container does not need
the docker socket for sandboxes, and the install sits at level 1, or level 3
with hostd. On a host without `/dev/kvm`, container sandboxes put it at
level 2. Enable nested virtualization (on a Cloudtop or GCE VM) rather than
accepting the socket.

## Deployments with no host executor

**Kubernetes has no host and no hostd.** Every agent CLI runs as a
subprocess in the daemon pod's own container, and capability servers run
in the pod or as HTTP services. This is the same stance `KubernetesMode`
takes on reboot, restart and shutdown (`pkg/daemon/host.go`).

The daemon decides once, at start, what "host" means for it
(`pkg/daemon/hostexec.go`, `DetectHostExecutor`):

| executor | when                                                                                   | host                         | container                                |
|----------|----------------------------------------------------------------------------------------|------------------------------|------------------------------------------|
| `local`  | the daemon is on the bare machine (`pkg/whererun`), a development daemon              | the daemon's own child       | not offered; a stored one runs on the host |
| `hostd`  | in a container, with `-hostd-socket` set or `/run/grain-hostd` mounted in               | through grain hostd          | a subprocess in the daemon's container   |
| `none`   | Kubernetes (`-k8s-sandboxes` / `-k8s-granule-sandboxes`), or a container with no hostd | refused                      | a subprocess in the daemon's container   |

Kubernetes is decided from the flags alone, so the daemon never looks for
hostd's socket there.

With no host executor:

- Settings → Models does not offer Host for any CLI, and says why ("this
  deployment has no host executor (Kubernetes)").
- `PUT /api/settings/cli-run` refuses `host` with a 400.
- `-claude-run`, `-codex-run` and `-agy-run=host` (and so
  `GRAIN_*_RUN=host`) are refused at startup, as is `-hostd-socket` on
  Kubernetes.
- The default is `container`. That includes agy and custom commands,
  because the daemon's own container needs no launcher.
- A `host` stored by an earlier install, after a migration say, runs in
  the container. It is logged once per CLI per process, and the
  resolution carries a `warning` that Settings shows. It does not fail
  every run.

With hostd configured but down, Host is still offered. Settings shows
hostd's status (`GET /api/settings/cli-run`'s `hostExecutor`), and a host
run fails with `grain hostd is not reachable at <socket>`.

Capability servers' `run: host` follows the same executor: only a
daemon whose executor is grain hostd has a hostd client to start one
(`Config.capabilityHostd`), and anywhere else the server fails to attach
saying so (below).

Tests: `pkg/daemon/hostexec_test.go` covers resolution in each mode.
`tests/security/cli_run_test.go` covers the API's refusal.
`tests/deploy/kubernetes_cli_run_test.go` checks that both Kubernetes
manifests start with default run locations without looking for hostd.
`ui/e2e/cli-run-locations-kubernetes.spec.js` drives the Settings card
against `grain demo -host-executor=kubernetes`.

## Behind grain hostd

With the `hostd` executor, a CLI set to `host` runs through grain hostd
(`agent.HostdHost{Entry: <cli>}`, entry `claude`, `codex` or `agy`) only
when hostd answers and its `Offers()` lists a `cli` entry of that name.
Each resolution asks, so every row carries `hostUnavailable`, the reason
host would fail now whatever the setting: hostd's own "not reachable at
<socket>", or "its allowlist (/etc/grain/hostd.yaml) offers no cli entry
"claude"". A CLI set to `host` takes that as its resolution error.
`cliHostdHost` then fails the run with it, before anything is staged.
Settings shows it in the row as "Host is unavailable for claude: …", next
to the hostd status line that lists everything the allowlist offers.
`grain demo -host-executor=hostd` serves a real hostd on a throwaway
allowlist, so the card can be seen this way.

**Defaults.** Behind hostd every CLI's built-in default is `container`,
the daemon's own. An edition can register another with
`daemon.RegisterHostdDefault`, which applies only behind hostd. The
Google build registers agy `host`, so a Google-track install defaults to
agy on the host and claude, codex and custom commands in the container.
The bare machine, Kubernetes and a container with no hostd keep their
defaults.

**Detached.** claude has a third location behind hostd, `detached`
(`-claude-run=detached`, Settings' "Detached in grain hostd"): the
image's own claude, which the installer's extractor copies into the
versioned hostd directory, started through the container-equivalent
allowlist entry `cli claude-detached` with the container's `HOME` and the
deployment's OAuth token. It forks its `mcpserver` on the host from the
extracted grain, so nothing of the run is the daemon's child and an
upgrade does not interrupt it. `setup.sh` makes it claude's default when
hostd is on (and `GRAIN_CLAUDE_PATH` is not set). A hostd that is down,
incompatible or without the entry runs it in the container, with a
warning in Settings; nothing fails. `host` keeps its meaning: the
operator's own `claude` entry. See [non-disruptive-upgrade.md](non-disruptive-upgrade.design.md),
"Which substrate carries which CLI".

**What a host-run agy is sent.** The antigravity framework
(`pkg/agent/antigravity`, `agyEnv`) adds exactly these variables to
every agy it starts. The installer's allowlist entry for agy (step 3)
must accept all four in `env:`, or hostd refuses the request:

- `HOME`: the run's private agy home, under `GRAIN_DATA_DIR`;
- `AGY_CLI_DISABLE_AUTO_UPDATE`: always `true`, so agy cannot replace
  its pinned binary mid-run;
- `GEMINI_API_KEY`: only when the connection authenticates with a key
  rather than the ambient corp session;
- `GOOGLE_API_KEY`: sent empty beside the key, so an unrelated key in
  the environment cannot win.

With the ambient corp identity, the Google track's case, only the first
two are sent. The entry should still list the key pair, for a connection
that uses a key. Name the variables exactly rather than as `GEMINI_*`, so
nothing else is accepted by accident.

**agy's MCP server and hooks.** agy forks grain's `mcpserver` and its
hook command itself, by the path of the daemon's own binary
(`/usr/local/bin/grain` in the image), which is written into its
`mcp_config.json` and hook config. A host-run agy therefore needs a
`grain` of the daemon's version at that same path on the bare machine,
and the installer (step 3) puts it there beside hostd. That mcpserver
then runs on the host as the installing user. It finds the data
directory at its usual path, reaches the daemon's API over host
networking, and uses the docker socket the user already has. claude and
codex need none of this. Their mcpserver stays in the daemon's container
behind grain's HTTP endpoint (`claude.WithHostMCP`; codex always works
this way).

**Capability servers.** A stdio server whose config sets `run: host`
names a `capabilities:` entry of hostd's allowlist in `command`. The
daemon checks the entry against `Offers()` (kind `capability`), then
starts it with `hostd.Client.Run`. The server's stdio goes over the
socket through a pair of `io.Pipe`s (`protocol.startHostd`).

HTTP capability servers need no field. The daemon's container runs with
`--network host` (`scripts/setup.sh`, `docker_run_args`), so a server
listening on the host's loopback is reachable at the same `serverUrl`.
That holds for every `mcp_endpoint` handed to a CLI too. See
`design/spec/capabilities/capabilities.spec.md` §3.1.1.

Known gaps:

- (Closed.) A `run: host` capability server's ports: hostd reports the
  pid it started (`hostd.Client.OnStarted`), `protocol.startHostd`
  records it, and `ServiceProcess` returns it. The container shares the
  host's pid namespace, so that pid is what its ports (§5.3.2) are
  checked against.
- (Closed.) agy's model-catalog probe (`antigravity/catalog.go`) runs
  through hostd's `agy` entry when agy runs on the host (`CatalogOn`), and
  the container's agy otherwise (`Catalog`).

Tests:

- `pkg/daemon/clirun_hostd_test.go`, against a real hostd on a temporary
  socket: `TestHostdResolutionMatrix` (setting × hostd absent, answering
  without the CLI, or offering it), the edition default, and the
  framework builders.
- `pkg/capability/protocol/hostd_test.go`: a capability server started
  and exercised through hostd, and refused when hostd is absent or does
  not offer it.
- `ui/e2e/cli-run-locations-hostd.spec.js`: the Settings card against
  `grain demo -host-executor=hostd`.

## Installed by setup.sh

Every machine install is container mode: `grain-daemon.service` is a
`docker run` of the release image. `scripts/setup.sh` installs grain hostd
beside it on every install, on the core track and the Google track alike,
unless `GRAIN_HOSTD=0` is set. `GRAIN_HOSTD=auto` and `GRAIN_HOSTD=1` both
install it; `auto` installs none for `GRAIN_USER=root`, since hostd refuses
to run as root. `google/scripts/cloudtop-install.sh` installs the same shape.

- **The binary.** The host extractor (`write_host_extractor`) takes the
  image's `grain` alone, to `/usr/local/bin/grain` on the host. The
  container unit runs it again as an `ExecStartPre`, so an upgrade's
  restart brings the host's binary to the daemon's version. agy forks
  `grain mcpserver` and its hooks from that path. The operator's `grain`
  is then that binary too, run as `GRAIN_USER`; granule stays the image
  wrapper.
- **The unit.** `grain-hostd.service` runs `grain hostd` as
  `GRAIN_USER`, with these settings:
  - `RuntimeDirectory=grain-hostd`, preserved across a stop;
  - `StateDirectory=grain-hostd` for `-state-dir`;
  - the corp proxy from `/etc/grain/hostd.env`.

  `grain-daemon.service` `Wants=` it and starts `After=` it, so the
  socket directory exists before docker mounts it. Neither restarts the
  other.
- **The mounts.** `docker_run_args` mounts `/run/grain-hostd`, the
  directory rather than the socket, so the socket of a restarted hostd
  is the one the running daemon sees. It also sets the container's
  `TMPDIR` to `GRAIN_SANDBOX_DIR/tmp`. `agent.HostdHost` stages through
  `LocalHost`, whose `MkdirTemp` is `os.MkdirTemp("")`. A temporary
  directory in the container's own `/tmp` exists nowhere on the host, and
  hostd would refuse it as outside `paths_under`. Everything else a
  host-run agy is handed is under `GRAIN_DATA_DIR`: its HOME and session
  store, its `mcp_config.json`, hooks and transcripts. `/etc/grain` is
  not mounted. The daemon is given `-hostd-socket`, but not
  `-agy-path`, and agy's directory is not mounted: the allowlist's entry
  names agy, and both agent turns and the catalog probe run through it
  when agy is set to `host`.
- **The allowlist.** `/etc/grain/hostd.yaml` is written once, root
  `0644`. A re-run keeps the file and only restores its owner and mode.
  On the core track it is empty (`clis: {}`), so hostd is there for an
  operator to add entries to. On the Google track its `agy` entry has:
  - `path`: `GRAIN_AGY_PATH`, else `installtrack.GoogleAgyCLIPath`;
  - `paths_under` and `cwd_under`: the data and sandbox directories;
  - `env`: the four names above;
  - `pass_env`: the six proxy spellings;
  - `set_env`: `GRAIN_LOCAL_API_TOKEN_FILE`, the access code's path.
    The mcpserver agy forks calls the daemon's API, and because it
    inherits none of the daemon's environment it was refused with a 401
    until the entry named it.
- **Docker.** The install says whether `GRAIN_USER` is in the `docker`
  group and whether the socket belongs to it. A host-run mcpserver
  reaches the sandboxes through that membership.
- **The opt-out.** `GRAIN_HOSTD=0` installs no hostd, and on a re-run
  removes an installed one: `grain-hostd.service` is disabled and
  deleted, and the daemon's unit is rewritten without `-hostd-socket` and
  without the `/run/grain-hostd` mount. The allowlist, which may hold an
  operator's edits, is kept. A `GRAIN_<CLI>_RUN=host` is dropped with a
  log line, because the daemon refuses a host location it has no executor
  for. The install then behaves like Kubernetes ("Deployments with no
  host executor"): everything runs in the container, and nothing runs as
  the user on the bare machine.
- **What the install says.** The readiness report has a `hostd:` line:
  `installed`, with the allowlist's entries (`clis: agy; capabilities:
  none`) and hostd's status as it is now -- the unit's state, and what
  `grain hostd -status` gets from the socket (its build and every entry
  it offers, with its path), or `NOT ANSWERING` -- or `not installed
  (GRAIN_HOSTD=0)`. The closing summary says
  the same. Settings -> Models shows hostd's state and what it offers,
  or "Host is not offered" with the reason, which names `GRAIN_HOSTD=0`.
- **Migration from host mode.** `GRAIN_INSTALL_MODE=host` is refused
  before anything is written, with the command to run instead. A re-run of
  either installer over a host-mode unit (`Environment=GRAIN_INSTALL_MODE=host`,
  or an `ExecStart` of the bare binary) writes the container unit under
  the same name, `grain-daemon.service`, over the same data directory
  (`/var/lib/grain/{data,scratch}` by default). It also removes what only
  host mode left on the machine: the nft sudoers rule, the old extractor's
  marker, the source copy under `/usr/local/share/grain/src`, and the
  systemd drop-in a host-mode Upgrade wrote. `--git` on a Cloudtop no
  longer selects host mode. An ambient git connection borrows the user's
  own login, which only a daemon on the bare machine could, so `--git` now
  reports that and the summary says how to add a token connection instead.
- **A host-mode install upgraded through the Upgrade button, or by
  hand.** See [Migrating a host-mode install](#migrating-a-host-mode-install)
  below.
- **Rollback.** Rolling back to a previous *version* still works:
  `GRAIN_IMAGE_TAG=<previous tag> sudo ./scripts/setup.sh` runs that
  image in the same container unit. Rolling back to *host mode* is not
  supported after this release. An older `setup.sh` could still write a
  host-mode unit, but nothing here tests that or keeps it working.

### Migrating a host-mode install

Only a re-run of the installer migrates a host-mode install. An upgrade
through the Upgrade button, or by hand (pull the image, rewrite
`image.env`, restart), only swaps the build the old unit starts: the
unit's own `ExecStartPre` takes the new binary out of the new image, and
the daemon comes back up on this build, still on the bare machine
(`pkg/whererun` calls it a host, as it calls a development daemon). A
daemon on the bare machine is offered only the `host` location for every
CLI, and there is no per-run container launcher any more, so a CLI with no
binary on the machine (usually claude, which host mode ran in a per-run
container) has nowhere to run.

The daemon says so (`pkg/daemon/installnotice.go`). It counts as an
unmigrated host-mode install when all of these hold:

- it runs on the bare machine (`whererun.Detect().Host`), not in
  Kubernetes;
- it upgrades by pulling release images (`-upgrade-image`), which the
  host-mode unit passed and a development daemon -- `grain demo`, a local
  `grain daemon`, the e2e harness -- or `grain init`'s unit, which builds
  from a checkout, does not;
- and its unit says host mode: systemd started it (`INVOCATION_ID`) with
  `GRAIN_INSTALL_MODE=host`, or `/etc/systemd/system/grain-daemon.service`'s
  `ExecStart` runs the binary rather than `docker run` / `podman run` (the
  reading `setup.sh`'s `installed_install_mode` makes).

When it does, the daemon:

- logs `level=warning condition=host_mode_install` at start, with the
  signals it saw, and one more line per agent CLI that lost its run
  location ("claude: no host binary, and no container location on a
  bare-machine daemon");
- reports the same notice on `GET /api/config` as `installNotice`
  (`kind`, `message`, `command`, `signals`, `affectedClis`, `docsUrl`),
  which the UI shows as a standing banner above the top bar, in System ->
  Health, and at the top of Settings -> Upgrade;
- refuses every UI upgrade (`hostModeUpgradeRefusal`, through
  `pkg/upgrade`'s `Config.Refusal`) with the same sentence and the command
  to run, since the next build expects container mode.

The fix is the migration above: `sudo ./scripts/setup.sh` (or
`./google/scripts/cloudtop-install.sh` on a Cloudtop). It keeps the data
directory and the unit name, and the restart it ends in clears the notice.
A container install with or without hostd, and a development daemon, show
nothing. `grain demo -host-mode-install` shows the notice without being
one, for the UI's own tests (`ui/e2e/install-notice.spec.js`).

Development daemons keep a plain-binary path: `grain demo`, a local
`grain daemon` and the e2e harness run the daemon on the bare machine,
where `pkg/whererun` finds no container marker and the host executor is
`local`. No installer writes that shape.

Tests:

- `tests/installer`'s `TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode`
  runs on a fake Google-track host, against the package's own install,
  which is made with `GRAIN_HOSTD=0`. It checks that:
  - the fresh `GRAIN_HOSTD=0` install has no hostd unit or socket mount,
    and offers no host;
  - `GRAIN_INSTALL_MODE=host` is refused over a host-mode unit and
    changes nothing;
  - a re-run at the default migrates that unit to container mode plus
    hostd and removes host mode's leftovers;
  - a fake claude runs in the daemon's container;
  - a fake agy (`tests/installer/fakeagy`) runs on the host through
    hostd, with MCP tool calls through the host's grain and a
    transcript;
  - cancel and the entry's `max_duration` deadline kill the run's
    process group;
  - a `run: host` capability server attaches through hostd;
  - the allowlist cannot be written from the container;
  - a re-run keeps the operator's edits;
  - a re-run with `GRAIN_HOSTD=0` switches hostd off and keeps the
    allowlist.
- `tests/installer/hostmode`'s `TestSetupMovesAHostModeInstallToContainerModePlusHostd`
  makes a real host-mode install with the last `setup.sh` that had the
  mode, upgrades it onto the build under test the way the Upgrade button
  does, and checks that it comes back up. It then checks that this
  `setup.sh` refuses `GRAIN_INSTALL_MODE=host`, and that a bare re-run
  migrates the install to container mode plus hostd, keeping the data
  directory.
- `tests/deploy` covers hostd as the default on both tracks, the opt-out,
  the readiness lines and the migration's cleanup. `google/scripts`'
  `TestCloudtopInstallerMigratesEveryHostInstallAndRefusesHostMode`
  covers the Cloudtop installer's side.

## Upgrades and host runs

This section covers a host CLI or `run: host` capability server that has
to survive either kind of upgrade. A **daemon upgrade** restarts the
container and drops every hostd connection. A **hostd upgrade** replaces
the hostd binary. The model is grain's run hosts
([non-disruptive-upgrade.md](non-disruptive-upgrade.design.md), "Run hosts"
and "Run hosts never touch the store"). hostd's processes go through the
same spool format and the same adoption table rather than a parallel
mechanism.

### hostd protocol 2 (implemented)

- **Detached starts.** A start with `detach` set, naming its run and
  task, has its stdout and stderr appended to a spool under hostd's
  `-state-dir` (`spool/<id>/`). The layout matches a run host's: `stdout`
  and `stderr` are append-only, and `exit.json` (code, error,
  finished_at) is written once, by rename, when the process ends. The
  connection that started it is fed the spool from cursor zero. When
  that connection drops, only stdin is closed (input sent while no
  daemon is attached is not kept). The process runs on until it ends,
  is cancelled, or reaches its deadline: the run's own deadline, capped
  by the entry's `max_duration`. `-disconnect-grace` no longer applies
  to it.
- **Listing.** `processes` lists each process with its id, run, task,
  entry, pid, start time, deadline, spool and where each stream ends.
- **Re-attach.** `attach` with an id and a cursor `{stdout, stderr}`
  streams what the spool holds past the cursor, then the exit. The
  cursor counts bytes already delivered, as a run host's does, so a
  second daemon that resumes from the first one's persisted cursor
  repeats nothing and skips nothing. Signals and cancel on an attached
  connection act on the process group as before. Once the exit has been
  sent, hostd drops the spool.
- **Release.** `cancel` kills a process's group if it still runs and
  drops its spool. This is the normal path for a process no live run
  claims.
- **Ended with nobody attached.** A detached process that ends while no
  daemon is attached keeps its final spool for 24 h. It survives a hostd
  restart, which lists it again (`loadSpools`), so the next daemon
  still reads how it ended.
- **Adoption.** `runhost.DecideHostd` maps the listing onto
  `runhost.Info` and decides it with `runhost.Decide`:
  - a live `task_run` row matched to a process is adopted, with the gap
    credited as wait time;
  - an unmatched process is released;
  - a process still running past its deadline is finished and released;
  - a hostd whose protocol this build cannot talk to has every matched
    row finished with the mismatch as its reason.

### The handshake

On `hello` each side sends `{protocol_version, min_compatible, build}`
(`hostd.ProtocolVersion`, `hostd.MinCompatible`). Two peers can talk when
each speaks at least the other's minimum (`hostd.Compatible`). A hostd
that predates `hello` answers "unknown op" and is taken as protocol 1,
which can start and list but not detach.

Bump `ProtocolVersion` for any change the previous version would misread
or could not use: a new op or frame the other side must understand, a
changed meaning for a field, or a new spool layout. A new optional JSON
field that an old peer ignores is not a bump. Raise `MinCompatible` only
when a build stops serving, or stops driving, an old version. Keep it at
`ProtocolVersion-1` or lower for at least one release after a bump, so
that a daemon and a hostd one version apart, in either order, can still
re-attach to what is running.

### Draining hostd for its own upgrade

`grain hostd -drain`, `SIGUSR1`, or the `drain` op starts a drain:

1. New starts are refused with a distinct draining refusal
   (`hostd.IsDraining`). `hello` reports `draining`.
2. Listing, attaching and cancelling go on as normal.
3. Running children keep running until they end.
4. hostd then exits, and systemd restarts it on whatever binary the unit
   points at.

The allowlist's `drain_cap` (default `1h`) bounds how long a stuck child
can hold up the upgrade. At the cap, each remaining child is cancelled
through the normal path, and a line naming it is logged. hostd logs the
cap as the drain starts, and reads it again on every pass, so an edit
made during a drain counts and a read that fails keeps the last cap read.

Drain was chosen over having the new hostd adopt the old one's children.
With drain, the children stay hostd's own: exit status, the deadline and
the process-group kill are exactly what they were. With adoption, a
process would have to outlive its parent and its unit's cgroup and be
watched by a non-parent. A drain does not lose a child's ending either:
a detached process that ended during the drain keeps its spool on disk
for the next hostd to list.

### The daemon's side (implemented)

- **Detached host CLI runs.** A run's host CLI goes through
  `runhost.Hostd` (`agent.HostdHost.Start`). It says hello first, then
  starts the CLI with `detach`, the run's id and task, and the run's
  deadline: its budget plus `runhost.DefaultAllowance`, as a run host's
  deadline label is. The stdout cursor is persisted in
  `task_run_spool_cursor` under a `hostd:` key (`Store.SetHostdCursor`).
  It is written as 0 once the process has started, which is what marks
  the run as driven through hostd, then at most every
  `DefaultCursorEvery`, and once more when the daemon lets go. A call
  that is not a run (a probe, a version check) is a plain protocol-1
  start, and so is every call on a protocol-1 hostd.
- **Letting go.** A daemon that is stopping (`orchestrator.StoppedByDaemon`)
  drops the connection and leaves the process running
  (`hostd.ErrDetached`, surfaced as `agent.ErrDetached`). The upgrade's
  flush does the same (`runHostUpgrade` flushes `runhost.Hostd` beside
  the run hosts). The row stays live for the next daemon.
- **Adoption at startup.** `orchestrator.RecoverHostd` runs beside
  `RecoverRunHosts`. It sends hello, lists the processes, then decides
  with `runhost.DecideHostd` over the live rows that have a hostd cursor
  or that a process names:
  - An adopted row resumes through `ResumeAdoptedRun`, the same path as
    a run host. The attach reads the spool from its start, so the whole
    stdout is there for the framework's `ParseResult`. Only what lies
    past the persisted cursor is appended to the live transcript, and
    the gap is credited as wait time.
  - A released process is cancelled (`Client.Cancel`).
  - A finished row goes through `recoverRun` with the table's reason.
  - `RecoverOrphanedRunsExcept` spares every adopted row, so a daemon
    upgrade never re-dispatches a run hostd is still running.
  - A hostd that does not answer hello decides nothing, and its rows
    are recovered as any other.
- **Handshake gating.**
  - An incompatible hostd (`hostd.Compatible`) starts no new host run.
    The run's resolution and Settings (`HostdStatus.Mismatch`) name the
    mismatch.
  - A draining hostd's refusal is `agent.ErrHostdDraining`. It is
    recorded as interrupted, so the task is re-queued without a strike
    and its next attempt starts on the hostd that replaces this one.
    Settings says "grain hostd is updating" (`HostdStatus.Updating`).
  - Dispatch doesn't start a host run while hostd drains. Admission
    (`AgentPool.Availability`, `hostdDrainHold`) asks hostd's `hello`
    for a task whose every candidate CLI resolves to `host`. If hostd
    says `draining`, the task waits in the queue with that reason, and
    the start and end of the hold are logged once each. So no sandbox
    is built only to be refused, and the refusal above is left for the
    race where hostd starts draining after admission.
- **antigravity** implements `agent.ResultParser` and leaves a detached
  attempt alone (no retry, no HOME cleanup), as claude does. Without
  that, an adopted agy run could not be read.
- **Tested.** `pkg/orchestrator/hostd_test.go` drives the adoption pass
  over fake listings. `pkg/runhost/hostdhost_linux_test.go` flushes and
  re-attaches a real hostd process from its cursor. The installer e2e
  (`tests/installer/zz_hostd_test.go`, "a daemon upgrade leaves a
  host-run agy running") restarts the daemon in the middle of a
  steadily printing host-run fake agy. It then checks that the same pid
  carries on, the run ends in its first attempt, and every line is in
  the transcript once.

### hostd's own upgrade (implemented)

- **Versioned extract.** The extractor (`write_host_extractor`) installs
  each image's grain at `/usr/local/lib/grain/hostd/<image-id>/grain`
  and points the `current` link at it by rename. It keeps the version it
  replaced, since a draining hostd may still run it, and removes anything
  older. `grain-hostd.service` runs `current/grain`.
  `/usr/local/bin/grain` is still installed too, because a host-run agy
  forks `grain mcpserver` from it.
- **A `.path` unit that drains.** `grain-hostd-upgrade.path` watches the
  link. On a change, `grain-hostd-upgrade.service` drains hostd as
  `$GRAIN_USER` (`grain hostd -drain`), unless hostd already runs the
  current binary (`/proc/<pid>/exe`). A hostd too old to drain is
  restarted instead; it has no detached runs to lose. A drained hostd
  exits 0, and the unit's `Restart=always` starts it again on the new
  binary. An Upgrade from the UI therefore reaches hostd: the daemon's
  `ExecStartPre` moves the link, and hostd follows once its runs end.
- **Capability servers do not hold a drain.** A `run: host` capability
  server serves the deployment, not a run, so it never ends on its own.
  hostd's drain waits only for runs. Once they have ended, it ends each
  capability server (logging the line `ending capability server`) and
  exits. The session the daemon's capability registry holds then reports
  ended (`protocol.Server.Ended`): `Lookup` refuses it at once ("has
  ended; it is being started again") instead of handing out a session
  every call over fails, and the registry's next sync (within its retry
  tick, a minute) attaches it afresh through the new hostd. An attach
  that reaches hostd mid-restart fails and is retried on that tick. A
  mint in flight when the server ends fails with the session's error.
- **setup.sh re-runs.** A re-run drains a hostd that holds runs rather
  than restarting it (`hostd_running_runs`). An idle hostd is restarted
  as before.
- **Seeing it.** `grain hostd -status` prints hostd's build and one line
  per process: run, task, pid, and whether a daemon is attached.
  setup.sh's readiness summary prints the same under `hostd:`. Settings
  → Models names hostd's build and protocol in the hostd notice, and
  lists the processes it holds (`HostdStatus.Processes`).

### Not yet wired

- `run: host` capability servers still start attached. They belong to
  the deployment's capability registry, not to a run, so there is no
  run deadline to hold them to. A stdio server also reads its requests
  from the connection that a daemon restart drops. The registry
  re-attaches them on the new daemon instead. A hostd drain ends them,
  and the registry starts them again on the new hostd (above); the
  walkthrough's step 7 should still confirm that on a VM.

## Parked alternatives

- **B: the CLI runs inside the sandbox microVM.** This is the long-term
  direction: the CLI sits next to the tools it drives, and the daemon's
  machine executes nothing model-facing. It needs the credential and
  MCP path to cross into the guest, so it is parked.
- **C: a full container release (now the shape).** The daemon itself
  runs as the container (`GRAIN_INSTALL_MODE=container`), Cloudtops
  included. It used to be parked because a Cloudtop would lose the host's
  corp environment for agy. grain hostd is what answers that: agy, and
  any `run: host` capability server, runs on the bare machine through
  hostd, and only those do ("Installed by setup.sh" above). Host mode
  was removed once this shipped (task 5041).
