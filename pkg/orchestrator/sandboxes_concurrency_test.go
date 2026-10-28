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

package orchestrator_test

// HostSandboxes and GranuleSandboxes each build one sandbox per run, and
// reconcileDispatch runs a cycle's dispatches concurrently -- one
// goroutine each (cycle.go's own doc comment) -- so both are genuinely
// called from several goroutines at once, each for a different sandbox.
// These tests drive that: many concurrent Acquires for distinct
// sandboxes, under -race, and a check that distinct VMs are actually
// built in parallel rather than serialized behind one lock.
//
// They used to also mix in repeat calls for the *same* slot, which was
// the interesting case while a sandbox was created lazily on first use
// and reused after. A sandbox belongs to one run now, so no two callers
// ever ask for the same one.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestHostSandboxesAcquireIsSafeForConcurrentCallers(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	h := orchestrator.NewHostSandboxes(base)

	const sandboxes = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, sandboxes)
	roots := make(chan string, sandboxes)

	wg.Add(sandboxes)
	for i := 0; i < sandboxes; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			sb, err := h.Acquire(context.Background(), fmt.Sprintf("t%d-1", i), orchestrator.Shape{})
			if err != nil {
				errs <- err
				return
			}
			root, err := sb.(interface{ Root() (string, error) }).Root()
			if err != nil {
				errs <- err
				return
			}
			roots <- root
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(roots)

	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for root := range roots {
		if seen[root] {
			t.Errorf("two concurrent Acquires got the same directory %q", root)
		}
		seen[root] = true
	}
	if len(seen) != sandboxes {
		t.Errorf("got %d distinct directories, want %d", len(seen), sandboxes)
	}
}

func TestGranuleSandboxesAcquireIsSafeForManyConcurrentSandboxes(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})

	const sandboxes = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, sandboxes)

	wg.Add(sandboxes)
	for i := 0; i < sandboxes; i++ {
		name := fmt.Sprintf("t%d-1", i)
		go func(name string) {
			defer wg.Done()
			<-start
			if _, err := k.Acquire(context.Background(), name, orchestrator.Shape{}); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	for i := 0; i < sandboxes; i++ {
		name := fmt.Sprintf("t%d-1", i)
		got, err := k.VMNameFor(name)
		if err != nil {
			t.Fatal(err)
		}
		if want := "g-" + name; got != want {
			t.Errorf("VMNameFor(%s) = %q, want %q", name, got, want)
		}
	}
}

// writeFakeGranuleWithDelay is writeFakeGranule plus an artificial pause
// inside "vm create" (delay), long enough that two concurrent creates
// only look non-serialized if they were genuinely run in parallel rather
// than one after another. It appends "<name> <start-ns> <end-ns>" to
// timesLog for every "vm create" call, straddling that pause, which
// TestGranuleSandboxesCreatesDistinctVMsConcurrentlyNotSerially reads
// back to check for overlap.
func writeFakeGranuleWithDelay(t *testing.T, argvLog, timesLog string, port int, delay time.Duration) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake granule script is POSIX shell only")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
if [ "$1" = "vm" ] && { [ "$2" = "create" ] || [ "$2" = "delete" ]; }; then
  action="$2"
  name="$3"
  statedir=""
  shift 3
  while [ $# -gt 0 ]; do
    if [ "$1" = "-state-dir" ]; then
      statedir="$2"
    fi
    shift
  done
  if [ "$action" = "create" ]; then
    start=$(date +%%s%%N)
    sleep %f
    end=$(date +%%s%%N)
    echo "$name $start $end" >> %q
    echo "{\"port\": %d}" > "$statedir/$name.json"
  else
    rm -f "$statedir/$name.json"
  fi
fi
`, argvLog, delay.Seconds(), timesLog, port)
	path := filepath.Join(dir, "granule")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGranuleSandboxesCreatesDistinctVMsConcurrentlyNotSerially holds
// cycle.go's reconcileDispatch doc comment to its word: two dispatches
// never touch the same sandbox, "so nothing here needs to hold a lock of
// its own" -- which is
// exactly what makes -max-workers/GRAIN_MAX_WORKERS a real
// concurrency knob rather than just a scheduling one (that same comment,
// bwsalmon/agents#435). If GranuleSandboxes held one lock across the whole
// map rather than none at all here, two dispatches acquiring distinct
// sandboxes at the same time would still create their VMs one at a time
// -- indistinguishable from -max-workers=1 for exactly the case that
// matters now, since every run builds a VM from scratch rather than
// reusing a slot's. This drives Acquire for several distinct sandboxes at
// once, each behind a fake granule that sleeps for delay inside "vm
// create", and asserts that at least one pair of those sleeps overlapped
// in wall-clock time -- something full serialization could never produce.
func TestGranuleSandboxesCreatesDistinctVMsConcurrentlyNotSerially(t *testing.T) {
	stateDir := t.TempDir()
	timesLog := filepath.Join(t.TempDir(), "times.log")
	writeFakeGranuleWithDelay(t, filepath.Join(t.TempDir(), "granule-argv.log"), timesLog, 30081, 300*time.Millisecond)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})

	const sandboxes = 5
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, sandboxes)
	wg.Add(sandboxes)
	for i := 0; i < sandboxes; i++ {
		name := fmt.Sprintf("t%d-1", i)
		go func(name string) {
			defer wg.Done()
			<-start
			if _, err := k.Acquire(context.Background(), name, orchestrator.Shape{}); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	type interval struct{ start, end int64 }
	intervals := map[string]interval{}
	data, err := os.ReadFile(timesLog)
	if err != nil {
		t.Fatalf("reading %s: %v", timesLog, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("malformed times.log line %q", line)
		}
		s, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("parsing start in %q: %v", line, err)
		}
		e, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			t.Fatalf("parsing end in %q: %v", line, err)
		}
		intervals[fields[0]] = interval{s, e}
	}
	if len(intervals) != sandboxes {
		t.Fatalf("times.log recorded %d distinct VM creates, want %d", len(intervals), sandboxes)
	}

	overlap := false
	names := make([]string, 0, len(intervals))
	for name := range intervals {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := intervals[names[i]], intervals[names[j]]
			if a.start < b.end && b.start < a.end {
				overlap = true
			}
		}
	}
	if !overlap {
		t.Error("no two of the concurrent \"vm create\" calls overlapped in time -- GranuleSandboxes is serializing distinct runs' VM creation behind one lock, which would silently undo the concurrency cycle.go's reconcileDispatch promises")
	}
}
