# The Konturfile specification

**Version:** `konturfile/1.0-draft`. This is the string §11.4 requires an
implementation to report, and it changes when this document changes in a
way a file can notice.

**Status:** proposed. This document defines the language; it is not a
claim that all of it is implemented. Section 12 states, construct by
construct, what exists in this tree today and what does not.

**Supersedes:** `docs/language_sketch.md`, which this replaces. Section
13 lists every place where this specification decides something the
sketch left open, or changes something the sketch showed, and says why.

A **Konturfile** describes how to build an OCI image, a kontur VM image,
or a set of intermediate stages that produce one. It is a strict
superset of the Dockerfile format: a Dockerfile *is* a Konturfile, and
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

kontur builds machines, and the format people already have describes
containers. The two are close enough that reusing the format is worth
more than a language of kontur's own: the editors, the linters, the
`--build-arg` habits, the layer cache and the registry all carry over,
and what is genuinely different about a VM is small enough to say in one
instruction.

So the rule the whole language follows is: **the vocabulary is docker's
wherever docker has a word for the thing, and what kontur adds is one
instruction that changes what the words mean from that point on.**
`RUN` is `RUN` in both kinds of stage -- it runs a command in the thing
being built. What changes is whether that thing is a container or a
machine.

### 1.2 The three shapes of a build

A Konturfile is one of three things, and which one it is follows from
its contents rather than from a flag:

| The file | What is built |
| --- | --- |
| No `VM` instruction anywhere | A container image. The file is a Dockerfile and is built as one. |
| A `VM WRAP` or `VM PACK` in the final stage, or a final `VM FROM` stage | A **kontur VM image**: a bootable OCI image carrying the VMM half and a guest half, which `docker run` boots (see the top-level `README.md`). |
| `VM` instructions in a stage that is not the target | Whatever the target stage is. Non-target stages exist to be copied from. |

A kontur VM image is still an OCI image. It is pushed, pulled, scanned
and run like any other; the VM is what happens when it starts.

### 1.3 The two kinds of stage

Every stage has a **kind**, either *container* or *VM*, and the kind
decides where the stage's instructions execute:

- In a **container stage**, instructions execute the way they do in a
  Dockerfile: `RUN` runs in a container, `COPY` writes into the
  container filesystem, and the stage's result is a filesystem plus OCI
  config.
- In a **VM stage**, `RUN` executes *inside a booted guest*, `COPY`
  writes into the guest's root filesystem, and the stage's result is a
  guest disk plus the VMM half that boots it.

A stage's kind can change exactly once, at a **converter** (`VM WRAP` or
`VM PACK`): the instructions above the converter are a container build,
and the instructions below it run in the machine that container build
became. That is the mechanism behind the sketch's one-liner -- adding
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

Terminals in capitals (`FROM`, `WRAP`) are keywords and are matched
case-insensitively, as Dockerfile matches its own instruction names.
Uppercase is the convention throughout this document and SHOULD be
used.

### 2.3 Conformance levels

An implementation MAY implement the language in three levels, and MUST
report which it implements (§11.4). Each level includes the ones before
it:

- **Level 1 -- Dockerfile.** Files with no `VM` instruction. A level 1
  implementation is a Dockerfile builder.
- **Level 2 -- converters.** `VM PACK`, `VM WRAP` and `VM SET`, in
  single-stage or multi-stage container builds. No instruction executes
  inside a VM at this level, so no level 2 build needs `/dev/kvm`.
- **Level 3 -- VM stages.** `VM FROM`, `RUN` below a converter, and
  cross-stage copies out of a VM stage. `RUN` in a VM stage needs
  `/dev/kvm` on the machine running the build.

An implementation MUST refuse a construct from a level it does not
implement, naming the level (§10.3). It MUST NOT ignore it.

## 3. Lexical structure

### 3.1 Files

A Konturfile is a UTF-8 text file. Lines are terminated by LF; a CR
immediately before an LF is ignored, so CRLF files parse identically.

The default file name is `Konturfile`. An implementation resolving a
file by default MUST look for `Konturfile` in the build context root,
then `Dockerfile`, and MUST use the first that exists. A file named on
the command line (`-f`) is used whatever it is called, including `-` for
standard input.

