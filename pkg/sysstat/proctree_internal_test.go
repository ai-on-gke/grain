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

//go:build !darwin

package sysstat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// fakeProc writes one /proc/<pid> entry: a stat line with the given
// ppid and CPU columns, and a cwd symlink when cwd is non-empty.
func fakeProc(t *testing.T, procDir string, pid, ppid int, comm, cwd string, utime, stime, cutime, cstime int64) {
	t.Helper()
	dir := filepath.Join(procDir, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fields 1..17 of proc(5): pid (comm) state ppid pgrp session tty_nr
	// tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime,
	// then a tail of later fields the parser must ignore.
	line := fmt.Sprintf("%d (%s) S %d %d %d 0 -1 4194304 10 0 0 0 %d %d %d %d 20 0 1 0 100 1000 50\n",
		pid, comm, ppid, pid, pid, utime, stime, cutime, cstime)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if cwd != "" {
		if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadProcessTreeCPUAttributesByWorkingDirectoryAndDescent(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	runs := filepath.Join(t.TempDir(), "runs")
	outside := t.TempDir()
	for _, d := range []string{filepath.Join(runs, "1-1"), filepath.Join(runs, "1-10")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	escapeLink := filepath.Join(runs, "1-1", "escape")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatal(err)
	}
	// 1: init, owns nothing. 50: the daemon, cwd elsewhere.
	fakeProc(t, proc, 1, 0, "init", "/", 900, 900, 0, 0)
	fakeProc(t, proc, 50, 1, "grain", outside, 500, 500, 0, 0)
	// A symlink inside 1-1 pointing outside must not attribute an unrelated process.
	fakeProc(t, proc, 60, 50, "escaped", escapeLink, 999, 999, 0, 0)
	// Run 1-1: the harness in its directory, a child that cd'd to /tmp,
	// and a grandchild under the checkout. 1 + 2 + 3 + 4 s in all.
	fakeProc(t, proc, 100, 50, "agent cli", runs+"/1-1", 60, 20, 10, 10)
	fakeProc(t, proc, 101, 100, "sh", "/tmp", 150, 50, 0, 0)
	fakeProc(t, proc, 102, 101, "go (test)", runs+"/1-1/repo/pkg", 200, 100, 0, 0)
	fakeProc(t, proc, 103, 100, "mcp", runs+"/1-1", 300, 100, 0, 0)
	// Run 1-10 must not be claimed by 1-1's prefix.
	fakeProc(t, proc, 200, 50, "agent", runs+"/1-10", 250, 250, 0, 0)
	// A process with no readable cwd (another user's) under the daemon:
	// it is not attributed, and neither are its children.
	fakeProc(t, proc, 300, 50, "other", "", 999, 999, 0, 0)
	fakeProc(t, proc, 301, 300, "other-child", "", 999, 999, 0, 0)
	// Junk the reader must skip.
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0 0 0"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readProcessTreeCPU(proc, map[string]string{
		"1-1":  runs + "/1-1/",
		"1-10": runs + "/1-10",
		"2-1":  runs + "/2-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := 10 * time.Second; got["1-1"] != want {
		t.Errorf("1-1 = %v, want %v (harness + /tmp child + checkout grandchild + mcp)", got["1-1"], want)
	}
	if want := 5 * time.Second; got["1-10"] != want {
		t.Errorf("1-10 = %v, want %v", got["1-10"], want)
	}
	if v, ok := got["2-1"]; ok {
		t.Errorf("2-1 has no process yet got %v, want it absent", v)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want exactly the two runs with processes", got)
	}
}

func TestParseProcStatCountsFromTheLastParenthesis(t *testing.T) {
	t.Parallel()
	line := "4242 (a) b (c)) R 7 4242 4242 0 -1 0 0 0 0 0 30 20 5 -1 20 0 1 0 1 1 1"
	st, ok := parseProcStat(line)
	if !ok {
		t.Fatal("parseProcStat rejected a comm with spaces and parentheses")
	}
	// cutime/cstime are signed in the kernel's struct; a negative one is
	// clamped rather than subtracted.
	if st.ppid != 7 || st.ticks != 55 {
		t.Fatalf("parseProcStat = %+v, want ppid 7 and 55 ticks", st)
	}
	if _, ok := parseProcStat("12 (short) S 1 2 3"); ok {
		t.Fatal("parseProcStat accepted a truncated line")
	}
	if _, ok := parseProcStat("no parens at all"); ok {
		t.Fatal("parseProcStat accepted a line without comm")
	}
}

func TestReadProcessTreeCPUFailsWithoutProc(t *testing.T) {
	t.Parallel()
	if _, err := readProcessTreeCPU(filepath.Join(t.TempDir(), "missing"), map[string]string{"x": "/x"}); err == nil {
		t.Fatal("readProcessTreeCPU over a missing /proc returned no error")
	}
}

// The real kernel: a busy shell started in a run directory is found
// through /proc and charged real CPU time, and a sibling run is not.
func TestProcessTreeCPUReadsARealBusyChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc is Linux's")
	}
	t.Parallel()
	base := t.TempDir()
	busy := filepath.Join(base, "7-1")
	idle := filepath.Join(base, "8-1")
	for _, d := range []string{busy, idle} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	cmd.Dir = busy
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	roots := map[string]string{"7-1": busy, "8-1": idle}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := ProcessTreeCPU(roots)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got["8-1"]; ok {
			t.Fatalf("idle run with no process got a reading: %v", got)
		}
		if got["7-1"] >= 200*time.Millisecond {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("busy run's CPU = %v after 10s, want >= 200ms", got["7-1"])
		}
		time.Sleep(100 * time.Millisecond)
	}
}
