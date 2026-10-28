//go:build darwin

package sysstat

// darwin's half of Read, field for field the same Snapshot as the /proc
// half beside it (sysstat_procfs.go) and from three interfaces instead of
// one file per question:
//
//   - sysctl, for every figure the kernel exports as a plain value:
//     vm.loadavg, hw.logicalcpu, hw.memsize, vm.swapusage. Pure Go
//     through golang.org/x/sys/unix, no subprocess and no cgo -- release
//     binaries are built CGO_ENABLED=0, so a reading that needed cgo
//     would not be in the binary that ships, which is worse than not
//     having written it.
//
//   - /usr/bin/vm_stat, for the page counts MemUsedMB is a subtraction
//     over. There is no sysctl for the ones that matter: vm.page_free_count
//     and vm.page_speculative_count exist, but the *inactive* queue does
//     not, and on a mac that queue is most of what is reclaimable at any
//     moment -- gigabytes of it. A sysctl-only estimate would report a
//     16GB laptop with 4GB genuinely available as 97% full. That is not a
//     rougher answer than vm_stat's, it is a wrong one, and it would be
//     wrong in the direction that makes an operator go looking for a
//     memory leak. The queue lengths themselves only exist in
//     host_statistics64, a Mach MIG call reachable from cgo and not from
//     here, which is why the subprocess is the honest option rather than
//     the lazy one.
//
//   - /usr/sbin/iostat, for CPUUtilization. The same wall: the cumulative
//     per-state tick counters /proc/stat hands over for free are
//     host_cpu_load_info, Mach again, so there is nothing in reach here
//     to take a delta of. iostat asks the kernel for exactly that delta
//     over an interval it states, and costs no measurable CPU of its own
//     while it waits -- which `top -l 2` does not, and would bias the
//     very number being read upward by the act of reading it.
//
// Both subprocesses are named by absolute path rather than looked up on
// PATH. They ship with every macOS and have never moved, and a daemon
// started by launchd gets whatever PATH launchd felt like giving it
// (commonly one without /usr/sbin): a host-stats pane that works from a
// maintainer's shell and not from the installed daemon is an expensive
// thing to debug for a dependency this easy to pin.
//
// A failure in either subprocess fails the whole of Read rather than
// zeroing one field, which is Read's own documented rule -- and it is
// worth most here, because a zero on this pane does not read as "not
// measured". It reads as an idle machine with memory to spare, which is
// the single most dangerous thing the sandbox-health pane could say
// wrongly.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// bytesPerMiB is the unit every memory figure here is finally reported
// in, and the reason each reader below stays in bytes until its last
// line: the kernel answers in bytes and in pages, and rounding to MiB
// before the subtraction would leave MemTotalMB minus MemUsedMB
// disagreeing with the available figure it was derived from by a MiB
// either way.
//
// Spelled out rather than borrowed from DiskUsage's own local const of
// the same value: disk_unix.go builds on darwin too, and one package-level
// name shadowed by a function-local one would be a footgun for whichever
// of the two is edited next.
const bytesPerMiB = 1024 * 1024

func read() (Snapshot, error) {
	s, err := readLoadAverage()
	if err != nil {
		return Snapshot{}, err
	}
	s.CPUs, err = readCPUs()
	if err != nil {
		return Snapshot{}, err
	}
	s.MemTotalMB, s.MemUsedMB, s.SwapTotalMB, s.SwapUsedMB, err = readMemory()
	if err != nil {
		return Snapshot{}, err
	}
	// Last, like the /proc path's own sleep, because this is the only
	// reading that costs wall time: everything above is a sysctl or a
	// subprocess that returns at once, and taking them first means they
	// describe the machine as it was when Read was called rather than a
	// second later.
	s.CPUUtilization, err = readCPUUtilization()
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// loadavg is <sys/sysctl.h>'s struct loadavg, which is what vm.loadavg
// answers in: three fixed-point averages and the scale to divide them by.
//
// fixpt_t is 32-bit and fscale is a long, so the C compiler pads between
// them -- hence the blank field, which encoding/binary reads and
// discards, and which is the whole reason this is a declared type rather
// than three offsets into the raw bytes. Getting that padding wrong reads
// fscale out of the hole next to it and divides every load average by
// something like 2^43.
//
// Decoded little-endian without a runtime check, because both
// architectures Go builds darwin binaries for (amd64, arm64) are
// little-endian and there has never been a big-endian one to guard
// against.
type loadavg struct {
	Load   [3]uint32
	_      uint32
	FScale uint64
}

// readLoadAverage is vm.loadavg, the same three numbers /proc/loadavg's
// first three fields are and the same ones `uptime` prints here.
//
// Not by shelling out to `sysctl -n vm.loadavg`, which would have been
// two fewer lines: that spelling hands back the kernel's own rounding to
// two decimals ("{ 1.59 1.48 2.57 }") and a brace-wrapped format nothing
// documents, where the raw struct is the figure the kernel actually
// holds. The subprocesses this file does run are ones with no alternative
// (see the file comment); this is not one of them.
func readLoadAverage() (Snapshot, error) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return Snapshot{}, fmt.Errorf("sysstat: sysctl vm.loadavg: %w", err)
	}
	var la loadavg
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &la); err != nil {
		return Snapshot{}, fmt.Errorf("sysstat: sysctl vm.loadavg returned %d bytes, want %d: %w",
			len(raw), binary.Size(&la), err)
	}
	if la.FScale == 0 {
		// Dividing by this is the entire conversion, so a zero here is
		// not a machine with no load -- it is this struct having been
		// misread, and the only reading that would be caught by nothing
		// downstream (a NaN load average plots as a gap, not as an error).
		return Snapshot{}, fmt.Errorf("sysstat: sysctl vm.loadavg reported fscale 0, so its averages cannot be scaled")
	}
	scale := float64(la.FScale)
	return Snapshot{
		LoadAverage1:  float64(la.Load[0]) / scale,
		LoadAverage5:  float64(la.Load[1]) / scale,
		LoadAverage15: float64(la.Load[2]) / scale,
	}, nil
}

