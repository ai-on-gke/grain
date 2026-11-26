# OverlayFS warnings during the 2026-11-18 daemon restart

Task 79 investigated this independently of task 78. No live containers,
mounts, images, or services were changed.

## Evidence and limits

Read the baked deployment source at
`/home/debian/grain-src`, revision
`46fdc1104ba8c6febd3abf9820dfb0055240421c`, especially
`scripts/setup.sh` and `Dockerfile`. This is the running build's source,
not a read of the host's generated unit, extractor script, or local overrides.
Those installed files remain a missing piece of evidence.

Queried both daemon and kernel journals for
2026-11-18T03:05:00Z–03:14:00Z, at most 1000 entries per source.
They returned 385 and 146 entries respectively, below that limit.

| UTC | Observed event |
| --- | --- |
| 03:10:55 | Upgrade asks the new image for schema 151; old daemon knows 147. |
| 03:10:57–03:11:00 | A separate disposable image container validates migration of the exported state. |
| 03:11:01 | Upgrade records the pinned image and touches the restart control file. |
| 03:11:01 | Daemon reports zero interrupted runs; Docker stop returns; systemd reports the old service stopped successfully and starts it again. |
| 03:11:03 | Kernel reports both upperdir and workdir already in use by another mount. Neither message names a path, mount ID, or container. |
| 03:11:04 | The pre-start removal of grain-daemon reports no such container; the service starts; Tini warns it is neither PID 1 nor a subreaper. |
| 03:11:04 onward | New daemon starts, migrates the store to 151, and brings up its services. |

The reported subsequent dispatches of tasks 76, 77, and 78, and lack of
observed corruption or OOM, establish recovery, not filesystem safety.
The bounded journals do not show corruption or OOM associated with this
restart.

Repeated the filtered kernel query for `upperdir is in-use` over
2026-11-17T00:00:00Z–2026-11-18T03:06:09Z: no entries matched.
A second filtered query for `overlayfs` over
2026-11-18T03:14:00Z–03:45:00Z also returned none (the query's end was in
the future when issued, so it covers only entries available then).
Neither result proves retention completeness or upgrade causality.

## Mount lifetime assessment

The generated service in `scripts/setup.sh:write_systemd_units`:

1. Stops the named daemon container with `docker stop --time 300`,
   with a systemd stop timeout of 330 seconds.
2. Promotes the image request and runs the host-binary extractor as
   startup pre-steps.
3. Removes a surviving named daemon container, ignoring the ordinary
   absent-container error.
4. Starts a new named container with `docker run --rm`.

The extractor in `write_host_extractor` creates a separate **stopped**
temporary container. It copies grain, then several dereferenced agent
CLI binaries with sequential `docker cp` calls, and removes its temporary
container in an EXIT trap. Hostd executes copies installed on the host;
it is deliberately independent of the daemon service's lifetime.

The repository does not specify OverlayFS upper/work directories or
deliberately give two daemon containers the same writable layer.
A new daemon container and a new extractor container normally have
different writable layers. Sequential copies from the same stopped
container are a candidate for repeated mount/unmount activity, but the
journal does not attribute these warnings to that extractor. Other host
container activity is also not excluded.

The warnings mean that a mount attempted to use directories still
claimed by another OverlayFS mount. They do **not** tell us whether that
older mount still had active file users, or only references awaiting
teardown. The observed service stop ordering argues against two running
daemon main processes; it cannot prove all mounts, namespaces, open
files, or other containers had drained. We cannot classify this event
as benign teardown overlap or unsafe concurrent writable use from the
available evidence.

In this task's isolated Docker engine (hostname `debuerreotype`, kernel
6.1.0-56-amd64, Docker 29.10.0, containerd 2.4.2, containerd OverlayFS
snapshotter), 30 sequential copies from one newly created stopped
scratch-based container did not emit an OverlayFS warning in the kernel
ring buffer. This used a small fixture, not the full deployment image
or a verified match for the live host's runtime. It is a negative
reproduction attempt, not a regression test or proof that extraction is
safe on the affected host.

No speculative mount cleanup, sleep, removal-policy change, or restart
change is proposed. In particular, adding a delay would not establish
that the old filesystem has no users.

## Confirmed Tini defect and repository change

`docker_run_args` deliberately passes `--pid host` so the Top pane can
see host processes. The image originally started
`tini -- /usr/local/bin/grain`. In the shared PID namespace Tini is not
PID 1, and without `-s` it does not adopt orphan descendants. They go to
another ancestor subreaper or namespace init instead.

