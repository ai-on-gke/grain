//go:build !linux && !darwin

package hosttop

import "fmt"

// This is the third half of the split hosttop_linux.go and
// hosttop_darwin.go make, and it exists for the same reason
// pkg/sysstat/disk_other.go does: the package's reading comes from a
// specific platform's `top`, and a build for anything else should keep
// compiling and report the same "no reading to give" every other optional
// pane already has a shape for.
//
// Refusing here rather than guessing is the point. Every flag in the two
// files above was wrong on the other platform, so there is no neutral
// invocation to fall back on -- a guess would either be rejected outright
// (what macOS did with procps' -b, and the bug this split fixed) or, worse,
// accepted with a different meaning and silently sorted or truncated wrong.
// An error naming the platform tells the operator the pane is unavailable
// here; a wrong invocation tells them nothing and lies about the %CPU.

// sampleHeader is never consulted: topArgs below refuses before Read ever
// runs top, so lastSample is unreachable on this platform. It is declared
// only because Read is shared and references it.
const sampleHeader = ""

// topArgs refuses rather than returning an invocation, for the reasons in
// the file comment above.
func topArgs(lines int) ([]string, error) {
	return nil, fmt.Errorf("hosttop: a host process table is only available on Linux and macOS")
}
