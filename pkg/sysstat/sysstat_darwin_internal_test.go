//go:build darwin

package sysstat

// The one thing on the darwin path that is worth testing against captured
// text rather than against the machine: iostat's column layout.
//
// Everything else here is a sysctl whose answer is only checkable against
// the kernel that gave it (sysstat_test.go exercises those for real), but
// parseIostatIdle has to find "id" among columns whose count depends on
// flags, on how many disks are attached and on whatever a future macOS
// decides to add. Its failure mode is silent -- a disk's MB/s is a small
// plausible number and would be read as an idle percentage, reporting a
// saturated host as a quiet one -- so the layouts it must survive are
// pinned here rather than left to whoever next runs the pane.

import (
	"strings"
	"testing"
)

func TestParseIostatIdleTakesTheSecondReport(t *testing.T) {
	// `iostat -c 2 -w 1 -n 0` as it is actually run: the first report is
	// averaged over uptime (98% idle on a machine that has been asleep all
	// day) and the second is the one-second sample that is the answer.
	out := strings.Join([]string{
		"      cpu    load average",
		" us sy id   1m   5m   15m",
		"  1  1 98  3.55 1.99 2.61",
		"  9  6 85  3.55 1.99 2.61",
		"",
	}, "\n")

	got, err := parseIostatIdle([]byte(out))
	if err != nil {
		t.Fatalf("parseIostatIdle: %v", err)
	}
	if want := 0.15; got != want {
		t.Errorf("parseIostatIdle = %v, want %v (100 minus the *second* report's idle, not the first's)", got, want)
	}
}

// -n 0 asks for no per-disk columns, and an iostat that ignored it must
// not shift the reading onto a disk. This is the layout iostat prints
// without the flag, and the "id" column is no longer the third field.
func TestParseIostatIdleFindsTheColumnPastTheDiskColumns(t *testing.T) {
	out := strings.Join([]string{
		"              disk0       cpu    load average",
		"    KB/t  tps  MB/s  us sy id   1m   5m   15m",
		"   25.68   47  1.19   1  1 98  1.49 1.48 2.48",
		"   11.18  151  1.65   3  3 94  1.49 1.48 2.48",
		"",
	}, "\n")

	got, err := parseIostatIdle([]byte(out))
	if err != nil {
		t.Fatalf("parseIostatIdle: %v", err)
	}
	if want := 0.06; got-want > 1e-9 || want-got > 1e-9 {
		t.Errorf("parseIostatIdle = %v, want %v; reading MB/s (1.65) as the idle percentage would give %v",
			got, want, (100-1.65)/100)
	}
}

// Fractions outside 0..1 are the one thing Snapshot.CPUUtilization
// promises not to carry (pkg/ui draws a bar from it), and iostat rounds
// each of its three columns to a whole percent without making them add
// back to 100 -- so an idle column of 101 is not a malformed report, it is
// rounding, and clamping is the answer rather than an error.
func TestParseIostatIdleClampsRoundingPastTheEnds(t *testing.T) {
	out := strings.Join([]string{
		" us sy id   1m   5m   15m",
		"  0  0 100  0.10 0.10 0.10",
		"  0  0 101  0.10 0.10 0.10",
		"",
	}, "\n")

	got, err := parseIostatIdle([]byte(out))
	if err != nil {
		t.Fatalf("parseIostatIdle: %v", err)
	}
	if got != 0 {
		t.Errorf("parseIostatIdle = %v, want 0 for an idle column rounded past 100", got)
	}
}

// One report is iostat having been run without -c 2, or having been cut
// short: the figure available then is the average over uptime, which is a
// different quantity and would be reported as though it were this
// second's. An error is the only honest answer.
func TestParseIostatIdleRefusesASingleReport(t *testing.T) {
	out := strings.Join([]string{
		" us sy id   1m   5m   15m",
		"  1  1 98  3.55 1.99 2.61",
		"",
	}, "\n")

	if _, err := parseIostatIdle([]byte(out)); err == nil {
		t.Error("parseIostatIdle accepted one report; the first is averaged over uptime and is not a sample")
	}
}

func TestParseIostatIdleRefusesOutputWithNoCPUColumns(t *testing.T) {
	// `iostat -d`: device statistics only, no cpu columns at all.
	out := strings.Join([]string{
		"              disk0",
		"    KB/t  tps  MB/s",
		"   25.68   47  1.19",
		"   11.18  151  1.65",
		"",
	}, "\n")

	if _, err := parseIostatIdle([]byte(out)); err == nil {
		t.Error("parseIostatIdle found an idle percentage in output with no cpu columns")
	}
}
