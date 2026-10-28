//go:build !darwin

package sysstat

import "testing"

// Guest time is already inside user (and guest_nice inside nice), so a
// host whose CPUs are busy running VMs must not count that time twice.
func TestParseCPUTimesDoesNotCountGuestTimeTwice(t *testing.T) {
	//            user nice system idle iowait irq softirq steal guest guest_nice
	line := "cpu  500  0    0      500  0      0   0       0     400   0"
	got, err := parseCPUTimes(line)
	if err != nil {
		t.Fatal(err)
	}
	if want := (cpuTimes{total: 1000, idle: 500}); got != want {
		t.Fatalf("parseCPUTimes = %+v, want %+v", got, want)
	}
}

func TestParseCPUTimesAcceptsAnOlderKernelsShorterLine(t *testing.T) {
	got, err := parseCPUTimes("cpu 10 0 10 70 10")
	if err != nil {
		t.Fatal(err)
	}
	if want := (cpuTimes{total: 100, idle: 80}); got != want {
		t.Fatalf("parseCPUTimes = %+v, want %+v", got, want)
	}
}

func TestParseCPUTimesRejectsAMalformedLine(t *testing.T) {
	for _, line := range []string{"", "cpu0 1 2 3 4 5", "cpu 1 2 3", "cpu 1 2 x 4 5"} {
		if _, err := parseCPUTimes(line); err == nil {
			t.Errorf("parseCPUTimes(%q) = nil error, want one", line)
		}
	}
}
