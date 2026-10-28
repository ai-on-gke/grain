# Root inside a granule guest is granted on purpose

**Decision (task 1474, following #2394): every granule guest keeps
`debian ALL=(ALL) NOPASSWD:ALL`.** The account that every tool call, agent
and grain CI job runs as has unrestricted root inside its own VM. This is
deliberate. Do not narrow it, remove it, or add a per-access-mode variant
without a new decision from a maintainer. Do not design anything that
assumes a task *cannot* get root in its guest.

The security boundary for a granule sandbox is **the VM and the host
around it**, never the unix user inside it. Treat everything inside the
guest as controlled by the task.

## What the grant is

`Granulefile` and `Granulefile.guest-base` both write the line to
`/etc/sudoers.d/90-debian` (mode 0440) in the step that creates the
`debian` account. `Granulefile` also puts `debian` in the `docker` group,
and that membership is root-equivalent on its own: `docker run
--privileged --net=host -v /:/host …` needs no sudo at all. Taking sudo
away without also taking docker away would change nothing.

Don't confuse it with the **host's** sudoers. `scripts/setup.sh` gives the
host's `$GRAIN_USER` exactly one command: `nft`, for
`pkg/orchestrator/granule_firewall.go`. That grant is deliberately narrow,
because the host is the boundary. The two are unrelated, so narrowing one
is no argument for narrowing the other.

## What a task can do with it, and why that is acceptable

With root in its guest a task can:

- put back the default route that `guestNetworkNoneCommand` deletes on a
  `network_access: none` sandbox;
- send packets from any source address it likes, and run raw sockets;
- rewrite `/etc/resolv.conf`, `/etc/apt`, `/etc/docker/daemon.json`, the
  CA bundle and everything else grain's package-cache setup writes;
- run privileged containers, load what it wants into its own kernel's
  view, and generally do anything to its own VM.

None of these crosses the boundary:

- **NetworkNone is enforced on the host.** `enforceGranuleHostIsolation`
  (`pkg/orchestrator/granule_firewall.go`) installs nftables rules in the
  host's forward, input and bridge paths, which nothing inside the VM can
  reach. A restored default route only turns a fast "Network is
  unreachable" into packets the host drops. `restrictGuestDNS` refuses the
  guest's lookups on the host side as well. If installing either one
  fails, the Acquire fails, because a sandbox whose only isolation is the
  guest's own cooperation is not a NetworkNone sandbox.
- **Spoofed source addresses are caught by where a frame enters, not by
  what it claims.** The bridge-prerouting jump in
  `granuleIsolationRuleset` sends every frame arriving on the VM
  container's own veth ports to the sandbox's chain, whatever its source
  address says. netshim's `spliceGuest` drops spoofed frames inside the VM
  container as well, on images built since it learned to.
- **Credentials are scoped outside the guest.** The git proxy serves only
  the task's own repo and its `reads`, and capability credentials are
  per-task files. Root in the guest reads nothing a task was not already
  given.
- **The VM is disposable.** It is destroyed at the end of the run, and
  `recreate_sandbox` rebuilds it from the image. Nothing a task does to
  its guest outlives it.

So the in-guest steps grain runs (route deletion, DNS and package-cache
configuration, docker0 repair) are **conveniences and fail-fast
behaviour, not controls**. Their code comments say so
(`guestNetworkNoneCommand`, `enforceGranuleHostIsolation`). Keep it that
way. A new isolation property belongs on the host side, enforced where the
guest cannot reach, and is never "the guest won't undo it".

## Why it isn't narrowed

These options were considered and turned down in task 1474:

- **A narrower sudoers rule** (e.g. only `apt-get`, `systemctl`, `ip`).
  Any rule that still lets `apt-get`/`dpkg` through *is* root: a dpkg
  hook, `-o APT::Update::Pre-Invoke::=…`, or a local `.deb` with a
  postinst. The same goes for `systemctl`, `tee`/`install` to arbitrary
  paths, `sed -i`, `usermod` and `ip`. A whitelist that keeps
  `sudo apt-get install` working removes nothing, and suggests a boundary
  that isn't there.
- **Dropping root only on NetworkNone guests** (delete `90-debian` and
  take `debian` out of `docker` at acquire time). This would make the
  route deletion stick, but it protects nothing the host chain does not
  already protect. Those tasks would lose `apt-get` (which works under
  NetworkNone through the on-link package cache), docker, and writes to
  system paths.
- **Removing the grant from the images for everyone.** This breaks what
  every task relies on: the dispatch prompt tells agents to
  `sudo apt-get install`; most of `.grain/workflows/` uses sudo to install
  packages or put tools under `/opt` and `/usr/local/bin`; and
  docker-in-guest needs the `docker` group.

## What depends on it

Changing the grant would break these. Check each one before reopening the
decision.

- **Agents.** `pkg/orchestrator/run.go` tells every run to use
  `sudo apt-get update && sudo apt-get install -y <package>`.
- **grain CI.** Runners execute as `debian`. `tests.yml`, `image.yml`,
  `gcp-smoke.yml`, `nightly.yml`, `kvm-route-stress.yml`, the
  claude/codex/agy surface workflows, `live-agent.yml` and
  `live-frameworks.yml` all use `sudo`.
- **grain's own guest setup.** It runs over `granule exec` as
  `cfg.SSHUser` and uses `sudo -n`:
  - `guestKVMGrant` (`usermod -aG kvm`);
  - `guestDockerNetworkRepair` (`ip link del docker0`, editing
    `/etc/docker/daemon.json`, `systemctl restart docker`);
  - `guestNetworkNoneCommand` (`ip route del default`);
  - `pkg/orchestrator/packagecache.go`: CA certificates,
    `update-ca-certificates`, `/etc/resolv.conf`, apt/pip/docker config,
    `systemctl reload docker`.

  These could instead run as root through the exec transport
  (`DockerExecRunner.User = "root"`, as `guestSelfReport` already does).
  That would be a refactor, not a security change.
- **`scripts/granule-guest-probe.sh`** reports the contents of
  `/etc/sudoers.d/90-debian`.

## When to revisit

Reopen this only if the boundary moves into the guest. That would be the
case if some sandbox's isolation came to depend on the guest's own
cooperation, or a guest started holding a secret that its task must not
read. Either of those is a design change in its own right, and it needs a
maintainer's decision first. Until then, a review comment or task that
proposes "remove passwordless sudo from the guest" should be answered by
pointing here.
