package guestimage

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The three places the current version is written down have to agree:
// the constant above, the file the overlay places in every guest, and
// the label the Dockerfile's final stage puts on the image carrying
// granule's own bundled guest. Two of them are not Go and cannot read the
// constant, so this is what keeps them in step -- bumping Contract
// without bumping them would publish an image claiming a contract its
// guest does not implement, which is worse than claiming none.
func TestContractIsDeclaredEverywhere(t *testing.T) {
	const (
		overlayFile = "../../deploy/guest-image/overlay-common/etc/granule/guest-contract"
		dockerfile  = "../../Dockerfile"
	)

	data, err := os.ReadFile(overlayFile)
	if err != nil {
		t.Fatalf("reading the overlay's own copy of %s: %v", ContractFile, err)
	}
	if got := strings.TrimSpace(string(data)); got != ContractValue() {
		t.Errorf("%s says %q, but guestimage.Contract is %s: the file the overlay places in every guest is what that guest declares from the inside", overlayFile, got, ContractValue())
	}

	source, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(regexp.QuoteMeta(ContractLabel) + `="([^"]*)"`).FindStringSubmatch(string(source))
	if m == nil {
		t.Fatalf("the Dockerfile sets no %s label, so the granule image's own bundled guest declares nothing and a rebase of it cannot be checked", ContractLabel)
	}
	if m[1] != ContractValue() {
		t.Errorf("the Dockerfile labels its guest %s=%q, want %q", ContractLabel, m[1], ContractValue())
	}
}

// The floor is a floor: it may sit under the current version, never over
// it, or this build would refuse every guest it packs itself.
func TestContractFloorIsBelowTheCurrentVersion(t *testing.T) {
	if MinimumContract < 1 {
		t.Errorf("MinimumContract is %d: a declared version is always at least 1, so a floor below that can never refuse anything", MinimumContract)
	}
	if MinimumContract > Contract {
		t.Errorf("MinimumContract (%d) is above Contract (%d): this granule would refuse guests it had packed itself", MinimumContract, Contract)
	}
	if state, version := ReadContract(ContractValue()); state != ContractSupported || version != Contract {
		t.Errorf("a guest packed by this build reads back as %v (version %d), want supported", state, version)
	}
}

func TestReadContract(t *testing.T) {
	for _, tc := range []struct {
		declared string
		want     ContractState
		version  int
	}{
		{"", ContractUndeclared, 0},
		{"   \n", ContractUndeclared, 0},
		// The file carrier ends in a newline, and the label carrier does
		// not: both have to read the same.
		{ContractValue() + "\n", ContractSupported, Contract},
		{ContractValue(), ContractSupported, Contract},
		{strconv.Itoa(Contract + 1), ContractAhead, Contract + 1},
		{"v1", ContractUnreadable, 0},
		{"1.2", ContractUnreadable, 0},
		{"0", ContractUnreadable, 0},
		{"-1", ContractUnreadable, 0},
		{"tomorrow", ContractUnreadable, 0},
	} {
		state, version := ReadContract(tc.declared)
		if state != tc.want || version != tc.version {
			t.Errorf("ReadContract(%q) = %v, %d; want %v, %d", tc.declared, state, version, tc.want, tc.version)
		}
	}
}

// Too old is the one state that exists to be refused, and today nothing
// can reach it: the floor is at the first version anything ever
// declared. This asserts the comparison rather than the current numbers,
// so that the refusal is already known to work on the day the floor
// moves.
func TestReadContractRefusesBelowTheFloor(t *testing.T) {
	if MinimumContract <= 1 {
		t.Skip("the floor is at the first version, so no declared version is below it yet")
	}
	state, version := ReadContract(strconv.Itoa(MinimumContract - 1))
	if state != ContractTooOld {
		t.Errorf("ReadContract(%d) = %v, want too old", MinimumContract-1, state)
	}
	if version != MinimumContract-1 {
		t.Errorf("ReadContract kept version %d, want %d", version, MinimumContract-1)
	}
}

func TestDescribeContract(t *testing.T) {
	for _, tc := range []struct{ declared, want string }{
		{"", "declares no guest-side contract version"},
		{"nonsense", `declares "nonsense"`},
		{ContractValue(), "which this granule supports"},
		{strconv.Itoa(Contract + 1), "newer than the " + ContractValue()},
	} {
		if got := DescribeContract(tc.declared); !strings.Contains(got, tc.want) {
			t.Errorf("DescribeContract(%q) = %q, want it to contain %q", tc.declared, got, tc.want)
		}
	}
}
