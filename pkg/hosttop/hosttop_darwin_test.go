//go:build darwin

package hosttop_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/hosttop"
)

// One sample, not both, and macOS top spells the line that opens one
// "Processes: 462 total, 2 running, ..." (hosttop_darwin.go's
// sampleHeader). Keeping the second one alone is what stops the pane
// showing the sample whose %CPU column is 0.0 for every single row.
func TestReadKeepsOnlyTheLastMacOSSample(t *testing.T) {
	keptOneSample(t, "Processes: ")
}

// The summary block the pane shows above the table: macOS top puts the
// load average on its own "Load Avg:" line, and it is in the sample
// lastSample keeps rather than only in the one it discards.
func TestReadKeepsTheMacOSSummaryBlock(t *testing.T) {
	requireTop(t)

	lines, err := hosttop.Read(context.Background(), 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "Load Avg:") {
			return
		}
	}
	t.Errorf("no \"Load Avg:\" line in the sample Read kept:\n%s", strings.Join(lines, "\n"))
}

// The assertion that earns its keep on macOS: `-o -cpu` really is
// descending. A literal port of procps' `+%CPU` to `+cpu` would run, exit
// 0 and return a table -- macOS top inverts the prefixes, so it would be
// sorted the wrong way and Read's trim would throw away the busy
// processes the pane exists to show. Nothing but real output catches that.
func TestReadSortsMacOSRowsByCPUDescending(t *testing.T) {
	requireTop(t)

	lines, err := hosttop.Read(context.Background(), 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// hosttop_darwin.go's stats list fixes the columns as
	// PID USER STATE MEM %CPU TIME COMMAND, so %CPU is found by name in
	// the header and read by that position in every row below it. It is
	// read positionally rather than by column offset because the widths
	// move between samples, and only the columns left of COMMAND are
	// looked at, since a process name may itself contain a space.
	header, column := -1, -1
	for i, line := range lines {
		if !strings.Contains(line, "PID") || !strings.Contains(line, "COMMAND") {
			continue
		}
		for j, field := range strings.Fields(line) {
			if field == "%CPU" {
				header, column = i, j
				break
			}
		}
		break
	}
	if column < 0 {
		t.Fatalf("no %%CPU column in the table header of:\n%s", strings.Join(lines, "\n"))
	}

	rows := 0
	previous := -1.0
	for _, line := range lines[header+1:] {
		fields := strings.Fields(line)
		if len(fields) <= column {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[column], 64)
		if err != nil {
			t.Fatalf("row %q: %%CPU field %q does not parse: %v", line, fields[column], err)
		}
		if previous >= 0 && cpu > previous {
			t.Fatalf("%%CPU rises from %v to %v, so the table is not sorted descending:\n%s", previous, cpu, strings.Join(lines, "\n"))
		}
		previous = cpu
		rows++
	}
	if rows < 2 {
		t.Fatalf("only %d process rows came back, too few to tell an order from:\n%s", rows, strings.Join(lines, "\n"))
	}
}
