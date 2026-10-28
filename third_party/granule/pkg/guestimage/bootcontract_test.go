package guestimage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// guestWith writes a disk and, when text is non-empty, the sidecar beside
// it, and returns the disk's path.
func guestWith(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(disk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text != "" {
		if err := os.WriteFile(filepath.Join(dir, ContractSidecarName), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return disk
}

// The boot floor is below every version anything declares, so that today
// the only guest a boot refuses on age is one from the future. See
// MinimumBootContract on why it is a second floor rather than a reuse of
// MinimumContract.
func TestBootFloorRefusesNothingYet(t *testing.T) {
	if MinimumBootContract > ContractUndeclaredVersion {
		t.Errorf("MinimumBootContract is %d: raising it stops guests in the field booting, and its history has to say what break justified that", MinimumBootContract)
	}
	if MinimumBootContract > MinimumContract {
		t.Errorf("MinimumBootContract (%d) is above MinimumContract (%d): a boot would refuse a guest a cheap rebase had just left alone as fine", MinimumBootContract, MinimumContract)
	}
}

// The sidecar is resolved beside whichever disk is being booted, because
// CHV_DISK_IMAGE moves the disk -- and a declaration that stayed at the
// conventional path would then be the bundled guest's, describing
// somebody else's filesystem.
func TestContractSidecarPath(t *testing.T) {
	if got := ContractSidecarPath(""); got != ContractSidecar {
		t.Errorf("ContractSidecarPath(%q) = %q, want the conventional %q", "", got, ContractSidecar)
	}
	if got := ContractSidecarPath(DiskImage); got != ContractSidecar {
		t.Errorf("ContractSidecarPath(%q) = %q, want %q", DiskImage, got, ContractSidecar)
	}
	if got, want := ContractSidecarPath("/srv/guests/theirs/disk.img"), "/srv/guests/theirs/"+ContractSidecarName; got != want {
		t.Errorf("ContractSidecarPath() = %q, want %q -- the declaration follows the disk", got, want)
	}
}

// No sidecar is version 0 and boots. Every guest assembled before the
// declaration existed was assembled against a VMM half that agreed with
// it by construction, so absence is not evidence of skew; see
// ContractUndeclaredVersion.
func TestReadBootContractAbsentIsVersionZero(t *testing.T) {
	c, err := ReadBootContract(guestWith(t, ""))
	if err != nil {
		t.Fatalf("ReadBootContract() error = %v, want an absent declaration to be no error at all", err)
	}
	if c.State != ContractUndeclared || c.Version != ContractUndeclaredVersion {
		t.Errorf("ReadBootContract() = %+v, want undeclared at version %d", c, ContractUndeclaredVersion)
	}
	if err := c.Check(MinimumBootContract); err != nil {
		t.Errorf("Check() = %v, want an undeclared guest to boot", err)
	}
	if !strings.Contains(c.Describe(), c.Path) {
		t.Errorf("Describe() = %q, want it to name %s so an operator can go and look", c.Describe(), c.Path)
	}
}

// What a pack writes reads back as this build's own version.
func TestReadBootContractPresent(t *testing.T) {
	c, err := ReadBootContract(guestWith(t, ContractValue()+"\n"))
	if err != nil {
		t.Fatalf("ReadBootContract() error = %v", err)
	}
	if c.State != ContractSupported || c.Version != Contract {
		t.Errorf("ReadBootContract() = %+v, want a supported version %d", c, Contract)
	}
	if err := c.Check(MinimumBootContract); err != nil {
		t.Errorf("Check() = %v, want a guest this build packed to boot under it", err)
	}
}

// A sidecar that is there and cannot be read is an error rather than an
// absence: a file at that path this process cannot open is evidence that
// something is wrong with the image, not that the image is old. The size
// cap and the regular-file rule are the same argument -- a fifo left
// there would otherwise block the boot for as long as nobody wrote to
// it.
func TestReadBootContractRefusesWhatItCannotRead(t *testing.T) {
	t.Run("a directory", func(t *testing.T) {
		disk := guestWith(t, "")
		if err := os.Mkdir(ContractSidecarPath(disk), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadBootContract(disk); err == nil {
			t.Error("ReadBootContract() = nil error for a directory where the declaration belongs")
		}
	})

	t.Run("a fifo", func(t *testing.T) {
		disk := guestWith(t, "")
		if err := syscall.Mkfifo(ContractSidecarPath(disk), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		if _, err := ReadBootContract(disk); err == nil {
			t.Error("ReadBootContract() = nil error for a fifo where the declaration belongs")
		}
	})

	t.Run("more bytes than a version", func(t *testing.T) {
		disk := guestWith(t, strings.Repeat("1", maxContractBytes+1))
		if _, err := ReadBootContract(disk); err == nil {
			t.Error("ReadBootContract() = nil error for a declaration past the size cap")
		}
	})
}

// A guest from the future is the case this check exists for: the VMM half
// is the deployment's own and the guest is not, so an upgrade of the
// first does not move the second -- and a guest expecting machinery this
// granule does not provide is a VM that boots and never answers.
func TestCheckRefusesAGuestFromTheFuture(t *testing.T) {
	future := Contract + 1
	c, err := ReadBootContract(guestWith(t, strconv.Itoa(future)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Check(MinimumBootContract)
	if err == nil {
		t.Fatalf("Check() = nil error for a guest declaring version %d", future)
	}
	for _, want := range []string{strconv.Itoa(future), strconv.Itoa(Contract)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() error = %v, want it to name version %s", err, want)
		}
	}
	if !strings.Contains(err.Error(), "docs/guest-images.md") {
		t.Errorf("Check() error = %v, want it to say where the contract is written down", err)
	}
}

// Present and unreadable is its own case, and at boot it is a refusal:
// an image that stamped something and got it wrong is a broken assembly,
// not an old guest, and taking it for an old one would boot it. (A cheap
// rebase reports this rather than refusing -- it is deciding whether to
// spend a build, not whether to hand out a sandbox.)
func TestCheckRefusesAnUnreadableDeclaration(t *testing.T) {
	c, err := ReadBootContract(guestWith(t, "version=1\n"))
	if err != nil {
		t.Fatalf("ReadBootContract() error = %v, want a readable file to come back for Check to judge", err)
	}
	if c.State != ContractUnreadable {
		t.Fatalf("ReadBootContract() = %+v, want it read as unreadable", c)
	}
	err = c.Check(MinimumBootContract)
	if err == nil {
		t.Fatal("Check() = nil error for a declaration that is not a version")
	}
	if !strings.Contains(err.Error(), `"version=1"`) {
		t.Errorf("Check() error = %v, want it to quote what the declaration actually said", err)
	}
}

// The floor refuses, and it refuses an undeclared guest in its own words
// -- which is the whole reason an operator can raise it today
// (CHV_GUEST_CONTRACT_MIN) and the whole reason MinimumBootContract can
// rise later without a second mechanism being needed.
func TestCheckRefusesBelowTheFloor(t *testing.T) {
	undeclared, err := ReadBootContract(guestWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	err = undeclared.Check(Contract)
	if err == nil {
		t.Fatalf("Check(%d) = nil error for an undeclared guest", Contract)
	}
	if !strings.Contains(err.Error(), "before the contract was declared") {
		t.Errorf("Check() error = %v, want the undeclared case in its own words rather than the wording for a stated version", err)
	}

	if Contract < 2 {
		return
	}
	old, err := ReadBootContract(guestWith(t, strconv.Itoa(Contract-1)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Check(Contract); err == nil {
		t.Errorf("Check(%d) = nil error for a guest declaring %d", Contract, Contract-1)
	}
}

// A caller may raise the floor and never lower it: a min under
// MinimumBootContract asks granule to boot a guest it has been told it
// cannot talk to, which is the warning this check exists not to be.
func TestCheckIgnoresAFloorBelowTheBuiltInOne(t *testing.T) {
	c, err := ReadBootContract(guestWith(t, ContractValue()+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check(MinimumBootContract - 100); err != nil {
		t.Errorf("Check() = %v, want a supported guest to boot whatever floor was asked for", err)
	}
}