The repository change adds `-s` to the image entrypoint, which also works
when Tini is PID 1. It preserves the host PID namespace and shutdown
ordering. This fixes a deterministic startup mismatch; there is no
evidence it fixes the OverlayFS event. The warning precedes the new
Tini's startup, and missing subreaper registration alone does not
establish a retained writable mount. Exited zombies do not hold live
process filesystem references.

`TestTheEntrypointReapsOrphansInBothPIDNamespaces` runs the image's actual
entrypoint as UID/GID 1234, replacing only grain with a shell probe. The
probe creates an orphan, requires Tini to adopt it while it is alive,
then terminates it and requires its /proc entry to disappear while the
main process remains running. It covers both private and host PID
namespaces and is gated by the container suite's existing
`GRAIN_TEST_IMAGE` setting.

Locally, small runtime fixture images used the repository's before/after
ENTRYPOINT verbatim with Debian Tini 0.19.0 and BusyBox:

- Before: private namespace passes; host namespace fails adoption and
  prints the same Tini warning.
- After: both namespaces pass adoption and reaping.

Seven focused deployment/image contract tests passed, as did ShellCheck
on the probe. An initial full tests/deploy invocation exceeded the
command's 300-second limit and produced no verdict; it is not counted
as a pass.

The full deployment image was not built locally. Release verification
runs the container suite against that image. The sandbox needed the
Debian packages `tini` and `busybox-static` only to assemble these small
local fixtures.

## Read-only host evidence still needed

The follow-up adds a bounded collector and an approval-gated host-script
registration bundle; see [the invocation and registration guide](../overlayfs-evidence.md).
Local hostd/CLI invocation is tested. Live collection still requires the
separate grain/config change and a task grant; the bundle does not apply
those settings or claim that host privileges are already available.

Collect from the actual deployment host, not an agent sandbox. Do not
restart anything, remove mounts or containers, or prune images. Redact
credentials in command arguments or local unit overrides before sharing.

1. Confirm installed startup code and runtime versions:

   ```sh
   sudo systemctl cat grain-daemon.service
   sudo systemctl show grain-daemon.service -p FragmentPath -p DropInPaths -p ExecStartPre -p ExecStart -p ExecStop -p TimeoutStopUSec -p KillMode
   docker version
   docker info --format '{{.Driver}} {{json .DriverStatus}} {{.DockerRootDir}}'
   uname -r
   ```

   Include the extractor script named by ExecStartPre. Do not include
   environment-file secret values.

2. Supply the retained precise restart-window logs and container events:

   ```sh
   sudo journalctl --utc --since '2026-11-18 03:10:50 UTC' --until '2026-11-18 03:11:10 UTC' -u grain-daemon.service -u docker.service -u containerd.service -u grain-hostd-upgrade.service -o short-iso-precise --no-pager
   sudo journalctl --utc -k --since '2026-11-18 03:10:50 UTC' --until '2026-11-18 03:11:10 UTC' -o short-iso-precise --no-pager
   docker events --since '2026-11-18T03:10:50Z' --until '2026-11-18T03:11:10Z' --filter type=container --format '{{.TimeNano}} {{.Action}} {{.ID}} {{index .Actor.Attributes "name"}} {{index .Actor.Attributes "image"}}'
   ```

   Docker's event buffer is bounded and runtime journals may have
   rotated. Say explicitly when historical evidence is unavailable.
   For any affected container IDs still retained, provide only its
   creation time, state/PID, and GraphDriver metadata, not a full
   environment-bearing inspect.

3. Snapshot currently visible overlay mounts across mount namespaces:

   ```sh
   sudo sh -c 'lsns -t mnt -n -o PID | while read -r p; do
     printf "PID=%s NS=%s\n" "$p" "$(readlink "/proc/$p/ns/mnt")"
     awk "/ - overlay /" "/proc/$p/mountinfo"
   done'
   ```

   Match upperdir/workdir paths to container/snapshot IDs and compare
   mountinfo filesystem device numbers: the same superblock cloned
   across namespaces differs from independent OverlayFS superblocks
   using the same directories. For implicated processes, root/cwd/open
   file mount references and fdinfo mount IDs are needed to distinguish
   retained users from teardown alone. A present-day clean snapshot
   cannot retrospectively settle the 03:11:03 event.

If historical evidence is gone, reproduce on a disposable host matching
the affected kernel/runtime and installed extractor. Capture container
IDs, mount/unmount activity, and filesystem users around each copy and
container start/stop. Keep that host work separate from this image fix;
do not use the live deployment as a reproduction target.
