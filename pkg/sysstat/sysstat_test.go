package sysstat_test

// Real /proc/loadavg, /proc/meminfo and statfs on Linux, and the real
// sysctl/vm_stat/iostat readings on darwin -- not fakes, on either. All of
// them are kernel interfaces every machine that runs these tests already
// has, the same "nothing here is a fake standing in for the real thing"
// discipline model/simulate_test.go's own doc comment holds this codebase
// to elsewhere.
//
// Every test below used to skip unless GOOS was linux, which was true of
// the package and is no longer: a darwin Read answers the same Snapshot
// from different interfaces, and a darwin DiskUsage is the same statfs
// call. Running them on both is most of the point of the darwin work --
// the readings there go through two subprocesses and two hand-decoded
// sysctl structs, which is exactly the sort of code that is correct until
// a macOS release reorders a column and nothing notices for a year. What
// the assertions can check is that each figure is present and inside its
// own bounds; what the numbers actually *are* is only verifiable against
// the machine running them, which is why there is no golden value here.
//
// They still skip everywhere else, because everywhere else the package
// honestly has no reading to give.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bwsalmon/grain/pkg/sysstat"
)

// hasReadings skips a test on a platform this package has no
// implementation for, naming it rather than saying "not Linux": the list
// is Linux and darwin, and the next platform added should have to come
// here and say so rather than inherit a skip.
func hasReadings(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("sysstat has no implementation for %s", runtime.GOOS)
	}
}

func TestReadReturnsARealSnapshot(t *testing.T) {
	hasReadings(t)

	snap, err := sysstat.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.LoadAverage1 < 0 {
		t.Errorf("LoadAverage1 = %v, want >= 0", snap.LoadAverage1)
	}
	// Every machine has at least one processor, and the count is what
	// makes the load averages above readable at all.
	if snap.CPUs <= 0 {
		t.Errorf("CPUs = %d, want > 0", snap.CPUs)
	}
	if snap.CPUUtilization < 0 || snap.CPUUtilization > 1 {
		t.Errorf("CPUUtilization = %v, want between 0 and 1", snap.CPUUtilization)
	}
	if snap.MemTotalMB <= 0 {
		t.Errorf("MemTotalMB = %d, want > 0", snap.MemTotalMB)
	}
	if snap.MemUsedMB < 0 || snap.MemUsedMB > snap.MemTotalMB {
		t.Errorf("MemUsedMB = %d, want between 0 and MemTotalMB (%d)", snap.MemUsedMB, snap.MemTotalMB)
	}
	if snap.SwapUsedMB < 0 || snap.SwapUsedMB > snap.SwapTotalMB {
		t.Errorf("SwapUsedMB = %d, want between 0 and SwapTotalMB (%d)", snap.SwapUsedMB, snap.SwapTotalMB)
	}
}

// Nothing here is a machine with no memory in use, and nothing here is a
// machine with all of it gone either -- a reading at either end is what
// both platforms' "available" arithmetic looks like when it has gone
// wrong, and on darwin that arithmetic is a sum of page queues this
// package chose (readAvailableMemory) rather than a figure the kernel
// published. Dropping the inactive queue from that sum, which is the
// mistake a sysctl-only implementation would have made, reports a laptop
// with gigabytes free as a few MB short of full: this is the assertion
// that would have caught it.
func TestReadReportsMemoryInUseWithoutClaimingTheMachineIsFull(t *testing.T) {
	hasReadings(t)

	snap, err := sysstat.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.MemUsedMB == 0 {
		t.Error("MemUsedMB = 0; no running machine has none of its memory in use")
	}
	if snap.MemUsedMB == snap.MemTotalMB {
		t.Errorf("MemUsedMB = MemTotalMB = %d; a machine with nothing available would not be running this test",
			snap.MemTotalMB)
	}
}

// DiskUsage against a real directory, for the same reason Read is
// exercised against the real kernel: statfs is the interface, and a fake
// of it would only test the arithmetic.
func TestDiskUsageReportsARealFilesystem(t *testing.T) {
	hasReadings(t)

	totalMB, usedMB, err := sysstat.DiskUsage(t.TempDir())
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if totalMB <= 0 {
		t.Errorf("totalMB = %d, want > 0", totalMB)
	}
	if usedMB < 0 || usedMB > totalMB {
		t.Errorf("usedMB = %d, want between 0 and totalMB (%d)", usedMB, totalMB)
	}
}

// A path that does not exist is an error rather than a silent 0/0: the
// caller (cmd/grain's hostStats) is the one that decides an unavailable
// reading is not worth failing the whole host-pressure call over, and it
// can only decide that if it is told.
func TestDiskUsageReportsAMissingPathAsAnError(t *testing.T) {
	hasReadings(t)

	if _, _, err := sysstat.DiskUsage(filepath.Join(t.TempDir(), "no-such-dir")); err == nil {
		t.Error("DiskUsage of a missing path returned no error")
	}
}

// The property cmd/grain's own hostStats relies on: two paths that are
// the same filesystem answer the same, and the answer survives the paths
// looking nothing alike. A subdirectory is the only two-paths-one-disk
// case a test can create without root (a bind mount needs CAP_SYS_ADMIN),
// but it is the same st_dev comparison the bind-mounted sandbox volume
// this exists for is decided by.
func TestFilesystemIDIsTheSameForOneFilesystem(t *testing.T) {
	hasReadings(t)

	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	root, err := sysstat.FilesystemID(dir)
	if err != nil {
		t.Fatalf("FilesystemID(%s): %v", dir, err)
	}
	nested, err := sysstat.FilesystemID(sub)
	if err != nil {
		t.Fatalf("FilesystemID(%s): %v", sub, err)
	}
	if root != nested {
		t.Errorf("FilesystemID = %d for %s and %d for %s, want one filesystem to answer once", root, dir, nested, sub)
	}
}

// Missing paths are errors here for the same reason they are for
// DiskUsage above: the caller decides what an unavailable reading costs,
// and folding two disk figures together on a 0 it was never given would
// hide one of them.
func TestFilesystemIDReportsAMissingPathAsAnError(t *testing.T) {
	hasReadings(t)

	if _, err := sysstat.FilesystemID(filepath.Join(t.TempDir(), "no-such-dir")); err == nil {
		t.Error("FilesystemID of a missing path returned no error")
	}
}
