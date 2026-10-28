package guestdockerfile

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bwsalmon/granule/pkg/granulefile"
)

// Supported lists the instructions a granule guest Dockerfile may use, in
// the order they are documented. They mean exactly what they mean to
// docker: this is a subset of Dockerfile, not a dialect of it.
var Supported = []string{"FROM", "RUN", "COPY", "ADD", "ENV", "WORKDIR", "USER", "ARG", "LABEL", "SHELL", "ENTRYPOINT", "CMD", "EXPOSE"}

// Error is the refusal of a whole file: every violation in it, not the
// first.
//
// Reporting all of them is deliberate. A consumer porting a container
// Dockerfile typically trips several of these at once (an ENTRYPOINT, a
// CMD and an EXPOSE are one habit, not three), and a validator that
// reports one per build turns a five-minute edit into five builds.
type Error struct {
	// Path is the file as the caller named it, so the message is
	// something an editor can be pointed at.
	Path       string
	Violations []Violation
}

func (e *Error) Error() string {
	var b strings.Builder
	noun := "instruction"
	if len(e.Violations) != 1 {
		noun = "instructions"
	}
	fmt.Fprintf(&b, "%s: %d %s granule cannot build a guest from:", e.Path, len(e.Violations), noun)
	for _, v := range e.Violations {
		fmt.Fprintf(&b, "\n  %s:%d: %s", e.Path, v.Line, v)
	}
	fmt.Fprintf(&b, "\n\nWhat a granule guest Dockerfile may use: %s. See deploy/guest-image/README.md.", strings.Join(Supported, ", "))
	return b.String()
}

// Validate checks an instruction list against the subset above and
// returns an *Error naming path, every line and every instruction it
// refuses.
//
// The rule it enforces, in both directions: the vocabulary is docker's
// wherever docker has a word for the thing, and what granule does not
// implement is refused rather than ignored. A format that silently drops
// half of what it was handed is worse than one that never claimed the
// resemblance -- a dropped CMD is a machine that comes up without the
// thing the file said to run, discovered in production rather than at
// build time.
//
// The subset is the same in every stage of a multi-stage file. A builder
// stage's ENTRYPOINT is inert rather than harmful, so refusing it is
// stricter than it strictly has to be -- but one list that holds
// everywhere is a rule a reader can keep in their head, and "this
// instruction is allowed in stages nothing is copied out of" is not.
// What differs between stages is the base, not the vocabulary: see Base.
//
// The list is this command's and not the Granule specification's, and
// the difference is deliberate. The file this validates is what
// docs/granulefile.md §7 calls a container stage above a `VM PACK` --
// granule ends it with a pack of its own rather than reading one out of
// the file -- and where the two agree, the reasons below are that
// document's §6 table. Where they do not, this command is the stricter
// or the looser of the two on purpose: it records an EXPOSE (exposeNote),
// it accepts a SHELL, it records ENTRYPOINT and CMD into a packed machine's
// workload configuration, and a FROM here is the base of a rootfs rather
// than a stage header in a language with two kinds of stage. A file that wants the language's own
// rules wants `granule granulefile check`.
func Validate(path, src string) ([]Instruction, error) {
	instrs, err := Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	// The parser directives are refused for the same mechanical reason
	// in both dialects -- granule rewrites the file before a builder sees
	// it -- so the refusals are granulefile's and are not written down
	// twice (§3.3, K8).
	violations := granulefile.Directives(src)
	for _, in := range instrs {
		if v, ok := refuse(in); ok {
			violations = append(violations, v)
		}
	}
	sort.SliceStable(violations, func(i, j int) bool { return violations[i].Line < violations[j].Line })

	if len(violations) > 0 {
		return nil, &Error{Path: path, Violations: violations}
	}
	if len(instrs) == 0 {
		return nil, fmt.Errorf("%s: no instructions: a guest build needs either a base to re-lay granule's own half onto (FROM) or something to do to granule's", path)
	}
	return instrs, nil
}

