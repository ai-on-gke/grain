//go:build linux

package hosttop_test

import "testing"

// One sample, not both, and procps spells the line that opens one
// "top - 10:11:31 up 3 days, ..." (hosttop_linux.go's sampleHeader).
func TestReadKeepsOnlyTheLastProcpsSample(t *testing.T) {
	keptOneSample(t, "top - ")
}