// readCPUs is hw.logicalcpu: this machine's own logical processor count.
//
// hw.logicalcpu rather than hw.ncpu, which answers the same number today
// and is documented as the legacy spelling of it, and emphatically not
// runtime.NumCPU -- see Snapshot.CPUs for why the machine's count is the
// one wanted and this process's is not. Logical, not physical, because
// that is what /proc/stat's per-CPU lines count on the Linux side: a
// hyperthread is a line there and a logicalcpu here, so the denominator
// the load averages are read against means the same thing on both
// platforms and a rollup taken on one can be compared with the other.
func readCPUs() (int, error) {
	cpus, err := unix.SysctlUint32("hw.logicalcpu")
	if err != nil {
		return 0, fmt.Errorf("sysstat: sysctl hw.logicalcpu: %w", err)
	}
	if cpus == 0 {
		// The /proc path refuses a zero here for the same reason: it is
		// the denominator every load average in the pane is divided by,
		// and a zero makes all three of them meaningless rather than
		// small.
		return 0, fmt.Errorf("sysstat: sysctl hw.logicalcpu reported 0 processors")
	}
	return int(cpus), nil
}

// readMemory is the darwin half of /proc/meminfo: hw.memsize for the
// total, the page queues for what is available, vm.swapusage for swap.
func readMemory() (totalMB, usedMB, swapTotalMB, swapUsedMB int, err error) {
	totalBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("sysstat: sysctl hw.memsize: %w", err)
	}
	if totalBytes == 0 {
		return 0, 0, 0, 0, fmt.Errorf("sysstat: sysctl hw.memsize reported 0 bytes of memory")
	}
	availBytes, err := readAvailableMemory()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// hw.memsize is how much RAM is installed and the page counts are
	// over what the VM system manages, which is a little less of it
	// (firmware carve-outs, and on Apple silicon whatever the GPU and the
	// secure enclave hold). The two are also read a moment apart. Neither
	// gap should ever make available exceed total, but if one did, the
	// subtraction below would go negative and MemUsedMB would come out as
	// a nonsense near-2^63 figure rather than as an obviously broken one.
	// Clamping reports a machine with nothing used, which is wrong in the
	// direction a reader can recognise.
	availBytes = min(availBytes, totalBytes)
	swapTotalMB, swapUsedMB, err = readSwap()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return int(totalBytes / bytesPerMiB), int((totalBytes - availBytes) / bytesPerMiB), swapTotalMB, swapUsedMB, nil
}