There is no requirement that a file called `Dockerfile` avoid `VM`
instructions, or that one called `Konturfile` use them. The name selects
nothing; §1.2 does.

### 3.2 Lines, comments and continuations

Lexing is Dockerfile's, and this document does not restate it. In
summary, and normatively where a Konturfile-specific rule depends on it:

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

`internal/guestdockerfile.Parse` implements exactly this much and no
more, deliberately: what an instruction *means* is the container
builder's business, and what is allowed to *appear* is kontur's.

### 3.3 Parser directives

A parser directive is a comment of the form `# syntax=...`, `# escape=...`
or `# check=...` in the leading comment block of the file.

- In a file with no `VM` instruction, directives mean what they mean to
  docker and MUST be passed through unchanged.
- In a file that uses `VM`, all three MUST be refused, with the reason
  named (§10).

The reason for the second rule is mechanical rather than a matter of
taste. A Konturfile that uses `VM` is rewritten -- the implementation
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

A build is a sequence of stages. A stage begins at a `FROM` or a
`VM FROM` instruction and ends at the next one, or at the end of the
file.

```ebnf
konturfile = { global-arg } stage { stage } ;
stage      = stage-header { instruction } ;
stage-header = "FROM" ref [ "AS" name ]
             | "VM" "FROM" ref [ "AS" name ] ;
```

Stage names (`AS name`) are case-insensitive, MUST be unique within a
file, MUST NOT be a number, and MUST match `[A-Za-z][A-Za-z0-9_.-]*`.
A stage MAY also be referred to by its zero-based index.

Instructions before the first stage header MUST be `ARG` (the global
`ARG`s) or comments. Anything else there MUST be refused.

### 4.2 Stage kinds

A stage's kind at any point in the file is determined by two things: how
it started, and whether a converter has been seen in it.

| Stage header | Kind at the header | After `VM WRAP`/`VM PACK` |
| --- | --- | --- |
| `FROM ref` | container | VM |
| `VM FROM ref` | VM | — (a converter is refused, rule K3) |

A container stage that is never converted stays a container stage for
the whole build, and produces a container image.

### 4.3 Converters

`VM WRAP` and `VM PACK` are converters. A converter takes the container
image the stage has built up to that point and produces a VM image from
it, and from that line on the stage is a VM stage: what `RUN` means,
what `COPY` writes into and what the stage produces all change.

The two differ in what they do with the container image, and that is the
only difference:

- **`VM PACK`** treats the stage's filesystem as the machine's root
  filesystem. The result is a machine whose `/` is what the container
  stage built.
- **`VM WRAP`** treats the stage's whole image -- filesystem *and*
  entrypoint -- as a workload to run *inside* a machine. The result is a
  machine with kontur's own guest root filesystem, carrying a container
  runtime and the wrapped image, which it runs at boot.

"Is the thing I built the machine, or is it something that runs on one?"
is the entire question a Konturfile author has to answer here, and each
answer has a verb.

### 4.4 The target stage and the result

The **target stage** is the last stage in the file, or the stage named
by the implementation's `--target` option. The build's result is the
target stage's result:

| Target stage kind | Result |
| --- | --- |
| container | An OCI container image. |
| VM | A kontur VM image (§9.1). |

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
  of `FROM`, `WRAP`, `PACK`, `SET`.
- **K2.** A stage MUST contain at most one converter (`VM WRAP` or
  `VM PACK`).
- **K3.** A converter MUST NOT appear in a stage that started with
  `VM FROM`: the stage is already a machine, and there is nothing to
  convert.
- **K4.** A converter MUST NOT appear before the first stage header.
- **K5.** `VM SET` MUST appear in a VM stage, or in a container stage
  that is converted later in that same stage. It MUST NOT appear in a
  stage that never becomes a VM stage, because nothing would read it.
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
               | "VM" "WRAP" { wrap-option } [ service-name ]
               | "VM" "PACK" { pack-option }
               | "VM" "SET" setting { setting } ;
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

