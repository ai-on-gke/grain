# The Granule specification

> **Note**: This project has been renamed from **kontur** to **granule** (`konturctl` -> `granule`, `kontur` -> `granule-shim`, `Konturfile` -> `Granule` / `Granulefile`).


**Version:** `granulefile/1.0-draft`. This is the string §11.4 requires an
implementation to report, and it changes when this document changes in a
way a file can notice.

**Status:** proposed. This document defines the language; it is not a
claim that all of it is implemented. Section 12 states, construct by
construct, what exists in this tree today and what does not.

**Supersedes:** `docs/language_sketch.md`, which this replaces. Section
13 lists every place where this specification decides something the
sketch left open, or changes something the sketch showed, and says why.

A **Granule** describes how to build an OCI image, a granule VM image,
or a set of intermediate stages that produce one. It is a strict
superset of the Dockerfile format: a Dockerfile *is* a Granule, and
building one produces the container image docker would have produced.
One instruction -- `VM` -- is added, and it is the whole of the
difference.

- [1. Overview](#1-overview)
- [2. Notation and conformance](#2-notation-and-conformance)
- [3. Lexical structure](#3-lexical-structure)
- [4. The build model](#4-the-build-model)
- [5. The VM instruction](#5-the-vm-instruction)
- [6. Instructions inside a VM stage](#6-instructions-inside-a-vm-stage)
- [7. Instructions in a packed container stage](#7-instructions-in-a-packed-container-stage)
- [8. Cross-stage copies](#8-cross-stage-copies)
- [9. Artifacts](#9-artifacts)
- [10. Diagnostics](#10-diagnostics)
- [11. Caching and reproducibility](#11-caching-and-reproducibility)
- [12. Conformance levels and implementation status](#12-conformance-levels-and-implementation-status)
- [13. Changes from the language sketch](#13-changes-from-the-language-sketch)
- [14. Worked examples](#14-worked-examples)
- [15. Deliberately out of scope](#15-deliberately-out-of-scope)
- [Appendix A: collected grammar](#appendix-a-collected-grammar)
- [Appendix B: reserved names and paths](#appendix-b-reserved-names-and-paths)

## 1. Overview

### 1.1 What the language is for

granule builds machines, and the format people already have describes
containers. The two are close enough that reusing the format is worth
more than a language of granule's own: the editors, the linters, the
`--build-arg` habits, the layer cache and the registry all carry over,
and what is genuinely different about a VM is small enough to say in one
instruction.

So the rule the whole language follows is: **the vocabulary is docker's
wherever docker has a word for the thing, and what granule adds is one
instruction that changes what the words mean from that point on.**
`RUN` is `RUN` in both kinds of stage -- it runs a command in the thing
being built. What changes is whether that thing is a container or a
machine.

### 1.2 The three shapes of a build

A Granule is one of three things, and which one it is follows from
its contents rather than from a flag:

| The file | What is built |
| --- | --- |
| No `VM` instruction anywhere, or a stage starting with `VM UNPACK FROM` that is never converted | A container image. The file produces a container image (Docker-compatible). |
| A `VM WRAP` or `VM PACK` in the final stage, or a final `VM FROM`, `VM PACK FROM`, or `VM WRAP FROM` stage | A **granule VM image**: a bootable OCI image carrying the VMM half and a guest half, which `docker run` boots (see the top-level `README.md`). |
| `VM` instructions in a stage that is not the target | Whatever the target stage is. Non-target stages exist to be copied from. |

A granule VM image is still an OCI image. It is pushed, pulled, scanned
and run like any other; the VM is what happens when it starts.

### 1.3 The two kinds of stage

Every stage has a **kind**, either *container* or *VM*, and the kind
decides where the stage's instructions execute:

- In a **container stage**, instructions execute the way they do in a
  Dockerfile: `RUN` runs in a container, `COPY` writes into the
  container filesystem, and the stage's result is a filesystem plus OCI
  config. A container stage begins at a `FROM` or `VM UNPACK FROM` header.
- In a **VM stage**, `RUN` executes *inside a booted guest*, `COPY`
  writes into the guest's root filesystem, and the stage's result is a
  guest disk plus the VMM half that boots it. A VM stage begins at a
  `VM FROM`, `VM PACK FROM`, or `VM WRAP FROM` header, or after an
  in-stage converter (`VM PACK` / `VM WRAP`).

A stage's kind can change from container to VM at a **converter** (`VM WRAP` or
`VM PACK`): the instructions above the converter are a container build,
and the instructions below it run in the machine that container build
became. The converter can optionally name the resulting VM stage with
`AS <name>`. That is the mechanism behind the sketch's one-liner -- adding
`VM WRAP` to the end of a Dockerfile turns it into a VM build without
touching a line above it, because everything above it is still a
container build.

## 2. Notation and conformance

### 2.1 Requirement levels

The key words MUST, MUST NOT, REQUIRED, SHALL, SHOULD, SHOULD NOT, MAY
and OPTIONAL are to be interpreted as described in RFC 2119 and RFC
8174, when and only when they appear in capitals.

An implementation that accepts a file this document says MUST be
refused is non-conforming, even if what it builds happens to work. The
failure this rule exists to prevent is the one in §10.1: a file that
says something the builder silently did not do.

### 2.2 Grammar notation

Grammar is given in EBNF, with these conventions:

```ebnf
rule       = alternative1 | alternative2 ;
[ x ]      = x is optional
{ x }      = x repeated zero or more times
"literal"  = a literal, matched case-insensitively for keywords
```

Terminals in capitals (`FROM`, `WRAP`, `PACK`, `UNPACK`) are keywords and are matched
case-insensitively, as Dockerfile matches its own instruction names.
Uppercase is the convention throughout this document and SHOULD be
used.

### 2.3 Conformance levels

An implementation MAY implement the language in three levels, and MUST
report which it implements (§11.4). Each level includes the ones before
it:

- **Level 1 -- Dockerfile.** Files with no `VM` instruction. A level 1
  implementation is a Dockerfile builder.
- **Level 2 -- converters and offline extraction.** `VM PACK`, `VM WRAP`,
  `VM SET`, `VM UNPACK FROM`, and stage-header converters (`VM PACK FROM`,
  `VM WRAP FROM`), in single-stage or multi-stage container builds. No
  instruction executes inside a VM at this level, so no level 2 build
  needs `/dev/kvm`.
- **Level 3 -- VM stages.** `VM FROM`, `RUN` in a VM stage (with `USER`
  and `SHELL` support), `ENTRYPOINT`, `CMD`, `VOLUME` mount handling, `VM SNAPSHOT` (and
  `VM SET snapshot=on`), and cross-stage copies out of a VM stage. `RUN`
  in a VM stage needs `/dev/kvm` on the machine running the build.

An implementation MUST refuse a construct from a level it does not
implement, naming the level (§10.3). It MUST NOT ignore it.

## 3. Lexical structure

### 3.1 Files

A Granule is a UTF-8 text file. Lines are terminated by LF; a CR
immediately before an LF is ignored, so CRLF files parse identically.

The default file name is `Granule`. An implementation resolving a
file by default MUST look for `Granule` in the build context root,
then `Dockerfile`, and MUST use the first that exists. A file named on
the command line (`-f`) is used whatever it is called, including `-` for
standard input.

There is no requirement that a file called `Dockerfile` avoid `VM`
instructions, or that one called `Granule` use them. The name selects
nothing; §1.2 does.

### 3.2 Lines, comments and continuations

Lexing is Dockerfile's, and this document does not restate it. In
summary, and normatively where a Granule-specific rule depends on it:

- A line whose first non-whitespace character is `#` is a comment, and
  is not an instruction.
- A blank line is not an instruction.
- A line ending in `\` continues onto the next line. Comment and blank
  lines *inside* a continuation are skipped rather than ending it.
- A heredoc (`RUN <<EOF`) consumes lines up to its terminator, and those
  lines are the instruction's body rather than instructions. A heredoc
  that is never closed MUST be an error: everything below it is its
  body, so parsing on would validate lines that are not instructions and
  skip the ones that are.

`pkg/guestdockerfile.Parse` implements exactly this much and no
more, deliberately: what an instruction *means* is the container
builder's business, and what is allowed to *appear* is granule's.

### 3.3 Parser directives

A parser directive is a comment of the form `# syntax=...`, `# escape=...`
or `# check=...` in the leading comment block of the file.

- In a file with no `VM` instruction, directives mean what they mean to
  docker and MUST be passed through unchanged.
- In a file that uses `VM`, all three MUST be refused, with the reason
  named (§10).

The reason for the second rule is mechanical rather than a matter of
taste. A Granule that uses `VM` is rewritten -- the implementation
splits it into per-stage builds and supplies lines of its own -- so a
directive that was at the top of the author's file is not at the top of
the file the container builder receives, and would be inert. A `syntax=`
directive additionally replaces the instruction set that was just
validated with whatever that frontend implements, and an `escape=`
directive changes the continuation character that the implementation's
own reading of the file did not follow, so the file built and the file
validated would not be the same file. Inert or unchecked, both are
refusals rather than warnings.

### 3.4 Instruction syntax

```ebnf
instruction = name { WS } arguments ;
name        = "VM" WS verb | dockerfile-instruction-name ;
```

An instruction's name is its first whitespace-delimited token,
case-insensitive. `VM` is the only instruction whose *second* token is
also part of its name; that token is the **verb** (§5).

`VM` is not a Dockerfile instruction, and never has been. A file
containing one was an error to every Dockerfile builder that has ever
existed, so no valid Dockerfile changes meaning under this
specification. This is what makes the superset claim in §1 exact rather
than approximate.

### 3.5 Variable expansion

Variable expansion (`$VAR`, `${VAR}`, `${VAR:-default}`) follows
Dockerfile's rules, in both kinds of stage and in `VM` instruction
arguments. The variables in scope are those set by `ARG` and `ENV` in
the current stage, plus the global `ARG`s declared before the first
stage.

An implementation MUST expand `VM` arguments at the point the
instruction appears, using the same scope an `ENV` on that line would
see.

## 4. The build model

### 4.1 Stages

A build is a sequence of stages. A stage begins at a stage header and ends
at the next one, or at the end of the file.

```ebnf
granulefile   = { global-arg } stage { stage } ;
stage        = stage-header { instruction } ;
stage-header = "FROM" WS ref [ WS "AS" WS name ]
             | "VM" WS "FROM" WS ref [ WS "AS" WS name ]
             | "VM" WS "UNPACK" WS "FROM" WS ref [ WS "AS" WS name ]
             | "VM" WS "PACK" WS "FROM" WS ref { WS pack-option } [ WS "AS" WS name ]
             | "VM" WS "WRAP" WS "FROM" WS ref { WS wrap-option } [ WS name ] [ WS "AS" WS name ] ;
```

Stage names (`AS name`) are case-insensitive, MUST be unique within a
file, MUST NOT be a number, and MUST match `[A-Za-z][A-Za-z0-9_.-]*`.
A stage MAY also be referred to by its zero-based index.

A converter (`VM PACK` or `VM WRAP`) appearing inside a container stage
MAY also name the converted VM stage with `AS name` (`VM PACK AS redis_vm`,
`VM WRAP my_service AS redis_vm`). When a stage begins with
`FROM ... AS container_stage` and later converts with `VM PACK AS vm_stage`,
`container_stage` names the container filesystem prior to the pack (for
`COPY --from=container_stage`), while `vm_stage` names the converted VM stage
(for `COPY --from=vm_stage` or `--target vm_stage`). If the stage header had
no name, `VM PACK AS name` assigns a name to the converted VM stage.

Instructions before the first stage header MUST be `ARG` (the global
`ARG`s) or comments. Anything else there MUST be refused.

### 4.2 Stage kinds

A stage's kind at any point in the file is determined by two things: how
it started, and whether an in-stage converter has been seen in it.

| Stage header | Kind at the header | After in-stage `VM WRAP`/`VM PACK` |
| --- | --- | --- |
| `FROM ref` | container | VM |
| `VM UNPACK FROM ref` | container | VM |
| `VM FROM ref` | VM | — (a converter is refused, rule K3) |
| `VM PACK FROM ref` | VM | — (a converter is refused, rule K3) |
| `VM WRAP FROM ref` | VM | — (a converter is refused, rule K3) |

A container stage that is never converted stays a container stage for
the whole build, and produces a container image.

### 4.3 Converters

`VM WRAP` and `VM PACK` are converters. A converter takes a container
image or filesystem and produces a VM image from it.

Converters can be used in two ways:
1. **In-stage converter:** Placed within a container stage (`VM PACK [options] [AS name]`,
   `VM WRAP [options] [service-name] [AS name]`). From that line on, the
   stage becomes a VM stage: what `RUN` means, what `COPY` writes into,
   and what the stage produces all change.
2. **Stage header converter:** Starting a new VM stage directly from an
   earlier container stage or external container image
   (`VM PACK FROM <ref> [options] [AS name]`,
   `VM WRAP FROM <ref> [options] [service-name] [AS name]`).

The two converters differ in what they do with the container image:

- **`VM PACK`** treats the source filesystem as the machine's root
  filesystem. The result is a machine whose `/` is what the container
  provided.
- **`VM WRAP`** treats the source image -- filesystem *and*
  entrypoint -- as a workload to run *inside* a machine. The result is a
  machine with granule's own guest root filesystem, carrying a container
  runtime and the wrapped image, which it runs at boot.

Both converters support `AS name` to name the resulting VM stage.

"Is the thing I built the machine, or is it something that runs on one?"
is the entire question a Granule author has to answer here, and each
answer has a verb.

### 4.4 The target stage and the result

The **target stage** is the last stage in the file, or the stage named
by the implementation's `--target` option. The build's result is the
target stage's result:

| Target stage kind | Result |
| --- | --- |
| container | An OCI container image. |
| VM | A granule VM image (§9.1). |

Stages that the target does not depend on MAY be skipped entirely; an
implementation SHOULD skip them, as docker does. Whether a skipped stage
is validated is up to the implementation, but an implementation that
validates only what it builds MUST say so in its diagnostics, because a
file whose errors appear one `--target` at a time is worse than one that
reports them all (§10.1).

### 4.5 Well-formedness rules

These are the rules an implementation MUST enforce before building
anything. They are numbered so diagnostics can cite them.

- **K1.** A `VM` instruction MUST have a verb, and the verb MUST be one
  of `FROM`, `WRAP`, `PACK`, `UNPACK`, `SET`, `SNAPSHOT`.
- **K2.** A stage MUST contain at most one in-stage converter (`VM WRAP` or
  `VM PACK`).
- **K3.** An in-stage converter MUST NOT appear in a stage that started with
  `VM FROM`, `VM PACK FROM`, or `VM WRAP FROM`: the stage is already a
  machine, and there is nothing to convert.
- **K4.** An in-stage converter MUST NOT appear before the first stage header.
- **K5.** `VM SET` and `VM SNAPSHOT` MUST appear in a VM stage, or in a container stage
  that is converted later in that same stage. They MUST NOT appear in a
  stage that never becomes a VM stage, because nothing would read them.
- **K6.** `FROM scratch` MUST NOT be converted by `VM WRAP`: a wrapped
  workload is run by a container runtime inside the guest, and an image
  with no filesystem has nothing to run.
- **K7.** A stage MUST NOT `COPY --from` itself, and the stage graph
  MUST be acyclic. (`--from` may only name an earlier stage or an
  external image, which makes this structural.)
- **K8.** A file that uses `VM` MUST NOT carry parser directives (§3.3).
- **K9.** Instructions refused for the stage kind they appear in (§6,
  §7) MUST be refused.
- **K10.** A file MUST contain at least one stage. A target stage that
  was *not* named explicitly (§4.6) MUST contain at least one
  instruction beyond its header: building the last stage of a file that
  does nothing would publish a copy of its own base under a new name,
  which is a mistake rather than an intention. A stage that exists only
  to be copied out of MAY be empty, and an explicitly named target MAY
  be, since both were asked for by name.
- **K11.** `VM UNPACK FROM` MUST only appear as a stage header. It MUST
  reference a valid granule VM image (§5.4) and MUST NOT reference `scratch`.

### 4.6 Options an implementation provides

This specification is about the file, not about a command line, and it
does not fix flag spellings. It does require that an implementation
offer a way to express each of the capabilities below, and that it
document the spelling it chose; the right-hand column is the
conventional spelling, which is the one used throughout this document.

| Capability | Conventional spelling |
| --- | --- |
| Name the file to build (§3.1), `-` for standard input | `-f`, `--file` |
| Name the build context | positional argument |
| Name the result | `-t`, `--tag` |
| Build a stage other than the last (§4.4) | `--target` |
| Set a build-time variable (§3.5) | `--build-arg` |
| Validate and report, building nothing | `--check` |
| Show what would be built without building it | `--print` |

Every row is REQUIRED, and the last two are the ones worth saying so
about: a way to validate a file, and a way to see what would be built,
without building either. That is what makes §10.1's "fail in a second
rather than after four minutes" available to an author's editor and to
CI, and not only to a build that was going to run anyway.

## 5. The VM instruction

```ebnf
vm-instruction = "VM" "FROM" ref [ "AS" name ]
               | "VM" "UNPACK" "FROM" ref [ "AS" name ]
               | "VM" "WRAP" [ "FROM" ref ] { wrap-option } [ service-name ] [ "AS" name ]
               | "VM" "PACK" [ "FROM" ref ] { pack-option } [ "AS" name ]
               | "VM" "SET" setting { setting }
               | "VM" "SNAPSHOT" ;
```

Options are written in Dockerfile's own flag style -- `--name=value`,
before positional arguments -- so that an author who knows
`COPY --from=` and `RUN --mount=` already knows the shape. An
implementation MUST refuse an unknown option rather than ignoring it,
and MUST refuse an option given twice.

### 5.1 `VM FROM`

```ebnf
"VM" "FROM" ref [ "AS" name ]
```

Starts a VM stage from an existing granule VM image. `ref` is an ordinary
image reference, resolved and pulled the way `FROM`'s is.

- The referenced image MUST be a granule VM image: it MUST carry a guest
  half at the paths in §9.1. An implementation MUST check this before
  running any instruction in the stage.
- A reference to a *guest rootfs* image (one that is a filesystem rather
  than a bootable image) MUST be refused with a diagnostic naming `VM
  PACK`, since `FROM <ref>` followed by `VM PACK` (or `VM PACK FROM <ref>`)
  is what the author meant. This mirrors what `granule guest build`
  already says when it is handed one.
- The VMM half of the result is the implementation's own current one,
  not the referenced image's. A `VM FROM` of an image built a year ago
  boots under today's hypervisor, entrypoint and agents; this is the
  rebase described in `docs/guest-images.md` applied automatically,
  because "inherit the machine, keep my hypervisor" is the only reading
  of `VM FROM` that is safe to offer.
- `VM FROM scratch` MUST be refused. There is no empty machine: a guest
  is a disk with an init on it.

### 5.2 `VM WRAP`

```ebnf
"VM" "WRAP" [ "FROM" ref ] { wrap-option } [ service-name ] [ "AS" name ]
wrap-option  = "--name=" name
             | "--restart=" ( "always" | "on-failure" | "no" )
             | "--exit-action=" ( "ignore" | "poweroff" )
             | "--runtime=" runtime-name
service-name = name ;
```

Converts a container stage into a VM stage whose guest runs that
container image as a workload. When `FROM ref` is specified, it acts as
a stage header starting a new VM stage from that container image or
earlier stage; when omitted, it converts the current container stage.

The guest root filesystem is granule's own (the same rootfs a `VM PACK`
of `ghcr.io/gke-labs/granule/debian.rootfs` would produce), plus a
container runtime and the wrapped image. The wrapped image is embedded
in the guest as an OCI layout (§9.2) and run at boot by a generated
service unit.

- The workload's command is the wrapped image's own `ENTRYPOINT`/`CMD`.
  An image with neither MUST be refused: there would be nothing to run.
- `ENV`, `WORKDIR`, `USER`, `VOLUME` and `EXPOSE` recorded in the wrapped image
  apply to the workload as they would under any container runtime.
  `EXPOSE` is documentation there as it is anywhere; ports reach the
  guest through the container's own network (see the top-level
  `README.md`).
- The **service name** is the positional argument, or `--name=`, or, if
  neither is given, the stage's name, or `app` for an unnamed stage. It
  MUST match `[a-z0-9][a-z0-9_.-]*`. It names the generated unit and the
  layout directory (§9.2), and is what `granule exec` into the guest will
  see running.
- `AS name` names the converted VM stage, allowing later stages to copy
  from it (`COPY --from=name`) or the CLI to target it (`--target name`).
- `--restart=` sets the unit's restart policy and defaults to `always`.
- `--exit-action=` decides what a workload exit does to the machine:
  `ignore` (default) leaves the guest running -- an operator can still
  `granule exec` into it, which is the whole reason a failed workload
  should not take the machine with it -- and `poweroff` shuts the guest
  down, which makes the VM image behave like the container it wraps for
  one-shot jobs.
- `--runtime=` names the in-guest container runtime. An implementation
  MUST support at least one and MUST document its default; it MUST
  refuse a name it does not implement.

Instructions after a `VM WRAP` run in the guest, *not* in the wrapped
workload (§6). This is the seam the sketch's "now it runs inside a VM"
comment is pointing at, and it is worth stating twice: a `RUN` below
`VM WRAP` changes the machine, not the container.

### 5.3 `VM PACK`

```ebnf
"VM" "PACK" [ "FROM" ref ] { pack-option } [ "AS" name ]
pack-option = "--variant=" ( "debian" | "alpine" | "auto" )
            | "--disk-extra-mb=" integer ;
```

Converts a container stage into a VM stage whose guest root filesystem
is that stage's filesystem, by packing it into a bootable disk. When
`FROM ref` is present, it acts as a stage header starting a new VM stage
by packing the named container image or earlier container stage `ref`.
When omitted, it converts the current container stage.

- The stage's filesystem MUST be a **guest rootfs**: it MUST carry an
  init (`/sbin/init`) the packed disk can boot. An implementation MUST
  check this during the pack and MUST refuse a filesystem that has none,
  naming what is missing. Building `FROM
  ghcr.io/gke-labs/granule/debian.rootfs`, unpacking an existing VM with
  `VM UNPACK FROM`, or packing directly via `VM PACK FROM <rootfs>` is
  the supported way to have one (see `deploy/guest-image/README.md`); an
  arbitrary container base is not a machine.
- `AS name` names the converted VM stage, making it addressable by
  `--target name` and `--from=name` independently from any name the
  container stage carried before the pack.
- granule's own guest-side pieces -- `granule-agent`, `granule-mem-agent`,
  the control-net overlay, the units -- are laid over the filesystem
  *during* the pack, out of the implementation's own current build, not
  out of the author's stage. An author neither has to install them nor
  can pin an old copy of them. This is `pkg/guestpack`'s existing
  property and this specification keeps it: the parts of a guest that
  must not come from a consumer are supplied by whoever packs.
- Nothing from the packed filesystem is executed. A `VM PACK` needs no
  `/dev/kvm` and runs nothing of the author's, which is what makes a
  level 2 build safe to run on a machine whose isolation is the point.
- `--variant=` selects which overlay is laid down; `auto`, the default,
  asks the filesystem which init system it runs.
- `--disk-extra-mb=` adds headroom in MiB on top of the 20% the disk is
  sized with, for a guest that will be provisioned further after packing
  -- by `RUN`s below the converter, or after it ships. Every MiB is paid
  by every host that pulls the image, so growing the overlay at boot is
  usually the better lever.

### 5.4 `VM UNPACK FROM`

```ebnf
"VM" "UNPACK" "FROM" ref [ "AS" name ]
```

Starts a **container stage** whose root filesystem is extracted from an
existing granule VM image `ref`.

- `ref` MUST be a bootable granule VM image carrying `/var/lib/granule/guest/disk.img`
  (§9.1). The implementation extracts the guest root filesystem from that
  disk image into the container stage's root filesystem.
- The stage kind is **container**. Instructions in this stage execute
  offline as ordinary container instructions (`COPY`, container `RUN`,
  `ENV`, `WORKDIR`, `USER`, `SHELL`, `VOLUME`) without requiring `/dev/kvm`.
- The stage may subsequently be converted back into a VM image using
  `VM PACK` (or `VM WRAP`). This enables updating an existing VM image
  (e.g., copying files or configuration in) entirely offline without
  needing `/dev/kvm`:

  ```dockerfile
  VM UNPACK FROM ghcr.io/example/my-vm:latest
  COPY myfile /myfile
  VM PACK
  ```

- `VM UNPACK FROM` MUST appear only as a stage header. It MUST NOT appear
  mid-stage.
- If `ref` is not a valid granule VM image, or references `scratch`, the
  instruction MUST be refused.

### 5.5 `VM SET`

```ebnf
"VM" "SET" setting { setting }
setting = key "=" value ;
```

Records a default for the machine the image boots. Settings are the
image's defaults, not fixed limits: the runtime environment overrides
them the way a `docker run -e` overrides an `ENV`, and this
specification does not give a Granule a way to prevent that.

| Key | Meaning | Runtime variable |
| --- | --- | --- |
| `cpus` | vCPUs at boot | `CHV_CPUS` |
| `cpus-max` | ceiling for hotplug | `CHV_CPUS_MAX` |
| `memory-mb` | memory at boot, MiB | `CHV_MEMORY_MB` |
| `memory-max-mb` | ceiling for hotplug, MiB | `CHV_MEMORY_MAX_MB` |
| `memory-hotplug` | `on`/`off` | `CHV_MEMORY_HOTPLUG` |
| `kernel-cmdline` | extra kernel command line | `CHV_CMDLINE` |
| `disk-mode` | `overlay`/`persistent`/`readonly` | `CHV_DISK_MODE` |
| `disk-size-mb` | grow the writable overlay at boot | `CHV_DISK_SIZE_MB` |
| `extent-size-mb` | overlay extent size, MiB (default 64) | `CHV_EXTENT_SIZE_MB` |
| `disk-num-queues` | virtio-blk queues (`num-queues`) | `CHV_DISK_NUM_QUEUES` |
| `disk-iothreads` | dedicated iothreads (`iothreads`, `on`/`off`) | `CHV_DISK_IOTHREADS` |
| `disk-io-engine` | block I/O engine (`io-engine`, `io_uring`/`epoll`/`sync`) | `CHV_DISK_IO_ENGINE` |
| `shutdown-timeout` | graceful shutdown budget | `CHV_SHUTDOWN_TIMEOUT` |
| `snapshot` | snapshot VM on last instruction (`on`/`off`) | `CHV_SNAPSHOT_PATH` |

An implementation MUST refuse an unknown key, and MUST validate a
value's syntax at build time rather than leaving it to fail at boot. It
MUST record settings as the corresponding runtime variables in the
produced image's OCI config, so that nothing has to read a Granule to
boot the result.

`snapshot` is the one key that is an instruction rather than a default,
and its row above names the variable it is about rather than a value to
record: `snapshot=on` is `VM SNAPSHOT` (§5.6), which the *build* carries
out. What it records is the snapshot bundle, and `CHV_SNAPSHOT_PATH` is
the path the runtime then restores from -- so an implementation MUST NOT
write the setting's own value into that variable, which is a path and
would refuse to boot as one.

A later `VM SET` of the same key in the same stage overrides an earlier
one. A stage started by `VM FROM` inherits the settings recorded in the
image it came from, and its own `VM SET`s override those.

### 5.6 `VM SNAPSHOT`

```ebnf
"VM" "SNAPSHOT"
```

Instructs the builder to snapshot the running VM state upon completing the
last instruction in a VM stage, saving the full VM state directly into the
produced VM image artifact.

- **Build-time behavior:** During a VM stage build (which boots the guest to
  execute `RUN` instructions under Level 3 conformance), the builder pauses
  the VM after executing the stage's final instruction, captures a full snapshot
  of the VM's state (guest RAM, vCPU and device registers, and the writable disk
  overlay `granule-disk-overlay.qcow2`), and packages the snapshot into the
  output image under `/var/lib/granule/guest/snapshot/` (§9.1).
- **Runtime behavior:** When the resulting image is run (`docker run` or
  `granule run`), the VMM detects the embedded snapshot and launches with
  `--restore source_url=file:///var/lib/granule/guest/snapshot,resume=true`
  (`CHV_SNAPSHOT_PATH`), restoring guest memory and the disk overlay directly.
  The machine resumes execution from the saved snapshot state rather than
  performing a cold boot from scratch, enabling instant startup and skipping
  runtime initialization.
- **Sizing:** a restore replays the machine's shape from the snapshot rather
  than from the image's own settings, so a snapshotted machine's memory and
  vCPUs are decided when the snapshot is taken and not when it is run. An
  implementation MUST therefore boot the stage at the size the file asks for
  (§5.5's `memory-mb`, `cpus`), because a `VM SET` recorded on an image that
  resumes is a setting the machine can never honour -- an instruction dropped
  rather than built (§10.1). It follows that overriding one of these in the
  runtime environment, which §5.5 allows for every other machine, does nothing
  to this one.
- **Stage kind:** `VM SNAPSHOT` MUST appear in a VM stage (or in a container
  stage converted with `VM PACK` or `VM WRAP`). Placing `VM SNAPSHOT` in a
  container stage that is never converted MUST be refused (Rule K5).
- **Attribute equivalence:** Setting `VM SET snapshot=on` is equivalent to
  specifying `VM SNAPSHOT`, declaring that the VM stage should be snapshotted
  on the final instruction.
- **Conformance level:** Snapshotting requires running the guest under KVM
  (Level 3 conformance, §2.3). If a VM stage contains no `RUN` instructions,
  `VM SNAPSHOT` boots the VM to capture the initial running snapshot state.

## 6. Instructions inside a VM stage

A VM stage is a machine under construction. Instructions in it have the
meanings below, from the converter (or the `VM FROM`, `VM PACK FROM`,
`VM WRAP FROM` header) to the end of the stage.

| Instruction | Meaning in a VM stage |
| --- | --- |
| `RUN` | Execute the command **inside the guest**, as the active user (defaulting to `root`, or as set by `USER`), with the machine booted. Shell-form commands run under the shell set by `SHELL` (default `["/bin/sh", "-c"]`). Needs `/dev/kvm` (level 3). A non-zero exit fails the build. |
| `COPY` | Write into the guest root filesystem. Applied offline where the implementation can; no boot is required by this instruction. |
| `ADD` | As `COPY`, with tar extraction. Remote sources are refused (below). |
| `ENV` | Set variables for subsequent `RUN`s in this stage, and for expansion. **Build-time only**: they do not persist into the booted machine. To give a machine environment, write it (`/etc/environment`, a unit's `Environment=`). |
| `WORKDIR` | Set the working directory for subsequent `RUN`s and the default target of relative `COPY` destinations. Creates it if absent. |
| `USER` | Set the active user and optional group (`USER <user>[:<group>]` or `USER <uid>[:<gid>]`) for subsequent `RUN` instructions in this stage, matching Docker build behavior. Defaults to `root` (UID 0) if not specified. An implementation MAY refuse a user the guest filesystem does not define. |
| `SHELL` | Override the default shell interpreter (`["/bin/sh", "-c"]`) used for shell-form `RUN` instructions inside the guest. Syntax: `SHELL ["executable", "param1", ...]`. |
| `VOLUME` | Declare volume mount points inside the guest (`VOLUME <path>...` or `VOLUME ["<path>", ...]`). Mount points are recorded in image metadata; at runtime, an external volume (e.g. virtio-fs, 9p, or virtio-blk) can be mounted at this path, or an auxiliary disk image provisioned. Writes to the path prior to the `VOLUME` instruction are preserved in the root disk; subsequent `RUN` instructions in the build should not expect volume mutations to persist across runtime boots. |
| `ENTRYPOINT` | Set the default executable or process to run inside the guest VM at boot. While many VMs use systemd or OpenRC to manage background services, `ENTRYPOINT` provides a native way to configure the primary workload command inside the VM without requiring custom service unit files. Accepts exec form `ENTRYPOINT ["executable", "param1", ...]` or shell form `ENTRYPOINT command param1`. |
| `CMD` | Set default arguments for the guest `ENTRYPOINT`, or the default command to execute if `ENTRYPOINT` is not set. Trailing arguments passed at VM launch override `CMD`. Accepts exec form `CMD ["param1", "param2", ...]` or shell form `CMD command param1`. |
| `ARG` | As in a container stage: a build-time variable, in scope for the rest of the stage. |
| `LABEL` | Recorded on the produced image's OCI config, as it would be on a container image. |
| `VM SET` | §5.5. |
| `VM SNAPSHOT` | §5.6. Snapshot the running VM on the last instruction and save it into the image, so that running the image resumes rather than starting fresh. |

Everything else MUST be refused. The refusals, and the reason each one
is a refusal rather than a no-op, are the ones
`pkg/guestdockerfile` already gives, because they are properties of
what a machine is rather than of the implementation:

| Refused | Because |
| --- | --- |
| `EXPOSE` | Port metadata belongs to the container that publishes the ports, which is the granule image around this guest, not the guest. |
| `HEALTHCHECK` | Docker health checks probe a container's main process, and this guest is not run as one. Health is a unit's business inside the machine, or `granule vm status`/`vm wait` outside it. |
| `STOPSIGNAL` | A guest is stopped by an ACPI power button press its init turns into a shutdown. There is no process here to signal. |
| `ONBUILD` | Defers instructions to whoever builds `FROM` the result, where none of them is validated against these rules. |
| `MAINTAINER` | Long deprecated. `LABEL org.opencontainers.image.authors=` is the supported spelling. |
| `FROM`, `VM FROM`, `VM UNPACK FROM`, `VM PACK FROM`, `VM WRAP FROM` mid-stage | These start a stage; they cannot appear inside one. |
| `VM WRAP`, `VM PACK` in a VM stage | Rule K3. |
| `ADD` of a remote source | It fetches at build time from a host outside the build context, with the network of whatever machine ran the build and nothing recording what came back. `COPY` it out of the context, or fetch it in a `RUN` where the checksum is yours to check. |

An implementation MUST refuse these rather than dropping them. A format
that silently discards half of what it was handed is worse than one that
never claimed the resemblance: a dropped instruction is a machine that comes
up without the configuration the file specified, discovered in production
rather than at build time.

## 7. Instructions in a packed container stage

Above a converter, a stage is an ordinary container build, and how much
of Dockerfile it may use depends on which converter ends it:

- **Above `VM WRAP`**, the full Dockerfile instruction set is available.
  The stage really is building a container image, and an `ENTRYPOINT`,
  a `CMD`, a `USER`, an `EXPOSE`, a `VOLUME`, a `HEALTHCHECK`, or a `SHELL` in it means what it
  means to docker -- the workload's, not the machine's. This is what
  makes "take this Dockerfile and add one line" work without editing
  anything above the line.
- **Above `VM PACK`**, the stage is building a root filesystem. `ENTRYPOINT`
  and `CMD` are supported and recorded into the packed VM's workload
  configuration, providing a native way to run commands in the guest at boot.
  The remaining instructions in §6's refusal table that describe a
  *container's* runtime -- `EXPOSE`, `HEALTHCHECK`, `STOPSIGNAL`,
  `ONBUILD` -- MUST be refused with the same reasons. They would be
  recorded in an image config that nothing reads, since what is built
  from this stage is a disk.

An implementation MUST make this determination from the converter that
ends the stage, which means it MUST parse the whole stage before
validating any of it.

`ARG`, `ENV`, `LABEL`, `WORKDIR`, `USER`, `SHELL`, `VOLUME`, `ENTRYPOINT`, `CMD`, `RUN`, `COPY` and `ADD` are
available above either converter.

## 8. Cross-stage copies

`COPY --from=<stage>` and `ADD --from=<stage>` work across every
combination of stage kinds. `<stage>` is a stage name (assigned via
`AS name` at a stage header or at a converter like `VM PACK AS name`),
a stage index, or an external image reference.

| Source kind | What `--from` reads |
| --- | --- |
| container stage | That stage's filesystem, as in a Dockerfile. |
| VM stage | That stage's **guest root filesystem**, at its state at the end of the stage -- including anything a `RUN` in it wrote. |
| external image | That image's filesystem. If it is a granule VM image, its guest root filesystem is *not* what is read; its container filesystem is, which is where the guest disk lives as a file. Copy a path out of a machine by making it a `VM FROM` stage or extracting it with `VM UNPACK FROM`. |

The destination follows the destination stage's kind: into a container
stage it is the container filesystem, into a VM stage it is the guest
root filesystem.

A copy out of a VM stage MUST NOT require booting that stage's guest. An
implementation MAY read the packed disk directly; if the stage's own
instructions required a boot, the copy reads what that boot left behind.

## 9. Artifacts

### 9.1 A granule VM image

The result of a VM target stage is an OCI image carrying both halves:
the VMM half (the granule runtime, cloud-hypervisor, netshim, the
entrypoint) and the guest half, at the fixed paths
`pkg/guestimage` defines:

```text
/var/lib/granule/guest/disk.img         the packed guest root disk (raw)
/var/lib/granule/guest/vmlinuz          the guest's own kernel, when it has one
/var/lib/granule/guest/initrd.img       the initramfs matching it
/var/lib/granule/guest/vmlinux          the VMM half's kernel -- not the guest's
/var/lib/granule/guest/snapshot/        the VM memory and device snapshot state, when snapshotted
/var/lib/granule/overlay/disk.qcow2     per-VM writes, created at boot
```

Those paths are absolute and resolved inside the VM container, and their
suffixes are functional -- `hypervisor.BuildArgs` decides `image_type`
from them. An implementation MUST use them. See
`docs/guest-images.md` for why both properties are load-bearing.

When an image is built with `VM SNAPSHOT` (or `VM SET snapshot=on`), the
snapshot bundle (cloud-hypervisor memory file, device state `config.json`, and
the snapshotted disk overlay `granule-disk-overlay.qcow2`) is saved in the image
under `/var/lib/granule/guest/snapshot/`. At boot, the VMM detects the snapshot
and launches with `--restore source_url=file:///var/lib/granule/guest/snapshot,resume=true`
(`CHV_SNAPSHOT_PATH`), restoring the guest's memory and overlay directly so
the machine resumes instead of cold-booting.

The image's entrypoint is the VMM half's, so `docker run` on the result
boots the machine with no arguments, and `granule ready`, `granule exec`
and `granule cp` work against it as they do against any granule image.

### 9.2 A wrapped workload

`VM WRAP` MUST place the wrapped image in the guest filesystem as an OCI
layout under `/var/lib/granule/wrap/<service-name>/`, and MUST generate a
service that runs it at boot under the chosen runtime, named
`granule-wrap@<service-name>` (a systemd unit on the debian variant, an
OpenRC service on the alpine one).

Nothing in the wrapped image is executed during the build.

### 9.3 Labels

An implementation MUST record, on a VM image it produces:

| Label | Value |
| --- | --- |
| `dev.granule.guest.packed-from` | the rootfs image the guest disk was packed from, where there is one |
| `dev.granule.granulefile.stage` | the target stage's name, or its index |
| `dev.granule.granulefile.kind` | `pack`, `wrap` or `from` |
| `dev.granule.wrap.image` | on a `VM WRAP` result, the wrapped image's digest |
| `dev.granule.guest.snapshotted` | `true` if the VM image carries a saved snapshot state for resume |
| `dev.granule.granulefile.version` | the specification version the build implements (§11.4) |

`dev.granule.guest.packed-from` is the label `granule guest rebase`
already reads to say what a cheap rebase did not refresh; the others
exist so the same question -- "what was this built from, and how?" --
can be answered about a Granule build without the Granule.

## 10. Diagnostics

### 10.1 Fail closed, and report everything

Two rules, and they are the ones the format's credibility rests on:

1. **An implementation MUST refuse what it does not implement.** Never
   ignore, never warn-and-continue. §6's refusal list is refusals for
   this reason, and so is §5's unknown-option rule.
2. **An implementation MUST report every violation in a file, not the
   first.** An author porting a container Dockerfile typically trips
   several at once -- a `HEALTHCHECK`, a `STOPSIGNAL` and an `EXPOSE` are one
   habit, not three -- and a validator that reports one per build turns
   a five-minute edit into five builds.

Validation MUST happen before anything is built. The point of validating
is to fail in a second rather than after a four-minute build of
something that was never going to boot.

### 10.2 Message form

A diagnostic MUST name the file, the line in the author's own file
(1-based, counting comments, blanks and continuation lines), and the
instruction as the file spells it, so it can be searched for:

```text
Granule: 2 instructions granule cannot build a guest from:
  Granule:14: HEALTHCHECK: docker health checks probe a container's main process: ...
  Granule:16: EXPOSE: port metadata belongs to the container ...

What a granule VM stage may use: RUN, COPY, ADD, ENV, WORKDIR, USER,
SHELL, VOLUME, ENTRYPOINT, CMD, ARG, LABEL, VM SET, VM SNAPSHOT. See docs/granulefile.md.
```

A reason MUST be a sentence or two saying what granule does instead, or
what to write instead. A refusal that only says "unsupported" leaves the
reader to guess whether they have hit a policy or a gap.

### 10.3 Unimplemented versus forbidden

These are different answers and MUST read differently. An instruction
this specification forbids is forbidden, and the diagnostic says why it
could not work. A construct the implementation has not built yet is
unimplemented, and the diagnostic MUST say so and name the conformance
level it belongs to (§2.3), so a reader knows whether to change their
file or wait.

### 10.4 Line numbers in the underlying builder's output

An implementation that delegates to another builder (a `docker build` of
a rewritten file, as `pkg/guestdockerfile` does) MUST tell the
caller how far that builder's line numbers are from the author's, or
rewrite them. An error against line 31 of a file the author never saw is
a bug report about the wrong line.

## 11. Caching and reproducibility

### 11.1 Container stages

Cache behaviour in container stages is the underlying builder's, and
this specification adds nothing to it.

### 11.2 Converters and VM stages

An implementation SHOULD cache a converter's result, keyed on the input
image's digest and the converter's own options, and SHOULD cache each VM
stage instruction's result keyed on the previous state plus the
instruction and its inputs -- the same shape as a layer cache, over disk
states rather than layers.

A `RUN` in a VM stage boots a machine; it is the expensive instruction
in the language, and an implementation SHOULD say so when it runs one.

### 11.3 What is not reproducible

A `RUN` in a VM stage produces a filesystem image, and two runs of the
same command produce two byte-different disks (timestamps, journal, log
state). A Granule therefore MUST NOT be assumed to be
bit-reproducible at the disk level. Where reproducibility matters, prefer
`VM PACK` of a rootfs built by container instructions, which inherits
whatever reproducibility the container builder offers, and keep VM-stage
`RUN`s for what genuinely needs a booted machine.

### 11.4 Version reporting

An implementation MUST be able to report the specification version it
implements -- the string at the head of this document -- and its
conformance level (§2.3), and SHOULD record both on images it produces
(`dev.granule.granulefile.version`, `dev.granule.granulefile.level`).

A file cannot request a version. There is no `# granulefile=1.0`
directive and this specification does not reserve one: the compatibility
rule it relies on instead is that verbs, options and settings are closed
sets whose unknown members are errors (K1, §5, §5.5), so a file written
against a later version fails on an older implementation by naming the
construct it wanted rather than by building something else.

## 12. Conformance levels and implementation status

What exists in this tree today, against what this document defines.
Nothing below is a claim about a command that does not exist: the
right-hand column names the code, and "not implemented" means there is
none.

Two words are used precisely in the right-hand column. **Checked** means
`pkg/granulefile` parses the construct, knows what it means and
refuses it when it is wrong -- the front half of this document, with
nothing behind it. **Built** means something in this tree produces the
artifact. A row that is checked and not built is a file granule will tell
you the truth about and cannot build.

| Construct | Level | Today |
| --- | --- | --- |
| Lexing (§3.2), stages, stage headers and stage kinds (§4.1, §4.2) | 1-3 | `pkg/granulefile` (`Lex`, `Parse`). |
| Well-formedness rules K1-K11, per-kind instruction sets (§6, §7), every violation with file, line and rule (§10) | 1-3 | `pkg/granulefile.Validate`. Checked, not built. |
| Parser directive refusal | 2 | `pkg/granulefile.Directives` (K8), used by `pkg/guestdockerfile` as well. |
| Validate and report, building nothing (§4.6's `--check`) | 1-3 | `granule granulefile check` (`pkg/cli/granulefile.go`), which is the spelling this tree gives that row. `granule granulefile build -print` prints the build a file compiles to without running it. |
| Dockerfile pass-through | 1 | `granule granulefile build`, which hands a file with no `VM` in it to docker unchanged (§14.1). |
| Instruction subset validation, all violations, file:line | 2 | `pkg/guestdockerfile` (`Parse`, `Validate`), against the file `granule guest build` takes. It shares this package's lexer and its parser-directive refusals; its instruction set is its own and is deliberately not §7's (see below). |
| `VM PACK` semantics (pack a rootfs, lay granule's parts on, execute nothing) | 2 | `pkg/guestpack`, driven by `granule guest pack`, by `granule guest build -f`, and by a `VM PACK` line through `granule granulefile build` (`pkg/granulefile.PlanBuild`). |
| `VM PACK AS <name>` / `VM WRAP AS <name>` | 2 | Checked and built: the converted stage's name is the machine's, and the header's names the filesystem it was converted from. |
| `VM PACK FROM <ref>` / `VM WRAP FROM <ref>` stage headers | 2 | Checked and built. |
| `VM UNPACK FROM` (offline guest disk extraction into container rootfs) | 2 | Checked and built: the disk is read out with `debugfs` into a container stage, which `VM PACK` packs again (§14.5). |
| `VM SET` | 2 | Checked, recorded and honoured: `pkg/granulefile` validates the keys and values of §5.5's table, and a build writes each one into the produced image's config as the runtime variable `SettingEnv` names. Every command that starts a machine reads them back off the image and boots it with them unless the caller passed the matching flag, which is §5.5's override order: `granule vm create`, `granule vm run` and `granule guest verify/build` all resolve through `pkg/vmspec.ImageSettings` and `VMSpec.ResolveSettings`, so a machine is booted the same way whichever of them boots it. `memory-hotplug` and `snapshot` need no resolution -- granule passes neither to the VM container, so the image's own `ENV` stands (`snapshot` is refused at build time anyway, below). A `granule vm update` is the one exception on purpose: its flags are seeded from the spec the VM already runs with, so the image is not consulted again. |
| `VM WRAP` | 2 | Checked and built from a file, out of the same pieces `granule wrap` uses (`pkg/guestwrap`, `pkg/containerspec`). `--runtime=` is still validated as a name rather than against a set. |
| `VM FROM` | 3 | Checked and built: the guest filesystem is unpacked out of the referenced machine, the stage's offline instructions are applied to it, and it is packed onto this build's own VMM half -- which is the rebase `granule guest rebase` does, driven from a line. A `RUN` in such a stage is run in the guest that packing produces (below). Whether the reference is a bootable image rather than a rootfs (§5.1) is a registry question and is not checked here. |
| `RUN` in a VM stage (with `USER` and `SHELL` support) | 3 | Checked and built, for the stage being built: the generated build packs the machine under a tag of its own, `pkg/guestbuild` + `pkg/guestboot` boot it and run the stage's instructions in it one at a time (`guestbuild.Options.Steps`), and the running guest is committed as the result -- so a build of such a file needs `/dev/kvm`. `USER`, `SHELL`, `WORKDIR`, `ARG` and `ENV` are the build-time state deciding how each `RUN` runs, and none of them persists into the machine. An `ARG` there is the stage's own variable (§3.5): it is expanded into the instructions below it, it is in the environment those `RUN`s carry, a `--build-arg` overrides its default as it does a global's, and it goes no further than the stage that declared it. A boot in a stage that is *not* the one being built is refused as unimplemented (§10.3): what a boot produces is an image rather than a stage the rest of one Dockerfile can refer to. |
| `ENTRYPOINT`, `CMD` in VM stages and packed stages | 2/3 | Checked, and recorded by a `granule granulefile build` of a packed stage into the guest's workload configuration (`/etc/granule/container.json`), which is what the machine boots into (§14.9). `granule guest build` still refuses them (below). |
| `VM SNAPSHOT` / `VM SET snapshot=on` (snapshot VM at last instruction and resume on run) | 3 | Checked and built: the guest booted for the stage's `RUN`s is paused and snapshotted at the end of the stage into `/var/lib/granule/guest/snapshot/`, and the image records `dev.granule.guest.snapshotted`. A run of that image finds the bundle where the build put it and restores from it (`CHV_SNAPSHOT_PATH`, `pkg/config`), so the machine resumes rather than cold-boots. A stage with no `RUN` in it is booted anyway, which is what §5.6 asks for. |
| `VOLUME` (in-guest mount metadata / external mount or disk creation) | 2/3 | Checked, and carried into the produced image's `VOLUME` and `dev.granule.guest.volumes` from a packed stage and from a VM stage alike. No disk is created and nothing is mounted at boot. |
| Multi-stage, `COPY --from` across kinds | 3 | Checked and built. A copy out of a VM stage reads that stage's guest root filesystem, which is the container stage the pack was fed (§8). |
| Version and level reporting (§11.4) | 1-3 | `granulefile.Version`, and the level `granule granulefile check` was asked for, both printed by that command; a built machine records the version as `dev.granule.granulefile.version`. |
| `granule granulefile check` / `granule granulefile build` CLI | 1-3 | Spelled `granule granulefile check` and `granule granulefile build` (`pkg/cli/granulefile.go`, `pkg/granulefile.Build`), beside `granule guest build/pack/rebase/verify` (`pkg/cli/guest.go`). `test/granulefiles` builds every example in §14 with it. |

`pkg/guestdockerfile` is a caller of `pkg/granulefile` for the
lexing and the directives and not for the instruction set, and the
difference is a policy of that command rather than a disagreement about
the language: it accepts a `SHELL`, records an `EXPOSE`, and decides for
itself what a `FROM` in its file means, because it supplies a pack of its
own after the file rather than reading one out of it. It is also stricter
than §7 in three places -- `MAINTAINER`, a remote `ADD`, and `ENTRYPOINT`
and `CMD`, which this document now records into a packed machine's
workload configuration and which nothing in this tree records yet. Where
the two agree, the reasons that command gives are this document's.

The implementation order this suggests is the one
`docs/guest-images.md` already argues for, with validation ahead of all
of it: checking needs no KVM, no docker and no network and executes
nothing, so it can be finished while the rest is still a plan. Then the
packing and unpacking path, since it needs no KVM either; then VM
stages, which need a booted guest.

That last part is where this tree now is, and the shape it took is worth
recording: a stage is booted once and its instructions are run in that
one guest, rather than a boot per instruction with a cache to make it
bearable (§11.2). A boot is nearly a minute, so a stage of five `RUN`s
that booted five times would spend five of them; running them in one
guest costs a stage the boot once and gives each instruction the same
machine the last one left behind, which is what a reader of the file
expects anyway. What it does not give is §11.2's per-instruction cache:
nothing here resumes a stage part-way through, so editing the last `RUN`
of a stage re-runs all of them.

## 13. Changes from the language sketch

`docs/language_sketch.md` opened these questions without settling them.
Each was settled here, and this is the list of what a reader of the
sketch will find different.

1. **`VM` takes a verb, and the verbs are closed.** `FROM`, `WRAP`,
   `PACK`, `UNPACK`, `SET`, and `SNAPSHOT`. The sketch used the first three without saying
   whether the set was open; an unknown verb is now an error (K1), which
   is what makes adding a verb later a compatible change rather than a
   silent reinterpretation of somebody's file.
2. **`VM WRAP my_service`'s argument is defined**: it is the service
   name the wrapped workload runs under, defaulting to the stage name.
   The sketch showed the argument once, with no statement of what it
   meant; reading it as an image or a command were both live readings,
   and both are now wrong.
3. **Converters are one-way and once per stage** (K2, K3). The sketch
   showed a converter mid-stage but did not say whether a second one, or
   a converter in a `VM FROM` stage, meant anything.
4. **Converters support `AS name` and `FROM <ref>`** (`VM PACK FROM <ref> AS name`,
   `VM WRAP FROM <ref> AS name`, `VM PACK AS name`, `VM WRAP AS name`).
   This allows multi-stage builds to address pre-conversion container
   stages and converted VM stages independently in `COPY --from`, or to
   start a VM stage by packing/wrapping an existing stage or image directly.
5. **`VM UNPACK FROM` is added as a stage header.** It extracts a VM image's
   guest root filesystem into a container stage, allowing offline file
   injection and manipulation without KVM before repacking with `VM PACK`.
6. **Stage kind decides instruction meaning, and is stated as a rule**
   rather than as a comment in an example. The sketch's `# Running
   inside a container` / `# Running inside the VM!` comments are §4.2 and
   §6.
7. **What a stage above a converter may use depends on which converter
   ends it** (§7). This is new: it is what lets `VM WRAP` be appended to
   an arbitrary Dockerfile -- entrypoint and all -- while `VM PACK`
   still refuses the container-runtime instructions that a disk cannot
   honour.
8. **`USER` matches Docker build defaults and behavior**: `RUN` executes as
   root by default, and `USER` changes the active user for subsequent `RUN`s
   in both container and VM stages.
9. **`SHELL` is supported in VM stages** to configure the interpreter used for
   shell-form `RUN` steps inside the guest.
10. **`VOLUME` is specced for VM mount point declarations**, enabling external
    volume mounting (virtio-fs / virtio-blk) or auxiliary disk image creation.
11. **`VM SET` is added.** The sketch gave no way to say a machine wants
    4 GiB, which is the first question every author asks. It maps to the
    runtime variables that already exist rather than inventing a second
    configuration vocabulary.
12. **Options are `--flag=value`, before positionals**, matching
    `COPY --from=`. The sketch had no options.
13. **`VM FROM` requires a bootable granule image**, and a rootfs image
    handed to it is an error naming `VM PACK`. The sketch's `VM FROM
    granule/alpine` is ambiguous about which of the two it meant.
14. **`VM FROM` takes the current VMM half**, rather than inheriting the
    referenced image's. The sketch's comment ("replace the hypervisor
    side with our validated code") asked for exactly this; it is now the
    defined behaviour rather than an aside, and it is the rebase
    `docs/guest-images.md` already specifies.
15. **`ENV` in a VM stage is build-time only** (§6). Neither reading is
    obviously right, and an author who assumes the other one gets a
    machine whose services do not see the variable, so the specification
    picks one and says what to write instead.
16. **The examples are corrected.** The sketch's `RUN apt-get install
    redis` on `alpine:latest` (Alpine has `apk`), and its `RUN systemd
    add my_service` (not a command), are replaced in §14 by lines that
    would actually run. The sketch was prose about shape; a
    specification's examples have to be executable to be worth reading.
17. **File name and resolution are defined** (§3.1): `Granule`, then
    `Dockerfile`. The sketch named the format after a file nobody had
    said how to find.
18. **Parser directives are refused in a file that uses `VM`** (§3.3).
    Not mentioned in the sketch, and the rewriting it implies makes them
    inert rather than merely unusual.
19. **`VM SNAPSHOT` and snapshot attributes are specified** (§5.6). The sketch
    did not define build-time VM snapshotting; Granules can now specify
    `VM SNAPSHOT` or `VM SET snapshot=on` in a VM stage to snapshot the
    running VM on the last instruction and save it into the image, enabling
    instant resume at runtime instead of cold boot.
20. **`ENTRYPOINT` and `CMD` are supported natively in VM stages and packed stages** (§6, §7).
    While VMs often manage background services with systemd or OpenRC,
    `ENTRYPOINT` and `CMD` provide a straightforward, native way to define the
    primary workload command executed by the guest VM at boot without requiring
    systemd service files.

Nothing in the sketch's shape was rejected. Every example in it has a
direct translation here, given in §14.

## 14. Worked examples

### 14.1 A Dockerfile is a Granule

```dockerfile
FROM alpine:latest

# This runs in a container and I get a container.
RUN apk add --no-cache redis
```

Built by granule, this produces the container image docker would produce.
No `VM` instruction, no machine (§1.2).

### 14.2 Wrapping it in a machine

```dockerfile
FROM alpine:latest

RUN apk add --no-cache redis
CMD ["redis-server", "--protected-mode", "no"]

# Now it runs inside a VM.
VM WRAP redis AS redis_vm
```

One line added to the end, nothing above it touched. The result is a
granule VM image whose guest runs the container above as
`granule-wrap@redis`, restarting always, leaving the machine up if it
exits (§5.2). `CMD` is legal here because the stage above the converter
is a container stage ending in `VM WRAP` (§7). `AS redis_vm` names the
resulting VM stage.

### 14.3 Packing a rootfs into a machine

```dockerfile
FROM ghcr.io/gke-labs/granule/debian12.rootfs:latest

# Copy a file into the rootfs (in a container, offline).
COPY greeting.txt /greeting.txt
RUN apt-get update && apt-get install -y nginx && systemctl enable nginx
VOLUME /var/log/nginx

# Now the VM boots with that rootfs as its root filesystem.
VM PACK AS web_vm
```

Also one line at the end, and also no KVM: everything here is a
container build plus a pack (§5.3). The base must be a guest rootfs, and
the packing lays granule's agents and units over it. `VOLUME` declares an
in-guest mount point for nginx logs.

### 14.4 Deriving a machine from a machine

```dockerfile
# Inherit the machine; the hypervisor half is this build's own.
VM FROM ghcr.io/example/github-runner-vm:latest

# This runs inside the VM, and I get a VM image.
COPY runner.service /etc/systemd/system/runner.service
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN systemctl enable runner.service
```

The `COPY` is applied offline; the `SHELL` configures bash for shell commands;
the `RUN` boots the guest and executes in it, so this file needs `/dev/kvm`
(level 3, §2.3).

### 14.5 Updating a VM image offline without KVM

```dockerfile
# Unpack the guest rootfs of an existing VM image into a container stage.
VM UNPACK FROM ghcr.io/example/my-vm:latest AS unpacked

# Offline container-level copy into the rootfs -- no /dev/kvm needed.
COPY updated-config.yaml /etc/service/config.yaml

# Repack the modified rootfs into a bootable VM image.
VM PACK AS updated_vm
```

`VM UNPACK FROM` converts a VM image's disk back into a container stage
filesystem. Modifications are applied offline as container instructions, and
`VM PACK` repacks the disk without requiring a booted VM or KVM.

### 14.6 Packing directly from an earlier stage

```dockerfile
FROM ghcr.io/gke-labs/granule/debian12.rootfs:latest AS rootfs_builder
RUN apt-get update && apt-get install -y redis-server

# Start a VM stage directly by packing the builder stage.
VM PACK FROM rootfs_builder AS redis_vm
USER redis
RUN redis-server --test-config
USER root
RUN systemctl enable redis-server
VOLUME /var/lib/redis
VM SET memory-mb=2048 cpus=2
```

`VM PACK FROM rootfs_builder AS redis_vm` starts a VM stage directly by
packing the rootfs produced by `rootfs_builder`.

### 14.7 Composing machines

```dockerfile
# A VM build stage, used only as a source of files.
VM FROM ghcr.io/example/github-runner-vm:latest AS github_vm

# The final machine.
VM FROM ghcr.io/gke-labs/granule-alpine-vm:latest

# Copy out of the other machine's root filesystem.
COPY --from=github_vm /etc/runner/config.json /etc/runner/config.json
VM SET memory-mb=4096
```

Neither stage runs anything, so neither needs KVM: the copy reads the
first stage's guest root filesystem directly (§8).

### 14.8 Snapshotting a warmed VM for instant resume

```dockerfile
VM FROM ghcr.io/example/python-ml-vm:latest AS warmed_vm

# Perform heavy warm-up inside the guest (e.g. load model weights into RAM).
RUN python3 -c "import model; model.preload_weights()"

# Snapshot the running VM state so subsequent runs resume instantly.
VM SNAPSHOT
VM SET memory-mb=4096
```

The VM executes the warm-up `RUN` step in the live guest. Upon completion of
the stage, the builder pauses and snapshots the running VM memory and device
state into the image. When this image is run via `docker run` or `granule run`,
it restores the snapshot and immediately resumes execution with the model
weights already in memory, avoiding cold-boot and warm-up latency.

### 14.9 Setting a native entrypoint and command in a VM stage

```dockerfile
# Inherit a base guest rootfs and define the native boot workload.
FROM ghcr.io/gke-labs/granule/debian12.rootfs:latest

RUN apt-get update && apt-get install -y python3
COPY app.py /app/app.py
WORKDIR /app

ENTRYPOINT ["python3", "app.py"]
CMD ["--port", "8080"]

# Pack into a VM image. The entrypoint and command execute natively at boot.
VM PACK AS app_vm
```

`ENTRYPOINT` and `CMD` are carried into the VM image so the guest boots
directly into the specified workload without requiring custom systemd unit
files.

## 15. Deliberately out of scope

- **A Granule that fixes resource limits.** `VM SET` records
  defaults, and the runtime overrides them. An image that could refuse
  to boot with more memory than its author imagined would be a new
  policy mechanism, and the one place such a policy belongs is the
  cluster that schedules the machine.
- **Arbitrary multi-point snapshot trees during build.** A stage may
  snapshot its final state (`VM SNAPSHOT`), but the language does not
  specify branchable checkpoint/restore trees mid-stage.
- **Multi-architecture VM stages.** A guest disk is built for one
  architecture and boots under a hypervisor of that architecture. A
  `--platform` on a VM stage would have to mean cross-booting, which
  needs emulation this project does not carry.
- **A layer chain of guest disks.** Rejected on its own merits in
  `docs/guest-images.md`: disk chaining adds overhead and fragility. The packed path is both the cheap one and the safe
  one, and the language should not offer a syntax for the other.
- **Running a Granule's build inside a VM.** The isolation argument
  in `docs/guest-images.md` applies to where a build runs, not to what
  it is written in.

## Appendix A: collected grammar

```ebnf
granulefile     = { global-arg | comment } stage { stage } ;
global-arg     = "ARG" WS name [ "=" value ] ;

stage          = stage-header { instruction | comment } ;
stage-header   = "FROM" WS ref [ WS "AS" WS name ]
               | "VM" WS "FROM" WS ref [ WS "AS" WS name ]
               | "VM" WS "UNPACK" WS "FROM" WS ref [ WS "AS" WS name ]
               | "VM" WS "PACK" WS "FROM" WS ref { WS pack-option } [ WS "AS" WS name ]
               | "VM" WS "WRAP" WS "FROM" WS ref { WS wrap-option } [ WS name ] [ WS "AS" WS name ] ;

instruction    = dockerfile-instruction | vm-instruction ;

(* VM FROM, VM UNPACK FROM, VM PACK FROM, and VM WRAP FROM are stage headers:
   they start a stage, so they cannot appear inside one (§5.1, §5.4, §6). *)
vm-instruction = "VM" WS "WRAP" { WS wrap-option } [ WS name ] [ WS "AS" WS name ]
               | "VM" WS "PACK" { WS pack-option } [ WS "AS" WS name ]
               | "VM" WS "SET" WS setting { WS setting }
               | "VM" WS "SNAPSHOT" ;

wrap-option    = "--name=" name
               | "--restart=" ( "always" | "on-failure" | "no" )
               | "--exit-action=" ( "ignore" | "poweroff" )
               | "--runtime=" name ;

pack-option    = "--variant=" ( "debian" | "alpine" | "auto" )
               | "--disk-extra-mb=" integer ;

setting        = key "=" value ;
name           = letter { letter | digit | "_" | "." | "-" } ;
```

`dockerfile-instruction` is Dockerfile's own instruction grammar, with
the restrictions in §6 and §7 applied according to the stage's kind.

## Appendix B: reserved names and paths

An implementation owns these and an author MUST NOT expect to control
them:

| | |
| --- | --- |
| `/var/lib/granule/guest/` | the guest half of a VM image (§9.1) |
| `/var/lib/granule/guest/snapshot/` | the VM snapshot state for resume (§5.6, §9.1) |
| `/var/lib/granule/overlay/` | per-VM writes, created at boot |
| `/var/lib/granule/wrap/` | wrapped workloads (§9.2) |
| `/usr/local/bin/granule`, `/usr/local/bin/granule-agent`, `/usr/local/bin/granule-mem-agent` | the runtime and the guest agents |
| `granule-*` units and services | granule's own, in any guest variant |
| `dev.granule.*` labels | granule's own image metadata (§9.3) |
| `CHV_*`, `GRANULE_*` environment | the runtime's own configuration |

A `COPY` or `RUN` that writes into one of these paths in a VM stage
SHOULD be diagnosed, and MUST NOT be relied on: a pack or a rebase lays
granule's own copies down over them afterwards, which is the property
that lets an image built months ago boot with today's agent in it.
