//go:build darwin

package hosttop

import "strconv"

// This is Apple's top, which shares only its name with the procps top in
// hosttop_linux.go. Everything in this file was checked against top(1) and
// against the binary on a real macOS host rather than inferred from the
// Linux flags, because every one of those flags is either absent here or
// means something else.

// samples is macOS top's -l, and it is 2 for the reason procps' -n 2 is:
// %CPU is a difference between consecutive samples, so the first sample has
// nothing to difference against. top(1) is explicit about it -- "Note that
// the first sample displayed will have an invalid %CPU displayed for each
// process, as it is calculated using the delta between samples" -- and the
// real output is blunter than the man page: every row of sample one reads
// 0.0, so a one-sample run would sort an entire host's process table by a
// column of zeroes. The second sample is the one Read keeps.
//
// -l is also what stands in for procps' -b. It is documented as "logging
// mode", meaning plain periodic lines on stdout with no cursor addressing
// and no tty required, which is the property -b was being asked for; macOS
// top has no -b at all and exits 1 on it.
const samples = "2"

// delay is macOS top's -s, the gap the %CPU column is measured over. It is
// 1 rather than the 0.5 hosttop_linux.go can afford because macOS top
// refuses a fractional delay -- `-s 0.5` exits 1 with "invalid argument
// for sleep interval (not an integer): 0.5" -- and 1 is its floor. Every
// Read on macOS therefore costs about a second of wall clock, twice the
// Linux cost; that is the shortest honest interval available, not a tuned
// choice, and ctx is what bounds it for a caller that will not wait.
const delay = "1"

// stats is macOS top's -stats, which picks the columns. It is spelled out
// because the default logging-mode column set is ~36 columns wide, most of
// them Mach counters (MSGSENT, SYSMACH, BOOSTS) and several of them a
// literal "N/A" unless top is also asked to traverse every process's
// memory object map with -r, which is the expensive thing -R exists to
// avoid. The keys below are the closest macOS equivalents of what procps
// prints by default -- PID USER S %CPU TIME+ COMMAND, with MEM for RES --
// and -stats emits them in the order given, so COMMAND stays last as it
// does on Linux. VIRT has no cheap equivalent: macOS top's vsize is one of
// the columns that reads N/A without -r, so it is left out rather than
// shown empty.
const stats = "pid,user,state,mem,cpu,time,command"

// sampleHeader opens each of macOS top's samples: "Processes: 462 total, 2
// running, ...". It is not procps' "top - " line, and that difference is
// the reason sampleHeader is per-platform at all -- Read's lastSample
// would otherwise find no header in macOS output and return both samples,
// handing the pane the sample whose %CPU column is all zeroes.
const sampleHeader = "Processes: "

// topArgs is the macOS invocation. Unlike procps, this one is told how
// many processes to print: -n nprocs is applied after the sort, so capping
// it at lines can never drop a row that Read's trim would have kept (trim
// cuts the sample to lines lines *including* the eleven-line summary
// block, so it keeps strictly fewer than lines rows). What it does buy is
// not printing and not accounting for every one of a few hundred
// processes, twice, to throw almost all of it away.
//
// -o -cpu sorts by CPU descending. The minus is deliberate and is not a
// typo for procps' "+%CPU": macOS top inverts the prefixes, documenting
// "+ or - can be prefixed to the key name to specify ascending or
// descending order, respectively", so a literal port of +%CPU to +cpu
// compiles, runs, exits 0, and quietly sorts the busiest processes to the
// bottom where trim throws them away. Verified both ways against the real
// binary under synthetic load.
//
// There is no -w and none is needed for the row: macOS top's -ncols
// defaults to infinite in logging mode, so rows are not cut to the
// terminal width the way procps' are. What macOS top cannot do is widen
// COMMAND itself, which it hard-codes to 16 characters with no flag to
// change it: "spotlightknowledged" prints as "spotlightknowled", and
// unlike procps' top -c there is no mode that shows argv at all. The pane
// on macOS therefore shows a process *name*, truncated, where on Linux it
// shows a full command line. That is a limit of the tool, not of this
// file; a fuller name would mean giving up on `top` and reading
// libproc/sysctl directly, which is the reimplementation the package doc
// comment argues against.
func topArgs(lines int) ([]string, error) {
	return []string{
		"-l", samples,
		"-s", delay,
		"-n", strconv.Itoa(lines),
		"-o", "-cpu",
		"-stats", stats,
	}, nil
}