Starts a VM stage from an existing kontur VM image. `ref` is an ordinary
image reference, resolved and pulled the way `FROM`'s is.

- The referenced image MUST be a kontur VM image: it MUST carry a guest
  half at the paths in §9.1. An implementation MUST check this before
  running any instruction in the stage.
- A reference to a *guest rootfs* image (one that is a filesystem rather
  than a bootable image) MUST be refused with a diagnostic naming `VM
  PACK`, since `FROM <ref>` followed by `VM PACK` is what the author
  meant. This mirrors what `konturctl guest build` already says when it
  is handed one.
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
"VM" "WRAP" { wrap-option } [ service-name ]
wrap-option  = "--name=" name
             | "--restart=" ( "always" | "on-failure" | "no" )
             | "--exit-action=" ( "ignore" | "poweroff" )
             | "--runtime=" runtime-name
service-name = name ;
```

Converts the current container stage into a VM stage whose guest runs
that container image as a workload.

The guest root filesystem is kontur's own (the same rootfs a `VM PACK`
of `ghcr.io/gke-labs/kontur-guest-rootfs` would produce), plus a
container runtime and the wrapped image. The wrapped image is embedded
in the guest as an OCI layout (§9.2) and run at boot by a generated
service unit.

- The workload's command is the wrapped image's own `ENTRYPOINT`/`CMD`.
  An image with neither MUST be refused: there would be nothing to run.
- `ENV`, `WORKDIR`, `USER` and `EXPOSE` recorded in the wrapped image
  apply to the workload as they would under any container runtime.
  `EXPOSE` is documentation there as it is anywhere; ports reach the
  guest through the container's own network (see the top-level
  `README.md`).
- The **service name** is the positional argument, or `--name=`, or, if
  neither is given, the stage's name, or `app` for an unnamed stage. It
  MUST match `[a-z0-9][a-z0-9_.-]*`. It names the generated unit and the
  layout directory (§9.2), and is what `kontur exec` into the guest will
  see running.
- `--restart=` sets the unit's restart policy and defaults to `always`.
- `--exit-action=` decides what a workload exit does to the machine:
  `ignore` (default) leaves the guest running -- an operator can still
  `kontur exec` into it, which is the whole reason a failed workload
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
"VM" "PACK" { pack-option }
pack-option = "--variant=" ( "debian" | "alpine" | "auto" )
            | "--disk-extra-mb=" integer ;
```

Converts the current container stage into a VM stage whose guest root
filesystem is that stage's filesystem, by packing it into a bootable
disk.

- The stage's filesystem MUST be a **guest rootfs**: it MUST carry an
  init (`/sbin/init`) the packed disk can boot. An implementation MUST
  check this during the pack and MUST refuse a filesystem that has none,
  naming what is missing. Building `FROM
  ghcr.io/gke-labs/kontur-guest-rootfs` is the supported way to have one
  (see `deploy/guest-image/README.md`); an arbitrary container base is
  not a machine.
- kontur's own guest-side pieces -- `kontur-agent`, `kontur-mem-agent`,
  the control-net overlay, the units -- are laid over the filesystem
  *during* the pack, out of the implementation's own current build, not
  out of the author's stage. An author neither has to install them nor
  can pin an old copy of them. This is `internal/guestpack`'s existing
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

### 5.4 `VM SET`

```ebnf
"VM" "SET" setting { setting }
setting = key "=" value ;
```

Records a default for the machine the image boots. Settings are the
image's defaults, not fixed limits: the runtime environment overrides
them the way a `docker run -e` overrides an `ENV`, and this
specification does not give a Konturfile a way to prevent that.

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
| `shutdown-timeout` | graceful shutdown budget | `CHV_SHUTDOWN_TIMEOUT` |

An implementation MUST refuse an unknown key, and MUST validate a
value's syntax at build time rather than leaving it to fail at boot. It
MUST record settings as the corresponding runtime variables in the
produced image's OCI config, so that nothing has to read a Konturfile to
boot the result.

A later `VM SET` of the same key in the same stage overrides an earlier
one. A stage started by `VM FROM` inherits the settings recorded in the
image it came from, and its own `VM SET`s override those.