// refuse decides one instruction.
func refuse(in Instruction) (Violation, bool) {
	v := Violation{Line: in.Line, Instruction: in.Name}

	// A VM instruction is a Granule's, and this command is not a
	// Granule builder. It is a gap rather than a rule, so it is named
	// as one (§10.3) with the level it belongs to, and with where the
	// thing it asks for already lives.
	if in.IsVM() {
		return Violation{
			Line: in.Line, Instruction: in.String(), Level: granulefile.Level2,
			Reason: "unimplemented: " + in.String() + " is a Granule instruction (docs/granulefile.md), and `granule guest build` is not a Granule builder -- it validates one file's worth of instructions, supplies the pack itself and packs what docker produces. This file is already the container stage a VM PACK would convert, so the pack is the command rather than a line: drop it and the build does what it says. `granule granulefile check` validates the language",
		}, true
	}

	switch in.Name {
	// SHELL is here rather than below because it is the one purely
	// build-time instruction of the five that used to be refused
	// alongside it: it decides which interpreter the shell form of RUN
	// is handed to, docker implements all of it, and it asks nothing of
	// the VM. `SHELL ["/bin/bash", "-euo", "pipefail", "-c"]` is
	// ordinary good practice -- without it a failure in the middle of a
	// pipeline is ignored and the RUN succeeds -- so refusing it cost
	// real safety in consumers' own steps and bought nothing.
	//
	// EXPOSE is accepted and recorded rather than opened: see
	// exposeNote and pkg/guestpack. It opens nothing here, exactly
	// as it opens nothing under docker -- what a launcher does with the
	// recorded declaration is its own decision (`granule vm create
	// -publish-declared`).
	case "RUN", "COPY", "ENV", "WORKDIR", "USER", "ARG", "LABEL", "SHELL", "ENTRYPOINT", "CMD", "EXPOSE":
		return Violation{}, false

	case "FROM":
		if from := ParseFrom(in); from.Image == "" {
			v.Reason = "FROM names no image. A guest build's last FROM is the base its filesystem comes from -- granule's own guest rootfs image, or one built from it -- and an earlier one is a build stage"
			return v, true
		}
		return Violation{}, false

	case "ADD":
		remote := remoteSources(in.Args)
		if len(remote) == 0 {
			return Violation{}, false
		}
		v.Reason = fmt.Sprintf("granule refuses ADD of a remote source (%s): it fetches at build time from a host outside the build context, with the network of whatever machine ran the build and nothing recording what came back. COPY it out of the build context, or fetch it in a RUN where the checksum is yours to check. ADD of a local path is supported, and is COPY with tar extraction", strings.Join(remote, ", "))
		return v, true

	case "VOLUME":
		v.Reason = "a guest's filesystem is a packed disk image the VM mounts as its root, not a container filesystem with mount points the runtime can substitute. Nothing would act on this declaration at boot, and a directory declared as a volume in a rootfs is an ordinary directory in the disk"
		return v, true

	case "HEALTHCHECK":
		v.Reason = "docker health checks probe a container's main process, and this guest is not run as one. Health of a guest is a systemd unit's business inside it, or `granule vm status`/`vm wait` from outside"
		return v, true

	case "STOPSIGNAL":
		v.Reason = "a granule guest is stopped by an ACPI power button press, which acpid turns into an ordinary shutdown (see deploy/guest-image/README.md's \"Graceful shutdown\"). There is no process in here to signal, so this would change nothing about how the machine goes down"
		return v, true

	case "ONBUILD":
		v.Reason = "ONBUILD defers instructions to whoever builds FROM the result, where none of them is validated against this subset and where granule is not the one supplying the base. There is also no second build to defer to: what comes out of a guest build is packed into a disk, not built on"
		return v, true

	case "MAINTAINER":
		v.Reason = "docker deprecated MAINTAINER long ago and granule does not carry it forward. `LABEL org.opencontainers.image.authors=\"...\"` is the supported spelling and lands in the image's metadata"
		return v, true
	}

	v.Reason = "not an instruction granule knows. If docker has it and this list does not, it is unimplemented rather than forbidden -- say so, rather than working around it"
	return v, true
}

