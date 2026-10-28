package hosttop_test

// The real `top`, not a fake standing in for it -- the whole of this
// package is the flags it passes and what it keeps of the output, and a
// stub would assert only that the stub was written to match. That matters
// more since the split per platform, not less: the two tops disagree on
// every flag, so the only thing that can tell you the macOS invocation is
// right is the macOS binary. A machine without top skips, the same gate
// pkg/procgroup's own suite puts on bash.
//
// What is platform-agnostic lives here. What is a claim about one top's
// output format -- which line opens a sample, how the columns are spelled
// -- lives in hosttop_linux_test.go and hosttop_darwin_test.go beside the
// file that asked for it.

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/hosttop"
)

func requireTop(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("hosttop has no invocation for %s (hosttop_other.go)", runtime.GOOS)
	}
	if _, err := exec.LookPath("top"); err != nil {
		t.Skip("top is not installed (procps)")
	}
}

// The process table itself, which is the point of the pane. Both tops head
// it with PID and end it with COMMAND, so this one assertion holds either
// side of the split -- and it is the assertion that caught the original
// darwin failure, where Read returned an error instead of any table at all.
func TestReadReturnsAProcessTable(t *testing.T) {
	requireTop(t)

	lines, err := hosttop.Read(context.Background(), 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("Read returned no lines")
	}
	table := -1
	for i, line := range lines {
		if strings.Contains(line, "PID") && strings.Contains(line, "COMMAND") {
			table = i
			break
		}
	}
	if table < 0 {
		t.Fatalf("no process table header in:\n%s", strings.Join(lines, "\n"))
	}
	if table == len(lines)-1 {
		t.Error("the process table header is the last line, so no process rows came back")
	}
}

func TestReadCapsTheLinesItReturns(t *testing.T) {
	requireTop(t)

	lines, err := hosttop.Read(context.Background(), 8)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 8 {
		t.Fatalf("len(lines) = %d, want 8:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// A caller that gives up -- the browser closing the pane's own request --
// takes top down with it rather than leaving it sampling for the rest of
// its interval. On a platform hosttop has no invocation for this still
// errors, by refusing earlier (hosttop_other.go), which is the same answer
// the pane needs.
func TestReadFailsOnACancelledContext(t *testing.T) {
	if _, err := exec.LookPath("top"); err != nil && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
		t.Skip("top is not installed (procps)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := hosttop.Read(ctx, 0); err == nil {
		t.Fatal("Read on a cancelled context returned no error")
	}
}

// keptOneSample is the shared body of the per-platform "exactly the last
// sample" tests: keeping the second sample alone is what makes the %CPU
// column mean "now" rather than "since this process started" (Linux) or
// "0.0 for every row" (macOS), and the only platform-specific part of
// checking it is how that top spells the line opening a sample.
func keptOneSample(t *testing.T, header string) {
	t.Helper()
	requireTop(t)

	lines, err := hosttop.Read(context.Background(), 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("Read returned no lines")
	}
	if !strings.HasPrefix(lines[0], header) {
		t.Errorf("first line = %q, want a line opening with %q", lines[0], header)
	}
	headers := 0
	for _, line := range lines {
		if strings.HasPrefix(line, header) {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("output carries %d sample headers, want exactly 1:\n%s", headers, strings.Join(lines, "\n"))
	}
}