## 6. Instructions inside a VM stage

A VM stage is a machine under construction. Instructions in it have the
meanings below, from the converter (or the `VM FROM` header) to the end
of the stage.

| Instruction | Meaning in a VM stage |
| --- | --- |
| `RUN` | Execute the command **inside the guest**, as root, with the machine booted. Needs `/dev/kvm` (level 3). A non-zero exit fails the build. |
| `COPY` | Write into the guest root filesystem. Applied offline where the implementation can; no boot is required by this instruction. |
| `ADD` | As `COPY`, with tar extraction. Remote sources are refused (below). |
| `ENV` | Set variables for subsequent `RUN`s in this stage, and for expansion. **Build-time only**: they do not persist into the booted machine. To give a machine environment, write it (`/etc/environment`, a unit's `Environment=`). |
| `WORKDIR` | Set the working directory for subsequent `RUN`s and the default target of relative `COPY` destinations. Creates it if absent. |
| `USER` | Set the user for subsequent `RUN`s. An implementation MAY refuse a user the guest filesystem does not define. |
| `ARG` | As in a container stage: a build-time variable, in scope for the rest of the stage. |
| `LABEL` | Recorded on the produced image's OCI config, as it would be on a container image. |
| `VM SET` | §5.4. |

Everything else MUST be refused. The refusals, and the reason each one
is a refusal rather than a no-op, are the ones
`internal/guestdockerfile` already gives, because they are properties of
what a machine is rather than of the implementation:

| Refused | Because |
| --- | --- |
| `ENTRYPOINT`, `CMD` | A guest boots an init and runs services: there is no single process whose exit is the machine's exit. What you mean is a service -- `COPY` a unit in and enable it. (In a `VM WRAP` stage the *wrapped* image's entrypoint is meaningful; it is set above the converter, where the stage is still a container.) |
| `EXPOSE` | Port metadata belongs to the container that publishes the ports, which is the kontur image around this guest, not the guest. |
| `VOLUME` | A guest's filesystem is a packed disk the VM mounts as its root, not a container filesystem whose mount points a runtime can substitute. Nothing would act on the declaration at boot. |
| `HEALTHCHECK` | Docker health checks probe a container's main process, and this guest is not run as one. Health is a unit's business inside the machine, or `konturctl vm status`/`vm wait` outside it. |
| `STOPSIGNAL` | A guest is stopped by an ACPI power button press its init turns into a shutdown. There is no process here to signal. |
| `SHELL` | `SHELL` changes the interpreter for `RUN` and for `ENTRYPOINT`/`CMD`, and the latter have no meaning for a machine. A step needing another interpreter should name it in the command it runs. |
| `ONBUILD` | Defers instructions to whoever builds `FROM` the result, where none of them is validated against these rules. |
| `MAINTAINER` | Long deprecated. `LABEL org.opencontainers.image.authors=` is the supported spelling. |
| `FROM`, `VM FROM` mid-stage | These start a stage; they cannot appear inside one. |
| `VM WRAP`, `VM PACK` | Rule K3. |
| `ADD` of a remote source | It fetches at build time from a host outside the build context, with the network of whatever machine ran the build and nothing recording what came back. `COPY` it out of the context, or fetch it in a `RUN` where the checksum is yours to check. |

An implementation MUST refuse these rather than dropping them. A format
that silently discards half of what it was handed is worse than one that
never claimed the resemblance: a dropped `CMD` is a machine that comes
up without the thing the file said to run, discovered in production
rather than at build time.

## 7. Instructions in a packed container stage

Above a converter, a stage is an ordinary container build, and how much
of Dockerfile it may use depends on which converter ends it:

- **Above `VM WRAP`**, the full Dockerfile instruction set is available.
  The stage really is building a container image, and an `ENTRYPOINT`,
  a `CMD`, a `USER`, an `EXPOSE` or a `HEALTHCHECK` in it means what it
  means to docker -- the workload's, not the machine's. This is what
  makes "take this Dockerfile and add one line" work without editing
  anything above the line.