// From is what one FROM says: the image (or earlier stage) it names, and
// the name it gives the stage it opens, if any.
type From struct {
	// Image is the first non-flag word of the instruction -- an image
	// reference, or the name of a stage declared earlier in the file.
	Image string
	// Stage is the "AS name" this FROM gives its stage, or "".
	Stage string
	// Line is where it was written, for a message that points at it.
	Line int
}

// ParseFrom reads a FROM instruction. It understands exactly as much as
// granule has a decision to make about -- the image and the stage name --
// and leaves the rest (--platform and anything docker adds later) to
// docker, which is the one actually resolving it.
func ParseFrom(in Instruction) From {
	from := From{Line: in.Line}
	fields := strings.Fields(in.Args)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.HasPrefix(f, "--") {
			continue
		}
		if from.Image == "" {
			from.Image = f
			continue
		}
		if strings.EqualFold(f, "AS") && i+1 < len(fields) {
			from.Stage = fields[i+1]
		}
		break
	}
	return from
}

// Base returns the FROM that decides what the guest's filesystem is made
// of: the last one in the file.
//
// The last one, because the last stage is the one a `docker build`
// without --target produces, and what this build produces is the rootfs
// that gets packed. Earlier FROMs are build stages -- `FROM golang AS
// builder` with a `COPY --from=builder` later on is the reason
// multi-stage is allowed at all -- and they may name anything, since
// nothing of a build stage reaches the guest except what a COPY takes
// out of it.
//
// ok is false for a file that names no base at all, which is the
// ordinary case and the one where granule supplies its own.
func Base(instrs []Instruction) (From, bool) {
	var base From
	var ok bool
	for _, in := range instrs {
		if in.Name == "FROM" {
			base, ok = ParseFrom(in), true
		}
	}
	return base, ok
}

// exposeNote is printed by a build whose file declares ports, because
// EXPOSE means something slightly stronger here than it does to docker
// and the difference is worth stating once rather than discovering.
//
// Under docker EXPOSE opens nothing: its one functional consumer is
// `docker run -P`, and -p works whether or not a port was exposed. A
// granule guest is shielded by default, so the word can carry the useful
// meaning -- the image's own declaration of what it wants reachable --
// and granule keeps docker's two-part relationship for it. The image
// declares; something else grants. Anything else would make every
// casually written documentation line punch a hole in the shield, and
// make the same file mean "nothing" under `docker build` and "open 8080"
// under granule.
//
// The something else is `granule vm create -publish-declared`, which is
// this relationship's `docker run -P`: it reads the declaration back off
// the packed image and publishes those ports on the VM's own container
// (pkg/dockervm, and the top-level README's "Publishing a guest's
// ports"). Naming it here is the point of the note -- a declaration
// whose grant nobody can find is one people work around.
const exposeNote = "EXPOSE records what this guest wants reachable and opens nothing, here or under docker. The declaration is carried into the packed image's own OCI config, where a launcher reads it: \"granule vm create -publish-declared\" publishes these ports on the VM's container, and without that grant they stay shut."

// remoteSources returns the sources of an ADD that are not local paths.
//
// Everything but the last field is a source, and a field beginning with
// "--" is one of ADD's own flags (--chown, --chmod, --checksum, --link,
// --keep-git-dir, --exclude) rather than either.
func remoteSources(args string) []string {
	fields := strings.Fields(args)
	var sources []string
	for i, f := range fields {
		if strings.HasPrefix(f, "--") {
			continue
		}
		if i == len(fields)-1 {
			break
		}
		sources = append(sources, f)
	}

	var remote []string
	for _, s := range sources {
		if remoteSource.MatchString(s) {
			remote = append(remote, s)
		}
	}
	return remote
}

// remoteSource matches the source forms ADD fetches over the network: a
// URL, and git's scp-style remote syntax.
var remoteSource = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://|[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:)`)
