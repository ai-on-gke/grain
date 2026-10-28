# Volumes in a wrapped container

A container runtime gives a container its bind mounts and volumes by
mounting host directories into the container's filesystem. A wrapped
image's container runs inside a VM, so the host's directory has to cross
the VM boundary first. granule does that with virtio-fs: the host attaches
a share, and `granule-container` in the guest mounts it inside the
container's root before the container's process starts.

## Attaching a share

granule does not start `virtiofsd`. The caller does, with the share's
directory, and gives granule the socket (see `docs/rootless.md` for running
it unprivileged). `CHV_FS` and `CHV_VOLUMES` are the same setting, and
granule reads both: `;`-separated entries in either of two forms.

```
tag:source[:destination][:ro|rw]
tag=<tag>,socket=<path>[,destination=<path>][,readonly=on|off]
```

`source` is the virtiofsd socket when it ends in `.sock` or `.socket`, and
otherwise a shared directory. In the key form, `dest` and `target` mean the
same as `destination`, and `shared_dir`, `dir` and `path` name a shared
directory.

Without a destination the share is attached and nothing in the guest
mounts it, as before: a guest that is not a wrapped image mounts it itself
(`mount -t virtiofs <tag> <dir>`).

## Mounting it inside the container

With a destination, an absolute path inside the container, the share is
mounted there before the workload starts:

```sh
docker run ... \
  -e CHV_VOLUMES='data:/run/data.sock:/var/lib/app;conf:/run/conf.sock:/etc/app:ro' \
  app_vm
```

The mapping from tag to destination reaches the guest in the container
override, the small read-only disk that already carries
`CHV_CONTAINER_OVERRIDE` (see `containerspec.Override`), as its `mounts`:

```json
{"mounts": [{"tag": "data", "destination": "/var/lib/app"},
            {"tag": "conf", "destination": "/etc/app", "readonly": true}]}
```

`CHV_CONTAINER_OVERRIDE` can set `mounts` directly. Any it sets come first,
followed by the destinations from `CHV_FS` and `CHV_VOLUMES`. Two mounts on
the same destination, a relative destination, or a mount on `/` are
refused when the host starts, before the VM boots.

In the guest, `granule-container`:

- resolves each destination inside the wrapped filesystem with
  `containerspec.MkdirIn`, the way the container's root will resolve it, and creates
  it if it is missing. A `..` or a symlink in the image cannot put a share
  anywhere on the guest outside the container: a `/data` that the image
  ships as a symlink to `/etc` mounts on the container's own `/etc`. A
  destination that resolves to the container's root is refused.
- mounts parents first, so `/data/cache` lands inside the share on `/data`
  rather than underneath it.
- mounts with `MS_RDONLY` when the mount is read-only. That holds inside
  the guest whatever virtiofsd allows on the host side.
- mounts before it resolves the container's user, program and working
  directory, so a share over any of them is what is found, as it would be
  under a container runtime.
- does not chown a share's root, even if the image declares it as a
  `VOLUME`. Its ownership belongs to the host.
- fails the container's start if a share cannot be mounted (an unknown
  tag, say), leaving the VM up for `granule exec` to look at, the same as
  any other container that cannot start. A container started without its
  volume would write its data into the image instead, where nobody would
  look for it.

The mounts are made in the guest's own mount namespace before the
workload starts. The workload's namespace is a copy of that one, with the
wrapped filesystem bound onto itself and `pivot_root`ed to (see
`cmd/granule-container/enter.go`), so the workload sees them. So does
`granule exec --container`, which chroots into the same directory. Mounts
the workload makes for itself stay in its own namespace, and an exec
doesn't see them. The shares are also recorded in the effective spec at
`/run/granule/container.json`.