- **Above `VM PACK`**, the stage is building a root filesystem, so the
  instructions in §6's refusal table that describe a *container's*
  runtime -- `ENTRYPOINT`, `CMD`, `EXPOSE`, `VOLUME`, `HEALTHCHECK`,
  `STOPSIGNAL`, `ONBUILD` -- MUST be refused with the same reasons. They
  would be recorded in an image config that nothing reads, since what is
  built from this stage is a disk.

An implementation MUST make this determination from the converter that
ends the stage, which means it MUST parse the whole stage before
validating any of it. Refusing an `ENTRYPOINT` in a stage whose last
line turns out to be `VM WRAP` would be wrong; accepting one in a stage
that ends in `VM PACK` would be worse.

`ARG`, `ENV`, `LABEL`, `WORKDIR`, `USER`, `RUN`, `COPY` and `ADD` are
available above either converter.

## 8. Cross-stage copies

`COPY --from=<stage>` and `ADD --from=<stage>` work across every
combination of stage kinds. `<stage>` is a stage name, a stage index, or
an external image reference.

| Source kind | What `--from` reads |
| --- | --- |
| container stage | That stage's filesystem, as in a Dockerfile. |
| VM stage | That stage's **guest root filesystem**, at its state at the end of the stage -- including anything a `RUN` in it wrote. |
| external image | That image's filesystem. If it is a kontur VM image, its guest root filesystem is *not* what is read; its container filesystem is, which is where the guest disk lives as a file. Copy a path out of a machine by making it a `VM FROM` stage. |

The destination follows the destination stage's kind: into a container
stage it is the container filesystem, into a VM stage it is the guest
root filesystem.

A copy out of a VM stage MUST NOT require booting that stage's guest. An
implementation MAY read the packed disk directly; if the stage's own
instructions required a boot, the copy reads what that boot left behind.

## 9. Artifacts

### 9.1 A kontur VM image

The result of a VM target stage is an OCI image carrying both halves:
the VMM half (the kontur runtime, cloud-hypervisor, netshim, the
entrypoint) and the guest half, at the fixed paths
`internal/guestimage` defines:

```text
/var/lib/kontur/guest/disk.img      the packed guest root disk (raw)
/var/lib/kontur/guest/vmlinuz       the guest's own kernel, when it has one
/var/lib/kontur/guest/initrd.img    the initramfs matching it
/var/lib/kontur/guest/vmlinux       the VMM half's kernel -- not the guest's
/var/lib/kontur/overlay/disk.qcow2  per-VM writes, created at boot
```

Those paths are absolute and resolved inside the VM container, and their
suffixes are functional -- `hypervisor.BuildArgs` decides `image_type`
and `backing_files` from them. An implementation MUST use them. See
`docs/guest-images.md` for why both properties are load-bearing.

The image's entrypoint is the VMM half's, so `docker run` on the result
boots the machine with no arguments, and `kontur ready`, `kontur exec`
and `kontur cp` work against it as they do against any kontur image.

### 9.2 A wrapped workload

`VM WRAP` MUST place the wrapped image in the guest filesystem as an OCI
layout under `/var/lib/kontur/wrap/<service-name>/`, and MUST generate a
service that runs it at boot under the chosen runtime, named
`kontur-wrap@<service-name>` (a systemd unit on the debian variant, an
OpenRC service on the alpine one).

Nothing in the wrapped image is executed during the build.

### 9.3 Labels

An implementation MUST record, on a VM image it produces:

| Label | Value |
| --- | --- |
| `dev.kontur.guest.packed-from` | the rootfs image the guest disk was packed from, where there is one |
| `dev.kontur.konturfile.stage` | the target stage's name, or its index |
| `dev.kontur.konturfile.kind` | `pack`, `wrap` or `from` |
| `dev.kontur.wrap.image` | on a `VM WRAP` result, the wrapped image's digest |
| `dev.kontur.konturfile.version` | the specification version the build implements (§11.4) |

`dev.kontur.guest.packed-from` is the label `konturctl guest rebase`
already reads to say what a cheap rebase did not refresh; the others
exist so the same question -- "what was this built from, and how?" --
can be answered about a Konturfile build without the Konturfile.