// availableMemoryQueues are the `vm_stat` page counts readAvailableMemory
// sums, and the list is the definition -- so it is stated here, once,
// rather than spread through a switch.
//
// Linux publishes MemAvailable and this package just subtracts it
// (man 5 proc: memory a new process could get without swapping). darwin
// publishes the queue lengths such an estimate would be built from and no
// estimate, so the definition has to be chosen here, and these three are
// the choice:
//
//   - free is unallocated. Immediately available, by definition.
//   - inactive is allocated but not recently touched, and is the first
//     thing the pageout daemon takes. On a mac this is the bulk of the
//     figure, and a count without it does not under-report available
//     memory so much as describe a different machine.
//   - speculative is read-ahead nothing has asked for yet, dropped
//     without consequence. It is listed separately because `vm_stat`
//     already subtracts it out of its own "Pages free" line, so these two
//     add rather than overlap.
//
// Deliberately not counted, though both are real and reclaimable:
// purgeable and reusable. Neither is a queue of its own -- vm_stat counts
// them inside the active and inactive queues rather than beside them -- so
// adding them would count the same pages twice and report more memory
// available than the machine physically has.
//
// Also not counted, because they are not available: wired pages, and
// pages occupied by the compressor. The second is the one worth being
// explicit about, since it is where darwin and Linux could most easily
// have drifted apart: a mac with 4GB of its 16 compressed reports those
// 4 as *used*, because getting them back costs a decompression -- the
// same reason Linux's MemAvailable does not count what it would have to
// swap in. That is the definition this is choosing, and the point of
// choosing it is that MemUsedMB means one thing across a history that
// spans both platforms (sysstat.History rolls them up without knowing
// which machine a sample came from). It is deliberately *not* claimed to
// equal Activity Monitor's "Memory Used" to the megabyte -- that figure
// is not documented and has not been checked against this -- only to be
// built from the same idea of what is reclaimable for free.
var availableMemoryQueues = []string{"Pages free", "Pages inactive", "Pages speculative"}

// hostTool builds the command for one of the two system binaries this
// file has to shell out to (see the file comment for why there are two).
//
// Env is set rather than left nil for two reasons, and the ratchet that
// insists on it -- exec-env-implicit -- cares about the first: a nil Env
// hands the child this daemon's whole environment, API keys and cloud
// credentials included, and neither of these tools has any business
// seeing it. The second is that both are parsed by position and by
// decimal digits, so the locale is part of this package's contract: LC_ALL=C
// pins the number formatting the parsers expect rather than inheriting
// whatever the operator's shell happened to set. Nothing else is needed --
// both are invoked by absolute path, so not even PATH.
func hostTool(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.Env = []string{"LC_ALL=C"}
	return cmd
}

// readAvailableMemory is darwin's MemAvailable, in bytes: the queues in
// availableMemoryQueues, summed and scaled by the page they are counted
// in. See that variable for why those queues and no others.
func readAvailableMemory() (uint64, error) {
	// vm.pagesize rather than hw.pagesize, which is the same number in
	// eight bytes instead of four and would need the other accessor; and
	// rather than the "(page size of N bytes)" prose in vm_stat's own
	// header, which is the authoritative scale for these counts but is a
	// sentence, and a sentence is a worse thing to depend on than a
	// sysctl the kernel has only one answer for.
	pageSize, err := unix.SysctlUint32("vm.pagesize")
	if err != nil {
		return 0, fmt.Errorf("sysstat: sysctl vm.pagesize: %w", err)
	}
	if pageSize == 0 {
		return 0, fmt.Errorf("sysstat: sysctl vm.pagesize reported a page size of 0")
	}
	out, err := hostTool("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("sysstat: running /usr/bin/vm_stat: %w", err)
	}
	var pages uint64
	seen := 0
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		label, count, ok := strings.Cut(scanner.Text(), ":")
		if !ok || !slices.Contains(availableMemoryQueues, strings.TrimSpace(label)) {
			continue
		}
		// vm_stat terminates every count with a full stop ("Pages free:
		// 4229."), which ParseUint will not take.
		count = strings.TrimSuffix(strings.TrimSpace(count), ".")
		value, convErr := strconv.ParseUint(count, 10, 64)
		if convErr != nil {
			return 0, fmt.Errorf("sysstat: parsing vm_stat line %q: %w", scanner.Text(), convErr)
		}
		pages += value
		seen++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("sysstat: reading /usr/bin/vm_stat output: %w", err)
	}
	// Every one of them, not however many turned up: a missing queue is a
	// vm_stat this code does not know, and the failure mode is a smaller
	// available figure, which is to say a pane reporting memory pressure
	// that is not there. There is nothing downstream that could tell that
	// apart from a machine genuinely running out.
	if seen != len(availableMemoryQueues) {
		return 0, fmt.Errorf("sysstat: vm_stat named %d of the %d page counts available memory is the sum of (%s): %q",
			seen, len(availableMemoryQueues), strings.Join(availableMemoryQueues, ", "), out)
	}
	return pages * uint64(pageSize), nil
}

// xswUsage is <sys/sysctl.h>'s struct xsw_usage, which vm.swapusage
// answers in. Declared rather than indexed for struct loadavg's reason:
// the field widths are the only thing keeping the three 64-bit figures
// aligned with what the kernel wrote.
type xswUsage struct {
	Total, Avail, Used uint64
	PageSize           uint32
	// xsu_encrypted. Nothing in the pane asks whether swap is encrypted,
	// but the field is part of the struct's size and has to be named for
	// the decode to accept the bytes.
	_ uint32
}

