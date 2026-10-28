package sysstat_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/sysstat"
)

// Against the real /sys/module, which is why this asserts a set rather
// than a value: a CI runner may have kvm_intel loaded, kvm_amd loaded or
// neither, and all three are correct answers. What it does pin down is
// that every path returns one of the three documented statuses and never
// an empty string -- the pane renders on the status alone, and an
// unlisted one would show as a blank chip.
func TestNestedVirtualizationAlwaysReportsAKnownStatus(t *testing.T) {
	status, detail := sysstat.NestedVirtualization()

	switch status {
	case sysstat.NestedVirtEnabled, sysstat.NestedVirtDisabled:
		if detail == "" {
			t.Errorf("status = %q with no detail, want the reading it came from", status)
		}
	case sysstat.NestedVirtUnavailable:
	default:
		t.Errorf("status = %q, want one of enabled/disabled/unavailable", status)
	}
}
