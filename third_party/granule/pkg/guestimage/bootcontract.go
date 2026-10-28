package guestimage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The boot-time half of the guest contract: the copy of the declaration
// the VMM half reads, and the refusal it makes.
//
// contract.go says what the contract is, what its version means and
// where a guest writes it down. Two of the three carriers it names are
// no use to a boot:
//
//   - ContractLabel needs an image to inspect, and at boot there is no
//     image -- there is a container that already is one, running the VMM
//     half, with no docker to ask. It is the carrier a *rebase* uses,
//     because a rebase is a build.
//   - ContractFile is inside the guest filesystem, which at boot is an
//     unmounted ext4 image an untrusted consumer authored. Reading it
//     would mean either mounting that filesystem -- a privilege the VM
//     container deliberately does not have -- or putting an ext4 reader
//     inside the one binary whose smallness is itself an isolation
//     property.
//
// So a pack writes it a third time, as a plain file beside the disk:
// ContractSidecar. The VMM half opens it with os.Open before
// cloud-hypervisor is started, which is the only moment a refusal is
// worth anything. A VM that boots and is then killed has already been
// handed to its caller as running.
//
// # Why the refusal, and why it is not a warning
//
// A guest and the granule booting it disagreeing about the contract is
// not a VM that fails. It is a VM that boots, looks healthy from
// outside, and whose control link, agents or boot parameters are not the
// ones the VMM half is providing -- so it never answers. A caller who
// was handed it believes they have a working sandbox. That is the one
// outcome worth refusing a boot to avoid, and a warning in a log nobody
// reads is not a mitigation for it.
//
// # What this is not
//
// An attestation. The sidecar is a file in an image its author controls,
// so it can say anything; a hostile guest is contained by the isolation
// boundary -- the VMM half, which is always the deployment's own -- and
// not by this number. What it catches is accidental skew, which is the
// kind that actually happens: an operator's upgrade moves the VMM half
// for every deployment at once, and a guest packed six weeks ago by
// another repository does not move with it.
const (
	// ContractSidecarName is the declaration's name beside the packed
	// disk. The same basename it has inside the guest, because it is the
	// same bytes: deploy/guest-image/pack-guest copies it out of the
	// rootfs it has just laid the overlay over.
	ContractSidecarName = "guest-contract"

	// ContractSidecar is where that copy sits for the guest half at its
	// conventional location, i.e. beside DiskImage. A boot resolves it
	// against whatever disk it was actually told to boot -- see
	// ContractSidecarPath -- so this is the default rather than the rule.
	ContractSidecar = Dir + "/" + ContractSidecarName

	// ContractUndeclaredVersion is what a guest half carrying no sidecar
	// at all is taken to be speaking: the contract as it stood before
	// anyone wrote a version down.
	//
	// The alternative -- refusing a guest that declares nothing -- was
	// considered and rejected; the reasoning is in docs/guest-images.md
	// under "Absent is version 0". In short, absence is not evidence of
	// skew. Every guest assembled before the declaration existed was
	// assembled against a VMM half that agreed with it by construction,
	// and Contract 1 is that same contract with a name on it. Refusing
	// them would mean this check's first act was to break every guest
	// already in the field on the operator's next upgrade, for skew that
	// has not happened -- and a check that refuses everything is a check
	// somebody turns off.
	//
	// It is a deferral rather than a hole. The moment the contract
	// changes in a way version 0 cannot satisfy, MinimumBootContract
	// rises above it and every undeclared guest is refused by the same
	// named error. Until then each boot logs that the guest declared
	// nothing, so an operator can find those images before an upgrade
	// does, and CHV_GUEST_CONTRACT_MIN (pkg/config) lets a
	// deployment that packs its own guests require a declaration today.
	ContractUndeclaredVersion = 0

	// MinimumBootContract is the oldest guest contract this build of the
	// VMM half will boot.
	//
	// It is a second floor beside MinimumContract, not a duplicate of it,
	// because the two answer different questions at different prices.
	// MinimumContract is what a cheap `granule guest rebase` refuses on:
	// the remedy is a repack, the caller is at a terminal, and the cost of
	// being too strict is a build. This one is what a running deployment
	// refuses on: the remedy is that somebody's workload does not start,
	// and the cost of being too strict is an outage. So it moves later and
	// more reluctantly than MinimumContract does, and it starts at
	// ContractUndeclaredVersion -- below every version anything declares
	// -- so that today it refuses only a guest from the *future*.
	//
	// Raise it when a contract change leaves an older guest unreachable
	// rather than merely missing something new. That is the whole
	// difference between bumping Contract and bumping a floor, and this
	// floor is the one that makes guests stop booting, so it moves for a
	// reason somebody has written down in this file's history.
	MinimumBootContract = ContractUndeclaredVersion

	// maxContractBytes bounds what is read from a sidecar. A declaration
	// is one decimal number; anything larger is not one, and it arrives
	// from an image this deployment did not build.
	maxContractBytes = 4 << 10
)

