# The specification's worked examples, as files granule builds

Every directory here is one example out of `docs/granulefile.md` --
§14's worked examples, and the ones §5 gives for an instruction in
passing -- written out as a file granule is asked to build, with what
the result has to be.

They are here because a specification's examples are only worth reading
if they run. Each one is the file the specification prints, with two
kinds of deviation and no others:

* **References.** An example names `ghcr.io/example/my-vm:latest` or
  granule's published guest rootfs. Each such reference is a global
  `ARG` whose default is the reference the specification names, so the
  file still reads as it does in the document; `run.sh` points them at
  images built from this checkout with `-rootfs` and `-vm-image`,
  because a test run happens before anything is published.
* **A line the guest needs.** Where a stock package will not come up in
  granule's guest, the example says so in a comment and fixes it.

## Running them

```sh
# Everything: build each file with granule, then boot what it produced.
test/granulefiles/run.sh

# Against images built from this checkout rather than published ones.
docker build -t granule:latest .
docker build --target guest-rootfs-image -t granule-guest-rootfs:local .
test/granulefiles/run.sh -rootfs granule-guest-rootfs:local

# One example, keeping the image it built.
test/granulefiles/run.sh -only 03 -keep
```

The examples that start from an existing machine (`VM UNPACK FROM`,
`VM FROM`) name `ghcr.io/example/my-vm:latest` and the like, which is
what the specification prints and is published by nobody. With no
`-vm-image`, the run packs a machine of its own out of the guest rootfs
it is using and points those examples at that; `-vm-image <ref>` uses a
machine you already hold instead.

The guest checks boot a real microVM and need `/dev/kvm`; without one
the builds still run and the boots are skipped, which is said in the
output rather than passed over. An example carrying a `needs-kvm` file
is skipped outright there, because its *build* boots a guest too (see
the three below) and there is no half of it left to run.

## What is in an example

| File | What it is |
| --- | --- |
| `Granule` | the example itself (required) |
| context files | whatever its `COPY` lines name |
| `build-args` | `KEY=VALUE` lines passed as `-build-arg` |
| `expect-dockerfile` | substrings the compiled build must contain |
| `expect-unit` | substrings one generated support file must contain |
| `expect-boot` | substrings the guest phase must contain: what the build runs inside the booted machine, and whether it snapshots it |
| `expect-build-error` | the build must fail, saying this |
| `needs-kvm` | the build itself boots a guest, so skip the example without `/dev/kvm` |
| `image-checks` | shell run on the host with `$IMAGE` set |
| `guest-checks` | `granule guest verify` checks, run inside the booted guest |

`go test ./pkg/granulefile` (`examples_test.go`) reads the same
directories and holds every example to the compiled build, the support
files, the refusal and the guest phase. That half needs no docker, no
registry and no `/dev/kvm`, so it runs on every `go test ./...`; this
script is the half that builds and boots.

## The three that boot a guest during the build

`13-pack-from-earlier-stage`, `14-derive-a-machine` and
`15-snapshot-a-warmed-vm` name a `RUN` inside a booted guest, and the
last of them a build-time snapshot as well. Those are level 3 (§2.3),
and building one is not one `docker build`: granule packs the machine,
boots it, runs the stage's instructions in it and commits the result. So
these three need a writable `/dev/kvm` to *build* and not only to check,
which is what their `needs-kvm` says: a run without one skips them
whole, rather than building a machine and skipping the checks, because
there is no build of them that does not boot.

Their guest checks are written to prove the `RUN` really ran in the
guest, since every one of them has an offline explanation that must be
ruled out: a file owned by the account `USER` named, a unit that is
active at boot only because the enable ran inside the machine, and --
for the snapshot -- state in the `/run` tmpfs, which no cold boot could
have.
