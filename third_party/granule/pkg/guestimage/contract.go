package guestimage

import (
	"fmt"
	"strconv"
	"strings"
)

// The guest-side contract: what granule requires of the machine inside a
// guest image, and how an image says which version of it it was built
// against.
//
// # What the contract covers
//
// Everything granule puts inside a guest and then depends on from
// outside it:
//
//   - the agents (granule-agent, granule-mem-agent) and the vsock protocol
//     they speak, pkg/execwire;
//   - the units that start them, and the order they start in --
//     deploy/guest-image/overlay-debian and overlay-alpine;
//   - the helpers the overlay places (granule-growfs, granule-configure-dns,
//     granule-configure-net and the rest of overlay-common), and the
//     kernel command line they read;
//   - the assumptions pkg/guestexec and pkg/guestcp make about
//     the session those agents hand back.
//
// A guest built months ago under an older granule may satisfy none of
// that. Overwriting the guest side is the mitigation -- `guest pack` and
// both complete forms of `guest rebase` lay the current pieces down --
// and this version is the backstop: it is what lets a *cheap* rebase,
// which leaves the disk alone, tell a guest that is fine from one whose
// agents predate something granule now requires. Without it the only
// symptom is a VM that boots, looks healthy and never answers.
//
// # A single integer with a floor, not a set of capabilities
//
// The alternative was a named capability set ("this guest can do X"),
// which answers the question a fleet eventually asks and is strictly
// more work: every capability is a name that has to be minted, written,
// read and then kept meaning the same thing for as long as any image
// carries it. What the refusal needs is narrower than that -- one
// ordering, so that "older than granule can drive" is a comparison rather
// than a set difference -- and it is the same shape as the check
// pack-guest already makes about a rootfs that enables none of granule's
// units: a specific, nameable reason to stop.
//
// So: one integer that counts up, and a floor below which this build
// refuses. A capability set can still be added later *beside* this, if
// the fleet question turns up; what it must not do is replace the floor,
// because "can this guest do X yet" and "may this guest be left alone"
// are different questions and only the second one can refuse.
//
// # Where it is written
//
// Both places, because they fail in different directions:
//
//   - ContractLabel, on the packed image. Readable with one `docker image
//     inspect`, without opening a multi-gigabyte disk -- which is exactly
//     what a cheap rebase can afford, since the whole point of the cheap
//     form is that it never looks inside disk.img.
//   - ContractFile, inside the guest filesystem, placed by the overlay
//     (deploy/guest-image/overlay-common). It survives being repacked by
//     somebody else's tooling, being committed out of a running VM, and
//     having the labels stripped or rewritten -- none of which the label
//     survives -- so it is the record of last resort, and the thing to
//     read when an image's label and its disk disagree.
//
// The label is the operative one: it is what the refusal below is
// decided on, because it is the only one a cheap rebase can read. The
// file is what makes the answer recoverable when the label is gone.
//
// # Moving these numbers
//
// Bump Contract when granule changes what it puts in a guest or what it
// expects back from one -- a new unit, a new helper, a wire change in
// pkg/execwire. That is cheap: it costs one line here, one in
// deploy/guest-image/overlay-common/etc/granule/guest-contract, and one
// in the Dockerfile's final stage, and TestContractIsDeclaredEverywhere
// fails until all three agree.
//
// Moving MinimumContract is the expensive one, and it is a deliberate
// act rather than a consequence: every image published under a contract
// below it stops being rebasable cheaply, and its operator has to spend
// a repack or a boot. Move it only when granule genuinely cannot drive
// the older guest any more -- when the failure it is preventing is the
// VM that boots and never answers -- and say in this file's history what
// the break was.
//
// # History
//
//	1  The contract as of the version this was introduced in: the vsock
//	   protocol pkg/execwire speaks, the agents and units in
//	   deploy/guest-image's overlays, and the helpers they run. Every
//	   guest packed before this declares nothing at all, which is why
//	   ContractUndeclared is a report rather than a refusal.
//	2  granule-mem-agent takes its token from the root-only file granule
//	   writes over vsock (pkg/memagent's TokenFile) instead of the kernel
//	   command line, which granule no longer puts it on. A guest below
//	   this still boots and execs; its memory agent just never signals
//	   with a token the host accepts, so the guest stays at its boot
//	   size. Not a reason to move MinimumContract.
//	3  The guest configures IPv6: granule-configure-ip6 applies
//	   granule.ip6=, and granule-configure-routes applies granule.routes6=
//	   beside granule.routes=. A guest below this ignores both and boots
//	   with IPv4 only -- or, on a v6-only pod, with no network at all --
//	   which granule warns about at boot (ContractIPv6). Not a reason to
//	   move MinimumContract: an IPv4 guest is unaffected.
const (
	// Contract is the guest-side contract version this build of granule
	// writes into every guest it packs or completely rebases.
	Contract = 3

	// ContractIPv6 is the first contract whose guests configure the
	// IPv6 half of their identity (granule.ip6= and granule.routes6=).
	ContractIPv6 = 3

	// MinimumContract is the oldest contract this build can still drive.
	// A guest declaring less than this is one granule refuses to leave
	// alone, rather than one it warns about.
	MinimumContract = 1

	// ContractLabel is the image label carrying the version of the guest
	// *inside* that image. It describes the disk, not the image's own
	// build, which is why a cheap rebase has to restate it rather than
	// inherit the granule image's: the VMM half moves and the guest half
	// does not.
	ContractLabel = "dev.granule.guest.contract"

	// ContractFile is the same number inside the guest filesystem, placed
	// by the overlay. One line, decimal, no decoration -- it is read by
	// /bin/sh as often as by Go.
	ContractFile = "/etc/granule/guest-contract"
)