// BootContract is what the guest half beside a disk says about itself,
// as the VMM half read it.
type BootContract struct {
	// State is how ReadContract classified the declaration, with one
	// difference: ContractTooOld is never set here, because "too old" at
	// boot is measured against the caller's floor rather than against
	// MinimumContract. Check is what makes that judgement.
	State ContractState

	// Version is the version declared, or ContractUndeclaredVersion when
	// nothing was declared or what was declared did not parse.
	Version int

	// Declared is the sidecar's contents as read, trimmed -- empty when
	// there was no sidecar. Kept so a refusal can quote text that is not
	// a number, which is the whole of what is wrong with it.
	Declared string

	// Path is where the sidecar was looked for, so that every message
	// about it names a file an operator can go and read.
	Path string
}

// ContractSidecarPath is where the declaration for the guest disk at
// diskImage lives.
//
// Resolved against the disk rather than fixed at ContractSidecar because
// CHV_DISK_IMAGE moves the disk, and a declaration that stayed put would
// then be describing some other guest -- most likely the bundled one,
// which is the one wrong answer worse than no answer at all. An empty
// diskImage gives the conventional path.
func ContractSidecarPath(diskImage string) string {
	if diskImage == "" {
		return ContractSidecar
	}
	return filepath.Join(filepath.Dir(diskImage), ContractSidecarName)
}

// ReadBootContract reads the declaration belonging to the guest disk at
// diskImage.
//
// A sidecar that is not there is not an error: it is
// ContractUndeclared, for the reasons on ContractUndeclaredVersion. A
// sidecar that is there and cannot be *read* is an error, and
// deliberately a different case from absence -- a file sitting at that
// path unreadable is evidence that something is wrong with the image,
// not evidence that the image is old.
//
// A sidecar that reads but does not parse is neither: it comes back as
// ContractUnreadable for Check to refuse, so that the refusal can quote
// what it actually said.
func ReadBootContract(diskImage string) (BootContract, error) {
	path := ContractSidecarPath(diskImage)
	c := BootContract{State: ContractUndeclared, Version: ContractUndeclaredVersion, Path: path}

	// O_NONBLOCK, because of the fifo the mode check below refuses: an
	// ordinary open of one blocks until somebody writes, and "the boot
	// hangs" is a worse answer than any of the ones this function can
	// return. It costs a regular file nothing.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("reading the guest's contract declaration %s: %w", path, err)
	}
	defer f.Close()

	// Stat the open file rather than the path, and insist on a regular
	// one: a fifo left at that path would otherwise block the read -- and
	// so the boot -- for as long as nobody wrote to it, and a directory
	// there is an assembly mistake worth naming.
	info, err := f.Stat()
	if err != nil {
		return c, fmt.Errorf("reading the guest's contract declaration %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return c, fmt.Errorf("the guest's contract declaration %s is not a regular file (mode %s)", path, info.Mode())
	}
	if info.Size() > maxContractBytes {
		return c, fmt.Errorf("the guest's contract declaration %s is %d bytes, over the %d-byte limit: a declaration is one decimal version number", path, info.Size(), maxContractBytes)
	}

	text, err := io.ReadAll(io.LimitReader(f, maxContractBytes+1))
	if err != nil {
		return c, fmt.Errorf("reading the guest's contract declaration %s: %w", path, err)
	}
	if len(text) > maxContractBytes {
		return c, fmt.Errorf("the guest's contract declaration %s grew past the %d-byte limit while being read", path, maxContractBytes)
	}

	c.Declared = strings.TrimSpace(string(text))
	state, version := ReadContract(c.Declared)
	c.State = state
	if state == ContractTooOld || state == ContractSupported || state == ContractAhead {
		c.Version = version
	}
	return c, nil
}