// readSwap is vm.swapusage, in MiB.
//
// Worth knowing when reading the figures it returns: darwin has no
// configured swap partition. The kernel opens swap files on demand, so
// SwapTotalMB here is how much backing store it has opened so far and
// grows under pressure, where on Linux it is a constant set at install
// time. A mac at "80% of swap used" is therefore saying much less than a
// deployment at the same fraction -- what matters here is the absolute
// SwapUsedMB, and that it is above zero at all.
func readSwap() (totalMB, usedMB int, err error) {
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return 0, 0, fmt.Errorf("sysstat: sysctl vm.swapusage: %w", err)
	}
	var xsw xswUsage
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &xsw); err != nil {
		return 0, 0, fmt.Errorf("sysstat: sysctl vm.swapusage returned %d bytes, want %d: %w",
			len(raw), binary.Size(&xsw), err)
	}
	// Total minus available, not the kernel's own xsu_used, so that
	// SwapUsedMB is the same subtraction on both platforms -- the /proc
	// path has no SwapUsed field to trust and computes SwapTotal minus
	// SwapFree. The two disagree here by well under a MiB, which is
	// nothing to the pane and everything to a reader trying to work out
	// what the number means. The clamp is for the same reason the memory
	// one above is: these are three independently maintained counters,
	// and an unsigned subtraction that underflows reports exabytes of
	// swap in use.
	return int(xsw.Total / bytesPerMiB), int((xsw.Total - min(xsw.Avail, xsw.Total)) / bytesPerMiB), nil
}

// readCPUUtilization is the fraction of aggregate CPU time spent working,
// over one second, from `iostat`.
//
// -c 2 -w 1 asks for two reports a second apart. iostat's first report is
// averaged over the machine's whole uptime, which is a different quantity
// and a misleading one (a host that was saturated at 09:00 and is idle now
// reports saturated), so the first is read only to start iostat's clock
// and the second is the answer. One second is iostat's floor -- -w takes
// whole seconds -- which makes this ten times the 100ms the /proc path
// spends on the same figure. That is the price of having no cumulative
// tick counters to subtract (see the file comment), and it is why Read's
// own doc comment tells callers to sample on a cadence rather than per
// request. -n 0 asks for no per-disk columns, which nothing here reads.
func readCPUUtilization() (float64, error) {
	out, err := hostTool("/usr/sbin/iostat", "-c", "2", "-w", "1", "-n", "0").Output()
	if err != nil {
		return 0, fmt.Errorf("sysstat: running /usr/sbin/iostat: %w", err)
	}
	return parseIostatIdle(out)
}

// parseIostatIdle pulls the idle percentage out of the last report in
// iostat's output and returns the complement of it, 0..1.
//
// The "id" column is located by name in iostat's own header rather than at
// a fixed offset, because the columns to its left are per-disk and
// conditional -- -n 0 asks for none, but an iostat that ignored the flag,
// or a future one that groups its headers differently, would otherwise
// have this reading a disk's MB/s as an idle percentage. Both are small
// plausible numbers, so that failure would not look like one: it would
// look like a quiet host.
func parseIostatIdle(out []byte) (float64, error) {
	idleColumn := -1
	var idle []float64
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		// The header is the row that names both of iostat's own cpu
		// columns; requiring "us" as well as "id" keeps a device named
		// "id" from being mistaken for it.
		if i := slices.Index(fields, "id"); i >= 0 && slices.Contains(fields, "us") {
			idleColumn = i
			continue
		}
		if idleColumn < 0 || len(fields) <= idleColumn {
			continue
		}
		if value, convErr := strconv.ParseFloat(fields[idleColumn], 64); convErr == nil {
			idle = append(idle, value)
		}
	}
	if idleColumn < 0 {
		return 0, fmt.Errorf("sysstat: iostat printed no cpu column header: %q", out)
	}
	if len(idle) < 2 {
		return 0, fmt.Errorf("sysstat: iostat printed %d cpu reports, want the second one (the first is averaged over uptime): %q",
			len(idle), out)
	}
	// iostat rounds each of us/sy/id to a whole percent and they need not
	// add back to exactly 100, so this can land a hair outside the 0..1
	// Snapshot.CPUUtilization promises and pkg/ui draws a bar from. Clamp
	// rather than publish a fraction nothing downstream guards against.
	return min(max((100-idle[len(idle)-1])/100, 0), 1), nil
}