// ContractValue is Contract as it is written down: what goes in the
// label, and what the overlay's own copy of ContractFile has to say.
func ContractValue() string { return strconv.Itoa(Contract) }

// ContractState is what this build of granule makes of a version some
// guest declares.
type ContractState int

const (
	// ContractUndeclared is an image or guest that says nothing, which is
	// every one published before granule started stamping a version. It is
	// never a refusal: making it one would mean the first thing this
	// feature did was break every existing guest.
	ContractUndeclared ContractState = iota

	// ContractUnreadable is a declaration that is there but is not a
	// version -- a label somebody set by hand, a file that picked up a
	// build's worth of text. Treated as undeclared for the purposes of
	// refusing, and said out loud rather than silently ignored, because
	// an image that meant to declare something and failed is worth a
	// sentence.
	ContractUnreadable

	// ContractTooOld is below MinimumContract: a guest whose granule-side
	// pieces predate something this granule requires.
	ContractTooOld

	// ContractSupported is a version this build can drive as it stands.
	ContractSupported

	// ContractAhead is newer than this build writes: the guest was packed
	// by a later granule than the one now rebasing it. Not a refusal --
	// the caller is replacing the VMM half with their own on purpose --
	// but worth saying, since it means the halves are skewed in the
	// direction nothing here can fix.
	ContractAhead
)

func (s ContractState) String() string {
	switch s {
	case ContractUndeclared:
		return "undeclared"
	case ContractUnreadable:
		return "unreadable"
	case ContractTooOld:
		return "too old"
	case ContractSupported:
		return "supported"
	case ContractAhead:
		return "ahead"
	}
	return "unknown"
}

// ReadContract classifies what a guest declares, given the raw text of
// its label or its ContractFile. The version it returns is 0 for
// anything that did not parse, so a caller can print the state without
// having to decide what an absent number means.
//
// Whitespace is trimmed because one of the two carriers is a file that
// ends in a newline. Nothing else is accepted: a version is a decimal
// integer above zero, and a "v1" or a "1.2" is a declaration somebody
// invented rather than one granule wrote.
func ReadContract(declared string) (ContractState, int) {
	text := strings.TrimSpace(declared)
	if text == "" {
		return ContractUndeclared, 0
	}
	version, err := strconv.Atoi(text)
	if err != nil || version <= 0 {
		return ContractUnreadable, 0
	}
	switch {
	case version < MinimumContract:
		return ContractTooOld, version
	case version > Contract:
		return ContractAhead, version
	default:
		return ContractSupported, version
	}
}

// DescribeContract is the neutral half of what a caller says about a
// declared version: the fact, and not the remedy. Which flag refreshes a
// guest, and whether the caller is in a position to spend it, belongs to
// the command that has those flags -- see pkg/guestrebase -- and
// this is here so that the fact itself is worded the same way wherever
// it is reported.
//
// The declaration is rendered with %q where it did not parse: it comes
// off an image somebody else built, and is text rather than a number by
// definition in exactly that case.
func DescribeContract(declared string) string {
	state, version := ReadContract(declared)
	switch state {
	case ContractUndeclared:
		return "declares no guest-side contract version, so how old its guest side is cannot be told from the outside"
	case ContractUnreadable:
		return fmt.Sprintf("declares %q as its guest-side contract version, which is not a version this granule can compare against its own", strings.TrimSpace(declared))
	case ContractTooOld:
		return fmt.Sprintf("declares guest-side contract version %d, and this granule needs at least %d", version, MinimumContract)
	case ContractAhead:
		return fmt.Sprintf("declares guest-side contract version %d, which is newer than the %d this granule writes: it was packed by a later granule than the one rebasing it", version, Contract)
	default:
		return fmt.Sprintf("declares guest-side contract version %d, which this granule supports", version)
	}
}