## 10. Diagnostics

### 10.1 Fail closed, and report everything

Two rules, and they are the ones the format's credibility rests on:

1. **An implementation MUST refuse what it does not implement.** Never
   ignore, never warn-and-continue. §6's refusal list is refusals for
   this reason, and so is §5's unknown-option rule.
2. **An implementation MUST report every violation in a file, not the
   first.** An author porting a container Dockerfile typically trips
   several at once -- an `ENTRYPOINT`, a `CMD` and an `EXPOSE` are one
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
Konturfile: 2 instructions kontur cannot build a guest from:
  Konturfile:14: CMD: a guest boots an init and runs services: ...
  Konturfile:16: EXPOSE: port metadata belongs to the container ...

What a kontur VM stage may use: RUN, COPY, ADD, ENV, WORKDIR, USER,
ARG, LABEL, VM SET. See docs/konturfile.md.
```

A reason MUST be a sentence or two saying what kontur does instead, or
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
a rewritten file, as `internal/guestdockerfile` does) MUST tell the
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
state). A Konturfile therefore MUST NOT be assumed to be
bit-reproducible at the disk level. Where reproducibility matters, prefer
`VM PACK` of a rootfs built by container instructions, which inherits
whatever reproducibility the container builder offers, and keep VM-stage
`RUN`s for what genuinely needs a booted machine.

### 11.4 Version reporting

An implementation MUST be able to report the specification version it
implements -- the string at the head of this document -- and its
conformance level (§2.3), and SHOULD record both on images it produces
(`dev.kontur.konturfile.version`, `dev.kontur.konturfile.level`).

A file cannot request a version. There is no `# konturfile=1.0`
directive and this specification does not reserve one: the compatibility
rule it relies on instead is that verbs, options and settings are closed
sets whose unknown members are errors (K1, §5, §5.4), so a file written
against a later version fails on an older implementation by naming the
construct it wanted rather than by building something else.

## 12. Conformance levels and implementation status

What exists in this tree today, against what this document defines.
Nothing below is a claim about a command that does not exist: the
right-hand column names the code, and "not implemented" means there is
none.