// Describe is what a boot says about the guest it just read, whether it
// is going on to refuse it or not.
func (c BootContract) Describe() string {
	switch c.State {
	case ContractUndeclared:
		return fmt.Sprintf("the guest beside %s declares no contract version, so it is taken to be version %d", c.Path, ContractUndeclaredVersion)
	case ContractUnreadable:
		return fmt.Sprintf("the guest's contract declaration %s says %q, which is not a version number", c.Path, c.Declared)
	default:
		return fmt.Sprintf("the guest declares contract version %d (%s)", c.Version, c.Path)
	}
}

// Check decides whether a guest declaring c can be booted by this build
// of the VMM half, and returns an error naming both versions and what
// would satisfy the newer of them when it cannot.
//
// min is the floor the caller requires, raised to MinimumBootContract if
// it is below it: a caller may ask for more than this build's floor and
// never for less. A min above Contract refuses every guest there is,
// which is why pkg/config rejects such a value up front, where the
// operator can still read why.
//
// Three refusals, and all three are the same failure: a guest whose
// control link, agents or boot parameters are not the ones the VMM half
// is providing.
func (c BootContract) Check(min int) error {
	if min < MinimumBootContract {
		min = MinimumBootContract
	}

	if c.State == ContractUnreadable {
		return fmt.Errorf("%s: a guest that stamped something unreadable is a guest whose assembly went wrong, which is not the same as one assembled before there was anything to stamp -- so it is refused rather than taken for an old guest. Repack or rebase it with a granule implementing guest contract version %d, which writes the declaration itself. See docs/guest-images.md, %q",
			c.Describe(), Contract, "The guest contract")
	}

	if c.Version > Contract {
		return fmt.Errorf("%s, but this granule implements guest contract version %d: the guest half was built against a newer granule than the VMM half booting it, and expects guest-side machinery -- control link, agents, boot parameters -- this one does not provide, which is a VM that boots and cannot be reached rather than one that fails to. Boot it under a granule implementing version %d or later, or repack the guest against this one so it declares version %d. See docs/guest-images.md, %q",
			c.Describe(), Contract, c.Version, Contract, "The guest contract")
	}

	if c.Version < min {
		remedy := fmt.Sprintf("Repack or rebase the guest with a granule implementing version %d, which lays down a guest half declaring it, or boot it under a granule that still accepts version %d.", Contract, c.Version)
		if c.State == ContractUndeclared {
			return fmt.Errorf("%s, below the minimum version %d this granule boots (it implements version %d): the guest half was assembled before the contract was declared, so its overlay and agents predate a change the VMM half no longer tolerates. %s See docs/guest-images.md, %q",
				c.Describe(), min, Contract, remedy, "The guest contract")
		}
		return fmt.Errorf("%s, below the minimum version %d this granule boots (it implements version %d): the guest half's overlay and agents predate a change the VMM half no longer tolerates, so the control link it brings is not the one this granule speaks. %s See docs/guest-images.md, %q",
			c.Describe(), min, Contract, remedy, "The guest contract")
	}

	return nil
}
