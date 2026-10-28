// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package hosttop reads a `top` snapshot of the processes running
// alongside this deployment's own daemon -- pkg/cli/daemon.go's
// ui.Config.HostTop, behind the System overlay's Top tab.
//
// It is the per-process half of what pkg/sysstat already reports in
// aggregate. Sandbox health answers "is this machine starved" with a load
// average and a memory figure; the question that always follows is "by
// what", and neither /proc/loadavg nor /proc/meminfo can say. `top` can,
// and it is already the first thing an operator with an SSH session
// reaches for -- so this shells out to the same tool rather than
// reimplementing its accounting over /proc, the same argument
// pkg/systemlog.Journalctl makes for shelling out to journalctl instead
// of vendoring a journal client. procps is in the deployment image for
// exactly this (Dockerfile's own header, tests/container's
// TestTheImageCarriesEveryBinaryTheDaemonShellsOutTo).
//
// Shelling out buys the operator's familiar tool at the price of that
// tool's command line, and `top` is two unrelated programs wearing one
// name: procps' on Linux and Apple's on macOS share neither a flag nor an
// output format. The deployment image is Linux, but the daemon is also run
// straight from a developer's macOS checkout, where the procps flags are
// not merely ignored -- macOS top rejects -b outright and exits 1, so the
// pane showed "top: exit status 1: invalid option or syntax: -b" instead
// of a process table. So the invocation and the shape of the output it
// produces live in hosttop_linux.go and hosttop_darwin.go, one topArgs and
// one sampleHeader each, and everything below is what both have in common.
package hosttop

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultLines is how many lines Read keeps when a caller passes 0 or
// less -- the summary block and a generously sized process table. Callers
// can ask for less when they need to, but the default should not hide busy
// processes on a large host.
const DefaultLines = 500

// Read runs `top` once and returns at most lines lines of its output,
// summary block first, oldest-to-newest in top's own order.
//
// Errors carry top's own stderr: "top: command not found" on an image
// without procps, or a permissions failure reading /proc, are both things
// the pane should show the operator rather than swallow into an empty
// block. Both tops write their diagnostics there and exit non-zero, so
// this one wrapping serves either.
//
// ctx bounds the whole run, interval included -- a caller that gives up
// (the browser closing the pane's request) kills top with it rather than
// leaving it sampling.
func Read(ctx context.Context, lines int) ([]string, error) {
	if lines <= 0 {
		lines = DefaultLines
	}
	args, err := topArgs(lines)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "top", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("top: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return trim(lastSample(splitLines(stdout.String())), lines), nil
}

// lastSample is the last of the samples topArgs asked for and nothing
// before it, which is the whole point of asking for more than one: only a
// sample with a predecessor to difference against has a %CPU column that
// means "now" (see each platform file's own count and its reason).
//
// Both tops open every sample with one recognisable line -- sampleHeader,
// spelled per platform -- so the last of those starts the sample worth
// keeping. Output with no such line at all (a procps build that words its
// header differently, a macOS release that renames the first summary
// field) is returned whole rather than emptied, since some output is a
// better answer for the pane reading it than none.
func lastSample(all []string) []string {
	for i := len(all) - 1; i >= 0; i-- {
		if strings.HasPrefix(all[i], sampleHeader) {
			return all[i:]
		}
	}
	return all
}

// trim caps the sample at n lines. It cuts from the end, where top's
// descending %CPU sort has already put the processes doing nothing.
func trim(sample []string, n int) []string {
	if len(sample) <= n {
		return sample
	}
	return sample[:n]
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