| Construct | Level | Today |
| --- | --- | --- |
| Dockerfile pass-through | 1 | Not implemented as `kontur build`; docker builds these files unchanged today. |
| Instruction subset validation, all violations, file:line | 2 | `internal/guestdockerfile` (`Parse`, `Validate`), against a single-stage file. |
| Parser directive refusal | 2 | `internal/guestdockerfile.directives`. |
| `VM PACK` semantics (pack a rootfs, lay kontur's parts on, execute nothing) | 2 | `internal/guestpack`, driven by `konturctl guest pack`; reached from a Dockerfile by `konturctl guest build -f`. |
| `VM SET` | 2 | Not implemented. The runtime variables in §5.4's table all exist (`internal/config`); nothing writes them from a build file. |
| `VM WRAP` | 2 | Not implemented. No guest variant carries an in-guest container runtime today. |
| `VM FROM` | 3 | Partly: `konturctl guest rebase` is `VM FROM` with no instructions after it -- inherit the machine, take the current VMM half. There is no stage syntax for it. |
| `RUN` in a VM stage | 3 | `internal/guestbuild` + `internal/guestboot` run commands in a booted guest (`konturctl guest build -setup`), which is the mechanism; there is no per-instruction form. |
| Multi-stage, `COPY --from` across kinds | 3 | Not implemented. |
| `konturctl build` / `kontur build` CLI | 1-3 | Not implemented. `konturctl guest build/pack/rebase/verify` are today's entry points (`internal/cli/guest.go`). |

The implementation order this suggests is the one
`docs/guest-images.md` already argues for: the packing path first, since
it needs no KVM and executes nothing; VM stages after it, because they
need a boot per instruction and a cache to make that bearable.

## 13. Changes from the language sketch

`docs/language_sketch.md` opened these questions without settling them.
Each was settled here, and this is the list of what a reader of the
sketch will find different.

1. **`VM` takes a verb, and the verbs are closed.** `FROM`, `WRAP`,
   `PACK` and `SET`. The sketch used the first three without saying
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
4. **Stage kind decides instruction meaning, and is stated as a rule**
   rather than as a comment in an example. The sketch's `# Running
   inside a container` / `# Running inside the VM!` comments are §4.2 and
   §6.
5. **What a stage above a converter may use depends on which converter
   ends it** (§7). This is new: it is what lets `VM WRAP` be appended to
   an arbitrary Dockerfile -- entrypoint and all -- while `VM PACK`
   still refuses the container-runtime instructions that a disk cannot
   honour.
6. **`VM SET` is added.** The sketch gave no way to say a machine wants
   4 GiB, which is the first question every author asks. It maps to the
   runtime variables that already exist rather than inventing a second
   configuration vocabulary.
7. **Options are `--flag=value`, before positionals**, matching
   `COPY --from=`. The sketch had no options.
8. **`VM FROM` requires a bootable kontur image**, and a rootfs image
   handed to it is an error naming `VM PACK`. The sketch's `VM FROM
   kontur/alpine` is ambiguous about which of the two it meant.
9. **`VM FROM` takes the current VMM half**, rather than inheriting the
   referenced image's. The sketch's comment ("replace the hypervisor
   side with our validated code") asked for exactly this; it is now the
   defined behaviour rather than an aside, and it is the rebase
   `docs/guest-images.md` already specifies.
10. **`ENV` in a VM stage is build-time only** (§6). Neither reading is
    obviously right, and an author who assumes the other one gets a
    machine whose services do not see the variable, so the specification
    picks one and says what to write instead.
11. **The examples are corrected.** The sketch's `RUN apt-get install
    redis` on `alpine:latest` (Alpine has `apk`), and its `RUN systemd
    add my_service` (not a command), are replaced in §14 by lines that
    would actually run. The sketch was prose about shape; a
    specification's examples have to be executable to be worth reading.
12. **File name and resolution are defined** (§3.1): `Konturfile`, then
    `Dockerfile`. The sketch named the format after a file nobody had
    said how to find.
13. **Parser directives are refused in a file that uses `VM`** (§3.3).
    Not mentioned in the sketch, and the rewriting it implies makes them
    inert rather than merely unusual.

Nothing in the sketch's shape was rejected. Every example in it has a
direct translation here, given in §14.

## 14. Worked examples

### 14.1 A Dockerfile is a Konturfile

```dockerfile
FROM alpine:latest

# This runs in a container and I get a container.
RUN apk add --no-cache redis
```

Built by kontur, this produces the container image docker would produce.
No `VM` instruction, no machine (§1.2).

### 14.2 Wrapping it in a machine

```dockerfile
FROM alpine:latest

RUN apk add --no-cache redis
CMD ["redis-server", "--protected-mode", "no"]

# Now it runs inside a VM.
VM WRAP redis
```

One line added to the end, nothing above it touched. The result is a
kontur VM image whose guest runs the container above as
`kontur-wrap@redis`, restarting always, leaving the machine up if it
exits (§5.2). `CMD` is legal here because the stage above the converter
is a container stage ending in `VM WRAP` (§7).

### 14.3 Packing a rootfs into a machine

```dockerfile
FROM ghcr.io/gke-labs/kontur-guest-rootfs:debian12

# Copy a file into the rootfs (in a container, offline).
COPY greeting.txt /greeting.txt
RUN apt-get update && apt-get install -y nginx && systemctl enable nginx

# Now the VM boots with that rootfs as its root filesystem.
VM PACK
```

Also one line at the end, and also no KVM: everything here is a
container build plus a pack (§5.3). The base must be a guest rootfs, and
the packing lays kontur's agents and units over it.

### 14.4 Deriving a machine from a machine

```dockerfile
# Inherit the machine; the hypervisor half is this build's own.
VM FROM ghcr.io/example/github-runner-vm:latest

# This runs inside the VM, and I get a VM image.
COPY runner.service /etc/systemd/system/runner.service
RUN systemctl enable runner.service
```

The `COPY` is applied offline; the `RUN` boots the guest and executes in
it, so this file needs `/dev/kvm` (level 3, §2.3).

### 14.5 Building a machine and then provisioning it

```dockerfile
FROM ghcr.io/gke-labs/kontur-guest-rootfs:debian12 AS redis_vm

# Running inside a container.
RUN apt-get update && apt-get install -y redis-server

VM PACK

# Running inside the VM.
RUN systemctl enable redis-server
VM SET memory-mb=2048 cpus=2
```

The stage crosses from container to machine at `VM PACK`. Both `RUN`s
are in the same stage and neither is ambiguous: the first is a container
build step, the second boots the machine the first produced. Doing the
`systemctl enable` above the converter would have been cheaper, and an
implementation MAY say so; it is not an error, because the general case
-- a step that needs a booted machine -- is exactly what a `RUN` below a
converter is for.

### 14.6 Composing machines

```dockerfile
# A VM build stage, used only as a source of files.
VM FROM ghcr.io/example/github-runner-vm:latest AS github_vm

# The final machine.
VM FROM ghcr.io/gke-labs/kontur-alpine-vm:latest

# Copy out of the other machine's root filesystem.
COPY --from=github_vm /etc/runner/config.json /etc/runner/config.json
VM SET memory-mb=4096
```

Neither stage runs anything, so neither needs KVM: the copy reads the
first stage's guest root filesystem directly (§8).

## 15. Deliberately out of scope

- **A Konturfile that fixes resource limits.** `VM SET` records
  defaults, and the runtime overrides them. An image that could refuse
  to boot with more memory than its author imagined would be a new
  policy mechanism, and the one place such a policy belongs is the
  cluster that schedules the machine.
- **Snapshot and resume as language constructs.** Booting a machine to a
  saved state is a runtime operation on a VM, not a build step, and
  nothing in a build file would make it one.
- **Multi-architecture VM stages.** A guest disk is built for one
  architecture and boots under a hypervisor of that architecture. A
  `--platform` on a VM stage would have to mean cross-booting, which
  needs emulation this project does not carry.
- **A layer chain of guest disks.** Rejected on its own merits in
  `docs/guest-images.md`: a qcow2 records its backing file's path and
  size rather than its content, and stacking requires turning a CVE
  mitigation off. The packed path is both the cheap one and the safe
  one, and the language should not offer a syntax for the other.
- **Running a Konturfile's build inside a VM.** The isolation argument
  in `docs/guest-images.md` applies to where a build runs, not to what
  it is written in.

## Appendix A: collected grammar

```ebnf
konturfile     = { global-arg | comment } stage { stage } ;
global-arg     = "ARG" WS name [ "=" value ] ;

stage          = stage-header { instruction | comment } ;
stage-header   = "FROM" WS ref [ WS "AS" WS name ]
               | "VM" WS "FROM" WS ref [ WS "AS" WS name ] ;

instruction    = dockerfile-instruction | vm-instruction ;

(* VM FROM is a stage header rather than an instruction: it starts a
   stage, so it cannot appear inside one (§6). *)
vm-instruction = "VM" WS "WRAP" { WS wrap-option } [ WS name ]
               | "VM" WS "PACK" { WS pack-option }
               | "VM" WS "SET" WS setting { WS setting } ;

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
| `/var/lib/kontur/guest/` | the guest half of a VM image (§9.1) |
| `/var/lib/kontur/overlay/` | per-VM writes, created at boot |
| `/var/lib/kontur/wrap/` | wrapped workloads (§9.2) |
| `/usr/local/bin/kontur`, `/usr/local/bin/kontur-agent`, `/usr/local/bin/kontur-mem-agent` | the runtime and the guest agents |
| `kontur-*` units and services | kontur's own, in any guest variant |
| `dev.kontur.*` labels | kontur's own image metadata (§9.3) |
| `CHV_*`, `KONTUR_*` environment | the runtime's own configuration |

A `COPY` or `RUN` that writes into one of these paths in a VM stage
SHOULD be diagnosed, and MUST NOT be relied on: a pack or a rebase lays
kontur's own copies down over them afterwards, which is the property
that lets an image built months ago boot with today's agent in it.
