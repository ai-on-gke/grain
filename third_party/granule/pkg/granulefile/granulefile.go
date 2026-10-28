// Package granulefile parses and validates the Granule language:
// Dockerfile's format with one instruction added, VM, which turns a
// container build into a machine build. docs/granulefile.md is the
// specification, and the section numbers quoted throughout this package
// are its.
//
// This package is the front half and nothing else. It reads a file,
// works out its stages and their kinds, and says what is wrong with it;
// it builds nothing, pulls nothing and executes nothing, which is what
// makes it the piece that can land before there is a builder to put
// behind it (§12). A caller that wants to know whether a file is
// buildable asks Check; a caller that wants the structure asks Parse.
//
// Three properties are the whole of what it is for, and all three are
// §10's:
//
//   - It fails closed. What the language does not allow is refused, and
//     what this tree has not built yet is refused as unimplemented and
//     named as such -- never ignored, never warned about and built
//     anyway. A format that silently drops half of what it was handed is
//     worse than one that never claimed the resemblance.
//   - It reports every violation, not the first. An author porting a
//     container Dockerfile trips several at once: a HEALTHCHECK, a
//     STOPSIGNAL and an EXPOSE are one habit, not three.
//   - Every refusal names the file, the line in the author's own file and
//     the instruction as the file spells it, and cites the rule it
//     refuses under, so the reason can be looked up rather than guessed
//     at.
package granulefile

// Version is the specification version this package implements: the
// string at the head of docs/granulefile.md, which §11.4 requires an
// implementation to be able to report. It changes when that document
// changes in a way a file can notice.
const Version = "granulefile/1.0-draft"

// Level is a conformance level (§2.3). Each includes the ones before it,
// and an implementation MUST refuse a construct from a level it does not
// implement, naming the level, rather than ignoring it.
type Level int

const (
	// Level1 is Dockerfile: files with no VM instruction.
	Level1 Level = 1
	// Level2 adds the converters and offline extraction -- VM PACK, VM
	// WRAP, VM SET, VM UNPACK FROM and the stage-header converters (VM
	// PACK FROM, VM WRAP FROM). No instruction executes inside a VM at
	// this level, so no level 2 build needs /dev/kvm.
	Level2 Level = 2
	// Level3 adds VM stages: VM FROM, RUN in a VM stage, VOLUME mount
	// handling, VM SNAPSHOT, and cross-stage copies out of a VM stage. A
	// RUN in a VM stage boots a machine, so it needs /dev/kvm on the
	// machine running the build.
	Level3 Level = 3
)

func (l Level) String() string {
	switch l {
	case Level1:
		return "level 1 (Dockerfile)"
	case Level2:
		return "level 2 (converters and offline extraction)"
	case Level3:
		return "level 3 (VM stages)"
	default:
		return "an unknown level"
	}
}

// Kind is what a stage is building at a point in the file (§4.2): a
// container image, or a machine. A stage has one at its header and may
// have the other below an in-stage converter, which is why so much of
// this package asks *where* in a stage it is being asked about.
type Kind int

const (
	// Container is a Dockerfile stage: RUN runs in a container, COPY
	// writes into the container filesystem.
	Container Kind = iota
	// VM is a machine under construction: RUN runs inside a booted
	// guest, COPY writes into the guest root filesystem.
	VM
)

func (k Kind) String() string {
	if k == VM {
		return "VM"
	}
	return "container"
}
